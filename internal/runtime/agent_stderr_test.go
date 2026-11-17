package runtime

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// fakeAgentStderrArm goes in front of a fake openshell's agent launch arm:
// it writes $FULLSEND_TEST_AGENT_STDERR to stderr, as an agent CLI writes
// its own diagnostics, before the stream.
const fakeAgentStderrArm = `[ -n "${FULLSEND_TEST_AGENT_STDERR:-}" ] && printf '%s' "$FULLSEND_TEST_AGENT_STDERR" >&2; `

// agentStderrText stands in for what an agent CLI prints on stderr: it can
// quote text the agent read, so at quiet none of it may reach the job log.
const agentStderrText = "warn: reading PRIVATE-ISSUE-TEXT\nwarn: second line\n"

// captureAgentStderrLog points the job-log side of the agent CLI's stderr
// at a buffer for the test.
func captureAgentStderrLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := agentStderrLog
	agentStderrLog = func() io.Writer { return buf }
	t.Cleanup(func() { agentStderrLog = prev })
	return buf
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// At full and tools (and unset) the agent CLI's stderr goes to the job log
// byte for byte, as before, and no file is written.
func TestOpenAgentStderr_FullAndToolsPassThrough(t *testing.T) {
	for _, level := range []string{"", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
		t.Run("level="+level, func(t *testing.T) {
			jobLog := captureAgentStderrLog(t)
			path := filepath.Join(t.TempDir(), AgentStderrFile)
			var out bytes.Buffer
			w, end := openAgentStderr(RunParams{ProgressVerbosity: level, StderrPath: path}, ui.New(&out))
			_, err := io.WriteString(w, agentStderrText+"no newline")
			require.NoError(t, err)
			end()
			assert.Equal(t, agentStderrText+"no newline", jobLog.String())
			assert.Empty(t, out.String(), "no count line outside quiet")
			assert.NoFileExists(t, path)
		})
	}
}

// At quiet the stderr streams to the file, the job log gets none of it,
// and fullsend prints one line with the count.
func TestOpenAgentStderr_QuietStreamsToFile(t *testing.T) {
	jobLog := captureAgentStderrLog(t)
	path := filepath.Join(t.TempDir(), AgentStderrFile)
	var out bytes.Buffer
	w, end := openAgentStderr(RunParams{ProgressVerbosity: config.ProgressVerbosityQuiet, StderrPath: path}, ui.New(&out))
	// Split across writes the way a pipe delivers it.
	for _, chunk := range []string{"warn: reading PRIV", "ATE-ISSUE-TEXT\nwarn: sec", "ond line\n", "tail without newline"} {
		n, err := io.WriteString(w, chunk)
		require.NoError(t, err)
		assert.Equal(t, len(chunk), n)
	}
	end()
	assert.Empty(t, jobLog.String())
	assert.Equal(t, agentStderrText+"tail without newline", readFileOrEmpty(t, path))
	assert.Contains(t, out.String(), "Agent CLI stderr: 3 line(s) kept out of the log at quiet")
	assert.NotContains(t, out.String(), "PRIVATE")
	assert.NotContains(t, out.String(), "tail without")
}

// An unknown level counts as quiet: a bad value never prints more.
func TestOpenAgentStderr_UnknownLevelIsQuiet(t *testing.T) {
	jobLog := captureAgentStderrLog(t)
	path := filepath.Join(t.TempDir(), AgentStderrFile)
	w, end := openAgentStderr(RunParams{ProgressVerbosity: "bogus", StderrPath: path}, ui.New(&bytes.Buffer{}))
	_, _ = io.WriteString(w, agentStderrText)
	end()
	assert.Empty(t, jobLog.String())
	assert.Equal(t, agentStderrText, readFileOrEmpty(t, path))
}

// Quiet with no file, or a file that cannot be created, drops the stderr:
// it never falls back to the job log.
func TestOpenAgentStderr_QuietWithoutFileDiscards(t *testing.T) {
	for name, path := range map[string]string{
		"no path":       "",
		"cannot create": filepath.Join(t.TempDir(), "missing-dir", AgentStderrFile),
	} {
		t.Run(name, func(t *testing.T) {
			jobLog := captureAgentStderrLog(t)
			var out bytes.Buffer
			w, end := openAgentStderr(RunParams{ProgressVerbosity: config.ProgressVerbosityQuiet, StderrPath: path}, ui.New(&out))
			n, err := io.WriteString(w, agentStderrText)
			require.NoError(t, err, "a dropped write must not stop the pipe from draining")
			assert.Equal(t, len(agentStderrText), n)
			end()
			assert.Empty(t, jobLog.String())
			assert.Contains(t, out.String(), "Agent CLI stderr: 2 line(s) kept out of the log at quiet")
			assert.Contains(t, out.String(), "discarded")
			assert.NotContains(t, out.String(), "PRIVATE")
		})
	}
}

// Quiet with no stderr prints nothing.
func TestOpenAgentStderr_QuietNoStderrNoLine(t *testing.T) {
	captureAgentStderrLog(t)
	var out bytes.Buffer
	_, end := openAgentStderr(RunParams{ProgressVerbosity: config.ProgressVerbosityQuiet, StderrPath: filepath.Join(t.TempDir(), AgentStderrFile)}, ui.New(&out))
	end()
	assert.Empty(t, out.String())
}

// assertQuietStderrRouted checks one runtime's quiet run: the stderr is in
// the file, not in the job log or the printer output, and the count line
// printed.
func assertQuietStderrRouted(t *testing.T, jobLog *bytes.Buffer, out, path, wantFile string, wantLines int) {
	t.Helper()
	assert.NotContains(t, jobLog.String(), "PRIVATE-ISSUE-TEXT", "quiet stderr must not reach the job log")
	assert.NotContains(t, out, "PRIVATE-ISSUE-TEXT", "quiet never echoes a stderr excerpt")
	assert.Equal(t, wantFile, readFileOrEmpty(t, path))
	assert.Contains(t, out, "Agent CLI stderr: "+strconv.Itoa(wantLines)+" line(s) kept out of the log at quiet")
}

// Claude Code: quiet stderr lands in the file; full keeps the pass-through.
// Both model attempts of a fallback write to the same file.
func TestClaudeRun_AgentStderrAtQuietGoesToFile(t *testing.T) {
	t.Setenv("FULLSEND_TEST_AGENT_STDERR", agentStderrText)
	for _, level := range []string{config.ProgressVerbosityQuiet, config.ProgressVerbosityFull} {
		t.Run(level, func(t *testing.T) {
			jobLog := captureAgentStderrLog(t)
			fakeOpenshellClaude(t, filepath.Join(t.TempDir(), "openshell.log"), map[string]string{
				"claude-opus-5-5": claudeNotServedStream,
				"claude-opus-4-6": claudeServedStream,
			})
			path := filepath.Join(t.TempDir(), AgentStderrFile)
			var out bytes.Buffer
			exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
				SandboxName: "fs-claude-stderr", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
				Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
				ProgressVerbosity: level, StderrPath: path,
			}, ui.New(&out), time.Now(), &RunMetrics{})
			require.NoError(t, err)
			assert.Equal(t, 0, exit)
			if level == config.ProgressVerbosityQuiet {
				assertQuietStderrRouted(t, jobLog, out.String(), path, agentStderrText+agentStderrText, 4)
				return
			}
			assert.Equal(t, agentStderrText+agentStderrText, jobLog.String(), "full passes stderr through unchanged")
			assert.NoFileExists(t, path)
			assert.NotContains(t, out.String(), "Agent CLI stderr")
		})
	}
}

// pi: quiet stderr lands in the file; full keeps the pass-through.
func TestPiRun_AgentStderrAtQuietGoesToFile(t *testing.T) {
	t.Setenv("FULLSEND_TEST_AGENT_STDERR", agentStderrText)
	for _, level := range []string{config.ProgressVerbosityQuiet, config.ProgressVerbosityFull} {
		t.Run(level, func(t *testing.T) {
			jobLog := captureAgentStderrLog(t)
			path, out := runPiFixture(t, "basic_run.ndjson", level)
			if level == config.ProgressVerbosityQuiet {
				assertQuietStderrRouted(t, jobLog, out, path, agentStderrText, 2)
				return
			}
			assert.Equal(t, agentStderrText, jobLog.String())
			assert.NoFileExists(t, path)
		})
	}
}

// runPiFixture bootstraps and runs pi against a testdata/pi stream at
// level, with the stderr file in a temp dir. It returns that path and the
// printer output.
func runPiFixture(t *testing.T, fixtureName, level string) (string, string) {
	t.Helper()
	work := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("testdata", "pi", fixtureName))
	require.NoError(t, err)
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), filepath.Join(work, "store"), fixture)
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, testAgentDef), agentName: "triage",
	}))
	path := filepath.Join(work, AgentStderrFile)
	var out bytes.Buffer
	_, err = PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", AgentBaseName: "triage", RepoDir: "/sandbox/workspace/repo",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
		ProgressVerbosity: level, StderrPath: path,
	}, ui.New(&out), time.Now(), &RunMetrics{})
	require.NoError(t, err)
	return path, out.String()
}

// runCodexFixture runs codex against a testdata/codex stream at level.
func runCodexFixture(t *testing.T, fixtureName, level string) (int, string, string) {
	t.Helper()
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fixture, err := filepath.Abs(filepath.Join("testdata", "codex", fixtureName))
	require.NoError(t, err)
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "openshell.log"), storeDir, "codex-cli 0.152.1", fixture)
	path := filepath.Join(t.TempDir(), AgentStderrFile)
	var out bytes.Buffer
	exit, err := r.Run(t.Context(), RunParams{
		SandboxName: "sb", RepoDir: "/sandbox/workspace/repo", Model: "gpt-5-mini",
		Timeout: time.Minute, OnEvent: func(AgentEvent) {},
		ProgressVerbosity: level, StderrPath: path,
	}, ui.New(&out), time.Now(), &RunMetrics{})
	require.NoError(t, err)
	return exit, path, out.String()
}

// Codex: quiet stderr lands in the file; full keeps the pass-through.
func TestCodexRun_AgentStderrAtQuietGoesToFile(t *testing.T) {
	t.Setenv("FULLSEND_TEST_AGENT_STDERR", agentStderrText)
	for _, level := range []string{config.ProgressVerbosityQuiet, config.ProgressVerbosityFull} {
		t.Run(level, func(t *testing.T) {
			jobLog := captureAgentStderrLog(t)
			exit, path, out := runCodexFixture(t, "basic_run.ndjson", level)
			assert.Equal(t, 0, exit)
			if level == config.ProgressVerbosityQuiet {
				assertQuietStderrRouted(t, jobLog, out, path, agentStderrText, 2)
				return
			}
			assert.Equal(t, agentStderrText, jobLog.String())
			assert.NoFileExists(t, path)
		})
	}
}

// Codex at quiet still counts the dropped-notification marker on stderr.
func TestCodexRun_DroppedNotificationCountedAtQuiet(t *testing.T) {
	t.Setenv("FULLSEND_TEST_CODEX_DROP", "1")
	jobLog := captureAgentStderrLog(t)
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	sessions := writeCodexChildRollouts(t)
	fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), storeDir, filepath.Join("testdata", "codex", "basic_run.ndjson"), sessions)
	params := codexChildrenRunParams
	params.ProgressVerbosity = config.ProgressVerbosityQuiet
	params.StderrPath = filepath.Join(t.TempDir(), AgentStderrFile)
	var out bytes.Buffer
	exit, err := r.Run(context.Background(), params, ui.New(&out), time.Now(), &RunMetrics{})
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Contains(t, out.String(), "! codex dropped 1 stream notification(s) under backpressure; the progress log is incomplete")
	assert.Empty(t, jobLog.String())
	assert.Contains(t, readFileOrEmpty(t, params.StderrPath), codexDroppedNotification)
	assert.Contains(t, out.String(), "Agent CLI stderr: 1 line(s) kept out of the log at quiet")
}

// runOpenCodeStream runs OpenCode against an inline stream at level.
func runOpenCodeStream(t *testing.T, stream, level string) (int, error, string, string) {
	t.Helper()
	t.Setenv(openCodeProviderEnv, "")
	fixture := filepath.Join(t.TempDir(), "stream.jsonl")
	require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o644))
	fakeOpenshellOpenCodeStream(t, fixture)
	path := filepath.Join(t.TempDir(), AgentStderrFile)
	var out bytes.Buffer
	exit, err := OpenCodeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", AgentBaseName: "triage", RepoDir: "/repo",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
		ProgressVerbosity: level, StderrPath: path,
	}, ui.New(&out), time.Now(), &RunMetrics{})
	return exit, err, path, out.String()
}

const openCodeOKStream = `{"type":"step_finish","timestamp":1,"sessionID":"s1","part":{"reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\n"

// OpenCode: quiet stderr lands in the file; full keeps the pass-through.
func TestOpenCodeRun_AgentStderrAtQuietGoesToFile(t *testing.T) {
	t.Setenv("FULLSEND_TEST_AGENT_STDERR", agentStderrText)
	for _, level := range []string{config.ProgressVerbosityQuiet, config.ProgressVerbosityFull} {
		t.Run(level, func(t *testing.T) {
			jobLog := captureAgentStderrLog(t)
			exit, err, path, out := runOpenCodeStream(t, openCodeOKStream, level)
			require.NoError(t, err)
			assert.Equal(t, 0, exit)
			if level == config.ProgressVerbosityQuiet {
				assertQuietStderrRouted(t, jobLog, out, path, agentStderrText, 2)
				return
			}
			assert.Equal(t, agentStderrText, jobLog.String())
			assert.NoFileExists(t, path)
		})
	}
}

// OpenCode at quiet still sees the agent-fallback marker on stderr and
// refuses the run.
func TestOpenCodeRun_FallbackMarkerSeenAtQuiet(t *testing.T) {
	t.Setenv("FULLSEND_TEST_AGENT_STDERR", "PRIVATE-ISSUE-TEXT "+openCodeAgentFallbackMarker+"\n")
	jobLog := captureAgentStderrLog(t)
	_, err, path, out := runOpenCodeStream(t, openCodeOKStream, config.ProgressVerbosityQuiet)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opencode ran its default agent")
	assert.Empty(t, jobLog.String())
	assert.Contains(t, readFileOrEmpty(t, path), openCodeAgentFallbackMarker)
	assert.NotContains(t, out, "PRIVATE-ISSUE-TEXT")
}

// Fix for the "exited 0 but the stream reports an error" warning: at quiet
// it names the subtype only, never the stream's error message; at full it
// is unchanged.
func TestPiRun_ExitedZeroStreamErrorAtQuietNamesSubtypeOnly(t *testing.T) {
	_, quiet := runPiFixture(t, "error_run.ndjson", config.ProgressVerbosityQuiet)
	assert.Contains(t, quiet, "pi exited 0 but the stream reports an error (subtype: error)")
	assert.NotContains(t, quiet, "quota exhausted")

	_, full := runPiFixture(t, "error_run.ndjson", config.ProgressVerbosityFull)
	assert.Contains(t, full, "pi exited 0 but the stream reports an error: quota exhausted")
}

func TestCodexRun_ExitedZeroStreamErrorAtQuietNamesSubtypeOnly(t *testing.T) {
	exit, _, quiet := runCodexFixture(t, "turn_failed.ndjson", config.ProgressVerbosityQuiet)
	assert.Equal(t, 1, exit)
	assert.Contains(t, quiet, "codex exited 0 but the stream reports an error (subtype: ")
	assert.NotContains(t, quiet, "exceeded retry limit")

	_, _, full := runCodexFixture(t, "turn_failed.ndjson", config.ProgressVerbosityFull)
	assert.Contains(t, full, "codex exited 0 but the stream reports an error: stream error: exceeded retry limit")
}

func TestOpenCodeRun_ExitedZeroStreamErrorAtQuietNamesSubtypeOnly(t *testing.T) {
	stream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"ProviderError","data":{"message":"quota exhausted"}}}` + "\n"
	exit, err, _, quiet := runOpenCodeStream(t, stream, config.ProgressVerbosityQuiet)
	require.NoError(t, err)
	assert.Equal(t, 1, exit)
	assert.Contains(t, quiet, "opencode exited 0 but the stream reports an error (subtype: ")
	assert.NotContains(t, quiet, "quota exhausted")

	_, _, _, full := runOpenCodeStream(t, stream, config.ProgressVerbosityFull)
	assert.Contains(t, full, "opencode exited 0 but the stream reports an error: quota exhausted")
}

// A file write that fails mid-run never fails the pipe and never falls
// back to the job log; the count line says the file is incomplete.
func TestOpenAgentStderr_QuietWriteFailureKeepsDraining(t *testing.T) {
	jobLog := captureAgentStderrLog(t)
	var out bytes.Buffer
	w, end := openAgentStderr(RunParams{ProgressVerbosity: config.ProgressVerbosityQuiet, StderrPath: filepath.Join(t.TempDir(), AgentStderrFile)}, ui.New(&out))
	sink, ok := w.(*quietAgentStderr)
	require.True(t, ok)
	require.NoError(t, sink.file.Close())
	n, err := io.WriteString(w, "")
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = io.WriteString(w, agentStderrText)
	require.NoError(t, err)
	assert.Equal(t, len(agentStderrText), n)
	end()
	assert.Empty(t, jobLog.String())
	assert.Contains(t, out.String(), "Agent CLI stderr: 2 line(s) kept out of the log at quiet; writing agent-stderr.log failed, so it is incomplete")
	assert.NotContains(t, out.String(), "PRIVATE")
}

// The stderr file can quote text the agent read, so only the runner's
// user can read it.
func TestOpenAgentStderr_QuietFileIsPrivate(t *testing.T) {
	captureAgentStderrLog(t)
	path := filepath.Join(t.TempDir(), AgentStderrFile)
	w, end := openAgentStderr(RunParams{ProgressVerbosity: config.ProgressVerbosityQuiet, StderrPath: path}, ui.New(&bytes.Buffer{}))
	_, _ = io.WriteString(w, agentStderrText)
	end()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

const claudeErrorStream = `{"type":"system","subtype":"init","model":"claude-test-model","claude_code_version":"2.1.296","session_id":"2d9d8e0c-0000-4750-89b9-3129edaa5ccf"}
{"type":"result","subtype":"error_during_execution","is_error":true,"result":"failed","num_turns":1,"total_cost_usd":0}
`

// The count line prints when the agent CLI exits non-zero too.
func TestClaudeRun_AgentStderrCountOnNonZeroExit(t *testing.T) {
	t.Setenv("FULLSEND_TEST_AGENT_STDERR", agentStderrText)
	jobLog := captureAgentStderrLog(t)
	fakeOpenshellClaude(t, filepath.Join(t.TempDir(), "openshell.log"), map[string]string{
		"claude-test-model": claudeErrorStream,
	})
	path := filepath.Join(t.TempDir(), AgentStderrFile)
	var out bytes.Buffer
	exit, _ := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-stderr", AgentBaseName: "code", RepoDir: "/r", Model: "claude-test-model",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
		ProgressVerbosity: config.ProgressVerbosityQuiet, StderrPath: path,
	}, ui.New(&out), time.Now(), &RunMetrics{})
	assert.NotEqual(t, 0, exit)
	assertQuietStderrRouted(t, jobLog, out.String(), path, agentStderrText, 2)
}

// A runtime warning that ends in sandbox stderr, an openshell error or an
// agent-writable file prints the label only at quiet; other levels print
// the detail as before.
func TestDetailWarning(t *testing.T) {
	for _, level := range []string{"", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
		assert.Equal(t, "Could not read X: SECRET-DETAIL", detailWarning("Could not read X", "SECRET-DETAIL", level), "level %q", level)
	}
	for _, level := range []string{config.ProgressVerbosityQuiet, "bogus"} {
		assert.Equal(t, "Could not read X", detailWarning("Could not read X", "SECRET-DETAIL", level), "level %q", level)
	}
}

// Codex at quiet: a failed read of the children's rollouts and of the
// agent-writable debug log warn without quoting the failure.
func TestCodexRun_ReadFailureWarningsAtQuiet(t *testing.T) {
	t.Setenv("FULLSEND_TEST_CHILD_READ_FAIL", "1")
	t.Setenv("FULLSEND_TEST_DEBUG_LOG_READ_FAIL", "1")
	captureAgentStderrLog(t)
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	sessions := writeCodexChildRollouts(t, codexChildSameModelID, codexChildOtherModelID, codexChildFailedID)
	writeCodexSpawnAdmissions(t, sessions, 4)
	fakeOpenshellCodexChildren(t, filepath.Join(t.TempDir(), "openshell.log"), storeDir, filepath.Join("testdata", "codex", "collab_spawn.ndjson"), sessions)
	params := codexChildrenRunParams
	params.Debug = "1"
	params.ProgressVerbosity = config.ProgressVerbosityQuiet
	params.StderrPath = filepath.Join(t.TempDir(), AgentStderrFile)

	var out bytes.Buffer
	exit, err := r.Run(context.Background(), params, ui.New(&out), time.Now(), &RunMetrics{})
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Contains(t, out.String(), "! Sub-agent usage not fully counted\n")
	assert.Contains(t, out.String(), "! Could not read codex's log for dropped stream notifications\n")
	assert.NotContains(t, out.String(), "fake openshell", "the openshell stderr stays out of the log")
}
