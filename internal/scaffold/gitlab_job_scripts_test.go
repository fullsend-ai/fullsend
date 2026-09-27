package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeGitLabScript(t *testing.T, dir, relPath string) string {
	t.Helper()
	content, err := GitLabPerRepoFile(relPath)
	require.NoError(t, err)
	require.NotEmpty(t, content)
	path := filepath.Join(dir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

func TestRunPollJobScript_RejectsInvalidPollMode(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"FULLSEND_POLL_MODE=bogus",
		"CI_API_V4_URL=https://gitlab.example/api/v4",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=https://gitlab.example",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "FULLSEND_POLL_MODE must be 'slash' or 'events'")
}

func TestRunPollJobScript_DebugTraceAborts(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
}

func TestRunPollJobScript_BlanksSiblingSecretsBeforePoll(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	bin := t.TempDir()
	writeStub := func(name, body string) {
		path := filepath.Join(bin, name)
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	}
	writeStub("curl", "exit 0\n")
	writeStub("fullsend", "echo POLL_RAN\nexit 0\n")

	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"; echo ANALYST="${FULLSEND_GITLAB_ANALYST_TOKEN-unset}"; echo CODER="${FULLSEND_GITLAB_CODER_TOKEN-unset}"; echo SHARED="${FULLSEND_FORGE_TOKEN-unset}"; echo JOB="${FULLSEND_JOB_TOKEN-unset}"`)
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
		"FULLSEND_GITLAB_POLLER_TOKEN=poll-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"FULLSEND_POLL_MODE=events",
		"CI_API_V4_URL=https://gitlab.example/api/v4",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=https://gitlab.example",
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)
	assert.Contains(t, got, "POLL_RAN")
	assert.Contains(t, got, "ANALYST=unset")
	assert.Contains(t, got, "CODER=unset")
	assert.Contains(t, got, "SHARED=unset")
	assert.Contains(t, got, "JOB=poll-pat")
}

func TestRunAgentJobScript_DebugTraceAborts(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
}

func TestInstallFullsendCLIScript_DebugTraceAborts(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	script := writeGitLabScript(t, root, gitlabInstallCLIScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
}
