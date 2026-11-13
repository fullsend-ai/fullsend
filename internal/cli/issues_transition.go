package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/spf13/cobra"
)

type issuesTransitionConfig struct {
	trackerName string
	project     string
	token       string
	fullsendDir string
	number      int
	opts        tracker.TransitionOptions
	comment     string

	// Test overrides — when non-nil, bypass production dependencies.
	testClient       tracker.Client
	testConfigReader config.PerRepoConfigReader
	testWriter       io.Writer
}

// issueTransitionResult is the CLI JSON representation of a tracker result.
type issueTransitionResult struct {
	Changed    bool   `json:"changed"`
	Transition string `json:"transition,omitempty"`
	Status     string `json:"status,omitempty"`
}

func newIssuesTransitionCmd() *cobra.Command {
	var cfg issuesTransitionConfig
	cmd := &cobra.Command{
		Use:   "transition",
		Short: "Transition an issue through its tracker workflow",
		Long: `Close an issue with --state closed, or select an exact Jira transition
name with --transition or target status with --status. Exactly one selector
is required. Jira resolves a unique available action and accepts --resolution
when the workflow requires it. A required resolution is selected automatically
only when Jira offers exactly one valid allowed value and no default. No arbitrary
workflow action or resolution is chosen.

An optional --comment is sent in the same request as the Jira transition, or after closing
on GitHub/GitLab. A comment failure on a forge leaves the issue closed and
returns an error. Inspect the issue after an uncertain failure before retrying.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIssuesTransition(cmd.Context(), &cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.trackerName, "tracker", "", "tracker backend: github, gitlab, or jira (required unless a default is set via config)")
	cmd.Flags().StringVar(&cfg.project, "project", "", projectFlagUsage)
	cmd.Flags().IntVar(&cfg.number, "number", 0, "issue number (required)")
	cmd.Flags().StringVar(&cfg.fullsendDir, "fullsend-dir", "", fullsendDirFlagUsage)
	cmd.Flags().StringVar(&cfg.token, "token", "", tokenFlagUsage)
	cmd.Flags().StringVar(&cfg.opts.State, "state", "", "semantic lifecycle state: closed")
	cmd.Flags().StringVar(&cfg.opts.Name, "transition", "", "exact Jira transition name")
	cmd.Flags().StringVar(&cfg.opts.Status, "status", "", "exact Jira target status name")
	cmd.Flags().StringVar(&cfg.opts.Resolution, "resolution", "", "Jira resolution ID or name allowed by the transition")
	cmd.Flags().StringVar(&cfg.comment, "comment", "", "optional Markdown comment explaining the transition")
	_ = cmd.MarkFlagRequired("number")
	return cmd
}

func runIssuesTransition(ctx context.Context, cfg *issuesTransitionConfig) error {
	if cfg.number <= 0 {
		return fmt.Errorf("--number must be a positive integer, got %d", cfg.number)
	}
	if err := cfg.opts.Validate(); err != nil {
		return err
	}
	name, err := resolveTracker(cfg.trackerName, cfg.fullsendDir, cfg.testConfigReader)
	if err != nil {
		return err
	}
	project, err := resolveIssueProject(name, cfg.project, cfg.fullsendDir, cfg.testConfigReader)
	if err != nil {
		return err
	}
	if name == trackerJira {
		settings, err := loadJiraTrackingSettings(cfg.fullsendDir, cfg.testConfigReader)
		if err != nil {
			return err
		}
		if err := checkJiraTargetProject(settings, project); err != nil {
			return err
		}
	}

	tc := cfg.testClient
	if tc == nil {
		tc, err = newConfiguredTrackerClient(name, cfg.token, cfg.fullsendDir, cfg.testConfigReader)
		if err != nil {
			return err
		}
	}
	transitioner, ok := tc.(tracker.Transitioner)
	if !ok {
		return fmt.Errorf("issue transitions: %w", tracker.ErrNotSupported)
	}
	opts := cfg.opts
	opts.Comment = tracker.Body(cfg.comment)
	result, err := transitioner.TransitionIssue(ctx, project, cfg.number, opts)
	if err != nil {
		return fmt.Errorf("transitioning issue: %w", err)
	}
	out := cfg.testWriter
	if out == nil {
		out = os.Stdout
	}
	return json.NewEncoder(out).Encode(issueTransitionResult{
		Changed:    result.Changed,
		Transition: result.Transition,
		Status:     result.Status,
	})
}
