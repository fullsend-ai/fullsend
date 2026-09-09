package evalmeasure

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

func TestLoadRegistryAndScoreTrace(t *testing.T) {
	t.Parallel()
	reg, err := LoadRegistry(filepath.Join("testdata", "sample-registry.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "triage", reg.Agent)

	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	results := ScoreTrace(traces[0], reg)
	require.Len(t, results, 1)
	assert.Equal(t, "trace_fitness", results[0].Name)
	assert.Equal(t, "em-001@1", results[0].Version)
	assert.Equal(t, "pass", results[0].Label)
}

func TestMeasureFile_Idempotent(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	telemetry := filepath.Join("testdata", "complete.jsonl")
	registry := filepath.Join("testdata", "sample-registry.yaml")

	first, err := MeasureFile(telemetry, registry, out)
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := MeasureFile(telemetry, registry, out)
	require.NoError(t, err)
	assert.Empty(t, second)

	b, err := os.ReadFile(filepath.Join(out, MeasurementsFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name":"trace_fitness"`)
}

func TestMeasureFile_AppendBeforeLedger(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	telemetry := filepath.Join("testdata", "complete.jsonl")
	registry := filepath.Join("testdata", "sample-registry.yaml")
	ledgerPath := filepath.Join(out, LedgerFile)

	first, err := MeasureFile(telemetry, registry, out)
	require.NoError(t, err)
	require.Len(t, first, 1)

	require.NoError(t, os.Remove(ledgerPath))

	second, err := MeasureFile(telemetry, registry, out)
	require.NoError(t, err)
	require.Len(t, second, 1, "retry after missing ledger should re-score")

	lines, err := os.ReadFile(filepath.Join(out, MeasurementsFile))
	require.NoError(t, err)
	assert.Equal(t, 2, bytes.Count(lines, []byte("\n")), "missing ledger may duplicate JSONL lines; consumers dedupe on trace_id+version")
}

func TestMeasureFile_BadRegistry(t *testing.T) {
	clearOTLPEnv(t)
	_, err := MeasureFile(
		filepath.Join("testdata", "complete.jsonl"),
		filepath.Join(t.TempDir(), "missing.yaml"),
		t.TempDir(),
	)
	require.Error(t, err)
}

func TestMeasureFile_BadTelemetry(t *testing.T) {
	clearOTLPEnv(t)
	_, err := MeasureFile(
		filepath.Join(t.TempDir(), "missing.jsonl"),
		filepath.Join("testdata", "sample-registry.yaml"),
		t.TempDir(),
	)
	require.Error(t, err)
}

func TestMeasureAndExport_CancelledContext(t *testing.T) {
	clearOTLPEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := MeasureAndExport(
		ctx,
		filepath.Join("testdata", "complete.jsonl"),
		filepath.Join("testdata", "sample-registry.yaml"),
		t.TempDir(),
		"",
	)
	require.Error(t, err)
}

func TestWithPersistHook_NilContext(t *testing.T) {
	ctx := WithPersistHook(nil, func() {})
	require.NotNil(t, ctx)
	_, ok := ctx.Value(persistHookKey{}).(func())
	assert.True(t, ok)
}

func writeTwoTraceTelemetry(t *testing.T, completePath string) string {
	t.Helper()
	src, err := os.ReadFile(completePath)
	require.NoError(t, err)
	src = bytes.TrimSpace(src)
	second := bytes.ReplaceAll(src, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	p := filepath.Join(t.TempDir(), "run-telemetry.jsonl")
	require.NoError(t, os.WriteFile(p, append(append(src, '\n'), second...), 0o644))
	return p
}

func TestMeasureAndExport_KeepsFirstWhenSecondPersistFails(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/v1/traces")
	orig := newScoreOTLPExporter
	t.Cleanup(func() { newScoreOTLPExporter = orig })
	newScoreOTLPExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
		return telemetry.NewOTLPExporter(ctx)
	}

	out := t.TempDir()
	telem := writeTwoTraceTelemetry(t, filepath.Join("testdata", "complete.jsonl"))
	ctx := WithPersistHook(context.Background(), func() {
		meas := filepath.Join(out, MeasurementsFile)
		require.NoError(t, os.Remove(meas))
		require.NoError(t, os.Mkdir(meas, 0o755))
	})
	results, stats, err := MeasureAndExport(ctx, telem, filepath.Join("testdata", "sample-registry.yaml"), out, "test-1.2.3")
	require.Error(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", results[0].TraceID)
	assert.Contains(t, err.Error(), "append measurements")
	assert.Empty(t, stats.RemoteExportWarning, "persisted prefix must still attempt OTLP export")
	require.NotEmpty(t, sink.allSpans(), "first persisted row must be OTLP-exported before mid-loop return")
}

func TestMeasureAndExport_ScoresPartialFileDespiteParseError(t *testing.T) {
	clearOTLPEnv(t)
	// Oversized line after a good line: ParseTelemetryFile keeps the good
	// trace and returns sc.Err(); MeasureAndExport still scores it and
	// treats the partial parse as success (scores are data).
	good, err := os.ReadFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	dir := t.TempDir()
	telem := filepath.Join(dir, "run-telemetry.jsonl")
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

	out := t.TempDir()
	results, stats, err := MeasureAndExport(context.Background(), telem, filepath.Join("testdata", "sample-registry.yaml"), out, "")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, LabelPass, results[0].Label)
	assert.Greater(t, stats.NonEmptyLines, 0)
	assert.NotEmpty(t, stats.Incomplete, "oversized-line parse must set Incomplete for CLI warn")
}

func TestMeasureFile_PrescriptSkippedRecordsSkip(t *testing.T) {
	clearOTLPEnv(t)
	out := t.TempDir()
	results, err := MeasureFile(
		filepath.Join("testdata", "prescript-skipped.jsonl"),
		filepath.Join("testdata", "sample-registry.yaml"),
		out,
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, LabelSkip, results[0].Label)
	assert.NotEqual(t, LabelFail, results[0].Label)

	b, err := os.ReadFile(filepath.Join(out, MeasurementsFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"label":"skip"`)
	assert.NotContains(t, string(b), `"label":"fail"`)
}

func TestMeasureFile_EmptyIdentityPersistsFailRow(t *testing.T) {
	clearOTLPEnv(t)
	dir := t.TempDir()
	telem := filepath.Join(dir, "run-telemetry.jsonl")
	// Minimal OTLP line: run span with no agent identity.
	line := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"dddddddddddddddddddddddddddddddd","spanId":"1111111111111111","name":"run","startTimeUnixNano":"1","endTimeUnixNano":"2","attributes":[{"key":"fullsend.work_item_id","value":{"stringValue":"acme/demo#1"}},{"key":"exit_code","value":{"intValue":"0"}}]},{"traceId":"dddddddddddddddddddddddddddddddd","spanId":"2222222222222222","name":"sandbox_create","startTimeUnixNano":"1","endTimeUnixNano":"2"},{"traceId":"dddddddddddddddddddddddddddddddd","spanId":"3333333333333333","name":"agent","startTimeUnixNano":"1","endTimeUnixNano":"2","attributes":[{"key":"gen_ai.system","value":{"stringValue":"anthropic"}}]}]}]}]}` + "\n"
	require.NoError(t, os.WriteFile(telem, []byte(line), 0o644))

	out := t.TempDir()
	results, err := MeasureFile(telem, filepath.Join("testdata", "sample-registry.yaml"), out)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, LabelFail, results[0].Label)
	assert.Contains(t, results[0].Explanation, "identity=fail")

	b, err := os.ReadFile(filepath.Join(out, MeasurementsFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"label":"fail"`)
	assert.Contains(t, string(b), "identity=fail")
}
