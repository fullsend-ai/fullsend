package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	eventsFile  = "run-events.jsonl"
	summaryFile = "run-summary.json"
)

// Recorder captures structured lifecycle events alongside OTEL spans.
// It writes events to run-events.jsonl as they occur (crash-safe) and
// produces a run-summary.json at close. When an OTEL tracer is configured,
// each step also produces an OTEL span.
//
// All methods are safe for concurrent use.
type Recorder struct {
	mu             sync.Mutex
	dir            string
	file           *os.File
	enc            *json.Encoder
	tracer         trace.Tracer
	rootCtx        context.Context
	rootSpan       trace.Span
	spans          map[string]spanEntry
	steps          []StepSummary
	start          time.Time
	closed         bool
	summaryEnrich  func(*RunSummary) // optional enrichment callback set before Close
}

type spanEntry struct {
	span  trace.Span
	ctx   context.Context
	start time.Time
}

// RecorderOption configures optional behavior for NewRecorder.
type RecorderOption func(*recorderConfig)

type recorderConfig struct {
	spanKind trace.SpanKind
}

// WithSpanKind sets the OTEL SpanKind on the root span.
func WithSpanKind(kind trace.SpanKind) RecorderOption {
	return func(c *recorderConfig) { c.spanKind = kind }
}

// SpanKindInternal returns a RecorderOption for SpanKindInternal (default).
func SpanKindInternal() RecorderOption {
	return WithSpanKind(trace.SpanKindInternal)
}

// SpanKindConsumer returns a RecorderOption for SpanKindConsumer
// (use when this run was dispatched by an external system).
func SpanKindConsumer() RecorderOption {
	return WithSpanKind(trace.SpanKindConsumer)
}

// NewRecorder creates a Recorder that writes structured events to outputDir.
// The tracer may be nil (noop); OTEL span creation is skipped in that case.
// The returned context carries the root span for the run.
func NewRecorder(ctx context.Context, outputDir string, tracer trace.Tracer, runName string, attrs []Attr, opts ...RecorderOption) (*Recorder, context.Context, error) {
	cfg := &recorderConfig{spanKind: trace.SpanKindInternal}
	for _, o := range opts {
		o(cfg)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, ctx, fmt.Errorf("creating telemetry dir: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(outputDir, eventsFile),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, ctx, fmt.Errorf("opening events file: %w", err)
	}

	r := &Recorder{
		dir:    outputDir,
		file:   f,
		enc:    json.NewEncoder(f),
		tracer: tracer,
		spans:  make(map[string]spanEntry),
		start:  time.Now(),
	}

	var rootCtx context.Context
	if tracer != nil {
		var rootSpan trace.Span
		otelAttrs := attrsToOTEL(attrs)
		rootCtx, rootSpan = tracer.Start(ctx, runName,
			trace.WithAttributes(otelAttrs...),
			trace.WithSpanKind(cfg.spanKind),
		)
		r.rootCtx = rootCtx
		r.rootSpan = rootSpan
	} else {
		rootCtx = ctx
		r.rootCtx = ctx
	}

	r.record(RunEvent{
		Timestamp: time.Now().UTC(),
		Event:     EventRunStart,
		Step:      runName,
		Attrs:     attrsToMap(attrs),
		TraceID:   r.traceID(),
	})

	return r, rootCtx, nil
}

// StepStart records the beginning of a lifecycle step.
// Returns a context that may carry a child span.
func (r *Recorder) StepStart(ctx context.Context, name string, attrs ...Attr) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	event := RunEvent{
		Timestamp: now.UTC(),
		Event:     EventStepStart,
		Step:      name,
		Attrs:     attrsToMap(attrs),
		TraceID:   r.traceID(),
	}

	var stepCtx context.Context
	if r.tracer != nil {
		parentCtx := r.rootCtx
		if ctx != nil {
			parentCtx = ctx
		}
		otelAttrs := attrsToOTEL(attrs)
		var span trace.Span
		stepCtx, span = r.tracer.Start(parentCtx, name, trace.WithAttributes(otelAttrs...))
		r.spans[name] = spanEntry{span: span, ctx: stepCtx, start: now}
		event.SpanID = span.SpanContext().SpanID().String()
		if parentSC := trace.SpanContextFromContext(parentCtx); parentSC.IsValid() {
			event.ParentID = parentSC.SpanID().String()
		}
	} else {
		stepCtx = ctx
	}

	r.recordLocked(event)
	return stepCtx
}

// StepDone records successful completion of a lifecycle step.
func (r *Recorder) StepDone(name string, attrs ...Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	event := RunEvent{
		Timestamp: now.UTC(),
		Event:     EventStepDone,
		Step:      name,
		Status:    StatusOK,
		Attrs:     attrsToMap(attrs),
		TraceID:   r.traceID(),
	}

	if entry, ok := r.spans[name]; ok {
		dur := now.Sub(entry.start).Milliseconds()
		event.DurationMs = &dur
		event.SpanID = entry.span.SpanContext().SpanID().String()
		for _, a := range attrs {
			entry.span.SetAttributes(attribute.String(a.Key, a.Value))
		}
		entry.span.SetStatus(codes.Ok, "")
		entry.span.End()
		delete(r.spans, name)
		r.steps = append(r.steps, StepSummary{Name: name, Status: StatusOK, DurationMs: dur})
	} else {
		r.steps = append(r.steps, StepSummary{Name: name, Status: StatusOK})
	}

	r.recordLocked(event)
}

// StepFail records a failed lifecycle step.
func (r *Recorder) StepFail(name string, err error, attrs ...Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	event := RunEvent{
		Timestamp: now.UTC(),
		Event:     EventStepFail,
		Step:      name,
		Status:    StatusError,
		Error:     errMsg,
		Attrs:     attrsToMap(attrs),
		TraceID:   r.traceID(),
	}

	if entry, ok := r.spans[name]; ok {
		dur := now.Sub(entry.start).Milliseconds()
		event.DurationMs = &dur
		event.SpanID = entry.span.SpanContext().SpanID().String()
		if err != nil {
			entry.span.RecordError(err)
		}
		entry.span.SetStatus(codes.Error, errMsg)
		entry.span.End()
		delete(r.spans, name)
		r.steps = append(r.steps, StepSummary{Name: name, Status: StatusError, DurationMs: dur, Error: errMsg})
	} else {
		r.steps = append(r.steps, StepSummary{Name: name, Status: StatusError, Error: errMsg})
	}

	r.recordLocked(event)
}

// StepWarn records a step that completed with warnings.
func (r *Recorder) StepWarn(name string, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	event := RunEvent{
		Timestamp: time.Now().UTC(),
		Event:     EventStepWarn,
		Step:      name,
		Status:    StatusWarning,
		Error:     detail,
		TraceID:   r.traceID(),
	}

	if entry, ok := r.spans[name]; ok {
		dur := time.Since(entry.start).Milliseconds()
		event.DurationMs = &dur
		event.SpanID = entry.span.SpanContext().SpanID().String()
		entry.span.SetStatus(codes.Ok, detail)
		entry.span.End()
		delete(r.spans, name)
		r.steps = append(r.steps, StepSummary{Name: name, Status: StatusWarning, DurationMs: dur})
	} else {
		r.steps = append(r.steps, StepSummary{Name: name, Status: StatusWarning})
	}

	r.recordLocked(event)
}

// Context returns the root span context for propagation to subprocesses.
func (r *Recorder) Context() context.Context {
	return r.rootCtx
}

// StartTime returns when the recorder was created.
func (r *Recorder) StartTime() time.Time {
	return r.start
}

// SetSummaryFields registers a function that will be called to enrich the
// RunSummary before WriteSummary writes it to disk. This allows the caller
// to set fields that are only known at the end of the run (exit code,
// validation status) while the defer block handles the actual write.
func (r *Recorder) SetSummaryFields(fn func(*RunSummary)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summaryEnrich = fn
}

// Steps returns the accumulated step summaries.
func (r *Recorder) Steps() []StepSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]StepSummary, len(r.steps))
	copy(out, r.steps)
	return out
}

// WriteSummary writes the run-summary.json file. If SetSummaryFields was
// called, the enrichment function is applied before writing.
func (r *Recorder) WriteSummary(summary RunSummary) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.summaryEnrich != nil {
		r.summaryEnrich(&summary)
	}

	summary.SchemaVersion = SchemaVersion
	summary.Steps = make([]StepSummary, len(r.steps))
	copy(summary.Steps, r.steps)
	summary.DurationMs = time.Since(r.start).Milliseconds()
	if summary.EndTime.IsZero() {
		summary.EndTime = time.Now().UTC()
	}
	if summary.TraceID == "" {
		summary.TraceID = r.traceID()
	}
	if summary.Traceparent == "" {
		summary.Traceparent = Traceparent(r.rootCtx)
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling run summary: %w", err)
	}
	path := filepath.Join(r.dir, summaryFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing run summary: %w", err)
	}
	return nil
}

// Close ends the root span, writes a run.done event, and closes the events file.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}

	// End any orphaned step spans.
	for name, entry := range r.spans {
		entry.span.SetStatus(codes.Error, "step not completed before recorder close")
		entry.span.End()
		delete(r.spans, name)
	}

	dur := time.Since(r.start).Milliseconds()
	r.recordLocked(RunEvent{
		Timestamp:  time.Now().UTC(),
		Event:      EventRunDone,
		DurationMs: &dur,
		TraceID:    r.traceID(),
	})

	r.closed = true

	if r.rootSpan != nil {
		r.rootSpan.End()
	}

	return r.file.Close()
}

func (r *Recorder) record(event RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordLocked(event)
}

func (r *Recorder) recordLocked(event RunEvent) {
	if r.enc != nil && !r.closed {
		_ = r.enc.Encode(event) // best-effort; telemetry must not break the run
	}
}

func (r *Recorder) traceID() string {
	if r.rootSpan != nil {
		sc := r.rootSpan.SpanContext()
		if sc.IsValid() {
			return sc.TraceID().String()
		}
	}
	return ""
}

func attrsToOTEL(attrs []Attr) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, len(attrs))
	for i, a := range attrs {
		out[i] = attribute.String(a.Key, a.Value)
	}
	return out
}
