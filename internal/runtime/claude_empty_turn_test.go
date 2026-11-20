package runtime

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// claudeEmptyTerminalTurnStream ends the way the run in #9744 did: a tool
// result, then a success result with empty text and no assistant message
// in between. The tool result text stands in for what the agent read.
const claudeEmptyTerminalTurnStream = `{"type":"system","subtype":"init","model":"claude-opus-5-5","claude_code_version":"2.1.296","session_id":"3e1f0a2b-4444-4750-89b9-3129edaa5ccf"}
{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok PRIVATE-TOOL-OUTPUT"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"","stop_reason":"end_turn","terminal_reason":"completed","num_turns":39,"total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":2}}
`

// lastClaudeResult parses stream and returns its final ResultEvent.
func lastClaudeResult(t *testing.T, stream string) *ResultEvent {
	t.Helper()
	var last *ResultEvent
	for _, evt := range collectEvents(t, stream) {
		if r, ok := evt.(ResultEvent); ok {
			last = &r
		}
	}
	require.NotNil(t, last, "the stream has a result")
	return last
}

func TestParseClaudeStream_ResultCarriesTerminalTurnFields(t *testing.T) {
	r := lastClaudeResult(t, claudeEmptyTerminalTurnStream)
	assert.Equal(t, "end_turn", r.StopReason)
	assert.Equal(t, "completed", r.TerminalReason)
	assert.True(t, r.EndedAfterToolResult)
	assert.True(t, claudeEmptyTerminalTurn(r))
}

func TestClaudeEmptyTerminalTurn(t *testing.T) {
	const (
		toolUse    = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}` + "\n"
		toolResult = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a"}]}}` + "\n"
		answer     = `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}` + "\n"
		emptyEnd   = `{"type":"assistant","message":{"content":[]}}` + "\n"
		emptyOK    = `{"type":"result","subtype":"success","is_error":false,"result":"","stop_reason":"end_turn","terminal_reason":"completed","num_turns":2}` + "\n"
	)
	tests := []struct {
		name   string
		stream string
		want   bool
	}{
		{"empty success right after a tool result", toolUse + toolResult + emptyOK, true},
		{"whitespace-only result counts as empty", toolUse + toolResult + strings.Replace(emptyOK, `"result":""`, `"result":" \n"`, 1), true},
		{"final assistant text and a non-empty result", toolUse + toolResult + answer +
			`{"type":"result","subtype":"success","is_error":false,"result":"done","stop_reason":"end_turn","terminal_reason":"completed","num_turns":2}` + "\n", false},
		{"empty result after an assistant message", toolUse + toolResult + emptyEnd + emptyOK, false},
		{"non-empty result after a tool result", toolUse + toolResult + strings.Replace(emptyOK, `"result":""`, `"result":"done"`, 1), false},
		{"error result keeps the error path", toolUse + toolResult +
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","stop_reason":"end_turn","terminal_reason":"completed","num_turns":2}` + "\n", false},
		{"other terminal reason", toolUse + toolResult + strings.Replace(emptyOK, `"completed"`, `"aborted_tools"`, 1), false},
		{"other stop reason", toolUse + toolResult + strings.Replace(emptyOK, `"end_turn"`, `"tool_use"`, 1), false},
		{"no stop or terminal reason (older Claude Code)", toolUse + toolResult +
			`{"type":"result","subtype":"success","is_error":false,"result":"","num_turns":2}` + "\n", false},
		{"a streamed message after the tool result", toolUse + toolResult +
			`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":1}}}}` + "\n" + emptyOK, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, claudeEmptyTerminalTurn(lastClaudeResult(t, tt.stream)))
		})
	}
	assert.False(t, claudeEmptyTerminalTurn(nil), "no result is not this signature")
}

// A run that exits 0 on the empty terminal turn is flagged, at quiet too,
// and the warning carries none of the stream's text.
func TestClaudeRuntime_Run_WarnsOnEmptyTerminalTurn(t *testing.T) {
	for _, verbosity := range []string{config.ProgressVerbosityFull, config.ProgressVerbosityQuiet} {
		t.Run(verbosity, func(t *testing.T) {
			fakeOpenshellClaude(t, filepath.Join(t.TempDir(), "openshell.log"), map[string]string{
				"claude-opus-5-5": claudeEmptyTerminalTurnStream,
			})
			var out bytes.Buffer
			var metrics RunMetrics
			exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
				SandboxName: "fs-claude-empty-turn", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
				Timeout: 30 * time.Second, ProgressVerbosity: verbosity, OnEvent: func(AgentEvent) {},
			}, ui.New(&out), time.Now(), &metrics)
			require.NoError(t, err)
			assert.Equal(t, 0, exit)
			assert.Contains(t, out.String(), claudeEmptyTerminalTurnWarning)
			assert.NotContains(t, out.String(), "PRIVATE-TOOL-OUTPUT")
		})
	}
}

// A normal run whose final assistant message has text is not flagged.
func TestClaudeRuntime_Run_NoWarningOnNormalEnd(t *testing.T) {
	fakeOpenshellClaude(t, filepath.Join(t.TempDir(), "openshell.log"), map[string]string{
		"claude-opus-5-5": claudeServedStream,
	})
	var out bytes.Buffer
	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-normal-end", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(&out), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.NotContains(t, out.String(), claudeEmptyTerminalTurnWarning)
}
