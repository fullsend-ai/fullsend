package evalmeasure

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// TestParseTelemetryFile_RoundTripFromExporter binds the evalmeasure reader
// to the real file exporter: if the writer ever switches encoding (protojson
// enums, base64 ids, etc.), this fails instead of silently scoring nothing.
func TestParseTelemetryFile_RoundTripFromExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	dir := t.TempDir()
	tracer, cleanup := telemetry.Setup(dir, "roundtrip-test")

	ctx := context.Background()
	ctx, run := tracer.Start(ctx, "run")
	run.SetAttributes(
		attribute.String("fullsend.agent", "triage"),
		attribute.String("fullsend.work_item_id", "acme/demo#1"),
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.Int64("exit_code", 0),
		attribute.Int64("fullsend.num_turns", 1),
		attribute.Float64("fullsend.cost_usd", 0.01),
		attribute.Int64("fullsend.tool_calls", 5),
		attribute.Int64("fullsend.iterations", 1),
		attribute.String("gen_ai.request.model", "test-model"),
		attribute.Int64("gen_ai.usage.input_tokens", 1),
		attribute.Int64("gen_ai.usage.output_tokens", 1),
	)
	_, sandbox := tracer.Start(ctx, "sandbox_create")
	sandbox.End()
	_, agent := tracer.Start(ctx, "agent")
	agent.SetAttributes(
		attribute.String("gen_ai.agent.name", "triage"),
		attribute.String("gen_ai.system", "anthropic"),
		attribute.String("gen_ai.request.model", "test-model"),
		attribute.Int64("gen_ai.usage.input_tokens", 1),
		attribute.Int64("gen_ai.usage.output_tokens", 1),
		attribute.Float64("fullsend.cost_usd", 0.01),
		attribute.Int64("fullsend.tool_calls", 5),
	)
	agent.End()
	run.End()
	cleanup(ctx)

	path := filepath.Join(dir, telemetry.TelemetryFile)
	_, err := os.Stat(path)
	require.NoError(t, err)

	traces, stats, err := ParseTelemetryFile(path)
	require.NoError(t, err)
	assert.Equal(t, 0, stats.SkippedLines, "exporter JSONL must unmarshal")
	assert.Equal(t, 0, stats.SkippedSpans, "exporter spans must convert")
	require.NotEmpty(t, traces)

	r := ScoreFitness(traces[0])
	assert.Equal(t, LabelPass, r.Label, "round-tripped EM-001 fixture should pass: %s", r.Explanation)
}
