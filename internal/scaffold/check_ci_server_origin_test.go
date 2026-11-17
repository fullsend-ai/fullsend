package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckCIServerOrigin calls fullsend_check_ci_server_origin directly
// (#9465). The function is defined inline in run-agent-job.sh, so the test
// extracts the text between its BEGIN and END marker lines and sources that
// with the bash found on PATH, so the same test can run under bash 3.2. Scheme and authority compare
// case-insensitively, the relative URL root byte-for-byte, and a trailing
// "/" is ignored. A mismatch prints one ERROR line without any value.
func TestCheckCIServerOrigin(t *testing.T) {
	const (
		wantErr = "ERROR: CI_SERVER_URL or CI_SERVER_HOST disagrees with the pinned GitLab URL (FULLSEND_PINNED_GITLAB_URL)"
		ipv6    = "https://[2001:db8::1]:8443"
		ipv6NP  = "https://[2001:db8::1]"
	)
	tests := []struct {
		name       string
		pinned     string
		serverURL  string
		serverHost string
		wantPass   bool
	}{
		{"gitlab.com match", "https://gitlab.com", "https://gitlab.com", "gitlab.com", true},
		{"host-only URL with trailing slash", "https://gitlab.com", "https://gitlab.com/", "gitlab.com", true},
		{"uppercase DNS host in both variables", "https://gitlab.example.com", "HTTPS://GitLab.Example.COM", "GITLAB.example.com", true},
		{"non-default port match", "https://gitlab.example.com:8443", "https://gitlab.example.com:8443", "gitlab.example.com", true},
		{"non-default port with trailing slash", "https://gitlab.example.com:8443", "https://gitlab.example.com:8443/", "gitlab.example.com", true},
		{"port mismatch", "https://gitlab.example.com:8443", "https://gitlab.example.com:9443", "gitlab.example.com", false},
		{"port missing from CI_SERVER_URL", "https://gitlab.example.com:8443", "https://gitlab.example.com", "gitlab.example.com", false},
		{"relative URL root match", "https://gitlab.example.com/gitlab", "https://gitlab.example.com/gitlab", "gitlab.example.com", true},
		{"relative URL root with trailing slash", "https://gitlab.example.com/gitlab", "https://gitlab.example.com/gitlab/", "gitlab.example.com", true},
		{"relative URL root with uppercase host", "https://gitlab.example.com/gitlab", "https://GITLAB.example.com/gitlab", "gitlab.example.com", true},
		{"relative URL root case differs", "https://gitlab.example.com/gitlab", "https://gitlab.example.com/GitLab", "gitlab.example.com", false},
		{"relative URL root missing", "https://gitlab.example.com/gitlab", "https://gitlab.example.com", "gitlab.example.com", false},
		{"relative URL root added", "https://gitlab.example.com", "https://gitlab.example.com/gitlab", "gitlab.example.com", false},
		{"IPv6 with port, CI_SERVER_HOST bracketed", ipv6, ipv6, "[2001:db8::1]", true},
		{"IPv6 with port, CI_SERVER_HOST unbracketed", ipv6, ipv6, "2001:db8::1", true},
		{"IPv6 with port, trailing slash", ipv6, ipv6 + "/", "2001:db8::1", true},
		{"IPv6 without port, CI_SERVER_HOST bracketed", ipv6NP, ipv6NP, "[2001:db8::1]", true},
		{"IPv6 without port, CI_SERVER_HOST unbracketed", ipv6NP, ipv6NP, "2001:db8::1", true},
		{"IPv6 without port, trailing slash", ipv6NP, ipv6NP + "/", "[2001:db8::1]", true},
		{"IPv6 uppercase hex", "https://[2001:db8::a]", "https://[2001:DB8::A]", "2001:DB8::A", true},
		{"IPv6 different address", ipv6, ipv6, "2001:db8::2", false},
		{"DNS host given with brackets", "https://gitlab.com", "https://gitlab.com", "[gitlab.com]", false},
		{"DNS host with half a bracket", "https://gitlab.com", "https://gitlab.com", "gitlab.com]", false},
		{"http instead of https", "https://gitlab.com", "http://gitlab.com", "gitlab.com", false},
		{"CI_SERVER_HOST with port", "https://gitlab.example.com:8443", "https://gitlab.example.com:8443", "gitlab.example.com:8443", false},
		{"CI_SERVER_URL on another host", "https://gitlab.com", "https://other.example", "gitlab.com", false},
		{"CI_SERVER_HOST on another host", "https://gitlab.com", "https://gitlab.com", "other.example", false},
		{"userinfo in CI_SERVER_URL", "https://gitlab.com", "https://user@gitlab.com", "gitlab.com", false},
		{"CI_SERVER_URL without scheme", "https://gitlab.com", "gitlab.com", "gitlab.com", false},
		{"empty CI_SERVER_URL", "https://gitlab.com", "", "gitlab.com", false},
		{"empty CI_SERVER_HOST", "https://gitlab.com", "https://gitlab.com", "", false},
		{"empty pinned URL", "", "https://gitlab.com", "gitlab.com", false},
	}

	helper := filepath.Join(t.TempDir(), "check-ci-server-origin.sh")
	require.NoError(t, os.WriteFile(helper, []byte(ciServerOriginCheckBlock(t)), 0o644))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$HELPER"; fullsend_check_ci_server_origin`)
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"HELPER=" + helper,
				"FULLSEND_PINNED_GITLAB_URL=" + tt.pinned,
				"CI_SERVER_URL=" + tt.serverURL,
				"CI_SERVER_HOST=" + tt.serverHost,
			}
			out, err := cmd.CombinedOutput()
			got := string(out)
			if tt.wantPass {
				require.NoError(t, err, "output: %s", got)
				assert.Empty(t, got)
				return
			}
			require.Error(t, err, "output: %s", got)
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, 1, exitErr.ExitCode())
			assert.Equal(t, 1, strings.Count(got, "\n"), "one line only: %q", got)
			assert.Contains(t, got, wantErr)
			assert.NotContains(t, got, "bad substitution")
			for _, v := range []string{tt.pinned, tt.serverURL, tt.serverHost} {
				if v != "" {
					assert.NotContains(t, got, v, "the error must not print supplied values")
				}
			}
		})
	}
}

// ciServerOriginCheckBlock returns the lines of the scaffold run-agent-job.sh
// from the BEGIN to the END fullsend_check_ci_server_origin marker, failing
// the test unless each marker appears exactly once, in that order.
func ciServerOriginCheckBlock(t *testing.T) string {
	t.Helper()
	const (
		begin = "# BEGIN fullsend_check_ci_server_origin"
		end   = "# END fullsend_check_ci_server_origin"
	)
	content, err := GitLabPerRepoFile(gitlabRunAgentJobScriptPath)
	require.NoError(t, err)
	lines := strings.Split(string(content), "\n")
	beginIdx, endIdx := -1, -1
	for i, line := range lines {
		switch line {
		case begin:
			require.Equal(t, -1, beginIdx, "%q appears more than once", begin)
			beginIdx = i
		case end:
			require.Equal(t, -1, endIdx, "%q appears more than once", end)
			endIdx = i
		}
	}
	require.NotEqual(t, -1, beginIdx, "missing %q", begin)
	require.NotEqual(t, -1, endIdx, "missing %q", end)
	require.Less(t, beginIdx, endIdx, "%q must come before %q", begin, end)
	block := strings.Join(lines[beginIdx:endIdx+1], "\n") + "\n"
	require.Contains(t, block, "fullsend_check_ci_server_origin() {")
	return block
}
