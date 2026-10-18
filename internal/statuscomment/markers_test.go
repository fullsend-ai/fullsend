package statuscomment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestNeutralizeMarkers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text unchanged",
			in:   "Triage result here.",
			want: "Triage result here.",
		},
		{
			name: "status marker escaped",
			in:   "copied <!-- fullsend:agent-status:123 --> text",
			want: "copied &lt;!-- fullsend:agent-status:123 --> text",
		},
		{
			name: "sticky marker escaped",
			in:   "<!-- fullsend:review-agent -->\nbody",
			want: "&lt;!-- fullsend:review-agent -->\nbody",
		},
		{
			name: "mixed case and extra whitespace escaped",
			in:   "<!--FullSend :x--> and <!--\t\n  FULLSEND\t:y -->",
			want: "&lt;!--FullSend :x--> and &lt;!--\t\n  FULLSEND\t:y -->",
		},
		{
			name: "every occurrence escaped",
			in:   "<!-- fullsend:a --><!-- fullsend:b -->",
			want: "&lt;!-- fullsend:a -->&lt;!-- fullsend:b -->",
		},
		{
			name: "already escaped text unchanged",
			in:   "&lt;!-- fullsend:agent-status:123 -->",
			want: "&lt;!-- fullsend:agent-status:123 -->",
		},
		{
			name: "non-fullsend HTML comment unchanged",
			in:   "<!-- sticky:history-start --> <!-- other:marker -->",
			want: "<!-- sticky:history-start --> <!-- other:marker -->",
		},
		{
			name: "fullsend without colon unchanged",
			in:   "<!-- fullsend is great -->",
			want: "<!-- fullsend is great -->",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeutralizeMarkers(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, NeutralizeMarkers(got), "must be idempotent")
		})
	}
}

func TestBuildMarker_UsesStatusMarkerPrefix(t *testing.T) {
	m, err := buildMarker("run-1")
	require.NoError(t, err)
	assert.Equal(t, "<!-- "+statusMarkerPrefix+"run-1 -->", m)
	assert.Contains(t, m, statusMarkerOpener)
}

// An agent comment carrying escaped status marker text must still count
// as agent output, so the start comment is edited in place rather than a
// new terminal comment reporting that the agent did not post.
func TestPostCompletion_EscapedStatusMarkerInAgentOutput_CountsAsAgentPost(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n, fc := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Triaging issue"))

	agentBody := "<!-- fullsend:triage-agent -->\n" +
		NeutralizeMarkers("quoted: <!-- fullsend:agent-status:999 -->")
	_, err := fc.CreateIssueComment(context.Background(), "org", "repo", 7, agentBody)
	require.NoError(t, err)

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	require.NoError(t, n.PostCompletion(context.Background(), "Triaging issue", "success"))

	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 1, fc.UpdatedComments[0].CommentID)
	assert.Len(t, fc.IssueComments["org/repo/7"], 2)
}

// A live status marker from the same bot is still treated as a status
// comment, so the completion is posted as a new comment.
func TestPostCompletion_LiveStatusMarkerAfterStart_IsStatusComment(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n, fc := newTestNotifier(fc, cfg)

	require.NoError(t, n.PostStart(context.Background(), "Triaging issue"))

	_, err := fc.CreateIssueComment(context.Background(), "org", "repo", 7,
		"<!-- fullsend:agent-status:run-other -->\n🤖 Other · Started 2:35 PM UTC")
	require.NoError(t, err)

	n.now = func() time.Time { return fixedTime().Add(5 * time.Minute) }
	require.NoError(t, n.PostCompletion(context.Background(), "Triaging issue", "success"))

	assert.Empty(t, fc.UpdatedComments)
	assert.Len(t, fc.IssueComments["org/repo/7"], 3)
}
