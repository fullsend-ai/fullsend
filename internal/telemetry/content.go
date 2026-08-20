package telemetry

import (
	"os"
	"strings"
)

// ContentCaptureEnvVar is the Level 3 content-capture opt-in named by
// ADR 0050. The variable name and its value vocabulary come from the
// OpenTelemetry GenAI instrumentation convention (documented by the
// opentelemetry-python-contrib GenAI instrumentations); the semantic
// conventions specification itself does not define this variable.
// Fullsend's runner is the GenAI instrumentation that reads it — it is
// never passed to the agent runtime, whose own content-logging variables
// (OTEL_LOG_*) fullsend never sets.
const ContentCaptureEnvVar = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"

// ContentCaptureEnabled reports whether Level 3 content capture is on.
//
// Fullsend records content on span attributes only, so the affirmative
// values are the ones whose intent span recording can honor: "true"
// (legacy boolean form), "span_only", and "span_and_event". "event_only"
// is off — fullsend implements no event-based capture, and recording on
// spans would contradict the operator's "only". "NO_CONTENT", "false",
// unset, and anything unrecognized are off: telemetry never fails a run
// (ADR 0050), so an unexpected value disables capture rather than
// erroring.
func ContentCaptureEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ContentCaptureEnvVar))) {
	case "true", "span_only", "span_and_event":
		return true
	default:
		return false
	}
}
