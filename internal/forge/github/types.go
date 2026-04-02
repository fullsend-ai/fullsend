package github

import "fmt"

// AppPermissions defines the permissions for a GitHub App.
type AppPermissions struct {
	Issues         string `json:"issues,omitempty"`
	PullRequests   string `json:"pull_requests,omitempty"`
	Checks         string `json:"checks,omitempty"`
	Contents       string `json:"contents,omitempty"`
	Administration string `json:"administration,omitempty"`
	Members        string `json:"members,omitempty"`
}

// AppConfig defines the configuration for creating a GitHub App.
type AppConfig struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	Permissions AppPermissions `json:"default_permissions"`
	Events      []string       `json:"default_events"`
}

// DefaultAgentRoles returns the standard set of agent roles.
func DefaultAgentRoles() []string {
	return []string{"fullsend", "triage", "coder", "review"}
}

// AgentAppConfig returns the GitHub App configuration for a given agent role.
//
// Important: GitHub validates that event subscriptions are backed by matching
// permissions. For example, subscribing to "issues" events requires at least
// issues:read permission. Subscribing to "issue_comment" requires issues:read
// or issues:write. Mismatches cause the manifest to be rejected. Every Events
// entry below must have a corresponding permission.
func AgentAppConfig(org, role string) AppConfig {
	base := AppConfig{
		URL: fmt.Sprintf("https://github.com/%s", org),
	}

	switch role {
	case "fullsend":
		base.Name = fmt.Sprintf("fullsend-%s", org)
		base.Description = fmt.Sprintf("Fullsend orchestrator for %s", org)
		base.Permissions = AppPermissions{
			Contents:       "write",
			Issues:         "read",
			PullRequests:   "write",
			Checks:         "read",
			Administration: "write",
			Members:        "read",
		}
		base.Events = []string{"issues", "push", "workflow_dispatch"}

	case "triage":
		base.Name = fmt.Sprintf("fullsend-%s-triage", org)
		base.Description = fmt.Sprintf("Fullsend triage agent for %s", org)
		base.Permissions = AppPermissions{
			Issues: "write",
		}
		base.Events = []string{"issues", "issue_comment"}

	case "coder":
		base.Name = fmt.Sprintf("fullsend-%s-coder", org)
		base.Description = fmt.Sprintf("Fullsend coder agent for %s", org)
		base.Permissions = AppPermissions{
			Issues:       "read",
			Contents:     "write",
			PullRequests: "write",
			Checks:       "read",
		}
		base.Events = []string{"issues", "issue_comment", "pull_request", "check_run", "check_suite"}

	case "review":
		base.Name = fmt.Sprintf("fullsend-%s-review", org)
		base.Description = fmt.Sprintf("Fullsend review agent for %s", org)
		base.Permissions = AppPermissions{
			PullRequests: "write",
			Contents:     "read",
			Checks:       "read",
		}
		base.Events = []string{"pull_request", "pull_request_review"}

	default:
		base.Name = fmt.Sprintf("fullsend-%s-%s", org, role)
		base.Description = fmt.Sprintf("Fullsend %s agent for %s", role, org)
		base.Permissions = AppPermissions{
			Issues: "read",
		}
		base.Events = []string{"issues"}
	}

	return base
}
