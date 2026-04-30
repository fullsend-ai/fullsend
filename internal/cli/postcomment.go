package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newPostCommentCmd() *cobra.Command {
	var (
		repo   string
		number int
		marker string
		result string
		token  string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "post-comment",
		Short: "Post or update a sticky comment on an issue or PR",
		Long: `Posts a comment with a hidden HTML marker on an issue or pull request.

On first run, creates a new comment. On re-runs, finds the existing
comment by its marker and edits in-place, collapsing old content into
<details> blocks. This prevents comment flooding on re-runs.

The --marker flag identifies this agent's comments. Each agent should
use a unique marker (e.g. "<!-- fullsend:triage-agent -->").

The --result flag accepts a file path or "-" for stdin.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)

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

			body, err := readBody(result)
			if err != nil {
				return fmt.Errorf("reading comment body: %w", err)
			}

			printer.Header("Post Comment")

			client := gh.New(token)
			cfg := sticky.Config{
				Marker: marker,
				DryRun: dryRun,
			}
			return sticky.Post(cmd.Context(), client, owner, repoName, number, body, cfg, printer)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&number, "number", 0, "issue or pull request number (required)")
	cmd.Flags().StringVar(&marker, "marker", "", "hidden HTML marker to identify this agent's comments (required)")
	cmd.Flags().StringVar(&result, "result", "-", "path to comment body file, or '-' for stdin")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be posted without making API calls")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("marker")

	return cmd
}
