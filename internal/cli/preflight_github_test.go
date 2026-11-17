package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestPreflightGitHubResult_SkippedFields(t *testing.T) {
	// Verify the result struct reports skip reasons correctly.
	r := &preflightGitHubResult{Skipped: true, SkipReason: "GH_TOKEN not set in sandbox"}
	assert.True(t, r.Skipped)
	assert.Equal(t, "GH_TOKEN not set in sandbox", r.SkipReason)
}

func TestPreflightGitHubResult_NotSkipped(t *testing.T) {
	r := &preflightGitHubResult{}
	assert.False(t, r.Skipped)
	assert.Empty(t, r.SkipReason)
}

func TestPreflightGitHubTimeout(t *testing.T) {
	// Ensure the timeout constant is set to a reasonable value.
	require.Greater(t, preflightGitHubTimeout.Seconds(), float64(0))
	require.LessOrEqual(t, preflightGitHubTimeout.Seconds(), float64(60))
}

func TestPreflightGitHubWaitTimeout(t *testing.T) {
	// The sandbox network can take 7 to 15 seconds to become ready under
	// load (#9612), so the wait must comfortably exceed that.
	require.GreaterOrEqual(t, preflightGitHubWaitTimeout, 30*time.Second)
	require.LessOrEqual(t, preflightGitHubWaitTimeout, 2*time.Minute)
	require.Greater(t, preflightGitHubInitialDelay, time.Duration(0))
	require.LessOrEqual(t, preflightGitHubInitialDelay, preflightGitHubMaxDelay)
	require.Less(t, preflightGitHubMaxDelay, preflightGitHubWaitTimeout)
}

type fakeExecResult struct {
	stdout, stderr string
	exitCode       int
	err            error
}

// connectivityExec succeeds the GH_TOKEN/gh probe, then returns scripted
// results for each subsequent gh api /rate_limit call.
func connectivityExec(t *testing.T, checks []fakeExecResult) sandboxExecFunc {
	t.Helper()
	var i int
	return func(_, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "echo NOTOKEN") {
			return "OK", "", 0, nil
		}
		if !strings.Contains(cmd, "gh api /rate_limit") {
			t.Fatalf("unexpected sandbox command: %s", cmd)
		}
		if i >= len(checks) {
			t.Fatalf("unexpected extra connectivity check (already used %d)", i)
		}
		r := checks[i]
		i++
		return r.stdout, r.stderr, r.exitCode, r.err
	}
}

// fakePreflightClock is a simulated clock for the connectivity check. It
// only moves when the check sleeps or a test advances it explicitly.
type fakePreflightClock struct {
	start, now time.Time
}

func (c *fakePreflightClock) elapsed() time.Duration { return c.now.Sub(c.start) }

// useNoSleepPreflightGitHub replaces the sleep with one that records each
// wait and advances a fake clock, so the wait timeout is enforced without
// real delays.
func useNoSleepPreflightGitHub(t *testing.T) *[]time.Duration {
	t.Helper()
	slept, _ := useFakePreflightGitHubClock(t)
	return slept
}

func useFakePreflightGitHubClock(t *testing.T) (*[]time.Duration, *fakePreflightClock) {
	t.Helper()
	var slept []time.Duration
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &fakePreflightClock{start: start, now: start}
	origSleep, origNow := preflightGitHubSleep, preflightGitHubNow
	preflightGitHubSleep = func(d time.Duration) {
		slept = append(slept, d)
		clock.now = clock.now.Add(d)
	}
	preflightGitHubNow = func() time.Time { return clock.now }
	t.Cleanup(func() {
		preflightGitHubSleep = origSleep
		preflightGitHubNow = origNow
	})
	return &slept, clock
}

// alwaysFailingConnectivityExec succeeds the GH_TOKEN/gh probe, then fails
// every gh api /rate_limit call with the same result and counts the calls.
func alwaysFailingConnectivityExec(t *testing.T, r fakeExecResult, calls *int) sandboxExecFunc {
	t.Helper()
	return func(_, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "echo NOTOKEN") {
			return "OK", "", 0, nil
		}
		if !strings.Contains(cmd, "gh api /rate_limit") {
			t.Fatalf("unexpected sandbox command: %s", cmd)
		}
		*calls++
		return r.stdout, r.stderr, r.exitCode, r.err
	}
}

func sumDurations(ds []time.Duration) time.Duration {
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total
}

// assertWaitedOutTimeout checks the wait schedule of a check that never
// succeeded: capped backoff that ends exactly at the wait timeout.
func assertWaitedOutTimeout(t *testing.T, slept []time.Duration, calls int) {
	t.Helper()
	require.NotEmpty(t, slept)
	assert.Equal(t, preflightGitHubInitialDelay, slept[0])
	for _, d := range slept {
		assert.LessOrEqual(t, d, preflightGitHubMaxDelay)
	}
	assert.Equal(t, preflightGitHubWaitTimeout, sumDurations(slept))
	assert.Equal(t, len(slept)+1, calls, "one attempt before each wait plus the final attempt")
}

func usePreflightGitHubExec(t *testing.T, execFn sandboxExecFunc) {
	t.Helper()
	orig := preflightGitHubExec
	preflightGitHubExec = execFn
	t.Cleanup(func() { preflightGitHubExec = orig })
}

func TestCheckSandboxGitHubConnectivity_SuccessFirstAttempt(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	usePreflightGitHubExec(t, connectivityExec(t, []fakeExecResult{{}}))
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Skipped)
	assert.Empty(t, *slept)
}

// Both probe commands read .env as data, as the launch does (#8574), with the
// loader's warnings discarded so they cannot reach the 401/404 diagnosis.
func TestCheckSandboxGitHubConnectivity_ReadsEnvAsData(t *testing.T) {
	useNoSleepPreflightGitHub(t)
	var cmds []string
	inner := connectivityExec(t, []fakeExecResult{{}})
	usePreflightGitHubExec(t, func(name, cmd string, timeout time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return inner(name, cmd, timeout)
	})
	_, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.Len(t, cmds, 2)
	load := "{ " + agentruntime.EnvDataLoad("/sandbox/workspace/.env") + "; } 2>/dev/null"
	assert.True(t, strings.HasPrefix(cmds[0], load+"; "), cmds[0])
	assert.True(t, strings.HasPrefix(cmds[1], load+` && PATH="$FULLSEND_TRUSTED_PATH" gh api /rate_limit`), cmds[1])
	assert.Contains(t, cmds[0], `(PATH="$FULLSEND_TRUSTED_PATH"; command -v gh)`, "gh is looked up on the trusted PATH")
	for _, cmd := range cmds {
		assert.NotContains(t, cmd, ". /sandbox/workspace/.env")
	}
}

func TestCheckSandboxGitHubConnectivity_RetriesOnTransient403(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	var buf bytes.Buffer
	printer := ui.New(&buf)

	usePreflightGitHubExec(t, connectivityExec(t, []fakeExecResult{
		{stderr: "gh: HTTP 403 Forbidden", exitCode: 1},
		{stderr: "HTTP 403", exitCode: 1},
		{},
	}))
	result, err := checkSandboxGitHubConnectivity("sb", printer)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Skipped)
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second}, *slept)
	logs := buf.String()
	assert.Contains(t, logs, "attempt 1)")
	assert.Contains(t, logs, "attempt 2)")
	assert.Contains(t, logs, "HTTP 403")
	assert.Contains(t, logs, "retrying in 2s")
	assert.Contains(t, logs, "retrying in 4s")
	assert.Contains(t, logs, "gives up after 1m0s")
}

func TestCheckSandboxGitHubConnectivity_FailsAfterWaitTimeoutOnPersistent403(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	calls := 0
	usePreflightGitHubExec(t, alwaysFailingConnectivityExec(t, fakeExecResult{stderr: "HTTP 403", exitCode: 1}, &calls))
	result, err := checkSandboxGitHubConnectivity("sb", nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), fmt.Sprintf("failed after %d attempts over 1m0s", calls))
	assert.Contains(t, err.Error(), "wait timeout 1m0s exceeded")
	assert.Contains(t, err.Error(), "HTTP 403")
	assertWaitedOutTimeout(t, *slept, calls)
}

// The sandbox network can refuse connections for several seconds after the
// sandbox starts (#9612). The check waits it out instead of failing.
func TestCheckSandboxGitHubConnectivity_WaitsForSandboxNetwork(t *testing.T) {
	slept, clock := useFakePreflightGitHubClock(t)
	var buf bytes.Buffer
	printer := ui.New(&buf)
	calls := 0
	usePreflightGitHubExec(t, func(_, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "echo NOTOKEN") {
			return "OK", "", 0, nil
		}
		calls++
		if clock.elapsed() < 12*time.Second {
			return "", "dial tcp 127.0.0.1:3128: connect: Connection refused", 1, nil
		}
		return "", "", 0, nil
	})
	result, err := checkSandboxGitHubConnectivity("sb", printer)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Skipped)
	// Attempts at 0s, 2s, 6s and 11s are refused; the one at 16s succeeds.
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}, *slept)
	assert.Equal(t, 5, calls)
	assert.Less(t, sumDurations(*slept), preflightGitHubWaitTimeout)
	logs := buf.String()
	assert.Contains(t, logs, "connection refused")
	assert.Contains(t, logs, "attempt 4)")
}

func TestCheckSandboxGitHubConnectivity_FailsAfterWaitTimeoutOnPersistentConnectionRefused(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	calls := 0
	usePreflightGitHubExec(t, alwaysFailingConnectivityExec(t,
		fakeExecResult{stderr: "dial tcp: Connection refused", exitCode: 1}, &calls))
	result, err := checkSandboxGitHubConnectivity("sb", nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "wait timeout 1m0s exceeded")
	assert.Contains(t, err.Error(), "connection failed")
	assert.Contains(t, err.Error(), "Connection refused")
	assertWaitedOutTimeout(t, *slept, calls)
}

// Time spent inside slow attempts counts toward the wait timeout, so a
// check whose attempts each hang until the per-attempt timeout stops after
// the deadline instead of retrying on a fixed attempt count.
func TestCheckSandboxGitHubConnectivity_WaitTimeoutCountsAttemptTime(t *testing.T) {
	slept, clock := useFakePreflightGitHubClock(t)
	calls := 0
	timeoutErr := fmt.Errorf("command timed out after %s", preflightGitHubTimeout)
	usePreflightGitHubExec(t, func(_, cmd string, timeout time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "echo NOTOKEN") {
			return "OK", "", 0, nil
		}
		calls++
		clock.now = clock.now.Add(timeout)
		return "", "", 124, timeoutErr
	})
	result, err := checkSandboxGitHubConnectivity("sb", nil)

	require.Error(t, err)
	assert.Nil(t, result)
	// Attempt 1 ends at 30s; after a 2s wait, attempt 2 ends at 62s, past
	// the 60s wait timeout.
	assert.Equal(t, 2, calls)
	assert.Equal(t, []time.Duration{2 * time.Second}, *slept)
	assert.Contains(t, err.Error(), "failed after 2 attempts over 1m2s")
	assert.ErrorIs(t, err, timeoutErr)
}

func TestCheckSandboxGitHubConnectivity_DoesNotRetryOn401(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	calls := 0
	execFn := func(_, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "echo NOTOKEN") {
			return "OK", "", 0, nil
		}
		calls++
		return "", "gh: HTTP 401 Unauthorized", 1, nil
	}

	usePreflightGitHubExec(t, execFn)
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 1, calls, "401 must fail on the first connectivity check")
	assert.Empty(t, *slept)
	assert.NotContains(t, err.Error(), "after")
	assert.Contains(t, err.Error(), "401")
}

func TestCheckSandboxGitHubConnectivity_DoesNotRetryOn404(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	calls := 0
	execFn := func(_, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "echo NOTOKEN") {
			return "OK", "", 0, nil
		}
		calls++
		return "", "gh: HTTP 404", 1, nil
	}

	usePreflightGitHubExec(t, execFn)
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 1, calls, "404 must fail on the first connectivity check")
	assert.Empty(t, *slept)
}

func TestCheckSandboxGitHubConnectivity_RetriesOnConnectionRefused(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	usePreflightGitHubExec(t, connectivityExec(t, []fakeExecResult{
		{stderr: "dial tcp: Connection refused", exitCode: 1},
		{},
	}))
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []time.Duration{2 * time.Second}, *slept)
}

func TestCheckSandboxGitHubConnectivity_RetriesOnEOF(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	usePreflightGitHubExec(t, connectivityExec(t, []fakeExecResult{
		{stderr: `Get "https://api.github.com/rate_limit": EOF`, exitCode: 1},
		{},
	}))
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []time.Duration{2 * time.Second}, *slept)
}

func TestCheckSandboxGitHubConnectivity_FailsAfterWaitTimeoutOnPersistentEOF(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	calls := 0
	eof := `Get "https://api.github.com/rate_limit": EOF`
	usePreflightGitHubExec(t, alwaysFailingConnectivityExec(t, fakeExecResult{stderr: eof, exitCode: 1}, &calls))
	result, err := checkSandboxGitHubConnectivity("sb", nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "wait timeout 1m0s exceeded")
	assert.Contains(t, err.Error(), "connection closed before a response")
	assertWaitedOutTimeout(t, *slept, calls)
}

func TestCheckSandboxGitHubConnectivity_RetriesOnConnectionReset(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	usePreflightGitHubExec(t, connectivityExec(t, []fakeExecResult{
		{stderr: "read: connection reset by peer", exitCode: 1},
		{},
	}))
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []time.Duration{2 * time.Second}, *slept)
}

func TestCheckSandboxGitHubConnectivity_RetriesOnTimeout(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	timeoutErr := fmt.Errorf("command timed out after %s", preflightGitHubTimeout)
	usePreflightGitHubExec(t, connectivityExec(t, []fakeExecResult{
		{exitCode: 124, err: timeoutErr},
		{},
	}))
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []time.Duration{2 * time.Second}, *slept)
}

func TestCheckSandboxGitHubConnectivity_DoesNotRetryOnDNSFailure(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	calls := 0
	execFn := func(_, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "echo NOTOKEN") {
			return "OK", "", 0, nil
		}
		calls++
		return "", "Could not resolve host: api.github.com", 1, nil
	}

	usePreflightGitHubExec(t, execFn)
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 1, calls)
	assert.Empty(t, *slept)
	assert.Contains(t, err.Error(), "DNS resolution failed")
}

func TestCheckSandboxGitHubConnectivity_SkipNoToken(t *testing.T) {
	usePreflightGitHubExec(t, func(_, _ string, _ time.Duration) (string, string, int, error) {
		return "NOTOKEN", "", 0, nil
	})
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Skipped)
	assert.Equal(t, "GH_TOKEN not set in sandbox", result.SkipReason)
}

func TestCheckSandboxGitHubConnectivity_SkipNoGh(t *testing.T) {
	usePreflightGitHubExec(t, func(_, _ string, _ time.Duration) (string, string, int, error) {
		return "NOGH", "", 0, nil
	})
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Skipped)
	assert.Equal(t, "gh CLI not available in sandbox", result.SkipReason)
}

func TestCheckSandboxGitHubConnectivity_SkipProbeError(t *testing.T) {
	usePreflightGitHubExec(t, func(_, _ string, _ time.Duration) (string, string, int, error) {
		return "", "", -1, errors.New("openshell exec failed to start")
	})
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.SkipReason, "probe command failed")
}

func TestCheckSandboxGitHubConnectivity_SkipProbeNonZeroExit(t *testing.T) {
	usePreflightGitHubExec(t, func(_, _ string, _ time.Duration) (string, string, int, error) {
		return "", "boom", 1, nil
	})
	result, err := checkSandboxGitHubConnectivity("sb", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Skipped)
	assert.Equal(t, "GH_TOKEN not set in sandbox", result.SkipReason)
}

func TestIsRetryablePreflightGitHubFailure(t *testing.T) {
	timeoutErr := errors.New("command timed out after 30s")
	tests := []struct {
		name     string
		exitCode int
		err      error
		output   string
		want     bool
	}{
		{name: "403", output: "HTTP 403", want: true},
		{name: "Forbidden", output: "Forbidden", want: true},
		{name: "connection refused", output: "Connection refused", want: true},
		{name: "lowercase connection refused", output: "connection refused", want: true},
		{name: "connection timed out", output: "Connection timed out", want: true},
		{name: "exec timeout", exitCode: 124, err: timeoutErr, want: true},
		{name: "401", output: "HTTP 401 Unauthorized", want: false},
		{name: "404", output: "HTTP 404", want: false},
		{name: "dns", output: "Could not resolve host", want: false},
		{name: "generic", exitCode: 1, output: "something else went wrong", want: false},
		{name: "401 wins over 403 substring", output: "HTTP 401 Unauthorized (proxy also returned 403 earlier)", want: false},
		{name: "403 body with unrelated digits resembling 401/404", output: "HTTP 403 Forbidden (ref 40199, trace 40404)", want: true},
		{name: "observed rate_limit EOF", output: `Get "https://api.github.com/rate_limit": EOF`, want: true},
		{name: "connection reset by peer", output: "connection reset by peer", want: true},
		{name: "Connection reset by peer", output: "Connection reset by peer", want: true},
		{name: "401 wins over EOF", output: "HTTP 401 Unauthorized: EOF", want: false},
		{name: "unrelated unexpected EOF is not a connection EOF", exitCode: 1, output: "yaml: line 1: unexpected EOF", want: false},
		{name: "EOF letters inside a word", exitCode: 1, output: "GEOFENCE policy rejected", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryablePreflightGitHubFailure(tt.exitCode, tt.err, tt.output)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDiagnosePreflightGitHubFailure(t *testing.T) {
	err := diagnosePreflightGitHubFailure(1, nil, "HTTP 403 Forbidden")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy allowlist")

	err = diagnosePreflightGitHubFailure(1, nil, "Could not resolve host")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DNS resolution failed")

	err = diagnosePreflightGitHubFailure(1, nil, "Name or service not known")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DNS resolution failed")

	err = diagnosePreflightGitHubFailure(1, nil, "Connection refused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")

	err = diagnosePreflightGitHubFailure(1, nil, "Connection timed out")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")

	err = diagnosePreflightGitHubFailure(1, nil, `Get "https://api.github.com/rate_limit": EOF`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection closed before a response")

	err = diagnosePreflightGitHubFailure(1, nil, "read: connection reset by peer")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection closed before a response")

	err = diagnosePreflightGitHubFailure(2, nil, "mystery")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 2")

	underlying := errors.New("openshell exec failed to start")
	err = diagnosePreflightGitHubFailure(-1, underlying, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, underlying)
}

func TestPreflightGitHubRetryReason(t *testing.T) {
	assert.Equal(t, "HTTP 403", preflightGitHubRetryReason(1, nil, "HTTP 403"))
	assert.Equal(t, "HTTP 403", preflightGitHubRetryReason(1, nil, "Forbidden"))
	assert.Equal(t, "connection refused", preflightGitHubRetryReason(1, nil, "Connection refused"))
	assert.Equal(t, "timeout", preflightGitHubRetryReason(124, errors.New("command timed out after 30s"), ""))
	assert.Equal(t, "timeout", preflightGitHubRetryReason(1, nil, "Connection timed out"))
	assert.Equal(t, "EOF", preflightGitHubRetryReason(1, nil, `Get "https://api.github.com/rate_limit": EOF`))
	assert.Equal(t, "connection reset", preflightGitHubRetryReason(1, nil, "connection reset by peer"))
	assert.Equal(t, "connection reset", preflightGitHubRetryReason(1, nil, "Connection reset by peer"))
	assert.Equal(t, "transient network error", preflightGitHubRetryReason(1, nil, "other"))
}
