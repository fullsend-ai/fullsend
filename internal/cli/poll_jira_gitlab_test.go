package cli

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestValidateJiraGitLabPollArgs(t *testing.T) {
	base := jiraPollGitLabFlags{
		jiraURL:     "https://acme.atlassian.net",
		jiraProject: "PROJ",
		projectPath: "group/sub/project",
		gitlabURL:   "https://gitlab.com",
		fullsendDir: ".fullsend",
	}
	tests := []struct {
		name        string
		mutate      func(*jiraPollGitLabFlags)
		env         map[string]string
		wantProject string
		wantURL     string
		wantErr     string
	}{
		{name: "nested project path is kept", wantProject: "group/sub/project", wantURL: "https://gitlab.com"},
		{
			name:        "self-managed URL keeps its path prefix",
			mutate:      func(f *jiraPollGitLabFlags) { f.gitlabURL = "https://gitlab.example.com/gitlab/" },
			wantProject: "group/sub/project",
			wantURL:     "https://gitlab.example.com/gitlab",
		},
		{
			name:        "CI_PROJECT_PATH fallback",
			mutate:      func(f *jiraPollGitLabFlags) { f.projectPath = "" },
			env:         map[string]string{"CI_PROJECT_PATH": "top/mid/leaf/project"},
			wantProject: "top/mid/leaf/project",
			wantURL:     "https://gitlab.com",
		},
		{
			name:        "JIRA_BASE_URL fallback",
			mutate:      func(f *jiraPollGitLabFlags) { f.jiraURL = "" },
			env:         map[string]string{"JIRA_BASE_URL": "https://acme.atlassian.net"},
			wantProject: "group/sub/project",
			wantURL:     "https://gitlab.com",
		},
		{name: "missing jira URL", mutate: func(f *jiraPollGitLabFlags) { f.jiraURL = "" }, wantErr: "--jira-url or JIRA_BASE_URL"},
		{name: "target-repo is GitHub only", mutate: func(f *jiraPollGitLabFlags) { f.targetRepo = "acme/platform" }, wantErr: "--target-repo names a GitHub repository"},
		{name: "missing project", mutate: func(f *jiraPollGitLabFlags) { f.projectPath = "" }, wantErr: "--project or CI_PROJECT_PATH"},
		{name: "project without namespace", mutate: func(f *jiraPollGitLabFlags) { f.projectPath = "project" }, wantErr: "full GitLab project path"},
		{name: "project with dot-dot", mutate: func(f *jiraPollGitLabFlags) { f.projectPath = "group/../project" }, wantErr: "full GitLab project path"},
		{name: "project with leading hyphen", mutate: func(f *jiraPollGitLabFlags) { f.projectPath = "-group/project" }, wantErr: "full GitLab project path"},
		{name: "project with space", mutate: func(f *jiraPollGitLabFlags) { f.projectPath = "group/my project" }, wantErr: "full GitLab project path"},
		{name: "http GitLab URL", mutate: func(f *jiraPollGitLabFlags) { f.gitlabURL = "http://gitlab.example.com" }, wantErr: "must be an https URL"},
		{name: "GitLab URL with credentials", mutate: func(f *jiraPollGitLabFlags) { f.gitlabURL = "https://user:pw@gitlab.example.com" }, wantErr: "must be an https URL"},
		{name: "GitLab URL with query", mutate: func(f *jiraPollGitLabFlags) { f.gitlabURL = "https://gitlab.example.com/?x=1" }, wantErr: "query or fragment"},
		{name: "missing jira scope", mutate: func(f *jiraPollGitLabFlags) { f.jiraProject = "" }, wantErr: "--jira-project or --jql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := base
			if tt.mutate != nil {
				tt.mutate(&f)
			}
			getenv := func(k string) string { return tt.env[k] }
			args, gitlabURL, err := validateJiraGitLabPollArgs(f, getenv)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantProject, args.targetRepo)
			assert.Equal(t, tt.wantURL, gitlabURL)
			assert.Equal(t, "https://acme.atlassian.net", args.jiraURL)
			assert.Empty(t, args.outputPath, "--output is optional for GitLab targets")
		})
	}
}

func TestPollCmd_JiraPollInvalidTargetForge(t *testing.T) {
	clearPollEnv(t)
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--input-driver", "jira-poll", "--target-forge", "bitbucket", "--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--target-forge must be")
}

// TestPollCmd_JiraPollGitLabPreflight checks the GitLab target fails closed
// on missing GitLab dispatch prerequisites before any Jira or GitLab call.
func TestPollCmd_JiraPollGitLabPreflight(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{name: "missing poller credential", wantErr: forge.SecretGitLabPollerToken},
		{
			name:    "missing pipeline ref",
			env:     map[string]string{forge.SecretGitLabPollerToken: "tok"},
			wantErr: "CI_COMMIT_REF_NAME or CI_DEFAULT_BRANCH",
		},
		{
			name:    "missing dispatch secret",
			env:     map[string]string{forge.SecretGitLabPollerToken: "tok", "CI_DEFAULT_BRANCH": "main"},
			wantErr: "FULLSEND_DISPATCH_SECRET is required",
		},
		{
			name: "child route without job token",
			env: map[string]string{
				forge.SecretGitLabPollerToken: "tok", "CI_COMMIT_REF_NAME": "main", "FULLSEND_DISPATCH_SECRET": "s",
				forge.VarGitLabDispatchRoute: forge.GitLabDispatchRouteChildPipeline,
			},
			wantErr: "requires CI_JOB_TOKEN",
		},
		{
			name:    "missing Jira credentials",
			env:     map[string]string{forge.SecretGitLabPollerToken: "tok", "CI_COMMIT_REF_NAME": "main", "FULLSEND_DISPATCH_SECRET": "s"},
			wantErr: "create Jira client",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearPollEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cmd := newPollCmd()
			cmd.SetArgs([]string{
				"--input-driver", "jira-poll",
				"--target-forge", "gitlab",
				"--jira-url", "https://acme.atlassian.net",
				"--jira-project", "PROJ",
				"--project", "group/sub/project",
				"--fullsend-dir", t.TempDir(),
			})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestPollCmd_JiraPollGitLabAuthFailure drives the GitLab target through
// client construction to the GitLab identity preflight, which runs before
// any Jira lock or checkpoint is touched.
func TestPollCmd_JiraPollGitLabAuthFailure(t *testing.T) {
	var gitlabCalls int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gitlabCalls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))

	clearPollEnv(t)
	t.Setenv("CI_SERVER_TLS_CA_FILE", caFile)
	t.Setenv(forge.SecretGitLabPollerToken, "bad-tok")
	t.Setenv("CI_COMMIT_REF_NAME", "main")
	t.Setenv("FULLSEND_DISPATCH_SECRET", "secret")
	t.Setenv("JIRA_TOKEN", "jira-tok")
	t.Setenv("JIRA_USER_EMAIL", "bot@example.com")

	cmd := newPollCmd()
	cmd.SetArgs([]string{
		"--input-driver", "jira-poll",
		"--target-forge", "gitlab",
		"--jira-url", "https://acme.atlassian.net",
		"--jira-project", "PROJ",
		"--project", "group/sub/project",
		"--gitlab-url", srv.URL,
		"--fullsend-dir", t.TempDir(),
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve GitLab poller identity")
	assert.Positive(t, gitlabCalls, "the GitLab identity preflight never reached the server")
}

type fakeJiraPipelineDispatcher struct {
	stage string
	event *normevent.Event
	err   error
}

func (f *fakeJiraPipelineDispatcher) DispatchJiraEvent(_ context.Context, stage string, event *normevent.Event) error {
	f.stage = stage
	f.event = event
	return f.err
}

// jiraDispatchRecord builds a dispatch record the way the jira-poll CEL
// matcher does, from the normative Jira example retargeted at projectPath.
func jiraDispatchRecord(t *testing.T, projectPath string) jirapoll.DispatchRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples", "jira-fs-triage-comment.json"))
	require.NoError(t, err)
	ev, err := normevent.ParseJSON(raw)
	require.NoError(t, err)
	ev.Repo = projectPath
	ref, err := harnessdispatch.ProjectExecutionRef("triage", "triage", ev)
	require.NoError(t, err)
	return jirapoll.DispatchRecord{Agent: ref.Agent, Role: ref.Role, EventType: ref.EventType, EventPayload: ref.EventPayload}
}

func TestGitLabJiraDispatcher_ForwardsNormalizedEvent(t *testing.T) {
	fake := &fakeJiraPipelineDispatcher{}
	d := gitlabJiraDispatcher{poller: fake}
	require.NoError(t, d.Dispatch(context.Background(), jiraDispatchRecord(t, "group/sub/project")))
	assert.Equal(t, "triage", fake.stage)
	require.NotNil(t, fake.event)
	assert.Equal(t, "group/sub/project", fake.event.Repo)
	assert.Equal(t, "PROJ-123", fake.event.Entity.Key)
	assert.Equal(t, "https://acme.atlassian.net/browse/PROJ-123", fake.event.Entity.URL)

	fake.err = errors.New("pipeline rejected")
	err := d.Dispatch(context.Background(), jiraDispatchRecord(t, "group/sub/project"))
	assert.ErrorIs(t, err, fake.err)
}

func TestGitLabJiraDispatcher_RejectsBadPayloads(t *testing.T) {
	valid := jiraDispatchRecord(t, "group/project")
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(valid.EventPayload), &payload))
	norm := payload["_normalized_event"].(map[string]any)
	delete(norm, "repo")
	invalid, err := json.Marshal(payload)
	require.NoError(t, err)

	for name, tc := range map[string]struct{ payload, wantErr string }{
		"not JSON":                 {"{", "parse event payload"},
		"no normalized event":      {`{"issue":{"number":1}}`, "no _normalized_event"},
		"invalid normalized event": {string(invalid), "repo is required"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeJiraPipelineDispatcher{}
			err := gitlabJiraDispatcher{poller: fake}.Dispatch(context.Background(), jirapoll.DispatchRecord{Agent: "triage", EventPayload: tc.payload})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, fake.event, "no pipeline dispatch for an invalid payload")
		})
	}
}
