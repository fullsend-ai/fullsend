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

func TestLoadConfigDir_MissingFile(t *testing.T) {
	cfg, err := LoadConfigDir(t.TempDir())
	require.NoError(t, err)
	assert.False(t, cfg.KillSwitch)
	assert.Empty(t, cfg.Agents)
}

func TestLoadConfigDir_PerRepo(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.KillSwitch = true
	cfg.Agents = []config.AgentEntry{{Name: "ping", Source: "harness/ping.yaml"}}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	loaded, err := LoadConfigDir(dir)
	require.NoError(t, err)
	assert.True(t, loaded.KillSwitch)
	require.Len(t, loaded.Agents, 1)
	assert.Equal(t, "ping", loaded.Agents[0].Name)
}
