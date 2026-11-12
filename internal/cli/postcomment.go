package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newPostCommentCmd() *cobra.Command {
	var (
		repo        string
		number      int
		marker      string
		result      string
		token       string
		dryRun      bool
		keepHistory bool
		fullsendDir string
	)

	cmd := &cobra.Command{
		Use:        "post-comment",
		Short:      "Post a marked comment on an issue or PR (deprecated: use 'fullsend issues post-comment')",
		Deprecated: "use 'fullsend issues post-comment --tracker github' instead",
		Long: `Posts a comment with a hidden HTML marker on an issue or pull request.

DEPRECATED: This command targets GitHub only. Use
'fullsend issues post-comment --tracker github' for GitHub, GitLab,
or Jira support.

Every run adds a new comment at the end of the thread: nothing is
edited in place and there are no collapsed "Previous run" sections —
the thread itself is the history.

The --marker flag identifies this agent's comments. Each agent should
use a unique marker (e.g. "<!-- fullsend:triage-agent -->").

The --result flag accepts a file path or "-" for stdin.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)

			client, err := newAuthenticatedGitHubClient(token, "")
			if err != nil {
				if errors.Is(err, errGitHubTokenMissing) {
					return githubTokenFlagError("--token")
				}
				return err
			}

			if number <= 0 {
				return fmt.Errorf("--number must be a positive integer, got %d", number)
			}

			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			body, err := readBody(result)
			if err != nil {
				return fmt.Errorf("reading comment body: %w", err)
			}

			printer.Header("Post Comment")

			// Resolve keep_history: explicit --keep-history flag takes
			// precedence, otherwise fall back to config.yaml via
			// --fullsend-dir (matching the pattern in issues post-comment
			// and post-review).
			resolvedKeepHistory := keepHistory
			if cmd.Flags().Changed("keep-history") {
				warnKeepHistoryDeprecated(cmd)
			} else {
				var khFlag *bool // nil = not explicitly set
				resolved, resolveErr := resolveKeepHistory(khFlag, fullsendDir, nil)
				if resolveErr != nil {
					printer.StepWarn(fmt.Sprintf("Warning: %v (keep_history is deprecated and unused; every result is posted as a new comment)", resolveErr))
				}
				resolvedKeepHistory = resolved
			}

			cfg := sticky.Config{
				Marker:      marker,
				DryRun:      dryRun,
				KeepHistory: resolvedKeepHistory,
			}
			_, err = sticky.Post(cmd.Context(), client, owner, repoName, number, body, cfg, printer)
			return err
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&number, "number", 0, "issue or pull request number (required)")
	cmd.Flags().StringVar(&marker, "marker", "", "hidden HTML marker to identify this agent's comments (required)")
	cmd.Flags().StringVar(&result, "result", "-", "path to comment body file, or '-' for stdin")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: GH_TOKEN, GITHUB_TOKEN, or gh auth token)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be posted without making API calls")
	cmd.Flags().BoolVar(&keepHistory, "keep-history", true, "deprecated: no effect (every result is posted as a new comment; sticky comments were removed)")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", os.Getenv("FULLSEND_DIR"), "path to .fullsend config directory (default: $FULLSEND_DIR; sources defaults from its config.yaml when flags are omitted)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("marker")

	return cmd
}
