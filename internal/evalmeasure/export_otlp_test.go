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

func TestExportOTLPScores_NoopWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "bbbbbbbbbbbbbbbb",
	}})
	require.NoError(t, err)
}

func TestExportOTLPScores_EmitsGenAIEvaluationEvent(t *testing.T) {
	sink := newScoreOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SDK_DISABLED", "")

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
	}})
	require.NoError(t, err)

	reqs := sink.allSpans()
	require.NotEmpty(t, reqs)

	var foundEvent bool
	var spanName string
	var parentHex string
	var traceHex string
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
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
}

func TestExportOTLPScores_InvalidTraceID(t *testing.T) {
	sink := newScoreOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", TraceID: "not-a-trace", SpanID: "77f8c0902eaeedcb",
	}})
	require.Error(t, err)
}

func TestExportOTLPScores_InvalidSpanIDHex(t *testing.T) {
	sink := newScoreOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name:    "trace_fitness",
		TraceID: "84d470ba2451ffeccfe09022d9b2aebd",
		SpanID:  "zzzzzzzzzzzzzzzz",
	}})
	require.Error(t, err)
}

func TestExportOTLPScores_DisabledSDK(t *testing.T) {
	sink := newScoreOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_SDK_DISABLED", "true")
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", TraceID: "84d470ba2451ffeccfe09022d9b2aebd", SpanID: "77f8c0902eaeedcb",
	}})
	require.NoError(t, err)
	assert.Empty(t, sink.allSpans())
}

func TestExportOTLPScores_FailLabel(t *testing.T) {
	sink := newScoreOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	err := ExportOTLPScores(context.Background(), []EvaluationResult{{
		Name: "trace_fitness", Label: LabelFail, Explanation: "nope",
		TraceID: "84d470ba2451ffeccfe09022d9b2aebd", SpanID: "77f8c0902eaeedcb",
		Agent: "review", Version: "em-001@1", Value: 0,
	}})
	require.NoError(t, err)
	require.NotEmpty(t, sink.allSpans())
}

func TestMeasureAndExport_OTLPFailOpen(t *testing.T) {
	dir := t.TempDir()
	telemSrc := filepath.Join("testdata", "complete.jsonl")
	telem := filepath.Join(dir, "run-telemetry.jsonl")
	raw, err := os.ReadFile(telemSrc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(telem, raw, 0o644))
	reg := filepath.Join("testdata", "sample-registry.yaml")

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // closed port
	results, stats, err := MeasureAndExport(context.Background(), telem, reg, dir)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	_, statErr := os.Stat(filepath.Join(dir, MeasurementsFile))
	require.NoError(t, statErr)
	require.NotEmpty(t, stats.RemoteExportWarning, "expected OTLP failure warning with local JSONL kept")
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
