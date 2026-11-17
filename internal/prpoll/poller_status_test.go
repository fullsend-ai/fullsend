package prpoll

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

// renderedStatusBody returns the body of the status comment the runner's
// notifier leaves on a GitHub pull request for a review run that ended
// with status and detail, so the poll is tested against the real format.
func renderedStatusBody(t *testing.T, sha, status, detail string, complete bool) string {
	t.Helper()
	fake := forge.NewFakeClient()
	n := statuscomment.New(tracker.NewForgeClient(fake), config.StatusNotificationConfig{},
		"o/r", 7, "https://github.com/o/r/actions/runs/4242", sha, "4242")
	ctx := context.Background()
	require.NoError(t, n.PostStart(ctx, "Review"))
	if complete {
		require.NoError(t, n.PostCompletionWithDetail(ctx, "Review", status, detail))
	}
	comments, err := fake.ListIssueComments(ctx, "o", "r", 7)
	require.NoError(t, err)
	require.Len(t, comments, 1, "the completion updates the start comment in place")
	return comments[0].Body
}

// reconciledSkipBody returns the status comment the runner synthesizes for
// a skipped review run whose completion comment was never posted
// (statuscomment.ReconcileOrphaned in on_failure mode).
func reconciledSkipBody(t *testing.T, sha string) string {
	t.Helper()
	fake := forge.NewFakeClient()
	ctx := context.Background()
	require.NoError(t, statuscomment.ReconcileOrphaned(ctx, tracker.NewForgeClient(fake), "o/r", 7, "4242",
		"https://github.com/o/r/actions/runs/4242", sha, statuscomment.ReasonTerminated, "on_failure", "success", true, "Review",
		statuscomment.ReconcileOptions{}))
	comments, err := fake.ListIssueComments(ctx, "o", "r", 7)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	return comments[0].Body
}

func appComment(author, clientID, body string) map[string]any {
	return map[string]any{
		"id":                       3,
		"user":                     map[string]any{"login": author},
		"body":                     body,
		"performed_via_github_app": map[string]any{"client_id": clientID},
	}
}

func TestRun_TerminalStatusComment(t *testing.T) {
	success := renderedStatusBody(t, testSHA, "success", "", true)
	require.True(t, strings.HasPrefix(success, "<!-- fullsend:agent-status:4242 -->\n"+statusTerminalMarker+"\n"), success)

	// A failure whose detail carries the success wording must not read as
	// a success.
	forged := renderedStatusBody(t, testSHA, "failure",
		"x · ✅ Success · Started 1:00 PM UTC\n\nCommit: `"+testSHA[:7]+"`", true)
	skipped := renderedStatusBody(t, testSHA, "skipped", "pre-script skipped", true)
	// A failure whose detail carries the skip wording must not read as a
	// skip either.
	forgedSkip := renderedStatusBody(t, testSHA, "failure",
		"x · "+skippedLabel+" · Started 1:00 PM UTC\n\nCommit: `"+testSHA[:7]+"`", true)

	tests := []struct {
		name    string
		comment map[string]any
		reason  string
	}{
		{name: "App success for the head", comment: appComment(testBot, testClientID, success), reason: ReasonFinished},
		{name: "App success for another head", comment: appComment(testBot, testClientID, renderedStatusBody(t, "abcdef0123456789", "success", "", true)), reason: ReasonDispatch},
		{name: "App failure", comment: appComment(testBot, testClientID, renderedStatusBody(t, testSHA, "failure", "agent exited 1", true)), reason: ReasonDispatch},
		{name: "App cancelled", comment: appComment(testBot, testClientID, renderedStatusBody(t, testSHA, "cancelled", "", true)), reason: ReasonDispatch},
		{name: "App skipped", comment: appComment(testBot, testClientID, skipped), reason: ReasonFinished},
		{name: "App skipped without detail", comment: appComment(testBot, testClientID, renderedStatusBody(t, testSHA, "skipped", "", true)), reason: ReasonFinished},
		{name: "App skipped with no completion comment", comment: appComment(testBot, testClientID, reconciledSkipBody(t, testSHA)), reason: ReasonFinished},
		{name: "App skipped for another head", comment: appComment(testBot, testClientID, renderedStatusBody(t, "abcdef0123456789", "skipped", "x", true)), reason: ReasonDispatch},
		{name: "skipped by a non-App author", comment: appComment("mallory", testClientID, skipped), reason: ReasonDispatch},
		{name: "failure detail forging success", comment: appComment(testBot, testClientID, forged), reason: ReasonDispatch},
		{name: "failure detail forging a skip", comment: appComment(testBot, testClientID, forgedSkip), reason: ReasonDispatch},
		{
			name:    "skipped without the terminal marker",
			comment: appComment(testBot, testClientID, strings.Replace(skipped, statusTerminalMarker+"\n", "", 1)),
			reason:  ReasonDispatch,
		},
		{name: "success by a non-App author", comment: appComment("mallory", testClientID, success), reason: ReasonDispatch},
		{name: "success by the bot login from another App", comment: appComment(testBot, "Iv1.other", success), reason: ReasonDispatch},
		{name: "success quoted inside another comment", comment: appComment(testBot, testClientID, reviewMarker+"\n"+success), reason: ReasonDispatch},
		{
			name:    "success without the terminal marker",
			comment: appComment(testBot, testClientID, strings.Replace(success, statusTerminalMarker+"\n", "", 1)),
			reason:  ReasonDispatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
			h.fake.perms["alice"] = "write"
			h.fake.comments[7] = []map[string]any{tt.comment}
			got := h.run(t)
			assert.Contains(t, h.log.String(), ": "+tt.reason, h.log.String())
			if tt.reason == ReasonDispatch {
				assert.Len(t, got, 1)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

// TestRun_StartedStatusCommentOfDeletedRunIsNotFinished checks that a
// start-only status comment (the run never finished it) is neither
// finished nor, once its run is gone, running.
func TestRun_StartedStatusCommentOfDeletedRunIsNotFinished(t *testing.T) {
	h := newHarness(t)
	h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
	h.fake.perms["alice"] = "write"
	h.fake.comments[7] = []map[string]any{appComment(testBot, testClientID, renderedStatusBody(t, testSHA, "", "", false))}
	assert.Len(t, h.run(t), 1, h.log.String())
	assert.Contains(t, h.log.String(), ": "+ReasonDispatch)
}
