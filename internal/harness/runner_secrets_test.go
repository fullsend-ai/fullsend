package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEnvRefNames(t *testing.T) {
	assert.Nil(t, EnvRefNames("no references"))
	assert.Equal(t, []string{"A", "B", "A"}, EnvRefNames("${A}-$B/${A}"))
	assert.Equal(t, []string{"X"}, EnvRefNames("Bearer ${X}"))
}

func TestOverlayGuardReadsOnlyForgeOrConfig(t *testing.T) {
	allowed := []string{
		`runtime.forge == "github"`,
		`runtime.forge in ["github", "gitlab"]`,
		`true`,
	}
	for _, when := range allowed {
		assert.True(t, overlayGuardReadsOnlyForgeOrConfig(when), when)
	}

	refused := []string{
		`has(event.kind)`,
		`runtime.forge == "github" && has(event.kind)`,
		`has(runtime.forge)`,
		`runtime.forge == "github" || runtime == {}`,
		`not valid cel ((`,
	}
	for _, when := range refused {
		assert.False(t, overlayGuardReadsOnlyForgeOrConfig(when), when)
	}
}

func TestValidateOverlayRunnerSecretRefs(t *testing.T) {
	names := map[string]bool{"X": true}
	withRunnerRef := func(when string) OverlayEntry {
		return OverlayEntry{When: when, ForgeConfig: ForgeConfig{Env: &EnvConfig{Runner: map[string]string{"X": "${X}"}}}}
	}

	t.Run("no names", func(t *testing.T) {
		require.NoError(t, ValidateOverlayRunnerSecretRefs([]OverlayEntry{withRunnerRef(`has(event.kind)`)}, nil))
	})
	t.Run("forge guard allowed", func(t *testing.T) {
		require.NoError(t, ValidateOverlayRunnerSecretRefs([]OverlayEntry{withRunnerRef(`runtime.forge == "github"`)}, names))
	})
	t.Run("event guard refused", func(t *testing.T) {
		err := ValidateOverlayRunnerSecretRefs([]OverlayEntry{
			withRunnerRef(`runtime.forge == "github"`),
			withRunnerRef(`has(event.kind)`),
		}, names)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overlays[1] references runner secret(s) X")
	})
	t.Run("event guard without reference allowed", func(t *testing.T) {
		require.NoError(t, ValidateOverlayRunnerSecretRefs([]OverlayEntry{{
			When:        `has(event.kind)`,
			ForgeConfig: ForgeConfig{Env: &EnvConfig{Runner: map[string]string{"Y": "${Y}"}}},
		}}, names))
	})

	fields := map[string]ForgeConfig{
		"runner_env":      {RunnerEnv: map[string]string{"A": "$X"}},
		"env.sandbox":     {Env: &EnvConfig{Sandbox: map[string]string{"A": "${X}"}}},
		"host_files":      {HostFiles: []HostFile{{Src: "/p/${X}", Dest: "/d"}}},
		"schema":          {ValidationLoop: &ValidationLoop{Schema: "${X}"}},
		"preflight_check": {ValidationLoop: &ValidationLoop{PreflightCheck: "${X}"}},
	}
	for field, fc := range fields {
		t.Run("detects "+field, func(t *testing.T) {
			err := ValidateOverlayRunnerSecretRefs([]OverlayEntry{{When: `has(event.kind)`, ForgeConfig: fc}}, names)
			require.Error(t, err)
		})
	}
}

// The top-level preflight_check cannot ride an overlay past the runner
// secret rules (ADR 0136; ADR 0129 D1 keeps the field top level only):
// ForgeConfig has no preflight_check field, so a preflight_check key inside
// an overlay entry is dropped at unmarshal time — unlike the nested
// validation_loop.preflight_check the fields map above detects. A runner
// secret referenced from a real top-level preflight_check is refused on
// the resolved harness by the CLI's validateRunnerSecretRefs, before
// expansion and before any command runs; this pins that an overlay can
// neither deliver such a check nor steer one past the overlay guard.
func TestValidateOverlayRunnerSecretRefs_TopLevelPreflightCheckKeyIsDropped(t *testing.T) {
	names := map[string]bool{"X": true}
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(`
agent: agents/test.md
role: test
overlays:
  - when: 'has(event.kind)'
    preflight_check: "echo ${X}"
`), &h))
	require.Len(t, h.Overlays, 1)
	assert.Empty(t, h.PreflightCheck, "a preflight_check key inside an overlay never populates the harness field")
	assert.Empty(t, overlayRunnerSecretRefs(&h.Overlays[0].ForgeConfig, names),
		"the dropped key carries no reference for the overlay walk to refuse")
	require.NoError(t, ValidateOverlayRunnerSecretRefs(h.Overlays, names))
	// Round-tripping the parsed entry proves the key is gone rather than
	// silently retained: if PreflightCheck ever gains a ForgeConfig field,
	// this fails and the overlay walk and merge rules need revisiting.
	roundTrip, err := yaml.Marshal(h.Overlays[0])
	require.NoError(t, err)
	assert.NotContains(t, string(roundTrip), "preflight_check")
}
