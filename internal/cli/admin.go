package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/inference/vertex"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Manage fullsend installation for an organization",
		Long:  "Administrative commands for installing, uninstalling, and analyzing fullsend in a GitHub organization.",
	}
	cmd.AddCommand(newInstallCmd())
	cmd.AddCommand(newUninstallCmd())
	cmd.AddCommand(newAnalyzeCmd())
	cmd.AddCommand(newEnableCmd())
	cmd.AddCommand(newDisableCmd())
	return cmd
}

// resolveToken finds a GitHub token by checking, in order:
//  1. GH_TOKEN env var
//  2. GITHUB_TOKEN env var
//  3. gh auth token (subprocess call to the GitHub CLI)
//
// This chain allows users who are already authenticated with gh to use
// fullsend without manually exporting tokens. The CLI runs a preflight
// check before each operation and reports exactly which scopes are
// missing, so callers do not need to request all scopes upfront.
//
// Note that gh auth scopes apply to every organization the account
// belongs to. Users who want to limit the blast radius can create a
// fine-grained PAT scoped to a single org and export it as GH_TOKEN.
func resolveToken() (string, error) {
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token, nil
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		token := strings.TrimSpace(string(out))
		if token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no GitHub token found: set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login'")
}

// validateOrgName checks that org is a valid GitHub organization name.
func validateOrgName(org string) error {
	if org == "" {
		return fmt.Errorf("organization name cannot be empty")
	}
	if strings.HasPrefix(org, "-") || strings.HasSuffix(org, "-") {
		return fmt.Errorf("organization name cannot start or end with a hyphen")
	}
	for _, c := range org {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("organization name contains invalid character: %c", c)
		}
	}
	return nil
}

// githubOwnerPattern matches valid GitHub usernames and org names
// (alphanumeric and single hyphens only, no dots or underscores).
var githubOwnerPattern = regexp.MustCompile(`^[a-zA-Z0-9](-?[a-zA-Z0-9])*$`)

// githubRepoPattern matches valid GitHub repository names
// (alphanumeric, hyphens, dots, and underscores).
var githubRepoPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$`)

// rolePattern validates agent role names (lowercase alphanumeric, hyphens, underscores).
var rolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// perOrgOnlyFlags are flags that only apply to per-org mode.
var perOrgOnlyFlags = []string{
	"vendor-fullsend-binary", "enroll-all", "enroll-none",
}

// skipMintDispatcher implements dispatch.Dispatcher for --skip-mint-check mode.
// It returns the user-provided mint URL without making any GCP API calls.
type skipMintDispatcher struct {
	mintURL string
}

func (d *skipMintDispatcher) Name() string                        { return "skip-mint-check" }
func (d *skipMintDispatcher) OrgSecretNames() []string            { return nil }
func (d *skipMintDispatcher) OrgVariableNames() []string          { return []string{"FULLSEND_MINT_URL"} }
func (d *skipMintDispatcher) StoreAgentPEM(context.Context, string, string, []byte) error {
	return nil
}
func (d *skipMintDispatcher) Provision(context.Context) (map[string]string, error) {
	return map[string]string{"FULLSEND_MINT_URL": d.mintURL}, nil
}

type perRepoInstallConfig struct {
	RepoFullName        string
	Agents              string
	MintURL             string
	InferenceRegion     string
	InferenceProject    string
	InferenceWIFProvider string
	MintProject         string
	MintRegion          string
	DryRun              bool
	SkipAppSetup        bool
	PublicApps          bool
	MintProvider        string
	MintSourceDir       string
	MintSkipDeploy      bool
	SkipMintCheck       bool
	AppSet              string
}

// wifProviderPattern validates the full WIF provider resource name format
// required by google-github-actions/auth@v3.
// GCP pool/provider IDs: 4-32 chars, [a-z0-9-], start with letter, no trailing hyphen.
var wifProviderPattern = regexp.MustCompile(
	`^projects/\d+/locations/global/workloadIdentityPools/[a-z][a-z0-9-]{2,30}[a-z0-9]/providers/[a-z][a-z0-9-]{2,30}[a-z0-9]$`,
)

func validateWIFProvider(raw string) error {
	if !wifProviderPattern.MatchString(raw) {
		return fmt.Errorf(
			"--inference-wif-provider must be a full WIF provider resource name "+
				"(projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}), got %q",
			raw,
		)
	}
	return nil
}

func validateMintURL(raw string) error {
	if err := validateMintURLHTTPS(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(parsed.Host, ".run.app") &&
		!strings.HasSuffix(parsed.Host, ".cloudfunctions.net") {
		return fmt.Errorf("--mint-url must be a Cloud Run URL (.run.app or .cloudfunctions.net), got host %q", parsed.Host)
	}
	return nil
}

func validateSkipMintCheck(mintURL string) error {
	if mintURL == "" {
		return fmt.Errorf("--mint-url is required when using --skip-mint-check")
	}
	return validateMintURLHTTPS(mintURL)
}

func validateMintURLHTTPS(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		scheme := ""
		if parsed != nil {
			scheme = parsed.Scheme
		}
		return fmt.Errorf("--mint-url must be a valid HTTPS URL (got scheme=%q)", scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("--mint-url must not contain embedded credentials (userinfo)")
	}
	return nil
}

// parseAgentRoles splits a comma-separated agents string into a validated role list.
func parseAgentRoles(agents string) ([]string, error) {
	var roles []string
	for _, entry := range strings.Split(agents, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			if !rolePattern.MatchString(trimmed) {
				return nil, fmt.Errorf("invalid role name %q: must match %s", trimmed, rolePattern.String())
			}
			roles = append(roles, trimmed)
		}
	}
	return roles, nil
}

func newInstallCmd() *cobra.Command {
	var agents string
	var dryRun bool
	var skipAppSetup bool
	var vendorBinary bool
	var enrollAllFlag bool
	var enrollNoneFlag bool
	var inferenceProject string
	var inferenceRegion string
	var inferenceWIFProvider string
	var mintProvider string
	var mintProject string
	var mintRegion string
	var mintSourceDir string
	var mintSkipDeploy bool
	var skipMintCheck bool
	var publicApps bool
	var appSet string
	// Per-repo flags.
	var mintURL string

	cmd := &cobra.Command{
		Use:   "install <org-or-owner/repo>",
		Short: "Install fullsend in an organization or repository",
		Long: `Sets up the fullsend agentic development pipeline.

Per-org mode (argument is an org name, e.g. "acme"):
  Creates the .fullsend config repo, per-role GitHub Apps, token mint,
  shim workflows, secrets, and repo enrollment.

Per-repo mode (argument is owner/repo, e.g. "acme/widget"):
  Bootstraps a single repository with the shim workflow and .fullsend/
  configuration directory. No config repo or cross-repo dispatch needed.

Inference authentication:
  If --inference-project is provided without --inference-wif-provider,
  fullsend auto-provisions WIF infrastructure in the GCP project
  (requires project access with AI Platform permissions).

  If --inference-wif-provider is also provided with the full resource
  name (projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}),
  auto-provisioning is skipped and the value is used as-is. This is
  useful when a GCP admin has already provisioned WIF and shared the
  provider resource name.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := appsetup.ValidateAppSet(appSet); err != nil {
				return fmt.Errorf("invalid --app-set: %w", err)
			}

			arg := args[0]
			if strings.Contains(arg, "/") {
				for _, name := range perOrgOnlyFlags {
					if cmd.Flags().Changed(name) {
						return fmt.Errorf("--%s is only valid for per-org installation (fullsend admin install <org>)", name)
					}
				}
				perRepoAgents := agents
				if !cmd.Flags().Changed("agents") {
					perRepoAgents = strings.Join(config.PerRepoDefaultRoles(), ",")
				}
				perRepoMintProject := mintProject
				if perRepoMintProject == "" {
					perRepoMintProject = inferenceProject
				}
				return runPerRepoInstall(cmd.Context(), perRepoInstallConfig{
					RepoFullName:        arg,
					Agents:              perRepoAgents,
					MintURL:             mintURL,
					InferenceRegion:     inferenceRegion,
					InferenceProject:    inferenceProject,
					InferenceWIFProvider: inferenceWIFProvider,
					MintProject:         perRepoMintProject,
					MintRegion:          mintRegion,
					DryRun:              dryRun,
					SkipAppSetup:        skipAppSetup,
					PublicApps:          publicApps,
					MintProvider:        mintProvider,
					MintSourceDir:       mintSourceDir,
					MintSkipDeploy:      mintSkipDeploy,
					SkipMintCheck:       skipMintCheck,
					AppSet:              appSet,
				})
			}

			org := arg
			if err := validateOrgName(org); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner()
			printer.Blank()
			printer.Header("Installing fullsend for " + org)
			printer.Blank()

			roles, err := parseAgentRoles(agents)
			if err != nil {
				return err
			}

			if skipMintCheck {
				if err := validateSkipMintCheck(mintURL); err != nil {
					return err
				}
			} else {
				// Validate mint provider (only required for real installs, not dry-run).
				if !dryRun {
					if mintProvider != "gcf" {
						return fmt.Errorf("--mint-provider must be 'gcf'")
					}
					if mintProject == "" {
						return fmt.Errorf("--mint-project is required")
					}
				}

				// Validate --mint-url early (before app setup which is irreversible).
				if mintURL != "" {
					if err := validateMintURL(mintURL); err != nil {
						return err
					}
				}
			}

			// Validate inference flag dependencies.
			if inferenceProject == "" && (cmd.Flags().Changed("inference-region") || inferenceWIFProvider != "") {
				return fmt.Errorf("--inference-wif-provider and --inference-region require --inference-project to be set")
			}

			// Validate WIF provider format when explicitly given.
			if inferenceWIFProvider != "" {
				if err := validateWIFProvider(inferenceWIFProvider); err != nil {
					return err
				}
				printer.StepWarn("Using provided WIF provider value — skipping inference provider auto-provisioning")
			}

			// Auto-provision WIF when not explicitly given (idempotent: safe to re-run).
			if inferenceProject != "" && inferenceWIFProvider == "" {
				if dryRun {
					printer.StepInfo("Would auto-provision WIF provider in project " + inferenceProject)
				} else {
					printer.StepStart("Provisioning WIF infrastructure for inference")
					gcpClient := gcf.NewLiveGCFClient()
					provisioner := gcf.NewProvisioner(gcf.Config{
						ProjectID:  inferenceProject,
						GitHubOrgs: []string{org},
					}, gcpClient)
					inferenceWIFProvider, err = provisioner.ProvisionWIF(ctx)
					if err != nil {
						printer.StepFail("WIF provisioning failed")
						return fmt.Errorf("provisioning WIF for inference: %w", err)
					}
					printer.StepDone("WIF infrastructure ready")
					printer.StepInfo("IAM policy changes may take up to 7 minutes to propagate")
				}
			}

			// Build inference provider from flags.
			var inferenceProvider inference.Provider
			var inferenceProviderName string
			if inferenceProject != "" {
				vcfg := vertex.Config{
					ProjectID:   inferenceProject,
					Region:      inferenceRegion,
					WIFProvider: inferenceWIFProvider,
				}
				inferenceProvider = vertex.New(vcfg)
				inferenceProviderName = "vertex"
			} else {
				// Preserve existing inference config if no inference flags provided.
				inferenceProviderName = loadExistingInferenceProvider(ctx, client, org)
			}

			// Validate enrollment flags.
			if enrollAllFlag && enrollNoneFlag {
				return fmt.Errorf("--enroll-all and --enroll-none are mutually exclusive")
			}

			// Determine enrollment choice: use flag if set, otherwise prompt.
			var enrollAll bool
			if enrollAllFlag {
				enrollAll = true
			} else if enrollNoneFlag {
				enrollAll = false
			} else {
				// Prompt for enrollment choice: all or none.
				enrollAll, err = promptEnrollment(printer, os.Stdin)
				if err != nil {
					return err
				}
			}

			// Discover all org repos upfront to avoid redundant API calls in runDryRun/runInstall.
			allRepos, err := client.ListOrgRepos(ctx, org)
			if err != nil {
				return fmt.Errorf("listing org repos: %w", err)
			}

			var repos []string
			if enrollAll {
				// Filter out .fullsend and per-repo installed repos from enrollment.
				var reader *bufio.Reader
				var skippedPerRepo int
				var skippedErrors int
				var eligibleCount int
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
						if reader == nil {
							reader = bufio.NewReader(os.Stdin)
						}
						printer.StepInfo(fmt.Sprintf("%s has per-repo install (guard=%s). Enroll with per-org? [y/n]: ", r.Name, guardVal))
						choice, _ := reader.ReadString('\n')
						if strings.TrimSpace(strings.ToLower(choice)) != "y" {
							printer.StepInfo(fmt.Sprintf("Skipping %s", r.Name))
							skippedPerRepo++
							continue
						}
					}
					repos = append(repos, r.Name)
				}
				// If every eligible repo was skipped due to guard-check errors,
				// the token likely lacks the required scope — fail loudly.
				if eligibleCount > 0 && skippedErrors == eligibleCount {
					return fmt.Errorf("all %d repos were skipped due to guard-check errors — verify your token has variables:read scope", eligibleCount)
				}
				msg := fmt.Sprintf("Enrolling %d repositories (excluding %s)", len(repos), forge.ConfigRepoName)
				if skippedPerRepo-skippedErrors > 0 {
					msg += fmt.Sprintf(", %d per-repo installed", skippedPerRepo-skippedErrors)
				}
				if skippedErrors > 0 {
					msg += fmt.Sprintf(", %d guard-check errors", skippedErrors)
				}
				printer.StepInfo(msg)
			} else {
				printer.StepInfo("No repositories will be enrolled during install")
				printer.StepInfo("To enroll repositories later, use:")
				printer.StepInfo(fmt.Sprintf("  fullsend admin enable repos %s <repo-name> [repo-name...]", org))
				printer.StepInfo(fmt.Sprintf("  fullsend admin enable repos %s --all", org))
			}
			printer.Blank()

			if dryRun {
				return runDryRun(ctx, client, printer, org, repos, roles, inferenceProvider, inferenceProviderName, skipMintCheck, mintURL, allRepos)
			}

			if err := checkInstallScopes(ctx, client, printer); err != nil {
				return err
			}
			printer.Blank()

			// Pre-copy PEM secrets for shared public apps before app setup.
			var sharedSlugs map[string]string
			var perOrgStoredIDs map[string]string
			if mintProject != "" && !skipAppSetup && !skipMintCheck {
				slugs, storedIDs, err := copySharedAppPEMs(ctx, client, printer, org, roles, mintProject, mintRegion)
				if err != nil {
					return err
				}
				sharedSlugs = slugs
				perOrgStoredIDs = storedIDs
			}

			// Collect agent credentials via app setup.
			var agentCreds []layers.AgentCredentials
			if !skipAppSetup && !skipMintCheck {
				if err := ensureConfigRepoExists(ctx, client, printer, org); err != nil {
					return err
				}
				creds, err := runAppSetup(ctx, client, printer, org, roles, mintProject, publicApps, sharedSlugs, appSet, perOrgStoredIDs)
				if err != nil {
					return err
				}
				agentCreds = creds
			}

			return runInstall(ctx, client, printer, org, repos, roles, agentCreds, inferenceProvider, inferenceProviderName, vendorBinary, mintProvider, mintProject, mintRegion, mintSourceDir, mintSkipDeploy, mintURL, skipMintCheck, allRepos)
		},
	}

	cmd.Flags().StringVar(&agents, "agents", strings.Join(config.DefaultAgentRoles(), ","), "comma-separated agent roles")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&skipAppSetup, "skip-app-setup", false, "skip GitHub App creation/setup")
	cmd.Flags().BoolVar(&vendorBinary, "vendor-fullsend-binary", false, "cross-compile and upload the fullsend binary into .fullsend/bin/ for development iteration")
	cmd.Flags().BoolVar(&enrollAllFlag, "enroll-all", false, "enroll all repositories without prompting")
	cmd.Flags().BoolVar(&enrollNoneFlag, "enroll-none", false, "skip repository enrollment without prompting")
	cmd.Flags().StringVar(&inferenceProject, "inference-project", "", "GCP project ID for inference (Agent Platform)")
	cmd.Flags().StringVar(&inferenceRegion, "inference-region", "global", "GCP region for inference (default: global)")
	cmd.Flags().StringVar(&inferenceWIFProvider, "inference-wif-provider", "", "full WIF provider resource name (projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}); skips auto-provisioning when set")
	cmd.Flags().StringVar(&mintProvider, "mint-provider", "gcf", "token mint provider (gcf)")
	cmd.Flags().StringVar(&mintProject, "mint-project", "", "cloud project for token mint (e.g. GCP project ID)")
	cmd.Flags().StringVar(&mintRegion, "mint-region", "us-central1", "cloud region for token mint")
	cmd.Flags().StringVar(&mintSourceDir, "mint-source-dir", "", "path to mint function source (default: internal/mint/)")
	cmd.Flags().BoolVar(&mintSkipDeploy, "skip-mint-deploy", false, "skip Cloud Function deployment, reuse existing mint URL")
	cmd.Flags().BoolVar(&skipMintCheck, "skip-mint-check", false, "skip mint validation, GCP provisioning, and app setup; requires --mint-url")
	cmd.Flags().BoolVar(&publicApps, "public", false, "create public (unlisted) GitHub Apps installable by other orgs")
	cmd.Flags().StringVar(&appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for GitHub Apps (e.g., fullsend-ai creates fullsend-ai-fullsend, fullsend-ai-coder)")
	// Shared flags.
	cmd.Flags().StringVar(&mintURL, "mint-url", "", "token mint URL for OIDC token exchange")

	return cmd
}

func runPerRepoInstall(ctx context.Context, c perRepoInstallConfig) error {
	repoFullName := c.RepoFullName
	agents := c.Agents
	mintURL := c.MintURL
	inferenceRegion := c.InferenceRegion
	inferenceProject := c.InferenceProject
	inferenceWIFProvider := c.InferenceWIFProvider
	mintProject := c.MintProject
	mintRegion := c.MintRegion
	dryRun := c.DryRun
	skipAppSetup := c.SkipAppSetup
	publicApps := c.PublicApps
	mintProvider := c.MintProvider
	mintSourceDir := c.MintSourceDir
	mintSkipDeploy := c.MintSkipDeploy
	skipMintCheck := c.SkipMintCheck

	if strings.Contains(repoFullName, "://") || strings.HasPrefix(repoFullName, "www.") {
		return fmt.Errorf("expected owner/repo format, got a URL — use just the owner/repo portion (e.g. acme/widget)")
	}
	parts := strings.SplitN(repoFullName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("repo must be in owner/repo format, got %q", repoFullName)
	}
	owner, repo := parts[0], parts[1]
	if !githubOwnerPattern.MatchString(owner) {
		return fmt.Errorf("invalid owner name %q: must contain only alphanumeric characters and hyphens", owner)
	}
	if !githubRepoPattern.MatchString(repo) {
		return fmt.Errorf("invalid repo name %q: must contain only alphanumeric characters, hyphens, dots, or underscores", repo)
	}

	if skipMintCheck {
		if err := validateSkipMintCheck(mintURL); err != nil {
			return err
		}
	} else if mintURL != "" {
		if err := validateMintURL(mintURL); err != nil {
			return err
		}
	}
	if mintProject == "" && mintURL == "" && !skipMintCheck {
		return fmt.Errorf("--mint-project (or --inference-project) is required for per-repo installation")
	}
	if inferenceProject == "" {
		return fmt.Errorf("--inference-project is required for per-repo installation")
	}
	// Validate WIF provider format when explicitly given.
	if inferenceWIFProvider != "" {
		if err := validateWIFProvider(inferenceWIFProvider); err != nil {
			return err
		}
	}
	roles, err := parseAgentRoles(agents)
	if err != nil {
		return err
	}

	token, err := resolveToken()
	if err != nil {
		return err
	}

	client := gh.New(token)
	printer := ui.New(os.Stdout)

	printer.Banner()
	printer.Blank()
	printer.Header("Installing per-repo fullsend for " + repoFullName)
	printer.Blank()

	if inferenceWIFProvider != "" {
		printer.StepWarn("Using provided WIF provider value — skipping inference provider auto-provisioning")
	}

	cfg := config.NewPerRepoConfig(roles)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	shimContent, err := scaffold.PerRepoShimTemplate()
	if err != nil {
		return fmt.Errorf("loading per-repo shim template: %w", err)
	}

	cfgYAML, err := cfg.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling per-repo config: %w", err)
	}

	var files []forge.TreeFile
	files = append(files, forge.TreeFile{
		Path:    ".github/workflows/fullsend.yaml",
		Content: shimContent,
		Mode:    "100644",
	})
	files = append(files, forge.TreeFile{
		Path:    ".fullsend/config.yaml",
		Content: cfgYAML,
		Mode:    "100644",
	})

	for _, dir := range scaffold.PerRepoCustomizedDirs() {
		files = append(files, forge.TreeFile{
			Path:    dir + "/.gitkeep",
			Content: []byte(""),
			Mode:    "100644",
		})
	}

	needsWIFProvision := inferenceWIFProvider == ""

	guardVal, guardExists, guardErr := client.GetRepoVariable(ctx, owner, repo, forge.PerRepoGuardVar)
	if guardErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not check existing guard variable: %v", guardErr))
	}
	switch {
	case guardExists && guardVal == "true":
		printer.StepInfo(fmt.Sprintf("%s/%s is per-repo mode, updating installation", owner, repo))
	case guardExists && guardVal == "false":
		printer.StepWarn(fmt.Sprintf("%s/%s has per-repo guard set to %q — this install will re-enable it", owner, repo, guardVal))
	case guardExists:
		printer.StepWarn(fmt.Sprintf("%s/%s has per-repo guard set to unexpected value %q — overwriting with \"true\"", owner, repo, guardVal))
	default:
		printer.StepInfo(fmt.Sprintf("Setting up new per-repo installation for %s/%s", owner, repo))
	}

	// Phase 1: Discover existing infrastructure (read-only, safe for dry-run).
	var mintFound bool
	var appsFound bool
	var agentAppIDs map[string]string
	var agentPEMs map[string][]byte

	var existingIDs map[string]string

	if skipMintCheck {
		mintFound = true
		printer.StepDone(fmt.Sprintf("Using self-provisioned mint at %s (--skip-mint-check)", mintURL))
	} else {
		discoverer := gcf.NewProvisioner(gcf.Config{
			ProjectID:  mintProject,
			Region:     mintRegion,
			GitHubOrgs: []string{owner},
		}, gcf.NewLiveGCFClient())

		if mintURL != "" {
			mintFound = true
			// Mint URL provided — still discover role IDs from the function
			// to resolve existing apps. Skipped in dry-run to avoid requiring
			// GCP credentials for preview-only invocations.
			if mintProject != "" && !dryRun {
				printer.StepStart("Resolving app IDs from mint")
				discovery, discoverErr := discoverer.DiscoverMint(ctx)
				if discoverErr != nil {
					if !errors.Is(discoverErr, gcf.ErrFunctionNotFound) {
						printer.StepFail("Failed to read mint state")
						return fmt.Errorf("reading mint state: %w", discoverErr)
					}
					printer.StepDone("Mint function not found in project — will discover apps from setup")
				} else {
					existingIDs = discovery.RoleAppIDs
					printer.StepDone("Resolved app IDs from mint")
				}
			}
		} else if mintProject != "" {
			printer.StepStart("Discovering mint infrastructure")
			discovery, discoverErr := discoverer.DiscoverMint(ctx)
			if discoverErr != nil {
				if !errors.Is(discoverErr, gcf.ErrFunctionNotFound) {
					printer.StepFail("Mint discovery failed")
					return fmt.Errorf("failed to discover mint in project %s region %s: %w",
						mintProject, mintRegion, discoverErr)
				}
				printer.StepDone("No existing mint found — will deploy")
			} else {
				mintURL = discovery.URL
				mintFound = true
				existingIDs = discovery.RoleAppIDs
				printer.StepDone(fmt.Sprintf("Found mint at %s", mintURL))
			}
		}
	}

	if mintFound && existingIDs != nil {
		roleAppIDs, resolveErr := resolveSharedRoleAppIDs(ctx, client, existingIDs, owner, roles)
		if resolveErr != nil {
			printer.StepWarn(fmt.Sprintf("Could not resolve shared app IDs: %v (will attempt app creation)", resolveErr))
		} else {
			agentAppIDs = make(map[string]string, len(roles))
			appsFound = true
			for _, role := range roles {
				appID, ok := roleAppIDs[owner+"/"+role]
				if !ok {
					appsFound = false
					break
				}
				agentAppIDs[role] = appID
			}
		}
		if appsFound {
			printer.StepDone("Resolved all app IDs")
		} else {
			printer.StepDone("Some app IDs missing — will create apps")
		}
	}

	if dryRun {
		mintDisplay := mintURL
		if mintDisplay == "" {
			mintDisplay = fmt.Sprintf("(will deploy to project %s, region %s)", mintProject, mintRegion)
		}
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		if skipMintCheck {
			printer.StepInfo("Mint checks skipped (--skip-mint-check):")
			printer.StepInfo(fmt.Sprintf("  Mint URL (trusted): %s", mintURL))
			printer.StepInfo("  App setup: skipped")
			printer.StepInfo("  GCP mint validation: skipped")
			printer.StepInfo("  PEM storage: skipped")
		} else {
			if !appsFound && !skipAppSetup {
				printer.StepInfo(fmt.Sprintf("Would create GitHub Apps for roles: %s", strings.Join(roles, ", ")))
				if publicApps {
					printer.StepInfo("  Apps would be public (unlisted)")
				}
				printer.Blank()
			}
			if !mintFound {
				printer.StepInfo(fmt.Sprintf("Would deploy token mint to project %s, region %s", mintProject, mintRegion))
				printer.Blank()
			}
			printer.StepInfo("Mint infrastructure:")
			printer.StepInfo(fmt.Sprintf("  Mint URL: %s", mintDisplay))
			printer.StepInfo(fmt.Sprintf("  Mint project: %s, region: %s", mintProject, mintRegion))
			if mintFound {
				printer.StepInfo(fmt.Sprintf("  Would register %s in ALLOWED_ORGS", owner))
				printer.StepInfo(fmt.Sprintf("  Would set ROLE_APP_IDS entries for %s/{%s}", owner, strings.Join(roles, ",")))
			}
		}
		printer.Blank()
		if needsWIFProvision {
			printer.StepInfo("Would provision WIF infrastructure in GCP project " + inferenceProject)
			printer.StepInfo(fmt.Sprintf("  Service account: fullsend-mint@%s.iam.gserviceaccount.com", inferenceProject))
			printer.StepInfo("  WIF pool: fullsend-pool")
			printer.StepInfo(fmt.Sprintf("  WIF provider: %s", gcf.BuildRepoProviderID(owner, repo)))
			printer.StepInfo(fmt.Sprintf("  Repo restriction: %s/%s", owner, repo))
			printer.Blank()
		}
		for _, f := range files {
			printer.StepDone(fmt.Sprintf("Would write: %s (%d bytes)", f.Path, len(f.Content)))
		}
		printer.Blank()
		printer.StepInfo("Would set repository variables:")
		dryRunVars := map[string]string{
			"FULLSEND_MINT_URL":   mintDisplay,
			"FULLSEND_GCP_REGION": inferenceRegion,
			forge.PerRepoGuardVar: "true",
		}
		for _, name := range sortedStringMapKeys(dryRunVars) {
			printer.StepInfo(fmt.Sprintf("  %s = %s", name, dryRunVars[name]))
		}
		secretNames := []string{"FULLSEND_GCP_PROJECT_ID", "FULLSEND_GCP_WIF_PROVIDER"}
		printer.StepInfo(fmt.Sprintf("Would set %d repository secrets:", len(secretNames)))
		for _, name := range secretNames {
			printer.StepInfo(fmt.Sprintf("  %s", name))
		}
		return nil
	}

	// Early scope check — at minimum we need repo+workflow. If app creation
	// turns out to be needed, checkInstallScopes escalates below.
	if err := checkPerRepoScopes(ctx, client, printer); err != nil {
		return err
	}

	needAppSetup := !appsFound && !skipAppSetup && !skipMintCheck
	needMintDeploy := !mintFound && !skipMintCheck

	if !skipMintCheck && skipAppSetup && !appsFound {
		if !mintFound {
			return fmt.Errorf("no mint function found in project %s region %s and --skip-app-setup prevents creating one", mintProject, mintRegion)
		}
		return fmt.Errorf("could not resolve app IDs for %s from the mint and --skip-app-setup prevents creating them", owner)
	}

	// Scope escalation: app creation requires admin:org beyond the
	// repo+workflow scopes already verified above.
	if needAppSetup {
		if err := checkInstallScopes(ctx, client, printer); err != nil {
			return err
		}
	}

	// Phase 2: App creation + mint provisioning based on discovered state.
	if needAppSetup {
		var sharedSlugs map[string]string
		if mintProject != "" {
			slugs, storedIDs, slugErr := copySharedAppPEMs(ctx, client, printer, owner, roles, mintProject, mintRegion)
			if slugErr != nil {
				return slugErr
			}
			sharedSlugs = slugs
			if existingIDs == nil {
				existingIDs = storedIDs
			}
		}

		creds, credErr := runAppSetup(ctx, client, printer, owner, roles, mintProject, publicApps, sharedSlugs, c.AppSet, existingIDs)
		if credErr != nil {
			return credErr
		}

		agentAppIDs = make(map[string]string, len(roles))
		agentPEMs = make(map[string][]byte)
		for _, ac := range creds {
			if ac.AppID != 0 {
				agentAppIDs[ac.Role] = strconv.Itoa(ac.AppID)
				if ac.PEM != "" {
					agentPEMs[ac.Role] = []byte(ac.PEM)
				}
			}
		}
	}

	if skipMintCheck {
		printer.StepDone(fmt.Sprintf("Skipping mint provisioning (--skip-mint-check), using %s", mintURL))
	} else if needMintDeploy {
		if mintProvider != "gcf" {
			return fmt.Errorf("--mint-provider must be 'gcf' for mint deployment")
		}
		if mintSourceDir == "" {
			mintSourceDir = gcf.DefaultFunctionSourceDir()
		}
		deployMode := gcf.DeployAuto
		if mintSkipDeploy {
			deployMode = gcf.DeploySkip
		}

		printer.StepStart("Deploying token mint")
		mintProvisioner := gcf.NewProvisioner(gcf.Config{
			ProjectID:         mintProject,
			Region:            mintRegion,
			GitHubOrgs:        []string{owner},
			AgentPEMs:         agentPEMs,
			AgentAppIDs:       agentAppIDs,
			FunctionSourceDir: mintSourceDir,
			DeployMode:        deployMode,
			Repo:              owner + "/" + repo,
		}, gcf.NewLiveGCFClient())

		provResult, provErr := mintProvisioner.Provision(ctx)
		if provErr != nil {
			printer.StepFail("Mint deployment failed")
			return fmt.Errorf("provisioning mint: %w", provErr)
		}
		if url, ok := provResult["FULLSEND_MINT_URL"]; ok {
			mintURL = url
		}
		printer.StepDone(fmt.Sprintf("Mint deployed at %s", mintURL))
	} else {
		printer.StepStart("Validating mint infrastructure")
		mintProvisioner := gcf.NewProvisioner(gcf.Config{
			ProjectID:   mintProject,
			Region:      mintRegion,
			GitHubOrgs:  []string{owner},
			AgentAppIDs: agentAppIDs,
			AgentPEMs:   agentPEMs,
			MintURL:     mintURL,
			Repo:        owner + "/" + repo,
		}, gcf.NewLiveGCFClient())

		if _, err := mintProvisioner.Provision(ctx); err != nil {
			printer.StepFail("Mint provisioning failed")
			return fmt.Errorf("provisioning mint: %w", err)
		}
		printer.StepDone("Mint validated and org registered")
	}

	if needsWIFProvision {
		printer.StepStart("Provisioning WIF infrastructure")
		provisioner := gcf.NewProvisioner(gcf.Config{
			ProjectID:  inferenceProject,
			GitHubOrgs: []string{owner},
			Repo:       owner + "/" + repo,
		}, gcf.NewLiveGCFClient())
		var provErr error
		inferenceWIFProvider, provErr = provisioner.ProvisionWIF(ctx)
		if provErr != nil {
			printer.StepFail("WIF provisioning failed")
			return fmt.Errorf("provisioning WIF: %w", provErr)
		}
		printer.StepDone("WIF infrastructure ready")
		printer.StepInfo("IAM policy changes may take up to 7 minutes to propagate")
		printer.StepInfo("Agent workflows that authenticate via WIF may fail until propagation completes")
	}

	repoVars := map[string]string{
		"FULLSEND_MINT_URL":   mintURL,
		"FULLSEND_GCP_REGION": inferenceRegion,
		forge.PerRepoGuardVar: "true",
	}

	repoSecrets := map[string]string{
		"FULLSEND_GCP_PROJECT_ID":   inferenceProject,
		"FULLSEND_GCP_WIF_PROVIDER": inferenceWIFProvider,
	}

	printer.StepStart("Writing per-repo scaffold files")
	committed, err := client.CommitFiles(ctx, owner, repo,
		"chore: initialize fullsend per-repo installation", files)
	if err != nil {
		printer.StepFail("Failed to write scaffold files")
		return fmt.Errorf("committing scaffold files: %w", err)
	}
	if committed {
		printer.StepDone(fmt.Sprintf("Wrote %d files", len(files)))
	} else {
		printer.StepDone("Scaffold up to date")
	}

	printer.StepStart("Configuring repository variables")
	for _, name := range sortedStringMapKeys(repoVars) {
		if err := client.CreateOrUpdateRepoVariable(ctx, owner, repo, name, repoVars[name]); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set variable %s", name))
			return fmt.Errorf("setting repo variable %s: %w", name, err)
		}
	}
	printer.StepDone(fmt.Sprintf("Set %d repository variables", len(repoVars)))

	printer.StepStart("Configuring repository secrets")
	for _, name := range sortedStringMapKeys(repoSecrets) {
		if err := client.CreateRepoSecret(ctx, owner, repo, name, repoSecrets[name]); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set secret %s", name))
			return fmt.Errorf("setting repo secret %s: %w", name, err)
		}
	}
	printer.StepDone(fmt.Sprintf("Set %d repository secrets", len(repoSecrets)))

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Per-repo installation complete for %s/%s", owner, repo))
	return nil
}

// vendorFullsendBinary cross-compiles the fullsend binary for linux/amd64
// and uploads it to .fullsend/bin/fullsend via layers.VendorBinary.
func vendorFullsendBinary(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
	printer.StepStart("Cross-compiling fullsend for linux/amd64")

	tmpBinary, err := os.CreateTemp("", "fullsend-linux-amd64-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpBinary.Close()
	defer os.Remove(tmpBinary.Name())

	buildCmd := exec.Command("go", "build",
		"-ldflags", fmt.Sprintf("-X github.com/fullsend-ai/fullsend/internal/cli.version=%s-vendored", version),
		"-o", tmpBinary.Name(),
		"./cmd/fullsend/",
	)
	buildCmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		printer.StepFail("Cross-compilation failed")
		return fmt.Errorf("cross-compiling: %w", err)
	}
	printer.StepDone("Cross-compiled fullsend for linux/amd64")

	printer.StepStart("Uploading vendored binary to .fullsend/bin/fullsend")
	if err := layers.VendorBinary(ctx, client, org, tmpBinary.Name()); err != nil {
		printer.StepFail("Failed to upload vendored binary")
		return err
	}

	info, _ := os.Stat(tmpBinary.Name())
	if info != nil {
		printer.StepDone(fmt.Sprintf("Uploaded vendored binary (%d MB)", info.Size()/(1024*1024)))
	} else {
		printer.StepDone("Uploaded vendored binary")
	}

	return nil
}

func newUninstallCmd() *cobra.Command {
	var yolo bool
	var appSet string

	cmd := &cobra.Command{
		Use:   "uninstall <org>",
		Short: "Remove fullsend from a GitHub organization",
		Long:  "Tears down the fullsend installation for a GitHub organization, removing the config repo and associated resources.",
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
			ctx := cmd.Context()

			printer.Banner()
			printer.Blank()
			printer.Header("Uninstalling fullsend from " + org)
			printer.Blank()

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

			return runUninstall(ctx, client, printer, org, appSet)
		},
	}

	cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")
	cmd.Flags().StringVar(&appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for GitHub Apps (used for fallback slug generation when config is unavailable)")

	return cmd
}

func newAnalyzeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analyze <org>",
		Short: "Analyze fullsend installation status",
		Long:  "Checks the current state of fullsend installation in a GitHub organization and reports what would need to change.",
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
			ctx := cmd.Context()

			printer.Banner()
			printer.Blank()
			printer.Header("Analyzing fullsend installation for " + org)
			printer.Blank()

			return runAnalyze(ctx, client, printer, org)
		},
	}

	return cmd
}

// runDryRun builds a layer stack with empty credentials and analyzes.
// If discoveredRepos is non-nil, it will be used instead of calling ListOrgRepos.
func runDryRun(ctx context.Context, client forge.Client, printer *ui.Printer, org string, enabledRepos, roles []string, inferenceProvider inference.Provider, inferenceProviderName string, skipMintCheck bool, mintURL string, discoveredRepos []forge.Repository) error {
	printer.Header("Dry run - analyzing what install would do")
	printer.Blank()

	var allRepos []forge.Repository
	var err error

	if discoveredRepos != nil {
		allRepos = discoveredRepos
		printer.StepDone(fmt.Sprintf("Using %d discovered repositories", len(allRepos)))
	} else {
		allRepos, err = client.ListOrgRepos(ctx, org)
		if err != nil {
			return fmt.Errorf("listing org repos: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Found %d repositories", len(allRepos)))
	}

	repoNames := repoNameList(allRepos)

	// Config repo is always public: cross-repo workflow_call from any
	// caller visibility (public, private, internal) only works reliably
	// when the called repo is public, across all GitHub plan tiers.
	privateRepo := false

	// When enabledRepos is nil the user chose not to modify enrollment.
	// Preserve existing enrollment so the dry-run analysis is accurate.
	// See #861.
	if enabledRepos == nil {
		enabledRepos = loadExistingEnabledRepos(ctx, client, org)
	}

	// Validate that every enabled repository matches a discovered repo.
	if err := validateEnabledRepos(enabledRepos, repoNames); err != nil {
		return err
	}

	// Build config with empty agents for analysis.
	cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, nil, inferenceProviderName)
	cfg.Dispatch.Mode = "oidc-mint"

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	// Build dummy agent credentials for analysis.
	var agentCreds []layers.AgentCredentials
	for _, role := range roles {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{Role: role},
		})
	}

	enrolledRepoIDs := collectEnrolledRepoIDs(allRepos, enabledRepos)
	var dispatcher dispatch.Dispatcher
	if skipMintCheck {
		dispatcher = &skipMintDispatcher{mintURL: mintURL}
	} else {
		dispatcher = gcf.NewProvisioner(gcf.Config{}, nil)
	}
	stack := buildLayerStack(org, client, cfg, printer, user, privateRepo, enabledRepos, agentCreds, enrolledRepoIDs, inferenceProvider, false, nil, dispatcher)

	if err := runPreflight(ctx, stack, layers.OpInstall, client, printer); err != nil {
		return err
	}
	printer.Blank()

	return printAnalysis(ctx, stack, printer)
}

// resolveSharedRoleAppIDs discovers app IDs for the given org by matching
// installed apps against existing ROLE_APP_IDS entries from other orgs.
func resolveSharedRoleAppIDs(ctx context.Context, client forge.Client, existingIDs map[string]string, owner string, roles []string) (map[string]string, error) {
	if len(existingIDs) == 0 {
		return nil, fmt.Errorf("mint has no existing ROLE_APP_IDS — cannot determine app IDs for %s", owner)
	}

	installations, err := client.ListOrgInstallations(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("listing installations for %s: %w", owner, err)
	}

	installedAppIDs := make(map[string]bool, len(installations))
	for _, inst := range installations {
		installedAppIDs[strconv.Itoa(inst.AppID)] = true
	}

	result := make(map[string]string, len(roles))
	for _, role := range roles {
		// If the owner already has an entry, use it directly.
		if appID, ok := existingIDs[owner+"/"+role]; ok && installedAppIDs[appID] {
			result[owner+"/"+role] = appID
			continue
		}
		// Otherwise, find a shared app from another org.
		// Sort keys for deterministic selection when multiple orgs share the role.
		sortedExisting := make([]string, 0, len(existingIDs))
		for k := range existingIDs {
			sortedExisting = append(sortedExisting, k)
		}
		sort.Strings(sortedExisting)
		for _, key := range sortedExisting {
			appID := existingIDs[key]
			parts := strings.SplitN(key, "/", 2)
			if len(parts) != 2 || parts[1] != role || parts[0] == owner {
				continue
			}
			if installedAppIDs[appID] {
				result[owner+"/"+role] = appID
				break
			}
		}
		if _, ok := result[owner+"/"+role]; !ok {
			return nil, fmt.Errorf("no shared app for role %q is installed in %s — install the app first", role, owner)
		}
	}

	return result, nil
}

// copySharedAppPEMs detects public GitHub Apps shared across orgs and copies
// their PEM secrets to the target org's naming convention. This runs before
// app setup so that handleExistingApp finds the PEM and returns credentials
// without trying to generate a new key.
// Returns a role → app-slug mapping for detected shared apps and the full
// ROLE_APP_IDS map (org/role → app_id) so callers can pass it to app setup
// without a redundant GCP API call.
func copySharedAppPEMs(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string, mintProject, mintRegion string) (map[string]string, map[string]string, error) {
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:  mintProject,
		Region:     mintRegion,
		GitHubOrgs: []string{org},
	}, gcf.NewLiveGCFClient())

	existingIDs, err := prov.GetExistingRoleAppIDs(ctx)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not read ROLE_APP_IDS: %v", err))
		return nil, nil, nil
	}
	if len(existingIDs) == 0 {
		return nil, nil, nil
	}

	installations, err := client.ListOrgInstallations(ctx, org)
	if err != nil {
		return nil, existingIDs, nil
	}

	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}

	sharedSlugs := make(map[string]string)
	for _, inst := range installations {
		appIDStr := strconv.Itoa(inst.AppID)
		for key, existingAppID := range existingIDs {
			if existingAppID != appIDStr {
				continue
			}
			parts := strings.SplitN(key, "/", 2)
			if len(parts) != 2 {
				continue
			}
			srcOrg, role := parts[0], parts[1]
			if srcOrg == org || !roleSet[role] {
				continue
			}

			sharedSlugs[role] = inst.AppSlug

			exists, _ := prov.SecretExists(ctx, org, role)
			if exists {
				continue
			}

			printer.StepStart(fmt.Sprintf("Shared app detected: %s (app %d) — copying PEM from %s", role, inst.AppID, srcOrg))
			if err := prov.CopyAgentPEM(ctx, srcOrg, org, role); err != nil {
				return nil, nil, fmt.Errorf("copying shared PEM for %s: %w", role, err)
			}
			printer.StepDone(fmt.Sprintf("Copied shared %s PEM", role))
			break
		}
	}
	return sharedSlugs, existingIDs, nil
}

// runAppSetup creates or reuses GitHub Apps for each role. When mintProject is
// non-empty, PEMs are also stored in GCP Secret Manager during app creation so
// they survive partial provisioning failures.
func runAppSetup(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string, mintProject string, publicApps bool, sharedSlugs map[string]string, appSet string, storedAppIDs map[string]string) ([]layers.AgentCredentials, error) {
	printer.Header("Setting up GitHub Apps")
	printer.Blank()

	setup := appsetup.NewSetup(client, appsetup.StdinPrompter{}, appsetup.DefaultBrowser{}, printer).
		WithPublicApps(publicApps).
		WithAppSet(appSet).
		WithStoredAppIDs(storedAppIDs)

	// Merge known slugs: config-based first, then shared app overrides.
	knownSlugs := loadKnownSlugs(ctx, client, org)
	if knownSlugs == nil {
		knownSlugs = make(map[string]string)
	}
	for role, slug := range sharedSlugs {
		knownSlugs[role] = slug
	}
	if len(knownSlugs) > 0 {
		setup = setup.WithKnownSlugs(knownSlugs)
	}

	// Build an optional Secret Manager provisioner for OIDC mint mode.
	var pemProvisioner *gcf.Provisioner
	if mintProject != "" {
		pemProvisioner = gcf.NewProvisioner(gcf.Config{
			ProjectID:  mintProject,
			GitHubOrgs: []string{org},
		}, gcf.NewLiveGCFClient())
	}

	// In OIDC mint mode, PEMs live in Secret Manager — check there.
	// Otherwise, check GitHub repo secrets.
	if pemProvisioner != nil {
		setup = setup.WithSecretExists(func(role string) (bool, error) {
			return pemProvisioner.SecretExists(ctx, org, role)
		})
	} else {
		setup = setup.WithSecretExists(func(role string) (bool, error) {
			secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(role))
			return client.RepoSecretExists(ctx, org, forge.ConfigRepoName, secretName)
		})
	}

	// In OIDC mint mode, store PEMs only in Secret Manager.
	// Otherwise, store in GitHub repo secrets.
	if pemProvisioner != nil {
		setup = setup.WithStoreSecret(func(sctx context.Context, role, pem string) error {
			return pemProvisioner.StoreAgentPEM(sctx, org, role, []byte(pem))
		})
	} else {
		setup = setup.WithStoreSecret(func(sctx context.Context, role, pem string) error {
			secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(role))
			return client.CreateRepoSecret(sctx, org, forge.ConfigRepoName, secretName, pem)
		})
	}

	var creds []layers.AgentCredentials
	for _, role := range roles {
		appCreds, err := setup.Run(ctx, org, role)
		if err != nil {
			return nil, fmt.Errorf("setting up app for role %s: %w", role, err)
		}
		creds = append(creds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{
				Role: role,
				Name: appCreds.Name,
				Slug: appCreds.Slug,
			},
			PEM:      appCreds.PEM,
			ClientID: appCreds.ClientID,
			AppID:    appCreds.AppID,
		})
	}

	if err := setup.PermissionErrors(); err != nil {
		return nil, err
	}

	printer.Blank()
	return creds, nil
}

// ensureConfigRepoExists creates the .fullsend config repo if it doesn't
// already exist. This is called before app setup so PEM secrets can be
// stored immediately after each app is created.
func ensureConfigRepoExists(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
	_, err := client.GetRepo(ctx, org, forge.ConfigRepoName)
	if err == nil {
		return nil
	}
	if !forge.IsNotFound(err) {
		return fmt.Errorf("checking for config repo: %w", err)
	}

	printer.StepStart("Creating " + forge.ConfigRepoName + " repository")
	desc := fmt.Sprintf("fullsend configuration for %s", org)
	if _, err := client.CreateRepo(ctx, org, forge.ConfigRepoName, desc, false); err != nil {
		recheck, recheckErr := client.GetRepo(ctx, org, forge.ConfigRepoName)
		if recheckErr == nil && recheck != nil {
			printer.StepInfo(forge.ConfigRepoName + " repository already exists")
			return nil
		}
		printer.StepFail("Failed to create " + forge.ConfigRepoName + " repository")
		return fmt.Errorf("creating config repo: %w", err)
	}
	printer.StepDone("Created " + forge.ConfigRepoName + " repository")
	return nil
}

// validateEnabledRepos checks that every enabled repository exists in the
// discovered (eligible) repo list. Repos filtered out by ListOrgRepos
// (private, forks, archived) will not appear in discoveredNames, so this
// catches the case where an enabled repo is private, a fork, or archived.
//
// Private repos are excluded because the default .fullsend config repo is
// public and agent workflow logs would expose private repo content.
// Forks may live outside the org's permission boundary or lack the same
// CODEOWNERS governance, and archived repos have no active development.
// See the ListOrgRepos comment in forge.Client for the full rationale.
func validateEnabledRepos(enabledRepos, discoveredNames []string) error {
	if len(enabledRepos) == 0 {
		return nil
	}
	discovered := make(map[string]bool, len(discoveredNames))
	for _, name := range discoveredNames {
		discovered[name] = true
	}
	var missing []string
	for _, name := range enabledRepos {
		if !discovered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("repos not found in %s: %s — they may be private, forks, archived, or misspelled",
			"the discovered repo list", strings.Join(missing, ", "))
	}
	return nil
}

// runInstall performs the full installation.
// If discoveredRepos is non-nil, it will be used instead of calling ListOrgRepos.
func runInstall(ctx context.Context, client forge.Client, printer *ui.Printer, org string, enabledRepos, roles []string, agentCreds []layers.AgentCredentials, inferenceProvider inference.Provider, inferenceProviderName string, vendorBinary bool, mintProvider, mintProject, mintRegion, mintSourceDir string, mintSkipDeploy bool, mintURL string, skipMintCheck bool, discoveredRepos []forge.Repository) error {
	var allRepos []forge.Repository
	var err error

	if discoveredRepos != nil {
		allRepos = discoveredRepos
		printer.Header("Using discovered repositories")
		printer.StepDone(fmt.Sprintf("Found %d repositories", len(allRepos)))
	} else {
		printer.Header("Discovering repositories")
		allRepos, err = client.ListOrgRepos(ctx, org)
		if err != nil {
			return fmt.Errorf("listing org repos: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Found %d repositories", len(allRepos)))
	}

	repoNames := repoNameList(allRepos)

	privateRepo := false
	printer.Blank()

	// When enabledRepos is nil the user chose not to modify enrollment.
	// Preserve existing enrollment from the current config.yaml so that
	// re-running install without repo selection does not unenroll everything.
	// See #861.
	if enabledRepos == nil {
		enabledRepos = loadExistingEnabledRepos(ctx, client, org)
	}

	// Validate that every enabled repository matches a discovered repo.
	if err := validateEnabledRepos(enabledRepos, repoNames); err != nil {
		return err
	}

	// Collect IDs for repos that will be enrolled.
	enrolledRepoIDs := collectEnrolledRepoIDs(allRepos, enabledRepos)

	// Build agent entries for config.
	agents := make([]config.AgentEntry, len(agentCreds))
	for i, ac := range agentCreds {
		agents[i] = ac.AgentEntry
	}

	cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, agents, inferenceProviderName)
	cfg.Dispatch.Mode = "oidc-mint"

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	var disp dispatch.Dispatcher
	if skipMintCheck {
		disp = &skipMintDispatcher{mintURL: mintURL}
	} else {
		// Build the mint infrastructure provisioner.
		agentPEMs := make(map[string][]byte)
		agentAppIDs := make(map[string]string)
		for _, ac := range agentCreds {
			if ac.AppID != 0 {
				agentAppIDs[ac.Role] = strconv.Itoa(ac.AppID)
				if ac.PEM != "" {
					agentPEMs[ac.Role] = []byte(ac.PEM)
				}
			}
		}
		if len(agentAppIDs) == 0 {
			return fmt.Errorf("OIDC mint requires at least one agent with credentials")
		}

		if mintSourceDir == "" {
			mintSourceDir = gcf.DefaultFunctionSourceDir()
		}

		deployMode := gcf.DeployAuto
		if mintSkipDeploy {
			deployMode = gcf.DeploySkip
		}

		disp = gcf.NewProvisioner(gcf.Config{
			ProjectID:         mintProject,
			Region:            mintRegion,
			GitHubOrgs:        []string{org},
			AgentPEMs:         agentPEMs,
			AgentAppIDs:       agentAppIDs,
			FunctionSourceDir: mintSourceDir,
			DeployMode:        deployMode,
			MintURL:           mintURL,
		}, gcf.NewLiveGCFClient())
	}

	stack := buildLayerStack(org, client, cfg, printer, user, privateRepo, enabledRepos, agentCreds, enrolledRepoIDs, inferenceProvider, vendorBinary, vendorFullsendBinary, disp)

	if err := runPreflight(ctx, stack, layers.OpInstall, client, printer); err != nil {
		return err
	}
	printer.Blank()

	printer.Header("Installing")
	printer.Blank()

	if err := stack.InstallAll(ctx); err != nil {
		return fmt.Errorf("installation failed: %w", err)
	}

	printer.Blank()
	printer.Summary("Installation complete", []string{
		fmt.Sprintf("Organization: %s", org),
		fmt.Sprintf("Roles: %s", strings.Join(roles, ", ")),
		fmt.Sprintf("Enabled repos: %d", len(enabledRepos)),
	})

	return nil
}

// runUninstall tears down the fullsend installation.
func runUninstall(ctx context.Context, client forge.Client, printer *ui.Printer, org, appSet string) error {
	// Try to load agent slugs from existing config. If the .fullsend repo
	// is already gone (e.g., previous partial uninstall), fall back to the
	// default naming convention so we can still guide the user to delete
	// the apps. Without this fallback, a partial uninstall leaves orphaned
	// apps that block reinstallation (PEM keys are one-shot).
	var agentSlugs []string
	var configMode string
	cfgData, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err == nil {
		if parsedCfg, parseErr := config.ParseOrgConfig(cfgData); parseErr == nil {
			for _, agent := range parsedCfg.Agents {
				agentSlugs = append(agentSlugs, agent.Slug)
			}
			configMode = parsedCfg.Dispatch.Mode
		} else {
			printer.StepWarn(fmt.Sprintf("Could not parse existing config: %v; using defaults", parseErr))
		}
	}
	if len(agentSlugs) == 0 {
		// Config unavailable — assume default app naming convention.
		for _, role := range config.DefaultAgentRoles() {
			agentSlugs = append(agentSlugs, appsetup.AppSlug(appSet, role))
		}
		if err != nil {
			printer.StepInfo("Config repo unavailable; using default app names")
		}
	}

	// Build the dispatch layer based on detected mode.
	var dispatchLayer layers.Layer
	switch configMode {
	case "oidc-mint":
		dispatchLayer = layers.NewOIDCDispatchLayer(org, client, nil, gcf.NewProvisioner(gcf.Config{}, nil), printer)
	default:
		// Config unavailable — clean both modes to ensure nothing is left behind.
		dispatchLayer = layers.NewBothModesDispatchLayer(org, client, gcf.NewProvisioner(gcf.Config{}, nil), printer)
	}

	// Build a minimal stack for uninstall.
	// Only ConfigRepoLayer matters for uninstall since other layers are no-ops.
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil, "")
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(org, client, emptyCfg, printer, false),
		layers.NewWorkflowsLayer(org, client, printer, ""),
		layers.NewSecretsLayer(org, client, nil, printer),
		layers.NewInferenceLayer(org, client, nil, printer),
		dispatchLayer,
		layers.NewEnrollmentLayer(org, client, nil, nil, printer),
	)

	if err := runPreflight(ctx, stack, layers.OpUninstall, client, printer); err != nil {
		return err
	}
	printer.Blank()

	errs := stack.UninstallAll(ctx)
	if len(errs) > 0 {
		for _, e := range errs {
			printer.StepFail(e.Error())
		}
	}

	printer.Blank()

	// Check which apps actually exist before opening browser pages.
	// GitHub App uninstallation via API (DELETE /app/installations/{id}) requires
	// JWT auth from the app's own private key, not a PAT. Since we authenticate
	// with a PAT, we open the browser to the app's advanced settings page instead.
	// The correct URL for org-scoped apps is /organizations/{org}/settings/apps/{slug}/advanced
	// (the /advanced suffix is required to see the delete button; /settings/apps/{slug}
	// alone is for user-scoped apps and will 404 for org-scoped ones).
	if len(agentSlugs) > 0 {
		// Find which slugs correspond to real installed apps.
		var existingSlugs []string
		installations, listErr := client.ListOrgInstallations(ctx, org)
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
			// Can't check — fall back to opening all of them.
			printer.StepWarn("Could not verify which apps exist; opening all")
			existingSlugs = agentSlugs
		}

		if len(existingSlugs) > 0 {
			printer.Header("App cleanup")
			printer.StepInfo("Opening browser for each app that needs to be deleted.")
			printer.StepInfo("Click 'Delete GitHub App' on each page, then return here.")
			printer.Blank()

			browser := appsetup.DefaultBrowser{}
			for _, slug := range existingSlugs {
				deleteURL := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s/advanced", org, slug)
				printer.StepStart(fmt.Sprintf("Opening %s settings...", slug))
				if err := browser.Open(ctx, deleteURL); err != nil {
					printer.StepWarn(fmt.Sprintf("Could not open browser: %v", err))
					printer.StepInfo(fmt.Sprintf("  Delete manually at: %s", deleteURL))
				} else {
					printer.StepDone(fmt.Sprintf("Opened %s", slug))
				}
			}
			printer.Blank()
		}
	}

	if len(errs) > 0 {
		printer.Summary("Uninstall completed with errors", []string{
			fmt.Sprintf("Organization: %s", org),
			fmt.Sprintf("%d errors occurred during uninstall", len(errs)),
		})
		return fmt.Errorf("uninstall completed with %d errors", len(errs))
	}

	printer.Summary("Uninstall complete", []string{
		fmt.Sprintf("Organization: %s", org),
		"Config repo deleted",
	})

	return nil
}

// runAnalyze assesses the current installation state.
func runAnalyze(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
	allRepos, err := client.ListOrgRepos(ctx, org)
	if err != nil {
		return fmt.Errorf("listing org repos: %w", err)
	}

	repoNames := repoNameList(allRepos)

	privateRepo := false

	printer.StepDone(fmt.Sprintf("Found %d repositories", len(allRepos)))
	printer.Blank()

	// Build a config for analysis using defaults.
	defaultRoles := config.DefaultAgentRoles()
	var agentCreds []layers.AgentCredentials
	for _, role := range defaultRoles {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{Role: role},
		})
	}

	cfg := config.NewOrgConfig(repoNames, nil, defaultRoles, nil, "")

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	// Detect inference provider from existing config.
	var inferenceProvider inference.Provider
	if providerName := loadExistingInferenceProvider(ctx, client, org); providerName != "" {
		inferenceProvider = vertex.NewAnalyzeOnly()
	}

	dispatcher := gcf.NewProvisioner(gcf.Config{}, nil)
	stack := buildLayerStack(org, client, cfg, printer, user, privateRepo, nil, agentCreds, nil, inferenceProvider, false, nil, dispatcher)

	if err := runPreflight(ctx, stack, layers.OpAnalyze, client, printer); err != nil {
		return err
	}
	printer.Blank()

	return printAnalysis(ctx, stack, printer)
}

// buildLayerStack creates the ordered layer stack.
func buildLayerStack(
	org string,
	client forge.Client,
	cfg *config.OrgConfig,
	printer *ui.Printer,
	user string,
	privateRepo bool,
	enabledRepos []string,
	agentCreds []layers.AgentCredentials,
	enrolledRepoIDs []int64,
	inferenceProvider inference.Provider,
	vendorBinary bool,
	vendorFn layers.VendorFunc,
	dispatcher dispatch.Dispatcher,
) *layers.Stack {
	dispatchLayer := layers.NewOIDCDispatchLayer(org, client, enrolledRepoIDs, dispatcher, printer)

	// When enabledRepos is nil the caller chose not to modify enrollment
	// (e.g. --enroll-none or the user answered "n" at the prompt). In that
	// case we must also suppress the disabled-repos list so the enrollment
	// layer becomes a no-op instead of creating unenrollment PRs for every
	// previously enrolled repo. See #861.
	var disabledRepos []string
	if enabledRepos != nil {
		disabledRepos = cfg.DisabledRepos()
	}

	return layers.NewStack(
		layers.NewConfigRepoLayer(org, client, cfg, printer, privateRepo),
		layers.NewWorkflowsLayer(org, client, printer, user),
		layers.NewVendorBinaryLayer(org, client, printer, vendorBinary, vendorFn),
		layers.NewSecretsLayer(org, client, agentCreds, printer).WithOIDCMode(),
		layers.NewInferenceLayer(org, client, inferenceProvider, printer),
		dispatchLayer,
		layers.NewEnrollmentLayer(org, client, enabledRepos, disabledRepos, printer),
	)
}

// installRequiredScopes is the set of OAuth scopes the install command
// needs. Keep in sync with the union of RequiredScopes(OpInstall) across
// all layers; TestCheckInstallScopes_SyncWithLayers asserts parity.
var installRequiredScopes = []string{"repo", "workflow", "admin:org"}

// perRepoRequiredScopes is the set of OAuth scopes needed for per-repo install.
var perRepoRequiredScopes = []string{"repo", "workflow"}

// checkInstallScopes verifies that the token has the scopes needed for
// install before starting interactive app setup. This avoids wasting
// time on browser-based app creation only to fail on missing scopes.
func checkInstallScopes(ctx context.Context, client forge.Client, printer *ui.Printer) error {
	return checkTokenScopes(ctx, client, printer, installRequiredScopes)
}

// runPreflight checks that the token has all required scopes for the
// given operation. Returns nil if all scopes are present or if scope
// introspection is unavailable (fine-grained tokens). Returns an error
// with remediation instructions if scopes are missing.
func runPreflight(ctx context.Context, stack *layers.Stack, op layers.Operation, client forge.Client, printer *ui.Printer) error {
	printer.StepStart("Checking token permissions")

	result, err := stack.Preflight(ctx, op, client)
	if err != nil {
		printer.StepFail("Could not verify token permissions")
		return fmt.Errorf("preflight check: %w", err)
	}

	if !result.OK() {
		printer.StepFail("Token is missing required scopes")
		printer.Blank()
		printer.ErrorBox("Missing token scopes", result.Error())
		return fmt.Errorf("token is missing required scopes: %s", strings.Join(result.Missing, ", "))
	}

	if result.Skipped {
		printer.StepWarn("Preflight skipped: fine-grained token detected (scopes cannot be verified)")
	} else {
		printer.StepDone("Token permissions verified")
	}
	return nil
}

// printAnalysis runs AnalyzeAll and prints reports.
func printAnalysis(ctx context.Context, stack *layers.Stack, printer *ui.Printer) error {
	reports, err := stack.AnalyzeAll(ctx)
	if err != nil {
		return fmt.Errorf("analysis failed: %w", err)
	}

	allInstalled := true
	for _, report := range reports {
		printer.Header(fmt.Sprintf("Layer: %s", report.Name))

		switch report.Status {
		case layers.StatusInstalled:
			printer.StepDone("Status: installed")
		case layers.StatusNotInstalled:
			printer.StepFail("Status: not installed")
			allInstalled = false
		case layers.StatusDegraded:
			printer.StepWarn("Status: degraded")
			allInstalled = false
		default:
			printer.StepInfo("Status: unknown")
			allInstalled = false
		}

		for _, detail := range report.Details {
			printer.StepInfo(detail)
		}
		for _, item := range report.WouldInstall {
			printer.StepInfo("would install: " + item)
		}
		for _, item := range report.WouldFix {
			printer.StepInfo("would fix: " + item)
		}
		printer.Blank()
	}

	if allInstalled {
		printer.Summary("Assessment", []string{"All layers are installed and healthy."})
	} else {
		printer.Summary("Assessment", []string{
			"Some layers need attention.",
			"Run 'fullsend admin install <org>' to install or repair.",
		})
	}

	return nil
}

// loadExistingInferenceProvider reads the inference provider name from
// an existing config.yaml in .fullsend, if available. This prevents
// re-installs without --inference-project from silently erasing the inference section.
func loadExistingInferenceProvider(ctx context.Context, client forge.Client, org string) string {
	data, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		return ""
	}
	cfg, err := config.ParseOrgConfig(data)
	if err != nil {
		return ""
	}
	return cfg.Inference.Provider
}

// loadExistingEnabledRepos reads the enabled repos list from an existing
// config.yaml in .fullsend, if available. This prevents re-installs
// without repo selection from silently unenrolling all repos. See #861.
func loadExistingEnabledRepos(ctx context.Context, client forge.Client, org string) []string {
	data, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		return nil
	}
	cfg, err := config.ParseOrgConfig(data)
	if err != nil {
		return nil
	}
	return cfg.EnabledRepos()
}
// loadKnownSlugs tries to read agent slugs from an existing config.
func loadKnownSlugs(ctx context.Context, client forge.Client, org string) map[string]string {
	data, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		return nil
	}
	cfg, err := config.ParseOrgConfig(data)
	if err != nil {
		return nil
	}
	return cfg.AgentSlugs()
}

// collectEnrolledRepoIDs returns the IDs of repos whose names appear in
// the enabledRepos list.
func collectEnrolledRepoIDs(allRepos []forge.Repository, enabledRepos []string) []int64 {
	enabled := make(map[string]bool, len(enabledRepos))
	for _, name := range enabledRepos {
		enabled[name] = true
	}
	var ids []int64
	for _, r := range allRepos {
		if enabled[r.Name] {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// promptEnrollment asks the user whether to enroll all repositories or none.
// Returns true if the user chooses to enroll all, false if none.
// Accepts an io.Reader to enable testing without os.Stdin.
func promptEnrollment(printer *ui.Printer, in io.Reader) (bool, error) {
	printer.Header("Repository Enrollment")
	printer.Blank()
	printer.StepInfo("Choose repository enrollment:")
	printer.StepInfo("  [a] Enroll all repositories (excluding .fullsend)")
	printer.StepInfo("  [n] Enroll no repositories (configure later with 'fullsend admin enable repos')")
	printer.Blank()

	reader := bufio.NewReader(in)
	for {
		printer.StepInfo("Enter choice (a/n): ")
		choice, err := reader.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("reading enrollment choice: %w", err)
		}
		choice = strings.TrimSpace(strings.ToLower(choice))

		switch choice {
		case "a", "all":
			return true, nil
		case "n", "none":
			return false, nil
		default:
			printer.StepWarn(fmt.Sprintf("Invalid choice: %q (expected 'a' or 'n')", choice))
		}
	}
}

func newEnableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable fullsend features",
		Long:  "Commands for enabling fullsend features such as repository enrollment.",
	}
	cmd.AddCommand(newEnableReposCmd())
	return cmd
}

func newDisableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Disable fullsend features",
		Long:  "Commands for disabling fullsend features such as repository enrollment.",
	}
	cmd.AddCommand(newDisableReposCmd())
	return cmd
}

// reposRunFunc is the signature for repo enable/disable operations.
type reposRunFunc func(ctx context.Context, client forge.Client, printer *ui.Printer, org string, repos []string, all bool, yolo bool) error

// newReposSubcommand creates a repos enable or disable subcommand with shared setup logic.
// If withYolo is true, the --yolo flag is added to skip confirmation prompts.
func newReposSubcommand(use, short, long, allFlagHelp string, runFn reposRunFunc, withYolo bool) *cobra.Command {
	var all bool
	var yolo bool

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}

			// When --all is set, ignore positional repo arguments.
			// Otherwise, require at least one repo name.
			var repos []string
			if all {
				// Ignore positional args; repos will be discovered from org
				repos = nil
			} else {
				hasRepos := len(args) > 1
				if !hasRepos {
					return fmt.Errorf("must specify repository names or use --all flag")
				}
				repos = args[1:]
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			return runFn(ctx, client, printer, org, repos, all, yolo)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, allFlagHelp)
	if withYolo {
		cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")
	}

	return cmd
}

func newEnableReposCmd() *cobra.Command {
	return newReposSubcommand(
		"repos <org> [repo...]",
		"Enable repositories for fullsend enrollment",
		"Enables the specified repositories for fullsend enrollment by updating config.yaml in the .fullsend repository. Use --all to enable all repositories (excluding .fullsend).",
		"enable all repositories (excluding .fullsend)",
		runEnableRepos,
		false, // no confirmation prompt, so no --yolo flag
	)
}

func newDisableReposCmd() *cobra.Command {
	return newReposSubcommand(
		"repos <org> [repo...]",
		"Disable repositories from fullsend enrollment",
		"Disables the specified repositories from fullsend enrollment by updating config.yaml in the .fullsend repository. Use --all to disable all repositories.",
		"disable all repositories",
		runDisableRepos,
		true, // has confirmation prompt for --all, so include --yolo flag
	)
}

// runEnableRepos enables the specified repositories for fullsend enrollment.
// The yolo parameter is accepted for signature compatibility with reposRunFunc but is unused
// since enable has no destructive operations that require confirmation.
func runEnableRepos(ctx context.Context, client forge.Client, printer *ui.Printer, org string, repos []string, all bool, yolo bool) error {
	printer.Banner()
	printer.Blank()
	printer.Header("Enabling repositories for " + org)
	printer.Blank()

	// Load current config.
	cfg, err := loadRepoConfig(ctx, client, printer, org)
	if err != nil {
		return err
	}

	// Determine which repos to enable.
	var reposToEnable []string
	if all {
		// Get all org repos by calling ListOrgRepos.
		// Note: disable --all iterates cfg.Repos instead of calling ListOrgRepos.
		// This asymmetry is intentional: enable --all discovers all current org repos,
		// while disable --all operates on previously configured repos (which may have
		// been deleted from the org but still need unenrollment PRs for cleanup).
		printer.StepStart("Discovering all organization repositories")
		allRepos, err := client.ListOrgRepos(ctx, org)
		if err != nil {
			printer.StepFail("Failed to list organization repositories")
			printer.StepInfo("Hint: verify your token has 'repo' scope with: gh auth refresh -s repo")
			return fmt.Errorf("listing org repos: %w", err)
		}
		for _, r := range allRepos {
			if r.Name != forge.ConfigRepoName {
				reposToEnable = append(reposToEnable, r.Name)
			}
		}
		sort.Strings(reposToEnable)
		printer.StepDone(fmt.Sprintf("Found %d repositories to enable", len(reposToEnable)))
	} else {
		// Validate provided repo names against org repos.
		// Fetch org repos once and validate against the list instead of making
		// one API call per repo (O(n) → O(1) API calls).
		printer.StepStart("Validating repository names")

		allOrgRepos, err := client.ListOrgRepos(ctx, org)
		if err != nil {
			printer.StepFail("Failed to list organization repositories")
			printer.StepInfo("Hint: verify your token has 'repo' scope with: gh auth refresh -s repo")
			return fmt.Errorf("listing org repos: %w", err)
		}

		// Build a set of valid repo names for O(1) lookup.
		validRepos := make(map[string]bool, len(allOrgRepos))
		for _, r := range allOrgRepos {
			validRepos[r.Name] = true
		}

		// Validate each requested repo.
		for _, repo := range repos {
			if repo == forge.ConfigRepoName {
				printer.StepFail("Cannot enable .fullsend repository")
				return fmt.Errorf("cannot enable .fullsend repository itself")
			}
			if !validRepos[repo] {
				printer.StepFail(fmt.Sprintf("Repository %s not found", repo))
				return fmt.Errorf("repository %s not found in %s", repo, org)
			}
		}
		reposToEnable = repos
		printer.StepDone("Repository names validated")
	}

	if len(reposToEnable) == 0 {
		printer.StepInfo("No repositories to enable")
		return nil
	}

	// Update config.
	printer.StepStart("Updating config.yaml")
	changed := 0
	for _, repo := range reposToEnable {
		rc, exists := cfg.Repos[repo]
		if !exists {
			// Add new repo entry.
			cfg.Repos[repo] = config.RepoConfig{Enabled: true}
			changed++
		} else if !rc.Enabled {
			// Update existing entry.
			rc.Enabled = true
			cfg.Repos[repo] = rc
			changed++
		}
	}

	if changed == 0 {
		printer.StepInfo("All specified repositories are already enabled")
		return nil
	}
	printer.StepDone(fmt.Sprintf("Updated %d repositories in config.yaml", changed))

	// Save updated config.
	commitMsg := fmt.Sprintf("chore: enable %d repositories for fullsend enrollment", changed)
	if err := saveRepoConfig(ctx, client, printer, org, cfg, commitMsg); err != nil {
		return err
	}

	printer.Blank()
	printer.Summary("Repositories enabled", []string{
		fmt.Sprintf("Organization: %s", org),
		fmt.Sprintf("Enabled: %d repositories", changed),
		"The repo-maintenance workflow will create enrollment PRs",
	})

	return nil
}

// runDisableRepos disables the specified repositories from fullsend enrollment.
func runDisableRepos(ctx context.Context, client forge.Client, printer *ui.Printer, org string, repos []string, all bool, yolo bool) error {
	printer.Banner()
	printer.Blank()
	printer.Header("Disabling repositories for " + org)
	printer.Blank()

	// Load current config.
	cfg, err := loadRepoConfig(ctx, client, printer, org)
	if err != nil {
		return err
	}

	// Determine which repos to disable.
	var reposToDisable []string
	if all {
		// Disable all repos currently in config.
		printer.StepStart("Collecting all configured repositories")
		for repo := range cfg.Repos {
			reposToDisable = append(reposToDisable, repo)
		}
		sort.Strings(reposToDisable)
		printer.StepDone(fmt.Sprintf("Found %d repositories to disable", len(reposToDisable)))

		// Prompt for confirmation when disabling all repos.
		if !yolo && len(reposToDisable) > 0 {
			printer.Blank()
			printer.StepWarn(fmt.Sprintf("This will disable all %d repositories in %s.", len(reposToDisable), org))
			printer.StepInfo(fmt.Sprintf("Type the organization name (%s) to confirm:", org))

			// Check if stdin is a terminal before prompting for input.
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("stdin is not a terminal; use --yolo to skip confirmation in non-interactive environments")
			}

			var confirmation string
			if _, err := fmt.Scanln(&confirmation); err != nil {
				return fmt.Errorf("reading confirmation: %w", err)
			}
			if confirmation != org {
				return fmt.Errorf("confirmation did not match; aborting disable")
			}
			printer.Blank()
		}
	} else {
		// Validate provided repo names against config (not GitHub).
		// Unlike enable, disable is cleanup and must handle repos deleted from GitHub.
		printer.StepStart("Validating repository names")
		for _, repo := range repos {
			if repo == forge.ConfigRepoName {
				printer.StepFail("Cannot disable .fullsend repository")
				return fmt.Errorf("cannot disable .fullsend repository itself")
			}
			// Check if repo exists in config (don't require GitHub existence for cleanup).
			if _, exists := cfg.Repos[repo]; !exists {
				printer.StepWarn(fmt.Sprintf("Repository %s not in config (skipping)", repo))
				continue
			}
			reposToDisable = append(reposToDisable, repo)
		}
		printer.StepDone("Repository names validated")
	}

	if len(reposToDisable) == 0 {
		printer.StepInfo("No repositories to disable")
		return nil
	}

	// Update config.
	printer.StepStart("Updating config.yaml")
	changed := 0
	for _, repo := range reposToDisable {
		rc, exists := cfg.Repos[repo]
		if exists && rc.Enabled {
			// Update existing entry to disabled.
			rc.Enabled = false
			cfg.Repos[repo] = rc
			changed++
		}
	}

	if changed == 0 {
		printer.StepInfo("All specified repositories are already disabled")
		return nil
	}
	printer.StepDone(fmt.Sprintf("Updated %d repositories in config.yaml", changed))

	// Save updated config.
	commitMsg := fmt.Sprintf("chore: disable %d repositories from fullsend enrollment", changed)
	if err := saveRepoConfig(ctx, client, printer, org, cfg, commitMsg); err != nil {
		return err
	}

	printer.Blank()
	printer.Summary("Repositories disabled", []string{
		fmt.Sprintf("Organization: %s", org),
		fmt.Sprintf("Disabled: %d repositories", changed),
		"The repo-maintenance workflow will create unenrollment PRs",
	})

	return nil
}

// loadRepoConfig verifies the .fullsend repository exists and loads config.yaml.
//
// Note: The read-modify-write pattern used by enable/disable (loadRepoConfig →
// modify → saveRepoConfig) has no optimistic concurrency control. Concurrent
// admin CLI invocations could race, with the last write winning. This is
// acceptable for an admin CLI where concurrent usage is rare, and the state
// is recoverable (just re-run the command). Production systems would use
// conditional writes (e.g., if-match headers with ETags).
func loadRepoConfig(ctx context.Context, client forge.Client, printer *ui.Printer, org string) (*config.OrgConfig, error) {
	// Verify .fullsend repository exists.
	printer.StepStart("Checking .fullsend repository")
	_, err := client.GetRepo(ctx, org, forge.ConfigRepoName)
	if err != nil {
		if forge.IsNotFound(err) {
			printer.StepFail(".fullsend repository not found")
			return nil, fmt.Errorf(".fullsend repository not found: run 'fullsend admin install %s' first", org)
		}
		printer.StepFail("Failed to check .fullsend repository")
		printer.StepInfo("Hint: verify your token has 'repo' scope with: gh auth refresh -s repo")
		return nil, fmt.Errorf("checking .fullsend repository: %w", err)
	}
	printer.StepDone(".fullsend repository exists")

	// Get current config.yaml.
	printer.StepStart("Reading config.yaml")
	configData, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		printer.StepFail("Failed to read config.yaml")
		printer.StepInfo("Hint: verify your token has 'repo' scope with: gh auth refresh -s repo")
		return nil, fmt.Errorf("reading config.yaml: %w", err)
	}

	cfg, err := config.ParseOrgConfig(configData)
	if err != nil {
		printer.StepFail("Failed to parse config.yaml")
		return nil, fmt.Errorf("parsing config.yaml: %w", err)
	}
	printer.StepDone("Read config.yaml")

	return cfg, nil
}

// saveRepoConfig marshals and commits the updated config, then triggers the repo-maintenance workflow.
func saveRepoConfig(ctx context.Context, client forge.Client, printer *ui.Printer, org string, cfg *config.OrgConfig, commitMsg string) error {
	// Marshal updated config.
	updatedConfigData, err := cfg.Marshal()
	if err != nil {
		printer.StepFail("Failed to marshal config.yaml")
		return fmt.Errorf("marshaling config.yaml: %w", err)
	}

	// Commit and push changes.
	printer.StepStart("Committing changes to .fullsend")
	if err := client.CreateOrUpdateFile(ctx, org, forge.ConfigRepoName, "config.yaml", commitMsg, updatedConfigData); err != nil {
		printer.StepFail("Failed to commit changes")
		printer.StepInfo("Hint: verify your token has 'repo' scope with: gh auth refresh -s repo")
		return fmt.Errorf("committing config.yaml: %w", err)
	}
	printer.StepDone("Changes committed to .fullsend")

	// Trigger repo-maintenance workflow.
	printer.StepStart("Triggering repo-maintenance workflow")
	if err := client.DispatchWorkflow(ctx, org, forge.ConfigRepoName, "repo-maintenance.yml", "main", nil); err != nil {
		printer.StepWarn(fmt.Sprintf("Failed to trigger repo-maintenance: %v", err))
		printer.StepInfo("Hint: verify your token has 'workflow' scope with: gh auth refresh -s workflow")
		printer.StepInfo("Changes committed successfully, but you may need to manually trigger the workflow")
	} else {
		printer.StepDone("Triggered repo-maintenance workflow")
	}

	return nil
}

// checkPerRepoScopes verifies the token has sufficient permissions for per-repo install.
func checkPerRepoScopes(ctx context.Context, client forge.Client, printer *ui.Printer) error {
	return checkTokenScopes(ctx, client, printer, perRepoRequiredScopes)
}

// checkTokenScopes verifies the token has all required OAuth scopes.
func checkTokenScopes(ctx context.Context, client forge.Client, printer *ui.Printer, required []string) error {
	printer.StepStart("Checking token permissions")

	granted, err := client.GetTokenScopes(ctx)
	if err != nil {
		printer.StepFail("Could not verify token permissions")
		return fmt.Errorf("checking token scopes: %w", err)
	}

	if granted == nil {
		printer.StepWarn("Preflight skipped: fine-grained token detected (scopes cannot be verified)")
		return nil
	}

	grantedSet := make(map[string]bool, len(granted))
	for _, s := range granted {
		grantedSet[s] = true
	}

	var missing []string
	for _, scope := range required {
		if !grantedSet[scope] {
			missing = append(missing, scope)
		}
	}

	if len(missing) > 0 {
		printer.StepFail("Token is missing required scopes")
		printer.Blank()
		result := &layers.PreflightResult{
			Required: required,
			Granted:  granted,
			Missing:  missing,
		}
		printer.ErrorBox("Missing token scopes", result.Error())
		return fmt.Errorf("token is missing required scopes: %s", strings.Join(missing, ", "))
	}

	printer.StepDone("Token permissions verified")
	return nil
}

// Helper functions.

func sortedStringMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func repoNameList(repos []forge.Repository) []string {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return names
}
