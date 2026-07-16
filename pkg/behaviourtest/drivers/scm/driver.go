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
	CommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error
	CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error)
	SubmitPullRequestReview(ctx context.Context, owner, repo string, number int, event string) error
	CloseIssue(ctx context.Context, owner, repo string, number int) error

	// CreateFork creates a fork of owner/repo under the authenticated
	// user's account. It is idempotent — if a fork already exists, it
	// returns the existing fork's owner and repo name.
	CreateFork(ctx context.Context, owner, repo string) (forkOwner, forkRepo string, err error)

	// CommitFileToFork commits a file to a branch on a fork repository.
	// Analogous to CommitFileToBranch but targets the fork.
	CommitFileToFork(ctx context.Context, forkOwner, forkRepo, branch, path, message string, content []byte) error

	// CreateForkChangeProposal opens a cross-fork pull request from
	// forkOwner:headBranch into baseOwner/baseRepo's baseBranch.
	CreateForkChangeProposal(ctx context.Context, baseOwner, baseRepo, title, body, forkOwner, headBranch, baseBranch string) (*forge.ChangeProposal, error)
}
