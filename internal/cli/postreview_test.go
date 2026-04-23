package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"io"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestBuildUpdatedBody_CollapsesOldContent(t *testing.T) {
	oldBody := ReviewMarker + "\nOld review findings here."
	newBody := ReviewMarker + "\nNew review findings here."

	result := buildUpdatedBody(oldBody, newBody)

	assert.Contains(t, result, ReviewMarker)
	assert.Contains(t, result, "New review findings here.")
	assert.Contains(t, result, "<details>")
	assert.Contains(t, result, "<summary>Previous review</summary>")
	assert.Contains(t, result, "Old review findings here.")
	assert.Contains(t, result, "</details>")
}

func TestBuildUpdatedBody_PreservesNestedHistory(t *testing.T) {
	// Simulate a third run where old body already has a collapsed section.
	oldBody := ReviewMarker + "\nSecond review.\n\n<details>\n<summary>Previous review</summary>\n\nFirst review.\n\n</details>"
	newBody := ReviewMarker + "\nThird review."

	result := buildUpdatedBody(oldBody, newBody)

	assert.Contains(t, result, "Third review.")
	assert.Contains(t, result, "Second review.")
	assert.Contains(t, result, "First review.")
	// Should have nested details blocks.
	assert.Equal(t, 2, strings.Count(result, "<details>"))
	assert.Equal(t, 2, strings.Count(result, "</details>"))
}

func TestBuildUpdatedBody_TruncatesLargeHistory(t *testing.T) {
	// Create a body that exceeds maxCommentSize.
	largeOld := ReviewMarker + "\n" + strings.Repeat("x", maxCommentSize)
	newBody := ReviewMarker + "\nNew review."

	result := buildUpdatedBody(largeOld, newBody)

	assert.LessOrEqual(t, len(result), maxCommentSize+100) // allow for truncation message
	assert.Contains(t, result, "truncated due to comment size limits")
}

func TestTruncateBody(t *testing.T) {
	t.Run("under limit", func(t *testing.T) {
		body := "short body"
		assert.Equal(t, body, truncateBody(body))
	})

	t.Run("over limit", func(t *testing.T) {
		body := strings.Repeat("a", maxCommentSize+1000)
		result := truncateBody(body)
		assert.LessOrEqual(t, len(result), maxCommentSize+100)
		assert.Contains(t, result, "truncated")
	})
}

func TestPostReview_CreateNew(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "fullsend-bot"
	printer := ui.New(io.Discard)
	ctx := context.Background()

	err := postReview(ctx, client, "owner", "repo", 1, "Review findings.", false, printer)
	require.NoError(t, err)

	// Should have created a comment.
	comments := client.IssueComments["owner/repo/1"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, ReviewMarker)
	assert.Contains(t, comments[0].Body, "Review findings.")
}

func TestPostReview_UpdateExisting(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "fullsend-bot"
	client.IssueComments = map[string][]forge.IssueComment{
		"owner/repo/1": {
			{ID: 100, Body: ReviewMarker + "\nOld findings.", Author: "fullsend-bot"},
		},
	}
	printer := ui.New(io.Discard)
	ctx := context.Background()

	err := postReview(ctx, client, "owner", "repo", 1, "New findings.", false, printer)
	require.NoError(t, err)

	// Should have called UpdateIssueComment.
	require.Len(t, client.UpdatedComments, 1)
	assert.Equal(t, 100, client.UpdatedComments[0].CommentID)
	assert.Contains(t, client.UpdatedComments[0].Body, "New findings.")
	assert.Contains(t, client.UpdatedComments[0].Body, "<details>")
	assert.Contains(t, client.UpdatedComments[0].Body, "Old findings.")
}

func TestPostReview_DryRun(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(io.Discard)
	ctx := context.Background()

	err := postReview(ctx, client, "owner", "repo", 1, "Review body.", true, printer)
	require.NoError(t, err)

	// Should NOT have created any comments.
	assert.Empty(t, client.IssueComments)
}

func TestPostReview_SkipsNonMarkerComments(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "fullsend-bot"
	client.IssueComments = map[string][]forge.IssueComment{
		"owner/repo/1": {
			{ID: 50, Body: "Regular comment without marker.", Author: "human"},
		},
	}
	printer := ui.New(io.Discard)
	ctx := context.Background()

	err := postReview(ctx, client, "owner", "repo", 1, "Review.", false, printer)
	require.NoError(t, err)

	// Should create new comment since no marker was found.
	comments := client.IssueComments["owner/repo/1"]
	require.Len(t, comments, 2)
	assert.Contains(t, comments[1].Body, ReviewMarker)
}

func TestParseReviewResult_JSON(t *testing.T) {
	input := `{"body": "Looks good!", "action": "approve"}`
	result := parseReviewResult(input)
	assert.Equal(t, "Looks good!", result.Body)
	assert.Equal(t, "approve", result.Action)
}

func TestParseReviewResult_PlainText(t *testing.T) {
	input := "This is plain text review."
	result := parseReviewResult(input)
	assert.Equal(t, input, result.Body)
	assert.Equal(t, "comment", result.Action)
}

func TestParseReviewResult_DefaultAction(t *testing.T) {
	input := `{"body": "Some review"}`
	result := parseReviewResult(input)
	assert.Equal(t, "Some review", result.Body)
	assert.Equal(t, "comment", result.Action)
}

func TestReviewMarkerDetection(t *testing.T) {
	body := "Some text\n" + ReviewMarker + "\nReview content"
	assert.True(t, strings.Contains(body, ReviewMarker))

	noMarker := "Some text\nReview content"
	assert.False(t, strings.Contains(noMarker, ReviewMarker))
}
