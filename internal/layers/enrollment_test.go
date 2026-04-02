package layers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newEnrollmentLayer(t *testing.T, client forge.Client, repos []string, defaults map[string]string) (*EnrollmentLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewEnrollmentLayer("test-org", client, repos, defaults, printer)
	return layer, &buf
}

func TestEnrollmentLayer_Name(t *testing.T) {
	layer, _ := newEnrollmentLayer(t, &forge.FakeClient{}, nil, nil)
	assert.Equal(t, "enrollment", layer.Name())
}

func TestEnrollmentLayer_Install_CreatesEnrollmentPRs(t *testing.T) {
	client := &forge.FakeClient{}
	repos := []string{"repo-a", "repo-b"}
	defaults := map[string]string{"repo-a": "main", "repo-b": "main"}
	layer, _ := newEnrollmentLayer(t, client, repos, defaults)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Should have created 2 branches
	require.Len(t, client.CreatedBranches, 2)
	assert.Contains(t, client.CreatedBranches, "test-org/repo-a/fullsend/onboard")
	assert.Contains(t, client.CreatedBranches, "test-org/repo-b/fullsend/onboard")

	// Should have created 2 files on branches
	require.Len(t, client.CreatedFiles, 2)
	for _, f := range client.CreatedFiles {
		assert.Equal(t, "test-org", f.Owner)
		assert.Equal(t, shimWorkflowPath, f.Path)
		assert.Equal(t, enrollBranch, f.Branch)
		// Verify shim workflow content uses dispatch token and repository_owner
		assert.Contains(t, string(f.Content), "FULLSEND_DISPATCH_TOKEN")
		assert.Contains(t, string(f.Content), "github.repository_owner")
	}

	// Should have created 2 PRs
	require.Len(t, client.CreatedProposals, 2)
	for _, pr := range client.CreatedProposals {
		assert.Equal(t, "Connect to fullsend agent pipeline", pr.Title)
	}
}

func TestEnrollmentLayer_Install_SkipsAlreadyEnrolled(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/repo-a/.github/workflows/fullsend.yaml": []byte("existing shim"),
		},
	}
	repos := []string{"repo-a", "repo-b"}
	defaults := map[string]string{"repo-a": "main", "repo-b": "main"}
	layer, _ := newEnrollmentLayer(t, client, repos, defaults)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Only repo-b should have been enrolled
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "test-org/repo-b/fullsend/onboard", client.CreatedBranches[0])

	require.Len(t, client.CreatedFiles, 1)
	assert.Equal(t, "repo-b", client.CreatedFiles[0].Repo)

	require.Len(t, client.CreatedProposals, 1)
}

func TestEnrollmentLayer_Install_ContinuesOnError(t *testing.T) {
	// Use a custom client that fails CreateFileOnBranch only for repo-a.
	// This simulates a real failure (e.g., permission denied) that should
	// trigger a warning for repo-a but not stop repo-b from enrolling.
	client := &perRepoFileErrorClient{
		FakeClient: &forge.FakeClient{},
		failRepo:   "repo-a",
	}
	repos := []string{"repo-a", "repo-b"}
	defaults := map[string]string{"repo-a": "main", "repo-b": "main"}
	layer, _ := newEnrollmentLayer(t, client, repos, defaults)

	err := layer.Install(context.Background())
	// Install itself should not return an error — it warns and continues
	require.NoError(t, err)

	// repo-b should still have been enrolled (branch + file + PR).
	// repo-a should have a branch but no file or PR.
	require.Len(t, client.CreatedFiles, 1)
	assert.Equal(t, "repo-b", client.CreatedFiles[0].Repo)

	require.Len(t, client.CreatedProposals, 1)
}

func TestEnrollmentLayer_Install_NoRepos(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newEnrollmentLayer(t, client, nil, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.CreatedBranches)
	assert.Empty(t, client.CreatedFiles)
	assert.Empty(t, client.CreatedProposals)
}

func TestEnrollmentLayer_Uninstall_Noop(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a"}, nil)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.CreatedBranches)
	assert.Empty(t, client.CreatedFiles)
	assert.Empty(t, client.CreatedProposals)
	assert.Empty(t, client.DeletedRepos)
}

func TestEnrollmentLayer_Analyze_AllEnrolled(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/repo-a/.github/workflows/fullsend.yaml": []byte("shim"),
			"test-org/repo-b/.github/workflows/fullsend.yaml": []byte("shim"),
		},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, _ := newEnrollmentLayer(t, client, repos, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Len(t, report.Details, 2)
	joined := strings.Join(report.Details, " ")
	assert.Contains(t, joined, "repo-a")
	assert.Contains(t, joined, "repo-b")
	assert.Empty(t, report.WouldInstall)
	assert.Empty(t, report.WouldFix)
}

func TestEnrollmentLayer_Analyze_NoneEnrolled(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, _ := newEnrollmentLayer(t, client, repos, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Empty(t, report.Details)
	assert.Len(t, report.WouldInstall, 2)
	joined := strings.Join(report.WouldInstall, " ")
	assert.Contains(t, joined, "repo-a")
	assert.Contains(t, joined, "repo-b")
}

func TestEnrollmentLayer_Analyze_Partial(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/repo-a/.github/workflows/fullsend.yaml": []byte("shim"),
		},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, _ := newEnrollmentLayer(t, client, repos, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)

	// Details should list enrolled repo
	require.Len(t, report.Details, 1)
	assert.Contains(t, report.Details[0], "repo-a")

	// WouldFix should list unenrolled repo
	require.Len(t, report.WouldFix, 1)
	assert.Contains(t, report.WouldFix[0], "repo-b")
}

// perRepoFileErrorClient wraps FakeClient but fails CreateFileOnBranch for a specific repo.
type perRepoFileErrorClient struct {
	*forge.FakeClient
	failRepo string
}

func (c *perRepoFileErrorClient) CreateFileOnBranch(_ context.Context, owner, repo, branch, path, message string, content []byte) error {
	if repo == c.failRepo {
		return fmt.Errorf("file write failed for %s", repo)
	}
	return c.FakeClient.CreateFileOnBranch(context.Background(), owner, repo, branch, path, message, content)
}

// GetFileContent delegates to the embedded FakeClient.
func (c *perRepoFileErrorClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	return c.FakeClient.GetFileContent(ctx, owner, repo, path)
}

// ListRepoPullRequests delegates to the embedded FakeClient.
func (c *perRepoFileErrorClient) ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	return c.FakeClient.ListRepoPullRequests(ctx, owner, repo)
}
