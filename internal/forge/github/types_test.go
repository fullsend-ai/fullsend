package github

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultAgentRoles(t *testing.T) {
	roles := DefaultAgentRoles()
	require.Len(t, roles, 5)
	assert.Equal(t, []string{"fullsend", "triage", "coder", "review", "fix"}, roles)
}

func TestAgentAppConfig_Fullsend(t *testing.T) {
	cfg := AgentAppConfig("myorg", "fullsend")

	assert.Equal(t, "fullsend-fullsend", cfg.Name)
	assert.NotEmpty(t, cfg.Description)
	assert.NotEmpty(t, cfg.URL)

	assert.Equal(t, "write", cfg.Permissions.Contents)
	assert.Equal(t, "write", cfg.Permissions.Workflows)
	assert.Equal(t, "read", cfg.Permissions.Issues)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Checks)
	assert.Equal(t, "write", cfg.Permissions.Administration)
	assert.Equal(t, "read", cfg.Permissions.Members)
	assert.Equal(t, "read", cfg.Permissions.Variables)
	assert.Equal(t, "read", cfg.Permissions.OrganizationProjects)

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "push")
	assert.Contains(t, cfg.Events, "workflow_dispatch")
}

func TestAgentAppConfig_Triage(t *testing.T) {
	cfg := AgentAppConfig("myorg", "triage")

	assert.Equal(t, "fullsend-triage", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Equal(t, "read", cfg.Permissions.Contents)

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "issue_comment")
}

func TestAgentAppConfig_Coder(t *testing.T) {
	cfg := AgentAppConfig("myorg", "coder")

	assert.Equal(t, "fullsend-coder", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.Issues)
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

	assert.Equal(t, "fullsend-review", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Equal(t, "read", cfg.Permissions.Checks)
	assert.Equal(t, "write", cfg.Permissions.Issues)

	assert.Contains(t, cfg.Events, "pull_request")
}

func TestAgentAppConfig_Prioritize(t *testing.T) {
	cfg := AgentAppConfig("myorg", "prioritize")

	assert.Equal(t, "fullsend-prioritize", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.OrganizationProjects)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Empty(t, cfg.Permissions.PullRequests)

	// Prioritize is cron-driven, no webhook events.
	assert.Empty(t, cfg.Events)
}

func TestAgentAppConfig_Retro(t *testing.T) {
	cfg := AgentAppConfig("myorg", "retro")

	assert.Equal(t, "fullsend-retro", cfg.Name)
	assert.Equal(t, "read", cfg.Permissions.Actions)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Equal(t, "read", cfg.Permissions.PullRequests)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Empty(t, cfg.Permissions.OrganizationProjects)

	// Retro is triggered via workflow_dispatch, no webhook events.
	assert.Empty(t, cfg.Events)
}

func TestAgentAppConfig_UnknownRole(t *testing.T) {
	cfg := AgentAppConfig("myorg", "custom-bot")

	assert.Equal(t, "fullsend-custom-bot", cfg.Name)
	assert.Equal(t, "read", cfg.Permissions.Issues)
	assert.Empty(t, cfg.Permissions.Contents)
	assert.Empty(t, cfg.Permissions.PullRequests)

	assert.Contains(t, cfg.Events, "issues")
}

func TestAppPermissions_ActionsVariablesKey(t *testing.T) {
	p := AppPermissions{Variables: "read"}
	b, err := json.Marshal(p)
	require.NoError(t, err)

	assert.True(t, strings.Contains(string(b), `"actions_variables"`),
		"expected actions_variables key, got: %s", b)
	assert.False(t, strings.Contains(string(b), `"variables"`),
		"should not contain bare 'variables' key: %s", b)
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
