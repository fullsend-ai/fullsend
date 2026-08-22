package runtime

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// RunMetrics collects execution statistics from stream parsing.
type RunMetrics struct {
	ToolCalls                atomic.Int32
	NumTurns                 int     `json:"num_turns"`
	TotalCostUSD             float64 `json:"total_cost_usd"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	ReasoningTokens          int     `json:"reasoning_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	Model                    string  `json:"model"`
}

// RunParams configures a single agent invocation inside the sandbox.
type RunParams struct {
	SandboxName   string
	AgentBaseName string
	Model         string
	Effort        string
	RepoDir       string
	FullsendDir   string
	PluginDirs    []string
	Debug         string
	// HooksSettingsPath, if set, is passed as --settings so Claude Code
	// loads the runner's hook wiring regardless of its working directory.
	HooksSettingsPath string
	Timeout           time.Duration
	OutputPath        string           // if set, tee stream-json stdout to this file
	OnEvent           func(AgentEvent) // if non-nil, called with normalized events during Run
}

// TranscriptError holds extracted error information from a runtime transcript.
type TranscriptError struct {
	Source       string
	IsError      bool
	ErrorMessage string
	Subtype      string
}

// DisplayMessage returns the sanitized, bounded message for a transcript
// error: ErrorMessage with ANSI escapes, control characters, and GHA
// workflow command markers stripped, or the subtype fallback when it
// sanitizes to empty (both fields are omitempty in the transcript).
// ErrorMessage is truncated at parse time; Subtype is not, so the
// fallback applies the same truncateError bound before sanitizing. Every
// sink that renders a transcript error — GHA annotations, the CLI
// console, span status and events — goes through this one method so the
// treatments agree.
func (te TranscriptError) DisplayMessage() string {
	msg := sanitizeOutput(te.ErrorMessage)
	if msg == "" {
		msg = fmt.Sprintf("agent terminated with error (subtype: %s)", sanitizeOutput(truncateError(te.Subtype)))
	}
	return msg
}

// Runtime is an agent execution backend (LLM tool-use loop) inside the sandbox.
type Runtime interface {
	Name() string
	// System returns the OTEL GenAI `gen_ai.system` value (the model vendor) for
	// this runtime, e.g. "anthropic". Kept on the runtime so telemetry stays
	// runtime-agnostic rather than hardcoding a vendor in the CLI (ADR 0050).
	System() string
	ConfigDir() string
	WorkspaceDir() string
	EnvExports() []string
	Bootstrap(input BootstrapInput) error
	Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (exitCode int, err error)
	ClearIterationArtifacts(sandboxName string) error
}

// Backend pairs the active runtime with its transcript/debug artifact handler.
type Backend struct {
	Runtime
	Transcripts TranscriptHandler
}

// Default returns the Claude Code backend. Prefer ResolveFromConfig for org-aware selection.
func Default() Backend {
	r := ClaudeRuntime{}
	return Backend{Runtime: r, Transcripts: r}
}

// DebugLogNamer is an optional extension a runtime or TranscriptHandler
// implements to name the local debug-log artifact the runner writes per
// iteration (e.g. "claude-debug.log"). Runtimes without it get
// DefaultDebugLogName.
type DebugLogNamer interface {
	DebugLogName() string
}

// DefaultDebugLogName is the local debug-log filename for runtimes that do
// not implement DebugLogNamer.
const DefaultDebugLogName = "agent-debug.log"

// DebugLogNameFor returns the debug-log filename from the first candidate
// that implements DebugLogNamer with a non-empty name (callers pass the
// Backend's Runtime and TranscriptHandler), falling back to
// DefaultDebugLogName.
func DebugLogNameFor(candidates ...any) string {
	for _, v := range candidates {
		if n, ok := v.(DebugLogNamer); ok {
			if name := n.DebugLogName(); name != "" {
				return name
			}
		}
	}
	return DefaultDebugLogName
}

// ContextBridger is an optional Runtime extension for runtimes that only
// auto-load CLAUDE.md (not AGENTS.md) into their system context. When it
// reports true and the target repo has AGENTS.md but no CLAUDE.md, the runner
// injects a minimal CLAUDE.md pointer so the agent is not context-blind.
// Runtimes that read AGENTS.md natively should not implement it (or return
// false).
type ContextBridger interface {
	NeedsClaudeMDBridge() bool
}

// WantsClaudeMDBridge reports whether rt wants the CLAUDE.md→AGENTS.md
// bridge file; false for runtimes that do not implement ContextBridger.
func WantsClaudeMDBridge(rt Runtime) bool {
	if b, ok := rt.(ContextBridger); ok {
		return b.NeedsClaudeMDBridge()
	}
	return false
}
