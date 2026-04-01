package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentAppConfig_Triage(t *testing.T) {
	cfg := AgentAppConfig("my-org", "triage")

	assert.Equal(t, "fullsend-my-org-triage", cfg.Name)
	assert.Contains(t, cfg.Description, "my-org")
	assert.Contains(t, cfg.Description, "triage")
	assert.Contains(t, cfg.URL, "fullsend")

	// Triage: issues write only, no code access
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Empty(t, cfg.Permissions.PullRequests, "triage should not have PR access")
	assert.Empty(t, cfg.Permissions.Contents, "triage should not have contents access")
	assert.Empty(t, cfg.Permissions.Checks, "triage should not have checks access")

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "issue_comment")
}

func TestAgentAppConfig_Coder(t *testing.T) {
	cfg := AgentAppConfig("my-org", "coder")

	assert.Equal(t, "fullsend-my-org-coder", cfg.Name)
	assert.Contains(t, cfg.Description, "my-org")
	assert.Contains(t, cfg.Description, "coder")
	assert.Contains(t, cfg.URL, "fullsend")

	// Coder: issues read, contents write, PRs write, checks read
	assert.Equal(t, "read", cfg.Permissions.Issues, "coder needs issues read to know what to implement")
	assert.Equal(t, "write", cfg.Permissions.Contents)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Checks)

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "pull_request")
}

func TestAgentAppConfig_Review(t *testing.T) {
	cfg := AgentAppConfig("my-org", "review")

	assert.Equal(t, "fullsend-my-org-review", cfg.Name)
	assert.Contains(t, cfg.Description, "my-org")
	assert.Contains(t, cfg.Description, "review")
	assert.Contains(t, cfg.URL, "fullsend")

	// Review: PRs write, contents read, checks read
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Equal(t, "read", cfg.Permissions.Checks)
	assert.Empty(t, cfg.Permissions.Issues, "review should not have issues access")

	assert.Contains(t, cfg.Events, "pull_request")
	assert.Contains(t, cfg.Events, "pull_request_review")
}

func TestAgentAppConfig_UnknownRole(t *testing.T) {
	cfg := AgentAppConfig("my-org", "unknown-role")

	assert.Equal(t, "fullsend-my-org-unknown-role", cfg.Name)
	// Unknown roles get minimal permissions
	assert.Equal(t, "read", cfg.Permissions.Issues)
}

func TestDefaultAgentRoles(t *testing.T) {
	roles := DefaultAgentRoles()
	assert.Equal(t, []string{"triage", "coder", "review"}, roles)
}
