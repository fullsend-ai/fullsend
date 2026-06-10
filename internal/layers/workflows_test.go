package layers

import (
	"bytes"
	"context"
	"errors"
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
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user", "test-version", vendored)
	return layer, &buf
}

func TestWorkflowsLayer_Name(t *testing.T) {
	layer, _ := newWorkflowsLayer(t, forge.NewFakeClient(), false)
	assert.Equal(t, "workflows", layer.Name())
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

	assert.Contains(t, triageContent, "fullsend-ai/fullsend/.github/workflows/reusable-triage.yml@v0")
	assert.NotContains(t, triageContent, "distribution_mode")
	assert.NotContains(t, triageContent, "fullsend_ai_repo:")
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
	assert.NotContains(t, triageContent, "fullsend-ai/fullsend/")
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

	expected, err := scaffold.FullsendRepoFile(".github/workflows/repo-maintenance.yml")
	require.NoError(t, err)
	assert.Equal(t, string(expected), maintenanceContent)
}

func TestWorkflowsLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
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
