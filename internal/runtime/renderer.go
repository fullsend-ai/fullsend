package runtime

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// EventRenderer renders normalized AgentEvent values to a Printer.
// It tracks block state (text/thinking) so transitions between event
// types produce clean output boundaries.
type EventRenderer struct {
	printer    *ui.Printer
	isCI       bool
	inText     bool
	inThinking bool
	seenInit   bool
}

// NewEventRenderer creates a renderer that writes to the given printer.
func NewEventRenderer(printer *ui.Printer) *EventRenderer {
	return &EventRenderer{
		printer: printer,
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
		if r.seenInit {
			return
		}
		r.seenInit = true
		r.endBlock()
		model := sanitizeOutput(e.Model)
		label := model
		if e.Version != "" {
			label = fmt.Sprintf("%s (v%s)", model, sanitizeOutput(e.Version))
		}
		r.printer.Header("Agent: " + label)
	case ThinkingEvent:
		if !r.inThinking {
			r.endBlock()
			r.printer.Raw(thinkingStyle.Render("  \U0001f9e0 "))
			r.inThinking = true
		}
		r.printer.Raw(thinkingStyle.Render(sanitizeStreamText(e.Text)))
	case TextEvent:
		if !r.inText {
			r.endBlock()
			r.printer.Raw("  \U0001f4ac ")
			r.inText = true
		}
		r.printer.Raw(sanitizeStreamText(e.Text))
	case ToolUseEvent:
		r.endBlock()
		var msg string
		if e.Summary != "" {
			msg = fmt.Sprintf("%s: %s", e.Name, e.Summary)
		} else {
			msg = e.Name
		}
		msg = sanitizeOutput(msg)
		if r.isCI {
			fmt.Fprintf(os.Stderr, "::notice::%s\n", msg)
		}
		r.printer.ToolProgress(msg)
	case TokensEvent:
		r.endBlock()
		total := e.InputTokens + e.OutputTokens + e.CacheRead + e.CacheWrite
		r.printer.StepInfo(fmt.Sprintf(
			"TOKENS in=%d out=%d cache_r=%d cache_w=%d total=%d",
			e.InputTokens, e.OutputTokens, e.CacheRead, e.CacheWrite, total,
		))
	case ResultEvent:
		r.endBlock()
		subtype := sanitizeOutput(e.Subtype)
		label := "Result"
		if subtype != "" {
			label = fmt.Sprintf("Result: %s", subtype)
		}
		if e.IsError {
			label = "Result: ERROR"
			if subtype != "" {
				label = fmt.Sprintf("Result: ERROR (%s)", subtype)
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
