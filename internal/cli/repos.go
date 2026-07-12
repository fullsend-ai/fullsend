package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	cmd.AddCommand(newReposInstallCmd())
	cmd.AddCommand(newReposStatusCmd())
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
		Use:   "install",
		Short: "Install fullsend on repos defined in a manifest",
		Long: `Install fullsend on repos not yet installed, as defined in a repos.yaml manifest.

Runs in three phases:
  1. Parallel discovery: check which repos are already installed
  2. Sequential WIF: provision WIF infrastructure per repo (not concurrent-safe)
  3. Parallel scaffold: commit scaffold files and write variables/secrets`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposInstall(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.manifest, "manifest", "f", "repos.yaml", "path or URL to repos.yaml manifest")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview what would be installed without making changes")
	cmd.Flags().StringArrayVar(&opts.repoFilter, "repo", nil, "install specific repos only (repeatable)")
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
	return s.mint.DeletePerRepoWIF(ctx, repo)
}
