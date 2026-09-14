package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

const reviewCompletionContext = "fullsend/review-completed"

var reviewStatusSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// reviewStatusGitHubClientFn creates the GitHub status client.
// Override in tests to inspect status writes without network access.
var reviewStatusGitHubClientFn = func() (forge.Client, error) {
	return newAuthenticatedGitHubClient("", "")
}

// reviewStatusGitLabClientFn creates the GitLab status client.
// Override in tests to inspect status writes without network access.
var reviewStatusGitLabClientFn = newGitLabClientFromEnv

func newReviewStatusCmd() *cobra.Command {
	var repo, sha, state, jobStatus, runURL, forgeFlag string
	var wasSkipped bool

	cmd := &cobra.Command{
		Use:    "review-status",
		Short:  "Update the internal automated-review completion status",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			if !reviewStatusSHA.MatchString(sha) {
				return fmt.Errorf("--sha must be a 40-character hexadecimal commit SHA, got %q", sha)
			}

			if state == "" {
				if jobStatus == "" {
					return fmt.Errorf("either --state or --job-status is required")
				}
				state = terminalReviewStatusState(jobStatus, wasSkipped)
			} else if jobStatus != "" {
				return fmt.Errorf("--state and --job-status cannot be used together")
			}
			statusState, description, err := parseReviewStatusState(state)
			if err != nil {
				return err
			}
			forgePlatform, err := detectForgePlatform(forgeFlag, nil)
			if err != nil {
				return err
			}

			var client forge.Client
			if forgePlatform == repos.ForgeGitLab {
				client, err = reviewStatusGitLabClientFn("review completion status")
			} else {
				client, err = reviewStatusGitHubClientFn()
			}
			if err != nil {
				return err
			}

			status := forge.CommitStatus{
				State:       statusState,
				Context:     reviewCompletionContext,
				Description: description,
				TargetURL:   runURL,
			}
			if err := client.SetCommitStatus(cmd.Context(), parts[0], parts[1], sha, status); err != nil {
				return fmt.Errorf("setting %s status: %w", reviewCompletionContext, err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format")
	cmd.Flags().StringVar(&sha, "sha", "", "exact pull request head commit SHA")
	cmd.Flags().StringVar(&state, "state", "", "status state: pending, success, failure, or error")
	cmd.Flags().StringVar(&jobStatus, "job-status", "", "CI job outcome used to derive a terminal status")
	cmd.Flags().BoolVar(&wasSkipped, "was-skipped", false, "whether the review agent skipped execution")
	cmd.Flags().StringVar(&runURL, "run-url", "", "URL to the workflow run")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform (e.g. "github", "gitlab"); auto-detected from CI env vars when omitted`)
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("sha")
	return cmd
}

func terminalReviewStatusState(jobStatus string, wasSkipped bool) string {
	if jobStatus == "cancelled" {
		return "error"
	}
	if jobStatus == "success" && !wasSkipped {
		return "success"
	}
	return "failure"
}

func parseReviewStatusState(state string) (forge.CommitStatusState, string, error) {
	switch state {
	case "pending":
		return forge.CommitStatusPending, "Automated review is running", nil
	case "success":
		return forge.CommitStatusSuccess, "Automated review completed", nil
	case "failure":
		return forge.CommitStatusFailure, "Automated review did not complete", nil
	case "error":
		return forge.CommitStatusError, "Automated review did not complete", nil
	default:
		return "", "", fmt.Errorf("unsupported review status state %q", state)
	}
}
