package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/spf13/cobra"
)

func newReposCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "Manage per-repo installations across multiple orgs",
		Long:  "Commands for managing fullsend per-repo installations at scale via a declarative repos.yaml manifest.",
	}
	cmd.AddCommand(newReposStatusCmd())
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
