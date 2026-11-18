package harness

import (
	"os"
	"strings"
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
		// The global runtime.forge, spelled with a leading dot under a
		// shadowing comprehension variable.
		`[0].exists(runtime, .runtime.forge == "github")`,
		// A dotless runtime comprehension variable used only as .forge is
		// read as the global runtime.forge.
		`[{"forge": "github"}].exists(runtime, runtime.forge == "github")`,
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
		// A shadowing comprehension variable makes the checker keep the
		// global reference as ".event" / ".runtime".
		`[0].exists(event, .event.kind == "x")`,
		`[0].exists(runtime, .runtime.other == "x")`,
		// A dotless event is refused even when it is the comprehension
		// variable rather than the global (fails closed).
		`[0].exists(event, event == 0)`,
		// The dotless runtime variable is used only as .forge, but the range
		// it iterates reads event.kind, so the expression is still refused.
		`[{"forge": event.kind}].exists(runtime, runtime.forge == "github")`,
	}
	for _, when := range refused {
		assert.False(t, overlayGuardReadsOnlyForgeOrConfig(when), when)
	}
}

// TestOverlayGuardDocCommentNamesNewOverlayEnvRule keeps the guard's doc
// comment honest about its blind spot: NewOverlayEnv declares the variables
// a when: expression can read, and the guard must handle each one, or a
// variable added there passes the guard unchecked. The comment is the
// contract the next reader of the guard relies on, the same way
// TestUpstreamRefOverride_MatchesE2EBuildStamp keeps a stamp in sync.
func TestOverlayGuardDocCommentNamesNewOverlayEnvRule(t *testing.T) {
	src, err := os.ReadFile("runner_secrets.go")
	require.NoError(t, err)
	idx := strings.Index(string(src), "func overlayGuardReadsOnlyForgeOrConfig(")
	require.NotEqual(t, -1, idx, "overlayGuardReadsOnlyForgeOrConfig stays in runner_secrets.go")
	// The doc comment is the contiguous // block immediately above the func.
	lines := strings.Split(strings.TrimRight(string(src[:idx]), " \t\n"), "\n")
	var comment []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "//") {
			break
		}
		comment = append([]string{strings.TrimPrefix(line, "//")}, comment...)
	}
	doc := strings.Join(comment, " ")
	assert.Contains(t, doc, "NewOverlayEnv", "the comment names the variable source the guard must keep up with")
	assert.Contains(t, doc, "unchecked", "the comment says an unhandled variable passes the guard unchecked")
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
	t.Run("shadowed event guard refused", func(t *testing.T) {
		err := ValidateOverlayRunnerSecretRefs([]OverlayEntry{
			withRunnerRef(`runtime.forge == "github"`),
			withRunnerRef(`[0].exists(event, .event.kind == "x")`),
		}, names)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overlays[1] references runner secret(s) X")
	})
	t.Run("shadowed runtime guard refused", func(t *testing.T) {
		err := ValidateOverlayRunnerSecretRefs([]OverlayEntry{
			withRunnerRef(`runtime.forge == "github"`),
			withRunnerRef(`[0].exists(runtime, .runtime.other == "x")`),
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
