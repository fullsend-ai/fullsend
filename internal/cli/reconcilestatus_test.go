package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

// gitlabNoteServer returns an httptest.Server that handles GitLab note API calls
// for reconcile-status tests.
func gitlabNoteServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
}

func TestNewReconcileStatusCmd_RequiredFlags(t *testing.T) {
	cmd := newReconcileStatusCmd()

	for _, name := range []string{"repo", "number", "run-id"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}
}

func TestNewReconcileStatusCmd_ReasonFlagDefault(t *testing.T) {
	cmd := newReconcileStatusCmd()

	reason := cmd.Flags().Lookup("reason")
	require.NotNil(t, reason)
	assert.Equal(t, "terminated", reason.DefValue)
}

func TestNewReconcileStatusCmd_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing mint-url",
			args:    []string{"--repo", "org/repo", "--number", "7", "--run-id", "run-1"},
			wantErr: "--mint-url or FULLSEND_MINT_URL required",
		},
		{
			name:    "invalid number",
			args:    []string{"--repo", "org/repo", "--number", "0", "--run-id", "run-1"},
			wantErr: "--number must be a positive integer",
		},
		{
			name:    "invalid repo format",
			args:    []string{"--repo", "noslash", "--number", "7", "--run-id", "run-1"},
			wantErr: "--repo must be in owner/repo format",
		},
		{
			name:    "mint-url without role",
			args:    []string{"--repo", "org/repo", "--number", "7", "--run-id", "run-1", "--mint-url", "https://mint.example.com"},
			wantErr: "--role is required when using --mint-url",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newReconcileStatusCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNewReconcileStatusCmd_MintURLFlags(t *testing.T) {
	cmd := newReconcileStatusCmd()

	for _, name := range []string{"mint-url", "role"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}

	mintURL := cmd.Flags().Lookup("mint-url")
	assert.Equal(t, "", mintURL.DefValue)

	role := cmd.Flags().Lookup("role")
	assert.Equal(t, "", role.DefValue)
}

func TestNewReconcileStatusCmd_MintURLFromEnv(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{"--repo", "org/repo", "--number", "7", "--run-id", "run-1", "--role", "review"})
	err := cmd.Execute()
	// Will fail at the OIDC exchange (no ACTIONS_ID_TOKEN_REQUEST_URL), but
	// proves the env var was picked up and --role validation passed.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minting status token")
}

func TestNewReconcileStatusCmd_TokenFlagRemoved(t *testing.T) {
	cmd := newReconcileStatusCmd()
	f := cmd.Flags().Lookup("token")
	assert.Nil(t, f, "--token flag should no longer exist")
}

func TestNewReconcileStatusCmd_MintSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	origMint := reconcileMintToken
	reconcileMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		assert.Equal(t, "coder", req.Role)
		assert.Equal(t, []string{"repo"}, req.Repos)
		return &mintclient.MintResult{Token: "ghs_minted_token"}, nil
	}
	defer func() { reconcileMintToken = origMint }()

	origForge := reconcileNewForgeClient
	reconcileNewForgeClient = func(token string) forge.Client {
		return gh.New(token).WithBaseURL(srv.URL)
	}
	defer func() { reconcileNewForgeClient = origForge }()

	t.Setenv("FULLSEND_MINT_URL", "")
	t.Setenv("GITHUB_ACTIONS", "true")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", srv.URL,
		"--role", "code",
	})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewReconcileStatusCmd_MintSuccessCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	origMint := reconcileMintToken
	reconcileMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_minted_token"}, nil
	}
	defer func() { reconcileMintToken = origMint }()

	origForge := reconcileNewForgeClient
	reconcileNewForgeClient = func(token string) forge.Client {
		return gh.New(token).WithBaseURL(srv.URL)
	}
	defer func() { reconcileNewForgeClient = origForge }()

	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--reason", "cancelled",
		"--mint-url", srv.URL,
		"--role", "review",
	})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewReconcileStatusCmd_RejectsMalformedToken(t *testing.T) {
	origMint := reconcileMintToken
	reconcileMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "not-a-valid-token!"}, nil
	}
	defer func() { reconcileMintToken = origMint }()

	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "coder",
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected characters")
}

func TestNewReconcileStatusCmd_HasForgeFlag(t *testing.T) {
	cmd := newReconcileStatusCmd()

	f := cmd.Flags().Lookup("forge")
	require.NotNil(t, f, "reconcile-status command should have --forge flag")
	assert.Equal(t, "", f.DefValue)
}

func TestNewReconcileStatusCmd_GitLabSuccess(t *testing.T) {
	srv := gitlabNoteServer()
	defer srv.Close()

	t.Setenv("GITLAB_TOKEN", "glpat-test-token")
	t.Setenv("CI_SERVER_URL", srv.URL)
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--forge", "gitlab",
	})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewReconcileStatusCmd_GitLabCancelled(t *testing.T) {
	srv := gitlabNoteServer()
	defer srv.Close()

	t.Setenv("GITLAB_TOKEN", "glpat-test-token")
	t.Setenv("CI_SERVER_URL", srv.URL)

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--reason", "cancelled",
		"--forge", "gitlab",
	})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewReconcileStatusCmd_GitLabNoToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--forge", "gitlab",
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitLab token found")
}

func TestNewReconcileStatusCmd_GitLabCustomBaseURL(t *testing.T) {
	srv := gitlabNoteServer()
	defer srv.Close()

	t.Setenv("GITLAB_TOKEN", "glpat-test-token")
	t.Setenv("FULLSEND_GITLAB_URL", srv.URL)
	t.Setenv("CI_SERVER_URL", "https://should-not-be-used.example.com")
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--forge", "gitlab",
	})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestNewReconcileStatusCmd_GitLabNoMintRequired(t *testing.T) {
	srv := gitlabNoteServer()
	defer srv.Close()

	// GitLab path should succeed without --mint-url or --role.
	t.Setenv("GITLAB_TOKEN", "glpat-test-token")
	t.Setenv("CI_SERVER_URL", srv.URL)
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--forge", "gitlab",
	})

	err := cmd.Execute()
	require.NoError(t, err)
}

// stubReconcileVars replaces the three package-level func vars used by
// newReconcileStatusCmd and returns a teardown function.
func stubReconcileVars(t *testing.T, onReconcile func(completionMode, jobStatus string, wasSkipped bool, agentDescription string)) {
	t.Helper()
	origMint := reconcileMintToken
	origForge := reconcileNewForgeClient
	origTracker := reconcileNewTrackerClient
	origReconcile := reconcileOrphaned

	reconcileMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_stub_token"}, nil
	}
	reconcileNewForgeClient = func(token string) forge.Client {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		}))
		t.Cleanup(srv.Close)
		return gh.New(token).WithBaseURL(srv.URL)
	}
	reconcileNewTrackerClient = func(fc forge.Client) tracker.Client {
		return tracker.NewForgeClient(fc)
	}
	reconcileOrphaned = func(_ context.Context, _ tracker.Client, _ string, _ int, _, _, _ string, _ statuscomment.TerminationReason, completionMode, jobStatus string, wasSkipped bool, agentDescription string) error {
		onReconcile(completionMode, jobStatus, wasSkipped, agentDescription)
		return nil
	}
	t.Cleanup(func() {
		reconcileMintToken = origMint
		reconcileNewForgeClient = origForge
		reconcileNewTrackerClient = origTracker
		reconcileOrphaned = origReconcile
	})
}

func TestNewReconcileStatusCmd_FullsendDir_OnFailureConfig(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  status_notifications:
    comment:
      completion: on_failure
`), 0o644)
	require.NoError(t, err)

	var gotMode, gotStatus string
	stubReconcileVars(t, func(completionMode, jobStatus string, _ bool, _ string) {
		gotMode = completionMode
		gotStatus = jobStatus
	})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
		"--fullsend-dir", dir,
		"--job-status", "failure",
	})

	err = cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "on_failure", gotMode)
	assert.Equal(t, "failure", gotStatus)
}

func TestNewReconcileStatusCmd_FullsendDir_PerRepoConfig_OnFailure(t *testing.T) {
	dir := t.TempDir()
	// No "dispatch"/"repos"/"inference"/"defaults" key, so this is
	// detected as a per-repo config, not an org config.
	err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
roles: ["review"]
status_notifications:
  comment:
    completion: on_failure
`), 0o644)
	require.NoError(t, err)

	var gotMode, gotStatus string
	stubReconcileVars(t, func(completionMode, jobStatus string, _ bool, _ string) {
		gotMode = completionMode
		gotStatus = jobStatus
	})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
		"--fullsend-dir", dir,
		"--job-status", "failure",
	})

	err = cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "on_failure", gotMode, "per-repo config should be able to configure completion mode")
	assert.Equal(t, "failure", gotStatus)
}

func TestNewReconcileStatusCmd_FullsendDir_MalformedConfig(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`{{{not valid yaml`), 0o644)
	require.NoError(t, err)

	var gotMode string
	stubReconcileVars(t, func(completionMode, _ string, _ bool, _ string) {
		gotMode = completionMode
	})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
		"--fullsend-dir", dir,
	})

	err = cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "", gotMode, "malformed config should fall back to empty completionMode")
}

func TestNewReconcileStatusCmd_FullsendDir_MissingConfig(t *testing.T) {
	dir := t.TempDir() // no config.yaml written

	var gotMode string
	stubReconcileVars(t, func(completionMode, _ string, _ bool, _ string) {
		gotMode = completionMode
	})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
		"--fullsend-dir", dir,
	})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "", gotMode, "missing config should fall back to empty completionMode")
}

func TestNewReconcileStatusCmd_FullsendDir_MissingConfig_LogsDiagnostic(t *testing.T) {
	dir := t.TempDir() // no config.yaml written

	stubReconcileVars(t, func(_, _ string, _ bool, _ string) {})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
		"--fullsend-dir", dir,
	})

	var err error
	stderr := captureStderr(t, func() {
		err = cmd.Execute()
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "status_notifications", "should log a diagnostic distinguishing 'not an org config' from a real load error")
}

func TestNewReconcileStatusCmd_WasSkippedFlag_Threaded(t *testing.T) {
	var gotSkipped bool
	stubReconcileVars(t, func(_, _ string, wasSkipped bool, _ string) {
		gotSkipped = wasSkipped
	})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
		"--job-status", "success",
		"--was-skipped",
	})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, gotSkipped)
}

func TestNewReconcileStatusCmd_WasSkippedFlag_DefaultsFalse(t *testing.T) {
	var gotSkipped bool
	stubReconcileVars(t, func(_, _ string, wasSkipped bool, _ string) {
		gotSkipped = wasSkipped
	})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
	})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.False(t, gotSkipped)
}

func TestNewReconcileStatusCmd_AgentDescription_DerivedFromRole(t *testing.T) {
	var gotDescription string
	stubReconcileVars(t, func(_, _ string, _ bool, agentDescription string) {
		gotDescription = agentDescription
	})
	t.Setenv("FULLSEND_MINT_URL", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "code-review",
	})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "Code Review", gotDescription)
}

// writeJiraDispatchEvent writes a dispatch/event-payload.json containing a
// Jira normalized event with the given entity key (e.g. "PROJ-123") under dir.
func writeJiraDispatchEvent(t *testing.T, dir, entityKey string) {
	t.Helper()
	dispatchDir := filepath.Join(dir, "dispatch")
	require.NoError(t, os.MkdirAll(dispatchDir, 0o755))
	payload := fmt.Sprintf(`{
  "_normalized_event": {
    "repo": "org/repo",
    "entity": {"kind": "work_item", "id": 1, "url": "https://jira.example.com/browse/%s", "key": "%s"},
    "transition": {"kind": "updated"},
    "actor": {"id": "bot", "kind": "bot", "role": "write"},
    "state": {"labels": []},
    "source": {"system": "jira", "raw_type": "jira:issue_updated"}
  }
}`, entityKey, entityKey)
	require.NoError(t, os.WriteFile(filepath.Join(dispatchDir, "event-payload.json"), []byte(payload), 0o644))
}

func TestNewReconcileStatusCmd_Jira(t *testing.T) {
	// Verify that a Jira normalized event in the dispatch payload routes
	// to a Jira tracker client with project and number derived from
	// entity.key.
	var gotProject string
	var gotNumber int
	origReconcile := reconcileOrphaned
	origJira := reconcileNewJiraTrackerClient
	reconcileOrphaned = func(_ context.Context, _ tracker.Client, project string, number int, _, _, _ string, _ statuscomment.TerminationReason, _, _ string, _ bool, _ string) error {
		gotProject = project
		gotNumber = number
		return nil
	}
	reconcileNewJiraTrackerClient = func() (tracker.Client, error) {
		return tracker.NewForgeClient(gh.New("fake")), nil // stub; type doesn't matter for this test
	}
	t.Cleanup(func() {
		reconcileOrphaned = origReconcile
		reconcileNewJiraTrackerClient = origJira
	})

	dir := t.TempDir()
	writeJiraDispatchEvent(t, dir, "PROJ-123")

	t.Setenv("JIRA_BASE_URL", "https://acme.atlassian.net")
	t.Setenv("JIRA_TOKEN", "jira-test-token")
	t.Setenv("JIRA_USER_EMAIL", "bot@example.com")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--run-id", "run-1",
		"--fullsend-dir", dir,
	})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "PROJ", gotProject)
	assert.Equal(t, 123, gotNumber)
}

func TestNewReconcileStatusCmd_Jira_NoBaseURL(t *testing.T) {
	dir := t.TempDir()
	writeJiraDispatchEvent(t, dir, "PROJ-123")

	t.Setenv("JIRA_BASE_URL", "")
	t.Setenv("JIRA_TOKEN", "test-token")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--run-id", "run-1",
		"--fullsend-dir", dir,
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JIRA_BASE_URL required")
}

func TestNewReconcileStatusCmd_Jira_NoToken(t *testing.T) {
	dir := t.TempDir()
	writeJiraDispatchEvent(t, dir, "PROJ-123")

	t.Setenv("JIRA_BASE_URL", "https://acme.atlassian.net")
	t.Setenv("JIRA_TOKEN", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--number", "1",
		"--run-id", "run-1",
		"--fullsend-dir", dir,
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JIRA_TOKEN required")
}

func TestNewReconcileStatusCmd_NoTrackerFlags(t *testing.T) {
	// --tracker-source and --tracker-project were removed in favor of
	// reading the normalized event from the dispatch payload.
	cmd := newReconcileStatusCmd()

	for _, name := range []string{"tracker-source", "tracker-project"} {
		f := cmd.Flags().Lookup(name)
		assert.Nil(t, f, "flag %q should no longer exist", name)
	}
}

func TestNewReconcileStatusCmd_Jira_ViaGitHubEventPath(t *testing.T) {
	// Verify the GITHUB_EVENT_PATH fallback: when --fullsend-dir is not
	// set, the command reads the normalized event from GITHUB_EVENT_PATH.
	var gotProject string
	var gotNumber int
	origReconcile := reconcileOrphaned
	origJira := reconcileNewJiraTrackerClient
	reconcileOrphaned = func(_ context.Context, _ tracker.Client, project string, number int, _, _, _ string, _ statuscomment.TerminationReason, _, _ string, _ bool, _ string) error {
		gotProject = project
		gotNumber = number
		return nil
	}
	reconcileNewJiraTrackerClient = func() (tracker.Client, error) {
		return tracker.NewForgeClient(gh.New("fake")), nil
	}
	t.Cleanup(func() {
		reconcileOrphaned = origReconcile
		reconcileNewJiraTrackerClient = origJira
	})

	// Write a workflow_dispatch event file with inputs.event_payload
	// containing a Jira normalized event.
	eventFile := filepath.Join(t.TempDir(), "event.json")
	innerPayload := `{"_normalized_event":{"repo":"org/repo","entity":{"kind":"work_item","id":1,"url":"https://jira.example.com/browse/TEAM-42","key":"TEAM-42"},"transition":{"kind":"updated"},"actor":{"id":"bot","kind":"bot","role":"write"},"state":{"labels":[]},"source":{"system":"jira","raw_type":"jira:issue_updated"}}}`
	eventContent := fmt.Sprintf(`{"inputs":{"event_payload":%s}}`, strconv.Quote(innerPayload))
	require.NoError(t, os.WriteFile(eventFile, []byte(eventContent), 0o644))

	t.Setenv("GITHUB_EVENT_PATH", eventFile)
	t.Setenv("JIRA_BASE_URL", "https://acme.atlassian.net")
	t.Setenv("JIRA_TOKEN", "jira-test-token")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--number", "1",
		"--run-id", "run-1",
	})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "TEAM", gotProject)
	assert.Equal(t, 42, gotNumber)
}

func TestNewReconcileStatusCmd_NoEvent_FallsBackToForge(t *testing.T) {
	// When no normalized event is found, the command falls back to the
	// forge path using --repo, verifying backward compatibility.
	stubReconcileVars(t, func(_, _ string, _ bool, _ string) {})
	t.Setenv("FULLSEND_MINT_URL", "")
	t.Setenv("GITHUB_EVENT_PATH", "")

	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "org/repo",
		"--number", "7",
		"--run-id", "run-1",
		"--mint-url", "https://mint.example.com",
		"--role", "review",
	})

	err := cmd.Execute()
	require.NoError(t, err)
}
