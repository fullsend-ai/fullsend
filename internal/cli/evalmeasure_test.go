package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// clearOTLPEnv keeps Measure*/eval-measure tests hermetic when CI injects
// OTEL_EXPORTER_OTLP_* org vars (same pattern as evalmeasure/export_otlp_test).
func clearOTLPEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("TRACEPARENT", "")
}

func TestEvalMeasureCmd_ScoresFixture(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	telemetryPath := filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl")
	registry := filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml")

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--telemetry", telemetryPath,
		"--registry", registry,
		"--out-dir", out,
	})
	err := cmd.Execute()
	require.NoError(t, err)

	b, err := os.ReadFile(filepath.Join(out, "eval-measurements.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name":"trace_fitness"`)
	assert.Contains(t, buf.String(), "Wrote 1 measurement(s)")
}

func TestEvalMeasureCmd_HasOfflineFlag(t *testing.T) {
	cmd := newEvalMeasureCmd()
	f := cmd.Flags().Lookup("offline")
	require.NotNil(t, f)
	assert.Equal(t, "false", f.DefValue)
}

func TestRootCommand_HasEvalMeasureSubcommand(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "eval-measure" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected eval-measure subcommand")
}

func TestEvalMeasureCmd_MissingRequiredFlags(t *testing.T) {
	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"eval-measure"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestEvalMeasureCmd_OutputDirIgnoresNestedTelemetry_LegacyFormat(t *testing.T) {
	clearOTLPEnv(t)
	fsDir := t.TempDir()
	outBase := t.TempDir()
	runDir := filepath.Join(outBase, "agent-triage-1-1")
	nested := filepath.Join(runDir, "iteration-1", "output")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	good, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, telemetry.TelemetryFile), good, 0o644))
	// Agent-planted copy: valid JSONL with a different trace id would produce
	// a second row if scored. Nested path must be ignored.
	planted := bytes.ReplaceAll(good, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte("ffffffffffffffffffffffffffffffff"))
	require.NoError(t, os.WriteFile(filepath.Join(nested, telemetry.TelemetryFile), planted, 0o644))

	reg, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(fsDir, "eval", "measurements"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fsDir, "eval", "measurements", "triage.yaml"), reg, 0o644))

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--agent", "triage",
		"--fullsend-dir", fsDir,
		"--output-dir", outBase,
	})
	require.NoError(t, cmd.Execute())

	b, err := os.ReadFile(filepath.Join(runDir, evalmeasure.MeasurementsFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)
	assert.NotContains(t, string(b), "ffffffffffffffffffffffffffffffff")
}

// TestEvalMeasureCmd_LocalFullsendDirManifestProducesJSONL_LegacyFormat
// locks the resolved-manifest path used before agents@v0 carries stock
// YAML: a local FULLSEND_DIR eval/measurements/<agent>.yaml must produce
// eval-measurements.jsonl.
func TestEvalMeasureCmd_LocalFullsendDirManifestProducesJSONL_LegacyFormat(t *testing.T) {
	clearOTLPEnv(t)
	fsDir := t.TempDir()
	outBase := t.TempDir()
	runDir := filepath.Join(outBase, "agent-triage-2-2")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	good, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, telemetry.TelemetryFile), good, 0o644))

	reg, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(fsDir, "eval", "measurements"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fsDir, "eval", "measurements", "triage.yaml"), reg, 0o644))

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--agent", "triage",
		"--fullsend-dir", fsDir,
		"--output-dir", outBase,
		"--offline",
	})
	require.NoError(t, cmd.Execute())

	b, err := os.ReadFile(filepath.Join(runDir, evalmeasure.MeasurementsFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name":"trace_fitness"`)
	assert.Contains(t, buf.String(), "Wrote")
}

func TestEvalMeasureCmd_OutputDirIgnoresNestedTelemetry_NewFormat(t *testing.T) {
	clearOTLPEnv(t)
	fsDir := t.TempDir()
	outBase := t.TempDir()
	runDir := filepath.Join(outBase, "fs-tri-aabbccddee00")
	nested := filepath.Join(runDir, "iteration-1", "output")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	good, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, telemetry.TelemetryFile), good, 0o644))
	// Agent-planted copy: valid JSONL with a different trace id would produce
	// a second row if scored. Nested path must be ignored.
	planted := bytes.ReplaceAll(good, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte("ffffffffffffffffffffffffffffffff"))
	require.NoError(t, os.WriteFile(filepath.Join(nested, telemetry.TelemetryFile), planted, 0o644))

	reg, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(fsDir, "eval", "measurements"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fsDir, "eval", "measurements", "triage.yaml"), reg, 0o644))

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--agent", "triage",
		"--fullsend-dir", fsDir,
		"--output-dir", outBase,
	})
	require.NoError(t, cmd.Execute())

	b, err := os.ReadFile(filepath.Join(runDir, evalmeasure.MeasurementsFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)
	assert.NotContains(t, string(b), "ffffffffffffffffffffffffffffffff")
}

func TestEvalMeasureCmd_LocalFullsendDirManifestProducesJSONL_NewFormat(t *testing.T) {
	clearOTLPEnv(t)
	fsDir := t.TempDir()
	outBase := t.TempDir()
	runDir := filepath.Join(outBase, "fs-tri-1122334455ff")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	good, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, telemetry.TelemetryFile), good, 0o644))

	reg, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(fsDir, "eval", "measurements"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fsDir, "eval", "measurements", "triage.yaml"), reg, 0o644))

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--agent", "triage",
		"--fullsend-dir", fsDir,
		"--output-dir", outBase,
		"--offline",
	})
	require.NoError(t, cmd.Execute())

	b, err := os.ReadFile(filepath.Join(runDir, evalmeasure.MeasurementsFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name":"trace_fitness"`)
	assert.Contains(t, buf.String(), "Wrote")
}

func writeTwoTraceTelemetry(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"))
	require.NoError(t, err)
	src = bytes.TrimSpace(src)
	second := bytes.ReplaceAll(src, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	p := filepath.Join(t.TempDir(), "run-telemetry.jsonl")
	require.NoError(t, os.WriteFile(p, append(append(src, '\n'), second...), 0o644))
	return p
}

func TestRunEvalMeasure_ErrorIncludesPartialResults(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	telem := writeTwoTraceTelemetry(t)
	registry := filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml")
	ctx := evalmeasure.WithPersistHook(context.Background(), func() {
		meas := filepath.Join(out, evalmeasure.MeasurementsFile)
		require.NoError(t, os.Remove(meas))
		require.NoError(t, os.Mkdir(meas, 0o755))
	})
	var buf bytes.Buffer
	results, skipped, err := runEvalMeasure(ctx, ui.New(&buf), evalMeasureOpts{
		telemetryPath: telem,
		registryPath:  registry,
		outDir:        out,
	})
	require.Error(t, err)
	assert.False(t, skipped)
	require.Len(t, results, 1)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", results[0].TraceID)

	printMeasurementResults(ui.New(&buf), results, false)
	assert.Contains(t, buf.String(), "trace_fitness")
	assert.NotContains(t, buf.String(), "Wrote")
}

func TestEvalMeasureCmd_ErrorPrintsPartialFromFailingFile(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	telem := writeTwoTraceTelemetry(t)
	ctx := evalmeasure.WithPersistHook(context.Background(), func() {
		meas := filepath.Join(out, evalmeasure.MeasurementsFile)
		require.NoError(t, os.Remove(meas))
		require.NoError(t, os.Mkdir(meas, 0o755))
	})

	cmd := newRootCmd()
	cmd.SetContext(ctx)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--telemetry", telem,
		"--registry", filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"),
		"--out-dir", out,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, buf.String(), "trace_fitness")
	assert.NotContains(t, buf.String(), "Wrote")
}

func TestEvalMeasureCmd_ErrorDoesNotPrintWrote(t *testing.T) {
	clearOTLPEnv(t)
	out := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(out, []byte("x"), 0o644))

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--telemetry", filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"),
		"--registry", filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"),
		"--out-dir", out,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.NotContains(t, buf.String(), "Wrote")
}

func TestEvalMeasureCmd_SkipWhenNoTelemetry(t *testing.T) {
	clearOTLPEnv(t)
	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--agent", "triage",
		"--output-dir", t.TempDir(),
	})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "skipping eval measurements")
	assert.NotContains(t, buf.String(), "Wrote")
}

func TestEvalMeasureCmd_WarnsOnCorruptTelemetryLine(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	good, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"))
	require.NoError(t, err)
	telem := filepath.Join(out, "run-telemetry.jsonl")
	require.NoError(t, os.WriteFile(telem, append(append([]byte{}, good...), []byte("\nnot-json\n")...), 0o644))

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--telemetry", telem,
		"--registry", filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"),
		"--out-dir", out,
	})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "skipped 1 unreadable of 2 telemetry line")
}

func TestEvalMeasureCmd_WarnsOnIncompleteParse(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	good, err := os.ReadFile(filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"))
	require.NoError(t, err)
	telem := filepath.Join(out, "run-telemetry.jsonl")
	f, err := os.Create(telem)
	require.NoError(t, err)
	_, err = f.Write(append(bytes.TrimSpace(good), '\n'))
	require.NoError(t, err)
	huge := make([]byte, 11*1024*1024)
	for i := range huge {
		huge[i] = 'a'
	}
	_, err = f.Write(huge)
	require.NoError(t, err)
	_, err = f.Write([]byte("\n"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--telemetry", telem,
		"--registry", filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml"),
		"--out-dir", out,
	})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "telemetry parse incomplete")
	assert.Contains(t, buf.String(), "scored available traces")
	assert.Contains(t, buf.String(), "Wrote")
}

func TestActionYML_EvalMeasureNoFloatingV0Curl(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	require.NoError(t, err)
	s := string(b)
	assert.NotContains(t, s, "raw.githubusercontent.com/fullsend-ai/agents/v0/eval/measurements")
	assert.NotContains(t, s, `find "${GITHUB_WORKSPACE}/output" -name run-telemetry.jsonl`)
	idx := strings.Index(s, "name: Eval measurements")
	require.Greater(t, idx, 0)
	step := s[idx:]
	if end := strings.Index(step, "name: Upload fullsend artifacts"); end > 0 {
		step = step[:end]
	}
	assert.Contains(t, step, "eval-measure")
	assert.Contains(t, step, "--output-dir")
	assert.Contains(t, step, "--agent")
	assert.Contains(t, step, "continue-on-error: true")
	assert.Contains(t, step, "if: always()")
	assert.Contains(t, step, "GH_TOKEN:")
	assert.Contains(t, step, "PR_BASE_SHA:")
	assert.Contains(t, step, "--registry")
	assert.Contains(t, step, "FULLSEND_DIR:")
	assert.Contains(t, step, "MEASURE_REL")
	assert.NotContains(t, step, "--fullsend-dir")
	assert.NotContains(t, step, "curl ")
	assert.NotContains(t, step, "Authorization: Bearer")
}

func TestPlatformTelemetryFileMatchesRecorder(t *testing.T) {
	assert.Equal(t, telemetry.TelemetryFile, evalmeasure.PlatformTelemetryFile)
}

func TestEvalMeasureFetchContext(t *testing.T) {
	printer := ui.New(io.Discard)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	opts, client := evalMeasureFetchContext("", false, printer)
	require.NotNil(t, client)
	assert.NotEmpty(t, opts.OrgAllowlist)
	assert.NotEmpty(t, opts.WorkspaceRoot)
	assert.False(t, opts.FetchPolicy.Offline)
	assert.NotEqual(t, filepath.Clean(os.TempDir()), filepath.Clean(opts.WorkspaceRoot),
		"empty --fullsend-dir must not use shared os.TempDir() as WorkspaceRoot")
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		assert.Equal(t, filepath.Join(cache, "fullsend", "eval-measure"), opts.WorkspaceRoot)
	}

	dir := t.TempDir()
	opts2, _ := evalMeasureFetchContext(dir, true, printer)
	assert.Contains(t, opts2.AuditLogPath, ".fullsend-cache")
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	assert.Equal(t, abs, opts2.WorkspaceRoot)
	assert.True(t, opts2.FetchPolicy.Offline)
}

func TestResolveEvalMeasureRegistry_LocalOverride(t *testing.T) {
	fsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(fsDir, "eval", "measurements"), 0o755))
	local := filepath.Join(fsDir, "eval", "measurements", "triage.yaml")
	require.NoError(t, os.WriteFile(local, []byte("agent: triage\n"), 0o644))
	got, err := resolveEvalMeasureRegistry(context.Background(), ui.New(io.Discard), evalMeasureOpts{
		agent:       "triage",
		fullsendDir: fsDir,
	})
	require.NoError(t, err)
	assert.Equal(t, local, got)
}

func TestResolveEvalMeasureRegistry_UnknownAgentSkipsRemote(t *testing.T) {
	got, err := resolveEvalMeasureRegistry(context.Background(), ui.New(io.Discard), evalMeasureOpts{
		agent:       "not-a-stock-agent",
		fullsendDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestResolveEvalMeasureRegistry_RequiresAgentOrRegistry(t *testing.T) {
	_, err := resolveEvalMeasureRegistry(context.Background(), ui.New(io.Discard), evalMeasureOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--registry or --agent")
}

func TestSanitizeMeasurementAgentName(t *testing.T) {
	got, err := sanitizeMeasurementAgentName(" Triage ")
	require.NoError(t, err)
	assert.Equal(t, "triage", got)

	for _, bad := range []string{"", "  ", "../evil", "foo/bar", `foo\bar`, "a..b"} {
		_, err := sanitizeMeasurementAgentName(bad)
		require.Error(t, err, "agent %q", bad)
	}
}

func TestLocalMeasurementManifestStaysUnderFullsendDir(t *testing.T) {
	fsDir := t.TempDir()
	got, err := localMeasurementManifest(fsDir, "triage")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(fsDir, "eval", "measurements", "triage.yaml"), got)
}

func TestResolveEvalMeasureRegistry_RejectsPathAgent(t *testing.T) {
	_, err := resolveEvalMeasureRegistry(context.Background(), ui.New(io.Discard), evalMeasureOpts{
		agent:       "../../../tmp/evil",
		fullsendDir: t.TempDir(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --agent")
}

func TestEvalMeasureCmd_TelemetryWithoutRegistry(t *testing.T) {
	clearOTLPEnv(t)
	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"eval-measure",
		"--telemetry", filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl"),
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--registry or --agent")
}

func TestPrintMeasurementResults_SkipAndNoWroteOnError(t *testing.T) {
	var buf bytes.Buffer
	p := ui.New(&buf)
	printMeasurementResults(p, []evalmeasure.EvaluationResult{{
		Name:    "trace_fitness",
		Version: "em-001@1",
		Label:   evalmeasure.LabelSkip,
	}}, true)
	assert.Contains(t, buf.String(), "skip")
	assert.Contains(t, buf.String(), "Wrote 1 measurement(s)")

	buf.Reset()
	printMeasurementResults(p, []evalmeasure.EvaluationResult{{
		Name:    "trace_fitness",
		Version: "em-001@1",
		Label:   evalmeasure.LabelFail,
	}}, false)
	assert.NotContains(t, buf.String(), "Wrote")
}

func TestRunEvalMeasure_OTLPFailWarns(t *testing.T) {
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	out := t.TempDir()
	telemetryPath := filepath.Join("..", "evalmeasure", "testdata", "complete.jsonl")
	registry := filepath.Join("..", "evalmeasure", "testdata", "sample-registry.yaml")
	var buf bytes.Buffer
	results, skipped, err := runEvalMeasure(context.Background(), ui.New(&buf), evalMeasureOpts{
		telemetryPath: telemetryPath,
		registryPath:  registry,
		outDir:        out,
	})
	require.NoError(t, err)
	assert.False(t, skipped)
	require.NotEmpty(t, results)
	assert.Contains(t, buf.String(), "OTLP score export failed")
	_, statErr := os.Stat(filepath.Join(out, evalmeasure.MeasurementsFile))
	require.NoError(t, statErr, "local JSONL must still be written")
}
