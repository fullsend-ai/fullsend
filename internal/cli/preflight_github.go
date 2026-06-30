package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

const (
	// preflightGitHubTimeout is the maximum time to wait for the GitHub API
	// connectivity check inside the sandbox. Short because this is a fast
	// pre-flight — if the proxy is blocking, the connection attempt fails
	// quickly (HTTP 403 on CONNECT).
	preflightGitHubTimeout = 30 * time.Second
)

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
// Returns a non-nil error when the API is unreachable (proxy 403, connection
// refused, DNS failure, etc.). Returns a nil error with Skipped=true when the
// check cannot run (no GH_TOKEN or no gh binary). Callers should treat a
// non-nil error as fatal — the agent will waste its entire timeout retrying
// doomed API calls. See #2143.
func checkSandboxGitHubConnectivity(sandboxName string) (*preflightGitHubResult, error) {
	envFile := sandbox.SandboxWorkspace + "/.env"

	// First check whether GH_TOKEN is set and gh is available. If neither is
	// present the agent does not need GitHub API access — skip silently.
	probeCmd := fmt.Sprintf(". %s 2>/dev/null; "+
		"if [ -z \"${GH_TOKEN:-}\" ]; then echo NOTOKEN; exit 0; fi; "+
		"if ! command -v gh >/dev/null 2>&1; then echo NOGH; exit 0; fi; "+
		"echo OK", envFile)

	stdout, _, exitCode, err := sandbox.Exec(sandboxName, probeCmd, 10*time.Second)
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
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, checkCmd, preflightGitHubTimeout)
	if err != nil {
		return nil, fmt.Errorf("GitHub API connectivity check failed: %w", err)
	}
	if exitCode == 0 {
		return &preflightGitHubResult{}, nil
	}

	// Non-zero exit — diagnose the failure.
	output := strings.TrimSpace(stdout + "\n" + stderr)
	if strings.Contains(output, "403") || strings.Contains(output, "Forbidden") {
		return nil, fmt.Errorf(
			"GitHub API unreachable from sandbox (HTTP 403 — proxy allowlist issue):\n%s\n\n"+
				"The sandbox proxy is blocking HTTPS CONNECT to api.github.com. "+
				"Check the OpenShell gateway network policy and proxy allowlist configuration",
			output)
	}
	if strings.Contains(output, "Could not resolve host") || strings.Contains(output, "Name or service not known") {
		return nil, fmt.Errorf(
			"GitHub API unreachable from sandbox (DNS resolution failed):\n%s\n\n"+
				"The sandbox cannot resolve api.github.com. "+
				"Check DNS configuration and network policies",
			output)
	}
	if strings.Contains(output, "Connection refused") || strings.Contains(output, "Connection timed out") {
		return nil, fmt.Errorf(
			"GitHub API unreachable from sandbox (connection failed):\n%s\n\n"+
				"The sandbox cannot connect to api.github.com or the HTTPS proxy. "+
				"Check network policies and proxy availability",
			output)
	}

	return nil, fmt.Errorf("GitHub API connectivity check failed (exit %d):\n%s", exitCode, output)
}
