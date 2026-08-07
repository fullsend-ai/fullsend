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
	testClient        forge.Client
	testProvisioner   repos.InferenceProvisioner
	testMintRegistrar repos.MintRegistrar
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
  3. Register per-repo WIF in the mint's PER_REPO_WIF_REPOS
  4. Unenroll from per-org config

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
		clients = newForgeClientFactory("", repos.ForgeSection{})
	}

	var provisioner repos.InferenceProvisioner
	if cfg.testProvisioner != nil {
		provisioner = cfg.testProvisioner
	} else {
		provisioner = newGCPInferenceProvisioner(cfg.project)
	}

	var mintReg repos.MintRegistrar
	if cfg.testMintRegistrar != nil {
		mintReg = cfg.testMintRegistrar
	} else {
		mintReg = newGCPMintRegistrar(cfg.project)
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool) error {
		fc, fcErr := clients.ConfigFor(repos.ForgeGitHub)
		if fcErr != nil {
			return fcErr
		}
		targetRepo, repoErr := fc.Client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		commitMsg := "chore: initialize fullsend per-repo installation"
		prTitle := "chore: initialize fullsend per-repo installation"
		prBody := defaultScaffoldPRBody
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, commitMsg, prTitle, prBody, files, direct, nil)
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

	migrateCfg := repos.MigrateConfig{
		Org:            org,
		Project:        cfg.project,
		RepoFilter:     cfg.repoFilter,
		DryRun:         cfg.dryRun,
		Direct:         cfg.direct,
		MaxConcurrency: cfg.concurrency,
		ManifestPath:   cfg.manifest,
		UpstreamRef:    upstreamRef,
		UpstreamTag:    upstreamTag,
		CLIVersion:     version,
	}

	result, err := repos.Migrate(ctx, migrateCfg, clients, provisioner, mintReg, scaffoldCommitFn, progressFn)
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
		Short: "Set a forge-level default in the manifest",
		Long: fmt.Sprintf(`Set or remove a forge-level default in repos.yaml.

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

	clients := newForgeClientFactory(getGitLabToken(cmd), m.Forge)

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

func printStatusTable(cmd *cobra.Command, result *repos.StatusResult) {
	out := cmd.OutOrStdout()

	maxRepo := len("REPO")
	maxRef := len("REF")
	for _, s := range result.Repos {
		name := s.Owner + "/" + s.Repo
		if len(name) > maxRepo {
			maxRepo = len(name)
		}
		ref := s.CurrentRef
		if ref == "" {
			ref = "—"
		}
		if len(ref) > maxRef {
			maxRef = len(ref)
		}
	}

	fmt.Fprintf(out, "%-*s  %-*s  %-14s  %s\n", maxRepo, "REPO", maxRef, "REF", "STATUS", "DRIFT")
	for _, s := range result.Repos {
		name := s.Owner + "/" + s.Repo
		ref := s.CurrentRef
		if ref == "" {
			ref = "—"
		}

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

	// Test overrides
	testClient forge.Client
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
	cmd.Flags().StringVar(&opts.inferenceProject, "inference-project", "", "GCP project ID for inference (written as FULLSEND_GCP_PROJECT_ID secret)")
	cmd.Flags().StringVar(&opts.inferenceProjectNumber, "inference-project-number", "", "numeric GCP project number for WIF provider computation")
	cmd.Flags().StringVar(&opts.inferenceRegion, "inference-region", "", "GCP region for inference (install-time only, not stored in manifest)")
	cmd.Flags().StringVar(&opts.fullsendRef, "fullsend-ref", "", "per-repo fullsend workflow ref override")
	cmd.Flags().StringVar(&opts.mintURL, "mint-url", "", "per-repo mint URL override")
	cmd.Flags().StringSliceVar(&opts.allowedRemoteResources, "allowed-remote-resources", nil, "per-repo allowed remote resources override")
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
		printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", len(manifest.Repos)))
	}

	var clients repos.ForgeClientFactory
	if opts.testClient != nil {
		clients = newSingleClientFactory(opts.testClient)
	} else {
		clients = newForgeClientFactory(opts.gitlabToken, manifest.Forge)
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
				forgeName = manifest.Defaults.Forge
			}
			if forgeName == "" {
				return fmt.Errorf("--forge is required when adding repos not yet in the manifest")
			}

			if forgeName != repos.ForgeGitHub {
				for _, pair := range []struct{ flag, val string }{
					{"inference-region", opts.inferenceRegion},
					{"fullsend-ref", opts.fullsendRef},
					{"mint-url", opts.mintURL},
				} {
					if pair.val != "" {
						printer.StepWarn(fmt.Sprintf("--%s is only used with GitHub repos; ignored for %s", pair.flag, forgeName))
					}
				}
			}

			entries := make([]repos.RepoEntry, len(notInManifest))
			for i, r := range notInManifest {
				entry := repos.RepoEntry{Repo: r}
				if forgeName != manifest.Defaults.Forge {
					entry.Forge = repos.NullableString{Set: true, Value: forgeName}
				}
				if forgeName == repos.ForgeGitHub {
					if opts.fullsendRef != "" && opts.fullsendRef != manifest.Forge.GitHub.FullsendRef {
						entry.FullsendRef = repos.NullableString{Set: true, Value: opts.fullsendRef}
					}
					if opts.mintURL != "" && opts.mintURL != manifest.Forge.GitHub.MintURL {
						entry.MintURL = repos.NullableString{Set: true, Value: opts.mintURL}
					}
				}
				if len(opts.allowedRemoteResources) > 0 {
					entry.AllowedRemoteResources = opts.allowedRemoteResources
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
			}, entries, clients, addProgress)
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

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool) error {
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
		commitMsg := "chore: initialize fullsend per-repo installation"
		if rc.Forge == repos.ForgeGitLab {
			commitMsg += " [skip ci]"
		}
		prTitle := "chore: initialize fullsend per-repo installation"
		prBody := defaultScaffoldPRBody
		_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
			targetRepo.DefaultBranch, commitMsg, prTitle, prBody, files, direct, nil)
		return commitErr
	}

	// Phase 1: provision repos not yet installed.
	cfg := repos.BatchInstallConfig{
		Manifest:               manifest,
		DryRun:                 opts.dryRun,
		RepoFilter:             opts.repoFilter,
		MaxConcurrency:         opts.concurrency,
		Roles:                  opts.roles,
		UpstreamRef:            upstreamRef,
		UpstreamTag:            upstreamTag,
		Direct:                 opts.direct,
		InferenceProject:       opts.inferenceProject,
		InferenceProjectNumber: opts.inferenceProjectNumber,
		InferenceRegion:        opts.inferenceRegion,
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

	result, err := repos.BatchInstall(ctx, cfg, clients, scaffoldCommitFn, progressFn)
	if err != nil {
		return err
	}

	// GitLab post-install: set up bot token and pipeline schedules for
	// newly installed GitLab repos. Bot token failures are treated as install
	// failures because the repo is non-functional without FULLSEND_FORGE_TOKEN.
	var postInstallFailed int
	if !opts.dryRun && len(result.Installed) > 0 {
		for _, r := range result.Installed {
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

			// Build WIF config when inference is configured (WIF mode).
			var wifCfg *botTokenWIFConfig
			if opts.inferenceProject != "" {
				wifCfg = &botTokenWIFConfig{
					GCPClient: gcf.NewLiveGCFClient(opts.inferenceProject),
					ProjectID: opts.inferenceProject,
				}
			}

			_, botErr := setupGitLabBotToken(ctx, fc.Client, glClient, printer, r.Owner, r.Repo, opts.gitlabBotToken, wifCfg)
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

			_, schedErr := setupGitLabPipelineSchedules(ctx, fc.Client, glClient, printer, r.Owner, r.Repo, targetRepo.DefaultBranch)
			if schedErr != nil {
				printer.StepWarn(fmt.Sprintf("[%s] Pipeline schedule setup failed: %v", repoFullName, schedErr))
			}

			// Break stale resource group locks that may have been left by
			// cancelled or deleted pipelines during a previous install.
			healGitLabResourceGroups(ctx, glClient, printer, r.Owner, r.Repo)
		}
	}

	// Phase 2: converge already-installed repos (variable reconciliation + ref upgrade).
	var alreadyInstalled []repos.InstallResult
	for _, r := range result.Skipped {
		if r.AlreadyInstalled {
			alreadyInstalled = append(alreadyInstalled, r)
		}
	}

	var converged, alreadyCurrent int
	failedRepos := make(map[string]bool)
	convergedRepos := make(map[string]bool)

	if len(alreadyInstalled) > 0 {
		printer.Blank()
		if opts.dryRun {
			printer.StepStart("Checking installed repos for drift")
		} else {
			printer.StepStart("Reconciling installed repos")
		}

		repoNames := make([]string, len(alreadyInstalled))
		for i, r := range alreadyInstalled {
			repoNames[i] = r.Owner + "/" + r.Repo
		}

		if opts.dryRun {
			driftResult, driftErr := repos.Diff(ctx, manifest, clients, opts.concurrency, repoNames)
			if driftErr != nil {
				return driftErr
			}
			if len(driftResult.Changes) > 0 {
				for _, c := range driftResult.Changes {
					convergedRepos[c.Owner+"/"+c.Repo] = true
				}
				printer.StepInfo(fmt.Sprintf("  %d repos have %d variable changes", len(convergedRepos), len(driftResult.Changes)))
				converged = len(convergedRepos)
			}
		} else {
			reconcileResult, reconcileErr := repos.Sync(ctx, manifest, clients, opts.concurrency, repoNames, progressFn)
			if reconcileErr != nil && reconcileResult == nil {
				return reconcileErr
			}
			if reconcileErr != nil && reconcileResult != nil {
				printer.StepWarn(fmt.Sprintf("Variable reconciliation had errors: %v", reconcileErr))
			}
			if reconcileResult != nil {
				for _, c := range reconcileResult.Applied {
					convergedRepos[c.Owner+"/"+c.Repo] = true
				}
				converged += len(convergedRepos)
				for _, fr := range reconcileResult.FailedRepos {
					repoKey := fr.Owner + "/" + fr.Repo
					failedRepos[repoKey] = true
					printer.StepInfo(fmt.Sprintf("  FAILED: %s — variable reconciliation failed", repoKey))
				}
			}
		}

		upgradeCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, isDirect bool) error {
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
			commitMsg := "chore: upgrade fullsend scaffold ref"
			prTitle := "chore: upgrade fullsend scaffold ref"
			prBody := "This PR upgrades the fullsend scaffold workflow ref.\n\n" +
				"Merge this PR to activate the updated workflows."
			_, commitErr := layers.CommitScaffoldFiles(ctx, fc.Client, printer, owner, repo,
				targetRepo.DefaultBranch, commitMsg, prTitle, prBody, files, isDirect, nil)
			return commitErr
		}

		upgradeRepoNames := repoNames
		if len(failedRepos) > 0 {
			upgradeRepoNames = make([]string, 0, len(repoNames))
			for _, name := range repoNames {
				if !failedRepos[name] {
					upgradeRepoNames = append(upgradeRepoNames, name)
				}
			}
		}

		if len(upgradeRepoNames) > 0 {
			upgradeCfg := repos.UpgradeConfig{
				Manifest:       manifest,
				RepoFilter:     upgradeRepoNames,
				DryRun:         opts.dryRun,
				Force:          opts.force,
				Direct:         opts.direct,
				MaxConcurrency: opts.concurrency,
			}

			upgradeResults, upgradeErr := repos.Upgrade(ctx, upgradeCfg, clients, upgradeCommitFn, progressFn)
			if upgradeErr != nil {
				return upgradeErr
			}
			for _, r := range upgradeResults {
				repoKey := r.Owner + "/" + r.Repo
				switch {
				case r.Error != nil:
					failedRepos[repoKey] = true
					printer.StepInfo(fmt.Sprintf("  FAILED: %s — %v", repoKey, r.Error))
				case r.Upgraded:
					if !convergedRepos[repoKey] {
						converged++
					}
					convergedRepos[repoKey] = true
				case r.Skipped:
					if !convergedRepos[repoKey] {
						alreadyCurrent++
					}
				}
			}
		}
	}

	printer.Blank()
	installed := len(result.Installed) - postInstallFailed
	failed := len(result.Failed) + len(failedRepos) + postInstallFailed

	for _, r := range result.Failed {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}

	printer.StepDone(fmt.Sprintf("Install complete: %d installed, %d converged, %d already current, %d failed",
		installed, converged, alreadyCurrent, failed))

	if failed > 0 {
		return fmt.Errorf("%d repos failed", failed)
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

	testClient           forge.Client
	testGCPClientFactory func(projectID string) gcf.GCFClient
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
	printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", len(manifest.Repos)))

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
		clients = newForgeClientFactory(opts.gitlabToken, manifest.Forge)
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

		// Pre-uninstall: gather GCP project IDs for GitLab WIF repos
		// so we can delete Secret Manager secrets after teardown. The
		// FULLSEND_SA variable is deleted during uninstall, so we read
		// it now.
		gcpProjectByRepo := make(map[string]string)
		if !opts.dryRun {
			for _, repoName := range concreteRepos {
				parts := strings.SplitN(repoName, "/", 2)
				if len(parts) != 2 {
					continue
				}
				owner, repo := parts[0], parts[1]
				rc, ok := manifest.ResolveConfigWithGlobs(owner, repo)
				if !ok || rc.Forge != repos.ForgeGitLab {
					continue
				}
				fc, fcErr := clients.ConfigFor(repos.ForgeGitLab)
				if fcErr != nil {
					continue
				}
				sa, found, readErr := fc.Client.GetRepoVariable(ctx, owner, repo, "FULLSEND_SA")
				if readErr != nil || !found {
					continue
				}
				if projectID := projectIDFromSAEmail(sa); projectID != "" {
					gcpProjectByRepo[repoName] = projectID
				}
			}
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

		// GitLab post-uninstall: clean up pipeline schedules, bot tokens,
		// and Secret Manager secrets.
		if !opts.dryRun {
			newGCPClient := opts.testGCPClientFactory
			if newGCPClient == nil {
				newGCPClient = func(pid string) gcf.GCFClient { return gcf.NewLiveGCFClient(pid) }
			}

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

				// Best-effort: delete the bot token Secret Manager
				// secret if we know the GCP project from the pre-
				// uninstall variable read.
				if projectID, ok := gcpProjectByRepo[repoFullName]; ok {
					cleanupGitLabBotTokenSecret(ctx, newGCPClient(projectID), printer, projectID, r.Owner, r.Repo)
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

// gcpMintRegistrar implements repos.MintRegistrar by calling
// the GCF provisioner's RegisterPerRepoWIF.
type gcpMintRegistrar struct {
	provisioner *gcf.Provisioner
}

func newGCPMintRegistrar(project string) *gcpMintRegistrar {
	gcpClient := gcf.NewLiveGCFClient(project)
	return &gcpMintRegistrar{
		provisioner: gcf.NewProvisioner(gcf.Config{
			ProjectID: project,
		}, gcpClient),
	}
}

func (m *gcpMintRegistrar) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	return m.provisioner.RegisterPerRepoWIF(ctx, repo)
}
