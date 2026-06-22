package cli

import (
	"bytes"
	"context"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestParseReviewResult_JSON(t *testing.T) {
	input := `{"body": "Looks good!", "action": "approve"}`
	result, err := parseReviewResult(input)
	require.NoError(t, err)
	assert.Equal(t, "Looks good!", result.Body)
	assert.Equal(t, "approve", result.Action)
}

func TestParseReviewResult_PlainText(t *testing.T) {
	input := "This is plain text review."
	result, err := parseReviewResult(input)
	require.NoError(t, err)
	assert.Equal(t, input, result.Body)
	assert.Equal(t, "comment", result.Action)
}

func TestParseReviewResult_DefaultAction(t *testing.T) {
	input := `{"body": "Some review"}`
	result, err := parseReviewResult(input)
	require.NoError(t, err)
	assert.Equal(t, "Some review", result.Body)
	assert.Equal(t, "comment", result.Action)
}

func TestParseReviewResult_EmptyBody(t *testing.T) {
	input := `{"action": "approve"}`
	_, err := parseReviewResult(input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty body")
}

func TestParseReviewResult_FailureAllowsEmptyBody(t *testing.T) {
	input := `{"action": "failure", "reason": "tool-failure"}`
	result, err := parseReviewResult(input)
	require.NoError(t, err)
	assert.Equal(t, "failure", result.Action)
	assert.Equal(t, "tool-failure", result.Reason)
	assert.Empty(t, result.Body)
}

func TestParseReviewResult_HeadSHA(t *testing.T) {
	input := `{"body": "Review", "action": "approve", "head_sha": "abc1234"}`
	result, err := parseReviewResult(input)
	require.NoError(t, err)
	assert.Equal(t, "abc1234", result.HeadSHA)
}

func TestParseReviewResult_Findings(t *testing.T) {
	input := `{"body":"Review","action":"approve","findings":[{"severity":"low","category":"docs","file":"README.md","line":12,"description":"Missing usage note","remediation":"Add a short note","actionable":true}]}`
	result, err := parseReviewResult(input)
	require.NoError(t, err)
	require.Len(t, result.Findings, 1)
	assert.Equal(t, "low", result.Findings[0].Severity)
	assert.True(t, result.Findings[0].Actionable)
}

func TestReviewActionToEvent(t *testing.T) {
	tests := []struct {
		action    string
		wantEvent string
		wantOK    bool
	}{
		{"approve", "APPROVE", true},
		{"Approve", "APPROVE", true},
		{"request-changes", "REQUEST_CHANGES", true},
		{"request_changes", "", false},
		{"comment", "COMMENT", true},
		{"reject", "REQUEST_CHANGES", true},
		{"Reject", "REQUEST_CHANGES", true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			event, ok := reviewActionToEvent(tt.action)
			assert.Equal(t, tt.wantEvent, event)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestCheckStaleHead_Matches(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PullRequestHeadSHA = "abc1234567890"
	printer := ui.New(io.Discard)

	stale, currentSHA, err := checkStaleHead(context.Background(), fc, "o", "r", 1, "abc1234567890", false, printer)
	require.NoError(t, err)
	assert.False(t, stale)
	assert.Equal(t, "abc1234567890", currentSHA)
}

func TestCheckStaleHead_Stale(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PullRequestHeadSHA = "new_sha_456"
	printer := ui.New(io.Discard)

	stale, currentSHA, err := checkStaleHead(context.Background(), fc, "o", "r", 1, "old_sha_123", false, printer)
	require.NoError(t, err)
	assert.True(t, stale)
	assert.Equal(t, "new_sha_456", currentSHA)
}

func TestCheckStaleHead_DryRun(t *testing.T) {
	fc := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	stale, _, err := checkStaleHead(context.Background(), fc, "o", "r", 1, "any_sha", true, printer)
	require.NoError(t, err)
	assert.False(t, stale, "dry run should not report stale")
}

func TestPostStaleHeadNotice(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	fc.PullRequestHeadSHA = "new_sha_456"
	printer := ui.New(io.Discard)

	cfg := sticky.Config{Marker: "<!-- test -->"}
	err := postStaleHeadNotice(context.Background(), fc, "o", "r", 1, "old_sha_123", "new_sha_456", cfg, printer)
	require.Error(t, err, "should return an error indicating staleness")
	assert.Contains(t, err.Error(), "stale")

	comments := fc.IssueComments["o/r/1"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "stale-head")
	assert.Contains(t, comments[0].Body, "old_sha_123")

	// Verify the error carries StaleHeadExitCode for post-review.sh.
	var she *staleHeadError
	require.ErrorAs(t, err, &she, "error should be *staleHeadError")
	assert.Equal(t, StaleHeadExitCode, she.ExitCode())
}

func TestStaleHeadError_ExitCode(t *testing.T) {
	err := &staleHeadError{reviewedSHA: "aaa", currentSHA: "bbb"}
	assert.Equal(t, StaleHeadExitCode, err.ExitCode())
	assert.Contains(t, err.Error(), "aaa")
	assert.Contains(t, err.Error(), "bbb")
}

func TestPostFailureNotice_WithBody(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	cfg := sticky.Config{Marker: "<!-- test -->"}
	parsed := ReviewResult{Action: "failure", Body: "Custom failure message", Reason: "tool-failure"}
	err := postFailureNotice(context.Background(), fc, "o", "r", 1, parsed, cfg, printer)
	require.NoError(t, err)

	comments := fc.IssueComments["o/r/1"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "Custom failure message")
}

func TestPostFailureNotice_WithoutBody(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	cfg := sticky.Config{Marker: "<!-- test -->"}
	parsed := ReviewResult{Action: "failure", Reason: "token-limit"}
	err := postFailureNotice(context.Background(), fc, "o", "r", 1, parsed, cfg, printer)
	require.NoError(t, err)

	comments := fc.IssueComments["o/r/1"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "token-limit")
	assert.Contains(t, comments[0].Body, "NOT reviewed")
}

func TestPostFailureNotice_EmptyReason(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	cfg := sticky.Config{Marker: "<!-- test -->"}
	parsed := ReviewResult{Action: "failure", Reason: ""}
	err := postFailureNotice(context.Background(), fc, "o", "r", 1, parsed, cfg, printer)
	require.NoError(t, err)

	comments := fc.IssueComments["o/r/1"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "unknown")
	assert.Contains(t, comments[0].Body, "NOT reviewed")
}

func TestCheckStaleHead_CaseInsensitive(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PullRequestHeadSHA = "abc1234567890abcdef1234567890abcdef123456"
	printer := ui.New(io.Discard)

	stale, _, err := checkStaleHead(context.Background(), fc, "o", "r", 1, "ABC1234567890ABCDEF1234567890ABCDEF123456", false, printer)
	require.NoError(t, err)
	assert.False(t, stale, "case-insensitive SHAs should match")
}

func TestSubmitFormalReview_CreatesAndMinimizesStale(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRReviews = map[string][]forge.PullRequestReview{
		"acme/repo/1": {
			{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "COMMENTED", Body: "old review 1"},
			{ID: 200, NodeID: "PRR_200", User: "someone-else", State: "APPROVED", Body: "lgtm"},
			{ID: 300, NodeID: "PRR_300", User: "fullsend-bot", State: "APPROVED", Body: "old review 2"},
		},
	}

	printer := ui.New(io.Discard)
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "abc123def456", "", nil, false, printer)
	require.NoError(t, err)

	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "APPROVE", fc.CreatedReviews[0].Event)
	assert.Equal(t, "abc123def456", fc.CreatedReviews[0].CommitSHA)

	require.Len(t, fc.MinimizedComments, 2)
	assert.Equal(t, "PRR_100", fc.MinimizedComments[0].NodeID)
	assert.Equal(t, "OUTDATED", fc.MinimizedComments[0].Reason)
	assert.Equal(t, "PRR_300", fc.MinimizedComments[1].NodeID)
	assert.Equal(t, "OUTDATED", fc.MinimizedComments[1].Reason)

	assert.Empty(t, fc.DismissedReviews, "no CHANGES_REQUESTED reviews to dismiss")
}

func TestSubmitFormalReview_DismissesStaleRequestChanges(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRReviews = map[string][]forge.PullRequestReview{
		"acme/repo/1": {
			{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "COMMENTED", Body: "old"},
			{ID: 200, NodeID: "PRR_200", User: "fullsend-bot", State: "CHANGES_REQUESTED", Body: "fix this"},
		},
	}

	printer := ui.New(io.Discard)
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", nil, false, printer)
	require.NoError(t, err)

	require.Len(t, fc.DismissedReviews, 1)
	assert.Equal(t, 200, fc.DismissedReviews[0].ReviewID)
	assert.Equal(t, "Superseded by updated review", fc.DismissedReviews[0].Message)
}

func TestSubmitFormalReview_DismissesOnCommentVerdict(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRReviews = map[string][]forge.PullRequestReview{
		"acme/repo/1": {
			{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "CHANGES_REQUESTED", Body: "fix this"},
		},
	}

	printer := ui.New(io.Discard)
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "", "", nil, false, printer)
	require.NoError(t, err)

	require.Len(t, fc.DismissedReviews, 1, "COMMENT verdict must still dismiss stale CHANGES_REQUESTED")
	assert.Equal(t, 100, fc.DismissedReviews[0].ReviewID)
	assert.Empty(t, fc.CreatedReviews, "COMMENT with no inline findings skips formal review")
}

func TestSubmitFormalReview_DryRun(t *testing.T) {
	fc := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", nil, true, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews)
}

func TestSubmitFormalReview_UnknownAction(t *testing.T) {
	fc := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "unknown-action", "", "", nil, false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews)
}

func TestMinimizeStaleReviews_MinimizesAll(t *testing.T) {
	fc := forge.NewFakeClient()
	reviews := []forge.PullRequestReview{
		{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "APPROVED", Body: "only review"},
	}

	printer := ui.New(io.Discard)
	minimizeStaleReviews(context.Background(), fc, "fullsend-bot", reviews, printer)
	require.Len(t, fc.MinimizedComments, 1)
	assert.Equal(t, "PRR_100", fc.MinimizedComments[0].NodeID)
}

func TestMinimizeStaleReviews_ErrorTolerance(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["MinimizeComment"] = fmt.Errorf("GraphQL error")
	reviews := []forge.PullRequestReview{
		{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "COMMENTED", Body: "review 1"},
		{ID: 200, NodeID: "PRR_200", User: "fullsend-bot", State: "APPROVED", Body: "review 2"},
	}

	printer := ui.New(io.Discard)
	minimizeStaleReviews(context.Background(), fc, "fullsend-bot", reviews, printer)
	// soft-fail: no panic despite MinimizeComment errors
}

func TestMinimizeStaleReviews_NoReviews(t *testing.T) {
	fc := forge.NewFakeClient()

	printer := ui.New(io.Discard)
	minimizeStaleReviews(context.Background(), fc, "fullsend-bot", nil, printer)
	assert.Empty(t, fc.MinimizedComments)
}

func TestHexSHAValidation(t *testing.T) {
	tests := []struct {
		sha   string
		valid bool
	}{
		{"abc123f", false},                                 // too short (7 chars)
		{"abc123def456", false},                            // too short (12 chars)
		{"abc123def456abc123def456abc123def456abcd", true}, // 40-char SHA-1
		{"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", true},   // 64-char SHA-256
		{"abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567890", false}, // 65 chars (too long)
		{"", true},               // empty is valid (means "no SHA provided")
		{"not-hex!", false},      // non-hex chars
		{"abc 123", false},       // spaces
		{"abc123`inject", false}, // backtick injection
		{"ABC123DEF456ABC123DEF456ABC123DEF456ABCD", true}, // uppercase 40-char
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("sha=%q", tt.sha), func(t *testing.T) {
			if tt.sha == "" {
				assert.True(t, tt.valid)
				return
			}
			assert.Equal(t, tt.valid, hexSHARe.MatchString(tt.sha))
		})
	}
}

func TestReasonValidation(t *testing.T) {
	tests := []struct {
		reason string
		valid  bool
	}{
		{"agent-no-output", true},
		{"tool_failure", true},
		{"token-limit", true},
		{"", true},
		{"reason with spaces", false},
		{"markdown\n**injection**", false},
		{"<script>alert(1)</script>", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("reason=%q", tt.reason), func(t *testing.T) {
			assert.Equal(t, tt.valid, reasonRe.MatchString(tt.reason))
		})
	}
}

func TestSubmitFormalReview_PassesCommitSHA(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "deadbeef1234", "", nil, false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews, "COMMENT with no inline findings should skip formal review")
}

func TestSubmitFormalReview_EmptyCommitSHA(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", nil, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "", fc.CreatedReviews[0].CommitSHA)
}

func TestSubmitFormalReview_ApproveEmptyBody(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", nil, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Empty(t, fc.CreatedReviews[0].Body, "APPROVE body should be empty to avoid duplicate notifications")
}

func TestSubmitFormalReview_RequestChangesIncludesCommentURL(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	commentURL := "https://github.com/acme/repo/pull/1#issuecomment-42"
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "request-changes", "", commentURL, nil, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "REQUEST_CHANGES", fc.CreatedReviews[0].Event)
	assert.Contains(t, fc.CreatedReviews[0].Body, commentURL)
	assert.Contains(t, fc.CreatedReviews[0].Body, "[review comment]")
}

func TestSubmitFormalReview_RequestChangesFallbackWithoutURL(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "request-changes", "", "", nil, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "REQUEST_CHANGES", fc.CreatedReviews[0].Event)
	assert.Equal(t, "See the review comment above for full details.", fc.CreatedReviews[0].Body)
}

func TestSubmitFormalReview_RejectSubmitsRequestChanges(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	commentURL := "https://github.com/acme/repo/pull/1#issuecomment-99"
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "reject", "abc123", commentURL, nil, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "REQUEST_CHANGES", fc.CreatedReviews[0].Event)
	assert.Contains(t, fc.CreatedReviews[0].Body, commentURL)
}

func TestSubmitFormalReview_CommentSkippedWithoutFindings(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "", "", nil, false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews, "COMMENT events with no inline findings should skip formal review")
}

func TestSubmitFormalReview_CommentSubmittedWithInlineFindings(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRFileDiffs = map[string][]forge.PullRequestFileDiff{
		"acme/repo/1": {
			{Path: "internal/service.go", Patch: "@@ -30,20 +30,25 @@ func main() {"},
		},
	}
	printer := ui.New(io.Discard)

	findings := []ReviewFinding{
		{
			Severity:    "medium",
			Category:    "logic-error",
			File:        "internal/service.go",
			Line:        42,
			Description: "Nil pointer dereference possible.",
		},
	}

	commentURL := "https://github.com/acme/repo/pull/1#issuecomment-99"
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "", commentURL, findings, false, printer)
	require.NoError(t, err)

	require.Len(t, fc.CreatedReviews, 1)
	review := fc.CreatedReviews[0]
	assert.Equal(t, "COMMENT", review.Event)
	assert.Contains(t, review.Body, commentURL)
	assert.Contains(t, review.Body, "[review comment]")
	require.Len(t, review.Comments, 1)
	assert.Equal(t, "internal/service.go", review.Comments[0].Path)
	assert.Equal(t, 42, review.Comments[0].Line)
}

func TestSubmitFormalReview_CommentSkippedWhenFindingsFilteredOut(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRFileDiffs = map[string][]forge.PullRequestFileDiff{
		"acme/repo/1": {
			{Path: "other.go", Patch: "@@ -1,5 +1,10 @@ package other"},
		},
	}
	printer := ui.New(io.Discard)

	findings := []ReviewFinding{
		{
			Severity:    "low",
			Category:    "style",
			File:        "not-in-diff.go",
			Line:        10,
			Description: "File not in PR diff.",
		},
	}

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "", "", findings, false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews, "COMMENT with no inline-eligible findings should skip formal review")
}

func TestDismissStaleRequestChanges(t *testing.T) {
	tests := []struct {
		name          string
		user          string
		reviews       []forge.PullRequestReview
		newEvent      string
		dismissErr    error
		wantDismissed int
		wantReviewID  int
	}{
		{
			name:     "softening from request-changes to comment",
			user:     "bot",
			newEvent: "COMMENT",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "CHANGES_REQUESTED"},
			},
			wantDismissed: 1,
			wantReviewID:  100,
		},
		{
			name:     "softening from request-changes to approve",
			user:     "bot",
			newEvent: "APPROVE",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "CHANGES_REQUESTED"},
			},
			wantDismissed: 1,
			wantReviewID:  100,
		},
		{
			name:     "same severity skips dismiss",
			user:     "bot",
			newEvent: "REQUEST_CHANGES",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "CHANGES_REQUESTED"},
			},
			wantDismissed: 0,
		},
		{
			name:          "no prior reviews",
			user:          "bot",
			newEvent:      "COMMENT",
			reviews:       nil,
			wantDismissed: 0,
		},
		{
			name:     "prior is commented not changes-requested",
			user:     "bot",
			newEvent: "COMMENT",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "COMMENTED"},
			},
			wantDismissed: 0,
		},
		{
			name:     "prior is approved and new is request-changes",
			user:     "bot",
			newEvent: "REQUEST_CHANGES",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "APPROVED"},
			},
			wantDismissed: 0,
		},
		{
			name:     "different user review ignored",
			user:     "bot",
			newEvent: "COMMENT",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "someone-else", State: "CHANGES_REQUESTED"},
			},
			wantDismissed: 0,
		},
		{
			name:     "multiple CR reviews dismisses all",
			user:     "bot",
			newEvent: "COMMENT",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "CHANGES_REQUESTED"},
				{ID: 200, User: "bot", State: "CHANGES_REQUESTED"},
			},
			wantDismissed: 2,
			wantReviewID:  100,
		},
		{
			name:     "dismiss API error is non-fatal",
			user:     "bot",
			newEvent: "COMMENT",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "CHANGES_REQUESTED"},
			},
			dismissErr:    fmt.Errorf("API error"),
			wantDismissed: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			if tt.dismissErr != nil {
				fc.Errors["DismissPullRequestReview"] = tt.dismissErr
			}

			printer := ui.New(io.Discard)
			dismissStaleRequestChanges(context.Background(), fc, "acme", "repo", 1, tt.newEvent, tt.user, tt.reviews, printer)

			assert.Len(t, fc.DismissedReviews, tt.wantDismissed)
			if tt.wantDismissed > 0 {
				assert.Equal(t, tt.wantReviewID, fc.DismissedReviews[0].ReviewID)
			}
		})
	}
}

func TestSubmitFormalReview_DismissesAllStaleRequestChanges(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRReviews = map[string][]forge.PullRequestReview{
		"acme/repo/1": {
			{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "CHANGES_REQUESTED", Body: "fix this"},
			{ID: 200, NodeID: "PRR_200", User: "someone-else", State: "CHANGES_REQUESTED", Body: "human review"},
			{ID: 300, NodeID: "PRR_300", User: "fullsend-bot", State: "CHANGES_REQUESTED", Body: "still broken"},
			{ID: 400, NodeID: "PRR_400", User: "fullsend-bot", State: "CHANGES_REQUESTED", Body: "try again"},
			{ID: 500, NodeID: "PRR_500", User: "fullsend-bot", State: "COMMENTED", Body: "note"},
		},
	}

	printer := ui.New(io.Discard)
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "", "", nil, false, printer)
	require.NoError(t, err)

	// All 3 CHANGES_REQUESTED reviews by the bot should be dismissed.
	require.Len(t, fc.DismissedReviews, 3)
	assert.Equal(t, 100, fc.DismissedReviews[0].ReviewID)
	assert.Equal(t, 300, fc.DismissedReviews[1].ReviewID)
	assert.Equal(t, 400, fc.DismissedReviews[2].ReviewID)

	// The other user's review must not be dismissed.
	for _, d := range fc.DismissedReviews {
		assert.NotEqual(t, 200, d.ReviewID)
	}

	// COMMENT events skip formal review submission.
	assert.Empty(t, fc.CreatedReviews)
}

func TestSubmitFormalReview_AuthErrorSkipsCleanup(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetAuthenticatedUser"] = fmt.Errorf("auth error")
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", nil, false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.DismissedReviews)
	assert.Empty(t, fc.MinimizedComments)
	require.Len(t, fc.CreatedReviews, 1, "review should still be created despite auth error")
}

func TestSubmitFormalReview_ListErrorSkipsCleanup(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	fc.Errors["ListPullRequestReviews"] = fmt.Errorf("list error")
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", nil, false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.DismissedReviews)
	assert.Empty(t, fc.MinimizedComments)
	require.Len(t, fc.CreatedReviews, 1, "review should still be created despite list error")
}

func TestSubmitFormalReview_AttachesInlineComments(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	findings := []ReviewFinding{
		{
			Severity:    "high",
			Category:    "missing-test",
			File:        "internal/service.go",
			Line:        42,
			Description: "Missing test coverage for error path.",
			Remediation: "Add a unit test for the error case.",
		},
		{
			Severity:    "medium",
			Category:    "logic-error",
			File:        "internal/handler.go",
			Line:        10,
			Description: "Nil pointer dereference possible.",
		},
	}

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "request-changes", "abc123", "", findings, false, printer)
	require.NoError(t, err)

	require.Len(t, fc.CreatedReviews, 1)
	review := fc.CreatedReviews[0]
	assert.Equal(t, "REQUEST_CHANGES", review.Event)
	require.Len(t, review.Comments, 2)

	assert.Equal(t, "internal/service.go", review.Comments[0].Path)
	assert.Equal(t, 42, review.Comments[0].Line)
	assert.Contains(t, review.Comments[0].Body, "high")
	assert.Contains(t, review.Comments[0].Body, "missing-test")
	assert.Contains(t, review.Comments[0].Body, "Missing test coverage")
	assert.Contains(t, review.Comments[0].Body, "Suggested fix:")

	assert.Equal(t, "internal/handler.go", review.Comments[1].Path)
	assert.Equal(t, 10, review.Comments[1].Line)
	assert.Contains(t, review.Comments[1].Body, "medium")
	assert.Contains(t, review.Comments[1].Body, "Nil pointer dereference")
	assert.NotContains(t, review.Comments[1].Body, "Suggested fix:")
}

func TestSubmitFormalReview_SkipsFindingsWithoutLocation(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	findings := []ReviewFinding{
		{
			Severity:    "high",
			Category:    "missing-test",
			File:        "internal/service.go",
			Line:        42,
			Description: "Has location.",
		},
		{
			Severity:    "medium",
			Category:    "style",
			File:        "",
			Description: "No file path.",
		},
		{
			Severity:    "low",
			Category:    "docs",
			File:        "README.md",
			Line:        0,
			Description: "No line number.",
		},
	}

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "request-changes", "", "", findings, false, printer)
	require.NoError(t, err)

	require.Len(t, fc.CreatedReviews, 1)
	require.Len(t, fc.CreatedReviews[0].Comments, 1, "only the finding with file+line should become an inline comment")
	assert.Equal(t, "internal/service.go", fc.CreatedReviews[0].Comments[0].Path)
}

func TestParseDiffLineRanges(t *testing.T) {
	tests := []struct {
		name   string
		patch  string
		expect [][2]int
	}{
		{
			name:   "single hunk",
			patch:  "@@ -10,5 +12,7 @@ func foo() {",
			expect: [][2]int{{12, 18}},
		},
		{
			name:   "multiple hunks",
			patch:  "@@ -1,3 +1,4 @@ header\n context\n+added\n@@ -20,5 +21,3 @@ other",
			expect: [][2]int{{1, 4}, {21, 23}},
		},
		{
			name:   "new file single hunk",
			patch:  "@@ -0,0 +1,50 @@",
			expect: [][2]int{{1, 50}},
		},
		{
			name:   "deletion only hunk size 0",
			patch:  "@@ -5,3 +5,0 @@ removed",
			expect: nil,
		},
		{
			name:   "omitted size defaults to 1",
			patch:  "@@ -1 +1 @@",
			expect: [][2]int{{1, 1}},
		},
		{
			name:   "empty patch",
			patch:  "",
			expect: nil,
		},
		{
			name:   "mixed hunks with deletion",
			patch:  "@@ -1,3 +1,5 @@ first\n context\n@@ -10,4 +12,0 @@ deleted\n@@ -20,2 +18,3 @@ third",
			expect: [][2]int{{1, 5}, {18, 20}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiffLineRanges(tt.patch)
			assert.Equal(t, tt.expect, got)
		})
	}
}

func TestFindingsToReviewComments(t *testing.T) {
	findings := []ReviewFinding{
		{File: "a.go", Line: 10, Severity: "high", Category: "bug", Description: "Desc A"},
		{File: "", Line: 5, Severity: "low", Category: "style", Description: "No file"},
		{File: "b.go", Line: 0, Severity: "info", Category: "docs", Description: "No line"},
		{File: "c.go", Line: 20, Severity: "critical", Category: "security", Description: "Desc C", Remediation: "Fix it"},
	}

	comments, fileFiltered, fileLevelFallback := findingsToReviewComments(findings, nil)
	assert.Equal(t, 0, fileFiltered)
	assert.Equal(t, 0, fileLevelFallback)
	require.Len(t, comments, 2)

	assert.Equal(t, "a.go", comments[0].Path)
	assert.Equal(t, 10, comments[0].Line)
	assert.Contains(t, comments[0].Body, "high")
	assert.Contains(t, comments[0].Body, "Desc A")

	assert.Equal(t, "c.go", comments[1].Path)
	assert.Equal(t, 20, comments[1].Line)
	assert.Contains(t, comments[1].Body, "critical")
	assert.Contains(t, comments[1].Body, "Fix it")
}

func TestFindingsToReviewComments_FiltersByDiffHunks(t *testing.T) {
	findings := []ReviewFinding{
		{File: "changed.go", Line: 10, Severity: "high", Category: "bug", Description: "In hunk"},
		{File: "changed.go", Line: 50, Severity: "low", Category: "style", Description: "Outside hunk"},
		{File: "not-changed.go", Line: 5, Severity: "low", Category: "docs", Description: "Not in diff"},
		{File: "also-changed.go", Line: 3, Severity: "medium", Category: "style", Description: "In hunk"},
	}
	diffHunks := map[string][][2]int{
		"changed.go":      {{5, 15}},
		"also-changed.go": {{1, 10}},
	}

	comments, fileFiltered, fileLevelFallback := findingsToReviewComments(findings, diffHunks)
	assert.Equal(t, 1, fileFiltered)
	assert.Equal(t, 1, fileLevelFallback, "low-severity out-of-hunk finding should fall back to file-level")
	require.Len(t, comments, 3)
	assert.Equal(t, "changed.go", comments[0].Path)
	assert.Equal(t, 10, comments[0].Line)
	// The out-of-hunk low finding now falls back to file-level.
	assert.Equal(t, "changed.go", comments[1].Path)
	assert.Equal(t, 0, comments[1].Line)
	assert.Contains(t, comments[1].Body, "Line 50", "file-level fallback should include original line number")
	assert.Equal(t, "also-changed.go", comments[2].Path)
	assert.Equal(t, 3, comments[2].Line)
}

func TestFindingsToReviewComments_EmptyPatchSkipsLineFiltering(t *testing.T) {
	findings := []ReviewFinding{
		{File: "binary.png", Line: 1, Severity: "high", Category: "bug", Description: "On binary file"},
		{File: "large.go", Line: 999, Severity: "medium", Category: "style", Description: "On truncated-patch file"},
		{File: "changed.go", Line: 10, Severity: "low", Category: "bug", Description: "In hunk"},
		{File: "changed.go", Line: 50, Severity: "info", Category: "docs", Description: "Outside hunk"},
	}
	diffHunks := map[string][][2]int{
		"binary.png": nil,
		"large.go":   nil,
		"changed.go": {{5, 15}},
	}

	comments, fileFiltered, fileLevelFallback := findingsToReviewComments(findings, diffHunks)
	assert.Equal(t, 0, fileFiltered)
	assert.Equal(t, 1, fileLevelFallback, "out-of-hunk info finding on changed.go should fall back to file-level")
	require.Len(t, comments, 4)
	assert.Equal(t, "binary.png", comments[0].Path)
	assert.Equal(t, "large.go", comments[1].Path)
	assert.Equal(t, "changed.go", comments[2].Path)
	assert.Equal(t, 10, comments[2].Line)
	// The info finding outside the hunk now falls back to file-level.
	assert.Equal(t, "changed.go", comments[3].Path)
	assert.Equal(t, 0, comments[3].Line)
	assert.Contains(t, comments[3].Body, "Line 50", "file-level fallback should include original line number")
}

func TestFindingsToReviewComments_AllSeveritiesPassThrough(t *testing.T) {
	findings := []ReviewFinding{
		{File: "a.go", Line: 10, Severity: "info", Category: "docs", Description: "Info finding with location"},
		{File: "a.go", Line: 15, Severity: "Info", Category: "docs", Description: "Info finding case insensitive"},
		{File: "a.go", Line: 20, Severity: "low", Category: "style", Description: "Low finding"},
		{File: "a.go", Line: 25, Severity: "medium", Category: "bug", Description: "Medium finding"},
	}

	comments, fileFiltered, fileLevelFallback := findingsToReviewComments(findings, nil)
	assert.Equal(t, 0, fileFiltered)
	assert.Equal(t, 0, fileLevelFallback)
	require.Len(t, comments, 4, "all findings should pass through regardless of severity")
	assert.Contains(t, comments[0].Body, "Info finding with location")
	assert.Contains(t, comments[1].Body, "Info finding case insensitive")
	assert.Contains(t, comments[2].Body, "Low finding")
	assert.Contains(t, comments[3].Body, "Medium finding")
}

func TestFindingsToReviewComments_AllSeveritiesFallbackToFileLevel(t *testing.T) {
	findings := []ReviewFinding{
		{File: "changed.go", Line: 10, Severity: "high", Category: "bug", Description: "In hunk"},
		{File: "changed.go", Line: 50, Severity: "medium", Category: "logic-error", Description: "Medium outside hunk"},
		{File: "changed.go", Line: 60, Severity: "critical", Category: "security", Description: "Critical outside hunk"},
		{File: "changed.go", Line: 70, Severity: "low", Category: "style", Description: "Low outside hunk"},
		{File: "changed.go", Line: 75, Severity: "info", Category: "docs", Description: "Info outside hunk"},
		{File: "changed.go", Line: 80, Severity: "High", Category: "bug", Description: "High outside hunk case insensitive"},
	}
	diffHunks := map[string][][2]int{
		"changed.go": {{5, 15}},
	}

	comments, fileFiltered, fileLevelFallback := findingsToReviewComments(findings, diffHunks)
	assert.Equal(t, 0, fileFiltered)
	assert.Equal(t, 5, fileLevelFallback, "all out-of-hunk findings should fall back to file-level")
	require.Len(t, comments, 6)

	// First comment: in-hunk high finding with line number.
	assert.Equal(t, "changed.go", comments[0].Path)
	assert.Equal(t, 10, comments[0].Line)

	// Remaining: file-level fallback comments for all out-of-hunk findings.
	expectedLines := []int{50, 60, 70, 75, 80}
	for i, desc := range []string{"Medium outside hunk", "Critical outside hunk", "Low outside hunk", "Info outside hunk", "High outside hunk case insensitive"} {
		idx := i + 1
		assert.Equal(t, "changed.go", comments[idx].Path)
		assert.Equal(t, 0, comments[idx].Line, "file-level comment should have Line=0")
		assert.Contains(t, comments[idx].Body, desc)
		assert.Contains(t, comments[idx].Body, fmt.Sprintf("Line %d", expectedLines[i]), "file-level fallback should include original line number")
	}
}

func TestSubmitFormalReview_FiltersByPRFileDiffs(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRFileDiffs = map[string][]forge.PullRequestFileDiff{
		"acme/repo/1": {
			{Path: "changed.go", Patch: "@@ -5,10 +5,12 @@ func main() {"},
			{Path: "also-changed.go", Patch: "@@ -1,5 +1,25 @@ package foo"},
		},
	}
	var out bytes.Buffer
	printer := ui.New(&out)

	findings := []ReviewFinding{
		{File: "changed.go", Line: 10, Severity: "high", Category: "bug", Description: "In hunk"},
		{File: "changed.go", Line: 50, Severity: "low", Category: "style", Description: "Outside hunk"},
		{File: "not-in-diff.go", Line: 5, Severity: "medium", Category: "style", Description: "File not in diff"},
		{File: "also-changed.go", Line: 20, Severity: "low", Category: "docs", Description: "In hunk"},
	}

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "request-changes", "", "", findings, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	require.Len(t, fc.CreatedReviews[0].Comments, 3, "file-not-in-diff finding omitted; out-of-hunk finding falls back to file-level")
	assert.Equal(t, "changed.go", fc.CreatedReviews[0].Comments[0].Path)
	assert.Equal(t, 10, fc.CreatedReviews[0].Comments[0].Line)
	// Out-of-hunk low finding falls back to file-level comment.
	assert.Equal(t, "changed.go", fc.CreatedReviews[0].Comments[1].Path)
	assert.Equal(t, 0, fc.CreatedReviews[0].Comments[1].Line)
	assert.Contains(t, fc.CreatedReviews[0].Comments[1].Body, "Line 50", "file-level fallback should include original line number")
	assert.Equal(t, "also-changed.go", fc.CreatedReviews[0].Comments[2].Path)
	assert.Contains(t, out.String(), "1 inline comment(s) omitted (file not in PR diff) — findings still count toward verdict")
	assert.Contains(t, out.String(), "1 finding(s) posted as file-level comment(s) (line outside diff hunk)")
}

func TestSubmitFormalReview_ListPRFileDiffsErrorFallsBack(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.Errors["ListPullRequestFileDiffs"] = fmt.Errorf("API rate limited")
	printer := ui.New(io.Discard)

	findings := []ReviewFinding{
		{File: "any-file.go", Line: 10, Severity: "high", Category: "bug", Description: "Should pass through"},
	}

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "request-changes", "", "", findings, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	require.Len(t, fc.CreatedReviews[0].Comments, 1, "all comments should pass through when ListPullRequestFileDiffs fails")
	assert.Equal(t, "any-file.go", fc.CreatedReviews[0].Comments[0].Path)
}

func TestSubmitFormalReview_EmptyPRFileDiffListFallsBack(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRFileDiffs = map[string][]forge.PullRequestFileDiff{
		"acme/repo/1": {},
	}
	var out bytes.Buffer
	printer := ui.New(&out)

	findings := []ReviewFinding{
		{File: "any-file.go", Line: 10, Severity: "high", Category: "bug", Description: "Should pass through"},
	}

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "request-changes", "", "", findings, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	require.Len(t, fc.CreatedReviews[0].Comments, 1, "comments pass through unfiltered when PR file list is empty")
	assert.Contains(t, out.String(), "PR file list is empty")
}

func TestFormatFindingComment(t *testing.T) {
	t.Run("with remediation", func(t *testing.T) {
		f := ReviewFinding{
			Severity:    "high",
			Category:    "missing-test",
			Description: "No coverage for error path.",
			Remediation: "Add a unit test.",
		}
		body := formatFindingComment(f)
		assert.Contains(t, body, "**[high]** missing-test")
		assert.Contains(t, body, "No coverage for error path.")
		assert.Contains(t, body, "**Suggested fix:** Add a unit test.")
	})

	t.Run("without remediation", func(t *testing.T) {
		f := ReviewFinding{
			Severity:    "low",
			Category:    "style",
			Description: "Consider renaming.",
		}
		body := formatFindingComment(f)
		assert.Contains(t, body, "**[low]** style")
		assert.Contains(t, body, "Consider renaming.")
		assert.NotContains(t, body, "Suggested fix:")
	})
}

func TestSanitizeReviewResult_RedactsSecretsInBody(t *testing.T) {
	printer := ui.New(io.Discard)
	secret := "ghp_FAKEtesttoken000000000000000000000000"
	r := ReviewResult{
		Body:   "Found this token: " + secret + " in the code.",
		Action: "comment",
	}

	sanitized := sanitizeReviewResult(r, printer)
	assert.NotContains(t, sanitized.Body, "ghp_FAKEtest", "secret should be redacted from body")
	assert.Contains(t, sanitized.Body, "Found this token:", "non-secret text should remain")
}

func TestSanitizeReviewResult_RedactsSecretsInFindings(t *testing.T) {
	printer := ui.New(io.Discard)
	secret := "ghp_FAKEtesttoken000000000000000000000000"
	r := ReviewResult{
		Body:   "Review body without secrets.",
		Action: "request-changes",
		Findings: []ReviewFinding{
			{
				Severity:    "high",
				Category:    "security",
				File:        "main.go",
				Line:        10,
				Description: "Hardcoded token: " + secret,
				Remediation: "Remove " + secret + " and use env var.",
			},
		},
	}

	sanitized := sanitizeReviewResult(r, printer)
	assert.NotContains(t, sanitized.Findings[0].Description, "ghp_FAKEtest", "secret should be redacted from finding description")
	assert.NotContains(t, sanitized.Findings[0].Remediation, "ghp_FAKEtest", "secret should be redacted from finding remediation")
	assert.Contains(t, sanitized.Findings[0].Description, "Hardcoded token:", "non-secret text should remain")
}

func TestSanitizeReviewResult_RedactsSecretsInSeverityAndCategory(t *testing.T) {
	printer := ui.New(io.Discard)
	secret := "ghp_FAKEtesttoken000000000000000000000000"
	r := ReviewResult{
		Body:   "Review body without secrets.",
		Action: "request-changes",
		Findings: []ReviewFinding{
			{
				Severity:    "high " + secret,
				Category:    "security " + secret,
				File:        "main.go",
				Line:        10,
				Description: "Clean description.",
			},
		},
	}

	sanitized := sanitizeReviewResult(r, printer)
	assert.NotContains(t, sanitized.Findings[0].Severity, "ghp_FAKEtest", "secret should be redacted from finding severity")
	assert.NotContains(t, sanitized.Findings[0].Category, "ghp_FAKEtest", "secret should be redacted from finding category")
}

func TestSanitizeReviewResult_ZeroWidthObfuscatedSecret(t *testing.T) {
	printer := ui.New(io.Discard)
	plain := "ghp_FAKEtesttoken000000000000000000000000"
	// Interleave zero-width non-joiner characters to obfuscate the token.
	var obfuscated string
	for _, c := range plain {
		obfuscated += string(c) + "\u200c"
	}
	r := ReviewResult{
		Body:   "Token: " + obfuscated,
		Action: "comment",
	}

	sanitized := sanitizeReviewResult(r, printer)
	assert.NotContains(t, sanitized.Body, "ghp_FAKEtest", "zero-width obfuscated secret should be caught after normalization")
}

func TestSanitizeReviewResult_NoSecretsPassesThrough(t *testing.T) {
	printer := ui.New(io.Discard)
	r := ReviewResult{
		Body:   "Looks good! No issues found.",
		Action: "approve",
		Findings: []ReviewFinding{
			{
				Severity:    "low",
				Category:    "style",
				File:        "main.go",
				Line:        5,
				Description: "Consider renaming variable.",
			},
		},
	}

	sanitized := sanitizeReviewResult(r, printer)
	assert.Equal(t, "Looks good! No issues found.", sanitized.Body, "clean body should pass through unchanged")
	assert.Equal(t, "Consider renaming variable.", sanitized.Findings[0].Description, "clean finding should pass through unchanged")
	assert.Equal(t, "low", sanitized.Findings[0].Severity, "clean severity should pass through unchanged")
	assert.Equal(t, "style", sanitized.Findings[0].Category, "clean category should pass through unchanged")
}

func TestSanitizeReviewResult_EmptyBody(t *testing.T) {
	printer := ui.New(io.Discard)
	r := ReviewResult{
		Body:   "",
		Action: "failure",
		Reason: "tool-failure",
	}

	sanitized := sanitizeReviewResult(r, printer)
	assert.Empty(t, sanitized.Body, "empty body should remain empty")
}

func TestPostApprovedFollowUpIssues_DisabledIsNoop(t *testing.T) {
	// Issue creation is disabled (#1137). Verify the function is a no-op for
	// approve actions with actionable findings.
	printer := ui.New(io.Discard)
	parsed := ReviewResult{
		Action: "approve",
		Findings: []ReviewFinding{
			{
				Severity:    "low",
				Category:    "missing-test",
				File:        "internal/service.go",
				Line:        42,
				Description: "Add coverage for the empty response path.",
				Actionable:  true,
			},
		},
	}

	err := postApprovedFollowUpIssues(context.Background(), "acme", "repo", 9, parsed, printer)
	require.NoError(t, err)
}
