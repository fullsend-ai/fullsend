package install

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestInstaller(client *forge.FakeClient) (*Installer, *bytes.Buffer) {
	var buf bytes.Buffer
	printer := ui.NewPrinter(&buf)
	return New(client, printer), &buf
}

func TestInstall_BasicFlow(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
		{Name: "web", FullName: "org/web", DefaultBranch: "main"},
	}

	inst, output := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{
		Org: "org",
	})
	require.NoError(t, err)

	// Should discover repos
	assert.Len(t, result.OrgRepos, 2)
	assert.Contains(t, result.OrgRepos, "api")
	assert.Contains(t, result.OrgRepos, "web")

	// Should create .fullsend repo
	assert.Equal(t, ".fullsend", result.ConfigRepo)
	assert.Len(t, client.CreatedRepos, 1)
	assert.Equal(t, ".fullsend", client.CreatedRepos[0].Name)

	// Should create config.yaml, workflow, and CODEOWNERS
	assert.GreaterOrEqual(t, len(client.CreatedFiles), 3)

	// No repos enabled, so no PRs
	assert.Empty(t, result.Proposals)

	// Check output
	assert.Contains(t, output.String(), "fullsend")
	assert.Contains(t, output.String(), "Installation complete")
}

func TestInstall_WithEnabledRepo(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
		{Name: "web", FullName: "org/web", DefaultBranch: "main"},
	}

	// Pre-populate a completed onboarding workflow run
	client.WorkflowRuns["org/.fullsend/repo-onboard.yaml"] = &forge.WorkflowRun{
		ID: 1, Status: "completed", Conclusion: "success",
		HTMLURL: "https://github.com/org/.fullsend/actions/runs/1",
	}

	inst, output := newTestInstaller(client)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := inst.Run(ctx, Options{
		Org:   "org",
		Repos: []string{"api"},
	})
	require.NoError(t, err)

	// Repo onboarding is now handled by the workflow, not direct PRs.
	// The install command writes the onboarding workflow and watches it.
	assert.Contains(t, output.String(), "onboarding workflow completed")
	assert.Contains(t, output.String(), "Installation complete")

	// Config should have the repo enabled
	assert.True(t, result.Config.Repos["api"].Enabled)
}

func TestInstall_MultipleEnabledRepos(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
		{Name: "web", FullName: "org/web", DefaultBranch: "main"},
		{Name: "docs", FullName: "org/docs", DefaultBranch: "main"},
	}
	client.WorkflowRuns["org/.fullsend/repo-onboard.yaml"] = &forge.WorkflowRun{
		ID: 1, Status: "completed", Conclusion: "success",
	}

	inst, _ := newTestInstaller(client)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := inst.Run(ctx, Options{
		Org:   "org",
		Repos: []string{"api", "docs"},
	})
	require.NoError(t, err)

	// Both repos should be enabled in config
	assert.True(t, result.Config.Repos["api"].Enabled)
	assert.True(t, result.Config.Repos["docs"].Enabled)
}

func TestInstall_CustomAgents(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
	}

	inst, _ := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{
		Org:   "org",
		Roles: []string{"review", "coder"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"review", "coder"}, result.Config.Defaults.Roles)
}

func TestInstall_SafeDefaults(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
	}

	inst, _ := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{Org: "org"})
	require.NoError(t, err)

	// Verify safe defaults per acceptance criteria
	assert.False(t, result.Config.Defaults.AutoMerge, "auto_merge must default to false")
	assert.False(t, result.Config.Repos["api"].Enabled, "repos must default to disabled")
}

// TestInstall_AppPermissions removed: permissions model changed to per-agent
// (per role), not per-org. See AgentAppConfig in forge/github/types.go.

func TestInstall_SkipsFullsendRepo(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "org/.fullsend", DefaultBranch: "main"},
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
	}

	inst, _ := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{Org: "org"})
	require.NoError(t, err)

	// .fullsend itself should not be in the discovered repos
	assert.NotContains(t, result.OrgRepos, ".fullsend")
	assert.Len(t, result.OrgRepos, 1)
}

func TestInstall_ListOrgReposError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["ListOrgRepos"] = errors.New("forbidden")

	inst, _ := newTestInstaller(client)

	_, err := inst.Run(context.Background(), Options{Org: "org"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listing org repos")
}

func TestInstall_CreateRepoError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["CreateRepo"] = errors.New("already exists")

	inst, _ := newTestInstaller(client)

	_, err := inst.Run(context.Background(), Options{Org: "org"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "creating .fullsend repo")
}

func TestInstall_CreateFileError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["CreateOrUpdateFile"] = errors.New("write failed")

	inst, _ := newTestInstaller(client)

	// Use a short-lived context to avoid waiting through retry backoff
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := inst.Run(ctx, Options{Org: "org"})
	assert.Error(t, err)
}

func TestInstall_OnboardingWorkflowTimeout(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
	}
	// No workflow run pre-populated — will time out

	inst, output := newTestInstaller(client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := inst.Run(ctx, Options{
		Org:   "org",
		Repos: []string{"api"},
	})
	require.NoError(t, err)

	// Should warn about timeout but not fail
	assert.Contains(t, output.String(), "onboarding")
	assert.Contains(t, output.String(), "Installation complete")
	assert.Empty(t, result.Proposals)
}

func TestGenerateReusableWorkflow(t *testing.T) {
	wf := generateReusableWorkflow()

	assert.Contains(t, wf, "workflow_call")
	assert.Contains(t, wf, "event_type")
	assert.Contains(t, wf, "event_payload")
	assert.Contains(t, wf, "APP_PRIVATE_KEY")
	assert.Contains(t, wf, "Agent Dispatch")
}

func TestGenerateOnboardingWorkflow(t *testing.T) {
	wf := generateOnboardingWorkflow("my-org")

	assert.Contains(t, wf, "Repo Onboarding")
	assert.Contains(t, wf, "config.yaml")
	assert.Contains(t, wf, "FULLSEND_FULLSEND_APP_PRIVATE_KEY")
	assert.Contains(t, wf, "my-org")
	assert.Contains(t, wf, "fullsend/onboard")
	assert.Contains(t, wf, "workflow_dispatch")
}

func TestGenerateStubWorkflow(t *testing.T) {
	wf := GenerateStubWorkflow("my-org")

	assert.Contains(t, wf, "my-org/.fullsend")
	assert.Contains(t, wf, "issues:")
	assert.Contains(t, wf, "issue_comment:")
	assert.Contains(t, wf, "pull_request:")
	assert.Contains(t, wf, "FULLSEND_APP_PRIVATE_KEY")
}

func TestGeneratePRBody(t *testing.T) {
	body := GeneratePRBody("my-org")

	assert.Contains(t, body, "my-org/.fullsend")
	assert.Contains(t, body, "No code is changed")
	assert.Contains(t, body, "No automatic merging")
	assert.Contains(t, body, "branch protection")
}

func TestGenerateCodeowners(t *testing.T) {
	co := generateCodeowners("octocat")

	assert.Contains(t, co, "@octocat")
	assert.Contains(t, co, "CODEOWNERS")
}

func TestGenerateCodeowners_Team(t *testing.T) {
	co := generateCodeowners("my-org/admin")

	assert.Contains(t, co, "@my-org/admin")
}

func TestValidateOrgName(t *testing.T) {
	tests := []struct {
		name    string
		org     string
		wantErr string
	}{
		{name: "valid", org: "my-org", wantErr: ""},
		{name: "valid alphanumeric", org: "org123", wantErr: ""},
		{name: "valid single char", org: "x", wantErr: ""},
		{name: "empty", org: "", wantErr: "organization name cannot be empty"},
		{name: "special chars", org: "my_org", wantErr: "only alphanumeric characters and hyphens allowed"},
		{name: "spaces", org: "my org", wantErr: "only alphanumeric characters and hyphens allowed"},
		{name: "starts with hyphen", org: "-org", wantErr: "cannot start or end with a hyphen"},
		{name: "ends with hyphen", org: "org-", wantErr: "cannot start or end with a hyphen"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOrgName(tt.org)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestInstall_DefaultBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "develop"},
	}
	client.WorkflowRuns["org/.fullsend/repo-onboard.yaml"] = &forge.WorkflowRun{
		ID: 1, Status: "completed", Conclusion: "success",
	}

	inst, _ := newTestInstaller(client)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := inst.Run(ctx, Options{
		Org:   "org",
		Repos: []string{"api"},
	})
	require.NoError(t, err)

	// DefaultBranches should be populated
	assert.Equal(t, "develop", result.DefaultBranches["api"])
}

func TestInstall_ConfigRepoPrivateWhenOrgHasPrivateRepos(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "public-repo", Private: false},
		{Name: "secret-repo", Private: true},
	}
	inst, output := newTestInstaller(client)

	_, err := inst.Run(context.Background(), Options{Org: "org"})
	require.NoError(t, err)

	require.Len(t, client.CreatedRepos, 1)
	assert.True(t, client.CreatedRepos[0].Private, ".fullsend repo should be private when org has private repos")
	assert.Contains(t, output.String(), "private")
}

func TestInstall_ConfigRepoPublicWhenOrgHasNoPrivateRepos(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "public-one", Private: false},
		{Name: "public-two", Private: false},
	}
	inst, output := newTestInstaller(client)

	_, err := inst.Run(context.Background(), Options{Org: "org"})
	require.NoError(t, err)

	require.Len(t, client.CreatedRepos, 1)
	assert.False(t, client.CreatedRepos[0].Private, ".fullsend repo should be public when org has no private repos")
	assert.Contains(t, output.String(), "public")
}

func TestInstall_InvalidOrgName(t *testing.T) {
	client := forge.NewFakeClient()
	inst, _ := newTestInstaller(client)

	_, err := inst.Run(context.Background(), Options{Org: "bad org!"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "only alphanumeric characters and hyphens allowed")
}

func TestInstall_RepoNotFoundWarning(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
	}
	client.WorkflowRuns["org/.fullsend/repo-onboard.yaml"] = &forge.WorkflowRun{
		ID: 1, Status: "completed", Conclusion: "success",
	}

	inst, output := newTestInstaller(client)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := inst.Run(ctx, Options{
		Org:   "org",
		Repos: []string{"nonexistent"},
	})
	require.NoError(t, err)

	assert.Contains(t, output.String(), "not found in organization")
}

func TestInstall_ConfigValidation(t *testing.T) {
	client := forge.NewFakeClient()
	inst, _ := newTestInstaller(client)

	// Invalid agent role should fail config validation
	_, err := inst.Run(context.Background(), Options{
		Org:   "org",
		Roles: []string{"invalid-agent"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid configuration")
}
