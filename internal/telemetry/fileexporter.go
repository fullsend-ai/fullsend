package telemetry

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// fileExporter writes spans as OTLP JSON (one TracesData per line).
// Trace/span IDs are hex-encoded per the OTLP JSON spec, not base64.
// Disables itself on first I/O error to avoid repeated stderr noise.
type fileExporter struct {
	mu     sync.Mutex
	f      *os.File
	failed bool
}

func newFileExporter(f *os.File) sdktrace.SpanExporter {
	return &fileExporter{f: f}
}

func (e *fileExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	if e.failed {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	rs := buildResourceSpans(spans)
	if len(rs) == 0 {
		return nil
	}
	b, err := json.Marshal(otlpTracesData{ResourceSpans: rs})
	if err != nil {
		return err
	}
	b = append(b, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed {
		return nil
	}
	if _, err = e.f.Write(b); err != nil {
		e.failed = true
		return err
	}
	if err = e.f.Sync(); err != nil {
		e.failed = true
		return err
	}
	return nil
}

func (e *fileExporter) Shutdown(context.Context) error { return nil }

// --- OTLP JSON types (hex trace IDs per spec) ---

type otlpTracesData struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   *otlpResource    `json:"resource,omitempty"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
	SchemaURL  string           `json:"schemaUrl,omitempty"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpScopeSpans struct {
	Scope     *otlpScope `json:"scope,omitempty"`
	Spans     []otlpSpan `json:"spans"`
	SchemaURL string     `json:"schemaUrl,omitempty"`
}

type otlpScope struct {
	Name       string         `json:"name,omitempty"`
	Version    string         `json:"version,omitempty"`
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpSpan struct {
	TraceID                string         `json:"traceId"`
	SpanID                 string         `json:"spanId"`
	ParentSpanID           string         `json:"parentSpanId,omitempty"`
	TraceState             string         `json:"traceState,omitempty"`
	Flags                  uint32         `json:"flags,omitempty"`
	Name                   string         `json:"name"`
	Kind                   int            `json:"kind"`
	StartTimeUnixNano      string         `json:"startTimeUnixNano"`
	EndTimeUnixNano        string         `json:"endTimeUnixNano"`
	Attributes             []otlpKeyValue `json:"attributes,omitempty"`
	Events                 []otlpEvent    `json:"events,omitempty"`
	Links                  []otlpLink     `json:"links,omitempty"`
	Status                 *otlpStatus    `json:"status,omitempty"`
	DroppedAttributesCount uint32         `json:"droppedAttributesCount,omitempty"`
	DroppedEventsCount     uint32         `json:"droppedEventsCount,omitempty"`
	DroppedLinksCount      uint32         `json:"droppedLinksCount,omitempty"`
}

type otlpEvent struct {
	TimeUnixNano           string         `json:"timeUnixNano"`
	Name                   string         `json:"name"`
	Attributes             []otlpKeyValue `json:"attributes,omitempty"`
	DroppedAttributesCount uint32         `json:"droppedAttributesCount,omitempty"`
}

type otlpLink struct {
	TraceID                string         `json:"traceId"`
	SpanID                 string         `json:"spanId"`
	TraceState             string         `json:"traceState,omitempty"`
	Attributes             []otlpKeyValue `json:"attributes,omitempty"`
	Flags                  uint32         `json:"flags,omitempty"`
	DroppedAttributesCount uint32         `json:"droppedAttributesCount,omitempty"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpKeyValue struct {
	Key   string  `json:"key"`
	Value otlpAny `json:"value"`
}

// otlpAny uses a custom marshaler to emit exactly one value field.
type otlpAny struct {
	v any
}

func (a otlpAny) MarshalJSON() ([]byte, error) { return json.Marshal(a.v) }

// --- SDK span → OTLP JSON conversion ---

func buildResourceSpans(sdl []sdktrace.ReadOnlySpan) []otlpResourceSpans {
	if len(sdl) == 0 {
		return nil
	}

	type rsKey = attribute.Distinct
	type ssKey struct {
		r  attribute.Distinct
		is instrumentation.Scope
	}

	type rsEntry struct {
		resource  *otlpResource
		schemaURL string
		scopes    []ssKey // insertion order
	}

	rsm := make(map[rsKey]*rsEntry)
	ssm := make(map[ssKey]*otlpScopeSpans)
	var rsOrder []rsKey // insertion order

	for _, sd := range sdl {
		if sd == nil {
			continue
		}

		rKey := sd.Resource().Equivalent()
		k := ssKey{r: rKey, is: sd.InstrumentationScope()}

		ss, iOk := ssm[k]
		if !iOk {
			ss = &otlpScopeSpans{
				Scope:     convertScope(sd.InstrumentationScope()),
				Spans:     []otlpSpan{},
				SchemaURL: sd.InstrumentationScope().SchemaURL,
			}
			ssm[k] = ss
		}
		ss.Spans = append(ss.Spans, convertSpan(sd))

		rs, rOk := rsm[rKey]
		if !rOk {
			rs = &rsEntry{
				resource:  convertResource(sd.Resource()),
				schemaURL: sd.Resource().SchemaURL(),
			}
			rsm[rKey] = rs
			rsOrder = append(rsOrder, rKey)
		}
		if !iOk {
			rs.scopes = append(rs.scopes, k)
		}
	}

	out := make([]otlpResourceSpans, 0, len(rsOrder))
	for _, rKey := range rsOrder {
		rs := rsm[rKey]
		scopes := make([]otlpScopeSpans, len(rs.scopes))
		for i, sk := range rs.scopes {
			scopes[i] = *ssm[sk]
		}
		out = append(out, otlpResourceSpans{
			Resource:   rs.resource,
			ScopeSpans: scopes,
			SchemaURL:  rs.schemaURL,
		})
	}
	return out
}

func convertSpan(sd sdktrace.ReadOnlySpan) otlpSpan {
	tid := sd.SpanContext().TraceID()
	sid := sd.SpanContext().SpanID()

	s := otlpSpan{
		TraceID:                hex.EncodeToString(tid[:]),
		SpanID:                 hex.EncodeToString(sid[:]),
		TraceState:             sd.SpanContext().TraceState().String(),
		Flags:                  buildSpanFlags(sd.SpanContext().TraceFlags(), sd.Parent()),
		Name:                   sd.Name(),
		Kind:                   int(sd.SpanKind()),
		StartTimeUnixNano:      fmt.Sprintf("%d", max(0, sd.StartTime().UnixNano())),
		EndTimeUnixNano:        fmt.Sprintf("%d", max(0, sd.EndTime().UnixNano())),
		Attributes:             convertAttrs(sd.Attributes()),
		Events:                 convertEvents(sd.Events()),
		Links:                  convertLinks(sd.Links()),
		Status:                 convertStatus(sd.Status()),
		DroppedAttributesCount: clampUint32(sd.DroppedAttributes()),
		DroppedEventsCount:     clampUint32(sd.DroppedEvents()),
		DroppedLinksCount:      clampUint32(sd.DroppedLinks()),
	}

	if psid := sd.Parent().SpanID(); psid.IsValid() {
		s.ParentSpanID = hex.EncodeToString(psid[:])
	}
	return s
}

func convertStatus(st sdktrace.Status) *otlpStatus {
	var c int
	switch st.Code {
	case codes.Ok:
		c = 1
	case codes.Error:
		c = 2
	}
	if c == 0 && st.Description == "" {
		return nil
	}
	return &otlpStatus{Code: c, Message: st.Description}
}

func convertEvents(es []sdktrace.Event) []otlpEvent {
	if len(es) == 0 {
		return nil
	}
	out := make([]otlpEvent, len(es))
	for i := range es {
		out[i] = otlpEvent{
			TimeUnixNano:           fmt.Sprintf("%d", max(0, es[i].Time.UnixNano())),
			Name:                   es[i].Name,
			Attributes:             convertAttrs(es[i].Attributes),
			DroppedAttributesCount: clampUint32(es[i].DroppedAttributeCount),
		}
	}
	return out
}

func convertLinks(links []sdktrace.Link) []otlpLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]otlpLink, 0, len(links))
	for _, l := range links {
		tid := l.SpanContext.TraceID()
		sid := l.SpanContext.SpanID()
		out = append(out, otlpLink{
			TraceID:                hex.EncodeToString(tid[:]),
			SpanID:                 hex.EncodeToString(sid[:]),
			Attributes:             convertAttrs(l.Attributes),
			Flags:                  buildSpanFlags(l.SpanContext.TraceFlags(), l.SpanContext),
			DroppedAttributesCount: clampUint32(l.DroppedAttributeCount),
		})
	}
	return out
}

func convertResource(r *resource.Resource) *otlpResource {
	if r == nil {
		return nil
	}
	return &otlpResource{Attributes: convertIterAttrs(r.Iter())}
}

func convertScope(il instrumentation.Scope) *otlpScope {
	if il == (instrumentation.Scope{}) {
		return nil
	}
	return &otlpScope{
		Name:       il.Name,
		Version:    il.Version,
		Attributes: convertIterAttrs(il.Attributes.Iter()),
	}
}

func convertAttrs(attrs []attribute.KeyValue) []otlpKeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]otlpKeyValue, 0, len(attrs))
	for _, kv := range attrs {
		out = append(out, otlpKeyValue{Key: string(kv.Key), Value: convertValue(kv.Value)})
	}
	return out
}

func convertIterAttrs(iter attribute.Iterator) []otlpKeyValue {
	l := iter.Len()
	if l == 0 {
		return nil
	}
	out := make([]otlpKeyValue, 0, l)
	for iter.Next() {
		kv := iter.Attribute()
		out = append(out, otlpKeyValue{Key: string(kv.Key), Value: convertValue(kv.Value)})
	}
	return out
}

func convertValue(v attribute.Value) otlpAny {
	switch v.Type() {
	case attribute.BOOL:
		return otlpAny{map[string]any{"boolValue": v.AsBool()}}
	case attribute.INT64:
		return otlpAny{map[string]any{"intValue": fmt.Sprintf("%d", v.AsInt64())}}
	case attribute.FLOAT64:
		return otlpAny{map[string]any{"doubleValue": sanitizeFloat64(v.AsFloat64())}}
	case attribute.STRING:
		return otlpAny{map[string]any{"stringValue": v.AsString()}}
	case attribute.BOOLSLICE:
		return otlpAny{map[string]any{"arrayValue": map[string]any{"values": boolSliceValues(v.AsBoolSlice())}}}
	case attribute.INT64SLICE:
		return otlpAny{map[string]any{"arrayValue": map[string]any{"values": int64SliceValues(v.AsInt64Slice())}}}
	case attribute.FLOAT64SLICE:
		return otlpAny{map[string]any{"arrayValue": map[string]any{"values": float64SliceValues(v.AsFloat64Slice())}}}
	case attribute.STRINGSLICE:
		return otlpAny{map[string]any{"arrayValue": map[string]any{"values": stringSliceValues(v.AsStringSlice())}}}
	case attribute.BYTESLICE:
		return otlpAny{map[string]any{"bytesValue": v.AsByteSlice()}}
	default:
		return otlpAny{map[string]any{"stringValue": "INVALID"}}
	}
}

func boolSliceValues(vals []bool) []map[string]any {
	out := make([]map[string]any, len(vals))
	for i, v := range vals {
		out[i] = map[string]any{"boolValue": v}
	}
	return out
}

func int64SliceValues(vals []int64) []map[string]any {
	out := make([]map[string]any, len(vals))
	for i, v := range vals {
		out[i] = map[string]any{"intValue": fmt.Sprintf("%d", v)}
	}
	return out
}

func float64SliceValues(vals []float64) []map[string]any {
	out := make([]map[string]any, len(vals))
	for i, v := range vals {
		out[i] = map[string]any{"doubleValue": sanitizeFloat64(v)}
	}
	return out
}

// sanitizeFloat64 encodes non-finite floats as proto3 JSON strings per
// https://protobuf.dev/programming-guides/proto3/#json (NaN → "NaN", etc.)
// so json.Marshal never fails on a span attribute.
func sanitizeFloat64(v float64) any {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "Infinity"
	}
	if math.IsInf(v, -1) {
		return "-Infinity"
	}
	return v
}

func stringSliceValues(vals []string) []map[string]any {
	out := make([]map[string]any, len(vals))
	for i, v := range vals {
		out[i] = map[string]any{"stringValue": v}
	}
	return out
}

func buildSpanFlags(tf trace.TraceFlags, parent trace.SpanContext) uint32 {
	flags := uint32(tf) | 0x100 // SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK
	if parent.IsRemote() {
		flags |= 0x200 // SPAN_FLAGS_CONTEXT_IS_REMOTE_MASK
	}
	return flags
}

func clampUint32(v int) uint32 {
	if v < 0 {
		return 0
	}
	if int64(v) > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}
