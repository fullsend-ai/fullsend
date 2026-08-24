// Command prove-otlp-scores scores a real run-telemetry.jsonl and asserts
// portable OTLP gen_ai.evaluation.result events arrive at a local sink.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <run-telemetry.jsonl> <registry.yaml> [out-dir]\n", os.Args[0])
		os.Exit(2)
	}
	telem := os.Args[1]
	reg := os.Args[2]
	out := filepath.Dir(telem)
	if len(os.Args) > 3 {
		out = os.Args[3]
	}
	_ = os.Remove(filepath.Join(out, evalmeasure.LedgerFile))
	_ = os.Remove(filepath.Join(out, evalmeasure.MeasurementsFile))

	var mu sync.Mutex
	var reqs []*coltracepb.ExportTraceServiceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		mu.Lock()
		reqs = append(reqs, &req)
		mu.Unlock()
		resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	_ = os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	_ = os.Unsetenv("OTEL_SDK_DISABLED")

	results, stats, err := evalmeasure.MeasureAndExport(context.Background(), telem, reg, out, "dev")
	if err != nil {
		fmt.Fprintf(os.Stderr, "measure failed: %v\n", err)
		os.Exit(1)
	}

	events := extractEvents(reqs)
	report := map[string]any{
		"endpoint":              srv.URL,
		"scores_written":        len(results),
		"remote_export_warning": stats.RemoteExportWarning,
		"results":               results,
		"otlp_requests":         len(reqs),
		"events":                events,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)

	if len(results) == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: no scores written\n")
		os.Exit(1)
	}
	if len(reqs) == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: no OTLP requests received\n")
		os.Exit(1)
	}
	if len(events) == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: no gen_ai.evaluation.result events\n")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "PASS: %d score(s), %d OTLP event(s)\n", len(results), len(events))
}

type eventView struct {
	SpanName   string         `json:"span_name"`
	TraceID    string         `json:"trace_id"`
	ParentID   string         `json:"parent_span_id"`
	EventName  string         `json:"event_name"`
	Attributes map[string]any `json:"attributes"`
}

func extractEvents(reqs []*coltracepb.ExportTraceServiceRequest) []eventView {
	var out []eventView
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, sp := range ss.GetSpans() {
					for _, ev := range sp.GetEvents() {
						if ev.GetName() != evalmeasure.EventGenAIEvaluationResult {
							continue
						}
						attrs := map[string]any{}
						for _, kv := range ev.GetAttributes() {
							attrs[kv.GetKey()] = anyValue(kv.GetValue())
						}
						out = append(out, eventView{
							SpanName:   sp.GetName(),
							TraceID:    hex.EncodeToString(sp.GetTraceId()),
							ParentID:   hex.EncodeToString(sp.GetParentSpanId()),
							EventName:  ev.GetName(),
							Attributes: attrs,
						})
					}
				}
			}
		}
	}
	return out
}

func anyValue(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	default:
		return v.String()
	}
}
