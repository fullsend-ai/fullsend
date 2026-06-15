package layers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newWorkflowsLayer(t *testing.T, client *forge.FakeClient) (*WorkflowsLayer, *bytes.Buffer) {
	t.Helper()
	if client.Repos == nil {
		client.Repos = []forge.Repository{{
			FullName:      "test-org/" + forge.ConfigRepoName,
			DefaultBranch: "main",
		}}
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user", "test-version")
	return layer, &buf
}

func TestWorkflowsLayer_Name(t *testing.T) {
	layer, _ := newWorkflowsLayer(t, forge.NewFakeClient())
	assert.Equal(t, "workflows", layer.Name())
}

func TestWorkflowsLayer_Install_WritesAllFiles(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Scaffold files go through CommitFiles as a single batch.
	require.Len(t, client.CommittedFiles, 1, "expected exactly one CommitFiles call")
	batch := client.CommittedFiles[0]
	assert.Equal(t, "test-org", batch.Owner)
	assert.Equal(t, ".fullsend", batch.Repo)

	paths := make(map[string]string)
	for _, f := range batch.Files {
		paths[f.Path] = string(f.Content)
	}

	assert.Contains(t, paths, ".github/workflows/triage.yml")
	assert.Contains(t, paths, ".github/workflows/code.yml")
	assert.Contains(t, paths, ".github/workflows/review.yml")
	assert.Contains(t, paths, ".github/workflows/fix.yml")
	assert.Contains(t, paths, ".github/workflows/repo-maintenance.yml")

	// CODEOWNERS is included in the same batch.
	assert.Contains(t, paths, "CODEOWNERS")
	assert.Contains(t, paths["CODEOWNERS"], "admin-user")
}

func TestWorkflowsLayer_Install_TriageWorkflowContent(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	var triageContent string
	for _, f := range client.CommittedFiles[0].Files {
		if f.Path == ".github/workflows/triage.yml" {
			triageContent = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, triageContent, "triage.yml should have been written")

	raw, err := scaffold.FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)
	expected := string(scaffold.PrependManagedHeader(".github/workflows/triage.yml", raw))
	assert.Equal(t, expected, triageContent)
}

func TestWorkflowsLayer_Install_RepoMaintenanceContent(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	var maintenanceContent string
	for _, f := range client.CommittedFiles[0].Files {
		if f.Path == ".github/workflows/repo-maintenance.yml" {
			maintenanceContent = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, maintenanceContent, "repo-maintenance.yml should have been written")

	raw, err := scaffold.FullsendRepoFile(".github/workflows/repo-maintenance.yml")
	require.NoError(t, err)
	expected := string(scaffold.PrependManagedHeader(".github/workflows/repo-maintenance.yml", raw))
	assert.Equal(t, expected, maintenanceContent)
}

func TestWorkflowsLayer_Install_ManagedHeaders(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	for _, f := range client.CommittedFiles[0].Files {
		header := scaffold.ManagedHeader(f.Path)
		if header != "" {
			assert.True(t, strings.HasPrefix(string(f.Content), header),
				"installed file %s should start with managed header", f.Path)
		} else {
			assert.False(t, strings.Contains(string(f.Content), "managed by fullsend"),
				"installed file %s should NOT have a managed header", f.Path)
		}
	}
}

func TestWorkflowsLayer_Install_ProtectedBranchFallback(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	layer, buf := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "test-org/.fullsend/fullsend/scaffold-install", client.CreatedBranches[0])

	require.Len(t, client.CommittedFilesToBranch, 1)
	assert.Equal(t, "fullsend/scaffold-install", client.CommittedFilesToBranch[0].Branch)

	require.Len(t, client.CreatedProposals, 1)
	assert.Contains(t, client.CreatedProposals[0].Title, "fullsend")

	output := buf.String()
	assert.Contains(t, output, "protected")
	assert.Contains(t, output, "PR #1")
	assert.Contains(t, output, "Merge the PR")
}

func TestWorkflowsLayer_Install_ProtectedBranch_ExistingBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateBranch"] = fmt.Errorf("branch: %w", forge.ErrAlreadyExists)
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFilesToBranch, 1, "should proceed despite branch existing")
	require.Len(t, client.CreatedProposals, 1)
}

func TestWorkflowsLayer_Install_ProtectedBranch_CreateBranchFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateBranch"] = fmt.Errorf("forbidden")
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold branch")
}

func TestWorkflowsLayer_Install_ProtectedBranch_CommitToBranchFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CommitFilesToBranch"] = fmt.Errorf("server error")
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing scaffold files to branch")
}

func TestWorkflowsLayer_Install_ProtectedBranch_CreatePRFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("forbidden")
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold PR")
}

func TestWorkflowsLayer_Install_ProtectedBranch_DuplicatePR(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("PR: %w", forge.ErrAlreadyExists)
	layer, buf := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "already exists")
	assert.Contains(t, output, "Merge the PR")
}

func TestWorkflowsLayer_Install_ProtectedBranch_BranchUpToDate(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	noChange := false
	client.CommitFilesChanged = &noChange
	layer, buf := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.CreatedProposals, "should not create PR when branch files are unchanged")
	assert.Contains(t, buf.String(), "up to date")
}

func TestWorkflowsLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{{
			FullName:      "test-org/" + forge.ConfigRepoName,
			DefaultBranch: "main",
		}},
		Errors: map[string]error{
			"CommitFiles": errors.New("write failed"),
		},
	}
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")
}

func TestWorkflowsLayer_Install_ExecutableModes(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	modes := make(map[string]string)
	for _, f := range client.CommittedFiles[0].Files {
		modes[f.Path] = f.Mode
	}

	assert.Equal(t, "100644", modes[".github/workflows/triage.yml"])
	assert.Equal(t, "100644", modes["customized/agents/.gitkeep"])
	assert.Equal(t, "100644", modes["AGENTS.md"])

	for path, mode := range modes {
		assert.Equal(t, "100644", mode, "all installed files should be 100644 (no executables after layering): %s", path)
	}
}

func TestWorkflowsLayer_Uninstall_Noop(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	// No repos deleted, no files created
	assert.Empty(t, client.DeletedRepos)
	assert.Empty(t, client.CreatedFiles)
}

func TestWorkflowsLayer_Analyze_AllPresent(t *testing.T) {
	fileContents := map[string][]byte{
		"test-org/.fullsend/CODEOWNERS": []byte("* @admin-user"),
	}
	// Populate all scaffold files
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
	// Details should list what exists
	joined := strings.Join(report.Details, " ")
	assert.Contains(t, joined, "triage.yml")
	// WouldFix should list what's missing
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
