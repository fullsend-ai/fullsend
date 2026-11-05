package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
