package runtime

import (
	"sync/atomic"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// RunMetrics collects execution statistics from stream parsing.
type RunMetrics struct {
	ToolCalls atomic.Int32
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
}

// TranscriptError holds extracted error information from a runtime transcript.
type TranscriptError struct {
	Source       string
	IsError      bool
	ErrorMessage string
	Subtype      string
}

// Runtime is an agent execution backend (LLM tool-use loop) inside the sandbox.
type Runtime interface {
	Name() string
	ConfigDir() string
	WorkspaceDir() string
	EnvExports() []string
	Bootstrap(input BootstrapInput) error
	Run(params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (exitCode int, err error)
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
