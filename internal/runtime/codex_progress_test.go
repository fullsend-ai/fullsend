package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexFixturePath(name string) string {
	return filepath.Join("testdata", "codex", name)
}

func readCodexFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(codexFixturePath(name))
	require.NoError(t, err)
	return data
}

// collectCodexEvents parses a fixture and returns every emitted event plus the
// thread id. parseCodexStream must not error on any fixture: a malformed or
// truncated line is data, not a read failure.
func collectCodexEvents(t *testing.T, name string) ([]AgentEvent, string) {
	t.Helper()
	f, err := os.Open(codexFixturePath(name))
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })

	var events []AgentEvent
	threadID, err := parseCodexStream(f, func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	return events, threadID
}

// codexEventsOfType filters the collected events down to one concrete type.
func codexEventsOfType[T AgentEvent](events []AgentEvent) []T {
	var out []T
	for _, evt := range events {
		if e, ok := evt.(T); ok {
			out = append(out, e)
		}
	}
	return out
}

func codexOnlyResult(t *testing.T, events []AgentEvent) ResultEvent {
	t.Helper()
	results := codexEventsOfType[ResultEvent](events)
	require.Len(t, results, 1, "exactly one ResultEvent per stream")
	return results[0]
}

// --- the live capture -------------------------------------------------------

func TestParseCodexStream_BasicRun(t *testing.T) {
	t.Parallel()

	events, threadID := collectCodexEvents(t, "basic_run.ndjson")

	// Asserted by shape, not value: regen.sh produces a new id every time
	// and re-capturing the fixture must not require editing this test.
	assert.Regexp(t, `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
		threadID, "thread.started carries a UUID/ULID-shaped thread id")

	// No InitEvent: the stream carries neither a model nor a CLI version.
	assert.Empty(t, codexEventsOfType[InitEvent](events),
		"codex exec --json has no model or version on the wire")

	thinking := codexEventsOfType[ThinkingEvent](events)
	require.Len(t, thinking, 1)
	assert.Contains(t, thinking[0].Text, "Executing commands")

	texts := codexEventsOfType[TextEvent](events)
	require.Len(t, texts, 2)
	assert.Contains(t, texts[0].Text, "list the workspace")
	assert.Equal(t, "done", texts[1].Text)

	// The capture has item.started *and* item.completed for both the command
	// and the file change; only the completions are reported.
	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 2)
	assert.Equal(t, "Bash", tools[0].Name)
	assert.Equal(t, "$ /bin/zsh -lc 'ls .'", tools[0].Summary,
		"a successful command shows the command, never its output")
	assert.Equal(t, "Write", tools[1].Name, "kind=add maps to Write")
	assert.Equal(t, "/sandbox/workspace/repo/hello.txt", tools[1].Summary)

	// codex nests its categories: input_tokens 41320 includes the 27386
	// cached and 13925 cache-write tokens, and output_tokens 295 includes
	// the 74 reasoning tokens. The normalized counters are disjoint, so the
	// five sum to the 41615 tokens the thread actually used rather than to
	// ~83000.
	tokens := codexEventsOfType[TokensEvent](events)
	require.Len(t, tokens, 1)
	assert.Equal(t, 9, tokens[0].InputTokens, "41320 - 27386 - 13925")
	assert.Equal(t, 221, tokens[0].OutputTokens, "295 - 74")
	assert.Equal(t, 74, tokens[0].ReasoningTokens)
	assert.Equal(t, 27386, tokens[0].CacheRead)
	assert.Equal(t, 13925, tokens[0].CacheWrite)
	assert.Equal(t, 41615,
		tokens[0].InputTokens+tokens[0].OutputTokens+tokens[0].ReasoningTokens+
			tokens[0].CacheRead+tokens[0].CacheWrite,
		"the renderer sums all five, so they must not overlap")

	result := codexOnlyResult(t, events)
	assert.False(t, result.IsError)
	assert.Empty(t, result.Subtype)
	assert.Equal(t, 1, result.NumTurns)
	assert.Zero(t, result.TotalCostUSD, "codex reports no cost")
	assert.Equal(t, 9, result.InputTokens)
	assert.Equal(t, 221, result.OutputTokens)
	assert.Equal(t, 74, result.ReasoningTokens)
	assert.Equal(t, 13925, result.CacheCreationInputTokens)
	assert.Equal(t, 27386, result.CacheReadInputTokens)
}

// --- fixture table ----------------------------------------------------------

func TestParseCodexStream_Fixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fixture     string
		threadID    string
		wantErr     bool
		subtype     string
		errContains string
		numTurns    int
		toolNames   []string
	}{
		{
			name:        "turn.failed is a failed run",
			fixture:     "turn_failed.ndjson",
			threadID:    "01a06300-0000-7000-8000-000000000001",
			wantErr:     true,
			subtype:     codexSubtypeFailed,
			errContains: "exceeded retry limit",
			numTurns:    1,
			toolNames:   []string{"Bash"},
		},
		{
			name:      "warnings and a top-level error before a completed turn are not failures",
			fixture:   "error_event.ndjson",
			threadID:  "01a06300-0000-7000-8000-000000000002",
			wantErr:   false,
			numTurns:  1,
			toolNames: nil,
		},
		{
			name:        "a top-level error with no terminal event is incomplete",
			fixture:     "critical_error_only.ndjson",
			threadID:    "01a06300-0000-7000-8000-000000000003",
			wantErr:     true,
			subtype:     codexSubtypeIncomplete,
			errContains: "401 Unauthorized",
			numTurns:    0,
		},
		{
			name:     "every tool item kind",
			fixture:  "mcp_and_file_change.ndjson",
			threadID: "01a06300-0000-7000-8000-000000000004",
			numTurns: 1,
			toolNames: []string{
				"Write", "Edit", "Edit", // add, update, delete
				"Edit",                   // the failed patch
				"mcp__github__get_issue", // succeeded
				"mcp__jira__search",      // errored
				"WebSearch",
				"Agent",
				"Bash", // declined
				"Bash", // completed
			},
		},
		{
			name:      "malformed and empty lines are skipped",
			fixture:   "malformed_line.ndjson",
			threadID:  "01a06300-0000-7000-8000-000000000006",
			numTurns:  1,
			toolNames: []string{"Bash"},
		},
		{
			name:      "a truncated stream yields what it has",
			fixture:   "truncated.ndjson",
			threadID:  "01a06300-0000-7000-8000-000000000007",
			wantErr:   true,
			subtype:   codexSubtypeIncomplete,
			numTurns:  0,
			toolNames: []string{"Bash"},
		},
		{
			name:      "a second turn that never finishes is not the first turn's success",
			fixture:   "second_turn_unfinished.ndjson",
			threadID:  "01a06300-0000-7000-8000-000000000008",
			wantErr:   true,
			subtype:   codexSubtypeIncomplete,
			numTurns:  1,
			toolNames: []string{"Bash"},
		},
		{
			name:      "unknown top-level and item types are skipped",
			fixture:   "unknown_types.ndjson",
			threadID:  "01a06300-0000-7000-8000-000000000005",
			numTurns:  1,
			toolNames: []string{"Bash"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, threadID := collectCodexEvents(t, tt.fixture)
			assert.Equal(t, tt.threadID, threadID)

			var names []string
			for _, tool := range codexEventsOfType[ToolUseEvent](events) {
				names = append(names, tool.Name)
			}
			assert.Equal(t, tt.toolNames, names)

			result := codexOnlyResult(t, events)
			assert.Equal(t, tt.wantErr, result.IsError)
			assert.Equal(t, tt.subtype, result.Subtype)
			assert.Equal(t, tt.numTurns, result.NumTurns)
			if tt.errContains != "" {
				assert.Contains(t, result.ErrorMessage, tt.errContains)
			}
		})
	}
}

// --- per-item mapping -------------------------------------------------------

func TestParseCodexStream_ToolSummaries(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "mcp_and_file_change.ndjson")
	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 10)

	assert.Equal(t, "/sandbox/workspace/repo/new.go", tools[0].Summary)
	assert.Equal(t, "/sandbox/workspace/repo/main.go", tools[1].Summary)
	assert.Equal(t, "/sandbox/workspace/repo/old.go", tools[2].Summary,
		"a delete is still an Edit of an existing path")
	assert.Equal(t, "/sandbox/workspace/repo/locked.go (failed)", tools[3].Summary)

	assert.Empty(t, tools[4].Summary, "a successful MCP call surfaces no payload")
	assert.Equal(t, "server not reachable", tools[5].Summary)

	assert.Equal(t, "codex exec json event schema", tools[6].Summary)
	assert.Equal(t, "spawn_agent (2 agent(s))", tools[7].Summary)

	assert.Equal(t, "$ rm -rf / (blocked)", tools[8].Summary,
		"a declined command was refused before it ran, not a failure")
	assert.Equal(t, "$ go test ./... -run TestCodex", tools[9].Summary,
		"multi-line commands are collapsed and successful output is not shown")

	// The reasoning item became a ThinkingEvent, the todo_list nothing.
	thinking := codexEventsOfType[ThinkingEvent](events)
	require.Len(t, thinking, 1)
	assert.Contains(t, thinking[0].Text, "Planning")
}

func TestParseCodexStream_FailedCommandShowsOutputTail(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "turn_failed.ndjson")
	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 1)
	assert.Equal(t, "Bash", tools[0].Name)
	assert.Contains(t, tools[0].Summary, "$ make build")
	assert.Contains(t, tools[0].Summary, "exit 1")
	assert.Contains(t, tools[0].Summary, "go: build failed",
		"a failed command surfaces its output so the failure is diagnosable")
}

func TestParseCodexStream_ErrorItemsAreNotFatal(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "error_event.ndjson")

	// The renderer prints every ErrorEvent as a failure line, so a run that
	// succeeded must emit none — neither the warning item nor the
	// non-terminal top-level error may paint it red.
	assert.Empty(t, codexEventsOfType[ErrorEvent](events),
		"a successful run emits no ErrorEvent")

	result := codexOnlyResult(t, events)
	assert.False(t, result.IsError, "turn.completed decides the verdict, not the errors before it")
	assert.Empty(t, result.ErrorMessage)

	// A failed run does emit one, so a real failure is still visible.
	failed, _ := collectCodexEvents(t, "turn_failed.ndjson")
	errs := codexEventsOfType[ErrorEvent](failed)
	require.Len(t, errs, 1)
	assert.Equal(t, codexSubtypeFailed, errs[0].ErrorType)
	assert.Contains(t, errs[0].Message, "exceeded retry limit")
}

func TestParseCodexStream_LastTerminalEventWins(t *testing.T) {
	t.Parallel()

	// Both directions of the documented rule: whichever terminal event
	// arrives last decides, because each describes its own turn.
	completedThenFailed := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0}}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.failed","error":{"message":"second turn failed"}}`,
	}, "\n")
	failedThenCompleted := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.failed","error":{"message":"first turn failed"}}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0}}`,
	}, "\n")

	collect := func(stream string) ResultEvent {
		var events []AgentEvent
		_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
			events = append(events, evt)
		})
		require.NoError(t, err)
		return codexOnlyResult(t, events)
	}

	first := collect(completedThenFailed)
	assert.True(t, first.IsError)
	assert.Equal(t, codexSubtypeFailed, first.Subtype)
	assert.Contains(t, first.ErrorMessage, "second turn failed")
	assert.Equal(t, 2, first.NumTurns, "a failed turn counts as a turn")

	second := collect(failedThenCompleted)
	assert.False(t, second.IsError, "the later completed turn stands")
	assert.Empty(t, second.Subtype)
	assert.Empty(t, second.ErrorMessage)
	assert.Equal(t, 2, second.NumTurns)
}

func TestParseCodexStream_SnapshotBaselineNeverDrops(t *testing.T) {
	t.Parallel()

	// A snapshot that reports less than the one before it must not lower the
	// baseline: if it did, the next increase would be measured from the
	// smaller value and counted twice. 500 -> 300 -> 500 is one thread that
	// used 500, not 700.
	snapshot := func(in int) string {
		return fmt.Sprintf(
			`{"type":"turn.completed","usage":{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0}}`, in)
	}
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		snapshot(500),
		`{"type":"turn.started"}`,
		snapshot(300),
		`{"type":"turn.started"}`,
		snapshot(500),
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	tokens := codexEventsOfType[TokensEvent](events)
	require.Len(t, tokens, 3)
	assert.Equal(t, 500, tokens[0].InputTokens)
	assert.Equal(t, 0, tokens[1].InputTokens, "a smaller snapshot adds nothing")
	assert.Equal(t, 0, tokens[2].InputTokens, "and does not let the recovery be counted again")

	total := 0
	for _, tk := range tokens {
		total += tk.InputTokens
	}
	assert.Equal(t, 500, total, "the deltas sum to the thread's usage, not 700")

	result := codexOnlyResult(t, events)
	assert.Equal(t, 500, result.InputTokens, "the high-water mark, not the last snapshot")
}

func TestParseCodexStream_FailedFileChangeWithoutPaths(t *testing.T) {
	t.Parallel()

	// A patch that failed before it could name a file still happened.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"file_change","changes":[],"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"file_change","changes":[],"status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 1, "only the failed one is reported; an empty successful patch is nothing")
	assert.Equal(t, "Edit", tools[0].Name)
	assert.Equal(t, "(failed)", tools[0].Summary)
}

func TestParseCodexStream_UsageIsCumulativeNotSummed(t *testing.T) {
	t.Parallel()

	// turn.completed carries the thread's running total (usage_from_last_total
	// in event_processor_with_jsonl_output.rs), so two turns must report the
	// second value, not their sum — while each TokensEvent shows the delta.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"cache_write_input_tokens":5,"output_tokens":20,"reasoning_output_tokens":2}}`,
		`{"type":"turn.completed","usage":{"input_tokens":250,"cached_input_tokens":40,"cache_write_input_tokens":5,"output_tokens":70,"reasoning_output_tokens":9}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	// Snapshots are nested as well as cumulative: turn 1 is
	// 100 - 10 - 5 = 85 uncached input and 20 - 2 = 18 non-reasoning output;
	// turn 2 is 250 - 40 - 5 = 205 and 70 - 9 = 61.
	tokens := codexEventsOfType[TokensEvent](events)
	require.Len(t, tokens, 2)
	assert.Equal(t, 85, tokens[0].InputTokens)
	assert.Equal(t, 120, tokens[1].InputTokens, "205 - 85: the second turn's delta")
	assert.Equal(t, 43, tokens[1].OutputTokens, "61 - 18")
	assert.Equal(t, 30, tokens[1].CacheRead)
	assert.Equal(t, 0, tokens[1].CacheWrite)

	result := codexOnlyResult(t, events)
	assert.Equal(t, 2, result.NumTurns)
	assert.Equal(t, 205, result.InputTokens, "cumulative, not summed")
	assert.Equal(t, 61, result.OutputTokens)
	assert.Equal(t, 9, result.ReasoningTokens)
	assert.Equal(t, 5, result.CacheCreationInputTokens)
	assert.Equal(t, 40, result.CacheReadInputTokens)
}

func TestParseCodexStream_TurnFailedFallsBackToCriticalError(t *testing.T) {
	t.Parallel()

	// The processor reuses last_critical_error when the failed turn carries no
	// error of its own; an empty message on the wire must not lose the reason.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"error","message":"upstream connect error"}`,
		`{"type":"turn.failed","error":{"message":""}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	result := codexOnlyResult(t, events)
	assert.True(t, result.IsError)
	assert.Equal(t, codexSubtypeFailed, result.Subtype)
	assert.Equal(t, "upstream connect error", result.ErrorMessage)
}

func TestParseCodexStream_NoTerminalEventIsIncomplete(t *testing.T) {
	t.Parallel()

	// An interrupted turn emits neither turn.completed nor turn.failed, and
	// codex exec can still exit 0, so the absence of a terminal event has to
	// fail the run on its own.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"working"}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	result := codexOnlyResult(t, events)
	assert.True(t, result.IsError)
	assert.Equal(t, codexSubtypeIncomplete, result.Subtype)
	assert.Empty(t, result.ErrorMessage, "nothing explained the truncation")
}

func TestParseCodexStream_TurnStartedReopensTheVerdict(t *testing.T) {
	t.Parallel()

	// turn.completed decides the turn it ends, not the run: a turn that
	// starts afterwards and never finishes must reopen the verdict, or a
	// stream killed mid-second-turn reports the first turn's success.
	events, _ := collectCodexEvents(t, "second_turn_unfinished.ndjson")

	result := codexOnlyResult(t, events)
	assert.True(t, result.IsError)
	assert.Equal(t, codexSubtypeIncomplete, result.Subtype)
	assert.Equal(t, 1, result.NumTurns, "only the first turn completed")
	// Usage from the completed turn is still reported, normalized:
	// 800 - 200 cached - 100 cache-write.
	assert.Equal(t, 500, result.InputTokens)

	// A failed turn followed by a new turn is reopened the same way.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.failed","error":{"message":"first turn blew up"}}`,
		`{"type":"turn.started"}`,
	}, "\n")
	var reopened []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		reopened = append(reopened, evt)
	})
	require.NoError(t, err)
	got := codexOnlyResult(t, reopened)
	assert.True(t, got.IsError)
	assert.Equal(t, codexSubtypeIncomplete, got.Subtype,
		"the second turn never finished, so the run is incomplete rather than turn_failed")
	assert.Empty(t, got.ErrorMessage, "the first turn's message described that turn")
}

func TestParseCodexStream_ReadErrorStillEmitsResult(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	reader := iotest.ErrReader(wantErr)

	var events []AgentEvent
	threadID, err := parseCodexStream(reader, func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, threadID)

	result := codexOnlyResult(t, events)
	assert.True(t, result.IsError, "a lost stream is not a successful run")
	assert.Equal(t, codexSubtypeIncomplete, result.Subtype)
}

func TestParseCodexStream_OverlongLineIsSkipped(t *testing.T) {
	t.Parallel()

	huge := `{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"` +
		strings.Repeat("x", streamBufSize+1024) + `"}}`
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		huge,
		`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"after"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	texts := codexEventsOfType[TextEvent](events)
	require.Len(t, texts, 1, "the oversized line is dropped, the stream continues")
	assert.Equal(t, "after", texts[0].Text)
	assert.False(t, codexOnlyResult(t, events).IsError)
}

// --- redaction --------------------------------------------------------------

func TestCodexSummaries_RedactSecrets(t *testing.T) {
	t.Parallel()

	// A token in the command, in a failed command's output, and in a search
	// query must not reach the renderer or the CI annotations.
	token := "ghp_" + strings.Repeat("a", 36)
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"curl -H 'Authorization: Bearer ` + token +
			`' https://api.example","aggregated_output":"denied for ` + token + `","exit_code":22,"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"web_search","query":"` + token + `","action":{"type":"search"}}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	for _, tool := range codexEventsOfType[ToolUseEvent](events) {
		assert.NotContains(t, tool.Summary, token, "tool summary leaked a secret: %s", tool.Name)
	}
}

func TestCodexOutputTail(t *testing.T) {
	t.Parallel()

	assert.Empty(t, codexOutputTail(""))
	assert.Equal(t, "hello", codexOutputTail("  hello\n"))

	long := strings.Repeat("a", codexOutputTailMax) + "TAIL"
	got := codexOutputTail(long)
	assert.True(t, strings.HasPrefix(got, "…"), "the head is dropped, not the tail")
	assert.True(t, strings.HasSuffix(got, "TAIL"))

	assert.Equal(t, "(output too large to summarize)",
		codexOutputTail(strings.Repeat("z", codexOutputScanMax+1)),
		"an unbounded output is dropped rather than cut before redaction")
}

func TestCodexCommandSummary_Branches(t *testing.T) {
	t.Parallel()

	exit := func(n int) *int { return &n }

	tests := []struct {
		name string
		item codexCommandExecutionItem
		want string
	}{
		{
			name: "completed with a non-zero exit still reports it",
			item: codexCommandExecutionItem{
				Command: "grep -q needle file", AggregatedOutput: "", ExitCode: exit(1), Status: "completed",
			},
			want: "$ grep -q needle file (exit 1)",
		},
		{
			name: "failed without an exit code says so",
			item: codexCommandExecutionItem{
				Command: "timeout 1 sleep 5", AggregatedOutput: "killed\n", ExitCode: nil, Status: "failed",
			},
			want: "$ timeout 1 sleep 5 (failed: killed)",
		},
		{
			name: "failed with an exit code keeps the failure visible",
			item: codexCommandExecutionItem{
				Command: "make build", AggregatedOutput: "boom\n", ExitCode: exit(1), Status: "failed",
			},
			want: "$ make build (failed (exit 1): boom)",
		},
		{
			// A failed item that reports exit 0 must not read as a success.
			name: "failed with exit 0 still says failed",
			item: codexCommandExecutionItem{
				Command: "flaky", AggregatedOutput: "", ExitCode: exit(0), Status: "failed",
			},
			want: "$ flaky (failed (exit 0))",
		},
		{
			name: "an empty command still carries its outcome",
			item: codexCommandExecutionItem{Command: "", ExitCode: nil, Status: "declined"},
			want: "blocked",
		},
		{
			name: "an unrecognized status is passed through",
			item: codexCommandExecutionItem{Command: "ls", Status: "abandoned"},
			want: "$ ls (abandoned)",
		},
		{
			name: "a still-running item reports the command alone",
			item: codexCommandExecutionItem{Command: "ls", Status: "in_progress"},
			want: "$ ls",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, codexCommandSummary(tt.item))
		})
	}
}

func TestCodexSummaries_AreCapped(t *testing.T) {
	t.Parallel()

	longPath := "/sandbox/workspace/repo/" + strings.Repeat("d/", maxPathDisplay) + "f.go"
	capped := codexCapPath(longPath)
	assert.True(t, strings.HasSuffix(capped, "…"))
	assert.Equal(t, maxPathDisplay+1, len([]rune(capped)), "one ellipsis rune, as claude and pi use")

	longQuery := strings.Repeat("q", maxPatternDisplay+40)
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"web_search","query":"` + longQuery + `","action":{"type":"search"}}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"mcp_tool_call","server":"jira","tool":"search","arguments":{},"result":null,"error":null,"status":"failed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 2)
	assert.Equal(t, "WebSearch", tools[0].Name)
	assert.Equal(t, maxPatternDisplay+1, len([]rune(tools[0].Summary)))
	assert.True(t, strings.HasSuffix(tools[0].Summary, "…"))

	// A failed MCP call with no error object still reports the failure.
	assert.Equal(t, "mcp__jira__search", tools[1].Name)
	assert.Equal(t, "failed", tools[1].Summary)
}

func TestCodexMcpToolName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "mcp__github__get_issue", codexMcpToolName("github", "get_issue"))
	assert.Equal(t, "mcp__github", codexMcpToolName("github", ""))
	assert.Equal(t, "mcp__search", codexMcpToolName("", "search"))
	assert.Equal(t, "mcp", codexMcpToolName("", ""))

	// Names come off the wire and reach CI annotations; they are redacted.
	token := "ghp_" + strings.Repeat("b", 36)
	assert.NotContains(t, codexMcpToolName(token, "search"), token)
}

func TestCodexFileChangeTool(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Write", codexFileChangeTool("add"))
	assert.Equal(t, "Edit", codexFileChangeTool("update"))
	assert.Equal(t, "Edit", codexFileChangeTool("delete"))
	assert.Equal(t, "Edit", codexFileChangeTool("rename_someday"))
}

// --- metrics ----------------------------------------------------------------

func TestApplyCodexMetrics(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "basic_run.ndjson")

	var metrics RunMetrics
	for _, evt := range events {
		applyCodexMetrics(&metrics, evt)
	}

	assert.Equal(t, 1, metrics.NumTurns)
	assert.Equal(t, 9, metrics.InputTokens)
	assert.Equal(t, 221, metrics.OutputTokens)
	assert.Equal(t, 74, metrics.ReasoningTokens)
	assert.Equal(t, 13925, metrics.CacheCreationInputTokens)
	assert.Equal(t, 27386, metrics.CacheReadInputTokens)
	assert.Equal(t, int32(2), metrics.ToolCalls.Load())

	// Left for the runner: the stream carries neither.
	assert.Zero(t, metrics.TotalCostUSD)
	assert.Empty(t, metrics.Model)

	assert.NotPanics(t, func() { applyCodexMetrics(nil, ResultEvent{}) })
}

func TestApplyCodexMetrics_CountsEveryFileChange(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "mcp_and_file_change.ndjson")

	var metrics RunMetrics
	for _, evt := range events {
		applyCodexMetrics(&metrics, evt)
	}
	// One tool call per change in a file_change item, plus the MCP, search,
	// collab and command items.
	assert.Equal(t, int32(10), metrics.ToolCalls.Load())
}

// --- capture detection and verdict ------------------------------------------

func TestIsCodexStreamCapture(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"basic_run.ndjson", "turn_failed.ndjson", "error_event.ndjson",
		"critical_error_only.ndjson", "mcp_and_file_change.ndjson",
		"malformed_line.ndjson", "truncated.ndjson", "unknown_types.ndjson",
	} {
		assert.True(t, isCodexStreamCapture(readCodexFixture(t, name)), "fixture %s", name)
	}

	assert.False(t, isCodexStreamCapture(nil))
	assert.False(t, isCodexStreamCapture([]byte(`{"type":"session","id":"ses_1","version":3}`)),
		"a pi capture is not a codex capture")

	// codex's own rollout session files use underscored inner names inside
	// session_meta/response_item/event_msg envelopes, so they must not match.
	rollout := `{"timestamp":"2026-09-02T11:58:53Z","type":"session_meta","payload":{"id":"t1"}}` + "\n" +
		`{"timestamp":"2026-09-02T11:58:54Z","type":"event_msg","payload":{"type":"item_completed"}}`
	assert.False(t, isCodexStreamCapture([]byte(rollout)),
		"rollout transcripts must not be mistaken for a --json capture")

	// A real item.updated line is a codex capture even when its text quotes
	// another event name — the top-level type is what decides.
	quoted := `{"type":"item.updated","item":{"id":"i0","type":"agent_message","text":"the type is \"turn.completed\""}}`
	assert.True(t, isCodexStreamCapture([]byte(quoted)))

	// The same quoted text in a line that is not a codex event does not match,
	// which the old substring scan could not distinguish.
	assert.False(t, isCodexStreamCapture(
		[]byte(`{"type":"log","message":"the type is \"turn.completed\""}`)))

	// Detection is structural, so a codex event name nested under another
	// envelope's key is not a codex capture either.
	assert.False(t, isCodexStreamCapture([]byte(`{"payload":{"type":"turn.completed"},"type":"event_msg"}`)),
		"a nested type must not be mistaken for a top-level event")
	assert.False(t, isCodexStreamCapture([]byte(`{"type":"wrapper","inner":{"type":"thread.started"}}`)))

	// A top-level "error" alone is too generic to identify a codex stream.
	assert.False(t, isCodexStreamCapture([]byte(`{"type":"error","message":"something else entirely"}`)),
		"error is a plausible type in unrelated JSONL")
	assert.True(t, isCodexStreamCapture([]byte(
		`{"type":"error","message":"x"}`+"\n"+`{"type":"turn.failed","error":{"message":"y"}}`)),
		"an error line alongside a codex event still identifies the stream")

	// A leading malformed line does not hide the stream.
	assert.True(t, isCodexStreamCapture([]byte("garbage\n"+`{"type":"thread.started","thread_id":"t1"}`)))

	// A hand-reformatted capture with spaces after the colon still matches.
	assert.True(t, isCodexStreamCapture([]byte(`{"type": "turn.completed", "usage": {}}`)))
}

func TestCodexStreamVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fixture     string
		wantErr     bool
		subtype     string
		errContains string
	}{
		{fixture: "basic_run.ndjson"},
		{fixture: "error_event.ndjson"},
		{fixture: "mcp_and_file_change.ndjson"},
		{fixture: "malformed_line.ndjson"},
		{fixture: "unknown_types.ndjson"},
		{fixture: "turn_failed.ndjson", wantErr: true, subtype: codexSubtypeFailed, errContains: "429"},
		{
			fixture: "critical_error_only.ndjson", wantErr: true,
			subtype: codexSubtypeIncomplete, errContains: "401 Unauthorized",
		},
		{fixture: "truncated.ndjson", wantErr: true, subtype: codexSubtypeIncomplete},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()

			te, ok := codexStreamVerdict(readCodexFixture(t, tt.fixture), tt.fixture)
			require.True(t, ok)
			assert.Equal(t, tt.fixture, te.Source)
			assert.Equal(t, tt.wantErr, te.IsError)
			assert.Equal(t, tt.subtype, te.Subtype)
			if tt.errContains != "" {
				assert.Contains(t, te.ErrorMessage, tt.errContains)
			}

			// The path form must agree with the bytes form.
			fromPath, okPath := parseCodexTranscriptFile(codexFixturePath(tt.fixture))
			require.True(t, okPath)
			assert.Equal(t, te, fromPath)
		})
	}
}

func TestCodexStreamVerdict_RejectsNonCaptures(t *testing.T) {
	t.Parallel()

	_, ok := codexStreamVerdict([]byte("not a capture\n"), "x.ndjson")
	assert.False(t, ok)

	_, ok = parseCodexTranscriptFile(filepath.Join("testdata", "codex", "does-not-exist.ndjson"))
	assert.False(t, ok, "a missing file is not a verdict")
}

func TestCodexStreamVerdict_BoundsTheErrorMessage(t *testing.T) {
	t.Parallel()

	// piSummarize caps the message inside the parser and truncateError caps it
	// again on the way out; a runaway error must not flood the annotation.
	long := strings.Repeat("e", maxTranscriptErrorLength*2)
	stream := `{"type":"thread.started","thread_id":"t1"}` + "\n" +
		`{"type":"turn.failed","error":{"message":"` + long + `"}}`

	te, ok := codexStreamVerdict([]byte(stream), "output.jsonl")
	require.True(t, ok)
	assert.True(t, te.IsError)
	assert.LessOrEqual(t, len(te.ErrorMessage), piSummaryMax)
}
