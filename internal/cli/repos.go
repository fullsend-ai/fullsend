package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newReposCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "Manage per-repo installations across multiple orgs",
		Long:  "Commands for managing fullsend per-repo installations at scale via a declarative repos.yaml manifest.",
	}
	cmd.AddCommand(newReposInitCmd())
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
