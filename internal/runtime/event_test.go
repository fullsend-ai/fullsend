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
