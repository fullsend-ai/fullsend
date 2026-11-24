package harnessdispatch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// builtinStub resolves built-in agents to local harness files with the
// given triggers (a built-in missing from triggers gets no trigger) and
// records each name it is asked for. A name in unavailable fails to
// resolve.
type builtinStub struct {
	t           *testing.T
	dir         string
	triggers    map[string]string
	unavailable []string
	requested   []string
}

func newBuiltinStub(t *testing.T, triggers map[string]string, unavailable ...string) *builtinStub {
	return &builtinStub{t: t, dir: t.TempDir(), triggers: triggers, unavailable: unavailable}
}

func (s *builtinStub) resolve(_ context.Context, name string, opts harness.ComposeOpts) (string, harness.ComposeOpts, bool) {
	s.requested = append(s.requested, name)
	if slices.Contains(s.unavailable, name) {
		return "", opts, false
	}
	body := "agent: agents/" + name + ".md\nrole: " + name + "\nslug: fullsend-ai-" + name +
		"\nmodel: opus\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\n"
	if trig := s.triggers[name]; trig != "" {
		body += "trigger: " + trig + "\n"
	}
	path := filepath.Join(s.dir, name+".yaml")
	require.NoError(s.t, os.WriteFile(path, []byte(body), 0o644))
	return path, opts, true
}

func loadYAMLConfig(t *testing.T, dir, yamlText string) config.ConfigReader {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlText), 0o644))
	cfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: true})
	require.NoError(t, err)
	return cfg
}

func candidateNames(c []TriggeredHarness) []string {
	var names []string
	for _, h := range c {
		names = append(names, h.Name)
	}
	return names
}

const effectiveTestTrigger = `"event.source.system == 'jira'"`

func TestListEffectiveTriggeredHarnesses_NoAgentsListsBuiltins(t *testing.T) {
	dir := t.TempDir()
	cfg := loadYAMLConfig(t, dir, "version: \"1\"\n")
	stub := newBuiltinStub(t, map[string]string{"triage": effectiveTestTrigger, "code": effectiveTestTrigger})

	out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
	require.NoError(t, err)
	assert.Equal(t, []string{"triage", "code"}, candidateNames(out))
	assert.Equal(t, config.ValidAgentNames(), stub.requested, "without roles every built-in is considered once")

	// The GitHub dispatch path keeps listing only source-registered agents.
	gh, err := ListTriggeredHarnesses(context.Background(), dir, cfg, nil)
	require.NoError(t, err)
	assert.Empty(t, gh)
}

func TestListEffectiveTriggeredHarnesses_OverrideOnlyEntryAppliesBlocks(t *testing.T) {
	dir := t.TempDir()
	cfg := loadYAMLConfig(t, dir, `version: "1"
agents:
  - name: triage
    runtime: claude
  - name: code
    harness:
      model: sonnet
`)
	stub := newBuiltinStub(t, map[string]string{"triage": effectiveTestTrigger, "code": effectiveTestTrigger})

	out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
	require.NoError(t, err)
	require.Equal(t, []string{"triage", "code"}, candidateNames(out))
	assert.Equal(t, "opus", out[0].Harness.Model)
	assert.Equal(t, "sonnet", out[1].Harness.Model, "the entry's harness: block composes on the built-in harness")
}

func TestListEffectiveTriggeredHarnesses_SourceEntryReplacesBuiltin(t *testing.T) {
	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	custom := "agent: agents/x.md\nrole: coder\nslug: custom\nmodel: haiku\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\ntrigger: " + effectiveTestTrigger + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "code.yaml"), []byte(custom), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "extra.yaml"), []byte(custom), 0o644))
	cfg := loadYAMLConfig(t, dir, `version: "1"
agents:
  - name: Code
    source: harness/code.yaml
  - name: extra
    source: harness/extra.yaml
`)
	stub := newBuiltinStub(t, map[string]string{"triage": effectiveTestTrigger, "code": effectiveTestTrigger})

	out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
	require.NoError(t, err)
	assert.Equal(t, []string{"Code", "extra", "triage"}, candidateNames(out))
	assert.NotContains(t, stub.requested, "code", "a source entry replaces the built-in, case-insensitively")
	assert.Equal(t, "haiku", out[0].Harness.Model)
}

func TestListEffectiveTriggeredHarnesses_DisabledAgents(t *testing.T) {
	t.Run("disabled built-in", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nagents:\n  - name: code\n    enabled: false\n")
		stub := newBuiltinStub(t, map[string]string{"triage": effectiveTestTrigger, "code": effectiveTestTrigger})
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Equal(t, []string{"triage"}, candidateNames(out))
		assert.NotContains(t, stub.requested, "code")
	})

	t.Run("source entry disabled by a later entry", func(t *testing.T) {
		dir := t.TempDir()
		harnessDir := filepath.Join(dir, "harness")
		require.NoError(t, os.MkdirAll(harnessDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "code.yaml"), []byte("agent: agents/x.md\nrole: coder\nslug: custom\nmodel: haiku\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\ntrigger: "+effectiveTestTrigger+"\n"), 0o644))
		cfg := loadYAMLConfig(t, dir, `version: "1"
agents:
  - name: code
    source: harness/code.yaml
  - name: code
    enabled: false
`)
		stub := newBuiltinStub(t, map[string]string{"code": effectiveTestTrigger})
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.NotContains(t, stub.requested, "code")
	})

	t.Run("every built-in disabled", func(t *testing.T) {
		dir := t.TempDir()
		yamlText := "version: \"1\"\nagents:\n"
		for _, name := range config.ValidAgentNames() {
			yamlText += "  - name: " + name + "\n    enabled: false\n"
		}
		cfg := loadYAMLConfig(t, dir, yamlText)
		stub := newBuiltinStub(t, map[string]string{"triage": effectiveTestTrigger})
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.Empty(t, stub.requested, "no disabled built-in is resolved")
	})
}

func TestListEffectiveTriggeredHarnesses_ResolveFailures(t *testing.T) {
	t.Run("partial failure is annotated and an error", func(t *testing.T) {
		buf := captureAnnotations(t)
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\n")
		stub := newBuiltinStub(t, map[string]string{"triage": effectiveTestTrigger, "code": effectiveTestTrigger}, "code")
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.Error(t, err)
		assert.Nil(t, out, "an incomplete list is never returned")
		assert.Contains(t, err.Error(), "some agents could not be loaded")
		assert.Contains(t, err.Error(), "built-in agent code: harness could not be resolved")
		assert.Contains(t, buf.String(), "::error::harness dispatch: skipping built-in agent code: its harness could not be resolved")
	})

	t.Run("total failure is an error", func(t *testing.T) {
		captureAnnotations(t)
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\n")
		stub := newBuiltinStub(t, nil, config.ValidAgentNames()...)
		_, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agents could be loaded")
		assert.Contains(t, err.Error(), "built-in agent triage: harness could not be resolved")
	})

	t.Run("built-in load failure is annotated", func(t *testing.T) {
		buf := captureAnnotations(t)
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\n")
		stub := newBuiltinStub(t, map[string]string{"triage": effectiveTestTrigger})
		resolve := func(ctx context.Context, name string, opts harness.ComposeOpts) (string, harness.ComposeOpts, bool) {
			if name == "code" {
				return filepath.Join(dir, "missing.yaml"), opts, true
			}
			return stub.resolve(ctx, name, opts)
		}
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, resolve)
		require.Error(t, err)
		assert.Nil(t, out)
		assert.Contains(t, err.Error(), "some agents could not be loaded")
		assert.Contains(t, buf.String(), "::error::harness dispatch: skipping agent code: load failed")
	})
}

func TestListEffectiveTriggeredHarnesses_RequiresResolver(t *testing.T) {
	dir := t.TempDir()
	cfg := loadYAMLConfig(t, dir, "version: \"1\"\n")
	_, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, nil)
	require.Error(t, err)
}

func TestListEffectiveTriggeredHarnesses_Roles(t *testing.T) {
	triggers := map[string]string{"triage": effectiveTestTrigger, "code": effectiveTestTrigger}

	t.Run("roles triage selects triage but not code", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nroles: [triage]\n")
		stub := newBuiltinStub(t, triggers)
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Equal(t, []string{"triage"}, candidateNames(out))
		assert.Equal(t, []string{"triage"}, stub.requested, "an unselected built-in is never resolved")
	})

	t.Run("empty roles selects and fetches no built-in", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nroles: []\n")
		stub := newBuiltinStub(t, triggers)
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.Empty(t, stub.requested, "no built-in is resolved when roles is explicitly empty")
	})

	t.Run("overlay roles [] over a base that selects roles selects no built-in", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("version: \"1\"\nroles: [triage, coder]\n"), 0o644))
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nroles: []\n")
		stub := newBuiltinStub(t, triggers)
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.Empty(t, stub.requested)
	})

	t.Run("roles coder selects code", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nroles: [coder]\n")
		stub := newBuiltinStub(t, triggers)
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Equal(t, []string{"code"}, candidateNames(out))
		assert.Equal(t, []string{"code", "fix"}, stub.requested, "coder also selects fix, which reuses the coder app")
	})

	t.Run("an unselected built-in that cannot load does not fail the poll", func(t *testing.T) {
		captureAnnotations(t)
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nroles: [triage]\n")
		stub := newBuiltinStub(t, triggers, "code", "review", "fix", "retro", "prioritize")
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Equal(t, []string{"triage"}, candidateNames(out))
		assert.NotContains(t, stub.requested, "code")
	})

	t.Run("a selected built-in that cannot load still fails the poll", func(t *testing.T) {
		captureAnnotations(t)
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nroles: [triage, coder]\n")
		stub := newBuiltinStub(t, triggers, "code")
		_, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "built-in agent code: harness could not be resolved")
	})

	t.Run("an explicitly disabled agent stays disabled", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadYAMLConfig(t, dir, "version: \"1\"\nroles: [triage, coder]\nagents:\n  - name: code\n    enabled: false\n")
		stub := newBuiltinStub(t, triggers)
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Equal(t, []string{"triage"}, candidateNames(out))
		assert.NotContains(t, stub.requested, "code")
	})

	t.Run("custom-source agents are not scoped by roles", func(t *testing.T) {
		dir := t.TempDir()
		harnessDir := filepath.Join(dir, "harness")
		require.NoError(t, os.MkdirAll(harnessDir, 0o755))
		custom := "agent: agents/x.md\nrole: coder\nslug: custom\nmodel: haiku\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\ntrigger: " + effectiveTestTrigger + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "code.yaml"), []byte(custom), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "extra.yaml"), []byte(custom), 0o644))
		cfg := loadYAMLConfig(t, dir, `version: "1"
roles: [triage]
agents:
  - name: code
    source: harness/code.yaml
  - name: extra
    source: harness/extra.yaml
`)
		stub := newBuiltinStub(t, triggers)
		out, err := ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, nil, stub.resolve)
		require.NoError(t, err)
		assert.Equal(t, []string{"code", "extra", "triage"}, candidateNames(out))
		assert.Equal(t, []string{"triage"}, stub.requested)
	})
}
