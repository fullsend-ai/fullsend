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

	dp, err := Resolve("dummy-playback")
	require.NoError(t, err)
	assert.Equal(t, "dummy-playback", dp.Runtime.Name())

	oc, err := Resolve("opencode")
	require.NoError(t, err)
	assert.Equal(t, "opencode", oc.Runtime.Name())
	assert.NotNil(t, oc.Transcripts)
	_, isOC := oc.Transcripts.(OpenCodeRuntime)
	assert.True(t, isOC, "Transcripts should be OpenCodeRuntime")

	cx, err := Resolve("codex")
	require.NoError(t, err)
	assert.Equal(t, "codex", cx.Runtime.Name())
	assert.IsType(t, CodexRuntime{}, cx.Transcripts)

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

	// codex is user-selectable too (#6920), and resolves its own backend
	// rather than falling back to the default.
	codexCfg := config.NewPerRepoConfig(nil, "")
	codexCfg.SetRuntime("codex")
	codexBackend, err := ResolveFromPerRepoConfig(codexCfg)
	require.NoError(t, err)
	assert.Equal(t, "codex", codexBackend.Runtime.Name())
	assert.IsType(t, CodexRuntime{}, codexBackend.Transcripts)

	invalidCfg := config.NewPerRepoConfig(nil, "")
	invalidCfg.SetRuntime("invalid")
	_, err = ResolveFromPerRepoConfig(invalidCfg)
	require.Error(t, err)
}

func TestResolveFromPerRepoConfig_OpenCodeSelectable(t *testing.T) {
	t.Parallel()

	// opencode is now user-selectable (unbound-force#510), so it resolves
	// through per-repo config like pi.
	ocCfg := config.NewPerRepoConfig(nil, "")
	ocCfg.SetRuntime("opencode")
	b, err := ResolveFromPerRepoConfig(ocCfg)
	require.NoError(t, err)
	assert.Equal(t, "opencode", b.Runtime.Name())

	// An unknown runtime is still rejected via config.
	badCfg := config.NewPerRepoConfig(nil, "")
	badCfg.SetRuntime("nope")
	_, err = ResolveFromPerRepoConfig(badCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid runtime")
}

func TestResolveFromConfig_OpenCodeSelectable(t *testing.T) {
	t.Parallel()

	// Org config selecting opencode now parses and resolves.
	cfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
  runtime: opencode
repos: {}
`))
	require.NoError(t, parseErr)
	b, err := ResolveFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "opencode", b.Runtime.Name())
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

func TestResolveForAgent_RejectsUnknownRuntimes(t *testing.T) {
	t.Parallel()
	// A per-agent value is validated like the repo-wide key: unknown names
	// cannot be activated through config.
	agents := []config.AgentEntry{{Name: "code", Runtime: "invalid"}}
	_, _, err := ResolveForAgent(agents, "pi", "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agents.code")
	assert.Contains(t, err.Error(), "invalid runtime")

	backend, _, err := ResolveForAgent(agents, "pi", "triage")
	require.NoError(t, err)
	assert.Equal(t, "pi", backend.Runtime.Name(), "other agents unaffected")

	_, _, err = ResolveForAgent(nil, "nope", "code")
	require.Error(t, err, "repo-wide unknown runtime is rejected too")
}

func TestResolveForAgent_SelectableRuntimes(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"codex", "opencode"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			agents := []config.AgentEntry{{Name: "code", Runtime: name}}
			backend, perAgent, err := ResolveForAgent(agents, "pi", "code")
			require.NoError(t, err)
			assert.Equal(t, name, backend.Runtime.Name())
			assert.True(t, perAgent)

			backend, _, err = ResolveForAgent(nil, name, "code")
			require.NoError(t, err)
			assert.Equal(t, name, backend.Runtime.Name())
		})
	}
}
