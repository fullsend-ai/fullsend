// Package telemetry implements fullsend's Level 1 distributed tracing: a
// zero-config, metadata-only local baseline (per ADR 0050). Every run writes a
// crash-safe NDJSON event stream (run-telemetry.jsonl) and an aggregated
// summary (run-summary.json) to the run output directory.
//
// The package is deliberately dependency-free and safe by construction: a nil
// *Recorder is valid, every method is a no-op when the recorder is nil or
// disabled, and no method returns an error. Any failure in the telemetry path
// disables the recorder rather than affecting the run — graceful degradation
// is a hard requirement.
package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SchemaVersion is the bespoke fullsend telemetry schema version, present on
// every event line and in the summary. Bump on incompatible schema changes.
const SchemaVersion = 1

// TelemetryFile and SummaryFile are the artifact names written to the output dir.
const (
	TelemetryFile = "run-telemetry.jsonl"
	SummaryFile   = "run-summary.json"
)

// eventRecord is one NDJSON line in run-telemetry.jsonl. Metadata only — Attrs
// never contains prompt/completion content (that is Level 3).
type eventRecord struct {
	V          int            `json:"v"`
	Event      string         `json:"event"` // "span_start" | "span_end"
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	Parent     string         `json:"parent"`
	Name       string         `json:"name"`
	TS         string         `json:"ts"`
	WorkItemID string         `json:"fullsend.work_item_id"`
	DurationMS int64          `json:"duration_ms,omitempty"` // span_end only
	Status     string         `json:"status,omitempty"`      // span_end only
	Attrs      map[string]any `json:"attrs,omitempty"`
}

// stepTiming is one entry in the summary's step list.
type stepTiming struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
}

// RunMetrics holds aggregate behavioral metrics for the run, surfaced into the
// summary via SetMetrics. Sourced from fullsend's existing per-run metrics (no
// new accounting). Omitted from the summary when never set.
type RunMetrics struct {
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	TotalCostUSD             float64 `json:"total_cost_usd"`
	NumTurns                 int     `json:"num_turns"`
	ToolCalls                int     `json:"tool_calls"`
}

// runSummary is the content of run-summary.json, written once on Finalize.
type runSummary struct {
	V           int          `json:"v"`
	TraceID     string       `json:"trace_id"`
	Traceparent string       `json:"traceparent"`
	Agent       string       `json:"agent"`
	Model       string       `json:"model,omitempty"`
	WorkItemID  string       `json:"fullsend.work_item_id"`
	ExitCode    int          `json:"exit_code"`
	StartedAt   string       `json:"started_at"`
	EndedAt     string       `json:"ended_at"`
	DurationMS  int64        `json:"duration_ms"`
	Steps       []stepTiming `json:"steps"`
	Metrics     *RunMetrics  `json:"metrics,omitempty"`
}

type spanState struct {
	name   string
	parent string
	start  time.Time
}

// Recorder writes crash-safe NDJSON telemetry and a final summary. The zero
// value is not usable; obtain one from New. A nil *Recorder is a valid no-op.
type Recorder struct {
	mu         sync.Mutex
	f          *os.File
	dir        string
	traceID    string
	rootSpanID string
	agent      string
	model      string
	workItem   string
	start      time.Time
	spans      map[string]*spanState
	steps      []stepTiming
	metrics    *RunMetrics
	disabled   bool
	finalized  bool
}

// New opens <dir>/run-telemetry.jsonl for append and emits the root "run" span
// (backdated to start). traceID is a 32-hex W3C trace-id and rootSpanID a
// 16-hex span-id; both are supplied by the caller so the trace correlates with
// the run's security trace id and with child processes.
//
// New never returns an error: if the file cannot be opened it returns a
// disabled (no-op) recorder so the run is never affected.
func New(dir, traceID, rootSpanID, agent, workItemID string, start time.Time) *Recorder {
	r := &Recorder{
		dir:        dir,
		traceID:    traceID,
		rootSpanID: rootSpanID,
		agent:      agent,
		workItem:   workItemID,
		start:      start,
		spans:      make(map[string]*spanState),
	}
	f, err := os.OpenFile(filepath.Join(dir, TelemetryFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.disabled = true
		return r
	}
	r.f = f

	r.mu.Lock()
	r.emit(eventRecord{
		V: SchemaVersion, Event: "span_start", TraceID: traceID, SpanID: rootSpanID,
		Parent: "", Name: "run", TS: start.UTC().Format(time.RFC3339Nano),
		WorkItemID: workItemID, Attrs: map[string]any{"agent": agent},
	})
	r.mu.Unlock()
	return r
}

// StartSpan emits a span_start and returns its span id. An empty parentID makes
// the span a child of the root span. Returns "" if the recorder is nil/disabled.
func (r *Recorder) StartSpan(name, parentID string, attrs map[string]any) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return ""
	}
	if parentID == "" {
		parentID = r.rootSpanID
	}
	id := NewSpanID()
	now := time.Now()
	r.spans[id] = &spanState{name: name, parent: parentID, start: now}
	r.emit(eventRecord{
		V: SchemaVersion, Event: "span_start", TraceID: r.traceID, SpanID: id,
		Parent: parentID, Name: name, TS: now.UTC().Format(time.RFC3339Nano),
		WorkItemID: r.workItem, Attrs: attrs,
	})
	return id
}

// EndSpan emits a span_end for spanID, records its duration as a summary step,
// and defaults an empty status to "ok". No-op for nil/disabled/empty spanID.
func (r *Recorder) EndSpan(spanID, status string, attrs map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled || spanID == "" {
		return
	}
	now := time.Now()
	name, parent := "", r.rootSpanID
	var dur int64
	if st := r.spans[spanID]; st != nil {
		name, parent = st.name, st.parent
		dur = now.Sub(st.start).Milliseconds()
		delete(r.spans, spanID)
	}
	if status == "" {
		status = "ok"
	}
	r.steps = append(r.steps, stepTiming{Name: name, DurationMS: dur, Status: status})
	r.emit(eventRecord{
		V: SchemaVersion, Event: "span_end", TraceID: r.traceID, SpanID: spanID,
		Parent: parent, Name: name, TS: now.UTC().Format(time.RFC3339Nano),
		WorkItemID: r.workItem, DurationMS: dur, Status: status, Attrs: attrs,
	})
}

// TraceParent returns the W3C traceparent for child-process propagation, or ""
// if the recorder is nil/disabled.
func (r *Recorder) TraceParent() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return ""
	}
	return TraceParent(r.traceID, r.rootSpanID)
}

// SetMetrics records aggregate run metrics to include in run-summary.json.
// Safe to call repeatedly (last wins); no-op for nil/disabled recorders.
func (r *Recorder) SetMetrics(m RunMetrics) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return
	}
	r.metrics = &m
}

// SetModel records the resolved model name to include in run-summary.json.
// Safe to call repeatedly (last wins); no-op for nil/disabled recorders.
func (r *Recorder) SetModel(model string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return
	}
	r.model = model
}

// Finalize emits the root span_end, writes run-summary.json atomically, and
// closes the telemetry file. It is idempotent and a no-op for nil/disabled.
func (r *Recorder) Finalize(exitCode int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return
	}
	r.finalized = true
	// Close the file if it was ever opened, even on the disabled path, so a
	// mid-run emit failure (which sets r.disabled) doesn't leak the handle.
	if r.f != nil {
		defer func() { _ = r.f.Close() }()
	}
	if r.disabled {
		return
	}

	end := time.Now()
	status := "ok"
	if exitCode != 0 {
		status = "error"
	}
	r.emit(eventRecord{
		V: SchemaVersion, Event: "span_end", TraceID: r.traceID, SpanID: r.rootSpanID,
		Parent: "", Name: "run", TS: end.UTC().Format(time.RFC3339Nano),
		WorkItemID: r.workItem, DurationMS: end.Sub(r.start).Milliseconds(), Status: status,
	})

	r.writeSummary(runSummary{
		V: SchemaVersion, TraceID: r.traceID, Traceparent: TraceParent(r.traceID, r.rootSpanID),
		Agent: r.agent, Model: r.model, WorkItemID: r.workItem, ExitCode: exitCode,
		StartedAt: r.start.UTC().Format(time.RFC3339Nano), EndedAt: end.UTC().Format(time.RFC3339Nano),
		DurationMS: end.Sub(r.start).Milliseconds(), Steps: r.steps, Metrics: r.metrics,
	})
}

// emit writes one JSON line + newline. The caller must hold r.mu and ensure the
// recorder is enabled. Any marshal/write failure or panic disables the recorder
// and is swallowed so the run is never affected. O_APPEND makes each write
// atomic at EOF; Sync keeps already-emitted lines durable across a crash.
func (r *Recorder) emit(rec eventRecord) {
	defer func() {
		if recover() != nil {
			r.disabled = true
		}
	}()
	data, err := json.Marshal(rec)
	if err != nil {
		r.disabled = true
		return
	}
	if _, err := r.f.Write(append(data, '\n')); err != nil {
		r.disabled = true
		return
	}
	// fsync per event is fine for L1's handful of spans per run; if L2/L3 raises
	// span volume, batch or move to an async flush (fsync can be 5-15ms on slow/
	// network filesystems).
	_ = r.f.Sync()
}

// writeSummary writes run-summary.json atomically (temp file + rename) so a
// reader never observes a partial summary. Failures are swallowed and leave no
// stray temp file. The caller must hold r.mu.
func (r *Recorder) writeSummary(s runSummary) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(r.dir, SummaryFile+".tmp")
	final := filepath.Join(r.dir, SummaryFile)
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
	}
}
