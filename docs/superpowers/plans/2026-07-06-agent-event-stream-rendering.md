# Agent Event Stream Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render the full agent event stream (thinking, text, tool calls, tokens, errors) live during `fullsend run` execution, behind the `Runtime` interface so any runtime can plug in.

**Architecture:** Three layers: normalized `AgentEvent` types (interface + concrete structs), a Claude-specific NDJSON parser that emits events via a callback, and a runtime-agnostic `EventRenderer` that renders structured colored output via `*ui.Printer`. The parser calls the handler synchronously -- no channels or goroutines.

**Tech Stack:** Go, lipgloss (terminal styling), existing `internal/ui` and `internal/runtime` packages.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/runtime/event.go` (new) | `AgentEvent` interface + concrete event structs |
| `internal/runtime/event_test.go` (new) | Verify marker method compilation, event type assertions |
| `internal/runtime/renderer.go` (new) | `EventRenderer` struct: consumes `AgentEvent`, renders via `*ui.Printer` |
| `internal/runtime/renderer_test.go` (new) | Renderer unit tests: each event type, block transitions, CI annotations |
| `internal/runtime/claude_progress.go` (modify) | Replace `progressParser` internals with `parseClaudeStream` + callback; keep `progressParser` as wrapper |
| `internal/runtime/claude_progress_test.go` (modify) | Add `parseClaudeStream` tests for stream_event deltas; existing `progressParser` tests stay as integration tests |
| `internal/runtime/runtime.go` (modify) | Add `OnEvent func(AgentEvent)` field to `RunParams` |
| `internal/runtime/claude.go` (modify) | Wire `OnEvent` / default `EventRenderer` in `ClaudeRuntime.Run` |

---

### Task 1: Define normalized event types

**Files:**
- Create: `internal/runtime/event.go`
- Create: `internal/runtime/event_test.go`

- [ ] **Step 1: Write the event type definitions**

Create `internal/runtime/event.go`:

```go
package runtime

// AgentEvent is the normalized event interface for runtime-agnostic rendering.
// Each concrete event type implements this with a no-op marker method.
type AgentEvent interface {
	agentEvent()
}

// InitEvent is emitted once at stream start with runtime metadata.
type InitEvent struct {
	Model   string
	Version string
}

func (InitEvent) agentEvent() {}

// ThinkingEvent carries an incremental thinking text delta.
type ThinkingEvent struct {
	Text string
}

func (ThinkingEvent) agentEvent() {}

// TextEvent carries an incremental assistant text delta.
type TextEvent struct {
	Text string
}

func (TextEvent) agentEvent() {}

// ToolUseEvent is emitted when a tool invocation completes.
// Name is the tool name ("tool" for unknown/non-allowlisted tools).
// Summary is a pre-computed one-line description safe for display.
type ToolUseEvent struct {
	Name    string
	Summary string
}

func (ToolUseEvent) agentEvent() {}

// TokensEvent carries incremental token usage counters.
type TokensEvent struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
}

func (TokensEvent) agentEvent() {}

// ResultEvent is emitted once at stream end with final metrics.
type ResultEvent struct {
	NumTurns                 int
	TotalCostUSD             float64
	IsError                  bool
	ErrorMessage             string
	Subtype                  string
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

func (ResultEvent) agentEvent() {}

// ErrorEvent is emitted when the runtime reports an error.
type ErrorEvent struct {
	ErrorType string
	Message   string
}

func (ErrorEvent) agentEvent() {}

// RetryEvent is emitted when the runtime retries an API call.
type RetryEvent struct {
	Attempt    int
	MaxRetries int
	DelayMs    int
	Error      string
}

func (RetryEvent) agentEvent() {}
```

- [ ] **Step 2: Write a compilation test for the interface**

Create `internal/runtime/event_test.go`:

```go
package runtime

import "testing"

func TestAgentEventInterfaceSatisfied(t *testing.T) {
	// Compile-time verification that all concrete types satisfy AgentEvent.
	var events []AgentEvent
	events = append(events,
		InitEvent{Model: "claude-opus-4-6", Version: "1.0.0"},
		ThinkingEvent{Text: "thinking..."},
		TextEvent{Text: "hello"},
		ToolUseEvent{Name: "Read", Summary: "/src/main.go"},
		TokensEvent{InputTokens: 100, OutputTokens: 50, CacheRead: 30, CacheWrite: 20},
		ResultEvent{NumTurns: 5, TotalCostUSD: 0.42},
		ErrorEvent{ErrorType: "overloaded", Message: "rate limited"},
		RetryEvent{Attempt: 1, MaxRetries: 3, DelayMs: 1000, Error: "timeout"},
	)
	if len(events) != 8 {
		t.Errorf("expected 8 event types, got %d", len(events))
	}
}
```

- [ ] **Step 3: Run test to verify it passes**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -run TestAgentEventInterfaceSatisfied -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/runtime/event.go internal/runtime/event_test.go
git commit -S -s -m "$(cat <<'EOF'
refactor(runtime): add normalized AgentEvent types for stream rendering

Define the AgentEvent interface and concrete event structs (InitEvent,
ThinkingEvent, TextEvent, ToolUseEvent, TokensEvent, ResultEvent,
ErrorEvent, RetryEvent) that bridge runtime-specific NDJSON parsing
and runtime-agnostic rendering.

Part of #3170.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Add OnEvent callback to RunParams

**Files:**
- Modify: `internal/runtime/runtime.go`

- [ ] **Step 1: Add the OnEvent field**

In `internal/runtime/runtime.go`, add the `OnEvent` field to `RunParams` after the `OutputPath` field:

```go
// RunParams configures a single agent invocation inside the sandbox.
type RunParams struct {
	SandboxName   string
	AgentBaseName string
	Model         string
	RepoDir       string
	PluginDirs    []string
	Debug         string
	Timeout       time.Duration
	OutputPath    string         // if set, tee stream-json stdout to this file
	OnEvent       func(AgentEvent) // if non-nil, called with normalized events during Run
}
```

- [ ] **Step 2: Run existing tests to verify nothing breaks**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -v`
Expected: all existing tests PASS (adding an optional field breaks nothing)

- [ ] **Step 3: Commit**

```bash
git add internal/runtime/runtime.go
git commit -S -s -m "$(cat <<'EOF'
refactor(runtime): add OnEvent callback to RunParams

Add an optional OnEvent func(AgentEvent) field to RunParams. When set,
the runtime calls it with normalized events during execution. When nil,
events are silently discarded (or the runtime uses a default renderer).

Part of #3170.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Implement EventRenderer

**Files:**
- Create: `internal/runtime/renderer.go`
- Create: `internal/runtime/renderer_test.go`

- [ ] **Step 1: Write failing tests for the renderer**

Create `internal/runtime/renderer_test.go`:

```go
package runtime

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newTestRenderer(buf *bytes.Buffer) (*EventRenderer, *RunMetrics) {
	printer := ui.New(buf)
	metrics := &RunMetrics{}
	r := NewEventRenderer(printer, time.Now(), metrics)
	return r, metrics
}

func TestRendererInitEvent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(InitEvent{Model: "claude-opus-4-6", Version: "1.2.3"})

	output := buf.String()
	if !strings.Contains(output, "claude-opus-4-6") {
		t.Errorf("expected model in output, got: %s", output)
	}
}

func TestRendererToolUseEvent(t *testing.T) {
	var buf bytes.Buffer
	r, metrics := newTestRenderer(&buf)

	r.Handle(ToolUseEvent{Name: "Read", Summary: "/src/main.go"})

	output := buf.String()
	if !strings.Contains(output, "Read") {
		t.Errorf("expected tool name in output, got: %s", output)
	}
	if !strings.Contains(output, "/src/main.go") {
		t.Errorf("expected summary in output, got: %s", output)
	}
	if metrics.ToolCalls.Load() != 1 {
		t.Errorf("expected 1 tool call, got %d", metrics.ToolCalls.Load())
	}
}

func TestRendererToolUseEventCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	var buf bytes.Buffer
	// In CI mode the renderer writes ::notice:: to stderr.
	// We capture stderr by creating the renderer with a separate writer.
	oldStderr := os.Stderr
	stderrR, stderrW, _ := os.Pipe()
	os.Stderr = stderrW
	defer func() { os.Stderr = oldStderr }()

	r, _ := newTestRenderer(&buf)
	r.Handle(ToolUseEvent{Name: "Bash", Summary: "make"})

	stderrW.Close()
	var stderrBuf bytes.Buffer
	stderrBuf.ReadFrom(stderrR)

	if !strings.Contains(stderrBuf.String(), "::notice::") {
		t.Errorf("expected ::notice:: annotation in CI mode, got: %s", stderrBuf.String())
	}
}

func TestRendererThinkingEvent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(ThinkingEvent{Text: "Let me consider"})

	output := buf.String()
	if !strings.Contains(output, "Let me consider") {
		t.Errorf("expected thinking text in output, got: %s", output)
	}
}

func TestRendererTextEvent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(TextEvent{Text: "Here is the answer"})

	output := buf.String()
	if !strings.Contains(output, "Here is the answer") {
		t.Errorf("expected text in output, got: %s", output)
	}
}

func TestRendererBlockTransition(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	// Thinking -> Tool should close thinking block first
	r.Handle(ThinkingEvent{Text: "pondering"})
	r.Handle(ToolUseEvent{Name: "Read", Summary: "/a.go"})

	output := buf.String()
	if !strings.Contains(output, "pondering") {
		t.Errorf("expected thinking text, got: %s", output)
	}
	if !strings.Contains(output, "Read") {
		t.Errorf("expected tool name after block transition, got: %s", output)
	}
}

func TestRendererResultEvent(t *testing.T) {
	var buf bytes.Buffer
	r, metrics := newTestRenderer(&buf)

	r.Handle(ResultEvent{
		NumTurns:                 8,
		TotalCostUSD:             0.42,
		InputTokens:              12000,
		OutputTokens:             3400,
		CacheCreationInputTokens: 8000,
		CacheReadInputTokens:     5000,
	})

	if metrics.NumTurns != 8 {
		t.Errorf("expected 8 turns, got %d", metrics.NumTurns)
	}
	if metrics.TotalCostUSD != 0.42 {
		t.Errorf("expected cost 0.42, got %f", metrics.TotalCostUSD)
	}
	if metrics.InputTokens != 12000 {
		t.Errorf("expected 12000 input tokens, got %d", metrics.InputTokens)
	}
	if metrics.OutputTokens != 3400 {
		t.Errorf("expected 3400 output tokens, got %d", metrics.OutputTokens)
	}
	if metrics.CacheCreationInputTokens != 8000 {
		t.Errorf("expected 8000 cache creation tokens, got %d", metrics.CacheCreationInputTokens)
	}
	if metrics.CacheReadInputTokens != 5000 {
		t.Errorf("expected 5000 cache read tokens, got %d", metrics.CacheReadInputTokens)
	}
}

func TestRendererErrorEvent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(ErrorEvent{ErrorType: "overloaded_error", Message: "rate limited"})

	output := buf.String()
	if !strings.Contains(output, "overloaded_error") {
		t.Errorf("expected error type in output, got: %s", output)
	}
	if !strings.Contains(output, "rate limited") {
		t.Errorf("expected error message in output, got: %s", output)
	}
}

func TestRendererRetryEvent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(RetryEvent{Attempt: 2, MaxRetries: 5, DelayMs: 1000, Error: "timeout"})

	output := buf.String()
	if !strings.Contains(output, "2") {
		t.Errorf("expected attempt number in output, got: %s", output)
	}
	if !strings.Contains(output, "timeout") {
		t.Errorf("expected error in output, got: %s", output)
	}
}

func TestRendererTokensEventThrottled(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	// First tokens event should render (total crosses 0 -> 5k threshold)
	r.Handle(TokensEvent{InputTokens: 4000, OutputTokens: 2000})
	first := buf.String()

	// Second event just below next 5k boundary should not add output
	buf.Reset()
	r.Handle(TokensEvent{InputTokens: 4000, OutputTokens: 2500})
	second := buf.String()

	if first == "" {
		t.Error("expected first tokens event to render")
	}
	if second != "" {
		t.Errorf("expected second tokens event to be throttled, got: %s", second)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -run TestRenderer -v`
Expected: FAIL with "undefined: NewEventRenderer"

- [ ] **Step 3: Implement the renderer**

Create `internal/runtime/renderer.go`:

```go
package runtime

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// EventRenderer renders normalized AgentEvent values to a Printer.
// It tracks block state (text/thinking) so transitions between event
// types produce clean output boundaries.
type EventRenderer struct {
	printer        *ui.Printer
	start          time.Time
	metrics        *RunMetrics
	isCI           bool
	inText         bool
	inThinking     bool
	lastTokenTotal int
}

// NewEventRenderer creates a renderer that writes to the given printer
// and populates metrics as events arrive.
func NewEventRenderer(printer *ui.Printer, start time.Time, metrics *RunMetrics) *EventRenderer {
	return &EventRenderer{
		printer: printer,
		start:   start,
		metrics: metrics,
		isCI:    os.Getenv("GITHUB_ACTIONS") == "true",
	}
}

var (
	thinkingStyle = lipgloss.NewStyle().Italic(true).Foreground(ui.ColorMuted)
)

// Handle dispatches a single AgentEvent to the appropriate rendering method.
func (r *EventRenderer) Handle(evt AgentEvent) {
	switch e := evt.(type) {
	case InitEvent:
		r.endBlock()
		label := e.Model
		if e.Version != "" {
			label = fmt.Sprintf("%s (v%s)", e.Model, e.Version)
		}
		r.printer.Header("Agent: " + label)
	case ThinkingEvent:
		if !r.inThinking {
			r.endBlock()
			r.printer.Raw(thinkingStyle.Render("  \U0001f9e0 "))
			r.inThinking = true
		}
		r.printer.Raw(thinkingStyle.Render(e.Text))
	case TextEvent:
		if !r.inText {
			r.endBlock()
			r.printer.Raw("  \U0001f4ac ")
			r.inText = true
		}
		r.printer.Raw(e.Text)
	case ToolUseEvent:
		r.endBlock()
		count := r.metrics.ToolCalls.Add(1)
		elapsed := time.Since(r.start).Truncate(time.Second)
		var msg string
		if e.Summary != "" {
			msg = fmt.Sprintf("%s: %s (%s, %d tools)", e.Name, e.Summary, elapsed, count)
		} else {
			msg = fmt.Sprintf("%s (%s, %d tools)", e.Name, elapsed, count)
		}
		msg = sanitizeOutput(msg)
		if r.isCI {
			fmt.Fprintf(os.Stderr, "::notice::%s\n", msg)
		}
		r.printer.Heartbeat(msg)
	case TokensEvent:
		total := e.InputTokens + e.OutputTokens + e.CacheRead + e.CacheWrite
		if total-r.lastTokenTotal >= 5000 {
			r.endBlock()
			r.lastTokenTotal = total
			r.printer.StepInfo(fmt.Sprintf(
				"TOKENS in=%d out=%d cache_r=%d cache_w=%d total=%d",
				e.InputTokens, e.OutputTokens, e.CacheRead, e.CacheWrite, total,
			))
		}
	case ResultEvent:
		r.endBlock()
		r.metrics.NumTurns = e.NumTurns
		r.metrics.TotalCostUSD = e.TotalCostUSD
		r.metrics.InputTokens = e.InputTokens
		r.metrics.OutputTokens = e.OutputTokens
		r.metrics.CacheCreationInputTokens = e.CacheCreationInputTokens
		r.metrics.CacheReadInputTokens = e.CacheReadInputTokens
		label := "Result"
		if e.Subtype != "" {
			label = fmt.Sprintf("Result: %s", e.Subtype)
		}
		if e.IsError {
			label = "Result: ERROR"
			if e.Subtype != "" {
				label = fmt.Sprintf("Result: ERROR (%s)", e.Subtype)
			}
		}
		r.printer.Header(label)
		r.printer.KeyValue("Turns", fmt.Sprintf("%d", e.NumTurns))
		r.printer.KeyValue("Cost", fmt.Sprintf("$%.4f", e.TotalCostUSD))
		r.printer.KeyValue("Tokens", fmt.Sprintf(
			"in=%d out=%d cache_create=%d cache_read=%d",
			e.InputTokens, e.OutputTokens,
			e.CacheCreationInputTokens, e.CacheReadInputTokens,
		))
		if e.IsError && e.ErrorMessage != "" {
			r.printer.StepFail(sanitizeOutput(e.ErrorMessage))
		}
	case ErrorEvent:
		r.endBlock()
		r.printer.StepFail(fmt.Sprintf("%s: %s",
			sanitizeOutput(e.ErrorType), sanitizeOutput(e.Message)))
	case RetryEvent:
		r.endBlock()
		r.printer.StepWarn(fmt.Sprintf("Retry %d/%d: %s (delay %dms)",
			e.Attempt, e.MaxRetries, sanitizeOutput(e.Error), e.DelayMs))
	}
}

// endBlock closes any open text or thinking block.
func (r *EventRenderer) endBlock() {
	if r.inText || r.inThinking {
		r.printer.Raw("\n")
		r.inText = false
		r.inThinking = false
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -run TestRenderer -v`
Expected: all TestRenderer* tests PASS

- [ ] **Step 5: Run full test suite to check for regressions**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -v`
Expected: all tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/renderer.go internal/runtime/renderer_test.go
git commit -S -s -m "$(cat <<'EOF'
refactor(runtime): add EventRenderer for structured agent output

EventRenderer consumes normalized AgentEvent values and renders
structured terminal output: thinking text (italic/dim), assistant text,
tool summaries with elapsed time and count, token usage (throttled),
result summaries, errors, and retry warnings. Tracks block state for
clean transitions between event types.

Part of #3170.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Refactor Claude stream parser to emit AgentEvent

**Files:**
- Modify: `internal/runtime/claude_progress.go`
- Modify: `internal/runtime/claude_progress_test.go`

- [ ] **Step 1: Write failing tests for parseClaudeStream**

Add the following tests to `internal/runtime/claude_progress_test.go`:

```go
func collectEvents(t *testing.T, input string) []AgentEvent {
	t.Helper()
	var events []AgentEvent
	err := parseClaudeStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	if err != nil {
		t.Fatalf("parseClaudeStream returned error: %v", err)
	}
	return events
}

func TestParseClaudeStreamInitEvent(t *testing.T) {
	input := `{"type":"system","subtype":"init","model":"claude-opus-4-6","claude_code_version":"1.0.50"}`
	events := collectEvents(t, input)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	init, ok := events[0].(InitEvent)
	if !ok {
		t.Fatalf("expected InitEvent, got %T", events[0])
	}
	if init.Model != "claude-opus-4-6" {
		t.Errorf("expected model claude-opus-4-6, got %q", init.Model)
	}
	if init.Version != "1.0.50" {
		t.Errorf("expected version 1.0.50, got %q", init.Version)
	}
}

func TestParseClaudeStreamRetryEvent(t *testing.T) {
	input := `{"type":"system","subtype":"api_retry","attempt":2,"max_retries":5,"retry_delay_ms":1000,"error":"overloaded"}`
	events := collectEvents(t, input)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	retry, ok := events[0].(RetryEvent)
	if !ok {
		t.Fatalf("expected RetryEvent, got %T", events[0])
	}
	if retry.Attempt != 2 || retry.MaxRetries != 5 || retry.DelayMs != 1000 || retry.Error != "overloaded" {
		t.Errorf("unexpected retry event: %+v", retry)
	}
}

func TestParseClaudeStreamTextDeltas(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	var texts []string
	for _, e := range events {
		if te, ok := e.(TextEvent); ok {
			texts = append(texts, te.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "hello " || texts[1] != "world" {
		t.Errorf("expected two text deltas [hello ] [world], got %v", texts)
	}
}

func TestParseClaudeStreamThinkingDeltas(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	var thinking []string
	for _, e := range events {
		if te, ok := e.(ThinkingEvent); ok {
			thinking = append(thinking, te.Text)
		}
	}
	if len(thinking) != 1 || thinking[0] != "hmm" {
		t.Errorf("expected one thinking delta [hmm], got %v", thinking)
	}
}

func TestParseClaudeStreamToolUse(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Read"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"/src/main.go\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	var tools []ToolUseEvent
	for _, e := range events {
		if te, ok := e.(ToolUseEvent); ok {
			tools = append(tools, te)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool event, got %d", len(tools))
	}
	if tools[0].Name != "Read" {
		t.Errorf("expected tool name Read, got %q", tools[0].Name)
	}
	if tools[0].Summary != "/src/main.go" {
		t.Errorf("expected summary /src/main.go, got %q", tools[0].Summary)
	}
}

func TestParseClaudeStreamUnknownTool(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"EvilTool"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"secret\":\"password123\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	var tools []ToolUseEvent
	for _, e := range events {
		if te, ok := e.(ToolUseEvent); ok {
			tools = append(tools, te)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool event, got %d", len(tools))
	}
	if tools[0].Name != "tool" {
		t.Errorf("expected masked tool name 'tool', got %q", tools[0].Name)
	}
	if tools[0].Summary != "" {
		t.Errorf("expected empty summary for unknown tool, got %q", tools[0].Summary)
	}
}

func TestParseClaudeStreamResultEvent(t *testing.T) {
	input := `{"type":"result","num_turns":8,"total_cost_usd":0.42,"is_error":false,"subtype":"success","usage":{"input_tokens":12000,"output_tokens":3400,"cache_creation_input_tokens":8000,"cache_read_input_tokens":5000}}`
	events := collectEvents(t, input)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	result, ok := events[0].(ResultEvent)
	if !ok {
		t.Fatalf("expected ResultEvent, got %T", events[0])
	}
	if result.NumTurns != 8 || result.TotalCostUSD != 0.42 {
		t.Errorf("unexpected result: %+v", result)
	}
	if result.InputTokens != 12000 || result.OutputTokens != 3400 {
		t.Errorf("unexpected token counts: %+v", result)
	}
}

func TestParseClaudeStreamErrorEvent(t *testing.T) {
	input := `{"type":"stream_event","event":{"type":"error","error":{"type":"overloaded_error","message":"rate limited"}}}`
	events := collectEvents(t, input)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	errEvt, ok := events[0].(ErrorEvent)
	if !ok {
		t.Fatalf("expected ErrorEvent, got %T", events[0])
	}
	if errEvt.ErrorType != "overloaded_error" || errEvt.Message != "rate limited" {
		t.Errorf("unexpected error event: %+v", errEvt)
	}
}

func TestParseClaudeStreamAssistantFallback(t *testing.T) {
	// When no stream_events have been seen, assistant messages should emit ToolUseEvent
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	var tools []ToolUseEvent
	for _, e := range events {
		if te, ok := e.(ToolUseEvent); ok {
			tools = append(tools, te)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool event from assistant fallback, got %d", len(tools))
	}
	if tools[0].Name != "Read" || tools[0].Summary != "/src/main.go" {
		t.Errorf("unexpected tool event: %+v", tools[0])
	}
}

func TestParseClaudeStreamAssistantSuppressedWhenStreaming(t *testing.T) {
	// When stream_events have been seen, assistant messages should NOT emit ToolUseEvent
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	for _, e := range events {
		if _, ok := e.(ToolUseEvent); ok {
			t.Error("assistant message should not emit ToolUseEvent when stream_events are active")
		}
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -run 'TestParseClaudeStream' -v`
Expected: FAIL with "undefined: parseClaudeStream"

- [ ] **Step 3: Implement parseClaudeStream**

Replace the contents of `internal/runtime/claude_progress.go` with:

```go
package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	maxPatternDisplay = 50
	maxPathDisplay    = 200
	tokenThreshold    = 5000
)

// streamEvent represents a single NDJSON event from Claude Code's stream-json output.
type streamEvent struct {
	Type string `json:"type"`
}

// systemEvent is Claude Code's initial "system"/"init" event, which carries the
// resolved model name. The result event does not include the model.
type systemEvent struct {
	Type               string `json:"type"`
	Subtype            string `json:"subtype"`
	Model              string `json:"model"`
	ClaudeCodeVersion  string `json:"claude_code_version"`
	Attempt            int    `json:"attempt"`
	MaxRetries         int    `json:"max_retries"`
	RetryDelayMs       int    `json:"retry_delay_ms"`
	Error              string `json:"error"`
}

// assistantMessage contains tool_use blocks from complete assistant messages.
// Claude Code's stream-json nests the content array (and model) under "message";
// older/flat shapes put content at the top level. We accept both.
type assistantMessage struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Message struct {
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
	} `json:"message"`
}

type contentItem struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// allowedTools is the set of tool names we display in progress output.
// Unknown tools emit no context to prevent information disclosure from
// untrusted sandbox output.
var allowedTools = map[string]bool{
	"Bash":  true,
	"Read":  true,
	"Write": true,
	"Edit":  true,
	"Grep":  true,
	"Glob":  true,
	"Agent": true,
}

// resultEvent represents the final NDJSON event from Claude Code's stream-json
// output, containing execution metrics.
type resultEvent struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// streamEventWrapper wraps the nested event structure from stream-json.
type streamEventWrapper struct {
	Type  string          `json:"type"`
	Event json.RawMessage `json:"event"`
}

type innerEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Message      json.RawMessage `json:"message"`
	Usage        json.RawMessage `json:"usage"`
	Error        json.RawMessage `json:"error"`
}

type contentBlock struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
}

type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type messageUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// parseClaudeStream reads NDJSON from Claude Code's stream-json output and
// emits normalized AgentEvent values via the onEvent callback.
func parseClaudeStream(r io.Reader, onEvent func(AgentEvent)) error {
	br := bufio.NewReaderSize(r, 1024*1024)

	var (
		seenStreamEvent bool
		// tool block accumulation state
		currentToolName string
		toolInputJSON   strings.Builder
		// token tracking for throttled TokensEvent
		totalInput      int
		totalOutput     int
		totalCacheRead  int
		totalCacheWrite int
		lastEmittedTotal int
	)

	emit := func(evt AgentEvent) {
		if onEvent != nil {
			onEvent(evt)
		}
	}

	for {
		line, isPrefix, err := br.ReadLine()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if isPrefix {
			for isPrefix && err == nil {
				_, isPrefix, err = br.ReadLine()
			}
			continue
		}
		if len(line) == 0 {
			continue
		}

		var evt streamEvent
		if jsonErr := json.Unmarshal(line, &evt); jsonErr != nil {
			continue
		}

		switch evt.Type {
		case "system":
			var se systemEvent
			if err := json.Unmarshal(line, &se); err != nil {
				continue
			}
			switch se.Subtype {
			case "init":
				emit(InitEvent{Model: se.Model, Version: se.ClaudeCodeVersion})
			case "api_retry":
				emit(RetryEvent{
					Attempt:    se.Attempt,
					MaxRetries: se.MaxRetries,
					DelayMs:    se.RetryDelayMs,
					Error:      se.Error,
				})
			}

		case "stream_event":
			seenStreamEvent = true
			var wrapper streamEventWrapper
			if err := json.Unmarshal(line, &wrapper); err != nil {
				continue
			}
			var inner innerEvent
			if err := json.Unmarshal(wrapper.Event, &inner); err != nil {
				continue
			}

			switch inner.Type {
			case "content_block_start":
				var cb contentBlock
				if err := json.Unmarshal(inner.ContentBlock, &cb); err != nil {
					continue
				}
				switch cb.Type {
				case "tool_use", "server_tool_use":
					currentToolName = cb.Name
					toolInputJSON.Reset()
				}

			case "content_block_delta":
				var d delta
				if err := json.Unmarshal(inner.Delta, &d); err != nil {
					continue
				}
				switch d.Type {
				case "text_delta":
					emit(TextEvent{Text: d.Text})
				case "thinking_delta":
					emit(ThinkingEvent{Text: d.Thinking})
				case "input_json_delta":
					toolInputJSON.WriteString(d.PartialJSON)
				}

			case "content_block_stop":
				if currentToolName != "" {
					name := currentToolName
					var summary string
					if !allowedTools[name] {
						name = "tool"
					} else {
						summary = extractSafeContext(currentToolName, json.RawMessage(toolInputJSON.String()))
					}
					emit(ToolUseEvent{Name: name, Summary: summary})
					currentToolName = ""
					toolInputJSON.Reset()
				}

			case "message_start":
				var msg struct {
					Message struct {
						Usage messageUsage `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal(wrapper.Event, &msg); err == nil {
					totalInput = msg.Message.Usage.InputTokens
					totalCacheRead = msg.Message.Usage.CacheReadInputTokens
					totalCacheWrite = msg.Message.Usage.CacheCreationInputTokens
				}

			case "message_delta":
				var md struct {
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(wrapper.Event, &md); err == nil && md.Usage.OutputTokens > 0 {
					totalOutput = md.Usage.OutputTokens
					total := totalInput + totalOutput + totalCacheRead + totalCacheWrite
					if total-lastEmittedTotal >= tokenThreshold {
						lastEmittedTotal = total
						emit(TokensEvent{
							InputTokens:  totalInput,
							OutputTokens: totalOutput,
							CacheRead:    totalCacheRead,
							CacheWrite:   totalCacheWrite,
						})
					}
				}

			case "error":
				var se streamError
				if err := json.Unmarshal(inner.Error, &se); err == nil {
					emit(ErrorEvent{ErrorType: se.Type, Message: se.Message})
				}
			}

		case "result":
			var re resultEvent
			if err := json.Unmarshal(line, &re); err == nil {
				emit(ResultEvent{
					NumTurns:                 re.NumTurns,
					TotalCostUSD:             re.TotalCostUSD,
					IsError:                  re.IsError,
					ErrorMessage:             re.Result,
					Subtype:                  re.Subtype,
					InputTokens:              re.Usage.InputTokens,
					OutputTokens:             re.Usage.OutputTokens,
					CacheCreationInputTokens: re.Usage.CacheCreationInputTokens,
					CacheReadInputTokens:     re.Usage.CacheReadInputTokens,
				})
			}

		case "assistant":
			if seenStreamEvent {
				// stream_event path is active; skip assistant fallback to
				// avoid double-counting tool calls.
				continue
			}
			var msg assistantMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				continue
			}
			// Capture model from assistant message as fallback when system
			// init event did not carry one.
			if msg.Message.Model != "" {
				emit(InitEvent{Model: msg.Message.Model})
			}
			content := msg.Message.Content
			if len(content) == 0 {
				content = msg.Content
			}
			var items []contentItem
			if err := json.Unmarshal(content, &items); err != nil {
				continue
			}
			for _, item := range items {
				if item.Type != "tool_use" {
					continue
				}
				name := item.Name
				var summary string
				if !allowedTools[name] {
					name = "tool"
				} else {
					summary = extractSafeContext(item.Name, item.Input)
				}
				emit(ToolUseEvent{Name: name, Summary: summary})
			}
		}
	}
}

// progressParser is the backward-compatible entry point. It creates an
// EventRenderer and feeds it through parseClaudeStream.
func progressParser(r io.Reader, printer *ui.Printer, start time.Time, metrics *RunMetrics) error {
	renderer := NewEventRenderer(printer, start, metrics)

	// Also capture the model from InitEvent for metrics, since the
	// renderer does not set metrics.Model.
	return parseClaudeStream(r, func(evt AgentEvent) {
		if init, ok := evt.(InitEvent); ok && metrics.Model == "" {
			metrics.Model = init.Model
		}
		// Fallback: capture model from assistant message if system init had none.
		// The old parser did this; replicate by checking assistant messages.
		renderer.Handle(evt)
	})
}

// extractSafeContext returns a safe, non-secret string for progress display.
func extractSafeContext(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}

	switch toolName {
	case "Bash":
		raw, ok := fields["command"]
		if !ok {
			return ""
		}
		var cmd string
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return ""
		}
		return extractBinaryName(cmd)

	case "Read", "Write", "Edit":
		raw, ok := fields["file_path"]
		if !ok {
			return ""
		}
		var path string
		if err := json.Unmarshal(raw, &path); err != nil {
			return ""
		}
		if utf8.RuneCountInString(path) > maxPathDisplay {
			runes := []rune(path)
			return string(runes[:maxPathDisplay]) + "\u2026"
		}
		return path

	case "Grep", "Glob":
		raw, ok := fields["pattern"]
		if !ok {
			return ""
		}
		var pattern string
		if err := json.Unmarshal(raw, &pattern); err != nil {
			return ""
		}
		if utf8.RuneCountInString(pattern) > maxPatternDisplay {
			runes := []rune(pattern)
			return string(runes[:maxPatternDisplay]) + "\u2026"
		}
		return pattern
	}

	return ""
}

// extractBinaryName returns only the binary name from a shell command,
// skipping leading KEY=VALUE environment variable assignments.
func extractBinaryName(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	// Skip leading KEY=VALUE env var assignments.
	for _, f := range fields {
		if !strings.Contains(f, "=") {
			// Strip path prefix (e.g. /usr/bin/make -> make).
			if idx := strings.LastIndex(f, "/"); idx >= 0 {
				f = f[idx+1:]
			}
			return f
		}
	}
	return ""
}

func emitToolProgress(printer *ui.Printer, toolName, context string, start time.Time, toolCount int32, isCI bool) {
	elapsed := time.Since(start).Truncate(time.Second)

	var msg string
	if context != "" {
		msg = fmt.Sprintf("%s: %s (%s, %d tools)", toolName, context, elapsed, toolCount)
	} else {
		msg = fmt.Sprintf("%s (%s, %d tools)", toolName, elapsed, toolCount)
	}

	msg = sanitizeOutput(msg)
	if isCI {
		fmt.Fprintf(os.Stderr, "::notice::%s\n", msg)
	}
	printer.Heartbeat(msg)
}
```

Note: `emitToolProgress` and `parseAssistantToolUse` are no longer called by the main path but `emitToolProgress` is kept for now since removing it is a separate cleanup. Actually, let me check if anything else uses it.

Actually, on reflection, `emitToolProgress` is only called by `parseAssistantToolUse` which is only called by the old `progressParser`. Since `progressParser` now delegates to `parseClaudeStream` + `EventRenderer`, both `emitToolProgress` and `parseAssistantToolUse` are dead code. Remove them in this step -- the `EventRenderer` handles the same rendering via `Handle(ToolUseEvent{...})`.

- [ ] **Step 4: Run the new parseClaudeStream tests to verify they pass**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -run 'TestParseClaudeStream' -v`
Expected: all PASS

- [ ] **Step 5: Run ALL existing tests to verify backward compatibility**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -v`
Expected: all tests PASS -- particularly `TestProgressParser*` tests which exercise the `progressParser` wrapper and should produce equivalent behavior.

- [ ] **Step 6: Update existing tests for new behavior**

The existing `TestProgressParserIgnoresStreamEvents` test verified that stream_events were ignored. With the new parser, stream_events ARE processed. Rename and update:

```go
func TestProgressParserStreamEventsProcessed(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Edit"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"/src/main.go\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/src/main.go"}}]}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	// Tool counted once from stream_event, not again from assistant fallback
	if metrics.ToolCalls.Load() != 1 {
		t.Errorf("expected 1 tool call (from stream_event, assistant suppressed), got %d", metrics.ToolCalls.Load())
	}
}
```

The `TestProgressParserCapturesModelFromAssistantWhenSystemLacksIt` test should still pass unchanged: `parseClaudeStream` emits an `InitEvent` from the assistant message's `msg.Message.Model` field (see the assistant fallback path), and the `progressParser` wrapper captures it into `metrics.Model`.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/claude_progress.go internal/runtime/claude_progress_test.go
git commit -S -s -m "$(cat <<'EOF'
refactor(runtime): replace progressParser with parseClaudeStream

Refactor the Claude Code NDJSON parser to emit normalized AgentEvent
values via a callback instead of calling printer.Heartbeat directly.
The new parseClaudeStream function processes stream_event deltas
(thinking, text, tool input JSON), system events (init, api_retry),
result events, and errors. The old progressParser is kept as a thin
wrapper that creates an EventRenderer.

Supports assistant message fallback for older Claude Code versions that
don't emit stream_event deltas.

Part of #3170.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Wire OnEvent in ClaudeRuntime.Run

**Files:**
- Modify: `internal/runtime/claude.go`

- [ ] **Step 1: Update ClaudeRuntime.Run to use OnEvent**

In `internal/runtime/claude.go`, replace the `progressParser` call in `Run` with OnEvent-aware wiring:

```go
func (ClaudeRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	cmd := buildRunCommand(params)
	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(ctx, params.SandboxName, cmd, params.Timeout, os.Stderr)
	if err != nil {
		return -1, err
	}
	defer cancel()

	var r io.Reader = stdout
	if params.OutputPath != "" {
		f, ferr := os.Create(params.OutputPath)
		if ferr != nil {
			printer.StepWarn(fmt.Sprintf("Failed to create %s: ", params.OutputPath) + ferr.Error())
		} else {
			defer f.Close()
			r = io.TeeReader(stdout, f)
		}
	}

	handler := params.OnEvent
	if handler == nil {
		renderer := NewEventRenderer(printer, start, metrics)
		handler = func(evt AgentEvent) {
			if init, ok := evt.(InitEvent); ok && metrics.Model == "" {
				metrics.Model = init.Model
			}
			renderer.Handle(evt)
		}
	}

	if parseErr := parseClaudeStream(r, handler); parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		cancel()
		io.Copy(io.Discard, r)
	}

	waitErr := execCmd.Wait()
	exitCode := -1
	if execCmd.ProcessState != nil {
		exitCode = execCmd.ProcessState.ExitCode()
	}

	if waitErr != nil && execCmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}

	return exitCode, nil
}
```

- [ ] **Step 2: Remove the old progressParser wrapper from claude_progress.go**

Since `ClaudeRuntime.Run` now directly calls `parseClaudeStream` and handles the model extraction and renderer setup inline, the `progressParser` wrapper function can be removed. Also remove `emitToolProgress` and `parseAssistantToolUse` if they are now dead code.

Check if anything else in the codebase calls `progressParser`:

Run: `cd /home/rbean/code/fullsend-2 && grep -rn 'progressParser\|emitToolProgress\|parseAssistantToolUse' --include='*.go' | grep -v _test.go`

If only `claude.go` and `claude_progress.go` reference these functions, and `claude.go` no longer calls `progressParser`, then remove `progressParser` and `emitToolProgress` from `claude_progress.go`.

However, the existing tests call `progressParser` directly. Rather than rewriting all the existing tests, keep `progressParser` as an internal test helper or update the tests to call `parseClaudeStream` directly. The cleanest approach: keep `progressParser` in `claude_progress.go` as an unexported wrapper used by tests, since the tests verify backward-compatible behavior.

- [ ] **Step 3: Run all tests**

Run: `cd /home/rbean/code/fullsend-2 && go test ./internal/runtime/ -v`
Expected: all tests PASS

- [ ] **Step 4: Run go vet**

Run: `cd /home/rbean/code/fullsend-2 && go vet ./internal/runtime/`
Expected: no issues

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/claude.go internal/runtime/claude_progress.go
git commit -S -s -m "$(cat <<'EOF'
feat(runtime): render agent event stream live during execution

Wire the OnEvent callback in ClaudeRuntime.Run. When OnEvent is nil
(the default), creates an EventRenderer that renders thinking, text,
tool summaries, token usage, errors, and result summaries to the
terminal in real time. When OnEvent is set, the caller receives
normalized AgentEvent values for custom handling.

Closes #3170.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Lint and final verification

**Files:** None (verification only)

- [ ] **Step 1: Stage all changes and run lint**

```bash
cd /home/rbean/code/fullsend-2 && git add -A && make lint
```

Expected: no lint failures

- [ ] **Step 2: Run full test suite**

```bash
cd /home/rbean/code/fullsend-2 && make go-test
```

Expected: all tests PASS

- [ ] **Step 3: Run go vet**

```bash
cd /home/rbean/code/fullsend-2 && make go-vet
```

Expected: no issues

- [ ] **Step 4: Fix any issues found by lint/vet and amend or create a new commit**

If lint or vet finds issues, fix them and commit:

```bash
git add -A
git commit -S -s -m "$(cat <<'EOF'
chore(runtime): fix lint/vet issues from event stream rendering

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```
