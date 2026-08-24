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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// GenAI evaluation event / attribute names.
//
// Attribute names follow OpenTelemetry GenAI semantic conventions
// (semantic-conventions-genai, evaluation events — pin consulted for this
// ship: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-events/
// and the gen_ai.evaluation.* attribute registry). Semconv remains
// unstable across minor versions (see gen_ai.system → gen_ai.provider.name);
// bump measurement versions when attribute names change.
//
// Post-hoc attach: the scored GenAI operation span is already flushed, so
// we emit a short child span (fullsend.eval_measure) remote-parented to
// that SpanID and AddEvent the evaluation result. Any OTLP backend can
// correlate by TraceID; vendor score UIs (Assessments panels, etc.) may
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
	// Score export is post-hoc and fail-open: bound total wall time so a
	// flaky collector cannot hang the agent job until GHA timeout.
	otlpExportBudget = 15 * time.Second
	otlpRetryBudget  = 5 * time.Second
)

// newScoreOTLPExporter is a test seam. Production uses a retry-bounded
// exporter so Simple/Batch export cannot retry forever (unlike live agent
// Setup, which may leave MaxElapsedTime at the SDK default).
var newScoreOTLPExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
	return telemetry.NewOTLPExporterBounded(ctx, otlpRetryBudget)
}

// ExportOTLPScores emits each measurement as a short child span on the same
// TraceID (remote-parented to the scored SpanID) with a
// gen_ai.evaluation.result event. Uses the same OTEL_EXPORTER_OTLP_* env as
// ADR 0050 agent traces. serviceVersion must match telemetry.Setup's version
// (CLI Version()) so resource identity stays aligned with agent spans.
// No-op when OTEL is unset, OTEL_SDK_DISABLED=true, or inbound TRACEPARENT
// is explicitly unsampled (-00), matching parentSampledProcessor on agent
// export. Fail-open: returns an error for the caller to warn on; never
// writes primary telemetry files. Rows with empty/zero IDs are skipped.
func ExportOTLPScores(ctx context.Context, results []EvaluationResult, serviceVersion string) error {
	if len(results) == 0 {
		return nil
	}
	if sdkDisable := os.Getenv("OTEL_SDK_DISABLED"); strings.EqualFold(strings.TrimSpace(sdkDisable), "true") {
		return nil
	}
	if !telemetry.OTLPEnabled() {
		return nil
	}
	if inboundTRACEPARENTUnsampled() {
		// Same job as fullsend run: if the inbound parent was unsampled,
		// agent OTLP was suppressed — do not orphan score spans on that TraceID.
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
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(telemetry.BuildResource(serviceVersion)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithRawSpanLimits(telemetry.SpanLimits()),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(capExp)),
	)
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), otlpRetryBudget)
		defer shutCancel()
		_ = tp.Shutdown(shutCtx)
	}()

	tr := tp.Tracer(otlpScopeName)
	var errs []error
	for _, r := range results {
		if err := exportOneScore(ctx, tr, r); err != nil {
			errs = append(errs, err)
		}
	}
	if err := tp.ForceFlush(ctx); err != nil {
		errs = append(errs, err)
	}
	capExp.mu.Lock()
	expErr := capExp.err
	capExp.mu.Unlock()
	if expErr != nil {
		errs = append(errs, fmt.Errorf("otlp export: %w", expErr))
	}
	return errors.Join(errs...)
}

// inboundTRACEPARENTUnsampled reports whether TRACEPARENT is present and
// carries the W3C sampled flag cleared (…-00). Empty/malformed TRACEPARENT
// is treated as "no inbound parent" (export proceeds).
func inboundTRACEPARENTUnsampled() bool {
	tp := strings.TrimSpace(os.Getenv("TRACEPARENT"))
	if tp == "" {
		return false
	}
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return false
	}
	flags, err := hex.DecodeString(parts[3])
	if err != nil || len(flags) != 1 {
		return false
	}
	return flags[0]&0x01 == 0
}

// capturingExporter records the first ExportSpans error so fail-open callers
// can warn. Mutex covers BatchSpanProcessor (async export goroutine).
type capturingExporter struct {
	base sdktrace.SpanExporter
	mu   sync.Mutex
	err  error
}

func (c *capturingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := c.base.ExportSpans(ctx, spans)
	if err != nil {
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
	}
	return err
}

func (c *capturingExporter) Shutdown(ctx context.Context) error {
	return c.base.Shutdown(ctx)
}

func exportOneScore(ctx context.Context, tr trace.Tracer, r EvaluationResult) error {
	if strings.TrimSpace(r.TraceID) == "" || strings.TrimSpace(r.SpanID) == "" {
		// EM-001 skip rows can omit span_id when the root run span is missing.
		// Local JSONL still records them; OTLP needs a parent to correlate.
		return nil
	}
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
		// applies AttributeValueLengthLimit to span attrs only); bound
		// explanation at the call site like agent exception messages.
		attribute.String(AttrGenAIEvaluationExplanation, truncateRunes(r.Explanation, telemetry.MaxSpanAttrValueLen)),
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
