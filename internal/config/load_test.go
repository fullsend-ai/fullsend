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
	assert.Equal(t, []string{"https://example.com/"}, cfg.AllowedResources())
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
	assert.Contains(t, err.Error(), "parsing config")
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
	assert.Contains(t, err.Error(), "parsing")
}

func TestIsPerRepoYAML(t *testing.T) {
	assert.True(t, IsPerRepoYAML([]byte("version: \"1\"\nagents: []\n")))
	assert.False(t, IsPerRepoYAML([]byte("version: \"1\"\ndispatch:\n  platform: github-actions\n")))
	assert.False(t, IsPerRepoYAML([]byte("version: \"1\"\ndispatch:\n  platform: \"\"\n")))
	assert.False(t, IsPerRepoYAML([]byte("not yaml")))
}
