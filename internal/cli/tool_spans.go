package cli

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// maxToolSpansPerIteration bounds how many execute_tool spans one iteration
// may record. The count of calls is under the sandboxed agent's control,
// and Finish ends every open call in a burst right before the agent span
// ends: the OTLP batch processor's queue (2048 by default) drops newest
// spans when full, so an unbounded burst would evict the agent span — the
// one carrying the iteration's content. Half the queue leaves room for the
// rest of the trace; real review iterations run 117-255 calls.
const maxToolSpansPerIteration = 1024

// maxToolNameBytes bounds gen_ai.tool.name. The name comes from the
// sandboxed agent's stream; real names (mcp__server__tool included) are
// well under 100 bytes.
const maxToolNameBytes = 256

// maxToolSpanNameBytes bounds the tool-name part of an execute_tool span
// name. A span name is not an attribute, so no SDK limit ever applied to
// it.
const maxToolSpanNameBytes = 128

// toolSpanTracker turns the tool calls one iteration's runtime stream
// reports into execute_tool child spans of that iteration's agent span
// (semconv v1.37.0: gen_ai.operation.name, gen_ai.tool.name,
// gen_ai.tool.call.id, error.type on failure; span kind Internal). Tool
// content stays on the agent span's gen_ai.output.messages record — these
// spans are Level 1 metadata and are emitted whether or not the content
// gate is on.
//
// A span starts when the ToolUseEvent arrives and ends when the matching
// ToolResultEvent arrives, so both timestamps are runner-side receipt
// instants on one clock: tool_use arrival is arguments-complete rather
// than execution start, and tool_result arrival trails execution end by
// the pipe latency. Calls are keyed by id because the stream interleaves
// several open calls (parallel sub-agent dispatch); a call that never gets
// a result — the runtime was stopped, or its result line exceeded the
// parser's 1 MiB cap — is ended by Finish as error.type=unanswered, and a
// result whose call was never seen (its tool_use line was skipped) becomes
// a marked span of near-zero duration. Events without an id — pi and codex
// emit none, and server-side tools get none because their result never
// arrives as a tool_result — produce no span. The name goes through the
// same output pipeline as span content (Unicode normalization, then secret
// redaction) and is bounded before it reaches the span: it lands on a Level
// 1 span in the telemetry file the output scan exempts on the strength of
// that treatment. Delivery is synchronous on one goroutine, so no lock.
type toolSpanTracker struct {
	tracer   trace.Tracer
	ctx      context.Context
	pipeline *security.Pipeline
	open     map[string]trace.Span
	created  int
	dropped  int
}

func newToolSpanTracker(tracer trace.Tracer, agentCtx context.Context) *toolSpanTracker {
	return &toolSpanTracker{
		tracer:   tracer,
		ctx:      agentCtx,
		pipeline: security.OutputPipeline(),
		open:     map[string]trace.Span{},
	}
}

// Handle opens a span for a tool call and ends it on the call's result. A
// nil tracker is inert.
func (t *toolSpanTracker) Handle(evt agentruntime.AgentEvent) {
	if t == nil {
		return
	}
	switch e := evt.(type) {
	case agentruntime.ToolUseEvent:
		id := boundedID(e.ID)
		if id == "" || !t.allow() {
			return
		}
		if prev, ok := t.open[id]; ok {
			endUnanswered(prev)
		}
		t.open[id] = t.start(id, e.Name)
	case agentruntime.ToolResultEvent:
		id := boundedID(e.ID)
		if id == "" {
			return
		}
		span, ok := t.open[id]
		if ok {
			delete(t.open, id)
		} else {
			if !t.allow() {
				return
			}
			span = t.start(id, "")
			span.SetAttributes(attribute.Bool("fullsend.tool.unmatched", true))
		}
		finalizeToolSpan(span, e.IsError)
	}
}

// Finish ends every call still open as unanswered and returns how many
// calls got no span because the iteration passed maxToolSpansPerIteration;
// call it before the agent span ends. A nil tracker is inert.
func (t *toolSpanTracker) Finish() int {
	if t == nil {
		return 0
	}
	for id, span := range t.open {
		endUnanswered(span)
		delete(t.open, id)
	}
	return t.dropped
}

// allow reports whether one more span may be created this iteration.
func (t *toolSpanTracker) allow() bool {
	if t.created >= maxToolSpansPerIteration {
		t.dropped++
		return false
	}
	t.created++
	return true
}

// safeName sanitizes the stream-derived tool name and bounds it — in that
// order, so a cut can never split a credential past recognition. An empty
// result with findings means the whole name was sanitized away (the same
// reading contentCollector.redact applies), so nothing is shown.
func (t *toolSpanTracker) safeName(name string) string {
	scanned := t.pipeline.Scan(name)
	if scanned.Sanitized != "" {
		name = scanned.Sanitized
	} else if len(scanned.Findings) > 0 {
		return ""
	}
	return strings.ToValidUTF8(truncateStatusMsgTo(name, maxToolNameBytes), "")
}

func (t *toolSpanTracker) start(id, name string) trace.Span {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "execute_tool"),
		stringAttr("gen_ai.tool.call.id", id),
	}
	spanName := "execute_tool"
	if name != "" {
		name = t.safeName(name)
	}
	if name != "" {
		attrs = append(attrs, attribute.String("gen_ai.tool.name", name))
		spanName += " " + strings.ToValidUTF8(truncateStatusMsgTo(name, maxToolSpanNameBytes), "")
	}
	_, span := t.tracer.Start(t.ctx, spanName,
		trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...))
	return span
}

// finalizeToolSpan ends a tool span from the wire's is_error flag — the
// only failure signal the stream carries, so error.type is a fixed value
// and no exception event is recorded.
func finalizeToolSpan(span trace.Span, isError bool) {
	if isError {
		span.SetAttributes(attribute.String("error.type", "tool_error"))
		span.SetStatus(codes.Error, "tool reported is_error")
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

func endUnanswered(span trace.Span) {
	span.SetAttributes(attribute.String("error.type", "unanswered"))
	span.SetStatus(codes.Error, "no tool_result before the iteration ended")
	span.End()
}
