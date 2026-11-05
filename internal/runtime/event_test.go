package runtime

import "testing"

func TestAgentEventInterfaceSatisfied(t *testing.T) {
	// Compile-time verification that all concrete types satisfy AgentEvent.
	var events []AgentEvent
	events = append(events,
		TaskProgressEvent{TaskID: "task", State: TaskRunning},
		StatusProgressEvent{Kind: "hook", Label: "Hook"},
		ModelFallbackEvent{From: "old", To: "new"},
		InitEvent{Model: "claude-opus-4-6", Version: "1.0.0"},
		ThinkingEvent{Text: "thinking..."},
		TextEvent{Text: "hello"},
		ToolUseEvent{Name: "Read", Summary: "/src/main.go"},
		TokensEvent{InputTokens: 100, OutputTokens: 50, CacheRead: 30, CacheWrite: 20},
		ResultEvent{NumTurns: 5, TotalCostUSD: 0.42},
		ErrorEvent{ErrorType: "overloaded", Message: "rate limited"},
		RetryEvent{Attempt: 1, MaxRetries: 3, DelayMs: 1000, Error: "timeout"},
		ToolResultEvent{ID: "toolu_01abc", Result: "file contents"},
		PluginErrorEvent{Plugin: "demo@inline", Type: "path-not-found", Path: "/plugins/demo", Message: "not found"},
	)
	if len(events) != 13 {
		t.Errorf("expected 13 event types, got %d", len(events))
	}
}
