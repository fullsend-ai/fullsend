package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// preflightGitHubTimeout is the maximum time to wait for the GitHub API
	// connectivity check inside the sandbox. Short because this is a fast
	// pre-flight — if the proxy is blocking, the connection attempt fails
	// quickly (HTTP 403 on CONNECT).
	preflightGitHubTimeout = 30 * time.Second
)

// preflightGitHubRetryDelays is the wait after each retryable failure of
// the sandbox GitHub API connectivity check, before the next attempt.
// Three attempts total: the initial try plus one retry after each delay.
var preflightGitHubRetryDelays = []time.Duration{
	2 * time.Second,
	5 * time.Second,
}

// preflightGitHubSleep is the wait between retryable connectivity-check
// failures. Tests replace it to skip real delays and to record the waits.
var preflightGitHubSleep = time.Sleep

// preflightGitHubExec runs commands inside the sandbox for the connectivity
// check. Tests replace it with a fake runner.
var preflightGitHubExec sandboxExecFunc = sandbox.Exec

// preflightGitHubResult captures the outcome of a sandbox-side GitHub API
// connectivity check.
type preflightGitHubResult struct {
	// Skipped is true when the check could not run (e.g., GH_TOKEN not set
	// or gh not on PATH inside the sandbox).
	Skipped bool
	// SkipReason explains why the check was skipped.
	SkipReason string
}

// checkSandboxGitHubConnectivity runs a lightweight GitHub API check inside
// the sandbox to verify that api.github.com is reachable through the proxy.
//
// The check sources the sandbox .env file (to pick up GH_TOKEN and PATH),
// then calls `gh api /rate_limit`. This validates both network connectivity
// (the HTTPS CONNECT tunnel through the proxy) and token validity in a
// single low-cost API call.
//
// Transient proxy 403s, connection-refused, and timeout failures are retried
// with backoff (see preflightGitHubRetryDelays). Auth and config failures
// (HTTP 401/404) fail immediately. printer, when non-nil, logs each retry so
// self-resolving transients remain visible in the run log.
//
// Returns a non-nil error when the API is unreachable (proxy 403, connection
// refused, DNS failure, etc.). Returns a nil error with Skipped=true when the
// check cannot run (no GH_TOKEN or no gh binary). Callers should treat a
// non-nil error as fatal — the agent will waste its entire timeout retrying
// doomed API calls. See #2143, #6855.
func checkSandboxGitHubConnectivity(sandboxName string, printer *ui.Printer) (*preflightGitHubResult, error) {
	envFile := sandbox.SandboxWorkspace + "/.env"

	// First check whether GH_TOKEN is set and gh is available. If neither is
	// present the agent does not need GitHub API access — skip silently.
	probeCmd := fmt.Sprintf(". %s 2>/dev/null; "+
		"if [ -z \"${GH_TOKEN:-}\" ]; then echo NOTOKEN; exit 0; fi; "+
		"if ! command -v gh >/dev/null 2>&1; then echo NOGH; exit 0; fi; "+
		"echo OK", envFile)

	stdout, _, exitCode, err := preflightGitHubExec(sandboxName, probeCmd, 10*time.Second)
	if err != nil {
		return &preflightGitHubResult{Skipped: true, SkipReason: "probe command failed: " + err.Error()}, nil
	}
	probe := strings.TrimSpace(stdout)
	if exitCode != 0 || probe == "NOTOKEN" {
		return &preflightGitHubResult{Skipped: true, SkipReason: "GH_TOKEN not set in sandbox"}, nil
	}
	if probe == "NOGH" {
		return &preflightGitHubResult{Skipped: true, SkipReason: "gh CLI not available in sandbox"}, nil
	}

	// GH_TOKEN is set and gh is available — test actual connectivity.
	// Use /rate_limit as the lightest authenticated endpoint.
	checkCmd := fmt.Sprintf(". %s 2>/dev/null && gh api /rate_limit --silent 2>&1", envFile)
	maxAttempts := len(preflightGitHubRetryDelays) + 1
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		stdout, stderr, exitCode, err := preflightGitHubExec(sandboxName, checkCmd, preflightGitHubTimeout)
		if err == nil && exitCode == 0 {
			return &preflightGitHubResult{}, nil
		}
		output := strings.TrimSpace(stdout + "\n" + stderr)
		lastErr = diagnosePreflightGitHubFailure(exitCode, err, output)
		if !isRetryablePreflightGitHubFailure(exitCode, err, output) {
			return nil, lastErr
		}
		if attempt == maxAttempts {
			break
		}
		delay := preflightGitHubRetryDelays[attempt-1]
		if printer != nil {
			printer.StepWarn(fmt.Sprintf(
				"GitHub API connectivity check failed (attempt %d/%d): %s — retrying in %s",
				attempt, maxAttempts, preflightGitHubRetryReason(exitCode, err, output), delay))
		}
		preflightGitHubSleep(delay)
	}
	return nil, fmt.Errorf("GitHub API connectivity check failed after %d attempts: %w", maxAttempts, lastErr)
}

// diagnosePreflightGitHubFailure turns a failed gh api /rate_limit invocation
// into the user-facing error returned after retries are exhausted (or skipped).
func diagnosePreflightGitHubFailure(exitCode int, err error, output string) error {
	if err != nil {
		return fmt.Errorf("GitHub API connectivity check failed: %w", err)
	}
	if strings.Contains(output, "403") || strings.Contains(output, "Forbidden") {
		return fmt.Errorf(
			"GitHub API unreachable from sandbox (HTTP 403 — proxy allowlist issue):\n%s\n\n"+
				"The sandbox proxy is blocking HTTPS CONNECT to api.github.com. "+
				"Check the OpenShell gateway network policy and proxy allowlist configuration",
			output)
	}
	if strings.Contains(output, "Could not resolve host") || strings.Contains(output, "Name or service not known") {
		return fmt.Errorf(
			"GitHub API unreachable from sandbox (DNS resolution failed):\n%s\n\n"+
				"The sandbox cannot resolve api.github.com. "+
				"Check DNS configuration and network policies",
			output)
	}
	if strings.Contains(output, "Connection refused") || strings.Contains(output, "Connection timed out") {
		return fmt.Errorf(
			"GitHub API unreachable from sandbox (connection failed):\n%s\n\n"+
				"The sandbox cannot connect to api.github.com or the HTTPS proxy. "+
				"Check network policies and proxy availability",
			output)
	}

	return fmt.Errorf("GitHub API connectivity check failed (exit %d):\n%s", exitCode, output)
}

// isRetryablePreflightGitHubFailure reports whether a failed connectivity
// check should be retried. HTTP 403 and network-level errors (connection
// refused, timeout) are transient proxy/gateway issues. HTTP 401/404 indicate
// real auth or config problems and must not be retried. See #6855.
func isRetryablePreflightGitHubFailure(exitCode int, err error, output string) bool {
	combined := output
	if err != nil {
		combined = strings.TrimSpace(combined + "\n" + err.Error())
	}
	// Auth/config failures take precedence even if the output also matches
	// a retryable substring.
	if strings.Contains(combined, "401") || strings.Contains(combined, "Unauthorized") {
		return false
	}
	if strings.Contains(combined, "404") {
		return false
	}
	if strings.Contains(combined, "403") || strings.Contains(combined, "Forbidden") {
		return true
	}
	if strings.Contains(combined, "Connection refused") || strings.Contains(combined, "connection refused") {
		return true
	}
	if strings.Contains(combined, "Connection timed out") || strings.Contains(combined, "connection timed out") {
		return true
	}
	if exitCode == 124 || strings.Contains(combined, "timed out") {
		return true
	}
	return false
}

// preflightGitHubRetryReason is a short label for the retry log line.
func preflightGitHubRetryReason(exitCode int, err error, output string) string {
	combined := output
	if err != nil {
		combined = strings.TrimSpace(combined + "\n" + err.Error())
	}
	switch {
	case strings.Contains(combined, "403") || strings.Contains(combined, "Forbidden"):
		return "HTTP 403"
	case strings.Contains(combined, "Connection refused") || strings.Contains(combined, "connection refused"):
		return "connection refused"
	case exitCode == 124 || strings.Contains(combined, "timed out") || strings.Contains(combined, "Connection timed out"):
		return "timeout"
	default:
		return "transient network error"
	}
}
