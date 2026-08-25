package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
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

	backend, source, err := resolveBackend(runOverrides{}, cfg, "")
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name())
	assert.Equal(t, cfg, source)

	backend, source, err = resolveBackend(runOverrides{runtime: "pi", runtimeSource: sourceFlagRuntime}, cfg, "")
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

func TestResolveBackend_PerAgentRuntime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(`# fullsend per-repo configuration
version: "1"
runtime: pi
roles:
  - triage
  - coder
agents:
  - name: code
    runtime: claude
`), 0o644))

	// Without agent name: repo-wide runtime applies.
	backend, source, err := resolveBackend(runOverrides{}, cfg, "")
	require.NoError(t, err)
	assert.Equal(t, "pi", backend.Runtime.Name())
	assert.Equal(t, cfg, source)

	// The agents: entry decides for "code", and the source names it.
	backend, source, err = resolveBackend(runOverrides{}, cfg, "code")
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name())
	assert.Equal(t, cfg+" agents.code", source)

	// No entry for "triage": repo-wide applies.
	backend, _, err = resolveBackend(runOverrides{}, cfg, "triage")
	require.NoError(t, err)
	assert.Equal(t, "pi", backend.Runtime.Name())

	// Flag override still wins over the entry.
	backend, source, err = resolveBackend(runOverrides{runtime: "dummy", runtimeSource: sourceFlagRuntime}, cfg, "code")
	require.NoError(t, err)
	assert.Equal(t, "dummy", backend.Runtime.Name())
	assert.Equal(t, sourceFlagRuntime, source)

	// Org configs honour agents: entries too.
	orgData := []byte(`# fullsend organization configuration
version: "1"
dispatch:
  platform: github
defaults:
  roles: [triage]
  runtime: dummy
repos: {}
agents:
  - name: triage
    runtime: claude
`)
	backend, err = resolveBackendFromConfigData(orgData, "triage")
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name())
	backend, err = resolveBackendFromConfigData(orgData, "code")
	require.NoError(t, err)
	assert.Equal(t, "dummy", backend.Runtime.Name())
}

func TestResolveBackend_PerAgentRuntimeRejectsStub(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: code
    runtime: opencode
`), 0o644))

	// `fullsend run` never calls Validate() on the config it loads, so the
	// per-agent runtime must be checked against ValidRuntimes here — a stub
	// runtime cannot be activated through an agents: entry any more than
	// through the repo-wide key.
	_, _, err := resolveBackend(runOverrides{}, cfg, "code")
	require.Error(t, err)
	assert.ErrorIs(t, err, errResolvingRuntime)
	assert.Contains(t, err.Error(), "agents.code")
	assert.Contains(t, err.Error(), `invalid runtime "opencode"`)

	backend, _, err := resolveBackend(runOverrides{}, cfg, "triage")
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name(), "other agents unaffected")
}

func TestRunConfig_AgentSettings_LayeredAndValidated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
runtime: claude
agents:
  - name: triage
    runtime: pi
    model: opus
`), 0o644))
	cfg := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: Triage
    model: haiku
`), 0o644))

	rc, err := loadRunConfig(cfg)
	require.NoError(t, err)

	// Runtime selection reads the layered config (overlay over base, ADR
	// 0069): the base entry's runtime applies, labelled through the
	// effective config file.
	backend, source, err := rc.backend("triage")
	require.NoError(t, err)
	assert.Equal(t, "pi", backend.Runtime.Name())
	assert.Equal(t, cfg+" agents.triage", source)

	// Settings merge per field across layers; lookup is case-insensitive.
	entry, found, err := rc.agentSettings("triage")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "haiku", entry.Model, "overlay wins per field")
	assert.Equal(t, "pi", entry.Runtime, "base value inherited")

	// No entry: nothing to apply.
	_, found, err = rc.agentSettings("code")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestRunConfig_AgentSettings_RejectsBadEntriesOnRunPath(t *testing.T) {
	t.Parallel()
	// `fullsend run` never calls Validate(); a mistyped entry must still
	// fail the run for every agent rather than silently no-op — in the
	// overlay and in config.base.yaml alike.
	for _, layout := range []struct{ name, base, overlay string }{
		{"overlay", "", "agents:\n  - name: coder\n    model: sonnet\n"},
		{"base with overlay", "agents:\n  - name: coder\n    model: sonnet\n", ""},
		{"base only", "agents:\n  - name: coder\n    model: sonnet\n", "<none>"},
		{"bad value", "", "agents:\n  - name: code\n    effort: turbo\n"},
	} {
		t.Run(layout.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if layout.base != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("# fullsend per-repo configuration\nversion: \"1\"\n"+layout.base), 0o644))
			}
			if layout.overlay != "<none>" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("# fullsend per-repo configuration\nversion: \"1\"\n"+layout.overlay), 0o644))
			}
			rc, err := loadRunConfig(filepath.Join(dir, "config.yaml"))
			require.NoError(t, err)
			require.NotNil(t, rc.perRepo)
			_, _, err = rc.agentSettings("triage")
			require.Error(t, err)
			assert.Contains(t, err.Error(), rc.source)
			if layout.name == "bad value" {
				assert.Contains(t, err.Error(), `invalid effort "turbo"`)
			} else {
				assert.Contains(t, err.Error(), `did you mean "code"`)
			}
		})
	}
}

func TestLoadRunConfig_NestedAndBaseOnly(t *testing.T) {
	t.Parallel()
	// The config may live at the nested .fullsend/config.yaml, or as a
	// config.base.yaml without an overlay; runtime selection and agent
	// settings must both find it.
	for _, layout := range []struct {
		name string
		dir  func(string) string
		file string
	}{
		{"nested overlay", func(d string) string { return filepath.Join(d, ".fullsend") }, "config.yaml"},
		{"base only", func(d string) string { return d }, "config.base.yaml"},
		{"nested base only", func(d string) string { return filepath.Join(d, ".fullsend") }, "config.base.yaml"},
	} {
		t.Run(layout.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			dir := layout.dir(root)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			path := filepath.Join(dir, layout.file)
			require.NoError(t, os.WriteFile(path, []byte(`# fullsend per-repo configuration
version: "1"
runtime: pi
agents:
  - name: code
    runtime: claude
    model: sonnet
`), 0o644))
			rc, err := loadRunConfig(filepath.Join(root, "config.yaml"))
			require.NoError(t, err)
			assert.Equal(t, path, rc.source)
			backend, source, err := rc.backend("code")
			require.NoError(t, err)
			assert.Equal(t, "claude", backend.Runtime.Name())
			assert.Equal(t, path+" agents.code", source)
			entry, found, err := rc.agentSettings("code")
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, "sonnet", entry.Model)
		})
	}
	// Missing file: defaults, no settings, no error.
	none, err := loadRunConfig(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	_, found, err := none.agentSettings("code")
	require.NoError(t, err)
	assert.False(t, found)
	backend, source, err := none.backend("code")
	require.NoError(t, err)
	assert.Equal(t, "claude", backend.Runtime.Name())
	assert.Equal(t, "default (config not found)", source)
}

func TestApplyAgentSettings(t *testing.T) {
	t.Parallel()
	const path = "/repo/.fullsend/config.yaml"

	h := &harness.Harness{Model: "opus"}
	o := runOverrides{}
	applyAgentSettings(h, &o, config.AgentEntry{Name: "triage", Model: "xai-vertex/xai/grok-4.6", Effort: "medium"}, "triage", path)
	assert.Equal(t, "xai-vertex/xai/grok-4.6", h.Model)
	assert.Equal(t, path+" agents.triage", o.modelSource)
	assert.Equal(t, "medium", h.Effort)
	assert.Equal(t, path+" agents.triage", o.effortSource)
	assert.Equal(t, path+" agents.triage", modelOverrideSource(o, h.Model))

	// Flag/env keep precedence; the caller applies o.model/o.effort next.
	h = &harness.Harness{Model: "opus", Effort: "high"}
	o = runOverrides{model: "sonnet", modelSource: sourceFlagModel, effort: "low", effortSource: envEffort}
	applyAgentSettings(h, &o, config.AgentEntry{Name: "code", Model: "haiku", Effort: "max"}, "code", path)
	assert.Equal(t, "opus", h.Model)
	assert.Equal(t, "high", h.Effort)
	assert.Equal(t, sourceFlagModel, o.modelSource)
	assert.Equal(t, envEffort, o.effortSource)

	// Runtime-only entry leaves model and effort alone.
	h = &harness.Harness{Model: "opus"}
	o = runOverrides{}
	applyAgentSettings(h, &o, config.AgentEntry{Name: "code", Runtime: "pi"}, "code", path)
	assert.Equal(t, "opus", h.Model)
	assert.Empty(t, o.modelSource)
	assert.Equal(t, sourceHarness, modelOverrideSource(o, h.Model))
}

func TestResolveBackend_ConfigReadErrorSurfaces(t *testing.T) {
	t.Parallel()
	// A config path that exists but cannot be read as a file (a directory)
	// is an error, not "config not found".
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "config.yaml"), 0o755))
	_, source, err := resolveBackend(runOverrides{}, filepath.Join(dir, "config.yaml"), "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config.yaml for runtime selection")
	assert.Equal(t, filepath.Join(dir, "config.yaml"), source)

	// The flag path never touches the file.
	backend, source, err := resolveBackendFrom(runOverrides{runtime: "nope", runtimeSource: sourceFlagRuntime}, runConfig{}, "triage")
	require.Error(t, err)
	assert.Equal(t, sourceFlagRuntime, source)
	assert.Empty(t, backend.Runtime)
}
