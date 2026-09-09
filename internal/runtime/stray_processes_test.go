package runtime

import (
	"bytes"
	"errors"
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
	clearStrayProcesses(recordingExec(&calls, "stray processes killed: 2\n", "", 0, nil), "sb", &out, "the previous iteration")
	assert.Contains(t, out.String(), "2 stray process")
	assert.Regexp(t, `\([0-9.]+(m|µ|n)?s\)`, out.String(), "the sweep duration is logged")
}

// TerminateStrayProcesses is the same sweep run right after an iteration
// was ended at its budget (#7042); only the origin wording differs.
func TestTerminateStrayProcesses_NamesTheTimedOutIteration(t *testing.T) {
	var calls []string
	orig := terminateExecFn
	terminateExecFn = recordingExec(&calls, "stray processes killed: 4\n", "", 0, nil)
	t.Cleanup(func() { terminateExecFn = orig })

	var out bytes.Buffer
	TerminateStrayProcesses("sb", &out)
	require.Len(t, calls, 1)
	assert.Equal(t, killStrayProcessesScript(), calls[0])
	assert.Contains(t, out.String(), "Terminated 4 stray process(es) left running by the timed-out iteration")
}

func TestClearStrayProcesses_SilentWhenNothingKilled(t *testing.T) {
	t.Parallel()

	var calls []string
	var out bytes.Buffer
	clearStrayProcesses(recordingExec(&calls, "stray processes killed: 0\n", "", 0, nil), "sb", &out, "the previous iteration")
	assert.Empty(t, out.String())
}

// A failed sweep is a warning, never an error: the file cleanup that
// follows in ClearIterationArtifacts must still run and the iteration
// must proceed.
func TestClearStrayProcesses_WarnsOnFailure(t *testing.T) {
	t.Parallel()

	var calls []string
	var out bytes.Buffer
	clearStrayProcesses(recordingExec(&calls, "", "boom", 124, errors.New("command timed out after 15s")), "sb", &out, "the previous iteration")
	assert.Contains(t, out.String(), "Warning")
	assert.Contains(t, out.String(), "stray")
	assert.Contains(t, out.String(), "timed out")
}

// The ps-failure exit takes the same warning path as a gateway error.
func TestClearStrayProcesses_WarnsOnPsFailure(t *testing.T) {
	t.Parallel()

	var calls []string
	var out bytes.Buffer
	clearStrayProcesses(recordingExec(&calls, "", "stray processes: ps failed\n", 3, nil), "sb", &out, "the previous iteration")
	assert.Contains(t, out.String(), "Warning")
	assert.Contains(t, out.String(), "ps failed")
}
