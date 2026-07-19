package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/layers"
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
	cmd.AddCommand(newReposInitCmd())
	cmd.AddCommand(newReposAddCmd())
	cmd.AddCommand(newReposRemoveCmd())
	cmd.AddCommand(newReposInstallCmd())
	cmd.AddCommand(newReposUninstallCmd())
	cmd.AddCommand(newReposStatusCmd())
	cmd.AddCommand(newReposDiffCmd())
	cmd.AddCommand(newReposSyncCmd())
	cmd.AddCommand(newReposUpgradeCmd())
	cmd.AddCommand(newReposUpgradeMintCmd())
	return cmd
}

type reposInitConfig struct {
	output           string
	repoNames        string
	all              bool
	mintProject      string
	mintRegion       string
	inferenceProject string
	concurrency      int
	force            bool
}

func newReposInitCmd() *cobra.Command {
	var cfg reposInitConfig

	cmd := &cobra.Command{
		Use:   "init <org|owner/repo>",
		Short: "Generate a repos.yaml manifest by discovering existing installations",
		Long: `Discovers existing fullsend installations (per-repo and per-org) and
generates a repos.yaml manifest reflecting their current state.

For greenfield onboarding, select which repos to include and the command
generates a manifest with default config. For migration from existing
installations, the command discovers their state and generates a manifest
that reflects current reality.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			token, err := resolveToken()
			if err != nil {
				return err
			}
			client := gh.New(token)
			printerOut := os.Stdout
			if cfg.output == "-" {
				printerOut = os.Stderr
			}
			printer := ui.New(printerOut)
			printer.Banner(Version())
			ctx := cmd.Context()

			owner := target
			if idx := strings.IndexByte(target, '/'); idx >= 0 {
				owner = target[:idx]
			}
			if err := validateOrgName(owner); err != nil {
				return err
			}

			initCfg := repos.InitConfig{
				Target:           target,
				All:              cfg.all,
				MintProject:      cfg.mintProject,
				MintRegion:       cfg.mintRegion,
				InferenceProject: cfg.inferenceProject,
				MaxConcurrency:   cfg.concurrency,
				CLIVersion:       version,
			}

			if cfg.repoNames != "" {
				parts := strings.Split(cfg.repoNames, ",")
				for i, p := range parts {
					parts[i] = strings.TrimSpace(p)
				}
				initCfg.Repos = parts
			}

			progress := func(repo, phase, message string) {
				printer.StepInfo(fmt.Sprintf("[%s] %s: %s", phase, repo, message))
			}

			result, err := repos.Init(ctx, initCfg, client, nil, progress)
			if err != nil {
				return err
			}

			data, err := repos.MarshalWithHeader(result.Manifest)
			if err != nil {
				return err
			}

			if cfg.output == "-" {
				fmt.Print(string(data))
			} else {
				if !cfg.force {
					if _, statErr := os.Stat(cfg.output); statErr == nil {
						return fmt.Errorf("output file %s already exists (use --force to overwrite)", cfg.output)
					} else if !errors.Is(statErr, os.ErrNotExist) {
						return fmt.Errorf("checking output file: %w", statErr)
					}
				}
				if writeErr := os.WriteFile(cfg.output, data, 0o644); writeErr != nil {
					return fmt.Errorf("writing manifest: %w", writeErr)
				}
				printer.StepDone(fmt.Sprintf("Manifest written to %s", cfg.output))
			}

			printer.Blank()
			printer.StepInfo(fmt.Sprintf("Discovered: %d per-repo, %d per-org, %d new",
				result.PerRepoCount, result.PerOrgCount, result.NewCount))

			if len(result.Errors) > 0 {
				printer.Blank()
				printer.StepWarn(fmt.Sprintf("Discovery failed for %d repos (excluded from manifest):", len(result.Errors)))
				for _, e := range result.Errors {
					printer.StepWarn("- " + e)
				}
			}

			if len(result.TODOs) > 0 {
				printer.Blank()
				printer.StepInfo("TODOs (fields requiring manual attention):")
				for _, todo := range result.TODOs {
					printer.StepInfo("- " + todo)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&cfg.output, "output", "o", "repos.yaml", "output path (use - for stdout)")
	cmd.Flags().StringVar(&cfg.repoNames, "repos", "", "comma-separated list of repos to include")
	cmd.Flags().BoolVar(&cfg.all, "all", false, "include all eligible repos without prompting")
	cmd.Flags().StringVar(&cfg.mintProject, "mint-project", "", "GCP project for the mint")
	cmd.Flags().StringVar(&cfg.mintRegion, "mint-region", "us-central1", "GCP region for the mint")
	cmd.Flags().StringVar(&cfg.inferenceProject, "inference-project", "", "default GCP project for inference")
	cmd.Flags().IntVar(&cfg.concurrency, "concurrency", 8, "max parallel API calls (capped at 64)")
	cmd.Flags().BoolVar(&cfg.force, "force", false, "overwrite output file if it already exists")
	cmd.MarkFlagsMutuallyExclusive("repos", "all")

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

	token, err := resolveToken()
	if err != nil {
		return err
	}
	client := newGitHubLiveClient(token)

	m, err := repos.LoadManifest(ctx, manifestPath)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}

	result, err := repos.Status(ctx, m, client, concurrency, repoFilter)
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
}

// reposInstallConfig holds flag values and testing overrides for repos install.
type reposInstallConfig struct {
	manifest      string
	dryRun        bool
	repoFilter    []string
	skipMintCheck bool
	concurrency   int
	roles         []string
	direct        bool

	// Testing overrides — when non-nil, used instead of resolving from
	// the environment. Not set by CLI flag parsing.
	testClient      forge.Client
	testProvisioner repos.WIFProvisioner
}

func newReposInstallCmd() *cobra.Command {
	opts := &reposInstallConfig{}

	cmd := &cobra.Command{
		Use:   "install [repos...]",
		Short: "Install fullsend on repos defined in a manifest",
		Long: `Install fullsend on repos not yet installed, as defined in a repos.yaml manifest.

When repos are specified as positional arguments, only those repos are installed.
Glob patterns (e.g. "acme/*") are matched against manifest entries.
When no repos are specified, all manifest repos are installed.

Runs in three phases:
  1. Parallel discovery: check which repos are already installed
  2. Sequential WIF: provision WIF infrastructure per repo (not concurrent-safe)
  3. Parallel scaffold: commit scaffold files and write variables/secrets`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.repoFilter = args
			return runReposInstall(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be installed without making changes")
	cmd.Flags().BoolVar(&opts.skipMintCheck, "skip-mint-check", false, "skip mint URL discovery and org registration (EnsureOrgInMint)")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")
	cmd.Flags().StringSliceVar(&opts.roles, "roles", config.PerRepoDefaultRoles(), "agent roles to install")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push scaffold directly to default branch (skip PR)")

	return cmd
}

func runReposInstall(ctx context.Context, opts *reposInstallConfig) error {
	if opts.concurrency < 1 || opts.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}

	printer := ui.New(os.Stdout)

	printer.StepStart("Loading manifest")
	manifest, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", len(manifest.Repos)))

	var client forge.Client
	if opts.testClient != nil {
		client = opts.testClient
	} else {
		token, tokenErr := resolveToken()
		if tokenErr != nil {
			return tokenErr
		}
		client = newGitHubLiveClient(token)
	}

	if err := checkPerRepoScopes(ctx, client, printer); err != nil {
		return err
	}

	upstreamRef, upstreamTag := resolveUpstreamRef()

	provisionerFactory := func(resolved repos.ResolvedConfig) repos.WIFProvisioner {
		if opts.testProvisioner != nil {
			return opts.testProvisioner
		}
		mintProv := &gcfProvisionerAdapter{
			provisioner: gcf.NewProvisioner(gcf.Config{
				ProjectID:   resolved.MintProject,
				Region:      resolved.MintRegion,
				GitHubOrgs:  []string{resolved.Owner},
				Repo:        resolved.Owner + "/" + resolved.Repo,
				WIFPoolName: gcf.DefaultInferencePool,
				MintURL:     resolved.MintURL,
			}, gcf.NewLiveGCFClient(resolved.MintProject)),
		}
		wifProv := &gcfProvisionerAdapter{
			provisioner: gcf.NewProvisioner(gcf.Config{
				ProjectID:   resolved.InferenceProject,
				Region:      resolved.InferenceRegion,
				GitHubOrgs:  []string{resolved.Owner},
				Repo:        resolved.Owner + "/" + resolved.Repo,
				WIFPoolName: gcf.DefaultInferencePool,
			}, gcf.NewLiveGCFClient(resolved.InferenceProject)),
		}
		return &splitProjectAdapter{mint: mintProv, inference: wifProv}
	}

	scaffoldCommitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool) error {
		targetRepo, repoErr := client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		commitMsg := "chore: initialize fullsend per-repo installation"
		prTitle := "chore: initialize fullsend per-repo installation"
		prBody := "This PR adds the fullsend scaffold files for per-repo installation.\n\n" +
			"Merge this PR to activate fullsend workflows."
		_, commitErr := layers.CommitScaffoldFiles(ctx, client, printer, owner, repo,
			targetRepo.DefaultBranch, commitMsg, prTitle, prBody, files, direct, nil)
		return commitErr
	}

	cfg := repos.BatchInstallConfig{
		Manifest:       manifest,
		DryRun:         opts.dryRun,
		RepoFilter:     opts.repoFilter,
		MaxConcurrency: opts.concurrency,
		SkipMintCheck:  opts.skipMintCheck,
		Roles:          opts.roles,
		UpstreamRef:    upstreamRef,
		UpstreamTag:    upstreamTag,
		Direct:         opts.direct,
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "org-mint":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		case "done":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	printer.Blank()
	if opts.dryRun {
		printer.StepStart("Dry-run: previewing installation")
	} else {
		printer.StepStart("Installing fullsend on manifest repos")
	}

	result, err := repos.BatchInstall(ctx, cfg, client, provisionerFactory, scaffoldCommitFn, progressFn)
	if err != nil {
		return err
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Batch install complete: %d installed, %d skipped, %d failed",
		len(result.Installed), len(result.Skipped), len(result.Failed)))

	for _, r := range result.Failed {
		printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
	}

	if len(result.Failed) > 0 {
		return fmt.Errorf("%d repos failed to install", len(result.Failed))
	}
	return nil
}

// reposAddConfig holds flag values for repos add.
type reposAddConfig struct {
	manifest    string
	dryRun      bool
	install     bool
	concurrency int
	direct      bool
	roles       []string

	testClient      forge.Client
	testProvisioner repos.WIFProvisioner
}

func newReposAddCmd() *cobra.Command {
	opts := &reposAddConfig{}

	cmd := &cobra.Command{
		Use:   "add <repos...>",
		Short: "Add repo entries to a repos.yaml manifest",
		Long: `Add one or more repo entries to the repos.yaml manifest file, editing it
in place.

Use --install to also install fullsend on the added repos after updating
the manifest.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposAdd(cmd.Context(), opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be added without making changes")
	cmd.Flags().BoolVar(&opts.install, "install", false, "also install fullsend on the added repos")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push scaffold directly to default branch (skip PR)")
	cmd.Flags().StringSliceVar(&opts.roles, "roles", config.PerRepoDefaultRoles(), "agent roles to install (used with --install)")

	return cmd
}

func runReposAdd(ctx context.Context, opts *reposAddConfig, repoArgs []string) error {
	if opts.install && (opts.concurrency < 1 || opts.concurrency > 32) {
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

	var client forge.Client
	if opts.testClient != nil {
		client = opts.testClient
	} else {
		token, tokenErr := resolveToken()
		if tokenErr != nil {
			return tokenErr
		}
		client = newGitHubLiveClient(token)
	}

	entries := make([]repos.RepoEntry, len(repoArgs))
	for i, r := range repoArgs {
		entries[i] = repos.RepoEntry{Repo: r}
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "done", "manifest":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	result, _, err := repos.AddToManifest(ctx, repos.ManifestEditConfig{
		Manifest:     manifest,
		ManifestPath: opts.manifest,
		DryRun:       opts.dryRun,
	}, entries, client, progressFn)
	if err != nil {
		return err
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Add complete: %d added, %d skipped", len(result.Added), len(result.Skipped)))

	if opts.install && len(result.Added) > 0 && !opts.dryRun {
		printer.Blank()
		printer.StepStart("Installing fullsend on added repos")
		installOpts := &reposInstallConfig{
			manifest:        opts.manifest,
			repoFilter:      result.Added,
			concurrency:     opts.concurrency,
			direct:          opts.direct,
			roles:           opts.roles,
			testClient:      opts.testClient,
			testProvisioner: opts.testProvisioner,
		}
		if err := runReposInstall(ctx, installOpts); err != nil {
			return err
		}
	}

	return nil
}

// reposRemoveConfig holds flag values for repos remove.
type reposRemoveConfig struct {
	manifest       string
	dryRun         bool
	uninstall      bool
	yes            bool
	skipWIFCleanup bool
	concurrency    int

	testClient      forge.Client
	testProvisioner repos.WIFProvisioner
}

func newReposRemoveCmd() *cobra.Command {
	opts := &reposRemoveConfig{}

	cmd := &cobra.Command{
		Use:   "remove <repos...>",
		Short: "Remove repo entries from a repos.yaml manifest",
		Long: `Remove one or more repo entries from the repos.yaml manifest file,
editing it in place.

Glob patterns (e.g. "acme/*") are matched against manifest entries and
prompt for confirmation unless --yes is set.

Use --uninstall to tear down fullsend from the repos before removing them
from the manifest (deletes workflow, variables, secrets, and WIF).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposRemove(cmd.Context(), opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be removed without making changes")
	cmd.Flags().BoolVar(&opts.uninstall, "uninstall", false, "tear down fullsend from repos before removing from manifest")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip confirmation prompt when multiple repos are targeted")
	cmd.Flags().BoolVar(&opts.skipWIFCleanup, "skip-wif-cleanup", false, "skip GCP WIF provider deletion (only with --uninstall)")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")

	return cmd
}

func runReposRemove(ctx context.Context, opts *reposRemoveConfig, repoArgs []string) error {
	if opts.uninstall && (opts.concurrency < 1 || opts.concurrency > 32) {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}

	printer := ui.New(os.Stdout)
	var uninstallFailed int

	printer.StepStart("Loading manifest")
	manifest, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", len(manifest.Repos)))

	if !opts.yes && !opts.dryRun {
		action := "remove"
		if opts.uninstall {
			action = "remove and uninstall"
		}
		if err := confirmBulkAction(printer, action, repoArgs, manifest, os.Stdin); err != nil {
			return err
		}
	}

	if opts.uninstall {
		matched, matchErr := repos.MatchManifestRepos(manifest, repoArgs)
		if matchErr != nil {
			return matchErr
		}
		var concreteRepos []string
		for _, r := range matched {
			if strings.ContainsAny(r, "*?[") {
				printer.StepInfo(fmt.Sprintf("[%s] Skipping glob manifest entry (use concrete repo names to uninstall)", r))
				continue
			}
			concreteRepos = append(concreteRepos, r)
		}
		if len(concreteRepos) > 0 {
			printer.Blank()
			if opts.dryRun {
				printer.StepStart("Previewing uninstall for repos")
			} else {
				printer.StepStart("Uninstalling fullsend from repos before removing from manifest")
			}

			var client forge.Client
			if opts.testClient != nil {
				client = opts.testClient
			} else if !opts.dryRun {
				token, tokenErr := resolveToken()
				if tokenErr != nil {
					return tokenErr
				}
				client = newGitHubLiveClient(token)
			}

			uninstallCfg := repos.UninstallConfig{
				Manifest:       manifest,
				Repos:          concreteRepos,
				DryRun:         opts.dryRun,
				SkipWIFCleanup: opts.skipWIFCleanup,
				MaxConcurrency: opts.concurrency,
			}
			provFactory := buildProvisionerFactory(opts.testProvisioner, opts.skipWIFCleanup)
			progressFn := func(repo, phase, msg string) {
				switch phase {
				case "done", "wif":
					printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
				default:
					printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
				}
			}
			results, uninstallErr := repos.Uninstall(ctx, uninstallCfg, client, provFactory, progressFn)
			if uninstallErr != nil {
				return uninstallErr
			}
			var succeededRepos []string
			for _, r := range results {
				if r.Success {
					succeededRepos = append(succeededRepos, r.Owner+"/"+r.Repo)
				} else {
					uninstallFailed++
					printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
				}
			}
			if uninstallFailed > 0 {
				if len(succeededRepos) > 0 {
					repoArgs = succeededRepos
				} else {
					return fmt.Errorf("%d repos failed to uninstall, manifest unchanged", uninstallFailed)
				}
			}
		}
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "done", "manifest":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	result, _, err := repos.RemoveFromManifest(repos.ManifestEditConfig{
		Manifest:     manifest,
		ManifestPath: opts.manifest,
		DryRun:       opts.dryRun,
	}, repoArgs, progressFn)
	if err != nil {
		return err
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Remove complete: %d removed, %d skipped", len(result.Removed), len(result.Skipped)))

	if opts.uninstall && uninstallFailed > 0 {
		return fmt.Errorf("%d repos failed to uninstall (successfully uninstalled repos were removed from manifest)", uninstallFailed)
	}
	return nil
}

// reposUninstallConfig holds flag values for repos uninstall.
type reposUninstallConfig struct {
	manifest       string
	dryRun         bool
	yes            bool
	skipWIFCleanup bool
	concurrency    int

	testClient      forge.Client
	testProvisioner repos.WIFProvisioner
}

func newReposUninstallCmd() *cobra.Command {
	opts := &reposUninstallConfig{}

	cmd := &cobra.Command{
		Use:   "uninstall <repos...>",
		Short: "Tear down fullsend from specific repos",
		Long: `Tear down fullsend from the specified repos by deleting workflow files,
variables, secrets, and WIF infrastructure.

Does NOT modify repos.yaml — use "repos remove" for that.

Glob patterns (e.g. "acme/*") are matched against manifest entries and
prompt for confirmation unless --yes is set.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposUninstall(cmd.Context(), opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be uninstalled without making changes")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip confirmation prompt when multiple repos are targeted")
	cmd.Flags().BoolVar(&opts.skipWIFCleanup, "skip-wif-cleanup", false, "skip GCP WIF provider deletion and mint deregistration")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")

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

	if !opts.yes && !opts.dryRun {
		if err := confirmBulkAction(printer, "uninstall", repoArgs, manifest, os.Stdin); err != nil {
			return err
		}
	}

	var client forge.Client
	if opts.testClient != nil {
		client = opts.testClient
	} else {
		token, tokenErr := resolveToken()
		if tokenErr != nil {
			return tokenErr
		}
		client = newGitHubLiveClient(token)
	}

	if err := checkPerRepoScopes(ctx, client, printer); err != nil {
		return err
	}

	provFactory := buildProvisionerFactory(opts.testProvisioner, opts.skipWIFCleanup)

	cfg := repos.UninstallConfig{
		Manifest:       manifest,
		Repos:          concreteRepos,
		DryRun:         opts.dryRun,
		SkipWIFCleanup: opts.skipWIFCleanup,
		MaxConcurrency: opts.concurrency,
	}

	progressFn := func(repo, phase, msg string) {
		switch phase {
		case "done", "wif":
			printer.StepDone(fmt.Sprintf("[%s] %s", repo, msg))
		default:
			printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
		}
	}

	printer.Blank()
	if opts.dryRun {
		printer.StepStart("Dry-run: previewing uninstall")
	} else {
		printer.StepStart("Uninstalling fullsend from repos")
	}

	results, err := repos.Uninstall(ctx, cfg, client, provFactory, progressFn)
	if err != nil {
		return err
	}

	var succeeded, failed int
	for _, r := range results {
		if r.Success {
			succeeded++
		} else {
			failed++
		}
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Uninstall complete: %d uninstalled, %d failed", succeeded, failed))

	for _, r := range results {
		if !r.Success {
			printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d repos failed to uninstall", failed)
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

// buildProvisionerFactory creates a ProvisionerFactory for uninstall operations.
// When skipWIF is true or testProv is non-nil, shortcuts are used.
func buildProvisionerFactory(testProv repos.WIFProvisioner, skipWIF bool) repos.ProvisionerFactory {
	if skipWIF {
		return nil
	}
	return func(resolved repos.ResolvedConfig) repos.WIFProvisioner {
		if testProv != nil {
			return testProv
		}
		mintProv := &gcfProvisionerAdapter{
			provisioner: gcf.NewProvisioner(gcf.Config{
				ProjectID:   resolved.MintProject,
				Region:      resolved.MintRegion,
				GitHubOrgs:  []string{resolved.Owner},
				Repo:        resolved.Owner + "/" + resolved.Repo,
				WIFPoolName: gcf.DefaultInferencePool,
				MintURL:     resolved.MintURL,
			}, gcf.NewLiveGCFClient(resolved.MintProject)),
		}
		wifProv := &gcfProvisionerAdapter{
			provisioner: gcf.NewProvisioner(gcf.Config{
				ProjectID:   resolved.InferenceProject,
				Region:      resolved.InferenceRegion,
				GitHubOrgs:  []string{resolved.Owner},
				Repo:        resolved.Owner + "/" + resolved.Repo,
				WIFPoolName: gcf.DefaultInferencePool,
			}, gcf.NewLiveGCFClient(resolved.InferenceProject)),
		}
		return &splitProjectAdapter{mint: mintProv, inference: wifProv}
	}
}

// splitProjectAdapter routes WIFProvisioner methods to the correct GCP project:
// ProvisionWIF targets the inference project (IAM resources) while mint
// operations target the mint project (Cloud Function env vars).
type splitProjectAdapter struct {
	mint      repos.WIFProvisioner
	inference repos.WIFProvisioner
}

func (s *splitProjectAdapter) DiscoverMint(ctx context.Context) (*repos.MintDiscovery, error) {
	return s.mint.DiscoverMint(ctx)
}

func (s *splitProjectAdapter) ProvisionWIF(ctx context.Context) (string, error) {
	return s.inference.ProvisionWIF(ctx)
}

func (s *splitProjectAdapter) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	return s.mint.RegisterPerRepoWIF(ctx, repo)
}

func (s *splitProjectAdapter) EnsureOrgInMint(ctx context.Context, expectedURL string, org string) error {
	return s.mint.EnsureOrgInMint(ctx, expectedURL, org)
}

func (s *splitProjectAdapter) DeletePerRepoWIF(ctx context.Context, repo string) error {
	if err := s.mint.DeletePerRepoWIF(ctx, repo); err != nil {
		return fmt.Errorf("deregistering from mint: %w", err)
	}
	if err := s.inference.DeleteWIFProvider(ctx, repo); err != nil {
		return fmt.Errorf("deleting WIF provider for %s (mint deregistration already succeeded — re-run is safe): %w", repo, err)
	}
	return nil
}

func (s *splitProjectAdapter) DeleteWIFProvider(ctx context.Context, repo string) error {
	return s.inference.DeleteWIFProvider(ctx, repo)
}

type reposDiffConfig struct {
	manifest    string
	repoFilter  []string
	jsonOutput  bool
	concurrency int

	testClient forge.Client
	out        io.Writer
}

func newReposDiffCmd() *cobra.Command {
	opts := &reposDiffConfig{}

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show configuration drift between manifest and actual state",
		Long:  "Compare the repos.yaml manifest against actual forge state and display the changes needed to reconcile. Exit code 1 signals drift exists.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposDiff(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or HTTPS URL to manifest file")
	cmd.Flags().StringArrayVar(&opts.repoFilter, "repo", nil, "filter to specific repos (repeatable)")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit JSON output instead of table")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 8, "max parallel API calls")

	return cmd
}

func runReposDiff(ctx context.Context, opts *reposDiffConfig) error {
	out := opts.out
	if out == nil {
		out = os.Stdout
	}
	printerOut := out
	if opts.jsonOutput {
		printerOut = io.Discard
	}
	printer := ui.New(printerOut)

	var client forge.Client
	if opts.testClient != nil {
		client = opts.testClient
	} else {
		token, err := resolveToken()
		if err != nil {
			return err
		}
		client = newGitHubLiveClient(token)
	}

	if err := checkPerRepoScopes(ctx, client, printer); err != nil {
		return err
	}

	m, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}

	result, err := repos.Diff(ctx, m, client, opts.concurrency, opts.repoFilter)
	if err != nil {
		return err
	}

	if opts.jsonOutput {
		b, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("marshalling JSON: %w", marshalErr)
		}
		fmt.Fprintln(out, string(b))
	} else {
		fmt.Fprint(out, repos.FormatDiffTable(result))
	}

	if len(result.Changes) > 0 {
		return fmt.Errorf("%d changes needed to reconcile manifest", len(result.Changes))
	}
	return nil
}

type reposSyncConfig struct {
	manifest    string
	repoFilter  []string
	dryRun      bool
	jsonOutput  bool
	concurrency int

	testClient forge.Client
	out        io.Writer
}

func newReposSyncCmd() *cobra.Command {
	opts := &reposSyncConfig{}

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile configuration drift for installed repos",
		Long:  "Apply variable and secret changes to reconcile installed repos with the manifest. Use --dry-run to preview changes without applying them.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposSync(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or HTTPS URL to manifest file")
	cmd.Flags().StringArrayVar(&opts.repoFilter, "repo", nil, "filter to specific repos (repeatable)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview changes without applying them")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit JSON output instead of table")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations (1-32)")

	return cmd
}

func runReposSync(ctx context.Context, opts *reposSyncConfig) error {
	out := opts.out
	if out == nil {
		out = os.Stdout
	}
	printerOut := out
	if opts.jsonOutput {
		printerOut = io.Discard
	}
	printer := ui.New(printerOut)

	var client forge.Client
	if opts.testClient != nil {
		client = opts.testClient
	} else {
		token, err := resolveToken()
		if err != nil {
			return err
		}
		client = newGitHubLiveClient(token)
	}

	if err := checkPerRepoScopes(ctx, client, printer); err != nil {
		return err
	}

	m, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}

	if opts.dryRun {
		result, diffErr := repos.Diff(ctx, m, client, opts.concurrency, opts.repoFilter)
		if diffErr != nil {
			return diffErr
		}

		if opts.jsonOutput {
			b, marshalErr := json.MarshalIndent(result, "", "  ")
			if marshalErr != nil {
				return fmt.Errorf("marshalling JSON: %w", marshalErr)
			}
			fmt.Fprintln(out, string(b))
		} else {
			fmt.Fprint(out, repos.FormatDiffTable(result))
		}
		if len(result.Changes) > 0 {
			return fmt.Errorf("%d changes needed to reconcile manifest", len(result.Changes))
		}
		return nil
	}

	var progressFn repos.ProgressFunc
	if !opts.jsonOutput {
		progressFn = func(repo, phase, message string) {
			printer.StepInfo(fmt.Sprintf("[%s] %s: %s", phase, repo, message))
		}
	}

	result, err := repos.Sync(ctx, m, client, opts.concurrency, opts.repoFilter, progressFn)
	if err != nil && result == nil {
		return err
	}

	if opts.jsonOutput {
		b, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("marshalling JSON: %w", marshalErr)
		}
		fmt.Fprintln(out, string(b))
	} else {
		if result.Failed > 0 {
			fmt.Fprintf(out, "Applied %d changes, %d repos failed.\n", len(result.Applied), result.Failed)
		} else {
			fmt.Fprintf(out, "Applied %d changes.\n", len(result.Applied))
		}
		for _, w := range result.Warnings {
			fmt.Fprintf(out, "WARNING: %s\n", w)
		}
	}

	return err
}

// reposUpgradeConfig holds flag values and testing overrides for repos upgrade.
type reposUpgradeConfig struct {
	manifest    string
	refOverride string
	dryRun      bool
	force       bool
	concurrency int
	direct      bool

	// Testing overrides — when non-nil, used instead of resolving from
	// the environment. Not set by CLI flag parsing.
	testClient forge.Client
}

func newReposUpgradeCmd() *cobra.Command {
	opts := &reposUpgradeConfig{}

	cmd := &cobra.Command{
		Use:   "upgrade [repos...]",
		Short: "Upgrade scaffold shim ref across repos",
		Long: `Upgrades the fullsend scaffold workflow ref for repos defined in a repos.yaml manifest.

When repos are specified as positional arguments (owner/repo format), only those
repos are upgraded. When no repos are specified, all manifest repos are upgraded.

Reads each repo's current workflow file, compares against the manifest's
fullsend_ref (or --ref override), and commits the updated workflow.

Floating refs (latest, main, v0) are skipped. Downgrades are blocked
unless --force is set.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposUpgrade(cmd.Context(), opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or HTTPS URL to manifest file")
	cmd.Flags().StringVar(&opts.refOverride, "ref", "", "override manifest fullsend_ref for all repos")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be upgraded without making changes")
	cmd.Flags().BoolVar(&opts.force, "force", false, "upgrade even if current ref is newer than target")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "max parallel operations")
	cmd.Flags().BoolVar(&opts.direct, "direct", false, "push scaffold directly to default branch (skip PR)")

	return cmd
}

func runReposUpgrade(ctx context.Context, opts *reposUpgradeConfig, repoFilter []string) error {
	printer := ui.New(os.Stdout)

	if opts.concurrency < 1 || opts.concurrency > 32 {
		return fmt.Errorf("--concurrency must be between 1 and 32, got %d", opts.concurrency)
	}

	if opts.refOverride != "" && !repos.IsValidRef(opts.refOverride) {
		return fmt.Errorf("--ref value %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", opts.refOverride)
	}

	printer.StepStart("Loading manifest")
	m, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Loaded manifest with %d repo entries", len(m.Repos)))

	var client forge.Client
	if opts.testClient != nil {
		client = opts.testClient
	} else {
		token, tokenErr := resolveToken()
		if tokenErr != nil {
			return tokenErr
		}
		client = newGitHubLiveClient(token)
	}

	if err := checkPerRepoScopes(ctx, client, printer); err != nil {
		return err
	}

	commitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, isDirect bool) error {
		targetRepo, repoErr := client.GetRepo(ctx, owner, repo)
		if repoErr != nil {
			return fmt.Errorf("getting repo info: %w", repoErr)
		}
		commitMsg := "chore: upgrade fullsend scaffold ref"
		prTitle := "chore: upgrade fullsend scaffold ref"
		prBody := "This PR upgrades the fullsend scaffold workflow ref.\n\n" +
			"Merge this PR to activate the updated workflows."
		_, commitErr := layers.CommitScaffoldFiles(ctx, client, printer, owner, repo,
			targetRepo.DefaultBranch, commitMsg, prTitle, prBody, files, isDirect, nil)
		return commitErr
	}

	cfg := repos.UpgradeConfig{
		Manifest:       m,
		RefOverride:    opts.refOverride,
		RepoFilter:     repoFilter,
		DryRun:         opts.dryRun,
		Force:          opts.force,
		Direct:         opts.direct,
		MaxConcurrency: opts.concurrency,
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
		printer.StepStart("Dry-run: previewing upgrades")
	} else {
		printer.StepStart("Upgrading repos")
	}

	results, err := repos.Upgrade(ctx, cfg, client, commitFn, progressFn)
	if err != nil {
		return err
	}

	var upgraded, skipped, failed int
	for _, r := range results {
		switch {
		case r.Error != nil:
			failed++
			printer.StepInfo(fmt.Sprintf("  FAILED: %s/%s — %v", r.Owner, r.Repo, r.Error))
		case r.Upgraded:
			upgraded++
		case r.Skipped:
			skipped++
		}
	}

	printer.Blank()
	printer.StepDone(fmt.Sprintf("Upgrade complete: %d upgraded, %d skipped, %d failed",
		upgraded, skipped, failed))

	if failed > 0 {
		return fmt.Errorf("%d repos failed to upgrade", failed)
	}
	return nil
}

// reposUpgradeMintConfig holds flag values and testing overrides for repos upgrade-mint.
type reposUpgradeMintConfig struct {
	manifest string

	// Testing overrides — when non-nil, used instead of resolving from
	// the environment. Not set by CLI flag parsing.
	testProvisioner repos.WIFProvisioner
}

func newReposUpgradeMintCmd() *cobra.Command {
	opts := &reposUpgradeMintConfig{}

	cmd := &cobra.Command{
		Use:   "upgrade-mint",
		Short: "Verify the token mint deployment",
		Long: `Verifies the token mint Cloud Function matches the manifest configuration.

Discovers the current mint deployment and checks that its URL matches
the manifest's mint.url. Run this before 'repos upgrade' to ensure
mint compatibility.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposUpgradeMint(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or HTTPS URL to manifest file")

	return cmd
}

func runReposUpgradeMint(ctx context.Context, opts *reposUpgradeMintConfig) error {
	printer := ui.New(os.Stdout)

	printer.StepStart("Loading manifest")
	m, err := repos.LoadManifest(ctx, opts.manifest)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}
	printer.StepDone("Loaded manifest")

	var provisioner repos.WIFProvisioner
	if opts.testProvisioner != nil {
		provisioner = opts.testProvisioner
	} else {
		provisioner = &gcfProvisionerAdapter{
			provisioner: gcf.NewProvisioner(gcf.Config{
				ProjectID: m.Mint.Project,
				Region:    m.Mint.Region,
				MintURL:   m.Mint.URL,
			}, gcf.NewLiveGCFClient(m.Mint.Project)),
		}
	}

	progressFn := func(repo, phase, msg string) {
		printer.StepInfo(fmt.Sprintf("[%s] %s", repo, msg))
	}

	if err := repos.UpgradeMint(ctx, m, provisioner, progressFn); err != nil {
		return err
	}

	printer.StepDone("Mint verified successfully")
	return nil
}
