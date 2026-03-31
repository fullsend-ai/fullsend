package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNewOrgConfig_Defaults(t *testing.T) {
	repos := []string{"api", "web", "docs"}
	cfg := NewOrgConfig(repos, nil, nil)

	assert.Equal(t, "1", cfg.Version)
	assert.Equal(t, "github-actions", cfg.Dispatch.Platform)
	assert.False(t, cfg.Defaults.AutoMerge, "auto_merge should default to false")
	assert.Equal(t, 2, cfg.Defaults.MaxImplementationRetries)
	assert.Equal(t, DefaultAgents(), cfg.Defaults.Agents)

	// All repos should be listed but disabled
	assert.Len(t, cfg.Repos, 3)
	for _, name := range repos {
		repo, ok := cfg.Repos[name]
		require.True(t, ok, "repo %s should be in config", name)
		assert.False(t, repo.Enabled, "repo %s should be disabled by default", name)
	}
}

func TestNewOrgConfig_EnabledRepos(t *testing.T) {
	repos := []string{"api", "web", "docs"}
	enabled := []string{"api", "docs"}

	cfg := NewOrgConfig(repos, enabled, nil)

	assert.True(t, cfg.Repos["api"].Enabled)
	assert.False(t, cfg.Repos["web"].Enabled)
	assert.True(t, cfg.Repos["docs"].Enabled)
}

func TestNewOrgConfig_CustomAgents(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, []string{"review", "implementation"})

	assert.Equal(t, []string{"review", "implementation"}, cfg.Defaults.Agents)
}

func TestNewOrgConfig_EmptyRepos(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, nil)

	assert.Empty(t, cfg.Repos)
	assert.Equal(t, DefaultAgents(), cfg.Defaults.Agents)
}

func TestOrgConfig_EnabledRepos(t *testing.T) {
	cfg := &OrgConfig{
		Repos: map[string]RepoConfig{
			"api":  {Enabled: true},
			"web":  {Enabled: false},
			"docs": {Enabled: true},
			"cli":  {Enabled: false},
		},
	}

	enabled := cfg.EnabledRepos()
	assert.Len(t, enabled, 2)
	assert.Contains(t, enabled, "api")
	assert.Contains(t, enabled, "docs")
}

func TestOrgConfig_EnabledRepos_NoneEnabled(t *testing.T) {
	cfg := &OrgConfig{
		Repos: map[string]RepoConfig{
			"api": {Enabled: false},
			"web": {Enabled: false},
		},
	}

	assert.Empty(t, cfg.EnabledRepos())
}

func TestOrgConfig_Marshal(t *testing.T) {
	cfg := NewOrgConfig([]string{"api", "web"}, []string{"api"}, nil)

	data, err := cfg.Marshal()
	require.NoError(t, err)

	// Should have a header comment
	assert.Contains(t, string(data), "# fullsend organization configuration")
	assert.Contains(t, string(data), "https://github.com/fullsend-ai/fullsend")

	// Should be valid YAML after the comments
	var parsed OrgConfig
	err = yaml.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "1", parsed.Version)
	assert.Equal(t, "github-actions", parsed.Dispatch.Platform)
	assert.False(t, parsed.Defaults.AutoMerge)
	assert.True(t, parsed.Repos["api"].Enabled)
	assert.False(t, parsed.Repos["web"].Enabled)
}

func TestOrgConfig_Validate_Valid(t *testing.T) {
	cfg := NewOrgConfig([]string{"api"}, nil, nil)
	assert.NoError(t, cfg.Validate())
}

func TestOrgConfig_Validate_MissingVersion(t *testing.T) {
	cfg := &OrgConfig{
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{Agents: DefaultAgents()},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version is required")
}

func TestOrgConfig_Validate_InvalidPlatform(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "kubernetes"},
		Defaults: RepoDefaults{Agents: DefaultAgents()},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported dispatch platform")
}

func TestOrgConfig_Validate_NegativeRetries(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			MaxImplementationRetries: -1,
			Agents:                   DefaultAgents(),
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max_implementation_retries must be non-negative")
}

func TestOrgConfig_Validate_InvalidAgent(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Agents: []string{"triage", "foobar"},
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown agent role")
}

func TestDefaultAgents(t *testing.T) {
	assert.Equal(t, []string{"triage", "implementation", "review"}, DefaultAgents())
}
