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
	CloseIssue(ctx context.Context, owner, repo string, number int) error
}
