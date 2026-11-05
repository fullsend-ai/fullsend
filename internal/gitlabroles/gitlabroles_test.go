package gitlabroles

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresenceFrom(t *testing.T) {
	env := map[string]string{
		forge.SecretGitLabPollerToken: "poller",
		forge.SecretGitLabCoderToken:  "  ",
	}
	present := PresenceFrom(func(k string) string { return env[k] }, Registry{})
	assert.True(t, present[forge.SecretGitLabPollerToken])
	assert.False(t, present[forge.SecretGitLabAnalystToken])
	assert.False(t, present[forge.SecretGitLabCoderToken])
}

func TestResolveAlwaysUsesRegisteredRoleCredential(t *testing.T) {
	present := map[string]bool{
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	for _, tc := range []struct {
		job    Job
		role   Role
		secret string
	}{
		{PollerJob(), RolePoller, forge.SecretGitLabPollerToken},
		{AgentJob("review"), RoleAnalyst, forge.SecretGitLabAnalystToken},
		{AgentJob("code"), RoleCoder, forge.SecretGitLabCoderToken},
		{AgentJob("fix"), RoleCoder, forge.SecretGitLabCoderToken},
	} {
		t.Run(string(tc.job.Kind)+"_"+tc.job.Name, func(t *testing.T) {
			src, err := Resolve(Request{Job: tc.job, Present: present})
			require.NoError(t, err)
			assert.Equal(t, tc.role, src.Role)
			assert.Equal(t, tc.secret, src.SecretName)
		})
	}
}

func TestResolveMissingRoleDoesNotUseLegacySharedToken(t *testing.T) {
	_, err := Resolve(Request{Job: PollerJob(), Present: map[string]bool{
		forge.SecretForgeToken: true,
	}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnconfigured)
	assert.Contains(t, err.Error(), forge.SecretGitLabPollerToken)
}

func TestResolveUnknownAndFailedJobs(t *testing.T) {
	_, err := Resolve(Request{Job: AgentJob("unknown"), Present: map[string]bool{}})
	assert.ErrorIs(t, err, ErrUnregistered)
	_, err = Resolve(Request{Job: AgentJob(""), Present: map[string]bool{}})
	assert.ErrorIs(t, err, ErrUnknownJob)
	_, err = Resolve(Request{
		Job:          AgentJob("review"),
		Present:      map[string]bool{forge.SecretGitLabAnalystToken: true},
		FailedSecret: forge.SecretGitLabAnalystToken,
	})
	assert.ErrorIs(t, err, ErrAuthFailed)
	var roleErr *Error
	require.ErrorAs(t, err, &roleErr)
	assert.Equal(t, RoleAnalyst, roleErr.Role)
}

func TestDiagnose(t *testing.T) {
	rep := Diagnose(map[string]bool{forge.SecretGitLabPollerToken: true}, Registry{})
	assert.True(t, rep.Partial)
	assert.False(t, rep.Ready)
	assert.Equal(t, []Role{RoleAnalyst, RoleCoder}, rep.Missing)
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "partial role configuration")

	all := map[string]bool{
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	rep = Diagnose(all, Registry{})
	assert.True(t, rep.Ready)
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "all role credentials configured")
}

func TestErrorDoesNotExposeModeOrSecretValue(t *testing.T) {
	e := &Error{Err: ErrUnconfigured, Role: RoleCoder, Secret: forge.SecretGitLabCoderToken}
	assert.Contains(t, e.Error(), `role "coder"`)
	assert.Contains(t, e.Error(), forge.SecretGitLabCoderToken)
	assert.NotContains(t, e.Error(), "glpat-")
}

func TestCustomRoleUsesSameResolvePath(t *testing.T) {
	reg := mustParseRegistry(t, `{"roles":[{"name":"scanner","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`)
	secret := CustomSecretName(Role("scanner"))
	src, err := Resolve(Request{
		Job:      AgentJob("scanner"),
		Registry: reg,
		Present:  map[string]bool{secret: true},
	})
	require.NoError(t, err)
	assert.Equal(t, Role("scanner"), src.Role)
	assert.Equal(t, secret, src.SecretName)
	assert.Equal(t, RoleKindCustom, src.Kind)
}
