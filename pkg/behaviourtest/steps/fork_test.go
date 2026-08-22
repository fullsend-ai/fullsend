package steps

import (
	"context"
	"fmt"
	"testing"
	"time"

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

func TestGivenFork_ErrorsWhenNoRepoConfigured(t *testing.T) {
	w := &world.World{
		Org: "auto-org",
		SCM: &fakeForkSCM{forkRepo: "auto-repo-fork"},
	}
	err := givenFork(w, "auto-repo-fork")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
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

// --- resolveForkName unit tests ---

func TestResolveForkName_NoLease(t *testing.T) {
	w := &world.World{RepoName: "test-repo"}
	got := resolveForkName(w, "test-repo-fork")
	assert.Equal(t, "test-repo-fork", got, "without lease, logical name is unchanged")
}

func TestResolveForkName_LeasedRepoMaps(t *testing.T) {
	w := &world.World{
		LeasedRepoName: "test-repo-07",
		RepoName:       "test-repo-07",
	}
	got := resolveForkName(w, "test-repo-fork")
	assert.Equal(t, "test-repo-07-fork", got,
		"leased repo should remap test-repo-fork to test-repo-07-fork")
}

func TestResolveForkName_CustomNameUnchanged(t *testing.T) {
	w := &world.World{
		LeasedRepoName: "test-repo-07",
		RepoName:       "test-repo-07",
	}
	got := resolveForkName(w, "custom-fork")
	assert.Equal(t, "custom-fork", got,
		"non-prefixed name should be unchanged even with a lease")
}

func TestResolveForkName_DifferentSuffix(t *testing.T) {
	w := &world.World{
		LeasedRepoName: "test-repo-03",
		RepoName:       "test-repo-03",
	}
	got := resolveForkName(w, "test-repo-secondary")
	assert.Equal(t, "test-repo-03-secondary", got,
		"should preserve arbitrary suffix after test-repo prefix")
}

func TestGivenFork_LeasedRepoResolvesForkName(t *testing.T) {
	scmDriver := &fakeForkSCM{}
	w := &world.World{
		Org:            "org",
		RepoOwner:      "org",
		RepoName:       "test-repo-07",
		RepoFull:       "org/test-repo-07",
		LeasedRepoName: "test-repo-07",
		SCM:            scmDriver,
	}
	err := givenFork(w, "test-repo-fork")
	require.NoError(t, err)
	assert.Equal(t, "test-repo-07-fork", scmDriver.createForkName,
		"CreateFork should receive the resolved fork name")
	assert.Equal(t, "test-repo-07-fork", w.ForkRepo)
}

// --- awaitForkReady unit tests ---
//
// awaitForkReady has two phases:
//   1. Resolve the default branch name via GetDefaultBranch (repo metadata).
//   2. Poll GetBranchRef until the git ref is readable (returns a SHA).
//
// Phase 2 is the actual readiness signal — CreateBranch needs the
// default-branch git ref to exist, and repo metadata can report a
// default_branch name before the ref has been replicated.

func TestGivenFork_PollsBranchRef(t *testing.T) {
	// GetDefaultBranch succeeds immediately (repo metadata ready),
	// but GetBranchRef fails once (git ref not yet replicated),
	// then succeeds on the 2nd call. awaitForkReady should retry
	// the ref poll and ultimately succeed.
	//
	// Calls awaitForkReady directly with poll=0 to avoid the 2s
	// real sleep from the production forkReadyPoll constant; the
	// givenFork → awaitForkReady wiring is proven by
	// TestGivenFork_BranchRefImmediateSuccess.
	scmDriver := &fakeForkSCM{
		getBranchRefFailures: 1,
	}
	w := &world.World{
		SCM: scmDriver,
	}
	err := awaitForkReady(context.Background(), w, "org", "repo-fork", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, scmDriver.getDefaultBranchCalls,
		"GetDefaultBranch should be called once to resolve the branch name")
	assert.Equal(t, 2, scmDriver.getBranchRefCalls,
		"GetBranchRef should be called 1 failure + 1 success = 2 times")
}

func TestGivenFork_BranchRefImmediateSuccess(t *testing.T) {
	// Both GetDefaultBranch and GetBranchRef succeed on the first
	// call — no replication delay. givenFork should return
	// immediately without retries.
	scmDriver := &fakeForkSCM{
		forkRepo: "repo-fork",
	}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       scmDriver,
	}
	err := givenFork(w, "repo-fork")
	require.NoError(t, err)
	assert.Equal(t, 1, scmDriver.getDefaultBranchCalls,
		"GetDefaultBranch should be called exactly once")
	assert.Equal(t, 1, scmDriver.getBranchRefCalls,
		"GetBranchRef should be called exactly once on immediate success")
}

func TestAwaitForkReady_RefPollExhausted(t *testing.T) {
	// GetDefaultBranch succeeds (repo metadata ready) but
	// GetBranchRef always fails — simulates a fork whose git ref
	// is never replicated. awaitForkReady should return a clear
	// timeout error after exhausting all attempts.
	scmDriver := &fakeForkSCM{
		getBranchRefFailures: -1, // always fail
	}
	w := &world.World{
		SCM: scmDriver,
	}
	err := awaitForkReady(context.Background(), w, "org", "repo-fork", 5, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not readable after 5 attempts")
	assert.Equal(t, 1, scmDriver.getDefaultBranchCalls,
		"GetDefaultBranch should be called once to resolve branch name")
	assert.Equal(t, 5, scmDriver.getBranchRefCalls,
		"GetBranchRef should be called exactly maxAttempts times")
}

func TestAwaitForkReady_DefaultBranchPollExhausted(t *testing.T) {
	// GetDefaultBranch always fails — repo metadata not available.
	// awaitForkReady should fail in the first phase before reaching
	// the ref poll.
	scmDriver := &fakeForkSCM{
		getDefaultBranchFailures: -1, // always fail
	}
	w := &world.World{
		SCM: scmDriver,
	}
	err := awaitForkReady(context.Background(), w, "org", "repo-fork", 5, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default branch name not available after 5 attempts")
	assert.Equal(t, 5, scmDriver.getDefaultBranchCalls)
	assert.Equal(t, 0, scmDriver.getBranchRefCalls,
		"GetBranchRef should not be called if branch name resolution fails")
}

func TestAwaitForkReady_RefRetriesThenSucceeds(t *testing.T) {
	// GetDefaultBranch succeeds immediately; GetBranchRef fails
	// twice then succeeds on the 3rd call.
	scmDriver := &fakeForkSCM{
		getBranchRefFailures: 2,
	}
	w := &world.World{
		SCM: scmDriver,
	}
	err := awaitForkReady(context.Background(), w, "org", "repo-fork", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, scmDriver.getDefaultBranchCalls,
		"GetDefaultBranch should be called once")
	assert.Equal(t, 3, scmDriver.getBranchRefCalls,
		"GetBranchRef should be called 2 failures + 1 success = 3 times")
}

func TestAwaitForkReady_ContextCancelledDuringRefPoll(t *testing.T) {
	// Verify that awaitForkReady respects context cancellation
	// during the ref-polling phase and does not block indefinitely.
	scmDriver := &fakeForkSCM{
		getBranchRefFailures: -1, // always fail
	}
	w := &world.World{
		SCM: scmDriver,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	err := awaitForkReady(ctx, w, "org", "repo-fork", 30, 2*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

func TestAwaitForkReady_ContextCancelledDuringBranchNamePoll(t *testing.T) {
	// Verify that awaitForkReady respects context cancellation
	// during the branch-name resolution phase.
	scmDriver := &fakeForkSCM{
		getDefaultBranchFailures: -1, // always fail
	}
	w := &world.World{
		SCM: scmDriver,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	err := awaitForkReady(ctx, w, "org", "repo-fork", 30, 2*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

// fakeForkSCM implements scm.Driver for fork step unit tests.
type fakeForkSCM struct {
	forkRepo           string
	prNumber           int
	createForkCalled   bool
	createForkName     string // records the forkName arg passed to CreateFork
	createBranchCalled bool
	commitToForkCalled bool
	createForkPRCalled bool
	createForkErr      error
	createBranchErr    error
	commitToForkErr    error
	createForkPRErr    error
	addedLabels        []addedLabelRecord
	addIssueLabelsErr  error

	// getDefaultBranchFailures controls how many times GetDefaultBranch
	// returns an error before succeeding. Each call decrements the
	// counter; when it reaches 0, GetDefaultBranch returns success.
	// A value of -1 means GetDefaultBranch always fails.
	getDefaultBranchFailures int
	getDefaultBranchCalls    int

	// getBranchRefFailures controls how many times GetBranchRef returns
	// an error before succeeding. Works the same way as
	// getDefaultBranchFailures: each call decrements the counter;
	// when it reaches 0, GetBranchRef returns a SHA. A value of -1
	// means GetBranchRef always fails.
	getBranchRefFailures int
	getBranchRefCalls    int
}

type addedLabelRecord struct {
	owner  string
	repo   string
	number int
	label  string
}

func (f *fakeForkSCM) CreateFork(_ context.Context, _, _, forkName string) (string, error) {
	f.createForkCalled = true
	f.createForkName = forkName
	if f.createForkErr != nil {
		return "", f.createForkErr
	}
	if f.forkRepo != "" {
		return f.forkRepo, nil
	}
	return forkName, nil
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

func (f *fakeForkSCM) DeleteBranch(context.Context, string, string, string) error {
	return nil
}

func (f *fakeForkSCM) DeleteRepo(context.Context, string, string) error {
	return nil
}

func (f *fakeForkSCM) CreateRepo(context.Context, string, string, string) error {
	return nil
}

func (f *fakeForkSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeForkSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeForkSCM) EnsureRepoPublic(context.Context, string, string) error {
	return nil
}

func (f *fakeForkSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	f.getDefaultBranchCalls++
	if f.getDefaultBranchFailures == -1 {
		return "", fmt.Errorf("github api: 409 Git Repository is empty")
	}
	if f.getDefaultBranchFailures > 0 {
		f.getDefaultBranchFailures--
		return "", fmt.Errorf("github api: 409 Git Repository is empty")
	}
	return "main", nil
}

func (f *fakeForkSCM) GetBranchRef(context.Context, string, string, string) (string, error) {
	f.getBranchRefCalls++
	if f.getBranchRefFailures == -1 {
		return "", fmt.Errorf("github api: 409 Git Repository is empty")
	}
	if f.getBranchRefFailures > 0 {
		f.getBranchRefFailures--
		return "", fmt.Errorf("github api: 409 Git Repository is empty")
	}
	return "abc123", nil
}
func (f *fakeForkSCM) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, nil
}
