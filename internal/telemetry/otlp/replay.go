package otlp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// scopeName identifies the instrumentation scope on exported spans.
const scopeName = "github.com/fullsend-ai/fullsend/internal/telemetry"

// event mirrors the Level 1 eventRecord NDJSON line shape. It is declared
// locally so internal/telemetry keeps its internals unexported and stays
// dependency-free.
type event struct {
	V          int            `json:"v"`
	Event      string         `json:"event"`
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	Parent     string         `json:"parent"`
	Name       string         `json:"name"`
	TS         string         `json:"ts"`
	WorkItemID string         `json:"fullsend.work_item_id"`
	Status     string         `json:"status"`
	Attrs      map[string]any `json:"attrs"`
}

// readRun parses dir's Level 1 artifacts into exportable span snapshots and
// reports whether the run's trace is sampled (from the summary traceparent's
// W3C flags). Missing artifacts mean "nothing to export" (nil spans, no
// error): a missing summary means the run never finalized — the NDJSON file
// on disk is the crash-forensics record and stays local. Malformed lines,
// invalid ids, and span_starts without a span_end are skipped: OTLP spans
// require a complete identity and an end time, and a partial artifact must
// never block export of the well-formed remainder.
func readRun(dir, serviceVersion string) ([]sdktrace.ReadOnlySpan, bool, error) {
	summary, err := os.ReadFile(filepath.Join(dir, telemetry.SummaryFile))
	if err != nil {
		return nil, false, nil // never finalized (or no telemetry at all)
	}
	if !summarySampled(summary) {
		return nil, false, nil // upstream said: do not sample
	}

	f, err := os.Open(filepath.Join(dir, telemetry.TelemetryFile))
	if err != nil {
		return nil, false, nil
	}
	defer f.Close()

	starts := map[string]event{}
	ends := map[string]event{}
	var order []string // span ids in file (start-line) order, for determinism
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e event
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber() // keep numbers exact: 2^53+1 must not round through float64
		if dec.Decode(&e) != nil || e.SpanID == "" {
			continue
		}
		switch e.Event {
		case "span_start":
			if _, dup := starts[e.SpanID]; !dup {
				starts[e.SpanID] = e
				order = append(order, e.SpanID)
			}
		case "span_end":
			ends[e.SpanID] = e
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", telemetry.TelemetryFile, err)
	}

	res := buildResource(serviceVersion)
	scope := instrumentation.Scope{Name: scopeName, Version: serviceVersion}

	var spans []sdktrace.ReadOnlySpan
	for _, id := range order {
		start := starts[id]
		end, finished := ends[id]
		if !finished {
			continue // in-flight at crash: no end time, stays file-only
		}
		stub, ok := buildStub(start, end, starts, res, scope)
		if !ok {
			continue
		}
		spans = append(spans, stub.Snapshot())
	}
	return spans, true, nil
}

// summarySampled extracts the W3C sampled bit from the summary's traceparent.
// Artifacts without a parseable traceparent count as sampled — Level 1 always
// writes one, so this only affects hand-edited files.
func summarySampled(summary []byte) bool {
	var s struct {
		Traceparent string `json:"traceparent"`
	}
	if json.Unmarshal(summary, &s) != nil {
		return true
	}
	_, _, flags, ok := telemetry.ParseTraceParent(s.Traceparent)
	if !ok {
		return true
	}
	bits, err := strconv.ParseUint(flags, 16, 8)
	if err != nil {
		return true
	}
	return bits&0x01 != 0
}

// buildStub converts a span_start/span_end pair into a span snapshot stub.
// tracetest.SpanStub is the SDK's only public ReadOnlySpan constructor
// (ReadOnlySpan has an unexported method), and the replay design needs exact
// control of ids and timestamps so the exported span is identical to the one
// in the Level 1 file.
func buildStub(start, end event, starts map[string]event, res *resource.Resource, scope instrumentation.Scope) (tracetest.SpanStub, bool) {
	tid, err := trace.TraceIDFromHex(start.TraceID)
	if err != nil {
		return tracetest.SpanStub{}, false
	}
	sid, err := trace.SpanIDFromHex(start.SpanID)
	if err != nil {
		return tracetest.SpanStub{}, false
	}
	startTime, err := time.Parse(time.RFC3339Nano, start.TS)
	if err != nil {
		return tracetest.SpanStub{}, false
	}
	endTime, err := time.Parse(time.RFC3339Nano, end.TS)
	if err != nil {
		return tracetest.SpanStub{}, false
	}

	// Parent: a span id recorded in this file is a local parent; anything
	// else is the remote parent adopted from an inbound TRACEPARENT (#2779).
	// Only export happens when sampled, so flags are always FlagsSampled.
	parent := trace.SpanContext{}
	kind := trace.SpanKindInternal
	if start.Parent != "" {
		psid, err := trace.SpanIDFromHex(start.Parent)
		if err != nil {
			return tracetest.SpanStub{}, false
		}
		_, local := starts[start.Parent]
		parent = trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid, SpanID: psid, TraceFlags: trace.FlagsSampled, Remote: !local,
		})
		if !local {
			// A dispatched run's root span consumes work from the parent
			// pipeline (matches the published SpanKind contract).
			kind = trace.SpanKindConsumer
		}
	}

	status := sdktrace.Status{Code: codes.Unset}
	switch end.Status {
	case "ok":
		status.Code = codes.Ok
	case "error":
		status.Code = codes.Error
	}

	return tracetest.SpanStub{
		Name: start.Name,
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled,
		}),
		Parent:               parent,
		SpanKind:             kind,
		StartTime:            startTime,
		EndTime:              endTime,
		Attributes:           mergedAttrs(start, end),
		Status:               status,
		Resource:             res,
		InstrumentationScope: scope,
	}, true
}

// mergedAttrs merges span_start and span_end attributes (end wins on key
// conflicts) plus the per-line work-item id, in sorted key order for
// deterministic output.
func mergedAttrs(start, end event) []attribute.KeyValue {
	merged := map[string]any{}
	for k, v := range start.Attrs {
		merged[k] = v
	}
	for k, v := range end.Attrs {
		merged[k] = v
	}
	if wi := firstNonEmpty(end.WorkItemID, start.WorkItemID); wi != "" {
		merged["fullsend.work_item_id"] = wi
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	attrs := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		if kv, ok := attrKV(k, merged[k]); ok {
			attrs = append(attrs, kv)
		}
	}
	return attrs
}

// attrKV maps a decoded JSON attribute value onto the matching OTel
// attribute type. Nothing is dropped and nothing panics — an attribute of an
// unexpected shape degrades to its string form rather than disappearing.
func attrKV(k string, v any) (attribute.KeyValue, bool) {
	switch val := v.(type) {
	case nil:
		return attribute.KeyValue{}, false
	case string:
		return attribute.String(k, val), true
	case bool:
		return attribute.Bool(k, val), true
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return attribute.Int64(k, i), true
		}
		if f, err := val.Float64(); err == nil {
			return attribute.Float64(k, f), true
		}
		return attribute.String(k, val.String()), true
	default:
		return attribute.String(k, fmt.Sprint(val)), true
	}
}

// buildResource assembles the export Resource: fullsend's identity plus any
// standard env overrides (OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES) —
// env detectors run last so operator configuration wins. Plain attribute
// keys are used instead of semconv constants: newer semconv versions renamed
// keys this package's contract (ADR 0050) pins, and service.name/version are
// stable strings.
func buildResource(serviceVersion string) *resource.Resource {
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			attribute.String("service.name", "fullsend"),
			attribute.String("service.version", serviceVersion),
		),
		resource.WithFromEnv(),
	)
	if err != nil || res == nil {
		return resource.NewSchemaless(
			attribute.String("service.name", "fullsend"),
			attribute.String("service.version", serviceVersion),
		)
	}
	return res
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
