package gitlabroles

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw     string
		want    Mode
		wantErr error
	}{
		{raw: "", want: ModeDisabled},
		{raw: "   ", want: ModeDisabled},
		{raw: "disabled", want: ModeDisabled},
		{raw: "DISABLED", want: ModeDisabled},
		{raw: " Disabled ", want: ModeDisabled},
		{raw: "migrating", want: ModeMigrating},
		{raw: "MIGRATING", want: ModeMigrating},
		{raw: "rollback", want: ModeRollback},
		{raw: "enforced", want: ModeEnforced},
		{raw: "enforce", wantErr: ErrInvalidMode},
		{raw: "bogus", wantErr: ErrInvalidMode},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMode(tt.raw)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestModePredicates(t *testing.T) {
	t.Parallel()
	assert.True(t, ModeDisabled.Valid())
	assert.True(t, ModeMigrating.Valid())
	assert.True(t, ModeRollback.Valid())
	assert.True(t, ModeEnforced.Valid())
	assert.False(t, Mode("nope").Valid())
	assert.False(t, Mode("").Valid())

	assert.True(t, ModeDisabled.UsesSharedOnly())
	assert.True(t, ModeRollback.UsesSharedOnly())
	assert.False(t, ModeMigrating.UsesSharedOnly())
	assert.False(t, ModeEnforced.UsesSharedOnly())

	assert.True(t, ModeMigrating.AllowsSharedFallback())
	assert.False(t, ModeDisabled.AllowsSharedFallback())
	assert.False(t, ModeRollback.AllowsSharedFallback())
	assert.False(t, ModeEnforced.AllowsSharedFallback())

	assert.True(t, ModeEnforced.RequiresRoleCredentials())
	assert.False(t, ModeDisabled.RequiresRoleCredentials())
	assert.False(t, ModeMigrating.RequiresRoleCredentials())
	assert.False(t, ModeRollback.RequiresRoleCredentials())
}

func TestRoleFor(t *testing.T) {
	t.Parallel()
	cases := map[string]Role{
		"poller":     RolePoller,
		"POLLER":     RolePoller,
		"analyst":    RoleAnalyst,
		"review":     RoleAnalyst,
		"triage":     RoleAnalyst,
		"prioritize": RoleAnalyst,
		"retro":      RoleAnalyst,
		"scribe":     RoleAnalyst,
		" Review ":   RoleAnalyst,
		"coder":      RoleCoder,
		"code":       RoleCoder,
		"fix":        RoleCoder,
		"CODE":       RoleCoder,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := RoleFor(name)
			require.True(t, ok)
			assert.Equal(t, want, got)
		})
	}
	for _, name := range []string{"", "e2e", "fullsend", "unknown", "sync"} {
		t.Run("unmapped_"+name, func(t *testing.T) {
			t.Parallel()
			got, ok := RoleFor(name)
			assert.False(t, ok)
			assert.Empty(t, got)
		})
	}
}

func TestIdentifiers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []Role{RolePoller, RoleAnalyst, RoleCoder}, AllRoles())
	assert.Equal(t, forge.SecretGitLabPollerToken, SecretName(RolePoller))
	assert.Equal(t, forge.SecretGitLabAnalystToken, SecretName(RoleAnalyst))
	assert.Equal(t, forge.SecretGitLabCoderToken, SecretName(RoleCoder))
	assert.Empty(t, SecretName(Role("nope")))
	assert.Equal(t, []string{
		forge.SecretGitLabPollerToken,
		forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
	}, SecretNames())
	assert.Equal(t, forge.SecretForgeToken, SharedSecretName())
	assert.Equal(t, forge.VarGitLabRoleMigration, ModeVariableName())
	assert.Equal(t, "fullsend-poller", ProjectAccessTokenName(RolePoller))
	assert.Equal(t, "fullsend-analyst", ProjectAccessTokenName(RoleAnalyst))
	assert.Equal(t, "fullsend-coder", ProjectAccessTokenName(RoleCoder))
	assert.Empty(t, ProjectAccessTokenName(Role("nope")))
	assert.Equal(t, SharedTokenName, "fullsend-bot")
	assert.Equal(t, []string{"api"}, TokenScopes())
	assert.Equal(t, 30, DeveloperAccessLevel)
	assert.Equal(t, Job{Kind: KindPoller}, PollerJob())
	assert.Equal(t, Job{Kind: KindAgent, Name: "review"}, AgentJob("review"))
}

func TestModeFromAndPresenceFrom(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:  "migrating",
		forge.SecretForgeToken:        "shared",
		forge.SecretGitLabPollerToken: "poller",
		forge.SecretGitLabCoderToken:  "  ",
	}
	getenv := func(k string) string { return env[k] }

	mode, err := ModeFrom(getenv)
	require.NoError(t, err)
	assert.Equal(t, ModeMigrating, mode)

	present := PresenceFrom(getenv)
	assert.True(t, present[forge.SecretForgeToken])
	assert.True(t, present[forge.SecretGitLabPollerToken])
	assert.False(t, present[forge.SecretGitLabAnalystToken])
	assert.False(t, present[forge.SecretGitLabCoderToken], "whitespace-only is absent")
}

func TestModeFromInvalid(t *testing.T) {
	t.Parallel()
	_, err := ModeFrom(func(string) string { return "nope" })
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMode)
}

func TestModeFromNilGetenvUsesEnv(t *testing.T) {
	t.Setenv(forge.VarGitLabRoleMigration, "rollback")
	t.Setenv(forge.SecretForgeToken, "x")
	mode, err := ModeFrom(nil)
	require.NoError(t, err)
	assert.Equal(t, ModeRollback, mode)
	present := PresenceFrom(nil)
	assert.True(t, present[forge.SecretForgeToken])
}

func TestResolveDisabledMatchesExistingInstall(t *testing.T) {
	t.Parallel()
	present := map[string]bool{forge.SecretForgeToken: true}
	jobs := []Job{
		PollerJob(),
		AgentJob("review"),
		AgentJob("code"),
		AgentJob("fix"),
		AgentJob("triage"),
		AgentJob("custom-agent"),
		{Kind: KindAgent, Name: ""},
		{},
	}
	for _, job := range jobs {
		t.Run(string(job.Kind)+"_"+job.Name, func(t *testing.T) {
			t.Parallel()
			src, err := Resolve(Request{Mode: ModeDisabled, Job: job, Present: present})
			require.NoError(t, err)
			assert.Equal(t, forge.SecretForgeToken, src.SecretName)
			assert.True(t, src.Shared)
			assert.False(t, src.Fallback)
			assert.Contains(t, src.Reason, "disabled")
		})
	}
}

func TestResolveDisabledMissingShared(t *testing.T) {
	t.Parallel()
	_, err := Resolve(Request{Mode: ModeDisabled, Job: PollerJob(), Present: nil})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSharedUnconfigured)
	assert.Contains(t, err.Error(), forge.SecretForgeToken)
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestResolveRollbackIgnoresRoleSecrets(t *testing.T) {
	t.Parallel()
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	src, err := Resolve(Request{Mode: ModeRollback, Job: AgentJob("code"), Present: present})
	require.NoError(t, err)
	assert.Equal(t, forge.SecretForgeToken, src.SecretName)
	assert.True(t, src.Shared)
	assert.False(t, src.Fallback)
	assert.Equal(t, RoleCoder, src.Role)
	assert.Contains(t, src.Reason, "rollback")
}

func TestResolveMigrating(t *testing.T) {
	t.Parallel()
	t.Run("role configured uses role secret", func(t *testing.T) {
		t.Parallel()
		present := map[string]bool{
			forge.SecretForgeToken:        true,
			forge.SecretGitLabCoderToken:  true,
			forge.SecretGitLabPollerToken: true,
		}
		src, err := Resolve(Request{Mode: ModeMigrating, Job: AgentJob("fix"), Present: present})
		require.NoError(t, err)
		assert.Equal(t, RoleCoder, src.Role)
		assert.Equal(t, forge.SecretGitLabCoderToken, src.SecretName)
		assert.False(t, src.Shared)
		assert.False(t, src.Fallback)
	})
	t.Run("role unconfigured falls back to shared", func(t *testing.T) {
		t.Parallel()
		present := map[string]bool{forge.SecretForgeToken: true}
		src, err := Resolve(Request{Mode: ModeMigrating, Job: AgentJob("review"), Present: present})
		require.NoError(t, err)
		assert.Equal(t, RoleAnalyst, src.Role)
		assert.Equal(t, forge.SecretForgeToken, src.SecretName)
		assert.True(t, src.Shared)
		assert.True(t, src.Fallback)
		assert.Contains(t, src.Reason, "unconfigured")
	})
	t.Run("role and shared unconfigured", func(t *testing.T) {
		t.Parallel()
		_, err := Resolve(Request{Mode: ModeMigrating, Job: PollerJob(), Present: map[string]bool{}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnconfigured)
		assert.NotErrorIs(t, err, ErrAuthFailed)
		assert.Contains(t, err.Error(), forge.SecretGitLabPollerToken)
	})
	t.Run("unknown agent", func(t *testing.T) {
		t.Parallel()
		_, err := Resolve(Request{
			Mode:    ModeMigrating,
			Job:     AgentJob("e2e"),
			Present: map[string]bool{forge.SecretForgeToken: true},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnknownJob)
	})
}

func TestResolveEnforced(t *testing.T) {
	t.Parallel()
	t.Run("role configured", func(t *testing.T) {
		t.Parallel()
		present := map[string]bool{
			forge.SecretForgeToken:         true,
			forge.SecretGitLabAnalystToken: true,
		}
		src, err := Resolve(Request{Mode: ModeEnforced, Job: AgentJob("triage"), Present: present})
		require.NoError(t, err)
		assert.Equal(t, forge.SecretGitLabAnalystToken, src.SecretName)
		assert.False(t, src.Shared)
		assert.False(t, src.Fallback)
	})
	t.Run("missing role does not use shared", func(t *testing.T) {
		t.Parallel()
		present := map[string]bool{forge.SecretForgeToken: true}
		_, err := Resolve(Request{Mode: ModeEnforced, Job: AgentJob("code"), Present: present})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnconfigured)
		assert.Contains(t, err.Error(), forge.SecretGitLabCoderToken)
		assert.NotContains(t, err.Error(), "fallback")
	})
}

func TestResolveAuthFailureNeverFallsBack(t *testing.T) {
	t.Parallel()
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
		forge.SecretGitLabPollerToken:  true,
	}
	modes := []Mode{ModeDisabled, ModeMigrating, ModeRollback, ModeEnforced}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			src, err := Resolve(Request{
				Mode:         mode,
				Job:          AgentJob("review"),
				Present:      present,
				FailedSecret: forge.SecretGitLabAnalystToken,
			})
			require.Error(t, err)
			assert.Zero(t, src)
			assert.ErrorIs(t, err, ErrAuthFailed)
			assert.Contains(t, err.Error(), forge.SecretGitLabAnalystToken)
			assert.NotContains(t, err.Error(), "glpat-")
			var ge *Error
			require.ErrorAs(t, err, &ge)
			assert.Equal(t, RoleAnalyst, ge.Role)
			assert.Equal(t, mode, ge.Mode)
			assert.Equal(t, forge.SecretGitLabAnalystToken, ge.Secret)
		})
	}
}

func TestResolveInvalidMode(t *testing.T) {
	t.Parallel()
	_, err := Resolve(Request{Mode: Mode("weird"), Job: PollerJob(), Present: map[string]bool{forge.SecretForgeToken: true}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMode)
}

func TestResolveUnknownKind(t *testing.T) {
	t.Parallel()
	_, err := Resolve(Request{
		Mode:    ModeEnforced,
		Job:     Job{Kind: Kind("other")},
		Present: map[string]bool{forge.SecretGitLabPollerToken: true},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownJob)
}

func TestDiagnoseExistingInstall(t *testing.T) {
	t.Parallel()
	rep := Diagnose(ModeDisabled, map[string]bool{forge.SecretForgeToken: true})
	assert.Equal(t, ModeDisabled, rep.Mode)
	assert.True(t, rep.SharedPresent)
	assert.True(t, rep.Ready, "existing install with shared token is ready")
	assert.False(t, rep.Partial)
	assert.Equal(t, []Role{RolePoller, RoleAnalyst, RoleCoder}, rep.Missing)
	require.Len(t, rep.Roles, 3)
	for _, rr := range rep.Roles {
		assert.Equal(t, RoleStateUnconfigured, rr.State)
	}
	joined := strings.Join(rep.Diagnostics, "\n")
	assert.Contains(t, joined, "legacy shared-token path ready")
	assert.Contains(t, joined, "not required")
	for _, d := range rep.Diagnostics {
		assert.NotRegexp(t, `glpat-|sk-|ghp_`, d)
	}
}

func TestDiagnosePartialMigrating(t *testing.T) {
	t.Parallel()
	rep := Diagnose(ModeMigrating, map[string]bool{
		forge.SecretForgeToken:        true,
		forge.SecretGitLabPollerToken: true,
	})
	assert.True(t, rep.Partial)
	assert.False(t, rep.Ready)
	assert.Equal(t, []Role{RoleAnalyst, RoleCoder}, rep.Missing)
	joined := strings.Join(rep.Diagnostics, "\n")
	assert.Contains(t, joined, "partial role configuration: 1/3")
	assert.Contains(t, joined, "pending")
}

func TestDiagnoseEnforcedMissing(t *testing.T) {
	t.Parallel()
	rep := Diagnose(ModeEnforced, map[string]bool{forge.SecretForgeToken: true})
	assert.False(t, rep.Ready)
	assert.False(t, rep.Partial)
	joined := strings.Join(rep.Diagnostics, "\n")
	assert.Contains(t, joined, "missing (required)")
	assert.Contains(t, joined, "no role credentials configured")
}

func TestDiagnoseAllRolesReady(t *testing.T) {
	t.Parallel()
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	rep := Diagnose(ModeEnforced, present)
	assert.True(t, rep.Ready)
	assert.False(t, rep.Partial)
	assert.Empty(t, rep.Missing)
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "all role credentials configured")

	disabled := Diagnose(ModeDisabled, present)
	assert.True(t, disabled.Ready)
	joined := strings.Join(disabled.Diagnostics, "\n")
	assert.Contains(t, joined, "configured but unused")
}

func TestDiagnoseInvalidMode(t *testing.T) {
	t.Parallel()
	rep := Diagnose(Mode("nope"), nil)
	assert.False(t, rep.Ready)
	require.NotEmpty(t, rep.Diagnostics)
	assert.Contains(t, rep.Diagnostics[0], "invalid migration mode")
}

func TestDiagnoseMissingSharedDisabled(t *testing.T) {
	t.Parallel()
	rep := Diagnose(ModeDisabled, nil)
	assert.False(t, rep.Ready)
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "legacy path not ready")
}

func TestErrorNilAndUnwrap(t *testing.T) {
	t.Parallel()
	var e *Error
	assert.Equal(t, "gitlab role credential error", e.Error())
	assert.Nil(t, e.Unwrap())
	wrapped := &Error{Err: ErrAuthFailed, Role: RoleCoder, Mode: ModeMigrating, Secret: forge.SecretGitLabCoderToken}
	assert.ErrorIs(t, wrapped, ErrAuthFailed)
	assert.Equal(t, RoleCoder, wrapped.Role)
}

func TestResolveEmptyAgentNameEnforced(t *testing.T) {
	t.Parallel()
	_, err := Resolve(Request{
		Mode:    ModeEnforced,
		Job:     AgentJob(""),
		Present: map[string]bool{forge.SecretGitLabCoderToken: true},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownJob)
}

func TestRoleForSecretUnknown(t *testing.T) {
	t.Parallel()
	role, ok := roleForSecret(forge.SecretForgeToken)
	assert.False(t, ok)
	assert.Empty(t, role)
}

func TestErrorWithoutRoleModeSecret(t *testing.T) {
	t.Parallel()
	e := &Error{Err: ErrUnknownJob}
	assert.Equal(t, ErrUnknownJob.Error(), e.Error())
}
