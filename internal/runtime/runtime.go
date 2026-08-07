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
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	Model                    string  `json:"model"`
}

// RunParams configures a single agent invocation inside the sandbox.
type RunParams struct {
	SandboxName   string
	AgentBaseName string
	Model         string
	RepoDir       string
	FullsendDir   string
	PluginDirs    []string
	Debug         string
	Timeout       time.Duration
	OutputPath    string           // if set, tee stream-json stdout to this file
	OnEvent       func(AgentEvent) // if non-nil, called with normalized events during Run
}

// TranscriptError holds extracted error information from a runtime transcript.
type TranscriptError struct {
	Source       string
	IsError      bool
	ErrorMessage string
	Subtype      string
}

// DisplayMessage returns the sanitized message for a transcript error:
// ErrorMessage with ANSI escapes, control characters, and GHA workflow
// command markers stripped, or the subtype fallback when it sanitizes to
// empty (both fields are omitempty in the transcript). Every sink that
// renders a transcript error — GHA annotations, the CLI console, span
// status and events — goes through this one method so the treatments
// agree. Callers with a size-bounded sink truncate the result themselves;
// ErrorMessage is parser-truncated but Subtype is not.
func (te TranscriptError) DisplayMessage() string {
	msg := sanitizeOutput(te.ErrorMessage)
	if msg == "" {
		msg = fmt.Sprintf("agent terminated with error (subtype: %s)", sanitizeOutput(te.Subtype))
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
