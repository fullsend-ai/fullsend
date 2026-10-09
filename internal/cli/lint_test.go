package cli

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/resolve"
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

func TestRunLint_RejectsSymlinkedHarnessOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte("agent: agents/x.md\nrole: test\n"), 0o644))
	if err := os.Symlink(outside, filepath.Join(dir, "harness", "evil.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "outside workspace root")
}

func TestCheckLocalHarnessFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	require.NoError(t, os.WriteFile(good, []byte("agent: a\n"), 0o644))
	assert.NoError(t, checkLocalHarnessFile(good, dir))

	assert.Error(t, checkLocalHarnessFile(dir, dir), "directory is not a regular file")

	big := filepath.Join(dir, "big.yaml")
	require.NoError(t, os.WriteFile(big, bytes.Repeat([]byte("a"), resolve.MaxLocalResourceBytes+1), 0o644))
	assert.Error(t, checkLocalHarnessFile(big, dir), "oversized file")
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

func TestRunLint_ForgeOnlyHarnessFailsStrict(t *testing.T) {
	// Regression test: LoadWithBase must preserve hadForgeBeforeResolve (the
	// signal Harness.Lint() uses for the "forge" deprecation warning) so a
	// harness that only uses the deprecated `forge:` field cannot pass
	// `lint --strict` silently.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "forge:\n  github: {}\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", true, false, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deprecation warning")
	assert.Contains(t, buf.String(), "forge is deprecated")
}

func TestRunLint_SanitizesInjectedEnvKeyInDiagnostic(t *testing.T) {
	// Adversarial test: fullsend lint runs as a CI gate over PR branches, so
	// an env.runner map key is attacker-controlled. A key carrying a decoded
	// newline followed by GitHub Actions workflow-command syntax must not
	// reach stdout intact, or it could inject a spurious log line.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "env:\n  runner:\n    \"FOO\\n::error::pwned\": \"GITHUB_ISSUE_URL\"\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	out := buf.String()
	assert.NotContains(t, out, "\n::")
	assert.NotContains(t, out, "::error::")
	assert.Contains(t, out, "FOO")
}

func TestRunLint_SanitizesInjectedHostFileSrcInDiagnostic(t *testing.T) {
	// Adversarial test: the env.sandbox/host_files overlap diagnostic embeds
	// host_files[].src and .dest verbatim (internal/harness/lint.go), and
	// both are attacker-controlled harness content. A src path carrying
	// embedded workflow-command syntax must be neutralized before reaching
	// stdout.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"env:\n  sandbox:\n    FOO: bar\n"+
			"host_files:\n  - src: \"GITHUB_ISSUE_URL\\n::error::pwned\"\n    dest: \".env.d/secrets\"\n    expand: true\n    optional: true\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "env.sandbox coexists")
	assert.NotContains(t, out, "\n::")
	assert.NotContains(t, out, "::error::")
}

func TestRunLint_ConfigValidateFailure(t *testing.T) {
	dir := t.TempDir()
	// Unsupported version — parses fine as YAML but fails Validate().
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("version: \"2\"\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "unsupported version")
}

func TestRunLint_BaseOnlyConfigMalformedYAMLIsStructuralError(t *testing.T) {
	// A standalone config.base.yaml is a supported configuration; a missing
	// config.yaml must not let a malformed base layer pass the CI gate.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.base.yaml"),
		[]byte("not: [valid, yaml, mapping\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.NotContains(t, buf.String(), "config.yaml: not found")
}

func TestRunLint_BaseOnlyConfigIsLoaded(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.base.yaml"),
		[]byte("version: \"1\"\n"),
		0o644,
	))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "config.base.yaml: valid")
	assert.NotContains(t, buf.String(), "config.yaml: not found")
}

func TestRunLint_ConditionedOverlayMissingScriptIsStructuralError(t *testing.T) {
	// An overlay-only harness must be linted under a concrete forge platform;
	// otherwise forge-conditioned overlays are discarded before file checks
	// and a missing script passes lint.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"overlays:\n- when: 'runtime.forge == \"github\"'\n  pre_script: scripts/missing.sh\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runLint(context.Background(), dir, "", false, false, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structural error")
}

func TestRunLint_SanitizesAdversarialDuplicateHarnessFilenames(t *testing.T) {
	// Both <name>.yaml and <name>.yml exist, which makes resolveHarnessPath
	// warn with the raw agent name. A filename carrying a newline plus an
	// Actions workflow command must not reach stdout intact.
	dir := t.TempDir()
	name := "evil\n::error::pwned"
	writeValidLocalHarness(t, dir, "ok", "")
	content, err := os.ReadFile(filepath.Join(dir, "harness", "ok.yaml"))
	require.NoError(t, err)
	for _, ext := range []string{".yaml", ".yml"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", name+ext), content, 0o644))
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)
	_ = runLint(context.Background(), dir, "", false, false, printer)
	out := buf.String()
	assert.Contains(t, out, "exist; using .yaml")
	assert.NotContains(t, out, "\n::")
	assert.NotContains(t, out, "::error::")
}

func TestRunLint_BaseOnlyConfigInvalidVersionIsStructuralError(t *testing.T) {
	// The overlay returned by the layered loader has no local version, so
	// validating only it would let an invalid base-layer version pass.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("version: \"2\"\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "unsupported version")
}

func TestRunLint_InheritedBaseConfigInvalidRuntimeIsStructuralError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("runtime: nonexistent\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"1\"\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "config.base.yaml")
}

func TestRunLint_RegisteredSourceCollidingWithLocalHarnessIsValidated(t *testing.T) {
	// runAgent resolves config registrations first, so registering name
	// "code" with a source that does not exist must fail lint even though a
	// valid harness/code.yaml is present.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - name: code\n    source: custom/code.yaml\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "config-registered")
}

func TestRunLint_RegisteredSourcePointingAtLocalHarnessIsNotCheckedTwice(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - name: code\n    source: harness/code.yaml\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Checked 1 harness(es)")
}

func TestRunLint_EventConditionedOverlayMissingScriptIsStructuralError(t *testing.T) {
	// The overlay's when expression needs event fields, so an empty event
	// never selects it; its files must still be checked.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"overlays:\n- when: 'has(event.entity) && event.entity.kind == \"work_item\"'\n  pre_script: scripts/missing.sh\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structural error")
	assert.Contains(t, buf.String(), "overlay 0")
}

func TestRunLint_EventConditionedOverlayDeprecatedReferenceFailsStrict(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"overlays:\n- when: 'has(event.entity) && event.entity.kind == \"work_item\"'\n  env:\n    runner:\n      ISSUE_URL: ${GITHUB_ISSUE_URL}\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deprecation warning")
	assert.Contains(t, buf.String(), "GITHUB_ISSUE_URL")
}

func TestRunLint_MalformedAllowlistOnLocalRuntimeFetchHarness(t *testing.T) {
	// No static URL references, but allowed_remote_resources still governs
	// runtime fetching, so a malformed prefix must be rejected.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"allow_runtime_fetch: true\nallowed_remote_resources:\n  - not-a-url\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "not a valid HTTPS URL")
}

func TestRunLint_UncoveredAllowlistOnLocalRuntimeFetchHarness(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"allow_runtime_fetch: true\nallowed_remote_resources:\n  - \"https://example.com/\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("allowed_remote_resources:\n  - \"https://github.com/org/\"\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "not covered by the org allowlist")
}

func TestRunLint_AdversarialOverlayExpressionDoesNotInjectIntoLoggerOrPrinter(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"overlays:\n- when: 'event[\"evil\\n::error::pwned\"] == \"x\"'\n  pre_script: scripts/missing.sh\n")

	var buf bytes.Buffer
	_ = runLint(context.Background(), dir, "", false, false, ui.New(&buf))

	for name, out := range map[string]string{"logger": logBuf.String(), "printer": buf.String()} {
		// Workflow commands only take effect at the start of a line, so
		// the text may survive mid-line as long as no raw newline precedes it.
		assert.NotContains(t, out, "\n::", name)
		assert.NotContains(t, out, "\x1b", name)
	}
	assert.Contains(t, logBuf.String(), `\n::error::pwned`, "logger must escape the newline")
}

func TestRunLint_InheritedEventConditionedOverlayInSeparateBaseIsChecked(t *testing.T) {
	// The overlay lives in a local base outside harness/, not in the child, so
	// it must be enumerated across the whole base chain.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"),
		[]byte("overlays:\n- when: 'has(event.entity) && event.entity.kind == \"work_item\"'\n  pre_script: scripts/missing.sh\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structural error")
	assert.Contains(t, buf.String(), "overlay 0")
}

func TestRunLint_EventOverlayNotMaskedByEmptyEventFallbackOverlay(t *testing.T) {
	// A later overlay matching the empty event overwrites pre_script with an
	// existing file; the event-conditioned overlay selecting the missing
	// script must still be inspected in isolation.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "ok.sh"), []byte("#!/bin/sh\n"), 0o755))
	writeValidLocalHarness(t, dir, "code",
		"overlays:\n"+
			"- when: 'has(event.entity) && event.entity.kind == \"work_item\"'\n  pre_script: scripts/missing.sh\n"+
			"- when: 'true'\n  pre_script: scripts/ok.sh\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structural error")
	assert.Contains(t, buf.String(), "overlay 0")
}

func TestRunLint_EventOverlayDeprecatedReferenceNotMaskedByFallbackOverlay(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"overlays:\n"+
			"- when: 'has(event.entity) && event.entity.kind == \"work_item\"'\n  env:\n    runner:\n      ISSUE_URL: ${GITHUB_ISSUE_URL}\n"+
			"- when: 'true'\n  env:\n    runner:\n      ISSUE_URL: ${FULLSEND_WORK_ITEM_URL}\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deprecation warning")
	assert.Contains(t, buf.String(), "GITHUB_ISSUE_URL")
}

func TestRunLint_BaseURLAgentAuthorizedByOverlayAllowlist(t *testing.T) {
	// config.base.yaml registers a URL agent that only config.yaml's
	// allowed_remote_resources authorizes; the base must be validated as a
	// layer, not as a standalone config.
	dir := t.TempDir()
	url := "https://github.com/org/repo/blob/main/agents/x.yaml#sha256=" + strings.Repeat("a", 64)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"),
		[]byte("agents:\n  - name: x\n    source: \""+url+"\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("allowed_remote_resources:\n  - \"https://github.com/org/\"\n"), 0o644))

	var buf bytes.Buffer
	// Offline: lint must not hit the network; only the config check matters.
	_ = runLint(context.Background(), dir, "", false, true, ui.New(&buf))
	assert.NotContains(t, buf.String(), "config.base.yaml:")
	assert.Contains(t, buf.String(), "config.yaml: valid")
}

func TestRunLint_InvalidConfigForgeIsStructuralError(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"overlay":          {"config.yaml": "forge: nonsense\n"},
		"base only":        {"config.base.yaml": "forge: nonsense\n"},
		"base via overlay": {"config.base.yaml": "forge: nonsense\n", "config.yaml": "version: \"1\"\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for f, c := range files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644))
			}
			var buf bytes.Buffer
			err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
			require.Error(t, err)
			assert.Contains(t, buf.String(), "not a valid forge platform")
		})
	}
}

func TestRunLint_InvalidForgeWithMalformedAllowlistNeverFetches(t *testing.T) {
	// A bad forge must not hide a malformed allowlist: composition must still
	// run with a deny-all allowlist rather than fall back to the default.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("forge: nonsense\nallowed_remote_resources:\n  - not-a-url\n"), 0o644))
	base := "https://github.com/fullsend-ai/fullsend/blob/main/harness/base.yaml#sha256=" + strings.Repeat("b", 64)
	writeValidLocalHarness(t, dir, "code", "base: \""+base+"\"\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "not a valid forge platform")
	assert.Contains(t, buf.String(), "URL base requires config-level allowed_remote_resources")
	assert.NoDirExists(t, filepath.Join(dir, ".fullsend-cache"))
}

func TestRunLint_MalformedConfigNeverFetches(t *testing.T) {
	// A malformed config must not fall back to the default allowlist: the URL
	// base below is under a default-allowed prefix, but composition must run
	// with a deny-all allowlist rather than attempt a download.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("allowed_remote_resources: [unterminated\n"), 0o644))
	base := "https://github.com/fullsend-ai/fullsend/blob/main/harness/base.yaml#sha256=" + strings.Repeat("b", 64)
	writeValidLocalHarness(t, dir, "code", "base: \""+base+"\"\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "URL base requires config-level allowed_remote_resources")
	assert.NoDirExists(t, filepath.Join(dir, ".fullsend-cache"))
}

func TestRunLint_UnauthorizedURLReferenceUnderDenyAllConfig(t *testing.T) {
	dir := t.TempDir()
	skill := "https://github.com/org/repo/tree/main/skills/s#sha256=" + strings.Repeat("c", 64)
	writeValidLocalHarness(t, dir, "code", "skills:\n  - \""+skill+"\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("allowed_remote_resources: []\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "not in allowed_remote_resources")
}

func TestLintGitToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok-from-env")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	assert.Equal(t, "tok-from-env", lintGitToken("", true, printer), "resolved from environment")
	assert.Equal(t, "explicit", lintGitToken("explicit", true, printer), "explicit token wins")
	assert.Equal(t, "", lintGitToken("", false, printer), "nothing resolved when offline")
}

func TestRunLint_PartialBaseInHarnessDirIsNotStandalone(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "base: common.yaml\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "common.yaml"), []byte("model: opus\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.NoError(t, err, buf.String())
	assert.Contains(t, buf.String(), "Checked 1 harness(es)")
}

func TestRunLint_UnreferencedPartialHarnessStillFails(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "common.yaml"), []byte("model: opus\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
}

func TestRunLint_URLBaseWithoutConfigIsStructuralError(t *testing.T) {
	// The URL is under a default-allowed prefix, but with no config layer
	// lint must not fetch it, matching run and lock.
	dir := t.TempDir()
	base := "https://github.com/fullsend-ai/fullsend/blob/main/harness/base.yaml#sha256=" + strings.Repeat("b", 64)
	writeValidLocalHarness(t, dir, "code", "base: \""+base+"\"\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "URL base requires config.yaml or config.base.yaml")
	assert.NoDirExists(t, filepath.Join(dir, ".fullsend-cache"))
}

func TestRunLint_MalformedConfigAllowlistWithoutHarnesses(t *testing.T) {
	for _, file := range []string{"config.yaml", "config.base.yaml"} {
		t.Run(file, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("allowed_remote_resources:\n  - not-a-url\n"), 0o644))

			var buf bytes.Buffer
			err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
			require.Error(t, err)
			assert.Contains(t, buf.String(), "not a valid HTTPS URL")
		})
	}
}

func writeRawHarness(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", name), []byte(content), 0o644))
}

func TestRunLint_SelfReferencingPartialHarnessFails(t *testing.T) {
	dir := t.TempDir()
	writeRawHarness(t, dir, "loop.yaml", "base: loop.yaml\nmodel: opus\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "Checked 1 harness(es)")
}

func TestRunLint_MutuallyReferencingPartialHarnessesFail(t *testing.T) {
	dir := t.TempDir()
	writeRawHarness(t, dir, "a.yaml", "base: b.yaml\nmodel: opus\n")
	writeRawHarness(t, dir, "b.yaml", "base: a.yaml\nmodel: opus\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "Checked 2 harness(es)")
}

func TestRunLint_CyclicPartialHarnessesWithUnauthorizedURLFail(t *testing.T) {
	dir := t.TempDir()
	skill := "https://github.com/org/repo/tree/main/skills/s#sha256=" + strings.Repeat("c", 64)
	writeRawHarness(t, dir, "a.yaml", "base: b.yaml\nskills:\n  - \""+skill+"\"\n")
	writeRawHarness(t, dir, "b.yaml", "base: a.yaml\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("allowed_remote_resources: []\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "Checked 2 harness(es)")
}

func TestRunLint_PartialChainBelowRetainedTargetIsStillSuppressed(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "base: mid.yaml\n")
	writeRawHarness(t, dir, "mid.yaml", "base: common.yaml\nmodel: opus\n")
	writeRawHarness(t, dir, "common.yaml", "model: opus\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", true, false, ui.New(&buf))
	require.NoError(t, err, buf.String())
	assert.Contains(t, buf.String(), "Checked 1 harness(es)")
}

func TestRunLint_LocalBaseWithRemoteAncestorWithoutConfigNeverFetches(t *testing.T) {
	// The remote ancestor is under a default-allowed prefix, but with no
	// config layer composition must deny it before any fetch or cache write.
	dir := t.TempDir()
	remote := "https://github.com/fullsend-ai/fullsend/blob/main/harness/base.yaml#sha256=" + strings.Repeat("b", 64)
	writeValidLocalHarness(t, dir, "code", "base: mid.yaml\n")
	writeRawHarness(t, dir, "mid.yaml", "base: \""+remote+"\"\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "not in allowed_remote_resources")
	assert.NoDirExists(t, filepath.Join(dir, ".fullsend-cache"))
}

func TestRunLint_LocalProviderFileIsParsed(t *testing.T) {
	cases := map[string]string{
		"malformed YAML":    "name: [unterminated\n",
		"missing type":      "name: example\n",
		"missing name":      "type: generic\n",
		"invalid name char": "name: \"bad name\"\ntype: generic\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeValidLocalHarness(t, dir, "code", "providers:\n  - providers/p.yaml\n")
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "p.yaml"), []byte(content), 0o644))

			var buf bytes.Buffer
			err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
			require.Error(t, err, buf.String())
		})
	}

	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		writeValidLocalHarness(t, dir, "code", "providers:\n  - providers/p.yaml\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "p.yaml"), []byte("name: example\ntype: generic\n"), 0o644))

		var buf bytes.Buffer
		require.NoError(t, runLint(context.Background(), dir, "", false, false, ui.New(&buf)), buf.String())
	})
}

func TestRunLint_LocalProfileFileIsParsed(t *testing.T) {
	cases := map[string]string{
		"malformed YAML": "id: [unterminated\n",
		"missing id":     "description: no id\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeValidLocalHarness(t, dir, "code", "openshell:\n  profiles:\n    - profiles/p.yaml\n")
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "p.yaml"), []byte(content), 0o644))

			var buf bytes.Buffer
			err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
			require.Error(t, err, buf.String())
		})
	}

	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		writeValidLocalHarness(t, dir, "code", "openshell:\n  profiles:\n    - profiles/p.yaml\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "p.yaml"), []byte("id: my-profile\n"), 0o644))

		var buf bytes.Buffer
		require.NoError(t, runLint(context.Background(), dir, "", false, false, ui.New(&buf)), buf.String())
	})
}

func TestRunLint_BareProviderNameLocalDefinitionIsParsed(t *testing.T) {
	cases := map[string]string{
		"malformed YAML": "name: [unterminated\n",
		"missing type":   "name: custom\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeValidLocalHarness(t, dir, "code", "providers:\n  - custom\n")
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "custom.yaml"), []byte(content), 0o644))

			var buf bytes.Buffer
			err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
			require.Error(t, err, buf.String())
			assert.Contains(t, buf.String(), "custom.yaml")
		})
	}

	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		writeValidLocalHarness(t, dir, "code", "providers:\n  - custom\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "custom.yaml"), []byte("name: custom\ntype: generic\n"), 0o644))

		var buf bytes.Buffer
		require.NoError(t, runLint(context.Background(), dir, "", false, false, ui.New(&buf)), buf.String())
	})

	t.Run("no local definition is not an error", func(t *testing.T) {
		// run falls back to an embedded definition for an undefined name.
		dir := t.TempDir()
		writeValidLocalHarness(t, dir, "code", "providers:\n  - custom\n")

		var buf bytes.Buffer
		require.NoError(t, runLint(context.Background(), dir, "", false, false, ui.New(&buf)), buf.String())
	})
}

func TestRunLint_ProviderPathOutsideWorkspaceIsRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "p.yaml")
	require.NoError(t, os.WriteFile(secret, []byte("name: example\ntype: generic\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o755))
	require.NoError(t, os.Symlink(secret, filepath.Join(dir, "providers", "p.yaml")))
	writeValidLocalHarness(t, dir, "code", "providers:\n  - providers/p.yaml\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err, buf.String())
	assert.Contains(t, buf.String(), "outside workspace root")
}

func TestRunLint_DisabledRegisteredAgentIsNotLinted(t *testing.T) {
	// Last-writer-wins: a later enabled:false entry disables the agent, so the
	// (unresolvable) earlier registration must not be linted.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "other", "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - name: code\n    source: custom/code.yaml\n  - name: code\n    enabled: false\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.NoError(t, err, buf.String())
	assert.NotContains(t, buf.String(), "config-registered")
}

func TestRunLint_DisableThenEnableRegisteredAgentIsLinted(t *testing.T) {
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "other", "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - name: code\n    enabled: false\n  - name: code\n    source: custom/code.yaml\n"), 0o644))

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "config-registered")
}

func TestRunLint_ChildInheritsRequiredFieldFromUnconditionalBaseOverlay(t *testing.T) {
	// The base's unconditional overlay supplies validation_loop.script; the
	// child's event overlay overrides only max_iterations. Forcing the child
	// overlay must keep the base default, as normal composition does.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "validate.sh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"),
		[]byte("overlays:\n- when: 'true'\n  validation_loop:\n    script: scripts/validate.sh\n    max_iterations: 1\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\n"+
		"overlays:\n- when: 'has(event.entity) && event.entity.kind == \"work_item\"'\n  validation_loop:\n    max_iterations: 3\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.NoError(t, err, buf.String())
}

func TestRunLint_ChildInheritsRequiredFieldFromMatchingEventConditionedBaseOverlay(t *testing.T) {
	// The base and child overlays share the same event condition, so they
	// co-occur at runtime: the base supplies validation_loop.script and the
	// child overrides only max_iterations. Forcing the child overlay must
	// keep the base overlay.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "validate.sh"), []byte("#!/bin/sh\n"), 0o755))
	cond := `has(event.entity) && event.entity.kind == "work_item"`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"),
		[]byte("overlays:\n- when: '"+cond+"'\n  validation_loop:\n    script: scripts/validate.sh\n    max_iterations: 1\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\n"+
		"overlays:\n- when: '"+cond+"'\n  validation_loop:\n    max_iterations: 3\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.NoError(t, err, buf.String())
}

func TestRunLint_ConfigSymlinkedOutsideWorkspaceIsRejected(t *testing.T) {
	for _, name := range []string{"config.yaml", "config.base.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "secret.yaml")
			require.NoError(t, os.WriteFile(outside, []byte("SECRET-CONTENT: [unterminated\n"), 0o644))
			if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

			var buf bytes.Buffer
			err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
			require.Error(t, err)
			assert.Contains(t, buf.String(), "outside workspace root")
			assert.NotContains(t, buf.String(), "SECRET-CONTENT")
		})
	}
}

func TestRunLint_RegisteredLocalHarnessIsContainmentChecked(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom", "code.yaml"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
			[]byte("agents:\n  - name: code\n    source: custom/code.yaml\n"), 0o644))

		var buf bytes.Buffer
		err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
		require.Error(t, err)
		assert.Contains(t, buf.String(), "not a regular file")
	})
	t.Run("oversized", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "custom", "code.yaml"),
			bytes.Repeat([]byte("a"), resolve.MaxLocalResourceBytes+1), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
			[]byte("agents:\n  - name: code\n    source: custom/code.yaml\n"), 0o644))

		var buf bytes.Buffer
		err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
		require.Error(t, err)
		assert.Contains(t, buf.String(), "exceeds")
	})
	t.Run("fifo", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom"), 0o755))
		if err := syscall.Mkfifo(filepath.Join(dir, "custom", "code.yaml"), 0o644); err != nil {
			t.Skipf("fifo unavailable: %v", err)
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
			[]byte("agents:\n  - name: code\n    source: custom/code.yaml\n"), 0o644))

		var buf bytes.Buffer
		err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
		require.Error(t, err)
		assert.Contains(t, buf.String(), "not a regular file")
	})
}

func TestRunLint_MissingFullsendDirFails(t *testing.T) {
	var buf bytes.Buffer
	err := runLint(context.Background(), filepath.Join(t.TempDir(), "nope"), "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--fullsend-dir")
}

func TestRunLint_URLResourceWithoutConfigIsStructuralError(t *testing.T) {
	// The skill URL is under a default-allowed prefix, but like run and lock
	// (requireOrgConfig on HasURLReferences) lint needs a config layer for any
	// URL reference, not only a URL base.
	dir := t.TempDir()
	skillURL := "https://github.com/fullsend-ai/fullsend/tree/abc/skills/x#sha256=" + strings.Repeat("a", 64)
	writeValidLocalHarness(t, dir, "code", "skills:\n  - \""+skillURL+"\"\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "URL resources require config.yaml or config.base.yaml")
}

func TestRunLint_ChildInheritsRequiredFieldFromBroaderEventConditionedBaseOverlay(t *testing.T) {
	// The child's condition adds a term to the base's, so the base overlay
	// applies whenever the child's does and supplies validation_loop.script.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "validate.sh"), []byte("#!/bin/sh\n"), 0o755))
	cond := `has(event.entity) && event.entity.kind == "work_item"`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"),
		[]byte("overlays:\n- when: '"+cond+"'\n  validation_loop:\n    script: scripts/validate.sh\n    max_iterations: 1\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\n"+
		"overlays:\n- when: '"+cond+" && event.action == \"opened\"'\n  validation_loop:\n    max_iterations: 3\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.NoError(t, err, buf.String())
}

func TestRunLint_TopLevelValidationLoopInheritsScriptFromEventConditionedBaseOverlay(t *testing.T) {
	// The child sets only validation_loop.max_iterations at the top level; the
	// script comes from a base overlay that the empty event drops. That is valid
	// for matching events, so the empty-event composition must not fail.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "validate.sh"), []byte("#!/bin/sh\n"), 0o755))
	cond := `has(event.entity) && event.entity.kind == "work_item"`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"),
		[]byte("overlays:\n- when: '"+cond+"'\n  validation_loop:\n    script: scripts/validate.sh\n    max_iterations: 1\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\nvalidation_loop:\n  max_iterations: 3\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.NoError(t, err, buf.String())
}

func TestRunLint_TopLevelValidationLoopWithoutScriptAnywhereStillFails(t *testing.T) {
	// An overlay that does not supply the script cannot rescue the top level.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"),
		[]byte("overlays:\n- when: 'has(event.entity)'\n  timeout_minutes: 5\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\nvalidation_loop:\n  max_iterations: 3\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "validation_loop.script is required")
}

func TestRunLint_PluginPathOutsideWorkspaceIsRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "index.js"), []byte("export default {}\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "plugins:\n  - path: \""+outside+"\"\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "outside workspace root")
}

func TestRunLint_PluginPathOutsideWorkspaceIsRejectedBeforeInspection(t *testing.T) {
	// An empty outside directory would fail plugin-format detection if it were
	// walked; the containment error must win, proving it was never inspected.
	dir := t.TempDir()
	outside := t.TempDir()
	writeValidLocalHarness(t, dir, "code", "plugins:\n  - path: \""+outside+"\"\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "outside workspace root")
}

func TestRunLint_PluginSymlinkedOutsideWorkspaceIsRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "index.js"), []byte("export default {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "plugins"), 0o755))
	if err := os.Symlink(outside, filepath.Join(dir, "plugins", "evil")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeValidLocalHarness(t, dir, "code", "plugins:\n  - path: plugins/evil\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "outside workspace root")
}

func TestRunLint_OversizedBaseLayerIsStructuralError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	big := "# " + strings.Repeat("x", harness.MaxHarnessFileBytes) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"), []byte(big), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\n")

	var buf bytes.Buffer
	err := runLint(context.Background(), dir, "", false, false, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "exceeds")
}

func TestRunLint_ForgeExcludedOverlayDoesNotSupplyRequiredScript(t *testing.T) {
	// The base's only script-supplying overlay needs runtime.forge == "github".
	// Under gitlab it can never apply, so the child's partial validation_loop
	// fails at runtime and lint must say so; under github it passes.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bases"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "validate.sh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bases", "common.yaml"),
		[]byte("overlays:\n- when: 'runtime.forge == \"github\" && has(event.entity)'\n  validation_loop:\n    script: scripts/validate.sh\n"), 0o644))
	writeValidLocalHarness(t, dir, "code", "base: ../bases/common.yaml\nvalidation_loop:\n  max_iterations: 3\n")

	var buf bytes.Buffer
	err := runLintWithFlags(context.Background(), dir, "gitlab", false, resolveFlags{offline: true}, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "validation_loop.script is required")

	buf.Reset()
	err = runLintWithFlags(context.Background(), dir, "github", false, resolveFlags{offline: true}, ui.New(&buf))
	require.NoError(t, err, buf.String())
}

func TestRunLint_ForgeExcludedOverlayIsNotForced(t *testing.T) {
	// An overlay conditioned on another forge cannot be composed under the
	// selected one, so its broken reference is not reported there.
	dir := t.TempDir()
	writeValidLocalHarness(t, dir, "code",
		"overlays:\n- when: 'runtime.forge == \"github\" && has(event.entity)'\n  pre_script: scripts/missing.sh\n")

	var buf bytes.Buffer
	err := runLintWithFlags(context.Background(), dir, "gitlab", false, resolveFlags{offline: true}, ui.New(&buf))
	require.NoError(t, err, buf.String())

	buf.Reset()
	err = runLintWithFlags(context.Background(), dir, "github", false, resolveFlags{offline: true}, ui.New(&buf))
	require.Error(t, err)
	assert.Contains(t, buf.String(), "overlay 0")
}
