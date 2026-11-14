package sticky

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// UpdateComment replaces the body of a comment the same run created (the
// review door adding its diff hash after the formal approval succeeds),
// so an approve run leaves one review-agent comment. It assembles the
// body exactly as Post does: the runner's marker on the first line, the
// size cap applied, and a trusted leading line the cap would cut refused
// rather than written partial.

func TestUpdateComment_ReplacesBodyOfCommentCreatedThisRun(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	created, err := client.CreateIssueComment(context.Background(), "o", "r", 1, "<!-- test -->\nFirst.")
	require.NoError(t, err)

	cfg := Config{Marker: "<!-- test -->"}
	err = UpdateComment(context.Background(), client, "o", "r", created.ID, "Second.", cfg, printer)
	require.NoError(t, err)

	comments := client.IssueComments["o/r/1"]
	require.Len(t, comments, 1, "the update adds no comment")
	assert.Equal(t, "<!-- test -->\nSecond.", comments[0].Body, "the marker stays on the first line")
	require.Len(t, client.UpdatedComments, 1)
	assert.Equal(t, created.ID, client.UpdatedComments[0].CommentID, "the comment created this run is the one updated")
}

func TestUpdateComment_RequiresTheCreatedCommentID(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	err := UpdateComment(context.Background(), client, "o", "r", 0, "Second.", Config{Marker: "<!-- test -->"}, printer)

	require.Error(t, err, "without the ID the update must not guess a comment to edit")
	assert.Contains(t, err.Error(), "comment ID is required")
	assert.Empty(t, client.UpdatedComments, "no comment is edited in place")
}

func TestUpdateComment_TrustedLeadingLineTooLargeForSizeCapIsRefused(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	created, err := client.CreateIssueComment(context.Background(), "o", "r", 1, "<!-- test -->\nFirst.")
	require.NoError(t, err)

	// The same cap shape as the create path: room for the runner marker
	// and the truncation note but not the whole trusted line, so the cut
	// would land inside that line.
	cfg := Config{
		Marker:             "<!-- fullsend:review-agent -->",
		MaxSize:            len("<!-- fullsend:review-agent -->\n"+trustedLine) + 20,
		TrustedLeadingLine: trustedLineRe,
	}
	body := trustedLine + "\n\n" + strings.Repeat("x", 200)

	err = UpdateComment(context.Background(), client, "o", "r", created.ID, body, cfg, printer)

	require.Error(t, err, "UpdateComment must refuse rather than write a partial trusted line")
	assert.Contains(t, err.Error(), "size cap")
	assert.Equal(t, "<!-- test -->\nFirst.", client.IssueComments["o/r/1"][0].Body,
		"the comment keeps the body it had: nothing partial is written")
}
