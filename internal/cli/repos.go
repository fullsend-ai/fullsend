package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newReposCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "Manage per-repo installations across multiple orgs",
		Long: `Manage per-repo fullsend installations at scale via a declarative repos.yaml manifest.

The repos subcommand group provides bulk operations for platform administrators
managing fullsend across many repositories and organizations.`,
	}
	cmd.PersistentFlags().String("gitlab-token", "", "GitLab personal or project access token (overrides GITLAB_TOKEN env var)")
	cmd.AddCommand(newReposMigrateCmd())
	cmd.AddCommand(newReposInstallCmd())
	cmd.AddCommand(newReposUninstallCmd())
	cmd.AddCommand(newReposStatusCmd())
	cmd.AddCommand(newReposSetDefaultCmd())
	return cmd
}

type reposMigrateConfig struct {
	project     string
	repoFilter  []string
	dryRun      bool
	direct      bool
	concurrency int
	manifest    string

	// Test overrides
	testClient      forge.Client
	testProvisioner repos.InferenceProvisioner
}

func newReposMigrateCmd() *cobra.Command {
	var cfg reposMigrateConfig

	cmd := &cobra.Command{
		Use:   "migrate <org>",
		Short: "Migrate an org from per-org to per-repo install",
		Long: `One-command migration from per-org to per-repo fullsend installation.

For each repo enrolled in the org's per-org config (.fullsend config repo):
  1. Check inference WIF status; provision if needed
  2. Install per-repo (scaffold, variables, secrets) with config carried over
  3. Remove the repository entry from per-org config

Generates a repos.yaml manifest reflecting the migrated state.

Re-running after a partial migration picks up where it left off:
  - Already per-repo installed → skipped
  - Inference already provisioned → reuse existing WIF provider
  - Already removed from per-org config → no-op

Individual repo failures do not abort the batch.

Required GCP permissions:
  - roles/iam.workloadIdentityPoolAdmin
  - roles/resourcemanager.projectIamAdmin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}
			return runReposMigrate(cmd, org, &cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.project, "project", "", "GCP project ID for inference (required)")
	_ = cmd.MarkFlagRequired("project")
	cmd.Flags().StringSliceVar(&cfg.repoFilter, "repo", nil, "filter to specific repos (repeatable, supports globs)")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "preview only")
	cmd.Flags().BoolVar(&cfg.direct, "direct", false, "push scaffold to default branch instead of PR")
	cmd.Flags().IntVar(&cfg.concurrency, "concurrency", 4, "parallel limit (1-32)")
	cmd.Flags().StringVarP(&cfg.manifest, "manifest", "f", "repos.yaml", "output path for generated repos.yaml")

	return cmd
}

func runReposMigrate(cmd *cobra.Command, org string, cfg *reposMigrateConfig) error {
	if cfg.concurrency < 1 || cfg.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", cfg.concurrency)
	}
	if !repos.IsValidGCPProjectID(cfg.project) {
		return fmt.Errorf("--project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter, no trailing hyphen)", cfg.project)
	}

	printer := ui.New(os.Stdout)
	printer.Banner(Version())
	ctx := cmd.Context()

	var clients repos.ForgeClientFactory
	if cfg.testClient != nil {
		clients = newSingleClientFactory(cfg.testClient)
	} else {
		clients = newForgeClientFactory("", nil)
	}

	var provisioner repos.InferenceProvisioner
	if cfg.testProvisioner != nil {
		provisioner = cfg.testProvisioner
	} else {
		provisioner = newGCPInferenceProvisioner(cfg.project)
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool, installed bool) error {
		fc, fcErr := clients.ConfigFor(repos.ForgeGitHub)
		if fcErr != nil {
			return fcErr
		}
		targetRepo, repoErr := fc.Client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		meta := repos.BuildScaffoldPRMetadata(ctx, fc.Client, owner, repo, upstreamTag,
			repos.ScaffoldMetadataOpts{GuardInstalled: &installed})
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, meta, files, direct, nil)
		return commitErr
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "done":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	printer.Blank()
	if cfg.dryRun {
		printer.StepStart("Dry-run: previewing migration")
	} else {
		printer.StepStart(fmt.Sprintf("Migrating %s from per-org to per-repo install", org))
	}

	// Resolve the review app client ID for provenance validation.
	var reviewAppClientID string
	if fc, fcErr := clients.ConfigFor(repos.ForgeGitHub); fcErr == nil {
		reviewAppClientID = resolveReviewAppClientID(ctx, fc.Client, appsetup.DefaultAppSet)
	}

	migrateCfg := repos.MigrateConfig{
		Org:               org,
		Project:           cfg.project,
		RepoFilter:        cfg.repoFilter,
		DryRun:            cfg.dryRun,
		Direct:            cfg.direct,
		MaxConcurrency:    cfg.concurrency,
		ManifestPath:      cfg.manifest,
		UpstreamRef:       upstreamRef,
		UpstreamTag:       upstreamTag,
		CLIVersion:        version,
		ReviewAppClientID: reviewAppClientID,
	}

	result, err := repos.Migrate(ctx, migrateCfg, clients, provisioner, scaffoldCommitFn, progressFn)
	if err != nil {
		return err
	}

	// Write manifest if generated (skip in dry-run mode).
	if !cfg.dryRun && result.Manifest != nil {
		data, marshalErr := repos.MarshalWithHeader(result.Manifest)
		if marshalErr != nil {
			return marshalErr
		}
		if writeErr := os.WriteFile(cfg.manifest, data, 0o644); writeErr != nil {
			return fmt.Errorf("writing manifest: %w", writeErr)
		}
		printer.StepDone(fmt.Sprintf("Manifest written to %s", cfg.manifest))
	}

	// Print summary.
	printer.Blank()
	migrated := len(result.Migrated)
	skipped := len(result.Skipped)
	failed := len(result.Failed)

	for _, r := range result.Failed {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}

	for _, r := range result.Migrated {
		if r.Error != nil {
			printer.StepInfo(fmt.Sprintf("  WARNING: %s/%s — %v", r.Owner, r.Repo, r.Error))
		}
	}

	if result.UnenrollError != nil {
		printer.StepInfo(fmt.Sprintf("  WARNING: unenroll failed — %v", result.UnenrollError))
	}

	printer.StepDone(fmt.Sprintf("Migration complete: %d migrated, %d skipped, %d failed, %d unenrolled",
		migrated, skipped, failed, result.Unenrolled))

	if failed > 0 {
		return fmt.Errorf("%d repos failed during migration", failed)
	}
	if result.UnenrollError != nil {
		return fmt.Errorf("migration succeeded but unenroll failed: %w", result.UnenrollError)
	}
	return nil
}

func newReposSetDefaultCmd() *cobra.Command {
	var manifest string

	cmd := &cobra.Command{
		Use:   "set-default <key> <value>",
		Short: "Set a platform-level default in the manifest",
		Long: fmt.Sprintf(`Set or remove a platform-level default in repos.yaml.

An empty value removes the key. Creates the manifest with version: 1 if it does not exist.

Valid keys:
  %s`, strings.Join(repos.ValidDefaultKeys, "\n  ")),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return repos.SetDefault(manifest, args[0], args[1])
		},
	}

	cmd.Flags().StringVarP(&manifest, "manifest", "f", "repos.yaml", "path to repos.yaml")

	return cmd
}

// reposStatusConfig holds flags and test overrides for repos status.
type reposStatusConfig struct {
	manifest    string
	jsonOutput  bool
	repoFilter  []string
	concurrency int

	// Test overrides
	testClient forge.Client
}

func newReposStatusCmd() *cobra.Command {
	opts := &reposStatusConfig{}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Compare manifest against actual repo state",
		Long:  "Read-only comparison of the repos.yaml manifest against actual forge state. Reports installation status and configuration drift for each repo, including declared configuration-preset drift against .fullsend/config.base.yaml and managed configuration drift against .fullsend/config.yaml.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposStatus(cmd, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or HTTPS URL to manifest file")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit JSON output instead of table")
	cmd.Flags().StringArrayVar(&opts.repoFilter, "repo", nil, "filter to specific repos (repeatable)")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 8, "max parallel API calls")

	return cmd
}

func runReposStatus(cmd *cobra.Command, opts *reposStatusConfig) error {
	ctx := cmd.Context()

	m, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}

	var clients repos.ForgeClientFactory
	if opts.testClient != nil {
		clients = newSingleClientFactory(opts.testClient)
	} else {
		clients = newForgeClientFactory(getGitLabToken(cmd), m)
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()
	result, err := repos.Status(ctx, m, clients, opts.concurrency, opts.repoFilter, repos.DriftConfig{UpstreamRef: upstreamRef, UpstreamTag: upstreamTag})
	if err != nil {
		return err
	}
	annotateGitLabRoleLifecycle(ctx, clients, result)

	return renderStatusResult(cmd, result, opts.jsonOutput)
}

func renderStatusResult(cmd *cobra.Command, result *repos.StatusResult, jsonOutput bool) error {
	if jsonOutput {
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling JSON: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
	} else {
		printStatusTable(cmd, result)
	}

	if result.Summary.Drifted > 0 || result.Summary.NotInstalled > 0 || result.Summary.Errored > 0 {
		cmd.SilenceUsage = true
		return fmt.Errorf("%d installed, %d drifted, %d not installed, %d errored",
			result.Summary.Installed, result.Summary.Drifted, result.Summary.NotInstalled, result.Summary.Errored)
	}
	return nil
}

// formatRef formats a ref for display in the status table. SHA-like refs
// (40 hex characters) are truncated to 7 characters. When the expected ref
// is set and differs from the current ref, it is appended in parentheses
// (also truncated if it is a SHA).
//
// SHA detection reuses commitSHAPattern (defined in agent.go) which matches
// exactly 40 lowercase hex characters. This intentionally differs from
// isSHARef in internal/repos/ref_ops.go, which accepts 7–40 hex chars
// case-insensitively; the stricter check here is appropriate because Git
// stores full SHAs as lowercase and status output always receives full refs.
func formatRef(currentRef, expectedRef string) string {
	if currentRef == "" {
		return "—"
	}

	if !commitSHAPattern.MatchString(currentRef) {
		return currentRef
	}

	display := currentRef[:7]
	if expectedRef != "" && expectedRef != currentRef {
		if commitSHAPattern.MatchString(expectedRef) {
			display += " (" + expectedRef[:7] + ")"
		} else {
			display += " (" + expectedRef + ")"
		}
	}

	return display
}

func showGitLabRoleStatus(s repos.RepoStatus) bool {
	// Surface role-credential diagnostics and parse/read/registry errors.
	return len(s.GitLabRoleDiagnostics) > 0
}

func printStatusTable(cmd *cobra.Command, result *repos.StatusResult) {
	out := cmd.OutOrStdout()

	maxRepo := len("REPO")
	maxRef := len("REF")
	for _, s := range result.Repos {
		name := s.Owner + "/" + s.Repo
		if len(name) > maxRepo {
			maxRepo = len(name)
		}
		ref := formatRef(s.CurrentRef, s.ExpectedRef)
		if len(ref) > maxRef {
			maxRef = len(ref)
		}
	}

	fmt.Fprintf(out, "%-*s  %-*s  %-14s  %s\n", maxRepo, "REPO", maxRef, "REF", "STATUS", "DRIFT")
	for _, s := range result.Repos {
		name := s.Owner + "/" + s.Repo
		ref := formatRef(s.CurrentRef, s.ExpectedRef)

		var status string
		switch {
		case s.Error != "":
			status = "error"
		case !s.Installed:
			status = "not installed"
		default:
			status = "installed"
		}

		var drift string
		switch {
		case s.Error != "":
			drift = s.Error
		case len(s.Drifts) == 0:
			drift = "none"
		default:
			fields := make([]string, len(s.Drifts))
			for i, d := range s.Drifts {
				fields[i] = d.Field + " differs"
			}
			drift = strings.Join(fields, ", ")
		}

		fmt.Fprintf(out, "%-*s  %-*s  %-14s  %s\n", maxRepo, name, maxRef, ref, status, drift)
	}

	fmt.Fprintf(out, "\n%d installed, %d drifted, %d not installed",
		result.Summary.Installed, result.Summary.Drifted, result.Summary.NotInstalled)
	if result.Summary.Errored > 0 {
		fmt.Fprintf(out, ", %d errored", result.Summary.Errored)
	}
	fmt.Fprintln(out)

	for _, s := range result.Repos {
		if !showGitLabRoleStatus(s) {
			continue
		}
		fmt.Fprintf(out, "\n%s GitLab roles:\n", s.Owner+"/"+s.Repo)
		for _, d := range s.GitLabRoleDiagnostics {
			fmt.Fprintf(out, "  %s\n", d)
		}
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(out, "WARNING: %s\n", w)
	}
}

// reposInstallConfig holds flags and test overrides for repos install.
type reposInstallConfig struct {
	// Core flags
	manifest            string
	dryRun              bool
	repoFilter          []string
	concurrency         int
	roles               []string
	rolesChanged        bool
	direct              bool
	force               bool
	reactivateSchedules bool
	gitlabToken         string
	forge               string

	// GCP credentials (install-time only)
	inferenceProject       string
	inferenceWIFProvider   string
	inferenceProjectNumber string // auto-derived from --inference-project; not a CLI flag
	inferenceRegion        string

	// openAIAPIKey is written as FULLSEND_OPENAI_API_KEY to selected
	// repos whose inference.auth is openai-api-key. Command-line only:
	// never persisted to the manifest and never logged.
	openAIAPIKey string

	// GitLab-specific
	gitlabURL          string
	gitlabRoleRegistry string
	gitlabRoleTokens   []string

	gitlabRoleRegistryJSON string
	gitlabRoleProvided     map[gitlabroles.Role]string
	rotateGitLabRoles      bool
	rotateGitLabRoleNames  []string
	rotateGitLabRoleFilter []gitlabroles.Role

	// Per-repo overrides
	fullsendRef            string
	mintURL                string
	appSet                 string
	allowedRemoteResources []string
	runtime                string
	inferenceAuth          string

	// Vendor flags
	vendor         bool
	fullsendBinary string
	fullsendSource string
	vendorChanged  bool

	// Test overrides
	testClient               forge.Client
	testFactory              repos.ForgeClientFactory
	testGitLabTokenInventory repos.ProjectAccessTokenClient
	testProjectNumberFn      func(ctx context.Context, projectID string) (string, error)
}

func newReposInstallCmd() *cobra.Command {
	opts := &reposInstallConfig{}

	cmd := &cobra.Command{
		Use:   "install [repos...]",
		Short: "Converge repos to the desired state defined in a manifest",
		Long: `Idempotent convergence operator for repos.yaml manifest entries.

For repos not yet in the manifest, adds them (requires --forge). For repos
whose shim workflow is not yet on the default branch, scaffolds workflow
files and writes variables/secrets onto the initialization branch, including
re-runs while an initialization PR/MR is still open. For repos whose workflow
is already on the default branch, reconciles variable drift, disabled GitLab
pipeline schedules (reported as drift; reactivated only when
--reactivate-schedules is passed), declared configuration-preset drift
against .fullsend/config.base.yaml, managed .fullsend/config.yaml drift
for opted-in repositories, and upgrades scaffold refs to match the
manifest.

When repos are specified as positional arguments, only those repos are
processed. Glob patterns (e.g. "acme/*") are matched against manifest
entries. When no repos are specified, all manifest repos are converged.
Credentials are required only for the forges of the selected repos.

Every selected repo must resolve an inference authentication method
(inference.auth: vertex-wif or openai-api-key) from its manifest entry, its
forge section, or defaults; there is no implicit default. --inference-auth
records the selection as inference.auth on each selected manifest entry
(new and existing), never on defaults or forge sections.

Each repo's inference.auth selects the inference credentials provisioned on
it: vertex-wif writes FULLSEND_GCP_PROJECT_ID, FULLSEND_GCP_WIF_PROVIDER and
FULLSEND_GCP_REGION (from --inference-project, --inference-region and the
derived or --inference-wif-provider WIF provider); openai-api-key writes
FULLSEND_OPENAI_API_KEY (from --openai-api-key, GitHub and GitLab). Existing
secrets are reused when no values are supplied; supplied values replace
them. A repo missing its credentials with no values supplied fails before
any write. After switching methods, the other method's Fullsend-managed
secrets are removed once the new ones are written. --openai-api-key is a
command-line input only; it is never written to repos.yaml.

GCP infrastructure (WIF, mint) must be provisioned separately via
'inference provision' and 'mint enroll' before running this command.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.repoFilter = args
			opts.gitlabToken = getGitLabToken(cmd)
			if err := validateVendorFlags(opts.vendor, opts.fullsendBinary, opts.fullsendSource); err != nil {
				return err
			}
			opts.vendorChanged = cmd.Flags().Changed("vendor")
			opts.rolesChanged = cmd.Flags().Changed("roles")
			return runReposInstall(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would change without applying")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")
	cmd.Flags().StringSliceVar(&opts.roles, "roles", config.PerRepoDefaultRoles(), "agent roles to install")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push scaffold directly to default branch (skip PR)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "allow scaffold ref downgrades")
	cmd.Flags().BoolVar(&opts.reactivateSchedules, "reactivate-schedules", false, "reactivate required GitLab pipeline schedules that exist but are disabled (leave disabled by default so off-system polling setups are not silently reverted)")
	cmd.Flags().StringVar(&opts.forge, "forge", "", "forge type for repos not yet in the manifest (github or gitlab)")
	cmd.Flags().StringVar(&opts.inferenceProject, "inference-project", "", "GCP project ID for inference")
	cmd.Flags().StringVar(&opts.inferenceWIFProvider, "inference-wif-provider", "", "full WIF provider resource name (projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}); uses this provider for all repos instead of deriving per-repo providers")
	cmd.Flags().StringVar(&opts.inferenceRegion, "inference-region", "", "GCP region for inference (default: global)")
	cmd.Flags().StringVar(&opts.openAIAPIKey, "openai-api-key", "", "OpenAI API key written as FULLSEND_OPENAI_API_KEY to selected repos whose inference.auth is openai-api-key; command-line only, never written to repos.yaml and never logged")
	cmd.Flags().StringVar(&opts.fullsendRef, "fullsend-ref", "", "per-repo fullsend workflow ref override")
	cmd.Flags().StringVar(&opts.mintURL, "mint-url", "", "per-repo mint URL override")
	cmd.Flags().StringVar(&opts.appSet, "app-set", "", "GitHub App set prefix (apps named {app-set}-{role}) persisted as FULLSEND_APP_SET for selected repos; GitHub-only")
	cmd.Flags().StringSliceVar(&opts.allowedRemoteResources, "allowed-remote-resources", nil, "per-repo allowed remote resources override")
	cmd.Flags().StringVar(&opts.runtime, "runtime", "", "agent runtime written to the per-repo config for repos added by this command (claude, pi, codex); repos already in the manifest keep their entry/defaults.runtime")
	cmd.Flags().StringVar(&opts.inferenceAuth, "inference-auth", "", "inference authentication method (vertex-wif or openai-api-key) persisted as inference.auth on each selected manifest entry, overriding forge-section and defaults values for those repos")
	cmd.Flags().StringVar(&opts.gitlabURL, "gitlab-url", "", "GitLab instance URL (e.g. https://gitlab.example.com); sets gitlab.url in the manifest and implies --forge=gitlab when no forge is specified")
	cmd.Flags().StringVar(&opts.gitlabRoleRegistry, "gitlab-role-registry", "", "path to administrator GitLab role registry JSON (custom roles; never secret values)")
	cmd.Flags().StringArrayVar(&opts.gitlabRoleTokens, "gitlab-role-token", nil, "administrator-provided GitLab role PAT (repeatable, role=token); values are never logged")
	cmd.Flags().BoolVar(&opts.rotateGitLabRoles, "rotate-gitlab-roles", false, "force-rotate GitLab role credentials even if they are not near expiry")
	cmd.Flags().StringArrayVar(&opts.rotateGitLabRoleNames, "rotate-gitlab-role", nil, "rotate a specific GitLab role (repeatable); default is all own-credential roles that are due")
	addVendorFlags(cmd, &opts.vendor, &opts.fullsendBinary, &opts.fullsendSource)

	return cmd
}

func runReposInstall(ctx context.Context, opts *reposInstallConfig) error {
	if opts.concurrency < 1 || opts.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}
	if opts.inferenceProject != "" && !repos.IsValidGCPProjectID(opts.inferenceProject) {
		return fmt.Errorf("--inference-project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter, no trailing hyphen)", opts.inferenceProject)
	}
	if opts.inferenceWIFProvider != "" {
		if err := validateWIFProvider(opts.inferenceWIFProvider); err != nil {
			return err
		}
	}
	if opts.forge != "" && !repos.IsValidForge(opts.forge) {
		return fmt.Errorf("--forge: %q is not a valid forge platform (valid: %s, %s)", opts.forge, repos.ForgeGitHub, repos.ForgeGitLab)
	}
	if err := repos.ValidateInferenceAuth("--inference-auth", opts.inferenceAuth); err != nil {
		return err
	}
	if opts.fullsendRef != "" && !repos.IsValidRef(opts.fullsendRef) {
		return fmt.Errorf("--fullsend-ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", opts.fullsendRef)
	}
	if opts.mintURL != "" {
		mu, muErr := url.Parse(opts.mintURL)
		if muErr != nil || mu.Scheme != "https" || mu.Host == "" {
			return fmt.Errorf("--mint-url must be a valid HTTPS URL, got %q", opts.mintURL)
		}
	}
	if opts.appSet != "" {
		if opts.forge == repos.ForgeGitLab || opts.gitlabURL != "" {
			return fmt.Errorf("--app-set is a GitHub-only option and cannot be combined with GitLab installs")
		}
		if opts.appSet != repos.NoneSentinel {
			if err := appsetup.ValidateAppSet(opts.appSet); err != nil {
				return fmt.Errorf("--app-set: %w", err)
			}
		}
	}
	if opts.gitlabURL != "" {
		if opts.forge == repos.ForgeGitHub {
			return fmt.Errorf("--gitlab-url cannot be combined with --forge=github")
		}
		gu, guErr := url.Parse(opts.gitlabURL)
		if guErr != nil || gu.Scheme != "https" || gu.Host == "" {
			return fmt.Errorf("--gitlab-url must be a valid HTTPS URL, got %q", opts.gitlabURL)
		}
		if err := repos.RejectExtraneousURLParts(gu, "--gitlab-url"); err != nil {
			return err
		}
	}
	if err := prepareGitLabRoleFlags(opts); err != nil {
		return err
	}

	printer := ui.New(os.Stdout)

	// Default --inference-region to "global" (matching admin install)
	// when --inference-project is set but --inference-region is not.
	if opts.inferenceProject != "" && opts.inferenceRegion == "" {
		opts.inferenceRegion = "global"
	}

	// When --inference-wif-provider is not set, converge derives the
	// project number from --inference-project via the GCP Resource
	// Manager API so that per-repo WIF provider paths can be constructed.
	// The lookup is lazy: it runs only when a selected vertex-wif repo
	// needs a derived provider, so OpenAI-only runs never call GCP.
	var resolveProjectNumber func(ctx context.Context, projectID string) (string, error)
	if opts.inferenceProject != "" && opts.inferenceWIFProvider == "" && opts.inferenceProjectNumber == "" {
		resolveProjectNumber = opts.testProjectNumberFn
		if resolveProjectNumber == nil {
			resolveProjectNumber = func(ctx context.Context, projectID string) (string, error) {
				return gcf.NewLiveGCFClient(projectID).GetProjectNumber(ctx, projectID)
			}
		}
	}

	printer.StepStart("Loading manifest")
	manifest, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		// Bootstrap an empty manifest when the file does not exist and
		// positional repos are provided. The --forge requirement is
		// enforced later when repos are added to the manifest.
		if len(opts.repoFilter) > 0 &&
			!strings.HasPrefix(opts.manifest, "https://") &&
			!strings.HasPrefix(opts.manifest, "http://") &&
			errors.Is(err, os.ErrNotExist) {
			manifest = &repos.Manifest{Version: 1}
			printer.StepDone("No manifest found; bootstrapping new manifest")
		} else {
			return fmt.Errorf("loading manifest: %w", err)
		}
	} else {
		if err := manifest.Validate(); err != nil {
			return fmt.Errorf("manifest validation failed: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", manifest.TotalRepoCount()))
	}

	// When --gitlab-url is provided, set the URL in-memory before
	// creating the forge client factory so it captures the correct
	// URL for GitLab API calls during repo probing and converge.
	// The forge-inference block below has an explicit guard that
	// sets forgeName to ForgeGitLab when --gitlab-url is provided,
	// so the EnsurePlatform side effect here is only for the URL.
	if opts.gitlabURL != "" {
		manifest.EnsurePlatform(repos.ForgeGitLab)
		manifest.GitLab.URL = opts.gitlabURL
	}

	var clients repos.ForgeClientFactory
	switch {
	case opts.testFactory != nil:
		clients = opts.testFactory
	case opts.testClient != nil:
		clients = newSingleClientFactory(opts.testClient)
	default:
		clients = newForgeClientFactory(opts.gitlabToken, manifest)
	}

	// Phase 0: add repos not yet in the manifest.
	var newlyAdded []string
	var notInManifest []string
	var globCovered []string
	if len(opts.repoFilter) > 0 {
		for _, r := range opts.repoFilter {
			if strings.ContainsAny(r, "*?[") {
				continue
			}
			parts := strings.SplitN(r, "/", 2)
			if len(parts) != 2 {
				continue
			}
			if _, found := manifest.ResolveConfig(parts[0], parts[1]); !found {
				// A repo covered by a manifest glob is already tracked: keep
				// the glob (and its inference.auth and other overrides)
				// rather than creating an explicit entry that would drop
				// them. Per-entry override flags and --inference-auth carve
				// out an explicit copy of the glob below.
				if _, globbed := manifest.ResolveConfigWithGlobs(parts[0], parts[1]); globbed {
					globCovered = append(globCovered, r)
					continue
				}
				notInManifest = append(notInManifest, r)
			}
		}
		hasEntryOverrides := opts.fullsendRef != "" || opts.mintURL != "" ||
			len(opts.allowedRemoteResources) > 0 || opts.runtime != "" || opts.vendorChanged ||
			opts.appSet != ""
		// A textual glob match does not guarantee that convergence discovers
		// the repository: ListOrgRepos omits forks and archived repos, so a
		// glob-covered target can be absent from the expansion and would then
		// fail as an unmatched filter. Give such repos an explicit copy of
		// their matching glob entry so they stay eligible for convergence.
		carveTargets := globCovered
		if len(globCovered) > 0 && !hasEntryOverrides {
			carveTargets = nil
			expanded, expandErr := manifest.ExpandGlobsFor(ctx, clients, globCovered)
			if expandErr != nil {
				// Convergence repeats the expansion and reports the error.
				printer.StepWarn(fmt.Sprintf("Could not expand manifest globs to verify glob-covered repos: %v", expandErr))
			} else {
				discovered := make(map[string]bool, len(expanded))
				for _, rr := range expanded {
					discovered[strings.ToLower(rr.Owner+"/"+rr.Repo)] = true
				}
				for _, r := range globCovered {
					if !discovered[strings.ToLower(r)] {
						carveTargets = append(carveTargets, r)
					}
				}
			}
		}
		if len(carveTargets) > 0 {
			if opts.runtime != "" {
				if err := validateRuntimeName(opts.runtime); err != nil {
					return fmt.Errorf("--runtime: %w", err)
				}
			}
			if len(opts.allowedRemoteResources) > 0 {
				if err := repos.ValidateAllowedRemoteResourcesFormat("--allowed-remote-resources", opts.allowedRemoteResources); err != nil {
					return err
				}
			}
			// Copy the matching glob into an explicit entry for each selected
			// repo and record the requested overrides on it, so the repo keeps
			// the glob's other settings and its siblings are unchanged. A value
			// is written when it differs from the inherited platform/default
			// value or when the copied glob entry already overrides it.
			carved, carveErr := repos.CarveOutGlobCovered(ctx, repos.ManifestEditConfig{
				Manifest:     manifest,
				ManifestPath: opts.manifest,
				DryRun:       opts.dryRun,
			}, carveTargets, clients, func(forgeName string, platform *repos.PlatformConfig, entry *repos.RepoEntry) error {
				// Reject a target that would have no effective inference
				// authentication selection before anything is persisted:
				// the flag, or the copied glob entry's own, forge-section,
				// or defaults value. --app-set also carves here, so the
				// check runs before UpdateAppSet can persist its entry.
				if forgeName != repos.ForgeGitHub && opts.appSet != "" {
					return fmt.Errorf("--app-set is a GitHub-only option and cannot be combined with GitLab installs")
				}
				if opts.inferenceAuth == "" {
					owner, repo, _ := strings.Cut(entry.Name, "/")
					if err := manifest.ResolveConfigForEntry(owner, repo, forgeName, *entry).RequireInferenceAuth(); err != nil {
						return err
					}
				}
				if opts.fullsendRef != "" && (entry.FullsendRef != "" || opts.fullsendRef != platform.FullsendRef) {
					entry.FullsendRef = opts.fullsendRef
				}
				if forgeName == repos.ForgeGitHub && opts.mintURL != "" && (entry.MintURL != "" || opts.mintURL != platform.MintURL) {
					entry.MintURL = opts.mintURL
				}
				if len(opts.allowedRemoteResources) > 0 {
					entry.AllowedRemoteResources = opts.allowedRemoteResources
				}
				if opts.runtime != "" && (entry.Runtime != "" || opts.runtime != manifest.Defaults.Runtime) {
					entry.Runtime = opts.runtime
				}
				if opts.vendorChanged {
					defaultVendor := manifest.Defaults.Vendor != nil && *manifest.Defaults.Vendor
					if entry.Vendor != nil || opts.vendor != defaultVendor {
						v := opts.vendor
						entry.Vendor = &v
					}
				}
				return nil
			})
			if carveErr != nil {
				return fmt.Errorf("applying per-repo overrides to glob-covered repos: %w", carveErr)
			}
			if len(carved) > 0 && !opts.dryRun {
				printer.StepDone(fmt.Sprintf("Created explicit manifest entry for %s from matching glob", strings.Join(carved, ", ")))
			}
		}
		if len(notInManifest) > 0 {
			forgeName := opts.forge
			if forgeName == "" && opts.gitlabURL != "" {
				// --gitlab-url explicitly implies --forge=gitlab. Set it
				// before the general inference so that manifests with
				// existing GitHub repos don't pull the new repo into the
				// wrong platform section.
				forgeName = repos.ForgeGitLab
			}
			if forgeName == "" {
				// Infer forge from platform sections that contain repos,
				// falling back to section existence for empty manifests
				// bootstrapped via set-default.
				ghHasRepos := manifest.GitHub != nil && len(manifest.GitHub.Repos) > 0
				glHasRepos := manifest.GitLab != nil && len(manifest.GitLab.Repos) > 0
				if ghHasRepos && !glHasRepos {
					forgeName = repos.ForgeGitHub
				} else if glHasRepos && !ghHasRepos {
					forgeName = repos.ForgeGitLab
				} else if !ghHasRepos && !glHasRepos {
					if manifest.GitHub != nil && manifest.GitLab == nil {
						forgeName = repos.ForgeGitHub
					} else if manifest.GitLab != nil && manifest.GitHub == nil {
						forgeName = repos.ForgeGitLab
					}
				}
			}
			if forgeName == "" {
				return fmt.Errorf("--forge is required when adding repos not yet in the manifest")
			}

			if forgeName != repos.ForgeGitHub && opts.mintURL != "" {
				printer.StepWarn(fmt.Sprintf("--mint-url is only used with GitHub repos; ignored for %s", forgeName))
			}
			if forgeName != repos.ForgeGitHub && opts.appSet != "" {
				// Unlike --mint-url, --app-set is rejected outright rather
				// than silently ignored: the explicit-forge/--gitlab-url
				// path above (line ~580) already rejects --app-set for
				// GitLab, and a forge inferred from the manifest's
				// existing GitLab repos must reject it the same way
				// instead of warning and dropping the flag.
				return fmt.Errorf("--app-set is a GitHub-only option and cannot be combined with GitLab installs")
			}
			if forgeName != repos.ForgeGitHub && opts.vendorChanged && opts.vendor {
				printer.StepWarn("--vendor only fully supported for GitHub repos; GitLab CI templates do not yet reference the vendored binary")
			}
			if opts.runtime != "" {
				if err := validateRuntimeName(opts.runtime); err != nil {
					return fmt.Errorf("--runtime: %w", err)
				}
			}
			if len(opts.allowedRemoteResources) > 0 {
				if err := repos.ValidateAllowedRemoteResourcesFormat("--allowed-remote-resources", opts.allowedRemoteResources); err != nil {
					return err
				}
			}

			// New entries must resolve an inference authentication method
			// before anything is written: either the flag (persisted on the
			// entry) or an inherited forge-section/defaults value.
			if opts.inferenceAuth == "" {
				inherited := manifest.Defaults.Inference.Auth
				if p := manifest.PlatformFor(forgeName); p != nil && p.Inference.Auth != "" {
					inherited = p.Inference.Auth
				}
				if inherited == "" {
					return fmt.Errorf("no inference authentication selected for %s: pass --inference-auth (%s) or set inference.auth in the %s section or under defaults in the manifest",
						strings.Join(notInManifest, ", "), strings.Join(repos.ValidInferenceAuths(), " or "), forgeName)
				}
			}

			entries := make([]repos.RepoEntry, len(notInManifest))
			for i, r := range notInManifest {
				entry := repos.RepoEntry{Name: r}
				// An explicit --inference-auth is always kept at repo scope
				// so later status/converge runs resolve the same selection
				// even if forge-section or defaults values change.
				entry.Inference.Auth = opts.inferenceAuth
				platform := manifest.PlatformFor(forgeName)
				if opts.fullsendRef != "" && (platform == nil || opts.fullsendRef != platform.FullsendRef) {
					entry.FullsendRef = opts.fullsendRef
				}
				if forgeName == repos.ForgeGitHub {
					if opts.mintURL != "" && (manifest.GitHub == nil || opts.mintURL != manifest.GitHub.MintURL) {
						entry.MintURL = opts.mintURL
					}
					// Record a per-repo app_set only when it differs from the
					// manifest default, so the entry stays minimal. The "none"
					// sentinel is written when it overrides a non-empty default.
					if opts.appSet != "" && (manifest.GitHub == nil || opts.appSet != manifest.GitHub.AppSet) {
						entry.AppSet = opts.appSet
					}
				}
				if len(opts.allowedRemoteResources) > 0 {
					entry.AllowedRemoteResources = opts.allowedRemoteResources
				}
				if opts.runtime != "" && opts.runtime != manifest.Defaults.Runtime {
					entry.Runtime = opts.runtime
				}
				if opts.vendorChanged {
					defaultVendor := manifest.Defaults.Vendor != nil && *manifest.Defaults.Vendor
					if opts.vendor && !defaultVendor {
						v := true
						entry.Vendor = &v
					} else if !opts.vendor && defaultVendor {
						v := false
						entry.Vendor = &v
					}
				}
				entries[i] = entry
			}

			addProgress := func(repo, phase, msg string) {
				switch phase {
				case "done", "manifest":
					printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
				default:
					printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
				}
			}
			addResult, _, addErr := repos.AddToManifest(ctx, repos.ManifestEditConfig{
				Manifest:     manifest,
				ManifestPath: opts.manifest,
				DryRun:       opts.dryRun,
			}, forgeName, entries, clients, addProgress)
			if addErr != nil {
				return addErr
			}
			newlyAdded = addResult.Added

			// A dry run does not persist the new entries, but the read-only
			// convergence preview must still validate and plan them (inference
			// credential validation, secret writes) exactly as the real run
			// would. Add them to the in-memory manifest only; nothing is
			// written to disk in a dry run.
			if opts.dryRun && len(newlyAdded) > 0 {
				added := make(map[string]bool, len(newlyAdded))
				for _, a := range newlyAdded {
					added[strings.ToLower(a)] = true
				}
				platform := manifest.EnsurePlatform(forgeName)
				for _, e := range entries {
					if added[strings.ToLower(e.Name)] {
						platform.Repos = append(platform.Repos, e)
					}
				}
			}
		}
	}

	// Apply --app-set to every selected GitHub manifest entry, including
	// entries that were already tracked before this invocation. This makes
	// the flag a real per-repo override rather than a silent no-op on reruns.
	// Reject a selection containing GitLab before changing the manifest: the
	// app-set variable is meaningful only for GitHub installations.
	if opts.appSet != "" {
		targetedForges, forgeErr := manifest.DistinctForgesFor(opts.repoFilter)
		if forgeErr != nil {
			return fmt.Errorf("determining targeted forges for --app-set: %w", forgeErr)
		}
		for _, forgeName := range targetedForges {
			if forgeName == repos.ForgeGitLab {
				return fmt.Errorf("--app-set is a GitHub-only option and cannot be combined with GitLab installs")
			}
		}
		updated, updateErr := repos.UpdateAppSet(repos.ManifestEditConfig{
			Manifest:     manifest,
			ManifestPath: opts.manifest,
			DryRun:       opts.dryRun,
		}, opts.repoFilter, opts.appSet)
		if updateErr != nil {
			return fmt.Errorf("updating app-set overrides: %w", updateErr)
		}
		if len(updated) > 0 && !opts.dryRun {
			printer.StepDone(fmt.Sprintf("Updated app-set override for %d manifest entr%s", len(updated), map[bool]string{true: "y", false: "ies"}[len(updated) == 1]))
		}
	}

	// Apply --inference-auth to every selected manifest entry (both forges),
	// including entries tracked before this invocation, so the explicit
	// selection persists at repo scope. Entries added above already carry
	// it; defaults and forge sections are never changed.
	if opts.inferenceAuth != "" {
		updated, updateErr := repos.UpdateInferenceAuth(ctx, repos.ManifestEditConfig{
			Manifest:     manifest,
			ManifestPath: opts.manifest,
			DryRun:       opts.dryRun,
		}, opts.repoFilter, opts.inferenceAuth, clients)
		if updateErr != nil {
			return fmt.Errorf("updating inference.auth: %w", updateErr)
		}
		if len(updated) > 0 && !opts.dryRun {
			printer.StepDone(fmt.Sprintf("Set inference.auth=%s on %d manifest entr%s", opts.inferenceAuth, len(updated), map[bool]string{true: "y", false: "ies"}[len(updated) == 1]))
		}
	}

	// Persist --gitlab-url to the manifest file. The in-memory assignment
	// was done earlier (before factory creation) so the forge client
	// factory already has the correct URL.
	if opts.gitlabURL != "" {
		if len(manifest.GitLab.Repos) > 0 {
			if opts.dryRun {
				announceGitLabURLDryRun(printer, opts.gitlabURL)
			} else {
				if err := repos.SetDefault(opts.manifest, "gitlab.url", opts.gitlabURL); err != nil {
					return fmt.Errorf("writing gitlab.url to manifest: %w", err)
				}
				printer.StepDone(fmt.Sprintf("Set gitlab.url=%s in manifest", opts.gitlabURL))
			}
		} else {
			printer.StepWarn("--gitlab-url was provided but no GitLab repos are in the manifest; flag had no effect")
		}
	}

	targetedForges, err := manifest.DistinctForgesFor(opts.repoFilter)
	if err != nil {
		return fmt.Errorf("determining targeted forges: %w", err)
	}
	if err := checkAllForgeScopes(ctx, clients, printer, targetedForges); err != nil {
		return err
	}

	// When --vendor is explicitly set on the CLI, override the manifest
	// vendor setting for all repos in this run. Both --vendor and
	// --vendor=false are honored so the CLI can enable or disable
	// vendoring for a one-off run.
	var vendorOverride *bool
	if opts.vendorChanged {
		vendorOverride = &opts.vendor
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool, installed bool) error {
		rc, ok := manifest.ResolveConfigWithGlobs(owner, repo)
		if !ok {
			return fmt.Errorf("repo %s/%s not found in manifest", owner, repo)
		}
		fc, fcErr := clients.ConfigFor(rc.Forge)
		if fcErr != nil {
			return fcErr
		}

		// When vendor is enabled (CLI flag or manifest), append vendored
		// binary and content files to the scaffold commit so they are
		// delivered atomically.
		repoVendor := rc.Vendor
		if vendorOverride != nil {
			repoVendor = *vendorOverride
		}
		if repoVendor {
			var vendorErr error
			files, _, vendorErr = appendVendorTreeFiles(ctx, fc.Client, printer, owner, repo, files, true, opts.fullsendBinary, opts.fullsendSource)
			if vendorErr != nil {
				return fmt.Errorf("collecting vendored assets: %w", vendorErr)
			}
		}

		targetRepo, repoErr := fc.Client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		meta := repos.BuildScaffoldPRMetadata(ctx, fc.Client, owner, repo, upstreamTag,
			repos.ScaffoldMetadataOpts{GuardInstalled: &installed})
		if rc.Forge == repos.ForgeGitLab {
			meta.CommitMsg += " [skip ci]"
			meta.PRTitle += " [skip ci]"
		}
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, meta, files, direct, nil)
		return commitErr
	}

	// Resolve the review app client ID for provenance validation.
	// Best-effort: a missing client ID does not block installation.
	// Skip the GitHub lookup when this run does not target any GitHub repo.
	//
	// reviewAppSet is the app set this ID was resolved for; it seeds the
	// converge cache so repos on that app set skip a second lookup, and
	// repos whose effective app set differs resolve their own.
	reviewAppSet := appsetup.DefaultAppSet
	if manifest.GitHub != nil && manifest.GitHub.AppSet != "" && manifest.GitHub.AppSet != repos.NoneSentinel {
		reviewAppSet = manifest.GitHub.AppSet
	}
	var reviewAppClientID string
	if forgeListIncludesGitHub(targetedForges) {
		if fc, fcErr := clients.ConfigFor(repos.ForgeGitHub); fcErr == nil {
			// Resolve the review app by its {app_set}-review slug. Prefer
			// the manifest's GitHub app_set so a custom app set resolves
			// its own review app rather than the built-in default.
			reviewAppClientID = resolveReviewAppClientID(ctx, fc.Client, reviewAppSet)
		}
	}

	convergeCfg := repos.ConvergeConfig{
		Manifest:                manifest,
		DryRun:                  opts.dryRun,
		RepoFilter:              opts.repoFilter,
		MaxConcurrency:          opts.concurrency,
		Roles:                   opts.roles,
		RolesExplicit:           opts.rolesChanged,
		UpstreamRef:             upstreamRef,
		UpstreamTag:             upstreamTag,
		Direct:                  opts.direct,
		Force:                   opts.force,
		ReactivateSchedules:     opts.reactivateSchedules,
		InferenceProject:        opts.inferenceProject,
		InferenceProjectNumber:  opts.inferenceProjectNumber,
		InferenceRegion:         opts.inferenceRegion,
		WIFProvider:             opts.inferenceWIFProvider,
		ResolveProjectNumber:    resolveProjectNumber,
		OpenAIAPIKey:            opts.openAIAPIKey,
		ReviewAppClientID:       reviewAppClientID,
		ReviewAppClientIDAppSet: reviewAppSet,
		VendorOverride:          vendorOverride,
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "done":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	printer.Blank()
	if opts.dryRun {
		printer.StepStart("Dry-run: previewing convergence")
	} else {
		printer.StepStart("Converging repos to desired state")
	}

	result, err := repos.Converge(ctx, convergeCfg, clients, scaffoldCommitFn, progressFn)
	if err != nil {
		return err
	}

	installed := result.Installed()
	converged := result.Converged()
	alreadyCurrent := result.AlreadyCurrent()
	failed := result.Failed()

	// Writeback: when platform-level fullsend_ref was empty and repos
	// were installed, write the binary's version back to repos.yaml
	// so future installs and status checks have a baseline.
	// Exception to ADR-0057 manual-maintenance: authorized by #6190.
	if !opts.dryRun && len(installed) > 0 {
		writebackRef := upstreamTag
		if writebackRef == "" {
			writebackRef = upstreamRef
		}
		if writebackRef != "" {
			ghEmpty := manifest.GitHub == nil || manifest.GitHub.FullsendRef == ""
			glEmpty := manifest.GitLab == nil || manifest.GitLab.FullsendRef == ""
			if ghEmpty || glEmpty {
				var hasGH, hasGL bool
				for _, r := range installed {
					rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
					if !ok {
						continue
					}
					if rc.Forge == repos.ForgeGitHub {
						hasGH = true
					} else if rc.Forge == repos.ForgeGitLab {
						hasGL = true
					}
				}
				if hasGH && ghEmpty {
					if wbErr := repos.SetDefault(opts.manifest, "github.fullsend_ref", writebackRef); wbErr == nil {
						manifest.EnsurePlatform(repos.ForgeGitHub).FullsendRef = writebackRef
						printer.StepDone(fmt.Sprintf("Wrote fullsend_ref=%s to manifest (GitHub)", writebackRef))
					} else {
						printer.StepWarn(fmt.Sprintf("failed to write fullsend_ref to manifest (GitHub): %v", wbErr))
					}
				}
				if hasGL && glEmpty {
					if wbErr := repos.SetDefault(opts.manifest, "gitlab.fullsend_ref", writebackRef); wbErr == nil {
						manifest.EnsurePlatform(repos.ForgeGitLab).FullsendRef = writebackRef
						printer.StepDone(fmt.Sprintf("Wrote fullsend_ref=%s to manifest (GitLab)", writebackRef))
					} else {
						printer.StepWarn(fmt.Sprintf("failed to write fullsend_ref to manifest (GitLab): %v", wbErr))
					}
				}
			}
		}
	}

	// GitLab post-install: set up pipeline schedules for repos whose
	// convergence result says they are missing. Entry into the loop body
	// is gated on NeedsGitLabPostInstall (true when schedules are missing).
	// Schedule setup itself is gated on NeedsGitLabPipelineSchedules so a
	// repo re-run while its initialization MR is still open (#7417) does
	// not delete and recreate live schedules. Shared-token bot-PAT setup
	// was removed: role credentials are the only GitLab runtime path.
	// failedRepoKeys tracks owner/repo pairs that have already failed in an
	// earlier stage (post-install setup, poll-state provisioning) so the
	// role-provisioning pass below can skip them instead of double-counting
	// them as both a stage failure and a role failure.
	failedRepoKeys := make(map[string]bool)

	var installedPostFail int
	var postInstallFailedRepos []repos.ConvergeResult
	postInstallRepos := make([]repos.ConvergeResult, 0, len(installed)+len(converged)+len(alreadyCurrent))
	postInstallRepos = append(postInstallRepos, installed...)
	postInstallRepos = append(postInstallRepos, converged...)
	postInstallRepos = append(postInstallRepos, alreadyCurrent...)
	if !opts.dryRun && len(postInstallRepos) > 0 {
		for _, r := range postInstallRepos {
			if !r.NeedsGitLabPostInstall {
				continue
			}
			rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
			if !ok || rc.Forge != repos.ForgeGitLab {
				continue
			}
			repoFullName := r.Owner + "/" + r.Repo
			printer.Blank()
			printer.StepStart(fmt.Sprintf("[%s] GitLab post-install setup", repoFullName))

			fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
			if fcErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s] Could not get GitLab client: %v", repoFullName, fcErr))
				r.Error = fcErr
				postInstallFailedRepos = append(postInstallFailedRepos, r)
				if r.Installed {
					installedPostFail++
				}
				failedRepoKeys[repoFullName] = true
				continue
			}
			glClient, ok := fc.Client.(*gl.LiveClient)
			if !ok {
				err := fmt.Errorf("GitLab client type assertion failed — post-install setup skipped")
				printer.StepWarn(fmt.Sprintf("[%s] %v", repoFullName, err))
				r.Error = err
				postInstallFailedRepos = append(postInstallFailedRepos, r)
				if r.Installed {
					installedPostFail++
				}
				failedRepoKeys[repoFullName] = true
				continue
			}

			if r.Installed && r.NeedsGitLabPipelineSchedules {
				targetRepo, repoErr := fc.Client.GetRepo(ctx, r.Owner, r.Repo)
				if repoErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s] Could not get repo info for schedule setup: %v", repoFullName, repoErr))
					r.Error = repoErr
					postInstallFailedRepos = append(postInstallFailedRepos, r)
					if r.Installed {
						installedPostFail++
					}
					failedRepoKeys[repoFullName] = true
					continue
				}

				schedErr := setupGitLabPipelineSchedules(ctx, fc.Client, printer, r.Owner, r.Repo, targetRepo.DefaultBranch, r.GitLabTypedDispatch)
				if schedErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s] Pipeline schedule setup failed: %v", repoFullName, schedErr))
					r.Error = schedErr
					postInstallFailedRepos = append(postInstallFailedRepos, r)
					if r.Installed {
						installedPostFail++
					}
					failedRepoKeys[repoFullName] = true
				}
			}

			healGitLabResourceGroups(ctx, glClient, printer, r.Owner, r.Repo)
		}
	}

	// GitLab poll-state provisioning for already-enrolled repos.
	// Fresh installs are handled above via repos.Install (Step 5b);
	// converged and already-current repos still need
	// FULLSEND_DISPATCH_SECRET and the two poll-state branches, with
	// any legacy CI/CD variables migrated into signed documents. This
	// runs with the operator's Maintainer-level client and never
	// touches the bot PAT, so it is safe on live repos.
	var pollStateFail int
	var pollStateFailedRepos []repos.ConvergeResult
	if !opts.dryRun {
		existing := make([]repos.ConvergeResult, 0, len(converged)+len(alreadyCurrent))
		existing = append(existing, converged...)
		existing = append(existing, alreadyCurrent...)
		for _, r := range existing {
			if failedRepoKeys[r.Owner+"/"+r.Repo] {
				continue
			}
			rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
			if !ok || rc.Forge != repos.ForgeGitLab {
				continue
			}
			fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
			if fcErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] Could not get GitLab client for poll-state provisioning: %v", r.Owner, r.Repo, fcErr))
				pollStateFail++
				r.Error = fcErr
				pollStateFailedRepos = append(pollStateFailedRepos, r)
				failedRepoKeys[r.Owner+"/"+r.Repo] = true
				continue
			}
			if err := provisionGitLabPollState(ctx, fc.Client, printer, r.Owner, r.Repo); err != nil {
				pollStateFail++
				r.Error = err
				pollStateFailedRepos = append(pollStateFailedRepos, r)
				failedRepoKeys[r.Owner+"/"+r.Repo] = true
			}
		}
	}

	var roleFail int
	var roleFailedRepos []repos.ConvergeResult
	var roleFailInstalledCount int
	{
		type tagged struct {
			r     repos.ConvergeResult
			fresh bool
		}
		all := make([]tagged, 0, len(installed)+len(converged)+len(alreadyCurrent))
		for _, r := range installed {
			all = append(all, tagged{r: r, fresh: true})
		}
		for _, r := range converged {
			all = append(all, tagged{r: r, fresh: false})
		}
		for _, r := range alreadyCurrent {
			all = append(all, tagged{r: r, fresh: false})
		}
		for _, item := range all {
			// Skip repos that already failed at an earlier stage (post-install
			// setup, poll-state provisioning) so they are not double-counted
			// as both a stage failure and a role-provisioning failure.
			if item.r.Error != nil || failedRepoKeys[item.r.Owner+"/"+item.r.Repo] {
				continue
			}
			rc, ok := manifest.ResolveConfigWithGlobs(item.r.Owner, item.r.Repo)
			if !ok || rc.Forge != repos.ForgeGitLab {
				continue
			}
			fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
			if fcErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] Could not get GitLab client for role provisioning: %v", item.r.Owner, item.r.Repo, fcErr))
				roleFail++
				item.r.Error = fcErr
				roleFailedRepos = append(roleFailedRepos, item.r)
				if item.fresh {
					roleFailInstalledCount++
				}
				continue
			}
			if err := maybeProvisionGitLabRoles(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab role provisioning failed: %v", item.r.Owner, item.r.Repo, err))
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			if err := maybeRotateGitLabRoles(ctx, opts, fc.Client, printer, item.r.Owner, item.r.Repo); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab role rotation failed: %v", item.r.Owner, item.r.Repo, err))
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			if item.r.Error != nil {
				continue
			}
			if err := ensureGitLabPollerPipelineAccess(ctx, fc.Client, gitLabTokenInventory(opts, fc.Client), printer, item.r.Owner, item.r.Repo, opts.dryRun); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab poller protected-ref pipeline access failed: %v", item.r.Owner, item.r.Repo, err))
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
				continue
			}
			if err := ensureGitLabPipelineVariableOverrideRole(ctx, fc.Client, printer, item.r.Owner, item.r.Repo, opts.dryRun); err != nil {
				printer.StepWarn(fmt.Sprintf("[%s/%s] GitLab pipeline-variable override role failed: %v", item.r.Owner, item.r.Repo, err))
				roleFail++
				item.r.Error = err
				if item.fresh {
					roleFailInstalledCount++
				}
				roleFailedRepos = append(roleFailedRepos, item.r)
			}
		}
	}

	printer.Blank()
	installedCount := len(installed) - installedPostFail - roleFailInstalledCount
	failedCount := len(failed) + len(postInstallFailedRepos) + pollStateFail + roleFail

	for _, r := range failed {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range pollStateFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range postInstallFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}
	for _, r := range roleFailedRepos {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}

	printer.StepDone(fmt.Sprintf("Install complete: %d installed, %d converged, %d already current, %d failed",
		installedCount, len(converged), len(alreadyCurrent), failedCount))

	if failedCount > 0 {
		return fmt.Errorf("%d repos failed", failedCount)
	}
	return nil
}

type reposUninstallConfig struct {
	manifest      string
	dryRun        bool
	yes           bool
	direct        bool
	concurrency   int
	manifestOnly  bool
	uninstallOnly bool
	gitlabToken   string

	testClient       forge.Client
	testFactory      repos.ForgeClientFactory
	testGitLabTokens repos.ProjectAccessTokenClient
}

func newReposUninstallCmd() *cobra.Command {
	opts := &reposUninstallConfig{}

	cmd := &cobra.Command{
		Use:   "uninstall <repos...>",
		Short: "Tear down fullsend from repos and remove from manifest",
		Long: `Tear down fullsend from the specified repos and remove them from the manifest.

By default, tears down (removes workflow files via a pull request, deletes
variables and secrets immediately) and then removes the repo entry from
repos.yaml. The manifest entry is only removed if teardown succeeds.

File deletions (workflow YAML, .fullsend/config.yaml, and GitLab
.gitlab-ci.yml unmerge) are delivered as a PR unless --direct is set.
Variable and secret deletions are API operations and always happen
immediately. Use --direct to push file deletions to the default branch.

Use --manifest-only to remove from the manifest without tearing down (e.g.
when a repo is already deleted). Use --uninstall-only to tear down without
modifying the manifest (e.g. for temporary teardown with intent to reinstall).

GCP infrastructure (WIF) must be cleaned up separately via
'inference deprovision'.

Glob patterns (e.g. "acme/*") are matched against manifest entries and
prompt for confirmation unless --yes is set.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.gitlabToken = getGitLabToken(cmd)
			return runReposUninstall(cmd.Context(), opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be uninstalled without making changes")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip confirmation prompt when multiple repos are targeted")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push file deletions to default branch instead of PR")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")
	cmd.Flags().BoolVar(&opts.manifestOnly, "manifest-only", false, "remove from manifest without tearing down")
	cmd.Flags().BoolVar(&opts.uninstallOnly, "uninstall-only", false, "tear down without removing from manifest")
	cmd.MarkFlagsMutuallyExclusive("manifest-only", "uninstall-only")

	return cmd
}

func runReposUninstall(ctx context.Context, opts *reposUninstallConfig, repoArgs []string) error {
	if opts.concurrency < 1 || opts.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}

	printer := ui.New(os.Stdout)

	printer.StepStart("Loading manifest")
	manifest, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", manifest.TotalRepoCount()))

	matched, matchErr := repos.MatchManifestRepos(manifest, repoArgs)
	if matchErr != nil {
		return matchErr
	}
	if len(matched) == 0 {
		printer.StepInfo("No manifest entries matched the given patterns")
		return nil
	}

	var concreteRepos []string
	for _, r := range matched {
		if strings.ContainsAny(r, "*?[") {
			printer.StepInfo(fmt.Sprintf("[%s] Skipping glob manifest entry (use concrete repo names to uninstall)", r))
			continue
		}
		concreteRepos = append(concreteRepos, r)
	}
	if len(concreteRepos) == 0 {
		printer.StepInfo("All matched entries are glob patterns — no concrete repos to uninstall")
		return nil
	}

	action := "uninstall and remove from manifest"
	if opts.manifestOnly {
		action = "remove from manifest"
	} else if opts.uninstallOnly {
		action = "uninstall"
	}
	if !opts.yes && !opts.dryRun {
		if err := confirmBulkAction(printer, action, repoArgs, manifest, os.Stdin); err != nil {
			return err
		}
	}

	var clients repos.ForgeClientFactory
	switch {
	case opts.testFactory != nil:
		clients = opts.testFactory
	case opts.testClient != nil:
		clients = newSingleClientFactory(opts.testClient)
	default:
		clients = newForgeClientFactory(opts.gitlabToken, manifest)
	}

	progressFn := func(repo, phase, msg string) {
		switch {
		case phase == "done" || phase == "manifest":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		case strings.HasPrefix(msg, "Warning:"):
			printer.StepWarn(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool, _ bool) error {
		forgeName := ""
		if rc, ok := manifest.ResolveConfigWithGlobs(owner, repo); ok {
			forgeName = rc.Forge
		}
		fc, fcErr := clients.ConfigFor(forgeName)
		if fcErr != nil {
			return fcErr
		}
		targetRepo, repoErr := fc.Client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		meta := repos.UninstallPRMetadata()
		if forgeName == repos.ForgeGitLab {
			meta.CommitMsg += " [skip ci]"
			meta.PRTitle += " [skip ci]"
		}
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, meta, files, direct, nil)
		return commitErr
	}

	// Teardown phase (skipped when --manifest-only).
	var succeededRepos []string
	var teardownFailed int
	if !opts.manifestOnly {
		targetedForges, err := manifest.DistinctForgesFor(concreteRepos)
		if err != nil {
			return fmt.Errorf("determining targeted forges: %w", err)
		}
		if err := checkAllForgeScopes(ctx, clients, printer, targetedForges); err != nil {
			return err
		}

		teardownCfg := repos.UninstallConfig{
			Manifest:       manifest,
			Repos:          concreteRepos,
			DryRun:         opts.dryRun,
			Direct:         opts.direct,
			MaxConcurrency: opts.concurrency,
			GitLabTokens:   gitLabUninstallTokens(opts, clients, printer, manifest, concreteRepos),
		}

		printer.Blank()
		if opts.dryRun {
			printer.StepStart("Dry-run: previewing uninstall")
		} else {
			printer.StepStart("Uninstalling fullsend from repos")
		}

		results, teardownErr := repos.Uninstall(ctx, teardownCfg, clients, scaffoldCommitFn, progressFn)
		if teardownErr != nil {
			return teardownErr
		}

		for _, r := range results {
			if r.Success {
				succeededRepos = append(succeededRepos, r.Owner+"/"+r.Repo)
			} else {
				teardownFailed++
				printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
			}
		}

		// GitLab post-uninstall: clean up pipeline schedules. Role and
		// shared-bot project access tokens are revoked inside Uninstall
		// when GitLabTokens is set, and a revocation failure fails the
		// uninstall so the manifest entry remains for retry.
		if !opts.dryRun {
			for _, r := range results {
				if !r.Success {
					continue
				}
				rc, ok := manifest.ResolveConfigWithGlobs(r.Owner, r.Repo)
				if !ok || rc.Forge != repos.ForgeGitLab {
					continue
				}
				repoFullName := r.Owner + "/" + r.Repo
				printer.Blank()
				printer.StepStart(fmt.Sprintf("[%s] GitLab cleanup", repoFullName))

				fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
				if fcErr != nil {
					printer.StepWarn(fmt.Sprintf("[%s] Could not get GitLab client: %v", repoFullName, fcErr))
					continue
				}
				_ = cleanupGitLabPipelineSchedules(ctx, fc.Client, printer, r.Owner, r.Repo)
			}
		}
	} else {
		succeededRepos = concreteRepos
	}

	// Manifest removal phase (skipped when --uninstall-only).
	if !opts.uninstallOnly && len(succeededRepos) > 0 {
		removeResult, _, removeErr := repos.RemoveFromManifest(repos.ManifestEditConfig{
			Manifest:     manifest,
			ManifestPath: opts.manifest,
			DryRun:       opts.dryRun,
		}, succeededRepos, progressFn)
		if removeErr != nil {
			return removeErr
		}

		printer.Blank()
		printer.StepDone(fmt.Sprintf("Removed %d entries from manifest", len(removeResult.Removed)))
	}

	if opts.manifestOnly {
		return nil
	}

	printer.Blank()
	uninstalled := len(succeededRepos)
	printer.StepDone(fmt.Sprintf("Uninstall complete: %d uninstalled, %d failed", uninstalled, teardownFailed))

	if teardownFailed > 0 {
		return fmt.Errorf("%d repos failed to uninstall", teardownFailed)
	}
	return nil
}

// confirmBulkAction prompts for confirmation when a destructive action targets
// multiple repos — either through glob expansion or an explicit bulk list.
func confirmBulkAction(printer *ui.Printer, action string, patterns []string, manifest *repos.Manifest, stdin *os.File) error {
	matched, err := repos.MatchManifestRepos(manifest, patterns)
	if err != nil {
		return err
	}
	if len(matched) <= 1 {
		return nil
	}

	if !term.IsTerminal(int(stdin.Fd())) {
		return fmt.Errorf("stdin is not a terminal; use --yes to skip confirmation")
	}

	printer.StepWarn(fmt.Sprintf("This will %s %d repos:", action, len(matched)))
	for _, r := range matched {
		printer.StepInfo("  " + r)
	}
	printer.StepInfo("Continue? [y/N]")

	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		return fmt.Errorf("aborted")
	}
	return nil
}

func forgeListIncludesGitHub(forges []string) bool {
	for _, forgeName := range forges {
		if forgeName == "" || forgeName == repos.ForgeGitHub {
			return true
		}
	}
	return false
}

// checkAllForgeScopes validates GitHub token permissions for the given
// forges. Only GitHub forges are checked because scope introspection is
// not supported by other forge providers. Callers must pass the forges
// actually targeted by the operation, not every forge in the manifest.
func checkAllForgeScopes(ctx context.Context, clients repos.ForgeClientFactory, printer *ui.Printer, forges []string) error {
	for _, forgeName := range forges {
		if forgeName != "" && forgeName != repos.ForgeGitHub {
			continue
		}
		fc, err := clients.ConfigFor(forgeName)
		if err != nil {
			return err
		}
		if err := checkPerRepoScopes(ctx, fc.Client, printer); err != nil {
			return err
		}
	}
	return nil
}

// gcpInferenceProvisioner implements repos.InferenceProvisioner using live
// GCP API calls. It provisions per-repo WIF infrastructure in the specified
// GCP project.
type gcpInferenceProvisioner struct {
	project string
}

// inferenceGCFClientFactory creates GCF clients for gcpInferenceProvisioner.
// Overridden in tests.
var inferenceGCFClientFactory = func(projectID string) gcf.GCFClient {
	return gcf.NewLiveGCFClient(projectID)
}

func newGCPInferenceProvisioner(project string) *gcpInferenceProvisioner {
	return &gcpInferenceProvisioner{project: project}
}

func (p *gcpInferenceProvisioner) Status(ctx context.Context, owner, repo string) (string, error) {
	gcpClient := inferenceGCFClientFactory(p.project)
	providerID := mintcore.BuildRepoProviderID(owner, repo)

	projectNumber, err := gcpClient.GetProjectNumber(ctx, p.project)
	if err != nil {
		return "", fmt.Errorf("getting project number: %w", err)
	}

	providerInfo, err := gcpClient.GetWIFProvider(ctx, projectNumber, gcf.DefaultInferencePool, providerID)
	if err != nil {
		return "", fmt.Errorf("checking WIF provider: %w", err)
	}
	// A soft-deleted or disabled provider is still returned by
	// providers.get but cannot exchange tokens; report it as not
	// provisioned so the caller runs Provision, which restores it.
	if providerInfo == nil || !providerInfo.Usable() {
		return "", nil
	}

	wifProvider := fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, gcf.DefaultInferencePool, providerID)
	return wifProvider, nil
}

func (p *gcpInferenceProvisioner) Provision(ctx context.Context, owner, repo string) (string, error) {
	gcpClient := inferenceGCFClientFactory(p.project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:   p.project,
		GitHubOrgs:  []string{owner},
		Repo:        owner + "/" + repo,
		WIFPoolName: gcf.DefaultInferencePool,
	}, gcpClient)

	wifProvider, err := provisioner.ProvisionWIF(ctx)
	if err != nil {
		return "", fmt.Errorf("provisioning WIF: %w", err)
	}
	return wifProvider, nil
}

// announceGitLabURLDryRun prints a dry-run preview message for --gitlab-url
// when the flag is set. Centralizes the message and guard so both the
// early-return path and the main --gitlab-url handler share a single
// definition.
func announceGitLabURLDryRun(printer *ui.Printer, gitlabURL string) {
	if gitlabURL != "" {
		printer.StepDone(fmt.Sprintf("Would set gitlab.url=%s in manifest", gitlabURL))
	}
}
