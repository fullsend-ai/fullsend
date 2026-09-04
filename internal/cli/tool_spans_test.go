package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// toolSpanFixture opens an agent span on a recording provider and returns
// a tracker parented under it, the recorder, and the agent span.
func toolSpanFixture(t *testing.T) (*toolSpanTracker, *tracetest.SpanRecorder, trace.Span) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")
	agentCtx, agentSpan := tracer.Start(context.Background(), "agent")
	return newToolSpanTracker(tracer, agentCtx), rec, agentSpan
}

// endedToolSpans returns every ended span except the agent span, in end
// order.
func endedToolSpans(rec *tracetest.SpanRecorder) []tracetest.SpanStub {
	var out []tracetest.SpanStub
	for _, s := range rec.Ended() {
		if s.Name() != "agent" {
			out = append(out, tracetest.SpanStubFromReadOnlySpan(s))
		}
	}
	return out
}

func toolSpanAttrs(s tracetest.SpanStub) map[attribute.Key]attribute.Value {
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes {
		out[kv.Key] = kv.Value
	}
	return out
}

func TestToolSpanTracker_PairEmitsExecuteToolChild(t *testing.T) {
	tr, rec, agentSpan := toolSpanFixture(t)

	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Summary: "ls"})
	require.Empty(t, rec.Ended(), "the span stays open until its result arrives")

	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_01", Result: "ok"})
	spans := endedToolSpans(rec)
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "execute_tool Bash", s.Name)
	assert.Equal(t, agentSpan.SpanContext().SpanID(), s.Parent.SpanID(),
		"execute_tool must be a child of the iteration's agent span")
	assert.Equal(t, trace.SpanKindInternal, s.SpanKind)
	assert.False(t, s.EndTime.Before(s.StartTime))

	attrs := toolSpanAttrs(s)
	assert.Equal(t, "execute_tool", attrs["gen_ai.operation.name"].AsString())
	assert.Equal(t, "Bash", attrs["gen_ai.tool.name"].AsString())
	assert.Equal(t, "toolu_01", attrs["gen_ai.tool.call.id"].AsString())
	assert.NotContains(t, attrs, attribute.Key("error.type"), "success carries no error.type")
	assert.NotContains(t, attrs, attribute.Key("fullsend.tool.unmatched"))
	assert.Equal(t, codes.Ok, s.Status.Code)
	assert.Empty(t, tr.open, "an answered call must not stay open (Finish would re-end it, and the map would grow per call)")
}

func TestToolSpanTracker_ErrorResultSetsErrorTypeAndStatus(t *testing.T) {
	tr, rec, _ := toolSpanFixture(t)

	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_02", Name: "Edit"})
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_02", Result: "no such file", IsError: true})

	spans := endedToolSpans(rec)
	require.Len(t, spans, 1)
	assert.Equal(t, "tool_error", toolSpanAttrs(spans[0])["error.type"].AsString())
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	assert.NotEmpty(t, spans[0].Status.Description)
	assert.Empty(t, spans[0].Events, "the wire carries no error text, so no exception event")
}

func TestToolSpanTracker_UnansweredCallEndsAtFinish(t *testing.T) {
	tr, rec, _ := toolSpanFixture(t)

	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_03", Name: "Bash"})
	require.Empty(t, rec.Ended())

	tr.Finish()
	spans := endedToolSpans(rec)
	require.Len(t, spans, 1)
	assert.Equal(t, "unanswered", toolSpanAttrs(spans[0])["error.type"].AsString())
	assert.Equal(t, codes.Error, spans[0].Status.Code)

	tr.Finish()
	assert.Len(t, endedToolSpans(rec), 1, "Finish is idempotent")
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_03"})
	assert.Len(t, endedToolSpans(rec), 2, "a result after Finish is an orphan, never a second end")
}

func TestToolSpanTracker_OrphanResultIsMarkedUnmatched(t *testing.T) {
	tr, rec, agentSpan := toolSpanFixture(t)

	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_04", Result: "late"})

	spans := endedToolSpans(rec)
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "execute_tool", s.Name, "no tool_use was seen, so no name is known")
	assert.Equal(t, agentSpan.SpanContext().SpanID(), s.Parent.SpanID())
	attrs := toolSpanAttrs(s)
	assert.True(t, attrs["fullsend.tool.unmatched"].AsBool())
	assert.Equal(t, "toolu_04", attrs["gen_ai.tool.call.id"].AsString())
	assert.NotContains(t, attrs, attribute.Key("gen_ai.tool.name"))
	assert.Equal(t, codes.Ok, s.Status.Code)

	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_05", IsError: true})
	spans = endedToolSpans(rec)
	require.Len(t, spans, 2)
	assert.Equal(t, "tool_error", toolSpanAttrs(spans[1])["error.type"].AsString())
	assert.Equal(t, codes.Error, spans[1].Status.Code)
}

func TestToolSpanTracker_EventsWithoutIDProduceNoSpan(t *testing.T) {
	// pi and codex emit ToolUseEvent without an id and never a result.
	tr, rec, _ := toolSpanFixture(t)

	tr.Handle(agentruntime.ToolUseEvent{Name: "bash", Summary: "ls"})
	tr.Handle(agentruntime.ToolResultEvent{Result: "stray"})
	tr.Finish()

	assert.Empty(t, endedToolSpans(rec))
}

func TestToolSpanTracker_MalformedIDProducesNoSpan(t *testing.T) {
	tr, rec, _ := toolSpanFixture(t)
	long := strings.Repeat("i", maxToolIDBytes+1)

	tr.Handle(agentruntime.ToolUseEvent{ID: long, Name: "Bash"})
	tr.Handle(agentruntime.ToolResultEvent{ID: long})
	tr.Finish()

	assert.Empty(t, endedToolSpans(rec), "an id beyond the bound is malformed: no span, not a truncated one")
}

func TestToolSpanTracker_NameIsBoundedAndRepaired(t *testing.T) {
	tr, rec, _ := toolSpanFixture(t)
	name := strings.Repeat("n", telemetry.MaxSpanAttrValueLen*2) + "\xff"

	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_06", Name: name})
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_06"})

	spans := endedToolSpans(rec)
	require.Len(t, spans, 1)
	s := spans[0]
	attr := toolSpanAttrs(s)["gen_ai.tool.name"].AsString()
	assert.LessOrEqual(t, len(attr), maxToolNameBytes)
	assert.True(t, utf8.ValidString(attr))
	assert.True(t, strings.HasPrefix(s.Name, "execute_tool "))
	assert.LessOrEqual(t, len(s.Name), len("execute_tool ")+maxToolSpanNameBytes)
	assert.True(t, utf8.ValidString(s.Name))
}

func TestToolSpanTracker_DuplicateIDSupersedesOpenCall(t *testing.T) {
	tr, rec, _ := toolSpanFixture(t)

	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_07", Name: "Read"})
	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_07", Name: "Read"})
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_07"})

	spans := endedToolSpans(rec)
	require.Len(t, spans, 2)
	assert.Equal(t, "unanswered", toolSpanAttrs(spans[0])["error.type"].AsString(),
		"the earlier open call is closed as unanswered when its id is reused")
	assert.Equal(t, codes.Ok, spans[1].Status.Code)
	tr.Finish()
	assert.Len(t, endedToolSpans(rec), 2)
}

func TestToolSpanTracker_CapsSpansPerIteration(t *testing.T) {
	// An agent-controlled burst of calls must not fill the OTLP batch queue
	// and evict the agent span that follows; past the cap nothing is
	// tracked or started, and the overflow is reported.
	tr, rec, _ := toolSpanFixture(t)
	for i := 0; i < maxToolSpansPerIteration+5; i++ {
		tr.Handle(agentruntime.ToolUseEvent{ID: fmt.Sprintf("toolu_%05d", i), Name: "Bash"})
	}
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_orphan"})

	dropped := tr.Finish()
	assert.Equal(t, 6, dropped, "five calls and one orphan past the cap")
	assert.Len(t, endedToolSpans(rec), maxToolSpansPerIteration)
	assert.Equal(t, 6, tr.Finish(), "the count is stable across calls")
	assert.Equal(t, 0, newToolSpanTracker(nil, context.Background()).Finish())
}

func TestToolSpanTracker_NameIsRedactedAndBounded(t *testing.T) {
	// The name is stream-derived and lands on a Level 1 span the output
	// scan exempts, so it is redacted like the console summary and bounded
	// far below the SDK cap.
	tr, rec, _ := toolSpanFixture(t)
	secret := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_09", Name: "mcp__vault__" + secret})
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_09"})
	long := strings.Repeat("n", maxToolNameBytes*3)
	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_10", Name: long})
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_10"})

	spans := endedToolSpans(rec)
	require.Len(t, spans, 2)
	name := toolSpanAttrs(spans[0])["gen_ai.tool.name"].AsString()
	assert.NotContains(t, name, secret)
	assert.NotContains(t, spans[0].Name, secret)
	assert.True(t, strings.HasPrefix(name, "mcp__vault__"), "the non-secret part survives")
	assert.LessOrEqual(t, len(toolSpanAttrs(spans[1])["gen_ai.tool.name"].AsString()), maxToolNameBytes)
}

func TestToolSpanTracker_NameGoesThroughTheOutputSanitizer(t *testing.T) {
	// The name lands on a Level 1 span in the telemetry file the output
	// scan exempts, so it must get the same treatment as span content:
	// Unicode normalization (escapes, NUL, zero-width and bidi overrides)
	// and then secret redaction — not the secret pass alone.
	tr, rec, _ := toolSpanFixture(t)
	cases := map[string]string{
		"toolu_11": "Bash\x1b[31mred\x1b[0m",
		"toolu_12": "Bash\u200b\u202eevil",
		"toolu_13": "Bash\x00nul",
	}
	for id, name := range cases {
		tr.Handle(agentruntime.ToolUseEvent{ID: id, Name: name})
		tr.Handle(agentruntime.ToolResultEvent{ID: id})
	}
	spans := endedToolSpans(rec)
	require.Len(t, spans, 3)
	for _, s := range spans {
		name := toolSpanAttrs(s)["gen_ai.tool.name"].AsString()
		for _, bad := range []string{"\x1b", "\u200b", "\u202e", "\x00"} {
			assert.NotContains(t, name, bad, "attribute must be normalized")
			assert.NotContains(t, s.Name, bad, "span name must be normalized")
		}
		assert.True(t, strings.HasPrefix(name, "Bash"), "the visible part survives: %q", name)
	}

	// A name that sanitizes to nothing shows nothing: no attribute, and the
	// bare operation as the span name — never the unsanitized bytes.
	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_14", Name: "\u200b\u202e"})
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_14"})
	spans = endedToolSpans(rec)
	require.Len(t, spans, 4)
	assert.Equal(t, "execute_tool", spans[3].Name)
	assert.NotContains(t, toolSpanAttrs(spans[3]), attribute.Key("gen_ai.tool.name"))
}

func TestRecordToolSpanOverflow_MarksAgentSpanOnlyWhenDropped(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, clean := tp.Tracer("test").Start(context.Background(), "agent")
	recordToolSpanOverflow(clean, 0)
	clean.End()
	_, over := tp.Tracer("test").Start(context.Background(), "agent")
	recordToolSpanOverflow(over, 3)
	over.End()

	ended := rec.Ended()
	require.Len(t, ended, 2)
	assert.NotContains(t, toolSpanAttrs(tracetest.SpanStubFromReadOnlySpan(ended[0])), attribute.Key("fullsend.tool_spans.dropped"))
	assert.Equal(t, int64(3), toolSpanAttrs(tracetest.SpanStubFromReadOnlySpan(ended[1]))["fullsend.tool_spans.dropped"].AsInt64())
}

func TestToolSpanTracker_NilIsInert(t *testing.T) {
	var tr *toolSpanTracker
	assert.NotPanics(t, func() {
		tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_08", Name: "Bash"})
		tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_08"})
		tr.Finish()
	})
}

func TestIterationEventHandler_RendersThenCollectsThenTracks(t *testing.T) {
	tr, rec, _ := toolSpanFixture(t)
	c := newContentCollector(4096)
	var rendered []agentruntime.AgentEvent
	handler := iterationEventHandler(func(e agentruntime.AgentEvent) { rendered = append(rendered, e) }, c, tr)
	require.NotNil(t, handler)

	handler(agentruntime.ToolUseEvent{ID: "toolu_09", Name: "Bash", Summary: "ls"})
	handler(agentruntime.ToolResultEvent{ID: "toolu_09", Result: "ok"})

	assert.Len(t, rendered, 2, "the console renderer sees every event")
	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"tool_call_response"`, "the collector still receives every event")
	assert.Len(t, endedToolSpans(rec), 1, "the tracker receives every event")
}

func TestIterationEventHandler_NilCollectorAndTrackerStillRender(t *testing.T) {
	// Tool spans are Level 1 metadata, so OnEvent is always set; supplying
	// it replaces the runtime's default renderer, which must therefore be
	// called here whatever else is off.
	var rendered []agentruntime.AgentEvent
	handler := iterationEventHandler(func(e agentruntime.AgentEvent) { rendered = append(rendered, e) }, nil, nil)
	require.NotNil(t, handler)
	handler(agentruntime.TextEvent{Text: "hello"})
	handler(agentruntime.ToolUseEvent{ID: "toolu_10", Name: "Bash"})
	assert.Len(t, rendered, 2)
}

func TestToolSpans_EndToEndFileSink(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "")
	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "")
	t.Setenv(telemetry.ContentCaptureEnvVar, "")

	dir := t.TempDir()
	tracer, cleanup := telemetry.Setup(dir, "test")
	agentCtx, agentSpan := tracer.Start(context.Background(), "agent")
	tr := newToolSpanTracker(tracer, agentCtx)
	tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_11", Name: "Grep", Summary: "TODO"})
	tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_11", Result: "3 hits"})
	tr.Finish()
	agentSpan.End()
	cleanup(context.Background())

	raw, err := os.ReadFile(filepath.Join(dir, telemetry.TelemetryFile))
	require.NoError(t, err)
	content := string(raw)
	assert.Equal(t, 1, strings.Count(content, `"execute_tool Grep"`))
	parent, err := json.Marshal(agentSpan.SpanContext().SpanID().String())
	require.NoError(t, err)
	assert.Contains(t, content, `"parentSpanId":`+string(parent),
		"the execute_tool span must be parented under the agent span in the file sink")
	assert.NotContains(t, content, "3 hits", "tool spans carry no content, gate off or on")
}

func TestToolSpanTracker_IDIsScannedBeforeItBecomesAnAttribute(t *testing.T) {
	// The id is stream-derived like the name and lands on a Level 1 span
	// the output scan exempts, so it gets the collector's redactID rule:
	// any finding drops the attribute (never a substituted id, which could
	// collide) while the raw id still keys use/result correlation.
	tr, rec, _ := toolSpanFixture(t)
	tainted := "toolu_ghp_" + strings.Repeat("A1b2C3d4", 5)
	tr.Handle(agentruntime.ToolUseEvent{ID: tainted, Name: "Bash"})
	tr.Handle(agentruntime.ToolResultEvent{ID: tainted, Result: "ok"})
	tr.Handle(agentruntime.ToolResultEvent{ID: "orphan_ghp_" + strings.Repeat("A1b2C3d4", 5)})

	spans := endedToolSpans(rec)
	require.Len(t, spans, 2, "the tainted pair still correlates into one span, plus the orphan")
	for _, s := range spans {
		attrs := toolSpanAttrs(s)
		assert.NotContains(t, attrs, attribute.Key("gen_ai.tool.call.id"), "a finding drops the id attribute")
		assert.NotContains(t, s.Name+attrs["gen_ai.tool.name"].AsString(), "ghp_")
	}
	assert.Equal(t, codes.Ok, spans[0].Status.Code)
	assert.True(t, toolSpanAttrs(spans[1])["fullsend.tool.unmatched"].AsBool())
	assert.Empty(t, tr.open)
}

func TestToolSpanTracker_RawIDKeysCorrelationEvenWhenMasksCollide(t *testing.T) {
	// Two distinct tainted ids mask to the same token. Keying the open-call
	// map by the masked form would make the second call supersede the first;
	// keying by the raw id keeps both pairs correlated.
	tr, rec, _ := toolSpanFixture(t)
	a := "toolu_ghp_" + strings.Repeat("A1b2C3d4", 5)
	b := "toolu_ghp_" + strings.Repeat("Z9y8X7w6", 5)
	tr.Handle(agentruntime.ToolUseEvent{ID: a, Name: "Bash"})
	tr.Handle(agentruntime.ToolUseEvent{ID: b, Name: "Read"})
	tr.Handle(agentruntime.ToolResultEvent{ID: a})
	tr.Handle(agentruntime.ToolResultEvent{ID: b})

	spans := endedToolSpans(rec)
	require.Len(t, spans, 2)
	for _, s := range spans {
		assert.Equal(t, codes.Ok, s.Status.Code)
		assert.NotContains(t, toolSpanAttrs(s), attribute.Key("error.type"))
		assert.NotContains(t, toolSpanAttrs(s), attribute.Key("gen_ai.tool.call.id"))
	}
	assert.Empty(t, tr.open)
}
