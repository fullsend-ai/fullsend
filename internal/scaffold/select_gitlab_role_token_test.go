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

func selectGitLabRoleTokenScript(t *testing.T) string {
	t.Helper()
	content, err := GitLabPerRepoFile(".gitlab/ci/scripts/select-gitlab-role-token.sh")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "select-gitlab-role-token.sh")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

func sourceRoleTokenScript(t *testing.T, script string, env []string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"; echo TOKEN_NAME=\"$FULLSEND_JOB_TOKEN_NAME\"; echo TOKEN=\"$FULLSEND_JOB_TOKEN\"")
	cmd.Env = append([]string{"SCRIPT=" + script, "PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return string(out), stderr.String(), err
}

func TestSelectGitLabRoleTokenBuiltins(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	for _, tc := range []struct {
		name, kind, stage, secret, token string
	}{
		{"poller", "poller", "", "FULLSEND_GITLAB_POLLER_TOKEN", "poller-pat"},
		{"analyst", "agent", "review", "FULLSEND_GITLAB_ANALYST_TOKEN", "analyst-pat"},
		{"coder", "agent", "fix", "FULLSEND_GITLAB_CODER_TOKEN", "coder-pat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{"FULLSEND_JOB_KIND=" + tc.kind, tc.secret + "=" + tc.token}
			if tc.stage != "" {
				env = append(env, "STAGE="+tc.stage)
			}
			out, stderr, err := sourceRoleTokenScript(t, script, env)
			require.NoError(t, err, "stderr: %s", stderr)
			assert.Contains(t, out, "TOKEN_NAME="+tc.secret)
			assert.Contains(t, out, "TOKEN="+tc.token)
		})
	}
}

func TestSelectGitLabRoleTokenCustomRoles(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	base := []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=scanner",
		`FULLSEND_GITLAB_ROLE_REGISTRY={"roles":[{"name":"scanner","agents":["scanner"]}]}`,
		"FULLSEND_GITLAB_ROLE_SCANNER_TOKEN=scanner-pat",
	}
	out, stderr, err := sourceRoleTokenScript(t, script, base)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_GITLAB_ROLE_SCANNER_TOKEN")

	reuse := []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=scanner",
		`FULLSEND_GITLAB_ROLE_REGISTRY={"roles":[{"name":"scanner","agents":["scanner"],"credential":"reuse","reuse":"coder"}]}`,
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
	}
	out, stderr, err = sourceRoleTokenScript(t, script, reuse)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_GITLAB_CODER_TOKEN")
}

func TestSelectGitLabRoleTokenIgnoresSharedForgeToken(t *testing.T) {
	// FULLSEND_FORGE_TOKEN is a leftover shared-token variable with no
	// runtime meaning: role credentials are selected unconditionally, and
	// a regression that fell back to the shared token — whether the role
	// secret is present or missing — must not go undetected.
	script := selectGitLabRoleTokenScript(t)

	_, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=poller",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	})
	require.Error(t, err)
	assert.Contains(t, stderr, "FULLSEND_GITLAB_POLLER_TOKEN is not set")
	assert.NotContains(t, stderr, "FULLSEND_FORGE_TOKEN is not set")

	out, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=poller",
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
	})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_GITLAB_POLLER_TOKEN")
	assert.Contains(t, out, "TOKEN=poller-pat")
	assert.NotContains(t, out, "TOKEN=shared-pat")
}

func TestSelectGitLabRoleTokenMissingOrUnregisteredFailsClosed(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	_, stderr, err := sourceRoleTokenScript(t, script, []string{"FULLSEND_JOB_KIND=poller"})
	require.Error(t, err)
	assert.Contains(t, stderr, "FULLSEND_GITLAB_POLLER_TOKEN is not set")

	_, stderr, err = sourceRoleTokenScript(t, script, []string{"FULLSEND_JOB_KIND=agent", "STAGE=e2e"})
	require.Error(t, err)
	assert.Contains(t, stderr, "GitLab role is not registered")
}
