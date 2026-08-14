package scm

import (
	"context"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Driver abstracts SCM operations for behaviour tests.
//
// Concurrency: the github.Driver implementation is an immutable wrapper
// around forge.Client (which is itself safe for concurrent use) and
// holds no unsynchronized mutable fields. Sharing a single Driver
// across goroutines via World.Clone is safe by design for
// GODOG_CONCURRENCY>1. TestConcurrentAccess in package
// github exercises the real driver under -race with a FakeClient.
//
// If a future implementation adds mutable state (caches, counters,
// buffers), it must synchronize access or be deep-copied per scenario
// in World.Clone.
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
	// ListOpenChangeProposals returns the repository's open pull
	// requests, including each proposal's head branch.
	ListOpenChangeProposals(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error)
	// ListComments returns the comments on an issue or pull request.
	ListComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error)
	// ListIssueReactions returns the emoji reactions on an issue or
	// pull request. Used by reaction notification assertions.
	ListIssueReactions(ctx context.Context, owner, repo string, number int) ([]forge.Reaction, error)

	// CreateRepo creates a new repository in the given org. It is
	// idempotent — if a repo with the given name already exists,
	// it returns without error.
	CreateRepo(ctx context.Context, org, name, description string) error
	// EnsureRepoPublic verifies that a repository is public and
	// attempts to update its visibility if the org forced it private.
	// Returns an error if the repo cannot be made public.
	EnsureRepoPublic(ctx context.Context, owner, repo string) error
	// GetDefaultBranch returns the name of a repository's default branch.
	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)
	// GetBranchRef returns the HEAD commit SHA for the named branch.
	// Returns an error if the branch ref does not exist (e.g. the
	// fork's Git data has not been replicated yet).
	GetBranchRef(ctx context.Context, owner, repo, branch string) (string, error)
	// DeleteRepo deletes a repository. Returns forge.ErrNotFound
	// if the repository does not exist.
	DeleteRepo(ctx context.Context, owner, repo string) error

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
	// forkOwner/forkRepo:head into baseOwner/baseRepo's base branch.
	// The forkRepo parameter is required to disambiguate same-owner forks
	// (where forkOwner == baseOwner) from branches on the base repo.
	CreateForkChangeProposal(ctx context.Context, baseOwner, baseRepo, title, body, forkOwner, forkRepo, head, base string) (*forge.ChangeProposal, error)
}
