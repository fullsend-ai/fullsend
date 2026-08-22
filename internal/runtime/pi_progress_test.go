package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadPiFixture(t *testing.T, name string) *os.File {
	t.Helper()
	path := filepath.Join("testdata", "pi", name)
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

func collectPiEvents(t *testing.T, name string) ([]AgentEvent, string) {
	t.Helper()
	f := loadPiFixture(t, name)
	var events []AgentEvent
	sessionID, err := parsePiStream(f, func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	return events, sessionID
}

func TestParsePiStream_BasicRun(t *testing.T) {
	t.Parallel()

	events, sessionID := collectPiEvents(t, "basic_run.ndjson")

	assert.Equal(t, "ses_pi_abc123", sessionID)

	var inits []InitEvent
	var texts []TextEvent
	var tools []ToolUseEvent
	var tokens []TokensEvent
	var results []ResultEvent
	for _, evt := range events {
		switch e := evt.(type) {
		case InitEvent:
			inits = append(inits, e)
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

	// Model comes from the assistant message, not the session header.
	require.Len(t, inits, 1)
	assert.Equal(t, "claude-sonnet-4-20250514", inits[0].Model)
	assert.Empty(t, inits[0].Version, "CLI version is not on the --mode json wire")

	require.Len(t, texts, 1)
	assert.Equal(t, "I'll list the files for you.", texts[0].Text)

	require.Len(t, tools, 1)
	assert.Equal(t, "bash", tools[0].Name)
	assert.Equal(t, "$ ls", tools[0].Summary, "summary comes from the call's arguments, not its output")

	require.Len(t, tokens, 1)
	assert.Equal(t, 100, tokens[0].InputTokens)
	assert.Equal(t, 50, tokens[0].OutputTokens)
	assert.Equal(t, 80, tokens[0].CacheRead)
	assert.Equal(t, 20, tokens[0].CacheWrite)

	require.Len(t, results, 1)
	assert.Equal(t, 1, results[0].NumTurns)
	assert.InDelta(t, 0.015, results[0].TotalCostUSD, 0.001)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "stop", results[0].Subtype)
	assert.Equal(t, 100, results[0].InputTokens)
	assert.Equal(t, 50, results[0].OutputTokens)

	_, isResult := events[len(events)-1].(ResultEvent)
	assert.True(t, isResult, "ResultEvent should be the last event")
}

func TestParsePiStream_ErrorRun(t *testing.T) {
	t.Parallel()

	events, sessionID := collectPiEvents(t, "error_run.ndjson")

	assert.Equal(t, "ses_pi_err456", sessionID)

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
	assert.Equal(t, "/nonexistent: file not found", tools[0].Summary,
		"failed tool keeps its argument context and appends the error text")

	require.Len(t, errEvents, 1)
	assert.Equal(t, "error", errEvents[0].ErrorType)
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

func TestParsePiStream_Reasoning(t *testing.T) {
	t.Parallel()

	events, _ := collectPiEvents(t, "reasoning_run.ndjson")

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

	require.Len(t, tokens, 1)
	assert.Equal(t, 50, tokens[0].ReasoningTokens, "reasoning tokens should be captured per-message")
	assert.Equal(t, 200, tokens[0].InputTokens)
	assert.Equal(t, 100, tokens[0].OutputTokens)

	require.Len(t, results, 1)
	assert.Equal(t, 50, results[0].ReasoningTokens, "reasoning tokens should appear in ResultEvent")
}

func TestParsePiStream_MultiStep(t *testing.T) {
	t.Parallel()

	events, _ := collectPiEvents(t, "multi_step.ndjson")

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
	assert.Equal(t, 100, tokens[0].InputTokens)
	assert.Equal(t, 30, tokens[0].OutputTokens)
	assert.Equal(t, 200, tokens[1].InputTokens)
	assert.Equal(t, 70, tokens[1].OutputTokens)

	require.Len(t, results, 1)
	assert.Equal(t, 2, results[0].NumTurns)
	assert.InDelta(t, 0.03, results[0].TotalCostUSD, 0.001)
	assert.Equal(t, 300, results[0].InputTokens)
	assert.Equal(t, 100, results[0].OutputTokens)
	assert.Equal(t, 240, results[0].CacheReadInputTokens)
	assert.Equal(t, 35, results[0].CacheCreationInputTokens)
	assert.False(t, results[0].IsError)
}

func TestParsePiStream_Malformed(t *testing.T) {
	t.Parallel()

	events, sessionID := collectPiEvents(t, "malformed.ndjson")

	assert.Equal(t, "ses_pi_mal", sessionID)

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
	assert.False(t, results[0].IsError)
}

func TestParsePiStream_Empty(t *testing.T) {
	t.Parallel()

	events, sessionID := collectPiEvents(t, "empty.ndjson")

	assert.Empty(t, sessionID)

	require.Len(t, events, 1)
	result, ok := events[0].(ResultEvent)
	require.True(t, ok)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, result.NumTurns)
	assert.Equal(t, "incomplete", result.Subtype)
}

func TestParsePiStream_Truncated(t *testing.T) {
	t.Parallel()

	events, sessionID := collectPiEvents(t, "truncated.ndjson")

	assert.Equal(t, "ses_pi_trunc", sessionID)

	var results []ResultEvent
	for _, evt := range events {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}

	require.Len(t, results, 1)
	assert.Equal(t, 1, results[0].NumTurns)
	assert.InDelta(t, 0.015, results[0].TotalCostUSD, 0.001)
	assert.True(t, results[0].IsError, "missing agent_end is incomplete, not success")
	assert.Equal(t, "incomplete", results[0].Subtype)
	assert.Equal(t, 100, results[0].InputTokens)
	assert.Equal(t, 50, results[0].OutputTokens)
	assert.Equal(t, 80, results[0].CacheReadInputTokens)
	assert.Equal(t, 20, results[0].CacheCreationInputTokens)
}

func TestParsePiStream_SessionID(t *testing.T) {
	t.Parallel()

	input := `{"type":"session","version":3,"id":"ses_first","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"message_update","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"totalTokens":15,"cost":{"total":0.01}},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hello"}}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}],"willRetry":false}
`
	var events []AgentEvent
	sessionID, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	assert.Equal(t, "ses_first", sessionID)
	assert.Len(t, events, 4) // InitEvent + TextEvent + TokensEvent + ResultEvent
}

func TestParsePiStream_ReadError(t *testing.T) {
	t.Parallel()

	valid := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hi"}}` + "\n"
	r := io.MultiReader(
		strings.NewReader(valid),
		iotest.ErrReader(errors.New("pipe broken")),
	)
	var events []AgentEvent
	sid, err := parsePiStream(r, func(e AgentEvent) { events = append(events, e) })
	require.Error(t, err)
	assert.Empty(t, sid)
	assert.Contains(t, err.Error(), "pipe broken")
}

func TestParsePiStream_SecretRedaction(t *testing.T) {
	t.Parallel()

	ghToken := "ghp_" + strings.Repeat("x", 40)
	skToken := "sk-proj-" + strings.Repeat("y", 40)

	completedLine := fmt.Sprintf(
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":"curl -H \"Authorization: Bearer %s\"","isError":false}`,
		ghToken,
	)
	errorLine := fmt.Sprintf(
		`{"type":"tool_execution_end","toolCallId":"c2","toolName":"bash","result":"request failed: token %s is expired","isError":true}`,
		skToken,
	)
	messageEnd := `{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}`
	agentEnd := `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}],"willRetry":false}`

	input := completedLine + "\n" + errorLine + "\n" + messageEnd + "\n" + agentEnd + "\n"

	var events []AgentEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
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
	assert.NotContains(t, tools[0].Summary, ghToken,
		"GitHub token should be redacted from completed tool summary")
	assert.NotContains(t, tools[1].Summary, skToken,
		"API key should be redacted from error tool summary")
}

func TestParsePiStream_ErrorStopReason(t *testing.T) {
	t.Parallel()

	// Assistant stopReason "error" must set IsError and ErrorMessage even
	// without a distinct error event — --mode json exits 0 on model error.
	input := `{"type":"session","version":3,"id":"ses_errsr","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"model overloaded"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"model overloaded"}],"willRetry":false}
`
	var events []AgentEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	var results []ResultEvent
	for _, evt := range events {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}

	require.Len(t, results, 1)
	assert.True(t, results[0].IsError, "stopReason=error must set IsError=true")
	assert.Equal(t, "error", results[0].Subtype)
	assert.Equal(t, "model overloaded", results[0].ErrorMessage)
}

func TestParsePiStream_AbortedStopReason(t *testing.T) {
	t.Parallel()

	input := `{"type":"session","version":3,"id":"ses_abort","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"aborted","errorMessage":"request aborted"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"aborted","errorMessage":"request aborted"}],"willRetry":false}
`
	var events []AgentEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	var results []ResultEvent
	for _, evt := range events {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}

	require.Len(t, results, 1)
	assert.True(t, results[0].IsError, "stopReason=aborted must set IsError=true")
	assert.Equal(t, "aborted", results[0].Subtype)
	assert.Equal(t, "request aborted", results[0].ErrorMessage)
}

func TestParsePiStream_OversizedLineSkipped(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", 1024*1024+100) + "\n"
	valid := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"after"}}` + "\n"
	messageEnd := `{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}},"stopReason":"stop"}}` + "\n"
	agentEnd := `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}],"willRetry":false}` + "\n"
	r := strings.NewReader(huge + valid + messageEnd + agentEnd)

	var events []AgentEvent
	_, err := parsePiStream(r, func(e AgentEvent) { events = append(events, e) })
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

func TestParsePiStream_ToolExecutionStartAbsorbed(t *testing.T) {
	t.Parallel()

	input := `{"type":"session","version":3,"id":"ses_tc","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"echo hello"}}
{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":"hello\n","isError":false}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}],"willRetry":false}
`
	var events []AgentEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	var tools []ToolUseEvent
	for _, evt := range events {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	}

	require.Len(t, tools, 1, "only tool_execution_end should emit ToolUseEvent")
	assert.Equal(t, "$ echo hello", tools[0].Summary, "summary is the command from tool_execution_start.args")
}

func TestPiToolContext(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tool, args, want string
	}{
		{"bash", `{"command":"  ls  -la\n/tmp "}`, "$ ls -la /tmp"},
		{"read", `{"path":"/sandbox/workspace/repo/main.go","offset":1}`, "/sandbox/workspace/repo/main.go"},
		{"write", `{"path":"out.txt","content":"secret body"}`, "out.txt"},
		{"edit", `{"path":"a.go","edits":[]}`, "a.go"},
		{"ls", `{"path":"src"}`, "src"},
		{"grep", `{"pattern":"TODO","path":"."}`, "TODO"},
		{"find", `{"pattern":"**/*.go"}`, "**/*.go"},
		{"bash", `{"timeout":5}`, ""},
		{"unknown_tool", `{"command":"x"}`, ""},
		{"bash", `not json`, ""},
		{"bash", `{"command":"curl -H 'Authorization: Bearer ghp_` + strings.Repeat("q", 40) + `'"}`, "$ curl -H 'Authorization: Bearer "},
	}
	for _, tc := range cases {
		got := piToolContext(tc.tool, json.RawMessage(tc.args))
		if strings.HasSuffix(tc.want, "Bearer ") {
			assert.True(t, strings.HasPrefix(got, tc.want), "%s %s → %q", tc.tool, tc.args, got)
			assert.NotContains(t, got, "ghp_q", "token in tool args must be redacted")
			continue
		}
		assert.Equal(t, tc.want, got, "%s %s", tc.tool, tc.args)
	}

	long := strings.Repeat("p", maxPathDisplay+5)
	assert.Equal(t, strings.Repeat("p", maxPathDisplay)+"…", piToolContext("read", json.RawMessage(`{"path":"`+long+`"}`)))
}

func TestPiToolContext_RedactsBeforeCapping(t *testing.T) {
	t.Parallel()

	// A token that starts inside the display cap but ends past it must be
	// redacted as a whole; cutting first would leave a usable prefix. These
	// forms have no "Authorization:"/"token=" wrapper, so only the prefix
	// pattern (which needs the full token) can catch them.
	token := "ghp_" + strings.Repeat("Q", 40)
	cmd := "cd /sandbox/workspace/repo && git clone --depth 1 --single-branch --branch main https://x-access-token:" + token + "@github.com/org/repo.git ../clone"
	got := piToolContext("bash", json.RawMessage(fmt.Sprintf(`{"command":%q}`, cmd)))
	assert.NotContains(t, got, "ghp_Q", "command: %q", got)
	assert.Contains(t, got, "x-access-token:ghp_", "redaction marker keeps the prefix for context")

	path := "/sandbox/workspace/" + strings.Repeat("d/", (maxPathDisplay-len("/sandbox/workspace/")-6)/2) + token
	got = piToolContext("read", json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)))
	assert.NotContains(t, got, "ghp_Q", "path: %q", got)
	assert.LessOrEqual(t, utf8.RuneCountInString(got), maxPathDisplay+1)
}

func TestParsePiStream_UnknownToolNeverSurfacesOutput(t *testing.T) {
	t.Parallel()

	// An extension-registered tool has no argument context. Its successful
	// output must not become the summary; its error text still may.
	input := `{"type":"tool_execution_start","toolCallId":"x1","toolName":"my_ext_tool","args":{"query":"q"}}
{"type":"tool_execution_end","toolCallId":"x1","toolName":"my_ext_tool","result":"BIG OUTPUT","isError":false}
{"type":"tool_execution_start","toolCallId":"x2","toolName":"my_ext_tool","args":{"query":"q"}}
{"type":"tool_execution_end","toolCallId":"x2","toolName":"my_ext_tool","result":"upstream 503","isError":true}
{"type":"tool_execution_end","toolCallId":"orphan","toolName":"my_ext_tool","result":"no start seen","isError":false}
{"type":"agent_end","messages":[],"willRetry":false}
`
	var tools []ToolUseEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, tools, 3)
	assert.Equal(t, "", tools[0].Summary, "successful unknown tool: no output leaks into the summary")
	assert.Equal(t, "upstream 503", tools[1].Summary)
	assert.Equal(t, "no start seen", tools[2].Summary, "end without a start falls back to result text")
}

func TestParsePiStream_ToolSummaryFromArgsNotOutput(t *testing.T) {
	t.Parallel()

	// A successful tool's (possibly huge or sensitive) output never reaches
	// the summary; a failed tool appends its result text for diagnosis.
	output := strings.Repeat("secret-looking-output ", 50)
	input := `{"type":"tool_execution_start","toolCallId":"ok","toolName":"read","args":{"path":"notes.txt"}}
{"type":"tool_execution_end","toolCallId":"ok","toolName":"read","result":{"content":[{"type":"text","text":` + fmt.Sprintf("%q", output) + `}]},"isError":false}
{"type":"tool_execution_start","toolCallId":"bad","toolName":"bash","args":{"command":"make"}}
{"type":"tool_execution_end","toolCallId":"bad","toolName":"bash","result":{"content":[{"type":"text","text":"make: *** No targets. Stop."}]},"isError":true}
{"type":"tool_execution_start","toolCallId":"interleaved-a","toolName":"grep","args":{"pattern":"alpha"}}
{"type":"tool_execution_start","toolCallId":"interleaved-b","toolName":"grep","args":{"pattern":"beta"}}
{"type":"tool_execution_end","toolCallId":"interleaved-b","toolName":"grep","result":"b","isError":false}
{"type":"tool_execution_end","toolCallId":"interleaved-a","toolName":"grep","result":"a","isError":false}
{"type":"agent_end","messages":[],"willRetry":false}
`
	var tools []ToolUseEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, tools, 4)
	assert.Equal(t, "notes.txt", tools[0].Summary)
	assert.NotContains(t, tools[0].Summary, "secret-looking-output")
	assert.Equal(t, "$ make: make: *** No targets. Stop.", tools[1].Summary)
	assert.Equal(t, "beta", tools[2].Summary, "parallel tool calls are matched by toolCallId")
	assert.Equal(t, "alpha", tools[3].Summary)
}

func TestParsePiStream_ToolResultObjectSummary(t *testing.T) {
	t.Parallel()

	input := `{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"ok from content"}]},"isError":false}
{"type":"tool_execution_end","toolCallId":"c2","toolName":"edit","result":{"content":[{"type":"text","text":"file not found"},{"type":"image","text":""}]},"isError":true}
{"type":"agent_end","messages":[],"willRetry":false}
`
	var tools []ToolUseEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "ok from content", tools[0].Summary)
	assert.Equal(t, "file not found", tools[1].Summary)
}

func TestParsePiStream_AgentEndWillRetry(t *testing.T) {
	t.Parallel()

	// willRetry=true is a checkpoint, not the terminal result. A later
	// agent_end with willRetry=false should be the only ResultEvent.
	input := `{"type":"session","version":3,"id":"ses_retry","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"agent_end","messages":[],"willRetry":true}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"recovered"}}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}
`
	var results []ResultEvent
	var texts []TextEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		switch e := evt.(type) {
		case ResultEvent:
			results = append(results, e)
		case TextEvent:
			texts = append(texts, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, texts, 1)
	require.Len(t, results, 1, "willRetry=true agent_end must not emit ResultEvent")
	assert.False(t, results[0].IsError)
	assert.Equal(t, 1, results[0].NumTurns)
	assert.Equal(t, "stop", results[0].Subtype)
}

func TestParsePiStream_RetryClearsStickyError(t *testing.T) {
	t.Parallel()

	input := `{"type":"session","version":3,"id":"ses_sticky","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"model overloaded"}}
{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"model overloaded"}],"willRetry":true}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":12,"output":6,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.02}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}
`
	var results []ResultEvent
	var errs []ErrorEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		switch e := evt.(type) {
		case ResultEvent:
			results = append(results, e)
		case ErrorEvent:
			errs = append(errs, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, errs, 1, "failed attempt still emits ErrorEvent")
	require.Len(t, results, 1)
	assert.False(t, results[0].IsError, "successful retry must not inherit sticky IsError")
	assert.Empty(t, results[0].ErrorMessage)
	assert.Equal(t, "stop", results[0].Subtype)
}

func TestParsePiStream_InitSkipsEmptyModel(t *testing.T) {
	t.Parallel()

	input := `{"type":"message_start","message":{"role":"assistant","model":"","stopReason":"pending"}}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}
`
	var inits []InitEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(InitEvent); ok {
			inits = append(inits, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, inits, 1)
	assert.Equal(t, "claude-sonnet-4-20250514", inits[0].Model)
}

func TestParsePiStream_InventedSchemaIsDropped(t *testing.T) {
	t.Parallel()

	// The previous fixture vocabulary (top-level text / tool_result /
	// snake_case stop_reason) is not pi's wire format. A real stream using
	// only those names must not produce TextEvent/ToolUseEvent/TokensEvent.
	input := `{"type":"session","session_id":"ses_fake","model":"claude-sonnet-4-20250514","version":"0.84.2"}
{"type":"text","session_id":"ses_fake","text":"should not appear"}
{"type":"tool_result","session_id":"ses_fake","tool":"bash","status":"completed","title":"$ ls"}
{"type":"message_end","session_id":"ses_fake","usage":{"input_tokens":10,"output_tokens":5},"cost":0.01}
{"type":"agent_end","session_id":"ses_fake","stop_reason":"end_turn"}
`
	var events []AgentEvent
	sessionID, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	assert.Empty(t, sessionID, "session id is `id`, not session_id")

	for _, evt := range events {
		switch evt.(type) {
		case TextEvent, ToolUseEvent, TokensEvent, InitEvent:
			t.Fatalf("invented schema must not produce %T", evt)
		}
	}
	require.Len(t, events, 1)
	result, ok := events[0].(ResultEvent)
	require.True(t, ok)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, result.NumTurns)
}

func TestParsePiStream_ResultSummaryTruncated(t *testing.T) {
	t.Parallel()

	// Object with no content[] hits the raw-JSON fallback; it must be capped.
	huge := strings.Repeat("x", piSummaryMax+200)
	input := fmt.Sprintf(
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"blob":%q},"isError":false}`+"\n"+
			`{"type":"agent_end","messages":[],"willRetry":false}`+"\n",
		huge,
	)
	var tools []ToolUseEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ToolUseEvent); ok {
			tools = append(tools, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.LessOrEqual(t, len(tools[0].Summary), piSummaryMax)
	assert.NotContains(t, tools[0].Summary, strings.Repeat("x", piSummaryMax+1))
}

func TestParsePiStream_SecretStraddlesSummaryCap(t *testing.T) {
	t.Parallel()

	// The token starts 20 bytes before the cap. Truncating before redacting
	// would hand the redactor a fragment too short to match its pattern and
	// emit "ghp_" + 20 real characters.
	token := "ghp_" + strings.Repeat("A", 40)
	body := strings.Repeat("x", piSummaryMax-len("ghp_")-20) + token + " trailing"

	cases := map[string]string{
		"string":  fmt.Sprintf("%q", body),
		"content": fmt.Sprintf(`{"content":[{"type":"text","text":%q}]}`, body),
		"raw":     fmt.Sprintf(`{"blob":%q}`, body),
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := fmt.Sprintf(
				`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":%s,"isError":false}`+"\n"+
					`{"type":"agent_end","messages":[],"willRetry":false}`+"\n",
				result,
			)
			var tools []ToolUseEvent
			_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
				if e, ok := evt.(ToolUseEvent); ok {
					tools = append(tools, e)
				}
			})
			require.NoError(t, err)
			require.Len(t, tools, 1)
			assert.LessOrEqual(t, len(tools[0].Summary), piSummaryMax)
			assert.NotContains(t, tools[0].Summary, "ghp_A",
				"partial token must not survive the summary cap")
		})
	}
}

func TestPiTruncate_RuneBoundary(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("x", piSummaryMax-1)
	out := piTruncate(prefix+"日本", piSummaryMax)
	assert.True(t, utf8.ValidString(out))
	assert.Equal(t, prefix, out, "cut must step back to the rune boundary")

	assert.Equal(t, "ab", piTruncate("ab", 2))
	assert.Equal(t, "日", piTruncate("日本", 3))
	assert.Equal(t, "", piTruncate("日", 1))

	// Already-invalid input (a run of continuation bytes) has no boundary to
	// find; the walk-back is bounded so the cap, not an empty string, wins.
	junk := strings.Repeat("\x80", 8)
	assert.Equal(t, junk[:4], piTruncate(junk, 4))
}

func TestParsePiStream_RetryCheckpointErrorFromAgentEndOnly(t *testing.T) {
	t.Parallel()

	// The checkpoint's own messages[] is the only source of the error when
	// no message_end preceded it.
	input := `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"model overloaded"}],"willRetry":true}` + "\n"
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "error", results[0].Subtype)
	assert.Equal(t, "model overloaded", results[0].ErrorMessage)
}

func TestParsePiStream_AgentSettledReleasesResultOnce(t *testing.T) {
	t.Parallel()

	// agent_settled is the end-of-prompt marker; the result built at
	// agent_end is settled there and emitted exactly once at EOF, so
	// trailing lines after it do not produce a second ResultEvent.
	input := `{"type":"session","version":3,"id":"ses_settled","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
{"type":"queue_update","steering":[],"followUp":[]}
`
	var events []AgentEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	var results []ResultEvent
	for _, evt := range events {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}
	require.Len(t, results, 1)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "stop", results[0].Subtype)
	_, last := events[len(events)-1].(ResultEvent)
	assert.True(t, last, "ResultEvent is released at agent_settled, after TokensEvent")
}

func TestParsePiStream_CompactionContinuesRun(t *testing.T) {
	t.Parallel()

	// Context overflow: pi emits agent_end{willRetry:false} (overflow is not
	// a retryable error), then compacts and continues the same prompt via a
	// fresh agent_start. Only the settled run may produce a ResultEvent, and
	// it must not inherit the overflow attempt's sticky error.
	input := `{"type":"session","version":3,"id":"ses_compact","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":100000,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.30}},"stopReason":"error","errorMessage":"context window exceeded"}}
{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"context window exceeded"}],"willRetry":false}
{"type":"compaction_start","reason":"overflow"}
{"type":"compaction_end","reason":"overflow","result":{},"aborted":false,"willRetry":true}
{"type":"agent_start"}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"done"}}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":2000,"output":50,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.02}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
`
	var results []ResultEvent
	var errs []ErrorEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		switch e := evt.(type) {
		case ResultEvent:
			results = append(results, e)
		case ErrorEvent:
			errs = append(errs, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, errs, 1, "overflow attempt still surfaces an ErrorEvent")
	require.Len(t, results, 1, "continued run must not emit two ResultEvents")
	assert.False(t, results[0].IsError)
	assert.Empty(t, results[0].ErrorMessage)
	assert.Equal(t, "stop", results[0].Subtype)
	assert.Equal(t, 2, results[0].NumTurns)
	assert.InDelta(t, 0.32, results[0].TotalCostUSD, 0.001)
}

func TestParsePiStream_ContinuedRunThenEOF(t *testing.T) {
	t.Parallel()

	// The run continued after agent_end but died before producing anything:
	// the discarded result must not leak out, and the overflow reason is
	// carried into the incomplete-stream fallback.
	input := `{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":100000,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.30}},"stopReason":"error","errorMessage":"context window exceeded"}}
{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"context window exceeded"}],"willRetry":false}
{"type":"compaction_start","reason":"overflow"}
{"type":"compaction_end","reason":"overflow","result":{},"aborted":false,"willRetry":true}
{"type":"agent_start"}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"retrying"}}
`
	var events []AgentEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	var results []ResultEvent
	for _, evt := range events {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "error", results[0].Subtype)
	assert.Equal(t, "context window exceeded", results[0].ErrorMessage)
	_, last := events[len(events)-1].(ResultEvent)
	assert.True(t, last, "ResultEvent must come from the EOF fallback, after the continued run's TextEvent")
}

func TestParsePiStream_CompactionWithoutRetryThenSettled(t *testing.T) {
	t.Parallel()

	// Threshold/overflow compaction after a clean stop does not re-run the
	// turn: the parked result stands and is emitted at EOF.
	input := `{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}
{"type":"compaction_start","reason":"threshold"}
{"type":"compaction_end","reason":"threshold","result":{},"aborted":false,"willRetry":false}
{"type":"agent_settled"}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "stop", results[0].Subtype)
}

func TestParsePiStream_DiedDuringCompactionIsIncomplete(t *testing.T) {
	t.Parallel()

	// A recoverable "length" stop is compact-and-retried by pi. If the
	// stream dies inside the compaction window the parked result (which
	// would read as a clean length stop) must not be reported as finished.
	input := `{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"length"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"length"}],"willRetry":false}
{"type":"compaction_start","reason":"overflow"}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "incomplete", results[0].Subtype)
}

func TestParsePiStream_ReadErrorStillEmitsResult(t *testing.T) {
	t.Parallel()

	run := `{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}` + "\n" +
		`{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}` + "\n"

	// A lost stream (read error, not EOF) is not evidence that pi finished:
	// an unsettled result is reported incomplete, a settled one stands.
	cases := map[string]struct {
		input   string
		isError bool
		subtype string
	}{
		"before settled": {run, true, "incomplete"},
		"after settled":  {run + `{"type":"agent_settled"}` + "\n", false, "stop"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := io.MultiReader(
				strings.NewReader(tc.input),
				iotest.ErrReader(errors.New("pipe broken")),
			)
			var results []ResultEvent
			_, err := parsePiStream(r, func(evt AgentEvent) {
				if e, ok := evt.(ResultEvent); ok {
					results = append(results, e)
				}
			})
			require.Error(t, err)
			require.Len(t, results, 1, "a read error must still yield the stream's ResultEvent")
			assert.Equal(t, tc.isError, results[0].IsError)
			assert.Equal(t, tc.subtype, results[0].Subtype)
			assert.Equal(t, 1, results[0].NumTurns)
		})
	}
}

func TestParsePiStream_QueuedFollowUpContinuesRun(t *testing.T) {
	t.Parallel()

	// A follow-up queued by an agent_end extension handler continues the
	// prompt with a bare agent_start (no compaction events). The first
	// agent_end's result is discarded; the settled continuation is reported.
	input := `{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"transient"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"error","errorMessage":"transient"}],"willRetry":false}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "stop", results[0].Subtype)
	assert.Equal(t, 2, results[0].NumTurns)
}

func TestParsePiStream_MalformedCompactionEndDoesNotPoisonRun(t *testing.T) {
	t.Parallel()

	input := `{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"stop"}],"willRetry":false}
{"type":"compaction_start","reason":"threshold"}
{"type":"compaction_end","willRetry":"not-a-bool"}
{"type":"agent_settled"}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].IsError, "an unparseable compaction_end must not leave the run marked as compacting")
}

func TestParsePiStream_MultiPromptLastRunWins(t *testing.T) {
	t.Parallel()

	// pi runs one prompt per positional message, each ending in
	// agent_settled. The stream yields a single ResultEvent covering all
	// runs; a failure in any run is sticky.
	run := func(stop, errMsg string) string {
		return `{"type":"agent_start"}` + "\n" +
			`{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"` + stop + `","errorMessage":"` + errMsg + `"}}` + "\n" +
			`{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"` + stop + `","errorMessage":"` + errMsg + `"}],"willRetry":false}` + "\n" +
			`{"type":"agent_settled"}` + "\n"
	}
	cases := map[string]struct {
		input   string
		isError bool
	}{
		"second fails": {run("stop", "") + run("error", "quota exhausted"), true},
		"first fails":  {run("error", "quota exhausted") + run("stop", ""), true},
		"both succeed": {run("stop", "") + run("stop", ""), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var results []ResultEvent
			_, err := parsePiStream(strings.NewReader(tc.input), func(evt AgentEvent) {
				if e, ok := evt.(ResultEvent); ok {
					results = append(results, e)
				}
			})
			require.NoError(t, err)
			require.Len(t, results, 1)
			assert.Equal(t, tc.isError, results[0].IsError)
			assert.Equal(t, 2, results[0].NumTurns)
			if tc.isError {
				assert.Equal(t, "quota exhausted", results[0].ErrorMessage)
			}
		})
	}
}

func TestParsePiStream_EarlierPromptFailureSurvivesLaterRetry(t *testing.T) {
	t.Parallel()

	// Prompt 1 fails; prompt 2 hits a retryable error, retries (a checkpoint
	// resets the per-run error state) and recovers. The earlier failure
	// must still make the single result an error.
	input := `{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"quota exhausted"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"error","errorMessage":"quota exhausted"}],"willRetry":false}
{"type":"agent_settled"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"overloaded"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"error","errorMessage":"overloaded"}],"willRetry":true}
{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":1000,"errorMessage":"overloaded"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError, "prompt 1 failure must survive prompt 2's retry checkpoint")
	assert.Equal(t, "quota exhausted", results[0].ErrorMessage)
	assert.Equal(t, 3, results[0].NumTurns)
}

func TestParsePiStream_ReadErrorDuringSecondPrompt(t *testing.T) {
	t.Parallel()

	// Prompt 1 settled cleanly, prompt 2 started, then the stream was lost:
	// the settled result must not mask the unfinished prompt.
	valid := `{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
{"type":"agent_start"}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"second"}}
`
	r := io.MultiReader(strings.NewReader(valid), iotest.ErrReader(errors.New("pipe broken")))
	var results []ResultEvent
	_, err := parsePiStream(r, func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.Error(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "incomplete", results[0].Subtype)
	assert.Equal(t, 1, results[0].NumTurns)
}

func TestParsePiStream_SecondPromptDiesBeforeAgentEnd(t *testing.T) {
	t.Parallel()

	input := `{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
{"type":"agent_start"}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"second"}}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError, "the settled first prompt must not mask an incomplete second prompt")
	assert.Equal(t, "incomplete", results[0].Subtype)
}

func TestParsePiStream_AutoRetryStartEmitsRetryEvent(t *testing.T) {
	t.Parallel()

	ghToken := "ghp_" + strings.Repeat("x", 40)
	input := fmt.Sprintf(`{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"overloaded"}}
{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"overloaded"}],"willRetry":true}
{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"overloaded (token %s)"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":12,"output":6,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.02}},"stopReason":"stop"}}
{"type":"auto_retry_end","success":true,"attempt":1}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
`, ghToken)
	var retries []RetryEvent
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		switch e := evt.(type) {
		case RetryEvent:
			retries = append(retries, e)
		case ResultEvent:
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, retries, 1)
	assert.Equal(t, 1, retries[0].Attempt)
	assert.Equal(t, 3, retries[0].MaxRetries)
	assert.Equal(t, 2000, retries[0].DelayMs)
	assert.Contains(t, retries[0].Error, "overloaded")
	assert.NotContains(t, retries[0].Error, ghToken, "retry error text is redacted")
	require.Len(t, results, 1)
	assert.False(t, results[0].IsError)
}

func TestParsePiStream_RetryCheckpointThenEOF(t *testing.T) {
	t.Parallel()

	// Stream dies right after a willRetry checkpoint: the result must still
	// be an error and must carry the reason the runtime was retrying.
	input := `{"type":"session","version":3,"id":"ses_ckpt","timestamp":"2026-08-14T12:00:00.000Z","cwd":"/tmp"}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"model overloaded"}}
{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"model overloaded"}],"willRetry":true}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "error", results[0].Subtype)
	assert.Equal(t, "model overloaded", results[0].ErrorMessage)
	assert.Equal(t, 1, results[0].NumTurns)
}

func TestParsePiStream_RetryCheckpointSupersededThenEOF(t *testing.T) {
	t.Parallel()

	// Once any assistant message arrives after the checkpoint, the EOF
	// fallback reports that live state (here: a clean "stop" that never got
	// its agent_end → incomplete) rather than the parked checkpoint error.
	input := `{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"error","errorMessage":"model overloaded"}}
{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"model overloaded"}],"willRetry":true}
{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":12,"output":6,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.02}},"stopReason":"stop"}}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "incomplete", results[0].Subtype)
	assert.Empty(t, results[0].ErrorMessage)
}

func TestParsePiStream_LengthStopReasonIsNotError(t *testing.T) {
	t.Parallel()

	// stopReason "length" (max output tokens) is a completed run; the cutoff
	// is surfaced via Subtype only.
	input := `{"type":"message_end","message":{"role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.01}},"stopReason":"length"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"claude-sonnet-4-20250514","stopReason":"length"}],"willRetry":false}
`
	var results []ResultEvent
	_, err := parsePiStream(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "length", results[0].Subtype)
}
