package github

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCreateFork(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	repo, err := d.CreateFork(context.Background(), "upstream", "repo", "my-fork")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "my-fork" {
		t.Errorf("expected fork repo %q, got %q", "my-fork", repo)
	}
	if len(fc.CreatedForks) != 1 || fc.CreatedForks[0] != "upstream/repo" {
		t.Errorf("expected CreateForkInOrg call for upstream/repo, got %v", fc.CreatedForks)
	}
}

func TestCreateFork_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateForkInOrg"] = errors.New("create failed")
	d := New(fc)

	_, err := d.CreateFork(context.Background(), "upstream", "repo", "my-fork")
	if err == nil || err.Error() != "create failed" {
		t.Fatalf("expected create failed error, got %v", err)
	}
}

func TestCommitFileToFork(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	err := d.CommitFileToFork(context.Background(), "fork-user", "repo", "feature-branch", "path/file.txt", "add file", []byte("content"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fc.CreatedFiles) != 1 {
		t.Fatalf("expected 1 file creation, got %d", len(fc.CreatedFiles))
	}
	f := fc.CreatedFiles[0]
	if f.Owner != "fork-user" {
		t.Errorf("expected owner %q, got %q", "fork-user", f.Owner)
	}
	if f.Repo != "repo" {
		t.Errorf("expected repo %q, got %q", "repo", f.Repo)
	}
	if f.Branch != "feature-branch" {
		t.Errorf("expected branch %q, got %q", "feature-branch", f.Branch)
	}
	if f.Path != "path/file.txt" {
		t.Errorf("expected path %q, got %q", "path/file.txt", f.Path)
	}
	if string(f.Content) != "content" {
		t.Errorf("expected content %q, got %q", "content", string(f.Content))
	}
}

func TestCommitFileToFork_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateOrUpdateFileOnBranch"] = errors.New("commit failed")
	d := New(fc)

	err := d.CommitFileToFork(context.Background(), "fork-user", "repo", "branch", "file.txt", "msg", []byte("data"))
	if err == nil || err.Error() != "commit failed" {
		t.Fatalf("expected commit failed error, got %v", err)
	}
}

func TestCreateForkChangeProposal_CrossOwner(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	// Cross-owner: forkOwner != baseOwner → uses REST CreateChangeProposal.
	cp, err := d.CreateForkChangeProposal(context.Background(), "upstream", "repo", "PR title", "PR body", "fork-user", "fork-repo", "feature-branch", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cp == nil {
		t.Fatal("expected non-nil ChangeProposal")
	}
	if cp.Title != "PR title" {
		t.Errorf("expected title %q, got %q", "PR title", cp.Title)
	}
	if cp.Base != "main" {
		t.Errorf("expected base %q, got %q", "main", cp.Base)
	}

	// Verify the head ref uses the cross-fork format.
	if len(fc.CreatedProposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(fc.CreatedProposals))
	}
	if fc.CreatedProposals[0].Head != "fork-user:feature-branch" {
		t.Errorf("expected head %q, got %q", "fork-user:feature-branch", fc.CreatedProposals[0].Head)
	}
}

func TestCreateForkChangeProposal_SameOwner(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	// Same-owner: forkOwner == baseOwner → uses CreateCrossRepoChangeProposal
	// to disambiguate via explicit head repository.
	cp, err := d.CreateForkChangeProposal(context.Background(), "org", "repo", "PR title", "PR body", "org", "repo-fork", "feature-branch", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cp == nil {
		t.Fatal("expected non-nil ChangeProposal")
	}
	if cp.Title != "PR title" {
		t.Errorf("expected title %q, got %q", "PR title", cp.Title)
	}

	// Verify it went through CreateCrossRepoChangeProposal (not CreateChangeProposal).
	if len(fc.CreatedProposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(fc.CreatedProposals))
	}
	// The fake's CreateCrossRepoChangeProposal sets head as "owner:branch".
	if fc.CreatedProposals[0].Head != "org:feature-branch" {
		t.Errorf("expected head %q, got %q", "org:feature-branch", fc.CreatedProposals[0].Head)
	}
}

func TestCreateForkChangeProposal_CrossOwner_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateChangeProposal"] = errors.New("pr failed")
	d := New(fc)

	_, err := d.CreateForkChangeProposal(context.Background(), "upstream", "repo", "title", "body", "fork-user", "fork-repo", "branch", "main")
	if err == nil || err.Error() != "pr failed" {
		t.Fatalf("expected pr failed error, got %v", err)
	}
}

func TestCreateForkChangeProposal_SameOwner_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateCrossRepoChangeProposal"] = errors.New("graphql failed")
	d := New(fc)

	_, err := d.CreateForkChangeProposal(context.Background(), "org", "repo", "title", "body", "org", "repo-fork", "branch", "main")
	if err == nil || err.Error() != "graphql failed" {
		t.Fatalf("expected graphql failed error, got %v", err)
	}
}

func TestDeleteBranch(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	err := d.DeleteBranch(context.Background(), "owner", "repo", "feature-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fc.DeletedRefs) != 1 {
		t.Fatalf("expected 1 deleted ref, got %d", len(fc.DeletedRefs))
	}
	// DeleteBranch should prepend "heads/" to the branch name.
	if fc.DeletedRefs[0] != "owner/repo/heads/feature-branch" {
		t.Errorf("expected ref %q, got %q", "owner/repo/heads/feature-branch", fc.DeletedRefs[0])
	}
}

func TestDeleteBranch_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["DeleteRef"] = errors.New("ref delete failed")
	d := New(fc)

	err := d.DeleteBranch(context.Background(), "owner", "repo", "branch")
	if err == nil || err.Error() != "ref delete failed" {
		t.Fatalf("expected ref delete failed error, got %v", err)
	}
}

func TestCreateFork_ExistingNonForkRepo(t *testing.T) {
	fc := forge.NewFakeClient()
	// Pre-populate with a non-fork repo that has the same name.
	fc.Repos = []forge.Repository{
		{Name: "my-fork", FullName: "upstream/my-fork", Fork: false},
	}
	d := New(fc)

	_, err := d.CreateFork(context.Background(), "upstream", "repo", "my-fork")
	if err == nil {
		t.Fatal("expected error when repo exists but is not a fork")
	}
	if !forge.IsNotFork(err) {
		t.Fatalf("expected ErrNotFork, got %v", err)
	}
}

func TestCreateFork_ExistingForkOfDifferentSource(t *testing.T) {
	fc := forge.NewFakeClient()
	// Pre-populate with a fork of a different source repo.
	fc.Repos = []forge.Repository{
		{Name: "my-fork", FullName: "upstream/my-fork", Fork: true},
	}
	fc.ForkParents = map[string]string{
		"upstream/my-fork": "other-owner/other-repo",
	}
	d := New(fc)

	_, err := d.CreateFork(context.Background(), "upstream", "repo", "my-fork")
	if err == nil {
		t.Fatal("expected error when repo is a fork of a different source")
	}
	if !forge.IsNotFork(err) {
		t.Fatalf("expected ErrNotFork, got %v", err)
	}
}

func TestCreateRepo_IdempotentAlreadyExists(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	// First create succeeds.
	err := d.CreateRepo(context.Background(), "org", "my-repo", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second create of the same repo should succeed (idempotent).
	err = d.CreateRepo(context.Background(), "org", "my-repo", "desc")
	if err != nil {
		t.Fatalf("idempotent CreateRepo should not error: %v", err)
	}
}

func TestCreateFork_ExistingForkOfSameSource(t *testing.T) {
	fc := forge.NewFakeClient()
	// Pre-populate with a fork of the same source repo (idempotent case).
	fc.Repos = []forge.Repository{
		{Name: "my-fork", FullName: "upstream/my-fork", Fork: true},
	}
	fc.ForkParents = map[string]string{
		"upstream/my-fork": "upstream/repo",
	}
	d := New(fc)

	repo, err := d.CreateFork(context.Background(), "upstream", "repo", "my-fork")
	if err != nil {
		t.Fatalf("unexpected error for idempotent fork: %v", err)
	}
	if repo != "my-fork" {
		t.Errorf("expected fork repo %q, got %q", "my-fork", repo)
	}
	// No new fork should have been created (idempotent).
	if len(fc.CreatedForks) != 0 {
		t.Errorf("expected no new fork creation for idempotent case, got %v", fc.CreatedForks)
	}
}

// --- Tests for PR-added methods ---

func TestGetDefaultBranch(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "repo", FullName: "org/repo", DefaultBranch: "develop"},
	}
	d := New(fc)

	branch, err := d.GetDefaultBranch(context.Background(), "org", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "develop" {
		t.Errorf("expected branch %q, got %q", "develop", branch)
	}
}

func TestGetDefaultBranch_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetRepo"] = errors.New("repo not found")
	d := New(fc)

	_, err := d.GetDefaultBranch(context.Background(), "org", "repo")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errors.New("")) && err.Error() == "" {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

func TestGetBranchRef(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.BranchRefs = map[string]string{
		"org/repo/main": "deadbeef",
	}
	d := New(fc)

	sha, err := d.GetBranchRef(context.Background(), "org", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "deadbeef" {
		t.Errorf("expected SHA %q, got %q", "deadbeef", sha)
	}
}

func TestGetBranchRef_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetBranchRef"] = errors.New("ref not found")
	d := New(fc)

	_, err := d.GetBranchRef(context.Background(), "org", "repo", "main")
	if err == nil || err.Error() != "ref not found" {
		t.Fatalf("expected ref not found error, got %v", err)
	}
}

func TestEnsureRepoPublic_AlreadyPublic(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "repo", FullName: "org/repo", Private: false},
	}
	d := New(fc)

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureRepoPublic_MakesPublic(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "repo", FullName: "org/repo", Private: true},
	}
	d := New(fc)

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After UpdateRepoVisibility + re-check, repo should be public.
	repo, _ := fc.GetRepo(context.Background(), "org", "repo")
	if repo.Private {
		t.Error("repo should be public after EnsureRepoPublic")
	}
}

func TestEnsureRepoPublic_GetRepoError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetRepo"] = errors.New("api error")
	d := New(fc)

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureRepoPublic_UpdateVisibilityError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "repo", FullName: "org/repo", Private: true},
	}
	fc.Errors["UpdateRepoVisibility"] = errors.New("org policy prevents public repos")
	d := New(fc)

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err == nil {
		t.Fatal("expected error when visibility update fails")
	}
}

func TestEnsureRepoPublic_ReVerifyError(t *testing.T) {
	// UpdateRepoVisibility succeeds, but the re-verification GetRepo fails.
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "repo", FullName: "org/repo", Private: true},
	}
	d := &Driver{Client: &reVerifyFailClient{FakeClient: fc}}

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err == nil {
		t.Fatal("expected error when re-verification GetRepo fails")
	}
}

// reVerifyFailClient wraps FakeClient. UpdateRepoVisibility succeeds
// but the second GetRepo call returns an error.
type reVerifyFailClient struct {
	*forge.FakeClient
	getRepoCount int
}

func (c *reVerifyFailClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	c.getRepoCount++
	if c.getRepoCount >= 2 {
		return nil, errors.New("re-verify API error")
	}
	return c.FakeClient.GetRepo(ctx, owner, repo)
}

func TestEnsureRepoPublic_StillPrivateAfterUpdate(t *testing.T) {
	// Simulate a repo that stays private even after UpdateRepoVisibility
	// (e.g., org policy override). We use a custom approach: set
	// UpdateRepoVisibility to not actually change the repo's private flag
	// by using error injection only on the re-verification GetRepo.
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "repo", FullName: "org/repo", Private: true},
	}
	d := &Driver{Client: &stillPrivateClient{FakeClient: fc}}

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err == nil {
		t.Fatal("expected error when repo remains private after update")
	}
}

// stillPrivateClient wraps FakeClient but makes UpdateRepoVisibility
// a no-op so the repo stays private after the call.
type stillPrivateClient struct {
	*forge.FakeClient
}

func (c *stillPrivateClient) UpdateRepoVisibility(_ context.Context, _, _ string, _ bool) error {
	// Intentionally don't change repo visibility to simulate org policy.
	return nil
}

func TestParseRepo_Success(t *testing.T) {
	owner, repo, err := ParseRepo("acme/widget")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "acme" || repo != "widget" {
		t.Errorf("expected acme/widget, got %s/%s", owner, repo)
	}
}

func TestParseRepo_Invalid(t *testing.T) {
	_, _, err := ParseRepo("invalid")
	if err == nil {
		t.Fatal("expected error for invalid repo name")
	}
}

func TestAddIssueLabels(t *testing.T) {
	fc := forge.NewFakeClient()
	issue, err := fc.CreateIssue(context.Background(), "org", "repo", "test", "body")
	if err != nil {
		t.Fatalf("unexpected error creating issue: %v", err)
	}
	d := New(fc)

	err = d.AddIssueLabels(context.Background(), "org", "repo", issue.Number, "bug", "priority")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloseIssue(t *testing.T) {
	fc := forge.NewFakeClient()
	issue, err := fc.CreateIssue(context.Background(), "org", "repo", "test", "body")
	if err != nil {
		t.Fatalf("unexpected error creating issue: %v", err)
	}
	d := New(fc)

	err = d.CloseIssue(context.Background(), "org", "repo", issue.Number)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCommitFileToBranch(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	err := d.CommitFileToBranch(context.Background(), "owner", "repo", "feature", "file.txt", "add file", []byte("content"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.CreatedFiles) != 1 {
		t.Fatalf("expected 1 file creation, got %d", len(fc.CreatedFiles))
	}
	if fc.CreatedFiles[0].Branch != "feature" {
		t.Errorf("expected branch %q, got %q", "feature", fc.CreatedFiles[0].Branch)
	}
}

func TestCommitFileToBranch_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateOrUpdateFileOnBranch"] = errors.New("commit error")
	d := New(fc)

	err := d.CommitFileToBranch(context.Background(), "owner", "repo", "branch", "file.txt", "msg", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateChangeProposal(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	cp, err := d.CreateChangeProposal(context.Background(), "owner", "repo", "title", "body", "head", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cp.Title != "title" {
		t.Errorf("expected title %q, got %q", "title", cp.Title)
	}
}

func TestCreateChangeProposal_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateChangeProposal"] = errors.New("pr create failed")
	d := New(fc)

	_, err := d.CreateChangeProposal(context.Background(), "owner", "repo", "title", "body", "head", "main")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSubmitPullRequestReview(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PullRequestHeadSHA = "abc123"
	d := New(fc)

	err := d.SubmitPullRequestReview(context.Background(), "owner", "repo", 1, "APPROVE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.CreatedReviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(fc.CreatedReviews))
	}
}

func TestSubmitPullRequestReview_GetSHAError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetPullRequestHeadSHA"] = errors.New("sha lookup failed")
	d := New(fc)

	err := d.SubmitPullRequestReview(context.Background(), "owner", "repo", 1, "APPROVE")
	if err == nil {
		t.Fatal("expected error when GetPullRequestHeadSHA fails")
	}
}

func TestSubmitPullRequestReview_CreateReviewError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PullRequestHeadSHA = "abc123"
	fc.Errors["CreatePullRequestReview"] = errors.New("review failed")
	d := New(fc)

	err := d.SubmitPullRequestReview(context.Background(), "owner", "repo", 1, "APPROVE")
	if err == nil {
		t.Fatal("expected error when CreatePullRequestReview fails")
	}
}

func TestListPullRequestReviews(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PullRequestHeadSHA = "abc123"
	d := New(fc)

	require.NoError(t, d.SubmitPullRequestReview(context.Background(), "owner", "repo", 1, "APPROVE"))

	reviews, err := d.ListPullRequestReviews(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, "APPROVE", reviews[0].State)
}

func TestListPullRequestReviews_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListPullRequestReviews"] = errors.New("list failed")
	d := New(fc)

	_, err := d.ListPullRequestReviews(context.Background(), "owner", "repo", 1)
	assert.ErrorContains(t, err, "list failed")
}

// laggingRepoClient returns ErrNotFound for the first fails GetRepo
// calls, then delegates: GitHub's read-after-create lag (#7861).
type laggingRepoClient struct {
	forge.Client
	fails int
	err   error
	calls int
}

func (c *laggingRepoClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	c.calls++
	if c.calls <= c.fails {
		if c.err != nil {
			return nil, c.err
		}
		return nil, forge.ErrNotFound
	}
	return c.Client.GetRepo(ctx, owner, repo)
}

func noRepoVisibleDelay(t *testing.T) {
	t.Helper()
	orig := repoVisibleDelay
	repoVisibleDelay = 0
	t.Cleanup(func() { repoVisibleDelay = orig })
}

func newPublicRepoFake() *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.Repos = append(fc.Repos, forge.Repository{Name: "host", FullName: "org/host", Private: false})
	return fc
}

func TestEnsureRepoPublic_RetriesNotFoundAfterCreate(t *testing.T) {
	noRepoVisibleDelay(t)
	c := &laggingRepoClient{Client: newPublicRepoFake(), fails: 2}

	require.NoError(t, New(c).EnsureRepoPublic(context.Background(), "org", "host"))
	assert.Equal(t, 3, c.calls)
}

func TestEnsureRepoPublic_NotFoundExhaustsBudget(t *testing.T) {
	noRepoVisibleDelay(t)
	c := &laggingRepoClient{Client: newPublicRepoFake(), fails: 100}

	err := New(c).EnsureRepoPublic(context.Background(), "org", "host")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
	assert.Contains(t, err.Error(), "still not found 5 attempts after creation")
	assert.Equal(t, repoVisibleAttempts, c.calls)
}

func TestEnsureRepoPublic_OtherErrorNotRetried(t *testing.T) {
	noRepoVisibleDelay(t)
	c := &laggingRepoClient{Client: newPublicRepoFake(), fails: 100, err: errors.New("forbidden")}

	err := New(c).EnsureRepoPublic(context.Background(), "org", "host")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")
	assert.Equal(t, 1, c.calls)
}

func TestEnsureRepoPublic_ContextCancelledWhileWaiting(t *testing.T) {
	c := &laggingRepoClient{Client: newPublicRepoFake(), fails: 100}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New(c).EnsureRepoPublic(ctx, "org", "host")
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, c.calls)
}
