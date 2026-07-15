package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- LayeredConfig accessor tests ---

func TestLayeredConfig_Version_OverlayWins(t *testing.T) {
	overlay := &PerRepoConfig{Version: "1"}
	base := &PerRepoConfig{Version: "1"}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, "1", lc.Version())
}

func TestLayeredConfig_Version_FallsToBase(t *testing.T) {
	overlay := &PerRepoConfig{}
	base := &PerRepoConfig{Version: "1"}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, "1", lc.Version())
}

func TestLayeredConfig_Version_NoLayers(t *testing.T) {
	lc := NewLayeredConfig()
	assert.Equal(t, "", lc.Version())
}

func TestLayeredConfig_KillSwitch_ORSemantics(t *testing.T) {
	tests := []struct {
		name     string
		overlay  bool
		base     bool
		expected bool
	}{
		{"both false", false, false, false},
		{"overlay true", true, false, true},
		{"base true", false, true, true},
		{"both true", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := &PerRepoConfig{KillSwitch: tt.overlay}
			base := &PerRepoConfig{KillSwitch: tt.base}
			lc := NewLayeredConfig(overlay, base)
			assert.Equal(t, tt.expected, lc.KillSwitch())
		})
	}
}

func TestLayeredConfig_KillSwitch_NoLayers(t *testing.T) {
	lc := NewLayeredConfig()
	assert.False(t, lc.KillSwitch())
}

func TestLayeredConfig_Runtime_OverlayWins(t *testing.T) {
	overlay := &PerRepoConfig{Runtime: "dummy"}
	base := &PerRepoConfig{Runtime: "claude"}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, "dummy", lc.Runtime())
}

func TestLayeredConfig_Runtime_FallsToBase(t *testing.T) {
	overlay := &PerRepoConfig{}
	base := &PerRepoConfig{Runtime: "claude"}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, "claude", lc.Runtime())
}

func TestLayeredConfig_Runtime_NoLayers(t *testing.T) {
	lc := NewLayeredConfig()
	assert.Equal(t, "", lc.Runtime())
}

func TestLayeredConfig_Roles_OverlayReplacesBase(t *testing.T) {
	overlay := &PerRepoConfig{Roles: []string{"triage"}}
	base := &PerRepoConfig{Roles: []string{"triage", "coder", "review"}}
	lc := NewLayeredConfig(overlay, base)
	// Slice override: overlay replaces entirely, not merged.
	assert.Equal(t, []string{"triage"}, lc.Roles())
}

func TestLayeredConfig_Roles_FallsToBase(t *testing.T) {
	overlay := &PerRepoConfig{} // Roles is nil
	base := &PerRepoConfig{Roles: []string{"triage", "coder"}}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, []string{"triage", "coder"}, lc.Roles())
}

func TestLayeredConfig_Roles_EmptySliceIsNonNil(t *testing.T) {
	// An explicit empty list (roles: []) should NOT fall through to base.
	overlay := &PerRepoConfig{Roles: []string{}}
	base := &PerRepoConfig{Roles: []string{"triage"}}
	lc := NewLayeredConfig(overlay, base)
	assert.NotNil(t, lc.Roles())
	assert.Empty(t, lc.Roles())
}

func TestLayeredConfig_Roles_NoLayers(t *testing.T) {
	lc := NewLayeredConfig()
	assert.Nil(t, lc.Roles())
}

func TestLayeredConfig_Agents_OverlayReplacesBase(t *testing.T) {
	overlay := &PerRepoConfig{
		Agents: []AgentEntry{{Source: "harness/overlay.yaml"}},
	}
	base := &PerRepoConfig{
		Agents: []AgentEntry{{Source: "harness/base.yaml"}},
	}
	lc := NewLayeredConfig(overlay, base)
	require.Len(t, lc.Agents(), 1)
	assert.Equal(t, "harness/overlay.yaml", lc.Agents()[0].Source)
}

func TestLayeredConfig_Agents_FallsToBase(t *testing.T) {
	overlay := &PerRepoConfig{}
	base := &PerRepoConfig{
		Agents: []AgentEntry{{Source: "harness/base.yaml"}},
	}
	lc := NewLayeredConfig(overlay, base)
	require.Len(t, lc.Agents(), 1)
	assert.Equal(t, "harness/base.yaml", lc.Agents()[0].Source)
}

func TestLayeredConfig_Agents_NoLayers(t *testing.T) {
	lc := NewLayeredConfig()
	assert.Nil(t, lc.Agents())
}

func TestLayeredConfig_AllowedRemoteResources_OverlayWins(t *testing.T) {
	overlay := &PerRepoConfig{
		AllowedRemoteResources: []string{"https://overlay.example.com/"},
	}
	base := &PerRepoConfig{
		AllowedRemoteResources: []string{"https://base.example.com/"},
	}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, []string{"https://overlay.example.com/"}, lc.AllowedRemoteResources())
}

func TestLayeredConfig_AllowedRemoteResources_FallsToBase(t *testing.T) {
	overlay := &PerRepoConfig{}
	base := &PerRepoConfig{
		AllowedRemoteResources: []string{"https://base.example.com/"},
	}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, []string{"https://base.example.com/"}, lc.AllowedRemoteResources())
}

func TestLayeredConfig_AllowedRemoteResources_NoLayers(t *testing.T) {
	lc := NewLayeredConfig()
	assert.Nil(t, lc.AllowedRemoteResources())
}

func TestLayeredConfig_CreateIssues_OverlayWins(t *testing.T) {
	overlayCfg := &CreateIssuesConfig{
		AllowTargets: AllowTargets{Orgs: []string{"overlay-org"}},
	}
	baseCfg := &CreateIssuesConfig{
		AllowTargets: AllowTargets{Orgs: []string{"base-org"}},
	}
	overlay := &PerRepoConfig{CreateIssues: overlayCfg}
	base := &PerRepoConfig{CreateIssues: baseCfg}
	lc := NewLayeredConfig(overlay, base)
	require.NotNil(t, lc.CreateIssues())
	assert.Equal(t, []string{"overlay-org"}, lc.CreateIssues().AllowTargets.Orgs)
}

func TestLayeredConfig_CreateIssues_FallsToBase(t *testing.T) {
	baseCfg := &CreateIssuesConfig{
		AllowTargets: AllowTargets{Repos: []string{"org/repo"}},
	}
	overlay := &PerRepoConfig{}
	base := &PerRepoConfig{CreateIssues: baseCfg}
	lc := NewLayeredConfig(overlay, base)
	require.NotNil(t, lc.CreateIssues())
	assert.Equal(t, []string{"org/repo"}, lc.CreateIssues().AllowTargets.Repos)
}

func TestLayeredConfig_CreateIssues_NoLayers(t *testing.T) {
	lc := NewLayeredConfig()
	assert.Nil(t, lc.CreateIssues())
}

func TestLayeredConfig_NilLayersFiltered(t *testing.T) {
	overlay := &PerRepoConfig{Runtime: "claude"}
	lc := NewLayeredConfig(nil, overlay, nil)
	assert.Equal(t, "claude", lc.Runtime())
	assert.Equal(t, 1, lc.LayerCount())
}

func TestLayeredConfig_Overlay(t *testing.T) {
	overlay := &PerRepoConfig{Version: "1"}
	base := &PerRepoConfig{Version: "1"}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, overlay, lc.Overlay())
}

func TestLayeredConfig_Overlay_Empty(t *testing.T) {
	lc := NewLayeredConfig()
	assert.Nil(t, lc.Overlay())
}

func TestLayeredConfig_Base(t *testing.T) {
	overlay := &PerRepoConfig{Version: "1"}
	base := &PerRepoConfig{Version: "1"}
	lc := NewLayeredConfig(overlay, base)
	assert.Equal(t, base, lc.Base())
}

func TestLayeredConfig_Base_SingleLayer(t *testing.T) {
	overlay := &PerRepoConfig{Version: "1"}
	lc := NewLayeredConfig(overlay)
	assert.Nil(t, lc.Base())
}

func TestLayeredConfig_LayerCount(t *testing.T) {
	assert.Equal(t, 0, NewLayeredConfig().LayerCount())
	assert.Equal(t, 1, NewLayeredConfig(&PerRepoConfig{}).LayerCount())
	assert.Equal(t, 2, NewLayeredConfig(&PerRepoConfig{}, &PerRepoConfig{}).LayerCount())
}

// --- Integration: full layered resolution chain ---

func TestLayeredConfig_FullResolutionChain(t *testing.T) {
	// Simulate a real scenario: overlay customizes roles and kill switch,
	// base provides defaults for agents, allowed resources, and runtime.
	overlay := &PerRepoConfig{
		Version:    "1",
		KillSwitch: false,
		Roles:      []string{"triage", "review"},
	}
	base := &PerRepoConfig{
		Version: "1",
		Runtime: "claude",
		Roles:   []string{"triage", "coder", "review", "retro"},
		Agents: []AgentEntry{
			{Source: "harness/triage.yaml"},
			{Source: "harness/review.yaml"},
		},
		AllowedRemoteResources: []string{
			"https://raw.githubusercontent.com/fullsend-ai/fullsend/",
		},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{Repos: []string{"org/repo"}},
		},
	}

	lc := NewLayeredConfig(overlay, base)

	// Overlay scalar wins for version.
	assert.Equal(t, "1", lc.Version())
	// Overlay bool: false doesn't mask base false -> false.
	assert.False(t, lc.KillSwitch())
	// Overlay sets Runtime to "" -> falls to base.
	assert.Equal(t, "claude", lc.Runtime())
	// Overlay has non-nil Roles -> overlay wins (slice override).
	assert.Equal(t, []string{"triage", "review"}, lc.Roles())
	// Overlay has nil Agents -> falls to base.
	require.Len(t, lc.Agents(), 2)
	assert.Equal(t, "harness/triage.yaml", lc.Agents()[0].Source)
	// Overlay has nil AllowedRemoteResources -> falls to base.
	assert.Equal(t, []string{
		"https://raw.githubusercontent.com/fullsend-ai/fullsend/",
	}, lc.AllowedRemoteResources())
	// Overlay has nil CreateIssues -> falls to base.
	require.NotNil(t, lc.CreateIssues())
	assert.Equal(t, []string{"org/repo"}, lc.CreateIssues().AllowTargets.Repos)
}

// --- LoadFromDir layered loading tests ---

func TestLoadFromDir_BothLayers(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: claude
roles:
  - triage
  - coder
  - review
agents:
  - name: lint
    source: harness/lint.yaml
allowed_remote_resources:
  - "https://example.com/"
`
	overlay := `version: "1"
roles:
  - triage
  - review
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)

	assert.False(t, cfg.IsOrg)
	require.NotNil(t, cfg.PerRepo)
	require.NotNil(t, cfg.Base)
	require.NotNil(t, cfg.Layered)
	assert.Equal(t, 2, cfg.Layered.LayerCount())

	// Overlay roles win (slice override).
	assert.Equal(t, []string{"triage", "review"}, cfg.Layered.Roles())
	// Runtime falls to base.
	assert.Equal(t, "claude", cfg.Layered.Runtime())
	// Agents fall to base.
	require.Len(t, cfg.Agents, 1)
	assert.Equal(t, "lint", cfg.Agents[0].Name)
	// AllowedRemoteResources fall to base.
	assert.Equal(t, []string{"https://example.com/"}, cfg.AllowedRemoteResources)
}

func TestLoadFromDir_OverlayOnly_BackwardCompat(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
roles:
  - triage
kill_switch: true
agents:
  - name: ping
    source: harness/ping.yaml
allowed_remote_resources:
  - "https://example.com/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)

	assert.False(t, cfg.IsOrg)
	require.NotNil(t, cfg.PerRepo)
	assert.Nil(t, cfg.Base)
	require.NotNil(t, cfg.Layered)
	assert.Equal(t, 1, cfg.Layered.LayerCount())

	// Values come from the single layer.
	assert.True(t, cfg.KillSwitch)
	require.Len(t, cfg.Agents, 1)
	assert.Equal(t, "ping", cfg.Agents[0].Name)
	assert.Equal(t, []string{"https://example.com/"}, cfg.AllowedRemoteResources)
	assert.Equal(t, []string{"triage"}, cfg.Layered.Roles())
}

func TestLoadFromDir_BaseOnly_MissingOK(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: claude
roles:
  - triage
  - coder
agents:
  - name: lint
    source: harness/lint.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: true})
	require.NoError(t, err)

	assert.False(t, cfg.IsOrg)
	require.NotNil(t, cfg.PerRepo)
	require.NotNil(t, cfg.Base)
	require.NotNil(t, cfg.Layered)
	assert.Equal(t, 2, cfg.Layered.LayerCount())

	// Values resolve from base.
	assert.Equal(t, "claude", cfg.Layered.Runtime())
	assert.Equal(t, []string{"triage", "coder"}, cfg.Layered.Roles())
	// Top-level DirConfig fields also resolved from base.
	require.Len(t, cfg.Agents, 1)
	assert.Equal(t, "lint", cfg.Agents[0].Name)
}

func TestLoadFromDir_NeitherFile_MissingOK(t *testing.T) {
	cfg, err := LoadFromDir(t.TempDir(), LoadOpts{MissingOK: true})
	require.NoError(t, err)

	assert.False(t, cfg.IsOrg)
	require.NotNil(t, cfg.PerRepo)
	assert.Nil(t, cfg.Base)
	require.NotNil(t, cfg.Layered)
	assert.Equal(t, 1, cfg.Layered.LayerCount())
	// Default PerRepoConfig is the single layer.
	assert.False(t, cfg.KillSwitch)
}

func TestLoadFromDir_NeitherFile_MissingNotOK(t *testing.T) {
	_, err := LoadFromDir(t.TempDir(), LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestLoadFromDir_BaseOnly_MissingNotOK(t *testing.T) {
	// config.yaml is required when MissingOK is false, even if base exists.
	dir := t.TempDir()
	base := `version: "1"
roles:
  - triage
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))

	_, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestLoadFromDir_OrgConfig_IgnoresBase(t *testing.T) {
	dir := t.TempDir()
	orgYAML := `version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
  max_implementation_retries: 2
repos: {}
`
	base := `version: "1"
roles:
  - triage
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)

	// Org configs do not use layering.
	assert.True(t, cfg.IsOrg)
	assert.Nil(t, cfg.Base)
	assert.Nil(t, cfg.Layered)
}

func TestLoadFromDir_KillSwitch_ORSemantics(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
kill_switch: true
`
	overlay := `version: "1"
roles:
  - triage
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)

	// Base sets kill_switch: true, overlay doesn't -> resolved to true (OR).
	assert.True(t, cfg.KillSwitch)
	assert.True(t, cfg.Layered.KillSwitch())
}

func TestLoadFromDir_InvalidBase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("not: [valid: yaml"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"1\"\nroles: [triage]"), 0o644))

	_, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing base config")
}

func TestLoadFromDir_BothLayers_OverlayScalarWins(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: claude
`
	overlay := `version: "1"
runtime: dummy
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)

	assert.Equal(t, "dummy", cfg.Layered.Runtime())
}

func TestLoadFromDir_BothLayers_OverlayAgentsWin(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
agents:
  - name: base-agent
    source: harness/base.yaml
`
	overlay := `version: "1"
agents:
  - name: overlay-agent
    source: harness/overlay.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)

	// Overlay agents replace base agents (slice override, not merge).
	require.Len(t, cfg.Agents, 1)
	assert.Equal(t, "overlay-agent", cfg.Agents[0].Name)
}

func TestLoadFromDir_LayeredPreservesPerRepoAndBase(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: claude
`
	overlay := `version: "1"
roles:
  - triage
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)

	// PerRepo is the overlay, Base is the base.
	require.NotNil(t, cfg.PerRepo)
	require.NotNil(t, cfg.Base)
	assert.Equal(t, []string{"triage"}, cfg.PerRepo.Roles)
	assert.Equal(t, "claude", cfg.Base.Runtime)
	// Layered accessor references the same objects.
	assert.Equal(t, cfg.PerRepo, cfg.Layered.Overlay())
	assert.Equal(t, cfg.Base, cfg.Layered.Base())
}

// --- LoadFromFile unchanged behavior ---

func TestLoadFromFile_StillWorksWithoutBase(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
roles:
  - triage
`
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	// LoadFromFile does not load config.base.yaml; it's a single-file loader.
	cfg, err := LoadFromFile(path, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrg)
	require.NotNil(t, cfg.PerRepo)
	assert.Nil(t, cfg.Base)
	assert.Nil(t, cfg.Layered)
}
