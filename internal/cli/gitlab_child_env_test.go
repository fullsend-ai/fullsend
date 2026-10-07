package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// envKeys returns the variable names present in an exec env slice.
func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			keys = append(keys, e[:i])
		}
	}
	return keys
}

// execEnvMap folds an exec env slice into a map, last value wins like os/exec.
func execEnvMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	return m
}

// gitlabStrippedCredentials are fullsend's own GitLab credentials that no
// host-side script may see (#8146).
var gitlabStrippedCredentials = []string{
	"FULLSEND_ID_TOKEN",
	"GCP_OIDC_TOKEN_FILE",
	"FULLSEND_JOB_TOKEN",
	forge.SecretForgeToken,
	forge.SecretGitLabPollerToken,
	forge.SecretGitLabAnalystToken,
	forge.SecretGitLabCoderToken,
	gitlabroles.CustomSecretName("foo"),
}

// setGitLabAgentJobEnv simulates the process environment of a GitLab agent
// job after applyGitLabRoleSelection picked the Analyst role.
func setGitLabAgentJobEnv(t *testing.T) {
	t.Helper()
	for _, key := range gitlabStrippedCredentials {
		t.Setenv(key, "value-of-"+key)
	}
	t.Setenv(forge.SecretGitLabAnalystToken, "analyst-pat")
	t.Setenv("GITLAB_TOKEN", "analyst-pat")
	t.Setenv("PUSH_TOKEN", "analyst-pat")
	t.Setenv(envGitLabRole, "analyst")
	t.Setenv(envGitLabRoleSecret, forge.SecretGitLabAnalystToken)
	t.Setenv(envGitLabRoleSource, "role")
	t.Setenv(forge.VarGitLabRoleRegistry, "")
	t.Setenv("FULLSEND_JOB_TOKEN_NAME", forge.SecretGitLabAnalystToken)
	t.Setenv("JIRA_API_TOKEN", "user-jira-token")
}

// TestChildScriptEnv_StripsGitLabCredentials covers every host-side script
// path: pre-script and preflight (childScriptEnv), post-script
// (postScriptEnv) and validation (stripOIDCEnv) (#8146).
func TestChildScriptEnv_StripsGitLabCredentials(t *testing.T) {
	setGitLabAgentJobEnv(t)
	runnerEnv := map[string]string{
		"JIRA_TOKEN":         "user-jira-token",
		"FULLSEND_JOB_TOKEN": "smuggled",
	}
	h := &harness.Harness{RunnerEnv: runnerEnv}

	paths := map[string][]string{
		"pre-script":  childScriptEnv(runnerEnv, ""),
		"post-script": postScriptEnv(h, ""),
		"validation":  stripOIDCEnv(append(os.Environ(), validationEnv(h, "", "/run")...)),
	}
	for name, env := range paths {
		t.Run(name, func(t *testing.T) {
			keys := envKeys(env)
			for _, key := range gitlabStrippedCredentials {
				assert.NotContains(t, keys, key, "%s must not reach a host-side script", key)
			}
			got := execEnvMap(env)
			assert.Equal(t, "analyst-pat", got["GITLAB_TOKEN"], "the selected role's credential still arrives as GITLAB_TOKEN")
			assert.Equal(t, "analyst-pat", got["PUSH_TOKEN"], "the selected role's credential still arrives as PUSH_TOKEN")
			assert.Equal(t, "user-jira-token", got["JIRA_TOKEN"], "user env.runner values still arrive")
			assert.Equal(t, "user-jira-token", got["JIRA_API_TOKEN"], "user CI/CD variables still arrive")
			assert.Equal(t, "analyst", got[envGitLabRole], "non-secret routing vars still arrive")
			assert.Equal(t, forge.SecretGitLabAnalystToken, got[envGitLabRoleSecret])
			assert.Equal(t, forge.SecretGitLabAnalystToken, got["FULLSEND_JOB_TOKEN_NAME"])
		})
	}
}

// On GitHub no GitLab role selection runs, so GCP_OIDC_TOKEN_FILE keeps
// reaching pre-scripts (#7689 tracks the GitHub side).
func TestChildScriptEnv_GitHubKeepsGCPOIDCTokenFile(t *testing.T) {
	t.Setenv(envGitLabRole, "")
	t.Setenv("GCP_OIDC_TOKEN_FILE", "/tmp/gcp-oidc-token.json")

	env := childScriptEnv(nil, "")
	assert.Equal(t, "/tmp/gcp-oidc-token.json", envLast(env, "GCP_OIDC_TOKEN_FILE"))
	assert.Equal(t, "/tmp/gcp-oidc-token.json", envLast(stripOIDCEnv(os.Environ()), "GCP_OIDC_TOKEN_FILE"))
}

func TestHarnessExpansionDenied_GitLabCredentials(t *testing.T) {
	for _, key := range []string{
		"FULLSEND_ID_TOKEN",
		"FULLSEND_JOB_TOKEN",
		forge.SecretForgeToken,
		forge.SecretGitLabPollerToken,
		forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
		forge.VarGitLabBotToken,
		gitlabroles.CustomSecretName("foo-bar"),
	} {
		t.Run(key, func(t *testing.T) {
			const value = "gitlab-credential-value"
			t.Setenv(key, value)
			assert.True(t, harnessExpansionDenied(key))
			assert.Empty(t, harnessEnvExpand(key))
			_, ok := harnessEnvLookup(key)
			assert.False(t, ok, "harness validation must reject a reference to %s", key)
			assert.NotContains(t, safeExpandEnv("x-${"+key+"}-y"), value)
			assert.NotContains(t, shellSafeExpandEnv("x-${"+key+"}-y"), value)
		})
	}
	assert.True(t, reservedSandboxKeys["FULLSEND_ID_TOKEN"], "env.sandbox must not inject the GitLab OIDC token")

	// GCP_OIDC_TOKEN_FILE stays expandable for host_files; the selected
	// credential and routing labels stay usable.
	for _, key := range []string{
		"GCP_OIDC_TOKEN_FILE",
		"GITLAB_TOKEN",
		"PUSH_TOKEN",
		envGitLabRole,
		envGitLabRoleSecret,
		envGitLabRoleSource,
		forge.VarGitLabRoleRegistry,
		"FULLSEND_JOB_TOKEN_NAME",
		"JIRA_API_TOKEN",
	} {
		assert.False(t, harnessExpansionDenied(key), "%s must stay expandable", key)
	}
}

// TestValidateRunnerEnv_RefusesGitLabCredentials checks the harness
// validation that runAgent performs with harnessEnvLookup (#8146).
func TestValidateRunnerEnv_RefusesGitLabCredentials(t *testing.T) {
	setGitLabAgentJobEnv(t)

	refused := &harness.Harness{
		Env: &harness.EnvConfig{
			Runner:  map[string]string{"COPY": "${FULLSEND_JOB_TOKEN}"},
			Sandbox: map[string]string{"COPY": "${FULLSEND_GITLAB_CODER_TOKEN}"},
		},
		HostFiles: []harness.HostFile{{Src: "${FULLSEND_ID_TOKEN}", Dest: "/sandbox/x", Expand: true}},
	}
	err := refused.ValidateRunnerEnvWith(harnessEnvLookup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_JOB_TOKEN is not set (referenced by env.runner[COPY])")
	assert.Contains(t, err.Error(), "FULLSEND_GITLAB_CODER_TOKEN is not set (referenced by env.sandbox[COPY])")
	assert.Contains(t, err.Error(), "FULLSEND_ID_TOKEN is not set (referenced by host_files[0].src)")

	allowed := &harness.Harness{
		Env: &harness.EnvConfig{
			Runner: map[string]string{"JIRA": "${JIRA_API_TOKEN}"},
		},
		HostFiles: []harness.HostFile{{Src: "${GCP_OIDC_TOKEN_FILE}", Dest: "/sandbox/workspace/.gcp-oidc-token"}},
	}
	require.NoError(t, allowed.ValidateRunnerEnvWith(harnessEnvLookup))
}

// TestCheckGitLabApprovalCapability_StrippedChildEnv runs the approval check
// against the env a GitLab post-script actually gets, as
// `fullsend post-review` does when post-review.sh calls it (#8146).
func TestCheckGitLabApprovalCapability_StrippedChildEnv(t *testing.T) {
	setGitLabAgentJobEnv(t)
	getenv := mapGetenv(execEnvMap(postScriptEnv(&harness.Harness{}, "")))
	require.Empty(t, getenv(forge.SecretGitLabAnalystToken), "precondition: the role secret is stripped")

	require.NoError(t, checkGitLabApprovalCapability("gitlab", "approve", "analyst-pat", getenv))
	require.NoError(t, checkGitLabApprovalCapability("gitlab", "approve", "", getenv), "GITLAB_TOKEN authenticates when --token is empty")

	err := checkGitLabApprovalCapability("gitlab", "approve", "other-pat", getenv)
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrIdentityMismatch)
}

func TestWithPinnedSelectedRoleCredential(t *testing.T) {
	t.Parallel()
	t.Run("stripped coder still cannot approve", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			envGitLabRole:       "coder",
			envGitLabRoleSecret: forge.SecretGitLabCoderToken,
			"GITLAB_TOKEN":      "c",
		}
		err := checkGitLabApprovalCapability("gitlab", "approve", "c", mapGetenv(env))
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrCapabilityDenied)
	})
	t.Run("recorded secret of another role does not stand in", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			envGitLabRole:       "analyst",
			envGitLabRoleSecret: forge.SecretGitLabCoderToken,
			"GITLAB_TOKEN":      "c",
		}
		err := checkGitLabApprovalCapability("gitlab", "approve", "c", mapGetenv(env))
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrUnconfigured)
	})
	t.Run("no recorded selection stays unconfigured", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			envGitLabRole:  "analyst",
			"GITLAB_TOKEN": "a",
		}
		err := checkGitLabApprovalCapability("gitlab", "approve", "a", mapGetenv(env))
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrUnconfigured)
	})
	t.Run("present secret is used as is", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			envGitLabRoleSecret:            forge.SecretGitLabAnalystToken,
			forge.SecretGitLabAnalystToken: "a",
			"GITLAB_TOKEN":                 "other",
		}
		getenv := withPinnedSelectedRoleCredential(mapGetenv(env))
		assert.Equal(t, "a", getenv(forge.SecretGitLabAnalystToken))
	})
	t.Run("absent secret answers with GITLAB_TOKEN only for the recorded name", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			envGitLabRoleSecret: forge.SecretGitLabAnalystToken,
			"GITLAB_TOKEN":      "a",
		}
		getenv := withPinnedSelectedRoleCredential(mapGetenv(env))
		assert.Equal(t, "a", getenv(forge.SecretGitLabAnalystToken))
		assert.Empty(t, getenv(forge.SecretGitLabCoderToken))
		assert.Equal(t, "a", getenv("GITLAB_TOKEN"))
	})
}
