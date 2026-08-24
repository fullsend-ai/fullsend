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

	oc, err := Resolve("opencode")
	require.NoError(t, err)
	assert.Equal(t, "opencode", oc.Runtime.Name())
	assert.NotNil(t, oc.Transcripts)
	_, isOC := oc.Transcripts.(OpenCodeRuntime)
	assert.True(t, isOC, "Transcripts should be OpenCodeRuntime")

	pb, err := Resolve("pi")
	require.NoError(t, err)
	assert.Equal(t, "pi", pb.Runtime.Name())
	assert.IsType(t, PiRuntime{}, pb.Transcripts)

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

	// pi is user-selectable (#6464).
	piCfg := config.NewPerRepoConfig(nil, "")
	piCfg.SetRuntime("pi")
	piBackend, err := ResolveFromPerRepoConfig(piCfg)
	require.NoError(t, err)
	assert.Equal(t, "pi", piBackend.Runtime.Name())

	invalidCfg := config.NewPerRepoConfig(nil, "")
	invalidCfg.SetRuntime("invalid")
	_, err = ResolveFromPerRepoConfig(invalidCfg)
	require.Error(t, err)
}

func TestResolveFromPerRepoConfig_RejectsStubRuntimes(t *testing.T) {
	t.Parallel()

	// Stub runtimes like "opencode" are resolvable via Resolve() for
	// dev/testing, but must be rejected when coming through config.
	for _, name := range []string{"opencode"} {
		ocCfg := config.NewPerRepoConfig(nil, "")
		ocCfg.SetRuntime(name)
		_, err := ResolveFromPerRepoConfig(ocCfg)
		require.Error(t, err, "stub runtime %q should fail via config path", name)
		assert.Contains(t, err.Error(), "invalid runtime")
	}

	// Direct Resolve() still works for dev/testing.
	rt, err := Resolve("opencode")
	require.NoError(t, err)
	assert.Equal(t, "opencode", rt.Runtime.Name())
}

func TestResolveFromConfig_RejectsStubRuntimes(t *testing.T) {
	t.Parallel()

	// Org config with a stub runtime should fail at resolution time.
	cfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
  runtime: opencode
repos: {}
`))
	// ParseOrgConfig calls Validate() which also rejects "opencode",
	// so this may fail at parse time.  If parsing succeeds (e.g. because
	// Validate() is not called), ResolveFromConfig must still reject it.
	if parseErr == nil {
		_, err := ResolveFromConfig(cfg)
		require.Error(t, err, "stub runtime %q should fail via org config path", "opencode")
		assert.Contains(t, err.Error(), "invalid runtime")
	}
}
