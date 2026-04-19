package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidRoles(t *testing.T) {
	roles := ValidRoles()
	assert.Len(t, roles, 4)
	assert.Contains(t, roles, "fullsend")
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "coder")
	assert.Contains(t, roles, "review")
}

func TestNewOrgConfig(t *testing.T) {
	allRepos := []string{"repo-a", "repo-b", "repo-c"}
	enabledRepos := []string{"repo-a", "repo-c"}
	roles := []string{"fullsend", "triage", "coder", "review"}
	agents := []AgentEntry{
		{Role: "fullsend", Name: "test", Slug: "test-slug"},
	}

	cfg := NewOrgConfig(allRepos, enabledRepos, roles, agents, "")

	assert.Equal(t, "1", cfg.Version)
	assert.Equal(t, "github-actions", cfg.Dispatch.Platform)
	assert.Equal(t, 2, cfg.Defaults.MaxImplementationRetries)
	assert.False(t, cfg.Defaults.AutoMerge)
	assert.Equal(t, roles, cfg.Defaults.Roles)

	assert.True(t, cfg.Repos["repo-a"].Enabled)
	assert.False(t, cfg.Repos["repo-b"].Enabled)
	assert.True(t, cfg.Repos["repo-c"].Enabled)

	assert.Len(t, cfg.Agents, 1)
	assert.Equal(t, "fullsend", cfg.Agents[0].Role)
	assert.Equal(t, "test", cfg.Agents[0].Name)
	assert.Equal(t, "test-slug", cfg.Agents[0].Slug)
}

func TestOrgConfigMarshal(t *testing.T) {
	cfg := &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
			AutoMerge:                false,
		},
		Agents: []AgentEntry{
			{Role: "fullsend", Name: "test-app", Slug: "test-app-slug"},
		},
		Repos: map[string]RepoConfig{
			"my-repo": {Enabled: true},
		},
	}

	data, err := cfg.Marshal()
	require.NoError(t, err)

	output := string(data)
	assert.True(t, strings.HasPrefix(output, "# fullsend organization configuration"))
	assert.Contains(t, output, "https://github.com/fullsend-ai/fullsend")
	assert.Contains(t, output, "This file is managed by fullsend")
	assert.Contains(t, output, "version:")
	assert.Contains(t, output, "github-actions")
	assert.Contains(t, output, "fullsend")
	assert.Contains(t, output, "my-repo")
}

func TestOrgConfigValidate_Valid(t *testing.T) {
	cfg := &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend", "coder"},
			MaxImplementationRetries: 2,
		},
	}

	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestOrgConfigValidate_BadVersion(t *testing.T) {
	cfg := &OrgConfig{
		Version: "2",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestOrgConfigValidate_BadPlatform(t *testing.T) {
	cfg := &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "jenkins",
		},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "platform")
}

func TestOrgConfigValidate_NegativeRetries(t *testing.T) {
	cfg := &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: -1,
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "retries")
}

func TestOrgConfigValidate_InvalidRole(t *testing.T) {
	cfg := &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    []string{"hacker"},
			MaxImplementationRetries: 2,
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "hacker")
}

func TestOrgConfigEnabledRepos(t *testing.T) {
	cfg := &OrgConfig{
		Repos: map[string]RepoConfig{
			"zoo":   {Enabled: true},
			"alpha": {Enabled: false},
			"beta":  {Enabled: true},
		},
	}

	enabled := cfg.EnabledRepos()
	assert.Equal(t, []string{"beta", "zoo"}, enabled)
}

func TestOrgConfigDisabledRepos(t *testing.T) {
	cfg := &OrgConfig{
		Repos: map[string]RepoConfig{
			"zoo":   {Enabled: true},
			"alpha": {Enabled: false},
			"beta":  {Enabled: true},
			"gamma": {Enabled: false},
		},
	}

	disabled := cfg.DisabledRepos()
	assert.Equal(t, []string{"alpha", "gamma"}, disabled)
}

func TestOrgConfigAgentSlugs(t *testing.T) {
	cfg := &OrgConfig{
		Agents: []AgentEntry{
			{Role: "fullsend", Name: "app1", Slug: "slug-1"},
			{Role: "coder", Name: "app2", Slug: "slug-2"},
		},
	}

	slugs := cfg.AgentSlugs()
	assert.Equal(t, "slug-1", slugs["fullsend"])
	assert.Equal(t, "slug-2", slugs["coder"])
	assert.Len(t, slugs, 2)
}

func TestOrgConfigDefaultRoles(t *testing.T) {
	cfg := &OrgConfig{
		Defaults: RepoDefaults{
			Roles: []string{"triage", "review"},
		},
	}

	roles := cfg.DefaultRoles()
	assert.Equal(t, []string{"triage", "review"}, roles)
}

func TestParseOrgConfig(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
    - coder
  max_implementation_retries: 3
  auto_merge: true
agents:
  - role: fullsend
    name: my-app
    slug: my-app-slug
repos:
  repo-x:
    enabled: true
  repo-y:
    enabled: false
`

	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)

	assert.Equal(t, "1", cfg.Version)
	assert.Equal(t, "github-actions", cfg.Dispatch.Platform)
	assert.Equal(t, 3, cfg.Defaults.MaxImplementationRetries)
	assert.True(t, cfg.Defaults.AutoMerge)
	assert.Equal(t, []string{"fullsend", "coder"}, cfg.Defaults.Roles)
	assert.Len(t, cfg.Agents, 1)
	assert.Equal(t, "fullsend", cfg.Agents[0].Role)
	assert.Equal(t, "my-app", cfg.Agents[0].Name)
	assert.Equal(t, "my-app-slug", cfg.Agents[0].Slug)
	assert.True(t, cfg.Repos["repo-x"].Enabled)
	assert.False(t, cfg.Repos["repo-y"].Enabled)
}

func TestNewOrgConfig_WithInferenceProvider(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, nil, nil, "vertex")
	assert.Equal(t, "vertex", cfg.Inference.Provider)
}

func TestNewOrgConfig_WithoutInferenceProvider(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, nil, nil, "")
	assert.Empty(t, cfg.Inference.Provider)
}

func TestOrgConfigValidate_ValidInferenceProvider(t *testing.T) {
	cfg := &OrgConfig{
		Version:   "1",
		Dispatch:  DispatchConfig{Platform: "github-actions"},
		Inference: InferenceConfig{Provider: "vertex"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}
	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestOrgConfigValidate_InvalidInferenceProvider(t *testing.T) {
	cfg := &OrgConfig{
		Version:   "1",
		Dispatch:  DispatchConfig{Platform: "github-actions"},
		Inference: InferenceConfig{Provider: "openai"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "openai")
}

func TestOrgConfigValidate_EmptyInferenceProvider(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}
	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestParseOrgConfig_WithInference(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
inference:
  provider: vertex
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
  auto_merge: false
agents: []
repos: {}
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.Equal(t, "vertex", cfg.Inference.Provider)
}

func TestOrgConfigMarshal_WithInference(t *testing.T) {
	cfg := &OrgConfig{
		Version:   "1",
		Dispatch:  DispatchConfig{Platform: "github-actions"},
		Inference: InferenceConfig{Provider: "vertex"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Agents: []AgentEntry{},
		Repos:  map[string]RepoConfig{},
	}

	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "inference:")
	assert.Contains(t, string(data), "provider: vertex")
}

func TestValidProviders(t *testing.T) {
	providers := ValidProviders()
	assert.Equal(t, []string{"vertex"}, providers)
}
