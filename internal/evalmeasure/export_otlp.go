package evalmeasure

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// GenAI evaluation event / attribute names (OpenTelemetry GenAI semconv).
// Scores travel as span events so any OTLP backend (Phoenix, MLflow collector,
// Jaeger, Arize, …) can correlate them to the agent trace without a vendor API.
const (
	EventGenAIEvaluationResult = "gen_ai.evaluation.result"

	AttrGenAIEvaluationName             = "gen_ai.evaluation.name"
	AttrGenAIEvaluationScoreValue       = "gen_ai.evaluation.score.value"
	AttrGenAIEvaluationScoreLabel       = "gen_ai.evaluation.score.label"
	AttrGenAIEvaluationExplanation      = "gen_ai.evaluation.explanation"
	AttrFullsendMeasurementVersion      = "fullsend.measurement.version"
	AttrFullsendEvaluationEvaluatorType = "fullsend.evaluation.evaluator.type"

	spanNameEvalMeasure = "fullsend.eval_measure"
	otlpScopeName       = "github.com/fullsend-ai/fullsend/internal/evalmeasure"
	otlpFlushTimeout    = 5 * time.Second
)

// newScoreOTLPExporter is a test seam over telemetry.NewOTLPExporter.
var newScoreOTLPExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
	return telemetry.NewOTLPExporter(ctx)
}

// ExportOTLPScores emits each measurement as a short child span on the same
// TraceID (remote-parented to the scored SpanID) with a
// gen_ai.evaluation.result event. Uses the same OTEL_EXPORTER_OTLP_* env as
// ADR 0050 agent traces. No-op when OTEL is unset or OTEL_SDK_DISABLED=true.
// Fail-open: returns an error for the caller to warn on; never writes primary
// telemetry files.
func ExportOTLPScores(ctx context.Context, results []EvaluationResult) error {
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

	exp, err := newScoreOTLPExporter(ctx)
	if err != nil {
		return fmt.Errorf("otlp exporter: %w", err)
	}
	capExp := &capturingExporter{base: exp}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(capExp)),
	)
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), otlpFlushTimeout)
		defer cancel()
		_ = tp.Shutdown(shutCtx)
	}()

	tr := tp.Tracer(otlpScopeName)
	var firstErr error
	for _, r := range results {
		if err := exportOneScore(ctx, tr, r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := tp.ForceFlush(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		return firstErr
	}
	if capExp.err != nil {
		return fmt.Errorf("otlp export: %w", capExp.err)
	}
	return nil
}

// capturingExporter records the first ExportSpans error so fail-open callers
// can warn. The OTEL SDK may also log; we still want a structured warning.
type capturingExporter struct {
	base sdktrace.SpanExporter
	err  error
}

func (c *capturingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := c.base.ExportSpans(ctx, spans)
	if err != nil && c.err == nil {
		c.err = err
	}
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
		attribute.String(AttrFullsendEvaluationEvaluatorType, "deterministic"),
	}
	if r.WorkItemID != "" {
		attrs = append(attrs, attribute.String(AttrFullsendWorkItemID, r.WorkItemID))
	}

	_, span := tr.Start(parent, spanNameEvalMeasure, trace.WithAttributes(attrs...))
	defer span.End()

	eventAttrs := []attribute.KeyValue{
		attribute.String(AttrGenAIEvaluationName, r.Name),
		attribute.String(AttrGenAIEvaluationScoreLabel, r.Label),
		attribute.Float64(AttrGenAIEvaluationScoreValue, r.Value),
		attribute.String(AttrGenAIEvaluationExplanation, r.Explanation),
		attribute.String(AttrFullsendMeasurementVersion, r.Version),
	}

	span.AddEvent(EventGenAIEvaluationResult, trace.WithAttributes(eventAttrs...))
	if r.Label == LabelFail {
		span.SetStatus(codes.Error, r.Explanation)
	} else {
		span.SetStatus(codes.Ok, "")
	}
	return nil
}

func parseTraceID(hexID string) (trace.TraceID, error) {
	var out trace.TraceID
	b, err := decodeFixedHex(hexID, len(out))
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}

func parseSpanID(hexID string) (trace.SpanID, error) {
	var out trace.SpanID
	b, err := decodeFixedHex(hexID, len(out))
	if err != nil {
		return out, err
	}
	copy(out[:], b)
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
	if len(b) != n {
		return nil, fmt.Errorf("decoded %d bytes, want %d", len(b), n)
	}
	return b, nil
}
