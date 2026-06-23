package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

func TestValidRoles(t *testing.T) {
	roles := ValidRoles()
	assert.Len(t, roles, 8)
	assert.Contains(t, roles, "fullsend")
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "coder")
	assert.Contains(t, roles, "review")
	assert.Contains(t, roles, "fix")
	assert.Contains(t, roles, "retro")
	assert.Contains(t, roles, "prioritize")
	assert.Contains(t, roles, "e2e")
}

func TestValidRoles_RecognizedByMintcore(t *testing.T) {
	for _, role := range ValidRoles() {
		assert.True(t, mintcore.HasRole(role),
			"ValidRoles() contains %q but mintcore.HasRole is false — role lists may have drifted (see issue tracking consolidation)", role)
	}
}

func TestPerRepoDefaultRoles(t *testing.T) {
	roles := PerRepoDefaultRoles()
	assert.Len(t, roles, 6)
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "coder")
	assert.Contains(t, roles, "review")
	assert.Contains(t, roles, "fix")
	assert.Contains(t, roles, "retro")
	assert.Contains(t, roles, "prioritize")
	// "fullsend" dispatch role must be excluded in per-repo mode.
	assert.NotContains(t, roles, "fullsend")
}

func TestNewOrgConfig(t *testing.T) {
	allRepos := []string{"repo-a", "repo-b", "repo-c"}
	enabledRepos := []string{"repo-a", "repo-c"}
	roles := []string{"fullsend", "triage", "coder", "review"}

	cfg := NewOrgConfig(allRepos, enabledRepos, roles, "", "")

	assert.Equal(t, "1", cfg.Version)
	assert.Equal(t, "github-actions", cfg.Dispatch.Platform)
	assert.Equal(t, 2, cfg.Defaults.MaxImplementationRetries)
	assert.False(t, cfg.Defaults.AutoMerge)
	assert.Equal(t, roles, cfg.Defaults.Roles)

	assert.True(t, cfg.Repos["repo-a"].Enabled)
	assert.False(t, cfg.Repos["repo-b"].Enabled)
	assert.True(t, cfg.Repos["repo-c"].Enabled)

	assert.Equal(t, []string{"https://raw.githubusercontent.com/fullsend-ai/fullsend/"}, cfg.AllowedRemoteResources)
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

func TestOrgConfigValidate_DuplicateRole(t *testing.T) {
	cfg := &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend", "coder", "fullsend"},
			MaxImplementationRetries: 2,
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate role")
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
	assert.True(t, cfg.Repos["repo-x"].Enabled)
	assert.False(t, cfg.Repos["repo-y"].Enabled)
}

func TestParseOrgConfig_IgnoresLegacyAgentsBlock(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents:
  - role: fullsend
    name: my-app
    slug: my-app-slug
repos: {}
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.Equal(t, "1", cfg.Version)
}

func TestNewOrgConfig_WithInferenceProvider(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, nil, "vertex", "")
	assert.Equal(t, "vertex", cfg.Inference.Provider)
}

func TestNewOrgConfig_WithoutInferenceProvider(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, nil, "", "")
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
		Repos: map[string]RepoConfig{},
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

func TestParseOrgConfig_KillSwitch(t *testing.T) {
	yamlData := `
version: "1"
kill_switch: true
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents: []
repos: {}
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.True(t, cfg.KillSwitch)
}

func TestParseOrgConfig_KillSwitchDefault(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents: []
repos: {}
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.False(t, cfg.KillSwitch)
}

func TestOrgConfigMarshal_KillSwitch(t *testing.T) {
	cfg := &OrgConfig{
		Version:    "1",
		KillSwitch: true,
		Dispatch:   DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos: map[string]RepoConfig{},
	}

	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "kill_switch: true")
}

func TestOrgConfigValidate_FixRole(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend", "review", "fix"},
			MaxImplementationRetries: 2,
		},
	}
	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestNewOrgConfig_KillSwitchDefaultFalse(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, []string{"fullsend"}, "", "")
	assert.False(t, cfg.KillSwitch)
}

func TestOrgConfigMarshal_KillSwitchOmitEmpty(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos: map[string]RepoConfig{},
	}

	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "kill_switch")
}

func TestOrgConfigValidate_DispatchModeEmpty(t *testing.T) {
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

func TestOrgConfigValidate_DispatchModePAT_Rejected(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions", Mode: "pat"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported dispatch mode")
}

func TestOrgConfigValidate_DispatchModeOIDCMint(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions", Mode: "oidc-mint"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}
	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestOrgConfigValidate_InvalidDispatchMode(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions", Mode: "invalid"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
	assert.Contains(t, err.Error(), "dispatch mode")
}

func TestParseOrgConfig_WithDispatchMode(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://fullsend-mint.run.app
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
	assert.Equal(t, "oidc-mint", cfg.Dispatch.Mode)
	assert.Equal(t, "https://fullsend-mint.run.app", cfg.Dispatch.MintURL)
}

func TestOrgConfigMarshal_WithDispatchMode(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions", Mode: "oidc-mint", MintURL: "https://fullsend-mint.run.app"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos: map[string]RepoConfig{},
	}

	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "mode: oidc-mint")
	assert.Contains(t, string(data), "mint_url: https://fullsend-mint.run.app")
}

func TestNewPerRepoConfig_DefaultRoles(t *testing.T) {
	cfg := NewPerRepoConfig(nil, "")
	assert.Equal(t, "1", cfg.Version)
	assert.Equal(t, DefaultAgentRoles(), cfg.Roles)
	assert.False(t, cfg.KillSwitch)
}

func TestNewPerRepoConfig_CustomRoles(t *testing.T) {
	cfg := NewPerRepoConfig([]string{"triage", "review"}, "")
	assert.Equal(t, []string{"triage", "review"}, cfg.Roles)
}

func TestPerRepoConfigValidate_Valid(t *testing.T) {
	cfg := &PerRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "triage", "coder"},
	}
	assert.NoError(t, cfg.Validate())
}

func TestPerRepoConfigValidate_InvalidVersion(t *testing.T) {
	cfg := &PerRepoConfig{
		Version: "2",
		Roles:   []string{"fullsend"},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported version")
}

func TestPerRepoConfigValidate_InvalidRole(t *testing.T) {
	cfg := &PerRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "invalid-role"},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestPerRepoConfigValidate_DuplicateRole(t *testing.T) {
	cfg := &PerRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "triage", "fullsend"},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate role")
}

func TestPerRepoConfigValidate_EmptyRoles(t *testing.T) {
	cfg := &PerRepoConfig{
		Version: "1",
		Roles:   []string{},
	}
	assert.NoError(t, cfg.Validate())
}

func TestParsePerRepoConfig(t *testing.T) {
	yamlData := `
version: "1"
kill_switch: true
roles:
  - fullsend
  - triage
  - review
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.Equal(t, "1", cfg.Version)
	assert.True(t, cfg.KillSwitch)
	assert.Equal(t, []string{"fullsend", "triage", "review"}, cfg.Roles)
}

func TestParsePerRepoConfig_Invalid(t *testing.T) {
	_, err := ParsePerRepoConfig([]byte("not: [valid: yaml"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing per-repo config")
}

func TestPerRepoConfigMarshal(t *testing.T) {
	cfg := &PerRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "triage"},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "fullsend per-repo configuration")
	assert.Contains(t, string(data), "version: \"1\"")
	assert.Contains(t, string(data), "- fullsend")
	assert.Contains(t, string(data), "- triage")
}

func TestPerRepoConfigMarshal_KillSwitchOmitted(t *testing.T) {
	cfg := &PerRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend"},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "kill_switch")
}

func TestPerRepoConfig_RoundTrip(t *testing.T) {
	original := NewPerRepoConfig([]string{"fullsend", "triage", "coder", "review", "fix"}, "")
	data, err := original.Marshal()
	require.NoError(t, err)

	headerEnd := strings.Index(string(data), "version:")
	require.True(t, headerEnd > 0)

	parsed, err := ParsePerRepoConfig(data[headerEnd:])
	require.NoError(t, err)
	assert.Equal(t, original.Version, parsed.Version)
	assert.Equal(t, original.Roles, parsed.Roles)
	assert.Equal(t, original.KillSwitch, parsed.KillSwitch)
}

// --- AllowedRemoteResources tests ---

func TestOrgConfig_AllowedRemoteResources(t *testing.T) {
	t.Run("parse YAML with allowed_remote_resources", func(t *testing.T) {
		yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents: []
repos: {}
allowed_remote_resources:
  - https://example.com/skills/
  - https://cdn.example.com/policies/
`
		cfg, err := ParseOrgConfig([]byte(yamlData))
		require.NoError(t, err)
		assert.Equal(t, []string{"https://example.com/skills/", "https://cdn.example.com/policies/"}, cfg.AllowedRemoteResources)
	})

	t.Run("parse YAML without allowed_remote_resources", func(t *testing.T) {
		yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents: []
repos: {}
`
		cfg, err := ParseOrgConfig([]byte(yamlData))
		require.NoError(t, err)
		assert.Empty(t, cfg.AllowedRemoteResources)
	})

	t.Run("marshal with field", func(t *testing.T) {
		cfg := &OrgConfig{
			Version:  "1",
			Dispatch: DispatchConfig{Platform: "github-actions"},
			Defaults: RepoDefaults{
				Roles:                    []string{"fullsend"},
				MaxImplementationRetries: 2,
			},
			Repos:                  map[string]RepoConfig{},
			AllowedRemoteResources: []string{"https://example.com/skills/"},
		}
		data, err := cfg.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(data), "allowed_remote_resources:")
		assert.Contains(t, string(data), "https://example.com/skills/")
	})

	t.Run("marshal without field omits key", func(t *testing.T) {
		cfg := &OrgConfig{
			Version:  "1",
			Dispatch: DispatchConfig{Platform: "github-actions"},
			Defaults: RepoDefaults{
				Roles:                    []string{"fullsend"},
				MaxImplementationRetries: 2,
			},
			Repos: map[string]RepoConfig{},
		}
		data, err := cfg.Marshal()
		require.NoError(t, err)
		assert.NotContains(t, string(data), "allowed_remote_resources")
	})
}

// --- StatusNotifications tests ---

func TestParseOrgConfig_WithStatusNotifications(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
  status_notifications:
    comment:
      start: enabled
      completion: disabled
agents: []
repos: {}
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, cfg.Defaults.StatusNotifications)
	assert.Equal(t, "enabled", cfg.Defaults.StatusNotifications.Comment.Start)
	assert.Equal(t, "disabled", cfg.Defaults.StatusNotifications.Comment.Completion)
}

func TestParseOrgConfig_WithoutStatusNotifications(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents: []
repos: {}
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.Nil(t, cfg.Defaults.StatusNotifications)
}

func TestOrgConfigValidate_ValidStatusNotifications(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
			StatusNotifications: &StatusNotificationConfig{
				Comment: CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
			},
		},
	}
	assert.NoError(t, cfg.Validate())
}

func TestOrgConfigValidate_InvalidCommentStart(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
			StatusNotifications: &StatusNotificationConfig{
				Comment: CommentNotificationConfig{Start: "bogus"},
			},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status_notifications.comment.start")
}

func TestOrgConfigValidate_InvalidCommentCompletion(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
			StatusNotifications: &StatusNotificationConfig{
				Comment: CommentNotificationConfig{Completion: "bogus"},
			},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status_notifications.comment.completion")
}

func TestOrgConfigMarshal_WithStatusNotifications(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
			StatusNotifications: &StatusNotificationConfig{
				Comment: CommentNotificationConfig{Start: "enabled"},
			},
		},
		Repos: map[string]RepoConfig{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "status_notifications:")
	assert.Contains(t, string(data), "start: enabled")
}

func TestOrgConfigMarshal_WithoutStatusNotifications(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos: map[string]RepoConfig{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "status_notifications")
}

// --- CreateIssues tests ---

func TestOrgConfig_CreateIssues_ParseYAML(t *testing.T) {
	yamlData := `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
agents: []
repos: {}
create_issues:
  allow_targets:
    orgs:
      - my-org
      - other-org
    repos:
      - external-org/some-repo
`
	cfg, err := ParseOrgConfig([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, cfg.CreateIssues)
	assert.Equal(t, []string{"my-org", "other-org"}, cfg.CreateIssues.AllowTargets.Orgs)
	assert.Equal(t, []string{"external-org/some-repo"}, cfg.CreateIssues.AllowTargets.Repos)
}

func TestOrgConfig_CreateIssues_OmittedWhenEmpty(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos: map[string]RepoConfig{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "create_issues")
}

func TestOrgConfig_CreateIssues_Marshal(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		Repos: map[string]RepoConfig{},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Orgs:  []string{"my-org"},
				Repos: []string{"other/repo"},
			},
		},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "create_issues:")
	assert.Contains(t, string(data), "allow_targets:")
	assert.Contains(t, string(data), "my-org")
	assert.Contains(t, string(data), "other/repo")
}

func TestOrgConfigValidate_CreateIssues_InvalidRepoFormat(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Repos: []string{"no-slash-here"},
			},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no-slash-here")
}

func TestOrgConfigValidate_CreateIssues_MalformedRepoFormat(t *testing.T) {
	malformed := []string{"/", "/repo", "owner/", "//"}
	for _, repo := range malformed {
		cfg := &OrgConfig{
			Version:  "1",
			Dispatch: DispatchConfig{Platform: "github-actions"},
			Defaults: RepoDefaults{
				Roles:                    []string{"fullsend"},
				MaxImplementationRetries: 2,
			},
			CreateIssues: &CreateIssuesConfig{
				AllowTargets: AllowTargets{
					Repos: []string{repo},
				},
			},
		}
		err := cfg.Validate()
		assert.Error(t, err, "expected error for repo %q", repo)
		assert.Contains(t, err.Error(), "owner/name", "expected owner/name message for repo %q", repo)
	}
}

func TestOrgConfigValidate_CreateIssues_EmptyOrg(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Orgs: []string{"valid-org", ""},
			},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty org")
}

func TestOrgConfigValidate_CreateIssues_Valid(t *testing.T) {
	cfg := &OrgConfig{
		Version:  "1",
		Dispatch: DispatchConfig{Platform: "github-actions"},
		Defaults: RepoDefaults{
			Roles:                    []string{"fullsend"},
			MaxImplementationRetries: 2,
		},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Orgs:  []string{"my-org"},
				Repos: []string{"other/repo"},
			},
		},
	}
	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestOrgConfigValidate_CreateIssues_Nil(t *testing.T) {
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

func TestNewOrgConfig_CreateIssuesDefaults(t *testing.T) {
	cfg := NewOrgConfig(nil, nil, []string{"fullsend"}, "", "my-org")
	require.NotNil(t, cfg.CreateIssues)
	assert.Equal(t, []string{"my-org"}, cfg.CreateIssues.AllowTargets.Orgs)
	assert.Equal(t, []string{"fullsend-ai/fullsend"}, cfg.CreateIssues.AllowTargets.Repos)
}

func TestPerRepoConfig_CreateIssues_ParseYAML(t *testing.T) {
	yamlData := `
version: "1"
roles:
  - fullsend
  - triage
create_issues:
  allow_targets:
    repos:
      - my-org/my-repo
      - fullsend-ai/fullsend
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, cfg.CreateIssues)
	assert.Equal(t, []string{"my-org/my-repo", "fullsend-ai/fullsend"}, cfg.CreateIssues.AllowTargets.Repos)
}

func TestNewPerRepoConfig_CreateIssuesDefaults(t *testing.T) {
	cfg := NewPerRepoConfig(nil, "my-org/my-repo")
	require.NotNil(t, cfg.CreateIssues)
	assert.Equal(t, []string{"my-org/my-repo", "fullsend-ai/fullsend"}, cfg.CreateIssues.AllowTargets.Repos)
}
