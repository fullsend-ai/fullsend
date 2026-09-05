package steps

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeCleanupDriver tracks MarkDeleted calls.
type fakeCleanupDriver struct {
	install.Driver
	marked []string
}

func (f *fakeCleanupDriver) MarkDeleted(name string) {
	f.marked = append(f.marked, name)
}

// TODO: remove skip after reverting KeepRepos hardcode in driver.go.
func skipIfCleanupDisabled(t *testing.T) {
	t.Helper()
	if install.KeepRepos() {
		t.Skip("KeepRepos() hardcoded to true for debugging")
	}
}

// --- Main repo cleanup tests ---

func TestCleanupScenario_DeletesMainRepo(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	driver := &fakeCleanupDriver{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "bt-abc123",
		SCM:       scmDriver,
		Driver:    driver,
	}
	err := CleanupScenario(w)

	require.NoError(t, err)
	require.Len(t, scmDriver.deletedRepos, 1)
	assert.Equal(t, "org", scmDriver.deletedRepos[0].owner)
	assert.Equal(t, "bt-abc123", scmDriver.deletedRepos[0].repo)
	assert.Equal(t, []string{"bt-abc123"}, driver.marked)
}

func TestCleanupScenario_DeletesMainRepo_NilDriver(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "bt-abc123",
		SCM:       scmDriver,
	}
	err := CleanupScenario(w)

	require.NoError(t, err)
	require.Len(t, scmDriver.deletedRepos, 1)
	assert.Equal(t, "bt-abc123", scmDriver.deletedRepos[0].repo)
}

func TestCleanupScenario_MainRepoError_Returned(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	scmDriver := &fakeCleanupSCM{deleteRepoErr: fmt.Errorf("server error")}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "bt-abc123",
		SCM:       scmDriver,
		Driver:    &fakeCleanupDriver{},
		Logf:      t.Logf,
	}
	err := CleanupScenario(w)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "server error")
}

func TestCleanupScenario_MainRepoNotFound_MarkedDeleted(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	driver := &fakeCleanupDriver{}
	scmDriver := &fakeCleanupSCM{deleteRepoErr: fmt.Errorf("delete repo: %w", forge.ErrNotFound)}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "bt-abc123",
		SCM:       scmDriver,
		Driver:    driver,
	}
	err := CleanupScenario(w)

	require.NoError(t, err)
	assert.Equal(t, []string{"bt-abc123"}, driver.marked,
		"not-found is a successful delete — repo should be marked")
}

// --- Fork repo cleanup tests ---

func TestCleanupScenario_DeletesForkRepo(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		SCM:       scmDriver,
		Driver:    &fakeCleanupDriver{},
	}
	err := CleanupScenario(w)

	require.NoError(t, err)
	require.Len(t, scmDriver.deletedRepos, 2) // main + fork
	assert.Equal(t, "repo-fork", scmDriver.deletedRepos[1].repo)
}

func TestCleanupScenario_DeleteForkRepoNotFound_SilentlyIgnored(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{deleteRepoErr: fmt.Errorf("delete repo: %w", forge.ErrNotFound)}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		SCM:       scmDriver,
		Driver:    &fakeCleanupDriver{},
		Logf:      func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	err := CleanupScenario(w)
	require.NoError(t, err)

	for _, msg := range logged {
		assert.NotContains(t, msg, "fork repo", "ErrNotFound should be silently ignored")
	}
}

func TestCleanupScenario_SkipsForkRepoDelete_WhenForkRepoEqualsRepoName(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		ForkOwner: "org",
		ForkRepo:  "repo",
		SCM:       scmDriver,
		Driver:    &fakeCleanupDriver{},
	}
	err := CleanupScenario(w)
	require.NoError(t, err)

	require.Len(t, scmDriver.deletedRepos, 1, "only main repo should be deleted")
	assert.Equal(t, "repo", scmDriver.deletedRepos[0].repo)
}

func TestCleanupScenario_SkipsForkRepoDelete_WhenFieldsMissing(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	tests := []struct {
		name  string
		world *world.World
	}{
		{
			name: "missing ForkOwner",
			world: &world.World{
				RepoOwner: "org",
				RepoName:  "repo",
				ForkRepo:  "repo-fork",
				SCM:       &fakeCleanupSCM{},
				Driver:    &fakeCleanupDriver{},
			},
		},
		{
			name: "missing ForkRepo",
			world: &world.World{
				RepoOwner: "org",
				RepoName:  "repo",
				ForkOwner: "org",
				SCM:       &fakeCleanupSCM{},
				Driver:    &fakeCleanupDriver{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scm := tt.world.SCM.(*fakeCleanupSCM)
			err := CleanupScenario(tt.world)
			require.NoError(t, err)
			require.Len(t, scm.deletedRepos, 1, "only main repo should be deleted")
		})
	}
}

// --- URL harness hosting repo cleanup tests ---

func TestCleanupScenario_DeletesHostingRepo(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "bt-abc123-url-harness-host",
		SCM:                 scmDriver,
		Driver:              &fakeCleanupDriver{},
	}
	err := CleanupScenario(w)

	require.NoError(t, err)
	require.Len(t, scmDriver.deletedRepos, 2) // main + harness
	assert.Equal(t, "bt-abc123-url-harness-host", scmDriver.deletedRepos[1].repo)
}

func TestCleanupScenario_SkipsHostingRepoDelete_WhenEqualsRepoName(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "repo",
		SCM:                 scmDriver,
		Driver:              &fakeCleanupDriver{},
	}
	err := CleanupScenario(w)
	require.NoError(t, err)

	require.Len(t, scmDriver.deletedRepos, 1, "only main repo should be deleted")
}

func TestCleanupScenario_SkipsHostingRepoDelete_WhenFieldsMissing(t *testing.T) {
	skipIfCleanupDisabled(t)
	t.Parallel()

	tests := []struct {
		name  string
		world *world.World
	}{
		{
			name: "missing URLHarnessRepoOwner",
			world: &world.World{
				RepoOwner:          "org",
				RepoName:           "repo",
				URLHarnessRepoName: "host-repo",
				SCM:                &fakeCleanupSCM{},
				Driver:             &fakeCleanupDriver{},
			},
		},
		{
			name: "missing URLHarnessRepoName",
			world: &world.World{
				RepoOwner:           "org",
				RepoName:            "repo",
				URLHarnessRepoOwner: "org",
				SCM:                 &fakeCleanupSCM{},
				Driver:              &fakeCleanupDriver{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scm := tt.world.SCM.(*fakeCleanupSCM)
			err := CleanupScenario(tt.world)
			require.NoError(t, err)
			require.Len(t, scm.deletedRepos, 1, "only main repo should be deleted")
		})
	}
}

// --- E2E_KEEP_REPOS tests ---

func TestCleanupScenario_KeepRepos_SkipsAllDeletion(t *testing.T) {
	t.Setenv("E2E_KEEP_REPOS", "true")

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		ForkOwner:           "org",
		ForkRepo:            "repo-fork",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host-repo",
		SCM:                 scmDriver,
	}
	err := CleanupScenario(w)
	require.NoError(t, err)

	assert.Empty(t, scmDriver.deletedRepos, "no repos should be deleted when E2E_KEEP_REPOS is set")
}

// fakeCleanupSCM implements scm.Driver for cleanup and runtime unit tests.
// Fields beyond cleanup (fileContent, commitFile*) are used by
// recordingSCM in runtime_test.go which embeds this type.
type fakeCleanupSCM struct {
	deletedRepos     []deletedRepoRecord
	deleteRepoErr    error
	commitFileCalled bool
	commitFileErr    error
	fileContent      []byte
	getFileErr       error
}

type deletedRepoRecord struct {
	owner string
	repo  string
}

func (f *fakeCleanupSCM) CloseIssue(context.Context, string, string, int) error {
	return nil
}

func (f *fakeCleanupSCM) DeleteRepo(_ context.Context, owner, repo string) error {
	if f.deleteRepoErr != nil {
		return f.deleteRepoErr
	}
	f.deletedRepos = append(f.deletedRepos, deletedRepoRecord{owner: owner, repo: repo})
	return nil
}

// Unused scm.Driver methods — required for interface satisfaction.

func (f *fakeCleanupSCM) DeleteBranch(context.Context, string, string, string) error {
	return nil
}

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
	return f.fileContent, f.getFileErr
}

func (f *fakeCleanupSCM) CommitFile(_ context.Context, _, _, _, _ string, _ []byte) error {
	f.commitFileCalled = true
	return f.commitFileErr
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

func (f *fakeCleanupSCM) CreateRepo(context.Context, string, string, string) error {
	return nil
}

func (f *fakeCleanupSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) EnsureRepoPublic(context.Context, string, string) error {
	return nil
}

func (f *fakeCleanupSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}

func (f *fakeCleanupSCM) GetBranchRef(context.Context, string, string, string) (string, error) {
	return "abc123", nil
}

func (f *fakeCleanupSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (f *fakeCleanupSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeCleanupSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, nil
}
