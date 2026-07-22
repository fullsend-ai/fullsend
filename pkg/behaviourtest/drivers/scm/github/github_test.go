package github

import (
	"context"
	"errors"
	"testing"

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
		t.Errorf("expected default branch %q, got %q", "develop", branch)
	}
}

func TestGetDefaultBranch_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetRepo"] = errors.New("not found")
	d := New(fc)

	_, err := d.GetDefaultBranch(context.Background(), "org", "missing")
	if err == nil {
		t.Fatal("expected error for missing repo")
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
		t.Fatalf("unexpected error for already-public repo: %v", err)
	}
}

func TestEnsureRepoPublic_MakesPrivatePublic(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "repo", FullName: "org/repo", Private: true},
	}
	d := New(fc)

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify the repo is now public.
	r, _ := fc.GetRepo(context.Background(), "org", "repo")
	if r.Private {
		t.Error("repo should be public after EnsureRepoPublic")
	}
}

func TestEnsureRepoPublic_GetRepoError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetRepo"] = errors.New("api error")
	d := New(fc)

	err := d.EnsureRepoPublic(context.Background(), "org", "repo")
	if err == nil {
		t.Fatal("expected error when GetRepo fails")
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
		t.Fatal("expected error when UpdateRepoVisibility fails")
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
