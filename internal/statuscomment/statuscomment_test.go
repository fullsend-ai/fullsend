package statuscomment

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

func fixedTime() time.Time {
	return time.Date(2026, 6, 3, 14, 34, 0, 0, time.UTC)
}

func newTestNotifier(fc *forge.FakeClient, cfg config.StatusNotificationConfig) *Notifier {
	fc.AuthenticatedUser = "fullsend-bot[bot]"
	n := New(fc, cfg, "org", "repo", 7, "https://ci/run/42", "a1b2c3d4e5f6789", "run-42")
	n.now = fixedTime
	return n
}

func TestPostStart_CommentEnabled(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Reviewing this PR")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "<!-- fullsend:agent-status:run-42 -->")
	assert.Contains(t, comments[0].Body, "🤖 Reviewing this PR · Started 2:34 PM UTC")
	assert.Contains(t, comments[0].Body, "Commit: `a1b2c3d`")
	assert.Contains(t, comments[0].Body, "[View workflow run →](https://ci/run/42)")
}

func TestPostStart_CommentDisabled(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working on issue")
	require.NoError(t, err)

	assert.Empty(t, fc.IssueComments)
}

func TestPostStart_DefaultEnabled(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	assert.Len(t, fc.IssueComments["org/repo/7"], 1)
}

func TestPostCompletion_EditInPlace(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Reviewing this PR")
	require.NoError(t, err)
	require.Equal(t, 1, n.startCommentID)

	completionTime := fixedTime().Add(7 * time.Minute)
	n.now = func() time.Time { return completionTime }

	err = n.PostCompletion(context.Background(), "Reviewing this PR", "success")
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 1, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Reviewing this PR")
	assert.Contains(t, fc.UpdatedComments[0].Body, "✅ Success")
	assert.Contains(t, fc.UpdatedComments[0].Body, "Started 2:34 PM UTC")
	assert.Contains(t, fc.UpdatedComments[0].Body, "Completed 2:41 PM UTC")
}

func TestPostCompletion_NewComment_WhenInterveningHumanActivity(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Triaging issue")
	require.NoError(t, err)

	// Simulate a human comment (different author than the bot).
	fc.IssueComments["org/repo/7"] = append(fc.IssueComments["org/repo/7"], forge.IssueComment{
		ID:     9999,
		Body:   "A human comment",
		Author: "some-human",
	})

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Triaging issue", "success")
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should post new comment when non-bot activity intervenes")

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 3)
	assert.Contains(t, comments[2].Body, "Finished Triaging issue")
}

func TestPostCompletion_EditStart_WhenAgentPostedOutput(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Triaging issue")
	require.NoError(t, err)

	// Agent posts its own output (same bot author, no status marker).
	fc.CreateIssueComment(context.Background(), "org", "repo", 7, "<!-- fullsend:triage-agent -->\nTriage result here")

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Triaging issue", "success")
	require.NoError(t, err)

	// Start comment should be updated in place — agent output is the visible signal.
	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 1, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Triaging issue")

	// No new completion comment should be created (only start + agent output = 2).
	comments := fc.IssueComments["org/repo/7"]
	assert.Len(t, comments, 2)
}

func TestPostCompletion_EditStart_WhenAgentAndHumanPosted(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Reviewing this PR")
	require.NoError(t, err)

	// Human comments, then agent posts output.
	fc.IssueComments["org/repo/7"] = append(fc.IssueComments["org/repo/7"], forge.IssueComment{
		ID:     9999,
		Body:   "Human question here",
		Author: "some-human",
	})
	fc.CreateIssueComment(context.Background(), "org", "repo", 7, "<!-- fullsend:review-agent -->\nReview findings")

	n.now = func() time.Time { return fixedTime().Add(7 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Reviewing this PR", "success")
	require.NoError(t, err)

	// Agent posted output → edit start in place, even though human also commented.
	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 1, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Reviewing this PR")
}

func TestPostCompletion_Cancelled(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	require.Len(t, fc.IssueComments["org/repo/7"], 1)

	completionTime := fixedTime().Add(2 * time.Minute)
	n.now = func() time.Time { return completionTime }

	err = n.PostCompletion(context.Background(), "Working", "cancelled")
	require.NoError(t, err)

	assert.Empty(t, fc.DeletedComments, "should update, not delete")
	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 1, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Working")
	assert.Contains(t, fc.UpdatedComments[0].Body, "⚠️ Cancelled")
}

func TestAllDisabled_NoAPICalls(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)

	assert.Empty(t, fc.IssueComments)
	assert.Empty(t, fc.UpdatedComments)
}

func TestRunURL_Omitted(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n := New(fc, cfg, "org", "repo", 7, "", "abc123", "run-1")
	n.now = fixedTime

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	body := fc.IssueComments["org/repo/7"][0].Body
	assert.NotContains(t, body, "View workflow run")
	assert.Contains(t, body, "Commit: `abc123`")
}

func TestSHA_Omitted(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n := New(fc, cfg, "org", "repo", 7, "https://ci/run/1", "", "run-1")
	n.now = fixedTime

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	body := fc.IssueComments["org/repo/7"][0].Body
	assert.NotContains(t, body, "Commit:")
	assert.Contains(t, body, "[View workflow run →](https://ci/run/1)")
}

func TestPostCompletion_Failure(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Coding issue #42")
	require.NoError(t, err)

	n.now = func() time.Time { return fixedTime().Add(10 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Coding issue #42", "failure")
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	assert.Contains(t, fc.UpdatedComments[0].Body, "❌ Failure")
}

func TestPostCompletion_UnknownStatus(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	n.now = func() time.Time { return fixedTime().Add(3 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "timeout")
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	assert.Contains(t, fc.UpdatedComments[0].Body, "⚠️ Timeout")
}

func TestPostCompletion_NoStartComment(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "disabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "Finished Working")
}

func TestCapitalize(t *testing.T) {
	assert.Equal(t, "Success", capitalize("success"))
	assert.Equal(t, "Failure", capitalize("failure"))
	assert.Equal(t, "", capitalize(""))
}

func TestFormatTime(t *testing.T) {
	ts := time.Date(2026, 1, 15, 9, 5, 0, 0, time.UTC)
	assert.Equal(t, "9:05 AM UTC", formatTime(ts))
}

func TestShortSHA(t *testing.T) {
	assert.Equal(t, "a1b2c3d", shortSHA("a1b2c3d4e5f6789"))
	assert.Equal(t, "abc", shortSHA("abc"))
}

func TestMarkerUniqueness(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n1 := New(fc, cfg, "org", "repo", 7, "", "", "run-1")
	n2 := New(fc, cfg, "org", "repo", 7, "", "", "run-2")
	assert.NotEqual(t, n1.marker, n2.marker)
	assert.Contains(t, n1.marker, "run-1")
	assert.Contains(t, n2.marker, "run-2")
}

func TestBuildMarker_SanitizesRunID(t *testing.T) {
	m, err := buildMarker("run-42")
	require.NoError(t, err)
	assert.Contains(t, m, "run-42")

	m, err = buildMarker("123_abc")
	require.NoError(t, err)
	assert.Contains(t, m, "123_abc")

	_, err = buildMarker("-->injected")
	assert.Error(t, err)

	_, err = buildMarker("run id with spaces")
	assert.Error(t, err)

	_, err = buildMarker("")
	assert.Error(t, err)
}

func TestMustBuildMarker_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() { mustBuildMarker("-->bad") })
}

func TestMustBuildMarker_ValidInput(t *testing.T) {
	assert.NotPanics(t, func() {
		m := mustBuildMarker("run-42")
		assert.Contains(t, m, "run-42")
	})
}

func setNow(t *testing.T, fixed time.Time) {
	t.Helper()
	orig := now
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = orig })
}

func TestReconcileOrphaned_InvalidRunID(t *testing.T) {
	fc := forge.NewFakeClient()
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "-->bad", "", "", ReasonTerminated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid run ID")
}

func TestPostCompletion_CompletionDisabled_CleansUpStartComment(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	require.Equal(t, 1, n.startCommentID)

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should not update start comment")
	require.Len(t, fc.DeletedComments, 1, "should delete orphaned start comment")
	assert.Equal(t, 1, fc.DeletedComments[0])
	assert.Empty(t, fc.IssueComments["org/repo/7"], "start comment should be removed")
}

func TestPostCompletion_CancelledWithCompletionDisabled(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	require.Equal(t, 1, n.startCommentID)

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "cancelled")
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should not update start comment")
	require.Len(t, fc.DeletedComments, 1, "should delete orphaned start comment")
	assert.Equal(t, 1, fc.DeletedComments[0])
	assert.Empty(t, fc.IssueComments["org/repo/7"], "start comment should be removed")
}

func TestRunURL_UnsafeDropped(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		inBody bool
	}{
		{"https valid", "https://github.com/org/repo/actions/runs/123", true},
		{"http rejected", "http://example.com/run", false},
		{"javascript rejected", "javascript:alert(1)", false},
		{"paren in url", "https://example.com/run)", false},
		{"bracket in url", "https://evil.com/x](evil)[click", false},
		{"newline in url", "https://example.com/run\ninjected", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			cfg := config.StatusNotificationConfig{}
			n := New(fc, cfg, "org", "repo", 7, tt.url, "abc123", "run-1")
			n.now = fixedTime

			err := n.PostStart(context.Background(), "Working")
			require.NoError(t, err)

			body := fc.IssueComments["org/repo/7"][0].Body
			if tt.inBody {
				assert.Contains(t, body, "View workflow run")
			} else {
				assert.NotContains(t, body, "View workflow run")
			}
		})
	}
}

func TestAnalyzeTimeline_EmptyBotUser_FallsBackToPositionOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	fc.AuthenticatedUser = ""
	n := New(fc, cfg, "org", "repo", 7, "https://ci/run/42", "a1b2c3d4e5f6789", "run-42")
	n.now = fixedTime

	err := n.PostStart(context.Background(), "Reviewing this PR")
	require.NoError(t, err)
	require.Equal(t, 1, n.startCommentID)

	// Start is the last comment → should edit in place even without bot user identity.
	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Reviewing this PR", "success")
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1, "should edit start in place via startIsLast fallback")
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Reviewing this PR")
}

func TestAnalyzeTimeline_EmptyBotUser_NewCommentWhenNotLast(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	fc.AuthenticatedUser = ""
	n := New(fc, cfg, "org", "repo", 7, "https://ci/run/42", "a1b2c3d4e5f6789", "run-42")
	n.now = fixedTime

	err := n.PostStart(context.Background(), "Reviewing this PR")
	require.NoError(t, err)

	// Agent posts output, but since botUser is empty it can't be identified as agent output.
	fc.IssueComments["org/repo/7"] = append(fc.IssueComments["org/repo/7"], forge.IssueComment{
		ID:     9999,
		Body:   "Some output",
		Author: "fullsend-bot[bot]",
	})

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Reviewing this PR", "success")
	require.NoError(t, err)

	// Without bot identity, agentPosted is false and startIsLast is false → new comment.
	assert.Empty(t, fc.UpdatedComments, "should not edit start when bot identity unknown and not last")
	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 3)
	assert.Contains(t, comments[2].Body, "Finished Reviewing this PR")
}

func TestAnalyzeTimeline_UsesStartCommentAuthor(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	// Set AuthenticatedUser so FakeClient stamps the start comment Author.
	fc.AuthenticatedUser = "fullsend-bot[bot]"
	// Inject GetAuthenticatedUser error to prove it is NOT called.
	fc.Errors["GetAuthenticatedUser"] = fmt.Errorf("should not be called")
	n := New(fc, cfg, "org", "repo", 7, "https://ci/run/42", "a1b2c3d4e5f6789", "run-42")
	n.now = fixedTime

	err := n.PostStart(context.Background(), "Reviewing this PR")
	require.NoError(t, err)

	// Agent posts output (same bot author, no status marker).
	fc.CreateIssueComment(context.Background(), "org", "repo", 7, "Review findings here")

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Reviewing this PR", "success")
	require.NoError(t, err)

	// Bot user derived from start comment Author → agentPosted = true → edit in place.
	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 1, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Reviewing this PR")
}

func TestShortSHA_NonHexRejected(t *testing.T) {
	assert.Equal(t, "", shortSHA("not-a-sha"))
	assert.Equal(t, "", shortSHA("abc`injected"))
	assert.Equal(t, "", shortSHA(""))
	assert.Equal(t, "abc123", shortSHA("abc123"))
	assert.Equal(t, "a1b2c3d", shortSHA("a1b2c3d4e5f6789"))
	assert.Equal(t, "ABCDEF0", shortSHA("ABCDEF0123456789"))
}

func TestReconcileOrphaned_UpdatesStartedComment(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}
	setNow(t, time.Date(2026, 6, 3, 7, 12, 0, 0, time.UTC))

	// Simulate a "Started" comment left by a killed process.
	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n🤖 Code · Started 6:43 AM UTC\nCommit: `abc1234` · [View workflow run →](https://ci/run/99)",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated)
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	body := fc.UpdatedComments[0].Body
	assert.Equal(t, 42, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, body, "Code")
	assert.Contains(t, body, "❌ Terminated")
	assert.Contains(t, body, "Started 6:43 AM UTC")
	assert.Contains(t, body, "Ended 7:12 AM UTC")
	assert.Contains(t, body, "<!-- fullsend:agent-status:run-99 -->")
	assert.Contains(t, body, "<!-- fullsend:status:terminal -->")
	assert.Contains(t, body, "Commit: `abc1234`")
	assert.Contains(t, body, "[View workflow run →](https://ci/run/99)")
}

func TestReconcileOrphaned_SkipsAlreadyFinished(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}

	// Comment already reached terminal state (via PostCompletion).
	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n<!-- fullsend:status:terminal -->\n🤖 Finished Code · ✅ Success · Started 6:43 AM UTC · Completed 6:50 AM UTC",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated)
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should not update already-finished comment")
}

func TestReconcileOrphaned_NoMatchingComment(t *testing.T) {
	fc := forge.NewFakeClient()

	// No comments at all.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated)
	require.NoError(t, err)
	assert.Empty(t, fc.UpdatedComments)
}

func TestReconcileOrphaned_DifferentRunID(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}

	// Comment from a different run.
	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-50 -->\n🤖 Code · Started 6:43 AM UTC",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated)
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should not touch comment from different run")
}

func TestReconcileOrphaned_ListError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListIssueComments"] = fmt.Errorf("api error")

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "", "", ReasonTerminated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing comments")
}

func TestReconcileOrphaned_NoURLOrSHA(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}
	setNow(t, time.Date(2026, 6, 3, 7, 12, 0, 0, time.UTC))

	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n🤖 Code · Started 6:43 AM UTC",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "", "", ReasonTerminated)
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	body := fc.UpdatedComments[0].Body
	assert.Contains(t, body, "Code")
	assert.Contains(t, body, "❌ Terminated")
	assert.Contains(t, body, "Started 6:43 AM UTC")
	assert.Contains(t, body, "Ended 7:12 AM UTC")
	assert.NotContains(t, body, "Commit:")
	assert.NotContains(t, body, "View workflow run")
}

func TestReconcileOrphaned_SkipsAlreadyInterrupted(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}

	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n<!-- fullsend:status:terminal -->\n🤖 Agent run interrupted (process terminated)",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated)
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should not re-update already-interrupted comment")
}

func TestReconcileOrphaned_UpdateError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}

	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n🤖 Working · Started 1:00 PM UTC",
			Author: "fullsend-bot[bot]",
		},
	}

	fc.Errors["UpdateIssueComment"] = fmt.Errorf("api rate limited")

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating orphaned comment")
}

func TestPostStart_ErrorPropagated(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateIssueComment"] = fmt.Errorf("api down")
	cfg := config.StatusNotificationConfig{}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "posting start comment")
}

func TestPostCompletion_CancelledWithNoStartComment(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	assert.Equal(t, 0, n.startCommentID)

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "cancelled")
	require.NoError(t, err)

	assert.Empty(t, fc.DeletedComments, "should not attempt deletion when no start comment exists")
	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1, "should post a completion comment")
	assert.Contains(t, comments[0].Body, "⚠️ Cancelled")
}

func TestPostCompletion_AnalyzeTimelineError_UpdatesStartInPlace(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	require.Equal(t, 1, n.startCommentID)

	// Inject error into ListIssueComments so analyzeTimeline fails.
	fc.Errors["ListIssueComments"] = fmt.Errorf("api timeout")

	var warnings []string
	n.SetWarnFunc(func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)

	// Should have warned about timeline analysis failure.
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "failed to analyze timeline")

	// Should fall back to updating the start comment in place to avoid orphaning it.
	require.Len(t, fc.UpdatedComments, 1, "should update start comment on timeline error")
	assert.Equal(t, 1, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Working")
	comments := fc.IssueComments["org/repo/7"]
	assert.Len(t, comments, 1, "should not create a new comment")
}

func TestReconcileOrphaned_CancelledReason(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}
	setNow(t, time.Date(2026, 6, 3, 14, 47, 0, 0, time.UTC))

	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n🤖 Reviewing this PR · Started 2:34 PM UTC\nCommit: `abc1234` · [View workflow run →](https://ci/run/99)",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonCancelled)
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	body := fc.UpdatedComments[0].Body
	assert.Contains(t, body, "Reviewing this PR")
	assert.Contains(t, body, "⚠️ Cancelled")
	assert.Contains(t, body, "Started 2:34 PM UTC")
	assert.Contains(t, body, "Ended 2:47 PM UTC")
	assert.Contains(t, body, terminalTag)
}

func TestReconcileOrphaned_StartTimeNotParseable(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}
	setNow(t, time.Date(2026, 6, 3, 14, 47, 0, 0, time.UTC))

	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\nSome manually edited comment",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated)
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	body := fc.UpdatedComments[0].Body
	assert.Contains(t, body, "Agent run interrupted")
	assert.Contains(t, body, "❌ Terminated")
	assert.NotContains(t, body, "Started")
	assert.Contains(t, body, "Ended 2:47 PM UTC")
	assert.Contains(t, body, terminalTag)
}

func TestParseStartBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantDesc  string
		wantStart string
	}{
		{
			name:      "standard",
			body:      "🤖 Code · Started 2:34 PM UTC",
			wantDesc:  "Code",
			wantStart: "2:34 PM UTC",
		},
		{
			name:      "multi-word description",
			body:      "🤖 Reviewing this PR · Started 6:43 AM UTC\nCommit: `abc`",
			wantDesc:  "Reviewing this PR",
			wantStart: "6:43 AM UTC",
		},
		{
			name:      "midnight",
			body:      "🤖 Working · Started 12:00 AM UTC",
			wantDesc:  "Working",
			wantStart: "12:00 AM UTC",
		},
		{
			name:      "no match",
			body:      "no time here",
			wantDesc:  "",
			wantStart: "",
		},
		{
			name:      "empty body",
			body:      "",
			wantDesc:  "",
			wantStart: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, start := parseStartBody(tt.body)
			assert.Equal(t, tt.wantDesc, desc)
			assert.Equal(t, tt.wantStart, start)
		})
	}
}

func TestReconcileOrphaned_UnknownReasonDefaultsToTerminated(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}
	setNow(t, time.Date(2026, 6, 3, 14, 47, 0, 0, time.UTC))

	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n🤖 Code · Started 6:43 AM UTC",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", TerminationReason("unknown-value"))
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	body := fc.UpdatedComments[0].Body
	assert.Contains(t, body, "Code")
	assert.Contains(t, body, "❌ Terminated")
	assert.Contains(t, body, "Started 6:43 AM UTC")
	assert.Contains(t, body, "Ended 2:47 PM UTC")
}
