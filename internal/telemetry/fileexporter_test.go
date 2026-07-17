package telemetry

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func tempExporter(t *testing.T) (sdktrace.SpanExporter, func() []byte) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "spans-*.jsonl")
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	read := func() []byte {
		b, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		return b
	}
	return newFileExporter(f), read
}

func TestFileExporter_RoundTrip(t *testing.T) {
	exp, readFile := tempExporter(t)

	tid := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	sid := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})

	now := time.Now()
	stub := tracetest.SpanStub{
		Name:        "test-span",
		SpanContext: sc,
		SpanKind:    trace.SpanKindServer,
		StartTime:   now,
		EndTime:     now.Add(100 * time.Millisecond),
		Attributes: []attribute.KeyValue{
			attribute.String("key", "value"),
			attribute.Int64("count", 42),
		},
		Status: sdktrace.Status{Code: codes.Ok},
		Resource: resource.NewSchemaless(
			attribute.String("service.name", "test"),
		),
		InstrumentationScope: instrumentation.Scope{
			Name:    "test-scope",
			Version: "1.0.0",
		},
	}

	spans := []sdktrace.ReadOnlySpan{stub.Snapshot()}
	require.NoError(t, exp.ExportSpans(context.Background(), spans))

	data := readFile()
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	rsList, ok := parsed["resourceSpans"].([]any)
	require.True(t, ok)
	require.Len(t, rsList, 1)

	raw := string(data)
	assert.Contains(t, raw, `"traceId":"0102030405060708090a0b0c0d0e0f10"`)
	assert.Contains(t, raw, `"spanId":"0102030405060708"`)
	assert.Contains(t, raw, `"name":"test-span"`)
	assert.Contains(t, raw, `"name":"test-scope"`)
	assert.Contains(t, raw, `"version":"1.0.0"`)
	assert.Contains(t, raw, `"kind":2`) // SpanKindServer
	assert.Contains(t, raw, `"code":1`) // STATUS_CODE_OK
	assert.Contains(t, raw, `"stringValue":"value"`)
	assert.Contains(t, raw, `"intValue":"42"`)
	assert.Contains(t, raw, `"stringValue":"test"`) // service.name
}

func TestFileExporter_EmptySpans(t *testing.T) {
	exp, readFile := tempExporter(t)

	require.NoError(t, exp.ExportSpans(context.Background(), nil))
	assert.Empty(t, readFile())

	require.NoError(t, exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{}))
	assert.Empty(t, readFile())
}

func TestFileExporter_MultiResource(t *testing.T) {
	exp, readFile := tempExporter(t)

	r1 := resource.NewSchemaless(attribute.String("service.name", "svc-a"))
	r2 := resource.NewSchemaless(attribute.String("service.name", "svc-b"))

	stubs := tracetest.SpanStubs{
		{
			Name:     "span-a",
			Resource: r1,
			SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
			}),
		},
		{
			Name:     "span-b",
			Resource: r2,
			SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{2}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
			}),
		},
	}

	require.NoError(t, exp.ExportSpans(context.Background(), stubs.Snapshots()))

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(readFile(), &parsed))
	rsList, ok := parsed["resourceSpans"].([]any)
	require.True(t, ok)
	assert.Len(t, rsList, 2)
}

func TestBuildResourceSpans_SameResourceScope(t *testing.T) {
	r := resource.NewSchemaless(attribute.String("service.name", "svc"))
	scope := instrumentation.Scope{Name: "s", Version: "1"}

	stubs := tracetest.SpanStubs{
		{
			Name: "span-1", Resource: r, InstrumentationScope: scope,
			SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
			}),
		},
		{
			Name: "span-2", Resource: r, InstrumentationScope: scope,
			SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
			}),
		},
		{
			Name: "span-3", Resource: r, InstrumentationScope: scope,
			SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{3}, TraceFlags: trace.FlagsSampled,
			}),
		},
	}

	rs := buildResourceSpans(stubs.Snapshots())
	require.Len(t, rs, 1)
	require.Len(t, rs[0].ScopeSpans, 1)
	assert.Len(t, rs[0].ScopeSpans[0].Spans, 3, "all spans with same resource+scope must appear")

	names := make([]string, len(rs[0].ScopeSpans[0].Spans))
	for i, s := range rs[0].ScopeSpans[0].Spans {
		names[i] = s.Name
	}
	assert.ElementsMatch(t, []string{"span-1", "span-2", "span-3"}, names)
}

func TestFileExporter_AllAttributeTypesEventsLinks(t *testing.T) {
	exp, readFile := tempExporter(t)

	tid := trace.TraceID{0xaa}
	sid := trace.SpanID{0xbb}
	parentSID := trace.SpanID{0xcc}
	linkTID := trace.TraceID{0xdd}
	linkSID := trace.SpanID{0xee}

	now := time.Now()
	stub := tracetest.SpanStub{
		Name: "full-span",
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled,
		}),
		Parent: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid, SpanID: parentSID, TraceFlags: trace.FlagsSampled, Remote: true,
		}),
		SpanKind:  trace.SpanKindClient,
		StartTime: now,
		EndTime:   now.Add(50 * time.Millisecond),
		Attributes: []attribute.KeyValue{
			attribute.String("s", "v"),
			attribute.Int64("i", 7),
			attribute.Float64("f", 3.14),
			attribute.Bool("b", true),
			attribute.StringSlice("ss", []string{"a", "b"}),
			attribute.Int64Slice("is", []int64{1, 2}),
			attribute.Float64Slice("fs", []float64{1.1, 2.2}),
			attribute.BoolSlice("bs", []bool{true, false}),
		},
		Events: []sdktrace.Event{
			{Name: "evt", Time: now, Attributes: []attribute.KeyValue{attribute.String("ek", "ev")}},
		},
		Links: []sdktrace.Link{
			{SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: linkTID, SpanID: linkSID, TraceFlags: trace.FlagsSampled,
			}), Attributes: []attribute.KeyValue{attribute.String("lk", "lv")}},
		},
		Status:   sdktrace.Status{Code: codes.Error, Description: "boom"},
		Resource: resource.NewSchemaless(attribute.String("service.name", "test")),
	}

	require.NoError(t, exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{stub.Snapshot()}))

	raw := string(readFile())

	// Parent span ID present.
	assert.Contains(t, raw, `"parentSpanId":"cc00000000000000"`)

	// All attribute value types.
	assert.Contains(t, raw, `"stringValue":"v"`)
	assert.Contains(t, raw, `"intValue":"7"`)
	assert.Contains(t, raw, `"doubleValue":3.14`)
	assert.Contains(t, raw, `"boolValue":true`)
	assert.Contains(t, raw, `"arrayValue"`)

	// Event.
	assert.Contains(t, raw, `"name":"evt"`)
	assert.Contains(t, raw, `"stringValue":"ev"`)

	// Link.
	assert.Contains(t, raw, `"traceId":"dd000000000000000000000000000000"`)
	assert.Contains(t, raw, `"stringValue":"lv"`)

	// Error status.
	assert.Contains(t, raw, `"code":2`)
	assert.Contains(t, raw, `"message":"boom"`)

	// Remote parent flag.
	assert.Contains(t, raw, `"flags"`)
}

func TestConvertStatus_Unset(t *testing.T) {
	s := convertStatus(sdktrace.Status{Code: codes.Unset})
	assert.Nil(t, s, "unset status with no description returns nil")
}

func TestClampUint32_Edges(t *testing.T) {
	assert.Equal(t, uint32(0), clampUint32(-1))
	assert.Equal(t, uint32(0), clampUint32(0))
	assert.Equal(t, uint32(42), clampUint32(42))
}

func TestConvertValue_ByteSlice(t *testing.T) {
	v := convertValue(attribute.ByteSliceValue([]byte{0xde, 0xad}))
	b, err := json.Marshal(v)
	require.NoError(t, err)
	assert.Contains(t, string(b), "bytesValue")
}

func TestFileExporter_NonFiniteFloat64(t *testing.T) {
	exp, readFile := tempExporter(t)

	stub := tracetest.SpanStub{
		Name: "nan-span",
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
		}),
		Attributes: []attribute.KeyValue{
			attribute.Float64("nan", math.NaN()),
			attribute.Float64("pos_inf", math.Inf(1)),
			attribute.Float64("neg_inf", math.Inf(-1)),
			attribute.Float64Slice("mixed", []float64{1.0, math.NaN(), math.Inf(-1)}),
			attribute.Float64("normal", 3.14),
		},
		Resource: resource.NewSchemaless(attribute.String("service.name", "test")),
	}

	require.NoError(t, exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{stub.Snapshot()}))

	raw := string(readFile())
	assert.Contains(t, raw, `"doubleValue":"NaN"`)
	assert.Contains(t, raw, `"doubleValue":"Infinity"`)
	assert.Contains(t, raw, `"doubleValue":"-Infinity"`)
	assert.Contains(t, raw, `"doubleValue":3.14`)
}

func TestConvertValue_Invalid(t *testing.T) {
	v := convertValue(attribute.Value{})
	b, err := json.Marshal(v)
	require.NoError(t, err)
	assert.Contains(t, string(b), "INVALID")
}

func TestConvertResource_Nil(t *testing.T) {
	assert.Nil(t, convertResource(nil))
}

func TestClampUint32_Overflow(t *testing.T) {
	assert.Equal(t, uint32(math.MaxUint32), clampUint32(math.MaxInt64))
}

func TestFileExporter_HexTraceIDs(t *testing.T) {
	exp, readFile := tempExporter(t)

	stub := tracetest.SpanStub{
		Name: "hex-test",
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    trace.TraceID{0xa8, 0xb7, 0x9f, 0x9e, 0xb2, 0xc9, 0x26, 0x61, 0xed, 0xfe, 0x93, 0x70, 0xdb, 0x46, 0xcb, 0xa4},
			SpanID:     trace.SpanID{0x9f, 0x62, 0xf8, 0x0d, 0x0d, 0xc1, 0xc1, 0xe3},
			TraceFlags: trace.FlagsSampled,
		}),
		Resource: resource.Default(),
	}

	require.NoError(t, exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{stub.Snapshot()}))

	raw := string(readFile())
	assert.Contains(t, raw, `"traceId":"a8b79f9eb2c92661edfe9370db46cba4"`)
	assert.Contains(t, raw, `"spanId":"9f62f80d0dc1c1e3"`)
	assert.NotContains(t, raw, "==") // no base64
}
