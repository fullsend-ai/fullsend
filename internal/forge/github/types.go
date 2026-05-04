package github

import "fmt"

// AppPermissions defines the permissions for a GitHub App.
type AppPermissions struct {
	Actions        string `json:"actions,omitempty"`
	Issues         string `json:"issues,omitempty"`
	PullRequests   string `json:"pull_requests,omitempty"`
	Checks         string `json:"checks,omitempty"`
	Contents       string `json:"contents,omitempty"`
	Workflows      string `json:"workflows,omitempty"`
	Administration string `json:"administration,omitempty"`
	Members        string `json:"members,omitempty"`
}

// HookAttributes configures the webhook for a GitHub App.
// Even when webhooks are not used, GitHub requires this field in the manifest.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// AppConfig defines the configuration for creating a GitHub App via the
// manifest flow. See https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest
type AppConfig struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	URL            string         `json:"url"`
	HookAttributes HookAttributes `json:"hook_attributes"`
	RedirectURL    string         `json:"redirect_url,omitempty"`
	Public         bool           `json:"public"`
	Permissions    AppPermissions `json:"default_permissions"`
	Events         []string       `json:"default_events"`
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
		// hook_attributes is required by the manifest spec even when we
		// don't use webhooks. Setting active: false disables delivery.
		HookAttributes: HookAttributes{
			URL:    fmt.Sprintf("https://github.com/%s", org),
			Active: false,
		},
	}

	// App naming convention: <org>-<role> for all roles.
	base.Name = fmt.Sprintf("%s-%s", org, role)

	switch role {
	case "fullsend":
		base.Description = fmt.Sprintf("Fullsend orchestrator for %s", org)
		base.Permissions = AppPermissions{
			Actions:        "write",
			Contents:       "write",
			Workflows:      "write",
			Issues:         "read",
			PullRequests:   "write",
			Checks:         "read",
			Administration: "write",
			Members:        "read",
		}
		base.Events = []string{"issues", "push", "workflow_dispatch"}

	case "triage":
		base.Description = fmt.Sprintf("Fullsend triage agent for %s", org)
		base.Permissions = AppPermissions{
			Issues: "write",
		}
		base.Events = []string{"issues", "issue_comment"}

	case "coder":
		base.Description = fmt.Sprintf("Fullsend coder agent for %s", org)
		base.Permissions = AppPermissions{
			Issues:       "read",
			Contents:     "write",
			PullRequests: "write",
			Checks:       "read",
		}
		base.Events = []string{"issues", "issue_comment", "pull_request", "check_run", "check_suite"}

	case "review":
		base.Description = fmt.Sprintf("Fullsend review agent for %s", org)
		base.Permissions = AppPermissions{
			PullRequests: "write",
			Contents:     "read",
			Checks:       "read",
			Issues:       "read",
		}
		base.Events = []string{"pull_request"}

	default:
		base.Description = fmt.Sprintf("Fullsend %s agent for %s", role, org)
		base.Permissions = AppPermissions{
			Issues: "read",
		}
		base.Events = []string{"issues"}
	}

	return base
}
