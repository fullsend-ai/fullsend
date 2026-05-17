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

// PerRepoGuardVar is the repo variable set by per-repo install to prevent
// per-org enrollment from overriding a per-repo installation.
const PerRepoGuardVar = "FULLSEND_PER_REPO_INSTALL"

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
	Body   string
	URL    string
	Labels []string
}

// IssueComment represents a comment on an issue.
type IssueComment struct {
	ID        int
	NodeID    string
	HTMLURL   string
	Body      string
	Author    string
	CreatedAt string
}

// PullRequestReview represents a formal review on a pull request.
type PullRequestReview struct {
	ID          int
	NodeID      string
	User        string
	State       string // "APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED"
	Body        string
	SubmittedAt string
}

// ReviewComment represents an inline comment on a specific line of a
// pull request diff. These are submitted as part of a formal PR review
// via the GitHub "Create a review" API.
type ReviewComment struct {
	Path string // relative file path in the repository
	Line int    // line number in the diff (right side)
	Body string // comment body (Markdown)
}

// Installation represents an app installation on an org.
type Installation struct {
	ID          int
	AppID       int
	AppSlug     string
	Permissions map[string]string
}

// TreeFile represents a file to be committed via the Git Trees API.
// Mode controls file permissions: "100644" for regular files,
// "100755" for executable files (e.g., shell scripts).
type TreeFile struct {
	Path    string
	Content []byte
	Mode    string // "100644" or "100755"
}

// Client abstracts all git forge operations.
// Implementations exist for GitHub (and eventually GitLab, Forgejo).
type Client interface {
	// Repository operations
	// ListOrgRepos returns repositories eligible for fullsend enrollment.
	// It excludes archived repos (no active development), forks, and
	// private repos.
	//
	// Private repos are excluded because the default .fullsend config repo
	// is public, and agent workflows dispatched to it run with public logs.
	// Enrolling a private repo would expose its code in those logs when
	// agents check out and process the repo content. Private repo support
	// requires per-repo .fullsend mode where agents run on the target repo.
	//
	// Forks are excluded because fullsend's trust model is org-centric:
	// trust derives from org repository permissions and CODEOWNERS
	// governance. Forks may live outside the org's permission boundary
	// or lack the same CODEOWNERS configuration, which could bypass
	// human-approval gates. Installing on both a fork and its upstream
	// also risks duplicate agent PRs and conflicting changes.
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
	DeleteFile(ctx context.Context, owner, repo, path, message string) error

	// CommitFiles atomically commits multiple files to the repository's
	// default branch in a single commit. It is idempotent: if all files
	// already have the expected content and mode, no commit is created
	// and it returns (false, nil).
	CommitFiles(ctx context.Context, owner, repo, message string, files []TreeFile) (committed bool, err error)

	// Branch operations
	CreateBranch(ctx context.Context, owner, repo, branchName string) error
	CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error
	// CreateOrUpdateFileOnBranch creates or updates a file on a specific branch.
	// Combines SHA-aware upsert with branch targeting.
	CreateOrUpdateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error

	// Change proposals (PRs/MRs)
	CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*ChangeProposal, error)
	ListRepoPullRequests(ctx context.Context, owner, repo string) ([]ChangeProposal, error)

	// Organization metadata
	// GetOrgPlan returns the billing plan name for the org (e.g. "free", "team", "enterprise").
	GetOrgPlan(ctx context.Context, org string) (string, error)

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
	GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error)

	// Org-level secrets (for cross-repo dispatch tokens)
	CreateOrgSecret(ctx context.Context, org, name, value string, selectedRepoIDs []int64) error
	OrgSecretExists(ctx context.Context, org, name string) (bool, error)
	DeleteOrgSecret(ctx context.Context, org, name string) error
	SetOrgSecretRepos(ctx context.Context, org, name string, repoIDs []int64) error
	// GetOrgSecretRepos returns the list of repository IDs that have access
	// to the given org-level secret.
	GetOrgSecretRepos(ctx context.Context, org, name string) ([]int64, error)

	// Org-level variables (for dispatch function URL)
	CreateOrUpdateOrgVariable(ctx context.Context, org, name, value string, selectedRepoIDs []int64) error
	OrgVariableExists(ctx context.Context, org, name string) (bool, error)
	DeleteOrgVariable(ctx context.Context, org, name string) error

	// CI/Workflow operations
	GetLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string) (*WorkflowRun, error)
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*WorkflowRun, error)
	DispatchWorkflow(ctx context.Context, owner, repo, workflowFile, ref string, inputs map[string]string) error

	// Issue operations
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*Issue, error)
	CloseIssue(ctx context.Context, owner, repo string, number int) error
	ListOpenIssues(ctx context.Context, owner, repo string, labels ...string) ([]Issue, error)
	ListIssueComments(ctx context.Context, owner, repo string, number int) ([]IssueComment, error)
	CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (*IssueComment, error)
	UpdateIssueComment(ctx context.Context, owner, repo string, commentID int, body string) error
	MinimizeComment(ctx context.Context, nodeID, reason string) error

	// Pull request operations
	GetPullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error)

	// Pull request review operations.
	// commitSHA, when non-empty, pins the review to a specific commit.
	// GitHub rejects the request if the commit is not the PR's current HEAD.
	// comments, when non-nil, attaches inline diff comments to the review.
	CreatePullRequestReview(ctx context.Context, owner, repo string, number int, event, body, commitSHA string, comments []ReviewComment) error
	ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]PullRequestReview, error)
	DismissPullRequestReview(ctx context.Context, owner, repo string, number, reviewID int, message string) error

	// Change proposal merge
	MergeChangeProposal(ctx context.Context, owner, repo string, number int) error

	// Workflow run listing
	ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]WorkflowRun, error)

	// GetWorkflowRunLogs downloads the logs for a workflow run as plain text.
	// On GitHub, this fetches job logs for each job in the run.
	GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error)

	// App installation operations
	ListOrgInstallations(ctx context.Context, org string) ([]Installation, error)

	// GetAppClientID returns the Client ID for a GitHub App identified by slug.
	GetAppClientID(ctx context.Context, slug string) (string, error)
}
