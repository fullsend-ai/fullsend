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
	cfg.KillSwitch = true
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	active, err := KillSwitchActive(dir)
	require.NoError(t, err)
	assert.True(t, active)
}

func TestKillSwitchActive_OrgConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewOrgConfig(nil, nil, nil, "", "o")
	cfg.KillSwitch = true
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	active, err := KillSwitchActive(dir)
	require.NoError(t, err)
	assert.True(t, active)
}
