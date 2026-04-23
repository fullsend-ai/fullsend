package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// ReviewMarker is a hidden HTML comment used to identify fullsend review
// comments. On re-runs, the CLI searches for this marker to find and
// update the existing comment instead of creating a new one.
const ReviewMarker = "<!-- fullsend:review-agent -->"

// maxCommentSize is GitHub's maximum comment body size (65536 chars).
// We leave a buffer for the marker and details wrapper.
const maxCommentSize = 65000

func newPostReviewCmd() *cobra.Command {
	var (
		repo   string
		pr     int
		result string
		token  string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "post-review",
		Short: "Post or update a sticky review comment on a PR",
		Long: `Posts review findings as a sticky issue comment on a pull request.

On first run, creates a new comment with a hidden HTML marker.
On re-runs, finds the existing comment, collapses old content into
a <details> block, and edits in-place. This prevents review comment
flooding on force-push, manual re-run, or workflow retry.

The --result flag accepts a file path containing the review body text,
or reads from stdin if set to "-".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)

			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("--token or GITHUB_TOKEN required")
			}

			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			body, err := readReviewBody(result)
			if err != nil {
				return fmt.Errorf("reading review body: %w", err)
			}

			client := gh.New(token)
			return postReview(cmd.Context(), client, owner, repoName, pr, body, dryRun, printer)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&pr, "pr", 0, "pull request number (required)")
	cmd.Flags().StringVar(&result, "result", "-", "path to review body file, or '-' for stdin")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be posted without making API calls")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")

	return cmd
}

// postReview implements the sticky comment lifecycle:
// 1. List existing comments to find one with ReviewMarker
// 2. If found: collapse old content into <details>, edit in-place
// 3. If not found: create a new comment with the marker
func postReview(ctx context.Context, client forge.Client, owner, repo string, pr int, body string, dryRun bool, printer *ui.Printer) error {
	printer.Header("Post Review")

	comments, err := client.ListIssueComments(ctx, owner, repo, pr)
	if err != nil {
		return fmt.Errorf("listing comments: %w", err)
	}

	// Find existing fullsend review comment.
	var existing *forge.IssueComment
	for i := range comments {
		if strings.Contains(comments[i].Body, ReviewMarker) {
			existing = &comments[i]
			break
		}
	}

	markedBody := ReviewMarker + "\n" + body

	if existing != nil {
		printer.StepStart("Found existing review comment, updating in-place")

		// Collapse old content into <details>.
		newBody := buildUpdatedBody(existing.Body, markedBody)

		if dryRun {
			printer.StepInfo("Dry run — would update comment " + strconv.Itoa(existing.ID))
			printer.StepInfo("Body length: " + strconv.Itoa(len(newBody)))
			return nil
		}

		if err := client.UpdateIssueComment(ctx, owner, repo, existing.ID, newBody); err != nil {
			return fmt.Errorf("updating comment: %w", err)
		}
		printer.StepDone("Review comment updated")
	} else {
		printer.StepStart("No existing review comment found, creating new one")

		if dryRun {
			printer.StepInfo("Dry run — would create new comment")
			printer.StepInfo("Body length: " + strconv.Itoa(len(markedBody)))
			return nil
		}

		if err := createIssueComment(ctx, client, owner, repo, pr, markedBody); err != nil {
			return fmt.Errorf("creating comment: %w", err)
		}
		printer.StepDone("Review comment created")
	}

	return nil
}

// readReviewBody reads the review body from a file path or stdin.
func readReviewBody(path string) (string, error) {
	if path == "-" {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		return sb.String(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// buildUpdatedBody collapses the old comment body into a <details> block
// and prepends the new body. Truncates history if exceeding maxCommentSize.
func buildUpdatedBody(oldBody, newBody string) string {
	// Strip the marker from the old body for the collapsed section.
	oldContent := strings.Replace(oldBody, ReviewMarker+"\n", "", 1)
	oldContent = strings.Replace(oldContent, ReviewMarker, "", 1)

	collapsed := fmt.Sprintf(
		"\n\n<details>\n<summary>Previous review</summary>\n\n%s\n\n</details>",
		oldContent,
	)

	combined := newBody + collapsed

	// Truncate if exceeding GitHub's comment size limit.
	if len(combined) > maxCommentSize {
		combined = truncateBody(combined)
	}

	return combined
}

// truncateBody trims the body to fit within maxCommentSize, keeping the
// current review intact and trimming history from the end.
func truncateBody(body string) string {
	if len(body) <= maxCommentSize {
		return body
	}

	truncationMsg := "\n\n---\n*Previous review history truncated due to comment size limits.*"
	budget := maxCommentSize - len(truncationMsg)
	if budget < 0 {
		budget = 0
	}

	return body[:budget] + truncationMsg
}

// createIssueComment creates a new issue comment via the forge Client.
// The forge.Client interface uses CreateIssueComment which we added
// alongside UpdateIssueComment.
func createIssueComment(ctx context.Context, client forge.Client, owner, repo string, number int, body string) error {
	_, err := client.CreateIssueComment(ctx, owner, repo, number, body)
	return err
}

// ReviewResult represents a parsed review result file.
type ReviewResult struct {
	Body   string `json:"body"`
	Action string `json:"action"` // "approve", "request-changes", "comment"
}

// parseReviewResult attempts to parse the body as a JSON ReviewResult.
// If parsing fails, treats the entire input as a plain-text body.
func parseReviewResult(input string) ReviewResult {
	var result ReviewResult
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		return ReviewResult{Body: input, Action: "comment"}
	}
	if result.Body == "" {
		result.Body = input
	}
	if result.Action == "" {
		result.Action = "comment"
	}
	return result
}
