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

func newWorkflowsLayer(t *testing.T, client *forge.FakeClient) (*WorkflowsLayer, *bytes.Buffer) {
	t.Helper()
	return newWorkflowsLayerWithRoles(t, client, nil)
}

func newWorkflowsLayerWithRoles(t *testing.T, client *forge.FakeClient, roles []string) (*WorkflowsLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user", roles)
	return layer, &buf
}

func TestWorkflowsLayer_Name(t *testing.T) {
	layer, _ := newWorkflowsLayer(t, &forge.FakeClient{})
	assert.Equal(t, "workflows", layer.Name())
}

func TestWorkflowsLayer_Install_WritesAllFiles(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Should have created scaffold files + CODEOWNERS in the .fullsend repo
	require.True(t, len(client.CreatedFiles) >= 23,
		"expected at least 23 files (22 scaffold + CODEOWNERS), got %d", len(client.CreatedFiles))

	paths := make(map[string]string) // path -> content
	for _, f := range client.CreatedFiles {
		assert.Equal(t, "test-org", f.Owner)
		assert.Equal(t, ".fullsend", f.Repo)
		paths[f.Path] = string(f.Content)
	}

	assert.Contains(t, paths, ".github/workflows/triage.yml")
	assert.Contains(t, paths, ".github/workflows/code.yml")
	assert.Contains(t, paths, ".github/workflows/review.yml")
	assert.Contains(t, paths, ".github/workflows/repo-maintenance.yml")
	assert.Contains(t, paths, "CODEOWNERS")

	// Verify CODEOWNERS contains the authenticated user
	assert.Contains(t, paths["CODEOWNERS"], "admin-user")
}

func TestWorkflowsLayer_Install_TriageWorkflowContent(t *testing.T) {
	client := &forge.FakeClient{}
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

	// Verify it matches the scaffold content
	expected, err := scaffold.FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)
	assert.Equal(t, string(expected), triageContent)
}

func TestWorkflowsLayer_Install_RepoMaintenanceContent(t *testing.T) {
	client := &forge.FakeClient{}
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

	// Verify it matches the scaffold content
	expected, err := scaffold.FullsendRepoFile(".github/workflows/repo-maintenance.yml")
	require.NoError(t, err)
	assert.Equal(t, string(expected), maintenanceContent)
}

func TestWorkflowsLayer_Install_CODEOWNERSOptional(t *testing.T) {
	// Use a custom client that only errors on CODEOWNERS path
	client := &codeownersErrorClient{FakeClient: &forge.FakeClient{}}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user", nil)

	err := layer.Install(context.Background())
	// Install should succeed even though CODEOWNERS write failed
	require.NoError(t, err)

	// All scaffold files should have been created (CODEOWNERS excluded since it failed)
	assert.Len(t, client.created, 45)
}

func TestWorkflowsLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{
			"CreateOrUpdateFile": errors.New("write failed"),
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
	assert.Len(t, report.Details, 46)
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
	assert.Len(t, report.WouldInstall, 46)
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

func TestWorkflowsLayer_DefaultRoles_ExcludesScribe(t *testing.T) {
	defaultRoles := []string{"fullsend", "triage", "coder", "review"}
	client := &forge.FakeClient{}
	layer, _ := newWorkflowsLayerWithRoles(t, client, defaultRoles)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	for _, f := range client.CreatedFiles {
		assert.NotContains(t, f.Path, "scribe",
			"scribe file %s should not be installed with default roles", f.Path)
	}
}

func TestWorkflowsLayer_WithScribeRole_IncludesScribe(t *testing.T) {
	rolesWithScribe := []string{"fullsend", "triage", "coder", "review", "scribe"}
	client := &forge.FakeClient{}
	layer, _ := newWorkflowsLayerWithRoles(t, client, rolesWithScribe)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, f := range client.CreatedFiles {
		paths[f.Path] = true
	}
	assert.True(t, paths[".github/workflows/scribe.yml"],
		"scribe workflow should be installed when scribe role is configured")
	assert.True(t, paths["agents/scribe.md"],
		"scribe agent prompt should be installed when scribe role is configured")
}

func TestWorkflowsLayer_NilRoles_IncludesAll(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newWorkflowsLayerWithRoles(t, client, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, f := range client.CreatedFiles {
		paths[f.Path] = true
	}
	assert.True(t, paths[".github/workflows/scribe.yml"],
		"nil roles should install all files including scribe")
}

func TestScaffoldDoesNotIncludeOldPlaceholders(t *testing.T) {
	err := scaffold.WalkFullsendRepo(func(path string, _ []byte) error {
		assert.NotEqual(t, ".github/workflows/agent.yaml", path,
			"scaffold should not include old agent.yaml placeholder")
		assert.NotEqual(t, ".github/workflows/repo-onboard.yaml", path,
			"scaffold should not include old repo-onboard.yaml placeholder")
		return nil
	})
	require.NoError(t, err)
}

// codeownersErrorClient is a test double that errors only on CODEOWNERS writes.
// It embeds FakeClient for all other methods.
type codeownersErrorClient struct {
	*forge.FakeClient
	created []forge.FileRecord
}

func (c *codeownersErrorClient) CreateOrUpdateFile(_ context.Context, owner, repo, path, message string, content []byte) error {
	if path == "CODEOWNERS" {
		return errors.New("codeowners write failed")
	}
	c.created = append(c.created, forge.FileRecord{
		Owner:   owner,
		Repo:    repo,
		Path:    path,
		Message: message,
		Content: content,
	})
	return nil
}
