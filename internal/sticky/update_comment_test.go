package sticky

import (
	"context"
	"io"
	"strconv"
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

// TestUpdateComment_DryRunPrintsTheNoticeAndTouchesNothing pins the dry-run
// branch: it prints the notice naming the comment and returns nil without
// reaching the forge. The shipped doors call UpdateComment only outside dry
// run today, so this locks the branch down for future wiring.
func TestUpdateComment_DryRunPrintsTheNoticeAndTouchesNothing(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	var out strings.Builder
	printer := ui.New(&out)

	created, err := client.CreateIssueComment(context.Background(), "o", "r", 1, "<!-- test -->\nFirst.")
	require.NoError(t, err)

	cfg := Config{Marker: "<!-- test -->", DryRun: true}
	err = UpdateComment(context.Background(), client, "o", "r", created.ID, "Second.", cfg, printer)

	require.NoError(t, err, "a dry run succeeds without touching the forge")
	assert.Contains(t, out.String(), "Dry run", "the dry-run notice prints")
	assert.Contains(t, out.String(), strconv.Itoa(created.ID), "the notice names the comment")
	assert.Empty(t, client.UpdatedComments, "a dry run never edits a comment")
	comments := client.IssueComments["o/r/1"]
	require.Len(t, comments, 1)
	assert.Equal(t, "<!-- test -->\nFirst.", comments[0].Body, "the existing body stands untouched")
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

// TestUpdateComment_TrustedLeadingLinesThirdLineCutBySizeCapIsRefused
// mirrors Post's triple-line case (the review approve path passes
// MaxTrustedLeadingLines=3): a cut landing inside the third trusted line,
// past the first line the single-line guard checks, must refuse here too,
// so a regression in the shared splitTrusted handling breaks both doors'
// tests instead of staying green on the update path.
func TestUpdateComment_TrustedLeadingLinesThirdLineCutBySizeCapIsRefused(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	created, err := client.CreateIssueComment(context.Background(), "o", "r", 1, "<!-- test -->\nFirst.")
	require.NoError(t, err)

	// The same cap shape as the create path's triple-line case: room for
	// the runner marker, the truncation note and the first two trusted
	// lines plus most of the third, so the cut lands inside the
	// projection line.
	cfg := Config{
		Marker:                 "<!-- fullsend:review-agent -->",
		TrustedLeadingLine:     trustedLinesRe,
		MaxTrustedLeadingLines: 3,
		MaxSize:                len("<!-- fullsend:review-agent -->\n"+hashLine+"\n"+headLine+"\n"+trustedLine) + 20,
	}
	body := hashLine + "\n" + headLine + "\n" + trustedLine + "\n\n" + strings.Repeat("x", 200)

	err = UpdateComment(context.Background(), client, "o", "r", created.ID, body, cfg, printer)

	require.Error(t, err, "UpdateComment must refuse rather than write a partial third trusted line")
	assert.Contains(t, err.Error(), "size cap")
	assert.Equal(t, "<!-- test -->\nFirst.", client.IssueComments["o/r/1"][0].Body,
		"the comment keeps the body it had: nothing partial is written")
}
