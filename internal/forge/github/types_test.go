package github

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultAgentRoles(t *testing.T) {
	roles := DefaultAgentRoles()
	require.Len(t, roles, 4)
	assert.Equal(t, []string{"fullsend", "triage", "coder", "review"}, roles)
}

func TestAgentAppConfig_Fullsend(t *testing.T) {
	cfg := AgentAppConfig("myorg", "fullsend")

	assert.Equal(t, "myorg-fullsend", cfg.Name)
	assert.NotEmpty(t, cfg.Description)
	assert.NotEmpty(t, cfg.URL)

	assert.Equal(t, "write", cfg.Permissions.Contents)
	assert.Equal(t, "write", cfg.Permissions.Workflows)
	assert.Equal(t, "read", cfg.Permissions.Issues)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Checks)
	assert.Equal(t, "write", cfg.Permissions.Administration)
	assert.Equal(t, "read", cfg.Permissions.Members)
	assert.Equal(t, "write", cfg.Permissions.OrganizationSecrets)

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "push")
	assert.Contains(t, cfg.Events, "workflow_dispatch")
}

func TestAgentAppConfig_Triage(t *testing.T) {
	cfg := AgentAppConfig("myorg", "triage")

	assert.Equal(t, "myorg-triage", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Empty(t, cfg.Permissions.Contents)

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "issue_comment")
}

func TestAgentAppConfig_Coder(t *testing.T) {
	cfg := AgentAppConfig("myorg", "coder")

	assert.Equal(t, "myorg-coder", cfg.Name)
	assert.Equal(t, "read", cfg.Permissions.Issues)
	assert.Equal(t, "write", cfg.Permissions.Contents)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Checks)

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "issue_comment")
	assert.Contains(t, cfg.Events, "pull_request")
	assert.Contains(t, cfg.Events, "check_run")
	assert.Contains(t, cfg.Events, "check_suite")
}

func TestAgentAppConfig_Review(t *testing.T) {
	cfg := AgentAppConfig("myorg", "review")

	assert.Equal(t, "myorg-review", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Equal(t, "read", cfg.Permissions.Checks)
	assert.Equal(t, "read", cfg.Permissions.Issues)

	assert.Contains(t, cfg.Events, "pull_request")
	assert.Contains(t, cfg.Events, "pull_request_review")
}

func TestAgentAppConfig_UnknownRole(t *testing.T) {
	cfg := AgentAppConfig("myorg", "custom-bot")

	assert.Equal(t, "myorg-custom-bot", cfg.Name)
	assert.Equal(t, "read", cfg.Permissions.Issues)
	assert.Empty(t, cfg.Permissions.Contents)
	assert.Empty(t, cfg.Permissions.PullRequests)

	assert.Contains(t, cfg.Events, "issues")
}

func TestAppConfig_RedirectURL_InJSON(t *testing.T) {
	cfg := AgentAppConfig("myorg", "fullsend")
	cfg.RedirectURL = "http://127.0.0.1:12345/callback"

	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	var raw map[string]interface{}
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	redirectURL, ok := raw["redirect_url"]
	assert.True(t, ok, "JSON must contain redirect_url key")
	assert.Equal(t, "http://127.0.0.1:12345/callback", redirectURL)
}
