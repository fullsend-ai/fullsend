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

func TestPreflightGitHubRetryDelays(t *testing.T) {
	require.Equal(t, []time.Duration{2 * time.Second, 5 * time.Second}, preflightGitHubRetryDelays,
		"three attempts total: initial plus one retry after each delay")
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

func useNoSleepPreflightGitHub(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	orig := preflightGitHubSleep
	preflightGitHubSleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { preflightGitHubSleep = orig })
	return &slept
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
	assert.Equal(t, []time.Duration{2 * time.Second, 5 * time.Second}, *slept)
	logs := buf.String()
	assert.Contains(t, logs, "attempt 1/3")
	assert.Contains(t, logs, "attempt 2/3")
	assert.Contains(t, logs, "HTTP 403")
	assert.Contains(t, logs, "retrying in 2s")
	assert.Contains(t, logs, "retrying in 5s")
}

func TestCheckSandboxGitHubConnectivity_FailsAfterMaxRetriesOnPersistent403(t *testing.T) {
	slept := useNoSleepPreflightGitHub(t)
	usePreflightGitHubExec(t, connectivityExec(t, []fakeExecResult{
		{stderr: "HTTP 403", exitCode: 1},
		{stderr: "HTTP 403", exitCode: 1},
		{stderr: "HTTP 403", exitCode: 1},
	}))
	result, err := checkSandboxGitHubConnectivity("sb", nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed after 3 attempts")
	assert.Contains(t, err.Error(), "HTTP 403")
	assert.Equal(t, []time.Duration{2 * time.Second, 5 * time.Second}, *slept)
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
	assert.Equal(t, "transient network error", preflightGitHubRetryReason(1, nil, "other"))
}
