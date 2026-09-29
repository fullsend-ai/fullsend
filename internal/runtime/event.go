package runtime

// streamBufSize is the bufio.Reader buffer size used by every NDJSON stream
// parser (Claude, pi, codex, OpenCode). Lines exceeding this size are
// skipped; the Claude parser still reports an oversized tool_result line's
// call id (ToolResultEvent.Oversized).
const streamBufSize = 1024 * 1024 // 1 MiB

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

// ToolUseEvent reports a tool call. The Claude Code parser emits it when
// it reads the line carrying the call's complete arguments (written ahead
// of the call's result; the tool may already be running by then); the pi,
// codex and OpenCode parsers emit it when the call has finished — codex
// once per changed path of a file_change item, so one apply_patch can
// yield several. Not every call yields one: a Claude Code server_tool_use
// block on an assistant line (the path production takes) and a tool_use
// line beyond streamBufSize emit nothing.
// ID is the tool call identifier from the runtime stream; it is empty
// when the parser passes none through — pi and codex (#7414), OpenCode,
// and a Claude Code server-side tool reported from stream_event blocks,
// whose result never arrives as a tool_result.
// Name is the raw tool name from the runtime stream.
// Summary is a one-line context string from extractSafeContext; it is
// empty for tools not recognized by that function.
type ToolUseEvent struct {
	ID      string
	Name    string
	Summary string
}

func (ToolUseEvent) agentEvent() {}

// ToolResultEvent carries the result of a completed tool invocation.
// ID is the tool call identifier from the runtime stream (matches
// ToolUseEvent.ID); Result is the raw result text; IsError reports a
// failed call, as set on the wire; Partial reports that non-text
// content (for example image blocks) was skipped while flattening, so
// Result is a fragment of what the wire carried; Oversized reports that
// the result's stream line exceeded streamBufSize and was skipped, so
// only ID is known — Result is empty and IsError is unknown, not false.
// Only the Claude runtime emits it today — see the runtime support
// matrix in docs/runtimes.md.
type ToolResultEvent struct {
	ID        string
	Result    string
	IsError   bool
	Partial   bool
	Oversized bool
}

func (ToolResultEvent) agentEvent() {}

// TokensEvent carries incremental token usage counters.
type TokensEvent struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	CacheRead       int
	CacheWrite      int
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
	ReasoningTokens          int
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
