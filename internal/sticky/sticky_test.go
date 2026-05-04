package sticky

import (
	"context"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

var testCfg = Config{
	Marker:       "<!-- fullsend:test -->",
	FooterMarker: "<!-- fullsend:test-footer -->",
}

func TestFindMarkedComment(t *testing.T) {
	comments := []forge.IssueComment{
		{ID: 1, Body: "regular comment", Author: "human"},
		{ID: 2, Body: "<!-- fullsend:test -->\nmarked", Author: "bot"},
		{ID: 3, Body: "another comment", Author: "human"},
	}

	found := FindMarkedComment(comments, "<!-- fullsend:test -->", "bot")
	require.NotNil(t, found)
	assert.Equal(t, 2, found.ID)

	notFound := FindMarkedComment(comments, "<!-- fullsend:other -->", "bot")
	assert.Nil(t, notFound)
}

func TestFindMarkedComment_IgnoresSpoofedMarker(t *testing.T) {
	comments := []forge.IssueComment{
		{ID: 1, Body: "<!-- fullsend:test -->\nspoofed by attacker", Author: "attacker"},
		{ID: 2, Body: "<!-- fullsend:test -->\nreal bot comment", Author: "bot"},
	}

	found := FindMarkedComment(comments, "<!-- fullsend:test -->", "bot")
	require.NotNil(t, found)
	assert.Equal(t, 2, found.ID, "should skip spoofed comment from attacker")
}

func TestFindMarkedComment_EmptyBotUser(t *testing.T) {
	comments := []forge.IssueComment{
		{ID: 1, Body: "<!-- fullsend:test -->\nmarked", Author: "anyone"},
	}

	found := FindMarkedComment(comments, "<!-- fullsend:test -->", "")
	require.NotNil(t, found, "empty botUser should match any author")
	assert.Equal(t, 1, found.ID)
}

func TestFindMarkedComment_Empty(t *testing.T) {
	found := FindMarkedComment(nil, "<!-- test -->", "bot")
	assert.Nil(t, found)
}

func TestBuildUpdatedBody_CollapsesOldContent(t *testing.T) {
	oldBody := "<!-- fullsend:test -->\nOld findings."
	newBody := "<!-- fullsend:test -->\nNew findings."

	result := BuildUpdatedBody(oldBody, newBody, testCfg)

	assert.Contains(t, result, "New findings.")
	assert.Contains(t, result, "<details>")
	assert.Contains(t, result, "<summary>Previous run</summary>")
	assert.Contains(t, result, "Old findings.")
}

func TestBuildUpdatedBody_FlatHistory(t *testing.T) {
	cfg := Config{Marker: "<!-- m -->"}

	// Run 1 → Run 2
	body1 := "<!-- m -->\nRun 1 content."
	body2 := "<!-- m -->\nRun 2 content."
	result2 := BuildUpdatedBody(body1, body2, cfg)

	// Run 2 → Run 3
	body3 := "<!-- m -->\nRun 3 content."
	result3 := BuildUpdatedBody(result2, body3, cfg)

	// Run 3 → Run 4
	body4 := "<!-- m -->\nRun 4 content."
	result4 := BuildUpdatedBody(result3, body4, cfg)

	// Run 4 → Run 5
	body5 := "<!-- m -->\nRun 5 content."
	result5 := BuildUpdatedBody(result4, body5, cfg)

	// 4 re-runs should produce 4 <details> blocks, all flat (not nested).
	assert.Equal(t, 4, strings.Count(result5, "<details>"))
	assert.Equal(t, 4, strings.Count(result5, "</details>"))

	// All run contents should be present.
	assert.Contains(t, result5, "Run 5 content.")
	assert.Contains(t, result5, "Run 4 content.")
	assert.Contains(t, result5, "Run 3 content.")
	assert.Contains(t, result5, "Run 2 content.")
	assert.Contains(t, result5, "Run 1 content.")

	// No nested details — each </details> should not be inside another <details>.
	// A simple proxy: no line should contain both </details> and <details>.
	for _, line := range strings.Split(result5, "\n") {
		if strings.Contains(line, "</details>") {
			assert.NotContains(t, line, "<details>")
		}
	}
}

func TestBuildUpdatedBody_NestedDetailsInContent(t *testing.T) {
	cfg := Config{Marker: "<!-- m -->"}

	// Run 1 content contains a <details> block (common in GitHub review output).
	body1 := "<!-- m -->\nReview findings:\n<details>\n<summary>Expanded diff</summary>\nsome diff content\n</details>\nEnd of review."
	body2 := "<!-- m -->\nRun 2 content."
	result2 := BuildUpdatedBody(body1, body2, cfg)

	// Run 2 → Run 3 to verify content survives multiple collapses.
	body3 := "<!-- m -->\nRun 3 content."
	result3 := BuildUpdatedBody(result2, body3, cfg)

	// All content should be preserved.
	assert.Contains(t, result3, "Run 3 content.")
	assert.Contains(t, result3, "Run 2 content.")
	assert.Contains(t, result3, "some diff content")
	assert.Contains(t, result3, "End of review.")
}

func TestBuildUpdatedBody_FooterStripping(t *testing.T) {
	oldBody := "<!-- fullsend:test -->\nOld review.\n\n<!-- fullsend:test-footer -->\n_some footer info_"
	newBody := "<!-- fullsend:test -->\nNew review."

	result := BuildUpdatedBody(oldBody, newBody, testCfg)

	assert.Contains(t, result, "New review.")
	assert.Contains(t, result, "Old review.")
	assert.Contains(t, result, "<!-- fullsend:test-footer -->")
	assert.Contains(t, result, "_some footer info_")

	// Footer should not be inside a <details> block.
	footerIdx := strings.Index(result, "<!-- fullsend:test-footer -->")
	lastDetailsClose := strings.LastIndex(result, "</details>")
	if lastDetailsClose >= 0 {
		assert.Greater(t, footerIdx, lastDetailsClose, "footer should come after all <details> blocks")
	}
}

func TestBuildUpdatedBody_NoFooterMarker(t *testing.T) {
	cfg := Config{Marker: "<!-- m -->"}
	oldBody := "<!-- m -->\nOld content."
	newBody := "<!-- m -->\nNew content."

	result := BuildUpdatedBody(oldBody, newBody, cfg)

	assert.Contains(t, result, "New content.")
	assert.Contains(t, result, "Old content.")
}

func TestTruncateBody_UnderLimit(t *testing.T) {
	body := "short body"
	assert.Equal(t, body, TruncateBody(body, defaultMaxSize))
}

func TestTruncateBody_OverLimit(t *testing.T) {
	body := strings.Repeat("a", defaultMaxSize+1000)
	result := TruncateBody(body, defaultMaxSize)
	assert.LessOrEqual(t, len(result), defaultMaxSize+100)
	assert.Contains(t, result, "truncated")
}

func TestBuildUpdatedBody_DropsOldestHistoryOnOverflow(t *testing.T) {
	cfg := Config{Marker: "<!-- m -->", MaxSize: 500}

	body1 := "<!-- m -->\n" + strings.Repeat("A", 100)
	body2 := "<!-- m -->\n" + strings.Repeat("B", 100)
	result2 := BuildUpdatedBody(body1, body2, cfg)

	body3 := "<!-- m -->\n" + strings.Repeat("C", 100)
	result3 := BuildUpdatedBody(result2, body3, cfg)

	body4 := "<!-- m -->\n" + strings.Repeat("D", 100)
	result4 := BuildUpdatedBody(result3, body4, cfg)

	assert.LessOrEqual(t, len(result4), 500, "combined body should not exceed maxSize")
	assert.Contains(t, result4, strings.Repeat("D", 100), "current content must be preserved")
	assert.NotContains(t, result4, "truncated", "should drop history blocks, not blind-truncate")
}

func TestTruncateBody_RuneSafe(t *testing.T) {
	// Build a string with multi-byte UTF-8 characters that would be split
	// at a naive byte boundary.
	prefix := strings.Repeat("a", 100)
	multibyte := strings.Repeat("é", 50) // é is 2 bytes in UTF-8
	body := prefix + multibyte + strings.Repeat("b", defaultMaxSize)

	result := TruncateBody(body, 200)

	// The result must be valid UTF-8.
	assert.True(t, utf8.ValidString(result), "truncated body should be valid UTF-8")
	assert.Contains(t, result, "truncated")
}

func TestPost_CreateNew(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->"}
	commentURL, err := Post(context.Background(), client, "o", "r", 1, "Body.", cfg, printer)
	require.NoError(t, err)
	assert.Contains(t, commentURL, "issuecomment-")

	comments := client.IssueComments["o/r/1"]
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "<!-- test -->")
	assert.Contains(t, comments[0].Body, "Body.")
}

func TestPost_UpdateExisting(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	client.IssueComments = map[string][]forge.IssueComment{
		"o/r/1": {{ID: 100, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-100", Body: "<!-- test -->\nOld.", Author: "bot"}},
	}
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->"}
	commentURL, err := Post(context.Background(), client, "o", "r", 1, "New.", cfg, printer)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/o/r/issues/1#issuecomment-100", commentURL)

	require.Len(t, client.UpdatedComments, 1)
	assert.Equal(t, 100, client.UpdatedComments[0].CommentID)
	assert.Contains(t, client.UpdatedComments[0].Body, "New.")
	assert.Contains(t, client.UpdatedComments[0].Body, "<details>")
	assert.Contains(t, client.UpdatedComments[0].Body, "Old.")
}

func TestPost_DryRun(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->", DryRun: true}
	commentURL, err := Post(context.Background(), client, "o", "r", 1, "Body.", cfg, printer)
	require.NoError(t, err)
	assert.Empty(t, commentURL)

	assert.Empty(t, client.IssueComments)
	assert.Empty(t, client.UpdatedComments)
}

func TestPost_EmptyBody(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->"}
	_, err := Post(context.Background(), client, "o", "r", 1, "", cfg, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestPost_WhitespaceBody(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->"}
	_, err := Post(context.Background(), client, "o", "r", 1, "  \n\t  ", cfg, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestPost_EmptyMarker(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(io.Discard)

	cfg := Config{Marker: ""}
	_, err := Post(context.Background(), client, "o", "r", 1, "Body.", cfg, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marker")
}

func TestPost_UpdateExisting_EmptyHTMLURL(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	client.IssueComments = map[string][]forge.IssueComment{
		"o/r/1": {{ID: 100, Body: "<!-- test -->\nOld.", Author: "bot"}},
	}
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->"}
	commentURL, err := Post(context.Background(), client, "o", "r", 1, "New.", cfg, printer)
	require.NoError(t, err)
	assert.Empty(t, commentURL, "should return empty URL when existing comment has no HTMLURL")

	require.Len(t, client.UpdatedComments, 1)
	assert.Contains(t, client.UpdatedComments[0].Body, "New.")
}

func TestPost_DryRunExisting(t *testing.T) {
	client := forge.NewFakeClient()
	client.IssueComments = map[string][]forge.IssueComment{
		"o/r/1": {{ID: 100, Body: "<!-- test -->\nOld.", Author: "bot"}},
	}
	printer := ui.New(io.Discard)

	cfg := Config{Marker: "<!-- test -->", DryRun: true}
	commentURL, err := Post(context.Background(), client, "o", "r", 1, "New.", cfg, printer)
	require.NoError(t, err)
	assert.Empty(t, commentURL)

	assert.Empty(t, client.UpdatedComments)
}
