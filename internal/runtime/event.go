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
// Name is the raw tool name from the runtime stream.
// Summary is a one-line context string from extractSafeContext; it is
// empty for tools not recognized by that function.
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
