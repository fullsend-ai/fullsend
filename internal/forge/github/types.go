// Package github implements the forge.Client interface for GitHub.
package github

import "fmt"

// AppPermissions describes the GitHub App permissions.
type AppPermissions struct {
	Issues       string `json:"issues,omitempty"`
	PullRequests string `json:"pull_requests,omitempty"`
	Checks       string `json:"checks,omitempty"`
	Contents     string `json:"contents,omitempty"`
}

// AppConfig holds the configuration for creating a GitHub App.
type AppConfig struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	Permissions AppPermissions `json:"permissions"`
	Events      []string       `json:"events"`
}

// AgentAppConfig returns the GitHub App configuration for a specific agent role.
// Each agent gets different permissions following the principle of least privilege:
//
//   - triage: issues read/write — reads issues, labels, and assigns; no code access
//   - coder: contents read/write, pull_requests write, checks read — pushes code, creates PRs
//   - review: pull_requests write, contents read, checks read — reviews PRs, reads code, no push
func AgentAppConfig(org, role string) *AppConfig {
	base := &AppConfig{
		URL: "https://github.com/fullsend-ai/fullsend",
	}

	switch role {
	case "triage":
		base.Name = fmt.Sprintf("fullsend-%s-triage", org)
		base.Description = fmt.Sprintf("fullsend triage agent for %s — issue triage and labeling", org)
		base.Permissions = AppPermissions{
			Issues: "write",
		}
		base.Events = []string{
			"issues",
			"issue_comment",
		}

	case "coder":
		base.Name = fmt.Sprintf("fullsend-%s-coder", org)
		base.Description = fmt.Sprintf("fullsend coder agent for %s — implementation and code changes", org)
		base.Permissions = AppPermissions{
			Issues:       "read",
			Contents:     "write",
			PullRequests: "write",
			Checks:       "read",
		}
		base.Events = []string{
			"issues",
			"issue_comment",
			"pull_request",
			"check_run",
			"check_suite",
		}

	case "review":
		base.Name = fmt.Sprintf("fullsend-%s-review", org)
		base.Description = fmt.Sprintf("fullsend review agent for %s — code review", org)
		base.Permissions = AppPermissions{
			PullRequests: "write",
			Contents:     "read",
			Checks:       "read",
		}
		base.Events = []string{
			"pull_request",
			"pull_request_review",
		}

	default:
		// Unknown role gets minimal permissions
		base.Name = fmt.Sprintf("fullsend-%s-%s", org, role)
		base.Description = fmt.Sprintf("fullsend %s agent for %s", role, org)
		base.Permissions = AppPermissions{
			Issues: "read",
		}
		base.Events = []string{"issues"}
	}

	return base
}

// DefaultAgentRoles returns the standard set of agent roles.
func DefaultAgentRoles() []string {
	return []string{"triage", "coder", "review"}
}
