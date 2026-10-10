package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/maputil"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Manage GitHub repo configuration",
		Long:  "Commands for configuring fullsend in a GitHub repository. Requires only GitHub access — no GCP credentials needed.",
	}
	cmd.AddCommand(newGitHubSetupCmd())
	cmd.AddCommand(newGitHubSetCmd())
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
	// OpenAI Workload Identity identifiers (ADR 0092), written to
	// inference.openai in .fullsend/config.yaml; all three or none.
	openaiAudience           string
	openaiIdentityProviderID string
	openaiServiceAccountID   string
	appSet                   string
	vendor                   bool
	fullsendBinary           string
	fullsendSource           string
	fullsendRef              string // --fullsend-ref: per-repo upstream workflow ref override
	dryRun                   bool
	direct                   bool
	runtime                  string
	configPreset             string // --config: local path or HTTPS URL to a preset
	configHash               string // --config-hash: SHA-256 hex digest for preset validation
	signoff                  bool   // --signoff: add Signed-off-by trailer to scaffold commits

	// Inference gateway block (ADR 0137), written to inference.gateway in
	// .fullsend/config.yaml; url + audience all or none.
	gatewayURL        string
	gatewayAudience   string
	gatewayModels     []string
	gatewayModelsFile string

	// changedFlags records which flags were explicitly set on the
	// command line (populated by RunE before calling the setup
	// function). Used to distinguish flag-specified values from
	// defaults when building the preset overlay.
	changedFlags map[string]bool
}

func newGitHubSetupCmd() *cobra.Command {
	var cfg githubSetupConfig

	cmd := &cobra.Command{
		Use:   "setup <owner/repo>",
		Short: "Configure fullsend for a GitHub repo",
		Long: `Sets up the fullsend agentic development pipeline using only GitHub APIs.

The argument is owner/repo (e.g. "acme/widget"). The repository is
bootstrapped with the shim workflow, configuration directory, repo
variables, and repo secrets.

This command does NOT require GCP credentials. All infrastructure
values (mint URL, WIF provider, project ID) are provided as flags.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.target = args[0]
			if _, _, isRepo := parseTarget(cfg.target); !isRepo {
				return errOrgTargetRemoved("fullsend github setup", cfg.target)
			}

			if err := appsetup.ValidateAppSet(cfg.appSet); err != nil {
				return fmt.Errorf("invalid --app-set: %w", err)
			}
			applyDeprecatedVendorBinaryFlag(cmd, &cfg.vendor)
			if err := validateVendorFlags(cfg.vendor, cfg.fullsendBinary, cfg.fullsendSource); err != nil {
				return err
			}
			if cfg.fullsendRef != "" {
				if cfg.vendor {
					return fmt.Errorf("--fullsend-ref conflicts with --vendor; use one or the other")
				}
				if !repos.IsValidRef(cfg.fullsendRef) {
					return fmt.Errorf("--fullsend-ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", cfg.fullsendRef)
				}
			}

			if cfg.configHash != "" && cfg.configPreset == "" {
				return fmt.Errorf("--config-hash requires --config")
			}

			// Validate only when a non-empty mint URL is provided; an
			// empty value is resolved to the code default later.
			if cfg.mintURL != "" {
				if err := validateMintURLHTTPS(cfg.mintURL); err != nil {
					return err
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

			return runGitHubSetupPerRepo(ctx, client, printer, cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.mintURL, "mint-url", "", "token mint URL (resolved to hosted public mint if unset)")
	cmd.Flags().StringVar(&cfg.agents, "agents", strings.Join(config.PerRepoDefaultRoles(), ","), "comma-separated agent roles")
	cmd.Flags().StringVar(&cfg.inferenceProvider, "inference-provider", "", "inference provider (resolved to vertex if unset)")
	cmd.Flags().StringVar(&cfg.inferenceProject, "inference-project", "", "GCP project ID for inference")
	cmd.Flags().StringVar(&cfg.inferenceRegion, "inference-region", "", "GCP region for inference (resolved to global if unset)")
	cmd.Flags().StringVar(&cfg.inferenceWIFProvider, "inference-wif-provider", "", "full WIF provider resource name")
	cmd.Flags().StringVar(&cfg.openaiAudience, "openai-audience", "", "OpenAI Workload Identity audience (GPT on pi or codex; with --openai-identity-provider-id and --openai-service-account-id)")
	cmd.Flags().StringVar(&cfg.openaiIdentityProviderID, "openai-identity-provider-id", "", "OpenAI Workload Identity provider ID")
	cmd.Flags().StringVar(&cfg.openaiServiceAccountID, "openai-service-account-id", "", "OpenAI service account ID the provider maps this repository to")
	cmd.Flags().StringVar(&cfg.gatewayURL, "inference-gateway-url", "", "inference gateway URL, https (plain http only for a loopback test host); gateway/ models on pi, with --inference-gateway-audience")
	cmd.Flags().StringVar(&cfg.gatewayAudience, "inference-gateway-audience", "", "OIDC audience the runner requests for the inference gateway")
	cmd.Flags().StringArrayVar(&cfg.gatewayModels, "inference-gateway-model", nil, "inference gateway model as id=api (repeatable; api is openai-responses, anthropic-messages or openai-completions)")
	cmd.Flags().StringVar(&cfg.gatewayModelsFile, "inference-gateway-models-file", "", "local pi-inference-gateway config file listing the gateway models; validated and committed as "+gatewayModelsFileRepoPath)
	cmd.Flags().StringVar(&cfg.appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for GitHub Apps")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "print actions without making changes")
	cmd.Flags().BoolVar(&cfg.direct, "direct", false, "push scaffold files directly to the default branch instead of creating a PR")
	cmd.Flags().StringVar(&cfg.runtime, "runtime", "", "agent runtime for per-repo config (claude, pi or codex; dummy is for behaviour-test installs only). Prompted on a terminal when omitted")
	addVendorFlags(cmd, &cfg.vendor, &cfg.fullsendBinary, &cfg.fullsendSource)
	cmd.Flags().StringVar(&cfg.fullsendRef, "fullsend-ref", "", "per-repo fullsend workflow ref override (conflicts with --vendor)")
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

	// Validate CLI-supplied persistent values at their source before any
	// layer is composed. Required-value checks wait until after composition
	// so a preset or existing overlay can satisfy them.
	roles, err := parseAgentRoles(cfg.agents)
	if err != nil {
		return err
	}
	if err := validateCLISetupValues(cfg); err != nil {
		return err
	}
	gatewayModelsFile, err := loadGatewayModelsFile(cfg)
	if err != nil {
		return err
	}

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Setting up per-repo fullsend for " + cfg.target)
	printer.Blank()

	presetData, err := fetchAndValidatePreset(ctx, cfg, printer)
	if err != nil {
		return err
	}

	// --- Existing per-repo config (re-run) ---
	// A re-run must not rewrite what the repo already configured
	// (agents: entries and their settings, allowlists, hand-written comments): the
	// existing .fullsend/config.yaml is kept verbatim unless a flag that
	// targets a config key was passed, in which case only that key is
	// changed on the loaded config. Managed workflow files still refresh.
	existingCfg, existingBase, err := loadExistingPerRepoConfig(ctx, client, owner, repo)
	if err != nil {
		if !cfg.dryRun {
			return err
		}
		// Dry runs may lack credentials for the repo; report and plan as
		// a first install rather than failing before printing the plan.
		printer.StepWarn("Could not read existing .fullsend/config.yaml (planning as a first install): " + err.Error())
		existingCfg = nil
		existingBase = nil
	}
	configFlagsChanged := setupConfigFlagsChanged(cfg)
	keepExistingConfig := existingCfg != nil && !configFlagsChanged

	// Runtime: --runtime wins; otherwise ask once on an interactive
	// terminal (Enter keeps claude) — but only on a first install. On a
	// re-run the existing config's runtime stays unless --runtime is
	// given, so an Enter cannot flip a pi repo back to claude. Presets
	// carry their own value; an explicit --runtime is written to the overlay.
	if cfg.runtime == "" && presetData == nil && !cfg.dryRun && existingCfg == nil {
		choice, err := promptRuntime(printer, os.Stdin, stdinIsInteractive())
		if err != nil {
			return err
		}
		cfg.runtime = choice
	}
	if cfg.runtime == "pi" {
		printer.StepWarn("runtime pi needs a sandbox image that carries pi (fullsend-sandbox/fullsend-code built from fullsend main after #6467); harnesses pinning an older image will fail at preflight")
	}
	if cfg.runtime == "codex" {
		printer.StepWarn("runtime codex needs a sandbox image that carries codex (fullsend-sandbox/fullsend-code built with CODEX_VERSION, #6920); harnesses pinning an older image will fail at preflight")
	}

	// Compare explicit persistent flags against the value they would
	// inherit without this write: the --config preset if present,
	// otherwise the repo's existing base layer, otherwise compiled
	// defaults. Do this before applySetupFlagsToConfig so overlay
	// values already on disk cannot mask a pin against the parent.
	// The same parent is used below to compose the effective config, so
	// a base-only repo (overlay missing) resolves omitted values from its
	// base rather than compiled defaults.
	inheritedBase := presetData
	if len(inheritedBase) == 0 {
		inheritedBase = existingBase
	}
	if configFlagsChanged {
		warnPinnedSetupFlags(printer, cfg, inheritedSetupReader(inheritedBase), roles)
	}

	// --- Build config files ---
	// cfgYAML stays nil when the existing overlay is kept verbatim, and
	// .fullsend/config.yaml is then left out of the scaffold files.
	var cfgYAML []byte
	var overlay config.PerRepoConfigWriter
	switch {
	case keepExistingConfig:
		printer.StepInfo("Keeping existing .fullsend/config.yaml unchanged (pass --runtime, --agents, --mint-url or --inference-* to change a key)")
		overlay = existingCfg
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
		overlay = existingCfg
	case presetData == nil:
		// No preset: generate a per-repo config.yaml. Only
		// explicitly-set flags are written to the overlay; unset
		// values fall through overlay → base → code defaults
		// (ADR 0069 Decision 1, same pattern as buildPresetOverlay).
		perRepoCfg, err := newSetupOverlay(roles, cfg.target, inheritedBase)
		if err != nil {
			return err
		}
		if cfg.runtime != "" && !cfg.changedFlags["runtime"] {
			perRepoCfg.SetRuntime(cfg.runtime)
		}
		applySetupFlagsToConfig(cfg, perRepoCfg, roles)
		if err := perRepoCfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		cfgYAML, err = perRepoCfg.Marshal()
		if err != nil {
			return fmt.Errorf("marshaling per-repo config: %w", err)
		}
		overlay = perRepoCfg
	default:
		// Preset provided: base layer carries the preset's values.
		// Flag-specified values go into the overlay so the base
		// layer remains identical to the fetched preset (ADR 0069).
		overlay = buildPresetOverlay(cfg, roles)
		if overlay != nil {
			if err := overlay.Validate(); err != nil {
				return fmt.Errorf("invalid overlay config: %w", err)
			}
			cfgYAML, err = overlay.Marshal()
			if err != nil {
				return fmt.Errorf("marshaling overlay config: %w", err)
			}
		} else {
			// No flags changed: use the stub overlay with comments
			// explaining the layered relationship.
			cfgYAML = []byte(stubConfigYAML)
		}
	}

	effective, err := composeSetupLayers(cfgYAML, overlay, inheritedBase)
	if err != nil {
		return err
	}
	if err := validateSetupValueFormats(effective, "composed config"); err != nil {
		return err
	}
	if err := validateEffectiveGateway(cfg, effective); err != nil {
		return err
	}

	reuseProject, reuseWIF, err := resolveInferenceReuse(ctx, client, owner, repo, cfg, effective)
	if err != nil {
		return err
	}
	if reuseProject {
		printer.StepInfo("Reusing existing FULLSEND_GCP_PROJECT_ID from " + cfg.target)
	}
	if reuseWIF {
		printer.StepInfo("Reusing existing FULLSEND_GCP_WIF_PROVIDER from " + cfg.target)
	}
	effectiveRuntime := effectiveSetupRuntime(cfg, effective)

	upstreamRef, upstreamTag := resolveUpstreamRef()
	if cfg.fullsendRef != "" {
		upstreamRef = cfg.fullsendRef
		upstreamTag = "" // explicit ref overrides the version tag
	}
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
		plan := preset.Apply(presetData, nil, nil)
		files = append(files, forge.TreeFile{
			Path:    preset.BasePath,
			Content: plan.Base,
			Mode:    "100644",
		})
	}
	if cfgYAML != nil {
		files = append(files, forge.TreeFile{
			Path:    preset.OverlayPath,
			Content: cfgYAML,
			Mode:    "100644",
		})
	}
	if gatewayModelsFile != nil {
		files = append(files, forge.TreeFile{
			Path:    gatewayModelsFileRepoPath,
			Content: gatewayModelsFile,
			Mode:    "100644",
		})
	}

	// Mint/inference values are stored in layered config (ADR 0069
	// Decision 1). Repo variables/secrets are ALSO written for backward
	// compatibility — existing workflow templates still reference
	// ${{ vars.FULLSEND_MINT_URL }}, ${{ vars.FULLSEND_GCP_REGION }},
	// ${{ secrets.FULLSEND_GCP_PROJECT_ID }}, and
	// ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}.
	// See #5870 / #4977 for the migration to config-only reads.
	//
	// Resolve effective values from the composed configuration so a
	// required value supplied only by the preset or existing overlay
	// still reaches vars/secrets. CLI struct fields remain a fallback
	// for callers that set them without recording changedFlags.
	effectiveMintURL := effectiveSetupMintURL(cfg, effective)
	effectiveRegion := effectiveInferenceRegion(cfg, effective)
	repoVars := map[string]string{
		"FULLSEND_MINT_URL":   effectiveMintURL,
		"FULLSEND_GCP_REGION": effectiveRegion,
		forge.PerRepoGuardVar: "true",
	}

	// Determine the effective app set to persist as FULLSEND_APP_SET so
	// scaffold workflows can derive bot identities, before resolving the
	// review app's client ID below: both must agree on the same app set,
	// or FULLSEND_REVIEW_CLIENT_ID would end up resolved against the
	// wrong app on a re-run (e.g. the flag's default instead of a custom
	// app set already persisted on the repo). When --app-set was passed
	// explicitly, that value is authoritative; otherwise preserve any
	// value already on the repo so a re-run does not silently overwrite a
	// custom app set with the built-in default, falling back to the flag
	// default only when the variable is genuinely absent. GetRepoVariable
	// reports a missing variable as (\"\", false, nil); a non-nil error
	// instead means the read failed, so we skip the write entirely rather
	// than clobber a possibly-custom existing value with the flag default.
	explicitAppSet := ""
	if cfg.changedFlags["app-set"] {
		explicitAppSet = cfg.appSet
	}
	existingAppSet := ""
	writeAppSet := true
	if explicitAppSet == "" {
		var appSetErr error
		existingAppSet, _, appSetErr = client.GetRepoVariable(ctx, owner, repo, forge.VarAppSet)
		if appSetErr != nil {
			writeAppSet = false
		}
		// Unlike explicitAppSet (validated via appsetup.ValidateAppSet
		// before this function runs), existingAppSet comes straight from
		// the repo variable. Reject a malformed value here, before it is
		// preserved and used to build a GitHub App slug below, instead of
		// trusting it unchecked.
		if existingAppSet != "" && appsetup.ValidateAppSet(existingAppSet) != nil {
			existingAppSet = ""
		}
	}
	appSetToPersist := appsetup.ResolvePersistedAppSet(explicitAppSet, existingAppSet)

	// Resolve the review app's client ID so pre-fetch-prior-review.sh
	// can validate provenance of prior review comments, using the same
	// effective app set computed above. Best-effort: a missing client ID
	// degrades incremental reviews but does not block installation.
	if reviewClientID := resolveReviewAppClientID(ctx, client, appSetToPersist); reviewClientID != "" {
		repoVars["FULLSEND_REVIEW_CLIENT_ID"] = reviewClientID
	}

	if writeAppSet {
		repoVars[forge.VarAppSet] = appSetToPersist
	}

	repoSecrets := make(map[string]string)
	if !reuseProject && effectiveInferenceProject(cfg, effective) != "" {
		repoSecrets["FULLSEND_GCP_PROJECT_ID"] = effectiveInferenceProject(cfg, effective)
	}
	if !reuseWIF && effectiveInferenceWIF(cfg, effective) != "" {
		repoSecrets["FULLSEND_GCP_WIF_PROVIDER"] = effectiveInferenceWIF(cfg, effective)
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
		trailer, trailerErr := resolveSignOffTrailer(ctx, client, "--signoff", "GitHub")
		if trailerErr != nil {
			return trailerErr
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
		var vendorCleanup func()
		files, _, vendorCleanup, vendorErr = appendVendorTreeFiles(ctx, client, printer, owner, repo, files, cfg.vendor, cfg.fullsendBinary, cfg.fullsendSource)
		if vendorCleanup != nil {
			defer vendorCleanup()
		}
		if vendorErr != nil {
			return fmt.Errorf("collecting vendored assets: %w", vendorErr)
		}
	}

	if err := applyPerRepoScaffold(ctx, client, printer, owner, repo, files, repoVars, repoSecrets, scaffoldOptions{direct: cfg.direct, signOffTrailer: signOffTrailer, runtime: effectiveRuntime}); err != nil {
		return err
	}

	if !cfg.vendor {
		if err := removeStaleVendoredAssets(ctx, client, printer, owner, repo); err != nil {
			return err
		}
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Per-repo setup complete for %s/%s", owner, repo))
	return nil
}

// setupConfigFlags are the flags that target a key in .fullsend/config.yaml.
// Any of them being passed explicitly turns a re-run from "keep the file"
// into "change that key on the existing file".
var setupConfigFlags = []string{"runtime", "agents", "mint-url", "inference-provider", "inference-project", "inference-region", "inference-wif-provider", "openai-audience", "openai-identity-provider-id", "openai-service-account-id", "inference-gateway-url", "inference-gateway-audience", "inference-gateway-model", "inference-gateway-models-file"}

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
// Returns a nil config.yaml writer when config.yaml does not exist (first
// install, or an overlay that was removed while config.base.yaml remains)
// and an error when config.yaml exists but cannot be parsed — a re-run
// must not silently regenerate over a file the repo edited. baseData is
// the raw config.base.yaml bytes when that file exists, returned even
// when there is no overlay, so callers can compare explicit CLI flags
// against the inherited lower layer.
func loadExistingPerRepoConfig(ctx context.Context, client forge.Client, owner, repo string) (config.PerRepoConfigWriter, []byte, error) {
	data, err := client.GetFileContent(ctx, owner, repo, ".fullsend/config.yaml")
	if err != nil {
		if !forge.IsNotFound(err) {
			return nil, nil, fmt.Errorf("reading existing .fullsend/config.yaml: %w", err)
		}
		// No overlay yet, but a base layer may still exist (overlay
		// removed while base remains) — callers compare CLI flags
		// against that base, so it must be returned even though there
		// is no overlay to parse.
		baseData, baseErr := readExistingPerRepoBase(ctx, client, owner, repo)
		if baseErr != nil {
			return nil, nil, baseErr
		}
		return nil, baseData, nil
	}
	if !config.IsPerRepoYAML(data) {
		return nil, nil, fmt.Errorf("existing .fullsend/config.yaml in %s/%s is not a per-repo config; fix or remove it before re-running setup", owner, repo)
	}

	// Fetch the base layer when present so validation sees the merged
	// agent set (an overlay entry tuning a base-registered custom agent
	// needs the base's source to pass ValidateAgentEntries).
	baseData, err := readExistingPerRepoBase(ctx, client, owner, repo)
	if err != nil {
		return nil, nil, err
	}

	parsed, err := config.ParsePerRepoConfigWriterLayered(data, baseData)
	if err != nil {
		return nil, nil, fmt.Errorf("existing .fullsend/config.yaml in %s/%s: %w — fix or remove it before re-running setup", owner, repo, err)
	}
	return parsed, baseData, nil
}

// readExistingPerRepoBase fetches the repo's config.base.yaml, returning
// nil data (and no error) when the file does not exist.
func readExistingPerRepoBase(ctx context.Context, client forge.Client, owner, repo string) ([]byte, error) {
	baseContent, baseErr := client.GetFileContent(ctx, owner, repo, ".fullsend/config.base.yaml")
	if baseErr == nil {
		return baseContent, nil
	}
	if forge.IsNotFound(baseErr) {
		return nil, nil
	}
	return nil, fmt.Errorf("reading existing .fullsend/config.base.yaml: %w", baseErr)
}

// inheritedSetupReader is the lower layer an overlay write would inherit
// from: empty overlay on the given base (and compiled defaults), or
// compiled defaults alone when baseData is empty. Callers use this to
// compare CLI values against the parent, not against overlay keys that
// may already be set.
func inheritedSetupReader(baseData []byte) config.PerRepoConfigReader {
	if len(baseData) == 0 {
		return config.NewEmptyPerRepoOverlay()
	}
	inherited, err := config.ParsePerRepoConfigWriterLayered([]byte("{}"), baseData)
	if err != nil {
		return config.NewEmptyPerRepoOverlay()
	}
	return inherited
}

// warnPinnedSetupFlags emits a warning for each explicitly passed
// persistent setup flag whose normalized value equals the value the
// overlay would otherwise inherit from a base layer or compiled
// default. The write still happens; the warning exists so users know
// the overlay will not pick up later changes to that lower layer.
// Omitted flags and per-run flags are ignored.
func warnPinnedSetupFlags(printer *ui.Printer, cfg githubSetupConfig, inherited config.PerRepoConfigReader, roles []string) {
	for _, w := range pinnedSetupFlags(cfg, inherited, roles) {
		printer.StepWarn(w)
	}
}

// pinnedSetupFlags returns one warning per explicitly supplied
// persistent setup value that matches the currently inherited value.
func pinnedSetupFlags(cfg githubSetupConfig, inherited config.PerRepoConfigReader, roles []string) []string {
	if inherited == nil || !setupConfigFlagsChanged(cfg) {
		return nil
	}
	var warnings []string
	if cfg.changedFlags["runtime"] && setupScalarEqual(cfg.runtime, inherited.ConfigRuntime()) {
		warnings = append(warnings, setupPinWarning("runtime"))
	}
	// A nil roles slice (empty --agents) is never persisted: SetRoles(nil)
	// leaves the overlay key unset, so the overlay still inherits.
	if cfg.changedFlags["agents"] && roles != nil && slices.Equal(roles, inherited.ConfigRoles()) {
		warnings = append(warnings, setupPinWarning("roles"))
	}
	if cfg.changedFlags["mint-url"] && setupScalarEqual(cfg.mintURL, inherited.ConfigMintURL()) {
		warnings = append(warnings, setupPinWarning("mint_url"))
	}
	if cfg.changedFlags["inference-provider"] && setupScalarEqual(cfg.inferenceProvider, inherited.ConfigInferenceProvider()) {
		warnings = append(warnings, setupPinWarning("inference.provider"))
	}
	if cfg.changedFlags["inference-project"] && setupScalarEqual(cfg.inferenceProject, inherited.ConfigInferenceProject()) {
		warnings = append(warnings, setupPinWarning("inference.project"))
	}
	if cfg.changedFlags["inference-region"] && setupScalarEqual(cfg.inferenceRegion, inherited.ConfigInferenceRegion()) {
		warnings = append(warnings, setupPinWarning("inference.region"))
	}
	if cfg.changedFlags["inference-wif-provider"] && setupScalarEqual(cfg.inferenceWIFProvider, inherited.ConfigInferenceWIFProvider()) {
		warnings = append(warnings, setupPinWarning("inference.wif_provider"))
	}
	if openaiFlagsChanged(cfg) {
		// Each identifier resolves independently through the layers, so
		// compare each supplied one against its own inherited
		// counterpart (both trimmed, matching how the block is persisted).
		ids := cfg.openaiIDs()
		inheritedIDs := inherited.ConfigInferenceOpenAI().Trimmed()
		if ids.Audience != "" && ids.Audience == inheritedIDs.Audience {
			warnings = append(warnings, setupPinWarning("inference.openai.audience"))
		}
		if ids.IdentityProviderID != "" && ids.IdentityProviderID == inheritedIDs.IdentityProviderID {
			warnings = append(warnings, setupPinWarning("inference.openai.identity_provider_id"))
		}
		if ids.ServiceAccountID != "" && ids.ServiceAccountID == inheritedIDs.ServiceAccountID {
			warnings = append(warnings, setupPinWarning("inference.openai.service_account_id"))
		}
	}
	if gatewayFlagsChanged(cfg) {
		inheritedGW := inherited.ConfigInferenceGateway().Trimmed()
		if u := strings.TrimSpace(cfg.gatewayURL); u != "" && u == inheritedGW.URL {
			warnings = append(warnings, setupPinWarning("inference.gateway.url"))
		}
		if a := strings.TrimSpace(cfg.gatewayAudience); a != "" && a == inheritedGW.Audience {
			warnings = append(warnings, setupPinWarning("inference.gateway.audience"))
		}
	}
	return warnings
}

// setupScalarEqual compares a CLI scalar with the inherited value
// verbatim: scalar flags are persisted and resolved without
// normalization, so a padded CLI value is a real override, not a restatement.
func setupScalarEqual(cli, inherited string) bool {
	return cli != "" && cli == inherited
}

func setupPinWarning(field string) string {
	return field + " is being pinned in .fullsend/config.yaml to the currently inherited value; later changes from config.base.yaml or compiled defaults will not apply. Remove the key from the overlay to inherit again"
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
	if openaiFlagsChanged(cfg) {
		w.SetInferenceOpenAI(cfg.openaiIDs())
		changed = append(changed, "inference.openai")
	}
	if gatewayFlagsChanged(cfg) {
		// validateGatewaySetupFlags already ran, so the block parses.
		if g, err := cfg.gatewayBlock(); err == nil {
			if gatewayClearRequested(cfg, g) {
				w.SetInferenceGateway(config.InferenceGatewayConfig{})
			} else {
				// Change only the keys the flags set: a url/audience
				// re-run keeps the repository's model list, and a
				// models-only run keeps (or inherits) url and audience.
				w.MergeInferenceGateway(g)
			}
			changed = append(changed, "inference.gateway")
		}
	}
	return changed
}

// buildPresetOverlay constructs the per-repo config overlay when a
// preset base layer is provided via --config. Only flag-specified
// values are written to the overlay; omitted fields inherit from the
// base layer via the layered accessor chain (ADR 0069 Decision 1).
// Returns nil when no relevant flags were changed, signaling the
// caller to use the stub overlay YAML with human-readable comments.
func buildPresetOverlay(cfg githubSetupConfig, roles []string) config.PerRepoConfigWriter {
	if !setupConfigFlagsChanged(cfg) {
		return nil
	}
	o := config.NewEmptyPerRepoOverlay()
	applySetupFlagsToConfig(cfg, o, roles)
	return o
}

// fetchAndValidatePreset loads the --config preset, verifies hash and
// YAML, then parses and validates it as a per-repo config layer.
// Returns nil data when --config was not set.
func fetchAndValidatePreset(ctx context.Context, cfg githubSetupConfig, printer *ui.Printer) ([]byte, error) {
	if cfg.configPreset == "" {
		return nil, nil
	}
	printer.StepStart("Fetching preset from " + cfg.configPreset)
	presetData, fetchErr := preset.Fetch(ctx, cfg.configPreset)
	if fetchErr != nil {
		printer.StepFail("Failed to fetch preset")
		return nil, fetchErr
	}
	printer.StepDone(fmt.Sprintf("Fetched preset (%d bytes)", len(presetData)))

	if cfg.configHash != "" {
		printer.StepStart("Validating preset hash")
		if hashErr := preset.ValidateHash(presetData, cfg.configHash); hashErr != nil {
			printer.StepFail("Preset hash validation failed")
			return nil, hashErr
		}
		printer.StepDone("Preset hash validated")
	} else if preset.IsRemote(cfg.configPreset) {
		printer.StepWarn("Remote preset fetched without --config-hash; content integrity is not verified")
	}

	if yamlErr := preset.ValidateYAML(presetData); yamlErr != nil {
		printer.StepFail("Preset YAML validation failed")
		return nil, yamlErr
	}
	if err := validatePresetLayer(presetData); err != nil {
		printer.StepFail("Preset validation failed")
		return nil, err
	}
	return presetData, nil
}

// validatePresetLayer parses the --config preset as a per-repo config
// layer, runs its structural Validate, and checks its mint URL and
// inference WIF provider formats. data is the raw preset YAML.
func validatePresetLayer(data []byte) error {
	if !config.IsPerRepoYAML(data) {
		return fmt.Errorf("preset is not a per-repo configuration")
	}
	w, err := config.ParsePerRepoConfigWriter(data)
	if err != nil {
		return fmt.Errorf("parsing preset: %w", err)
	}
	if err := w.Validate(); err != nil {
		return fmt.Errorf("invalid preset: %w", err)
	}
	pr, ok := w.(config.PerRepoConfigReader)
	if !ok {
		return fmt.Errorf("preset is not a per-repo configuration")
	}
	return validateSetupValueFormats(pr, "preset")
}

// validateCLISetupValues checks that explicitly passed CLI setup flags
// (--runtime, --inference-provider, --inference-wif-provider, and the
// --openai-* trio) hold valid values, independent of any preset layer.
func validateCLISetupValues(cfg githubSetupConfig) error {
	if cfg.runtime != "" && !slices.Contains(config.ValidRuntimes(), cfg.runtime) {
		return fmt.Errorf("invalid --runtime %q: must be one of %s", cfg.runtime, strings.Join(config.ValidRuntimes(), ", "))
	}
	if cfg.inferenceProvider != "" && !slices.Contains(config.ValidProviders(), cfg.inferenceProvider) {
		return fmt.Errorf("invalid --inference-provider %q: must be one of %s", cfg.inferenceProvider, strings.Join(config.ValidProviders(), ", "))
	}
	if cfg.inferenceWIFProvider != "" {
		if err := validateWIFProvider(cfg.inferenceWIFProvider); err != nil {
			return err
		}
	}
	if err := validateOpenAISetupFlags(cfg); err != nil {
		return err
	}
	return validateGatewaySetupFlags(cfg)
}

// validateSetupValueFormats checks the mint URL and inference WIF
// provider values readable from r (a preset or the composed effective
// config) against their required formats. source names the layer being
// checked (e.g. "preset") for error messages; nil r is a no-op.
func validateSetupValueFormats(r config.PerRepoConfigReader, source string) error {
	if r == nil {
		return nil
	}
	if u := r.ConfigMintURL(); u != "" {
		if err := validateMintURLHTTPS(u); err != nil {
			return fmt.Errorf("%s mint_url: %w", source, err)
		}
	}
	if wif := r.ConfigInferenceWIFProvider(); wif != "" {
		if err := validateWIFProvider(wif); err != nil {
			return fmt.Errorf("%s inference.wif_provider: %w", source, err)
		}
	}
	return nil
}

// newSetupOverlay creates the first-install overlay. When baseData is
// non-empty (an on-disk config.base.yaml with no overlay) the overlay is
// parented on that base so validation and value resolution see it. With a
// base present the overlay is sparse (like the --config preset path): no
// roles, create_issues targets, or remote-resource allowlist are
// materialized, so they inherit from the base; the caller writes only
// values passed through explicit flags.
func newSetupOverlay(roles []string, target string, baseData []byte) (config.PerRepoConfigWriter, error) {
	if len(baseData) == 0 {
		return config.NewPerRepoConfig(roles, target), nil
	}
	w := config.NewEmptyPerRepoOverlay()
	data, err := w.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshaling per-repo config: %w", err)
	}
	layered, err := config.ParsePerRepoConfigWriterLayered(data, baseData)
	if err != nil {
		return nil, fmt.Errorf("composing per-repo config with existing base: %w", err)
	}
	return layered, nil
}

// composeSetupLayers returns the effective per-repo config used for
// required-value checks and install-time variable/secret generation.
// overlay is the writable top layer; baseData is the inherited lower layer (the --config preset, or the
// repo's existing config.base.yaml).
func composeSetupLayers(overlayYAML []byte, overlay config.PerRepoConfigWriter, baseData []byte) (config.PerRepoConfigReader, error) {
	if len(baseData) == 0 {
		if overlay != nil {
			return overlay, nil
		}
		return config.NewEmptyPerRepoOverlay(), nil
	}
	data := overlayYAML
	if len(data) == 0 && overlay != nil {
		var err error
		data, err = overlay.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshaling overlay for composition: %w", err)
		}
	}
	return config.ParsePerRepoConfigWriterLayered(data, baseData)
}

// resolveInferenceReuse determines, for each of the GCP project and WIF
// provider inference values, whether setup should reuse the existing
// repo secret because neither the CLI flag nor the composed effective
// config supplied a value. When neither value is configured, GCP
// credentials are optional and existing secrets are left untouched.
func resolveInferenceReuse(ctx context.Context, client forge.Client, owner, repo string, cfg githubSetupConfig, effective config.PerRepoConfigReader) (reuseProject, reuseWIF bool, err error) {
	if effectiveInferenceProject(cfg, effective) == "" && effectiveInferenceWIF(cfg, effective) == "" {
		return false, false, nil
	}
	if effectiveInferenceProject(cfg, effective) == "" {
		var exists bool
		exists, err = client.RepoSecretExists(ctx, owner, repo, "FULLSEND_GCP_PROJECT_ID")
		if err != nil {
			return false, false, fmt.Errorf("checking existing secret FULLSEND_GCP_PROJECT_ID: %w (pass --inference-project to skip this check)", err)
		}
		if !exists {
			return false, false, fmt.Errorf("--inference-project is required for per-repo setup (no existing secret found)")
		}
		reuseProject = true
	}
	if effectiveInferenceWIF(cfg, effective) == "" {
		var exists bool
		exists, err = client.RepoSecretExists(ctx, owner, repo, "FULLSEND_GCP_WIF_PROVIDER")
		if err != nil {
			return false, false, fmt.Errorf("checking existing secret FULLSEND_GCP_WIF_PROVIDER: %w (pass --inference-wif-provider to skip this check)", err)
		}
		if !exists {
			return false, false, fmt.Errorf("--inference-wif-provider is required for per-repo setup (no existing secret found)")
		}
		reuseWIF = true
	}
	return reuseProject, reuseWIF, nil
}

// effectiveInferenceProject returns the GCP inference project to use:
// the explicit --inference-project flag if set, otherwise the value
// from the composed effective config, otherwise empty.
func effectiveInferenceProject(cfg githubSetupConfig, effective config.PerRepoConfigReader) string {
	if cfg.inferenceProject != "" {
		return cfg.inferenceProject
	}
	if effective != nil {
		return effective.ConfigInferenceProject()
	}
	return ""
}

// effectiveInferenceWIF returns the GCP inference WIF provider to use:
// the explicit --inference-wif-provider flag if set, otherwise the
// value from the composed effective config, otherwise empty.
func effectiveInferenceWIF(cfg githubSetupConfig, effective config.PerRepoConfigReader) string {
	if cfg.inferenceWIFProvider != "" {
		return cfg.inferenceWIFProvider
	}
	if effective != nil {
		return effective.ConfigInferenceWIFProvider()
	}
	return ""
}

// effectiveSetupMintURL returns the mint URL to use: the explicit
// --mint-url flag if set, otherwise the value from the composed
// effective config, otherwise config.DefaultPerRepoMintURL.
func effectiveSetupMintURL(cfg githubSetupConfig, effective config.PerRepoConfigReader) string {
	if cfg.mintURL != "" {
		return cfg.mintURL
	}
	if effective != nil {
		if u := effective.ConfigMintURL(); u != "" {
			return u
		}
	}
	return config.DefaultPerRepoMintURL
}

// effectiveInferenceRegion returns the inference region to use: the
// explicit --inference-region flag if set, otherwise the value from
// the composed effective config, otherwise
// config.DefaultPerRepoInferenceRegion.
func effectiveInferenceRegion(cfg githubSetupConfig, effective config.PerRepoConfigReader) string {
	if cfg.inferenceRegion != "" {
		return cfg.inferenceRegion
	}
	if effective != nil {
		if r := effective.ConfigInferenceRegion(); r != "" {
			return r
		}
	}
	return config.DefaultPerRepoInferenceRegion
}

// effectiveSetupRuntime returns the runtime to use: the explicit
// --runtime flag if set, otherwise the value from the composed
// effective config, otherwise empty.
func effectiveSetupRuntime(cfg githubSetupConfig, effective config.PerRepoConfigReader) string {
	if cfg.runtime != "" {
		return cfg.runtime
	}
	if effective != nil {
		return effective.ConfigRuntime()
	}
	return ""
}

// openaiSetupFlags are the three flags that together set inference.openai.
var openaiSetupFlags = []string{"openai-audience", "openai-identity-provider-id", "openai-service-account-id"}

func openaiFlagsChanged(cfg githubSetupConfig) bool {
	for _, name := range openaiSetupFlags {
		if cfg.changedFlags[name] {
			return true
		}
	}
	return false
}

func (cfg githubSetupConfig) openaiIDs() config.OpenAIWIFConfig {
	return config.OpenAIWIFConfig{
		Audience:           strings.TrimSpace(cfg.openaiAudience),
		IdentityProviderID: strings.TrimSpace(cfg.openaiIdentityProviderID),
		ServiceAccountID:   strings.TrimSpace(cfg.openaiServiceAccountID),
	}
}

// validateOpenAISetupFlags enforces all-three-or-none: a run needs the
// complete trio, and a partial block in config.yaml would only fail later,
// at the first GPT run. Passing all three empty explicitly clears the block.
func validateOpenAISetupFlags(cfg githubSetupConfig) error {
	if !openaiFlagsChanged(cfg) {
		return nil
	}
	ids := cfg.openaiIDs()
	if ids.IsZero() {
		// Clearing the block is deliberate only when all three flags were
		// passed (empty); a single empty flag is a mistake.
		for _, name := range openaiSetupFlags {
			if !cfg.changedFlags[name] {
				return fmt.Errorf("--openai-audience, --openai-identity-provider-id and --openai-service-account-id must be set together (pass all three empty to remove the block)")
			}
		}
		return nil
	}
	if missing := ids.Missing(); len(missing) > 0 {
		return fmt.Errorf("--openai-audience, --openai-identity-provider-id and --openai-service-account-id must be set together (missing %s)", strings.Join(missing, ", "))
	}
	return nil
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
	openAIRepoSecretName:        {storage: storageSecret},
}

func newGitHubSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <owner/repo> <key> <value>",
		Short: "Update a config value (secret or variable)",
		Long: `Sets a fullsend config value on a repo. The CLI maintains an internal
mapping of which keys are stored as secrets vs variables, so the user
doesn't need to know the storage type.

Valid keys:
  FULLSEND_GCP_REGION         repo variable   GCP region for inference
  FULLSEND_REVIEW_CLIENT_ID   repo variable   review app OAuth client ID
  FULLSEND_PER_REPO_INSTALL   repo variable   per-repo install marker
  FULLSEND_GCP_PROJECT_ID     repo secret     GCP project for inference
  FULLSEND_GCP_WIF_PROVIDER   repo secret     WIF provider resource name
  FULLSEND_OPENAI_API_KEY     repo secret     opt-in OpenAI API key when WIF is unset`,
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

	if key == openAIRepoSecretName && strings.TrimSpace(value) == "" {
		return fmt.Errorf("value for %s must not be empty", key)
	}

	owner, repo, isRepo := parseTarget(target)
	if !isRepo {
		return fmt.Errorf("fullsend github set requires an owner/repo target, got %q: per-org installation has been removed; set values on a repository with 'fullsend github set <owner/repo> <key> <value>'", target)
	}
	if !githubOwnerPattern.MatchString(owner) {
		return fmt.Errorf("invalid owner name %q: must contain only alphanumeric characters and hyphens", owner)
	}
	if !githubRepoPattern.MatchString(repo) {
		return fmt.Errorf("invalid repo name %q: must contain only alphanumeric characters, hyphens, dots, or underscores", repo)
	}

	switch key {
	case "FULLSEND_GCP_WIF_PROVIDER":
		if err := validateWIFProvider(value); err != nil {
			return err
		}
	}

	switch info.storage {
	case storageVariable:
		printer.StepStart(fmt.Sprintf("Setting repo variable %s on %s/%s", key, owner, repo))
		if err := client.CreateOrUpdateRepoVariable(ctx, owner, repo, key, value); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set repo variable %s", key))
			return fmt.Errorf("setting repo variable %s: %w", key, err)
		}
		printer.StepDone(fmt.Sprintf("Set repo variable %s on %s/%s", key, owner, repo))
	case storageSecret:
		printer.StepStart(fmt.Sprintf("Setting repo secret %s on %s/%s", key, owner, repo))
		if err := client.CreateRepoSecret(ctx, owner, repo, key, value); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set repo secret %s", key))
			return fmt.Errorf("setting repo secret %s: %w", key, err)
		}
		printer.StepDone(fmt.Sprintf("Set repo secret %s on %s/%s", key, owner, repo))
	}

	return nil
}

// resolveReviewAppClientID attempts to look up the review agent's OAuth
// client ID via the GitHub API. Returns the client ID on success, or
// an empty string if the lookup fails (best-effort — a missing client ID
// degrades incremental reviews but does not block installation).
func resolveReviewAppClientID(ctx context.Context, client forge.Client, appSet string) string {
	return appsetup.ResolveReviewAppClientID(ctx, client, appSet)
}
