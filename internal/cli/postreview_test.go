package cli

import (
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

func TestReviewActionToEvent(t *testing.T) {
	tests := []struct {
		action    string
		wantEvent string
		wantOK    bool
	}{
		{"approve", "APPROVE", true},
		{"Approve", "APPROVE", true},
		{"request-changes", "REQUEST_CHANGES", true},
		{"request_changes", "REQUEST_CHANGES", true},
		{"comment", "COMMENT", true},
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
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "abc123def456", "", false, printer)
	require.NoError(t, err)

	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "APPROVE", fc.CreatedReviews[0].Event)
	assert.Equal(t, "abc123def456", fc.CreatedReviews[0].CommitSHA)

	require.Len(t, fc.MinimizedComments, 2)
	assert.Equal(t, "PRR_100", fc.MinimizedComments[0].NodeID)
	assert.Equal(t, "OUTDATED", fc.MinimizedComments[0].Reason)
	assert.Equal(t, "PRR_300", fc.MinimizedComments[1].NodeID)
	assert.Equal(t, "OUTDATED", fc.MinimizedComments[1].Reason)
}

func TestSubmitFormalReview_DryRun(t *testing.T) {
	fc := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", true, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews)
}

func TestSubmitFormalReview_UnknownAction(t *testing.T) {
	fc := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "unknown-action", "", "", false, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.CreatedReviews)
}

func TestMinimizeStaleReviews_MinimizesAll(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRReviews = map[string][]forge.PullRequestReview{
		"acme/repo/1": {
			{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "APPROVED", Body: "only review"},
		},
	}

	printer := ui.New(io.Discard)
	err := minimizeStaleReviews(context.Background(), fc, "acme", "repo", 1, printer)
	require.NoError(t, err)
	require.Len(t, fc.MinimizedComments, 1)
	assert.Equal(t, "PRR_100", fc.MinimizedComments[0].NodeID)
}

func TestMinimizeStaleReviews_ErrorTolerance(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	fc.PRReviews = map[string][]forge.PullRequestReview{
		"acme/repo/1": {
			{ID: 100, NodeID: "PRR_100", User: "fullsend-bot", State: "COMMENTED", Body: "review 1"},
			{ID: 200, NodeID: "PRR_200", User: "fullsend-bot", State: "APPROVED", Body: "review 2"},
		},
	}
	fc.Errors["MinimizeComment"] = fmt.Errorf("GraphQL error")

	printer := ui.New(io.Discard)
	err := minimizeStaleReviews(context.Background(), fc, "acme", "repo", 1, printer)
	require.NoError(t, err, "minimizeStaleReviews should not return error when MinimizeComment fails")
}

func TestMinimizeStaleReviews_NoReviews(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"

	printer := ui.New(io.Discard)
	err := minimizeStaleReviews(context.Background(), fc, "acme", "repo", 1, printer)
	require.NoError(t, err)
	assert.Empty(t, fc.MinimizedComments)
}

func TestHexSHAValidation(t *testing.T) {
	tests := []struct {
		sha   string
		valid bool
	}{
		{"abc123f", false},                                                                    // too short (7 chars)
		{"abc123def456", false},                                                               // too short (12 chars)
		{"abc123def456abc123def456abc123def456abcd", true},                                     // 40-char SHA-1
		{"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", true},  // 64-char SHA-256
		{"abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567890", false}, // 65 chars (too long)
		{"", true},          // empty is valid (means "no SHA provided")
		{"not-hex!", false},  // non-hex chars
		{"abc 123", false},   // spaces
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

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "comment", "deadbeef1234", "", false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "COMMENT", fc.CreatedReviews[0].Event)
	assert.Equal(t, "deadbeef1234", fc.CreatedReviews[0].CommitSHA)
}

func TestSubmitFormalReview_EmptyCommitSHA(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "", fc.CreatedReviews[0].CommitSHA)
}

func TestSubmitFormalReview_IncludesCommentURL(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	commentURL := "https://github.com/acme/repo/pull/1#issuecomment-42"
	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", commentURL, false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Contains(t, fc.CreatedReviews[0].Body, commentURL)
	assert.Contains(t, fc.CreatedReviews[0].Body, "[review comment]")
}

func TestSubmitFormalReview_FallbackWithoutCommentURL(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)

	err := submitFormalReview(context.Background(), fc, "acme", "repo", 1, "approve", "", "", false, printer)
	require.NoError(t, err)
	require.Len(t, fc.CreatedReviews, 1)
	assert.Equal(t, "See the review comment above for full details.", fc.CreatedReviews[0].Body)
}
