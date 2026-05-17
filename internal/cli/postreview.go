package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
const reviewFollowupSummaryMarker = "<!-- fullsend:review-follow-ups -->"
const reviewFollowupIssueMarkerPrefix = "<!-- fullsend:review-follow-up:"
const maxReviewFollowUpIssues = 3

var hexSHARe = regexp.MustCompile(`^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`)
var reasonRe = regexp.MustCompile(`^[a-zA-Z0-9_-]*$`)

func newPostReviewCmd() *cobra.Command {
	var (
		repo              string
		pr                int
		result            string
		token             string
		headSHA           string
		dryRun            bool
		maxFollowUpIssues int
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
			if err := validateMaxReviewFollowUpIssues(maxFollowUpIssues, "--max-follow-up-issues"); err != nil {
				return err
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

			commentURL, err := sticky.Post(cmd.Context(), client, owner, repoName, pr, parsed.Body, cfg, printer)
			if err != nil {
				return err
			}

			if err := submitFormalReview(cmd.Context(), client, owner, repoName, pr, parsed.Action, parsed.HeadSHA, commentURL, parsed.Findings, dryRun, printer); err != nil {
				return err
			}

			return postApprovedFollowUpIssues(cmd.Context(), client, owner, repoName, pr, parsed, dryRun, maxFollowUpIssues, printer)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&pr, "pr", 0, "pull request number (required)")
	cmd.Flags().StringVar(&result, "result", "-", "path to review result file, or '-' for stdin")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "expected PR HEAD SHA (skips review if HEAD has moved)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be posted without making API calls")
	cmd.Flags().IntVar(&maxFollowUpIssues, "max-follow-up-issues", maxReviewFollowUpIssues, "maximum number of review follow-up issues to create after an approval (0 disables creation; max 3)")
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

// postStaleHeadNotice posts a failure comment when the PR HEAD has moved
// since the review was generated.
func postStaleHeadNotice(ctx context.Context, client forge.Client, owner, repo string, pr int, reviewedSHA, currentSHA string, cfg sticky.Config, printer *ui.Printer) error {
	body := fmt.Sprintf(`## Review

**Reason:** stale-head

The review agent reviewed commit `+"`%s`"+` but the PR HEAD is now `+"`%s`"+`. This review was discarded to avoid approving unreviewed code.`, reviewedSHA, currentSHA)

	if _, err := sticky.Post(ctx, client, owner, repo, pr, body, cfg, printer); err != nil {
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
//   - COMMENT: skipped entirely (sticky comment already covers it,
//     and the API requires a non-empty body)
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

	if event == "COMMENT" {
		printer.StepInfo("Skipping formal COMMENT review (sticky comment already updated)")
		return nil
	}

	inlineComments := findingsToReviewComments(findings)

	var reviewBody string
	if event == "REQUEST_CHANGES" {
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
func findingsToReviewComments(findings []ReviewFinding) []forge.ReviewComment {
	var comments []forge.ReviewComment
	for _, f := range findings {
		if f.File == "" || f.Line <= 0 {
			continue
		}
		comments = append(comments, forge.ReviewComment{
			Path: f.File,
			Line: f.Line,
			Body: formatFindingComment(f),
		})
	}
	return comments
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

type reviewFollowupIssue struct {
	finding ReviewFinding
	issue   *forge.Issue
	created bool
}

// postApprovedFollowUpIssues creates GitHub issues for actionable low/info
// findings after an approval. Blocking and comment-only findings stay in the
// review itself; this path only preserves non-blocking work that would
// otherwise disappear after merge.
func postApprovedFollowUpIssues(ctx context.Context, client forge.Client, owner, repo string, pr int, parsed ReviewResult, dryRun bool, maxFollowUpIssues int, printer *ui.Printer) error {
	if strings.ToLower(parsed.Action) != "approve" {
		return nil
	}
	if err := validateMaxReviewFollowUpIssues(maxFollowUpIssues, "max follow-up issues"); err != nil {
		return err
	}

	actionable := actionableApprovedFindings(parsed.Findings)
	if len(actionable) == 0 {
		return nil
	}

	if dryRun {
		toCreate := min(len(actionable), maxFollowUpIssues)
		skipped := len(actionable) - toCreate
		if maxFollowUpIssues == 0 {
			printer.StepInfo(fmt.Sprintf("Dry run — review follow-up issue creation disabled; would skip %d actionable finding(s)", len(actionable)))
		} else if skipped > 0 {
			printer.StepInfo(fmt.Sprintf("Dry run — would create up to %d review follow-up issue(s) and skip %d due to cap", toCreate, skipped))
		} else {
			printer.StepInfo(fmt.Sprintf("Dry run — would create %d review follow-up issue(s)", toCreate))
		}
		return nil
	}

	markers := make(map[string]ReviewFinding, len(actionable))
	for _, finding := range actionable {
		markers[reviewFollowupIssueMarker(owner, repo, finding)] = finding
	}

	printer.StepStart("Checking for existing review follow-up issues")
	openIssues, err := client.ListOpenIssues(ctx, owner, repo, "type/chore")
	if err != nil {
		return fmt.Errorf("listing open issues for review follow-up duplicate detection: %w", err)
	}
	existingByMarker := map[string]forge.Issue{}
	for _, issue := range openIssues {
		for marker := range markers {
			if strings.Contains(issue.Body, marker) {
				if existing, ok := existingByMarker[marker]; ok {
					printer.StepWarn(fmt.Sprintf("Duplicate review follow-up marker found in issues #%d and #%d; reusing #%d", existing.Number, issue.Number, existing.Number))
					continue
				}
				existingByMarker[marker] = issue
			}
		}
	}
	printer.StepDone("Duplicate check complete")

	results := make([]reviewFollowupIssue, 0, len(actionable))
	createdCount := 0
	skippedCount := 0
	for _, finding := range actionable {
		marker := reviewFollowupIssueMarker(owner, repo, finding)
		if issue, ok := existingByMarker[marker]; ok {
			issueCopy := issue
			results = append(results, reviewFollowupIssue{
				finding: finding,
				issue:   &issueCopy,
				created: false,
			})
			continue
		}
		if createdCount >= maxFollowUpIssues {
			skippedCount++
			continue
		}

		title := reviewFollowupIssueTitle(pr, finding)
		body := reviewFollowupIssueBody(owner, repo, pr, finding, marker)
		printer.StepStart("Creating review follow-up issue")
		issue, err := client.CreateIssue(ctx, owner, repo, title, body, "type/chore")
		if err != nil {
			return fmt.Errorf("creating review follow-up issue for %s: %w", reviewFindingLocation(finding), err)
		}
		printer.StepDone(fmt.Sprintf("Created follow-up issue #%d", issue.Number))
		results = append(results, reviewFollowupIssue{
			finding: finding,
			issue:   issue,
			created: true,
		})
		createdCount++
	}
	if skippedCount > 0 {
		printer.StepInfo(fmt.Sprintf("Review follow-up issue cap reached (%d); skipped %d actionable finding(s)", maxFollowUpIssues, skippedCount))
	}

	return postReviewFollowupSummary(ctx, client, owner, repo, pr, results, skippedCount, maxFollowUpIssues, printer)
}

func validateMaxReviewFollowUpIssues(value int, name string) error {
	if value < 0 || value > maxReviewFollowUpIssues {
		return fmt.Errorf("%s must be between 0 and %d, got %d", name, maxReviewFollowUpIssues, value)
	}
	return nil
}

func actionableApprovedFindings(findings []ReviewFinding) []ReviewFinding {
	actionable := make([]ReviewFinding, 0, len(findings))
	for _, finding := range findings {
		severity := strings.ToLower(finding.Severity)
		if finding.Actionable && (severity == "low" || severity == "info") {
			actionable = append(actionable, finding)
		}
	}
	return actionable
}

func postReviewFollowupSummary(ctx context.Context, client forge.Client, owner, repo string, pr int, results []reviewFollowupIssue, skippedCount, maxFollowUpIssues int, printer *ui.Printer) error {
	if len(results) == 0 && skippedCount == 0 {
		return nil
	}

	var created []reviewFollowupIssue
	var existing []reviewFollowupIssue
	for _, result := range results {
		if result.created {
			created = append(created, result)
		} else {
			existing = append(existing, result)
		}
	}

	var b strings.Builder
	b.WriteString("## Review follow-ups\n\n")
	if skippedCount > 0 {
		if maxFollowUpIssues == 0 {
			fmt.Fprintf(&b, "**Warning:** follow-up issue creation is disabled, so %d actionable non-blocking finding(s) were not filed.\n\n", skippedCount)
		} else {
			fmt.Fprintf(&b, "**Warning:** follow-up issue creation is capped at %d per review run; %d actionable non-blocking finding(s) were not filed.\n\n", maxFollowUpIssues, skippedCount)
		}
	}
	if len(created) > 0 {
		b.WriteString("Created follow-up issues for actionable non-blocking review findings:\n\n")
		for _, result := range created {
			fmt.Fprintf(&b, "- [#%d](%s) — %s\n", result.issue.Number, result.issue.URL, reviewFindingSummary(result.finding))
		}
	}
	if len(existing) > 0 {
		if len(created) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Existing follow-up issues already track these findings:\n\n")
		for _, result := range existing {
			fmt.Fprintf(&b, "- [#%d](%s) — %s\n", result.issue.Number, result.issue.URL, reviewFindingSummary(result.finding))
		}
	}

	cfg := sticky.Config{Marker: reviewFollowupSummaryMarker}
	if _, err := sticky.Post(ctx, client, owner, repo, pr, b.String(), cfg, printer); err != nil {
		return fmt.Errorf("posting review follow-up summary: %w", err)
	}
	return nil
}

func reviewFollowupIssueTitle(pr int, finding ReviewFinding) string {
	summary := reviewFindingSummary(finding)
	if summary == "" {
		summary = "actionable review finding"
	}
	return fmt.Sprintf("Follow-up from PR #%d: %s", pr, truncate(summary, 88))
}

func reviewFollowupIssueBody(owner, repo string, pr int, finding ReviewFinding, marker string) string {
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString("\n\n")
	b.WriteString("## Review follow-up\n\n")
	fmt.Fprintf(&b, "- PR: https://github.com/%s/%s/pull/%d\n", owner, repo, pr)
	fmt.Fprintf(&b, "- Severity: `%s`\n", finding.Severity)
	fmt.Fprintf(&b, "- Category: `%s`\n", finding.Category)
	fmt.Fprintf(&b, "- Location: `%s`\n", reviewFindingLocation(finding))
	b.WriteString("\n## Finding\n\n")
	b.WriteString(strings.TrimSpace(finding.Description))
	if finding.Remediation != "" {
		b.WriteString("\n\n## Suggested remediation\n\n")
		b.WriteString(strings.TrimSpace(finding.Remediation))
	}
	b.WriteString("\n\n---\n")
	b.WriteString("_Generated by the fullsend review agent from an approved PR. The PR was approved because this finding was non-blocking, but it was marked actionable so it is tracked separately._\n")
	return b.String()
}

func reviewFollowupIssueMarker(owner, repo string, finding ReviewFinding) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s/%s\n%s\n%s\n%s\n%d\n%s\n", owner, repo, strings.ToLower(finding.Severity), finding.Category, finding.File, finding.Line, compactWhitespace(finding.Description))
	return reviewFollowupIssueMarkerPrefix + hex.EncodeToString(hash.Sum(nil)) + " -->"
}

func reviewFindingLocation(finding ReviewFinding) string {
	if finding.Line > 0 {
		return fmt.Sprintf("%s:%d", finding.File, finding.Line)
	}
	return finding.File
}

func reviewFindingSummary(finding ReviewFinding) string {
	summary := compactWhitespace(finding.Description)
	if summary != "" {
		return summary
	}
	return compactWhitespace(finding.Category)
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return strings.TrimSpace(string(runes[:max-3])) + "..."
}

// dismissStaleRequestChanges dismisses the most recent CHANGES_REQUESTED
// review by the authenticated user when the new verdict is softer.
func dismissStaleRequestChanges(ctx context.Context, client forge.Client, owner, repo string, pr int, newEvent, user string, reviews []forge.PullRequestReview, printer *ui.Printer) {
	if newEvent == "REQUEST_CHANGES" {
		return
	}

	for i := len(reviews) - 1; i >= 0; i-- {
		r := reviews[i]
		if r.User != user || r.State != "CHANGES_REQUESTED" {
			continue
		}
		printer.StepStart(fmt.Sprintf("Dismissing stale CHANGES_REQUESTED review %d", r.ID))
		if err := client.DismissPullRequestReview(ctx, owner, repo, pr, r.ID, "Superseded by updated review"); err != nil {
			printer.StepInfo(fmt.Sprintf("Warning: could not dismiss review %d: %v", r.ID, err))
		} else {
			printer.StepDone("Stale review dismissed")
		}
		break
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
