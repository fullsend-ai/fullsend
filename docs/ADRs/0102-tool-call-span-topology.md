---
title: "102. Tool-call span topology"
status: Accepted
relates_to:
  - operational-observability
topics:
  - observability
  - telemetry
  - opentelemetry
---

# 102. Tool-call span topology

Date: 2026-09-03

## Status

Accepted

## Context

[ADR 0050](0050-distributed-tracing-instrumentation.md) chose OpenTelemetry
and a three-level opt-in but never named the spans: the
`run → sandbox_create → agent` tree lives only in the guides, and granularity
was left to [#294](https://github.com/fullsend-ai/fullsend/issues/294).
Level 3 content ([#6429](https://github.com/fullsend-ai/fullsend/pull/6429),
[#6603](https://github.com/fullsend-ai/fullsend/pull/6603)) put tool calls
and results on the `agent` span as `gen_ai.output.messages` parts — at
semconv v1.37.0 the `execute_tool` span is metadata-only; its content
attributes (`gen_ai.tool.call.arguments`, `.result`) arrive in v1.38.0 as
Opt-In and Development. Review of #6603 asked why tool calls are not spans.

fullsend observes the runtime's stream rather than executing tools. The
normalized `ToolUseEvent`/`ToolResultEvent` pairs carry a call id, a tool
name and an `is_error` flag; `tool_use` lines carry no timestamp and
`tool_result` lines carry a sandbox-clock one; several calls are open at
once (parallel sub-agent dispatch); `parent_tool_use_id` is dropped at
decode; the pi and codex parsers pass no call ids through.

## Options

1. **Message record only** (status quo): tool calls stay parts of
   `gen_ai.output.messages`; no per-call timing or status in the span tree.
2. **`execute_tool` child spans from the normalized events**: one span per
   call under the iteration's `agent` span, metadata only; content stays on
   the message record.
3. **The runtime's native OpenTelemetry**: Claude Code emits
   `claude_code.tool` spans (beta) and honours inbound `TRACEPARENT` — true
   timing, but redaction would leave fullsend's pipeline, the threat model
   keeps runtime telemetry out of scope, and the sandbox would need egress.
4. **Per-tool content on the spans**: needs v1.38.0's Opt-In attributes,
   and the scorers read the message record today.

## Decision

Option 2. `toolSpanTracker` (`internal/cli/tool_spans.go`) opens an
`execute_tool <tool name>` span (kind Internal) when the runtime reports a
call and ends it when the result arrives. Both timestamps are runner-side
receipt instants — one clock — so the span brackets execution rather than
measuring it, and the start is arguments-complete. Attributes follow semconv
v1.37.0: `gen_ai.operation.name=execute_tool`, `gen_ai.tool.name`,
`gen_ai.tool.call.id`; a result flagged `is_error` sets
`error.type=tool_error` and status Error. Calls that never get a result
close as `error.type=unanswered`, results for calls never reported are
marked `fullsend.tool.unmatched`, and events without a call id (pi, codex,
server-side tools) get no span — the edge cases are specified in the
[dev guide](../guides/dev/tracing.md#execute_tool-spans). Names and call ids
pass through the same sanitizer as span content — names bounded, ids dropped
on any finding; at most 1,024
spans are recorded per iteration, so an agent-controlled burst cannot fill
the OTLP batch queue and evict the `agent` span, with the overflow counted
in `fullsend.tool_spans.dropped`. The spans are Level 1 metadata, emitted
regardless of the content gate. Tool content — results now, full arguments
next — stays on the `agent` span's `gen_ai.output.messages` record, which
is the scorer contract
([ADR 0087](0087-eval-measurements-online-trace-scoring.md)).

Option 3 is the candidate to revisit once the runtime's tracing is stable and
a redaction stage outside fullsend is designed; option 4 waits for the
convention bump.

## Consequences

- The span tree gains one level; readers that select spans by name
  (`evalmeasure`) are unaffected, and the `execute_tool` count can be below
  `fullsend.tool_calls`, which counts every reported call, id or not.
- Tool-heavy iterations add hundreds of spans, each one synchronous write to
  `run-telemetry.jsonl` and one OTLP batch entry.
- Sub-agent calls are flat children of the `agent` span; nesting them under
  the dispatching `Agent` call is a small follow-up now that the parent span
  exists while its children run (ADR 0050's deferred item 1 stays deferred).
- `execute_tool` spans are Claude-only until the pi and codex parsers pass
  their streams' call ids through.
- This settles the span-granularity question in #294; retention and access
  remain open there.
