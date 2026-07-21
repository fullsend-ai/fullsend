package steps

import (
	"context"
	"fmt"
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

func TestGivenFork_AutoFillsRepoFromInstall(t *testing.T) {
	w := &world.World{
		Org:     "auto-org",
		Install: &fakeInstallState{testRepo: "auto-repo"},
		SCM:     &fakeForkSCM{forkRepo: "auto-repo-fork"},
	}
	err := givenFork(w, "auto-repo-fork")
	require.NoError(t, err)
	assert.Equal(t, "auto-org", w.RepoOwner)
	assert.Equal(t, "auto-repo", w.RepoName)
	assert.Equal(t, "auto-org/auto-repo", w.RepoFull)
	assert.Equal(t, "auto-org", w.ForkOwner)
	assert.Equal(t, "auto-repo-fork", w.ForkRepo)
}

func TestGivenFork_CreateForkError(t *testing.T) {
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       &fakeForkSCM{createForkErr: fmt.Errorf("fork conflict")},
	}
	err := givenFork(w, "repo-fork")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating fork")
	assert.Contains(t, err.Error(), "fork conflict")
}

func TestWhenForkPullRequestOpened_RequiresFork(t *testing.T) {
	w := &world.World{}
	err := whenForkPullRequestOpened(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fork created")
}

func TestWhenForkPullRequestOpened_CreateBranchError(t *testing.T) {
	w := &world.World{
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       &fakeForkSCM{createBranchErr: fmt.Errorf("branch creation failed")},
	}
	err := whenForkPullRequestOpened(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating fork branch")
	assert.Contains(t, err.Error(), "branch creation failed")
}

func TestWhenForkPullRequestOpened_CommitError(t *testing.T) {
	w := &world.World{
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       &fakeForkSCM{commitToForkErr: fmt.Errorf("commit failed")},
	}
	err := whenForkPullRequestOpened(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing to fork branch")
	// ForkPRBranch must be set even on commit failure so CleanupScenario
	// can delete the already-created branch.
	assert.NotEmpty(t, w.ForkPRBranch, "ForkPRBranch should be set after CreateBranch succeeds, even when CommitFileToFork fails")
}

func TestWhenForkPullRequestOpened_CreatePRError(t *testing.T) {
	w := &world.World{
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       &fakeForkSCM{createForkPRErr: fmt.Errorf("PR creation failed")},
	}
	err := whenForkPullRequestOpened(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating fork pull request")
	// ForkPRBranch must be set even on PR creation failure so
	// CleanupScenario can delete the already-created branch.
	assert.NotEmpty(t, w.ForkPRBranch, "ForkPRBranch should be set after CreateBranch succeeds, even when CreateForkChangeProposal fails")
}

func TestWhenCommitPushedToForkPR_RequiresPR(t *testing.T) {
	w := &world.World{}
	err := whenCommitPushedToForkPR(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fork pull request opened")
}

func TestWhenCommitPushedToForkPR_CommitError(t *testing.T) {
	w := &world.World{
		ForkPRNumber: 10,
		ForkOwner:    "org",
		ForkRepo:     "repo-fork",
		ForkPRBranch: "test-branch",
		SCM:          &fakeForkSCM{commitToForkErr: fmt.Errorf("push failed")},
	}
	err := whenCommitPushedToForkPR(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pushing commit to fork PR")
}

func TestWhenForkPullRequestLabeled_Success(t *testing.T) {
	scmDriver := &fakeForkSCM{}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 10,
		SCM:          scmDriver,
	}
	err := whenForkPullRequestLabeled(w, "ready-for-fork-ping")
	require.NoError(t, err)
	assert.False(t, w.ScenarioStart.IsZero(), "ScenarioStart should be set")
	require.Len(t, scmDriver.addedLabels, 1)
	assert.Equal(t, "org", scmDriver.addedLabels[0].owner)
	assert.Equal(t, "repo", scmDriver.addedLabels[0].repo)
	assert.Equal(t, 10, scmDriver.addedLabels[0].number)
	assert.Equal(t, "ready-for-fork-ping", scmDriver.addedLabels[0].label)
}

func TestWhenForkPullRequestLabeled_NoForkPR(t *testing.T) {
	w := &world.World{}
	err := whenForkPullRequestLabeled(w, "some-label")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fork pull request opened")
}

func TestWhenForkPullRequestLabeled_Error(t *testing.T) {
	scmDriver := &fakeForkSCM{addIssueLabelsErr: fmt.Errorf("label failed")}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 10,
		SCM:          scmDriver,
	}
	err := whenForkPullRequestLabeled(w, "some-label")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "label failed")
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
	assert.True(t, scmDriver.createBranchCalled, "CreateBranch should have been called before committing")
	assert.True(t, scmDriver.commitToForkCalled, "CommitFileToFork should have been called")
	assert.True(t, scmDriver.createForkPRCalled, "CreateForkChangeProposal should have been called")

	// Step 3: When a commit is pushed to the fork pull request
	scmDriver.commitToForkCalled = false // reset to verify second call
	err = whenCommitPushedToForkPR(w)
	require.NoError(t, err)
	assert.True(t, scmDriver.commitToForkCalled, "CommitFileToFork should have been called again")
}

// fakeInstallState implements install.State for fork step unit tests.
type fakeInstallState struct {
	testRepo string
}

func (f *fakeInstallState) Mode() string               { return "per-org" }
func (f *fakeInstallState) TestRepo() string           { return f.testRepo }
func (f *fakeInstallState) ConfigOwner() string        { return "" }
func (f *fakeInstallState) ConfigRepo() string         { return "" }
func (f *fakeInstallState) ConfigPathPrefix() string   { return "" }
func (f *fakeInstallState) TriageWorkflowRepo() string { return "" }
func (f *fakeInstallState) TriageWorkflowFile() string { return "" }
func (f *fakeInstallState) AgentWorkflowFile() string  { return "" }
func (f *fakeInstallState) AgentArtifactName() string  { return "" }

// fakeForkSCM implements scm.Driver for fork step unit tests.
type fakeForkSCM struct {
	forkRepo           string
	prNumber           int
	createForkCalled   bool
	createBranchCalled bool
	commitToForkCalled bool
	createForkPRCalled bool
	createForkErr      error
	createBranchErr    error
	commitToForkErr    error
	createForkPRErr    error
	addedLabels        []addedLabelRecord
	addIssueLabelsErr  error
}

type addedLabelRecord struct {
	owner  string
	repo   string
	number int
	label  string
}

func (f *fakeForkSCM) CreateFork(_ context.Context, _, _, _ string) (string, error) {
	f.createForkCalled = true
	if f.createForkErr != nil {
		return "", f.createForkErr
	}
	return f.forkRepo, nil
}

func (f *fakeForkSCM) CommitFileToFork(_ context.Context, _, _, _, _, _ string, _ []byte) error {
	f.commitToForkCalled = true
	if f.commitToForkErr != nil {
		return f.commitToForkErr
	}
	return nil
}

func (f *fakeForkSCM) CreateForkChangeProposal(_ context.Context, _, _, _, _, _, _, _, _ string) (*forge.ChangeProposal, error) {
	f.createForkPRCalled = true
	if f.createForkPRErr != nil {
		return nil, f.createForkPRErr
	}
	return &forge.ChangeProposal{Number: f.prNumber, Head: "test-branch"}, nil
}

// Unused scm.Driver methods -- required for interface satisfaction.

func (f *fakeForkSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeForkSCM) AddIssueLabels(_ context.Context, owner, repo string, number int, labels ...string) error {
	if f.addIssueLabelsErr != nil {
		return f.addIssueLabelsErr
	}
	for _, l := range labels {
		f.addedLabels = append(f.addedLabels, addedLabelRecord{owner: owner, repo: repo, number: number, label: l})
	}
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

func (f *fakeForkSCM) CreateBranch(_ context.Context, _, _, _ string) error {
	f.createBranchCalled = true
	if f.createBranchErr != nil {
		return f.createBranchErr
	}
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

func (f *fakeForkSCM) CreateRepo(context.Context, string, string, string) error {
	return nil
}

func (f *fakeForkSCM) DeleteRepo(context.Context, string, string) error {
	return nil
}

func (f *fakeForkSCM) DeleteBranch(context.Context, string, string, string) error {
	return nil
}
