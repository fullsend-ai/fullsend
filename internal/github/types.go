// Package github provides GitHub API client abstractions for fullsend.
//
// It defines interfaces for GitHub operations so the install and other
// commands can be tested without hitting the real API. The Client interface
// is implemented by a real GitHub client and by test fakes.
package github

import "context"

// AppPermissions describes the GitHub App permissions requested during creation.
type AppPermissions struct {
	Issues   string `json:"issues"`
	PullReqs string `json:"pull_requests"`
	Checks   string `json:"checks"`
	Contents string `json:"contents"`
}

// AppConfig holds the configuration for creating a GitHub App.
type AppConfig struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	Permissions AppPermissions `json:"permissions"`
	Events      []string       `json:"events"`
}

// Repository represents a GitHub repository.
type Repository struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
}

// PullRequest represents a created pull request.
type PullRequest struct {
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Number  int    `json:"number"`
}

// Client is the interface for GitHub API operations needed by fullsend.
// It is designed to be mockable for unit testing.
type Client interface {
	// ListOrgRepos returns all non-archived, non-fork repositories in the org.
	ListOrgRepos(ctx context.Context, org string) ([]Repository, error)

	// CreateRepo creates a new repository in the organization.
	CreateRepo(ctx context.Context, org string, name string, description string, private bool) (*Repository, error)

	// CreateFile creates a file in a repository.
	CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error

	// CreatePullRequest creates a PR from head branch to base branch.
	CreatePullRequest(ctx context.Context, owner, repo, title, body, head, base string) (*PullRequest, error)

	// CreateBranch creates a new branch from the default branch.
	CreateBranch(ctx context.Context, owner, repo, branchName string) error

	// CreateFileOnBranch creates a file on a specific branch.
	CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error
}

// DefaultAppConfig returns the standard GitHub App configuration for fullsend.
// Permissions follow the principle of least privilege per the acceptance criteria:
// issues read/write, PRs read/write, checks read, contents write.
func DefaultAppConfig(org string) *AppConfig {
	return &AppConfig{
		Name:        "fullsend-" + org,
		Description: "Autonomous agentic development pipeline for " + org,
		URL:         "https://github.com/fullsend-ai/fullsend",
		Permissions: AppPermissions{
			Issues:   "write",
			PullReqs: "write",
			Checks:   "read",
			Contents: "write",
		},
		Events: []string{
			"issues",
			"issue_comment",
			"pull_request",
			"pull_request_review",
			"check_run",
			"check_suite",
		},
	}
}
