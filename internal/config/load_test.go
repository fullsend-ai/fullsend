package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromDir_MissingOK(t *testing.T) {
	cfg, err := LoadFromDir(t.TempDir(), LoadOpts{MissingOK: true})
	require.NoError(t, err)
	assert.False(t, cfg.KillSwitch)
	assert.Empty(t, cfg.Agents)
	assert.False(t, cfg.IsOrg)
	require.NotNil(t, cfg.PerRepo)
}

func TestLoadFromDir_MissingNotOK(t *testing.T) {
	_, err := LoadFromDir(t.TempDir(), LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestLoadFromDir_PerRepo(t *testing.T) {
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
	assert.True(t, cfg.KillSwitch)
	require.Len(t, cfg.Agents, 1)
	assert.Equal(t, "ping", cfg.Agents[0].Name)
	assert.Equal(t, []string{"https://example.com/"}, cfg.AllowedRemoteResources)
}

func TestLoadFromDir_Org(t *testing.T) {
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

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	assert.True(t, cfg.IsOrg)
	require.NotNil(t, cfg.Org)
	require.Len(t, cfg.Agents, 1)
	assert.Equal(t, "triage", cfg.Agents[0].DerivedName())
}

func TestLoadFromDir_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(":\n- bad"), 0o644))

	_, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config")
}

func TestLoadFromDir_SharedFieldsDefaultPerRepo(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
agents:
  - harness/custom.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	cfg, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	assert.False(t, cfg.IsOrg)
	require.NotNil(t, cfg.PerRepo)
	require.Len(t, cfg.Agents, 1)
}

func TestLoadFromDir_InvalidOrgConfig(t *testing.T) {
	dir := t.TempDir()
	content := `version: "1"
dispatch:
  platform: ""
repos: not-a-map
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	_, err := LoadFromDir(dir, LoadOpts{MissingOK: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestIsPerRepoYAML(t *testing.T) {
	assert.True(t, IsPerRepoYAML([]byte("version: \"1\"\nagents: []\n")))
	assert.False(t, IsPerRepoYAML([]byte("version: \"1\"\ndispatch:\n  platform: github-actions\n")))
	assert.False(t, IsPerRepoYAML([]byte("not yaml")))
}
