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

	inst, output := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{
		Org:   "org",
		Repos: []string{"api"},
	})
	require.NoError(t, err)

	// Should create a PR for enabled repo
	assert.Len(t, result.Proposals, 1)
	proposal, ok := result.Proposals["api"]
	require.True(t, ok)
	assert.Equal(t, 1, proposal.Number)
	assert.Contains(t, proposal.URL, "api")

	// Should have created a branch and workflow file
	assert.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "fullsend/enroll", client.CreatedBranches[0].BranchName)

	// Check output mentions the PR
	assert.Contains(t, output.String(), "PR created for api")
	assert.Contains(t, output.String(), "Enrollment PRs: 1")
}

func TestInstall_MultipleEnabledRepos(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
		{Name: "web", FullName: "org/web", DefaultBranch: "main"},
		{Name: "docs", FullName: "org/docs", DefaultBranch: "main"},
	}

	inst, _ := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{
		Org:   "org",
		Repos: []string{"api", "docs"},
	})
	require.NoError(t, err)

	assert.Len(t, result.Proposals, 2)
	assert.Contains(t, result.Proposals, "api")
	assert.Contains(t, result.Proposals, "docs")
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

func TestInstall_PRCreationErrorContinues(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "api", FullName: "org/api", DefaultBranch: "main"},
		{Name: "web", FullName: "org/web", DefaultBranch: "main"},
	}
	// Fail branch creation for any repo — this will affect all PRs
	// but the install should still complete
	client.Errors["CreateBranch"] = errors.New("branch exists")

	inst, _ := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{
		Org:   "org",
		Repos: []string{"api", "web"},
	})
	require.NoError(t, err)

	// PRs should be empty since branch creation failed
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

func TestGenerateStubWorkflow(t *testing.T) {
	wf := generateStubWorkflow("my-org")

	assert.Contains(t, wf, "my-org/.fullsend")
	assert.Contains(t, wf, "issues:")
	assert.Contains(t, wf, "issue_comment:")
	assert.Contains(t, wf, "pull_request:")
	assert.Contains(t, wf, "FULLSEND_APP_PRIVATE_KEY")
}

func TestGeneratePRBody(t *testing.T) {
	body := generatePRBody("my-org")

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

	inst, _ := newTestInstaller(client)

	result, err := inst.Run(context.Background(), Options{
		Org:   "org",
		Repos: []string{"api"},
	})
	require.NoError(t, err)

	// The PR should use "develop" as the base branch, not "main"
	require.Len(t, client.CreatedProposals, 1)
	assert.Equal(t, "develop", client.CreatedProposals[0].Base)

	// DefaultBranches should be populated
	assert.Equal(t, "develop", result.DefaultBranches["api"])
}

func TestInstall_ConfigRepoIsPrivate(t *testing.T) {
	client := forge.NewFakeClient()
	inst, _ := newTestInstaller(client)

	_, err := inst.Run(context.Background(), Options{Org: "org"})
	require.NoError(t, err)

	require.Len(t, client.CreatedRepos, 1)
	assert.True(t, client.CreatedRepos[0].Private, ".fullsend repo should be private")
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

	inst, output := newTestInstaller(client)

	_, err := inst.Run(context.Background(), Options{
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
