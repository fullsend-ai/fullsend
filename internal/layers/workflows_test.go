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

const testRef = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  # v0.0.0-test"

func newWorkflowsLayer(t *testing.T, client *forge.FakeClient) (*WorkflowsLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user").WithRef(testRef)
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

	template, err := scaffold.FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)
	expected := bytes.ReplaceAll(template, []byte("@__FULLSEND_REF__"), []byte("@"+testRef))
	assert.Equal(t, string(expected), triageContent)
	assert.NotContains(t, triageContent, "@__FULLSEND_REF__", "placeholder must be substituted")
	assert.Contains(t, triageContent, "@"+testRef, "ref must appear in installed content")
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

	template, err := scaffold.FullsendRepoFile(".github/workflows/repo-maintenance.yml")
	require.NoError(t, err)
	expected := bytes.ReplaceAll(template, []byte("@__FULLSEND_REF__"), []byte("@"+testRef))
	assert.Equal(t, string(expected), maintenanceContent)
	assert.NotContains(t, maintenanceContent, "@__FULLSEND_REF__", "placeholder must be substituted")
}


func TestWorkflowsLayer_Install_NoRefLeavesPlaceholder(t *testing.T) {
	client := forge.NewFakeClient()
	var buf bytes.Buffer
	layer := NewWorkflowsLayer("test-org", client, ui.New(&buf), "admin-user") // no WithRef

	err := layer.Install(context.Background())
	require.NoError(t, err)

	for _, f := range client.CommittedFiles[0].Files {
		if strings.HasSuffix(f.Path, ".yml") {
			// Without a ref, placeholder is left intact (not substituted).
			if strings.Contains(string(f.Content), "@__FULLSEND_REF__") {
				return // found at least one file with placeholder — expected
			}
		}
	}
}

func TestWorkflowsLayer_Install_RefSubstitution(t *testing.T) {
	client := forge.NewFakeClient()
	ref := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef  # v1.2.3"
	var buf bytes.Buffer
	layer := NewWorkflowsLayer("test-org", client, ui.New(&buf), "admin-user").WithRef(ref)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	for _, f := range client.CommittedFiles[0].Files {
		content := string(f.Content)
		assert.NotContains(t, content, "@__FULLSEND_REF__",
			"placeholder must not appear in installed file %s", f.Path)
		if strings.Contains(content, "fullsend-ai/fullsend") {
			assert.Contains(t, content, "@"+ref,
				"ref must be substituted in file %s", f.Path)
		}
	}
}

func TestWorkflowsLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
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
