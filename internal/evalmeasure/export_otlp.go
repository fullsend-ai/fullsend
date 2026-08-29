package evalmeasure

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// GenAI evaluation event / attribute names.
//
// Attribute names follow OpenTelemetry GenAI semantic conventions
// (normative pin consulted for this ship:
// https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-events.md#event-gen_aievaluationresult
// — GenAI events live in semantic-conventions-genai; treat as low-stability.
// Supporting-library matrix only:
// https://github.com/open-telemetry/semantic-conventions-genai/blob/main/reference/reports/gen-ai-evaluation-result-event.md).
// Semconv remains unstable across minor versions (see gen_ai.system →
// gen_ai.provider.name); bump measurement versions when attribute names change.
//
// Carrier deviation (deliberate): the convention defines
// gen_ai.evaluation.result as a log-record / Event-API event (SHOULD parent
// to the GenAI operation span, or set gen_ai.response.id when span id is
// unavailable). Fullsend emits it as a span event via span.AddEvent on a
// short fullsend.eval_measure child over the traces OTLP path, because only
// a traces exporter is configured today — log-side consumers will not
// auto-discover these scores. A conforming log-record emit is follow-up
// work when a logs exporter exists; this PR does not add one.
//
// Post-hoc attach: the scored GenAI operation span is already flushed, so
// the child span is remote-parented to that SpanID. Any OTLP traces backend
// can correlate by TraceID; vendor score UIs (Assessments panels, etc.) may
// still need a collector/consumer mapping — fullsend does not call those
// APIs.
const (
	EventGenAIEvaluationResult = "gen_ai.evaluation.result"

	AttrGenAIEvaluationName        = "gen_ai.evaluation.name"
	AttrGenAIEvaluationScoreValue  = "gen_ai.evaluation.score.value"
	AttrGenAIEvaluationScoreLabel  = "gen_ai.evaluation.score.label"
	AttrGenAIEvaluationExplanation = "gen_ai.evaluation.explanation"
	AttrFullsendMeasurementVersion = "fullsend.measurement.version"

	spanNameEvalMeasure = "fullsend.eval_measure"
	otlpScopeName       = "github.com/fullsend-ai/fullsend/internal/evalmeasure"
	// Score export is post-hoc and fail-open: bound total wall time for
	// exporter create + span emit + ForceFlush + Shutdown so a flaky
	// collector cannot hang the agent job until GHA timeout.
	otlpExportBudget = 15 * time.Second
	otlpRetryBudget  = 5 * time.Second
)

// newScoreOTLPExporter is a test seam. Production uses a retry-bounded
// exporter so Simple/Batch export cannot retry forever. Live agent Setup's
// NewOTLPExporter sets InitialInterval/MaxInterval but leaves
// MaxElapsedTime at 0, which the otlptracehttp retry loop treats as "never
// give up on elapsed time" (bounded only by shutdown ctx cancellation) —
// scores intentionally stay tighter.
var newScoreOTLPExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
	return telemetry.NewOTLPExporterBounded(ctx, otlpRetryBudget)
}

// ExportOTLPScores emits each measurement as a short child span on the same
// TraceID (remote-parented to the scored SpanID) with a
// gen_ai.evaluation.result event. Uses the same OTEL_EXPORTER_OTLP_* env as
// ADR 0050 agent traces. serviceVersion must match telemetry.Setup's version
// (CLI Version()) so resource identity stays aligned with agent spans.
// No-op when OTEL is unset or OTEL_SDK_DISABLED=true. Per-score: when inbound
// TRACEPARENT is valid and unsampled, scores whose TraceID equals that
// parent TraceID are skipped (same rule as parentSampledProcessor — avoid
// orphaning score spans on a TraceID that never left the box). Other
// TraceIDs in the batch are unaffected. Fail-open: returns an error for the
// caller to warn on; never writes primary telemetry files. Skip rows that
// lack a parent SpanID are omitted; pass/fail rows with missing or malformed
// IDs report a warning. Pure ID failures report "N/M scores
// exported" so operators can tell partial from total failure; a
// ForceFlush/export transport error uses a distinct "failed for all N"
// message (row construction is not treated as delivery).
func ExportOTLPScores(ctx context.Context, results []EvaluationResult, serviceVersion string) (err error) {
	if len(results) == 0 {
		return nil
	}
	if sdkDisable := os.Getenv("OTEL_SDK_DISABLED"); strings.EqualFold(strings.TrimSpace(sdkDisable), "true") {
		return nil
	}
	if !telemetry.OTLPEnabled() {
		return nil
	}
	if err := telemetry.ValidateOTLPEndpoints(); err != nil {
		return fmt.Errorf("otlp endpoint validation: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, otlpExportBudget)
	defer cancel()

	exp, err := newScoreOTLPExporter(ctx)
	if err != nil {
		return fmt.Errorf("otlp exporter: %w", err)
	}
	capExp := &capturingExporter{base: exp}

	// Batch so N scores share one (or few) HTTP exports under the budget.
	// Size the queue for this bounded, already-materialized result set. The
	// SDK's default queue is 2,048 and drops spans once full; blocking instead
	// would use context.TODO internally and could violate this function's
	// fail-open wall-clock budget.
	queueSize := len(results)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(telemetry.BuildResource(serviceVersion)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithRawSpanLimits(telemetry.SpanLimits()),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(capExp, sdktrace.WithMaxQueueSize(queueSize))),
	)
	// Shutdown shares the same export budget (remaining deadline on ctx),
	// not an extra Background timeout stacked on top.
	defer func() {
		if shutErr := tp.Shutdown(ctx); shutErr != nil {
			err = errors.Join(err, fmt.Errorf("otlp shutdown: %w", shutErr))
		}
	}()

	suppressTID, suppressUnsampled := inboundUnsampledTRACEPARENT()
	tr := tp.Tracer(otlpScopeName)
	var (
		errs      []error
		attempted int
		failed    int
	)
	for _, r := range results {
		if suppressUnsampled && scoreTraceIDEquals(r.TraceID, suppressTID) {
			// Inbound parent for this TraceID was unsampled — agent OTLP
			// suppressed; do not orphan a score span on that TraceID.
			continue
		}
		if r.Label == LabelSkip && (strings.TrimSpace(r.TraceID) == "" || strings.TrimSpace(r.SpanID) == "") {
			// EM-001 can legitimately skip when it cannot find the root run
			// span. There is no parent to correlate remotely.
			continue
		}
		attempted++
		if err := exportOneScore(ctx, tr, r); err != nil {
			failed++
			errs = append(errs, err)
		}
	}
	var flushErr error
	if flushErr = tp.ForceFlush(ctx); flushErr != nil {
		errs = append(errs, flushErr)
	}
	capExp.mu.Lock()
	expErr := capExp.err
	capExp.mu.Unlock()
	if expErr != nil {
		errs = append(errs, fmt.Errorf("otlp export: %w", expErr))
	}
	if len(errs) == 0 {
		return nil
	}
	// Transport failure: nothing is known to have landed — do not claim N/M
	// success from spans that were only constructed locally.
	if flushErr != nil || expErr != nil {
		return fmt.Errorf("otlp export failed for all %d scores: %w", attempted, errors.Join(errs...))
	}
	exported := attempted - failed
	return fmt.Errorf("%d/%d scores exported; %d failed: %w", exported, attempted, failed, errors.Join(errs...))
}

// inboundUnsampledTRACEPARENT parses TRACEPARENT with the same W3C
// TraceContext propagator as fullsend run. When the inbound parent is valid,
// remote, and unsampled, it returns that TraceID and true. Empty/malformed
// or sampled TRACEPARENT → false (export proceeds for all scores).
func inboundUnsampledTRACEPARENT() (trace.TraceID, bool) {
	tp := strings.TrimSpace(os.Getenv("TRACEPARENT"))
	if tp == "" {
		return trace.TraceID{}, false
	}
	ctx := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{
		"traceparent": tp,
		"tracestate":  strings.TrimSpace(os.Getenv("TRACESTATE")),
	})
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || !sc.IsRemote() || sc.IsSampled() {
		return trace.TraceID{}, false
	}
	return sc.TraceID(), true
}

func scoreTraceIDEquals(hexID string, want trace.TraceID) bool {
	got, err := parseTraceID(hexID)
	if err != nil {
		return false
	}
	return got == want
}

// capturingExporter tracks the latest ExportSpans error so fail-open callers
// can warn. A later successful ExportSpans clears a prior transient failure
// (avoids false "remote export failed" after data actually landed). Mutex
// covers BatchSpanProcessor's async export goroutine.
type capturingExporter struct {
	base sdktrace.SpanExporter
	mu   sync.Mutex
	err  error
}

func (c *capturingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := c.base.ExportSpans(ctx, spans)
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	return err
}

func (c *capturingExporter) Shutdown(ctx context.Context) error {
	return c.base.Shutdown(ctx)
}

func exportOneScore(ctx context.Context, tr trace.Tracer, r EvaluationResult) error {
	tid, err := parseTraceID(r.TraceID)
	if err != nil {
		return fmt.Errorf("trace_id %q: %w", r.TraceID, err)
	}
	sid, err := parseSpanID(r.SpanID)
	if err != nil {
		return fmt.Errorf("span_id %q: %w", r.SpanID, err)
	}

	psc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	parent := trace.ContextWithRemoteSpanContext(ctx, psc)

	attrs := []attribute.KeyValue{
		attribute.String(AttrGenAIAgentName, r.Agent),
		attribute.String(AttrFullsendMeasurementVersion, r.Version),
	}
	if r.WorkItemID != "" {
		attrs = append(attrs, attribute.String(AttrFullsendWorkItemID, r.WorkItemID))
	}

	_, span := tr.Start(parent, spanNameEvalMeasure, trace.WithAttributes(attrs...))
	defer span.End()

	eventAttrs := []attribute.KeyValue{
		attribute.String(AttrGenAIEvaluationName, r.Name),
		attribute.String(AttrGenAIEvaluationScoreLabel, r.Label),
		// Event attribute values are not truncated by SpanLimits (SDK
		// applies AttributeValueLengthLimit to span attrs only). Bound
		// explanation at the call site via FreeTextAttrValueLenLimit —
		// operator OTEL_* overrides (incl. -1) or MaxSpanAttrValueLen —
		// not content-capture-aware SpanLimits (Level 3 would leave it
		// unbounded).
		attribute.String(AttrGenAIEvaluationExplanation, truncateRunes(r.Explanation, eventAttrValueLenLimit())),
		attribute.String(AttrFullsendMeasurementVersion, r.Version),
	}
	// Skip rows leave Value unused (serialized as 0 in JSONL); do not publish
	// a numeric zero that backends may chart as a real score.
	if r.Label != LabelSkip {
		eventAttrs = append(eventAttrs, attribute.Float64(AttrGenAIEvaluationScoreValue, r.Value))
	}

	span.AddEvent(EventGenAIEvaluationResult, trace.WithAttributes(eventAttrs...))
	// Keep Ok for all labels: pass/fail/skip live on the evaluation event.
	// Error status would conflate derived fitness fail with run failure in
	// backends that key off span status.
	span.SetStatus(codes.Ok, "")
	return nil
}

func truncateRunes(s string, max int) string {
	if max < 0 {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// eventAttrValueLenLimit bounds gen_ai.evaluation.explanation for OTLP.
// Uses telemetry.FreeTextAttrValueLenLimit: operator OTEL_* limit when set
// (including explicit -1 = unlimited), else MaxSpanAttrValueLen. Does not
// reuse SpanLimits(), whose negative sentinel under Level 3 content capture
// would leave explanations unbounded on the wire.
func eventAttrValueLenLimit() int {
	return telemetry.FreeTextAttrValueLenLimit()
}

func parseTraceID(hexID string) (trace.TraceID, error) {
	var out trace.TraceID
	b, err := decodeFixedHex(hexID, len(out))
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	if !out.IsValid() {
		return out, fmt.Errorf("trace_id must be non-zero")
	}
	return out, nil
}

func parseSpanID(hexID string) (trace.SpanID, error) {
	var out trace.SpanID
	b, err := decodeFixedHex(hexID, len(out))
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	if !out.IsValid() {
		return out, fmt.Errorf("span_id must be non-zero")
	}
	return out, nil
}

func decodeFixedHex(s string, n int) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) != n*2 {
		return nil, fmt.Errorf("want %d hex chars, got %d", n*2, len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return b, nil
}
