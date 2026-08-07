package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_MissingOK_ReturnsDefaultPerRepo(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir(), LoadOpts{MissingOK: true})
	require.NoError(t, err)
	assert.False(t, cfg.IsKillSwitchActive())
	assert.Empty(t, cfg.AgentEntries())
	assert.False(t, cfg.IsOrgMode())
}

func TestLoadConfig_MissingNotOK_ReturnsError(t *testing.T) {
	_, err := LoadConfig(t.TempDir(), LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestLoadConfig_PerRepo(t *testing.T) {
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

	cfg, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrgMode())
	assert.True(t, cfg.IsKillSwitchActive())
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "ping", cfg.AgentEntries()[0].Name)
	// AllowedResources unions with parent defaults.
	resources := cfg.AllowedResources()
	assert.Contains(t, resources, "https://example.com/")
	for _, d := range DefaultAllowedRemoteResources() {
		assert.Contains(t, resources, d)
	}
}

func TestLoadConfig_Org(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
repos: {}
agents:
  - harness/triage.yaml
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/fullsend/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	assert.True(t, cfg.IsOrgMode())
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "triage", cfg.AgentEntries()[0].DerivedName())
}

func TestLoadConfig_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(":\n- bad"), 0o644))

	_, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.Error(t, err)
	// Malformed YAML is detected before type probing so the error
	// names config.yaml rather than the misleading "parsing org config".
	assert.Contains(t, err.Error(), "parsing config.yaml")
}

func TestLoadConfig_SharedFieldsDefaultPerRepo(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
agents:
  - harness/custom.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrgMode())
	require.Len(t, cfg.AgentEntries(), 1)
}

func TestLoadConfig_InvalidOrgConfig(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
dispatch:
  platform: ""
repos: not-a-map
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	_, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestIsPerRepoYAML(t *testing.T) {
	assert.True(t, IsPerRepoYAML([]byte("version: \"1\"\nagents: []\n")))
	assert.False(t, IsPerRepoYAML([]byte("version: \"1\"\ndispatch:\n  platform: github-actions\n")))
	assert.False(t, IsPerRepoYAML([]byte("version: \"1\"\ndispatch:\n  platform: \"\"\n")))
	assert.False(t, IsPerRepoYAML([]byte("not yaml")))
}

// --- Base-layer loading (config.base.yaml) tests ---

func TestLoadConfig_BothFilesExist(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: custom-runtime
roles:
  - triage
kill_switch: true
`
	overlay := `roles:
  - coder
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrgMode())

	pcr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)

	// Overlay sets roles; should NOT fall through to base.
	assert.Equal(t, []string{"coder"}, pcr.ConfigRoles())
	// Runtime unset in overlay; falls through to base.
	assert.Equal(t, "custom-runtime", pcr.ConfigRuntime())
	// KillSwitch unset in overlay; falls through to base.
	assert.True(t, cfg.IsKillSwitchActive())
	// Version unset in overlay; falls through to base.
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestLoadConfig_OnlyOverlay(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
roles:
  - triage
runtime: claude
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrgMode())

	pcr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, []string{"triage"}, pcr.ConfigRoles())
	assert.Equal(t, "claude", pcr.ConfigRuntime())
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestLoadConfig_OnlyBase(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: custom-runtime
roles:
  - triage
  - coder
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))

	// Should succeed even without MissingOK, because base provides
	// config; overlay is created empty.
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrgMode())

	pcr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)
	// All values inherited from base.
	assert.Equal(t, []string{"triage", "coder"}, pcr.ConfigRoles())
	assert.Equal(t, "custom-runtime", pcr.ConfigRuntime())
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestLoadConfig_NeitherExists_MissingOK(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir, LoadOpts{MissingOK: true})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrgMode())
	// Returns NewPerRepoConfig default with populated fields.
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestLoadConfig_NeitherExists_NotOK(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestLoadConfig_BaseOnly_MarshalEmitsOnlyOverlay(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: custom-runtime
roles:
  - triage
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)

	data, err := writer.Marshal()
	require.NoError(t, err)
	s := string(data)
	// Empty overlay: marshal should NOT contain base values.
	assert.NotContains(t, s, "custom-runtime")
	assert.NotContains(t, s, "triage")
}

func TestLoadConfig_BothExist_MarshalEmitsOnlyOverlay(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: custom-runtime
roles:
  - triage
`
	overlay := `roles:
  - coder
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)

	data, err := writer.Marshal()
	require.NoError(t, err)
	s := string(data)
	// Overlay has roles=coder; should be marshaled.
	assert.Contains(t, s, "coder")
	// Base values should NOT appear in overlay marshal.
	assert.NotContains(t, s, "custom-runtime")
}

func TestLoadConfigWriter_BothExist_MutationsDoNotLeakToBase(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: custom-runtime
roles:
  - triage
`
	overlay := `roles:
  - coder
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)

	// Mutate the overlay.
	writer.SetKillSwitch(true)
	writer.SetAgents([]AgentEntry{{Source: "harness/test.yaml"}})

	// Writer reflects the mutation.
	assert.True(t, writer.IsKillSwitchActive())
	assert.Len(t, writer.AgentEntries(), 1)

	// Marshal emits only overlay-level values (including mutations).
	data, err := writer.Marshal()
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, "kill_switch: true")
	assert.Contains(t, s, "harness/test.yaml")
	// Base values should not appear.
	assert.NotContains(t, s, "custom-runtime")

	// Re-load base independently to confirm it was not mutated.
	baseData, err := os.ReadFile(filepath.Join(dir, "config.base.yaml"))
	require.NoError(t, err)
	baseCfg, err := ParsePerRepoConfig(baseData)
	require.NoError(t, err)
	assert.False(t, baseCfg.IsKillSwitchActive())
	assert.Empty(t, baseCfg.AgentEntries())
}

func TestLoadConfigWriter_OnlyBase_WritesCreateOverlay(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: custom-runtime
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)

	// Inherits from base before mutation.
	pcr, ok := writer.(PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, "custom-runtime", pcr.ConfigRuntime())

	// Mutate overlay.
	writer.SetKillSwitch(true)

	data, err := writer.Marshal()
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, "kill_switch: true")
	assert.NotContains(t, s, "custom-runtime")
}

func TestLoadConfigWriter_OnlyOverlay(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
roles:
  - triage
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)
	assert.False(t, writer.IsOrgMode())

	pcr, ok := writer.(PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, []string{"triage"}, pcr.ConfigRoles())
	// Runtime falls through to defaults.
	assert.Equal(t, "claude", pcr.ConfigRuntime())
}

func TestLoadConfig_OrgOverlayIgnoresBase(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
runtime: custom-runtime
`
	orgOverlay := `version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - fullsend
repos: {}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgOverlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	// Should be org mode — base is ignored for org configs.
	assert.True(t, cfg.IsOrgMode())
}

func TestLoadConfig_MalformedBase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(":\n- bad"), 0o644))

	_, err := LoadConfig(dir, LoadOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing base config")
}

func TestLoadConfig_MalformedOverlayWithBase(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(":\n- bad"), 0o644))

	_, err := LoadConfig(dir, LoadOpts{})
	require.Error(t, err)
	// Malformed overlay is detected before type probing.
	assert.Contains(t, err.Error(), "parsing config.yaml")
}

func TestLoadConfig_BothExist_FallbackToCodeDefaults(t *testing.T) {
	dir := t.TempDir()
	// Base has no runtime set, so it should fall through to code
	// defaults (perRepoDefaults).
	base := `roles:
  - triage
`
	overlay := `agents:
  - name: ping
    source: harness/ping.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)

	pcr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)
	// Runtime: overlay empty → base empty → code default "claude".
	assert.Equal(t, "claude", pcr.ConfigRuntime())
	// Roles: overlay nil → base ["triage"].
	assert.Equal(t, []string{"triage"}, pcr.ConfigRoles())
	// Version: overlay empty → base empty → code default "1".
	assert.Equal(t, "1", cfg.ConfigVersion())
	// Agents from overlay.
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "ping", cfg.AgentEntries()[0].Name)
}

func TestLoadConfig_BothExist_AgentEntryMergeAcrossLayers(t *testing.T) {
	dir := t.TempDir()
	// Base defines two agents: triage and review.
	base := `version: "1"
agents:
  - name: triage
    source: harness/triage.yaml
  - name: review
    source: harness/review.yaml
`
	// Overlay overrides triage's source and adds a new agent (deploy).
	overlay := `agents:
  - name: triage
    source: harness/triage-v2.yaml
  - name: deploy
    source: harness/deploy.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)

	agents := cfg.AgentEntries()
	// Expect 3 agents: triage (overridden), review (inherited), deploy (additive).
	require.Len(t, agents, 3)

	// triage: source overridden by overlay.
	assert.Equal(t, "triage", agents[0].DerivedName())
	assert.Equal(t, "harness/triage-v2.yaml", agents[0].Source)

	// review: inherited from base unchanged.
	assert.Equal(t, "review", agents[1].DerivedName())
	assert.Equal(t, "harness/review.yaml", agents[1].Source)

	// deploy: additive from overlay only.
	assert.Equal(t, "deploy", agents[2].DerivedName())
	assert.Equal(t, "harness/deploy.yaml", agents[2].Source)
}

func TestLoadConfig_BothExist_MintInferenceFallback(t *testing.T) {
	dir := t.TempDir()
	// Base sets mint/inference values; overlay overrides only mint_url.
	base := `version: "1"
mint_url: https://base-mint.example.com
inference_provider: vertex
inference_project: base-project
inference_region: us-central1
inference_wif_provider: projects/123/locations/global/workloadIdentityPools/pool/providers/prov
`
	overlay := `mint_url: https://overlay-mint.example.com
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)

	pcr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)

	// Overlay overrides mint_url.
	assert.Equal(t, "https://overlay-mint.example.com", pcr.ConfigMintURL())
	// Inference fields fall through to base.
	assert.Equal(t, "vertex", pcr.ConfigInferenceProvider())
	assert.Equal(t, "base-project", pcr.ConfigInferenceProject())
	assert.Equal(t, "us-central1", pcr.ConfigInferenceRegion())
	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/prov", pcr.ConfigInferenceWIFProvider())
}

func TestLoadConfig_OnlyBase_MintInference(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
mint_url: https://base-mint.example.com
inference_provider: vertex
inference_project: base-project
inference_region: global
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)

	pcr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)

	// All values from base.
	assert.Equal(t, "https://base-mint.example.com", pcr.ConfigMintURL())
	assert.Equal(t, "vertex", pcr.ConfigInferenceProvider())
	assert.Equal(t, "base-project", pcr.ConfigInferenceProject())
	assert.Equal(t, "global", pcr.ConfigInferenceRegion())
	// WIF provider not set in base — falls through to default (empty).
	assert.Equal(t, "", pcr.ConfigInferenceWIFProvider())
}

func TestLoadConfig_BothExist_MarshalOmitsMintInferenceFromBase(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
mint_url: https://base-mint.example.com
inference_provider: vertex
inference_project: base-project
`
	overlay := `roles:
  - coder
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)

	data, err := writer.Marshal()
	require.NoError(t, err)
	s := string(data)
	// Base values should NOT appear in overlay marshal.
	assert.NotContains(t, s, "base-mint")
	assert.NotContains(t, s, "inference_provider")
	assert.NotContains(t, s, "base-project")
	// Overlay values should appear.
	assert.Contains(t, s, "coder")
}

func TestLoadConfig_BothExist_AllowedResourcesUnionAcrossLayers(t *testing.T) {
	dir := t.TempDir()
	// Base defines one custom resource prefix.
	base := `version: "1"
allowed_remote_resources:
  - "https://example.com/base/"
`
	// Overlay defines a different custom resource prefix.
	overlay := `allowed_remote_resources:
  - "https://example.com/overlay/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)

	resources := cfg.AllowedResources()
	// Should contain overlay, base, and code-default resources (union with dedup).
	assert.Contains(t, resources, "https://example.com/overlay/")
	assert.Contains(t, resources, "https://example.com/base/")
	for _, d := range DefaultAllowedRemoteResources() {
		assert.Contains(t, resources, d)
	}
	// Verify no duplicates.
	seen := make(map[string]bool, len(resources))
	for _, r := range resources {
		assert.False(t, seen[r], "duplicate resource: %s", r)
		seen[r] = true
	}
}
