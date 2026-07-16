package github

import (
	"context"
	"errors"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCreateFork_ExistingFork(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.ExistingForks = map[string]string{
		"upstream/repo": "fork-user",
	}
	d := New(fc)

	owner, repo, err := d.CreateFork(context.Background(), "upstream", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "fork-user" {
		t.Errorf("expected fork owner %q, got %q", "fork-user", owner)
	}
	if repo != "repo" {
		t.Errorf("expected fork repo %q, got %q", "repo", repo)
	}
	// Should not have called CreateFork on the forge client.
	if len(fc.CreatedForks) != 0 {
		t.Errorf("expected no CreateFork calls, got %v", fc.CreatedForks)
	}
}

func TestCreateFork_NewFork(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.ForkOwner = "my-user"
	d := New(fc)

	owner, repo, err := d.CreateFork(context.Background(), "upstream", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "my-user" {
		t.Errorf("expected fork owner %q, got %q", "my-user", owner)
	}
	if repo != "repo" {
		t.Errorf("expected fork repo %q, got %q", "repo", repo)
	}
	if len(fc.CreatedForks) != 1 || fc.CreatedForks[0] != "upstream/repo" {
		t.Errorf("expected CreateFork call for upstream/repo, got %v", fc.CreatedForks)
	}
}

func TestCreateFork_FindExistingForkError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["FindExistingFork"] = errors.New("api error")
	d := New(fc)

	_, _, err := d.CreateFork(context.Background(), "upstream", "repo")
	if err == nil || err.Error() != "api error" {
		t.Fatalf("expected api error, got %v", err)
	}
}

func TestCreateFork_CreateForkError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateFork"] = errors.New("create failed")
	d := New(fc)

	_, _, err := d.CreateFork(context.Background(), "upstream", "repo")
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

func TestCreateForkChangeProposal(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	cp, err := d.CreateForkChangeProposal(context.Background(), "upstream", "repo", "PR title", "PR body", "fork-user", "feature-branch", "main")
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

func TestCreateForkChangeProposal_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateChangeProposal"] = errors.New("pr failed")
	d := New(fc)

	_, err := d.CreateForkChangeProposal(context.Background(), "upstream", "repo", "title", "body", "fork-user", "branch", "main")
	if err == nil || err.Error() != "pr failed" {
		t.Fatalf("expected pr failed error, got %v", err)
	}
}
