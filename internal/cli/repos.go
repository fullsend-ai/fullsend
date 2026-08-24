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
  3. Unenroll from per-org config

Generates a repos.yaml manifest reflecting the migrated state.

Re-running after a partial migration picks up where it left off:
  - Already per-repo installed → skipped
  - Inference already provisioned → reuse existing WIF provider
  - Already unenrolled → no-op

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

func newReposStatusCmd() *cobra.Command {
	var (
		manifest    string
		jsonOutput  bool
		repoFilter  []string
		concurrency int
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Compare manifest against actual repo state",
		Long:  "Read-only comparison of the repos.yaml manifest against actual forge state. Reports installation status and configuration drift for each repo.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposStatus(cmd, manifest, jsonOutput, repoFilter, concurrency)
		},
	}

	cmd.Flags().StringVarP(&manifest, "manifest", "f", "repos.yaml", "path or HTTPS URL to manifest file")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON output instead of table")
	cmd.Flags().StringArrayVar(&repoFilter, "repo", nil, "filter to specific repos (repeatable)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 8, "max parallel API calls")

	return cmd
}

func runReposStatus(cmd *cobra.Command, manifestPath string, jsonOutput bool, repoFilter []string, concurrency int) error {
	ctx := cmd.Context()

	m, err := repos.LoadManifest(ctx, manifestPath)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}

	clients := newForgeClientFactory(getGitLabToken(cmd), m)

	result, err := repos.Status(ctx, m, clients, concurrency, repoFilter)
	if err != nil {
		return err
	}

	return renderStatusResult(cmd, result, jsonOutput)
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

	for _, w := range result.Warnings {
		fmt.Fprintf(out, "WARNING: %s\n", w)
	}
}

// reposInstallConfig holds flags and test overrides for repos install.
type reposInstallConfig struct {
	// Core flags
	manifest    string
	dryRun      bool
	repoFilter  []string
	concurrency int
	roles       []string
	direct      bool
	force       bool
	gitlabToken string
	forge       string

	// GCP credentials (install-time only)
	inferenceProject       string
	inferenceProjectNumber string
	inferenceRegion        string

	// GitLab-specific
	gitlabBotToken string

	// Per-repo overrides
	fullsendRef            string
	mintURL                string
	allowedRemoteResources []string
	runtime                string

	// Test overrides
	testClient          forge.Client
	testProjectNumberFn func(ctx context.Context, projectID string) (string, error)
}

func newReposInstallCmd() *cobra.Command {
	opts := &reposInstallConfig{}

	cmd := &cobra.Command{
		Use:   "install [repos...]",
		Short: "Converge repos to the desired state defined in a manifest",
		Long: `Idempotent convergence operator for repos.yaml manifest entries.

For repos not yet in the manifest, adds them (requires --forge). For repos
not yet provisioned, scaffolds workflow files and writes variables/secrets.
For already-installed repos, reconciles variable drift and upgrades scaffold
refs to match the manifest.

When repos are specified as positional arguments, only those repos are
processed. Glob patterns (e.g. "acme/*") are matched against manifest
entries. When no repos are specified, all manifest repos are converged.

GCP infrastructure (WIF, mint) must be provisioned separately via
'inference provision' and 'mint enroll' before running this command.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.repoFilter = args
			opts.gitlabToken = getGitLabToken(cmd)
			if opts.gitlabBotToken == "" {
				opts.gitlabBotToken = os.Getenv("FULLSEND_GITLAB_BOT_TOKEN")
			}
			return runReposInstall(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would change without applying")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")
	cmd.Flags().StringSliceVar(&opts.roles, "roles", config.PerRepoDefaultRoles(), "agent roles to install")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push scaffold directly to default branch (skip PR)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "allow scaffold ref downgrades")
	cmd.Flags().StringVar(&opts.forge, "forge", "", "forge type for repos not yet in the manifest (github or gitlab)")
	cmd.Flags().StringVar(&opts.inferenceProject, "inference-project", "", "GCP project ID for inference")
	cmd.Flags().StringVar(&opts.inferenceProjectNumber, "inference-project-number", "", "numeric GCP project number (auto-derived from --inference-project when omitted)")
	cmd.Flags().StringVar(&opts.inferenceRegion, "inference-region", "", "GCP region for inference (default: global)")
	cmd.Flags().StringVar(&opts.fullsendRef, "fullsend-ref", "", "per-repo fullsend workflow ref override")
	cmd.Flags().StringVar(&opts.mintURL, "mint-url", "", "per-repo mint URL override")
	cmd.Flags().StringSliceVar(&opts.allowedRemoteResources, "allowed-remote-resources", nil, "per-repo allowed remote resources override")
	cmd.Flags().StringVar(&opts.runtime, "runtime", "", "agent runtime written to the per-repo config for repos added by this command (claude, pi); repos already in the manifest keep their entry/defaults.runtime")
	cmd.Flags().StringVar(&opts.gitlabBotToken, "gitlab-bot-token", "", "GitLab bot PAT for free-tier instances that don't support project access tokens")

	return cmd
}

func runReposInstall(ctx context.Context, opts *reposInstallConfig) error {
	if opts.concurrency < 1 || opts.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}
	if opts.inferenceProject != "" && !repos.IsValidGCPProjectID(opts.inferenceProject) {
		return fmt.Errorf("--inference-project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter, no trailing hyphen)", opts.inferenceProject)
	}
	if opts.inferenceProjectNumber != "" && !repos.IsNumeric(opts.inferenceProjectNumber) {
		return fmt.Errorf("--inference-project-number must be numeric, got %q", opts.inferenceProjectNumber)
	}
	if opts.forge != "" && !repos.IsValidForge(opts.forge) {
		return fmt.Errorf("--forge: %q is not a valid forge platform (valid: %s, %s)", opts.forge, repos.ForgeGitHub, repos.ForgeGitLab)
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

	printer := ui.New(os.Stdout)

	// Default --inference-region to "global" (matching admin install)
	// when --inference-project is set but --inference-region is not.
	if opts.inferenceProject != "" && opts.inferenceRegion == "" {
		opts.inferenceRegion = "global"
	}

	// Derive --inference-project-number from --inference-project via
	// the GCP Resource Manager API when not explicitly provided.
	if opts.inferenceProject != "" && opts.inferenceProjectNumber == "" {
		var projectNumber string
		var lookupErr error
		if opts.testProjectNumberFn != nil {
			projectNumber, lookupErr = opts.testProjectNumberFn(ctx, opts.inferenceProject)
		} else {
			gcpClient := gcf.NewLiveGCFClient(opts.inferenceProject)
			projectNumber, lookupErr = gcpClient.GetProjectNumber(ctx, opts.inferenceProject)
		}
		if lookupErr != nil {
			return fmt.Errorf("deriving project number from %q: %w (use --inference-project-number to specify it manually)", opts.inferenceProject, lookupErr)
		}
		opts.inferenceProjectNumber = projectNumber
		printer.StepDone(fmt.Sprintf("Derived project number %s from project %s", projectNumber, opts.inferenceProject))
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

	var clients repos.ForgeClientFactory
	if opts.testClient != nil {
		clients = newSingleClientFactory(opts.testClient)
	} else {
		clients = newForgeClientFactory(opts.gitlabToken, manifest)
	}

	// Phase 0: add repos not yet in the manifest.
	var newlyAdded []string
	if len(opts.repoFilter) > 0 {
		var notInManifest []string
		for _, r := range opts.repoFilter {
			if strings.ContainsAny(r, "*?[") {
				continue
			}
			parts := strings.SplitN(r, "/", 2)
			if len(parts) != 2 {
				continue
			}
			if _, found := manifest.ResolveConfig(parts[0], parts[1]); !found {
				notInManifest = append(notInManifest, r)
			}
		}
		if len(notInManifest) > 0 {
			forgeName := opts.forge
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
			if opts.runtime != "" {
				if err := validateRuntimeName(opts.runtime); err != nil {
					return fmt.Errorf("--runtime: %w", err)
				}
			}

			entries := make([]repos.RepoEntry, len(notInManifest))
			for i, r := range notInManifest {
				entry := repos.RepoEntry{Name: r}
				platform := manifest.PlatformFor(forgeName)
				if opts.fullsendRef != "" && (platform == nil || opts.fullsendRef != platform.FullsendRef) {
					entry.FullsendRef = opts.fullsendRef
				}
				if forgeName == repos.ForgeGitHub {
					if opts.mintURL != "" && (manifest.GitHub == nil || opts.mintURL != manifest.GitHub.MintURL) {
						entry.MintURL = opts.mintURL
					}
				}
				if len(opts.allowedRemoteResources) > 0 {
					entry.AllowedRemoteResources = opts.allowedRemoteResources
				}
				if opts.runtime != "" && opts.runtime != manifest.Defaults.Runtime {
					entry.Runtime = opts.runtime
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

			if opts.dryRun && len(newlyAdded) > 0 {
				var filtered []string
				added := make(map[string]bool)
				for _, a := range newlyAdded {
					added[strings.ToLower(a)] = true
				}
				for _, r := range opts.repoFilter {
					if !added[strings.ToLower(r)] {
						filtered = append(filtered, r)
					}
				}
				opts.repoFilter = filtered
				if len(filtered) == 0 {
					printer.Blank()
					printer.StepDone(fmt.Sprintf("Install complete: %d to add, 0 converged, 0 already current, 0 failed",
						len(newlyAdded)))
					return nil
				}
			}
		}
	}

	if err := checkAllForgeScopes(ctx, manifest, clients, printer); err != nil {
		return err
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
		targetRepo, repoErr := fc.Client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		meta := repos.BuildScaffoldPRMetadata(ctx, fc.Client, owner, repo, upstreamTag,
			repos.ScaffoldMetadataOpts{GuardInstalled: &installed})
		if rc.Forge == repos.ForgeGitLab {
			meta.CommitMsg += " [skip ci]"
		}
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, meta, files, direct, nil)
		return commitErr
	}

	// Resolve the review app client ID for provenance validation.
	// Best-effort: a missing client ID does not block installation.
	var reviewAppClientID string
	if fc, fcErr := clients.ConfigFor(repos.ForgeGitHub); fcErr == nil {
		reviewAppClientID = resolveReviewAppClientID(ctx, fc.Client, appsetup.DefaultAppSet)
	}

	convergeCfg := repos.ConvergeConfig{
		Manifest:               manifest,
		DryRun:                 opts.dryRun,
		RepoFilter:             opts.repoFilter,
		MaxConcurrency:         opts.concurrency,
		Roles:                  opts.roles,
		UpstreamRef:            upstreamRef,
		UpstreamTag:            upstreamTag,
		Direct:                 opts.direct,
		Force:                  opts.force,
		InferenceProject:       opts.inferenceProject,
		InferenceProjectNumber: opts.inferenceProjectNumber,
		InferenceRegion:        opts.inferenceRegion,
		ReviewAppClientID:      reviewAppClientID,
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

	// GitLab post-install: set up bot token and pipeline schedules for
	// newly installed GitLab repos. Only fresh installs need this —
	// converged repos already have working bot tokens and schedules.
	// Running on converged repos would revoke live bot PATs, breaking
	// in-flight pipelines.
	var postInstallFailed int
	if !opts.dryRun && len(installed) > 0 {
		for _, r := range installed {
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
				postInstallFailed++
				continue
			}
			glClient, ok := fc.Client.(*gl.LiveClient)
			if !ok {
				printer.StepWarn(fmt.Sprintf("[%s] GitLab client type assertion failed — bot token setup skipped", repoFullName))
				postInstallFailed++
				continue
			}

			_, botErr := setupGitLabBotToken(ctx, fc.Client, glClient, printer, r.Owner, r.Repo, opts.gitlabBotToken)
			if botErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s] Bot token setup failed: %v", repoFullName, botErr))
				postInstallFailed++
				continue
			}

			targetRepo, repoErr := fc.Client.GetRepo(ctx, r.Owner, r.Repo)
			if repoErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s] Could not get repo info for schedule setup: %v", repoFullName, repoErr))
				continue
			}

			schedErr := setupGitLabPipelineSchedules(ctx, fc.Client, printer, r.Owner, r.Repo, targetRepo.DefaultBranch)
			if schedErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s] Pipeline schedule setup failed: %v", repoFullName, schedErr))
			}

			healGitLabResourceGroups(ctx, glClient, printer, r.Owner, r.Repo)
		}
	}

	printer.Blank()
	installedCount := len(installed) - postInstallFailed
	failedCount := len(failed) + postInstallFailed

	for _, r := range failed {
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
	concurrency   int
	manifestOnly  bool
	uninstallOnly bool
	gitlabToken   string

	testClient forge.Client
}

func newReposUninstallCmd() *cobra.Command {
	opts := &reposUninstallConfig{}

	cmd := &cobra.Command{
		Use:   "uninstall <repos...>",
		Short: "Tear down fullsend from repos and remove from manifest",
		Long: `Tear down fullsend from the specified repos and remove them from the manifest.

By default, tears down (deletes workflow files, variables, secrets) and then
removes the repo entry from repos.yaml. The manifest entry is only removed
if teardown succeeds.

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
	if opts.testClient != nil {
		clients = newSingleClientFactory(opts.testClient)
	} else {
		clients = newForgeClientFactory(opts.gitlabToken, manifest)
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "done", "manifest":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	// Teardown phase (skipped when --manifest-only).
	var succeededRepos []string
	var teardownFailed int
	if !opts.manifestOnly {
		if err := checkAllForgeScopes(ctx, manifest, clients, printer); err != nil {
			return err
		}

		teardownCfg := repos.UninstallConfig{
			Manifest:       manifest,
			Repos:          concreteRepos,
			DryRun:         opts.dryRun,
			MaxConcurrency: opts.concurrency,
		}

		printer.Blank()
		if opts.dryRun {
			printer.StepStart("Dry-run: previewing uninstall")
		} else {
			printer.StepStart("Uninstalling fullsend from repos")
		}

		results, teardownErr := repos.Uninstall(ctx, teardownCfg, clients, progressFn)
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

		// GitLab post-uninstall: clean up pipeline schedules and bot tokens.
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

				if glClient, ok := fc.Client.(*gl.LiveClient); ok {
					_ = cleanupGitLabBotToken(ctx, glClient, printer, r.Owner, r.Repo)
				} else {
					printer.StepWarn(fmt.Sprintf("[%s] GitLab client type assertion failed — bot token cleanup skipped", repoFullName))
				}

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

// checkAllForgeScopes validates GitHub token permissions for forges used
// in the manifest. Only GitHub forges are checked because scope
// introspection is not supported by other forge providers.
func checkAllForgeScopes(ctx context.Context, m *repos.Manifest, clients repos.ForgeClientFactory, printer *ui.Printer) error {
	for _, forgeName := range m.DistinctForges() {
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

func newGCPInferenceProvisioner(project string) *gcpInferenceProvisioner {
	return &gcpInferenceProvisioner{project: project}
}

func (p *gcpInferenceProvisioner) Status(ctx context.Context, owner, repo string) (string, error) {
	gcpClient := gcf.NewLiveGCFClient(p.project)
	providerID := mintcore.BuildRepoProviderID(owner, repo)

	projectNumber, err := gcpClient.GetProjectNumber(ctx, p.project)
	if err != nil {
		return "", fmt.Errorf("getting project number: %w", err)
	}

	providerInfo, err := gcpClient.GetWIFProvider(ctx, projectNumber, gcf.DefaultInferencePool, providerID)
	if err != nil {
		return "", fmt.Errorf("checking WIF provider: %w", err)
	}
	if providerInfo == nil {
		return "", nil
	}

	wifProvider := fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, gcf.DefaultInferencePool, providerID)
	return wifProvider, nil
}

func (p *gcpInferenceProvisioner) Provision(ctx context.Context, owner, repo string) (string, error) {
	gcpClient := gcf.NewLiveGCFClient(p.project)
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
