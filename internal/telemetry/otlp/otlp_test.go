package otlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

const (
	testTraceID = "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d"
	testRootID  = "a1b2c3d4e5f60718"
	testVersion = "test-version"
)

// otlpSink is an in-process OTLP/HTTP trace collector for tests. It decodes
// each POST body into an ExportTraceServiceRequest and records the request
// headers and paths for assertions.
type otlpSink struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []*coltracepb.ExportTraceServiceRequest
	headers []http.Header
	paths   []string
}

func newOTLPSink(t *testing.T) *otlpSink {
	t.Helper()
	s := &otlpSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			require.NoError(t, err)
			raw, err = io.ReadAll(zr)
			require.NoError(t, err)
		}
		var req coltracepb.ExportTraceServiceRequest
		require.NoError(t, proto.Unmarshal(raw, &req))
		s.mu.Lock()
		s.reqs = append(s.reqs, &req)
		s.headers = append(s.headers, r.Header.Clone())
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *otlpSink) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// spans flattens all received resourceSpans/scopeSpans into a single list.
func (s *otlpSink) spans() []*tracepb.Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*tracepb.Span
	for _, req := range s.reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				out = append(out, ss.GetSpans()...)
			}
		}
	}
	return out
}

func (s *otlpSink) resourceAttrs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, req := range s.reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, kv := range rs.GetResource().GetAttributes() {
				out[kv.GetKey()] = kv.GetValue().GetStringValue()
			}
		}
	}
	return out
}

// spanAttrs returns the attribute map of the first received span with name.
func (s *otlpSink) spanAttrs(t *testing.T, name string) map[string]*commonpb.AnyValue {
	t.Helper()
	for _, sp := range s.spans() {
		if sp.GetName() == name {
			out := map[string]*commonpb.AnyValue{}
			for _, kv := range sp.GetAttributes() {
				out[kv.GetKey()] = kv.GetValue()
			}
			return out
		}
	}
	t.Fatalf("no span named %q received", name)
	return nil
}

// writeRunFixture drives a real Level 1 Recorder to produce genuine
// run-telemetry.jsonl and run-summary.json artifacts in dir.
func writeRunFixture(t *testing.T, dir string, tc telemetry.TraceContext, exitCode int) {
	t.Helper()
	r := telemetry.New(dir, tc, "code", "octo/repo#2862", time.Now().Add(-2*time.Second))
	sb := r.StartSpan("sandbox_create", "", nil)
	r.EndSpan(sb, "ok", nil)
	ag := r.StartSpan("agent", "", map[string]any{"iteration": 1})
	r.EndSpan(ag, "ok", map[string]any{
		"iteration":                 1,
		"exit_code":                 0,
		"gen_ai.request.model":      "claude-opus-4-6",
		"gen_ai.usage.input_tokens": 1234,
		"fullsend.cost_usd":         0.34,
		"cache_hit":                 true,
	})
	r.Finalize(exitCode)
}

func defaultTC() telemetry.TraceContext {
	return telemetry.TraceContext{TraceID: testTraceID, RootSpanID: testRootID}
}

// --- Gating ---

func TestEnabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	assert.False(t, Enabled(), "no endpoint => disabled")

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	assert.True(t, Enabled(), "generic endpoint => enabled")

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://localhost:4318/v1/traces")
	assert.True(t, Enabled(), "signal-specific endpoint => enabled")

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "   \t ")
	assert.False(t, Enabled(), "whitespace-only endpoint is unset per the OTel spec")
}

func TestExportRunDir_NoEndpoint_NoOpAndNoNetwork(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount(), "no endpoint => zero network activity")
}

func TestExportRunDir_BaseEndpointAppendsV1Traces(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	require.Equal(t, 1, sink.requestCount())
	assert.Equal(t, "/v1/traces", sink.paths[0], "generic endpoint gets /v1/traces appended (published contract)")
}

func TestExportRunDir_TracesEndpointUsedAsIs(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1/ignored") // must not be used
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/custom/ingest/path")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	require.Equal(t, 1, sink.requestCount())
	assert.Equal(t, "/custom/ingest/path", sink.paths[0],
		"signal-specific endpoint wins and is used as-is, no path appended (published contract)")
}

func TestExportRunDir_MalformedEndpointFailsOpen(t *testing.T) {
	sink := newOTLPSink(t)
	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	for _, bad := range []string{"://bad", "not a url", "ftp://host:4318"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", bad)
			err := ExportRunDir(dir, testVersion)
			assert.Error(t, err, "malformed endpoint must surface an error for the caller's warning")
			assert.Equal(t, 0, sink.requestCount(),
				"must not fall back to the SDK default localhost endpoint")
			assertL1Intact(t, dir)
		})
	}
}

func TestExportRunDir_HeadersReachWire(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)
	// Percent-encoded space + base64 padding: the exact form the MLflow
	// runbook uses (headers are parsed baggage-style and URL-decoded).
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "authorization=Basic%20dXNlcjpwYXNz,x-mlflow-experiment-id=42")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	require.Equal(t, 1, sink.requestCount())
	assert.Equal(t, "Basic dXNlcjpwYXNz", sink.headers[0].Get("Authorization"))
	assert.Equal(t, "42", sink.headers[0].Get("x-mlflow-experiment-id"))
}

func TestExportRunDir_TracesHeadersPrecedence(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-mlflow-experiment-id=1")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "x-mlflow-experiment-id=2")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	require.Equal(t, 1, sink.requestCount())
	assert.Equal(t, "2", sink.headers[0].Get("x-mlflow-experiment-id"),
		"signal-specific headers win (published contract)")
}

func TestExportRunDir_GzipCompression(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "gzip")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	require.Equal(t, 1, sink.requestCount())
	assert.Equal(t, "gzip", sink.headers[0].Get("Content-Encoding"))
	assert.Len(t, sink.spans(), 3, "compressed payload still decodes to all spans")
}

func TestExportRunDir_SDKDisabled(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_SDK_DISABLED", "true")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount(), "OTEL_SDK_DISABLED=true must be honored")
}

func TestExportRunDir_TracesExporterNone(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_TRACES_EXPORTER", "none")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount(), "OTEL_TRACES_EXPORTER=none must be honored")
}

func TestExportRunDir_UnsupportedProtocolRejected(t *testing.T) {
	// Only OTLP over http/protobuf is implemented. Posting protobuf at a
	// gRPC-only endpoint fails cryptically, so the mismatch is refused
	// loudly instead — for either protocol env var.
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	assert.Error(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount())

	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc")
	assert.Error(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount())

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
	assert.NoError(t, ExportRunDir(dir, testVersion), "explicit http/protobuf proceeds")
	assert.Equal(t, 1, sink.requestCount())
}

func TestExportRunDir_ExporterConstructionErrorSurfaced(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	orig := newExporter
	newExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
		return nil, errors.New("boom")
	}
	defer func() { newExporter = orig }()

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	err := ExportRunDir(dir, testVersion)
	assert.ErrorContains(t, err, "boom")
	assert.Equal(t, 0, sink.requestCount())
	assertL1Intact(t, dir)
}

func TestExportRunDir_MalformedResourceEnvStillExports(t *testing.T) {
	// A garbage OTEL_RESOURCE_ATTRIBUTES must not break export — the
	// resource degrades to fullsend's defaults.
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "%%%%not=valid=pairs%%%%,,,=")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	require.Equal(t, 1, sink.requestCount())
	assert.Equal(t, "fullsend", sink.resourceAttrs()["service.name"])
}

func TestExportRunDir_UnsampledRunSkipsExport(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	tc := defaultTC()
	tc.Flags = "00" // upstream said: do not sample
	writeRunFixture(t, dir, tc, 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, 0, sink.requestCount(),
		"an upstream-unsampled trace must not be exported (ParentBased semantics)")
	assertL1Intact(t, dir)
}

// --- Fidelity ---

func TestExportRunDir_SpanIdentityPreserved(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	spans := sink.spans()
	require.Len(t, spans, 3, "run + sandbox_create + agent")

	events := readEvents(t, dir)
	starts := map[string]map[string]any{} // span_id (hex) -> span_start line
	ends := map[string]map[string]any{}
	for _, e := range events {
		id, _ := e["span_id"].(string)
		if e["event"] == "span_start" {
			starts[id] = e
		} else {
			ends[id] = e
		}
	}

	byName := map[string]*tracepb.Span{}
	for _, sp := range spans {
		byName[sp.GetName()] = sp
		// Identity: ids on the wire are byte-equal to the L1 file's hex ids.
		assert.Equal(t, testTraceID, hexid(sp.GetTraceId()), "trace id preserved on %s", sp.GetName())
		st, ok := starts[hexid(sp.GetSpanId())]
		require.True(t, ok, "wire span %s must exist in the L1 file", sp.GetName())
		// Timestamps: nanosecond-exact against the file's RFC3339Nano values.
		startTS, err := time.Parse(time.RFC3339Nano, st["ts"].(string))
		require.NoError(t, err)
		assert.Equal(t, uint64(startTS.UnixNano()), sp.GetStartTimeUnixNano(), "start time exact on %s", sp.GetName())
		endTS, err := time.Parse(time.RFC3339Nano, ends[hexid(sp.GetSpanId())]["ts"].(string))
		require.NoError(t, err)
		assert.Equal(t, uint64(endTS.UnixNano()), sp.GetEndTimeUnixNano(), "end time exact on %s", sp.GetName())
	}

	root := byName["run"]
	require.NotNil(t, root)
	assert.Equal(t, testRootID, hexid(root.GetSpanId()))
	assert.Empty(t, root.GetParentSpanId(), "local trace root has no parent")
	assert.Equal(t, tracepb.Span_SPAN_KIND_INTERNAL, root.GetKind())
	for _, name := range []string{"sandbox_create", "agent"} {
		child := byName[name]
		require.NotNil(t, child, "%s span exported", name)
		assert.Equal(t, testRootID, hexid(child.GetParentSpanId()), "%s parents to root", name)
		assert.Equal(t, tracepb.Span_SPAN_KIND_INTERNAL, child.GetKind())
	}
}

func TestExportRunDir_RemoteParentContinuesTrace(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	tc := defaultTC()
	tc.ParentSpanID = "beefbeefbeefbeef" // inbound TRACEPARENT parent (issue #2779)
	writeRunFixture(t, dir, tc, 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	for _, sp := range sink.spans() {
		if sp.GetName() != "run" {
			continue
		}
		assert.Equal(t, "beefbeefbeefbeef", hexid(sp.GetParentSpanId()),
			"root span must join the inbound parent trace on the wire")
		assert.Equal(t, tracepb.Span_SPAN_KIND_CONSUMER, sp.GetKind(),
			"dispatched run roots are CONSUMER spans (published contract)")
		return
	}
	t.Fatal("run span not exported")
}

func TestExportRunDir_AttributeTypesPreserved(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	attrs := sink.spanAttrs(t, "agent")

	assert.Equal(t, int64(1234), attrs["gen_ai.usage.input_tokens"].GetIntValue(), "integral numbers stay Int64")
	assert.Equal(t, int64(1), attrs["iteration"].GetIntValue())
	assert.InDelta(t, 0.34, attrs["fullsend.cost_usd"].GetDoubleValue(), 1e-9, "fractional numbers stay Double")
	assert.Equal(t, "claude-opus-4-6", attrs["gen_ai.request.model"].GetStringValue())
	assert.True(t, attrs["cache_hit"].GetBoolValue())
}

func TestExportRunDir_WorkItemIDOnEverySpan(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	spans := sink.spans()
	require.NotEmpty(t, spans)
	for _, sp := range spans {
		found := false
		for _, kv := range sp.GetAttributes() {
			if kv.GetKey() == "fullsend.work_item_id" {
				found = true
				assert.Equal(t, "octo/repo#2862", kv.GetValue().GetStringValue())
			}
		}
		assert.True(t, found, "fullsend.work_item_id on span %s (primary correlation key, ADR 0050)", sp.GetName())
	}
}

func TestExportRunDir_StatusMapping(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 2) // nonzero exit => root span status "error"

	require.NoError(t, ExportRunDir(dir, testVersion))
	for _, sp := range sink.spans() {
		switch sp.GetName() {
		case "run":
			assert.Equal(t, tracepb.Status_STATUS_CODE_ERROR, sp.GetStatus().GetCode(), "error status maps to ERROR")
		default:
			assert.Equal(t, tracepb.Status_STATUS_CODE_OK, sp.GetStatus().GetCode(), "ok status maps to OK on %s", sp.GetName())
		}
	}
}

func TestExportRunDir_ResourceServiceNameAndVersion(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	res := sink.resourceAttrs()
	assert.Equal(t, "fullsend", res["service.name"])
	assert.Equal(t, testVersion, res["service.version"])
}

func TestExportRunDir_ServiceNameEnvOverride(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_SERVICE_NAME", "my-deployment")

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	require.NoError(t, ExportRunDir(dir, testVersion))
	assert.Equal(t, "my-deployment", sink.resourceAttrs()["service.name"],
		"OTEL_SERVICE_NAME overrides the default (standard resource env)")
}

// --- Fail-open ---

// assertL1Intact asserts both Level 1 artifacts exist and parse.
func assertL1Intact(t *testing.T, dir string) {
	t.Helper()
	events := readEvents(t, dir)
	assert.NotEmpty(t, events, "run-telemetry.jsonl intact")
	_, err := os.Stat(filepath.Join(dir, telemetry.SummaryFile))
	assert.NoError(t, err, "run-summary.json intact")
}

// exportWithBudget runs ExportRunDir against endpoint with a pinned export
// budget and asserts the fail-open invariants: an error is returned (for the
// caller's single warning line), the call is time-bounded, and the Level 1
// artifacts are untouched. The 15s bound is ~10x the pinned budget so loaded
// CI runners cannot flake it, while a regression to the SDK's default
// unbounded shutdown blows through it reliably.
func exportWithBudget(t *testing.T, dir, endpoint string) {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", endpoint)
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1000") // 1s per-request budget

	orig := exportTimeout
	exportTimeout = 1500 * time.Millisecond
	defer func() { exportTimeout = orig }()

	start := time.Now()
	err := ExportRunDir(dir, testVersion)
	elapsed := time.Since(start)

	assert.Error(t, err, "endpoint pathology must surface an error")
	assert.Less(t, elapsed, 15*time.Second, "export must be hard-bounded, got %v", elapsed)
	assertL1Intact(t, dir)
}

func TestExportFailOpen_TCPBlackHole(t *testing.T) {
	// Accepts TCP connections and never reads or responds — the nastiest
	// endpoint pathology (issue #2862's mandated test).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var conns []net.Conn
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)
	exportWithBudget(t, dir, "http://"+ln.Addr().String())
}

func TestExportFailOpen_HangingHTTPServer(t *testing.T) {
	// Accepts the HTTP request but never responds.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		srv.Close()
	})

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)
	exportWithBudget(t, dir, srv.URL)
}

func TestExportFailOpen_ConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close()) // dead address => RST on connect

	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)
	exportWithBudget(t, dir, "http://"+addr)
}

func TestExportFailOpen_DNSFailure(t *testing.T) {
	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)
	// .invalid is RFC 2606-reserved: never resolves, deterministically.
	exportWithBudget(t, dir, "http://fullsend-l2.invalid:4318")
}

func TestExportFailOpen_HTTP4xx(t *testing.T) {
	// 401 is exactly what an unauthenticated MLflow returns; 400 is a
	// missing x-mlflow-experiment-id. Neither is retryable per the OTLP
	// spec, so the export fails fast without a retry storm.
	for _, code := range []int{401, 400} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			writeRunFixture(t, dir, defaultTC(), 0)
			exportWithBudget(t, dir, srv.URL)
		})
	}
}

func TestExportFailOpen_Retry503ThenDelivered(t *testing.T) {
	// A transient 503 with Retry-After must be retried (within the bounded
	// budget) and the spans eventually delivered.
	var mu sync.Mutex
	calls := 0
	var got *coltracepb.ExportTraceServiceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req coltracepb.ExportTraceServiceRequest
		_ = proto.Unmarshal(raw, &req)
		got = &req
		resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", srv.URL)
	dir := t.TempDir()
	writeRunFixture(t, dir, defaultTC(), 0)

	start := time.Now()
	err := ExportRunDir(dir, testVersion)
	require.NoError(t, err, "transient 503 must be retried to success")
	assert.Less(t, time.Since(start), 15*time.Second)

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, calls, 2, "must have retried")
	require.NotNil(t, got)
	spans := 0
	for _, rs := range got.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			spans += len(ss.GetSpans())
		}
	}
	assert.Equal(t, 3, spans, "all spans delivered after retry")
}
