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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
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
	assert.Contains(t, names, "create")
	assert.Contains(t, names, "post-comment")
	assert.Contains(t, names, "link")
}

func TestNewIssuesLinkCmd_Flags(t *testing.T) {
	cmd := newIssuesLinkCmd()

	for _, name := range []string{"tracker", "from", "to", "type", "token", "fullsend-dir"} {
		require.NotNil(t, cmd.Flags().Lookup(name), "flag %q should exist", name)
	}
	for _, name := range []string{"from", "to", "type"} {
		f := cmd.Flags().Lookup(name)
		assert.Equal(t, []string{"true"}, f.Annotations[cobra.BashCompOneRequiredFlag], "flag %q should be required", name)
	}
}

func TestRunIssuesLink_Jira(t *testing.T) {
	jc, fj, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	var out bytes.Buffer
	cfg := &issuesLinkConfig{
		trackerName: trackerJira,
		from:        "PROJ-123",
		to:          "OTHER-456",
		linkType:    "Blocks",
		testClient:  jc,
		testPrinter: ui.New(&out),
	}

	require.NoError(t, runIssuesLink(context.Background(), cfg))
	require.Equal(t, []tracker.FakeJiraLink{{Type: "Blocks", InwardIssue: "PROJ-123", OutwardIssue: "OTHER-456"}}, fj.Links)
	assert.Contains(t, out.String(), "Linked PROJ-123 to OTHER-456 (Blocks)")
}

func TestRunIssuesLink_TypePassedThroughVerbatim(t *testing.T) {
	jc, fj, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	cfg := &issuesLinkConfig{
		trackerName: trackerJira,
		from:        "PROJ-1",
		to:          "PROJ-2",
		linkType:    "Problem/Incident",
		testClient:  jc,
		testPrinter: ui.New(io.Discard),
	}

	require.NoError(t, runIssuesLink(context.Background(), cfg))
	require.Len(t, fj.Links, 1)
	assert.Equal(t, "Problem/Incident", fj.Links[0].Type)
}

func TestRunIssuesLink_TypeWhitespacePreserved(t *testing.T) {
	jc, fj, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	cfg := &issuesLinkConfig{
		trackerName: trackerJira,
		from:        "PROJ-1",
		to:          "PROJ-2",
		linkType:    " Blocks ",
		testClient:  jc,
		testPrinter: ui.New(io.Discard),
	}

	require.NoError(t, runIssuesLink(context.Background(), cfg))
	require.Len(t, fj.Links, 1)
	assert.Equal(t, " Blocks ", fj.Links[0].Type)
}

func TestRunIssuesLink_InvalidKeyBeforeCredentials(t *testing.T) {
	t.Setenv("FULLSEND_JIRA_TOKEN", "")
	cfg := &issuesLinkConfig{
		trackerName: trackerJira,
		from:        "bogus",
		to:          "PROJ-2",
		linkType:    "Blocks",
		testPrinter: ui.New(io.Discard),
	}

	err := runIssuesLink(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --from")
}

func TestRunIssuesLink_ClientWithoutLinker(t *testing.T) {
	cfg := &issuesLinkConfig{
		trackerName: trackerJira,
		from:        "PROJ-1",
		to:          "PROJ-2",
		linkType:    "Blocks",
		testClient:  nonLinkerClient{},
		testPrinter: ui.New(io.Discard),
	}

	err := runIssuesLink(context.Background(), cfg)
	require.Error(t, err)
	assert.True(t, tracker.IsNotSupported(err))
	assert.Contains(t, err.Error(), "does not support typed issue links")
}

// nonLinkerClient is a tracker.Client that does not implement tracker.Linker.
type nonLinkerClient struct{ tracker.Client }

func TestRunIssuesLink_EmptyFlags(t *testing.T) {
	tests := []struct {
		name     string
		from     string
		to       string
		linkType string
		wantErr  string
	}{
		{name: "empty from", from: " ", to: "PROJ-2", linkType: "Blocks", wantErr: "--from must not be empty"},
		{name: "empty to", from: "PROJ-1", to: "", linkType: "Blocks", wantErr: "--to must not be empty"},
		{name: "empty type", from: "PROJ-1", to: "PROJ-2", linkType: "  ", wantErr: "--type must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jc, fj, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
			require.NoError(t, err)
			cfg := &issuesLinkConfig{
				trackerName: trackerJira,
				from:        tt.from,
				to:          tt.to,
				linkType:    tt.linkType,
				testClient:  jc,
				testPrinter: ui.New(io.Discard),
			}
			err = runIssuesLink(context.Background(), cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, fj.Links)
		})
	}
}

func TestRunIssuesLink_MalformedKeys(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		to      string
		wantErr string
	}{
		{name: "from without number", from: "PROJ", to: "PROJ-2", wantErr: "invalid --from"},
		{name: "from bare number", from: "123", to: "PROJ-2", wantErr: "invalid --from"},
		{name: "to non-numeric", from: "PROJ-1", to: "PROJ-abc", wantErr: "invalid --to"},
		{name: "to zero", from: "PROJ-1", to: "PROJ-0", wantErr: "invalid --to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jc, fj, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
			require.NoError(t, err)
			cfg := &issuesLinkConfig{
				trackerName: trackerJira,
				from:        tt.from,
				to:          tt.to,
				linkType:    "Blocks",
				testClient:  jc,
				testPrinter: ui.New(io.Discard),
			}
			err = runIssuesLink(context.Background(), cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, fj.Links)
		})
	}
}

func TestRunIssuesLink_UnsupportedTracker(t *testing.T) {
	for _, name := range []string{trackerGitHub, trackerGitLab} {
		t.Run(name, func(t *testing.T) {
			cfg := &issuesLinkConfig{
				trackerName: name,
				from:        "acme/widgets#1",
				to:          "acme/widgets#2",
				linkType:    "Blocks",
				testClient:  tracker.NewForgeClient(forge.NewFakeClient()),
				testPrinter: ui.New(io.Discard),
			}
			err := runIssuesLink(context.Background(), cfg)
			require.Error(t, err)
			assert.ErrorIs(t, err, tracker.ErrNotSupported)
			assert.Contains(t, err.Error(), "--tracker "+name)
		})
	}
}

func TestRunIssuesLink_UnsupportedTrackerWithoutCredentials(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	for _, name := range []string{trackerGitHub, trackerGitLab} {
		t.Run(name, func(t *testing.T) {
			cfg := &issuesLinkConfig{
				trackerName: name,
				from:        "PROJ-1",
				to:          "PROJ-2",
				linkType:    "Blocks",
				testPrinter: ui.New(io.Discard),
			}
			err := runIssuesLink(context.Background(), cfg)
			require.Error(t, err)
			assert.ErrorIs(t, err, tracker.ErrNotSupported)
			assert.Contains(t, err.Error(), "--tracker "+name)
		})
	}
}

func TestRunIssuesLink_TrackerRequired(t *testing.T) {
	cfg := &issuesLinkConfig{
		from:        "PROJ-1",
		to:          "PROJ-2",
		linkType:    "Blocks",
		testPrinter: ui.New(io.Discard),
	}
	err := runIssuesLink(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tracker is required")
}

func TestRunIssuesLink_LinkError(t *testing.T) {
	jc, fj, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	fj.LinkError = errors.New("jira api: 400 No issue link type with name 'Nope' found.")
	cfg := &issuesLinkConfig{
		trackerName: trackerJira,
		from:        "PROJ-1",
		to:          "PROJ-2",
		linkType:    "Nope",
		testClient:  jc,
		testPrinter: ui.New(io.Discard),
	}

	err = runIssuesLink(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linking issues")
	assert.Contains(t, err.Error(), "No issue link type")
}

func TestParseIssueKey(t *testing.T) {
	tests := []struct {
		key         string
		wantProject string
		wantNumber  int
		wantErr     bool
	}{
		{key: "PROJ-123", wantProject: "PROJ", wantNumber: 123},
		{key: "AB2-7", wantProject: "AB2", wantNumber: 7},
		{key: "PROJ", wantErr: true},
		{key: "-123", wantErr: true},
		{key: "PROJ-", wantErr: true},
		{key: "PROJ-x1", wantErr: true},
		{key: "PROJ--1", wantErr: true},
		{key: "PROJ-+1", wantErr: true},
		{key: "PROJ-1-2", wantErr: true},
		{key: "PROJ-0", wantErr: true},
		{key: "PROJ-99999999999999999999", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			project, number, err := parseIssueKey(tt.key)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantProject, project)
			assert.Equal(t, tt.wantNumber, number)
		})
	}
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

	for _, name := range []string{"token", "fullsend-dir", "fields"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}
}

// TestIssuesCmds_NoLegacyJiraFlags checks that Jira connection settings
// are no longer accepted on the command line: they come only from
// tracker.jira in config.yaml.
func TestIssuesCmds_NoLegacyJiraFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{newIssuesGetCmd(), newIssuesCreateCmd(), newIssuesPostCommentCmd(), newIssuesLinkCmd()} {
		for _, name := range []string{"jira-url", "jira-email"} {
			assert.Nil(t, cmd.Flags().Lookup(name), "%s: flag %q should no longer exist", cmd.Name(), name)
		}
	}
}

func TestRunIssuesGet_JiraProjectDefaultsFromConfig(t *testing.T) {
	var buf bytes.Buffer
	cfg := &issuesGetConfig{
		trackerName:      trackerJira,
		number:           5,
		testClient:       newJiraTrackerWithTypedIssue(t),
		testWriter:       &buf,
		testConfigReader: parseTestConfig(t, testJiraConfig),
	}
	require.NoError(t, runIssuesGet(context.Background(), cfg))

	var result issueGetResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result))
	assert.Equal(t, "https://acme.atlassian.net/browse/PROJ-5", result.URL, "the issue is read from tracker.jira.project")
}

func TestRunIssuesGet_ProjectRequiredForForge(t *testing.T) {
	cfg := &issuesGetConfig{
		trackerName: trackerGitHub,
		number:      1,
		testClient:  newFakeTrackerWithIssue(t, "acme/widgets", 1),
		testWriter:  &bytes.Buffer{},
	}
	err := runIssuesGet(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project must not be empty for --tracker github")
}

func TestRunIssuesGet_JiraWithoutConfig(t *testing.T) {
	t.Setenv("FULLSEND_JIRA_TOKEN", "jira-token")
	cfg := &issuesGetConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      1,
		testWriter:  &bytes.Buffer{},
	}
	err := runIssuesGet(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.jira.base_url")
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

func TestPostTrackerStickyComment_SecondRunAppends(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}
	ctx := context.Background()

	// First post creates the comment.
	_, err := postTrackerStickyComment(ctx, tc, "acme/widgets", 42, "first run", cfg, false, printer)
	require.NoError(t, err)

	// Second run adds a new comment at the end of the thread.
	_, err = postTrackerStickyComment(ctx, tc, "acme/widgets", 42, "second run", cfg, false, printer)
	require.NoError(t, err)

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 2, "a second run must add a new comment")
	assert.Contains(t, string(comments[0].Body), "first run")
	assert.NotContains(t, string(comments[0].Body), "second run", "the earlier comment is left unchanged")
	assert.Contains(t, string(comments[1].Body), "second run")
	assert.Contains(t, string(comments[1].Body), "<!-- test -->")
	assert.NotContains(t, string(comments[1].Body), "Previous run")
}

func TestPostTrackerStickyComment_NeutralizesMarkersInBody(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)

	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- fullsend:triage-agent -->", KeepHistory: true}
	ctx := context.Background()

	body := "result <!-- fullsend:agent-status:999 --> <!--FULLSEND\t:review-agent--> already &lt;!-- fullsend:x -->"
	for range 3 {
		_, err := postTrackerStickyComment(ctx, tc, "acme/widgets", 42, body, cfg, false, printer)
		require.NoError(t, err)
	}

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 3, "every run adds its own comment")

	for i, c := range comments {
		got := string(c.Body)
		assert.True(t, strings.HasPrefix(got, cfg.Marker+"\n"), "comment %d: marker at the start", i)
		assert.Equal(t, 1, strings.Count(got, cfg.Marker), "comment %d: exactly one runner marker", i)
		assert.Equal(t, 1, strings.Count(got, "fullsend:triage-agent"), "comment %d: no escaped copy of the runner's marker", i)
		assert.Equal(t, 1, strings.Count(strings.ToLower(got), "<!-- fullsend:"), "comment %d: only the runner's marker is live", i)
		assert.NotContains(t, got, "<!--FULLSEND")
		assert.Equal(t, 1, strings.Count(got, "&lt;!-- fullsend:agent-status:999 -->"), "comment %d: agent marker escaped once", i)
		assert.Equal(t, 1, strings.Count(got, "&lt;!--FULLSEND\t:review-agent-->"), "comment %d", i)
		assert.Equal(t, 1, strings.Count(got, "already &lt;!-- fullsend:x -->"), "comment %d: pre-escaped text is unchanged", i)
		assert.NotContains(t, got, "&amp;lt;")
	}
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

func TestPostTrackerStickyComment_DryRun_WithExistingComment(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}
	ctx := context.Background()

	// Create the initial comment (not dry run).
	_, err := postTrackerStickyComment(ctx, tc, "acme/widgets", 42, "first", cfg, false, printer)
	require.NoError(t, err)

	// A dry run posts nothing, so the existing comment is unchanged.
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

// newJiraTrackerWithTypedIssue returns a Jira-backed tracker.Client with
// PROJ-5, a Bug carrying one string and one object custom field.
func newJiraTrackerWithTypedIssue(t *testing.T) tracker.Client {
	t.Helper()
	tc, fc, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	fc.Issues = map[string]*jira.Issue{
		"PROJ-5": {
			Key: "PROJ-5",
			Fields: jira.IssueFields{
				Summary:   "Typed issue",
				IssueType: &jira.IssueType{ID: "10001", Name: "Bug"},
				CustomFields: map[string]json.RawMessage{
					"customfield_10875": json.RawMessage(`"https://github.com/acme/widgets/pull/7"`),
					"customfield_12345": json.RawMessage(`{"value":"High","id":"3"}`),
					"customfield_20000": json.RawMessage(`null`),
				},
			},
		},
	}
	return tc
}

func TestRunIssuesGet_JiraIssueTypeWithoutFields(t *testing.T) {
	var buf bytes.Buffer
	cfg := &issuesGetConfig{
		project:    "PROJ",
		number:     5,
		testClient: newJiraTrackerWithTypedIssue(t),
		testWriter: &buf,
	}
	require.NoError(t, runIssuesGet(context.Background(), cfg))

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(buf.Bytes(), &raw))
	assert.JSONEq(t, `"Bug"`, string(raw["issue_type"]))
	assert.NotContains(t, raw, "custom_fields", "custom_fields is opt-in via --fields")
}

func TestRunIssuesGet_JiraCustomFields(t *testing.T) {
	var buf bytes.Buffer
	cfg := &issuesGetConfig{
		project:    "PROJ",
		number:     5,
		fields:     []string{" customfield_10875", "customfield_12345", "customfield_10875", "", "customfield_20000", "customfield_30000"},
		testClient: newJiraTrackerWithTypedIssue(t),
		testWriter: &buf,
	}
	require.NoError(t, runIssuesGet(context.Background(), cfg))

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(buf.Bytes(), &raw))
	assert.JSONEq(t, `{
		"customfield_10875": "https://github.com/acme/widgets/pull/7",
		"customfield_12345": {"value": "High", "id": "3"},
		"customfield_20000": null,
		"customfield_30000": null
	}`, string(raw["custom_fields"]), "only requested fields; unset or absent ones are null")
}

func TestRunIssuesGet_FieldsIgnoredForNonJira(t *testing.T) {
	var buf, errBuf bytes.Buffer
	cfg := &issuesGetConfig{
		project:       "acme/widgets",
		number:        1,
		fields:        []string{"customfield_10875"},
		testClient:    newFakeTrackerWithIssue(t, "acme/widgets", 1),
		testWriter:    &buf,
		testErrWriter: &errBuf,
	}
	require.NoError(t, runIssuesGet(context.Background(), cfg))

	assert.Contains(t, errBuf.String(), "--fields is only supported for --tracker jira")
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(buf.Bytes(), &raw))
	assert.NotContains(t, raw, "custom_fields")
	assert.NotContains(t, raw, "issue_type", "GitHub output is unchanged")
}

func TestRunIssuesGet_InvalidFields(t *testing.T) {
	for _, f := range []string{"priority", "customfield_", "customfield_12a", "customfield_10875,x"} {
		cfg := &issuesGetConfig{
			project:    "PROJ",
			number:     5,
			fields:     []string{f},
			testClient: newJiraTrackerWithTypedIssue(t),
			testWriter: io.Discard,
		}
		err := runIssuesGet(context.Background(), cfg)
		require.Error(t, err, f)
		assert.Contains(t, err.Error(), "invalid --fields value")
	}
}

func TestIssuesGetCmd_FieldsFlagParsesCommaList(t *testing.T) {
	cmd := newIssuesGetCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--fields", "customfield_1,customfield_2", "--fields", "customfield_3"}))
	got, err := cmd.Flags().GetStringSlice("fields")
	require.NoError(t, err)
	assert.Equal(t, []string{"customfield_1", "customfield_2", "customfield_3"}, got)
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

func TestRunIssuesPostComment_SecondRunAppends(t *testing.T) {
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

	// Second run adds a new comment at the end of the thread.
	cfg.testBody = "second run"
	err = runIssuesPostComment(ctx, cfg)
	require.NoError(t, err)

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Contains(t, string(comments[0].Body), "first run")
	assert.NotContains(t, string(comments[0].Body), "second run", "the earlier comment is left unchanged")
	assert.Contains(t, string(comments[1].Body), "second run")
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

func TestRunIssuesPostComment_JiraProjectDefaultsFromConfig(t *testing.T) {
	tc, fake, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		trackerName:      trackerJira,
		number:           42,
		marker:           "<!-- fullsend:triage-agent -->",
		testClient:       tc,
		testPrinter:      ui.New(io.Discard),
		testBody:         "body",
		testConfigReader: parseTestConfig(t, testJiraConfig),
	}
	require.NoError(t, runIssuesPostComment(context.Background(), cfg))
	assert.NotEmpty(t, fake.Comments["PROJ-42"], "the comment is posted to tracker.jira.project")
}

func TestRunIssuesPostComment_JiraProjectWithoutConfig(t *testing.T) {
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		number:      42,
		marker:      "<!-- fullsend:triage-agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "body",
	}
	err = runIssuesPostComment(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default Jira project is configured")
}

func TestRunIssuesPostComment_JiraSecondRunAppends(t *testing.T) {
	// Drives the Jira property-based comment path twice through
	// tracker.JiraClient (via NewFakeJiraClientWithFake) so comment
	// bodies round-trip through real jira.MarkdownToADF/ADFToMarkdown.
	// The marker is stored in a comment entity property, not in the
	// visible ADF body, and the second run appends a new comment.
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
	require.Len(t, comments, 2, "second run should append a new comment")
	assert.Contains(t, string(comments[0].Body), "first run")
	assert.NotContains(t, string(comments[0].Body), "second run", "the earlier comment is left unchanged")
	assert.Contains(t, string(comments[1].Body), "second run")
	assert.NotContains(t, string(comments[1].Body), "Previous run")
}

func TestRunIssuesPostComment_JiraTruncatesOversizedBody(t *testing.T) {
	// Pins the Jira door's create-path size cap: runIssuesPostComment
	// caps stickyCfg.MaxSize at jira.MaxMarkdownBytes, and the fake
	// client runs the real jira.MarkdownToADF, which hard-rejects input
	// over that limit — so an oversized single result must be truncated
	// before it is created instead of failing the post.
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
		testBody:    strings.Repeat("a", jira.MaxMarkdownBytes+5000),
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.LessOrEqual(t, len(string(comments[0].Body)), jira.MaxMarkdownBytes,
		"the created body must be truncated to the Jira markdown limit")
}

func TestPostTrackerStickyComment_TruncatesOversizedBodyBeforeCreate(t *testing.T) {
	// Mirrors TestPost_TruncatesOversizedBodyBeforeCreate for the
	// tracker door: a single result over the configured size limit is
	// truncated before it is created instead of failing the post.
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	tc := tracker.NewForgeClient(fc)
	printer := ui.New(io.Discard)
	ctx := context.Background()

	cfg := sticky.Config{Marker: "<!-- test -->", MaxSize: 500}
	_, err := postTrackerStickyComment(ctx, tc, "acme/widgets", 42, strings.Repeat("a", 800), cfg, false, printer)
	require.NoError(t, err)

	comments, err := tc.ListComments(ctx, "acme/widgets", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.LessOrEqual(t, len(string(comments[0].Body)), 500, "the created body must fit the configured size")
	assert.Contains(t, string(comments[0].Body), "<!-- test -->")
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

// liveMarkerOpener matches a fullsend marker opener that would parse as a
// marker (case-insensitive, any whitespace around "fullsend").
var liveMarkerOpener = regexp.MustCompile(`(?i)<!--\s*fullsend\s*:`)

func TestRunIssuesPostComment_JiraNeutralizesInjectedMarkers(t *testing.T) {
	// Agent bodies carrying fullsend marker syntax (literal, and the
	// "&lt;" form the GitHub path writes, which Markdown-to-ADF decodes)
	// must stay inert through the real ADF conversion in every comment
	// posted.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	ctx := context.Background()

	sibling := "<!-- fullsend:review-agent -->"
	cfg := &issuesPostCommentConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		number:      42,
		marker:      "<!-- fullsend:triage-agent -->",
		testClient:  tc,
		testPrinter: ui.New(io.Discard),
		testBody:    "first run\n\n" + sibling + "\n\n&lt;!-- fullsend:agent-status:1 -->",
	}
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.NotRegexp(t, liveMarkerOpener, string(comments[0].Body), "create left a live marker opener")

	cfg.testBody = "second run <!--FullSend:code-agent-->\n\n" + sibling
	require.NoError(t, runIssuesPostComment(ctx, cfg))

	comments, err = tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 2, "second run should append a new comment")
	body := string(comments[1].Body)
	assert.NotRegexp(t, liveMarkerOpener, body, "the new comment left a live marker opener")
	assert.Contains(t, body, "second run")
	assert.Contains(t, body, `\&lt;!-- fullsend:review-agent -->`, "neutralized text should stay visible")

	// Neither the quoted sibling marker nor the quoted status marker
	// makes the comment discoverable as something it is not.
	raw, err := tc.ListJiraComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	assert.Nil(t, tc.FindCommentByMarkerProperty(raw, sibling))
	isStatus, err := tc.IsStatusComment(ctx, "PROJ", 42, comments[1].ID)
	require.NoError(t, err)
	assert.False(t, isStatus)
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

func TestPostJiraStickyComment_DryRun_WithExistingComment(t *testing.T) {
	// A dry run posts nothing, so the existing comment is unchanged.
	tc, _, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	printer := ui.New(io.Discard)
	cfg := sticky.Config{Marker: "<!-- test -->", KeepHistory: true}
	ctx := context.Background()

	// Create the initial comment (not dry run).
	_, err = postJiraStickyComment(ctx, tc, "PROJ", 42, "first", cfg, false, printer)
	require.NoError(t, err)

	// A dry run posts nothing, so the existing comment is unchanged.
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
	reader, err := config.ParsePerRepoConfig([]byte("tracker:\n  jira: {}\n"))
	require.NoError(t, err)

	got, err := resolveTracker(trackerGitHub, "", reader)
	require.NoError(t, err)
	assert.Equal(t, trackerGitHub, got)
}

func TestResolveTracker_FallsBackToConfigReader(t *testing.T) {
	reader, err := config.ParsePerRepoConfig([]byte("tracker:\n  jira: {}\n"))
	require.NoError(t, err)

	got, err := resolveTracker("", "", reader)
	require.NoError(t, err)
	assert.Equal(t, trackerJira, got)
}

func TestResolveTracker_FallsBackToFullsendDirConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("tracker:\n  gitlab: {}\n"), 0o644))

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

	reader, err := config.ParsePerRepoConfig([]byte("tracker:\n  github: {}\n"))
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

	reader, err := config.ParsePerRepoConfig([]byte("tracker:\n  github: {}\n"))
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

func TestRunIssuesPostComment_OnlyIfExists_AppendsAllClearAfterEarlierFindings(t *testing.T) {
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
	require.Len(t, comments, 2, "the all-clear is a new comment at the end of the thread")
	assert.Contains(t, string(comments[0].Body), "2 broken links")
	assert.NotContains(t, string(comments[0].Body), "all clear", "the earlier findings are left unchanged")
	assert.Contains(t, string(comments[1].Body), "all clear")
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

	// An earlier findings comment exists: the all-clear follows it as a
	// new comment.
	cfg.onlyIfExists = false
	cfg.testBody = "2 broken links"
	require.NoError(t, runIssuesPostComment(ctx, cfg))
	cfg.onlyIfExists = true
	cfg.testBody = "all clear"
	require.NoError(t, runIssuesPostComment(ctx, cfg))
	comments, err = tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Contains(t, string(comments[0].Body), "2 broken links")
	assert.NotContains(t, string(comments[0].Body), "all clear", "the earlier findings are left unchanged")
	assert.Contains(t, string(comments[1].Body), "all clear")
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

func TestRunIssuesPostComment_AppendsAfterOwnComment(t *testing.T) {
	for _, onlyIfExists := range []bool{false, true} {
		t.Run(fmt.Sprintf("only-if-exists=%v", onlyIfExists), func(t *testing.T) {
			err, comments, _ := postAfterCommentBy(t, testBot, onlyIfExists, 0)
			require.NoError(t, err)
			require.Len(t, comments, 2, "the result is a new comment at the end of the thread")
			assert.NotContains(t, string(comments[0].Body), "new result", "the earlier comment is left unchanged")
			assert.Contains(t, string(comments[1].Body), "new result")
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

func TestRunIssuesPostComment_TransientSelfFailureRetriesThenPosts(t *testing.T) {
	// Only --only-if-exists resolves the posting identity (to gate on the
	// poster's own earlier comment); a transient failure there is retried.
	err, comments, tc := postAfterCommentBy(t, testBot, true, len(selfLookupBackoff))
	require.NoError(t, err)
	assert.Equal(t, len(selfLookupBackoff)+1, tc.calls, "every retry is used before success")
	require.Len(t, comments, 2)
	assert.Contains(t, string(comments[1].Body), "new result")
}

func TestRunIssuesPostComment_PersistentSelfFailureErrorsAndPostsNothing(t *testing.T) {
	// Only --only-if-exists resolves the posting identity, so only that
	// path fails when it cannot be verified.
	err, comments, tc := postAfterCommentBy(t, testBot, true, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot verify which identity")
	assert.Contains(t, err.Error(), "rerun")
	assert.Contains(t, err.Error(), "502 Bad Gateway", "the error must name the cause")
	assert.Equal(t, len(selfLookupBackoff)+1, tc.calls)
	require.Len(t, comments, 1, "no comment may be created")
}

func TestRunIssuesPostComment_EmptySelfErrors(t *testing.T) {
	// An empty login with no error is unresolved too, never marker-only.
	// Only --only-if-exists resolves the posting identity.
	fastSelfLookup(t)
	ctx := context.Background()
	fc := forge.NewFakeClient()
	tc := tracker.NewForgeClient(fc)
	const marker = "<!-- test:agent -->"
	_, err := tc.CreateComment(ctx, "acme/widgets", 42, tracker.Body(marker+"\nauthorless"))
	require.NoError(t, err)

	cfg := &issuesPostCommentConfig{
		trackerName:  trackerGitHub,
		project:      "acme/widgets",
		number:       42,
		marker:       marker,
		onlyIfExists: true,
		testClient:   tc,
		testPrinter:  ui.New(io.Discard),
		testBody:     "new result",
	}
	err = runIssuesPostComment(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticated user is empty")
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
	require.Len(t, comments, 3, "the all-clear is a new comment at the end of the thread")
	assert.NotContains(t, string(comments[0].Body), "all clear", "the planted comment must be untouched")
	assert.NotContains(t, string(comments[1].Body), "all clear", "the own earlier comment is left unchanged")
	assert.Contains(t, string(comments[2].Body), "all clear")
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
func postJiraAfterCommentBy(t *testing.T, author jira.User, legacy bool, myselfErr error, onlyIfExists bool) (error, []tracker.Comment) {
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
		trackerName:  trackerJira,
		project:      "PROJ",
		number:       42,
		marker:       marker,
		onlyIfExists: onlyIfExists,
		testClient:   tc,
		testPrinter:  ui.New(io.Discard),
		testBody:     "new result",
	}
	runErr := runIssuesPostComment(ctx, cfg)

	comments, err := tc.ListComments(ctx, "PROJ", 42)
	require.NoError(t, err)
	return runErr, comments
}

func TestRunIssuesPostComment_Jira_AppendsAfterOwnComment(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			err, comments := postJiraAfterCommentBy(t, tracker.FakeJiraBot, legacy, nil, false)
			require.NoError(t, err)
			require.Len(t, comments, 2, "the result is a new comment at the end of the thread")
			assert.NotContains(t, string(comments[0].Body), "new result", "the earlier comment is left unchanged")
			assert.Contains(t, string(comments[1].Body), "new result")
		})
	}
}

func TestRunIssuesPostComment_Jira_IgnoresPlantedMarker(t *testing.T) {
	// Another account set the marker property (or, legacy, put the marker in
	// the body). Same display name as the bot: display names are not identity.
	mallory := jira.User{AccountID: "mallory-account-id", DisplayName: tracker.FakeJiraBot.DisplayName}
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			err, comments := postJiraAfterCommentBy(t, mallory, legacy, nil, false)
			require.NoError(t, err)
			require.Len(t, comments, 2, "the planted comment is left alone and a new one posted")
			assert.NotContains(t, string(comments[0].Body), "new result")
			assert.Contains(t, string(comments[1].Body), "new result")
		})
	}
}

func TestRunIssuesPostComment_Jira_UnresolvableSelfErrorsAndPostsNothing(t *testing.T) {
	// Only --only-if-exists resolves the posting identity, so only that
	// path fails when it cannot be verified.
	err, comments := postJiraAfterCommentBy(t, tracker.FakeJiraBot, false, errors.New("401 Unauthorized"), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot verify which identity")
	assert.Contains(t, err.Error(), "401 Unauthorized")
	require.Len(t, comments, 1, "no comment may be created")
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

func TestRunIssuesPostComment_DryRunOnlyIfExistsStillVerifiesIdentity(t *testing.T) {
	// --dry-run --only-if-exists resolves the posting identity too: a dry
	// run that cannot verify it fails rather than silently previewing a
	// post gated on the poster's own earlier comment.
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
				trackerName:  tc.tracker,
				project:      tc.project,
				number:       42,
				marker:       marker,
				dryRun:       true,
				onlyIfExists: true,
				testClient:   tc.client,
				testPrinter:  ui.New(io.Discard),
				testBody:     "preview",
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

func TestRunIssuesPostComment_OnlyIfExistsAppendsAllClear(t *testing.T) {
	// The generated ok path passes --keep-history=false, which no longer
	// has an effect: the all-clear follows the earlier findings as a new
	// comment, and repeated clean runs each add their own.
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
	require.Len(t, comments, 4, "the findings comment plus one all-clear per clean run")
	assert.Contains(t, string(comments[0].Body), "2 broken links")
	for _, c := range comments[1:] {
		assert.Contains(t, string(c.Body), "all clear")
		assert.NotContains(t, string(c.Body), "Previous run")
	}
}
