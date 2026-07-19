package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceparent(t *testing.T) {
	tid, _ := trace.TraceIDFromHex("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d")
	sid, _ := trace.SpanIDFromHex("a1b2c3d4e5f60718")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	assert.Equal(t, "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01", Traceparent(sc))
}

func TestTraceparent_ZeroSpanContext(t *testing.T) {
	assert.Equal(t, "", Traceparent(trace.SpanContext{}), "invalid span context must return empty string")
}

func TestTraceparent_AllZeroIDs(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{},
		SpanID:     trace.SpanID{},
		TraceFlags: trace.FlagsSampled,
	})
	assert.Equal(t, "", Traceparent(sc), "all-zero IDs are invalid per W3C, must return empty string")
}

func TestTraceparentWithFlags_PreservesUnsampledFlag(t *testing.T) {
	tid, _ := trace.TraceIDFromHex("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d")
	sid, _ := trace.SpanIDFromHex("a1b2c3d4e5f60718")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	got := TraceparentWithFlags(sc, 0)
	assert.Equal(t, "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-00", got,
		"must not re-advertise as sampled when caller passed unsampled flags")
}

func TestTraceparentWithFlags_InvalidReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", TraceparentWithFlags(trace.SpanContext{}, trace.FlagsSampled))
}
