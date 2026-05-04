package layers

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newWorkflowsLayer(t *testing.T, client forge.Client) (*WorkflowsLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, nil, printer, "admin-user")
	return layer, &buf
}

func newFakeClientWithRepo() *forge.FakeClient {
	return &forge.FakeClient{
		Repos: []forge.Repository{
			{Name: ".fullsend", FullName: "test-org/.fullsend", DefaultBranch: "main"},
		},
	}
}

func TestWorkflowsLayer_Name(t *testing.T) {
	layer, _ := newWorkflowsLayer(t, &forge.FakeClient{})
	assert.Equal(t, "workflows", layer.Name())
}

func TestWorkflowsLayer_Install_WritesAllFiles(t *testing.T) {
	client := newFakeClientWithRepo()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// SyncFiles writes scaffold files + CODEOWNERS.
	var scaffoldCount int
	_ = scaffold.WalkFullsendRepo(func(_ string, _ []byte) error {
		scaffoldCount++
		return nil
	})
	require.Equal(t, scaffoldCount+1, len(client.CreatedFiles),
		"expected %d scaffold files + CODEOWNERS", scaffoldCount)

	paths := make(map[string]string)
	for _, f := range client.CreatedFiles {
		assert.Equal(t, "test-org", f.Owner)
		assert.Equal(t, ".fullsend", f.Repo)
		paths[f.Path] = string(f.Content)
	}

	assert.Contains(t, paths, ".github/workflows/triage.yml")
	assert.Contains(t, paths, ".github/workflows/code.yml")
	assert.Contains(t, paths, ".github/workflows/review.yml")
	assert.Contains(t, paths, ".github/workflows/fix.yml")
	assert.Contains(t, paths, ".github/workflows/repo-maintenance.yml")
	assert.Contains(t, paths, "CODEOWNERS")
	assert.Contains(t, paths["CODEOWNERS"], "admin-user")
}

func TestWorkflowsLayer_Install_IncludesConfigYAML(t *testing.T) {
	client := newFakeClientWithRepo()
	cfg := config.NewOrgConfig(
		[]string{"repo-a"}, []string{"repo-a"},
		[]string{"coder"}, []config.AgentEntry{{Role: "coder", Name: "Bot", Slug: "bot"}}, "",
	)
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, cfg, printer, "admin-user")

	err := layer.Install(context.Background())
	require.NoError(t, err)

	paths := make(map[string]string)
	for _, f := range client.CreatedFiles {
		paths[f.Path] = string(f.Content)
	}

	assert.Contains(t, paths, "config.yaml", "config.yaml should be included in sync files")
	assert.Contains(t, paths["config.yaml"], "repo-a", "config.yaml should contain repo config")
}

func TestWorkflowsLayer_Install_NoConfigWhenNil(t *testing.T) {
	client := newFakeClientWithRepo()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	for _, f := range client.CreatedFiles {
		assert.NotEqual(t, "config.yaml", f.Path, "config.yaml should not be included when config is nil")
	}
}

func TestWorkflowsLayer_Install_CreatesBranchAndPR(t *testing.T) {
	client := newFakeClientWithRepo()
	layer, output := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	assert.Contains(t, client.CreatedBranches, "test-org/.fullsend/fullsend/sync-scaffold")
	require.Len(t, client.CreatedProposals, 1)

	pr := client.CreatedProposals[0]
	assert.Equal(t, "chore: sync scaffold files", pr.Title)
	assert.Equal(t, "fullsend/sync-scaffold", pr.Head)
	assert.Contains(t, output.String(), pr.URL)
}

func TestWorkflowsLayer_Install_ReusesExistingPR(t *testing.T) {
	client := newFakeClientWithRepo()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"test-org/.fullsend": {
			{URL: "https://github.com/test-org/.fullsend/pull/42", Title: "chore: sync scaffold files", Number: 42, Head: "fullsend/sync-scaffold"},
		},
	}
	layer, output := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.CreatedProposals, "should not create a new PR")
	assert.Contains(t, output.String(), "https://github.com/test-org/.fullsend/pull/42")
}

func TestWorkflowsLayer_Install_BranchAlreadyExists(t *testing.T) {
	client := newFakeClientWithRepo()
	client.Errors = map[string]error{
		"CreateBranch": forge.ErrAlreadyExists,
	}
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err, "should succeed even if branch already exists")

	assert.NotEmpty(t, client.CreatedFiles, "should still sync files")
	assert.Len(t, client.CreatedProposals, 1, "should still create PR")
}

func TestWorkflowsLayer_Install_TriageWorkflowContent(t *testing.T) {
	client := newFakeClientWithRepo()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	var triageContent string
	for _, f := range client.CreatedFiles {
		if f.Path == ".github/workflows/triage.yml" {
			triageContent = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, triageContent, "triage.yml should have been written")

	expected, err := scaffold.FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)
	assert.Equal(t, string(expected), triageContent)
}

func TestWorkflowsLayer_Install_RepoMaintenanceContent(t *testing.T) {
	client := newFakeClientWithRepo()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	var maintenanceContent string
	for _, f := range client.CreatedFiles {
		if f.Path == ".github/workflows/repo-maintenance.yml" {
			maintenanceContent = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, maintenanceContent, "repo-maintenance.yml should have been written")

	expected, err := scaffold.FullsendRepoFile(".github/workflows/repo-maintenance.yml")
	require.NoError(t, err)
	assert.Equal(t, string(expected), maintenanceContent)
}

func TestWorkflowsLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{
			"SyncFiles": errors.New("write failed"),
		},
	}
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")
}

func TestWorkflowsLayer_Uninstall_Noop(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.DeletedRepos)
	assert.Empty(t, client.CreatedFiles)
}

func TestWorkflowsLayer_Analyze_AllPresent(t *testing.T) {
	fileContents := map[string][]byte{
		"test-org/.fullsend/CODEOWNERS": []byte("* @admin-user"),
	}
	_ = scaffold.WalkFullsendRepo(func(path string, content []byte) error {
		fileContents["test-org/.fullsend/"+path] = content
		return nil
	})

	client := &forge.FakeClient{
		FileContents: fileContents,
	}
	layer, _ := newWorkflowsLayer(t, client)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "workflows", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Len(t, report.Details, len(managedFiles))
}

func TestWorkflowsLayer_Analyze_NonePresent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	layer, _ := newWorkflowsLayer(t, client)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "workflows", report.Name)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Len(t, report.WouldInstall, len(managedFiles))
}

func TestWorkflowsLayer_Analyze_Partial(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/.github/workflows/triage.yml": []byte("triage workflow"),
		},
	}
	layer, _ := newWorkflowsLayer(t, client)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "workflows", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)
	joined := strings.Join(report.Details, " ")
	assert.Contains(t, joined, "triage.yml")
	assert.NotEmpty(t, report.WouldFix)
	fixJoined := strings.Join(report.WouldFix, " ")
	assert.Contains(t, fixJoined, "CODEOWNERS")
}

func TestManagedFilesMatchScaffold(t *testing.T) {
	var scaffoldPaths []string
	err := scaffold.WalkFullsendRepo(func(path string, _ []byte) error {
		scaffoldPaths = append(scaffoldPaths, path)
		return nil
	})
	require.NoError(t, err)

	for _, path := range scaffoldPaths {
		found := false
		for _, managed := range managedFiles {
			if managed == path {
				found = true
				break
			}
		}
		assert.True(t, found, "managedFiles should include scaffold file %s", path)
	}
}

func TestManagedFilesDoNotIncludeOldPlaceholders(t *testing.T) {
	for _, path := range managedFiles {
		assert.NotEqual(t, ".github/workflows/agent.yaml", path,
			"managedFiles should not include old agent.yaml placeholder")
		assert.NotEqual(t, ".github/workflows/repo-onboard.yaml", path,
			"managedFiles should not include old repo-onboard.yaml placeholder")
	}
}
