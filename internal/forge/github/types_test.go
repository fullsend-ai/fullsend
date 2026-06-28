package github

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

func TestDefaultAgentRoles(t *testing.T) {
	roles := DefaultAgentRoles()
	require.Len(t, roles, 6)
	assert.Equal(t, []string{"fullsend", "triage", "coder", "review", "retro", "prioritize"}, roles)
}

func TestAgentAppConfig_Fullsend(t *testing.T) {
	cfg := AgentAppConfig("myorg", "fullsend", "fullsend")

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
	cfg := AgentAppConfig("myorg", "triage", "fullsend")

	assert.Equal(t, "fullsend-triage", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Equal(t, "read", cfg.Permissions.Contents)

	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "issue_comment")
}

func TestAgentAppConfig_Coder(t *testing.T) {
	cfg := AgentAppConfig("myorg", "coder", "fullsend")

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
	cfg := AgentAppConfig("myorg", "review", "fullsend")

	assert.Equal(t, "fullsend-review", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Equal(t, "read", cfg.Permissions.Checks)
	assert.Equal(t, "write", cfg.Permissions.Issues)

	assert.Contains(t, cfg.Events, "pull_request")
}

func TestAgentAppConfig_Prioritize(t *testing.T) {
	cfg := AgentAppConfig("myorg", "prioritize", "fullsend")

	assert.Equal(t, "fullsend-prioritize", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.OrganizationProjects)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Empty(t, cfg.Permissions.PullRequests)

	// Prioritize is cron-driven, no webhook events.
	assert.Empty(t, cfg.Events)
}

func TestAgentAppConfig_Retro(t *testing.T) {
	cfg := AgentAppConfig("myorg", "retro", "fullsend")

	assert.Equal(t, "fullsend-retro", cfg.Name)
	assert.Equal(t, "read", cfg.Permissions.Actions)
	assert.Equal(t, "read", cfg.Permissions.Contents)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Empty(t, cfg.Permissions.OrganizationProjects)

	// Retro is triggered via workflow_dispatch, no webhook events.
	assert.Empty(t, cfg.Events)
}

func TestAgentAppConfig_E2e(t *testing.T) {
	cfg := AgentAppConfig("myorg", "e2e", "fullsend-ai")

	assert.Equal(t, "fullsend-ai-e2e", cfg.Name)
	assert.Equal(t, "write", cfg.Permissions.Actions)
	assert.Equal(t, "read", cfg.Permissions.Variables)
	assert.Equal(t, "write", cfg.Permissions.Administration)
	assert.Equal(t, "write", cfg.Permissions.Contents)
	assert.Equal(t, "write", cfg.Permissions.Issues)
	assert.Equal(t, "write", cfg.Permissions.Members)
	assert.Equal(t, "write", cfg.Permissions.OrganizationAdministration)
	assert.Equal(t, "write", cfg.Permissions.PullRequests)
	assert.Equal(t, "write", cfg.Permissions.Secrets)
	assert.Equal(t, "write", cfg.Permissions.Workflows)
	assert.Empty(t, cfg.Events)
}

// appPermissionsAsMap converts manifest permissions to GitHub API permission names.
func appPermissionsAsMap(p AppPermissions) map[string]string {
	out := make(map[string]string)
	if p.Actions != "" {
		out["actions"] = p.Actions
	}
	if p.Issues != "" {
		out["issues"] = p.Issues
	}
	if p.PullRequests != "" {
		out["pull_requests"] = p.PullRequests
	}
	if p.Checks != "" {
		out["checks"] = p.Checks
	}
	if p.Contents != "" {
		out["contents"] = p.Contents
	}
	if p.Variables != "" {
		out["actions_variables"] = p.Variables
	}
	if p.Workflows != "" {
		out["workflows"] = p.Workflows
	}
	if p.Administration != "" {
		out["administration"] = p.Administration
	}
	if p.Members != "" {
		out["members"] = p.Members
	}
	if p.OrganizationProjects != "" {
		out["organization_projects"] = p.OrganizationProjects
	}
	if p.OrganizationAdministration != "" {
		out["organization_administration"] = p.OrganizationAdministration
	}
	if p.Secrets != "" {
		out["secrets"] = p.Secrets
	}
	return out
}

func TestAgentAppConfig_E2eMatchesMintcorePermissions(t *testing.T) {
	canonical := mintcore.RolePermissionsFor("e2e")
	require.NotNil(t, canonical)

	manifest := appPermissionsAsMap(AgentAppConfig("myorg", "e2e", "fullsend-ai").Permissions)

	// metadata is added at mint token time; GitHub App manifests omit it explicitly.
	for key, want := range canonical {
		if key == "metadata" {
			continue
		}
		got, ok := manifest[key]
		assert.True(t, ok, "AgentAppConfig(e2e) missing permission %q from mintcore canonicalRolePermissions", key)
		assert.Equal(t, want, got, "permission %q mismatch between AgentAppConfig and mintcore", key)
	}
}

func TestAgentAppConfig_UnknownRole(t *testing.T) {
	cfg := AgentAppConfig("myorg", "custom-bot", "fullsend")

	assert.Equal(t, "fullsend-custom-bot", cfg.Name)
	assert.Equal(t, "read", cfg.Permissions.Issues)
	assert.Empty(t, cfg.Permissions.Contents)
	assert.Empty(t, cfg.Permissions.PullRequests)

	assert.Contains(t, cfg.Events, "issues")
}

func TestAgentAppConfig_CustomAppSet(t *testing.T) {
	cfg := AgentAppConfig("myorg", "coder", "my-custom")
	assert.Equal(t, "my-custom-coder", cfg.Name)

	cfg = AgentAppConfig("myorg", "fullsend", "my-custom")
	assert.Equal(t, "my-custom-fullsend", cfg.Name)
}

func TestAgentAppConfig_DefaultAppSet(t *testing.T) {
	cfg := AgentAppConfig("myorg", "coder", "fullsend-ai")
	assert.Equal(t, "fullsend-ai-coder", cfg.Name)

	cfg = AgentAppConfig("myorg", "fullsend", "fullsend-ai")
	assert.Equal(t, "fullsend-ai-fullsend", cfg.Name)
}

func TestAppConfig_RedirectURL_InJSON(t *testing.T) {
	cfg := AgentAppConfig("myorg", "fullsend", "fullsend")
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
