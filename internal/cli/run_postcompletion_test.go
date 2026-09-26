package cli

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestPostCompletionTimeout_MatchesMaxMintDuration(t *testing.T) {
	// A shorter bound is the bug in #6667: mintclient retries are cut
	// off mid-backoff and the start comment is left non-terminal.
	assert.Equal(t, mintclient.MaxMintDuration, postCompletionTimeout)
}

func TestPostCompletionStatus_SurvivesParentCancel(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot[bot]"
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := statuscomment.New(tracker.NewForgeClient(fc), cfg, "org/repo", 7, "", "abc1234", "run-1")
	require.NoError(t, n.PostStart(context.Background(), "Working"))

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	printer := ui.New(io.Discard)
	postCompletionStatus(cancelled, n, printer, "Working", "success", "")

	comments := fc.IssueComments["org/repo/7"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "Finished Working")
	assert.Contains(t, comments[0].Body, "✅ Success")
}

func TestPostCompletionStatus_TimeoutProducesWarning(t *testing.T) {
	orig := postCompletionTimeout
	postCompletionTimeout = 10 * time.Millisecond
	defer func() { postCompletionTimeout = orig }()

	fc := forge.NewFakeClient()
	cfg := config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{Start: "enabled", Completion: "enabled"},
	}
	n := statuscomment.New(tracker.NewForgeClient(fc), cfg, "org/repo", 7, "", "abc1234", "run-1")
	n.SetClientFactory(func(ctx context.Context) (tracker.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	var buf bytes.Buffer
	printer := ui.New(&buf)
	postCompletionStatus(context.Background(), n, printer, "Working", "success", "")

	assert.Contains(t, buf.String(), "Failed to post completion status")
	assert.Contains(t, buf.String(), "context deadline exceeded")
	assert.Empty(t, fc.IssueComments, "no comment should be posted when minting times out")
}
