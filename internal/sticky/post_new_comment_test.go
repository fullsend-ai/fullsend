package sticky

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Issue #9334: sticky comments are removed. Every agent result is posted
// as a new comment at the end of the thread. Nothing is edited in place,
// there are no "Previous run" sections, and the thread itself is the
// history.

func TestPost_SecondRunAddsNewCommentAtEnd(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	client.IssueComments = map[string][]forge.IssueComment{
		"o/r/1": {
			{ID: 100, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-100", Body: "<!-- test -->\nFirst result.", Author: "bot"},
		},
	}
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->", KeepHistory: true}
	commentURL, err := Post(context.Background(), client, "o", "r", 1, "Second result.", cfg, printer)
	require.NoError(t, err)

	comments := client.IssueComments["o/r/1"]
	require.Len(t, comments, 2, "a second run must add a new comment at the end of the thread")
	assert.Equal(t, "<!-- test -->\nFirst result.", comments[0].Body, "the earlier comment must be left unchanged")
	assert.Contains(t, comments[1].Body, "Second result.")
	assert.Contains(t, comments[1].Body, "<!-- test -->", "the new comment keeps the hidden marker")
	assert.NotContains(t, comments[1].Body, "Previous run", "no Previous run section is produced")
	assert.Empty(t, client.UpdatedComments, "no comment is edited in place")
	assert.Contains(t, commentURL, "issuecomment-", "the result is the newly created comment")
	assert.NotEqual(t, "https://github.com/o/r/issues/1#issuecomment-100", commentURL, "the returned URL is not the earlier comment's")
}

func TestPost_EveryRunAppendsNewComment(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->", KeepHistory: true}
	for _, body := range []string{"Run 1.", "Run 2.", "Run 3."} {
		_, err := Post(context.Background(), client, "o", "r", 1, body, cfg, printer)
		require.NoError(t, err)
	}

	comments := client.IssueComments["o/r/1"]
	require.Len(t, comments, 3, "three runs leave three comments; the thread itself is the history")
	for i, want := range []string{"Run 1.", "Run 2.", "Run 3."} {
		assert.Contains(t, comments[i].Body, want)
		assert.NotContains(t, comments[i].Body, "Previous run")
	}
	assert.Empty(t, client.UpdatedComments, "no comment is edited in place")
}
