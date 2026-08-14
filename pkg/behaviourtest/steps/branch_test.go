package steps

import (
	"context"
	"testing"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeBranchSCM implements the scm.Driver methods the branch steps use.
// Unused methods come from the embedded interface and panic when called.
type fakeBranchSCM struct {
	scm.Driver

	branchSHAs map[string]string // branch → tip SHA returned by GetBranchRef

	deletedBranches []string
	createdBranches []string
	seededBranches  []string
	openPRs         []forge.ChangeProposal
	comments        []forge.IssueComment
	addedComments   []string

	nextPRNumber int
}

func (f *fakeBranchSCM) DeleteBranch(_ context.Context, _, _, branch string) error {
	f.deletedBranches = append(f.deletedBranches, branch)
	if _, ok := f.branchSHAs[branch]; !ok {
		return forge.ErrNotFound
	}
	delete(f.branchSHAs, branch)
	return nil
}

func (f *fakeBranchSCM) CreateBranch(_ context.Context, _, _, branch string) error {
	if f.branchSHAs == nil {
		f.branchSHAs = map[string]string{}
	}
	f.branchSHAs[branch] = "base-sha"
	f.createdBranches = append(f.createdBranches, branch)
	return nil
}

func (f *fakeBranchSCM) CommitFileToBranch(_ context.Context, _, _, branch, _, _ string, _ []byte) error {
	f.branchSHAs[branch] = "seeded-sha-" + branch
	f.seededBranches = append(f.seededBranches, branch)
	return nil
}

func (f *fakeBranchSCM) CreateChangeProposal(_ context.Context, _, _, title, body, head, base string) (*forge.ChangeProposal, error) {
	f.nextPRNumber++
	pr := forge.ChangeProposal{Title: title, Number: f.nextPRNumber, Head: head, Base: base}
	f.openPRs = append(f.openPRs, pr)
	return &pr, nil
}

func (f *fakeBranchSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}

func (f *fakeBranchSCM) GetBranchRef(_ context.Context, _, _, branch string) (string, error) {
	sha, ok := f.branchSHAs[branch]
	if !ok {
		return "", forge.ErrNotFound
	}
	return sha, nil
}

func (f *fakeBranchSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return f.openPRs, nil
}

func (f *fakeBranchSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return f.comments, nil
}

func (f *fakeBranchSCM) AddComment(_ context.Context, _, _ string, _ int, body string) (*forge.IssueComment, error) {
	f.addedComments = append(f.addedComments, body)
	return &forge.IssueComment{Body: body}, nil
}
func (f *fakeBranchSCM) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, nil
}

// fakeBranchCI implements WaitForFailedHarnessAgent; other ci.Driver
// methods come from the embedded interface and panic when called.
type fakeBranchCI struct {
	ci.Driver

	run *forge.WorkflowRun
	err error
}

func (f *fakeBranchCI) WaitForFailedHarnessAgent(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return f.run, f.err
}

func branchTestWorld(scmDriver scm.Driver) *world.World {
	return &world.World{
		SCM:       scmDriver,
		Org:       "test-org",
		RepoOwner: "test-org",
		RepoName:  "test-repo",
	}
}

func TestExpandIssuePlaceholder(t *testing.T) {
	w := &world.World{IssueNumber: 42}

	got, err := expandIssuePlaceholder(w, "agent/<issue>-impl")
	require.NoError(t, err)
	assert.Equal(t, "agent/42-impl", got)

	got, err = expandIssuePlaceholder(w, "no-placeholder")
	require.NoError(t, err)
	assert.Equal(t, "no-placeholder", got)
}

func TestExpandIssuePlaceholder_NoIssue(t *testing.T) {
	_, err := expandIssuePlaceholder(&world.World{}, "agent/<issue>-impl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no issue exists yet")
}

func TestGivenSeededRemoteBranch(t *testing.T) {
	scmDriver := &fakeBranchSCM{}
	w := branchTestWorld(scmDriver)
	w.IssueNumber = 7

	require.NoError(t, givenSeededRemoteBranch(w, "agent/<issue>-impl"))

	assert.Equal(t, []string{"agent/7-impl"}, scmDriver.deletedBranches, "leftover branch is reset first")
	assert.Equal(t, []string{"agent/7-impl"}, scmDriver.createdBranches)
	assert.Equal(t, []string{"agent/7-impl"}, scmDriver.seededBranches)
	assert.Equal(t, []string{"agent/7-impl"}, w.CreatedBranches)
}

func TestGivenOpenPullRequestOnBranch(t *testing.T) {
	scmDriver := &fakeBranchSCM{}
	w := branchTestWorld(scmDriver)

	require.NoError(t, givenOpenPullRequestOnBranch(w, "agent/99999-decoy"))

	require.Len(t, scmDriver.openPRs, 1)
	assert.Equal(t, "agent/99999-decoy", scmDriver.openPRs[0].Head)
	assert.Equal(t, scmDriver.openPRs[0].Number, w.PRNumber)
	assert.Equal(t, []int{scmDriver.openPRs[0].Number}, w.CreatedPRNumbers)
	assert.Equal(t, []string{"agent/99999-decoy"}, w.CreatedBranches)
	assert.False(t, w.ScenarioStart.IsZero())
}

func TestBranchTipRecordedAndUnchanged(t *testing.T) {
	scmDriver := &fakeBranchSCM{branchSHAs: map[string]string{"agent/99999-decoy": "abc123"}}
	w := branchTestWorld(scmDriver)

	require.NoError(t, givenBranchTipRecorded(w, "agent/99999-decoy"))
	require.NoError(t, thenBranchUnchanged(w, "agent/99999-decoy"))

	scmDriver.branchSHAs["agent/99999-decoy"] = "def456"
	err := thenBranchUnchanged(w, "agent/99999-decoy")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "moved")
}

func TestThenBranchUnchanged_NotRecorded(t *testing.T) {
	w := branchTestWorld(&fakeBranchSCM{})
	err := thenBranchUnchanged(w, "never-recorded")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never recorded")
}

func TestThenPullRequestHeadBranchMatches(t *testing.T) {
	scmDriver := &fakeBranchSCM{openPRs: []forge.ChangeProposal{
		{Number: 1, Head: "agent/99999-decoy"},
		{Number: 2, Head: "agent/42-99999-decoy"},
	}}
	w := branchTestWorld(scmDriver)
	w.IssueNumber = 42

	require.NoError(t, thenPullRequestHeadBranchMatches(w, `agent/<issue>-.*`))
	assert.Equal(t, []int{2}, w.CreatedPRNumbers, "matched PR is tracked for cleanup")
	assert.Equal(t, []string{"agent/42-99999-decoy"}, w.CreatedBranches)
}

func TestThenPullRequestHeadBranchMatches_Anchored(t *testing.T) {
	scmDriver := &fakeBranchSCM{openPRs: []forge.ChangeProposal{
		{Number: 1, Head: "agent/123-impl"},
	}}
	w := branchTestWorld(scmDriver)
	w.IssueNumber = 12

	err := thenPullRequestHeadBranchMatches(w, `agent/<issue>-.*`)
	require.Error(t, err, "agent/12-* must not match agent/123-impl")
	assert.Contains(t, err.Error(), "no open PR head branch matches")
}

func TestThenPullRequestHeadBranchMatches_Ambiguous(t *testing.T) {
	scmDriver := &fakeBranchSCM{openPRs: []forge.ChangeProposal{
		{Number: 1, Head: "agent/42-a"},
		{Number: 2, Head: "agent/42-b"},
	}}
	w := branchTestWorld(scmDriver)
	w.IssueNumber = 42

	err := thenPullRequestHeadBranchMatches(w, `agent/<issue>-.*`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want exactly 1")
}

func TestWhenCommentPostedOnPullRequest(t *testing.T) {
	scmDriver := &fakeBranchSCM{}
	w := branchTestWorld(scmDriver)

	err := whenCommentPostedOnPullRequest(w, "/fs-fix")
	require.Error(t, err, "requires an open PR")

	w.PRNumber = 5
	require.NoError(t, whenCommentPostedOnPullRequest(w, "/fs-fix"))
	assert.Equal(t, []string{"/fs-fix"}, scmDriver.addedComments)
	assert.False(t, w.ScenarioStart.IsZero())
}

func TestThenHarnessWorkflowFailsReporting(t *testing.T) {
	origWindow, origInterval := failureCommentPollWindow, failureCommentPollInterval
	failureCommentPollWindow, failureCommentPollInterval = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		failureCommentPollWindow, failureCommentPollInterval = origWindow, origInterval
	})

	scmDriver := &fakeBranchSCM{comments: []forge.IssueComment{
		{Body: "⚠️ Post-fix script failed — branch does not match. Refusing to push."},
	}}
	w := branchTestWorld(scmDriver)
	w.PRNumber = 5
	w.ScenarioStart = time.Now()
	w.RepoName = "test-repo"
	w.CI = &fakeBranchCI{run: &forge.WorkflowRun{ID: 9}}

	require.NoError(t, thenHarnessWorkflowFailsReporting(context.Background(), w, "fix", "Refusing to push"))
	assert.Equal(t, 9, w.WorkflowRun.ID)

	err := thenHarnessWorkflowFailsReporting(context.Background(), w, "fix", "not-present-text")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no comment on PR #5")
}

func TestEnsureScenarioRepo_NoRepoOwner(t *testing.T) {
	w := &world.World{RepoName: "test-repo"}
	err := ensureScenarioRepo(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestEnsureScenarioRepo_NoRepoName(t *testing.T) {
	w := &world.World{RepoOwner: "test-org"}
	err := ensureScenarioRepo(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestEnsureScenarioRepo_OK(t *testing.T) {
	w := &world.World{RepoOwner: "org", RepoName: "repo"}
	require.NoError(t, ensureScenarioRepo(w))
}

func TestGivenSeededRemoteBranch_NoRepo(t *testing.T) {
	err := givenSeededRemoteBranch(&world.World{SCM: &fakeBranchSCM{}}, "branch-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenOpenPullRequestOnBranch_NoRepo(t *testing.T) {
	err := givenOpenPullRequestOnBranch(&world.World{SCM: &fakeBranchSCM{}}, "branch-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenBranchTipRecorded_NoRepo(t *testing.T) {
	err := givenBranchTipRecorded(&world.World{SCM: &fakeBranchSCM{}}, "branch-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestThenBranchUnchanged_NoRepo(t *testing.T) {
	err := thenBranchUnchanged(&world.World{SCM: &fakeBranchSCM{}}, "branch-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestParseDummyAgentTable_ExpandsIssueInCheckoutBranchOnly(t *testing.T) {
	w := &world.World{
		Org:          "org",
		RepoName:     "repo",
		SCM:          &fakeCleanupSCM{},
		FixturesRoot: "e2e/behaviour",
		IssueNumber:  42,
	}
	table := &godog.Table{
		Rows: []*messages.PickleTableRow{
			{Cells: []*messages.PickleTableCell{{Value: "description"}, {Value: "op"}, {Value: "args"}}},
			{Cells: []*messages.PickleTableCell{{Value: "checkout"}, {Value: "checkout_branch"}, {Value: "agent/<issue>-impl"}}},
			{Cells: []*messages.PickleTableCell{{Value: "read"}, {Value: "read_file"}, {Value: "docs/<issue>.md"}}},
		},
	}
	require.NoError(t, parseDummyAgentTable(w, table))
	require.Len(t, w.DummyOps, 2)
	assert.Equal(t, "agent/42-impl", w.DummyOps[0].Args, "checkout_branch args expand <issue>")
	assert.Equal(t, "docs/<issue>.md", w.DummyOps[1].Args, "other ops keep <issue> literal")
}
