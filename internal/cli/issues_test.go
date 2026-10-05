package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestNewIssuesCmd_SubcommandRegistration(t *testing.T) {
	cmd := newIssuesCmd()

	var names []string
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	assert.Contains(t, names, "get")
	assert.Contains(t, names, "post-comment")
}

func TestNewIssuesGetCmd_RequiredFlags(t *testing.T) {
	cmd := newIssuesGetCmd()

	for _, name := range []string{"tracker", "project", "number"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}
}

func TestNewIssuesGetCmd_OptionalFlags(t *testing.T) {
	cmd := newIssuesGetCmd()

	for _, name := range []string{"token", "jira-url", "jira-email"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}
}

func TestNewIssuesPostCommentCmd_RequiredFlags(t *testing.T) {
	cmd := newIssuesPostCommentCmd()

	for _, name := range []string{"tracker", "project", "number", "marker"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}
}

func TestNewIssuesPostCommentCmd_DefaultFlags(t *testing.T) {
	cmd := newIssuesPostCommentCmd()

	result := cmd.Flags().Lookup("result")
	require.NotNil(t, result)
	assert.Equal(t, "-", result.DefValue)

	dryRun := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRun)
	assert.Equal(t, "false", dryRun.DefValue)

	onlyIfExists := cmd.Flags().Lookup("only-if-exists")
	require.NotNil(t, onlyIfExists)
	assert.Equal(t, "false", onlyIfExists.DefValue)
}

func TestFindMarkedTrackerComment(t *testing.T) {
	marker := "<!-- test:marker -->"
	comments := []tracker.Comment{
		{ID: "1", Body: "irrelevant comment", Author: "bot"},
		{ID: "0", Body: tracker.Body(marker + "\nauthorless")},
		{ID: "2", Body: tracker.Body(marker + "\nplanted"), Author: "mallory"},
		{ID: "3", Body: tracker.Body(marker + "\nsome content"), Author: "bot"},
		{ID: "4", Body: "another comment", Author: "bot"},
	}

	found := findMarkedTrackerComment(comments, marker, "bot")
	require.NotNil(t, found)
	assert.Equal(t, "3", found.ID)

	assert.Nil(t, findMarkedTrackerComment(comments, marker, ""),
		"an unverified self must never match a comment for editing")
}

func TestFindMarkedTrackerComment_NotFound(t *testing.T) {
	comments := []tracker.Comment{
		{ID: "1", Body: "no marker here"},
	}

	found := findMarkedTrackerComment(comments, "<!-- missing -->", "bot")
	assert.Nil(t, found)
}

func TestFindMarkedTrackerComment_Empty(t *testing.T) {
	found := findMarkedTrackerComment(nil, "<!-- marker -->", "bot")
	assert.Nil(t, found)
}

func TestPostTrackerStickyComment_Create(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}

	url, err := postTrackerStickyComment(context.Background(), tc, "acme/widgets", 42, "hello world", cfg, false, printer)
	require.NoError(t, err)
	assert.NotEmpty(t, url)

	// Verify the comment was created with marker prefix.
	comments, err := tc.ListComments(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "<!-- test -->")
	assert.Contains(t, string(comments[0].Body), "hello world")
}

func TestPostTrackerStickyComment_Update(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}
	ctx := context.Background()

	// First post creates the comment.
	_, err := postTrackerStickyComment(ctx, tc, "acme/widgets", 42, "first run", cfg, false, printer)
	require.NoError(t, err)

	// Second post updates in-place.
	_, err = postTrackerStickyComment(ctx, tc, "acme/widgets", 42, "second run", cfg, false, printer)
	require.NoError(t, err)

	// Verify only one comment exists (updated, not duplicated).
	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "second run")
	assert.Contains(t, string(comments[0].Body), "Previous run")
}

func TestPostTrackerStickyComment_EmptyBody(t *testing.T) {
	fc := forge.NewFakeClient()
	tc := tracker.NewForgeClient(fc)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}

	_, err := postTrackerStickyComment(context.Background(), tc, "acme/widgets", 42, "", cfg, false, printer)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "comment body is empty")
}

func TestPostTrackerStickyComment_EmptyMarker(t *testing.T) {
	fc := forge.NewFakeClient()
	tc := tracker.NewForgeClient(fc)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "", KeepHistory: true}

	_, err := postTrackerStickyComment(context.Background(), tc, "acme/widgets", 42, "hello", cfg, false, printer)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "marker is empty")
}

func TestPostTrackerStickyComment_DryRun_Create(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", DryRun: true, KeepHistory: true}

	url, err := postTrackerStickyComment(context.Background(), tc, "acme/widgets", 42, "hello", cfg, false, printer)
	require.NoError(t, err)
	assert.Empty(t, url) // dry run returns empty URL

	// No comment should be created.
	comments, err := tc.ListComments(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestPostTrackerStickyComment_DryRun_Update(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}
	ctx := context.Background()

	// Create the initial comment (not dry run).
	_, err := postTrackerStickyComment(ctx, tc, "acme/widgets", 42, "first", cfg, false, printer)
	require.NoError(t, err)

	// Dry run update should not modify the comment.
	cfg.DryRun = true
	url, err := postTrackerStickyComment(ctx, tc, "acme/widgets", 42, "second", cfg, false, printer)
	require.NoError(t, err)
	assert.Empty(t, url)

	// Comment should still have original content.
	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "first")
	assert.NotContains(t, string(comments[0].Body), "second")
}

// --- runIssuesGet tests ---

func newFakeTrackerWithIssue(t *testing.T, project string, number int) tracker.Client {
	t.Helper()
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	// Split project into owner/repo for the forge fake.
	issue, err := fc.CreateIssue(context.Background(), "acme", "widgets", "Widget is broken", "details here", "bug", "p1")
	require.NoError(t, err)
	require.Equal(t, number, issue.Number)
	// Add a comment.
	_, err = fc.CreateIssueComment(context.Background(), "acme", "widgets", number, "first comment body")
	require.NoError(t, err)
	return tracker.NewForgeClient(fc)
}

func TestRunIssuesGet(t *testing.T) {
	tc := newFakeTrackerWithIssue(t, "acme/widgets", 1)
	var buf bytes.Buffer

	cfg := &issuesGetConfig{
		project:    "acme/widgets",
		number:     1,
		testClient: tc,
		testWriter: &buf,
	}

	err := runIssuesGet(context.Background(), cfg)
	require.NoError(t, err)

	var result issueGetResult
	err = json.Unmarshal(buf.Bytes(), &result)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Number)
	assert.Equal(t, "Widget is broken", result.Title)
	assert.Equal(t, "details here", result.Body)
	assert.Contains(t, result.Labels, "bug")
	assert.Contains(t, result.Labels, "p1")
	require.Len(t, result.Comments, 1)
	assert.Equal(t, "first comment body", result.Comments[0].Body)
	assert.Equal(t, "bot", result.Comments[0].Author)
}

func TestRunIssuesGet_NoComments(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	_, err := fc.CreateIssue(context.Background(), "acme", "widgets", "No comments yet", "body text")
	require.NoError(t, err)
	tc := tracker.NewForgeClient(fc)

	var buf bytes.Buffer
	cfg := &issuesGetConfig{
		project:    "acme/widgets",
		number:     1,
		testClient: tc,
		testWriter: &buf,
	}

	err = runIssuesGet(context.Background(), cfg)
	require.NoError(t, err)

	var result issueGetResult
	err = json.Unmarshal(buf.Bytes(), &result)
	require.NoError(t, err)

	assert.Equal(t, "No comments yet", result.Title)
	assert.Empty(t, result.Comments)
}

func TestRunIssuesGet_InvalidNumber(t *testing.T) {
	fc := forge.NewFakeClient()
	tc := tracker.NewForgeClient(fc)

	for _, n := range []int{0, -1, -100} {
		cfg := &issuesGetConfig{
			project:    "acme/widgets",
			number:     n,
			testClient: tc,
			testWriter: io.Discard,
		}

		err := runIssuesGet(context.Background(), cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--number must be a positive integer")
	}
}

func TestRunIssuesGet_NilLabelsOutputAsEmptyArray(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	// Create an issue without any labels — Labels will be nil.
	_, err := fc.CreateIssue(context.Background(), "acme", "widgets", "No labels", "body text")
	require.NoError(t, err)
	tc := tracker.NewForgeClient(fc)

	var buf bytes.Buffer
	cfg := &issuesGetConfig{
		project:    "acme/widgets",
		number:     1,
		testClient: tc,
		testWriter: &buf,
	}

	err = runIssuesGet(context.Background(), cfg)
	require.NoError(t, err)

	// Verify labels serializes as [] not null.
	var raw map[string]json.RawMessage
	err = json.Unmarshal(buf.Bytes(), &raw)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(raw["labels"]), "nil labels should serialize as empty JSON array, not null")
}

func TestRunIssuesGet_IssueNotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	tc := tracker.NewForgeClient(fc)

	cfg := &issuesGetConfig{
		project:    "acme/widgets",
		number:     999,
		testClient: tc,
		testWriter: io.Discard,
	}

	err := runIssuesGet(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting issue")
}

// --- runIssuesPostComment tests ---

func TestRunIssuesPostComment_Create(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "automated comment body",
	}

	err := runIssuesPostComment(context.Background(), cfg)
	require.NoError(t, err)

	// Verify the comment was created.
	comments, err := tc.ListComments(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "<!-- test:agent -->")
	assert.Contains(t, string(comments[0].Body), "automated comment body")
}

func TestRunIssuesPostComment_Update(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	ctx := context.Background()

	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "first run",
	}

	// First run creates.
	err := runIssuesPostComment(ctx, cfg)
	require.NoError(t, err)

	// Second run updates.
	cfg.testBody = "second run"
	err = runIssuesPostComment(ctx, cfg)
	require.NoError(t, err)

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "second run")
}

func TestRunIssuesPostComment_InvalidNumber(t *testing.T) {
	cfg := &issuesPostCommentConfig{
		number:      0,
		marker:      "<!-- test -->",
		testPrinter: ui.New(io.Discard),
		testBody:    "body",
	}

	err := runIssuesPostComment(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--number must be a positive integer")
}

func TestRunIssuesPostComment_EmptyMarker(t *testing.T) {
	cfg := &issuesPostCommentConfig{
		number:      1,
		marker:      "   ",
		testPrinter: ui.New(io.Discard),
		testBody:    "body",
	}

	err := runIssuesPostComment(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--marker must not be empty")
}

func TestRunIssuesPostComment_DryRun(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		dryRun:      true,
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "dry run body",
	}

	err := runIssuesPostComment(context.Background(), cfg)
	require.NoError(t, err)

	// No comment should be created in dry-run mode.
	comments, err := tc.ListComments(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestRunIssuesPostComment_FromFile(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	// Write body to a temp file.
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "body.txt")
	err := os.WriteFile(bodyFile, []byte("comment from file"), 0o644)
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		result:      bodyFile,
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
	}

	err = runIssuesPostComment(context.Background(), cfg)
	require.NoError(t, err)

	comments, err := tc.ListComments(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "comment from file")
}

func TestRunIssuesPostComment_JiraCapsStickyMaxSize(t *testing.T) {
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	ctx := context.Background()

	// Under jira.MaxMarkdownBytes individually (so MarkdownToADF accepts
	// it on the first run), but large enough that the second-run body —
	// "second run" plus a <details> wrapper around this history — exceeds
	// jira.MaxMarkdownBytes, forcing the Jira-aware MaxSize cap to trim
	// the history rather than assembling a body that would fail once it
	// reaches Jira's MarkdownToADF.
	firstBody := strings.Repeat("a", 32700)

	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      "<!-- test:agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    firstBody,
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	cfg.testBody = "second run"
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)

	// The collapsed ~32 KB history plus "second run" and its <details>
	// wrapper exceeds jira.MaxMarkdownBytes, so the Jira path must
	// trim the history rather than assembling a body that would fail
	// once it reaches Jira's MarkdownToADF.
	assert.LessOrEqual(t, len(comments[0].Body), jira.MaxMarkdownBytes)
	assert.Contains(t, string(comments[0].Body), "second run")
}

func TestRunIssuesPostComment_NonJiraKeepsDefaultStickyMaxSize(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	ctx := context.Background()

	firstBody := strings.Repeat("a", 40000)

	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    firstBody,
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	cfg.testBody = "second run"
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)

	// Non-Jira trackers keep sticky's default 65000-byte cap, so the same
	// accumulated body (well under 65000) keeps its history intact.
	assert.Contains(t, string(comments[0].Body), "Previous run")
}

func TestRunIssuesPostComment_JiraAcceptsMarkerWithSpecialChars(t *testing.T) {
	// Jira now stores markers in comment entity properties instead of
	// the visible body, so marker character restrictions no longer
	// apply — characters that Jira's ADF round-trip would escape are
	// fine in a property value.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      "<!-- fullsend:post_review -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "body",
	}

	err = runIssuesPostComment(context.Background(), cfg)
	require.NoError(t, err)
}

func TestRunIssuesPostComment_JiraRoundTripUpdatesInPlace(t *testing.T) {
	// Drives the Jira property-based sticky comment path twice through
	// tracker.JiraClient (via NewFakeJiraClientWithFake) so comment
	// bodies round-trip through real jira.MarkdownToADF/ADFToMarkdown.
	// The marker is stored in a comment entity property, not in the
	// visible ADF body, and must be matched via property on the second
	// run to update in place instead of flooding a new comment.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	ctx := context.Background()

	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      "<!-- fullsend:triage-agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "first run",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	cfg.testBody = "second run"
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1, "second run should update the existing comment in place, not flood a new one")
	assert.Contains(t, string(comments[0].Body), "second run")
	assert.Contains(t, string(comments[0].Body), "Previous run")
}

func TestRunIssuesPostComment_JiraMarkerNotInVisibleBody(t *testing.T) {
	// The marker must be stored only in the comment entity property,
	// not in the visible ADF body that Jira renders to users.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	ctx := context.Background()

	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      "<!-- fullsend:triage-agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "visible content only",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	// The visible body must contain the content but NOT the marker.
	assert.Contains(t, string(comments[0].Body), "visible content only")
	assert.NotContains(t, string(comments[0].Body), "<!-- fullsend:triage-agent -->")
}

func TestRunIssuesPostComment_JiraLegacyMigration(t *testing.T) {
	// A legacy Jira comment has the marker embedded in the visible ADF
	// body (old behavior). On the next run, the property-based path
	// should find it via body-text fallback, set the property, and
	// strip the marker from the body on update.
	tc, fc, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	ctx := context.Background()

	marker := "<!-- fullsend:triage-agent -->"

	// Simulate a legacy comment: marker embedded in body, no property.
	legacyBody := marker + "\nlegacy content"
	_, createErr := fc.CreateComment(ctx, "PROJ-42", legacyBody)
	require.NoError(t, createErr)

	// Run post-comment, which should find the legacy comment via body
	// fallback, update it, and set the property.
	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      marker,
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "updated via property",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1, "should update the legacy comment in place")
	assert.Contains(t, string(comments[0].Body), "updated via property")
	// After migration, marker should not be in the visible body.
	assert.NotContains(t, string(comments[0].Body), marker)

	// Third run: should find the now-migrated comment via property,
	// not via body, confirming property was set during migration.
	cfg.testBody = "third run"
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err = tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1, "third run should still update the same comment")
	assert.Contains(t, string(comments[0].Body), "third run")
}

func TestRunIssuesPostComment_JiraPropertyPermissionFailure(t *testing.T) {
	// When the Jira API rejects property writes (e.g. 403), the
	// update should fail with an actionable error rather than
	// silently falling back to a visible marker.
	tc, fc, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	ctx := context.Background()

	marker := "<!-- fullsend:triage-agent -->"

	// First run succeeds (no property error on create).
	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      marker,
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "first run",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	// Now simulate a property write failure for the update path.
	fc.PropertyError = fmt.Errorf("set comment property: %w", forge.ErrForbidden)

	cfg.testBody = "second run"
	err = runIssuesPostComment(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sticky marker property")
}

func TestPostJiraStickyComment_DryRun_Create(t *testing.T) {
	// Dry-run create should not create a comment.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", DryRun: true, KeepHistory: true}

	url, err := postJiraStickyComment(context.Background(), tc, "PROJ", 42, "hello", cfg, false, printer)
	require.NoError(t, err)
	assert.Empty(t, url)

	// No comment should be created.
	comments, err := tc.ListComments(context.Background(), "PROJ", 42)
	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestPostJiraStickyComment_DryRun_Update(t *testing.T) {
	// Dry-run update should not modify the existing comment.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}
	ctx := context.Background()

	// Create the initial comment (not dry run).
	_, err = postJiraStickyComment(ctx, tc, "PROJ", 42, "first", cfg, false, printer)
	require.NoError(t, err)

	// Dry run update should not modify the comment.
	cfg.DryRun = true
	url, err := postJiraStickyComment(ctx, tc, "PROJ", 42, "second", cfg, false, printer)
	require.NoError(t, err)
	assert.Empty(t, url)

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.NotContains(t, string(comments[0].Body), "second")
}

func TestPostJiraStickyComment_EmptyBody(t *testing.T) {
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}

	_, err = postJiraStickyComment(context.Background(), tc, "PROJ", 42, "   ", cfg, false, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comment body is empty")
}

func TestPostJiraStickyComment_EmptyMarker(t *testing.T) {
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "  ", KeepHistory: true}

	_, err = postJiraStickyComment(context.Background(), tc, "PROJ", 42, "body", cfg, false, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marker is empty")
}

func TestRunIssuesPostComment_GitHubUnchangedByJiraPropertyFeature(t *testing.T) {
	// Verify that GitHub/GitLab comments still use the body-embedded
	// marker path — the Jira property feature must not affect other
	// backends.
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	ctx := context.Background()

	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "github body",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	// GitHub path embeds marker in body (HTML comment, invisible).
	assert.Contains(t, string(comments[0].Body), "<!-- test:agent -->")
	assert.Contains(t, string(comments[0].Body), "github body")
}

// --- resolveTracker tests ---
//
// fullsend-ai/fullsend#5991 specifies --tracker is required unless a
// default is supplied via config.

func TestResolveTracker_FlagOverridesConfig(t *testing.T) {
	reader, err := config.ParsePerRepoConfig([]byte("tracker: jira\n"))
	require.NoError(t, err)

	got, err := resolveTracker(trackerGitHub, "", reader)
	require.NoError(t, err)
	assert.Equal(t, trackerGitHub, got)
}

func TestResolveTracker_FallsBackToConfigReader(t *testing.T) {
	reader, err := config.ParsePerRepoConfig([]byte("tracker: jira\n"))
	require.NoError(t, err)

	got, err := resolveTracker("", "", reader)
	require.NoError(t, err)
	assert.Equal(t, trackerJira, got)
}

func TestResolveTracker_FallsBackToFullsendDirConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("tracker: gitlab\n"), 0o644))

	got, err := resolveTracker("", dir, nil)
	require.NoError(t, err)
	assert.Equal(t, trackerGitLab, got)
}

func TestResolveTracker_NormalizesCase(t *testing.T) {
	for _, input := range []string{"GITHUB", "GitHub", "Github", "JIRA", "Jira", "GITLAB", "GitLab"} {
		got, err := resolveTracker(input, "", nil)
		require.NoError(t, err, "resolveTracker(%q) should succeed", input)
		assert.Equal(t, strings.ToLower(input), got, "resolveTracker(%q) should normalize to lowercase", input)
	}
}

func TestResolveTracker_RejectsUnknownTracker(t *testing.T) {
	_, err := resolveTracker("servicenow", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported --tracker value")
	assert.Contains(t, err.Error(), "servicenow")
}

func TestResolveTracker_NoFlagNoConfig_Errors(t *testing.T) {
	_, err := resolveTracker("", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tracker is required")
}

func TestResolveTracker_FullsendDirWithoutTrackerSet_Errors(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("runtime: claude\n"), 0o644))

	_, err := resolveTracker("", dir, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tracker is required")
}

// --- resolveKeepHistory tests ---
//
// Mirrors the resolveTracker test suite. resolveKeepHistory resolves
// the keep_history setting from: (1) explicit flag, (2) config
// reader, (3) fullsend-dir config.yaml, (4) default true.

func TestResolveKeepHistory_FlagOverridesConfig(t *testing.T) {
	reader, err := config.ParsePerRepoConfig([]byte("keep_history: true\n"))
	require.NoError(t, err)

	flagVal := false
	got, err := resolveKeepHistory(&flagVal, "", reader)
	require.NoError(t, err)
	assert.False(t, got, "explicit flag=false should override config=true")
}

func TestResolveKeepHistory_FallsBackToConfigReader(t *testing.T) {
	reader, err := config.ParsePerRepoConfig([]byte("keep_history: false\n"))
	require.NoError(t, err)

	got, err := resolveKeepHistory(nil, "", reader)
	require.NoError(t, err)
	assert.False(t, got, "nil flag should fall back to config reader value")
}

func TestResolveKeepHistory_FallsBackToFullsendDirConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("keep_history: false\n"), 0o644))

	got, err := resolveKeepHistory(nil, dir, nil)
	require.NoError(t, err)
	assert.False(t, got, "nil flag + nil reader should fall back to fullsend-dir config")
}

func TestResolveKeepHistory_NilEverythingDefaultsTrue(t *testing.T) {
	got, err := resolveKeepHistory(nil, "", nil)
	require.NoError(t, err)
	assert.True(t, got, "nil flag + no config + no fullsend-dir should default to true")
}

func TestResolveKeepHistory_ConfigLoadErrorReturnsTrueWithError(t *testing.T) {
	// Point at a directory with an invalid config.yaml to trigger a load error.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(":\tinvalid yaml\n"), 0o644))

	got, err := resolveKeepHistory(nil, dir, nil)
	require.Error(t, err, "should propagate config load error")
	assert.True(t, got, "should default to true on config load error")
}

// --- config-default --tracker integration tests ---

func TestRunIssuesPostComment_TrackerFromConfig(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	reader, err := config.ParsePerRepoConfig([]byte("tracker: github\n"))
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		project:          "acme/widgets",
		number:           42,
		marker:           "<!-- test:agent -->",
		testClient:       tc,
		testConfigReader: reader,
		testPrinter:      ui.New(io.Discard),
		testBody:         "from config default",
	}

	err = runIssuesPostComment(context.Background(), cfg)
	require.NoError(t, err)

	comments, err := tc.ListComments(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "from config default")
}

func TestRunIssuesPostComment_TrackerFlagOverridesConfig(t *testing.T) {
	// --tracker jira should win over a "github" config default and
	// route through the Jira property-based path, proving the flag
	// takes priority over the config value.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)

	reader, err := config.ParsePerRepoConfig([]byte("tracker: github\n"))
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		trackerName:      trackerJira,
		project:          "PROJ",
		number:           42,
		marker:           "<!-- fullsend:triage -->",
		testClient:       tc,
		testConfigReader: reader,
		testPrinter:      ui.New(io.Discard),
		testBody:         "body",
	}

	err = runIssuesPostComment(context.Background(), cfg)
	require.NoError(t, err)

	comments, err := tc.ListComments(context.Background(), "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	// Marker should NOT be in visible body (Jira property path).
	assert.NotContains(t, string(comments[0].Body), "<!-- fullsend:triage -->")
	assert.Contains(t, string(comments[0].Body), "body")
}

func TestRunIssuesPostComment_NoTrackerNoConfig_Errors(t *testing.T) {
	cfg := &issuesPostCommentConfig{
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		testPrinter: ui.New(io.Discard),
		testBody:    "body",
	}

	err := runIssuesPostComment(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tracker is required")
}

// --- cobra-level: --tracker is no longer unconditionally required ---

func TestIssuesGetCmd_TrackerNotRequired(t *testing.T) {
	cmd := newIssuesGetCmd()
	cmd.SetArgs([]string{"--project", "acme/widgets", "--number", "1"})
	err := cmd.Execute()
	// No --tracker and no config default available: this should reach
	// RunE and fail there via resolveTracker, not at cobra's
	// required-flag validation stage.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), `required flag(s) "tracker"`)
	assert.Contains(t, err.Error(), "--tracker is required")
}

func TestIssuesPostCommentCmd_TrackerNotRequired(t *testing.T) {
	cmd := newIssuesPostCommentCmd()
	cmd.SetArgs([]string{"--project", "acme/widgets", "--number", "1", "--marker", "<!-- test -->"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), `required flag(s) "tracker"`)
	assert.Contains(t, err.Error(), "--tracker is required")
}

func TestRunIssuesPostComment_OnlyIfExists_SkipsCreate(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	cfg := &issuesPostCommentConfig{
		trackerName:  trackerGitHub,
		project:      "acme/widgets",
		number:       42,
		marker:       "<!-- test:agent -->",
		onlyIfExists: true,
		testClient:   tc,
		testPrinter:  ui.New(io.Discard),
		testBody:     "all clear",
	}

	require.NoError(t, runIssuesPostComment(context.Background(), cfg))

	comments, err := tc.ListComments(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	assert.Empty(t, comments, "only-if-exists must not create a first comment")
}

func TestRunIssuesPostComment_OnlyIfExists_ReplacesEarlierFindings(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	ctx := context.Background()

	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "2 broken links",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	cfg.onlyIfExists = true
	cfg.testBody = "all clear"
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "all clear")
}

func TestRunIssuesPostComment_Jira_OnlyIfExists(t *testing.T) {
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	ctx := context.Background()

	cfg := &issuesPostCommentConfig{
		trackerName:  trackerJira,
		project:      "PROJ",
		number:       42,
		marker:       "<!-- test:agent -->",
		onlyIfExists: true,
		testClient:   tc,
		testPrinter:  ui.New(io.Discard),
		testBody:     "all clear",
	}
	// No earlier comment: nothing is created.
	require.NoError(t, runIssuesPostComment(ctx, cfg))
	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	assert.Empty(t, comments)

	// An earlier findings comment exists: it is replaced.
	cfg.onlyIfExists = false
	cfg.testBody = "2 broken links"
	require.NoError(t, runIssuesPostComment(ctx, cfg))
	cfg.onlyIfExists = true
	cfg.testBody = "all clear"
	require.NoError(t, runIssuesPostComment(ctx, cfg))
	comments, err = tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, string(comments[0].Body), "all clear")
}

// fastSelfLookup shortens the identity-lookup retry backoff for a test.
func fastSelfLookup(t *testing.T) {
	t.Helper()
	saved := selfLookupBackoff
	selfLookupBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { selfLookupBackoff = saved })
}

// flakySelfClient fails the identity lookup failures times, then defers to
// the wrapped ForgeClient.
type flakySelfClient struct {
	*tracker.ForgeClient
	failures int
	calls    int
}

func (c *flakySelfClient) AuthenticatedUser(ctx context.Context) (string, error) {
	c.calls++
	if c.calls <= c.failures {
		return "", errors.New("502 Bad Gateway")
	}
	return c.ForgeClient.AuthenticatedUser(ctx)
}

const testBot = "fullsend-ai-review[bot]"

// postAfterCommentBy seeds one marker comment written by author, then runs
// post-comment as testBot through tc (a *flakySelfClient wrapping fc, so the
// identity lookup can be made to fail). It returns the run's error and the
// comments afterwards.
func postAfterCommentBy(t *testing.T, author string, onlyIfExists bool, failures int) (error, []tracker.Comment, *flakySelfClient) {
	t.Helper()
	fastSelfLookup(t)
	ctx := context.Background()
	fc := forge.NewFakeClient()
	tc := &flakySelfClient{ForgeClient: tracker.NewForgeClient(fc), failures: failures}
	const marker = "<!-- test:agent -->"

	fc.AuthenticatedUser = author
	_, err := tc.CreateComment(ctx, "acme/widgets", 42, tracker.Body(marker+"\nearlier findings"))
	require.NoError(t, err)

	fc.AuthenticatedUser = testBot
	cfg := &issuesPostCommentConfig{
		trackerName:  trackerGitHub,
		project:      "acme/widgets",
		number:       42,
		marker:       marker,
		onlyIfExists: onlyIfExists,
		testClient:   tc,
		testPrinter:  ui.New(io.Discard),
		testBody:     "new result",
	}
	runErr := runIssuesPostComment(ctx, cfg)

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	return runErr, comments, tc
}

func TestRunIssuesPostComment_EditsOwnComment(t *testing.T) {
	for _, onlyIfExists := range []bool{false, true} {
		t.Run(fmt.Sprintf("only-if-exists=%v", onlyIfExists), func(t *testing.T) {
			err, comments, _ := postAfterCommentBy(t, testBot, onlyIfExists, 0)
			require.NoError(t, err)
			require.Len(t, comments, 1)
			assert.Contains(t, string(comments[0].Body), "new result")
		})
	}
}

func TestRunIssuesPostComment_IgnoresPlantedMarker(t *testing.T) {
	// Login shape is not identity: another user, a different App whose login
	// merely shares the "[bot]" suffix, the same name without it, or a prefix
	// must not match.
	for _, author := range []string{"mallory", "evil-review[bot]", "fullsend-ai-review", "x-fullsend-ai-review[bot]"} {
		t.Run(author, func(t *testing.T) {
			// Normal path: the planted comment is left alone, a new one posted.
			err, comments, _ := postAfterCommentBy(t, author, false, 0)
			require.NoError(t, err)
			require.Len(t, comments, 2)
			assert.NotContains(t, string(comments[0].Body), "new result")
			assert.Contains(t, string(comments[1].Body), "new result")

			// --only-if-exists: neither edited nor created.
			err, comments, _ = postAfterCommentBy(t, author, true, 0)
			require.NoError(t, err)
			require.Len(t, comments, 1)
			assert.NotContains(t, string(comments[0].Body), "new result")
		})
	}
}

func TestRunIssuesPostComment_TransientSelfFailureRetriesThenEdits(t *testing.T) {
	err, comments, tc := postAfterCommentBy(t, testBot, false, len(selfLookupBackoff))
	require.NoError(t, err)
	assert.Equal(t, len(selfLookupBackoff)+1, tc.calls, "every retry is used before success")
	require.Len(t, comments, 1, "a transient failure must not create a second comment")
	assert.Contains(t, string(comments[0].Body), "new result")
}

func TestRunIssuesPostComment_PersistentSelfFailureErrorsAndPostsNothing(t *testing.T) {
	for _, onlyIfExists := range []bool{false, true} {
		t.Run(fmt.Sprintf("only-if-exists=%v", onlyIfExists), func(t *testing.T) {
			err, comments, tc := postAfterCommentBy(t, testBot, onlyIfExists, 1000)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot verify which identity")
			assert.Contains(t, err.Error(), "rerun")
			assert.Contains(t, err.Error(), "502 Bad Gateway", "the error must name the cause")
			assert.Equal(t, len(selfLookupBackoff)+1, tc.calls)
			require.Len(t, comments, 1, "no comment may be created")
			assert.NotContains(t, string(comments[0].Body), "new result", "no comment may be edited")
		})
	}
}

func TestRunIssuesPostComment_EmptySelfErrors(t *testing.T) {
	// An empty login with no error is unresolved too, never marker-only.
	fastSelfLookup(t)
	ctx := context.Background()
	fc := forge.NewFakeClient()
	tc := tracker.NewForgeClient(fc)
	const marker = "<!-- test:agent -->"
	_, err := tc.CreateComment(ctx, "acme/widgets", 42, tracker.Body(marker+"\nauthorless"))
	require.NoError(t, err)

	for _, onlyIfExists := range []bool{false, true} {
		cfg := &issuesPostCommentConfig{
			trackerName:  trackerGitHub,
			project:      "acme/widgets",
			number:       42,
			marker:       marker,
			onlyIfExists: onlyIfExists,
			testClient:   tc,
			testPrinter:  ui.New(io.Discard),
			testBody:     "new result",
		}
		err := runIssuesPostComment(ctx, cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authenticated user is empty")
	}
	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.NotContains(t, string(comments[0].Body), "new result")
}

func TestRunIssuesPostComment_OnlyIfExists_SkipsPlantedCommentBeforeOwn(t *testing.T) {
	// The realistic attack order: a planted marker comment listed first, the
	// poster's own comment second. The planted one is skipped, the own one
	// updated.
	ctx := context.Background()
	fc := forge.NewFakeClient()
	tc := tracker.NewForgeClient(fc)
	const marker = "<!-- test:agent -->"

	fc.AuthenticatedUser = "mallory"
	_, err := tc.CreateComment(ctx, "acme/widgets", 42, tracker.Body(marker+"\nplanted"))
	require.NoError(t, err)
	fc.AuthenticatedUser = testBot
	_, err = tc.CreateComment(ctx, "acme/widgets", 42, tracker.Body(marker+"\n2 broken links"))
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		trackerName:  trackerGitHub,
		project:      "acme/widgets",
		number:       42,
		marker:       marker,
		onlyIfExists: true,
		testClient:   tc,
		testPrinter:  ui.New(io.Discard),
		testBody:     "all clear",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.NotContains(t, string(comments[0].Body), "all clear")
	assert.Contains(t, string(comments[1].Body), "all clear")
}

func TestResolveTrackerSelf_StopsRetryingWhenCancelled(t *testing.T) {
	saved := selfLookupBackoff
	selfLookupBackoff = []time.Duration{time.Hour, time.Hour}
	t.Cleanup(func() { selfLookupBackoff = saved })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tc := &flakySelfClient{ForgeClient: tracker.NewForgeClient(forge.NewFakeClient()), failures: 1000}

	_, err := resolveTrackerSelf(ctx, tc)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, tc.calls, "a cancelled context must not wait out the backoff")
}

// postJiraAfterCommentBy seeds one marker comment on PROJ-42 written by
// author (with the marker as a comment property, or, when legacy is set,
// embedded in the body the way pre-property comments stored it), then
// runs post-comment as tracker.FakeJiraBot.
func postJiraAfterCommentBy(t *testing.T, author jira.User, legacy bool, myselfErr error) (error, []tracker.Comment) {
	t.Helper()
	fastSelfLookup(t)
	ctx := context.Background()
	tc, fc, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	const marker = "<!-- test:agent -->"

	fc.Myself = author
	if legacy {
		_, err = tc.CreateComment(ctx, "PROJ", 42, tracker.Body(marker+"\nearlier findings"))
	} else {
		_, err = tc.CreateCommentWithMarker(ctx, "PROJ", 42, tracker.Body("earlier findings"), marker)
	}
	require.NoError(t, err)

	fc.Myself = tracker.FakeJiraBot
	fc.MyselfError = myselfErr
	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      marker,
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "new result",
	}
	runErr := runIssuesPostComment(ctx, cfg)

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	return runErr, comments
}

func TestRunIssuesPostComment_Jira_EditsOwnComment(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			err, comments := postJiraAfterCommentBy(t, tracker.FakeJiraBot, legacy, nil)
			require.NoError(t, err)
			require.Len(t, comments, 1)
			assert.Contains(t, string(comments[0].Body), "new result")
		})
	}
}

func TestRunIssuesPostComment_Jira_IgnoresPlantedMarker(t *testing.T) {
	// Another account set the marker property (or, legacy, put the marker in
	// the body). Same display name as the bot: display names are not identity.
	mallory := jira.User{AccountID: "mallory-account-id", DisplayName: tracker.FakeJiraBot.DisplayName}
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			err, comments := postJiraAfterCommentBy(t, mallory, legacy, nil)
			require.NoError(t, err)
			require.Len(t, comments, 2, "the planted comment is left alone and a new one posted")
			assert.NotContains(t, string(comments[0].Body), "new result")
			assert.Contains(t, string(comments[1].Body), "new result")
		})
	}
}

func TestRunIssuesPostComment_Jira_UnresolvableSelfErrorsAndPostsNothing(t *testing.T) {
	err, comments := postJiraAfterCommentBy(t, tracker.FakeJiraBot, false, errors.New("401 Unauthorized"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot verify which identity")
	assert.Contains(t, err.Error(), "401 Unauthorized")
	require.Len(t, comments, 1, "no comment may be created")
	assert.NotContains(t, string(comments[0].Body), "new result", "no comment may be edited")
}

func TestOwnJiraComments(t *testing.T) {
	comments := []jira.Comment{
		{ID: "1", Author: jira.User{AccountID: "bot"}},
		{ID: "2", Author: jira.User{AccountID: "mallory", DisplayName: "bot"}},
		{ID: "3"},
	}
	own := ownJiraComments(comments, "bot")
	require.Len(t, own, 1)
	assert.Equal(t, "1", own[0].ID)
	assert.Empty(t, ownJiraComments(comments, ""), "an unverified self owns nothing, not even authorless comments")
}

func TestRunIssuesPostComment_DryRunStillVerifiesIdentity(t *testing.T) {
	// --dry-run resolves the posting identity too: a dry run that cannot
	// verify it fails rather than silently previewing an edit.
	fastSelfLookup(t)
	ctx := context.Background()
	const marker = "<!-- test:agent -->"

	fc := forge.NewFakeClient()
	gh := &flakySelfClient{ForgeClient: tracker.NewForgeClient(fc), failures: 1000}
	jc, fj, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	fj.MyselfError = errors.New("401 Unauthorized")

	for _, tc := range []struct {
		name    string
		tracker string
		project string
		client  tracker.Client
	}{
		{"github", trackerGitHub, "acme/widgets", gh},
		{"jira", trackerJira, "PROJ", jc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &issuesPostCommentConfig{
				trackerName: tc.tracker,
				project:     tc.project,
				number:      42,
				marker:      marker,
				dryRun:      true,
				testClient:  tc.client,
				testPrinter: ui.New(io.Discard),
				testBody:    "preview",
			}
			err := runIssuesPostComment(ctx, cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot verify which identity")
		})
	}
}

func TestRunIssuesPostComment_Jira_OnlyIfExistsIgnoresPlantedMarker(t *testing.T) {
	// The all-clear must not land on a comment whose marker property another
	// account set, and must not create one either.
	fastSelfLookup(t)
	ctx := context.Background()
	tc, fc, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	const marker = "<!-- test:agent -->"

	fc.Myself = jira.User{AccountID: "mallory-account-id", DisplayName: tracker.FakeJiraBot.DisplayName}
	_, err = tc.CreateCommentWithMarker(ctx, "PROJ", 42, tracker.Body("planted findings"), marker)
	require.NoError(t, err)
	fc.Myself = tracker.FakeJiraBot

	cfg := &issuesPostCommentConfig{
		trackerName:  trackerJira,
		project:      "PROJ",
		number:       42,
		marker:       marker,
		onlyIfExists: true,
		testClient:   tc,
		testPrinter:  ui.New(io.Discard),
		testBody:     "all clear",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.NotContains(t, string(comments[0].Body), "all clear")
}

func TestRunIssuesPostComment_OnlyIfExistsWithoutHistoryReplacesInPlace(t *testing.T) {
	// The generated ok path passes --keep-history=false: the all-clear
	// replaces the earlier findings, and repeated clean runs leave one
	// all-clear rather than stacking identical "Previous run" blocks.
	ctx := context.Background()
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	noHistory := false
	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- test:agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "2 broken links",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	cfg.onlyIfExists = true
	cfg.keepHistory = &noHistory
	cfg.testBody = "all clear"
	for i := 0; i < 3; i++ {
		require.NoError(t, runIssuesPostComment(ctx, cfg))
	}

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	body := string(comments[0].Body)
	assert.Equal(t, 1, strings.Count(body, "all clear"), "the all-clear replaces in place; got:\n%s", body)
	assert.NotContains(t, body, "2 broken links")
	assert.NotContains(t, body, "Previous run")
}
