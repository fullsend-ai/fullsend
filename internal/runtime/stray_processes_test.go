package runtime

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// TestKillStrayProcessesScript_Golden pins the snippet byte-for-byte to
// testdata/kill_stray_processes.sh — the file kill_stray_processes_test.sh
// executes under a real shell, so the golden is what guarantees the shell
// test exercises the production bytes.
func TestKillStrayProcessesScript_Golden(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile("testdata/kill_stray_processes.sh")
	require.NoError(t, err)
	assert.Equal(t, string(want), killStrayProcessesScript())
}

// TestKillStrayProcessesScript_Invariants checks the properties the sandbox
// image and the runner depend on, independent of the exact golden bytes.
func TestKillStrayProcessesScript_Invariants(t *testing.T) {
	t.Parallel()

	script := killStrayProcessesScript()
	// Only tools the sandbox image ships (procps ps, mawk, dash builtins);
	// no shebang because sandbox.Exec hands the text to `sh -c`.
	for _, forbidden := range []string{"pkill", "pgrep", "killall", "bash -c", "#!/"} {
		assert.NotContains(t, script, forbidden, "snippet must stay POSIX sh + ps/awk/kill/sleep/id")
	}
	// Numeric uid: the sandbox user need not be resolvable through NSS.
	assert.Contains(t, script, "ps -o pid= -o ppid= -o stat= -o args= -u \"$(id -u)\"")
	// The sandbox keep-alive main process runs as the sandbox user and must
	// survive, or OpenShell marks the sandbox terminal.
	assert.Contains(t, script, "-v keep="+shellQuote(sandbox.KeepAliveCommand))
	assert.Contains(t, script, "kill -s TERM")
	assert.Contains(t, script, "kill -s KILL")
	// A failed listing must be distinguishable from "nothing to kill".
	assert.Contains(t, script, "echo 'stray processes: ps failed' >&2\n  exit 3\n")
	// So must a failed liveness probe, which prints nothing and exits 1
	// exactly like "none of those pids exist"; the KILL pass still runs
	// over everything that was TERMed before the snippet gives up.
	assert.Contains(t, script, "echo 'stray processes: ps -p failed' >&2\n    exit 3\n")
	assert.Contains(t, script, "kill -0 ")
	assert.True(t, strings.HasSuffix(script, "exit 0\n"), "a completed sweep never fails the exec")
}

// The awk exclusion strips a leading directory from the command word and
// then compares the argv verbatim, so the constant must be exactly the two
// bare words — a path or an extra token would silently stop matching and
// the sweep would kill the keep-alive.
func TestKeepAliveCommandMatchesSweepExclusion(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"sleep", "infinity"}, strings.Fields(sandbox.KeepAliveCommand))
}

// recordingExec returns a sandboxExecFunc that appends every command to
// *calls and answers with the given result.
func recordingExec(calls *[]string, stdout, stderr string, exitCode int, err error) sandboxExecFunc {
	return func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		*calls = append(*calls, cmd)
		return stdout, stderr, exitCode, err
	}
}

func TestKillStrayProcesses_ReportsCount(t *testing.T) {
	t.Parallel()

	var calls []string
	var sbName string
	var timeout time.Duration
	execFn := func(name string, cmd string, d time.Duration) (string, string, int, error) {
		sbName, timeout = name, d
		calls = append(calls, cmd)
		return "stray processes killed: 3\n", "", 0, nil
	}

	n, err := killStrayProcesses(execFn, "sb")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, "sb", sbName)
	require.Len(t, calls, 1)
	assert.Equal(t, killStrayProcessesScript(), calls[0])
	assert.Greater(t, timeout, 2*time.Second, "exec timeout must cover the snippet's own 2s grace period")
}

func TestKillStrayProcesses_ExecError(t *testing.T) {
	t.Parallel()

	var calls []string
	boom := errors.New("command timed out after 15s")
	_, err := killStrayProcesses(recordingExec(&calls, "", "", 124, boom), "sb")
	require.ErrorIs(t, err, boom)
}

func TestKillStrayProcesses_NonZeroExit(t *testing.T) {
	t.Parallel()

	var calls []string
	_, err := killStrayProcesses(recordingExec(&calls, "", "sh: awk: not found", 127, nil), "sb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 127")
	assert.Contains(t, err.Error(), "awk: not found")
}

// The snippet's own "ps failed" exit (3) must surface as an error, not as a
// silent zero-kill success.
func TestKillStrayProcesses_PsFailure(t *testing.T) {
	t.Parallel()

	var calls []string
	n, err := killStrayProcesses(recordingExec(&calls, "", "stray processes: ps failed\n", 3, nil), "sb")
	require.Error(t, err)
	assert.Equal(t, 0, n)
	assert.Contains(t, err.Error(), "exit 3")
	assert.Contains(t, err.Error(), "ps failed")
}

func TestKillStrayProcesses_UnexpectedOutput(t *testing.T) {
	t.Parallel()

	var calls []string
	_, err := killStrayProcesses(recordingExec(&calls, "something else\n", "", 0, nil), "sb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "something else")
}

func TestClearStrayProcesses_ReportsKilledWithDuration(t *testing.T) {
	t.Parallel()

	var calls []string
	var out bytes.Buffer
	clearStrayProcesses(recordingExec(&calls, "stray processes killed: 2\n", "", 0, nil), "sb", &out)
	assert.Contains(t, out.String(), "2 stray process")
	assert.Regexp(t, `\([0-9.]+(m|µ|n)?s\)`, out.String(), "the sweep duration is logged")
}

func TestClearStrayProcesses_SilentWhenNothingKilled(t *testing.T) {
	t.Parallel()

	var calls []string
	var out bytes.Buffer
	clearStrayProcesses(recordingExec(&calls, "stray processes killed: 0\n", "", 0, nil), "sb", &out)
	assert.Empty(t, out.String())
}

// A failed sweep is a warning, never an error: the file cleanup that
// follows in ClearIterationArtifacts must still run and the iteration
// must proceed.
func TestClearStrayProcesses_WarnsOnFailure(t *testing.T) {
	t.Parallel()

	var calls []string
	var out bytes.Buffer
	clearStrayProcesses(recordingExec(&calls, "", "boom", 124, errors.New("command timed out after 15s")), "sb", &out)
	assert.Contains(t, out.String(), "Warning")
	assert.Contains(t, out.String(), "stray")
	assert.Contains(t, out.String(), "timed out")
}

// The ps-failure exit takes the same warning path as a gateway error.
func TestClearStrayProcesses_WarnsOnPsFailure(t *testing.T) {
	t.Parallel()

	var calls []string
	var out bytes.Buffer
	clearStrayProcesses(recordingExec(&calls, "", "stray processes: ps failed\n", 3, nil), "sb", &out)
	assert.Contains(t, out.String(), "Warning")
	assert.Contains(t, out.String(), "ps failed")
}

// TestKillStrayProcessesScript_InterruptGolden pins the longer-grace
// rendering byte-for-byte to testdata/kill_stray_processes_interrupt.sh,
// so kill_stray_processes_test.sh can execute the production bytes of the
// codex interrupt sweep under a real shell, exactly as it does for the
// default one.
func TestKillStrayProcessesScript_InterruptGolden(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile("testdata/kill_stray_processes_interrupt.sh")
	require.NoError(t, err)
	assert.Equal(t, string(want), killStrayProcessesScriptWithGrace(interruptStrayGrace))
}

// TestKillStrayProcessesScript_GraceRendering checks that the grace is the
// ONLY thing that differs between the two renderings: the process-selection
// logic the sandbox depends on must not fork.
func TestKillStrayProcessesScript_GraceRendering(t *testing.T) {
	t.Parallel()

	def := killStrayProcessesScript()
	interrupt := killStrayProcessesScriptWithGrace(interruptStrayGrace)

	assert.Contains(t, def, `while [ "$i" -lt 20 ]`, "default grace is 20 ticks of 100ms")
	assert.Contains(t, def, "still alive after 2s.")
	assert.Contains(t, interrupt, `while [ "$i" -lt 100 ]`, "interrupt grace is 100 ticks of 100ms")
	assert.Contains(t, interrupt, "still alive after 10s.")

	// No placeholder may survive into a rendered script.
	for _, script := range []string{def, interrupt} {
		for _, placeholder := range []string{"__GRACE_TICKS__", "__GRACE_LABEL__", "__KEEPALIVE__"} {
			assert.NotContains(t, script, placeholder)
		}
	}

	// Normalising the grace lines must make the two identical.
	norm := func(s string) string {
		s = strings.Replace(s, `-lt 100 `, `-lt N `, 1)
		s = strings.Replace(s, `-lt 20 `, `-lt N `, 1)
		s = strings.Replace(s, "after 10s.", "after Ns.", 1)
		return strings.Replace(s, "after 2s.", "after Ns.", 1)
	}
	assert.Equal(t, norm(def), norm(interrupt), "the two renderings must differ only in the grace")
}

// TestKillStrayProcessesTimeoutCoversTheGrace is the bound the maintainer
// asked for on #6753: the exec timeout exists to catch a hung gateway, and
// it must never cut the TERM wait short — otherwise raising the grace would
// silently mean the KILL pass never runs.
func TestKillStrayProcessesTimeoutCoversTheGrace(t *testing.T) {
	t.Parallel()

	for _, grace := range []time.Duration{defaultStrayGrace, interruptStrayGrace} {
		timeout := killStrayProcessesTimeoutFor(grace)
		assert.Greater(t, timeout, grace, "timeout must outlast the TERM wait for grace %s", grace)
		assert.GreaterOrEqual(t, timeout-grace, 5*time.Second,
			"headroom for the gateway round trip, the ps polls and the KILL pass (grace %s)", grace)
	}
	// The default rendering keeps the historical bound exactly.
	assert.Equal(t, 15*time.Second, killStrayProcessesTimeout)
}

// TestInterruptSweepUsesTheLongerGrace pins what codexSteerQueue actually
// runs: the sweep that stops a turn the runner means to resume gets the
// long grace and the matching timeout, while ClearIterationArtifacts keeps
// the short one.
func TestInterruptSweepUsesTheLongerGrace(t *testing.T) {
	t.Parallel()

	var cmd string
	var timeout time.Duration
	rec := func(_ string, c string, to time.Duration) (string, string, int, error) {
		cmd, timeout = c, to
		return "stray processes killed: 1\n", "", 0, nil
	}

	n, err := interruptSweep(rec, "sbx")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Contains(t, cmd, `while [ "$i" -lt 100 ]`, "the codex interrupt must use the long grace")
	assert.Equal(t, killStrayProcessesTimeoutFor(interruptStrayGrace), timeout)

	n, err = killStrayProcesses(rec, "sbx")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Contains(t, cmd, `while [ "$i" -lt 20 ]`, "the iteration boundary keeps the short grace")
	assert.Equal(t, killStrayProcessesTimeout, timeout)
}

// TestNewCodexSteerQueue_DefaultsToTheInterruptSweep guards the wiring: the
// queue's sweep field is injectable for tests, and a default that pointed
// at the short-grace sweep would restore the #6753 behaviour silently.
func TestNewCodexSteerQueue_DefaultsToTheInterruptSweep(t *testing.T) {
	t.Parallel()

	var cmd string
	q := newCodexSteerQueue("sbx", func(_ string, c string, _ time.Duration) (string, string, int, error) {
		cmd = c
		return "stray processes killed: 0\n", "", 0, nil
	}, io.Discard)

	q.interrupt()
	assert.Contains(t, cmd, `while [ "$i" -lt 100 ]`)
}
