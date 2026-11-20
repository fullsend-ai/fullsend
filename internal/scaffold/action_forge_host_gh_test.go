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

// fakeGHScript records the GH_HOST and GH_ENTERPRISE_TOKEN gh was invoked with,
// plus its arguments, and answers just enough of the API for the CLI install
// action's release steps to succeed.
const fakeGHScript = `#!/usr/bin/env bash
printf 'host=%s token=%s args=%s\n' "${GH_HOST-unset}" "${GH_ENTERPRISE_TOKEN-unset}" "$*" >> "${GH_CALLS:?}"
case "$*" in
  *releases/latest*) echo v1.2.3 ;;
  *git/tags/*) exit 1 ;;
  *tags?per_page*) echo '[]' ;;
esac
exit 0
`

// TestInstallCLIActionReleaseStepsUseJobHost checks the release-resolution and
// release-download steps of the CLI install action point gh at the job's host
// without a preconfigured GH_HOST, including the empty workflow_sha path taken
// on GitHub Enterprise Server.
func TestInstallCLIActionReleaseStepsUseJobHost(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
	const (
		relPath = ".github/actions/install-fullsend-cli/action.yml"
		token   = "job-host-token"
	)

	for _, step := range []string{"Resolve SHA to release tag", "Download pre-built binary from release"} {
		s := compositeActionStepNamed(t, relPath, step)
		assert.Equal(t, "${{ github.server_url }}", s.Env["SERVER_URL"], step)
		assert.Equal(t, "${{ inputs.github_token }}", s.Env["GH_ENTERPRISE_TOKEN"], step)
	}

	hosts := []struct {
		serverURL string
		wantHost  string
	}{
		{serverURL: "https://github.com", wantHost: "github.com"},
		{serverURL: "https://ghe.example.test/", wantHost: "ghe.example.test"},
		{serverURL: "http://localhost:8080", wantHost: "localhost:8080"},
	}

	run := func(t *testing.T, script string, serverURL string, extraEnv ...string) []string {
		t.Helper()
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		require.NoError(t, os.Mkdir(bin, 0o755))
		callsPath := filepath.Join(dir, "gh-calls")
		require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGHScript), 0o755))
		// The fake tar stands in for extraction so the install step finds a binary.
		require.NoError(t, os.WriteFile(filepath.Join(bin, "tar"),
			[]byte("#!/usr/bin/env bash\ntouch \"$4/fullsend\"\n"), 0o755))

		cmd := exec.Command("bash", "-c", script)
		cmd.Env = []string{}
		for _, kv := range os.Environ() {
			// A preconfigured host must not be what makes these steps work.
			if strings.HasPrefix(kv, "GH_HOST=") || strings.HasPrefix(kv, "GH_ENTERPRISE_TOKEN=") {
				continue
			}
			cmd.Env = append(cmd.Env, kv)
		}
		cmd.Env = append(cmd.Env,
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GH_CALLS="+callsPath,
			"GH_TOKEN="+token,
			"GH_ENTERPRISE_TOKEN="+token,
			"SERVER_URL="+serverURL,
			"WORKFLOW_REPOSITORY=example-org/fullsend",
			"RUNNER_TEMP="+dir,
			"GITHUB_PATH="+filepath.Join(dir, "github-path"),
			"GITHUB_OUTPUT="+filepath.Join(dir, "github-output"),
		)
		cmd.Env = append(cmd.Env, extraEnv...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))

		calls, err := os.ReadFile(callsPath)
		require.NoError(t, err)
		return strings.Split(strings.TrimSpace(string(calls)), "\n")
	}

	resolve := compositeActionStepNamed(t, relPath, "Resolve SHA to release tag").Run
	download := compositeActionStepNamed(t, relPath, "Download pre-built binary from release").Run

	for _, host := range hosts {
		wantPrefix := "host=" + host.wantHost + " token=" + token + " args="
		t.Run("resolve with empty workflow_sha/"+host.serverURL, func(t *testing.T) {
			calls := run(t, resolve, host.serverURL, "WORKFLOW_SHA=")
			require.Len(t, calls, 1)
			assert.Equal(t, wantPrefix+"api repos/example-org/fullsend/releases/latest --jq .tag_name", calls[0])
		})
		t.Run("resolve with workflow_sha/"+host.serverURL, func(t *testing.T) {
			calls := run(t, resolve, host.serverURL,
				"WORKFLOW_SHA=0123456789abcdef0123456789abcdef01234567")
			require.NotEmpty(t, calls)
			for _, call := range calls {
				assert.True(t, strings.HasPrefix(call, wantPrefix), call)
			}
		})
		t.Run("download/"+host.serverURL, func(t *testing.T) {
			calls := run(t, download, host.serverURL, "TAG=v1.2.3", "RUNNER_OS=Linux", "RUNNER_ARCH=X64")
			require.Len(t, calls, 1)
			assert.True(t, strings.HasPrefix(calls[0], wantPrefix+"release download v1.2.3 -R example-org/fullsend"), calls[0])
		})
	}
}
