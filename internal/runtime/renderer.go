package runtime

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// EventRenderer renders normalized AgentEvent values to a Printer.
// It tracks block state (text/thinking) so transitions between event
// types produce clean output boundaries.
type EventRenderer struct {
	// RedactProgress removes caller-owned credential literals from progress
	// metadata before and after normalization, before truncation. The callback
	// must match original and NormalizeProgressText-normalized literals and
	// use display-safe replacement markers. Pattern and registered
	// runtime-secret redaction apply even when this callback is nil.
	RedactProgress func(string) string

	// Verbosity limits what the renderer prints (ADR 0163): one of
	// config.ProgressVerbosityFull, Tools or Quiet. Empty means full, so
	// a renderer nobody configured prints what it always has. Any other
	// value prints at quiet. It changes printed output only; callers that
	// feed telemetry or content capture see every event regardless.
	Verbosity string

	printer    *ui.Printer
	isCI       bool
	inText     bool
	inThinking bool
	seenInit   bool
	progress   progressReconciler
}

// NewEventRenderer creates a renderer that writes to the given printer.
func NewEventRenderer(printer *ui.Printer) *EventRenderer {
	return &EventRenderer{
		printer: printer,
		isCI:    os.Getenv("GITHUB_ACTIONS") == "true",
	}
}

// newEventRendererAt creates a renderer that prints at the given
// verbosity. Runtimes use it for their default renderer, so a caller that
// passes RunParams.ProgressVerbosity without its own OnEvent gets the same
// level.
func newEventRendererAt(printer *ui.Printer, verbosity string) *EventRenderer {
	r := NewEventRenderer(printer)
	r.Verbosity = verbosity
	return r
}

// hidesText reports whether reasoning and reply text are left out (tools
// and quiet).
func (r *EventRenderer) hidesText() bool {
	return config.ProgressHidesText(r.Verbosity)
}

// quiet reports whether only fullsend-owned fields are printed. An
// unknown level counts as quiet, so a bad value never prints more.
func (r *EventRenderer) quiet() bool {
	return config.ProgressIsQuiet(r.Verbosity)
}

var (
	thinkingStyle = lipgloss.NewStyle().Italic(true).Foreground(ui.ColorMuted)
)

// Handle dispatches a single AgentEvent to the appropriate rendering method.
func (r *EventRenderer) Handle(evt AgentEvent) {
	switch e := evt.(type) {
	case StatusProgressEvent:
		if msg, show := r.progress.status(e, r.RedactProgress); show {
			if r.quiet() {
				r.printQuietProgress(quietStatusLine(e))
				return
			}
			r.printProgress(msg)
		}
	case TaskProgressEvent:
		if msg, show := r.progress.task(e, r.RedactProgress); show {
			if r.quiet() {
				r.printQuietProgress(quietTaskLine(e))
				return
			}
			r.printProgress(msg)
		}
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
		if r.hidesText() {
			return
		}
		if !r.inThinking {
			r.endBlock()
			r.printer.Raw(thinkingStyle.Render("  \U0001f9e0 "))
			r.inThinking = true
		}
		r.printer.Raw(thinkingStyle.Render(sanitizeStreamText(e.Text)))
	case TextEvent:
		if r.hidesText() {
			return
		}
		if !r.inText {
			r.endBlock()
			r.printer.Raw("  \U0001f4ac ")
			r.inText = true
		}
		r.printer.Raw(sanitizeStreamText(e.Text))
	case ToolUseEvent:
		if r.quiet() {
			return
		}
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
		total := e.InputTokens + e.OutputTokens + e.ReasoningTokens + e.CacheRead + e.CacheWrite
		if e.ReasoningTokens > 0 {
			r.printer.StepInfo(fmt.Sprintf(
				"TOKENS in=%d out=%d reasoning=%d cache_r=%d cache_w=%d total=%d",
				e.InputTokens, e.OutputTokens, e.ReasoningTokens, e.CacheRead, e.CacheWrite, total,
			))
		} else {
			r.printer.StepInfo(fmt.Sprintf(
				"TOKENS in=%d out=%d cache_r=%d cache_w=%d total=%d",
				e.InputTokens, e.OutputTokens, e.CacheRead, e.CacheWrite, total,
			))
		}
	case ResultEvent:
		r.endBlock()
		subtype := sanitizeOutput(e.Subtype)
		if r.quiet() {
			subtype = quietErrorType(e.Subtype, "")
		}
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
		if e.ReasoningTokens > 0 {
			r.printer.KeyValue("Tokens", fmt.Sprintf(
				"in=%d out=%d reasoning=%d cache_create=%d cache_read=%d",
				e.InputTokens, e.OutputTokens, e.ReasoningTokens,
				e.CacheCreationInputTokens, e.CacheReadInputTokens,
			))
		} else {
			r.printer.KeyValue("Tokens", fmt.Sprintf(
				"in=%d out=%d cache_create=%d cache_read=%d",
				e.InputTokens, e.OutputTokens,
				e.CacheCreationInputTokens, e.CacheReadInputTokens,
			))
		}
		if e.IsError && e.ErrorMessage != "" && !r.quiet() {
			r.printer.StepFail(sanitizeOutput(e.ErrorMessage))
		}
	case ErrorEvent:
		r.endBlock()
		if r.quiet() {
			r.printer.StepFail(quietErrorType(e.ErrorType, "error"))
			return
		}
		r.printer.StepFail(fmt.Sprintf("%s: %s",
			sanitizeOutput(e.ErrorType), sanitizeOutput(e.Message)))
	case RetryEvent:
		r.endBlock()
		if r.quiet() {
			r.printer.StepWarn(fmt.Sprintf("Retry %d/%d (delay %dms)",
				e.Attempt, e.MaxRetries, e.DelayMs))
			return
		}
		r.printer.StepWarn(fmt.Sprintf("Retry %d/%d: %s (delay %dms)",
			e.Attempt, e.MaxRetries, sanitizeOutput(e.Error), e.DelayMs))
	case ModelFallbackEvent:
		r.endBlock()
		r.printer.StepWarn(modelFallbackWarning(e.From, e.To, claudeCodeFallbackReason(e.Trigger)))
	case PluginErrorEvent:
		r.endBlock()
		if r.quiet() {
			msg := "Plugin failed to load"
			if e.Type != "" {
				msg += " (" + quietErrorType(e.Type, "") + ")"
			}
			r.printer.StepWarn(msg)
			return
		}
		r.printer.StepWarn(pluginErrorMessage(e))
	}
}

// quietErrorTypes are the result subtypes, error types, stop reasons and
// plugin error types quiet prints as they are. The runtimes copy these
// from the stream or from a transcript the agent can write, so quiet
// prints only a value listed here, with its source:
var quietErrorTypes = map[string]bool{
	// Claude Code result subtypes (Agent SDK SDKResultMessage; success,
	// error_max_turns, error_during_execution and error_max_budget_usd
	// also appear in the tests). error_unknown is in the transcript tests.
	"success": true, "error_max_turns": true, "error_during_execution": true,
	"error_max_budget_usd": true, "error_max_structured_output_retries": true,
	"error_unknown": true,
	// Claude Code stream error types: the Anthropic API error types
	// (API docs "Errors"); api_error and overloaded_error are in the tests.
	"invalid_request_error": true, "authentication_error": true, "billing_error": true,
	"permission_error": true, "not_found_error": true, "request_too_large": true,
	"rate_limit_error": true, "api_error": true, "timeout_error": true,
	"overloaded_error": true,
	// Claude Code plugin_errors types seen in claude_progress_test.go.
	"path-not-found": true, "manifest-validation-error": true, "generic-error": true,
	// Codex: codexSubtypeFailed and codexSubtypeIncomplete.
	codexSubtypeFailed: true, codexSubtypeIncomplete: true,
	// OpenCode step_finish reasons (AI SDK FinishReason; stop and
	// tool-calls are in the tests) and the transcript fallback "error".
	"stop": true, "length": true, "content-filter": true, "tool-calls": true,
	"error": true, "other": true, "unknown": true,
	// OpenCode error names (session error union; APIError and
	// UnknownError are in the tests).
	"APIError": true, "UnknownError": true, "ProviderAuthError": true,
	"MessageOutputLengthError": true, "MessageAbortedError": true,
	// pi stopReasons (packages/ai/src/types.ts, see piIsErrorStop).
	"pending": true, "toolUse": true, "aborted": true, "deferred": true,
}

// quietErrorType is a result subtype, error type, stop reason or plugin
// error type at quiet: the value if it is on quietErrorTypes, "other" if
// it is not, and fallback when it is empty.
func quietErrorType(s, fallback string) string {
	if s == "" {
		return fallback
	}
	if quietErrorTypes[s] {
		return s
	}
	return "other"
}

// quietStatusKinds and quietStatusStates are the status kinds and states
// the Claude decoders set themselves (decodeClaudeStatusProgress,
// decodeClaudeRateLimit). A notification's priority is copied from the
// stream, so it is not on the list.
var (
	quietStatusKinds = map[string]bool{
		"hook": true, "task_summary": true, "informational": true,
		"notification": true, "status": true, "session_state_changed": true,
		"permission_denied": true, "background_tasks_changed": true, "rate_limit": true,
	}
	quietStatusStates = map[string]bool{
		"running": true, "completed": true, "failed": true, "stopped": true,
		"continuation stopped": true, "compaction failed": true, "ready": true,
		"compacting": true, "idle": true, "requires action": true,
		"allowed": true, "approaching limit": true, "rejected": true,
	}
)

// quietStatusLine is a status row at quiet: the event kind and state. The
// label, subject, context and detail are left out, since they can carry
// text the agent wrote. A kind or state the decoders do not set themselves
// prints as "other", since it came from the stream.
func quietStatusLine(e StatusProgressEvent) string {
	kind := "status"
	if e.Kind != "" {
		kind = "other"
		if quietStatusKinds[e.Kind] {
			kind = e.Kind
		}
	}
	msg := "Status: " + kind
	if e.State != "" {
		state := "other"
		if quietStatusStates[e.State] {
			state = e.State
		}
		msg += " — " + state
	}
	return msg
}

// quietTaskLine is a task row at quiet: whether it is a task or one of its
// agents, and a known task state. The label, phase, tool, summary and
// detail are left out, since they can carry text the agent wrote.
func quietTaskLine(e TaskProgressEvent) string {
	kind := "Task"
	if e.AgentID != "" || e.AgentIndex > 0 {
		kind = "Task agent"
	}
	switch e.State {
	case TaskPending, TaskPaused, TaskRunning, TaskCompleted, TaskFailed,
		TaskStopped, TaskQueued, TaskRetrying, TaskSkipped:
		return kind + " — " + string(e.State)
	}
	return kind + " update"
}

// pluginErrorMessage formats a plugin load failure as one warning line. The
// whole line is sanitized once, since plugin names, paths and messages come
// from the sandbox.
func pluginErrorMessage(e PluginErrorEvent) string {
	name := e.Plugin
	if name == "" {
		name = "(unnamed)"
	}
	msg := "Plugin " + name + " failed to load"
	if e.Path != "" {
		msg += " from " + e.Path
	}
	if e.Type != "" {
		msg += " (" + e.Type + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return sanitizeOutput(msg)
}

// endBlock closes any open text or thinking block.
func (r *EventRenderer) endBlock() {
	if r.inText || r.inThinking {
		r.printer.Raw("\n")
		r.inText = false
		r.inThinking = false
	}
}

// printQuietProgress prints a quiet progress row. It writes no CI
// annotation: at quiet, nothing reaches the run summary page.
func (r *EventRenderer) printQuietProgress(msg string) {
	r.endBlock()
	r.printer.ToolProgress(msg)
}

func (r *EventRenderer) printProgress(msg string) {
	r.endBlock()
	if r.isCI {
		fmt.Fprintf(os.Stderr, "::notice::%s\n", msg)
	}
	r.printer.ToolProgress(msg)
}
