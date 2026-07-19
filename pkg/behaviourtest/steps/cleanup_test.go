package steps

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestShouldRemoveArtifactDir(t *testing.T) {
	t.Parallel()

	ciRoot := "/tmp/behaviour-artifacts"
	assert.False(t, shouldRemoveArtifactDir(ciRoot, ciRoot))
	assert.False(t, shouldRemoveArtifactDir(ciRoot+"/run-123", ciRoot))
	assert.True(t, shouldRemoveArtifactDir("/tmp/behaviour-artifacts-evil/run-123", ciRoot))
	assert.True(t, shouldRemoveArtifactDir("/var/tmp/local-run", ciRoot))
	assert.True(t, shouldRemoveArtifactDir("/tmp/local-run", ""))
}

func TestArtifactDirUnderCIRoot(t *testing.T) {
	t.Parallel()

	ciRoot := "/tmp/behaviour-artifacts"
	assert.True(t, artifactDirUnderCIRoot(ciRoot, ciRoot))
	assert.True(t, artifactDirUnderCIRoot(ciRoot+"/run-456", ciRoot))
	assert.False(t, artifactDirUnderCIRoot("/tmp/behaviour-artifacts-evil/run", ciRoot))
}

func TestCleanupScenario_ClosesForkPR(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 42,
		SCM:          scmDriver,
	}
	CleanupScenario(w)
	require.Len(t, scmDriver.closedIssues, 1)
	assert.Equal(t, "org", scmDriver.closedIssues[0].owner)
	assert.Equal(t, "repo", scmDriver.closedIssues[0].repo)
	assert.Equal(t, 42, scmDriver.closedIssues[0].number)
}

func TestCleanupScenario_ClosesForkPR_Error(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{closeIssueErr: fmt.Errorf("close failed")}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 42,
		SCM:          scmDriver,
		Logf:         func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "close fork PR #42")
}

func TestCleanupScenario_SkipsForkCleanupWhenNotSet(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       scmDriver,
	}
	CleanupScenario(w)
	assert.Empty(t, scmDriver.closedIssues)
}

func TestDeleteForkBranch_Success(t *testing.T) {
	t.Parallel()

	var receivedPath, receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var logged []string
	w := &world.World{
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	deleteForkBranch(context.Background(), srv.URL, "test-token", "org", "fork-repo", "test-branch", w)

	assert.Equal(t, "/repos/org/fork-repo/git/refs/heads/test-branch", receivedPath)
	assert.Equal(t, "Bearer test-token", receivedAuth)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "deleted fork branch test-branch on org/fork-repo")
}

func TestDeleteForkBranch_NotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var logged []string
	w := &world.World{
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	deleteForkBranch(context.Background(), srv.URL, "test-token", "org", "fork-repo", "gone-branch", w)

	// 404 is silently ignored — no log output.
	assert.Empty(t, logged)
}

func TestDeleteForkBranch_UnexpectedStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var logged []string
	w := &world.World{
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	deleteForkBranch(context.Background(), srv.URL, "test-token", "org", "fork-repo", "bad-branch", w)

	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "unexpected status 500")
}

func TestCleanupScenario_CallsDeleteForkBranch_WhenAllFieldsPresent(t *testing.T) {
	t.Parallel()

	// Verify that CleanupScenario closes the fork PR via SCM when all
	// fork fields are present. Branch deletion is tested separately
	// (TestDeleteForkBranch_*) with httptest, so we omit Token here to
	// skip the HTTP call and focus on the SCM path.
	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 10,
		ForkOwner:    "org",
		ForkRepo:     "fork-repo",
		ForkPRBranch: "test-branch",
		SCM:          scmDriver,
	}
	CleanupScenario(w)

	require.Len(t, scmDriver.closedIssues, 1)
	assert.Equal(t, 10, scmDriver.closedIssues[0].number)
}

func TestCleanupScenario_SkipsBranchDelete_WhenTokenMissing(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 10,
		ForkOwner:    "org",
		ForkRepo:     "fork-repo",
		ForkPRBranch: "test-branch",
		Token:        "", // no token — branch deletion should be skipped
		SCM:          scmDriver,
		Logf:         func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)

	// Fork PR should still be closed.
	require.Len(t, scmDriver.closedIssues, 1)

	// No branch deletion log because token was empty.
	for _, msg := range logged {
		assert.NotContains(t, msg, "fork branch", "branch deletion should not be attempted without a token")
	}
}

// fakeCleanupSCM implements scm.Driver for cleanup unit tests.
type fakeCleanupSCM struct {
	closedIssues  []closedIssueRecord
	closeIssueErr error
}

type closedIssueRecord struct {
	owner  string
	repo   string
	number int
}

func (f *fakeCleanupSCM) CloseIssue(_ context.Context, owner, repo string, number int) error {
	if f.closeIssueErr != nil {
		return f.closeIssueErr
	}
	f.closedIssues = append(f.closedIssues, closedIssueRecord{owner: owner, repo: repo, number: number})
	return nil
}

// Unused scm.Driver methods — required for interface satisfaction.

func (f *fakeCleanupSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}

func (f *fakeCleanupSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) CommitFile(context.Context, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeCleanupSCM) CreateBranch(context.Context, string, string, string) error {
	return nil
}

func (f *fakeCleanupSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeCleanupSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}

func (f *fakeCleanupSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (f *fakeCleanupSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeCleanupSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
