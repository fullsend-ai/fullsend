package steps

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestGivenFork_SetsWorldState(t *testing.T) {
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       &fakeForkSCM{forkRepo: "repo-fork"},
	}
	err := givenFork(w, "repo-fork")
	require.NoError(t, err)
	assert.Equal(t, "org", w.ForkOwner)
	assert.Equal(t, "repo-fork", w.ForkRepo)
}

func TestWhenForkPullRequestOpened_RequiresFork(t *testing.T) {
	w := &world.World{}
	err := whenForkPullRequestOpened(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fork created")
}

func TestWhenCommitPushedToForkPR_RequiresPR(t *testing.T) {
	w := &world.World{}
	err := whenCommitPushedToForkPR(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fork pull request opened")
}

// TestForkSteps_WorldStateTransitions verifies the full fork lifecycle:
// fork created -> PR opened -> commit pushed, checking world state after each step.
func TestForkSteps_WorldStateTransitions(t *testing.T) {
	scmDriver := &fakeForkSCM{forkRepo: "test-repo-fork", prNumber: 42}
	w := &world.World{
		Org:       "test-org",
		RepoOwner: "test-org",
		RepoName:  "test-repo",
		RepoFull:  "test-org/test-repo",
		SCM:       scmDriver,
	}

	// Step 1: Given a fork
	err := givenFork(w, "test-repo-fork")
	require.NoError(t, err)
	assert.Equal(t, "test-org", w.ForkOwner)
	assert.Equal(t, "test-repo-fork", w.ForkRepo)
	assert.True(t, scmDriver.createForkCalled, "CreateFork should have been called")

	// Step 2: When a fork pull request is opened
	err = whenForkPullRequestOpened(w)
	require.NoError(t, err)
	assert.Equal(t, 42, w.ForkPRNumber)
	assert.NotEmpty(t, w.ForkPRBranch)
	assert.False(t, w.ScenarioStart.IsZero())
	assert.True(t, scmDriver.commitToForkCalled, "CommitFileToFork should have been called")
	assert.True(t, scmDriver.createForkPRCalled, "CreateForkChangeProposal should have been called")

	// Step 3: When a commit is pushed to the fork pull request
	scmDriver.commitToForkCalled = false // reset to verify second call
	err = whenCommitPushedToForkPR(w)
	require.NoError(t, err)
	assert.True(t, scmDriver.commitToForkCalled, "CommitFileToFork should have been called again")
}

// fakeForkSCM implements scm.Driver for fork step unit tests.
type fakeForkSCM struct {
	forkRepo           string
	prNumber           int
	createForkCalled   bool
	commitToForkCalled bool
	createForkPRCalled bool
}

func (f *fakeForkSCM) CreateFork(_ context.Context, _, _, _ string) (string, error) {
	f.createForkCalled = true
	return f.forkRepo, nil
}

func (f *fakeForkSCM) CommitFileToFork(_ context.Context, _, _, _, _, _ string, _ []byte) error {
	f.commitToForkCalled = true
	return nil
}

func (f *fakeForkSCM) CreateForkChangeProposal(_ context.Context, _, _, _, _, _, _, _ string) (*forge.ChangeProposal, error) {
	f.createForkPRCalled = true
	return &forge.ChangeProposal{Number: f.prNumber, Head: "test-branch"}, nil
}

// Unused scm.Driver methods -- required for interface satisfaction.

func (f *fakeForkSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeForkSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}

func (f *fakeForkSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeForkSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeForkSCM) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}

func (f *fakeForkSCM) CommitFile(context.Context, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeForkSCM) CreateBranch(context.Context, string, string, string) error {
	return nil
}

func (f *fakeForkSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeForkSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeForkSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}

func (f *fakeForkSCM) CloseIssue(context.Context, string, string, int) error {
	return nil
}
