package telemetry

import "go.opentelemetry.io/otel/trace"

// Traceparent returns the W3C traceparent header value for a span context.
// Returns "" for invalid (zero) span contexts so callers can omit the header
// rather than emitting an all-zero value that W3C forbids.
func Traceparent(sc trace.SpanContext) string {
	return TraceparentWithFlags(sc, sc.TraceFlags())
}

// TraceparentWithFlags formats a W3C traceparent using sc's trace/span IDs
// but with explicit flags. Use this to propagate a sampling decision that
// differs from the local span's (e.g. preserving an upstream unsampled bit
// that AlwaysSample overrode locally).
func TraceparentWithFlags(sc trace.SpanContext, flags trace.TraceFlags) string {
	if !sc.IsValid() {
		return ""
	}
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + flags.String()
}
