package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const reviewMarker = "<!-- fullsend:review-agent -->"

// StaleHeadExitCode is the process exit code used when a review is
// discarded because the PR HEAD moved after the agent reviewed it.
// post-review.sh uses this to detect stale-head outcomes and
// re-dispatch a fresh review for the current HEAD.
const StaleHeadExitCode = 10

var hexSHARe = regexp.MustCompile(`^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`)
var reasonRe = regexp.MustCompile(`^[a-zA-Z0-9_-]*$`)
var hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

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

			// Sanitize review content through the output security
			// pipeline before posting to the forge. This redacts
			// leaked secrets and normalizes zero-width unicode
			// obfuscation that could bypass pattern-based redaction.
			parsed = sanitizeReviewResult(parsed, printer)

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

			commentURL, err := sticky.Post(cmd.Context(), client, owner, repoName, pr, parsed.Body, cfg, printer)
			if err != nil {
				return err
			}

			if err := submitFormalReview(cmd.Context(), client, owner, repoName, pr, parsed.Action, parsed.HeadSHA, commentURL, parsed.Findings, dryRun, printer); err != nil {
				return err
			}

			return postApprovedFollowUpIssues(cmd.Context(), owner, repoName, pr, parsed, printer)
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
	Body     string          `json:"body"`
	Action   string          `json:"action"`   // "approve", "request-changes", "comment", "reject", "failure"
	HeadSHA  string          `json:"head_sha"` // commit SHA the agent reviewed
	Reason   string          `json:"reason"`   // failure reason (when action is "failure")
	Findings []ReviewFinding `json:"findings"`
}

// ReviewFinding is the structured form emitted by the review agent.
type ReviewFinding struct {
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	File        string `json:"file"`
	Line        int    `json:"line,omitempty"`
	Description string `json:"description"`
	Remediation string `json:"remediation,omitempty"`
	Actionable  bool   `json:"actionable,omitempty"`
}

// reviewActionToEvent maps a ReviewResult action to a GitHub PR review event.
func reviewActionToEvent(action string) (string, bool) {
	switch strings.ToLower(action) {
	case "approve":
		return "APPROVE", true
	case "request-changes":
		return "REQUEST_CHANGES", true
	case "comment":
		return "COMMENT", true
	case "reject":
		return "REQUEST_CHANGES", true
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

// staleHeadError is returned when a review is discarded because the PR
// HEAD moved after the agent reviewed it. It carries StaleHeadExitCode
// so the CLI can exit with a distinct code that post-review.sh detects.
type staleHeadError struct {
	reviewedSHA string
	currentSHA  string
}

func (e *staleHeadError) Error() string {
	return fmt.Sprintf("review stale: reviewed %s but HEAD is now %s", e.reviewedSHA, e.currentSHA)
}

// ExitCode returns StaleHeadExitCode.
func (e *staleHeadError) ExitCode() int { return StaleHeadExitCode }

// postStaleHeadNotice posts a failure comment when the PR HEAD has moved
// since the review was generated. Returns a *staleHeadError so the CLI
// exits with StaleHeadExitCode, enabling post-review.sh to re-dispatch.
func postStaleHeadNotice(ctx context.Context, client forge.Client, owner, repo string, pr int, reviewedSHA, currentSHA string, cfg sticky.Config, printer *ui.Printer) error {
	body := fmt.Sprintf(`## Review

**Reason:** stale-head

The review agent reviewed commit `+"`%s`"+` but the PR HEAD is now `+"`%s`"+`. This review was discarded to avoid approving unreviewed code.`, reviewedSHA, currentSHA)

	if _, err := sticky.Post(ctx, client, owner, repo, pr, body, cfg, printer); err != nil {
		return fmt.Errorf("posting stale-head notice: %w", err)
	}
	return &staleHeadError{reviewedSHA: reviewedSHA, currentSHA: currentSHA}
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
		body = fmt.Sprintf(`## Review

**Reason:** %s

This PR was NOT reviewed. Do not count this as an approval.`, reason)
	}

	if _, err := sticky.Post(ctx, client, owner, repo, pr, body, cfg, printer); err != nil {
		return fmt.Errorf("posting failure notice: %w", err)
	}
	printer.StepDone("Failure notice posted")
	return nil
}

// submitFormalReview minimizes stale reviews by the same user, then
// submits a new GitHub PR review. When commitSHA is non-empty, the
// review is pinned to that commit via the commit_id field, closing
// the TOCTOU gap between the stale-head check and review submission.
//
// When findings are present and have file/line locations, they are
// posted as inline diff comments on the review. This places feedback
// directly on the relevant code lines instead of aggregating
// everything in a single top-level comment.
//
// The review body varies by event type to balance notification noise
// against GitHub API requirements:
//   - APPROVE: empty body (avoids duplicate notification)
//   - REQUEST_CHANGES: includes a link to the sticky comment (API
//     requires a non-empty body for this event); when inline comments
//     are attached, the body references them instead
//   - COMMENT: skipped when no inline-eligible findings exist (sticky
//     comment already covers it); when inline comments are present, a
//     COMMENT review is submitted with a body linking to the sticky
//     comment so findings appear on the relevant code lines
func submitFormalReview(ctx context.Context, client forge.Client, owner, repo string, pr int, action, commitSHA, commentURL string, findings []ReviewFinding, dryRun bool, printer *ui.Printer) error {
	event, ok := reviewActionToEvent(action)
	if !ok {
		printer.StepInfo(fmt.Sprintf("Unknown review action %q, skipping formal review", action))
		return nil
	}

	if dryRun {
		printer.StepInfo(fmt.Sprintf("Dry run — would submit %s review", event))
		return nil
	}

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		printer.StepInfo("Could not determine authenticated user, skipping stale review cleanup")
	} else if reviews, err := client.ListPullRequestReviews(ctx, owner, repo, pr); err != nil {
		printer.StepInfo("Could not list reviews, skipping stale review cleanup")
	} else {
		dismissStaleRequestChanges(ctx, client, owner, repo, pr, event, user, reviews, printer)
		minimizeStaleReviews(ctx, client, user, reviews, printer)
	}

	var diffHunks map[string][][2]int
	if fileDiffs, err := client.ListPullRequestFileDiffs(ctx, owner, repo, pr); err != nil {
		printer.StepInfo(fmt.Sprintf("Could not list PR files (%v), inline comments may be rejected", err))
	} else if len(fileDiffs) == 0 {
		printer.StepInfo("PR file list is empty, inline comments disabled")
	} else {
		diffHunks = make(map[string][][2]int, len(fileDiffs))
		for _, f := range fileDiffs {
			diffHunks[f.Path] = parseDiffLineRanges(f.Patch)
		}
	}

	// We filter inline comments here because the GitHub API cannot
	// accept review comments on lines outside the PR diff. The
	// findings themselves remain in the sticky comment body and
	// continue to influence the review verdict.
	//
	// Findings whose file is in the PR diff but whose line falls
	// outside any diff hunk are posted as file-level comments so
	// they remain visible on the PR code.
	inlineComments, fileFiltered, fileLevelFallback := findingsToReviewComments(findings, diffHunks)

	if fileFiltered > 0 {
		printer.StepWarn(fmt.Sprintf("%d inline comment(s) omitted (file not in PR diff) — findings still count toward verdict", fileFiltered))
	}
	if fileLevelFallback > 0 {
		printer.StepInfo(fmt.Sprintf("%d finding(s) posted as file-level comment(s) (line outside diff hunk)", fileLevelFallback))
	}

	// COMMENT verdicts skip the formal review unless there are inline-
	// eligible findings worth attaching. When inline comments exist,
	// a COMMENT review is submitted so the findings appear on the
	// relevant code lines.
	if event == "COMMENT" && len(inlineComments) == 0 {
		printer.StepInfo("Skipping formal COMMENT review (sticky comment already updated)")
		return nil
	}

	var reviewBody string
	if event == "REQUEST_CHANGES" || (event == "COMMENT" && len(inlineComments) > 0) {
		reviewBody = "See the review comment above for full details."
		if commentURL != "" {
			reviewBody = fmt.Sprintf("See the [review comment](%s) for full details.", commentURL)
		}
	}

	printer.StepStart(fmt.Sprintf("Submitting %s review", event))
	if len(inlineComments) > 0 {
		printer.StepInfo(fmt.Sprintf("Attaching %d inline comment(s)", len(inlineComments)))
	}
	if err := client.CreatePullRequestReview(ctx, owner, repo, pr, event, reviewBody, commitSHA, inlineComments); err != nil {
		return fmt.Errorf("submitting review: %w", err)
	}
	printer.StepDone("Review submitted")
	return nil
}

// findingsToReviewComments converts review findings with file and line
// locations into inline review comments. Findings without a file path
// or line number are omitted — they remain in the sticky comment body.
//
// When diffHunks is non-nil, findings referencing files outside the PR
// diff are omitted to avoid GitHub 422 errors. Findings whose file is
// in the diff but whose line falls outside any diff hunk are posted as
// file-level comments (Line=0) so they remain visible on the PR code;
// the original line number is included in the comment body since file-
// level comments have no line annotation in the UI. Files with empty hunk lists (binary files, truncated
// patches) skip line-level filtering — the file is known to be in the
// diff but hunk coverage is unavailable.
//
// Returns the comments, count of findings dropped because their file
// was not in the diff, and count of findings that fell back to
// file-level comments.
func findingsToReviewComments(findings []ReviewFinding, diffHunks map[string][][2]int) ([]forge.ReviewComment, int, int) {
	var comments []forge.ReviewComment
	var fileFiltered, fileLevelFallback int
	for _, f := range findings {
		if f.File == "" || f.Line <= 0 {
			continue
		}
		if diffHunks != nil {
			hunks, fileInDiff := diffHunks[f.File]
			if !fileInDiff {
				fileFiltered++
				continue
			}
			if len(hunks) > 0 && !lineInHunks(f.Line, hunks) {
				// Fall back to file-level comments so findings
				// remain visible on the PR even when the exact
				// line is outside the changed region. Include the
				// original line number in the body since file-level
				// comments have no line annotation in the UI.
				body := fmt.Sprintf("_Line %d_ · %s", f.Line, formatFindingComment(f))
				comments = append(comments, forge.ReviewComment{
					Path: f.File,
					Body: body,
				})
				fileLevelFallback++
				continue
			}
		}
		comments = append(comments, forge.ReviewComment{
			Path: f.File,
			Line: f.Line,
			Body: formatFindingComment(f),
		})
	}
	return comments, fileFiltered, fileLevelFallback
}

// formatFindingComment renders a single review finding as a Markdown
// inline comment body.
func formatFindingComment(f ReviewFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**[%s]** %s", f.Severity, f.Category)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(f.Description))
	if f.Remediation != "" {
		b.WriteString("\n\n**Suggested fix:** ")
		b.WriteString(strings.TrimSpace(f.Remediation))
	}
	return b.String()
}

// parseDiffLineRanges extracts new-file line ranges from unified diff
// hunk headers. Each returned [2]int is an inclusive [start, end] range
// of lines on the right (new) side of the diff that can receive inline
// comments.
func parseDiffLineRanges(patch string) [][2]int {
	var ranges [][2]int
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		m := hunkHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		size := 1
		if m[2] != "" {
			size, _ = strconv.Atoi(m[2])
		}
		if size == 0 {
			continue
		}
		ranges = append(ranges, [2]int{start, start + size - 1})
	}
	return ranges
}

// lineInHunks returns true if line falls within any of the given ranges.
func lineInHunks(line int, hunks [][2]int) bool {
	for _, h := range hunks {
		if line >= h[0] && line <= h[1] {
			return true
		}
	}
	return false
}

// postApprovedFollowUpIssues is disabled pending #1137. Follow-up issues
// should only be created after the PR merges, not while it is still open.
func postApprovedFollowUpIssues(_ context.Context, _, _ string, _ int, parsed ReviewResult, printer *ui.Printer) error {
	if strings.ToLower(parsed.Action) != "approve" {
		return nil
	}
	printer.StepInfo("Review follow-up issue creation is temporarily disabled (#1137)")
	return nil
}

// dismissStaleRequestChanges dismisses all CHANGES_REQUESTED reviews
// by the authenticated user when the new verdict is softer (comment or
// approve). This ensures that stale blocking reviews do not persist
// after the bot has cleared its objections.
func dismissStaleRequestChanges(ctx context.Context, client forge.Client, owner, repo string, pr int, newEvent, user string, reviews []forge.PullRequestReview, printer *ui.Printer) {
	if newEvent == "REQUEST_CHANGES" {
		return
	}

	for _, r := range reviews {
		if r.User != user || r.State != "CHANGES_REQUESTED" {
			continue
		}
		printer.StepStart(fmt.Sprintf("Dismissing stale CHANGES_REQUESTED review %d", r.ID))
		if err := client.DismissPullRequestReview(ctx, owner, repo, pr, r.ID, "Superseded by updated review"); err != nil {
			printer.StepInfo(fmt.Sprintf("Warning: could not dismiss review %d: %v", r.ID, err))
		} else {
			printer.StepDone("Stale review dismissed")
		}
	}
}

// minimizeStaleReviews finds previous reviews by the given user and
// minimizes them. Called before creating a new review, so all existing
// reviews by this user are stale.
func minimizeStaleReviews(ctx context.Context, client forge.Client, user string, reviews []forge.PullRequestReview, printer *ui.Printer) {
	var stale []forge.PullRequestReview
	for _, r := range reviews {
		if r.User == user {
			stale = append(stale, r)
		}
	}

	if len(stale) == 0 {
		return
	}

	printer.StepStart(fmt.Sprintf("Minimizing %d stale review(s)", len(stale)))
	for _, r := range stale {
		if err := client.MinimizeComment(ctx, r.NodeID, "OUTDATED"); err != nil {
			printer.StepInfo(fmt.Sprintf("Warning: could not minimize review %s: %v", r.NodeID, err))
		}
	}
	printer.StepDone("Stale reviews minimized")
}

// sanitizeReviewResult runs the security output pipeline over all
// user-visible text fields in a ReviewResult. This catches leaked
// secrets and zero-width–obfuscated tokens before they reach the
// forge API.
func sanitizeReviewResult(r ReviewResult, printer *ui.Printer) ReviewResult {
	pipeline := security.OutputPipeline()

	// Sanitize the main body.
	if r.Body != "" {
		result := pipeline.Scan(r.Body)
		if result.Sanitized != "" {
			r.Body = result.Sanitized
			printer.StepWarn(fmt.Sprintf("Sanitized review body (%d finding(s))", len(result.Findings)))
			for _, f := range result.Findings {
				printer.StepWarn(fmt.Sprintf("  %s: %s", f.Name, f.Detail))
			}
		}
	}

	// Sanitize finding fields — severity, category, description, and
	// remediation are all interpolated into Markdown posted to the
	// forge and could carry secrets from agent output.
	for i := range r.Findings {
		if r.Findings[i].Severity != "" {
			result := pipeline.Scan(r.Findings[i].Severity)
			if result.Sanitized != "" {
				r.Findings[i].Severity = result.Sanitized
			}
		}
		if r.Findings[i].Category != "" {
			result := pipeline.Scan(r.Findings[i].Category)
			if result.Sanitized != "" {
				r.Findings[i].Category = result.Sanitized
			}
		}
		if r.Findings[i].Description != "" {
			result := pipeline.Scan(r.Findings[i].Description)
			if result.Sanitized != "" {
				r.Findings[i].Description = result.Sanitized
			}
		}
		if r.Findings[i].Remediation != "" {
			result := pipeline.Scan(r.Findings[i].Remediation)
			if result.Sanitized != "" {
				r.Findings[i].Remediation = result.Sanitized
			}
		}
	}

	return r
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
