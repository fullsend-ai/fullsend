// Package forge defines the interface for source code forge operations.
//
// The Client interface abstracts repository hosting platforms (GitHub,
// GitLab, Forgejo) so the install and agent workflows can operate
// against any supported forge.
package forge

import "context"

// Repository represents a repository on any forge.
type Repository struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
}

// ChangeProposal represents a proposed code change (GitHub PR, GitLab MR, etc).
type ChangeProposal struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Number int    `json:"number"`
}

// Client is the interface for forge operations needed by fullsend.
// Each supported forge (GitHub, GitLab, Forgejo) implements this interface.
type Client interface {
	// ListOrgRepos returns all non-archived, non-fork repositories in the org/group.
	ListOrgRepos(ctx context.Context, org string) ([]Repository, error)

	// CreateRepo creates a new repository in the organization/group.
	CreateRepo(ctx context.Context, org, name, description string, private bool) (*Repository, error)

	// CreateFile creates a file in a repository on the default branch.
	CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error

	// CreateOrUpdateFile creates a file if it doesn't exist, or updates it if it does.
	CreateOrUpdateFile(ctx context.Context, owner, repo, path, message string, content []byte) error

	// CreateChangeProposal creates a change proposal (PR/MR) from head to base branch.
	CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*ChangeProposal, error)

	// CreateBranch creates a new branch from the default branch.
	CreateBranch(ctx context.Context, owner, repo, branchName string) error

	// CreateFileOnBranch creates a file on a specific branch.
	CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error

	// GetFileContent retrieves the content of a file from a repository.
	GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error)

	// DeleteRepo deletes a repository. This is irreversible.
	DeleteRepo(ctx context.Context, owner, repo string) error

	// GetAuthenticatedUser returns the login of the currently authenticated user.
	GetAuthenticatedUser(ctx context.Context) (string, error)
}
