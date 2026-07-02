package otlp

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// readEvents reads run-telemetry.jsonl from dir into decoded maps.
func readEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, telemetry.TelemetryFile))
	require.NoError(t, err)
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		out = append(out, m)
	}
	require.NoError(t, sc.Err())
	return out
}

// hexid renders an OTLP id byte slice as lowercase hex ("" for empty/zero).
func hexid(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

func TestReadRun_MissingTelemetryFileIsNoOp(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	// Endpoint configured but the run dir has no L1 artifacts at all (e.g. a
	// disabled recorder): nothing to export, no error, no network.
	require.NoError(t, ExportRunDir(t.TempDir(), testVersion))
	assert.Equal(t, 0, sink.requestCount())
}

func TestReadRun_MissingSummarySkipsExport(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	// A telemetry file without a summary means the run never finalized
	// (crash). The L1 file is the forensic record; export skips quietly.
	dir := t.TempDir()
	r := telemetry.New(dir, defaultTC(), "code", "wi", time.Now())
	sp := r.StartSpan("sandbox_create", "", nil)
	r.EndSpan(sp, "ok", nil)
	// no Finalize

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount())
}

func TestReadRun_UnpairedStartSkipped(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	// A span_start without a span_end (in-flight at crash, but the run still
	// finalized) must be skipped: OTLP spans require an end time. The L1
	// line on disk remains the forensic record.
	dir := t.TempDir()
	r := telemetry.New(dir, defaultTC(), "code", "wi", time.Now())
	_ = r.StartSpan("agent", "", nil) // never ended
	sb := r.StartSpan("sandbox_create", "", nil)
	r.EndSpan(sb, "ok", nil)
	r.Finalize(0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	names := []string{}
	for _, sp := range sink.spans() {
		names = append(names, sp.GetName())
	}
	assert.ElementsMatch(t, []string{"run", "sandbox_create"}, names,
		"unpaired agent span_start skipped; paired spans exported")
}

func TestReadRun_MalformedLinesSkipped(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	// Corrupt the file: append garbage and a torn line (crash artifact).
	f, err := os.OpenFile(filepath.Join(dir, telemetry.TelemetryFile), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("not json at all\n" + `{"v":1,"event":"span_start","trace_id":"x","spa`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Len(t, sink.spans(), 3, "well-formed spans exported; garbage skipped")
}

func TestReadRun_InvalidIDsSkipped(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	// Hand-written artifacts with a non-hex trace id: the affected span is
	// dropped rather than exported with a garbage identity.
	dir := t.TempDir()
	lines := []string{
		`{"v":1,"event":"span_start","trace_id":"ZZZ","span_id":"a1b2c3d4e5f60718","parent":"","name":"run","ts":"2026-07-02T10:00:00Z","fullsend.work_item_id":"wi"}`,
		`{"v":1,"event":"span_end","trace_id":"ZZZ","span_id":"a1b2c3d4e5f60718","parent":"","name":"run","ts":"2026-07-02T10:00:01Z","fullsend.work_item_id":"wi","status":"ok"}`,
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, telemetry.TelemetryFile),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, telemetry.SummaryFile),
		[]byte(`{"v":1,"trace_id":"ZZZ","traceparent":"00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01","exit_code":0}`), 0o644))

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount(), "no valid spans => nothing to send")
}

func TestReadRun_MalformedSummaryDefaultsToSampled(t *testing.T) {
	// Level 1 always writes a parseable summary; a hand-mangled one must not
	// silently suppress export — default is sampled.
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)
	require.NoError(t, os.WriteFile(filepath.Join(dir, telemetry.SummaryFile), []byte("{not json"), 0o644))

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 1, sink.requestCount())

	// Same for a summary whose traceparent doesn't parse.
	require.NoError(t, os.WriteFile(filepath.Join(dir, telemetry.SummaryFile),
		[]byte(`{"traceparent":"garbage"}`), 0o644))
	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 2, sink.requestCount())
}

func TestReadRun_OddSpanLinesSkipped(t *testing.T) {
	// Hand-written artifact pathologies beyond invalid trace ids: bad span
	// ids, bad parent ids, unparseable timestamps, duplicate span_starts.
	// Each bad span is dropped; the good one survives.
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	const tid = testTraceID
	lines := []string{
		// good root, duplicated span_start (second ignored)
		`{"v":1,"event":"span_start","trace_id":"` + tid + `","span_id":"a1b2c3d4e5f60718","parent":"","name":"run","ts":"2026-07-02T10:00:00Z","fullsend.work_item_id":"wi"}`,
		`{"v":1,"event":"span_start","trace_id":"` + tid + `","span_id":"a1b2c3d4e5f60718","parent":"","name":"run-dup","ts":"2026-07-02T10:00:00Z"}`,
		// bad span id
		`{"v":1,"event":"span_start","trace_id":"` + tid + `","span_id":"zz","parent":"","name":"bad-sid","ts":"2026-07-02T10:00:00Z"}`,
		`{"v":1,"event":"span_end","trace_id":"` + tid + `","span_id":"zz","parent":"","name":"bad-sid","ts":"2026-07-02T10:00:01Z","status":"ok"}`,
		// bad parent id
		`{"v":1,"event":"span_start","trace_id":"` + tid + `","span_id":"bbbbbbbbbbbbbbbb","parent":"nothex","name":"bad-parent","ts":"2026-07-02T10:00:00Z"}`,
		`{"v":1,"event":"span_end","trace_id":"` + tid + `","span_id":"bbbbbbbbbbbbbbbb","parent":"nothex","name":"bad-parent","ts":"2026-07-02T10:00:01Z","status":"ok"}`,
		// bad timestamps
		`{"v":1,"event":"span_start","trace_id":"` + tid + `","span_id":"cccccccccccccccc","parent":"","name":"bad-ts","ts":"yesterday"}`,
		`{"v":1,"event":"span_end","trace_id":"` + tid + `","span_id":"cccccccccccccccc","parent":"","name":"bad-ts","ts":"2026-07-02T10:00:01Z","status":"ok"}`,
		// good root end; span_end without work item id exercises the
		// start-side fallback
		`{"v":1,"event":"span_end","trace_id":"` + tid + `","span_id":"a1b2c3d4e5f60718","parent":"","name":"run","ts":"2026-07-02T10:00:02Z","status":"ok"}`,
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, telemetry.TelemetryFile),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, telemetry.SummaryFile),
		[]byte(`{"traceparent":"00-`+tid+`-a1b2c3d4e5f60718-01"}`), 0o644))

	require.NoError(t, ExportRunDir(dir, testVersion))
	spans := sink.spans()
	require.Len(t, spans, 1, "only the well-formed span survives")
	assert.Equal(t, "run", spans[0].GetName(), "first span_start wins over the duplicate")
	attrs := sink.spanAttrs(t, "run")
	assert.Equal(t, "wi", attrs["fullsend.work_item_id"].GetStringValue(),
		"work item id falls back to the span_start line")
}

func TestReadRun_UnexpectedAttrShapesDegradeToStrings(t *testing.T) {
	// Arrays and nested objects are not part of the L1 schema, but if they
	// ever appear they must degrade to strings — never dropped, never a panic.
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	r := telemetry.New(dir, defaultTC(), "code", "wi", time.Now())
	sp := r.StartSpan("agent", "", nil)
	r.EndSpan(sp, "ok", map[string]any{
		"arr":     []string{"a", "b"},
		"nested":  map[string]any{"k": 1},
		"nothing": nil,
	})
	r.Finalize(0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	attrs := sink.spanAttrs(t, "agent")
	assert.NotEmpty(t, attrs["arr"].GetStringValue(), "array degrades to its string form")
	assert.NotEmpty(t, attrs["nested"].GetStringValue(), "object degrades to its string form")
	_, present := attrs["nothing"]
	assert.False(t, present, "null attributes carry no information and are omitted")
}

func TestReadRun_NumberFidelity(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	// Large integers must survive the JSON round-trip without float64
	// mangling: 9007199254740993 (2^53+1) is not representable as float64.
	dir := t.TempDir()
	r := telemetry.New(dir, defaultTC(), "code", "wi", time.Now())
	sp := r.StartSpan("agent", "", nil)
	r.EndSpan(sp, "ok", map[string]any{"big": int64(9007199254740993), "neg": -7, "frac": 0.5})
	r.Finalize(0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	attrs := sink.spanAttrs(t, "agent")
	assert.Equal(t, int64(9007199254740993), attrs["big"].GetIntValue(), "2^53+1 preserved exactly")
	assert.Equal(t, int64(-7), attrs["neg"].GetIntValue())
	assert.InDelta(t, 0.5, attrs["frac"].GetDoubleValue(), 1e-12)
}
