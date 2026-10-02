package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// writeValidLocalHarness writes a minimal structurally-valid local harness
// (code.yaml) plus the agent.md file it references into dir/harness and
// dir/agents.
func writeValidLocalHarness(t *testing.T, dir, name, extra string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", name+".md"), []byte("You are a test agent."), 0o644))

	content := "agent: agents/" + name + ".md\nrole: test\n" + extra
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", name+".yaml"), []byte(content), 0o644))
}

func TestRunLint_ValidHarnessNoConfig(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "config.yaml: not found")
	assert.Contains(t, buf.String(), "0 error(s)")
}

func TestRunLint_StructuralErrorFailsAlways(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	// Missing the required `role` field — Validate() rejects this.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "broken.yaml"),
		[]byte("agent: agents/broken.md\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structural error")
}

func TestRunLint_MissingAgentFileIsStructuralError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/does-not-exist.md\nrole: test\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
}

func TestRunLint_DeprecationWarningDoesNotFailByDefault(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "runner_env:\n  FOO: bar\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "runner_env")
	assert.Contains(t, buf.String(), "1 warning(s)")
}

func TestRunLint_StrictFailsOnDeprecationWarning(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "runner_env:\n  FOO: bar\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", true, false, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deprecation warning")
}

func TestRunLint_ImplicitRuntimeFetchWarning(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "allowed_remote_resources:\n  - \"https://github.com/org/\"\n")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("allowed_remote_resources:\n  - \"https://github.com/org/\"\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "allow_runtime_fetch")
}

func TestRunLint_PerOrgModeDeprecationWarning(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("version: \"1\"\ndispatch:\n  platform: github-actions\nrepos:\n  repo-x:\n    enabled: true\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "per-org installation mode")
	assert.Contains(t, buf.String(), "ADR 0044")
}

func TestRunLint_InvalidConfigIsStructuralError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("not: [valid, yaml, mapping\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
}

func TestRunLint_InvalidForgeFlag(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "not-a-forge", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid forge platform")
}

func TestRunLint_NoHarnessesIsNotAnError(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No harness files found")
}

func TestRunLint_MultipleForgeVariantsDedupeDiagnostics(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "runner_env:\n  FOO: bar\nforge:\n  github: {}\n  gitlab: {}\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "OK (2 forge variants)")
	// The runner_env warning is identical across both forge variants and
	// must be deduplicated, not reported twice.
	assert.Equal(t, 2, strings.Count(out, "runner_env"))
}

func TestRunLint_InvalidForgeKeyIsStructuralError(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "forge:\n  bogus: {}\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "unrecognized key")
}

func TestRunLint_URLSkillNotCoveredByOrgAllowlist(t *testing.T) {
	dir := t.TempDir()
	skillURL := "https://github.com/org/skills/tree/abc/rust#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	writeValidLocalHarness(t, dir, "code", "allowed_remote_resources:\n  - \"https://github.com/org/\"\nskills:\n  - \""+skillURL+"\"\n")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("allowed_remote_resources:\n  - \"https://github.com/other-org/\"\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "not covered by the org allowlist")
}

func TestRunLint_ConfigValidateFailure(t *testing.T) {
	dir := t.TempDir()
	// Org-mode config (has a `dispatch` key) with an unsupported version —
	// parses fine as YAML but fails orgConfig.Validate().
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("version: \"2\"\ndispatch:\n  platform: github-actions\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "unsupported version")
}
