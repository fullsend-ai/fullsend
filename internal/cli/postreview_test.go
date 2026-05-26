package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.Empty(t, fc.CreatedReviews, "COMMENT events skip formal review submission")
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
	assert.Empty(t, fc.CreatedReviews, "COMMENT events should skip formal review")
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

func TestSubmitFormalReview_CommentSkipped(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "", "", nil, false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews, "COMMENT events should skip formal review")
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
			name:     "multiple CR reviews dismisses most recent only",
			user:     "bot",
			newEvent: "COMMENT",
			reviews: []forge.PullRequestReview{
				{ID: 100, User: "bot", State: "CHANGES_REQUESTED"},
				{ID: 200, User: "bot", State: "CHANGES_REQUESTED"},
			},
			wantDismissed: 1,
			wantReviewID:  200,
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

	comments, fileFiltered, lineFiltered := findingsToReviewComments(findings, nil)
	assert.Equal(t, 0, fileFiltered)
	assert.Equal(t, 0, lineFiltered)
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

	comments, fileFiltered, lineFiltered := findingsToReviewComments(findings, diffHunks)
	assert.Equal(t, 1, fileFiltered)
	assert.Equal(t, 1, lineFiltered)
	require.Len(t, comments, 2)
	assert.Equal(t, "changed.go", comments[0].Path)
	assert.Equal(t, 10, comments[0].Line)
	assert.Equal(t, "also-changed.go", comments[1].Path)
	assert.Equal(t, 3, comments[1].Line)
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

	comments, fileFiltered, lineFiltered := findingsToReviewComments(findings, diffHunks)
	assert.Equal(t, 0, fileFiltered)
	assert.Equal(t, 1, lineFiltered, "only the out-of-hunk finding on changed.go should be filtered")
	require.Len(t, comments, 3)
	assert.Equal(t, "binary.png", comments[0].Path)
	assert.Equal(t, "large.go", comments[1].Path)
	assert.Equal(t, "changed.go", comments[2].Path)
	assert.Equal(t, 10, comments[2].Line)
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
	require.Len(t, fc.CreatedReviews[0].Comments, 2, "file-filtered and line-filtered findings should be omitted")
	assert.Equal(t, "changed.go", fc.CreatedReviews[0].Comments[0].Path)
	assert.Equal(t, "also-changed.go", fc.CreatedReviews[0].Comments[1].Path)
	assert.Contains(t, out.String(), "1 inline comment(s) omitted (file not in PR diff) — findings still count toward verdict")
	assert.Contains(t, out.String(), "1 inline comment(s) omitted (line not in any diff hunk) — findings still count toward verdict")
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
