package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadOpenCodeFixture(t *testing.T, name string) *os.File {
	t.Helper()
	path := filepath.Join("testdata", "opencode", name)
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

func collectOpenCodeEvents(t *testing.T, name string) ([]AgentEvent, string) {
	t.Helper()
	f := loadOpenCodeFixture(t, name)
	var events []AgentEvent
	sessionID, _, err := parseOpenCodeStream(f, func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	return events, sessionID
}

func TestParseOpenCodeStream_BasicRun(t *testing.T) {
	t.Parallel()

	events, sessionID := collectOpenCodeEvents(t, "basic_run.ndjson")

	assert.Equal(t, "ses_test123", sessionID)

	// Expected: TextEvent, ToolUseEvent, TokensEvent, ResultEvent
	require.GreaterOrEqual(t, len(events), 4)

	// Find specific event types.
	var texts []TextEvent
	var tools []ToolUseEvent
	var tokens []TokensEvent
	var results []ResultEvent
	for _, evt := range events {
		switch e := evt.(type) {
		case TextEvent:
			texts = append(texts, e)
		case ToolUseEvent:
			tools = append(tools, e)
		case TokensEvent:
			tokens = append(tokens, e)
		case ResultEvent:
			results = append(results, e)
		}
	}

	require.Len(t, texts, 1)
	assert.Equal(t, "I'll list the files for you.", texts[0].Text)

	require.Len(t, tools, 1)
	assert.Equal(t, "bash", tools[0].Name)
	assert.Equal(t, "$ ls", tools[0].Summary)

	require.Len(t, tokens, 1)
	assert.Equal(t, 100, tokens[0].InputTokens)
	assert.Equal(t, 50, tokens[0].OutputTokens)
	assert.Equal(t, 80, tokens[0].CacheRead)
	assert.Equal(t, 20, tokens[0].CacheWrite)

	require.Len(t, results, 1)
	assert.Equal(t, 1, results[0].NumTurns)
	assert.InDelta(t, 0.015, results[0].TotalCostUSD, 0.001)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "tool-calls", results[0].Subtype)
	assert.Equal(t, 100, results[0].InputTokens)
	assert.Equal(t, 50, results[0].OutputTokens)

	// ResultEvent must be the last event emitted.
	_, isResult := events[len(events)-1].(ResultEvent)
	assert.True(t, isResult, "ResultEvent should be the last event")
}

func TestParseOpenCodeStream_ErrorRun(t *testing.T) {
	t.Parallel()

	events, sessionID := collectOpenCodeEvents(t, "error_run.ndjson")

	assert.Equal(t, "ses_err456", sessionID)

	var errEvents []ErrorEvent
	var tools []ToolUseEvent
	var results []ResultEvent
	for _, evt := range events {
		switch e := evt.(type) {
		case ErrorEvent:
			errEvents = append(errEvents, e)
		case ToolUseEvent:
			tools = append(tools, e)
		case ResultEvent:
			results = append(results, e)
		}
	}

	require.Len(t, tools, 1)
	assert.Equal(t, "edit", tools[0].Name)
	assert.Equal(t, "file not found", tools[0].Summary)

	require.Len(t, errEvents, 1)
	assert.Equal(t, "APIError", errEvents[0].ErrorType)
	assert.Equal(t, "quota exhausted", errEvents[0].Message)

	require.Len(t, results, 1)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "quota exhausted", results[0].ErrorMessage)
	assert.Equal(t, "error", results[0].Subtype)
	assert.Equal(t, 1, results[0].NumTurns)
	assert.Equal(t, 50, results[0].InputTokens)
	assert.Equal(t, 10, results[0].OutputTokens)
	assert.Equal(t, 40, results[0].CacheReadInputTokens)
	assert.Equal(t, 5, results[0].CacheCreationInputTokens)
}

func TestParseOpenCodeStream_Reasoning(t *testing.T) {
	t.Parallel()

	events, _ := collectOpenCodeEvents(t, "reasoning_run.ndjson")

	var thinking []ThinkingEvent
	var texts []TextEvent
	var tokens []TokensEvent
	var results []ResultEvent
	for _, evt := range events {
		switch e := evt.(type) {
		case ThinkingEvent:
			thinking = append(thinking, e)
		case TextEvent:
			texts = append(texts, e)
		case TokensEvent:
			tokens = append(tokens, e)
		case ResultEvent:
			results = append(results, e)
		}
	}

	require.Len(t, thinking, 1)
	assert.Equal(t, "Let me think about the best approach...", thinking[0].Text)

	require.Len(t, texts, 1)
	assert.Equal(t, "Here is my answer.", texts[0].Text)

	// Reasoning tokens must be captured in per-step TokensEvent.
	require.Len(t, tokens, 1)
	assert.Equal(t, 50, tokens[0].ReasoningTokens, "reasoning tokens should be captured per-step")
	assert.Equal(t, 200, tokens[0].InputTokens)
	assert.Equal(t, 100, tokens[0].OutputTokens)

	// Reasoning tokens must be accumulated in the synthesized ResultEvent.
	require.Len(t, results, 1)
	assert.Equal(t, 50, results[0].ReasoningTokens, "reasoning tokens should be accumulated in ResultEvent")
}

func TestParseOpenCodeStream_MultiStep(t *testing.T) {
	t.Parallel()

	events, _ := collectOpenCodeEvents(t, "multi_step.ndjson")

	var tokens []TokensEvent
	var results []ResultEvent
	for _, evt := range events {
		switch e := evt.(type) {
		case TokensEvent:
			tokens = append(tokens, e)
		case ResultEvent:
			results = append(results, e)
		}
	}

	require.Len(t, tokens, 2)
	// First step.
	assert.Equal(t, 100, tokens[0].InputTokens)
	assert.Equal(t, 30, tokens[0].OutputTokens)
	// Second step.
	assert.Equal(t, 200, tokens[1].InputTokens)
	assert.Equal(t, 70, tokens[1].OutputTokens)

	// ResultEvent should have accumulated totals.
	require.Len(t, results, 1)
	assert.Equal(t, 2, results[0].NumTurns)
	assert.InDelta(t, 0.03, results[0].TotalCostUSD, 0.001)
	assert.Equal(t, 300, results[0].InputTokens)             // 100 + 200
	assert.Equal(t, 100, results[0].OutputTokens)            // 30 + 70
	assert.Equal(t, 240, results[0].CacheReadInputTokens)    // 80 + 160
	assert.Equal(t, 35, results[0].CacheCreationInputTokens) // 10 + 25
	assert.False(t, results[0].IsError)
}

func TestParseOpenCodeStream_Malformed(t *testing.T) {
	t.Parallel()

	f := loadOpenCodeFixture(t, "malformed.ndjson")
	var events []AgentEvent
	sessionID, ml, err := parseOpenCodeStream(f, func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	assert.Equal(t, "ses_mal", sessionID)
	assert.Equal(t, 2, ml, "two malformed envelope lines should be counted")

	// Should get TextEvent from the valid line + TokensEvent + ResultEvent.
	var texts []TextEvent
	var results []ResultEvent
	for _, evt := range events {
		switch e := evt.(type) {
		case TextEvent:
			texts = append(texts, e)
		case ResultEvent:
			results = append(results, e)
		}
	}

	require.Len(t, texts, 1)
	assert.Equal(t, "valid line", texts[0].Text)

	require.Len(t, results, 1)
	assert.Equal(t, 1, results[0].NumTurns)
	// The stream has valid step_finish events so numTurns > 0. IsError is
	// false because the malformed lines were envelope failures (not error
	// events). The caller should use malformedLines > 0 to detect this.
	assert.False(t, results[0].IsError)
}

func TestParseOpenCodeStream_Empty(t *testing.T) {
	t.Parallel()

	events, sessionID := collectOpenCodeEvents(t, "empty.ndjson")

	assert.Empty(t, sessionID)

	// Should get only a ResultEvent with IsError=true (zero steps).
	require.Len(t, events, 1)
	result, ok := events[0].(ResultEvent)
	require.True(t, ok)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, result.NumTurns)
}

func TestParseOpenCodeStream_SessionID(t *testing.T) {
	t.Parallel()

	// Use inline ndjson to test sessionID from first event.
	input := `{"type":"text","timestamp":1723456781000,"sessionID":"ses_first","part":{"id":"prt_1","sessionID":"ses_first","messageID":"msg_1","type":"text","text":"hello","time":{"start":1,"end":2}}}
{"type":"step_finish","timestamp":1723456782000,"sessionID":"ses_first","part":{"id":"prt_2","sessionID":"ses_first","messageID":"msg_1","type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}
`
	var events []AgentEvent
	sessionID, ml, err := parseOpenCodeStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	assert.Equal(t, 0, ml, "no malformed lines in valid input")
	assert.Equal(t, "ses_first", sessionID)
	assert.Len(t, events, 3) // TextEvent + TokensEvent + ResultEvent
}

func TestParseOpenCodeStream_ReadError(t *testing.T) {
	t.Parallel()

	valid := `{"type":"text","timestamp":1,"sessionID":"ses_x","part":{"text":"hi"}}` + "\n"
	r := io.MultiReader(
		strings.NewReader(valid),
		iotest.ErrReader(errors.New("pipe broken")),
	)
	var events []AgentEvent
	sid, _, err := parseOpenCodeStream(r, func(e AgentEvent) { events = append(events, e) })
	require.Error(t, err)
	assert.Equal(t, "ses_x", sid)
	assert.Contains(t, err.Error(), "pipe broken")
}

func TestParseOpenCodeStream_SecretRedaction(t *testing.T) {
	t.Parallel()

	// Build fake tokens at runtime to avoid tripping gitleaks.
	// The redactor's prefix patterns match ghp_ and sk-proj- prefixes.
	ghToken := "ghp_" + strings.Repeat("x", 40)
	skToken := "sk-proj-" + strings.Repeat("y", 40)

	completedLine := fmt.Sprintf(
		`{"type":"tool_use","timestamp":1,"sessionID":"ses_sec","part":{"tool":"bash","state":{"status":"completed","title":"$ curl -H \"Authorization: Bearer %s\""}}}`,
		ghToken,
	)
	errorLine := fmt.Sprintf(
		`{"type":"tool_use","timestamp":2,"sessionID":"ses_sec","part":{"tool":"bash","state":{"status":"error","error":"request failed: token %s is expired"}}}`,
		skToken,
	)
	stepFinish := `{"type":"step_finish","timestamp":3,"sessionID":"ses_sec","part":{"reason":"stop","cost":0.01,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}`

	input := completedLine + "\n" + errorLine + "\n" + stepFinish + "\n"

	var events []AgentEvent
	_, _, err := parseOpenCodeStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	var tools []ToolUseEvent
	for _, evt := range events {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	}

	require.Len(t, tools, 2)

	// Completed tool: GitHub token in title must be redacted.
	assert.NotContains(t, tools[0].Summary, ghToken,
		"GitHub token should be redacted from completed tool summary")

	// Error tool: sk-proj token in error must be redacted.
	assert.NotContains(t, tools[1].Summary, skToken,
		"API key should be redacted from error tool summary")
}

func TestParseOpenCodeStream_SecretRedactionCleanPassthrough(t *testing.T) {
	t.Parallel()

	// Tool summary without any secrets should pass through unchanged.
	input := `{"type":"tool_use","timestamp":1,"sessionID":"ses_clean","part":{"tool":"read","state":{"status":"completed","title":"main.go"}}}
{"type":"step_finish","timestamp":2,"sessionID":"ses_clean","part":{"reason":"stop","cost":0.01,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}
`
	var events []AgentEvent
	_, _, err := parseOpenCodeStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	var tools []ToolUseEvent
	for _, evt := range events {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	}

	require.Len(t, tools, 1)
	assert.Equal(t, "main.go", tools[0].Summary, "clean summary should pass through unchanged")
}

func TestParseOpenCodeStream_PendingRunningFiltered(t *testing.T) {
	t.Parallel()

	// pending and running tool_use states should NOT produce ToolUseEvents.
	input := `{"type":"tool_use","timestamp":1,"sessionID":"ses_filt","part":{"tool":"bash","state":{"status":"pending","title":"$ ls"}}}
{"type":"tool_use","timestamp":2,"sessionID":"ses_filt","part":{"tool":"bash","state":{"status":"running","title":"$ ls"}}}
{"type":"tool_use","timestamp":3,"sessionID":"ses_filt","part":{"tool":"bash","state":{"status":"completed","title":"$ ls"}}}
{"type":"step_finish","timestamp":4,"sessionID":"ses_filt","part":{"reason":"stop","cost":0.01,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}
`
	var events []AgentEvent
	_, _, err := parseOpenCodeStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	var tools []ToolUseEvent
	for _, evt := range events {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	}

	require.Len(t, tools, 1, "only completed status should emit ToolUseEvent")
	assert.Equal(t, "$ ls", tools[0].Summary)
}

func TestParseOpenCodeStream_OversizedLineSkipped(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", 1024*1024+100) + "\n"
	valid := `{"type":"text","timestamp":1,"sessionID":"ses_big","part":{"text":"after"}}` + "\n"
	stepFinish := `{"type":"step_finish","timestamp":2,"sessionID":"ses_big","part":{"reason":"stop","cost":0.01,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\n"
	r := strings.NewReader(huge + valid + stepFinish)

	var events []AgentEvent
	_, _, err := parseOpenCodeStream(r, func(e AgentEvent) { events = append(events, e) })
	require.NoError(t, err)

	var texts []TextEvent
	for _, e := range events {
		if te, ok := e.(TextEvent); ok {
			texts = append(texts, te)
		}
	}
	require.Len(t, texts, 1)
	assert.Equal(t, "after", texts[0].Text)
}

func TestParseOpenCodeStream_TruncatedErrorEvent(t *testing.T) {
	t.Parallel()

	// A valid step_finish followed by a malformed error event: the envelope
	// unmarshal (ocEnvelope) succeeds because type and sessionID are present,
	// but the ocErrorEvent unmarshal fails because "error" has the wrong type
	// (string instead of object). Before the fix, sawError stayed false and
	// the run would report success.
	stepFinish := `{"type":"step_finish","timestamp":1,"sessionID":"ses_trunc","part":{"reason":"stop","cost":0.01,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}`
	// "error" is a string, not the expected {"name":...,"data":...} object.
	truncatedError := `{"type":"error","timestamp":2,"sessionID":"ses_trunc","error":"unexpected schema change"}`

	input := stepFinish + "\n" + truncatedError + "\n"

	var events []AgentEvent
	_, ml, err := parseOpenCodeStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	assert.Equal(t, 1, ml, "truncated error payload should be counted as malformed")

	var results []ResultEvent
	for _, evt := range events {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}

	require.Len(t, results, 1)
	// sawError is set before unmarshal, so IsError must be true even though
	// the error payload couldn't be parsed.
	assert.True(t, results[0].IsError, "truncated error event must set IsError=true")
	assert.Equal(t, 1, results[0].NumTurns)
}
