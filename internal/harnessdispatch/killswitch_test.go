package harnessdispatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
)

func TestKillSwitchActive_MissingConfig(t *testing.T) {
	active, err := KillSwitchActive(t.TempDir())
	require.NoError(t, err)
	assert.False(t, active)
}

func TestKillSwitchActive_PerRepo(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetKillSwitch(true)
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	active, err := KillSwitchActive(dir)
	require.NoError(t, err)
	assert.True(t, active)
}

func TestKillSwitchActive_PerOrgConfigRejected(t *testing.T) {
	dir := t.TempDir()
	data := []byte("version: \"1\"\nkill_switch: true\ndispatch:\n  platform: github-actions\nrepos: {}\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	_, err := KillSwitchActive(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-org configuration format")
}

func TestKillSwitchActive_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("not: valid: yaml: ["), 0o644))
	_, err := KillSwitchActive(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config.yaml")
}
