package scaffold

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupConfigOrigin is like setupPinnedConfigOrigin but lets the caller
// control the content of .fullsend/config.yaml (the overlay) and
// .fullsend/config.base.yaml (the base) committed to the origin
// remote's main branch independently. Pass "" for either argument to
// omit that file entirely (as opposed to committing it with empty
// content), so tests can exercise the "key/file absent" fallback path
// distinctly from "file present but doesn't set the key".
func setupConfigOrigin(t *testing.T, root, configYAML, configBaseYAML string) {
	t.Helper()
	upstream := t.TempDir()
	initGitRepo(t, upstream)
	if configYAML != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(upstream, ".fullsend"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(upstream, ".fullsend/config.yaml"), []byte(configYAML), 0o644))
		gitCmd(t, upstream, "add", ".fullsend/config.yaml")
	}
	if configBaseYAML != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(upstream, ".fullsend"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(upstream, ".fullsend/config.base.yaml"), []byte(configBaseYAML), 0o644))
		gitCmd(t, upstream, "add", ".fullsend/config.base.yaml")
	}
	// A placeholder file guarantees there is always at least one
	// tracked file to commit, even in the "neither config file
	// exists" case.
	require.NoError(t, os.WriteFile(filepath.Join(upstream, "README.md"), []byte("placeholder\n"), 0o644))
	gitCmd(t, upstream, "add", "README.md")
	gitCmd(t, upstream, "commit", "-m", "trusted config")

	initGitRepo(t, root)
	gitCmd(t, root, "remote", "add", "origin", upstream)
}

func TestRunAgentJobScript_KillSwitchConsultsConfigBaseYAML(t *testing.T) {
	const secret = "test-hmac-secret"
	hmacHex := computeDispatchHMAC(secret, "5", "", "", "", "", "", "", "", "test-key", "triage", "")

	cases := []struct {
		name           string
		configYAML     string
		configBaseYAML string
		wantHalt       bool
		wantOutput     string
	}{
		{
			name:           "overlay silent, base active halts dispatch",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
			wantOutput:     "Kill switch is active",
		},
		{
			name:           "overlay explicit false overrides base active",
			configYAML:     "kill_switch: false\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       false,
		},
		{
			name:     "neither file sets kill_switch",
			wantHalt: false,
		},
		{
			name:       "malformed overlay logs a warning and falls through",
			configYAML: "kill_switch: [unterminated\n",
			wantHalt:   false,
			wantOutput: "WARNING: invalid .fullsend/config.yaml or .fullsend/config.base.yaml — treating as unconfigured",
		},
		{
			name:           "overlay kill_switch: null falls through to base",
			configYAML:     "kill_switch: null\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
			wantOutput:     "Kill switch is active",
		},
		{
			name:           "overlay true survives a malformed base file",
			configYAML:     "kill_switch: true\n",
			configBaseYAML: "kill_switch: [unterminated\n",
			wantHalt:       true,
			wantOutput:     "Kill switch is active",
		},
		{
			name:       "overlay yes (bare) halts dispatch",
			configYAML: "kill_switch: yes\n",
			wantHalt:   true,
			wantOutput: "Kill switch is active",
		},
		{
			name:       `overlay "yes" (quoted) halts dispatch`,
			configYAML: "kill_switch: \"yes\"\n",
			wantHalt:   true,
			wantOutput: "Kill switch is active",
		},
		{
			name:       "overlay no does not halt dispatch",
			configYAML: "kill_switch: no\n",
			wantHalt:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
			writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
			writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
			script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
			})
			mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
			})
			mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				switch {
				case strings.Contains(path, "/repository/branches/"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
				case strings.Contains(path, "/pipelines/"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
				case strings.Contains(path, "/resource_groups/"):
					w.WriteHeader(http.StatusOK)
				case strings.Contains(path, "/members/all/"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"access_level":40}`))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`))
				}
			})
			srv := httptest.NewTLSServer(mux)
			t.Cleanup(srv.Close)

			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte("#!/bin/sh\nexit 0\n"), 0o755))

			setupConfigOrigin(t, root, tc.configYAML, tc.configBaseYAML)

			cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
			cmd.Dir = root
			cmd.Env = append([]string{
				"SCRIPT=" + script,
				"PATH=" + bin + ":" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
				"CI_PROJECT_DIR=" + root,
				"RUNNER_TEMP=" + t.TempDir(),
				"CI_JOB_TOKEN=job-token",
				"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
				"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
				"STAGE=triage",
				"RESOURCE_KEY=test-key",
				"ACTOR_ID=5",
				"FULLSEND_DISPATCH_SECRET=" + secret,
				"FULLSEND_DISPATCH_HMAC=" + hmacHex,
				"CI_PROJECT_PATH=group/project",
				"CI_SERVER_URL=https://gitlab.example",
				"CI_PIPELINE_URL=https://gitlab.example/group/project/-/pipelines/100",
				"CI_PROJECT_ID=1",
			}, pinTLSEnv(t, srv)...)
			out, err := cmd.CombinedOutput()

			if tc.wantHalt {
				require.Error(t, err, "stdout/stderr: %s", out)
			} else {
				require.NoError(t, err, "stdout/stderr: %s", out)
			}
			if tc.wantOutput != "" {
				assert.Contains(t, string(out), tc.wantOutput)
			}
		})
	}
}
