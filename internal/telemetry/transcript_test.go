package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestParseTranscriptInteractions_Basic(t *testing.T) {
	dir := t.TempDir()
	transcript := `{"type":"human","content":"Fix the bug in auth.go"}
{"type":"assistant","content":[{"type":"text","text":"I'll fix that bug now."}],"model":"claude-sonnet-4-20250514","usage":{"input_tokens":150,"output_tokens":42}}
{"type":"human","content":"Now add a test for it"}
{"type":"assistant","content":[{"type":"text","text":"Here's the test:"},{"type":"tool_use","name":"write_file","input":{"path":"auth_test.go"}}],"model":"claude-sonnet-4-20250514","stop_reason":"end_turn","usage":{"input_tokens":300,"output_tokens":85}}
`
	path := filepath.Join(dir, "transcript.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(transcript), 0o644))

	interactions := ParseTranscriptInteractions(path)
	require.Len(t, interactions, 2)

	assert.Equal(t, "Fix the bug in auth.go", interactions[0].Input)
	assert.Equal(t, "I'll fix that bug now.", interactions[0].Output)
	assert.Equal(t, "claude-sonnet-4-20250514", interactions[0].Model)
	assert.Equal(t, 150, interactions[0].InputTokens)
	assert.Equal(t, 42, interactions[0].OutputTokens)

	assert.Equal(t, "Now add a test for it", interactions[1].Input)
	assert.Contains(t, interactions[1].Output, "Here's the test:")
	assert.Equal(t, "end_turn", interactions[1].StopReason)
	assert.Len(t, interactions[1].ToolCalls, 1)
	assert.Equal(t, "write_file", interactions[1].ToolCalls[0].Name)
}

func TestParseTranscriptInteractions_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))

	interactions := ParseTranscriptInteractions(path)
	assert.Empty(t, interactions)
}

func TestParseTranscriptInteractions_NoAssistant(t *testing.T) {
	dir := t.TempDir()
	transcript := `{"type":"human","content":"Hello"}
{"type":"result","is_error":true,"result":"timeout"}
`
	path := filepath.Join(dir, "no-assistant.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(transcript), 0o644))

	interactions := ParseTranscriptInteractions(path)
	assert.Empty(t, interactions)
}

func TestEmitTranscriptSpans_CreatesChildSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test")

	dir := t.TempDir()
	rec, ctx, err := NewRecorder(context.Background(), dir, tracer, "test-run", nil)
	require.NoError(t, err)

	// Start a parent step (simulating an iteration).
	rec.StepStart(ctx, "agent-execution.iteration-1")

	// Write a transcript file.
	transcriptDir := filepath.Join(dir, "transcripts")
	require.NoError(t, os.MkdirAll(transcriptDir, 0o755))
	transcript := `{"type":"human","content":"What is 2+2?"}
{"type":"assistant","content":[{"type":"text","text":"The answer is 4."}],"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":8}}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(transcriptDir, "session.jsonl"),
		[]byte(transcript), 0o644))

	EmitTranscriptSpans(rec, "agent-execution.iteration-1", transcriptDir, "claude-sonnet-4-20250514")

	rec.StepDone("agent-execution.iteration-1")
	require.NoError(t, rec.Close())
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()

	// Find the LLM turn span.
	var llmSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "llm.turn.1" {
			llmSpan = &spans[i]
			break
		}
	}
	require.NotNil(t, llmSpan, "llm.turn.1 span should exist")

	attrMap := make(map[string]string)
	for _, a := range llmSpan.Attributes {
		attrMap[string(a.Key)] = a.Value.Emit()
	}

	assert.Equal(t, "What is 2+2?", attrMap["gen_ai.content.prompt"])
	assert.Equal(t, "The answer is 4.", attrMap["gen_ai.content.completion"])
	assert.Equal(t, "chat", attrMap["gen_ai.operation.name"])
	assert.Equal(t, "anthropic", attrMap["gen_ai.system"])
	assert.Equal(t, "claude-sonnet-4-20250514", attrMap["gen_ai.response.model"])
}
