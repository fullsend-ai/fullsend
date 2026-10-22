package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- PerRepoConfig getter tests ---

func TestPerRepoConfig_AgentEntries(t *testing.T) {
	agents := []AgentEntry{{Source: "harness/triage.yaml"}}
	cfg := &perRepoConfig{Agents: agents}
	assert.Equal(t, agents, cfg.AgentEntries())
}

func TestPerRepoConfig_AgentEntries_RefSurvivesLayeredMerge(t *testing.T) {
	// Parent defines an agent without a Ref (pre-existing entry).
	parent := &perRepoConfig{
		Agents: []AgentEntry{{
			Source: "https://raw.githubusercontent.com/org/repo/abc123/harness/triage.yaml#sha256=aaa",
			Name:   "triage",
		}},
	}
	// Overlay re-declares the same agent with a Ref from adoption.
	overlay := &perRepoConfig{
		parent: parent,
		Agents: []AgentEntry{{
			Name: "triage",
			Ref:  "release-1.0",
		}},
	}
	merged := overlay.AgentEntries()
	require.Len(t, merged, 1)
	assert.Equal(t, "release-1.0", merged[0].Ref, "Ref from overlay must survive layered merge")
	// Source should still come from parent since overlay didn't set it.
	assert.Contains(t, merged[0].Source, "raw.githubusercontent.com")
}

func TestPerRepoConfig_IsKillSwitchActive(t *testing.T) {
	tr := true
	cfg := &perRepoConfig{KillSwitch: &tr}
	assert.True(t, cfg.IsKillSwitchActive())
	f := false
	cfg.KillSwitch = &f
	assert.False(t, cfg.IsKillSwitchActive())
}

func TestPerRepoConfig_ConfigKeepHistory(t *testing.T) {
	f := false
	cfg := &perRepoConfig{KeepHistory: &f}
	assert.False(t, cfg.ConfigKeepHistory())
	tr := true
	cfg.KeepHistory = &tr
	assert.True(t, cfg.ConfigKeepHistory())
}

func TestPerRepoConfig_AllowedResources(t *testing.T) {
	resources := []string{"https://example.com/"}
	cfg := &perRepoConfig{AllowedRemoteResources: resources}
	assert.Equal(t, resources, cfg.AllowedResources())
}

func TestPerRepoConfig_IssueCreationConfig(t *testing.T) {
	ci := &CreateIssuesConfig{AllowTargets: AllowTargets{Repos: []string{"org/repo"}}}
	cfg := &perRepoConfig{CreateIssues: ci}
	assert.Equal(t, ci, cfg.IssueCreationConfig())
}

func TestPerRepoConfig_IssueCreationConfig_Nil(t *testing.T) {
	cfg := &perRepoConfig{}
	assert.Nil(t, cfg.IssueCreationConfig())
}

func TestPerRepoConfig_ConfigVersion(t *testing.T) {
	cfg := &perRepoConfig{Version: "1"}
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestPerRepoConfig_ConfigRoles(t *testing.T) {
	roles := []string{"triage", "coder"}
	cfg := &perRepoConfig{Roles: roles}
	assert.Equal(t, roles, cfg.ConfigRoles())
}

func TestPerRepoConfig_ConfigRuntime(t *testing.T) {
	cfg := &perRepoConfig{Runtime: "claude"}
	assert.Equal(t, "claude", cfg.ConfigRuntime())
}

func TestPerRepoConfig_StatusNotifications(t *testing.T) {
	t.Run("returns local value when set", func(t *testing.T) {
		sn := &StatusNotificationConfig{Comment: CommentNotificationConfig{Start: "enabled"}}
		cfg := &perRepoConfig{Notifications: sn}
		assert.Equal(t, sn, cfg.StatusNotifications())
	})

	t.Run("falls through to parent", func(t *testing.T) {
		sn := &StatusNotificationConfig{Comment: CommentNotificationConfig{Start: "enabled"}}
		parent := &perRepoConfig{Notifications: sn}
		child := &perRepoConfig{parent: parent}
		assert.Equal(t, sn, child.StatusNotifications())
	})

	t.Run("returns nil when unset", func(t *testing.T) {
		cfg := &perRepoConfig{}
		assert.Nil(t, cfg.StatusNotifications())
	})
}

// --- PerRepoConfig setter tests ---

func TestPerRepoConfig_SetKillSwitch(t *testing.T) {
	cfg := &perRepoConfig{}
	cfg.SetKillSwitch(true)
	require.NotNil(t, cfg.KillSwitch)
	assert.True(t, *cfg.KillSwitch)
	cfg.SetKillSwitch(false)
	require.NotNil(t, cfg.KillSwitch)
	assert.False(t, *cfg.KillSwitch)
}

func TestPerRepoConfig_SetKeepHistory(t *testing.T) {
	cfg := &perRepoConfig{}
	cfg.SetKeepHistory(false)
	require.NotNil(t, cfg.KeepHistory)
	assert.False(t, *cfg.KeepHistory)
	cfg.SetKeepHistory(true)
	require.NotNil(t, cfg.KeepHistory)
	assert.True(t, *cfg.KeepHistory)
}

func TestPerRepoConfig_SetAgents(t *testing.T) {
	cfg := &perRepoConfig{}
	agents := []AgentEntry{{Source: "harness/code.yaml"}}
	cfg.SetAgents(agents)
	assert.Equal(t, agents, cfg.Agents)
}

func TestPerRepoConfig_SetAllowedRemoteResources(t *testing.T) {
	cfg := &perRepoConfig{}
	resources := []string{"https://example.com/"}
	cfg.SetAllowedRemoteResources(resources)
	assert.Equal(t, resources, cfg.AllowedRemoteResources)
}

// --- LoadConfig factory tests ---

func TestLoadConfig_PerOrgConfig_Rejected(t *testing.T) {
	dir := t.TempDir()
	data := []byte("version: \"1\"\ndispatch:\n  platform: github-actions\ndefaults:\n  roles: [triage]\nrepos: {}\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	_, err := LoadConfig(dir, LoadOpts{})
	require.ErrorIs(t, err, errPerOrgConfig)
	assert.Contains(t, err.Error(), "per-org configuration format")
}

func TestLoadConfig_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := NewPerRepoConfig(nil, "o/r")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	reader, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	_, ok := reader.(PerRepoConfigReader)
	assert.True(t, ok)
}

func TestLoadConfig_MissingOK(t *testing.T) {
	dir := t.TempDir()
	reader, err := LoadConfig(dir, LoadOpts{MissingOK: true})
	require.NoError(t, err)
	_, ok := reader.(PerRepoConfigReader)
	assert.True(t, ok)
}

func TestLoadConfig_MissingNotOK(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfig(dir, LoadOpts{})
	require.Error(t, err)
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("not: valid: yaml: ["), 0o644))
	_, err := LoadConfig(dir, LoadOpts{})
	require.Error(t, err)
}

// --- LoadConfigWriter factory tests ---

func TestLoadConfigWriter_PerOrgConfig_Rejected(t *testing.T) {
	dir := t.TempDir()
	data := []byte("version: \"1\"\ndispatch:\n  platform: github-actions\ndefaults:\n  roles: [triage]\nrepos: {}\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	_, err := LoadConfigWriter(dir, LoadOpts{})
	require.ErrorIs(t, err, errPerOrgConfig)
}

func TestLoadConfigWriter_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := NewPerRepoConfig(nil, "o/r")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)
	_, ok := writer.(PerRepoConfigWriter)
	assert.True(t, ok)
}

func TestLoadConfigWriter_MissingOK(t *testing.T) {
	dir := t.TempDir()
	writer, err := LoadConfigWriter(dir, LoadOpts{MissingOK: true})
	require.NoError(t, err)
	_, ok := writer.(PerRepoConfigWriter)
	assert.True(t, ok)
}

func TestLoadConfigWriter_MissingNotOK(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfigWriter(dir, LoadOpts{})
	require.Error(t, err)
}

func TestLoadConfigWriter_Mutate(t *testing.T) {
	dir := t.TempDir()
	cfg := NewPerRepoConfig(nil, "o/r")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)

	writer.SetAgents([]AgentEntry{{Source: "harness/test.yaml"}})
	assert.Len(t, writer.AgentEntries(), 1)

	out, err := writer.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(out), "harness/test.yaml")
}

// --- Interface satisfaction tests ---

func TestPerRepoConfig_SatisfiesConfigReader(t *testing.T) {
	var _ ConfigReader = (*perRepoConfig)(nil)
}

func TestPerRepoConfig_SatisfiesPerRepoConfigReader(t *testing.T) {
	var _ PerRepoConfigReader = (*perRepoConfig)(nil)
}

func TestPerRepoConfig_SatisfiesConfigWriter(t *testing.T) {
	var _ ConfigWriter = (*perRepoConfig)(nil)
}

// --- ConfigWriter integration tests ---

func TestPerRepoConfig_ConfigWriter_RoundTrip(t *testing.T) {
	var w ConfigWriter = NewPerRepoConfig(nil, "o/r")
	w.SetKillSwitch(true)
	assert.True(t, w.IsKillSwitchActive())

	agents := []AgentEntry{{Source: "harness/code.yaml"}}
	w.SetAgents(agents)
	assert.Equal(t, agents, w.AgentEntries())

	data, err := w.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "kill_switch: true")
}

func TestPerRepoConfig_ConfigForge(t *testing.T) {
	t.Run("returns forge when set", func(t *testing.T) {
		cfg := &perRepoConfig{Forge: "gitlab"}
		assert.Equal(t, "gitlab", cfg.ConfigForge())
	})

	t.Run("falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{Forge: "github"}
		child := &perRepoConfig{parent: parent}
		assert.Equal(t, "github", child.ConfigForge())
	})

	t.Run("returns empty when unset", func(t *testing.T) {
		cfg := &perRepoConfig{}
		assert.Equal(t, "", cfg.ConfigForge())
	})
}

func TestPerRepoConfig_ConfigTracker(t *testing.T) {
	t.Run("returns tracker when set", func(t *testing.T) {
		cfg := &perRepoConfig{Tracker: "jira"}
		assert.Equal(t, "jira", cfg.ConfigTracker())
	})

	t.Run("falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{Tracker: "gitlab"}
		child := &perRepoConfig{parent: parent}
		assert.Equal(t, "gitlab", child.ConfigTracker())
	})

	t.Run("returns empty when unset", func(t *testing.T) {
		cfg := &perRepoConfig{}
		assert.Equal(t, "", cfg.ConfigTracker())
	})

	t.Run("independent of forge", func(t *testing.T) {
		// A repo hosted on GitHub can still track issues in Jira.
		cfg := &perRepoConfig{Forge: "github", Tracker: "jira"}
		assert.Equal(t, "github", cfg.ConfigForge())
		assert.Equal(t, "jira", cfg.ConfigTracker())
	})
}

// --- MintURL fallback ---

func TestPerRepoConfig_MintURL_Fallback(t *testing.T) {
	t.Run("empty falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{
			MintURL: "https://mint.example.com",
			parent:  &perRepoDefaults{},
		}
		overlay := &perRepoConfig{parent: parent}
		assert.Equal(t, "https://mint.example.com", overlay.ConfigMintURL())
	})

	t.Run("local overrides parent", func(t *testing.T) {
		parent := &perRepoConfig{
			MintURL: "https://base-mint.example.com",
			parent:  &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			MintURL: "https://overlay-mint.example.com",
			parent:  parent,
		}
		assert.Equal(t, "https://overlay-mint.example.com", overlay.ConfigMintURL())
	})

	t.Run("falls through to defaults when unset", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, DefaultPerRepoMintURL, cfg.ConfigMintURL())
	})
}

// --- Inference provider fallback ---

func TestPerRepoConfig_InferenceProvider_Fallback(t *testing.T) {
	t.Run("empty falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Provider: "vertex"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{parent: parent}
		assert.Equal(t, "vertex", overlay.ConfigInferenceProvider())
	})

	t.Run("local overrides parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Provider: "vertex"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Provider: "vertex"},
			parent:    parent,
		}
		assert.Equal(t, "vertex", overlay.ConfigInferenceProvider())
	})

	t.Run("falls through to defaults when unset", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, DefaultPerRepoInferenceProvider, cfg.ConfigInferenceProvider())
	})
}

// --- InferenceProject fallback ---

func TestPerRepoConfig_InferenceProject_Fallback(t *testing.T) {
	t.Run("empty falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Project: "my-project"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{parent: parent}
		assert.Equal(t, "my-project", overlay.ConfigInferenceProject())
	})

	t.Run("local overrides parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Project: "base-project"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Project: "overlay-project"},
			parent:    parent,
		}
		assert.Equal(t, "overlay-project", overlay.ConfigInferenceProject())
	})

	t.Run("falls through to defaults when unset", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, "", cfg.ConfigInferenceProject())
	})
}

// --- InferenceRegion fallback ---

func TestPerRepoConfig_InferenceRegion_Fallback(t *testing.T) {
	t.Run("empty falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Region: "us-central1"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{parent: parent}
		assert.Equal(t, "us-central1", overlay.ConfigInferenceRegion())
	})

	t.Run("local overrides parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Region: "us-central1"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Region: "europe-west1"},
			parent:    parent,
		}
		assert.Equal(t, "europe-west1", overlay.ConfigInferenceRegion())
	})

	t.Run("falls through to defaults when unset", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, DefaultPerRepoInferenceRegion, cfg.ConfigInferenceRegion())
	})
}

// --- InferenceWIFProvider fallback ---

func TestPerRepoConfig_InferenceWIFProvider_Fallback(t *testing.T) {
	t.Run("empty falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{WIFProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/prov"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{parent: parent}
		assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/prov", overlay.ConfigInferenceWIFProvider())
	})

	t.Run("local overrides parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{WIFProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/prov-base"},
			parent:    &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{WIFProvider: "projects/456/locations/global/workloadIdentityPools/pool/providers/prov-overlay"},
			parent:    parent,
		}
		assert.Equal(t, "projects/456/locations/global/workloadIdentityPools/pool/providers/prov-overlay", overlay.ConfigInferenceWIFProvider())
	})

	t.Run("falls through to defaults when unset", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, "", cfg.ConfigInferenceWIFProvider())
	})
}

// --- Chained fallback with mint/inference ---

func TestPerRepoConfig_MintInference_ChainedFallback(t *testing.T) {
	// defaults -> base -> overlay
	// base sets mint/inference; overlay sets only mint_url override.
	base := &perRepoConfig{
		MintURL: "https://base-mint.example.com",
		Inference: &PerRepoInferenceConfig{
			Provider:    "vertex",
			Project:     "base-project",
			Region:      "us-central1",
			WIFProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/prov",
		},
		parent: &perRepoDefaults{},
	}
	overlay := &perRepoConfig{
		MintURL: "https://overlay-mint.example.com",
		parent:  base,
	}

	// overlay overrides mint_url.
	assert.Equal(t, "https://overlay-mint.example.com", overlay.ConfigMintURL())
	// inference fields fall through overlay (nil) -> base.
	assert.Equal(t, "vertex", overlay.ConfigInferenceProvider())
	assert.Equal(t, "base-project", overlay.ConfigInferenceProject())
	assert.Equal(t, "us-central1", overlay.ConfigInferenceRegion())
	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/prov", overlay.ConfigInferenceWIFProvider())
}

// --- Setter tests for mint/inference ---

func TestPerRepoConfig_SetMintURL(t *testing.T) {
	cfg := &perRepoConfig{parent: &perRepoDefaults{}}
	cfg.SetMintURL("https://mint.example.com")
	assert.Equal(t, "https://mint.example.com", cfg.ConfigMintURL())
}

func TestPerRepoConfig_SetInferenceProvider_Setter(t *testing.T) {
	cfg := &perRepoConfig{parent: &perRepoDefaults{}}
	cfg.SetInferenceProvider("vertex")
	assert.Equal(t, "vertex", cfg.ConfigInferenceProvider())
}

func TestPerRepoConfig_SetInferenceProject(t *testing.T) {
	cfg := &perRepoConfig{parent: &perRepoDefaults{}}
	cfg.SetInferenceProject("my-project")
	assert.Equal(t, "my-project", cfg.ConfigInferenceProject())
}

func TestPerRepoConfig_SetInferenceRegion(t *testing.T) {
	cfg := &perRepoConfig{parent: &perRepoDefaults{}}
	cfg.SetInferenceRegion("us-central1")
	assert.Equal(t, "us-central1", cfg.ConfigInferenceRegion())
}

func TestPerRepoConfig_SetInferenceWIFProvider(t *testing.T) {
	cfg := &perRepoConfig{parent: &perRepoDefaults{}}
	cfg.SetInferenceWIFProvider("projects/123/locations/global/workloadIdentityPools/pool/providers/prov")
	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/prov", cfg.ConfigInferenceWIFProvider())
}

// --- Marshal emits mint/inference fields only when set ---

func TestPerRepoConfig_MarshalMintInference(t *testing.T) {
	t.Run("omits unset fields", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		data, err := cfg.Marshal()
		require.NoError(t, err)
		s := string(data)
		assert.NotContains(t, s, "mint_url:")
		assert.NotContains(t, s, "inference:")
	})

	t.Run("emits locally set fields", func(t *testing.T) {
		cfg := &perRepoConfig{
			MintURL: "https://mint.example.com",
			Inference: &PerRepoInferenceConfig{
				Provider:    "vertex",
				Project:     "my-project",
				Region:      "us-central1",
				WIFProvider: "projects/123/pool/prov",
			},
			parent: &perRepoDefaults{},
		}
		data, err := cfg.Marshal()
		require.NoError(t, err)
		s := string(data)
		assert.Contains(t, s, "mint_url: https://mint.example.com")
		assert.Contains(t, s, "inference:")
		assert.Contains(t, s, "provider: vertex")
		assert.Contains(t, s, "project: my-project")
		assert.Contains(t, s, "region: us-central1")
		assert.Contains(t, s, "wif_provider: projects/123/pool/prov")
	})
}

// --- YAML round-trip for mint/inference ---

func TestPerRepoConfig_MintInference_YAMLRoundTrip(t *testing.T) {
	yamlData := `version: "1"
mint_url: https://mint.example.com
inference:
  provider: vertex
  project: my-project
  region: us-central1
  wif_provider: projects/123/pool/prov
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)

	assert.Equal(t, "https://mint.example.com", cfg.ConfigMintURL())
	assert.Equal(t, "vertex", cfg.ConfigInferenceProvider())
	assert.Equal(t, "my-project", cfg.ConfigInferenceProject())
	assert.Equal(t, "us-central1", cfg.ConfigInferenceRegion())
	assert.Equal(t, "projects/123/pool/prov", cfg.ConfigInferenceWIFProvider())
}

// --- Validate inference provider ---

func TestPerRepoConfig_Validate_InferenceProvider(t *testing.T) {
	t.Run("valid provider passes", func(t *testing.T) {
		cfg := &perRepoConfig{
			Version:   "1",
			Roles:     []string{"triage"},
			Inference: &PerRepoInferenceConfig{Provider: "vertex"},
			parent:    &perRepoDefaults{},
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("empty provider passes (inherits from parent)", func(t *testing.T) {
		cfg := &perRepoConfig{
			Version: "1",
			Roles:   []string{"triage"},
			parent:  &perRepoDefaults{},
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("invalid provider fails", func(t *testing.T) {
		cfg := &perRepoConfig{
			Version:   "1",
			Roles:     []string{"triage"},
			Inference: &PerRepoInferenceConfig{Provider: "openai"},
			parent:    &perRepoDefaults{},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid inference provider")
	})
}

// --- NewEmptyPerRepoOverlay ---

func TestNewEmptyPerRepoOverlay(t *testing.T) {
	o := NewEmptyPerRepoOverlay()
	// Should resolve defaults through parent.
	assert.Equal(t, "1", o.ConfigVersion())
	assert.Equal(t, "claude", o.ConfigRuntime())
	// Mint/inference resolve through parent to code defaults.
	assert.Equal(t, DefaultPerRepoMintURL, o.ConfigMintURL())
	assert.Equal(t, DefaultPerRepoInferenceProvider, o.ConfigInferenceProvider())
	assert.Equal(t, DefaultPerRepoInferenceRegion, o.ConfigInferenceRegion())

	// Marshal should be minimal — code defaults must NOT leak into
	// the serialized overlay.
	data, err := o.Marshal()
	require.NoError(t, err)
	s := string(data)
	assert.NotContains(t, s, "version:")
	assert.NotContains(t, s, "runtime:")
	assert.NotContains(t, s, "mint_url:")
	assert.NotContains(t, s, "inference:")
}

// --- IsPerRepoYAML with inference block ---

func TestIsPerRepoYAML_WithInferenceBlock(t *testing.T) {
	// Nested inference block must NOT trigger org-mode detection.
	yamlData := `version: "1"
mint_url: https://mint.example.com
inference:
  provider: vertex
  project: my-project
  region: us-central1
`
	assert.True(t, IsPerRepoYAML([]byte(yamlData)))
}

// --- InferenceOpenAI fallback ---

func TestPerRepoConfig_InferenceOpenAI_Fallback(t *testing.T) {
	base := &perRepoConfig{
		Inference: &PerRepoInferenceConfig{OpenAI: &OpenAIWIFConfig{
			Audience:           "fullsend://acme",
			IdentityProviderID: "idp_base",
			ServiceAccountID:   "sa_base",
		}},
		parent: &perRepoDefaults{},
	}
	t.Run("empty falls through to parent", func(t *testing.T) {
		overlay := &perRepoConfig{parent: base}
		assert.Equal(t, OpenAIWIFConfig{Audience: "fullsend://acme", IdentityProviderID: "idp_base", ServiceAccountID: "sa_base"}, overlay.ConfigInferenceOpenAI())
	})
	t.Run("each identifier overrides independently", func(t *testing.T) {
		overlay := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{OpenAI: &OpenAIWIFConfig{ServiceAccountID: "sa_overlay"}},
			parent:    base,
		}
		assert.Equal(t, OpenAIWIFConfig{Audience: "fullsend://acme", IdentityProviderID: "idp_base", ServiceAccountID: "sa_overlay"}, overlay.ConfigInferenceOpenAI())
	})
	t.Run("falls through to defaults when unset", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.True(t, cfg.ConfigInferenceOpenAI().IsZero())
	})
	t.Run("setter round-trips through YAML and a zero value removes the block", func(t *testing.T) {
		w := NewEmptyPerRepoOverlay()
		w.SetInferenceOpenAI(OpenAIWIFConfig{Audience: "fullsend://acme", IdentityProviderID: "idp_1", ServiceAccountID: "sa_1"})
		out, err := w.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(out), "openai:\n        audience: fullsend://acme\n        identity_provider_id: idp_1\n        service_account_id: sa_1")
		w.SetInferenceOpenAI(OpenAIWIFConfig{})
		out, err = w.Marshal()
		require.NoError(t, err)
		assert.NotContains(t, string(out), "openai:")
	})
	assert.Equal(t, []string{"identity_provider_id", "service_account_id"}, OpenAIWIFConfig{Audience: "a"}.Missing())
}

// --- IsOwnersFileAuthEnabled: intentionally no parent fallback ---

func TestPerRepoConfig_IsOwnersFileAuthEnabled_NoFallback(t *testing.T) {
	t.Run("returns false when unset", func(t *testing.T) {
		cfg := &perRepoConfig{}
		assert.False(t, cfg.IsOwnersFileAuthEnabled())
	})

	t.Run("does not fall through to parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Authorization: []AuthorizationProvider{{Provider: "owners_file"}},
		}
		child := &perRepoConfig{parent: parent}
		assert.False(t, child.IsOwnersFileAuthEnabled())
	})

	t.Run("returns true when set locally", func(t *testing.T) {
		cfg := &perRepoConfig{
			Authorization: []AuthorizationProvider{{Provider: "owners_file"}},
		}
		assert.True(t, cfg.IsOwnersFileAuthEnabled())
	})
}

func TestPerRepoConfig_SetOwnersFileAuthEnabled(t *testing.T) {
	t.Run("enable adds provider", func(t *testing.T) {
		cfg := &perRepoConfig{}
		cfg.SetOwnersFileAuthEnabled(true)
		assert.True(t, cfg.IsOwnersFileAuthEnabled())
		assert.Len(t, cfg.Authorization, 1)
	})

	t.Run("enable is idempotent", func(t *testing.T) {
		cfg := &perRepoConfig{}
		cfg.SetOwnersFileAuthEnabled(true)
		cfg.SetOwnersFileAuthEnabled(true)
		assert.Len(t, cfg.Authorization, 1)
	})

	t.Run("disable removes provider", func(t *testing.T) {
		cfg := &perRepoConfig{
			Authorization: []AuthorizationProvider{{Provider: "owners_file"}},
		}
		cfg.SetOwnersFileAuthEnabled(false)
		assert.False(t, cfg.IsOwnersFileAuthEnabled())
		assert.Nil(t, cfg.Authorization)
	})

	t.Run("disable preserves other providers", func(t *testing.T) {
		cfg := &perRepoConfig{
			Authorization: []AuthorizationProvider{
				{Provider: "owners_file"},
				{Provider: "other"},
			},
		}
		cfg.SetOwnersFileAuthEnabled(false)
		assert.False(t, cfg.IsOwnersFileAuthEnabled())
		assert.Equal(t, []AuthorizationProvider{{Provider: "other"}}, cfg.Authorization)
	})

	t.Run("disable is no-op when not set", func(t *testing.T) {
		cfg := &perRepoConfig{}
		cfg.SetOwnersFileAuthEnabled(false)
		assert.Nil(t, cfg.Authorization)
	})
}

func TestPerRepoDefaults_AuthorizationOwnersFile(t *testing.T) {
	d := &perRepoDefaults{}
	assert.False(t, d.IsOwnersFileAuthEnabled())
}
