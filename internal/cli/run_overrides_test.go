package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveRunOverrides_Precedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		flags   runOverrideFlags
		env     map[string]string
		runtime string
		want    runOverrides
	}{
		{name: "nothing set", want: runOverrides{}},
		{
			name:  "flags beat env",
			flags: runOverrideFlags{runtime: "pi", model: "sonnet", effort: "low"},
			env:   map[string]string{envRuntime: "claude", envModel: "haiku", envEffort: "high", envPiModel: "x"},
			want: runOverrides{
				runtime: "pi", runtimeSource: sourceFlagRuntime,
				model: "sonnet", modelSource: sourceFlagModel,
				effort: "low", effortSource: sourceFlagEffort,
			},
		},
		{
			name: "env applies when flags absent",
			env:  map[string]string{envRuntime: " pi ", envModel: "google-vertex/gemini-2.5-flash", envEffort: "medium", envFallbackModels: "sonnet, haiku,"},
			want: runOverrides{
				runtime: "pi", runtimeSource: envRuntime,
				model: "google-vertex/gemini-2.5-flash", modelSource: envModel,
				effort: "medium", effortSource: envEffort,
				fallbackModels: []string{"sonnet", "haiku"}, fallbackSource: envFallbackModels,
			},
		},
		{
			name:    "FULLSEND_PI_MODEL is an alias on pi",
			env:     map[string]string{envPiModel: "claude-opus-4-8"},
			runtime: "pi",
			want:    runOverrides{model: "claude-opus-4-8", modelSource: envPiModel},
		},
		{
			name:    "FULLSEND_PI_MODEL is ignored on claude",
			env:     map[string]string{envPiModel: "claude-opus-4-8"},
			runtime: "claude",
			want:    runOverrides{},
		},
		{
			name: "FULLSEND_MODEL beats FULLSEND_PI_MODEL on pi",
			env:  map[string]string{envModel: "haiku", envPiModel: "opus", envRuntime: "pi"},
			want: runOverrides{runtime: "pi", runtimeSource: envRuntime, model: "haiku", modelSource: envModel},
		},
		{
			name: "runtime override gates the pi alias",
			env:  map[string]string{envPiModel: "opus", envRuntime: "pi"},
			// config said claude, env switches to pi: the alias applies.
			runtime: "claude",
			want:    runOverrides{runtime: "pi", runtimeSource: envRuntime, model: "opus", modelSource: envPiModel},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveRunOverrides(tc.flags, envMap(tc.env), tc.runtime)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveRunOverrides_InvalidRuntime(t *testing.T) {
	t.Parallel()
	_, err := resolveRunOverrides(runOverrideFlags{runtime: "opencode"}, envMap(nil), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--runtime flag")
	assert.Contains(t, err.Error(), `invalid runtime "opencode"`)

	_, err = resolveRunOverrides(runOverrideFlags{}, envMap(map[string]string{envRuntime: "nope"}), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), envRuntime)
}

func TestResolveBackend_OverrideWinsOverConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	require.NoError(t, os.WriteFile(cfg, []byte("# fullsend per-repo configuration\nversion: \"1\"\nruntime: claude\n"), 0o644))

	backend, source, err := resolveBackend(runOverrides{}, cfg)
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name())
	assert.Equal(t, cfg, source)

	backend, source, err = resolveBackend(runOverrides{runtime: "pi", runtimeSource: sourceFlagRuntime}, cfg)
	require.NoError(t, err)
	assert.Equal(t, "pi", backend.Runtime.Name())
	assert.Equal(t, sourceFlagRuntime, source)
}

func TestModelOverrideSource(t *testing.T) {
	t.Parallel()
	assert.Equal(t, sourceDefault, modelOverrideSource(runOverrides{}, ""))
	assert.Equal(t, sourceHarness, modelOverrideSource(runOverrides{}, "opus"))
	assert.Equal(t, envModel, modelOverrideSource(runOverrides{model: "haiku", modelSource: envModel}, "haiku"))
	assert.Equal(t, envPiModel, modelOverrideSource(runOverrides{model: "x", modelSource: envPiModel}, "x"))
}

func TestWithSource(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "opus", withSource("opus", ""))
	assert.Equal(t, "haiku (from FULLSEND_MODEL)", withSource("haiku", envModel))
}

func TestEmitRunInfoNotice(t *testing.T) {
	t.Parallel()
	info := runInfoFor(aggregateMetrics{Runtime: "pi", RequestedModel: "haiku", Model: "claude-haiku-4-5", TotalCostUSD: 0.42}, "medium")

	var out strings.Builder
	emitRunInfoNotice(&out, false, info)
	assert.Empty(t, out.String(), "no annotation outside CI")

	emitRunInfoNotice(&out, true, info)
	assert.Equal(t, "::notice::Runtime: pi · Model: haiku → claude-haiku-4-5 · Effort: medium · Cost: $0.42\n", out.String())

	out.Reset()
	emitRunInfoNotice(&out, true, runInfoFor(aggregateMetrics{}, ""))
	assert.Empty(t, out.String(), "nothing known, nothing emitted")
}
