package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/inference/vertex"
	"github.com/fullsend-ai/fullsend/internal/layers"
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

			if err := validateMintURLHTTPS(cfg.mintURL); err != nil {
				return err
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

	cmd.Flags().StringVar(&cfg.mintURL, "mint-url", DefaultMintURL, "token mint URL (default: hosted public mint)")
	cmd.Flags().StringVar(&cfg.agents, "agents", strings.Join(config.DefaultAgentRoles(), ","), "comma-separated agent roles")
	cmd.Flags().StringVar(&cfg.inferenceProject, "inference-project", "", "GCP project ID for inference")
	cmd.Flags().StringVar(&cfg.inferenceRegion, "inference-region", "global", "GCP region for inference")
	cmd.Flags().StringVar(&cfg.inferenceWIFProvider, "inference-wif-provider", "", "full WIF provider resource name")
	cmd.Flags().BoolVar(&cfg.skipAppSetup, "skip-app-setup", false, "skip GitHub App creation/setup")
	cmd.Flags().BoolVar(&cfg.publicApps, "public", false, "create public (unlisted) GitHub Apps")
	cmd.Flags().StringVar(&cfg.appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for GitHub Apps")
	cmd.Flags().BoolVar(&cfg.enrollAll, "enroll-all", false, "enroll all repositories without prompting")
	cmd.Flags().BoolVar(&cfg.enrollNone, "enroll-none", false, "skip repository enrollment without prompting")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "print actions without making changes")
	cmd.Flags().BoolVar(&cfg.direct, "direct", false, "push scaffold files directly to the default branch instead of creating a PR")
	addVendorFlags(cmd, &cfg.vendor, &cfg.fullsendBinary, &cfg.fullsendSource)

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

	perRepoCfg := config.NewPerRepoConfig(roles, cfg.target)
	if err := perRepoCfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	cfgYAML, err := perRepoCfg.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling per-repo config: %w", err)
	}

	installFiles, err := scaffold.CollectPerRepoInstallFiles(cfg.vendor)
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
	files = append(files, forge.TreeFile{
		Path:    ".fullsend/config.yaml",
		Content: cfgYAML,
		Mode:    "100644",
	})

	repoVars := map[string]string{
		"FULLSEND_MINT_URL":   cfg.mintURL,
		"FULLSEND_GCP_REGION": cfg.inferenceRegion,
		forge.PerRepoGuardVar: "true",
	}

	repoSecrets := make(map[string]string)
	if !reuseProject {
		repoSecrets["FULLSEND_GCP_PROJECT_ID"] = cfg.inferenceProject
	}
	if !reuseWIF {
		repoSecrets["FULLSEND_GCP_WIF_PROVIDER"] = cfg.inferenceWIFProvider
	}

	if cfg.dryRun {
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		for _, f := range files {
			printer.StepDone(fmt.Sprintf("Would commit: %s (%d bytes)", f.Path, len(f.Content)))
		}
		printer.Blank()
		printer.StepInfo("Would set repository variables:")
		for _, name := range sortedStringMapKeys(repoVars) {
			printer.StepInfo(fmt.Sprintf("  %s = %s", name, repoVars[name]))
		}
		secretNames := sortedStringMapKeys(repoSecrets)
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
		files, _, vendorErr = appendVendorTreeFiles(printer, owner, repo, files, cfg.vendor, cfg.fullsendBinary, cfg.fullsendSource)
		if vendorErr != nil {
			return fmt.Errorf("collecting vendored assets: %w", vendorErr)
		}
	}

	if err := applyPerRepoScaffold(ctx, client, printer, owner, repo, files, repoVars, repoSecrets, cfg.direct); err != nil {
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

	allRepos, err := client.ListOrgRepos(ctx, org)
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

	// Build config.
	privateRepo := false
	var inferenceProvider inference.Provider
	var inferenceProviderName string
	if cfg.inferenceProject != "" {
		vcfg := vertex.Config{
			ProjectID:   cfg.inferenceProject,
			Region:      cfg.inferenceRegion,
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
	orgCfg.Dispatch.Mode = "oidc-mint"

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	enrolledRepoIDs := collectEnrolledRepoIDs(allRepos, enabledRepos)
	dispatcher := &skipMintDispatcher{mintURL: cfg.mintURL}

	var vendorFn layers.VendorFunc
	var vendorCollect layers.VendorCollectFunc
	if cfg.vendor {
		vendorFn, vendorCollect = vendorStackArgs(true, cfg.fullsendBinary, cfg.fullsendSource)
	}

	stack := buildLayerStack(ctx, org, client, orgCfg, printer, user, privateRepo, enabledRepos, agentCreds, enrolledRepoIDs, inferenceProvider, cfg.vendor, vendorFn, vendorCollect, "", dispatcher, commitSHA, cfg.direct)

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

		creds, credErr := runAppSetup(ctx, client, printer, org, roles, "", cfg.mintURL, cfg.publicApps, nil, cfg.appSet, nil)
		if credErr != nil {
			return credErr
		}

		// Rebuild with real credentials.
		agentCreds = creds
		orgCfg = config.NewOrgConfig(repoNames, enabledRepos, roles, inferenceProviderName, org)
		orgCfg.Dispatch.Mode = "oidc-mint"

		stack = buildLayerStack(ctx, org, client, orgCfg, printer, user, privateRepo, enabledRepos, agentCreds, enrolledRepoIDs, inferenceProvider, cfg.vendor, vendorFn, vendorCollect, "", dispatcher, commitSHA, cfg.direct)
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

	installations, listErr := client.ListOrgInstallations(ctx, org)
	var existingSlugs []string
	if listErr == nil {
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
		// Can't check — fall back to showing all of them.
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
	cmd := &cobra.Command{
		Use:   "sync-scaffold <org>",
		Short: "Update workflow templates in .fullsend",
		Long:  "Re-commits scaffold files (shim and maintenance workflows) to the .fullsend repo without touching secrets, variables, or enrollment. Useful after fullsend version upgrades. Idempotent and safe to run repeatedly.",
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

			return runGitHubSyncScaffold(cmd.Context(), client, printer, org)
		},
	}

	return cmd
}

// runGitHubSyncScaffold runs only the WorkflowsLayer.
func runGitHubSyncScaffold(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
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

	wfLayer := layers.NewWorkflowsLayer(org, client, printer, user, version, vendored).WithDirect(true)
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
