package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const testReviewSHA = "0123456789abcdef0123456789abcdef01234567"

func stubReviewStatusClients(t *testing.T, githubClient, gitlabClient forge.Client) {
	t.Helper()
	origGitHub := reviewStatusGitHubClientFn
	origGitLab := reviewStatusGitLabClientFn
	reviewStatusGitHubClientFn = func() (forge.Client, error) {
		return githubClient, nil
	}
	reviewStatusGitLabClientFn = func(string) (forge.Client, error) {
		return gitlabClient, nil
	}
	t.Cleanup(func() {
		reviewStatusGitHubClientFn = origGitHub
		reviewStatusGitLabClientFn = origGitLab
	})
}

func TestReviewStatusCommand_EmitsSHAScopedStates(t *testing.T) {
	tests := []struct {
		state       string
		wantState   forge.CommitStatusState
		description string
	}{
		{state: "pending", wantState: forge.CommitStatusPending, description: "Automated review is running"},
		{state: "success", wantState: forge.CommitStatusSuccess, description: "Automated review completed"},
		{state: "failure", wantState: forge.CommitStatusFailure, description: "Automated review did not complete"},
		{state: "error", wantState: forge.CommitStatusError, description: "Automated review did not complete"},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			fc := forge.NewFakeClient()
			stubReviewStatusClients(t, fc, nil)

			cmd := newReviewStatusCmd()
			cmd.SetArgs([]string{
				"--repo", "acme/widget",
				"--sha", testReviewSHA,
				"--state", tt.state,
				"--run-url", "https://github.com/acme/widget/actions/runs/42",
				"--forge", "github",
			})
			require.NoError(t, cmd.Execute())
			require.Len(t, fc.CommitStatuses, 1)
			assert.Equal(t, testReviewSHA, fc.CommitStatuses[0].SHA)
			assert.Equal(t, forge.CommitStatus{
				State:       tt.wantState,
				Context:     reviewCompletionContext,
				Description: tt.description,
				TargetURL:   "https://github.com/acme/widget/actions/runs/42",
			}, fc.CommitStatuses[0].Status)
		})
	}
}

func TestReviewStatusCommand_SelectsGitLabClient(t *testing.T) {
	fc := forge.NewFakeClient()
	stubReviewStatusClients(t, nil, fc)

	cmd := newReviewStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "group/widget",
		"--sha", testReviewSHA,
		"--state", "pending",
		"--forge", "gitlab",
	})
	require.NoError(t, cmd.Execute())
	require.Len(t, fc.CommitStatuses, 1)
}

func TestReviewStatusCommand_DerivesTerminalState(t *testing.T) {
	tests := []struct {
		name       string
		jobStatus  string
		skipped    bool
		wantStatus forge.CommitStatusState
	}{
		{name: "completed", jobStatus: "success", wantStatus: forge.CommitStatusSuccess},
		{name: "skipped", jobStatus: "success", skipped: true, wantStatus: forge.CommitStatusFailure},
		{name: "failed", jobStatus: "failure", wantStatus: forge.CommitStatusFailure},
		{name: "timed out", jobStatus: "failure", wantStatus: forge.CommitStatusFailure},
		{name: "cancelled", jobStatus: "cancelled", wantStatus: forge.CommitStatusError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			stubReviewStatusClients(t, fc, nil)
			args := []string{
				"--repo", "acme/widget",
				"--sha", testReviewSHA,
				"--job-status", tt.jobStatus,
				"--forge", "github",
			}
			if tt.skipped {
				args = append(args, "--was-skipped")
			}
			cmd := newReviewStatusCmd()
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			require.Len(t, fc.CommitStatuses, 1)
			assert.Equal(t, tt.wantStatus, fc.CommitStatuses[0].Status.State)
		})
	}
}

func TestReviewStatusCommand_Validation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "repository", args: []string{"--repo", "widget", "--sha", testReviewSHA, "--state", "pending"}, wantErr: "owner/repo format"},
		{name: "empty owner", args: []string{"--repo", "/widget", "--sha", testReviewSHA, "--state", "pending"}, wantErr: "owner/repo format"},
		{name: "empty name", args: []string{"--repo", "acme/", "--sha", testReviewSHA, "--state", "pending"}, wantErr: "owner/repo format"},
		{name: "sha", args: []string{"--repo", "acme/widget", "--sha", "abc123", "--state", "pending"}, wantErr: "40-character hexadecimal"},
		{name: "state", args: []string{"--repo", "acme/widget", "--sha", testReviewSHA, "--state", "skipped"}, wantErr: "unsupported review status state"},
		{name: "forge", args: []string{"--repo", "acme/widget", "--sha", testReviewSHA, "--state", "pending", "--forge", "forgejo"}, wantErr: "not a valid forge platform"},
		{name: "missing outcome", args: []string{"--repo", "acme/widget", "--sha", testReviewSHA}, wantErr: "either --state or --job-status is required"},
		{name: "conflicting outcomes", args: []string{"--repo", "acme/widget", "--sha", testReviewSHA, "--state", "pending", "--job-status", "success"}, wantErr: "cannot be used together"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			stubReviewStatusClients(t, fc, fc)
			cmd := newReviewStatusCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, fc.CommitStatuses)
		})
	}
}

func TestReviewStatusCommand_PropagatesClientError(t *testing.T) {
	tests := []struct {
		name      string
		forgeFlag string
		stub      func()
	}{
		{
			name:      "GitHub",
			forgeFlag: "github",
			stub: func() {
				reviewStatusGitHubClientFn = func() (forge.Client, error) {
					return nil, errors.New("GitHub authentication unavailable")
				}
			},
		},
		{
			name:      "GitLab",
			forgeFlag: "gitlab",
			stub: func() {
				reviewStatusGitLabClientFn = func(string) (forge.Client, error) {
					return nil, errors.New("GitLab authentication unavailable")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origGitHub := reviewStatusGitHubClientFn
			origGitLab := reviewStatusGitLabClientFn
			tt.stub()
			t.Cleanup(func() {
				reviewStatusGitHubClientFn = origGitHub
				reviewStatusGitLabClientFn = origGitLab
			})

			cmd := newReviewStatusCmd()
			cmd.SetArgs([]string{
				"--repo", "acme/widget",
				"--sha", testReviewSHA,
				"--state", "pending",
				"--forge", tt.forgeFlag,
			})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "authentication unavailable")
		})
	}
}

func TestReviewStatusCommand_PropagatesForgeError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["SetCommitStatus"] = errors.New("status API unavailable")
	stubReviewStatusClients(t, fc, nil)

	cmd := newReviewStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "acme/widget",
		"--sha", testReviewSHA,
		"--state", "pending",
		"--forge", "github",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting fullsend/review-completed")
}

func TestReviewStatusCommand_UsesWorkflowToken(t *testing.T) {
	handlerCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, "Bearer workflow-token", r.Header.Get("Authorization"))
		assert.Equal(t, "/repos/acme/widget/statuses/"+testReviewSHA, r.URL.Path)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "pending", body["state"])
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "workflow-token")
	t.Setenv("GITHUB_API_URL", srv.URL)
	cmd := newReviewStatusCmd()
	cmd.SetArgs([]string{
		"--repo", "acme/widget",
		"--sha", testReviewSHA,
		"--state", "pending",
		"--forge", "github",
	})
	require.NoError(t, cmd.Execute())
	assert.True(t, handlerCalled, "status handler was not called")
}

func TestReviewStatusCommand_IsHiddenFromPublicCLI(t *testing.T) {
	cmd := newReviewStatusCmd()
	assert.True(t, cmd.Hidden)

	found, _, err := newRootCmd().Find([]string{"review-status"})
	require.NoError(t, err)
	assert.Equal(t, "review-status", found.Name())
}
