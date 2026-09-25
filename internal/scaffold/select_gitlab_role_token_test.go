package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

func selectGitLabRoleTokenScript(t *testing.T) string {
	t.Helper()
	content, err := GitLabPerRepoFile(".gitlab/ci/scripts/select-gitlab-role-token.sh")
	require.NoError(t, err)
	require.NotEmpty(t, content)
	path := filepath.Join(t.TempDir(), "select-gitlab-role-token.sh")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

func sourceRoleTokenScript(t *testing.T, script string, env []string) (stdout string, stderr string, err error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"; echo TOKEN_NAME=\"$FULLSEND_JOB_TOKEN_NAME\"; echo TOKEN=\"$FULLSEND_JOB_TOKEN\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
	}, env...)
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	out, runErr := cmd.Output()
	return string(out), stderrBuf.String(), runErr
}

func TestSelectGitLabRoleToken_DisabledUsesShared(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	out, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=poller",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_FORGE_TOKEN")
	assert.Contains(t, out, "TOKEN=shared-pat")
}

func TestSelectGitLabRoleToken_RollbackUsesShared(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	out, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=review",
		"FULLSEND_GITLAB_ROLE_MIGRATION=rollback",
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
	})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_FORGE_TOKEN")
	assert.Contains(t, out, "TOKEN=shared-pat")
}

func TestSelectGitLabRoleToken_EnforcedPoller(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	out, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=poller",
		"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_GITLAB_POLLER_TOKEN")
	assert.Contains(t, out, "TOKEN=poller-pat")
	assert.NotContains(t, out, "TOKEN=shared-pat")
}

func TestSelectGitLabRoleToken_EnforcedAnalystAndCoder(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	cases := []struct {
		stage  string
		secret string
		token  string
	}{
		{stage: "review", secret: "FULLSEND_GITLAB_ANALYST_TOKEN", token: "analyst-pat"},
		{stage: "triage", secret: "FULLSEND_GITLAB_ANALYST_TOKEN", token: "analyst-pat"},
		{stage: "code", secret: "FULLSEND_GITLAB_CODER_TOKEN", token: "coder-pat"},
		{stage: "fix", secret: "FULLSEND_GITLAB_CODER_TOKEN", token: "coder-pat"},
	}
	for _, tc := range cases {
		t.Run(tc.stage, func(t *testing.T) {
			out, stderr, err := sourceRoleTokenScript(t, script, []string{
				"FULLSEND_JOB_KIND=agent",
				"STAGE=" + tc.stage,
				"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
				"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
				"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
				"FULLSEND_FORGE_TOKEN=shared-pat",
			})
			require.NoError(t, err, "stderr: %s", stderr)
			assert.Contains(t, out, "TOKEN_NAME="+tc.secret)
			assert.Contains(t, out, "TOKEN="+tc.token)
		})
	}
}

func TestSelectGitLabRoleToken_EnforcedMissingRoleSecretFails(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	_, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=poller",
		"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	})
	require.Error(t, err)
	assert.Contains(t, stderr, "FULLSEND_GITLAB_POLLER_TOKEN is not set")
	assert.NotContains(t, stderr, "FULLSEND_FORGE_TOKEN is not set")
}

func TestSelectGitLabRoleToken_MigratingFallsBackToShared(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	out, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=code",
		"FULLSEND_GITLAB_ROLE_MIGRATION=migrating",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_FORGE_TOKEN")
	assert.Contains(t, out, "TOKEN=shared-pat")
}

func TestSelectGitLabRoleToken_DisabledMissingSharedFails(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	_, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=poller",
	})
	require.Error(t, err)
	assert.Contains(t, stderr, "FULLSEND_FORGE_TOKEN is not set")
}

func TestSelectGitLabRoleToken_UnregisteredAgentFailsClosed(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	_, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=e2e",
		"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	})
	require.Error(t, err)
	assert.Contains(t, stderr, "GitLab role is not registered")
}

func TestSelectGitLabRoleToken_CustomRoleFromRegistry(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	out, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=scanner",
		"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
		`FULLSEND_GITLAB_ROLE_REGISTRY={"roles":[{"name":"scanner","agents":["scanner"]}]}`,
		"FULLSEND_GITLAB_ROLE_SCANNER_TOKEN=scanner-pat",
	})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_GITLAB_ROLE_SCANNER_TOKEN")
	assert.Contains(t, out, "TOKEN=scanner-pat")
}

func TestSelectGitLabRoleToken_CustomRoleReusesBuiltin(t *testing.T) {
	// Regression test: a custom role whose credential reuses a builtin
	// (poller/analyst/coder) must resolve to the builtin's own secret
	// name, not a derived FULLSEND_GITLAB_ROLE_<NAME>_TOKEN that is never
	// set. See internal/gitlabroles/registry.go's resolveReuse for the Go
	// equivalent this script must mirror.
	script := selectGitLabRoleTokenScript(t)
	out, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=scanner",
		"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
		`FULLSEND_GITLAB_ROLE_REGISTRY={"roles":[{"name":"scanner","agents":["scanner"],"credential":"reuse","reuse":"coder"}]}`,
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
	})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, out, "TOKEN_NAME=FULLSEND_GITLAB_CODER_TOKEN")
	assert.Contains(t, out, "TOKEN=coder-pat")
	assert.NotContains(t, out, "TOKEN_NAME=FULLSEND_GITLAB_ROLE_SCANNER_TOKEN")
}

func TestSelectGitLabRoleToken_ReuseOfUnregisteredRoleFailsClosed(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	_, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=agent",
		"STAGE=scanner",
		"FULLSEND_GITLAB_ROLE_MIGRATION=enforced",
		`FULLSEND_GITLAB_ROLE_REGISTRY={"roles":[{"name":"scanner","agents":["scanner"],"credential":"reuse","reuse":"ghost"}]}`,
	})
	require.Error(t, err)
	assert.Contains(t, stderr, "reuses unregistered role")
}

func TestSelectGitLabRoleToken_MatchesGoRegistryResolution(t *testing.T) {
	// Table-driven cross-check: the script's resolved secret name must
	// agree with internal/gitlabroles.Select/SelectAgent for the same
	// inputs, so the bash/python reimplementation cannot silently drift
	// from the Go implementation it mirrors.
	script := selectGitLabRoleTokenScript(t)
	cases := []struct {
		name     string
		poller   bool
		stage    string
		registry string
		secrets  map[string]string
	}{
		{
			name:    "builtin poller",
			poller:  true,
			secrets: map[string]string{"FULLSEND_GITLAB_POLLER_TOKEN": "poller-pat"},
		},
		{
			name:    "builtin coder via alias",
			stage:   "fix",
			secrets: map[string]string{"FULLSEND_GITLAB_CODER_TOKEN": "coder-pat"},
		},
		{
			name:     "custom own role",
			stage:    "scanner",
			registry: `{"roles":[{"name":"scanner","agents":["scanner"]}]}`,
			secrets:  map[string]string{"FULLSEND_GITLAB_ROLE_SCANNER_TOKEN": "scanner-pat"},
		},
		{
			name:     "custom role reuses builtin",
			stage:    "scanner",
			registry: `{"roles":[{"name":"scanner","agents":["scanner"],"credential":"reuse","reuse":"coder"}]}`,
			secrets:  map[string]string{"FULLSEND_GITLAB_CODER_TOKEN": "coder-pat"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"FULLSEND_GITLAB_ROLE_MIGRATION": "enforced",
			}
			for k, v := range tc.secrets {
				env[k] = v
			}
			if tc.registry != "" {
				env["FULLSEND_GITLAB_ROLE_REGISTRY"] = tc.registry
			}
			getenv := func(k string) string { return env[k] }

			var sel gitlabroles.Selection
			var selErr error
			scriptEnv := []string{"FULLSEND_JOB_KIND=poller"}
			if tc.poller {
				sel, selErr = gitlabroles.Select(gitlabroles.PollerJob(), getenv)
			} else {
				scriptEnv = []string{
					"FULLSEND_JOB_KIND=agent",
					"STAGE=" + tc.stage,
				}
				sel, selErr = gitlabroles.SelectAgent(tc.stage, "", getenv)
			}
			require.NoError(t, selErr)

			scriptEnv = append(scriptEnv, "FULLSEND_GITLAB_ROLE_MIGRATION=enforced")
			for k, v := range tc.secrets {
				scriptEnv = append(scriptEnv, k+"="+v)
			}
			if tc.registry != "" {
				scriptEnv = append(scriptEnv, "FULLSEND_GITLAB_ROLE_REGISTRY="+tc.registry)
			}

			out, stderr, err := sourceRoleTokenScript(t, script, scriptEnv)
			require.NoError(t, err, "stderr: %s", stderr)
			assert.Contains(t, out, "TOKEN_NAME="+sel.Source.SecretName)
		})
	}
}

func TestSelectGitLabRoleToken_InvalidModeFailsClosed(t *testing.T) {
	script := selectGitLabRoleTokenScript(t)
	_, stderr, err := sourceRoleTokenScript(t, script, []string{
		"FULLSEND_JOB_KIND=poller",
		"FULLSEND_GITLAB_ROLE_MIGRATION=nope",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	})
	require.Error(t, err)
	assert.Contains(t, stderr, "invalid GitLab role migration mode")
}
