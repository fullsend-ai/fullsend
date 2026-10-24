package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// delegating reads the runner-held role set only, never the manifest.
func TestCodexRunnerHeldDigestSet_Delegating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		roles map[string]string
		want  bool
	}{
		{name: "nil map: no role registered", roles: nil, want: false},
		{name: "empty map: no role registered", roles: map[string]string{}, want: false},
		{name: "one role registered", roles: map[string]string{"default": "d0d0"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := codexRunnerHeldDigestSet{ConfigTOML: "cfg", RoleFiles: tt.roles}
			assert.Equal(t, tt.want, d.delegating(), "delegating follows the runner-held role set only")
		})
	}
}

// Thread ids of the fixtures (testdata/codex/README.md, "Child rollout
// fixtures") and of the spawn items in collab_spawn.ndjson.
const (
	codexChildSameModelID   = "01a06300-0000-7000-8000-0000000000a1"
	codexChildOtherModelID  = "01a06300-0000-7000-8000-0000000000a2"
	codexChildFailedID      = "01a06300-0000-7000-8000-0000000000a3"
	codexChildNoModelID     = "01a06300-0000-7000-8000-0000000000a4"
	codexChildForkedID      = "01a06300-0000-7000-8000-0000000000a5"
	codexChildFailedSpawnID = "01a06300-0000-7000-8000-0000000000a9"
	codexRootThreadID       = "01a06300-0000-7000-8000-000000000050"
)

// codexChildFixtures maps each child id to its rollout fixture.
var codexChildFixtures = map[string]string{
	codexChildSameModelID:  "child_usage_same_model.jsonl",
	codexChildOtherModelID: "child_usage_other_model.jsonl",
	codexChildFailedID:     "child_usage_failed.jsonl",
	codexChildNoModelID:    "child_usage_no_model.jsonl",
	codexChildForkedID:     "child_usage_forked.jsonl",
}

// writeCodexChildRollouts lays the child fixtures out as codex would under a
// sessions directory (YYYY/MM/DD/rollout-<start>-<thread id>.jsonl) and
// returns that directory.
func writeCodexChildRollouts(t *testing.T, ids ...string) string {
	t.Helper()
	sessions := filepath.Join(t.TempDir(), "sessions")
	day := filepath.Join(sessions, "2026", "09", "29")
	require.NoError(t, os.MkdirAll(day, 0o755))
	for _, id := range ids {
		name := "rollout-2026-09-29T15-46-38-" + id + ".jsonl"
		require.NoError(t, os.WriteFile(filepath.Join(day, name), readCodexFixture(t, codexChildFixtures[id]), 0o644))
	}
	return sessions
}

// runCodexChildUsageRead runs the read command under /bin/sh, so a quoting
// slip fails the test, and parses what it printed.
func runCodexChildUsageRead(t *testing.T, sessions string, ids []string) []codexChildUsage {
	t.Helper()
	cmd, err := codexChildUsageReadCommand(sessions, ids)
	require.NoError(t, err)
	out, err := exec.Command("/bin/sh", "-c", cmd).Output()
	require.NoError(t, err)
	return parseCodexChildUsage(out)
}

func TestCodexChildUsageReadCommand_RejectsBadIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
	}{
		{name: "path segment", id: "../etc/passwd"},
		{name: "upper case", id: "01A06300-0000-7000-8000-0000000000A1"},
		{name: "too short", id: "01a06300-0000-7000-8000-00000000a1"},
		{name: "shell text", id: "'; rm -rf /; '"},
		{name: "empty", id: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := codexChildUsageReadCommand("/sandbox/codex-config/sessions", []string{codexChildSameModelID, tt.id})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not a codex thread id")
		})
	}
}

func TestCodexChildUsageReadCommand_Executes(t *testing.T) {
	t.Parallel()

	sessions := writeCodexChildRollouts(t, codexChildSameModelID, codexChildOtherModelID, codexChildFailedID)
	// Unsorted, one id twice, one id with no rollout.
	ids := []string{codexChildFailedID, codexChildSameModelID, codexChildSameModelID, codexChildOtherModelID, "01a06300-0000-7000-8000-0000000000ff"}

	cmd, err := codexChildUsageReadCommand(sessions, ids)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(cmd, fmt.Sprintf("| command -p head -c %d", codexChildUsageMaxBytes)),
		"the read is bounded inside the sandbox, not after the bytes arrived")
	assert.Contains(t, cmd, `-type f -name "rollout-*-$id.jsonl"`,
		"only regular rollout files, found by thread id; never a cat of the directory")
	assert.Equal(t, 1, strings.Count(cmd, shellQuote(codexChildSameModelID)), "a repeated id is read once")

	got := runCodexChildUsageRead(t, sessions, ids)
	require.Len(t, got, 3, "an id with no rollout yields no record; a repeated id one; sorted by id")
	assert.Equal(t, codexChildUsage{
		ID: codexChildSameModelID, Model: "gpt-5.6-luna", HasUsage: true,
		Usage: codexUsage{InputTokens: 7381, CacheWriteInputTokens: 7378, OutputTokens: 8},
	}, got[0], "the thread total of the record, not the sum of the two token_counts")
	assert.Equal(t, codexChildUsage{
		ID: codexChildOtherModelID, Model: "gpt-5.6-terra", HasUsage: true,
		Usage: codexUsage{InputTokens: 4000, CachedInputTokens: 1000, CacheWriteInputTokens: 2000, OutputTokens: 300, ReasoningOutputTokens: 100},
	}, got[1])
	assert.Equal(t, codexChildUsage{ID: codexChildFailedID, Model: "gpt-5.6-nonexistent"}, got[2],
		"a child that errored before its first response has a model and no usage")
}

// The read takes the last record's thread total; a token_count is zeroed
// after a context-window overflow and empty before the first response.
func TestCodexChildUsageReadCommand_ReadsTheThreadTotal(t *testing.T) {
	t.Parallel()

	usage := func(in, out, total int) string {
		return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}`, in, out, total)
	}
	count := func(info string) string {
		return `{"type":"event_msg","payload":{"type":"token_count","info":` + info + `}}`
	}
	record := func(in, out int) string {
		return `{"type":"token_usage_record","payload":{"thread_token_usage":` + usage(in, out, in+out) + `}}`
	}
	rollout := strings.Join([]string{
		`{"type":"turn_context","payload":{"model":"gpt-5.6-luna"}}`,
		count(`null`),
		record(3000, 4),
		count(`{"total_token_usage":` + usage(3000, 4, 3004) + `}`),
		record(90000, 20),
		count(`{"total_token_usage":` + usage(0, 0, 258400) + `,"last_token_usage":` + usage(0, 0, 168380) + `,"model_context_window":258400}`),
	}, "\n") + "\n"
	sessions := writeCodexChildRollouts(t)
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "2026", "09", "29", "rollout-2026-09-29T15-46-38-"+codexChildSameModelID+".jsonl"), []byte(rollout), 0o644))

	got := runCodexChildUsageRead(t, sessions, []string{codexChildSameModelID})
	require.Len(t, got, 1)
	assert.Equal(t, codexChildUsage{
		ID: codexChildSameModelID, Model: "gpt-5.6-luna", HasUsage: true,
		Usage: codexUsage{InputTokens: 90000, OutputTokens: 20},
	}, got[0], "the last record's thread total, not the zeroed token_count after it")
}

// A codex killed mid-write leaves a torn last line; the record before it
// still counts.
func TestCodexChildUsageReadCommand_FallsBackPastATornRecord(t *testing.T) {
	t.Parallel()

	rollout := `{"type":"turn_context","payload":{"model":"gpt-5.6-luna"}}` + "\n" +
		`{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tokens":30,"output_tokens":3}}}` + "\n" +
		`{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tok`
	sessions := writeCodexChildRollouts(t)
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "2026", "09", "29", "rollout-2026-09-29T15-46-38-"+codexChildSameModelID+".jsonl"), []byte(rollout), 0o644))

	got := runCodexChildUsageRead(t, sessions, []string{codexChildSameModelID})
	require.Len(t, got, 1)
	assert.Equal(t, codexChildUsage{ID: codexChildSameModelID, Model: "gpt-5.6-luna", HasUsage: true, Usage: codexUsage{InputTokens: 30, OutputTokens: 3}, BadLine: true}, got[0])
}

// A forked child's rollout starts with the parent's turn_context lines; the
// read names the child by its own, the last.
func TestCodexChildUsageReadCommand_NamesAForkedChildByItsOwnTurn(t *testing.T) {
	t.Parallel()

	got := runCodexChildUsageRead(t, writeCodexChildRollouts(t, codexChildForkedID), []string{codexChildForkedID})
	require.Len(t, got, 1)
	assert.Equal(t, codexChildUsage{
		ID: codexChildForkedID, Model: "gpt-5.6-terra", HasUsage: true,
		Usage: codexUsage{InputTokens: 600, OutputTokens: 40, ReasoningOutputTokens: 10},
	}, got[0], "the child's model and record, not the parent's inherited turn_context or token_count")
}

// A planted file name with a newline splits into several paths; only those
// under the sessions directory are opened.
func TestCodexChildUsageReadCommand_SkipsPathsOutsideTheSessionsDirectory(t *testing.T) {
	t.Parallel()

	sessions := writeCodexChildRollouts(t)
	day := filepath.Join(sessions, "2026", "09", "29")
	require.NoError(t, os.WriteFile(filepath.Join(day, "rollout-a\nb-"+codexChildSameModelID+".jsonl"), []byte("{}\n"), 0o644))
	cwd := t.TempDir()
	record := `{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tokens":99,"output_tokens":9}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "b-"+codexChildSameModelID+".jsonl"), []byte(record), 0o644))

	cmd, err := codexChildUsageReadCommand(sessions, []string{codexChildSameModelID})
	require.NoError(t, err)
	sh := exec.Command("/bin/sh", "-c", cmd)
	sh.Dir = cwd
	out, err := sh.Output()
	require.NoError(t, err)
	got := parseCodexChildUsage(out)
	require.Len(t, got, 1)
	assert.Equal(t, codexChildUsage{ID: codexChildSameModelID, Unreadable: true}, got[0],
		"the fragment under the sessions directory is not a file, so it is reported; the one in the working directory is skipped")
}

func TestParseCodexChildUsage(t *testing.T) {
	t.Parallel()

	turn := func(model string) string {
		return `{"type":"turn_context","payload":{"model":"` + model + `"}}`
	}
	record := func(in, out int) string {
		return fmt.Sprintf(`{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}}}`, in, out, in+out)
	}
	data := strings.Join([]string{
		record(999, 999), // before any marker: nobody's line
		codexChildUsageMarker + " " + codexChildSameModelID,
		turn("gpt-5.6-terra"),
		turn("gpt-5.6-sol"),
		record(10, 1),
		"not json",
		record(20, 2),
		"",
		codexChildUsageMarker + " " + codexChildOtherModelID,
		record(5, 5),
		codexChildUsageMarker + " " + codexChildFailedID,
		turn("gpt-5.6-terra"),
		`{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tokens":7,"cach`,
	}, "\n")

	got := parseCodexChildUsage([]byte(data))
	require.Len(t, got, 3)
	assert.Equal(t, codexChildUsage{ID: codexChildSameModelID, Model: "gpt-5.6-sol", HasUsage: true, Usage: codexUsage{InputTokens: 20, OutputTokens: 2}, BadLine: true},
		got[0], "the last turn_context names the model, the last record wins, a malformed line is skipped and flagged")
	assert.Equal(t, codexChildUsage{ID: codexChildOtherModelID, HasUsage: true, Usage: codexUsage{InputTokens: 5, OutputTokens: 5}},
		got[1], "no turn_context: the model stays empty and the fold buckets it")
	assert.Equal(t, codexChildUsage{ID: codexChildFailedID, Model: "gpt-5.6-terra", BadLine: true},
		got[2], "a truncated last line (the head -c cut) is not usage")
	assert.Empty(t, parseCodexChildUsage(nil))
}

func TestCodexChildIDs_CollectsCompletedSpawns(t *testing.T) {
	t.Parallel()

	stream := readCodexFixture(t, "collab_spawn.ndjson")
	want := []string{codexChildSameModelID, codexChildOtherModelID, codexChildFailedID}

	whole := &codexChildIDs{}
	n, err := whole.Write(stream)
	require.NoError(t, err)
	assert.Equal(t, len(stream), n, "an io.Writer reports every byte consumed or the TeeReader fails")
	assert.Equal(t, want, whole.IDs,
		"the three completed spawns in stream order; the failed spawn's receiver is not a child; wait and close items name no new thread")
	assert.NotContains(t, whole.IDs, codexChildFailedSpawnID)
	assert.Equal(t, []string{codexRootThreadID, codexRootThreadID, codexRootThreadID}, whole.Senders,
		"one sender per completed spawn; the failed spawn is not counted")

	chunked := &codexChildIDs{}
	for i := 0; i < len(stream); i += 7 {
		_, err := chunked.Write(stream[i:min(i+7, len(stream))])
		require.NoError(t, err)
	}
	assert.Equal(t, want, chunked.IDs, "lines are reassembled across Write calls, as a TeeReader delivers them")
	assert.Len(t, chunked.Senders, 3)
}

func TestCodexChildIDs_SkipsOverlongLine(t *testing.T) {
	t.Parallel()

	spawn := `{"type":"item.completed","item":{"id":"item_1","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"01a06300-0000-7000-8000-000000000050","receiver_thread_ids":["` + codexChildSameModelID + `"],"prompt":null,"agents_states":{},"status":"completed"}}`
	// A completed spawn padded past the bound.
	overlong := `{"type":"item.completed","item":{"id":"item_0","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"01a06300-0000-7000-8000-000000000050","receiver_thread_ids":["` + codexChildOtherModelID + `"],"prompt":"` + strings.Repeat("a", streamBufSize) + `","agents_states":{},"status":"completed"}}`

	// A line of exactly the bound is dropped too, as parseCodexStream drops it.
	head := `{"type":"item.completed","item":{"id":"item_2","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"01a06300-0000-7000-8000-000000000050","receiver_thread_ids":["` + codexChildFailedID + `"],"prompt":"`
	tail := `","agents_states":{},"status":"completed"}}`
	exact := head + strings.Repeat("a", streamBufSize-len(head)-len(tail)) + tail
	require.Len(t, exact, streamBufSize)

	c := &codexChildIDs{}
	_, err := c.Write([]byte(overlong + "\n" + exact + "\n" + spawn + "\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{codexChildSameModelID}, c.IDs,
		"a line at or past the parser's own bound is dropped whole, as parseCodexStream drops it, even when it names a child; the next line is still scanned")
	assert.Empty(t, c.line, "nothing pending after a newline")
	assert.True(t, c.UnreadSpawn, "the dropped lines opened as spawn_agent items, so the run cannot pass on the count")
}

func TestCodexChildIDs_NotesAnUnreadSpawnItem(t *testing.T) {
	t.Parallel()

	spawn := `{"type":"item.completed","item":{"id":"item_0","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"` + codexRootThreadID + `","receiver_thread_ids":["` + codexChildOtherModelID + `"],"prompt":"` + strings.Repeat("a", streamBufSize) + `","agents_states":{},"status":"completed"}}` + "\n"
	chunked := &codexChildIDs{}
	for i := 0; i < len(spawn); i += 4096 {
		_, err := chunked.Write([]byte(spawn[i:min(i+4096, len(spawn))]))
		require.NoError(t, err)
	}
	assert.True(t, chunked.UnreadSpawn, "the item's opening is in the pending line when the bound is reached")
	assert.Empty(t, chunked.Senders)

	output := `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"cat big","aggregated_output":"` + strings.Repeat("a", streamBufSize) + `","exit_code":0,"status":"completed"}}` + "\n"
	other := &codexChildIDs{}
	_, err := other.Write([]byte(output))
	require.NoError(t, err)
	assert.False(t, other.UnreadSpawn, "an overlong line of another item kind is dropped silently, as before")
}

// A failed child shows only as failed wait and close items and never fails
// the run.
func TestParseCodexStream_FailedCollabItem(t *testing.T) {
	t.Parallel()

	events, threadID := collectCodexEvents(t, "collab_spawn.ndjson")
	assert.Equal(t, "01a06300-0000-7000-8000-000000000050", threadID)

	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 9, "four spawns, two waits, three closes")
	for _, tool := range tools {
		assert.Equal(t, "Agent", tool.Name)
	}
	assert.Equal(t, "spawn_agent (1 agent(s))", tools[0].Summary)
	assert.Equal(t, "spawn_agent (1 agent(s)) (failed)", tools[3].Summary, "a failed spawn is reported, not hidden")
	assert.Equal(t, "wait (2 agent(s))", tools[4].Summary)
	assert.Equal(t, "wait (1 agent(s)) (failed)", tools[5].Summary, "the errored child's wait carries status failed")
	assert.Equal(t, "close_agent (1 agent(s)) (failed)", tools[8].Summary)

	result := codexOnlyResult(t, events)
	assert.False(t, result.IsError, "a failed child never sets the run verdict")
	assert.Equal(t, 1, result.NumTurns)
	assert.Equal(t, 15, result.InputTokens)
	assert.Equal(t, 1140, result.OutputTokens)
	assert.Equal(t, 291, result.ReasoningTokens)
	assert.Equal(t, 13618, result.CacheCreationInputTokens)
	assert.Equal(t, 40590, result.CacheReadInputTokens)
}

// fakeOpenshellCodexChildren answers the manifest cat from storeDir, streams
// streamFixture for the launch, and runs the usage read under sh against
// sessionsDir, the admissions read against .security/findings.jsonl beside
// it and the debug-log read against codex-debug.log beside it.
// FULLSEND_TEST_CHILD_READ_FAIL=1 makes the usage read exit 1 (with
// FULLSEND_TEST_CHILD_READ_STDERR on stderr), =after makes it time out after
// printing; FULLSEND_TEST_ADMISSIONS_READ_FAIL does the same to the
// admissions read; FULLSEND_TEST_CODEX_EXIT sets the launch's exit status (killed:
// SIGKILL, as the budget's context kill ends it); FULLSEND_TEST_CODEX_DROP=1
// prints codex's dropped-notification warning on stderr before the stream,
// FULLSEND_TEST_CODEX_BACKFILL_FAIL=1 exec's failed-backfill warning.
func fakeOpenshellCodexChildren(t *testing.T, logPath, storeDir, streamFixture, sessionsDir string) {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + storeDir + `'/"$f"; exit $? ;;
    *"exec --json"*) [ "${FULLSEND_TEST_CODEX_DROP:-}" = 1 ] && echo "2026-10-22T16:00:00Z  WARN codex_app_server::in_process: ` + codexDroppedNotification + `" >&2; [ "${FULLSEND_TEST_CODEX_BACKFILL_FAIL:-}" = 1 ] && echo "2026-10-22T16:00:01Z  WARN codex_exec: ` + codexBackfillFailed + ` for turn completion: boom" >&2; cat '` + streamFixture + `'; [ "${FULLSEND_TEST_CODEX_EXIT:-}" = killed ] && kill -9 $$; exit "${FULLSEND_TEST_CODEX_EXIT:-0}" ;;
    *` + codexChildUsageMarker + `*)
      if [ "${FULLSEND_TEST_CHILD_READ_FAIL:-}" = 1 ]; then echo "fake openshell: refusing the rollout read${FULLSEND_TEST_CHILD_READ_STDERR:+ $FULLSEND_TEST_CHILD_READ_STDERR}" >&2; exit 1; fi
      sh -c "$(printf '%s' "$last" | sed 's#` + CodexRuntime{}.codexSessionsDir() + `#` + sessionsDir + `#g')"; rc=$?
      if [ "${FULLSEND_TEST_CHILD_READ_FAIL:-}" = after ]; then exit 124; fi
      exit $rc ;;
    *` + codexSpawnAdmitFinding + `*)
      if [ "${FULLSEND_TEST_ADMISSIONS_READ_FAIL:-}" = 1 ]; then echo "fake openshell: refusing the admissions read${FULLSEND_TEST_CHILD_READ_STDERR:+ $FULLSEND_TEST_CHILD_READ_STDERR}" >&2; exit 1; fi
      sh -c "$(printf '%s' "$last" | sed 's#` + codexFindingsLogPath + `#` + filepath.Join(filepath.Dir(sessionsDir), ".security", "findings.jsonl") + `#g')"; rc=$?
      if [ "${FULLSEND_TEST_ADMISSIONS_READ_FAIL:-}" = after ]; then exit 124; fi
      exit $rc ;;
    *` + codexDebugLogFile + `*)
      sh -c "$(printf '%s' "$last" | sed 's#` + sandbox.SandboxWorkspace + "/" + codexDebugLogFile + `#` + filepath.Join(filepath.Dir(sessionsDir), codexDebugLogFile) + `#g')"; exit $? ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// codexParentMetrics is the parent's iteration in collab_spawn.ndjson.
func codexParentMetrics(model string) *RunMetrics {
	return &RunMetrics{Model: model, NumTurns: 1, InputTokens: 15, OutputTokens: 1140, ReasoningTokens: 291, CacheCreationInputTokens: 13618, CacheReadInputTokens: 40590}
}

// codexTokenTotals is the run-level view the tests compare; RunMetrics
// itself carries an atomic and must not be copied.
type codexTokenTotals struct{ input, output, reasoning, cacheWrite, cacheRead int }

func codexTotalsOf(m *RunMetrics) codexTokenTotals {
	return codexTokenTotals{m.InputTokens, m.OutputTokens, m.ReasoningTokens, m.CacheCreationInputTokens, m.CacheReadInputTokens}
}

// assertCodexUsageSumsToTotals checks that per_model_usage sums to the run
// totals (docs/cli/run.md); reasoning is run-level only.
func assertCodexUsageSumsToTotals(t *testing.T, m *RunMetrics) {
	t.Helper()
	var sum ModelUsage
	for _, u := range m.PerModelUsage {
		sum.Add(u)
	}
	assert.Equal(t, m.InputTokens, sum.InputTokens, "input")
	assert.Equal(t, m.OutputTokens, sum.OutputTokens, "output")
	assert.Equal(t, m.CacheCreationInputTokens, sum.CacheCreationInputTokens, "cache_creation")
	assert.Equal(t, m.CacheReadInputTokens, sum.CacheReadInputTokens, "cache_read")
	assert.Equal(t, m.TotalCostUSD, sum.CostUSD, "cost (0 on codex)")
}

// Per fixture: parent 15/1140/291/13618/40590, luna child 3/8/0/7378/0,
// terra child 1000/200/100/2000/1000, no-model child 50/5/0/0/0 under
// unknown, failed child nothing.
func TestFoldCodexChildUsage(t *testing.T) {
	r := CodexRuntime{}
	parentOnly := ModelUsage{Requests: 1, InputTokens: 15, OutputTokens: 1140, CacheCreationInputTokens: 13618, CacheReadInputTokens: 40590}
	lunaBoth := ModelUsage{Requests: 2, InputTokens: 18, OutputTokens: 1148, CacheCreationInputTokens: 20996, CacheReadInputTokens: 40590}
	terra := ModelUsage{Requests: 1, InputTokens: 1000, OutputTokens: 200, CacheCreationInputTokens: 2000, CacheReadInputTokens: 1000}

	for name, tc := range map[string]struct {
		ids    []string
		folded int
		want   map[string]ModelUsage
		totals codexTokenTotals
	}{
		"same model aggregates into one entry": {
			ids: []string{codexChildSameModelID}, folded: 1,
			want:   map[string]ModelUsage{"gpt-5.6-luna": lunaBoth},
			totals: codexTokenTotals{18, 1148, 291, 20996, 40590},
		},
		"different model gets its own entry": {
			ids: []string{codexChildOtherModelID}, folded: 1,
			want:   map[string]ModelUsage{"gpt-5.6-luna": parentOnly, "gpt-5.6-terra": terra},
			totals: codexTokenTotals{1015, 1340, 391, 15618, 41590},
		},
		"failed child contributes nothing, not even a request": {
			ids: []string{codexChildFailedID}, folded: 0,
			want:   map[string]ModelUsage{"gpt-5.6-luna": parentOnly},
			totals: codexTokenTotals{15, 1140, 291, 13618, 40590},
		},
		"a child with usage but no model buckets under unknown": {
			ids: []string{codexChildNoModelID}, folded: 1,
			want:   map[string]ModelUsage{"gpt-5.6-luna": parentOnly, piSubagentUnknownModel: {Requests: 1, InputTokens: 50, OutputTokens: 5}},
			totals: codexTokenTotals{65, 1145, 291, 13618, 40590},
		},
		"all three": {
			ids: []string{codexChildSameModelID, codexChildOtherModelID, codexChildFailedID}, folded: 2,
			want:   map[string]ModelUsage{"gpt-5.6-luna": lunaBoth, "gpt-5.6-terra": terra},
			totals: codexTokenTotals{1018, 1348, 391, 22996, 41590},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sessions := writeCodexChildRollouts(t, codexChildSameModelID, codexChildOtherModelID, codexChildFailedID, codexChildNoModelID)
			fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

			m := codexParentMetrics("gpt-5.6-luna")
			n, err := r.foldCodexChildUsage("sb", tc.ids, m)
			require.NoError(t, err)
			assert.Equal(t, tc.folded, n, "one per child thread with usage")
			assert.Equal(t, tc.want, m.PerModelUsage)
			assert.Equal(t, tc.totals, codexTotalsOf(m), "child reasoning is in the run total only")
			assert.Zero(t, m.TotalCostUSD, "codex reports no cost; nothing is invented")
			assertCodexUsageSumsToTotals(t, m)
		})
	}
}

// A planted negative counter cannot lower the run's figures.
func TestFoldCodexChildUsage_ClampsNegativeCounters(t *testing.T) {
	sessions := writeCodexChildRollouts(t)
	rollout := `{"type":"turn_context","payload":{"model":"gpt-5.6-luna"}}` + "\n" +
		`{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tokens":10,"cached_input_tokens":-5000,"cache_write_input_tokens":-7000,"output_tokens":20,"reasoning_output_tokens":-900,"total_tokens":30}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "2026", "09", "29", "rollout-2026-09-29T15-46-38-"+codexChildSameModelID+".jsonl"), []byte(rollout), 0o644))
	fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

	m := codexParentMetrics("gpt-5.6-luna")
	n, err := CodexRuntime{}.foldCodexChildUsage("sb", []string{codexChildSameModelID}, m)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	// Input 10+5000+7000 and output 20+900: inflation is accepted, as on pi.
	assert.Equal(t, codexTokenTotals{15 + 12010, 1140 + 920, 291, 13618, 40590}, codexTotalsOf(m))
	assert.Equal(t, ModelUsage{Requests: 2, InputTokens: 15 + 12010, OutputTokens: 1140 + 920, CacheCreationInputTokens: 13618, CacheReadInputTokens: 40590},
		m.PerModelUsage["gpt-5.6-luna"])
	assertCodexUsageSumsToTotals(t, m)
}

// A read cut at the bound is a warning; the children before the cut stay
// folded.
func TestFoldCodexChildUsage_CutReadIsAWarning(t *testing.T) {
	sessions := writeCodexChildRollouts(t, codexChildSameModelID)
	day := filepath.Join(sessions, "2026", "09", "29")
	big := `{"type":"turn_context","payload":{"model":"` + strings.Repeat("x", 600<<10) + `"}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(day, "rollout-2026-09-29T15-46-38-"+codexChildOtherModelID+".jsonl"), []byte(big), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(day, "rollout-2026-09-29T15-46-38-"+codexChildFailedID+".jsonl"), append(readCodexFixture(t, "child_usage_other_model.jsonl"), big...), 0o644))
	fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

	m := codexParentMetrics("gpt-5.6-luna")
	n, err := CodexRuntime{}.foldCodexChildUsage("sb", []string{codexChildSameModelID, codexChildOtherModelID, codexChildFailedID}, m)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cut at")
	assert.Contains(t, err.Error(), "had a line that did not decode", "the cut line is reported too")
	assert.Equal(t, 1, n, "the child before the cut")
	assert.NotContains(t, m.PerModelUsage, "gpt-5.6-terra", "the child past the cut")
	assert.Equal(t, 2, m.PerModelUsage["gpt-5.6-luna"].Requests)
	assertCodexUsageSumsToTotals(t, m)
}

// An unreadable rollout, or one a failed search misses, is a warning;
// readable children stay folded.
func TestFoldCodexChildUsage_UnreadableRolloutIsAWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file modes do not stop root")
	}
	tests := []struct {
		name string
		lock func(t *testing.T, sessions string) string
	}{
		{name: "rollout file", lock: func(t *testing.T, sessions string) string {
			return filepath.Join(sessions, "2026", "09", "29", "rollout-2026-09-29T15-46-38-"+codexChildOtherModelID+".jsonl")
		}},
		{name: "sessions subdirectory", lock: func(t *testing.T, sessions string) string {
			dir := filepath.Join(sessions, "2026", "09", "30")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			name := "rollout-2026-09-29T15-46-38-" + codexChildOtherModelID + ".jsonl"
			require.NoError(t, os.Rename(filepath.Join(sessions, "2026", "09", "29", name), filepath.Join(dir, name)))
			return dir
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := writeCodexChildRollouts(t, codexChildSameModelID, codexChildOtherModelID)
			locked := tt.lock(t, sessions)
			require.NoError(t, os.Chmod(locked, 0))
			t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
			fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

			m := codexParentMetrics("gpt-5.6-luna")
			n, err := CodexRuntime{}.foldCodexChildUsage("sb", []string{codexChildSameModelID, codexChildOtherModelID}, m)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "1 child rollout(s) could not be read", "a failed search that still found a rollout is not a warning for it")
			assert.Equal(t, 1, n, "the readable child is still folded")
			assert.Equal(t, 2, m.PerModelUsage["gpt-5.6-luna"].Requests)
			assertCodexUsageSumsToTotals(t, m)
		})
	}
}

// Codex writes one rollout per thread, so a copy is planted: the id is
// reported and none of its copies count, while other children still do.
func TestFoldCodexChildUsage_DuplicateRolloutsCountNothing(t *testing.T) {
	sessions := writeCodexChildRollouts(t, codexChildSameModelID, codexChildOtherModelID)
	day := filepath.Join(sessions, "2026", "09", "29")
	data, err := os.ReadFile(filepath.Join(day, "rollout-2026-09-29T15-46-38-"+codexChildOtherModelID+".jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(day, "rollout-copy-"+codexChildOtherModelID+".jsonl"), data, 0o644))
	fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

	m := codexParentMetrics("gpt-5.6-luna")
	n, err := CodexRuntime{}.foldCodexChildUsage("sb", []string{codexChildSameModelID, codexChildOtherModelID}, m)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 child rollout(s) could not be read")
	assert.Equal(t, 1, n, "the luna child only")
	assert.NotContains(t, m.PerModelUsage, "gpt-5.6-terra", "neither copy counts")
	assertCodexUsageSumsToTotals(t, m)
}

// A credential planted as the child's model is redacted from the key.
func TestFoldCodexChildUsage_RedactsThePlantedModel(t *testing.T) {
	token := "ghp_" + strings.Repeat("A", 36)
	sessions := writeCodexChildRollouts(t)
	rollout := `{"type":"turn_context","payload":{"model":"` + token + `"}}` + "\n" +
		`{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "2026", "09", "29", "rollout-2026-09-29T15-46-38-"+codexChildSameModelID+".jsonl"), []byte(rollout), 0o644))
	fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

	m := codexParentMetrics("gpt-5.6-luna")
	n, err := CodexRuntime{}.foldCodexChildUsage("sb", []string{codexChildSameModelID}, m)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Len(t, m.PerModelUsage, 2)
	for spec := range m.PerModelUsage {
		assert.NotContains(t, spec, token)
	}
	assertCodexUsageSumsToTotals(t, m)
}

// A child with any counter past codexChildUsageMaxTokens is skipped with a
// warning.
func TestFoldCodexChildUsage_SkipsCountersOverMaxTokens(t *testing.T) {
	for _, field := range []string{"input_tokens", "cached_input_tokens", "cache_write_input_tokens", "output_tokens", "reasoning_output_tokens"} {
		t.Run(field, func(t *testing.T) {
			sessions := writeCodexChildRollouts(t)
			day := filepath.Join(sessions, "2026", "09", "29")
			for id, value := range map[string]string{codexChildSameModelID: "9223372036854775807", codexChildOtherModelID: "9223372036854775799"} {
				rollout := `{"type":"token_usage_record","payload":{"thread_token_usage":{"` + field + `":` + value + `}}}` + "\n"
				require.NoError(t, os.WriteFile(filepath.Join(day, "rollout-a-"+id+".jsonl"), []byte(rollout), 0o644))
			}
			fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

			m := codexParentMetrics("gpt-5.6-luna")
			n, err := CodexRuntime{}.foldCodexChildUsage("sb", []string{codexChildSameModelID, codexChildOtherModelID}, m)
			require.Error(t, err, "a skipped child is a warning, as a cut or unreadable read is")
			assert.Contains(t, err.Error(), "2 child(ren) with a counter past")
			assert.Zero(t, n)
			assert.Equal(t, codexTokenTotals{15, 1140, 291, 13618, 40590}, codexTotalsOf(m))
			assertCodexUsageSumsToTotals(t, m)
		})
	}
}

// No child id, no sandbox command (PATH has no openshell); the parent's
// entry still lands, under unknown when it has no model.
func TestFoldCodexChildUsage_NoChildrenNoSandbox(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	for _, model := range []string{"gpt-5.6-luna", ""} {
		m := codexParentMetrics(model)
		n, err := CodexRuntime{}.foldCodexChildUsage("sb", nil, m)
		require.NoError(t, err, "no id, no sandbox command")
		assert.Zero(t, n)
		key := model
		if key == "" {
			key = piSubagentUnknownModel
		}
		assert.Equal(t, map[string]ModelUsage{key: {Requests: 1, InputTokens: 15, OutputTokens: 1140, CacheCreationInputTokens: 13618, CacheReadInputTokens: 40590}}, m.PerModelUsage)
		assert.Equal(t, codexTokenTotals{15, 1140, 291, 13618, 40590}, codexTotalsOf(m), "the parent's numbers are not double-counted into the totals")
		assertCodexUsageSumsToTotals(t, m)
	}
}

func TestFoldCodexChildUsage_ReadFailuresKeepTheParentEntry(t *testing.T) {
	r := CodexRuntime{}
	parentOnly := map[string]ModelUsage{"gpt-5.6-luna": {Requests: 1, InputTokens: 15, OutputTokens: 1140, CacheCreationInputTokens: 13618, CacheReadInputTokens: 40590}}

	t.Run("a bad id never reaches the shell", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		m := codexParentMetrics("gpt-5.6-luna")
		n, err := r.foldCodexChildUsage("sb", []string{codexChildSameModelID, "'; rm -rf /; '"}, m)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a codex thread id")
		assert.Zero(t, n)
		assert.Equal(t, parentOnly, m.PerModelUsage)
	})

	t.Run("openshell cannot start", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		m := codexParentMetrics("gpt-5.6-luna")
		_, err := r.foldCodexChildUsage("sb", []string{codexChildSameModelID}, m)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading the child rollouts")
		assert.Equal(t, parentOnly, m.PerModelUsage)
		assert.Equal(t, codexTokenTotals{15, 1140, 291, 13618, 40590}, codexTotalsOf(m), "totals untouched")
	})

	t.Run("a timed-out read folds the stdout that came back", func(t *testing.T) {
		sessions := writeCodexChildRollouts(t, codexChildSameModelID)
		fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)
		t.Setenv("FULLSEND_TEST_CHILD_READ_FAIL", "after")
		m := codexParentMetrics("gpt-5.6-luna")
		n, err := r.foldCodexChildUsage("sb", []string{codexChildSameModelID}, m)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
		assert.Equal(t, 1, n)
		assert.Equal(t, 2, m.PerModelUsage["gpt-5.6-luna"].Requests)
		assertCodexUsageSumsToTotals(t, m)
	})

	t.Run("the read command exits non-zero", func(t *testing.T) {
		fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", t.TempDir())
		t.Setenv("FULLSEND_TEST_CHILD_READ_FAIL", "1")
		m := codexParentMetrics("gpt-5.6-luna")
		_, err := r.foldCodexChildUsage("sb", []string{codexChildSameModelID}, m)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exited 1")
		assert.Contains(t, err.Error(), "refusing the rollout read", "stderr is in the message")
		assert.Equal(t, parentOnly, m.PerModelUsage)
		assertCodexUsageSumsToTotals(t, m)
	})
}

// sumCodexIterations sums iterations as internal/cli aggregateRunMetrics does.
func sumCodexIterations(ms ...*RunMetrics) *RunMetrics {
	sum := &RunMetrics{PerModelUsage: map[string]ModelUsage{}}
	for _, m := range ms {
		sum.InputTokens += m.InputTokens
		sum.OutputTokens += m.OutputTokens
		sum.ReasoningTokens += m.ReasoningTokens
		sum.CacheCreationInputTokens += m.CacheCreationInputTokens
		sum.CacheReadInputTokens += m.CacheReadInputTokens
		sum.TotalCostUSD += m.TotalCostUSD
		for spec, u := range m.PerModelUsage {
			entry := sum.PerModelUsage[spec]
			entry.Add(u)
			sum.PerModelUsage[spec] = entry
		}
	}
	return sum
}

func TestFoldCodexChildUsage_TwoIterationsSumToTotals(t *testing.T) {
	sessions := writeCodexChildRollouts(t, codexChildSameModelID, codexChildOtherModelID)
	fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)
	r := CodexRuntime{}

	first := codexParentMetrics("gpt-5.6-luna")
	n, err := r.foldCodexChildUsage("sb", []string{codexChildSameModelID, codexChildOtherModelID}, first)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// The retry dispatched nothing; its parent entry still lands.
	second := &RunMetrics{Model: "gpt-5.6-luna", NumTurns: 1, InputTokens: 100, OutputTokens: 50, ReasoningTokens: 7, CacheCreationInputTokens: 20, CacheReadInputTokens: 30}
	n, err = r.foldCodexChildUsage("sb", nil, second)
	require.NoError(t, err)
	assert.Zero(t, n)

	sum := sumCodexIterations(first, second)
	assert.Equal(t, map[string]ModelUsage{
		"gpt-5.6-luna":  {Requests: 3, InputTokens: 118, OutputTokens: 1198, CacheCreationInputTokens: 21016, CacheReadInputTokens: 40620},
		"gpt-5.6-terra": {Requests: 1, InputTokens: 1000, OutputTokens: 200, CacheCreationInputTokens: 2000, CacheReadInputTokens: 1000},
	}, sum.PerModelUsage, "two parent iterations and one luna child under luna; the terra child alone under terra")
	assert.Equal(t, codexTokenTotals{1118, 1398, 398, 23016, 41620}, codexTotalsOf(sum))
	assertCodexUsageSumsToTotals(t, sum)
}

// codexChildrenRunParams are the RunParams of the run-level tests.
var codexChildrenRunParams = RunParams{
	SandboxName: "sb",
	RepoDir:     "/sandbox/workspace/repo",
	Model:       "openai/gpt-5.6-luna",
	Timeout:     time.Minute,
}

// tryCodexRunWithChildren runs Run against the fake with the rollouts of the
// three children the stream spawns and a findings log holding admitted
// admit lines (0: no file); delegating sets RoleFiles on the runner-held
// digests. It returns the metrics, exit code, output, argv log and Run's
// error.
func tryCodexRunWithChildren(t *testing.T, streamFixture string, delegating bool, admitted int) (*RunMetrics, int, string, string, error) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	if delegating {
		d, ok := lookupRunnerHeldDigests("sb")
		require.True(t, ok)
		d.RoleFiles = map[string]string{"default": "d0d0", "explore": "e0e0"}
		recordRunnerHeldDigests("sb", d)
	}
	sessions := writeCodexChildRollouts(t, codexChildSameModelID, codexChildOtherModelID, codexChildFailedID)
	writeCodexSpawnAdmissions(t, sessions, admitted)
	if !filepath.IsAbs(streamFixture) {
		streamFixture = filepath.Join("testdata", "codex", streamFixture)
	}
	fakeOpenshellCodexChildren(t, logPath, storeDir, streamFixture, sessions)

	var out bytes.Buffer
	metrics := &RunMetrics{}
	exit, err := r.Run(context.Background(), codexChildrenRunParams, ui.New(&out), time.Now(), metrics)
	return metrics, exit, out.String(), readFileString(t, logPath), err
}

// runCodexWithChildren is tryCodexRunWithChildren for a run that must not
// fail.
func runCodexWithChildren(t *testing.T, streamFixture string, delegating bool, admitted int) (*RunMetrics, int, string, string) {
	t.Helper()
	metrics, exit, out, log, err := tryCodexRunWithChildren(t, streamFixture, delegating, admitted)
	require.NoError(t, err)
	return metrics, exit, out, log
}

// A spawn in the stream alone (no RoleFiles) triggers the read.
func TestCodexRun_FoldsChildUsage(t *testing.T) {
	metrics, exit, out, log := runCodexWithChildren(t, "collab_spawn.ndjson", false, 3)

	assert.Equal(t, 0, exit)
	assert.Equal(t, "gpt-5.6-luna", metrics.Model)
	assert.Equal(t, codexTokenTotals{1018, 1348, 391, 22996, 41590}, codexTotalsOf(metrics),
		"parent (15/1140/291/13618/40590) plus the luna child (3/8/0/7378/0) plus the terra child (1000/200/100/2000/1000); the failed child nothing")
	assert.Equal(t, map[string]ModelUsage{
		"gpt-5.6-luna":  {Requests: 2, InputTokens: 18, OutputTokens: 1148, CacheCreationInputTokens: 20996, CacheReadInputTokens: 40590},
		"gpt-5.6-terra": {Requests: 1, InputTokens: 1000, OutputTokens: 200, CacheCreationInputTokens: 2000, CacheReadInputTokens: 1000},
	}, metrics.PerModelUsage)
	assertCodexUsageSumsToTotals(t, metrics)
	assert.Zero(t, metrics.TotalCostUSD)
	assert.Equal(t, 1, metrics.NumTurns, "turns stay parent-only")
	assert.Contains(t, out, "2 sub-agent thread(s) folded into the run metrics")
	assert.Equal(t, 1, strings.Count(log, codexChildUsageMarker), "one read command, after the run")
	assert.Equal(t, 1, strings.Count(log, codexSpawnAdmitFinding), "and one admissions read")
	d, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok)
	assert.Equal(t, 3, d.CompletedSpawns, "the run's count, kept for a retry")
	assert.Contains(t, log, shellQuote(codexChildFailedID), "the failed child's rollout is read too; it is its content that contributes nothing")
	assert.NotContains(t, log, codexChildFailedSpawnID, "the failed spawn's receiver is never looked up")
}

// A delegating agent that spawned nothing gets the parent's entry and no
// sandbox command.
func TestCodexRun_DelegatingWithoutSpawnHasParentEntry(t *testing.T) {
	metrics, exit, _, log := runCodexWithChildren(t, "basic_run.ndjson", true, 0)

	assert.Equal(t, 0, exit)
	assert.Equal(t, map[string]ModelUsage{
		"gpt-5.6-luna": {Requests: 1, InputTokens: 12, OutputTokens: 284, CacheCreationInputTokens: 11087, CacheReadInputTokens: 32508},
	}, metrics.PerModelUsage, "the numbers TestApplyCodexMetrics pins for basic_run.ndjson")
	assertCodexUsageSumsToTotals(t, metrics)
	assert.NotContains(t, log, codexChildUsageMarker, "no id, no sandbox command")
	assert.NotContains(t, log, codexSpawnAdmitFinding, "no completed spawn, no admissions read")
}

// A non-delegating agent with no spawn writes the same metrics.json as before.
func TestCodexRun_NoSpawnNoBreakdown(t *testing.T) {
	metrics, exit, _, log := runCodexWithChildren(t, "basic_run.ndjson", false, 0)

	assert.Equal(t, 0, exit)
	assert.Nil(t, metrics.PerModelUsage)
	data, err := json.Marshal(metrics)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "per_model_usage", "omitempty keeps the key out")
	assert.NotContains(t, log, codexChildUsageMarker)
	assert.NotContains(t, log, codexSpawnAdmitFinding)
}

// A failed read is a warning; the iteration keeps its exit code and the
// parent's entry.
func TestCodexRun_ChildReadFailureIsAWarning(t *testing.T) {
	token := "ghp_" + strings.Repeat("B", 36)
	t.Setenv("FULLSEND_TEST_CHILD_READ_FAIL", "1")
	t.Setenv("FULLSEND_TEST_CHILD_READ_STDERR", token)
	metrics, exit, out, _ := runCodexWithChildren(t, "collab_spawn.ndjson", false, 3)

	assert.Equal(t, 0, exit, "losing the children's tokens must not fail an iteration that succeeded")
	assert.Contains(t, out, "! Sub-agent usage not fully counted: reading the child rollouts: exited 1: fake openshell: refusing the rollout read")
	assert.NotContains(t, out, token, "the warning goes through the shared secret redactor")
	assert.NotContains(t, out, "folded into the run metrics")
	assert.Equal(t, codexTokenTotals{15, 1140, 291, 13618, 40590}, codexTotalsOf(metrics), "the parent's tokens only")
	assert.Equal(t, map[string]ModelUsage{
		"gpt-5.6-luna": {Requests: 1, InputTokens: 15, OutputTokens: 1140, CacheCreationInputTokens: 13618, CacheReadInputTokens: 40590},
	}, metrics.PerModelUsage)
	assertCodexUsageSumsToTotals(t, metrics)
}

// A non-zero codex exit still reads the children; with an exit-0 retry the
// breakdown still sums to the totals.
func TestCodexRun_NonZeroExitStillReadsTheChildren(t *testing.T) {
	t.Setenv("FULLSEND_TEST_CODEX_EXIT", "137")
	failed, exit, out, log := runCodexWithChildren(t, "collab_spawn.ndjson", true, 3)

	assert.Equal(t, 137, exit)
	assert.Equal(t, map[string]ModelUsage{
		"gpt-5.6-luna":  {Requests: 2, InputTokens: 18, OutputTokens: 1148, CacheCreationInputTokens: 20996, CacheReadInputTokens: 40590},
		"gpt-5.6-terra": {Requests: 1, InputTokens: 1000, OutputTokens: 200, CacheCreationInputTokens: 2000, CacheReadInputTokens: 1000},
	}, failed.PerModelUsage, "the parent's entry and the children it spawned")
	assert.Equal(t, codexTokenTotals{1018, 1348, 391, 22996, 41590}, codexTotalsOf(failed))
	assert.Equal(t, 1, strings.Count(log, codexChildUsageMarker), "one read command, after the run")
	assert.Contains(t, out, "2 sub-agent thread(s) folded into the run metrics")
	assertCodexUsageSumsToTotals(t, failed)

	t.Setenv("FULLSEND_TEST_CODEX_EXIT", "0")
	retry, exit, _, _ := runCodexWithChildren(t, "collab_spawn.ndjson", true, 3)
	require.Equal(t, 0, exit)
	assertCodexUsageSumsToTotals(t, sumCodexIterations(failed, retry))
	assert.NotContains(t, out, "may be undercounted", "codex exited on its own; its children were done")
}

// A codex stopped at the budget (the context kill, -1, or openshell's own
// timeout, 124) may still be running; the run says the children's later
// usage is not counted.
func TestCodexRun_BudgetStopWarnsOfLaterChildUsage(t *testing.T) {
	for exitEnv, want := range map[string]int{"killed": -1, "124": 124} {
		t.Run(exitEnv, func(t *testing.T) {
			t.Setenv("FULLSEND_TEST_CODEX_EXIT", exitEnv)
			_, exit, out, _ := runCodexWithChildren(t, "collab_spawn.ndjson", false, 3)

			assert.Equal(t, want, exit)
			assert.Contains(t, out, "2 sub-agent thread(s) folded into the run metrics")
			assert.Contains(t, out, "! Sub-agent usage may be undercounted: codex was stopped")
		})
	}
}

// writeCodexSpawnAdmissions writes n admission lines in the adapter's
// json.dumps spacing to .security/findings.jsonl beside the sessions tree,
// where the fake maps the sandbox findings log. 0 writes no file.
func writeCodexSpawnAdmissions(t *testing.T, sessions string, n int) {
	t.Helper()
	if n == 0 {
		return
	}
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `{"name": %q, "detail": "admitted spawn tool_use_id=call_1_%d agent_type=correctness"}`+"\n", codexSpawnAdmitFinding, i)
	}
	dir := filepath.Join(filepath.Dir(sessions), ".security")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "findings.jsonl"), []byte(b.String()), 0o600))
}

func TestCountCodexSpawnAdmissions(t *testing.T) {
	t.Parallel()

	data := string(readCodexFixture(t, "spawn_admissions.jsonl")) +
		`{"name": "codex_spawn_guard_admit", "detail": "admitted spawn tool_use_id=call_9_0 agent_ty` // the head -c cut
	assert.Equal(t, 3, countCodexSpawnAdmissions([]byte(data)),
		"the admit lines by name; the block line, the secret_redact_posttool line and a cut line are not counted")
	assert.Zero(t, countCodexSpawnAdmissions(nil), "no findings log: the command prints nothing")
}

func TestCompareCodexSpawnAdmissions(t *testing.T) {
	t.Parallel()

	root := codexRootThreadID
	tests := []struct {
		name                string
		primary             string
		senders             []string // this iteration's completed spawns
		completed, admitted int      // the whole run's
		want                string   // "" = no error
	}{
		{"equal counts", root, []string{root, root, root}, 3, 3, ""},
		{"more admissions: an admitted spawn codex then refused", root, []string{root, root}, 2, 3, ""},
		{"one completed spawn more than admissions", root, []string{root, root, root}, 3, 2,
			"codex_spawn_without_admission: 3 completed spawn(s) in this run's stream(s) but 2 recorded admission(s)"},
		{"an earlier iteration's spawns count", root, []string{root}, 4, 3,
			"codex_spawn_without_admission: 4 completed spawn(s) in this run's stream(s) but 3 recorded admission(s)"},
		{"a sender that is not the primary thread", root, []string{root, codexChildSameModelID}, 2, 3,
			"codex_spawn_without_admission: a completed spawn was not sent by the primary thread"},
		{"a spawn item without a sender", root, []string{""}, 1, 3,
			"codex_spawn_without_admission: a completed spawn was not sent by the primary thread"},
		{"the stream named no primary thread", "", []string{root}, 1, 3,
			"codex_spawn_without_admission: a completed spawn was not sent by the primary thread"},
		{"no sender and no primary thread", "", []string{""}, 1, 3,
			"codex_spawn_without_admission: a completed spawn was not sent by the primary thread"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := compareCodexSpawnAdmissions(tt.primary, tt.senders, tt.completed, tt.admitted)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.want)
		})
	}
}

func TestCodexSpawnAdmissionsReadCommand_Executes(t *testing.T) {
	t.Parallel()

	findings := filepath.Join(t.TempDir(), "findings.jsonl")
	require.NoError(t, os.WriteFile(findings, readCodexFixture(t, "spawn_admissions.jsonl"), 0o600))
	cmd := codexSpawnAdmissionsReadCommand(findings)
	assert.Contains(t, cmd, fmt.Sprintf("command -p head -c %d", codexChildUsageMaxBytes), "bounded inside the sandbox")

	out, err := exec.Command("/bin/sh", "-c", cmd).Output()
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(string(out), "\n"), "only the admit lines leave the sandbox")
	assert.Equal(t, 3, countCodexSpawnAdmissions(out))
}

// checkCodexSpawnAdmissions through the fake: the read, the count and the
// comparison together, and the read's two failures.
func TestCheckCodexSpawnAdmissions_ThroughTheSandbox(t *testing.T) {
	root := codexRootThreadID
	three := []string{root, root, root}
	for name, tc := range map[string]struct {
		senders  []string
		admitted int
		want     string
	}{
		"every completed spawn admitted": {three, 3, ""},
		"spawns, no findings log":        {three[:1], 0, "1 completed spawn(s) in this run's stream(s) but 0 recorded admission(s)"},
	} {
		t.Run(name, func(t *testing.T) {
			sessions := writeCodexChildRollouts(t)
			writeCodexSpawnAdmissions(t, sessions, tc.admitted)
			fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)

			err := checkCodexSpawnAdmissions("sb", root, tc.senders, len(tc.senders))
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("openshell cannot start", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		err := checkCodexSpawnAdmissions("sb", root, three, 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading the spawn admissions: openshell exec failed to start")
	})

	t.Run("the read times out", func(t *testing.T) {
		fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", t.TempDir())
		t.Setenv("FULLSEND_TEST_ADMISSIONS_READ_FAIL", "after")
		assert.EqualError(t, checkCodexSpawnAdmissions("sb", root, three, 3), "reading the spawn admissions: command timed out after 10s")
	})

	t.Run("the read is cut", func(t *testing.T) {
		sessions := writeCodexChildRollouts(t)
		writeCodexSpawnAdmissions(t, sessions, 12000)
		fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", sessions)
		err := checkCodexSpawnAdmissions("sb", root, three, 12000)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "recorded admission(s); the admissions read was cut at 1048576 bytes")
	})

	t.Run("the read exits non-zero", func(t *testing.T) {
		token := "ghp_" + strings.Repeat("B", 36)
		fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), t.TempDir(), "", t.TempDir())
		t.Setenv("FULLSEND_TEST_ADMISSIONS_READ_FAIL", "1")
		t.Setenv("FULLSEND_TEST_CHILD_READ_STDERR", token)
		err := checkCodexSpawnAdmissions("sb", root, three, 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading the spawn admissions: exited 1: fake openshell: refusing the admissions read")
		assert.NotContains(t, err.Error(), token, "stderr goes through the shared secret redactor")
	})
}

// A completed spawn with no admission fails the run whatever codex's exit:
// with a validation loop a non-zero exit alone does not. The fold runs
// first, so the metrics still carry the children.
func TestCodexRun_SpawnWithoutAdmissionFailsTheRun(t *testing.T) {
	for exitEnv, want := range map[string]int{"0": 0, "137": 137, "124": 124, "killed": -1} {
		t.Run(exitEnv, func(t *testing.T) {
			t.Setenv("FULLSEND_TEST_CODEX_EXIT", exitEnv)
			metrics, exit, out, log, err := tryCodexRunWithChildren(t, "collab_spawn.ndjson", true, 2)

			assert.EqualError(t, err, "codex_spawn_without_admission: 3 completed spawn(s) in this run's stream(s) but 2 recorded admission(s)",
				"the failed spawn of the fixture is not counted")
			assert.Equal(t, want, exit)
			assert.Contains(t, out, "2 sub-agent thread(s) folded into the run metrics", "the fold ran first")
			assert.Equal(t, 1, strings.Count(log, codexSpawnAdmitFinding))
			assert.Equal(t, codexTokenTotals{1018, 1348, 391, 22996, 41590}, codexTotalsOf(metrics))
		})
	}
}

// The count spans the run: the findings log keeps the earlier iterations'
// admissions, so a retry's spawns need admissions of their own.
func TestCodexRun_RetrySpawnsNeedTheirOwnAdmissions(t *testing.T) {
	runCodexWithChildren(t, "collab_spawn.ndjson", false, 3)

	var out bytes.Buffer
	_, err := CodexRuntime{}.Run(context.Background(), codexChildrenRunParams, ui.New(&out), time.Now(), &RunMetrics{})
	assert.EqualError(t, err, "codex_spawn_without_admission: 6 completed spawn(s) in this run's stream(s) but 3 recorded admission(s)")
}

func TestCodexChildIDs_FlushScansTheTornLastLine(t *testing.T) {
	t.Parallel()

	spawn := `{"type":"item.completed","item":{"id":"item_1","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"` + codexRootThreadID + `","receiver_thread_ids":["` + codexChildSameModelID + `"],"prompt":null,"agents_states":{},"status":"completed"}}`
	c := &codexChildIDs{}
	_, err := c.Write([]byte(spawn + "\n" + spawn))
	require.NoError(t, err)
	assert.Len(t, c.Senders, 1, "the last line has no newline yet")
	c.flush()
	assert.Equal(t, []string{codexChildSameModelID, codexChildSameModelID}, c.IDs, "codex killed mid-write leaves a completed spawn on the last line")
	assert.Len(t, c.Senders, 2)
	c.flush()
	assert.Len(t, c.Senders, 2, "nothing pending")
}

// A spawn item too long to read fails the run: whether that spawn completed
// is unknown, and the parent chooses the prompt's size.
func TestCodexRun_UnreadSpawnItemFailsTheRun(t *testing.T) {
	lines := bytes.Split(readCodexFixture(t, "collab_spawn.ndjson"), []byte("\n"))
	padded := false
	for i, line := range lines {
		if bytes.Contains(line, []byte(`"tool":"spawn_agent"`)) && bytes.Contains(line, []byte(`"status":"completed"`)) {
			lines[i] = bytes.Replace(line, []byte(`"prompt":"`), []byte(`"prompt":"`+strings.Repeat("a", streamBufSize)), 1)
			padded = true
			break
		}
	}
	require.True(t, padded)
	path := filepath.Join(t.TempDir(), "padded.ndjson")
	require.NoError(t, os.WriteFile(path, bytes.Join(lines, []byte("\n")), 0o600))

	_, exit, _, log, err := tryCodexRunWithChildren(t, path, false, 3)
	assert.EqualError(t, err, "codex_spawn_without_admission: a spawn_agent item longer than 1048576 bytes could not be read")
	assert.Equal(t, 0, exit)
	assert.NotContains(t, log, codexSpawnAdmitFinding, "nothing to compare: the unread item fails the run before the read")
}

// The findings log the admission cross-check counts across iterations
// survives ClearIterationArtifacts, which clears output/, the sessions
// directory and the debug log, nothing else under the workspace.
func TestCodexRuntime_ClearIterationArtifacts_KeepsTheFindingsLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodexChildren(t, logPath, t.TempDir(), "", t.TempDir())
	require.NoError(t, CodexRuntime{}.ClearIterationArtifacts("sb"))
	var clear string
	for _, line := range strings.Split(readFileString(t, logPath), "\n") {
		if i := strings.Index(line, "rm -rf "); i >= 0 {
			clear = line[i:]
		}
	}
	require.NotEmpty(t, clear, "the file cleanup ran")

	ws := t.TempDir()
	for _, p := range []string{".security/findings.jsonl", "output/result.txt"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ws, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ws, p), []byte("x\n"), 0o600))
	}
	out, err := exec.Command("/bin/sh", "-c", strings.ReplaceAll(clear, sandbox.SandboxWorkspace, ws)).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.FileExists(t, filepath.Join(ws, ".security", "findings.jsonl"), "the admissions of earlier iterations stay")
	assert.NoFileExists(t, filepath.Join(ws, "output", "result.txt"))
}

func TestCodexStderrDrops_CountsAcrossChunks(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	d := &codexStderrDrops{w: &sink}
	msg := "WARN codex_app_server::in_process: " + codexDroppedNotification + "\n"
	whole := "ERROR something else\n" + msg + "INFO dropping nothing\n" + msg
	for i := 0; i < len(whole); i += 5 {
		n, err := d.Write([]byte(whole[i:min(i+5, len(whole))]))
		require.NoError(t, err)
		assert.Equal(t, min(5, len(whole)-i), n)
	}
	assert.Equal(t, 2, d.Dropped, "one per line, however the writes split it")
	assert.Equal(t, whole, sink.String(), "every byte still reaches the runner's stderr")
	_, err := d.Write([]byte("WARN codex_exec: " + codexBackfillFailed + " for turn completion: boom\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, d.BackfillFailed)
	assert.Equal(t, 2, d.Dropped)

	last := &codexStderrDrops{w: &sink}
	_, err = last.Write([]byte("WARN " + codexDroppedNotification))
	require.NoError(t, err)
	assert.Equal(t, 0, last.Dropped, "no newline yet")
	last.flush()
	assert.Equal(t, 1, last.Dropped, "the last line is scanned once the stream has ended")
}

// A dropped stream notification can hide a completed spawn. Nothing can
// spawn until the multi-agent tools are switched on, so the run warns and
// the cross-check runs on what the stream did carry.
func TestCodexRun_DroppedNotificationWarns(t *testing.T) {
	t.Setenv("FULLSEND_TEST_CODEX_DROP", "1")
	_, exit, out, log := runCodexWithChildren(t, "collab_spawn.ndjson", true, 3)
	assert.Equal(t, 0, exit)
	assert.Contains(t, out, "! codex dropped 1 stream notification(s) under backpressure; the progress log is incomplete")
	assert.Equal(t, 1, strings.Count(log, codexSpawnAdmitFinding))

	t.Setenv("FULLSEND_TEST_CODEX_BACKFILL_FAIL", "1")
	_, _, out, _ = runCodexWithChildren(t, "collab_spawn.ndjson", true, 3)
	assert.Contains(t, out, "! codex dropped 1 stream notification(s) under backpressure, and its backfill of the turn's items failed, so a dropped completed item stayed lost; the progress log is incomplete")
}

// Under --debug codex's stderr goes to the sandbox log, which is read back.
func TestCodexRun_DroppedNotificationInTheDebugLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	sessions := writeCodexChildRollouts(t)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(sessions), codexDebugLogFile),
		[]byte("WARN codex_app_server::in_process: "+codexDroppedNotification+"\nWARN codex_exec: "+codexBackfillFailed+" for turn completion: boom\n"), 0o600))
	fakeOpenshellCodexChildren(t, logPath, storeDir, filepath.Join("testdata", "codex", "basic_run.ndjson"), sessions)

	params := codexChildrenRunParams
	params.Debug = "1"
	var out bytes.Buffer
	exit, err := r.Run(context.Background(), params, ui.New(&out), time.Now(), &RunMetrics{})
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Contains(t, out.String(), "! codex dropped 1 stream notification(s) under backpressure, and its backfill of the turn's items failed")
	assert.Contains(t, readFileString(t, logPath), "grep -a -c -F", "the counts came from the sandbox log")
}
