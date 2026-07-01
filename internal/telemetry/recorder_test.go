package telemetry

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTraceID = "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d"
	testRootID  = "a1b2c3d4e5f60718"
)

// readLines reads an NDJSON file and returns each non-empty line decoded as a
// map, failing the test if any complete line is not valid JSON.
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		require.NoErrorf(t, json.Unmarshal(sc.Bytes(), &m), "line must be valid JSON: %s", sc.Text())
		out = append(out, m)
	}
	require.NoError(t, sc.Err())
	return out
}

func TestRecorder_EmitsValidNDJSON(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "octo/repo#2577", time.Now())
	sp := r.StartSpan("sandbox_create", "", nil)
	r.EndSpan(sp, "ok", nil)
	r.Finalize(0)

	lines := readLines(t, filepath.Join(dir, "run-telemetry.jsonl"))
	// root span_start, child span_start, child span_end, root span_end
	require.Len(t, lines, 4)
	for _, m := range lines {
		assert.Equal(t, float64(SchemaVersion), m["v"])
		assert.Equal(t, testTraceID, m["trace_id"], "trace_id identical on every line")
		assert.Equal(t, "octo/repo#2577", m["fullsend.work_item_id"], "work_item_id present on every line")
		assert.NotEmpty(t, m["span_id"])
		assert.Contains(t, []any{"span_start", "span_end"}, m["event"])
	}
}

func TestRecorder_RootSpanHasEmptyParentAndChildPointsToRoot(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	sp := r.StartSpan("sandbox_create", "", nil) // empty parent => defaults to root
	r.EndSpan(sp, "ok", nil)
	r.Finalize(0)

	lines := readLines(t, filepath.Join(dir, "run-telemetry.jsonl"))
	for _, m := range lines {
		if m["name"] == "run" {
			assert.Equal(t, "", m["parent"], "root span has no parent")
		}
		if m["name"] == "sandbox_create" {
			assert.Equal(t, testRootID, m["parent"], "empty parent defaults to root span id")
		}
	}
}

func TestRecorder_SummaryFields(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().Add(-2 * time.Second)
	r := New(dir, testTraceID, testRootID, "code", "octo/repo#2577", start)
	sp := r.StartSpan("agent", "", map[string]any{"iteration": 1})
	r.EndSpan(sp, "ok", map[string]any{"iteration": 1, "exit_code": 0})
	r.Finalize(0)

	data, err := os.ReadFile(filepath.Join(dir, "run-summary.json"))
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(data, &s))

	assert.Equal(t, float64(SchemaVersion), s["v"])
	assert.Equal(t, testTraceID, s["trace_id"])
	assert.Equal(t, "code", s["agent"])
	assert.Equal(t, "octo/repo#2577", s["fullsend.work_item_id"])
	assert.Equal(t, float64(0), s["exit_code"])
	assert.NotEmpty(t, s["started_at"])
	assert.NotEmpty(t, s["ended_at"])
	durationMS, ok := s["duration_ms"].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, durationMS, float64(0))

	tp, ok := s["traceparent"].(string)
	require.True(t, ok)
	assert.Regexp(t, reTraceparent, tp)
	traceID, ok := s["trace_id"].(string)
	require.True(t, ok)
	assert.Contains(t, tp, traceID, "traceparent must embed trace_id")

	steps, ok := s["steps"].([]any)
	require.True(t, ok)
	require.Len(t, steps, 1)
	step0, ok := steps[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "agent", step0["name"])
	assert.Equal(t, "ok", step0["status"])
	stepDur, ok := step0["duration_ms"].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, stepDur, float64(0))
}

func TestRecorder_NonZeroExitMarksError(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.Finalize(2)

	data, err := os.ReadFile(filepath.Join(dir, "run-summary.json"))
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(data, &s))
	assert.Equal(t, float64(2), s["exit_code"])

	lines := readLines(t, filepath.Join(dir, "run-telemetry.jsonl"))
	var rootEnd map[string]any
	for _, m := range lines {
		if m["name"] == "run" && m["event"] == "span_end" {
			rootEnd = m
		}
	}
	require.NotNil(t, rootEnd)
	assert.Equal(t, "error", rootEnd["status"], "nonzero exit code => root span status error")
}

func TestRecorder_CrashSafety_LinesDurableWithoutFinalize(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	sp := r.StartSpan("sandbox_create", "", nil)
	r.EndSpan(sp, "ok", nil)
	// Simulate a crash: Finalize never called. Synced lines must still parse.
	lines := readLines(t, filepath.Join(dir, "run-telemetry.jsonl"))
	require.Len(t, lines, 3, "root start + child start + child end persisted")
	_, err := os.Stat(filepath.Join(dir, "run-summary.json"))
	assert.True(t, os.IsNotExist(err), "no summary without Finalize")
}

func TestRecorder_TruncatedTrailingLineTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run-telemetry.jsonl")
	content := `{"v":1,"event":"span_start","trace_id":"x","span_id":"a","name":"run"}` + "\n" +
		`{"v":1,"event":"span_end","trace_id":"x","span_id":"a","name":"run"}` + "\n" +
		`{"v":1,"event":"span_start","trace_id":"x","spa` // torn final line, no newline
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	parsed := 0
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			parsed++
		}
	}
	assert.Equal(t, 2, parsed, "two complete lines parse; truncated final line is skipped")
}

func TestRecorder_GracefulDegradation_UnwritableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist") // parent missing => OpenFile fails
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	require.NotNil(t, r, "New must never return nil, even on failure")

	sp := r.StartSpan("x", "", nil)
	assert.Equal(t, "", sp)
	assert.NotPanics(t, func() { r.EndSpan(sp, "ok", nil) })
	assert.Equal(t, "", r.TraceParent(), "disabled recorder yields no traceparent")
	assert.NotPanics(t, func() { r.Finalize(0) })

	_, err := os.Stat(filepath.Join(dir, "run-summary.json"))
	assert.Error(t, err, "nothing written when disabled")
}

func TestRecorder_NilSafe(t *testing.T) {
	var r *Recorder
	assert.NotPanics(t, func() {
		sp := r.StartSpan("x", "", nil)
		r.EndSpan(sp, "ok", nil)
		_ = r.TraceParent()
		r.Finalize(0)
	})
}

func TestRecorder_FinalizeIdempotentNoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.Finalize(0)
	assert.NotPanics(t, func() { r.Finalize(0) }, "second Finalize is a no-op")

	_, err := os.Stat(filepath.Join(dir, "run-summary.json.tmp"))
	assert.True(t, os.IsNotExist(err), "no .tmp file should remain after atomic write")
	_, err = os.Stat(filepath.Join(dir, "run-summary.json"))
	assert.NoError(t, err)
}

func TestRecorder_TraceParent(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	defer r.Finalize(0)
	assert.Equal(t, "00-"+testTraceID+"-"+testRootID+"-01", r.TraceParent())
}

func TestRecorder_ConcurrentWritesNoCorruption(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sp := r.StartSpan("agent", "", nil)
			r.EndSpan(sp, "ok", nil)
		}()
	}
	wg.Wait()
	r.Finalize(0)

	// Mutex must prevent interleaved writes; every line stays valid JSON.
	lines := readLines(t, filepath.Join(dir, "run-telemetry.jsonl"))
	assert.Len(t, lines, 42, "root start + 20*(start+end) + root end")
}

func TestRecorder_EndSpanUnknownSpanID(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.EndSpan("deadbeefdeadbeef", "ok", nil) // never started — must not panic
	r.Finalize(0)

	lines := readLines(t, filepath.Join(dir, "run-telemetry.jsonl"))
	var found map[string]any
	for _, m := range lines {
		if m["span_id"] == "deadbeefdeadbeef" {
			found = m
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, testRootID, found["parent"], "unknown span end defaults parent to root")
	assert.Equal(t, "", found["name"])
}

func TestRecorder_WriteFailureMidRunDisablesGracefully(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	// Simulate the telemetry file failing mid-run by closing it underneath.
	require.NoError(t, r.f.Close())

	assert.NotPanics(t, func() {
		sp := r.StartSpan("agent", "", nil) // emit hits a write error
		r.EndSpan(sp, "ok", nil)
	})
	assert.Equal(t, "", r.TraceParent(), "recorder disables itself after a write failure")
	assert.NotPanics(t, func() { r.Finalize(0) })
}

func TestRecorder_SummaryWriteFailureSwallowed(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	// Point the summary target at a non-existent subdir so WriteFile fails;
	// the failure must be swallowed and leave no stray temp file.
	r.dir = filepath.Join(dir, "missing")
	assert.NotPanics(t, func() { r.Finalize(0) })
	_, err := os.Stat(filepath.Join(r.dir, SummaryFile))
	assert.Error(t, err)
	_, err = os.Stat(filepath.Join(r.dir, SummaryFile+".tmp"))
	assert.True(t, os.IsNotExist(err), "no stray temp file on summary write failure")
}

func TestRecorder_SummaryMetrics(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.SetMetrics(RunMetrics{InputTokens: 18432, OutputTokens: 2901, CacheCreationInputTokens: 8000, CacheReadInputTokens: 50000, TotalCostUSD: 0.0731, NumTurns: 7, ToolCalls: 14})
	r.Finalize(0)

	data, err := os.ReadFile(filepath.Join(dir, SummaryFile))
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(data, &s))

	m, ok := s["metrics"].(map[string]any)
	require.True(t, ok, "summary must contain a metrics block")
	assert.Equal(t, float64(18432), m["input_tokens"])
	assert.Equal(t, float64(2901), m["output_tokens"])
	assert.Equal(t, float64(8000), m["cache_creation_input_tokens"])
	assert.Equal(t, float64(50000), m["cache_read_input_tokens"])
	assert.InDelta(t, 0.0731, m["total_cost_usd"], 1e-9)
	assert.Equal(t, float64(7), m["num_turns"])
	assert.Equal(t, float64(14), m["tool_calls"])
}

func TestRecorder_SummaryModel(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.SetModel("claude-opus-4-6")
	r.Finalize(0)

	data, err := os.ReadFile(filepath.Join(dir, SummaryFile))
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(data, &s))
	assert.Equal(t, "claude-opus-4-6", s["model"], "summary must carry the resolved model")
}

func TestRecorder_SummaryModelOmittedWhenUnset(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.Finalize(0) // no SetModel
	data, err := os.ReadFile(filepath.Join(dir, SummaryFile))
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(data, &s))
	_, present := s["model"]
	assert.False(t, present, "model must be omitted when never set")
}

func TestRecorder_SetModelNilAndDisabledSafe(t *testing.T) {
	var r *Recorder
	assert.NotPanics(t, func() { r.SetModel("m") })
	disabled := New(filepath.Join(t.TempDir(), "does-not-exist"), testTraceID, testRootID, "code", "wi", time.Now())
	assert.NotPanics(t, func() { disabled.SetModel("m") })
}

func TestRecorder_FinalizeClosesFileEvenWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.disabled = true // simulate a mid-run emit failure that disabled the recorder
	r.Finalize(0)
	_, err := r.f.Write([]byte("x"))
	assert.ErrorIs(t, err, os.ErrClosed, "Finalize must close the open file even when disabled")
}

func TestRecorder_EndSpanDefaultsEmptyStatusToOK(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	sp := r.StartSpan("sandbox_create", "", nil)
	r.EndSpan(sp, "", nil) // empty status must default to "ok"
	r.Finalize(0)

	data, err := os.ReadFile(filepath.Join(dir, SummaryFile))
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(data, &s))
	steps := s["steps"].([]any)
	require.Len(t, steps, 1)
	assert.Equal(t, "ok", steps[0].(map[string]any)["status"], "empty status defaults to ok")
}

func TestRecorder_EmitMarshalErrorDisablesRecorder(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	// A channel cannot be JSON-marshaled, so emit hits its marshal-error branch
	// and must disable the recorder gracefully rather than panic.
	assert.NotPanics(t, func() {
		r.StartSpan("agent", "", map[string]any{"bad": make(chan int)})
	})
	assert.Equal(t, "", r.TraceParent(), "marshal failure disables the recorder")
}

func TestRecorder_SummaryRenameFailureSwallowed(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	// Make the final summary path a non-empty directory so the atomic rename
	// fails; the failure must be swallowed and leave no stray temp file.
	blocker := filepath.Join(dir, SummaryFile)
	require.NoError(t, os.MkdirAll(blocker, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o644))

	assert.NotPanics(t, func() { r.Finalize(0) })
	_, err := os.Stat(filepath.Join(dir, SummaryFile+".tmp"))
	assert.True(t, os.IsNotExist(err), "no stray temp file when rename fails")
}

func TestRecorder_SummaryMetricsOmittedWhenUnset(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, testTraceID, testRootID, "code", "wi", time.Now())
	r.Finalize(0) // no SetMetrics

	data, err := os.ReadFile(filepath.Join(dir, SummaryFile))
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(data, &s))
	_, present := s["metrics"]
	assert.False(t, present, "metrics block must be omitted when never set")
}

func TestRecorder_SetMetricsNilAndDisabledSafe(t *testing.T) {
	var r *Recorder
	assert.NotPanics(t, func() { r.SetMetrics(RunMetrics{InputTokens: 1}) })

	disabled := New(filepath.Join(t.TempDir(), "does-not-exist"), testTraceID, testRootID, "code", "wi", time.Now())
	assert.NotPanics(t, func() { disabled.SetMetrics(RunMetrics{InputTokens: 1}) })
}
