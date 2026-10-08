package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

func TestNewIssuesCreateCmd_Flags(t *testing.T) {
	cmd := newIssuesCreateCmd()

	for _, name := range []string{"tracker", "project", "type", "parent", "title", "body", "token", "jira-url", "jira-email", "fullsend-dir"} {
		require.NotNil(t, cmd.Flags().Lookup(name), "flag %q should exist", name)
	}
}

func TestIssuesCreateCmd_RequiredFlags(t *testing.T) {
	cmd := newIssuesCreateCmd()
	cmd.SetArgs([]string{"--tracker", "github"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project")
	assert.Contains(t, err.Error(), "title")
}

func TestIssuesCreateCmd_TrackerNotRequired(t *testing.T) {
	cmd := newIssuesCreateCmd()
	cmd.SetArgs([]string{"--project", "acme/widgets", "--title", "t"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), `required flag(s) "tracker"`)
	assert.Contains(t, err.Error(), "--tracker is required")
}

func decodeCreateResult(t *testing.T, buf *bytes.Buffer) issueCreateResult {
	t.Helper()
	var result issueCreateResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result))
	return result
}

func TestRunIssuesCreate_GitHub(t *testing.T) {
	fc := forge.NewFakeClient()
	var buf bytes.Buffer

	cfg := &issuesCreateConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		title:       "New widget",
		body:        "widget details",
		testClient:  tracker.NewForgeClient(fc),
		testWriter:  &buf,
	}
	require.NoError(t, runIssuesCreate(context.Background(), cfg))

	require.Len(t, fc.CreatedIssues, 1)
	assert.Equal(t, "acme", fc.CreatedIssues[0].Owner)
	assert.Equal(t, "widgets", fc.CreatedIssues[0].Repo)
	assert.Equal(t, "New widget", fc.CreatedIssues[0].Title)
	assert.Equal(t, "widget details", fc.CreatedIssues[0].Body)

	result := decodeCreateResult(t, &buf)
	assert.Equal(t, fc.CreatedIssues[0].Number, result.Number)
	assert.Equal(t, "New widget", result.Title)
	assert.Equal(t, "https://github.com/acme/widgets/issues/1", result.URL)
	assert.Empty(t, result.Key, "key is Jira only")
	assert.NotContains(t, buf.String(), `"key"`)
}

func TestRunIssuesCreate_GitLabNestedProject(t *testing.T) {
	fc := forge.NewFakeClient()
	var buf bytes.Buffer

	cfg := &issuesCreateConfig{
		trackerName: trackerGitLab,
		project:     "group/subgroup/project",
		title:       "Nested",
		testClient:  tracker.NewForgeClient(fc),
		testWriter:  &buf,
	}
	require.NoError(t, runIssuesCreate(context.Background(), cfg))

	require.Len(t, fc.CreatedIssues, 1)
	assert.Equal(t, "group/subgroup", fc.CreatedIssues[0].Owner)
	assert.Equal(t, "project", fc.CreatedIssues[0].Repo)
	assert.Empty(t, fc.CreatedIssues[0].Body)
}

func TestRunIssuesCreate_JiraSubTask(t *testing.T) {
	tc, fake, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	var buf bytes.Buffer

	cfg := &issuesCreateConfig{
		trackerName: trackerJira,
		project:     "TC",
		issueType:   "10003",
		parent:      "TC-7",
		title:       "Sub-task title",
		body:        "-",
		testStdin:   strings.NewReader("## Plan\n\n- step one\n"),
		testClient:  tc,
		testWriter:  &buf,
	}
	require.NoError(t, runIssuesCreate(context.Background(), cfg))

	require.Len(t, fake.CreatedIssues, 1)
	assert.Equal(t, jira.CreateIssueInput{
		ProjectKey:  "TC",
		IssueType:   "10003",
		ParentKey:   "TC-7",
		Summary:     "Sub-task title",
		Description: "## Plan\n\n- step one\n",
	}, fake.CreatedIssues[0])

	result := decodeCreateResult(t, &buf)
	assert.Equal(t, 1, result.Number)
	assert.Equal(t, "TC-1", result.Key)
	assert.Equal(t, "Sub-task title", result.Title)
	assert.Equal(t, "https://acme.atlassian.net/browse/TC-1", result.URL)
}

// canonicalKeyClient mimics Jira returning a canonical key whose project
// spelling differs from the requested one.
type canonicalKeyClient struct {
	tracker.Client
}

func (canonicalKeyClient) CreateIssue(_ context.Context, _, title string, _ tracker.Body, _ tracker.CreateIssueOptions) (*tracker.Issue, error) {
	return &tracker.Issue{Number: 43, Title: title, Key: "PROJ-43", URL: "https://acme.atlassian.net/browse/PROJ-43"}, nil
}

func TestRunIssuesCreate_JiraKeyFromTracker(t *testing.T) {
	var buf bytes.Buffer
	cfg := &issuesCreateConfig{
		trackerName: trackerJira,
		project:     "proj",
		issueType:   "Task",
		title:       "t",
		testClient:  canonicalKeyClient{},
		testWriter:  &buf,
	}
	require.NoError(t, runIssuesCreate(context.Background(), cfg))

	result := decodeCreateResult(t, &buf)
	assert.Equal(t, "PROJ-43", result.Key)
	assert.Equal(t, "https://acme.atlassian.net/browse/PROJ-43", result.URL)
}

func TestRunIssuesCreate_TrackerFromConfig(t *testing.T) {
	reader, err := config.ParsePerRepoConfig([]byte("tracker: jira\n"))
	require.NoError(t, err)
	tc, fake, err := tracker.NewFakeJiraClientWithFake("https://acme.atlassian.net")
	require.NoError(t, err)
	var buf bytes.Buffer

	cfg := &issuesCreateConfig{
		project:          "PROJ",
		issueType:        "Task",
		title:            "From config",
		testClient:       tc,
		testWriter:       &buf,
		testConfigReader: reader,
	}
	require.NoError(t, runIssuesCreate(context.Background(), cfg))
	require.Len(t, fake.CreatedIssues, 1)
	assert.Equal(t, "PROJ-1", decodeCreateResult(t, &buf).Key)
}

func TestRunIssuesCreate_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		cfg     issuesCreateConfig
		wantErr string
	}{
		{
			name:    "empty project",
			cfg:     issuesCreateConfig{trackerName: trackerGitHub, project: " ", title: "t"},
			wantErr: "--project must not be empty",
		},
		{
			name:    "empty title",
			cfg:     issuesCreateConfig{trackerName: trackerGitHub, project: "acme/widgets", title: "  "},
			wantErr: "--title must not be empty",
		},
		{
			name:    "unknown tracker",
			cfg:     issuesCreateConfig{trackerName: "bitbucket", project: "acme/widgets", title: "t"},
			wantErr: "unsupported --tracker value",
		},
		{
			name:    "type on github",
			cfg:     issuesCreateConfig{trackerName: trackerGitHub, project: "acme/widgets", title: "t", issueType: "10003"},
			wantErr: "--type is only supported with --tracker jira",
		},
		{
			name:    "parent on gitlab",
			cfg:     issuesCreateConfig{trackerName: trackerGitLab, project: "acme/widgets", title: "t", parent: "PROJ-1"},
			wantErr: "--parent is only supported with --tracker jira",
		},
		{
			name:    "jira without type",
			cfg:     issuesCreateConfig{trackerName: trackerJira, project: "PROJ", title: "t"},
			wantErr: "--type is required with --tracker jira",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			cfg := tt.cfg
			cfg.testClient = tracker.NewForgeClient(fc)
			cfg.testWriter = &bytes.Buffer{}
			err := runIssuesCreate(context.Background(), &cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, fc.CreatedIssues, "nothing must be created on a validation error")
		})
	}
}

func TestRunIssuesCreate_StdinTooLarge(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := &issuesCreateConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		title:       "t",
		body:        "-",
		testStdin:   strings.NewReader(strings.Repeat("a", maxBodyBytes+1)),
		testClient:  tracker.NewForgeClient(fc),
		testWriter:  &bytes.Buffer{},
	}
	err := runIssuesCreate(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading issue body")
	assert.Empty(t, fc.CreatedIssues)
}

func TestRunIssuesCreate_TrackerError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"CreateIssue": errors.New("boom")}
	cfg := &issuesCreateConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		title:       "t",
		testClient:  tracker.NewForgeClient(fc),
		testWriter:  &bytes.Buffer{},
	}
	err := runIssuesCreate(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating issue: boom")
}

func TestRunIssuesCreate_JiraMissingCredentials(t *testing.T) {
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_BASE_URL", "")
	cfg := &issuesCreateConfig{
		trackerName: trackerJira,
		project:     "PROJ",
		issueType:   "10003",
		title:       "t",
		testWriter:  &bytes.Buffer{},
	}
	require.Error(t, runIssuesCreate(context.Background(), cfg))
}
