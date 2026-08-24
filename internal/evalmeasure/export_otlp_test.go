package evalmeasure

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

type scoreOTLPSink struct {
	mu   sync.Mutex
	reqs []*coltracepb.ExportTraceServiceRequest
	srv  *httptest.Server
}

func newScoreOTLPSink(t *testing.T) *scoreOTLPSink {
	t.Helper()
	s := &scoreOTLPSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			raw, err = io.ReadAll(zr)
			_ = zr.Close()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, &req)
		s.mu.Unlock()
		resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *scoreOTLPSink) allSpans() []*coltracepb.ExportTraceServiceRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*coltracepb.ExportTraceServiceRequest, len(s.reqs))
	copy(out, s.reqs)
	return out
}

func clearOTLPEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("TRACEPARENT", "")
}

func TestExportOTLPScores_NoopWithoutEndpoint(t *testing.T) {
	clearOTLPEnv(t)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "bbbbbbbbbbbbbbbb",
	}}, "test-1.2.3")
	require.NoError(t, err)
}

func TestExportOTLPScores_EmitsGenAIEvaluationEvent(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/v1/traces")

	orig := newScoreOTLPExporter
	t.Cleanup(func() { newScoreOTLPExporter = orig })
	newScoreOTLPExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
		return telemetry.NewOTLPExporter(ctx)
	}

	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name:        "trace_fitness",
		Label:       LabelPass,
		Explanation: "span_tree=pass",
		TraceID:     "84d470ba2451ffeccfe09022d9b2aebd",
		SpanID:      "77f8c0902eaeedcb",
		WorkItemID:  "fullsend-ai/fullsend#6449",
		Agent:       "review",
		Version:     "em-001@1",
		Value:       1.0,
	}}, "test-1.2.3")
	require.NoError(t, err)

	reqs := sink.allSpans()
	require.NotEmpty(t, reqs)

	var foundEvent bool
	var spanName string
	var parentHex string
	var traceHex string
	var svcName, svcVer string
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, kv := range rs.GetResource().GetAttributes() {
				switch kv.GetKey() {
				case "service.name":
					svcName = kv.GetValue().GetStringValue()
				case "service.version":
					svcVer = kv.GetValue().GetStringValue()
				}
			}
			for _, ss := range rs.GetScopeSpans() {
				for _, sp := range ss.GetSpans() {
					spanName = sp.GetName()
					traceHex = hexOf(sp.GetTraceId())
					parentHex = hexOf(sp.GetParentSpanId())
					for _, ev := range sp.GetEvents() {
						if ev.GetName() != EventGenAIEvaluationResult {
							continue
						}
						foundEvent = true
						attrs := map[string]string{}
						var score float64
						var hasScore bool
						for _, kv := range ev.GetAttributes() {
							switch kv.GetKey() {
							case AttrGenAIEvaluationName, AttrGenAIEvaluationScoreLabel, AttrGenAIEvaluationExplanation, AttrFullsendMeasurementVersion:
								attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
							case AttrGenAIEvaluationScoreValue:
								score = kv.GetValue().GetDoubleValue()
								hasScore = true
							}
						}
						assert.Equal(t, "trace_fitness", attrs[AttrGenAIEvaluationName])
						assert.Equal(t, LabelPass, attrs[AttrGenAIEvaluationScoreLabel])
						assert.Equal(t, "span_tree=pass", attrs[AttrGenAIEvaluationExplanation])
						assert.Equal(t, "em-001@1", attrs[AttrFullsendMeasurementVersion])
						require.True(t, hasScore)
						assert.Equal(t, 1.0, score)
					}
				}
			}
		}
	}
	require.True(t, foundEvent, "expected gen_ai.evaluation.result event")
	assert.Equal(t, spanNameEvalMeasure, spanName)
	assert.Equal(t, "84d470ba2451ffeccfe09022d9b2aebd", traceHex)
	assert.Equal(t, "77f8c0902eaeedcb", parentHex)
	assert.Equal(t, "fullsend", svcName)
	assert.Equal(t, "test-1.2.3", svcVer)
}

func TestExportOTLPScores_InvalidTraceID(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", TraceID: "not-a-trace", SpanID: "77f8c0902eaeedcb",
	}}, "test-1.2.3")
	require.Error(t, err)
}

func TestExportOTLPScores_InvalidSpanIDHex(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name:    "trace_fitness",
		TraceID: "84d470ba2451ffeccfe09022d9b2aebd",
		SpanID:  "zzzzzzzzzzzzzzzz",
	}}, "test-1.2.3")
	require.Error(t, err)
}

func TestExportOTLPScores_DisabledSDK(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_SDK_DISABLED", "true")
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", TraceID: "84d470ba2451ffeccfe09022d9b2aebd", SpanID: "77f8c0902eaeedcb",
	}}, "test-1.2.3")
	require.NoError(t, err)
	assert.Empty(t, sink.allSpans())
}

func TestExportOTLPScores_EmptySpanIDSkipped(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", Label: LabelSkip, TraceID: "84d470ba2451ffeccfe09022d9b2aebd", SpanID: "",
	}}, "test-1.2.3")
	require.NoError(t, err)
	assert.Empty(t, sink.allSpans())
}

func TestExportOTLPScores_ZeroTraceID(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", TraceID: "00000000000000000000000000000000", SpanID: "77f8c0902eaeedcb",
	}}, "test-1.2.3")
	require.Error(t, err)
}

func TestExportOTLPScores_SkipOmitsScoreValue(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", Label: LabelSkip, Explanation: "no run span",
		TraceID: "84d470ba2451ffeccfe09022d9b2aebd", SpanID: "77f8c0902eaeedcb",
		Agent: "review", Version: "em-001@1", Value: 0,
	}}, "test-1.2.3")
	require.NoError(t, err)
	reqs := sink.allSpans()
	require.NotEmpty(t, reqs)
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, sp := range ss.GetSpans() {
					for _, ev := range sp.GetEvents() {
						if ev.GetName() != EventGenAIEvaluationResult {
							continue
						}
						for _, kv := range ev.GetAttributes() {
							assert.NotEqual(t, AttrGenAIEvaluationScoreValue, kv.GetKey(), "skip must omit score.value")
						}
					}
				}
			}
		}
	}
}

func TestMeasureAndExport_OTLPFailOpen(t *testing.T) {
	dir := t.TempDir()
	telemSrc := filepath.Join("testdata", "complete.jsonl")
	telem := filepath.Join(dir, "run-telemetry.jsonl")
	raw, err := os.ReadFile(telemSrc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(telem, raw, 0o644))
	reg := filepath.Join("testdata", "sample-registry.yaml")

	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // closed port
	results, stats, err := MeasureAndExport(context.Background(), telem, reg, dir, "test-1.2.3")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	_, statErr := os.Stat(filepath.Join(dir, MeasurementsFile))
	require.NoError(t, statErr)
	require.NotEmpty(t, stats.RemoteExportWarning, "expected OTLP failure warning with local JSONL kept")
}

func TestExportOTLPScores_UnsampledTRACEPARENTNoop(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	// W3C TRACEPARENT with sampled flag cleared (-00).
	t.Setenv("TRACEPARENT", "00-84d470ba2451ffeccfe09022d9b2aebd-77f8c0902eaeedcb-00")
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", Label: LabelPass, TraceID: "84d470ba2451ffeccfe09022d9b2aebd",
		SpanID: "77f8c0902eaeedcb", Value: 1, Version: "em-001@1",
	}}, "test-1.2.3")
	require.NoError(t, err)
	assert.Empty(t, sink.allSpans(), "unsampled inbound TRACEPARENT must suppress OTLP score export")
}

func TestExportOTLPScores_UnsampledTRACEPARENTOtherTraceIDExports(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	// Unsampled parent for TraceID A must not suppress scores for TraceID B.
	t.Setenv("TRACEPARENT", "00-84d470ba2451ffeccfe09022d9b2aebd-77f8c0902eaeedcb-00")
	orig := newScoreOTLPExporter
	t.Cleanup(func() { newScoreOTLPExporter = orig })
	newScoreOTLPExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
		return telemetry.NewOTLPExporter(ctx)
	}
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", Label: LabelPass, TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SpanID: "bbbbbbbbbbbbbbbb", Value: 1, Version: "em-001@1",
	}}, "test-1.2.3")
	require.NoError(t, err)
	assert.NotEmpty(t, sink.allSpans(), "unrelated TraceID must still export under TraceID-scoped gate")
}

func TestCapturingExporter_ClearsErrorOnLaterSuccess(t *testing.T) {
	seq := &seqExporter{errs: []error{assert.AnError, nil}}
	cap := &capturingExporter{base: seq}
	require.Error(t, cap.ExportSpans(context.Background(), nil))
	cap.mu.Lock()
	require.Error(t, cap.err)
	cap.mu.Unlock()
	require.NoError(t, cap.ExportSpans(context.Background(), nil))
	cap.mu.Lock()
	assert.NoError(t, cap.err, "successful ExportSpans must clear prior latch")
	cap.mu.Unlock()
}

type seqExporter struct {
	errs []error
	i    int
}

func (s *seqExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	if s.i >= len(s.errs) {
		return nil
	}
	err := s.errs[s.i]
	s.i++
	return err
}

func (s *seqExporter) Shutdown(context.Context) error { return nil }

func TestExportOTLPScores_TruncatesLongExplanation(t *testing.T) {
	sink := newScoreOTLPSink(t)
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "")
	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "")
	huge := strings.Repeat("x", telemetry.MaxSpanAttrValueLen+500)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", Label: LabelPass, Explanation: huge,
		TraceID: "84d470ba2451ffeccfe09022d9b2aebd", SpanID: "77f8c0902eaeedcb",
		Version: "em-001@1", Value: 1,
	}}, "test-1.2.3")
	require.NoError(t, err)
	reqs := sink.allSpans()
	require.NotEmpty(t, reqs)
	var got string
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, sp := range ss.GetSpans() {
					for _, ev := range sp.GetEvents() {
						if ev.GetName() != EventGenAIEvaluationResult {
							continue
						}
						for _, kv := range ev.GetAttributes() {
							if kv.GetKey() == AttrGenAIEvaluationExplanation {
								got = kv.GetValue().GetStringValue()
							}
						}
					}
				}
			}
		}
	}
	require.NotEmpty(t, got)
	assert.LessOrEqual(t, len(got), telemetry.MaxSpanAttrValueLen)
	assert.Less(t, len(got), len(huge))
}

func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
