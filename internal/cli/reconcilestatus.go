package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
)

func newReconcileStatusCmd() *cobra.Command {
	var (
		repo   string
		number int
		runID  string
		runURL string
		sha    string
		token  string
	)

	cmd := &cobra.Command{
		Use:   "reconcile-status",
		Short: "Finalize orphaned status comments left by hard-killed agent processes",
		Long: `Finds and finalizes a status comment that was left in a non-terminal
state because the agent process was hard-killed (SIGKILL, OOM, etc.)
before its deferred PostCompletion call could run.

Searches for a comment matching the run's HTML marker
(<!-- fullsend:agent-status:<runID> -->) that does not contain the
terminal tag (<!-- fullsend:status:terminal -->). If found, updates it
to an "Interrupted" state and adds the terminal tag. If already
finalized, this is a no-op.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("--token or GITHUB_TOKEN required")
			}

			if number <= 0 {
				return fmt.Errorf("--number must be a positive integer, got %d", number)
			}

			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			client := gh.New(token)
			return statuscomment.ReconcileOrphaned(cmd.Context(), client, owner, repoName, number, runID, runURL, sha)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&number, "number", 0, "issue or pull request number (required)")
	cmd.Flags().StringVar(&runID, "run-id", "", "workflow run ID used in the status comment marker (required)")
	cmd.Flags().StringVar(&runURL, "run-url", "", "URL to the workflow run (optional)")
	cmd.Flags().StringVar(&sha, "sha", "", "commit SHA (optional, shown as short hash)")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("run-id")

	return cmd
}
