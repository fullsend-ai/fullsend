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

func TestResolveForAgent(t *testing.T) {
	t.Parallel()
	cfg, err := config.ParsePerRepoConfig([]byte(`# fullsend per-repo configuration
version: "1"
runtime: pi
agents:
  - name: code
    runtime: claude
  - name: fix
    model: sonnet
`))
	require.NoError(t, err)
	agents := cfg.AgentEntries()

	// The agents: entry's runtime wins over the repo-wide key.
	backend, perAgent, err := ResolveForAgent(agents, cfg.(config.PerRepoConfigReader).ConfigRuntime(), "code")
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name())
	assert.True(t, perAgent)

	// An entry without runtime falls back to the repo-wide key; so does a
	// missing entry or a missing agent name.
	for _, agent := range []string{"fix", "triage", ""} {
		backend, perAgent, err = ResolveForAgent(agents, "pi", agent)
		require.NoError(t, err, agent)
		assert.Equal(t, "pi", backend.Runtime.Name(), agent)
		assert.False(t, perAgent, agent)
	}

	// No entries and no repo-wide value: the code default.
	backend, perAgent, err = ResolveForAgent(nil, "", "code")
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name())
	assert.False(t, perAgent)
}

func TestResolveForAgent_RejectsStubRuntimes(t *testing.T) {
	t.Parallel()
	// A per-agent value is validated like the repo-wide key: stub runtimes
	// (opencode) and unknown names cannot be activated through config.
	for _, name := range []string{"opencode", "invalid"} {
		agents := []config.AgentEntry{{Name: "code", Runtime: name}}
		_, _, err := ResolveForAgent(agents, "pi", "code")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "agents.code")
		assert.Contains(t, err.Error(), "invalid runtime")

		backend, _, err := ResolveForAgent(agents, "pi", "triage")
		require.NoError(t, err)
		assert.Equal(t, "pi", backend.Runtime.Name(), "other agents unaffected")
	}
	_, _, err := ResolveForAgent(nil, "opencode", "code")
	require.Error(t, err, "repo-wide stub runtime is rejected too")
}
