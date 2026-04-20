// Package forge defines the interface for interacting with git forges
// (GitHub, GitLab, Forgejo). All forge-specific operations flow through
// the Client interface, keeping the rest of the codebase forge-agnostic.
package forge

import (
	"context"
	"errors"
)

// ConfigRepoName is the conventional name for the org-level fullsend
// configuration repository. See ADR-0003.
const ConfigRepoName = ".fullsend"

// ErrNotFound indicates a requested resource was not found on the forge.
var ErrNotFound = errors.New("not found")

// IsNotFound reports whether err indicates a resource was not found.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// Repository represents a repository on a git forge.
type Repository struct {
	ID            int64
	Name          string
	FullName      string
	DefaultBranch string
	Private       bool
	Archived      bool
	Fork          bool
}

// ChangeProposal represents a pull request or merge request.
type ChangeProposal struct {
	URL    string
	Title  string
	Number int
}

// WorkflowRun represents a CI/CD workflow execution.
type WorkflowRun struct {
	ID         int
	Name       string
	Status     string // "queued", "in_progress", "completed"
	Conclusion string // "success", "failure", "cancelled", etc.
	HTMLURL    string
	CreatedAt  string
}

// Issue represents a forge issue.
type Issue struct {
	Number int
	Title  string
	URL    string
}

// Installation represents an app installation on an org.
type Installation struct {
	ID      int
	AppID   int
	AppSlug string
}

// Client abstracts all git forge operations.
// Implementations exist for GitHub (and eventually GitLab, Forgejo).
type Client interface {
	// Repository operations
	ListOrgRepos(ctx context.Context, org string) ([]Repository, error)
	GetRepo(ctx context.Context, owner, repo string) (*Repository, error)
	CreateRepo(ctx context.Context, org, name, description string, private bool) (*Repository, error)
	DeleteRepo(ctx context.Context, owner, repo string) error

	// File operations
	CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error

	// CreateOrUpdateFile creates a file or updates it if it already exists.
	// On GitHub, updating an existing file requires the current file's SHA
	// (optimistic concurrency control). The GitHub implementation handles
	// this by fetching the existing SHA before writing. Without it, the
	// API returns a 422 "sha wasn't supplied" error.
	CreateOrUpdateFile(ctx context.Context, owner, repo, path, message string, content []byte) error

	GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error)

	// DeleteFile deletes a file from a repository. Idempotent: returns nil
	// if the file does not exist. On GitHub, deleting requires the file's
	// current SHA, which the implementation fetches automatically.
	DeleteFile(ctx context.Context, owner, repo, path, message string) error

	// Branch operations
	CreateBranch(ctx context.Context, owner, repo, branchName string) error
	CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error
	// CreateOrUpdateFileOnBranch creates or updates a file on a specific branch.
	// Combines SHA-aware upsert with branch targeting.
	CreateOrUpdateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error

	// Change proposals (PRs/MRs)
	CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*ChangeProposal, error)
	ListRepoPullRequests(ctx context.Context, owner, repo string) ([]ChangeProposal, error)

	// Authentication
	GetAuthenticatedUser(ctx context.Context) (string, error)

	// GetTokenScopes returns the OAuth scopes granted to the current token.
	// On GitHub, this is read from the X-OAuth-Scopes response header.
	// Returns nil (not an error) if the forge doesn't support scope introspection.
	GetTokenScopes(ctx context.Context) ([]string, error)

	// Secrets and variables
	CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error
	RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error)
	CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error
	RepoVariableExists(ctx context.Context, owner, repo, name string) (bool, error)

	// Org-level secrets (for cross-repo dispatch tokens)
	CreateOrgSecret(ctx context.Context, org, name, value string, selectedRepoIDs []int64) error
	OrgSecretExists(ctx context.Context, org, name string) (bool, error)
	DeleteOrgSecret(ctx context.Context, org, name string) error
	SetOrgSecretRepos(ctx context.Context, org, name string, repoIDs []int64) error

	// CI/Workflow operations
	GetLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string) (*WorkflowRun, error)
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*WorkflowRun, error)
	DispatchWorkflow(ctx context.Context, owner, repo, workflowFile, ref string, inputs map[string]string) error

	// Issue operations
	CreateIssue(ctx context.Context, owner, repo, title, body string) (*Issue, error)
	CloseIssue(ctx context.Context, owner, repo string, number int) error

	// Change proposal merge
	MergeChangeProposal(ctx context.Context, owner, repo string, number int) error

	// Workflow run listing
	ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]WorkflowRun, error)

	// GetWorkflowRunLogs downloads the logs for a workflow run as plain text.
	// On GitHub, this fetches job logs for each job in the run.
	GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error)

	// App installation operations
	ListOrgInstallations(ctx context.Context, org string) ([]Installation, error)
}
