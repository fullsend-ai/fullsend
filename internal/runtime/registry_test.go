package runtime

import (
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	claude, err := Resolve("claude")
	require.NoError(t, err)
	assert.Equal(t, "claude", claude.Runtime.Name())

	dummy, err := Resolve("dummy")
	require.NoError(t, err)
	assert.Equal(t, "dummy", dummy.Runtime.Name())

	_, err = Resolve("unknown")
	require.Error(t, err)
}

func TestResolveFromConfig(t *testing.T) {
	t.Parallel()

	defaultBackend, err := ResolveFromConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, "claude", defaultBackend.Runtime.Name())

	cfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
  runtime: dummy
repos: {}
`))
	require.NoError(t, parseErr)
	dummyBackend, err := ResolveFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "dummy", dummyBackend.Runtime.Name())
}

func TestResolveFromPerRepoConfig(t *testing.T) {
	t.Parallel()

	defaultBackend, err := ResolveFromPerRepoConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, "claude", defaultBackend.Runtime.Name())

	cfg := config.NewPerRepoConfig(nil, "")
	cfg.SetRuntime("dummy")
	dummyBackend, err := ResolveFromPerRepoConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "dummy", dummyBackend.Runtime.Name())

	invalidCfg := config.NewPerRepoConfig(nil, "")
	invalidCfg.SetRuntime("invalid")
	_, err = ResolveFromPerRepoConfig(invalidCfg)
	require.Error(t, err)
}
