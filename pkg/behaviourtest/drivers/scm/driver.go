package scm

import (
	"context"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Driver abstracts SCM operations for behaviour tests.
type Driver interface {
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error)
	AddIssueLabels(ctx context.Context, owner, repo string, number int, labels ...string) error
	AddComment(ctx context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error)
	GetIssue(ctx context.Context, owner, repo string, number int) (*forge.Issue, error)
	GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error)
	CommitFile(ctx context.Context, owner, repo, path, message string, content []byte) error
	CreateBranch(ctx context.Context, owner, repo, branch string) error
	// DeleteBranch deletes a branch from a repository. Returns
	// forge.ErrNotFound if the branch does not exist.
	DeleteBranch(ctx context.Context, owner, repo, branch string) error
	CommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error
	CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error)
	SubmitPullRequestReview(ctx context.Context, owner, repo string, number int, event string) error
	CloseIssue(ctx context.Context, owner, repo string, number int) error

	// CreateFork creates a fork of owner/repo within the same
	// organization as the source repository, using the given
	// forkName. It returns the actual repo name of the created
	// fork. The call is idempotent — if a fork with the given
	// name already exists, it returns without error.
	CreateFork(ctx context.Context, owner, repo, forkName string) (forkRepo string, err error)

	// CommitFileToFork commits a file to a branch on a fork repository.
	// Analogous to CommitFileToBranch but targets the fork.
	CommitFileToFork(ctx context.Context, forkOwner, forkRepo, branch, path, message string, content []byte) error

	// CreateForkChangeProposal opens a cross-fork pull request from
	// forkOwner/forkRepo:headBranch into baseOwner/baseRepo's baseBranch.
	// The forkRepo parameter is required to disambiguate same-owner forks
	// (where forkOwner == baseOwner) from branches on the base repo.
	CreateForkChangeProposal(ctx context.Context, baseOwner, baseRepo, title, body, forkOwner, forkRepo, headBranch, baseBranch string) (*forge.ChangeProposal, error)
}
