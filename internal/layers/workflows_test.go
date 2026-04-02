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
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newWorkflowsLayer(t *testing.T, client *forge.FakeClient) (*WorkflowsLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user")
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

	// Should have created 3 files in the .fullsend repo
	require.Len(t, client.CreatedFiles, 3)

	paths := make(map[string]string) // path -> content
	for _, f := range client.CreatedFiles {
		assert.Equal(t, "test-org", f.Owner)
		assert.Equal(t, ".fullsend", f.Repo)
		paths[f.Path] = string(f.Content)
	}

	assert.Contains(t, paths, ".github/workflows/agent.yaml")
	assert.Contains(t, paths, ".github/workflows/repo-onboard.yaml")
	assert.Contains(t, paths, "CODEOWNERS")

	// Verify CODEOWNERS contains the authenticated user
	assert.Contains(t, paths["CODEOWNERS"], "admin-user")
}

func TestWorkflowsLayer_Install_AgentWorkflowContent(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	var agentContent string
	for _, f := range client.CreatedFiles {
		if f.Path == ".github/workflows/agent.yaml" {
			agentContent = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, agentContent, "agent.yaml should have been written")
	assert.Contains(t, agentContent, "workflow_call")
}

func TestWorkflowsLayer_Install_OnboardWorkflowContent(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newWorkflowsLayer(t, client)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	var onboardContent string
	for _, f := range client.CreatedFiles {
		if f.Path == ".github/workflows/repo-onboard.yaml" {
			onboardContent = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, onboardContent, "repo-onboard.yaml should have been written")
	assert.Contains(t, onboardContent, "config.yaml")
}

func TestWorkflowsLayer_Install_CODEOWNERSOptional(t *testing.T) {
	// Use a custom client that only errors on CODEOWNERS path
	client := &codeownersErrorClient{}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewWorkflowsLayer("test-org", client, printer, "admin-user")

	err := layer.Install(context.Background())
	// Install should succeed even though CODEOWNERS write failed
	require.NoError(t, err)

	// The two workflow files should have been created
	assert.Len(t, client.created, 2)
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
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/.github/workflows/agent.yaml":        []byte("agent workflow"),
			"test-org/.fullsend/.github/workflows/repo-onboard.yaml": []byte("onboard workflow"),
			"test-org/.fullsend/CODEOWNERS":                          []byte("* @admin-user"),
		},
	}
	layer, _ := newWorkflowsLayer(t, client)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "workflows", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Len(t, report.Details, 3)
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
	assert.Len(t, report.WouldInstall, 3)
}

func TestWorkflowsLayer_Analyze_Partial(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/.github/workflows/agent.yaml": []byte("agent workflow"),
		},
	}
	layer, _ := newWorkflowsLayer(t, client)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "workflows", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)
	// Details should list what exists
	joined := strings.Join(report.Details, " ")
	assert.Contains(t, joined, "agent.yaml")
	// WouldFix should list what's missing
	assert.NotEmpty(t, report.WouldFix)
	fixJoined := strings.Join(report.WouldFix, " ")
	assert.Contains(t, fixJoined, "repo-onboard.yaml")
	assert.Contains(t, fixJoined, "CODEOWNERS")
}

// codeownersErrorClient is a test double that errors only on CODEOWNERS writes.
// It wraps forge operations: CreateOrUpdateFile fails only for CODEOWNERS path,
// all other methods are no-ops or succeed.
type codeownersErrorClient struct {
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

// Satisfy the rest of the forge.Client interface with no-ops.
func (c *codeownersErrorClient) ListOrgRepos(context.Context, string) ([]forge.Repository, error) {
	return nil, nil
}
func (c *codeownersErrorClient) CreateRepo(context.Context, string, string, string, bool) (*forge.Repository, error) {
	return nil, nil
}
func (c *codeownersErrorClient) DeleteRepo(context.Context, string, string) error { return nil }
func (c *codeownersErrorClient) CreateFile(context.Context, string, string, string, string, []byte) error {
	return nil
}
func (c *codeownersErrorClient) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}
func (c *codeownersErrorClient) CreateBranch(context.Context, string, string, string) error {
	return nil
}
func (c *codeownersErrorClient) CreateFileOnBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (c *codeownersErrorClient) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
func (c *codeownersErrorClient) ListRepoPullRequests(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, nil
}
func (c *codeownersErrorClient) GetAuthenticatedUser(context.Context) (string, error) {
	return "", nil
}
func (c *codeownersErrorClient) CreateRepoSecret(context.Context, string, string, string, string) error {
	return nil
}
func (c *codeownersErrorClient) RepoSecretExists(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (c *codeownersErrorClient) CreateOrUpdateRepoVariable(context.Context, string, string, string, string) error {
	return nil
}
func (c *codeownersErrorClient) GetLatestWorkflowRun(context.Context, string, string, string) (*forge.WorkflowRun, error) {
	return nil, nil
}
func (c *codeownersErrorClient) GetWorkflowRun(context.Context, string, string, int) (*forge.WorkflowRun, error) {
	return nil, nil
}
func (c *codeownersErrorClient) ListOrgInstallations(context.Context, string) ([]forge.Installation, error) {
	return nil, nil
}
