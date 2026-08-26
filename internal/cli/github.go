package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/inference/vertex"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/maputil"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Manage GitHub org and repo configuration",
		Long:  "Commands for configuring fullsend in a GitHub organization or repository. Requires only GitHub access — no GCP credentials needed.",
	}
	cmd.AddCommand(newGitHubSetupCmd())
	cmd.AddCommand(newGitHubEnrollCmd())
	cmd.AddCommand(newGitHubUnenrollCmd())
	cmd.AddCommand(newGitHubSetCmd())
	cmd.AddCommand(newGitHubStatusCmd())
	cmd.AddCommand(newGitHubUninstallCmd())
	cmd.AddCommand(newGitHubSyncScaffoldCmd())
	return cmd
}

// parseTarget splits a target string into owner and repo.
// Returns (owner, "", false) for org-only targets and (owner, repo, true) for owner/repo.
func parseTarget(target string) (string, string, bool) {
	if strings.Contains(target, "/") {
		parts := strings.SplitN(target, "/", 2)
		return parts[0], parts[1], true
	}
	return target, "", false
}

// githubSetupConfig holds configuration for the github setup command.
type githubSetupConfig struct {
	target               string
	mintURL              string
	agents               string
	inferenceProject     string
	inferenceRegion      string
	inferenceProvider    string
	inferenceWIFProvider string
	skipAppSetup         bool
	publicApps           bool
	appSet               string
	enrollAll            bool
	enrollNone           bool
	vendor               bool
	fullsendBinary       string
	fullsendSource       string
	dryRun               bool
	direct               bool
	runtime              string
	configPreset         string // --config: local path or HTTPS URL to a preset
	configHash           string // --config-hash: SHA-256 hex digest for preset validation
	signoff              bool   // --signoff: add Signed-off-by trailer to scaffold commits

	// changedFlags records which flags were explicitly set on the
	// command line (populated by RunE before calling the setup
	// function). Used to distinguish flag-specified values from
	// defaults when building the preset overlay.
	changedFlags map[string]bool
}

func newGitHubSetupCmd() *cobra.Command {
	var cfg githubSetupConfig

	cmd := &cobra.Command{
		Use:   "setup <org|owner/repo>",
		Short: "Configure fullsend for a GitHub org or repo",
		Long: `Sets up the fullsend agentic development pipeline using only GitHub APIs.

Per-org mode (argument is an org name, e.g. "acme"):
  Creates the .fullsend config repo, workflow files, secrets, variables,
  and repo enrollment. Uses pre-provisioned values from upstream commands
  (fullsend mint deploy, fullsend inference provision-wif).

Per-repo mode (argument is owner/repo, e.g. "acme/widget"):
  Bootstraps a single repository with the shim workflow, configuration
  directory, repo variables, and repo secrets.

This command does NOT require GCP credentials. All infrastructure
values (mint URL, WIF provider, project ID) are provided as flags.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.target = args[0]

			if err := appsetup.ValidateAppSet(cfg.appSet); err != nil {
				return fmt.Errorf("invalid --app-set: %w", err)
			}
			applyDeprecatedVendorBinaryFlag(cmd, &cfg.vendor)
			if err := validateVendorFlags(cfg.vendor, cfg.fullsendBinary, cfg.fullsendSource); err != nil {
				return err
			}

			if cfg.configHash != "" && cfg.configPreset == "" {
				return fmt.Errorf("--config-hash requires --config")
			}

			_, _, isRepoTarget := parseTarget(cfg.target)
			if !isRepoTarget {
				for _, name := range []string{"config", "config-hash", "signoff"} {
					if cmd.Flags().Changed(name) {
						return fmt.Errorf("--%s is only valid for per-repo setup (fullsend github setup <owner/repo>)", name)
					}
				}
			}
			if cfg.configPreset != "" {
				for _, name := range []string{"runtime", "agents"} {
					if cmd.Flags().Changed(name) {
						return fmt.Errorf("--%s cannot be used with --config (the preset provides its own configuration)", name)
					}
				}
			}

			// Validate only when a non-empty mint URL is provided; an
			// empty value is resolved to the code default later.
			if cfg.mintURL != "" {
				if err := validateMintURLHTTPS(cfg.mintURL); err != nil {
					return err
				}
			}

			_, _, isRepo := parseTarget(cfg.target)
			if isRepo {
				for _, name := range perOrgOnlyFlags {
					if cmd.Flags().Changed(name) {
						return fmt.Errorf("--%s is only valid for per-org setup (fullsend github setup <org>)", name)
					}
				}
				if !cmd.Flags().Changed("agents") {
					cfg.agents = strings.Join(config.PerRepoDefaultRoles(), ",")
				}
			}

			// Record which flags were explicitly set so the
			// installer can distinguish user overrides from defaults
			// when building the preset overlay (ADR 0069 Decision 1).
			cfg.changedFlags = make(map[string]bool)
			cmd.Flags().Visit(func(f *pflag.Flag) {
				cfg.changedFlags[f.Name] = true
			})

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			if isRepo {
				return runGitHubSetupPerRepo(ctx, client, printer, cfg)
			}
			return runGitHubSetupPerOrg(ctx, client, printer, cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.mintURL, "mint-url", "", "token mint URL (resolved to hosted public mint if unset)")
	cmd.Flags().StringVar(&cfg.agents, "agents", strings.Join(config.DefaultAgentRoles(), ","), "comma-separated agent roles")
	cmd.Flags().StringVar(&cfg.inferenceProvider, "inference-provider", "", "inference provider (resolved to vertex if unset)")
	cmd.Flags().StringVar(&cfg.inferenceProject, "inference-project", "", "GCP project ID for inference")
	cmd.Flags().StringVar(&cfg.inferenceRegion, "inference-region", "", "GCP region for inference (resolved to global if unset)")
	cmd.Flags().StringVar(&cfg.inferenceWIFProvider, "inference-wif-provider", "", "full WIF provider resource name")
	cmd.Flags().BoolVar(&cfg.skipAppSetup, "skip-app-setup", false, "skip GitHub App creation/setup")
	cmd.Flags().BoolVar(&cfg.publicApps, "public", false, "create public (unlisted) GitHub Apps")
	cmd.Flags().StringVar(&cfg.appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for GitHub Apps")
	cmd.Flags().BoolVar(&cfg.enrollAll, "enroll-all", false, "enroll all repositories without prompting")
	cmd.Flags().BoolVar(&cfg.enrollNone, "enroll-none", false, "skip repository enrollment without prompting")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "print actions without making changes")
	cmd.Flags().BoolVar(&cfg.direct, "direct", false, "push scaffold files directly to the default branch instead of creating a PR")
	cmd.Flags().StringVar(&cfg.runtime, "runtime", "", "agent runtime for per-repo config (claude or pi; dummy is for behaviour-test installs only). Prompted on a terminal when omitted")
	addVendorFlags(cmd, &cfg.vendor, &cfg.fullsendBinary, &cfg.fullsendSource)
	cmd.Flags().StringVar(&cfg.configPreset, "config", "", "local file path or HTTPS URL to a vendor preset (committed as .fullsend/config.base.yaml)")
	cmd.Flags().StringVar(&cfg.configHash, "config-hash", "", "SHA-256 hex digest to validate the preset content")
	cmd.Flags().BoolVar(&cfg.signoff, "signoff", false, "add Signed-off-by trailer to scaffold commits (requires GitHub user identity)")

	return cmd
}

// runGitHubSetupPerRepo sets up fullsend for a single repository.
// This is the GitHub-only equivalent of runPerRepoInstall without GCP calls.
func runGitHubSetupPerRepo(ctx context.Context, client forge.Client, printer *ui.Printer, cfg githubSetupConfig) error {
	owner, repo, _ := parseTarget(cfg.target)

	if !githubOwnerPattern.MatchString(owner) {
		return fmt.Errorf("invalid owner name %q: must contain only alphanumeric characters and hyphens", owner)
	}
	if !githubRepoPattern.MatchString(repo) {
		return fmt.Errorf("invalid repo name %q: must contain only alphanumeric characters, hyphens, dots, or underscores", repo)
	}

	// On re-run, allow skipping --inference-project and --inference-wif-provider
	// if the corresponding secrets already exist on the repo (matching per-org
	// fallback behavior). Each flag is checked independently so the user can
	// update one while keeping the other.
	reuseProject := false
	reuseWIF := false
	if cfg.inferenceProject == "" {
		exists, err := client.RepoSecretExists(ctx, owner, repo, "FULLSEND_GCP_PROJECT_ID")
		if err != nil {
			return fmt.Errorf("checking existing secret FULLSEND_GCP_PROJECT_ID: %w (pass --inference-project to skip this check)", err)
		}
		if !exists {
			return fmt.Errorf("--inference-project is required for per-repo setup (no existing secret found)")
		}
		reuseProject = true
	}
	if cfg.inferenceWIFProvider == "" {
		exists, err := client.RepoSecretExists(ctx, owner, repo, "FULLSEND_GCP_WIF_PROVIDER")
		if err != nil {
			return fmt.Errorf("checking existing secret FULLSEND_GCP_WIF_PROVIDER: %w (pass --inference-wif-provider to skip this check)", err)
		}
		if !exists {
			return fmt.Errorf("--inference-wif-provider is required for per-repo setup (no existing secret found)")
		}
		reuseWIF = true
	}

	// Validate format only when a new value is provided; reused secrets were
	// validated on first write.
	if cfg.inferenceWIFProvider != "" {
		if err := validateWIFProvider(cfg.inferenceWIFProvider); err != nil {
			return err
		}
	}

	roles, err := parseAgentRoles(cfg.agents)
	if err != nil {
		return err
	}

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Setting up per-repo fullsend for " + cfg.target)
	printer.Blank()

	if reuseProject {
		printer.StepInfo("Reusing existing FULLSEND_GCP_PROJECT_ID from " + cfg.target)
	}
	if reuseWIF {
		printer.StepInfo("Reusing existing FULLSEND_GCP_WIF_PROVIDER from " + cfg.target)
	}

	// --- Preset handling (--config / --config-hash) ---
	var presetData []byte
	if cfg.configPreset != "" {
		printer.StepStart("Fetching preset from " + cfg.configPreset)
		var fetchErr error
		presetData, fetchErr = fetchPreset(cfg.configPreset)
		if fetchErr != nil {
			printer.StepFail("Failed to fetch preset")
			return fetchErr
		}
		printer.StepDone(fmt.Sprintf("Fetched preset (%d bytes)", len(presetData)))

		if cfg.configHash != "" {
			printer.StepStart("Validating preset hash")
			if hashErr := validatePresetHash(presetData, cfg.configHash); hashErr != nil {
				printer.StepFail("Preset hash validation failed")
				return hashErr
			}
			printer.StepDone("Preset hash validated")
		} else if isRemotePreset(cfg.configPreset) {
			printer.StepWarn("Remote preset fetched without --config-hash; content integrity is not verified")
		}

		if yamlErr := validatePresetYAML(presetData); yamlErr != nil {
			printer.StepFail("Preset YAML validation failed")
			return yamlErr
		}
	}

	// --- Existing per-repo config (re-run) ---
	// A re-run must not rewrite what the repo already configured
	// (agents: entries and their settings, allowlists, hand-written comments): the
	// existing .fullsend/config.yaml is kept verbatim unless a flag that
	// targets a config key was passed, in which case only that key is
	// changed on the loaded config. Managed workflow files still refresh.
	existingCfg, err := loadExistingPerRepoConfig(ctx, client, owner, repo)
	if err != nil {
		if !cfg.dryRun {
			return err
		}
		// Dry runs may lack credentials for the repo; report and plan as
		// a first install rather than failing before printing the plan.
		printer.StepWarn("Could not read existing .fullsend/config.yaml (planning as a first install): " + err.Error())
		existingCfg = nil
	}
	configFlagsChanged := setupConfigFlagsChanged(cfg)
	keepExistingConfig := existingCfg != nil && !configFlagsChanged

	// Runtime: --runtime wins; otherwise ask once on an interactive
	// terminal (Enter keeps claude) — but only on a first install. On a
	// re-run the existing config's runtime stays unless --runtime is
	// given, so an Enter cannot flip a pi repo back to claude. Presets
	// carry their own value.
	if cfg.runtime == "" && presetData == nil && !cfg.dryRun && existingCfg == nil {
		choice, err := promptRuntime(printer, os.Stdin, stdinIsInteractive())
		if err != nil {
			return err
		}
		cfg.runtime = choice
	}
	effectiveRuntime := cfg.runtime
	if effectiveRuntime == "" && existingCfg != nil {
		effectiveRuntime = existingCfg.ConfigRuntime()
	}
	if cfg.runtime == "pi" {
		printer.StepWarn("runtime pi needs a sandbox image that carries pi (fullsend-sandbox/fullsend-code built from fullsend main after #6467); harnesses pinning an older image will fail at preflight")
	}

	// --- Build config files ---
	// cfgYAML stays nil when the existing overlay is kept verbatim, and
	// .fullsend/config.yaml is then left out of the scaffold files.
	var cfgYAML []byte
	switch {
	case keepExistingConfig:
		printer.StepInfo("Keeping existing .fullsend/config.yaml unchanged (pass --runtime, --agents, --mint-url or --inference-* to change a key)")
	case existingCfg != nil:
		// Re-run with config-targeting flags: change only those keys on
		// the loaded config so everything else the repo set survives.
		changed := applySetupFlagsToConfig(cfg, existingCfg, roles)
		if err := existingCfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
		cfgYAML, err = existingCfg.Marshal()
		if err != nil {
			return fmt.Errorf("marshaling per-repo config: %w", err)
		}
		printer.StepInfo("Updating existing .fullsend/config.yaml: " + strings.Join(changed, ", ") + " (other keys kept; comments are not preserved)")
	case presetData == nil:
		// No preset: generate a per-repo config.yaml. Only
		// explicitly-set flags are written to the overlay; unset
		// values fall through overlay → base → code defaults
		// (ADR 0069 Decision 1, same pattern as buildPresetOverlay).
		perRepoCfg := config.NewPerRepoConfig(roles, cfg.target)
		if cfg.runtime != "" {
			perRepoCfg.SetRuntime(cfg.runtime)
		}
		if cfg.changedFlags["mint-url"] {
			perRepoCfg.SetMintURL(cfg.mintURL)
		}
		if cfg.changedFlags["inference-provider"] {
			perRepoCfg.SetInferenceProvider(cfg.inferenceProvider)
		}
		if cfg.changedFlags["inference-region"] {
			perRepoCfg.SetInferenceRegion(cfg.inferenceRegion)
		}
		if cfg.changedFlags["inference-project"] {
			perRepoCfg.SetInferenceProject(cfg.inferenceProject)
		}
		if cfg.changedFlags["inference-wif-provider"] {
			perRepoCfg.SetInferenceWIFProvider(cfg.inferenceWIFProvider)
		}
		if err := perRepoCfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		cfgYAML, err = perRepoCfg.Marshal()
		if err != nil {
			return fmt.Errorf("marshaling per-repo config: %w", err)
		}
	default:
		// Preset provided: base layer carries the preset's values.
		// Flag-specified values go into the overlay so the base
		// layer remains identical to the fetched preset (ADR 0069).
		if overlayCfg := buildPresetOverlay(cfg); overlayCfg != nil {
			if err := overlayCfg.Validate(); err != nil {
				return fmt.Errorf("invalid overlay config: %w", err)
			}
			cfgYAML, err = overlayCfg.Marshal()
			if err != nil {
				return fmt.Errorf("marshaling overlay config: %w", err)
			}
		} else {
			// No flags changed: use the stub overlay with comments
			// explaining the layered relationship.
			cfgYAML = []byte(stubConfigYAML)
		}
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()
	installFiles, err := scaffold.CollectPerRepoInstallFiles(cfg.vendor, upstreamRef, upstreamTag)
	if err != nil {
		return fmt.Errorf("collecting per-repo scaffold files: %w", err)
	}

	var files []forge.TreeFile
	for _, f := range installFiles {
		files = append(files, forge.TreeFile{
			Path:    f.Path,
			Content: f.Content,
			Mode:    f.Mode,
		})
	}
	if presetData != nil {
		files = append(files, forge.TreeFile{
			Path:    ".fullsend/config.base.yaml",
			Content: presetData,
			Mode:    "100644",
		})
	}
	if cfgYAML != nil {
		files = append(files, forge.TreeFile{
			Path:    ".fullsend/config.yaml",
			Content: cfgYAML,
			Mode:    "100644",
		})
	}

	// Mint/inference values are stored in config.yaml (ADR 0069
	// Decision 1). Repo variables/secrets are ALSO written for backward
	// compatibility — existing workflow templates still reference
	// ${{ vars.FULLSEND_MINT_URL }}, ${{ vars.FULLSEND_GCP_REGION }},
	// ${{ secrets.FULLSEND_GCP_PROJECT_ID }}, and
	// ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}.
	// See #5870 / #4977 for the migration to config-only reads.
	//
	// Resolve effective values: use the flag value when explicitly
	// set, otherwise fall back to code defaults so first-time
	// installs still get working vars/secrets without polluting
	// the overlay (ADR 0069).
	effectiveMintURL := cfg.mintURL
	if effectiveMintURL == "" {
		effectiveMintURL = config.DefaultPerRepoMintURL
	}
	effectiveRegion := cfg.inferenceRegion
	if effectiveRegion == "" {
		effectiveRegion = config.DefaultPerRepoInferenceRegion
	}
	repoVars := map[string]string{
		"FULLSEND_MINT_URL":   effectiveMintURL,
		"FULLSEND_GCP_REGION": effectiveRegion,
		forge.PerRepoGuardVar: "true",
	}

	// Resolve the review app's client ID so pre-fetch-prior-review.sh
	// can validate provenance of prior review comments. Best-effort:
	// a missing client ID degrades incremental reviews but does not
	// block installation.
	if reviewClientID := resolveReviewAppClientID(ctx, client, cfg.appSet); reviewClientID != "" {
		repoVars["FULLSEND_REVIEW_CLIENT_ID"] = reviewClientID
	}

	repoSecrets := make(map[string]string)
	if !reuseProject {
		repoSecrets["FULLSEND_GCP_PROJECT_ID"] = cfg.inferenceProject
	}
	if !reuseWIF {
		repoSecrets["FULLSEND_GCP_WIF_PROVIDER"] = cfg.inferenceWIFProvider
	}

	// Resolve Signed-off-by trailer when --signoff is set.
	//
	// Identity resolution runs before the dry-run early return so that
	// --dry-run --signoff validates the token's identity up front instead
	// of silently skipping the check.
	//
	// Unlike sync-scaffold (which gracefully degrades when identity is
	// unavailable), setup uses an explicit opt-in flag and hard-fails.
	// The user explicitly requested DCO sign-off; silently omitting the
	// trailer would cause the DCO check to fail with a confusing error.
	var signOffTrailer string
	if cfg.signoff {
		id, idErr := client.GetAuthenticatedUserIdentity(ctx)
		if idErr != nil {
			return fmt.Errorf("--signoff requires a GitHub user identity (name and email) — this is not available for GitHub App tokens: %w", idErr)
		}
		if id.Name == "" || id.Email == "" {
			return fmt.Errorf("--signoff requires a GitHub user identity with both name and email set (got name=%q, email=%q)", id.Name, id.Email)
		}
		trailer, trailerErr := id.SignOffTrailer()
		if trailerErr != nil {
			return fmt.Errorf("--signoff: %w", trailerErr)
		}
		signOffTrailer = trailer
	}

	if cfg.dryRun {
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		for _, f := range files {
			printer.StepDone(fmt.Sprintf("Would commit: %s (%d bytes)", f.Path, len(f.Content)))
		}
		if signOffTrailer != "" {
			printer.StepDone(fmt.Sprintf("Would add trailer: %s", signOffTrailer))
		}
		printer.Blank()
		printer.StepInfo("Would set repository variables:")
		for _, name := range maputil.SortedKeys(repoVars) {
			printer.StepInfo(fmt.Sprintf("  %s = %s", name, repoVars[name]))
		}
		secretNames := maputil.SortedKeys(repoSecrets)
		printer.StepInfo(fmt.Sprintf("Would set %d repository secrets:", len(secretNames)))
		for _, name := range secretNames {
			printer.StepInfo(fmt.Sprintf("  %s", name))
		}
		if cfg.vendor {
			printer.Blank()
			printer.StepInfo(vendorDryRunMessage(cfg.fullsendBinary, cfg.fullsendSource, layers.VendoredBinaryPathPerRepo))
		} else {
			printer.Blank()
			printer.StepInfo(fmt.Sprintf("Would remove stale vendored assets at %s (if present)", layers.VendoredBinaryPathPerRepo))
		}
		return nil
	}

	if err := checkPerRepoScopes(ctx, client, printer); err != nil {
		return err
	}
	printer.Blank()

	if cfg.vendor {
		var vendorErr error
		files, _, vendorErr = appendVendorTreeFiles(ctx, client, printer, owner, repo, files, cfg.vendor, cfg.fullsendBinary, cfg.fullsendSource)
		if vendorErr != nil {
			return fmt.Errorf("collecting vendored assets: %w", vendorErr)
		}
	}

	if err := applyPerRepoScaffold(ctx, client, printer, owner, repo, files, repoVars, repoSecrets, scaffoldOptions{direct: cfg.direct, signOffTrailer: signOffTrailer, runtime: effectiveRuntime}); err != nil {
		return err
	}

	if !cfg.vendor {
		if err := removeStaleVendoredAssets(ctx, client, printer, owner, repo, true); err != nil {
			return err
		}
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Per-repo setup complete for %s/%s", owner, repo))
	return nil
}

// buildPresetOverlay constructs the per-repo config overlay when a
// preset base layer is provided via --config. Only flag-specified
// values are written to the overlay; omitted fields inherit from the
// base layer via the layered accessor chain (ADR 0069 Decision 1).
// Returns nil when no relevant flags were changed, signaling the
// caller to use the stub overlay YAML with human-readable comments.
// setupConfigFlags are the flags that target a key in .fullsend/config.yaml.
// Any of them being passed explicitly turns a re-run from "keep the file"
// into "change that key on the existing file".
var setupConfigFlags = []string{"runtime", "agents", "mint-url", "inference-provider", "inference-project", "inference-region", "inference-wif-provider"}

// setupConfigFlagsChanged reports whether any config-targeting flag was
// passed explicitly (cobra's Changed, recorded in changedFlags — value
// comparison cannot tell --agents' non-empty default from a request).
func setupConfigFlagsChanged(cfg githubSetupConfig) bool {
	for _, name := range setupConfigFlags {
		if cfg.changedFlags[name] {
			return true
		}
	}
	return false
}

// loadExistingPerRepoConfig reads the repo's current .fullsend/config.yaml
// (and config.base.yaml when present) so the parsed config carries the
// full parent chain: overlay → base → code defaults. This ensures
// ValidateAgentEntries sees the merged agent set — an overlay entry that
// tunes a custom agent registered only in config.base.yaml would
// otherwise fail with "is not a built-in agent".
// Returns (nil, nil) when config.yaml does not exist (first install)
// and an error when it exists but cannot be parsed — a re-run must not
// silently regenerate over a file the repo edited.
func loadExistingPerRepoConfig(ctx context.Context, client forge.Client, owner, repo string) (config.PerRepoConfigWriter, error) {
	data, err := client.GetFileContent(ctx, owner, repo, ".fullsend/config.yaml")
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading existing .fullsend/config.yaml: %w", err)
	}
	if !config.IsPerRepoYAML(data) {
		return nil, fmt.Errorf("existing .fullsend/config.yaml in %s/%s is not a per-repo config; fix or remove it before re-running setup", owner, repo)
	}

	// Fetch the base layer when present so validation sees the merged
	// agent set (an overlay entry tuning a base-registered custom agent
	// needs the base's source to pass ValidateAgentEntries).
	var baseData []byte
	baseContent, baseErr := client.GetFileContent(ctx, owner, repo, ".fullsend/config.base.yaml")
	if baseErr == nil {
		baseData = baseContent
	} else if !forge.IsNotFound(baseErr) {
		return nil, fmt.Errorf("reading existing .fullsend/config.base.yaml: %w", baseErr)
	}

	parsed, err := config.ParsePerRepoConfigWriterLayered(data, baseData)
	if err != nil {
		return nil, fmt.Errorf("existing .fullsend/config.yaml in %s/%s: %w — fix or remove it before re-running setup", owner, repo, err)
	}
	return parsed, nil
}

// applySetupFlagsToConfig sets the keys targeted by explicitly passed
// flags on an existing config and returns the names of the keys changed.
func applySetupFlagsToConfig(cfg githubSetupConfig, w config.PerRepoConfigWriter, roles []string) []string {
	var changed []string
	if cfg.changedFlags["runtime"] {
		w.SetRuntime(cfg.runtime)
		changed = append(changed, "runtime")
	}
	if cfg.changedFlags["agents"] {
		w.SetRoles(roles)
		changed = append(changed, "roles")
	}
	if cfg.changedFlags["mint-url"] {
		w.SetMintURL(cfg.mintURL)
		changed = append(changed, "mint_url")
	}
	if cfg.changedFlags["inference-provider"] {
		w.SetInferenceProvider(cfg.inferenceProvider)
		changed = append(changed, "inference.provider")
	}
	if cfg.changedFlags["inference-project"] {
		w.SetInferenceProject(cfg.inferenceProject)
		changed = append(changed, "inference.project")
	}
	if cfg.changedFlags["inference-region"] {
		w.SetInferenceRegion(cfg.inferenceRegion)
		changed = append(changed, "inference.region")
	}
	if cfg.changedFlags["inference-wif-provider"] {
		w.SetInferenceWIFProvider(cfg.inferenceWIFProvider)
		changed = append(changed, "inference.wif_provider")
	}
	return changed
}

func buildPresetOverlay(cfg githubSetupConfig) config.PerRepoConfigWriter {
	flagNames := []string{"mint-url", "inference-provider", "inference-project", "inference-region", "inference-wif-provider"}
	anyChanged := false
	for _, name := range flagNames {
		if cfg.changedFlags[name] {
			anyChanged = true
			break
		}
	}
	if !anyChanged {
		return nil
	}

	o := config.NewEmptyPerRepoOverlay()
	if cfg.changedFlags["mint-url"] {
		o.SetMintURL(cfg.mintURL)
	}
	if cfg.changedFlags["inference-provider"] {
		o.SetInferenceProvider(cfg.inferenceProvider)
	}
	if cfg.changedFlags["inference-project"] {
		o.SetInferenceProject(cfg.inferenceProject)
	}
	if cfg.changedFlags["inference-region"] {
		o.SetInferenceRegion(cfg.inferenceRegion)
	}
	if cfg.changedFlags["inference-wif-provider"] {
		o.SetInferenceWIFProvider(cfg.inferenceWIFProvider)
	}
	return o
}

// runGitHubSetupPerOrg sets up fullsend for an entire organization.
// This is the GitHub-only equivalent of admin install without GCP calls.
func runGitHubSetupPerOrg(ctx context.Context, client forge.Client, printer *ui.Printer, cfg githubSetupConfig) error {
	org := cfg.target
	if err := validateOrgName(org); err != nil {
		return err
	}

	roles, err := parseAgentRoles(cfg.agents)
	if err != nil {
		return err
	}

	if cfg.inferenceProject == "" && cfg.inferenceWIFProvider != "" {
		return fmt.Errorf("--inference-wif-provider requires --inference-project to be set")
	}
	if cfg.inferenceWIFProvider != "" {
		if err := validateWIFProvider(cfg.inferenceWIFProvider); err != nil {
			return err
		}
	}

	if cfg.enrollAll && cfg.enrollNone {
		return fmt.Errorf("--enroll-all and --enroll-none are mutually exclusive")
	}

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Setting up fullsend for " + org)
	printer.Blank()

	// Determine enrollment choice: use flag if set, otherwise prompt.
	var enrollAll bool
	if cfg.enrollAll {
		enrollAll = true
	} else if cfg.enrollNone {
		enrollAll = false
	} else {
		enrollAll, err = promptEnrollment(printer, os.Stdin)
		if err != nil {
			return err
		}
	}

	allRepos, err := client.ListOrgRepos(ctx, org, false)
	if err != nil {
		return fmt.Errorf("listing org repos: %w", err)
	}

	repoNames := repoNameList(allRepos)

	var enabledRepos []string
	if enrollAll {
		var skippedPerRepo, skippedErrors, eligibleCount int
		for _, r := range allRepos {
			if r.Name == forge.ConfigRepoName {
				continue
			}
			eligibleCount++
			guardVal, guardExists, guardErr := client.GetRepoVariable(ctx, org, r.Name, forge.PerRepoGuardVar)
			if guardErr != nil {
				printer.StepWarn(fmt.Sprintf("Could not check per-repo guard for %s: %v — skipping to be safe", r.Name, guardErr))
				skippedPerRepo++
				skippedErrors++
				continue
			}
			if guardExists && guardVal == "true" {
				printer.StepWarn(fmt.Sprintf("Skipping %s — per-repo installation active", r.Name))
				skippedPerRepo++
				continue
			}
			if guardExists {
				printer.StepInfo(fmt.Sprintf("%s has per-repo guard set to %q (not active) — enrolling with per-org", r.Name, guardVal))
			}
			enabledRepos = append(enabledRepos, r.Name)
		}
		if eligibleCount > 0 && skippedErrors == eligibleCount {
			return fmt.Errorf("all %d repos were skipped due to guard-check errors — verify your token has variables:read scope", eligibleCount)
		}
		msg := fmt.Sprintf("Enrolling %d repositories (excluding %s)", len(enabledRepos), forge.ConfigRepoName)
		if skippedPerRepo-skippedErrors > 0 {
			msg += fmt.Sprintf(", %d per-repo installed", skippedPerRepo-skippedErrors)
		}
		if skippedErrors > 0 {
			msg += fmt.Sprintf(", %d guard-check errors", skippedErrors)
		}
		printer.StepInfo(msg)
	} else {
		printer.StepInfo("No repositories will be enrolled during setup")
		printer.StepInfo("To enroll repositories later, use:")
		printer.StepInfo(fmt.Sprintf("  fullsend github enroll %s <repo-name> [repo-name...]", org))
	}
	printer.Blank()

	if enabledRepos == nil {
		enabledRepos = loadExistingEnabledRepos(ctx, client, org)
	}
	if err := validateEnabledRepos(enabledRepos, repoNames); err != nil {
		return err
	}

	// Resolve effective values: flags default to empty; code
	// defaults fill in when unset (same pattern as per-repo).
	effectiveMintURL := cfg.mintURL
	if effectiveMintURL == "" {
		effectiveMintURL = config.DefaultPerRepoMintURL
	}
	effectiveRegion := cfg.inferenceRegion
	if effectiveRegion == "" {
		effectiveRegion = config.DefaultPerRepoInferenceRegion
	}

	// Build config.
	privateRepo := false
	var inferenceProvider inference.Provider
	var inferenceProviderName string
	if cfg.inferenceProject != "" {
		vcfg := vertex.Config{
			ProjectID:   cfg.inferenceProject,
			Region:      effectiveRegion,
			WIFProvider: cfg.inferenceWIFProvider,
		}
		inferenceProvider = vertex.New(vcfg)
		inferenceProviderName = "vertex"
	} else {
		inferenceProviderName = loadExistingInferenceProvider(ctx, client, org)
	}

	// Build dummy agent credentials for the layer stack.
	var agentCreds []layers.AgentCredentials
	for _, role := range roles {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			Role: role,
		})
	}

	orgCfg := config.NewOrgConfig(repoNames, enabledRepos, roles, inferenceProviderName, org)
	{
		d := orgCfg.DispatchSettings()
		d.Mode = "oidc-mint"
		orgCfg.SetDispatch(d)
	}

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	enrolledRepoIDs := collectEnrolledRepoIDs(allRepos, enabledRepos)
	dispatcher := &skipMintDispatcher{mintURL: effectiveMintURL}

	var vendorFn layers.VendorFunc
	var vendorCollect layers.VendorCollectFunc
	if cfg.vendor {
		vendorFn, vendorCollect = vendorStackArgs(true, cfg.fullsendBinary, cfg.fullsendSource)
	}

	stack := buildLayerStack(ctx, org, client, orgCfg, printer, user, privateRepo, enabledRepos, agentCreds, enrolledRepoIDs, inferenceProvider, cfg.vendor, vendorFn, vendorCollect, "", dispatcher, cfg.direct)

	if cfg.dryRun {
		printer.Header("Dry run — analyzing what setup would do")
		printer.Blank()
		if err := runPreflight(ctx, stack, layers.OpInstall, client, printer); err != nil {
			return err
		}
		printer.Blank()
		return printAnalysis(ctx, stack, printer)
	}

	if err := checkInstallScopes(ctx, client, printer); err != nil {
		return err
	}
	printer.Blank()

	if !cfg.skipAppSetup {
		if err := ensureConfigRepoExists(ctx, client, printer, org); err != nil {
			return err
		}

		creds, credErr := runAppSetup(ctx, client, printer, org, roles, "", effectiveMintURL, cfg.publicApps, nil, cfg.appSet, nil)
		if credErr != nil {
			return credErr
		}

		// Rebuild with real credentials.
		agentCreds = creds
		orgCfg = config.NewOrgConfig(repoNames, enabledRepos, roles, inferenceProviderName, org)
		{
			d := orgCfg.DispatchSettings()
			d.Mode = "oidc-mint"
			orgCfg.SetDispatch(d)
		}

		stack = buildLayerStack(ctx, org, client, orgCfg, printer, user, privateRepo, enabledRepos, agentCreds, enrolledRepoIDs, inferenceProvider, cfg.vendor, vendorFn, vendorCollect, "", dispatcher, cfg.direct)
	}

	if err := runPreflight(ctx, stack, layers.OpInstall, client, printer); err != nil {
		return err
	}
	printer.Blank()

	printer.Header("Installing")
	printer.Blank()

	if err := stack.InstallAll(ctx); err != nil {
		return fmt.Errorf("setup failed: %w", err)
	}

	printer.Blank()
	printer.Summary("Setup complete", []string{
		fmt.Sprintf("Organization: %s", org),
		fmt.Sprintf("Roles: %s", strings.Join(roles, ", ")),
		fmt.Sprintf("Enabled repos: %d", len(enabledRepos)),
	})

	return nil
}

// --- enroll / unenroll commands ---

func newGitHubEnrollCmd() *cobra.Command {
	return newReposSubcommand(
		"enroll <org> [repo...]",
		"Enable repositories for fullsend enrollment",
		"Enables the specified repositories for fullsend enrollment by updating config.yaml in the .fullsend repository. Use --all to enable all repositories (excluding .fullsend). This is a lightweight config toggle — it does NOT set secrets or variables.",
		"enable all repositories (excluding .fullsend)",
		runEnableRepos,
		false,
	)
}

func newGitHubUnenrollCmd() *cobra.Command {
	return newReposSubcommand(
		"unenroll <org> [repo...]",
		"Disable repositories from fullsend enrollment",
		"Disables the specified repositories from fullsend enrollment by updating config.yaml in the .fullsend repository. Use --all to disable all repositories. This is a lightweight config toggle — it does NOT remove secrets or variables.",
		"disable all repositories",
		runDisableRepos,
		true,
	)
}

// --- set command ---

// configKeyStorage defines the storage type for a config key.
type configKeyStorage int

const (
	storageVariable configKeyStorage = iota
	storageSecret
)

// configKeyInfo describes how a config key is stored.
type configKeyInfo struct {
	storage configKeyStorage
}

// configKeyMapping maps config key names to their storage type.
var configKeyMapping = map[string]configKeyInfo{
	"FULLSEND_GCP_REGION":       {storage: storageVariable},
	"FULLSEND_REVIEW_CLIENT_ID": {storage: storageVariable},
	forge.PerRepoGuardVar:       {storage: storageVariable},
	"FULLSEND_GCP_PROJECT_ID":   {storage: storageSecret},
	"FULLSEND_GCP_WIF_PROVIDER": {storage: storageSecret},
}

func newGitHubSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <org|owner/repo> <key> <value>",
		Short: "Update a config value (secret or variable)",
		Long: `Sets a fullsend config value on a repo. The CLI maintains an internal
mapping of which keys are stored as secrets vs variables, so the user
doesn't need to know the storage type.

Org-scope variables (like FULLSEND_MINT_URL) are managed by
'fullsend github setup' to preserve repository access lists.

Valid keys:
  FULLSEND_GCP_REGION         repo variable   GCP region for inference
  FULLSEND_REVIEW_CLIENT_ID   repo variable   review app OAuth client ID
  FULLSEND_PER_REPO_INSTALL   repo variable   per-repo install marker
  FULLSEND_GCP_PROJECT_ID     repo secret     GCP project for inference
  FULLSEND_GCP_WIF_PROVIDER   repo secret     WIF provider resource name`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			key := args[1]
			value := args[2]

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)

			return runGitHubSet(cmd.Context(), client, printer, target, key, value)
		},
	}

	return cmd
}

// runGitHubSet applies a config key-value pair to the specified target.
func runGitHubSet(ctx context.Context, client forge.Client, printer *ui.Printer, target, key, value string) error {
	info, ok := configKeyMapping[key]
	if !ok {
		var validKeys []string
		for k := range configKeyMapping {
			validKeys = append(validKeys, k)
		}
		sort.Strings(validKeys)
		return fmt.Errorf("unknown config key %q; valid keys: %s", key, strings.Join(validKeys, ", "))
	}

	owner, repo, isRepo := parseTarget(target)

	if isRepo {
		if !githubOwnerPattern.MatchString(owner) {
			return fmt.Errorf("invalid owner name %q: must contain only alphanumeric characters and hyphens", owner)
		}
		if !githubRepoPattern.MatchString(repo) {
			return fmt.Errorf("invalid repo name %q: must contain only alphanumeric characters, hyphens, dots, or underscores", repo)
		}
	} else {
		if err := validateOrgName(owner); err != nil {
			return err
		}
	}

	switch key {
	case "FULLSEND_GCP_WIF_PROVIDER":
		if err := validateWIFProvider(value); err != nil {
			return err
		}
	}

	switch info.storage {
	case storageVariable:
		if !isRepo {
			repo = forge.ConfigRepoName
		}
		printer.StepStart(fmt.Sprintf("Setting repo variable %s on %s/%s", key, owner, repo))
		if err := client.CreateOrUpdateRepoVariable(ctx, owner, repo, key, value); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set repo variable %s", key))
			return fmt.Errorf("setting repo variable %s: %w", key, err)
		}
		printer.StepDone(fmt.Sprintf("Set repo variable %s on %s/%s", key, owner, repo))
	case storageSecret:
		// Repo-scope secret.
		if !isRepo {
			// Default to .fullsend repo for org targets.
			repo = forge.ConfigRepoName
		}
		printer.StepStart(fmt.Sprintf("Setting repo secret %s on %s/%s", key, owner, repo))
		if err := client.CreateRepoSecret(ctx, owner, repo, key, value); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set repo secret %s", key))
			return fmt.Errorf("setting repo secret %s: %w", key, err)
		}
		printer.StepDone(fmt.Sprintf("Set repo secret %s on %s/%s", key, owner, repo))
	}

	return nil
}

// --- status command ---

func newGitHubStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <org>",
		Short: "Analyze GitHub-side installation status",
		Long:  "Checks the current state of fullsend's GitHub-side installation for an organization. Reports on config repo, workflows, org variables, inference secrets, and enrollment state. Does NOT check GCP resources.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)

			return runGitHubStatus(cmd.Context(), client, printer, org)
		},
	}

	return cmd
}

// runGitHubStatus checks GitHub-side layers only.
func runGitHubStatus(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("GitHub status for " + org)
	printer.Blank()

	// Check config repo.
	_, err := client.GetRepo(ctx, org, forge.ConfigRepoName)
	if err != nil {
		if forge.IsNotFound(err) {
			printer.StepFail(forge.ConfigRepoName + " repository not found")
			printer.StepInfo("Run 'fullsend github setup " + org + "' to configure")
			return nil
		}
		return fmt.Errorf("checking config repo: %w", err)
	}
	printer.StepDone(forge.ConfigRepoName + " repository exists")

	// Check config.yaml.
	cfgData, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		printer.StepFail("config.yaml not found in " + forge.ConfigRepoName)
	} else {
		cfg, parseErr := config.ParseOrgConfig(cfgData)
		if parseErr != nil {
			printer.StepWarn("config.yaml exists but is invalid: " + parseErr.Error())
		} else {
			printer.StepDone("config.yaml exists and is valid")

			// Report enrollment state.
			enabled := cfg.EnabledRepos()
			printer.StepInfo(fmt.Sprintf("Enrolled repositories: %d", len(enabled)))
			for _, name := range enabled {
				printer.StepInfo(fmt.Sprintf("  - %s", name))
			}
		}
	}

	// Check org variables.
	mintURLExists, err := client.OrgVariableExists(ctx, org, "FULLSEND_MINT_URL")
	if err != nil {
		printer.StepWarn("Could not check FULLSEND_MINT_URL: " + err.Error())
	} else if mintURLExists {
		printer.StepDone("FULLSEND_MINT_URL org variable exists")
	} else {
		printer.StepFail("FULLSEND_MINT_URL org variable not found")
	}

	vars, err := client.ListOrgVariables(ctx, org)
	if err != nil {
		printer.StepWarn("Could not list org variables: " + err.Error())
	} else {
		for _, v := range vars {
			if role, ok := parseForeignVariableName(v.Name); ok {
				entries := mintcore.ParseForeignAllowlist(v.Value)
				printer.StepDone(fmt.Sprintf("%s (%s): %s", v.Name, role, strings.Join(entries, ", ")))
			}
		}
	}

	// Check inference secrets on .fullsend repo.
	inferenceSecrets := []string{"FULLSEND_GCP_PROJECT_ID", "FULLSEND_GCP_WIF_PROVIDER"}
	for _, name := range inferenceSecrets {
		exists, secErr := client.RepoSecretExists(ctx, org, forge.ConfigRepoName, name)
		if secErr != nil {
			printer.StepWarn(fmt.Sprintf("Could not check %s: %v", name, secErr))
		} else if exists {
			printer.StepDone(fmt.Sprintf("%s exists", name))
		} else {
			printer.StepInfo(fmt.Sprintf("%s not found (may use org-level inference)", name))
		}
	}

	// Check inference region variable.
	regionExists, err := client.RepoVariableExists(ctx, org, forge.ConfigRepoName, "FULLSEND_GCP_REGION")
	if err != nil {
		printer.StepWarn("Could not check FULLSEND_GCP_REGION: " + err.Error())
	} else if regionExists {
		printer.StepDone("FULLSEND_GCP_REGION variable exists")
	} else {
		printer.StepInfo("FULLSEND_GCP_REGION not found (using default)")
	}

	printer.Blank()
	return nil
}

// --- uninstall command ---

func newGitHubUninstallCmd() *cobra.Command {
	var yolo bool
	var appSet string

	cmd := &cobra.Command{
		Use:   "uninstall <org>",
		Short: "Remove fullsend GitHub configuration from an organization",
		Long:  "Deletes the .fullsend config repo and removes org-level variables. Guides the user to delete GitHub Apps via the browser.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}
			if err := appsetup.ValidateAppSet(appSet); err != nil {
				return fmt.Errorf("invalid --app-set: %w", err)
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)

			if !yolo {
				printer.StepWarn(fmt.Sprintf("This will permanently delete the %s repo and all stored secrets for %s.", forge.ConfigRepoName, org))
				printer.StepInfo(fmt.Sprintf("Type the organization name (%s) to confirm:", org))
				var confirmation string
				if _, err := fmt.Scanln(&confirmation); err != nil {
					return fmt.Errorf("reading confirmation: %w", err)
				}
				if confirmation != org {
					return fmt.Errorf("confirmation did not match; aborting uninstall")
				}
			}

			return runGitHubUninstall(cmd.Context(), client, printer, org, appSet)
		},
	}

	cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")
	cmd.Flags().StringVar(&appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for GitHub Apps")

	return cmd
}

// runGitHubUninstall tears down the GitHub-side installation.
func runGitHubUninstall(ctx context.Context, client forge.Client, printer *ui.Printer, org, appSet string) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("Uninstalling fullsend from " + org)
	printer.Blank()

	// Discover agent slugs from harness files, then default naming convention.
	var agentSlugs []string

	agentSlugs = discoverAgentSlugs(ctx, client, org, forge.ConfigRepoName, "main", appSet, printer)

	if len(agentSlugs) == 0 {
		for _, role := range config.DefaultAgentRoles() {
			agentSlugs = append(agentSlugs, appsetup.AppSlug(appSet, role))
		}
	}

	// Delete .fullsend repository.
	_, err := client.GetRepo(ctx, org, forge.ConfigRepoName)
	if err != nil {
		if forge.IsNotFound(err) {
			printer.StepInfo(forge.ConfigRepoName + " repository already deleted")
		} else {
			return fmt.Errorf("checking for config repo: %w", err)
		}
	} else {
		printer.StepStart("Deleting " + forge.ConfigRepoName + " repository")
		if err := client.DeleteRepo(ctx, org, forge.ConfigRepoName); err != nil {
			if forge.IsNotFound(err) {
				printer.StepInfo(forge.ConfigRepoName + " repository already deleted")
			} else {
				printer.StepFail("Failed to delete " + forge.ConfigRepoName)
				return fmt.Errorf("deleting config repo: %w", err)
			}
		} else {
			printer.StepDone("Deleted " + forge.ConfigRepoName + " repository")
		}
	}

	// Delete org-level variables.
	orgVars := []string{"FULLSEND_MINT_URL"}
	for _, name := range orgVars {
		exists, varErr := client.OrgVariableExists(ctx, org, name)
		if varErr != nil {
			printer.StepWarn(fmt.Sprintf("Could not check org variable %s: %v", name, varErr))
			continue
		}
		if !exists {
			printer.StepInfo(fmt.Sprintf("%s already deleted", name))
			continue
		}
		printer.StepStart("Deleting org variable " + name)
		if err := client.DeleteOrgVariable(ctx, org, name); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to delete org variable %s", name))
			return fmt.Errorf("deleting org variable %s: %w", name, err)
		}
		printer.StepDone("Deleted org variable " + name)
	}

	// Delete org-level secrets created by the dispatch layer.
	orgSecrets := []string{"FULLSEND_DISPATCH_TOKEN"}
	for _, name := range orgSecrets {
		exists, secErr := client.OrgSecretExists(ctx, org, name)
		if secErr != nil {
			printer.StepWarn(fmt.Sprintf("Could not check org secret %s: %v", name, secErr))
			continue
		}
		if !exists {
			continue
		}
		printer.StepStart("Deleting org secret " + name)
		if err := client.DeleteOrgSecret(ctx, org, name); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to delete org secret %s", name))
			return fmt.Errorf("deleting org secret %s: %w", name, err)
		}
		printer.StepDone("Deleted org secret " + name)
	}

	var installations []forge.Installation
	var listErr error
	if ghExt, ok := client.(forge.GitHubExtensions); ok {
		installations, listErr = ghExt.ListOrgInstallations(ctx, org)
	} else {
		listErr = forge.ErrNotSupported
	}
	var existingSlugs []string
	if forge.IsNotSupported(listErr) {
		printer.StepInfo("App uninstall is not available on this forge — skipping")
	} else if listErr == nil {
		installedSet := make(map[string]bool, len(installations))
		for _, inst := range installations {
			installedSet[inst.AppSlug] = true
		}
		for _, slug := range agentSlugs {
			if installedSet[slug] {
				existingSlugs = append(existingSlugs, slug)
			} else {
				printer.StepInfo(fmt.Sprintf("App %s not found, skipping", slug))
			}
		}
	} else {
		printer.StepWarn("Could not verify which apps exist; showing all")
		existingSlugs = agentSlugs
	}
	if len(existingSlugs) > 0 {
		printer.Blank()
		printer.Header("App cleanup")
		printer.StepInfo("The following GitHub Apps should be deleted manually:")
		for _, slug := range existingSlugs {
			deleteURL := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s/advanced", org, slug)
			printer.StepInfo(fmt.Sprintf("  %s: %s", slug, deleteURL))
		}
	}

	printer.Blank()
	printer.Summary("Uninstall complete", []string{
		fmt.Sprintf("Organization: %s", org),
		"Config repo deleted",
		"GCP resources (mint, inference) must be removed separately",
	})

	return nil
}

// --- sync-scaffold command ---

func newGitHubSyncScaffoldCmd() *cobra.Command {
	var directFlag bool

	cmd := &cobra.Command{
		Use:   "sync-scaffold <org>",
		Short: "Update workflow templates in .fullsend",
		Long:  "Re-commits scaffold files (shim and maintenance workflows) to the .fullsend repo without touching secrets, variables, or enrollment. Useful after fullsend version upgrades. Idempotent and safe to run repeatedly.\n\nBy default, changes are delivered via a pull request. Use --direct to push to the default branch instead.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)

			// Default is PR delivery; --direct overrides to direct push.
			return runGitHubSyncScaffold(cmd.Context(), client, printer, org, directFlag)
		},
	}

	cmd.Flags().BoolVar(&directFlag, "direct", false, "push scaffold files directly to the default branch instead of creating a PR")

	return cmd
}

// runGitHubSyncScaffold runs only the WorkflowsLayer.
func runGitHubSyncScaffold(ctx context.Context, client forge.Client, printer *ui.Printer, org string, direct bool) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("Syncing scaffold for " + org)
	printer.Blank()

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	vendored := false
	if _, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, scaffold.VendoredMarkerPath()); err == nil {
		vendored = true
	} else if !forge.IsNotFound(err) {
		return fmt.Errorf("checking vendored marker: %w", err)
	}

	if cfgData, cfgErr := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml"); cfgErr == nil {
		if _, parseErr := config.ParseOrgConfig(cfgData); parseErr != nil {
			return fmt.Errorf("parsing config.yaml: %w", parseErr)
		}
	} else if !forge.IsNotFound(cfgErr) {
		return fmt.Errorf("reading config.yaml: %w", cfgErr)
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()
	wfLayer := layers.NewWorkflowsLayer(org, client, printer, user, version, vendored).WithDirect(direct).WithUpstreamRef(upstreamRef, upstreamTag)
	if id, idErr := client.GetAuthenticatedUserIdentity(ctx); idErr == nil {
		wfLayer = wfLayer.WithSignOff(id.Name, id.Email)
	}

	if err := wfLayer.Install(ctx); err != nil {
		return fmt.Errorf("syncing scaffold: %w", err)
	}

	printer.Blank()
	printer.StepDone("Scaffold sync complete for " + org)
	return nil
}

// resolveReviewAppClientID attempts to look up the review agent's OAuth
// client ID via the GitHub API. Returns the client ID on success, or
// an empty string if the lookup fails (best-effort — a missing client ID
// degrades incremental reviews but does not block installation).
func resolveReviewAppClientID(ctx context.Context, client forge.Client, appSet string) string {
	ghExt, ok := client.(forge.GitHubExtensions)
	if !ok {
		return ""
	}
	slug := appsetup.AppSlug(appSet, "review")
	clientID, err := ghExt.GetAppClientID(ctx, slug)
	if err != nil {
		return ""
	}
	return clientID
}
