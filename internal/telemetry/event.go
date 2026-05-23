package telemetry

import "time"

const SchemaVersion = "1"

// RunEvent is a single lifecycle event emitted to run-events.jsonl.
// Designed with OTEL span semantics so events can be promoted to spans
// without schema changes.
type RunEvent struct {
	Timestamp  time.Time         `json:"ts"`
	Event      string            `json:"event"`
	Step       string            `json:"step"`
	DurationMs *int64            `json:"duration_ms,omitempty"`
	Status     string            `json:"status,omitempty"`
	Error      string            `json:"error,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	TraceID    string            `json:"trace_id,omitempty"`
	SpanID     string            `json:"span_id,omitempty"`
	ParentID   string            `json:"parent_span_id,omitempty"`
}

// Event type constants.
const (
	EventStepStart = "step.start"
	EventStepDone  = "step.done"
	EventStepFail  = "step.fail"
	EventStepWarn  = "step.warn"
	EventRunStart  = "run.start"
	EventRunDone   = "run.done"
)

// Status constants.
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusWarning = "warning"
	StatusSkipped = "skipped"
)

// RunSummary is the top-level metadata written to run-summary.json at the
// end of an agent run. It provides a machine-stable contract for downstream
// consumers to ingest without parsing CLI stdout.
type RunSummary struct {
	SchemaVersion   string            `json:"schema_version"`
	Agent           string            `json:"agent"`
	Harness         string            `json:"harness"`
	Model           string            `json:"model,omitempty"`
	Image           string            `json:"image,omitempty"`
	WorkItemID      string            `json:"work_item_id,omitempty"`
	TraceID         string            `json:"trace_id,omitempty"`
	SecurityTraceID string            `json:"security_trace_id,omitempty"`
	Traceparent     string            `json:"traceparent,omitempty"`
	StartTime       time.Time         `json:"start_time"`
	EndTime         time.Time         `json:"end_time"`
	DurationMs      int64             `json:"duration_ms"`
	ExitCode        int               `json:"exit_code"`
	Iterations      int               `json:"iterations"`
	Validation      *ValidationResult `json:"validation,omitempty"`
	Steps           []StepSummary     `json:"steps"`
	Attrs           map[string]string `json:"attrs,omitempty"`
}

// ValidationResult captures the outcome of the validation loop.
type ValidationResult struct {
	Configured bool   `json:"configured"`
	Passed     bool   `json:"passed"`
	Iterations int    `json:"iterations"`
	Status     string `json:"status"`
}

// StepSummary captures the outcome of a single lifecycle step.
type StepSummary struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// Attr is a key-value attribute pair passed to recorder methods.
type Attr struct {
	Key   string
	Value string
}

// StringAttr creates an Attr.
func StringAttr(key, value string) Attr {
	return Attr{Key: key, Value: value}
}

// attrsToMap converts a slice of Attr to a map.
func attrsToMap(attrs []Attr) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value
	}
	return m
}
