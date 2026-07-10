# Agent Event Stream Rendering

**Date:** 2026-07-06
**Issue:** #3170
**Status:** Draft

## Problem

When an agent runs via `fullsend run`, the agent's reasoning and tool usage are
invisible until the run completes. The existing `progressParser` emits only tool
name + safe context (e.g. `Read: path/to/file`) via `printer.Heartbeat()`. It
ignores thinking text, assistant text, token usage, errors, retries, and
stream-level deltas. In GHA workflow logs the agent step appears as a
long-running black box with no incremental output.

## Decision

Implement full structured rendering of the agent event stream during execution,
behind the `Runtime` interface so it works for any supported runtime. Claude Code
exposes `--output-format stream-json`; opencode exposes `--format json` with
ndjson events. A normalized event type set bridges runtime-specific parsing and
runtime-agnostic rendering.

## Architecture

Three layers:

1. **Normalized event types** (`internal/runtime/event.go`) -- a Go interface
   `AgentEvent` with concrete structs for each event kind.
2. **Runtime-specific parsers** -- each runtime reads its native ndjson format
   and emits `AgentEvent` values via a callback.
3. **EventRenderer** (`internal/runtime/renderer.go`) -- consumes `AgentEvent`
   values and renders structured, colored terminal output via `*ui.Printer`.

```
sandbox stdout (ndjson)
    |
    v
parseClaudeStream()  -- or parseOpenCodeStream() for opencode
    |
    | calls onEvent(AgentEvent) synchronously per event
    v
EventRenderer.Handle()
    |
    v
ui.Printer  -->  stderr (terminal / GHA log)
```

The callback is synchronous: the parser blocks on the handler, which matches
the existing pattern where `progressParser` blocks on `printer.Heartbeat`.
No channels or extra goroutines.

## Event Types

File: `internal/runtime/event.go`

```go
// AgentEvent is the normalized event interface.
type AgentEvent interface{ agentEvent() }

type InitEvent struct {
    Model   string
    Version string // runtime version, optional
}

type ThinkingEvent struct {
    Text string // incremental thinking delta
}

type TextEvent struct {
    Text string // incremental assistant text delta
}

type ToolUseEvent struct {
    Name    string // "Bash", "Read", "Write", etc. ("tool" for unknown)
    Summary string // compact one-line summary
}

type TokensEvent struct {
    InputTokens  int
    OutputTokens int
    CacheRead    int
    CacheWrite   int
}

type ResultEvent struct {
    NumTurns                 int
    TotalCostUSD             float64
    IsError                  bool
    ErrorMessage             string
    Subtype                  string // "success", "error_max_turns", etc.
    InputTokens              int
    OutputTokens             int
    CacheCreationInputTokens int
    CacheReadInputTokens     int
}

type ErrorEvent struct {
    ErrorType string
    Message   string
}

type RetryEvent struct {
    Attempt    int
    MaxRetries int
    DelayMs    int
    Error      string
}
```

`ToolUseEvent` carries a pre-computed `Summary` rather than raw input params.
The runtime-specific parser builds the summary using the existing
`extractSafeContext` logic (expanded to match agentic-ci's `_format_tool`
coverage). This keeps the renderer simple and avoids leaking raw tool arguments
across the abstraction boundary.

`ThinkingEvent` and `TextEvent` carry incremental deltas so the renderer prints
text as it arrives, matching agentic-ci's streaming behavior.

Each struct embeds a no-op `agentEvent()` marker method to satisfy the interface.

## Callback Integration

A new field on `RunParams`:

```go
type RunParams struct {
    // ... existing fields unchanged ...
    OnEvent func(AgentEvent) // if nil, events are silently discarded
}
```

The runtime's `Run` method calls `params.OnEvent(evt)` as it parses each line.

## Claude Stream Parser Refactor

File: `internal/runtime/claude_progress.go`

The existing `progressParser` is replaced by:

```go
func parseClaudeStream(r io.Reader, onEvent func(AgentEvent)) error
```

It processes all event types from the `stream-json` format:

| stream-json event | Emitted AgentEvent |
|---|---|
| `system` / `init` | `InitEvent{Model, Version}` |
| `system` / `api_retry` | `RetryEvent{Attempt, MaxRetries, DelayMs, Error}` |
| `stream_event` / `content_block_start` (text) | sets internal state |
| `stream_event` / `content_block_start` (thinking) | sets internal state |
| `stream_event` / `content_block_start` (tool_use) | sets internal state, records tool name |
| `stream_event` / `content_block_delta` (text_delta) | `TextEvent{Text}` |
| `stream_event` / `content_block_delta` (thinking_delta) | `ThinkingEvent{Text}` |
| `stream_event` / `content_block_delta` (input_json_delta) | accumulates tool input JSON |
| `stream_event` / `content_block_stop` (tool block) | `ToolUseEvent{Name, Summary}` |
| `stream_event` / `message_delta` | `TokensEvent` (throttled to every 5k tokens) |
| `stream_event` / `error` | `ErrorEvent{ErrorType, Message}` |
| `result` | `ResultEvent` with all metric fields |
| `assistant` | if no `stream_event` has been seen yet, parse tool_use blocks and emit `ToolUseEvent` (supports older Claude Code versions that emit complete messages without streaming deltas) |

The existing `extractSafeContext`, `extractBinaryName`, and `allowedTools`
functions are reused to build `ToolUseEvent.Summary`. Unknown tools get
name `"tool"` and empty summary.

`progressParser` is a thin wrapper that creates a renderer and metrics handler:

```go
func progressParser(r io.Reader, printer *ui.Printer, metrics *RunMetrics) error {
    renderer := NewEventRenderer(printer)
    return parseClaudeStream(r, func(evt AgentEvent) {
        // populate metrics from events
        renderer.Handle(evt)
    })
}
```

Metrics population (model, tool count, result stats) is handled by the caller's
wrapper — not the renderer — so custom `OnEvent` handlers also get correct metrics.

## EventRenderer

File: `internal/runtime/renderer.go`

`EventRenderer` is a pure rendering concern — it writes formatted output to the
printer but does not populate `RunMetrics`.

```go
type EventRenderer struct {
    printer    *ui.Printer
    isCI       bool
    inText     bool
    inThinking bool
}

func NewEventRenderer(printer *ui.Printer) *EventRenderer
func (r *EventRenderer) Handle(evt AgentEvent)
```

`Handle` dispatches on event type:

| Event | Rendering |
|---|---|
| `InitEvent` | `printer.Header` with model/version, `printer.KeyValue` for details |
| `ThinkingEvent` | dim/italic prefix "Thinking" then incremental text via `printer.Raw` |
| `TextEvent` | prefix "Claude" then incremental text via `printer.Raw` |
| `ToolUseEvent` | wrench icon + name + summary via `printer.Heartbeat`, increments `metrics.ToolCalls` |
| `TokensEvent` | token stats via `printer.StepInfo`, only when total crosses 5k boundary |
| `ResultEvent` | populates `metrics.*` fields, renders summary via `printer.Header` + `printer.KeyValue` |
| `ErrorEvent` | `printer.StepFail` with error type and message |
| `RetryEvent` | `printer.StepWarn` with attempt count and error |

Block state tracking: the renderer tracks `inText` / `inThinking`. When a new
block starts (ThinkingEvent after text, ToolUseEvent after thinking, etc.), the
renderer closes the previous block by emitting a newline and resetting style.
This is the same state machine as agentic-ci's `_end_block()`.

For CI (`GITHUB_ACTIONS=true`), tool use events also emit `::notice::`
annotations to stderr, same as today.

No new `Printer` methods are needed. `Raw()` handles incremental text output.
The renderer uses `lipgloss` directly for italic/dim styling on thinking text,
writing through `printer.Raw()`.

## ClaudeRuntime.Run Wiring

Minimal change to `claude.go`:

```go
func (ClaudeRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer,
    start time.Time, metrics *RunMetrics) (int, error) {
    // ... existing sandbox exec + TeeReader setup unchanged ...

    handler := params.OnEvent
    if handler == nil {
        renderer := NewEventRenderer(printer)
        handler = renderer.Handle
    }
    // Always wrap to capture metrics regardless of custom/default handler.
    innerHandler := handler
    handler = func(evt AgentEvent) {
        // populate metrics from InitEvent, ResultEvent, ToolUseEvent
        innerHandler(evt)
    }

    if parseErr := parseClaudeStream(r, handler); parseErr != nil {
        // ... existing error handling unchanged ...
    }
    // ... existing wait logic unchanged ...
}
```

When `fullsend run` in `run.go` sets up the call, it passes `OnEvent: nil` to
get the default rendering. Custom handlers can be provided for testing or
alternative output modes.

## Information Disclosure

The existing `allowedTools` safeguard carries over unchanged -- unknown tools
get name `"tool"` and empty summary.

Thinking and text events contain the model's own output inside the sandbox. This
is the same content that ends up in the transcript JSONL artifact. Anyone who
can see the GHA logs can already see the transcript artifact. If redaction is
needed later, the `EventRenderer` is the natural place to add a filter.

## Future: opencode Integration

When opencode lands, it adds:

```go
func parseOpenCodeStream(r io.Reader, onEvent func(AgentEvent)) error
```

This maps opencode's ndjson events (`tool_use`, `text`, `thinking`,
`step_start`, `step_finish`, `error`) to the same `AgentEvent` types. The
`EventRenderer` works unchanged. The event types become the format-neutral
bridge between runtime-specific parsing and runtime-agnostic rendering, which
is the contract issue #1935 is asking for.

## Testing Strategy

- **Event types**: Unit tests that parse known Claude Code stream-json fixtures
  and assert the sequence of `AgentEvent` values emitted. Fixtures derived from
  real agent runs (sanitized).
- **Renderer**: Unit tests that feed `AgentEvent` sequences to `EventRenderer`
  and assert the output written to a `bytes.Buffer` via the Printer.
- **Integration**: The existing `claude_progress_test.go` tests are updated to
  verify the new parser produces equivalent output to the old one for the same
  input fixtures.
- **Block state machine**: Tests for edge cases -- thinking followed by text,
  tool_use inside thinking, multiple consecutive tools, empty deltas.
- **Sanitization**: Tests that `allowedTools` filtering and `sanitizeOutput`
  are applied correctly to all rendered output.

## Files Changed

| File | Change |
|---|---|
| `internal/runtime/event.go` | New: normalized event type definitions |
| `internal/runtime/renderer.go` | New: `EventRenderer` struct and `Handle` method |
| `internal/runtime/claude_progress.go` | Refactor: replace `progressParser` with `parseClaudeStream`, keep `progressParser` as wrapper |
| `internal/runtime/runtime.go` | Add `OnEvent` field to `RunParams` |
| `internal/runtime/claude.go` | Wire `OnEvent` / default `EventRenderer` in `Run` |
| `internal/runtime/renderer_test.go` | New: renderer unit tests |
| `internal/runtime/claude_progress_test.go` | Update: verify new parser against existing fixtures |

## Related Issues

- #663 -- Evaluate showboat for code agent work visibility (different approach)
- #1591 -- `StepDebug` and `--verbose` flag (complementary: debug internals vs agent progress)
- #1935 -- `TranscriptHandler` format-neutral contract (event types serve as that contract for live streaming)
- #872 -- Surface failure reasons when logs inaccessible (partially addressed by live progress)
