package steps

import (
	"context"
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

func TestCleanupScenario_IsNoOp(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	driver := &fakeCleanupDriver{}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "bt-abc123",
		ForkOwner:           "org",
		ForkRepo:            "bt-abc123-fork",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "bt-abc123-url-harness-host",
		SCM:                 scmDriver,
		Driver:              driver,
	}
	err := CleanupScenario(w)

	require.NoError(t, err)
	assert.Empty(t, scmDriver.deletedRepos, "CleanupScenario should not delete any repos (deferred cleanup)")
	assert.Empty(t, driver.marked, "CleanupScenario should not mark any repos deleted")
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
