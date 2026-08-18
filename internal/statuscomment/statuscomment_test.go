package statuscomment

import (
	"context"
	"fmt"
	"strings"
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

func TestPostCompletion_Skipped(t *testing.T) {
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

	err = n.PostCompletion(context.Background(), "Working", "skipped")
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	assert.Contains(t, fc.UpdatedComments[0].Body, "Finished Working")
	assert.Contains(t, fc.UpdatedComments[0].Body, "⏭️ Skipped")
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
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "-->bad", "", "", ReasonTerminated, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should not update already-finished comment")
}

func TestReconcileOrphaned_NoMatchingComment(t *testing.T) {
	fc := forge.NewFakeClient()

	// No comments at all.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
	require.NoError(t, err)
	assert.Empty(t, fc.UpdatedComments)
}

func TestReconcileOrphaned_OnFailure_SynthesizesWhenNoMarker(t *testing.T) {
	fc := forge.NewFakeClient()
	setNow(t, time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC))

	// No comments at all — simulates on_failure mode where PostStart was suppressed
	// and the process was hard-killed before PostCompletion ran.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "on_failure", "failure", false, "")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1, "should synthesize an interrupted comment")
	body := comments[0].Body
	assert.Contains(t, body, "<!-- fullsend:agent-status:run-99 -->")
	assert.Contains(t, body, "<!-- fullsend:status:terminal -->")
	assert.Contains(t, body, "❌ Terminated")
	assert.Contains(t, body, "Ended 2:00 PM UTC")
	assert.Contains(t, body, "Commit: `abc1234`")
	assert.Contains(t, body, "[View workflow run →](https://ci/run/99)")
}

func TestReconcileOrphaned_OnFailure_NoSynthesisWhenMarkerExists(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{}
	setNow(t, time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC))

	// A start comment exists (shouldn't happen under on_failure, but if it does,
	// reconcile should finalize it normally, not double-post).
	fc.IssueComments["org/repo/7"] = []forge.IssueComment{
		{
			ID:     42,
			Body:   "<!-- fullsend:agent-status:run-99 -->\n🤖 Code · Started 6:43 AM UTC",
			Author: "fullsend-bot[bot]",
		},
	}

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "on_failure", "failure", false, "")
	require.NoError(t, err)

	// Should update the existing comment, not create a new one.
	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 42, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.UpdatedComments[0].Body, "❌ Terminated")
}

func TestReconcileOrphaned_EnabledMode_NoSynthesisWhenNoMarker(t *testing.T) {
	fc := forge.NewFakeClient()

	// No comments, default completion mode, unknown job status — should
	// NOT synthesize.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
	require.NoError(t, err)
	assert.Empty(t, fc.IssueComments, "should not synthesize for enabled mode")
	assert.Empty(t, fc.UpdatedComments)
}

func TestReconcileOrphaned_EnabledMode_NoSynthesisWhenJobSucceeded(t *testing.T) {
	fc := forge.NewFakeClient()

	// No comments, default completion mode, job succeeded — the run
	// completed and posted its own completion comment as expected; nothing
	// to synthesize.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "enabled", "success", false, "")
	require.NoError(t, err)
	assert.Empty(t, fc.IssueComments)
	assert.Empty(t, fc.UpdatedComments)
}

func TestReconcileOrphaned_EnabledMode_SynthesizesOnFailureWithNoMarker(t *testing.T) {
	fc := forge.NewFakeClient()
	setNow(t, time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC))

	// No comments, default ("") completion mode, job failed. A status
	// comment should always exist for a run that reached the harness under
	// the default mode — its absence alongside a failed job means the
	// process crashed before it could post anything at all (e.g. during
	// environment validation), leaving maintainers unable to tell "no
	// review was triggered" from "review was attempted and failed
	// silently." See #3635.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "failure", false, "Review")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1, "should synthesize an interrupted comment for a crash before any comment posted")
	assert.Contains(t, comments[0].Body, "❌ Terminated")
	assert.Contains(t, comments[0].Body, "Review")
}

func TestReconcileOrphaned_EnabledMode_SynthesizesOnCancelledWithNoMarker(t *testing.T) {
	fc := forge.NewFakeClient()
	setNow(t, time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC))

	// Same as above, but the job was cancelled rather than failed, and
	// completion is explicitly "enabled" rather than the implicit default.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonCancelled, "enabled", "cancelled", false, "")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "⚠️ Cancelled")
}

func TestReconcileOrphaned_DisabledMode_NoSynthesisEvenOnFailure(t *testing.T) {
	fc := forge.NewFakeClient()

	// completion: disabled is an explicit opt-out of all status comments.
	// Even though no marker exists and the job failed, we must not
	// synthesize one — that would override the user's choice.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "disabled", "failure", false, "")
	require.NoError(t, err)
	assert.Empty(t, fc.IssueComments, "disabled completion mode must never synthesize a comment")
	assert.Empty(t, fc.UpdatedComments)
}

func TestReconcileOrphaned_OnFailure_NoSynthesisWhenJobSucceeded(t *testing.T) {
	fc := forge.NewFakeClient()

	// No comments — on_failure mode but the job succeeded. The agent completed
	// normally and PostCompletion suppressed the comment. ReconcileOrphaned
	// must NOT synthesize a false "Interrupted" comment.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "on_failure", "success", false, "")
	require.NoError(t, err)
	assert.Empty(t, fc.IssueComments, "should not synthesize when job succeeded")
	assert.Empty(t, fc.UpdatedComments)
}

func TestReconcileOrphaned_OnFailure_NoSynthesisWhenJobStatusEmpty(t *testing.T) {
	fc := forge.NewFakeClient()

	// No comments — on_failure mode but jobStatus is empty (--job-status flag
	// was omitted). Should NOT synthesize since the job outcome is unknown.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "on_failure", "", false, "")
	require.NoError(t, err)
	assert.Empty(t, fc.IssueComments, "should not synthesize when job status is unknown")
	assert.Empty(t, fc.UpdatedComments)
}

func TestReconcileOrphaned_OnFailure_SynthesizesWhenSkippedEvenIfJobSucceeded(t *testing.T) {
	fc := forge.NewFakeClient()
	setNow(t, time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC))

	// No comments — the run was skipped and the skip-reason comment itself
	// failed to post (its error is only logged, not propagated to the job's
	// exit code, so jobStatus is still "success"). wasSkipped must force
	// synthesis so the failure isn't silently lost. See PR #5736.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "on_failure", "success", true, "")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1, "should synthesize an interrupted comment despite jobStatus==success")
	// The label is deliberately outcome-neutral: "no completion comment" is
	// true whether the notifier failed to set up (never attempted a post)
	// or its own skip-reason comment post failed — ReconcileOrphaned can't
	// tell those apart, so it shouldn't assert a specific cause. See the
	// review discussion on PR #5736.
	assert.Contains(t, comments[0].Body, "⏭️ Skipped (no completion comment)")
	assert.NotContains(t, comments[0].Body, "comment failed to post")
	assert.NotContains(t, comments[0].Body, "❌ Terminated")
}

func TestReconcileOrphaned_OnFailure_SkippedWithRealCancellationKeepsReason(t *testing.T) {
	fc := forge.NewFakeClient()
	setNow(t, time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC))

	// wasSkipped is true, but jobStatus is "cancelled" — the job was
	// actually cancelled (unrelated to the skip-reason comment), so the
	// passed-in reason should be preserved rather than relabeled as a
	// comment-post failure.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonCancelled, "on_failure", "cancelled", true, "")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "⚠️ Cancelled")
	assert.NotContains(t, comments[0].Body, "Skipped (no completion comment)")
}

func TestReconcileOrphaned_EnabledMode_NoSynthesisWhenSkippedButNotOnFailure(t *testing.T) {
	fc := forge.NewFakeClient()

	// wasSkipped alone shouldn't trigger synthesis outside on_failure mode —
	// completionMode must also be "on_failure".
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "success", true, "")
	require.NoError(t, err)
	assert.Empty(t, fc.IssueComments, "should not synthesize when completionMode isn't on_failure")
}

func TestReconcileOrphaned_SynthesizedComment_UsesAgentDescription(t *testing.T) {
	fc := forge.NewFakeClient()
	setNow(t, time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC))

	// No comments — synthesized comment heading should reflect the agent
	// description so operators can tell which agent failed when multiple
	// agents run against the same issue/PR.
	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "on_failure", "failure", false, "Triage")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "🤖 Triage · ❌ Terminated")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
	require.NoError(t, err)

	assert.Empty(t, fc.UpdatedComments, "should not touch comment from different run")
}

func TestReconcileOrphaned_ListError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListIssueComments"] = fmt.Errorf("api error")

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "", "", ReasonTerminated, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "", "", ReasonTerminated, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonCancelled, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
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

	err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", TerminationReason("unknown-value"), "", "", false, "")
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	body := fc.UpdatedComments[0].Body
	assert.Contains(t, body, "Code")
	assert.Contains(t, body, "❌ Terminated")
	assert.Contains(t, body, "Started 6:43 AM UTC")
	assert.Contains(t, body, "Ended 2:47 PM UTC")
}

func TestClientFactory_CalledBeforePostStart(t *testing.T) {
	fc1 := forge.NewFakeClient()
	fc2 := forge.NewFakeClient()
	fc2.AuthenticatedUser = "mint-bot[bot]"
	cfg := config.StatusNotificationConfig{}

	n := New(fc1, cfg, "org", "repo", 7, "https://ci/run/42", "a1b2c3d", "run-42")
	n.now = fixedTime

	factoryCalled := false
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		factoryCalled = true
		return fc2, nil
	})

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	assert.True(t, factoryCalled, "factory should be called before PostStart API calls")
	assert.Len(t, fc2.IssueComments["org/repo/7"], 1, "comment should be on factory-returned client")
	assert.Empty(t, fc1.IssueComments, "original client should not be used")
}

func TestClientFactory_CalledBeforePostCompletion(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot[bot]"
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}

	n := newTestNotifier(fc, cfg)
	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	fc2 := forge.NewFakeClient()
	fc2.AuthenticatedUser = "bot[bot]"
	// Pre-populate fc2 with the same comments so analyzeTimeline works.
	fc2.IssueComments = map[string][]forge.IssueComment{
		"org/repo/7": {fc.IssueComments["org/repo/7"][0]},
	}

	completionFactoryCalled := false
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		completionFactoryCalled = true
		return fc2, nil
	})

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)
	assert.True(t, completionFactoryCalled, "factory should be called before PostCompletion API calls")
}

func TestClientFactory_ErrorPropagated(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n := New(fc, cfg, "org", "repo", 7, "", "", "run-42")
	n.now = fixedTime

	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		return nil, fmt.Errorf("mint service unavailable")
	})

	err := n.PostStart(context.Background(), "Working")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint service unavailable")
}

func TestClientFactory_NilUsesStaticClient(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	assert.Len(t, fc.IssueComments["org/repo/7"], 1, "static client should be used when no factory set")
}

func TestClientFactory_ErrorOnPostCompletion(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		return nil, fmt.Errorf("token expired")
	})

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token expired")
}

func TestClientFactory_CompletionDisabled_DeletePath(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	require.Equal(t, 1, n.startCommentID)

	fc2 := forge.NewFakeClient()
	fc2.AuthenticatedUser = "fullsend-bot[bot]"
	fc2.IssueComments = map[string][]forge.IssueComment{
		"org/repo/7": {fc.IssueComments["org/repo/7"][0]},
	}

	factoryCalled := false
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		factoryCalled = true
		return fc2, nil
	})

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)
	assert.True(t, factoryCalled, "factory should be called even when completion disabled (for delete)")
	require.Len(t, fc2.DeletedComments, 1)
	assert.Equal(t, 1, fc2.DeletedComments[0])
}

func TestClientFactory_BothDisabled_NoMint(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	factoryCalled := false
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		factoryCalled = true
		return nil, fmt.Errorf("should not be called")
	})

	err := n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err, "should not error when no API call is needed")
	assert.False(t, factoryCalled, "factory should not be called when both disabled and no start comment")
}

func TestHasClientFactory(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n := newTestNotifier(fc, cfg)

	assert.False(t, n.HasClientFactory(), "should be false when no factory set")

	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		return fc, nil
	})
	assert.True(t, n.HasClientFactory(), "should be true after SetClientFactory")
}

func TestClientFactory_CompletionDisabled_MintError(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	require.NotZero(t, n.startCommentID)

	var warnings []string
	n.SetWarnFunc(func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		return nil, fmt.Errorf("mint service down")
	})

	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err, "should not return error — fail-open on cleanup")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "mint service down")
}

// --- on_failure completion tests ---

func TestPostCompletion_OnFailure_SuppressedOnSuccess(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	assert.Equal(t, 0, n.startCommentID, "start comment should be auto-suppressed when completion is on_failure")

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)

	// No start comment was posted, so nothing to delete or update.
	assert.Empty(t, fc.IssueComments, "no comments should exist on success")
	assert.Empty(t, fc.UpdatedComments)
	assert.Empty(t, fc.DeletedComments)
}

func TestPostCompletion_OnFailure_PostsOnFailure(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Coding issue #42")
	require.NoError(t, err)
	assert.Equal(t, 0, n.startCommentID, "start comment auto-suppressed")

	n.now = func() time.Time { return fixedTime().Add(10 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Coding issue #42", "failure")
	require.NoError(t, err)

	// No start comment to update — failure should create a new completion comment.
	assert.Empty(t, fc.UpdatedComments)
	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1, "should post completion on failure")
	assert.Contains(t, comments[0].Body, "Finished Coding issue #42")
	assert.Contains(t, comments[0].Body, "❌ Failure")
}

func TestPostCompletion_OnFailure_PostsOnCancelled(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	assert.Equal(t, 0, n.startCommentID, "start comment auto-suppressed")

	n.now = func() time.Time { return fixedTime().Add(2 * time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "cancelled")
	require.NoError(t, err)

	// No start comment to update — cancelled should create a new completion comment.
	assert.Empty(t, fc.UpdatedComments)
	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1, "should post completion on cancellation")
	assert.Contains(t, comments[0].Body, "⚠️ Cancelled")
}

func TestPostCompletion_OnFailure_NoStartComment_PostsOnFailure(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "disabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	assert.Equal(t, 0, n.startCommentID)

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "failure")
	require.NoError(t, err)

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1, "should post completion on failure")
	assert.Contains(t, comments[0].Body, "❌ Failure")
}

func TestPostCompletion_OnFailure_NoStartComment_SuppressedOnSuccess(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "disabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err)

	assert.Empty(t, fc.IssueComments, "should not post anything on success with on_failure")
	assert.Empty(t, fc.UpdatedComments)
	assert.Empty(t, fc.DeletedComments)
}

func TestPostCompletion_OnFailure_PostsOnSkipped(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	assert.Equal(t, 0, n.startCommentID, "start comment auto-suppressed")

	n.now = func() time.Time { return fixedTime().Add(time.Minute) }
	err = n.PostCompletion(context.Background(), "Working", "skipped")
	require.NoError(t, err)

	assert.Len(t, fc.IssueComments, 1, "skip reason is informative signal — should post even with on_failure")
}

func TestClientFactory_CompletionDisabled_DeleteError(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)
	require.NotZero(t, n.startCommentID)

	fc2 := forge.NewFakeClient()
	fc2.Errors["DeleteIssueComment"] = fmt.Errorf("forbidden")

	var warnings []string
	n.SetWarnFunc(func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		return fc2, nil
	})

	err = n.PostCompletion(context.Background(), "Working", "success")
	require.NoError(t, err, "should not return error — fail-open on cleanup")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "forbidden")
}

func TestPostCompletionWithDetail_SkippedShowsReason(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))

	completionTime := fixedTime().Add(2 * time.Minute)
	n.now = func() time.Time { return completionTime }

	err := n.PostCompletionWithDetail(context.Background(), "Working", "skipped",
		"PR #123 already addresses this issue")
	require.NoError(t, err)

	require.Len(t, fc.UpdatedComments, 1)
	// A skip with no visible reason is the one thing a reader needs and
	// the one thing the status line used to omit.
	assert.Contains(t, fc.UpdatedComments[0].Body, "⏭️ Skipped (PR #123 already addresses this issue)")
}

func TestSanitizeDetail(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   string
		want string
	}{
		"empty":                    {"", ""},
		"plain":                    {"open PR exists", "open PR exists"},
		"newline collapses":        {"line one\nline two", "line one line two"},
		"CR collapses":             {"a\rb", "a b"},
		"tabs and NUL collapse":    {"a\t\x00b", "a b"},
		"runs of space collapse":   {"a   b", "a b"},
		"trims":                    {"  padded  ", "padded"},
		"HTML comment neutralized": {"<!-- fullsend:status:terminal -->", "&lt;!-- fullsend:status:terminal -->"},
		"markdown link neutralized": {"see [details](https://evil.example)",
			"see &#91;details](https://evil.example)"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, sanitizeDetail(tc.in))
		})
	}
}

func TestSanitizeDetail_Truncates(t *testing.T) {
	t.Parallel()

	got := sanitizeDetail(strings.Repeat("x", maxDetailLen+50))
	assert.Equal(t, strings.Repeat("x", maxDetailLen)+"…", got)
}

// A reason is script-controlled text, so it must not be able to forge the
// marker comments ReconcileOrphaned depends on, nor escape the status line.
func TestPostCompletionWithDetail_DetailCannotForgeMarkers(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled"},
	}
	n := newTestNotifier(fc, cfg)
	require.NoError(t, n.PostStart(context.Background(), "Working"))

	err := n.PostCompletionWithDetail(context.Background(), "Working", "skipped",
		"evil\n<!-- fullsend:agent-status:999 -->")
	require.NoError(t, err)

	body := fc.UpdatedComments[0].Body
	// The escaped text may still read as "fullsend:agent-status", but it
	// can no longer open an HTML comment, so only the real marker parses.
	assert.NotContains(t, body, "<!-- fullsend:agent-status:999")
	assert.Equal(t, 1, strings.Count(body, "<!-- fullsend:agent-status"))
	assert.Equal(t, 1, strings.Count(body, terminalTag))
	// And the detail stays on the status line.
	assert.Contains(t, body, "⏭️ Skipped (evil &lt;!-- fullsend:agent-status:999 -->)")
}

func TestParagraphBreak_BetweenStatusAndMetadata(t *testing.T) {
	// Verify that buildStartBody, buildCompletionBody, and
	// buildInterruptedBody use \n\n (paragraph break) between the status
	// line and the metadata line. A bare \n renders as inline whitespace
	// on GitLab (strict CommonMark), collapsing the two lines into one.
	t.Run("start body", func(t *testing.T) {
		fc := forge.NewFakeClient()
		cfg := config.StatusNotificationConfig{}
		n := newTestNotifier(fc, cfg)

		err := n.PostStart(context.Background(), "Triaging issue")
		require.NoError(t, err)

		body := fc.IssueComments["org/repo/7"][0].Body
		assert.Contains(t, body, "Started 2:34 PM UTC\n\nCommit:",
			"start body should use paragraph break before metadata line")
	})

	t.Run("completion body", func(t *testing.T) {
		fc := forge.NewFakeClient()
		cfg := config.StatusNotificationConfig{
			Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
		}
		n := newTestNotifier(fc, cfg)

		require.NoError(t, n.PostStart(context.Background(), "Triaging issue"))
		n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
		require.NoError(t, n.PostCompletion(context.Background(), "Triaging issue", "success"))

		require.Len(t, fc.UpdatedComments, 1)
		body := fc.UpdatedComments[0].Body
		assert.Contains(t, body, "Completed 2:39 PM UTC\n\nCommit:",
			"completion body should use paragraph break before metadata line")
	})

	t.Run("interrupted body", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{}
		setNow(t, time.Date(2026, 6, 3, 7, 12, 0, 0, time.UTC))

		fc.IssueComments["org/repo/7"] = []forge.IssueComment{
			{
				ID:     42,
				Body:   "<!-- fullsend:agent-status:run-99 -->\n🤖 Code · Started 6:43 AM UTC\nCommit: `abc1234` · [View workflow run →](https://ci/run/99)",
				Author: "fullsend-bot[bot]",
			},
		}

		err := ReconcileOrphaned(context.Background(), fc, "org", "repo", 7, "run-99", "https://ci/run/99", "abc1234def", ReasonTerminated, "", "", false, "")
		require.NoError(t, err)

		require.Len(t, fc.UpdatedComments, 1)
		body := fc.UpdatedComments[0].Body
		assert.Contains(t, body, "Ended 7:12 AM UTC\n\nCommit:",
			"interrupted body should use paragraph break before metadata line")
	})
}

// --- Reaction tests ---

func TestPostStart_ReactionEnabled(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Reaction: config.ReactionNotificationConfig{Start: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	require.Len(t, fc.AddedReactions, 1)
	assert.Equal(t, forge.ReactionRecord{ID: 1, Owner: "org", Repo: "repo", Number: 7, Content: "eyes"}, fc.AddedReactions[0])
}

func TestPostStart_ReactionDisabledByDefault(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err)

	assert.Empty(t, fc.AddedReactions, "reactions are opt-in, unlike comments")
}

func TestPostCompletion_ReactionSuccess(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.Len(t, fc.AddedReactions, 1)
	startReactionID := int64(1)

	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	assert.Equal(t, []int64{startReactionID}, fc.DeletedReactions, "start reaction is replaced, not stacked")
	require.Len(t, fc.AddedReactions, 2)
	assert.Equal(t, "+1", fc.AddedReactions[1].Content)
}

func TestPostCompletion_ReactionFailure(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NoError(t, n.PostCompletion(context.Background(), "Working", "failure"))

	require.Len(t, fc.AddedReactions, 2)
	assert.Equal(t, "confused", fc.AddedReactions[1].Content)
}

func TestPostCompletion_ReactionOnFailure_SuppressedOnSuccess(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	assert.Len(t, fc.AddedReactions, 1, "no completion reaction added on success")
	assert.Equal(t, []int64{1}, fc.DeletedReactions, "start reaction is cleaned up, leaving no trace")
}

func TestPostCompletion_ReactionOnFailure_FiresOnFailure(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "on_failure"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NoError(t, n.PostCompletion(context.Background(), "Working", "failure"))

	require.Len(t, fc.AddedReactions, 2)
	assert.Equal(t, "confused", fc.AddedReactions[1].Content)
}

func TestPostCompletion_ReactionDisabled_CleansUpStartReaction(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "disabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	assert.Empty(t, fc.AddedReactions[1:], "no completion reaction posted")
	assert.Equal(t, []int64{1}, fc.DeletedReactions)
}

func TestPostCompletion_ReactionNoStartReaction(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	assert.Empty(t, fc.DeletedReactions, "nothing to clean up when start reaction was never posted")
	require.Len(t, fc.AddedReactions, 1)
	assert.Equal(t, "+1", fc.AddedReactions[0].Content)
}

func TestPostStart_ReactionErrorIsNonFatal(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"AddIssueReaction": fmt.Errorf("boom")}
	cfg := config.StatusNotificationConfig{
		Reaction: config.ReactionNotificationConfig{Start: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	err := n.PostStart(context.Background(), "Working")
	require.NoError(t, err, "a failed reaction should not fail the run")
}

// A comment API failure should leave the start reaction in place rather
// than swapping it to reflect a completion that was never successfully
// recorded — otherwise the reaction and comment tell contradictory stories.
func TestPostCompletion_ReactionNotSwappedWhenCommentFails(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.Len(t, fc.AddedReactions, 1)

	fc.Errors = map[string]error{"UpdateIssueComment": fmt.Errorf("boom")}

	err := n.PostCompletion(context.Background(), "Working", "success")
	require.Error(t, err)

	assert.Empty(t, fc.DeletedReactions, "start reaction should survive a failed completion comment")
	assert.Len(t, fc.AddedReactions, 1, "no completion reaction should be posted when the comment fails")
}

// Verify that a failed start reaction delete preserves the in-memory ID
// so a subsequent caller could retry the cleanup.
func TestPostCompletion_ReactionDeleteFailPreservesID(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NotZero(t, n.startReactionID)

	fc.Errors["DeleteIssueReaction"] = fmt.Errorf("transient API error")

	var warnings []string
	n.SetWarnFunc(func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	assert.NotZero(t, n.startReactionID, "startReactionID should be preserved when delete fails")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "transient API error")
	// When the start-reaction delete fails, the completion reaction must
	// NOT be posted — otherwise both 👀 and 👍/😕 end up stacked.
	assert.Len(t, fc.AddedReactions, 1, "only the start reaction should exist; no completion reaction should be added when delete fails")
}

// Verify that the suppressed-completion path (cleanupComment) attempts
// the comment delete before the reaction swap, and skips the reaction
// swap when the delete fails.
func TestPostCompletion_SuppressedCompletion_SkipsReactionOnDeleteFailure(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NotZero(t, n.startCommentID)
	require.NotZero(t, n.startReactionID)

	fc.Errors["DeleteIssueComment"] = fmt.Errorf("forbidden")

	var warnings []string
	n.SetWarnFunc(func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "forbidden")
	assert.Empty(t, fc.DeletedReactions, "start reaction should NOT be cleaned up when comment delete fails")
	assert.Len(t, fc.AddedReactions, 1, "no completion reaction should be posted when comment delete fails")
}

// Verify that the suppressed-completion path correctly swaps the
// reaction when there is no comment to clean up.
func TestPostCompletion_SuppressedCompletion_SwapsReactionWhenNoComment(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.Zero(t, n.startCommentID, "no start comment when start disabled")
	require.NotZero(t, n.startReactionID)

	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	assert.Equal(t, []int64{1}, fc.DeletedReactions, "start reaction should be cleaned up")
	require.Len(t, fc.AddedReactions, 2)
	assert.Equal(t, "+1", fc.AddedReactions[1].Content)
}

// Verify that ErrNotSupported from reaction operations is truly silent
// (no warnings logged), matching the docs that say GitLab reaction
// config is "silently a no-op."
func TestPostStart_ReactionErrNotSupported_Silent(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["AddIssueReaction"] = forge.ErrNotSupported
	cfg := config.StatusNotificationConfig{
		Reaction: config.ReactionNotificationConfig{Start: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	var warnings []string
	n.SetWarnFunc(func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	assert.Empty(t, warnings, "ErrNotSupported should not produce a warning")
}

func TestPostCompletion_ReactionErrNotSupported_Silent(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment:  config.CommentNotificationConfig{Start: "disabled", Completion: "disabled"},
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Working"))

	fc.Errors["DeleteIssueReaction"] = forge.ErrNotSupported
	fc.Errors["AddIssueReaction"] = forge.ErrNotSupported

	var warnings []string
	n.SetWarnFunc(func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))
	assert.Empty(t, warnings, "ErrNotSupported should not produce warnings during completion")
}

// --- Comment-targeted reaction tests (slash-command triggered runs) ---

func TestPostStart_ReactionTargetsTriggeringComment(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Reaction: config.ReactionNotificationConfig{Start: "enabled"},
	}
	n := newTestNotifier(fc, cfg)
	n.SetTriggerCommentID(555)

	require.NoError(t, n.PostStart(context.Background(), "Working"))

	assert.Empty(t, fc.AddedReactions, "should not react to the issue/PR when triggered by a slash command")
	require.Len(t, fc.AddedCommentReactions, 1)
	assert.Equal(t, forge.CommentReactionRecord{Owner: "org", Repo: "repo", CommentID: 555, Content: "eyes"}, fc.AddedCommentReactions[0])
}

func TestPostCompletion_ReactionTargetsTriggeringComment(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Reaction: config.ReactionNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := newTestNotifier(fc, cfg)
	n.SetTriggerCommentID(555)

	require.NoError(t, n.PostStart(context.Background(), "Working"))
	require.NoError(t, n.PostCompletion(context.Background(), "Working", "success"))

	assert.Empty(t, fc.DeletedReactions, "cleanup should target the comment reaction, not the issue reaction")
	require.Len(t, fc.DeletedCommentReactions, 1)
	assert.Empty(t, fc.AddedReactions)
	require.Len(t, fc.AddedCommentReactions, 2)
	assert.Equal(t, "+1", fc.AddedCommentReactions[1].Content)
}
