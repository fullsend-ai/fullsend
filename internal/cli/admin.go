package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/maputil"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// DefaultMintURL is the hosted public mint URL used when --mint-url is not
// explicitly provided. Users who self-host a mint can override this via
// the --mint-url flag.
const DefaultMintURL = "https://mint.fullsend.sh"

// adminMintDiscovery holds the results of a mint infrastructure discovery call.
type adminMintDiscovery struct {
	URL             string
	RoleAppIDs      map[string]string
	PerRepoWIFRepos []string
}

// adminWIFProvisioner abstracts WIF and mint discovery for admin install.
type adminWIFProvisioner interface {
	DiscoverMint(ctx context.Context) (*adminMintDiscovery, error)
	ProvisionWIF(ctx context.Context) (string, error)
	RegisterPerRepoWIF(ctx context.Context, repo string) error
	DeletePerRepoWIF(ctx context.Context, repo string) error
	DeleteWIFProvider(ctx context.Context, repo string) error
}

// errMintNotFound indicates the mint function does not exist.
var errMintNotFound = errors.New("mint function not found")

func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Manage fullsend installation for a repository",
		Long:  "Administrative commands for installing fullsend in a GitHub repository and managing cross-org mint authorization.",
	}
	cmd.AddCommand(newInstallCmd())
	cmd.AddCommand(newForeignCmd())
	return cmd
}

// validateOrgName checks that org is a valid GitHub organization name.
func validateOrgName(org string) error {
	if org == "" {
		return fmt.Errorf("organization name cannot be empty")
	}
	if len(org) > 39 {
		return fmt.Errorf("organization name too long (max 39 characters)")
	}
	if strings.HasPrefix(org, "-") || strings.HasSuffix(org, "-") {
		return fmt.Errorf("organization name cannot start or end with a hyphen")
	}
	if strings.Contains(org, "--") {
		return fmt.Errorf("organization name cannot contain consecutive hyphens")
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
// (alphanumeric, hyphens, dots, and underscores). Dot-prefixed repos such as
// .fullsend (config repo convention) are allowed.
var githubRepoPattern = regexp.MustCompile(`^(?:\.[a-zA-Z][a-zA-Z0-9._-]*|[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?)$`)

// errOrgTargetRemoved is returned when a command receives an organization
// name where an owner/repo target is required. Per-org installation was
// removed (ADR 0044); per-repo is the only supported installation model.
func errOrgTargetRemoved(command, target string) error {
	return fmt.Errorf("%s requires an owner/repo target, got %q: per-org installation has been removed; install each repository with '%s <owner/repo>'", command, target, command)
}

type perRepoInstallConfig struct {
	RepoFullName         string
	Agents               string
	MintURL              string
	InferenceRegion      string
	InferenceProject     string
	InferenceWIFProvider string
	MintProject          string
	MintRegion           string
	DryRun               bool
	SkipAppSetup         bool
	PublicApps           bool
	MintProvider         string
	MintSourceDir        string
	MintSkipDeploy       bool
	SkipMintCheck        bool
	AppSet               string
	// AppSetExplicit records whether --app-set was passed explicitly,
	// as opposed to carrying its flag default. Only an explicit value
	// repairs drift; otherwise a rerun preserves whatever app set is
	// already on the repo, falling back to AppSet only when none exists.
	AppSetExplicit bool
	Vendor         bool
	FullsendBinary string
	FullsendSource string
	Direct         bool
	// Runtime is the --runtime value when the flag was given; empty keeps
	// the per-repo config's code default.
	Runtime string

	// Testing overrides — when non-nil, used instead of resolving from
	// the environment. Not set by CLI flag parsing.
	testClient         forge.Client
	testPrinter        *ui.Printer
	testWIFProvisioner adminWIFProvisioner
}

func validateWIFProvider(raw string) error {
	return validateWIFProviderFlag("--inference-wif-provider", raw)
}

// validateWIFProviderFlag validates a WIF provider resource name and
// names flag in the error so each command reports its own flag.
func validateWIFProviderFlag(flag, raw string) error {
	if !repos.WIFProviderPattern.MatchString(raw) {
		return fmt.Errorf(
			"%s must be a full WIF provider resource name "+
				"(projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}), got %q",
			flag, raw,
		)
	}
	return nil
}

// IsHostedMintURL reports whether raw is the hosted community mint URL
// (mint.fullsend.sh). internal/e2etest duplicates this check locally so it
// does not import internal/cli (which pulls the nested mintcore module).
func IsHostedMintURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "mint.fullsend.sh")
}

func validateMintURL(raw string) error {
	if err := validateMintURLHTTPS(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "mint.fullsend.sh") ||
		strings.HasSuffix(host, ".run.app") ||
		strings.HasSuffix(host, ".cloudfunctions.net") {
		return nil
	}
	return fmt.Errorf("--mint-url must be mint.fullsend.sh or a Cloud Run URL (.run.app or .cloudfunctions.net), got host %q", host)
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
			if !mintcore.RolePattern.MatchString(trimmed) {
				return nil, fmt.Errorf("invalid role name %q: must match %s", trimmed, mintcore.RolePattern.String())
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
	var vendor bool
	var fullsendBinary string
	var fullsendSource string
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
	var runtimeName string
	var direct bool
	var mintURL string

	cmd := &cobra.Command{
		Use:   "install <owner/repo>",
		Short: "Install fullsend in a repository",
		Long: `Sets up the fullsend agentic development pipeline for a single repository.

The argument is owner/repo (e.g. "acme/widget"). The repository is
bootstrapped with the shim workflow and .fullsend/ configuration
directory. No config repo or cross-repo dispatch needed.

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
			arg := args[0]
			if !strings.Contains(arg, "/") {
				return errOrgTargetRemoved("fullsend admin install", arg)
			}
			if err := appsetup.ValidateAppSet(appSet); err != nil {
				return fmt.Errorf("invalid --app-set: %w", err)
			}
			applyDeprecatedVendorBinaryFlag(cmd, &vendor)
			if err := validateVendorFlags(vendor, fullsendBinary, fullsendSource); err != nil {
				return err
			}

			perRepoMintProject := mintProject
			if perRepoMintProject == "" {
				perRepoMintProject = inferenceProject
			}
			perRepoRuntime := ""
			if cmd.Flags().Changed("runtime") {
				if !slices.Contains(config.ValidRuntimes(), runtimeName) {
					return fmt.Errorf("invalid --runtime %q: must be one of %s", runtimeName, strings.Join(config.ValidRuntimes(), ", "))
				}
				perRepoRuntime = runtimeName
			}
			return runPerRepoInstall(cmd.Context(), perRepoInstallConfig{
				RepoFullName:         arg,
				Runtime:              perRepoRuntime,
				Agents:               agents,
				MintURL:              mintURL,
				InferenceRegion:      inferenceRegion,
				InferenceProject:     inferenceProject,
				InferenceWIFProvider: inferenceWIFProvider,
				MintProject:          perRepoMintProject,
				MintRegion:           mintRegion,
				DryRun:               dryRun,
				SkipAppSetup:         skipAppSetup,
				PublicApps:           publicApps,
				MintProvider:         mintProvider,
				MintSourceDir:        mintSourceDir,
				MintSkipDeploy:       mintSkipDeploy,
				SkipMintCheck:        skipMintCheck,
				AppSet:               appSet,
				AppSetExplicit:       cmd.Flags().Changed("app-set"),
				Vendor:               vendor,
				FullsendBinary:       fullsendBinary,
				FullsendSource:       fullsendSource,
				Direct:               direct,
			})
		},
	}

	cmd.Flags().StringVar(&agents, "agents", strings.Join(config.PerRepoDefaultRoles(), ","), "comma-separated agent roles")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&skipAppSetup, "skip-app-setup", false, "skip GitHub App creation/setup")
	addVendorFlags(cmd, &vendor, &fullsendBinary, &fullsendSource)
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
	cmd.Flags().StringVar(&appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for GitHub Apps (e.g., myorg creates myorg-fullsend, myorg-coder)")
	cmd.Flags().StringVar(&runtimeName, "runtime", "claude", "agent runtime for fullsend run (claude, pi or dummy; dummy is for behaviour test orgs only)")
	cmd.Flags().StringVar(&mintURL, "mint-url", DefaultMintURL, "token mint URL for OIDC token exchange (default: hosted public mint)")
	cmd.Flags().BoolVar(&direct, "direct", false, "push scaffold files directly to the default branch instead of creating a PR")

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
	vendor := c.Vendor
	fullsendBinary := c.FullsendBinary
	fullsendSource := c.FullsendSource

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

	var client forge.Client
	var printer *ui.Printer
	if c.testClient != nil {
		client = c.testClient
		if c.testPrinter == nil {
			return fmt.Errorf("testPrinter must be set when testClient is set")
		}
		printer = c.testPrinter
	} else {
		ghClient, tokenErr := newAuthenticatedGitHubClient("", "")
		if tokenErr != nil {
			return tokenErr
		}
		client = ghClient
		printer = ui.New(os.Stdout)
	}

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Installing per-repo fullsend for " + repoFullName)
	printer.Blank()

	if inferenceWIFProvider != "" {
		printer.StepWarn("Using provided WIF provider value — skipping inference provider auto-provisioning")
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()

	needsWIFProvision := inferenceWIFProvider == ""

	mintVal, mintExists, mintErr := client.GetRepoVariable(ctx, owner, repo, "FULLSEND_MINT_URL")
	if mintErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not check existing installation: %v", mintErr))
	}
	alreadyInstalled := mintExists && mintVal != ""
	if alreadyInstalled {
		printer.StepInfo(fmt.Sprintf("%s/%s is per-repo mode, updating installation", owner, repo))
	} else {
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
		}, gcf.NewLiveGCFClient(mintProject))

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
				appID, ok := roleAppIDs[role]
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
				printer.StepInfo(fmt.Sprintf("  Would use shared ROLE_APP_IDS for roles: %s", strings.Join(roles, ",")))
			}
		}
		printer.Blank()
		if needsWIFProvision {
			printer.StepInfo("Would provision WIF infrastructure in GCP project " + inferenceProject)
			printer.StepInfo("  Service account: " + gcf.MintServiceAccountEmail(inferenceProject))
			printer.StepInfo("  WIF pool: " + gcf.DefaultInferencePool)
			printer.StepInfo(fmt.Sprintf("  WIF provider: %s", mintcore.BuildRepoProviderID(owner, repo)))
			printer.StepInfo(fmt.Sprintf("  Repo restriction: %s/%s", owner, repo))
			printer.Blank()
		}
		// BuildScaffoldFiles only reads Owner, Repo, Roles, Runtime,
		// VendorBinary, UpstreamRef, UpstreamTag. Extra fields are included to stay aligned
		// with the non-dry-run installCfg; Skip* flags are omitted because
		// they control Install() flow, not scaffold file generation.
		dryRunFiles, dryRunErr := repos.BuildScaffoldFiles(repos.InstallConfig{
			Owner:            owner,
			Repo:             repo,
			Forge:            repos.ForgeGitHub,
			Roles:            roles,
			Runtime:          c.Runtime,
			MintURL:          mintDisplay,
			InferenceProject: inferenceProject,
			InferenceRegion:  inferenceRegion,
			UpstreamRef:      upstreamRef,
			UpstreamTag:      upstreamTag,
			WIFProvider:      inferenceWIFProvider,
			VendorBinary:     vendor,
			Direct:           c.Direct,
		})
		if dryRunErr != nil {
			return fmt.Errorf("generating scaffold files for dry run: %w", dryRunErr)
		}
		for _, f := range dryRunFiles {
			printer.StepDone(fmt.Sprintf("Would write: %s (%d bytes)", f.Path, len(f.Content)))
		}
		printer.Blank()
		printer.StepInfo("Would set repository variables:")
		dryRunVars := map[string]string{
			"FULLSEND_MINT_URL":   mintDisplay,
			"FULLSEND_GCP_REGION": inferenceRegion,
		}
		for _, name := range maputil.SortedKeys(dryRunVars) {
			printer.StepInfo(fmt.Sprintf("  %s = %s", name, dryRunVars[name]))
		}
		secretNames := []string{"FULLSEND_GCP_PROJECT_ID", "FULLSEND_GCP_WIF_PROVIDER"}
		printer.StepInfo(fmt.Sprintf("Would set %d repository secrets:", len(secretNames)))
		for _, name := range secretNames {
			printer.StepInfo(fmt.Sprintf("  %s", name))
		}
		if vendor {
			printer.Blank()
			printer.StepInfo(vendorDryRunMessage(fullsendBinary, fullsendSource, layers.VendoredBinaryPathPerRepo))
		} else {
			printer.Blank()
			printer.StepInfo(fmt.Sprintf("Would remove stale vendored assets at %s (if present)", layers.VendoredBinaryPathPerRepo))
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
		// Ensure the mint service account exists before storing PEM
		// secrets — StoreAgentPEM grants the SA access to each secret,
		// which fails if the SA hasn't been created yet.
		if mintProject != "" {
			prov := gcf.NewProvisioner(gcf.Config{ProjectID: mintProject}, gcf.NewLiveGCFClient(mintProject))
			if err := prov.EnsureMintServiceAccount(ctx); err != nil {
				return fmt.Errorf("ensuring mint service account: %w", err)
			}
		}

		var sharedSlugs map[string]string
		if mintProject != "" {
			slugs, storedIDs, slugErr := detectSharedApps(ctx, client, printer, owner, roles, mintProject, mintRegion)
			if slugErr != nil {
				return slugErr
			}
			sharedSlugs = slugs
			if existingIDs == nil {
				existingIDs = storedIDs
			}
		}

		creds, credErr := runAppSetup(ctx, client, printer, owner, roles, mintProject, mintURL, publicApps, sharedSlugs, c.AppSet, existingIDs)
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
		}, gcf.NewLiveGCFClient(mintProject))

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
		}, gcf.NewLiveGCFClient(mintProject))

		if _, err := mintProvisioner.Provision(ctx); err != nil {
			printer.StepFail("Mint provisioning failed")
			return fmt.Errorf("provisioning mint: %w", err)
		}
		trafficEnv, envErr := mintProvisioner.GetServiceTrafficEnvVars(ctx)
		printer.StepDone(mintValidationMessage(trafficEnv, envErr))
	}

	// WIF provisioning — admin.go handles GCP operations directly before
	// delegating forge-side work to repos.Install.
	if needsWIFProvision && inferenceWIFProvider == "" {
		var wifProvisioner adminWIFProvisioner
		if c.testWIFProvisioner != nil {
			wifProvisioner = c.testWIFProvisioner
		} else {
			wifProvisioner = &gcfProvisionerAdapter{
				provisioner: gcf.NewProvisioner(gcf.Config{
					ProjectID:   inferenceProject,
					GitHubOrgs:  []string{owner},
					Repo:        owner + "/" + repo,
					WIFPoolName: gcf.DefaultInferencePool,
				}, gcf.NewLiveGCFClient(inferenceProject)),
			}
		}

		printer.StepStart("Provisioning WIF infrastructure")
		var err error
		inferenceWIFProvider, err = wifProvisioner.ProvisionWIF(ctx)
		if err != nil {
			printer.StepFail("WIF provisioning failed")
			return fmt.Errorf("provisioning WIF: %w", err)
		}
		printer.StepDone("WIF infrastructure ready")
		printer.StepInfo("IAM policy changes may take up to 7 minutes to propagate")
		printer.StepInfo("Agent workflows that authenticate via WIF may fail until propagation completes")
	}

	// Scaffold commit function wrapping layers.CommitScaffoldFiles, which
	// provides retry on non-fast-forward errors, branch-protection fallback
	// to PR delivery, and fork-based PR support for non-owner users.
	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool, _ bool) error {
		targetRepo, repoErr := client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			if gh.IsPATForbiddenError(repoErr) {
				return handlePATForbidden(printer, owner, repo, repoErr)
			}
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		meta := repos.BuildScaffoldPRMetadata(ctx, client, owner, repo, upstreamTag,
			repos.ScaffoldMetadataOpts{GuardInstalled: &alreadyInstalled})
		if direct {
			printer.StepStart(fmt.Sprintf("Committing scaffold files to %s/%s (%s branch)",
				owner, repo, targetRepo.DefaultBranch))
		} else {
			printer.StepStart(fmt.Sprintf("Creating scaffold PR for %s/%s (target: %s)",
				owner, repo, targetRepo.DefaultBranch))
		}
		_, err := layers.CommitScaffoldFiles(ctx, client, printer, owner, repo,
			targetRepo.DefaultBranch, meta, files, direct, os.Stdin)
		return err
	}

	// Determine the effective app set to persist as FULLSEND_APP_SET. An
	// explicit --app-set repairs drift to that value; otherwise preserve
	// any value already on the repo so a rerun does not silently
	// overwrite a custom app set with the flag default, falling back to
	// that default only when the variable is genuinely absent or the
	// read fails.
	effectiveAppSet := c.AppSet
	if !c.AppSetExplicit {
		existingAppSet, _, appSetErr := client.GetRepoVariable(ctx, owner, repo, forge.VarAppSet)
		if appSetErr != nil {
			existingAppSet = ""
		}
		// existingAppSet comes straight from the repo variable, not from
		// the validated --app-set flag. Reject a malformed value here,
		// before it is preserved and used to build a GitHub App slug via
		// resolveReviewAppClientID below, instead of trusting it unchecked.
		if existingAppSet != "" && appsetup.ValidateAppSet(existingAppSet) != nil {
			existingAppSet = ""
		}
		effectiveAppSet = appsetup.ResolvePersistedAppSet("", existingAppSet)
	}

	// Resolve review app client ID for provenance validation.
	reviewAppClientID := resolveReviewAppClientID(ctx, client, effectiveAppSet)

	installCfg := repos.InstallConfig{
		Owner:                 owner,
		Repo:                  repo,
		Forge:                 repos.ForgeGitHub,
		Roles:                 roles,
		Runtime:               c.Runtime,
		MintURL:               mintURL,
		InferenceProject:      inferenceProject,
		InferenceRegion:       inferenceRegion,
		UpstreamRef:           upstreamRef,
		UpstreamTag:           upstreamTag,
		SkipAppSetup:          true,
		WIFProvider:           inferenceWIFProvider,
		ReviewAppClientID:     reviewAppClientID,
		AppSet:                effectiveAppSet,
		VendorBinary:          vendor,
		Direct:                c.Direct,
		SkipScaffoldAndConfig: vendor,
	}

	progressFn := func(_ string, phase, msg string) {
		switch phase {
		case "scaffold":
			if strings.Contains(msg, "Committing") || strings.Contains(msg, "Generating") {
				printer.StepStart(msg)
			} else {
				printer.StepDone(msg)
			}
		case "vars":
			if strings.Contains(msg, "Configuring") {
				printer.StepStart(msg)
			} else {
				printer.StepDone(msg)
			}
		case "secrets":
			if strings.Contains(msg, "Configuring") {
				printer.StepStart(msg)
			} else {
				printer.StepDone(msg)
			}
		}
	}

	installResult, installErr := repos.Install(ctx, installCfg, client, scaffoldCommitFn, progressFn)
	if installErr != nil {
		return installErr
	}

	if installResult.WIFProvider != "" {
		inferenceWIFProvider = installResult.WIFProvider
	}

	if vendor {
		scaffoldFiles, buildErr := repos.BuildScaffoldFiles(installCfg)
		if buildErr != nil {
			return fmt.Errorf("building scaffold files for vendor: %w", buildErr)
		}
		vendorFiles, _, vendorCleanup, vendorErr := appendVendorTreeFiles(ctx, client, printer, owner, repo, scaffoldFiles, vendor, fullsendBinary, fullsendSource)
		if vendorCleanup != nil {
			defer vendorCleanup()
		}
		if vendorErr != nil {
			return fmt.Errorf("collecting vendored assets: %w", vendorErr)
		}
		repoVars := map[string]string{
			forge.PerRepoGuardVar: "true",
			"FULLSEND_MINT_URL":   mintURL,
			"FULLSEND_GCP_REGION": inferenceRegion,
			forge.VarAppSet:       effectiveAppSet,
		}
		repoSecrets := map[string]string{
			"FULLSEND_GCP_PROJECT_ID":   inferenceProject,
			"FULLSEND_GCP_WIF_PROVIDER": inferenceWIFProvider,
		}
		if err := applyPerRepoScaffold(ctx, client, printer, owner, repo, vendorFiles, repoVars, repoSecrets, scaffoldOptions{direct: c.Direct, installed: alreadyInstalled}); err != nil {
			return err
		}
	}

	if !vendor {
		if err := removeStaleVendoredAssets(ctx, client, printer, owner, repo); err != nil {
			return err
		}
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Per-repo installation complete for %s/%s", owner, repo))
	return nil
}

// gcfProvisionerAdapter wraps a gcf.Provisioner to implement adminWIFProvisioner,
// bridging the GCF-specific provisioner to the package-agnostic interface.
type gcfProvisionerAdapter struct {
	provisioner *gcf.Provisioner
}

func (a *gcfProvisionerAdapter) DiscoverMint(ctx context.Context) (*adminMintDiscovery, error) {
	if a.provisioner == nil {
		return nil, errMintNotFound
	}
	d, err := a.provisioner.DiscoverMint(ctx)
	if err != nil {
		if errors.Is(err, gcf.ErrFunctionNotFound) {
			return nil, fmt.Errorf("%w: %w", errMintNotFound, err)
		}
		return nil, err
	}
	return &adminMintDiscovery{
		URL:             d.URL,
		RoleAppIDs:      d.RoleAppIDs,
		PerRepoWIFRepos: d.PerRepoWIFRepos,
	}, nil
}

func (a *gcfProvisionerAdapter) ProvisionWIF(ctx context.Context) (string, error) {
	if a.provisioner == nil {
		return "", fmt.Errorf("WIF provisioner not configured")
	}
	return a.provisioner.ProvisionWIF(ctx)
}

func (a *gcfProvisionerAdapter) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	if a.provisioner == nil {
		return fmt.Errorf("WIF provisioner not configured")
	}
	return a.provisioner.RegisterPerRepoWIF(ctx, repo)
}

func (a *gcfProvisionerAdapter) DeletePerRepoWIF(ctx context.Context, repo string) error {
	if a.provisioner == nil {
		return fmt.Errorf("WIF provisioner not configured")
	}
	return a.provisioner.RemoveRepoFromMint(ctx, repo)
}

func (a *gcfProvisionerAdapter) DeleteWIFProvider(ctx context.Context, repo string) error {
	if a.provisioner == nil {
		return fmt.Errorf("WIF provisioner not configured")
	}
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repo format %q: expected owner/repo", repo)
	}
	providerID := mintcore.BuildRepoProviderID(strings.ToLower(parts[0]), strings.ToLower(parts[1]))
	return a.provisioner.DeleteWIFProvider(ctx, providerID)
}

// scaffoldOptions holds optional behavioral modifiers for applyPerRepoScaffold,
// keeping the function signature stable as new options are added.
type scaffoldOptions struct {
	direct         bool   // push directly to the default branch instead of creating a PR
	signOffTrailer string // e.g. "Signed-off-by: Name <email>"; appended to the commit message when non-empty
	runtime        string // runtime written to .fullsend/config.yaml ("" = default claude); described in the PR body
	installed      bool   // true when the repo already has fullsend installed (upgrade path)
}

// applyPerRepoScaffold commits scaffold files to the repo's default branch
// and configures the repository variables and secrets needed for fullsend.
func applyPerRepoScaffold(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo string, files []forge.TreeFile,
	repoVars, repoSecrets map[string]string, opts scaffoldOptions) error {

	targetRepo, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		if gh.IsPATForbiddenError(err) {
			return handlePATForbidden(printer, owner, repo, err)
		}
		return fmt.Errorf("getting repo info: %w", err)
	}
	meta := repos.BuildScaffoldPRMetadata(ctx, client, owner, repo, "",
		repos.ScaffoldMetadataOpts{GuardInstalled: &opts.installed})
	meta.PRBody += repos.RuntimeSection(opts.runtime)
	if opts.signOffTrailer != "" {
		meta.CommitMsg += "\n\n" + opts.signOffTrailer
	}
	if opts.direct {
		printer.StepStart(fmt.Sprintf("Committing scaffold files to %s/%s (%s branch)",
			owner, repo, targetRepo.DefaultBranch))
	} else {
		printer.StepStart(fmt.Sprintf("Creating scaffold PR for %s/%s (target: %s)",
			owner, repo, targetRepo.DefaultBranch))
	}
	if _, err := layers.CommitScaffoldFiles(ctx, client, printer,
		owner, repo, targetRepo.DefaultBranch,
		meta, files, opts.direct, os.Stdin); err != nil {
		return err
	}

	printer.StepStart("Configuring repository variables")
	for _, name := range maputil.SortedKeys(repoVars) {
		if err := client.CreateOrUpdateRepoVariable(ctx, owner, repo, name, repoVars[name]); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set variable %s", name))
			return fmt.Errorf("setting repo variable %s: %w", name, err)
		}
	}
	printer.StepDone(fmt.Sprintf("Set %d repository variables", len(repoVars)))

	printer.StepStart("Configuring repository secrets")
	for _, name := range maputil.SortedKeys(repoSecrets) {
		if err := client.CreateRepoSecret(ctx, owner, repo, name, repoSecrets[name]); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set secret %s", name))
			return fmt.Errorf("setting repo secret %s: %w", name, err)
		}
	}
	printer.StepDone(fmt.Sprintf("Set %d repository secrets", len(repoSecrets)))

	return nil
}

// resolveSharedRoleAppIDs discovers app IDs for the given org by matching
// installed apps against shared role-only ROLE_APP_IDS entries.
func resolveSharedRoleAppIDs(ctx context.Context, client forge.Client, existingIDs map[string]string, owner string, roles []string) (map[string]string, error) {
	roleOnly := mintcore.RoleOnlyAppIDs(existingIDs)
	if len(roleOnly) == 0 {
		return nil, fmt.Errorf("mint has no existing ROLE_APP_IDS — cannot determine app IDs for %s", owner)
	}

	ghExt, ok := client.(forge.GitHubExtensions)
	if !ok {
		return nil, fmt.Errorf("listing installations for %s: %w", owner, forge.ErrNotSupported)
	}
	installations, err := ghExt.ListOrgInstallations(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("listing installations for %s: %w", owner, err)
	}

	installedAppIDs := make(map[string]bool, len(installations))
	for _, inst := range installations {
		installedAppIDs[strconv.Itoa(inst.AppID)] = true
	}

	result := make(map[string]string, len(roles))
	for _, role := range roles {
		appID, ok := roleOnly[role]
		if !ok {
			return nil, fmt.Errorf("no app ID configured for role %q on mint", role)
		}
		if !installedAppIDs[appID] {
			return nil, fmt.Errorf("no shared app for role %q is installed in %s — install the app first", role, owner)
		}
		result[role] = appID
	}

	return result, nil
}

// detectSharedAppsGCFClientFactory creates GCF clients for detectSharedApps. Overridden in tests.
var detectSharedAppsGCFClientFactory = func(projectID string) gcf.GCFClient {
	return gcf.NewLiveGCFClient(projectID)
}

// detectSharedApps finds public GitHub Apps shared across orgs so app setup
// can reuse existing app registrations without generating new keys.
// Returns a role → app-slug mapping for detected shared apps and the full
// ROLE_APP_IDS map (role → app_id) so callers can pass it to app setup
// without a redundant GCP API call.
func detectSharedApps(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string, mintProject, mintRegion string) (map[string]string, map[string]string, error) {
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:  mintProject,
		Region:     mintRegion,
		GitHubOrgs: []string{org},
	}, detectSharedAppsGCFClientFactory(mintProject))

	existingIDs, err := prov.GetExistingRoleAppIDs(ctx)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not read ROLE_APP_IDS: %v", err))
		return nil, nil, nil
	}
	if len(existingIDs) == 0 {
		return nil, nil, nil
	}
	roleOnly := mintcore.RoleOnlyAppIDs(existingIDs)

	ghExt, ok := client.(forge.GitHubExtensions)
	if !ok {
		return nil, roleOnly, nil
	}
	installations, err := ghExt.ListOrgInstallations(ctx, org)
	if err != nil {
		return nil, roleOnly, nil
	}

	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}

	sharedSlugs := make(map[string]string)
	for _, inst := range installations {
		appIDStr := strconv.Itoa(inst.AppID)
		for role, existingAppID := range roleOnly {
			if existingAppID != appIDStr || !roleSet[role] {
				continue
			}
			sharedSlugs[role] = inst.AppSlug
			break
		}
	}
	return sharedSlugs, roleOnly, nil
}

// runAppSetup creates or reuses GitHub Apps for each role. When mintProject is
// non-empty, PEMs are also stored in GCP Secret Manager during app creation so
// they survive partial provisioning failures. When mintURL is non-empty but
// mintProject is empty (e.g. a per-repo install against a hosted mint), PEMs are managed by a
// remote mint — the secret-existence check is skipped and existing apps are
// reused silently.
func runAppSetup(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string, mintProject string, mintURL string, publicApps bool, sharedSlugs map[string]string, appSet string, storedAppIDs map[string]string) ([]layers.AgentCredentials, error) {
	printer.Header("Setting up GitHub Apps")
	printer.Blank()

	setup := appsetup.NewSetup(client, appsetup.StdinPrompter{}, appsetup.DefaultBrowser{}, printer).
		WithPublicApps(publicApps).
		WithAppSet(appSet).
		WithStoredAppIDs(storedAppIDs)

	// Merge known slugs: config-based first, then shared app overrides.
	// Filter both config slugs and shared slugs to the requested app-set
	// so that an existing install of app-set A doesn't shadow a new install
	// of app-set B. Without this, nonflux-triage (app-set "nonflux") would
	// prevent fullsend-ai-triage (app-set "fullsend-ai") from being detected
	// and installed.
	knownSlugs := filterSlugsByAppSet(loadKnownSlugs(ctx, client, org, forge.ConfigRepoName, "HEAD", printer), appSet)
	for role, slug := range filterSlugsByAppSet(sharedSlugs, appSet) {
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
		}, gcf.NewLiveGCFClient(mintProject))
	}

	// In OIDC mint mode with direct GCP access, PEMs live in Secret
	// Manager — check there.  When only a mint URL is available (no local
	// GCP project), the remote mint manages PEMs and we cannot verify
	// their existence — skip the check so handleExistingApp assumes reuse.
	// Otherwise, check GitHub repo secrets.
	if pemProvisioner != nil {
		setup = setup.WithSecretExists(func(role string) (bool, error) {
			return pemProvisioner.SecretExists(ctx, role)
		})
	} else if mintURL == "" {
		setup = setup.WithSecretExists(func(role string) (bool, error) {
			return client.RepoSecretExists(ctx, org, forge.ConfigRepoName, roleAppPrivateKeySecret(role))
		})
	}

	// In OIDC mint mode with direct GCP access, store PEMs only in Secret
	// Manager.  When only a mint URL is available, PEM storage is handled
	// by the remote mint — skip local storage.
	// Otherwise, store in GitHub repo secrets.
	if pemProvisioner != nil {
		setup = setup.WithStoreSecret(func(sctx context.Context, role, pem string) error {
			return pemProvisioner.StoreAgentPEM(sctx, role, []byte(pem))
		})
	} else if mintURL == "" {
		setup = setup.WithStoreSecret(func(sctx context.Context, role, pem string) error {
			return client.CreateRepoSecret(sctx, org, forge.ConfigRepoName, roleAppPrivateKeySecret(role), pem)
		})
	}

	var creds []layers.AgentCredentials
	for _, role := range roles {
		appCreds, err := setup.Run(ctx, org, role)
		if err != nil {
			return nil, fmt.Errorf("setting up app for role %s: %w", role, err)
		}
		creds = append(creds, toAgentCredentials(role, appCreds))
	}

	if err := setup.PermissionErrors(); err != nil {
		return nil, err
	}

	printer.Blank()
	return creds, nil
}

// roleAppPrivateKeySecret is the .fullsend repo secret that stores a role's
// GitHub App PEM. Hyphens in the role become underscores so the name is a
// valid GitHub Actions secret identifier.
func roleAppPrivateKeySecret(role string) string {
	return fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", mintcore.RoleIdentifier(role))
}

// installRequiredScopes is the set of OAuth scopes the install command
// needs when it must also create GitHub Apps. It is perRepoRequiredScopes
// plus admin:org, which app creation needs;
// TestCheckInstallScopes_SyncWithPerRepoScopes asserts parity.
var installRequiredScopes = []string{"repo", "workflow", "admin:org"}

// perRepoRequiredScopes is the set of OAuth scopes needed for per-repo install.
var perRepoRequiredScopes = []string{"repo", "workflow"}

// checkInstallScopes verifies that the token has the scopes needed for
// install before starting interactive app setup. This avoids wasting
// time on browser-based app creation only to fail on missing scopes.
func checkInstallScopes(ctx context.Context, client forge.Client, printer *ui.Printer) error {
	return checkTokenScopes(ctx, client, printer, installRequiredScopes)
}

func toAgentCredentials(role string, ac *appsetup.AppCredentials) layers.AgentCredentials {
	return layers.AgentCredentials{
		Role:     role,
		Name:     ac.Name,
		Slug:     ac.Slug,
		PEM:      ac.PEM,
		ClientID: ac.ClientID,
		AppID:    ac.AppID,
	}
}

// filterSlugsByAppSet returns a new map containing only entries whose slug
// matches the convention for the given app set (i.e., slug == appSet + "-" + role).
// Slugs from a previous install with a different app set must not be carried
// over, as they would cause findExistingInstallation to pick up the wrong app.
// Always returns a non-nil map.
func filterSlugsByAppSet(slugs map[string]string, appSet string) map[string]string {
	out := make(map[string]string, len(slugs))
	for role, slug := range slugs {
		if slug == appsetup.AppSlug(appSet, role) {
			out[role] = slug
		}
	}
	return out
}

// loadKnownSlugs discovers agent slugs from harness wrapper files in the
// config repo.
func loadKnownSlugs(ctx context.Context, client forge.Client, org, configRepo, ref string, printer *ui.Printer) map[string]string {
	agents, err := harness.DiscoverRemoteAgents(ctx, client, org, configRepo, ref)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("harness discovery: %v", err))
	}
	if len(agents) == 0 {
		return nil
	}
	slugs := make(map[string]string, len(agents))
	seen := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.Role == "" && a.Slug == "" {
			continue
		}
		if a.Role == "" || a.Slug == "" {
			printer.StepWarn(fmt.Sprintf("harness %s has role=%q slug=%q; both must be set", a.Filename, a.Role, a.Slug))
			continue
		}
		if seen[a.Role] {
			printer.StepInfo(fmt.Sprintf("duplicate role %q in harness file %s, using first occurrence", a.Role, a.Filename))
			continue
		}
		seen[a.Role] = true
		slugs[a.Role] = a.Slug
	}
	if len(slugs) > 0 {
		return slugs
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

	isInstallation, err := client.IsInstallationToken(ctx)
	if err != nil {
		printer.StepFail("Could not verify token permissions")
		return fmt.Errorf("detecting installation token: %w", err)
	}
	if isInstallation {
		printer.StepWarn("Preflight skipped: installation token (OAuth scopes do not apply)")
		return nil
	}

	granted, err := client.GetTokenScopes(ctx)
	if err != nil {
		printer.StepFail("Could not verify token permissions")
		return fmt.Errorf("checking token scopes: %w", err)
	}

	if granted == nil {
		printer.StepWarn("Preflight skipped: fine-grained token detected (scopes cannot be verified)")
		printSkipGuidance(printer, &layers.PreflightResult{Required: required, Skipped: true, SkippedReason: layers.SkipFineGrained})
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

// printSkipGuidance prints fine-grained permission guidance when
// preflight is skipped due to a fine-grained token.
func printSkipGuidance(printer *ui.Printer, result *layers.PreflightResult) {
	if guidance := result.SkipGuidance(); guidance != "" {
		for _, line := range strings.Split(guidance, "\n") {
			if line != "" {
				printer.StepInfo(line)
			}
		}
	}
}

func handlePATForbidden(printer *ui.Printer, owner, repo string, err error) error {
	printer.StepFail("Classic PAT rejected by organization")
	printer.Blank()
	printer.ErrorBox("Token not accepted", patForbiddenGuidance(owner, repo))
	return fmt.Errorf("organization forbids classic PATs: %w", err)
}

func patForbiddenGuidance(owner, repo string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Organization %q forbids classic personal access tokens.\n\n", owner)
	b.WriteString("The CLI resolves tokens in this order:\n")
	b.WriteString("  1. GH_TOKEN environment variable\n")
	b.WriteString("  2. GITHUB_TOKEN environment variable\n")
	b.WriteString("  3. gh auth token (GitHub CLI)\n\n")
	b.WriteString("The token that resolved was rejected. To fix this, create a\n")
	b.WriteString("fine-grained PAT at https://github.com/settings/personal-access-tokens/new\n")
	fmt.Fprintf(&b, "scoped to %s/%s with these permissions:\n\n", owner, repo)
	b.WriteString("  • Contents:      read/write\n")
	b.WriteString("  • Workflows:     read/write\n")
	b.WriteString("  • Secrets:       read/write\n")
	b.WriteString("  • Variables:     read/write\n")
	b.WriteString("  • Pull requests: read/write (without --direct)\n")
	b.WriteString("  • Metadata:      read-only  (required by GitHub)\n\n")
	b.WriteString("Then export it before running setup:\n")
	fmt.Fprintf(&b, "  export GH_TOKEN=github_pat_...\n")
	fmt.Fprintf(&b, "  fullsend github setup %s/%s", owner, repo)
	return b.String()
}
