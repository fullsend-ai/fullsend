package e2eauth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveEventMode(t *testing.T) {
	t.Run("empty allows author path", func(t *testing.T) {
		mode, err := resolveEventMode(Input{})
		require.NoError(t, err)
		assert.Equal(t, "author", mode)
	})
	t.Run("pull_request", func(t *testing.T) {
		mode, err := resolveEventMode(Input{EventName: "pull_request"})
		require.NoError(t, err)
		assert.Equal(t, "author", mode)
	})
	t.Run("issue_comment", func(t *testing.T) {
		mode, err := resolveEventMode(Input{EventName: "issue_comment"})
		require.NoError(t, err)
		assert.Equal(t, "issue_comment", mode)
	})
	t.Run("unsupported", func(t *testing.T) {
		_, err := resolveEventMode(Input{EventName: "workflow_dispatch"})
		require.Error(t, err)
	})
	t.Run("malformed issue_comment", func(t *testing.T) {
		_, err := resolveEventMode(Input{EventName: "Issue_comment"})
		require.Error(t, err)
	})
	t.Run("comment id without issue_comment", func(t *testing.T) {
		_, err := resolveEventMode(Input{EventName: "pull_request", CommentID: 1})
		require.Error(t, err)
	})
}

func TestCheck_UnsupportedEventName(t *testing.T) {
	h := newHarness()
	got := Check(context.Background(), h.gate, baseInput().withEventName("workflow_dispatch"))
	assert.Equal(t, "error", got.Reason)
	assert.Error(t, got.Err)
}

func TestCheck_CommentIDWithoutIssueCommentEvent(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.EventName = ""
	in.CommentID = 900
	got := Check(context.Background(), h.gate, in)
	assert.Equal(t, "error", got.Reason)
	assert.Error(t, got.Err)
}

func (in Input) withEventName(name string) Input {
	in.EventName = name
	return in
}
