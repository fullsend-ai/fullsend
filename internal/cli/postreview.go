package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const reviewMarker = "<!-- fullsend:review-agent -->"

var hexSHARe = regexp.MustCompile(`^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`)
var reasonRe = regexp.MustCompile(`^[a-zA-Z0-9_-]*$`)

func newPostReviewCmd() *cobra.Command {
	var (
		repo    string
		pr      int
		result  string
		token   string
		headSHA string
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "post-review",
		Short: "Post or update a sticky review comment on a PR",
		Long: `Posts review findings as a sticky issue comment on a pull request,
then submits a formal GitHub PR review with the disposition.

On first run, creates a new comment with a hidden HTML marker.
On re-runs, finds the existing comment, collapses old content into
a <details> block, and edits in-place. Stale formal reviews by the
same user are minimized before submitting a new one.

The --result flag accepts a file path containing a JSON review result
(with action, body, and optionally head_sha fields), or reads from
stdin if set to "-". Plain text input is treated as a comment-only
review.

When --head-sha is provided (or head_sha is in the JSON), the CLI
verifies that the PR HEAD still matches before posting. If the HEAD
has moved, a stale-head failure is posted instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)

			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("--token or GITHUB_TOKEN required")
			}

			if pr <= 0 {
				return fmt.Errorf("--pr must be a positive integer, got %d", pr)
			}

			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			raw, err := readBody(result)
			if err != nil {
				return fmt.Errorf("reading review body: %w", err)
			}

			parsed, err := parseReviewResult(raw)
			if err != nil {
				return fmt.Errorf("parsing review result: %w", err)
			}

			// CLI flag takes precedence over JSON field.
			if headSHA != "" {
				parsed.HeadSHA = headSHA
			}
			if parsed.HeadSHA != "" && !hexSHARe.MatchString(parsed.HeadSHA) {
				return fmt.Errorf("head SHA must be a 40 or 64 character hex string, got %q", parsed.HeadSHA)
			}

			printer.Header("Post Review")

			client := gh.New(token)
			cfg := sticky.Config{
				Marker: reviewMarker,
				DryRun: dryRun,
			}

			// Stale-head check: refuse to post a review against code
			// that has changed since the agent reviewed it.
			if parsed.HeadSHA != "" {
				stale, currentSHA, err := checkStaleHead(cmd.Context(), client, owner, repoName, pr, parsed.HeadSHA, dryRun, printer)
				if err != nil {
					return err
				}
				if stale {
					return postStaleHeadNotice(cmd.Context(), client, owner, repoName, pr, parsed.HeadSHA, currentSHA, cfg, printer)
				}
			}

			// Failure action: post a failure notice as a sticky comment,
			// skip formal review.
			if strings.ToLower(parsed.Action) == "failure" {
				return postFailureNotice(cmd.Context(), client, owner, repoName, pr, parsed, cfg, printer)
			}

			if err := sticky.Post(cmd.Context(), client, owner, repoName, pr, parsed.Body, cfg, printer); err != nil {
				return err
			}

			return submitFormalReview(cmd.Context(), client, owner, repoName, pr, parsed.Action, parsed.HeadSHA, dryRun, printer)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&pr, "pr", 0, "pull request number (required)")
	cmd.Flags().StringVar(&result, "result", "-", "path to review result file, or '-' for stdin")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "expected PR HEAD SHA (skips review if HEAD has moved)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be posted without making API calls")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")

	return cmd
}

// ReviewResult represents a parsed review result file.
type ReviewResult struct {
	Body    string `json:"body"`
	Action  string `json:"action"`   // "approve", "request-changes", "comment", "failure"
	HeadSHA string `json:"head_sha"` // commit SHA the agent reviewed
	Reason  string `json:"reason"`   // failure reason (when action is "failure")
}

// reviewActionToEvent maps a ReviewResult action to a GitHub PR review event.
func reviewActionToEvent(action string) (string, bool) {
	switch strings.ToLower(action) {
	case "approve":
		return "APPROVE", true
	case "request-changes", "request_changes":
		return "REQUEST_CHANGES", true
	case "comment":
		return "COMMENT", true
	default:
		return "", false
	}
}

// checkStaleHead compares the reviewed SHA against the current PR HEAD.
// Returns (stale, currentSHA, error). When stale is true, currentSHA
// contains the actual PR HEAD so callers can include it in notices
// without a redundant API call.
func checkStaleHead(ctx context.Context, client forge.Client, owner, repo string, pr int, reviewedSHA string, dryRun bool, printer *ui.Printer) (bool, string, error) {
	printer.StepStart("Checking PR HEAD against reviewed SHA")

	if dryRun {
		printer.StepInfo("Dry run — would check HEAD SHA")
		return false, "", nil
	}

	currentSHA, err := client.GetPullRequestHeadSHA(ctx, owner, repo, pr)
	if err != nil {
		return false, "", fmt.Errorf("fetching PR HEAD: %w", err)
	}

	if !strings.EqualFold(currentSHA, reviewedSHA) {
		printer.StepInfo(fmt.Sprintf("Stale: reviewed %s but HEAD is now %s", reviewedSHA[:min(len(reviewedSHA), 12)], currentSHA[:min(len(currentSHA), 12)]))
		return true, currentSHA, nil
	}

	printer.StepDone("HEAD matches reviewed SHA")
	return false, currentSHA, nil
}

// postStaleHeadNotice posts a failure comment when the PR HEAD has moved
// since the review was generated.
func postStaleHeadNotice(ctx context.Context, client forge.Client, owner, repo string, pr int, reviewedSHA, currentSHA string, cfg sticky.Config, printer *ui.Printer) error {
	body := fmt.Sprintf(`## Review: automated review

**Outcome:** failure
**Reason:** stale-head

The review agent reviewed commit `+"`%s`"+` but the PR HEAD is now `+"`%s`"+`. This review was discarded to avoid approving unreviewed code.`, reviewedSHA, currentSHA)

	if err := sticky.Post(ctx, client, owner, repo, pr, body, cfg, printer); err != nil {
		return fmt.Errorf("posting stale-head notice: %w", err)
	}
	return fmt.Errorf("review stale: reviewed %s but HEAD is now %s", reviewedSHA, currentSHA)
}

// postFailureNotice posts a failure comment as a sticky comment.
func postFailureNotice(ctx context.Context, client forge.Client, owner, repo string, pr int, parsed ReviewResult, cfg sticky.Config, printer *ui.Printer) error {
	printer.StepStart("Review agent reported failure, posting notice")

	reason := parsed.Reason
	if reason == "" {
		reason = "unknown"
	} else if !reasonRe.MatchString(reason) {
		reason = "invalid-reason"
	}

	var body string
	if parsed.Body != "" {
		body = parsed.Body
	} else {
		body = fmt.Sprintf(`## Review: automated review

**Outcome:** failure
**Reason:** %s

This PR was NOT reviewed. Do not count this as an approval.`, reason)
	}

	if err := sticky.Post(ctx, client, owner, repo, pr, body, cfg, printer); err != nil {
		return fmt.Errorf("posting failure notice: %w", err)
	}
	printer.StepDone("Failure notice posted")
	return nil
}

// submitFormalReview minimizes stale reviews by the same user, then
// submits a new GitHub PR review. When commitSHA is non-empty, the
// review is pinned to that commit via the commit_id field, closing
// the TOCTOU gap between the stale-head check and review submission.
func submitFormalReview(ctx context.Context, client forge.Client, owner, repo string, pr int, action, commitSHA string, dryRun bool, printer *ui.Printer) error {
	event, ok := reviewActionToEvent(action)
	if !ok {
		printer.StepInfo(fmt.Sprintf("Unknown review action %q, skipping formal review", action))
		return nil
	}

	if dryRun {
		printer.StepInfo(fmt.Sprintf("Dry run — would submit %s review", event))
		return nil
	}

	if err := minimizeStaleReviews(ctx, client, owner, repo, pr, printer); err != nil {
		return err
	}

	printer.StepStart(fmt.Sprintf("Submitting %s review", event))
	reviewBody := "See the review comment above for full details."
	if err := client.CreatePullRequestReview(ctx, owner, repo, pr, event, reviewBody, commitSHA); err != nil {
		return fmt.Errorf("submitting review: %w", err)
	}
	printer.StepDone("Review submitted")
	return nil
}

// minimizeStaleReviews lists all reviews on the PR, finds previous reviews
// by the authenticated user, and minimizes them. Called before creating a
// new review, so all existing reviews by this user are stale.
func minimizeStaleReviews(ctx context.Context, client forge.Client, owner, repo string, pr int, printer *ui.Printer) error {
	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		printer.StepInfo("Could not determine authenticated user, skipping stale review cleanup")
		return nil
	}

	reviews, err := client.ListPullRequestReviews(ctx, owner, repo, pr)
	if err != nil {
		printer.StepInfo("Could not list reviews, skipping stale review cleanup")
		return nil
	}

	var stale []forge.PullRequestReview
	for _, r := range reviews {
		if r.User == user {
			stale = append(stale, r)
		}
	}

	if len(stale) == 0 {
		return nil
	}

	printer.StepStart(fmt.Sprintf("Minimizing %d stale review(s)", len(stale)))
	for _, r := range stale {
		if err := client.MinimizeComment(ctx, r.NodeID, "OUTDATED"); err != nil {
			printer.StepInfo(fmt.Sprintf("Warning: could not minimize review %s: %v", r.NodeID, err))
		}
	}
	printer.StepDone("Stale reviews minimized")

	return nil
}

// parseReviewResult attempts to parse the body as a JSON ReviewResult.
// If parsing fails, treats the entire input as a plain-text body.
// Returns an error if the JSON is valid but the body field is empty
// (unless the action is "failure", which may omit the body).
func parseReviewResult(input string) (ReviewResult, error) {
	var result ReviewResult
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		return ReviewResult{Body: input, Action: "comment"}, nil
	}
	if result.Body == "" && strings.ToLower(result.Action) != "failure" {
		return ReviewResult{}, fmt.Errorf("review result JSON has empty body field")
	}
	if result.Action == "" {
		result.Action = "comment"
	}
	return result, nil
}
