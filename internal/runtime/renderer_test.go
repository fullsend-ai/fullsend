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

func TestRendererDuplicateInitEventSuppressed(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(InitEvent{Model: "claude-opus-4-6", Version: "1.2.3"})
	r.Handle(InitEvent{Model: "claude-opus-4-6"})
	r.Handle(InitEvent{Model: "claude-opus-4-6"})

	output := buf.String()
	if count := strings.Count(output, "claude-opus-4-6"); count != 1 {
		t.Errorf("expected init header exactly once, got %d times in: %s", count, output)
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

func TestRendererResultEventWithError(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(ResultEvent{
		NumTurns:     3,
		TotalCostUSD: 0.10,
		IsError:      true,
		ErrorMessage: "Max turns reached",
		Subtype:      "error_max_turns",
	})

	output := buf.String()
	if !strings.Contains(output, "ERROR") {
		t.Errorf("expected ERROR in result header, got: %s", output)
	}
	if !strings.Contains(output, "Max turns reached") {
		t.Errorf("expected error message in output, got: %s", output)
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

func TestRendererTokensEvent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestRenderer(&buf)

	r.Handle(TokensEvent{InputTokens: 4000, OutputTokens: 2000, CacheRead: 500, CacheWrite: 200})

	output := buf.String()
	if !strings.Contains(output, "TOKENS") {
		t.Errorf("expected TOKENS in output, got: %s", output)
	}
	if !strings.Contains(output, "in=4000") {
		t.Errorf("expected input token count, got: %s", output)
	}
}
