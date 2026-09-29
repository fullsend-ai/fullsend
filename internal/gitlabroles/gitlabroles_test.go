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

	assert.True(t, ModeEnforced.RequiresRoleCredentials())
	assert.True(t, ModeMigrating.RequiresRoleCredentials())
	assert.False(t, ModeDisabled.RequiresRoleCredentials())
	assert.False(t, ModeRollback.RequiresRoleCredentials())

	assert.True(t, ModeEnforced.OperatorSettable())
	assert.True(t, ModeRollback.OperatorSettable())
	assert.False(t, ModeDisabled.OperatorSettable())
	assert.False(t, ModeMigrating.OperatorSettable())
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

	present := PresenceFrom(getenv, Registry{})
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
	present := PresenceFrom(nil, Registry{})
	assert.True(t, present[forge.SecretForgeToken])
}

func TestResolveDisabledRequiresRoleCredentials(t *testing.T) {
	t.Parallel()
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	jobs := []struct {
		job    Job
		secret string
		role   Role
	}{
		{job: PollerJob(), secret: forge.SecretGitLabPollerToken, role: RolePoller},
		{job: AgentJob("review"), secret: forge.SecretGitLabAnalystToken, role: RoleAnalyst},
		{job: AgentJob("code"), secret: forge.SecretGitLabCoderToken, role: RoleCoder},
		{job: AgentJob("fix"), secret: forge.SecretGitLabCoderToken, role: RoleCoder},
		{job: AgentJob("triage"), secret: forge.SecretGitLabAnalystToken, role: RoleAnalyst},
	}
	for _, tc := range jobs {
		t.Run(string(tc.job.Kind)+"_"+tc.job.Name, func(t *testing.T) {
			t.Parallel()
			src, err := Resolve(Request{Mode: ModeDisabled, Job: tc.job, Present: present})
			require.NoError(t, err)
			assert.Equal(t, tc.secret, src.SecretName)
			assert.Equal(t, tc.role, src.Role)
			assert.False(t, src.Shared)
		})
	}
}

func TestResolveDisabledMissingRoleDoesNotUseShared(t *testing.T) {
	t.Parallel()
	_, err := Resolve(Request{Mode: ModeDisabled, Job: PollerJob(), Present: map[string]bool{forge.SecretForgeToken: true}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnconfigured)
	assert.NotErrorIs(t, err, ErrSharedUnconfigured)
	assert.Contains(t, err.Error(), forge.SecretGitLabPollerToken)
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestResolveDisabledUnmappedFailsClosed(t *testing.T) {
	t.Parallel()
	_, err := Resolve(Request{Mode: ModeDisabled, Job: AgentJob("custom-agent"), Present: map[string]bool{forge.SecretForgeToken: true}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnregistered)

	_, err = Resolve(Request{Mode: ModeDisabled, Job: AgentJob(""), Present: map[string]bool{forge.SecretForgeToken: true}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownJob)
}

func TestResolveRollbackUsesRoleSecrets(t *testing.T) {
	t.Parallel()
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	src, err := Resolve(Request{Mode: ModeRollback, Job: AgentJob("code"), Present: present})
	require.NoError(t, err)
	assert.Equal(t, forge.SecretGitLabCoderToken, src.SecretName)
	assert.False(t, src.Shared)
	assert.Equal(t, RoleCoder, src.Role)
	assert.Equal(t, RoleKindBuiltin, src.Kind)
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
		assert.Equal(t, RoleKindBuiltin, src.Kind)
		assert.Equal(t, forge.SecretGitLabCoderToken, src.SecretName)
		assert.False(t, src.Shared)
	})
	t.Run("role unconfigured does not fall back to shared", func(t *testing.T) {
		t.Parallel()
		present := map[string]bool{forge.SecretForgeToken: true}
		_, err := Resolve(Request{Mode: ModeMigrating, Job: AgentJob("review"), Present: present})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnconfigured)
		assert.Contains(t, err.Error(), forge.SecretGitLabAnalystToken)
		assert.NotContains(t, err.Error(), "fallback")
	})
	t.Run("role and shared unconfigured", func(t *testing.T) {
		t.Parallel()
		_, err := Resolve(Request{Mode: ModeMigrating, Job: PollerJob(), Present: map[string]bool{}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnconfigured)
		assert.NotErrorIs(t, err, ErrAuthFailed)
		assert.NotErrorIs(t, err, ErrUnregistered)
		assert.Contains(t, err.Error(), forge.SecretGitLabPollerToken)
	})
	t.Run("unregistered agent", func(t *testing.T) {
		t.Parallel()
		_, err := Resolve(Request{
			Mode:    ModeMigrating,
			Job:     AgentJob("e2e"),
			Present: map[string]bool{forge.SecretForgeToken: true},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnregistered)
		assert.NotErrorIs(t, err, ErrUnconfigured)
		assert.NotErrorIs(t, err, ErrAuthFailed)
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
	rep := Diagnose(ModeDisabled, map[string]bool{forge.SecretForgeToken: true}, Registry{})
	assert.Equal(t, ModeDisabled, rep.Mode)
	assert.True(t, rep.SharedPresent)
	assert.True(t, rep.Ready, "existing install with shared token is ready")
	assert.False(t, rep.Partial)
	assert.Equal(t, []Role{RolePoller, RoleAnalyst, RoleCoder}, rep.Missing)
	require.Len(t, rep.Roles, 3)
	for _, rr := range rep.Roles {
		assert.Equal(t, RoleStateUnconfigured, rr.State)
		assert.Equal(t, RoleKindBuiltin, rr.Kind)
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
	}, Registry{})
	assert.True(t, rep.Partial)
	assert.False(t, rep.Ready)
	assert.Equal(t, []Role{RoleAnalyst, RoleCoder}, rep.Missing)
	joined := strings.Join(rep.Diagnostics, "\n")
	assert.Contains(t, joined, "partial role configuration: 1/3")
	assert.Contains(t, joined, "missing (required)")
}

func TestDiagnoseEnforcedMissing(t *testing.T) {
	t.Parallel()
	rep := Diagnose(ModeEnforced, map[string]bool{forge.SecretForgeToken: true}, Registry{})
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
	rep := Diagnose(ModeEnforced, present, Registry{})
	assert.True(t, rep.Ready)
	assert.False(t, rep.Partial)
	assert.Empty(t, rep.Missing)
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "all role credentials configured")

	disabled := Diagnose(ModeDisabled, present, Registry{})
	assert.True(t, disabled.Ready)
	joined := strings.Join(disabled.Diagnostics, "\n")
	assert.Contains(t, joined, "configured but unused")
}

func TestDiagnoseInvalidMode(t *testing.T) {
	t.Parallel()
	rep := Diagnose(Mode("nope"), nil, Registry{})
	assert.False(t, rep.Ready)
	require.NotEmpty(t, rep.Diagnostics)
	assert.Contains(t, rep.Diagnostics[0], "invalid migration mode")
}

func TestDiagnoseMissingSharedDisabled(t *testing.T) {
	t.Parallel()
	rep := Diagnose(ModeDisabled, nil, Registry{})
	assert.False(t, rep.Ready)
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "legacy path not ready")
}

// TestDiagnoseReadyDivergesFromResolveOnLeftoverModes documents a known,
// intentional gap flagged in review on #7811: Diagnose.Ready is
// install/converge-state readiness (see Report.Ready), while runtime
// credential selection (Resolve/Select) always requires the registered
// role secret in every mode, including leftover disabled and explicit
// rollback, with no shared-token fallback (#7782). An install that only
// has the legacy FULLSEND_FORGE_TOKEN provisioned — not yet migrated to
// per-role secrets — reports Ready via Diagnose (a truthful statement
// about install/converge state) while `fullsend poll`/`fullsend run`
// fail closed with ErrUnconfigured on that same install. Do not "fix"
// this by making the two agree without also updating repos
// status/converge (#7501/#7524), which intentionally still branch on
// the legacy shared-token path and are out of scope for #7782.
func TestDiagnoseReadyDivergesFromResolveOnLeftoverModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeDisabled, ModeRollback} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			present := map[string]bool{forge.SecretForgeToken: true}

			rep := Diagnose(mode, present, Registry{})
			assert.True(t, rep.Ready,
				"Diagnose reports install/converge-state readiness from the shared token alone")

			getenv := func(k string) string {
				if k == forge.SecretForgeToken {
					return "glpat-shared-secret"
				}
				return ""
			}
			_, err := Select(PollerJob(), getenv)
			require.Error(t, err,
				"Resolve/Select must still fail closed on the same install: no role secret, no shared-token fallback")
			assert.ErrorIs(t, err, ErrUnconfigured)
		})
	}
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

func TestErrorWithoutRoleModeSecret(t *testing.T) {
	t.Parallel()
	e := &Error{Err: ErrUnknownJob}
	assert.Equal(t, ErrUnknownJob.Error(), e.Error())
}

func TestErrorFormatQuotesRoleAndModeLabelsSecret(t *testing.T) {
	t.Parallel()
	e := &Error{Err: ErrUnconfigured, Role: RoleCoder, Mode: ModeEnforced, Secret: forge.SecretGitLabCoderToken}
	want := ErrUnconfigured.Error() +
		`: role "coder"` +
		`: mode "enforced"` +
		`: secret ` + forge.SecretGitLabCoderToken
	assert.Equal(t, want, e.Error())
}

func TestIdentifiers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []Role{RolePoller, RoleAnalyst, RoleCoder}, BuiltinRoles())
	assert.Equal(t, forge.SecretForgeToken, SharedSecretName())
	assert.Equal(t, forge.VarGitLabRoleMigration, ModeVariableName())
	assert.Equal(t, forge.VarGitLabRoleRegistry, RegistryVariableName())
	assert.Equal(t, SharedTokenName, "fullsend-bot")
	assert.Equal(t, PollerTokenName, "fullsend-poller")
	assert.Equal(t, AnalystTokenName, "fullsend-analyst")
	assert.Equal(t, CoderTokenName, "fullsend-coder")
	assert.Equal(t, []string{"api"}, TokenScopes())
	assert.Equal(t, 30, DeveloperAccessLevel)
	assert.Equal(t, Job{Kind: KindPoller}, PollerJob())
	assert.Equal(t, Job{Kind: KindAgent, Name: "review"}, AgentJob("review"))
}

func TestContractDistinguishableOutcomes(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "scanner",
			"credential": "own",
			"capabilities": ["read_issues"],
			"agents": ["scanner"]
		}]
	}`)
	present := map[string]bool{
		forge.SecretForgeToken:            true,
		forge.SecretGitLabCoderToken:      true,
		CustomSecretName(Role("scanner")): true,
	}

	t.Run("disabled still selects registered role secret", func(t *testing.T) {
		t.Parallel()
		src, err := Resolve(Request{Mode: ModeDisabled, Job: AgentJob("scanner"), Registry: reg, Present: present})
		require.NoError(t, err)
		assert.Equal(t, CustomSecretName(Role("scanner")), src.SecretName)
		assert.False(t, src.Shared)
	})

	t.Run("builtin and custom share resolve path", func(t *testing.T) {
		t.Parallel()
		coder, err := Resolve(Request{Mode: ModeEnforced, Job: AgentJob("code"), Registry: reg, Present: present})
		require.NoError(t, err)
		scanner, err := Resolve(Request{Mode: ModeEnforced, Job: AgentJob("scanner"), Registry: reg, Present: present})
		require.NoError(t, err)
		assert.Equal(t, RoleCoder, coder.Role)
		assert.Equal(t, RoleKindBuiltin, coder.Kind)
		assert.Equal(t, forge.SecretGitLabCoderToken, coder.SecretName)
		assert.Equal(t, Role("scanner"), scanner.Role)
		assert.Equal(t, RoleKindCustom, scanner.Kind)
		assert.Equal(t, CustomSecretName(Role("scanner")), scanner.SecretName)
		assert.False(t, coder.Shared)
		assert.False(t, scanner.Shared)
	})

	t.Run("unregistered vs unconfigured vs auth-failed", func(t *testing.T) {
		t.Parallel()
		_, unreg := Resolve(Request{
			Mode:     ModeEnforced,
			Job:      AgentJob("not-a-role"),
			Registry: reg,
			Present:  present,
		})
		require.Error(t, unreg)
		assert.ErrorIs(t, unreg, ErrUnregistered)

		_, unconf := Resolve(Request{
			Mode:     ModeEnforced,
			Job:      AgentJob("review"),
			Registry: reg,
			Present:  present,
		})
		require.Error(t, unconf)
		assert.ErrorIs(t, unconf, ErrUnconfigured)
		assert.NotErrorIs(t, unconf, ErrUnregistered)
		assert.NotErrorIs(t, unconf, ErrAuthFailed)

		_, auth := Resolve(Request{
			Mode:         ModeEnforced,
			Job:          AgentJob("scanner"),
			Registry:     reg,
			Present:      present,
			FailedSecret: CustomSecretName(Role("scanner")),
		})
		require.Error(t, auth)
		assert.ErrorIs(t, auth, ErrAuthFailed)
		assert.NotErrorIs(t, auth, ErrUnconfigured)
		assert.NotErrorIs(t, auth, ErrUnregistered)
	})
}
