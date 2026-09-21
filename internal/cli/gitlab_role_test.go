package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mapGetenv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestResolveGitLabPollerCredentialDisabled(t *testing.T) {
	t.Parallel()
	env := map[string]string{forge.SecretForgeToken: "glpat-SHARED"}
	sel, token, err := resolveGitLabPollerCredential(mapGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, "glpat-SHARED", token)
	assert.Equal(t, forge.SecretForgeToken, sel.Source.SecretName)
	assert.Equal(t, gitlabroles.RolePoller, sel.Source.Role)
	assert.Equal(t, "shared", sel.IdentitySource())
}

func TestResolveGitLabPollerCredentialRoleAware(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "glpat-SHARED",
		forge.SecretGitLabPollerToken:  "glpat-POLLER",
		forge.SecretGitLabAnalystToken: "glpat-ANALYST",
		forge.SecretGitLabCoderToken:   "glpat-CODER",
	}
	sel, token, err := resolveGitLabPollerCredential(mapGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, "glpat-POLLER", token)
	assert.Equal(t, forge.SecretGitLabPollerToken, sel.Source.SecretName)
	assert.False(t, sel.Registration.Has(gitlabroles.CapWriteRepository))
}

func TestResolveGitLabPollerCredentialMissingShared(t *testing.T) {
	t.Parallel()
	_, _, err := resolveGitLabPollerCredential(mapGetenv(nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrSharedUnconfigured)
	assert.Contains(t, err.Error(), forge.SecretForgeToken)
}

func TestApplyGitLabAgentCredentialsBuiltinMappings(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "glpat-SHARED",
		forge.SecretGitLabPollerToken:  "glpat-POLLER",
		forge.SecretGitLabAnalystToken: "glpat-ANALYST",
		forge.SecretGitLabCoderToken:   "glpat-CODER",
		"PUSH_TOKEN":                   "glpat-SHARED",
	}
	cases := []struct {
		agent      string
		role       string
		wantToken  string
		wantPush   string
		wantSecret string
	}{
		{agent: "review", role: "analyst", wantToken: "glpat-ANALYST", wantPush: "", wantSecret: forge.SecretGitLabAnalystToken},
		{agent: "triage", role: "analyst", wantToken: "glpat-ANALYST", wantPush: "", wantSecret: forge.SecretGitLabAnalystToken},
		{agent: "code", role: "coder", wantToken: "glpat-CODER", wantPush: "glpat-CODER", wantSecret: forge.SecretGitLabCoderToken},
		{agent: "fix", role: "coder", wantToken: "glpat-CODER", wantPush: "glpat-CODER", wantSecret: forge.SecretGitLabCoderToken},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			t.Parallel()
			got := map[string]string{}
			setenv := func(k, v string) { got[k] = v }
			var buf bytes.Buffer
			err := applyGitLabAgentCredentials(tc.agent, "", mapGetenv(env), setenv, ui.New(&buf))
			require.NoError(t, err)
			assert.Equal(t, tc.wantToken, got["GITLAB_TOKEN"])
			assert.Equal(t, tc.wantPush, got["PUSH_TOKEN"])
			assert.Equal(t, tc.role, got[envGitLabRole])
			assert.Equal(t, tc.wantSecret, got[envGitLabRoleSecret])
			assert.Equal(t, "role", got[envGitLabRoleSource])
			out := buf.String()
			assert.Contains(t, out, tc.role)
			assert.Contains(t, out, tc.wantSecret)
			assert.NotContains(t, out, "glpat-")
			assert.NotContains(t, out, tc.wantToken)
		})
	}
}

func TestApplyGitLabAgentCredentialsDisabledDoesNotClearPushToken(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.SecretForgeToken: "glpat-SHARED",
		"PUSH_TOKEN":           "glpat-SHARED",
	}
	got := map[string]string{"PUSH_TOKEN": "glpat-SHARED"}
	setenv := func(k, v string) { got[k] = v }
	err := applyGitLabAgentCredentials("review", "review", mapGetenv(env), setenv, nil)
	require.NoError(t, err)
	assert.Equal(t, "glpat-SHARED", got["GITLAB_TOKEN"])
	assert.Equal(t, "glpat-SHARED", got["PUSH_TOKEN"], "disabled mode must not rewrite PUSH_TOKEN")
	assert.Equal(t, "shared", got[envGitLabRoleSource])
}

func TestApplyGitLabAgentCredentialsMigratingFallback(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration: "migrating",
		forge.SecretForgeToken:       "glpat-SHARED",
		"PUSH_TOKEN":                 "glpat-SHARED",
	}
	got := map[string]string{}
	setenv := func(k, v string) { got[k] = v }
	err := applyGitLabAgentCredentials("code", "coder", mapGetenv(env), setenv, nil)
	require.NoError(t, err)
	assert.Equal(t, "glpat-SHARED", got["GITLAB_TOKEN"])
	assert.Equal(t, "migration-fallback", got[envGitLabRoleSource])
	assert.Equal(t, "coder", got[envGitLabRole])
	// Role is Coder so PUSH_TOKEN is granted even on explicit fallback.
	assert.Equal(t, "glpat-SHARED", got["PUSH_TOKEN"])
}

func TestApplyGitLabAgentCredentialsAnalystClearsPushTokenWhenMigrating(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration: "migrating",
		forge.SecretForgeToken:       "glpat-SHARED",
		"PUSH_TOKEN":                 "glpat-SHARED",
	}
	got := map[string]string{"PUSH_TOKEN": "glpat-SHARED"}
	setenv := func(k, v string) { got[k] = v }
	err := applyGitLabAgentCredentials("review", "review", mapGetenv(env), setenv, nil)
	require.NoError(t, err)
	assert.Equal(t, "", got["PUSH_TOKEN"])
	assert.Equal(t, "glpat-SHARED", got["GITLAB_TOKEN"])
}

func TestApplyGitLabAgentCredentialsCustomRole(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:            "enforced",
		forge.SecretForgeToken:                  "glpat-SHARED",
		forge.SecretGitLabPollerToken:           "p",
		forge.SecretGitLabAnalystToken:          "a",
		forge.SecretGitLabCoderToken:            "c",
		gitlabroles.CustomSecretName("scanner"): "glpat-SCANNER",
		forge.VarGitLabRoleRegistry: `{
			"roles": [{
				"name": "scanner",
				"credential": "own",
				"capabilities": ["read_issues"],
				"agents": ["scanner"]
			}]
		}`,
	}
	got := map[string]string{"PUSH_TOKEN": "leftover"}
	setenv := func(k, v string) { got[k] = v }
	err := applyGitLabAgentCredentials("scanner", "", mapGetenv(env), setenv, nil)
	require.NoError(t, err)
	assert.Equal(t, "glpat-SCANNER", got["GITLAB_TOKEN"])
	assert.Equal(t, "", got["PUSH_TOKEN"])
	assert.Equal(t, "scanner", got[envGitLabRole])
}

// TestApplyGitLabAgentCredentialsClearsSiblingSecrets verifies the
// auth-bypass/privilege-escalation fix from the review on PR #7510: after a
// role is selected, every other registered role secret (and the shared
// FULLSEND_FORGE_TOKEN) that was present in the environment is blanked so a
// host-side pre/post-script inheriting the process environment cannot read
// a sibling role's raw token and authenticate as a different identity.
func TestApplyGitLabAgentCredentialsClearsSiblingSecrets(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "glpat-SHARED",
		forge.SecretGitLabPollerToken:  "glpat-POLLER",
		forge.SecretGitLabAnalystToken: "glpat-ANALYST",
		forge.SecretGitLabCoderToken:   "glpat-CODER",
	}
	got := map[string]string{}
	setenv := func(k, v string) { got[k] = v }
	err := applyGitLabAgentCredentials("code", "coder", mapGetenv(env), setenv, nil)
	require.NoError(t, err)
	assert.Equal(t, "glpat-CODER", got["GITLAB_TOKEN"])
	assert.Equal(t, "", got[forge.SecretForgeToken], "shared token must be blanked, not left for a script to read")
	assert.Equal(t, "", got[forge.SecretGitLabPollerToken], "sibling Poller secret must be blanked")
	assert.Equal(t, "", got[forge.SecretGitLabAnalystToken], "sibling Analyst secret must be blanked")
	_, coderSecretTouched := got[forge.SecretGitLabCoderToken]
	assert.False(t, coderSecretTouched, "the selected role's own secret variable is left untouched")
}

func TestApplyGitLabAgentCredentialsUnregistered(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "shared",
		forge.SecretGitLabPollerToken:  "p",
		forge.SecretGitLabAnalystToken: "a",
		forge.SecretGitLabCoderToken:   "c",
	}
	err := applyGitLabAgentCredentials("e2e", "", mapGetenv(env), func(string, string) {}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrUnregistered)
}

func TestApplyGitLabAgentCredentialsRawTokenRegistryRejected(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration: "migrating",
		forge.SecretForgeToken:       "shared",
		forge.VarGitLabRoleRegistry:  `{"roles":[{"name":"scanner","secret_name":"glpat-RAWTOKEN"}]}`,
	}
	err := applyGitLabAgentCredentials("scanner", "", mapGetenv(env), func(string, string) {}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-RAWTOKEN")
}

func TestCheckGitLabApprovalCapability(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "shared",
		forge.SecretGitLabPollerToken:  "p",
		forge.SecretGitLabAnalystToken: "a",
		forge.SecretGitLabCoderToken:   "c",
	}

	t.Run("coder cannot approve", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		env[envGitLabRole] = "coder"
		err := checkGitLabApprovalCapability("gitlab", "approve", "c", mapGetenv(env))
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrCapabilityDenied)
		assert.NotContains(t, err.Error(), "glpat-")
	})
	t.Run("analyst can approve", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		env[envGitLabRole] = "analyst"
		require.NoError(t, checkGitLabApprovalCapability("gitlab", "approve", "a", mapGetenv(env)))
	})
	t.Run("review stage can approve", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		env["STAGE"] = "review"
		require.NoError(t, checkGitLabApprovalCapability("gitlab", "approve", "a", mapGetenv(env)))
	})
	t.Run("comment skips check", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		env[envGitLabRole] = "coder"
		require.NoError(t, checkGitLabApprovalCapability("gitlab", "comment", "", mapGetenv(env)))
	})
	t.Run("github skips check", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, checkGitLabApprovalCapability("github", "approve", "", mapGetenv(base)))
	})
	t.Run("disabled allows coder approve", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			forge.SecretForgeToken: "shared",
			envGitLabRole:          "coder",
		}
		require.NoError(t, checkGitLabApprovalCapability("gitlab", "approve", "", mapGetenv(env)))
	})
	t.Run("enforced without identity fails closed", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		err := checkGitLabApprovalCapability("gitlab", "approve", "", mapGetenv(env))
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrUnknownJob)
	})
	t.Run("token from GITLAB_TOKEN env matching selected secret can approve", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		env[envGitLabRole] = "analyst"
		env["GITLAB_TOKEN"] = "a"
		require.NoError(t, checkGitLabApprovalCapability("gitlab", "approve", "", mapGetenv(env)))
	})
	t.Run("token authenticating as a different identity is denied", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		env[envGitLabRole] = "analyst"
		err := checkGitLabApprovalCapability("gitlab", "approve", "c", mapGetenv(env))
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrIdentityMismatch)
		assert.NotContains(t, err.Error(), "glpat-")
	})
	t.Run("no token available is denied", func(t *testing.T) {
		t.Parallel()
		env := copyStringMap(base)
		env[envGitLabRole] = "analyst"
		err := checkGitLabApprovalCapability("gitlab", "approve", "", mapGetenv(env))
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrIdentityMismatch)
	})
}

func TestWrapGitLabAuthFailureDoesNotSwitchIdentity(t *testing.T) {
	t.Parallel()
	sel := gitlabroles.Selection{
		Mode: gitlabroles.ModeMigrating,
		Source: gitlabroles.Source{
			Role:       gitlabroles.RoleAnalyst,
			SecretName: forge.SecretGitLabAnalystToken,
		},
	}
	cause := &gl.APIError{StatusCode: http.StatusUnauthorized, Message: "401 Unauthorized"}
	err := wrapGitLabAuthFailure(sel, cause)
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrAuthFailed)
	assert.Contains(t, err.Error(), forge.SecretGitLabAnalystToken)
	assert.NotContains(t, err.Error(), "FULLSEND_FORGE_TOKEN")
	assert.NotContains(t, err.Error(), "glpat-")

	other := fmt.Errorf("network timeout")
	assert.Equal(t, other, wrapGitLabAuthFailure(sel, other))
	assert.Nil(t, wrapGitLabAuthFailure(sel, nil))

	forbidden := fmt.Errorf("denied: %w", forge.ErrForbidden)
	wrapped := wrapGitLabAuthFailure(sel, forbidden)
	assert.ErrorIs(t, wrapped, gitlabroles.ErrAuthFailed)
	assert.ErrorIs(t, wrapped, forge.ErrForbidden)
}

func TestIsGitLabAuthFailure(t *testing.T) {
	t.Parallel()
	assert.False(t, isGitLabAuthFailure(nil))
	assert.False(t, isGitLabAuthFailure(errors.New("nope")))
	assert.True(t, isGitLabAuthFailure(forge.ErrForbidden))
	assert.True(t, isGitLabAuthFailure(&gl.APIError{StatusCode: http.StatusUnauthorized}))
	assert.True(t, isGitLabAuthFailure(&gl.APIError{StatusCode: http.StatusForbidden}))
	assert.False(t, isGitLabAuthFailure(&gl.APIError{StatusCode: http.StatusNotFound}))
}

func TestLogGitLabRoleDiagnosticsNilPrinter(t *testing.T) {
	t.Parallel()
	logGitLabRoleDiagnostics(gitlabroles.Selection{}, nil)
}

func TestApplyGitLabRoleSelectionNilSetenv(t *testing.T) {
	t.Setenv(forge.SecretForgeToken, "glpat-SHARED")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv(envGitLabRole, "")
	t.Setenv(envGitLabRoleSecret, "")
	t.Setenv(envGitLabRoleSource, "")
	sel := gitlabroles.Selection{
		Mode: gitlabroles.ModeDisabled,
		Source: gitlabroles.Source{
			Role:       gitlabroles.RolePoller,
			SecretName: forge.SecretForgeToken,
			Shared:     true,
		},
	}
	applyGitLabRoleSelection(sel, "glpat-SHARED", nil, nil)
	assert.Equal(t, "glpat-SHARED", os.Getenv("GITLAB_TOKEN"))
	assert.Equal(t, "poller", os.Getenv(envGitLabRole))
}

func TestCheckGitLabApprovalCapabilityInvalidMode(t *testing.T) {
	t.Parallel()
	err := checkGitLabApprovalCapability("gitlab", "approve", "", func(string) string { return "nope" })
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrInvalidMode)
}

func TestCheckGitLabApprovalCapabilityNilGetenvDisabled(t *testing.T) {
	t.Setenv(forge.VarGitLabRoleMigration, "")
	require.NoError(t, checkGitLabApprovalCapability("gitlab", "approve", "", nil))
}

func TestResolveGitLabPollerCredentialMigratingFallback(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration: "migrating",
		forge.SecretForgeToken:       "glpat-SHARED",
	}
	sel, token, err := resolveGitLabPollerCredential(mapGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, "glpat-SHARED", token)
	assert.True(t, sel.Source.Fallback)
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
