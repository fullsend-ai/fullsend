package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- perRepoDefaults interface satisfaction ---

func TestPerRepoDefaults_SatisfiesPerRepoConfigReader(t *testing.T) {
	var _ PerRepoConfigReader = (*perRepoDefaults)(nil)
}

// --- perRepoDefaults returns compiled-in code defaults ---

func TestPerRepoDefaults_CodeDefaults(t *testing.T) {
	d := &perRepoDefaults{}

	assert.Equal(t, "1", d.ConfigVersion())
	assert.Equal(t, "claude", d.ConfigRuntime())
	assert.False(t, d.IsKillSwitchActive())
	assert.Equal(t, PerRepoDefaultRoles(), d.ConfigRoles())
	assert.Nil(t, d.AgentEntries())
	assert.Equal(t, DefaultAllowedRemoteResources(), d.AllowedResources())
	assert.Nil(t, d.IssueCreationConfig())
	assert.False(t, d.IsOrgMode())
	assert.Equal(t, "", d.ConfigForge())

	// Mint/inference operational defaults.
	assert.Equal(t, DefaultPerRepoMintURL, d.ConfigMintURL())
	assert.Equal(t, DefaultPerRepoInferenceProvider, d.ConfigInferenceProvider())
	assert.Equal(t, "", d.ConfigInferenceProject(), "project has no code default")
	assert.Equal(t, DefaultPerRepoInferenceRegion, d.ConfigInferenceRegion())
	assert.Equal(t, "", d.ConfigInferenceWIFProvider(), "WIF provider has no code default")
}

// --- Unset fields resolve through parent to code defaults ---

func TestPerRepoConfig_EmptyConfigResolvesDefaults(t *testing.T) {
	cfg := &perRepoConfig{parent: &perRepoDefaults{}}

	assert.Equal(t, "1", cfg.ConfigVersion())
	assert.Equal(t, "claude", cfg.ConfigRuntime())
	assert.False(t, cfg.IsKillSwitchActive())
	assert.Equal(t, PerRepoDefaultRoles(), cfg.ConfigRoles())
	assert.Nil(t, cfg.AgentEntries())
	assert.Equal(t, DefaultAllowedRemoteResources(), cfg.AllowedResources())
	assert.Nil(t, cfg.IssueCreationConfig())
	assert.False(t, cfg.IsOrgMode())

	// Mint/inference fields fall through to code defaults.
	assert.Equal(t, DefaultPerRepoMintURL, cfg.ConfigMintURL())
	assert.Equal(t, DefaultPerRepoInferenceProvider, cfg.ConfigInferenceProvider())
	assert.Equal(t, "", cfg.ConfigInferenceProject(), "project has no code default")
	assert.Equal(t, DefaultPerRepoInferenceRegion, cfg.ConfigInferenceRegion())
	assert.Equal(t, "", cfg.ConfigInferenceWIFProvider(), "WIF provider has no code default")
}

// --- Local values override parent values ---

func TestPerRepoConfig_LocalOverridesParent(t *testing.T) {
	tr := true
	cfg := &perRepoConfig{
		Version:    "1",
		KillSwitch: &tr,
		Runtime:    "dummy",
		Roles:      []string{"triage", "review"},
		Agents: []AgentEntry{
			{Source: "harness/code.yaml"},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{Repos: []string{"org/repo"}},
		},
		parent: &perRepoDefaults{},
	}

	assert.Equal(t, "1", cfg.ConfigVersion())
	assert.Equal(t, "dummy", cfg.ConfigRuntime())
	assert.True(t, cfg.IsKillSwitchActive())
	assert.Equal(t, []string{"triage", "review"}, cfg.ConfigRoles())
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "harness/code.yaml", cfg.AgentEntries()[0].Source)
	require.NotNil(t, cfg.IssueCreationConfig())
	assert.Equal(t, []string{"org/repo"}, cfg.IssueCreationConfig().AllowTargets.Repos)
}

// --- Chained fallback: overlay -> base -> defaults ---

func TestPerRepoConfig_ChainedFallback(t *testing.T) {
	// defaults -> base -> overlay
	// base sets runtime="dummy" and roles; overlay sets only version.
	base := &perRepoConfig{
		Runtime: "dummy",
		Roles:   []string{"triage"},
		parent:  &perRepoDefaults{},
	}
	overlay := &perRepoConfig{
		Version: "1",
		parent:  base,
	}

	// overlay has version locally.
	assert.Equal(t, "1", overlay.ConfigVersion())
	// runtime falls through overlay (empty) -> base ("dummy").
	assert.Equal(t, "dummy", overlay.ConfigRuntime())
	// roles falls through overlay (nil) -> base.
	assert.Equal(t, []string{"triage"}, overlay.ConfigRoles())
	// kill_switch falls through overlay (nil) -> base (nil) -> defaults (false).
	assert.False(t, overlay.IsKillSwitchActive())
	// allowed_remote_resources: overlay nil -> base nil -> defaults.
	assert.Equal(t, DefaultAllowedRemoteResources(), overlay.AllowedResources())
}

// --- KillSwitch *bool pointer semantics ---

func TestPerRepoConfig_KillSwitch_PointerSemantics(t *testing.T) {
	t.Run("nil falls through to parent default false", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.False(t, cfg.IsKillSwitchActive())
	})

	t.Run("explicit false does not fall through", func(t *testing.T) {
		// Parent has kill_switch=true, overlay explicitly sets false.
		tr := true
		parentCfg := &perRepoConfig{
			KillSwitch: &tr,
			parent:     &perRepoDefaults{},
		}
		f := false
		overlay := &perRepoConfig{
			KillSwitch: &f,
			parent:     parentCfg,
		}
		assert.False(t, overlay.IsKillSwitchActive())
	})

	t.Run("explicit true overrides parent false", func(t *testing.T) {
		tr := true
		cfg := &perRepoConfig{
			KillSwitch: &tr,
			parent:     &perRepoDefaults{},
		}
		assert.True(t, cfg.IsKillSwitchActive())
	})
}

// --- Agents keyed merge by DerivedName ---

func TestPerRepoConfig_AgentsMerge(t *testing.T) {
	t.Run("nil overlay returns parent agents", func(t *testing.T) {
		parent := &perRepoConfig{
			Agents: []AgentEntry{
				{Source: "harness/triage.yaml"},
			},
			parent: &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			// Agents is nil (key omitted).
			parent: parent,
		}
		agents := overlay.AgentEntries()
		require.Len(t, agents, 1)
		assert.Equal(t, "harness/triage.yaml", agents[0].Source)
	})

	t.Run("empty overlay preserves parent agents", func(t *testing.T) {
		parent := &perRepoConfig{
			Agents: []AgentEntry{
				{Source: "harness/triage.yaml"},
				{Source: "harness/review.yaml"},
			},
			parent: &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			Agents: []AgentEntry{}, // explicit empty
			parent: parent,
		}
		agents := overlay.AgentEntries()
		require.Len(t, agents, 2)
		assert.Equal(t, "triage", agents[0].DerivedName())
		assert.Equal(t, "review", agents[1].DerivedName())
	})

	t.Run("overlay disables parent agent", func(t *testing.T) {
		parent := &perRepoConfig{
			Agents: []AgentEntry{
				{Source: "harness/triage.yaml"},
				{Source: "harness/review.yaml"},
			},
			parent: &perRepoDefaults{},
		}
		f := false
		overlay := &perRepoConfig{
			Agents: []AgentEntry{
				{Name: "triage", Enabled: &f},
			},
			parent: parent,
		}
		agents := overlay.AgentEntries()
		require.Len(t, agents, 2)
		// triage should be disabled.
		assert.Equal(t, "triage", agents[0].DerivedName())
		assert.False(t, agents[0].IsEnabled())
		// triage source preserved from parent.
		assert.Equal(t, "harness/triage.yaml", agents[0].Source)
		// review unchanged.
		assert.Equal(t, "review", agents[1].DerivedName())
		assert.True(t, agents[1].IsEnabled())
	})

	t.Run("overlay replaces parent agent source", func(t *testing.T) {
		parent := &perRepoConfig{
			Agents: []AgentEntry{
				{Source: "harness/triage.yaml"},
			},
			parent: &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			Agents: []AgentEntry{
				{Name: "triage", Source: "harness/custom-triage.yaml"},
			},
			parent: parent,
		}
		agents := overlay.AgentEntries()
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].DerivedName())
		assert.Equal(t, "harness/custom-triage.yaml", agents[0].Source)
		assert.True(t, agents[0].IsEnabled())
	})

	t.Run("overlay adds new agent not in parent", func(t *testing.T) {
		parent := &perRepoConfig{
			Agents: []AgentEntry{
				{Source: "harness/triage.yaml"},
			},
			parent: &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			Agents: []AgentEntry{
				{Source: "harness/lint.yaml"},
			},
			parent: parent,
		}
		agents := overlay.AgentEntries()
		require.Len(t, agents, 2)
		assert.Equal(t, "triage", agents[0].DerivedName())
		assert.Equal(t, "lint", agents[1].DerivedName())
	})

	t.Run("no parent returns local agents only", func(t *testing.T) {
		cfg := &perRepoConfig{
			Agents: []AgentEntry{
				{Source: "harness/code.yaml"},
			},
		}
		agents := cfg.AgentEntries()
		require.Len(t, agents, 1)
		assert.Equal(t, "code", agents[0].DerivedName())
	})
}

// --- AllowedResources union and deny-all ---

func TestPerRepoConfig_AllowedResources_Fallback(t *testing.T) {
	t.Run("nil falls through to parent defaults", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, DefaultAllowedRemoteResources(), cfg.AllowedResources())
	})

	t.Run("explicit empty is deny-all", func(t *testing.T) {
		cfg := &perRepoConfig{
			AllowedRemoteResources: []string{},
			parent:                 &perRepoDefaults{},
		}
		result := cfg.AllowedResources()
		assert.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("non-empty unions with parent", func(t *testing.T) {
		cfg := &perRepoConfig{
			AllowedRemoteResources: []string{"https://example.com/custom/"},
			parent:                 &perRepoDefaults{},
		}
		result := cfg.AllowedResources()
		assert.Contains(t, result, "https://example.com/custom/")
		// Defaults surface via the terminal perRepoDefaults parent.
		for _, d := range DefaultAllowedRemoteResources() {
			assert.Contains(t, result, d)
		}
	})

	t.Run("non-empty with overlap produces no duplicates", func(t *testing.T) {
		defaults := DefaultAllowedRemoteResources()
		cfg := &perRepoConfig{
			AllowedRemoteResources: []string{defaults[0], "https://example.com/"},
			parent:                 &perRepoDefaults{},
		}
		result := cfg.AllowedResources()
		// Count occurrences of defaults[0].
		count := 0
		for _, r := range result {
			if r == defaults[0] {
				count++
			}
		}
		assert.Equal(t, 1, count, "should not duplicate existing entries")
		assert.Contains(t, result, defaults[1], "missing default should be appended")
		assert.Contains(t, result, "https://example.com/")
	})

	t.Run("deny-all in parent with non-empty overlay", func(t *testing.T) {
		parent := &perRepoConfig{
			AllowedRemoteResources: []string{}, // deny-all
			parent:                 &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			AllowedRemoteResources: []string{"https://example.com/"},
			parent:                 parent,
		}
		result := overlay.AllowedResources()
		assert.Contains(t, result, "https://example.com/")
		// Parent returned deny-all (empty), so overlay union contains
		// only the overlay entries — code defaults are not re-injected.
		assert.Len(t, result, 1)
	})

	t.Run("custom parent without code defaults is honored", func(t *testing.T) {
		// An intermediate parent that returns a custom allowlist without
		// baked-in prefixes. The overlay getter honors the parent's
		// effective list without re-injecting code defaults.
		parent := &perRepoConfig{
			AllowedRemoteResources: []string{"https://custom.example.com/"},
		}
		overlay := &perRepoConfig{
			AllowedRemoteResources: []string{"https://overlay.example.com/"},
			parent:                 parent,
		}
		result := overlay.AllowedResources()
		assert.Contains(t, result, "https://overlay.example.com/")
		assert.Contains(t, result, "https://custom.example.com/")
		assert.Len(t, result, 2)
		// Code defaults are NOT present — the parent did not include
		// them, and the overlay getter honors that.
		for _, d := range DefaultAllowedRemoteResources() {
			assert.NotContains(t, result, d)
		}
	})
}

// --- CreateIssues fallback ---

func TestPerRepoConfig_CreateIssues_Fallback(t *testing.T) {
	t.Run("nil falls through to parent", func(t *testing.T) {
		parent := &perRepoConfig{
			CreateIssues: &CreateIssuesConfig{
				AllowTargets: AllowTargets{Orgs: []string{"my-org"}},
			},
			parent: &perRepoDefaults{},
		}
		overlay := &perRepoConfig{parent: parent}
		require.NotNil(t, overlay.IssueCreationConfig())
		assert.Equal(t, []string{"my-org"}, overlay.IssueCreationConfig().AllowTargets.Orgs)
	})

	t.Run("local replaces parent entirely", func(t *testing.T) {
		parent := &perRepoConfig{
			CreateIssues: &CreateIssuesConfig{
				AllowTargets: AllowTargets{Orgs: []string{"parent-org"}},
			},
			parent: &perRepoDefaults{},
		}
		overlay := &perRepoConfig{
			CreateIssues: &CreateIssuesConfig{
				AllowTargets: AllowTargets{Repos: []string{"org/repo"}},
			},
			parent: parent,
		}
		require.NotNil(t, overlay.IssueCreationConfig())
		assert.Empty(t, overlay.IssueCreationConfig().AllowTargets.Orgs)
		assert.Equal(t, []string{"org/repo"}, overlay.IssueCreationConfig().AllowTargets.Repos)
	})
}

// --- Roles fallback ---

func TestPerRepoConfig_Roles_Fallback(t *testing.T) {
	t.Run("nil falls through to parent", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, PerRepoDefaultRoles(), cfg.ConfigRoles())
	})

	t.Run("empty slice replaces parent (no roles)", func(t *testing.T) {
		cfg := &perRepoConfig{
			Roles:  []string{},
			parent: &perRepoDefaults{},
		}
		assert.NotNil(t, cfg.ConfigRoles())
		assert.Empty(t, cfg.ConfigRoles())
	})

	t.Run("non-empty replaces parent", func(t *testing.T) {
		cfg := &perRepoConfig{
			Roles:  []string{"triage"},
			parent: &perRepoDefaults{},
		}
		assert.Equal(t, []string{"triage"}, cfg.ConfigRoles())
	})
}

// --- Version fallback ---

func TestPerRepoConfig_Version_Fallback(t *testing.T) {
	t.Run("empty falls through to parent", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, "1", cfg.ConfigVersion())
	})

	t.Run("local overrides parent", func(t *testing.T) {
		cfg := &perRepoConfig{
			Version: "1",
			parent:  &perRepoDefaults{},
		}
		assert.Equal(t, "1", cfg.ConfigVersion())
	})
}

// --- Runtime fallback ---

func TestPerRepoConfig_Runtime_Fallback(t *testing.T) {
	t.Run("empty falls through to parent", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.Equal(t, "claude", cfg.ConfigRuntime())
	})

	t.Run("local overrides parent", func(t *testing.T) {
		cfg := &perRepoConfig{
			Runtime: "dummy",
			parent:  &perRepoDefaults{},
		}
		assert.Equal(t, "dummy", cfg.ConfigRuntime())
	})
}

// --- Marshal emits only locally-set fields ---

func TestPerRepoConfig_MarshalOmitsInheritedValues(t *testing.T) {
	// An empty config with parent should marshal to only the YAML
	// header and an empty YAML body — no inherited values leak.
	cfg := &perRepoConfig{parent: &perRepoDefaults{}}
	data, err := cfg.Marshal()
	require.NoError(t, err)

	output := string(data)
	// Header is always present.
	assert.Contains(t, output, "fullsend per-repo configuration")
	// No inherited values should appear.
	assert.NotContains(t, output, "version:")
	assert.NotContains(t, output, "runtime:")
	assert.NotContains(t, output, "kill_switch:")
	assert.NotContains(t, output, "roles:")
	assert.NotContains(t, output, "agents:")
	assert.NotContains(t, output, "allowed_remote_resources:")
	assert.NotContains(t, output, "create_issues:")
	// But the config still resolves defaults via parent.
	assert.Equal(t, "1", cfg.ConfigVersion())
	assert.Equal(t, "claude", cfg.ConfigRuntime())
}

func TestPerRepoConfig_MarshalEmitsLocalValues(t *testing.T) {
	tr := true
	cfg := &perRepoConfig{
		Version:    "1",
		KillSwitch: &tr,
		Runtime:    "dummy",
		Roles:      []string{"triage"},
		parent:     &perRepoDefaults{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)

	output := string(data)
	assert.Contains(t, output, "version:")
	assert.Contains(t, output, "kill_switch: true")
	assert.Contains(t, output, "runtime: dummy")
	assert.Contains(t, output, "- triage")
}

func TestPerRepoConfig_MarshalExplicitFalseKillSwitch(t *testing.T) {
	f := false
	cfg := &perRepoConfig{
		Version:    "1",
		KillSwitch: &f,
		parent:     &perRepoDefaults{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	// Explicit false should appear in output (distinguishable from unset).
	assert.Contains(t, string(data), "kill_switch: false")
}

func TestPerRepoConfig_MarshalDenyAll(t *testing.T) {
	cfg := &perRepoConfig{
		Version:                "1",
		AllowedRemoteResources: []string{}, // deny-all
		parent:                 &perRepoDefaults{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	// Deny-all (empty slice) must appear in output so it survives
	// a roundtrip. Without MarshalYAML the omitempty tag would drop it.
	assert.Contains(t, string(data), "allowed_remote_resources: []")
}

// --- Deny-all YAML roundtrip ---

func TestPerRepoConfig_DenyAll_YAMLRoundTrip(t *testing.T) {
	// A config with deny-all allowed_remote_resources must preserve
	// the deny-all semantics through marshal -> parse -> getter.
	cfg := &perRepoConfig{
		Version:                "1",
		Roles:                  []string{"triage"},
		AllowedRemoteResources: []string{}, // deny-all
		parent:                 &perRepoDefaults{},
	}

	data, err := cfg.Marshal()
	require.NoError(t, err)

	// The marshaled output must include the empty list.
	assert.Contains(t, string(data), "allowed_remote_resources: []")

	// Strip the header so ParsePerRepoConfig can parse the YAML body.
	headerEnd := strings.Index(string(data), "version:")
	require.True(t, headerEnd >= 0, "marshaled output should contain version:")

	parsed, err := ParsePerRepoConfig(data[headerEnd:])
	require.NoError(t, err)

	// After roundtrip, AllowedResources must return deny-all (empty,
	// non-nil) — NOT fall through to parent defaults.
	result := parsed.AllowedResources()
	assert.NotNil(t, result, "deny-all must not become nil after roundtrip")
	assert.Empty(t, result, "deny-all must remain empty after roundtrip")
}

// --- Empty roles YAML roundtrip ---

func TestPerRepoConfig_EmptyRoles_YAMLRoundTrip(t *testing.T) {
	// A config with roles: [] must preserve the empty-slice semantics
	// through marshal -> parse -> ConfigRoles(). Before the fix,
	// omitempty dropped the empty slice, re-parse yielded nil, and
	// ConfigRoles() fell through to parent defaults.
	parent := &perRepoConfig{
		Roles:  []string{"triage", "coder", "review"},
		parent: &perRepoDefaults{},
	}
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{}, // explicitly empty — no roles
		parent:  parent,
	}

	// Pre-marshal: ConfigRoles must be empty, not parent roles.
	require.NotNil(t, cfg.ConfigRoles())
	require.Empty(t, cfg.ConfigRoles())

	data, err := cfg.Marshal()
	require.NoError(t, err)

	// The marshaled output must include the empty list.
	assert.Contains(t, string(data), "roles: []")

	// Strip the header so ParsePerRepoConfig can parse the YAML body.
	headerEnd := strings.Index(string(data), "version:")
	require.True(t, headerEnd >= 0, "marshaled output should contain version:")

	parsed, err := ParsePerRepoConfig(data[headerEnd:])
	require.NoError(t, err)

	// After roundtrip (without parent), the raw Roles field must be
	// non-nil empty — not nil.
	prc := parsed.(*perRepoConfig)
	assert.NotNil(t, prc.Roles, "roles must not become nil after roundtrip")
	assert.Empty(t, prc.Roles, "roles must remain empty after roundtrip")

	// Wire up the same parent and verify ConfigRoles does not fall
	// through.
	prc.parent = parent
	assert.NotNil(t, prc.ConfigRoles(), "ConfigRoles must not be nil after roundtrip")
	assert.Empty(t, prc.ConfigRoles(), "ConfigRoles must be empty after roundtrip, not parent roles")
}

func TestPerRepoConfig_MarshalEmptyRoles(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{}, // explicitly empty
		parent:  &perRepoDefaults{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	// Empty roles (non-nil) must appear in output so they survive
	// a roundtrip. Without MarshalYAML the omitempty tag would drop it.
	assert.Contains(t, string(data), "roles: []")
}

// --- Validate with fallback ---

func TestPerRepoConfig_ValidateEmptyVersion_FallsThrough(t *testing.T) {
	// Empty version (inherits from parent) should pass validation.
	cfg := &perRepoConfig{
		Roles:  []string{"triage"},
		parent: &perRepoDefaults{},
	}
	assert.NoError(t, cfg.Validate())
}

func TestPerRepoConfig_ValidateNilRoles_FallsThrough(t *testing.T) {
	// Nil roles (inherits from parent) should pass validation.
	cfg := &perRepoConfig{
		Version: "1",
		parent:  &perRepoDefaults{},
	}
	assert.NoError(t, cfg.Validate())
}

// --- ParsePerRepoConfig sets parent ---

func TestParsePerRepoConfig_SetsParent(t *testing.T) {
	yamlData := `version: "1"
roles:
  - triage
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	// ConfigRuntime should resolve through parent to "claude".
	assert.Equal(t, "claude", cfg.ConfigRuntime())
}

func TestParsePerRepoConfigWriter_SetsParent(t *testing.T) {
	yamlData := `version: "1"
roles:
  - triage
`
	cfg, err := ParsePerRepoConfigWriter([]byte(yamlData))
	require.NoError(t, err)
	// Cast to PerRepoConfigReader to access ConfigRuntime.
	pcr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, "claude", pcr.ConfigRuntime())
}

func TestParsePerRepoConfigWriterLayered_MergesBase(t *testing.T) {
	baseYAML := `version: "1"
agents:
  - name: lint
    source: https://raw.githubusercontent.com/acme/agents/main/harness/lint.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
allowed_remote_resources:
  - https://raw.githubusercontent.com/acme/agents/
`
	overlayYAML := `version: "1"
agents:
  - name: lint
    effort: medium
`
	cfg, err := ParsePerRepoConfigWriterLayered([]byte(overlayYAML), []byte(baseYAML))
	require.NoError(t, err)
	agents := cfg.AgentEntries()
	require.Len(t, agents, 1)
	assert.Equal(t, "lint", agents[0].Name)
	assert.Contains(t, agents[0].Source, "lint.yaml")
	assert.Equal(t, "medium", agents[0].Effort)
	// Validation must pass on the merged set.
	require.NoError(t, cfg.Validate())
}

func TestParsePerRepoConfigWriterLayered_NilBase(t *testing.T) {
	overlayYAML := `version: "1"
roles:
  - triage
`
	cfg, err := ParsePerRepoConfigWriterLayered([]byte(overlayYAML), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"triage"}, cfg.ConfigRoles())
	// Code defaults surface as the parent.
	assert.Equal(t, "claude", cfg.ConfigRuntime())
}

// --- Existing single-file behavior unchanged ---

func TestPerRepoConfig_ExistingSingleFileBehavior(t *testing.T) {
	// A fully populated config (like NewPerRepoConfig produces) should
	// behave identically to pre-parent-chain behavior.
	cfg := NewPerRepoConfig([]string{"triage", "coder", "review"}, "org/repo")

	assert.Equal(t, "1", cfg.ConfigVersion())
	assert.False(t, cfg.IsKillSwitchActive())
	assert.False(t, cfg.IsOrgMode())
	assert.Equal(t, []string{"triage", "coder", "review"}, cfg.ConfigRoles())
	assert.Empty(t, cfg.AgentEntries())
	assert.Equal(t, DefaultAllowedRemoteResources(), cfg.AllowedResources())
	require.NotNil(t, cfg.IssueCreationConfig())

	// Marshal round-trip.
	data, err := cfg.Marshal()
	require.NoError(t, err)

	headerEnd := strings.Index(string(data), "version:")
	require.True(t, headerEnd > 0)

	parsed, err := ParsePerRepoConfig(data[headerEnd:])
	require.NoError(t, err)
	assert.Equal(t, cfg.ConfigVersion(), parsed.ConfigVersion())
	assert.Equal(t, cfg.IsKillSwitchActive(), parsed.IsKillSwitchActive())
	assert.Equal(t, cfg.ConfigRoles(), parsed.ConfigRoles())
}

// --- KillSwitch YAML round-trip ---

func TestPerRepoConfig_KillSwitch_YAMLRoundTrip(t *testing.T) {
	t.Run("kill_switch true round-trips", func(t *testing.T) {
		yamlData := `version: "1"
kill_switch: true
roles:
  - triage
`
		cfg, err := ParsePerRepoConfig([]byte(yamlData))
		require.NoError(t, err)
		assert.True(t, cfg.IsKillSwitchActive())
	})

	t.Run("kill_switch false round-trips", func(t *testing.T) {
		yamlData := `version: "1"
kill_switch: false
roles:
  - triage
`
		cfg, err := ParsePerRepoConfig([]byte(yamlData))
		require.NoError(t, err)
		assert.False(t, cfg.IsKillSwitchActive())
		// Verify it was explicitly set (not inherited).
		prc := cfg.(*perRepoConfig)
		require.NotNil(t, prc.KillSwitch)
		assert.False(t, *prc.KillSwitch)
	})

	t.Run("kill_switch omitted falls through to default", func(t *testing.T) {
		yamlData := `version: "1"
roles:
  - triage
`
		cfg, err := ParsePerRepoConfig([]byte(yamlData))
		require.NoError(t, err)
		assert.False(t, cfg.IsKillSwitchActive())
		// Verify it was not set.
		prc := cfg.(*perRepoConfig)
		assert.Nil(t, prc.KillSwitch)
	})
}
