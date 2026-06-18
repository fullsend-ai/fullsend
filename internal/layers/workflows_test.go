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

func newWorkflowsLayer(t *testing.T, client *forge.FakeClient, vendored bool) (*WorkflowsLayer, *bytes.Buffer) {
	t.Helper()
	ensureFakeConfigRepo(client)
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user", "test-version", vendored)
	return layer, &buf
}

func ensureFakeConfigRepo(client *forge.FakeClient) {
	fullName := "test-org/" + forge.ConfigRepoName
	for _, r := range client.Repos {
		if r.FullName == fullName {
			goto ensureConfig
		}
	}
	client.Repos = append(client.Repos, forge.Repository{
		Name:          forge.ConfigRepoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
ensureConfig:
	if client.FileContents == nil {
		client.FileContents = map[string][]byte{}
	}
	configKey := fullName + "/config.yaml"
	if _, ok := client.FileContents[configKey]; !ok {
		client.FileContents[configKey] = []byte("repos: {}\n")
	}
}

func TestWorkflowsLayer_Name(t *testing.T) {
	layer, _ := newWorkflowsLayer(t, forge.NewFakeClient(), false)
	assert.Equal(t, "workflows", layer.Name())
}

func TestWorkflowsLayer_RequiredScopes(t *testing.T) {
	layer, _ := newWorkflowsLayer(t, forge.NewFakeClient(), false)
	assert.Equal(t, []string{"repo", "workflow"}, layer.RequiredScopes(OpInstall))
	assert.Nil(t, layer.RequiredScopes(OpUninstall))
	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpAnalyze))
}

func TestWorkflowsLayer_Install_WritesAllFiles(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)

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
	assert.Contains(t, paths, "CODEOWNERS")
	assert.Contains(t, paths["CODEOWNERS"], "admin-user")

	require.Len(t, client.CreatedFiles, 1)
	assert.Equal(t, "config.yaml", client.CreatedFiles[0].Path)
	assert.Equal(t, "chore: activate fullsend workflows", client.CreatedFiles[0].Message)
}

func TestWorkflowsLayer_Install_ActivatesRepoMaintenance(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["test-org/.fullsend/config.yaml"] = []byte("repos: {}\n")
	layer, buf := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedFiles, 1)
	assert.Equal(t, "config.yaml", client.CreatedFiles[0].Path)
	assert.Equal(t, "chore: activate fullsend workflows", client.CreatedFiles[0].Message)
	assert.Contains(t, buf.String(), "Activated repo-maintenance workflow")
}

func TestWorkflowsLayer_Install_ActivateRepoMaintenanceFailure(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["test-org/.fullsend/config.yaml"] = []byte("repos: {}\n")
	client.Errors = map[string]error{
		"CreateOrUpdateFile": errors.New("branch protected"),
	}
	layer, buf := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "repo-maintenance workflow was not activated automatically")
}

func TestWorkflowsLayer_Install_TriageWorkflowContent(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client, false)

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
	rendered, err := scaffold.RenderTemplate(".github/workflows/triage.yml", raw, scaffold.RenderOptionsForInstall(false, false))
	require.NoError(t, err)
	expected := string(scaffold.PrependManagedHeader(".github/workflows/triage.yml", rendered))
	assert.Equal(t, expected, triageContent)
	assert.NotContains(t, triageContent, "distribution_mode")
	assert.NotContains(t, triageContent, "fullsend_ai_repo:")
}

func TestWorkflowsLayer_Install_CombinedVendorCommit(t *testing.T) {
	client := forge.NewFakeClient()
	ensureFakeConfigRepo(client)
	collectFn := func(_ context.Context, _ *ui.Printer, owner, repo string) ([]forge.TreeFile, int, error) {
		assert.Equal(t, "test-org", owner)
		assert.Equal(t, forge.ConfigRepoName, repo)
		return []forge.TreeFile{
			{Path: "bin/fullsend", Content: []byte("bin"), Mode: "100755"},
			{Path: ".defaults/action.yml", Content: []byte("marker"), Mode: "100644"},
		}, 1, nil
	}
	layer := NewWorkflowsLayer("test-org", client, ui.New(&bytes.Buffer{}), "admin-user", "test-version", true)
	layer = layer.WithVendorCollect(collectFn)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	paths := make(map[string]struct{})
	for _, f := range client.CommittedFiles[0].Files {
		paths[f.Path] = struct{}{}
	}
	assert.Contains(t, paths, ".github/workflows/triage.yml")
	assert.Contains(t, paths, "bin/fullsend")
	assert.Contains(t, paths, ".defaults/action.yml")
}

func TestWorkflowsLayer_Install_VendoredUsesLocalReusablePaths(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client, true)

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

	assert.Contains(t, triageContent, "uses: ./.github/workflows/reusable-triage.yml")
	assert.NotContains(t, triageContent, "uses: fullsend-ai/fullsend/")
	assert.NotContains(t, triageContent, "distribution_mode")
}

func TestWorkflowsLayer_Install_RepoMaintenanceContent(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client, false)

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
	rendered, err := scaffold.RenderTemplate(".github/workflows/repo-maintenance.yml", raw, scaffold.RenderOptionsForInstall(false, false))
	require.NoError(t, err)
	expected := string(scaffold.PrependManagedHeader(".github/workflows/repo-maintenance.yml", rendered))
	assert.Equal(t, expected, maintenanceContent)
}

func TestWorkflowsLayer_Install_ManagedHeaders(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client, false)

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
	layer, buf := newWorkflowsLayer(t, client, false)

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
	layer, _ := newWorkflowsLayer(t, client, false)

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
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold branch")
}

func TestWorkflowsLayer_Install_ProtectedBranch_CommitToBranchFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CommitFilesToBranch"] = fmt.Errorf("server error")
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing scaffold files to branch")
}

func TestWorkflowsLayer_Install_ProtectedBranch_ScaffoldBranchAlsoProtected(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CommitFilesToBranch"] = fmt.Errorf("%w: scaffold branch also protected", forge.ErrBranchProtected)
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scaffold branch")
	assert.Contains(t, err.Error(), "configure branch protection")
}

func TestWorkflowsLayer_Install_ProtectedBranch_CreatePRFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("forbidden")
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold PR")
}

func TestWorkflowsLayer_Install_ProtectedBranch_DuplicatePR(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "test-org/.fullsend", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("PR: %w", forge.ErrAlreadyExists)
	layer, buf := newWorkflowsLayer(t, client, false)

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
	client.Errors["CreateChangeProposal"] = fmt.Errorf("PR: %w", forge.ErrAlreadyExists)
	noChange := false
	client.CommitFilesChanged = &noChange
	layer, buf := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "up to date")
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
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")
}

func TestWorkflowsLayer_Install_ExecutableModes(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	modes := make(map[string]string)
	for _, f := range client.CommittedFiles[0].Files {
		modes[f.Path] = f.Mode
	}

	assert.Equal(t, "100644", modes[".github/workflows/triage.yml"])
	assert.Equal(t, "100644", modes["customized/agents/.gitkeep"])
	assert.Equal(t, "100644", modes["AGENTS.md"])
}

func TestWorkflowsLayer_Uninstall_Noop(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newWorkflowsLayer(t, client, false)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.DeletedRepos)
	assert.Empty(t, client.CreatedFiles)
}

func TestWorkflowsLayer_Analyze_AllPresent(t *testing.T) {
	managed, err := scaffold.ManagedPaths(false, "")
	require.NoError(t, err)

	fileContents := map[string][]byte{
		"test-org/.fullsend/CODEOWNERS": []byte("* @admin-user"),
	}
	for _, path := range managed {
		fileContents["test-org/.fullsend/"+path] = []byte("content")
	}

	client := &forge.FakeClient{FileContents: fileContents}
	layer, _ := newWorkflowsLayer(t, client, false)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "workflows", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Len(t, report.Details, len(managed)+1)
}

func TestWorkflowsLayer_Analyze_NonePresent(t *testing.T) {
	managed, err := scaffold.ManagedPaths(false, "")
	require.NoError(t, err)

	client := &forge.FakeClient{FileContents: map[string][]byte{}}
	layer, _ := newWorkflowsLayer(t, client, false)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "workflows", report.Name)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Len(t, report.WouldInstall, len(managed)+1)
}

func TestWorkflowsLayer_Analyze_WithVendoredMarkerUsesEmbedOnly(t *testing.T) {
	managed, err := scaffold.ManagedPaths(false, "")
	require.NoError(t, err)

	fileContents := map[string][]byte{
		"test-org/.fullsend/CODEOWNERS":                            []byte("* @admin-user"),
		"test-org/.fullsend/.defaults/action.yml":                  []byte("marker"),
		"test-org/.fullsend/bin/fullsend":                          []byte("binary"),
		"test-org/.fullsend/.github/workflows/reusable-triage.yml": []byte("reusable"),
	}
	for _, path := range managed {
		fileContents["test-org/.fullsend/"+path] = []byte("content")
	}

	client := &forge.FakeClient{FileContents: fileContents}
	layer, _ := newWorkflowsLayer(t, client, true)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusInstalled, report.Status)
	joined := strings.Join(report.Details, " ")
	assert.NotContains(t, joined, ".defaults/action.yml")
	assert.NotContains(t, joined, "reusable-triage.yml")
}

func TestWorkflowsLayer_Analyze_Partial(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/.github/workflows/triage.yml": []byte("triage workflow"),
		},
	}
	layer, _ := newWorkflowsLayer(t, client, false)

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

func TestManagedPathsMatchLayeredScaffold(t *testing.T) {
	managed, err := scaffold.ManagedPaths(false, "")
	require.NoError(t, err)

	var scaffoldPaths []string
	err = scaffold.WalkFullsendRepo(func(path string, _ []byte) error {
		scaffoldPaths = append(scaffoldPaths, path)
		return nil
	})
	require.NoError(t, err)

	for _, path := range scaffoldPaths {
		assert.Contains(t, managed, path, "managed paths should include scaffold file %s", path)
	}
}

func TestManagedVendoredContentPathsFromEmbed(t *testing.T) {
	paths, err := scaffold.ManagedVendoredContentPaths("")
	require.NoError(t, err)

	assert.Contains(t, paths, ".github/workflows/reusable-triage.yml")
	assert.Contains(t, paths, ".defaults/internal/scaffold/fullsend-repo/agents/triage.md")
	assert.Contains(t, paths, scaffold.VendoredMarkerPath())
}
