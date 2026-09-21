package gitlabroles

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testGetenv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestJobForAgentPrefersRegisteredAgentName(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "scanner",
			"credential": "own",
			"capabilities": ["read_issues"],
			"agents": ["scanner"]
		}]
	}`)
	job := JobForAgent(reg, "scanner", "coder")
	assert.Equal(t, KindAgent, job.Kind)
	assert.Equal(t, "scanner", job.Name)
}

func TestJobForAgentFallsBackToHarnessRole(t *testing.T) {
	t.Parallel()
	reg := BuiltinRegistry()
	job := JobForAgent(reg, "grillme", "review")
	assert.Equal(t, "review", job.Name)

	unmapped := JobForAgent(reg, "e2e", "unknown")
	assert.Equal(t, "e2e", unmapped.Name)
}

func TestSelectDisabledUsesSharedToken(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.SecretForgeToken:        "glpat-SHARED-secret",
		forge.SecretGitLabPollerToken: "glpat-POLLER-secret",
	}
	sel, err := Select(PollerJob(), testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, ModeDisabled, sel.Mode)
	assert.Equal(t, forge.SecretForgeToken, sel.Source.SecretName)
	assert.True(t, sel.Source.Shared)
	assert.False(t, sel.Source.Fallback)
	assert.Equal(t, RolePoller, sel.Source.Role)
	assert.Equal(t, "shared", sel.IdentitySource())

	token, err := sel.Token(testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, "glpat-SHARED-secret", token)
	for _, line := range sel.Diagnostics() {
		assert.NotContains(t, line, "glpat-")
		assert.NotContains(t, line, "SHARED-secret")
	}
}

func TestSelectDisabledAllowsUnmappedAgent(t *testing.T) {
	t.Parallel()
	env := map[string]string{forge.SecretForgeToken: "shared"}
	sel, err := SelectAgent("e2e", "", testGetenv(env))
	require.NoError(t, err)
	assert.True(t, sel.Source.Shared)
	assert.Equal(t, forge.SecretForgeToken, sel.Source.SecretName)
}

func TestSelectBuiltinMappings(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "glpat-shared-token",
		forge.SecretGitLabPollerToken:  "glpat-poller-token",
		forge.SecretGitLabAnalystToken: "glpat-analyst-token",
		forge.SecretGitLabCoderToken:   "glpat-coder-token",
	}
	getenv := testGetenv(env)
	cases := []struct {
		job  Job
		role Role
		sec  string
	}{
		{job: PollerJob(), role: RolePoller, sec: forge.SecretGitLabPollerToken},
		{job: AgentJob("review"), role: RoleAnalyst, sec: forge.SecretGitLabAnalystToken},
		{job: AgentJob("triage"), role: RoleAnalyst, sec: forge.SecretGitLabAnalystToken},
		{job: AgentJob("prioritize"), role: RoleAnalyst, sec: forge.SecretGitLabAnalystToken},
		{job: AgentJob("retro"), role: RoleAnalyst, sec: forge.SecretGitLabAnalystToken},
		{job: AgentJob("scribe"), role: RoleAnalyst, sec: forge.SecretGitLabAnalystToken},
		{job: AgentJob("code"), role: RoleCoder, sec: forge.SecretGitLabCoderToken},
		{job: AgentJob("fix"), role: RoleCoder, sec: forge.SecretGitLabCoderToken},
		{job: AgentJob("coder"), role: RoleCoder, sec: forge.SecretGitLabCoderToken},
	}
	for _, tc := range cases {
		t.Run(string(tc.job.Kind)+"_"+tc.job.Name, func(t *testing.T) {
			t.Parallel()
			sel, err := Select(tc.job, getenv)
			require.NoError(t, err)
			assert.Equal(t, tc.role, sel.Source.Role)
			assert.Equal(t, tc.sec, sel.Source.SecretName)
			assert.False(t, sel.Source.Shared)
			assert.Equal(t, "role", sel.IdentitySource())
			token, err := sel.Token(getenv)
			require.NoError(t, err)
			joined := strings.Join(sel.Diagnostics(), "\n")
			assert.NotContains(t, joined, token)
			assert.NotContains(t, joined, "glpat-")
		})
	}
}

func TestSelectCustomRoleMapping(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:      "enforced",
		forge.SecretForgeToken:            "shared",
		forge.SecretGitLabPollerToken:     "p",
		forge.SecretGitLabAnalystToken:    "a",
		forge.SecretGitLabCoderToken:      "c",
		CustomSecretName(Role("scanner")): "glpat-SCANNER-secret",
		forge.VarGitLabRoleRegistry: `{
			"roles": [{
				"name": "scanner",
				"credential": "own",
				"capabilities": ["read_issues", "write_notes"],
				"agents": ["scanner"]
			}]
		}`,
	}
	sel, err := SelectAgent("scanner", "review", testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, Role("scanner"), sel.Source.Role)
	assert.Equal(t, RoleKindCustom, sel.Source.Kind)
	assert.Equal(t, CustomSecretName(Role("scanner")), sel.Source.SecretName)
	assert.True(t, sel.Registration.Has(CapWriteNotes))
	assert.False(t, sel.Registration.Has(CapWriteRepository))
	token, err := sel.Token(testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, "glpat-SCANNER-secret", token)
	for _, line := range sel.Diagnostics() {
		assert.NotContains(t, line, "glpat-")
	}
}

func TestSelectCustomReuseRole(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "shared",
		forge.SecretGitLabPollerToken:  "p",
		forge.SecretGitLabAnalystToken: "a",
		forge.SecretGitLabCoderToken:   "glpat-CODER-secret",
		forge.VarGitLabRoleRegistry: `{
			"roles": [{
				"name": "deployer",
				"credential": "reuse",
				"reuse": "coder",
				"capabilities": ["write_repository", "write_merge_request"],
				"agents": ["deploy"]
			}]
		}`,
	}
	sel, err := SelectAgent("deploy", "", testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, Role("deployer"), sel.Source.Role)
	assert.True(t, sel.Source.Reused)
	assert.Equal(t, forge.SecretGitLabCoderToken, sel.Source.SecretName)
	assert.Contains(t, strings.Join(sel.Diagnostics(), "\n"), "reuses credential of role coder")
}

func TestSelectUnregisteredFailsClosedInRoleAwareModes(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.SecretForgeToken:         "shared",
		forge.SecretGitLabPollerToken:  "p",
		forge.SecretGitLabAnalystToken: "a",
		forge.SecretGitLabCoderToken:   "c",
	}
	for _, mode := range []string{"migrating", "enforced"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := copyEnv(env)
			e[forge.VarGitLabRoleMigration] = mode
			_, err := SelectAgent("e2e", "", testGetenv(e))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnregistered)
			assert.NotContains(t, err.Error(), "shared")
			assert.NotContains(t, err.Error(), "glpat-")
		})
	}
}

func TestSelectMigratingFallbackWhenRoleMissing(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration: "migrating",
		forge.SecretForgeToken:       "glpat-SHARED-secret",
	}
	sel, err := Select(AgentJob("review"), testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, RoleAnalyst, sel.Source.Role)
	assert.True(t, sel.Source.Fallback)
	assert.Equal(t, "migration-fallback", sel.IdentitySource())
	assert.Equal(t, forge.SecretForgeToken, sel.Source.SecretName)
	token, err := sel.Token(testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, "glpat-SHARED-secret", token)
	for _, line := range sel.Diagnostics() {
		assert.NotContains(t, line, "glpat-")
	}
}

func TestSelectEnforcedMissingRoleDoesNotFallback(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration: "enforced",
		forge.SecretForgeToken:       "shared",
	}
	_, err := Select(AgentJob("code"), testGetenv(env))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnconfigured)
	assert.NotErrorIs(t, err, ErrAuthFailed)
	assert.Contains(t, err.Error(), forge.SecretGitLabCoderToken)
}

func TestSelectInvalidModeFailsClosed(t *testing.T) {
	t.Parallel()
	_, err := Select(PollerJob(), func(string) string { return "nope" })
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMode)
}

func TestSelectInvalidRegistryFailsClosed(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration: "migrating",
		forge.SecretForgeToken:       "shared",
		forge.VarGitLabRoleRegistry:  `{"roles":[{"name":"x","token":"glpat-LEAKED"}]}`,
	}
	_, err := Select(PollerJob(), testGetenv(env))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestSelectAgentUsesHarnessRoleForUnlistedCustomAgent(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:   "enforced",
		forge.SecretForgeToken:         "shared",
		forge.SecretGitLabPollerToken:  "p",
		forge.SecretGitLabAnalystToken: "analyst-token",
		forge.SecretGitLabCoderToken:   "c",
	}
	sel, err := SelectAgent("grillme", "review", testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, RoleAnalyst, sel.Source.Role)
	assert.Equal(t, forge.SecretGitLabAnalystToken, sel.Source.SecretName)
}

func TestRequireCrossRoleMisuse(t *testing.T) {
	t.Parallel()
	reg := BuiltinRegistry()
	poller, _ := reg.Lookup(RolePoller)
	analyst, _ := reg.Lookup(RoleAnalyst)
	coder, _ := reg.Lookup(RoleCoder)

	assert.ErrorIs(t, Require(analyst, CapWriteRepository), ErrCapabilityDenied)
	assert.ErrorIs(t, Require(analyst, CapWritePollState), ErrCapabilityDenied)
	assert.ErrorIs(t, Require(poller, CapWriteRepository), ErrCapabilityDenied)
	assert.ErrorIs(t, Require(poller, CapApproveMergeRequest), ErrCapabilityDenied)
	assert.ErrorIs(t, Require(coder, CapApproveMergeRequest), ErrCapabilityDenied)
	assert.ErrorIs(t, Require(coder, CapWritePollState), ErrCapabilityDenied)

	require.NoError(t, Require(poller, CapWritePollState))
	require.NoError(t, Require(poller, CapDispatchPipeline))
	require.NoError(t, Require(analyst, CapApproveMergeRequest))
	require.NoError(t, Require(analyst, CapWriteNotes))
	require.NoError(t, Require(coder, CapWriteRepository))
	require.NoError(t, Require(coder, CapWriteMergeRequest))

	err := Require(coder, CapApproveMergeRequest)
	assert.Contains(t, err.Error(), "coder")
	assert.Contains(t, err.Error(), string(CapApproveMergeRequest))
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestAuthFailedNeverIncludesSecretValue(t *testing.T) {
	t.Parallel()
	err := AuthFailed(RoleAnalyst, ModeMigrating, forge.SecretGitLabAnalystToken)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthFailed)
	assert.Contains(t, err.Error(), forge.SecretGitLabAnalystToken)
	assert.NotContains(t, err.Error(), "glpat-")
	var ge *Error
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, RoleAnalyst, ge.Role)
	assert.Equal(t, ModeMigrating, ge.Mode)
	assert.Equal(t, forge.SecretGitLabAnalystToken, ge.Secret)
}

func TestSelectNilGetenvUsesProcessEnv(t *testing.T) {
	t.Setenv(forge.VarGitLabRoleMigration, "")
	t.Setenv(forge.VarGitLabRoleRegistry, "")
	t.Setenv(forge.SecretForgeToken, "from-process")
	t.Setenv(forge.SecretGitLabPollerToken, "")
	t.Setenv(forge.SecretGitLabAnalystToken, "")
	t.Setenv(forge.SecretGitLabCoderToken, "")
	sel, err := Select(PollerJob(), nil)
	require.NoError(t, err)
	assert.Equal(t, forge.SecretForgeToken, sel.Source.SecretName)
	token, err := sel.Token(nil)
	require.NoError(t, err)
	assert.Equal(t, "from-process", token)

	sel, err = SelectAgent("review", "", nil)
	require.NoError(t, err)
	assert.Equal(t, RoleAnalyst, sel.Source.Role)
}

func TestSelectAgentErrorPaths(t *testing.T) {
	t.Parallel()
	_, err := SelectAgent("review", "", func(string) string { return "nope" })
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMode)

	env := map[string]string{
		forge.VarGitLabRoleMigration: "migrating",
		forge.SecretForgeToken:       "shared",
		forge.VarGitLabRoleRegistry:  `{not-json`,
	}
	_, err = SelectAgent("review", "", testGetenv(env))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestSelectUnknownJobKindInRoleAwareMode(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		forge.VarGitLabRoleMigration:  "enforced",
		forge.SecretGitLabPollerToken: "p",
	}
	_, err := Select(Job{Kind: Kind("other")}, testGetenv(env))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownJob)
}

func TestTokenMissingAndEmpty(t *testing.T) {
	t.Parallel()
	_, err := (Selection{Mode: ModeEnforced}).Token(testGetenv(nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnconfigured)

	sel := Selection{
		Mode: ModeMigrating,
		Source: Source{
			Role:       RoleAnalyst,
			SecretName: forge.SecretGitLabAnalystToken,
			Shared:     false,
		},
	}
	_, err = sel.Token(testGetenv(map[string]string{forge.SecretGitLabAnalystToken: "  "}))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnconfigured)
	assert.NotErrorIs(t, err, ErrSharedUnconfigured)

	sel.Source.Shared = true
	sel.Source.SecretName = forge.SecretForgeToken
	_, err = sel.Token(testGetenv(nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSharedUnconfigured)
}

func TestDiagnosticsUnmappedAndReuse(t *testing.T) {
	t.Parallel()
	unmapped := Selection{
		Mode: ModeDisabled,
		Source: Source{
			SecretName: forge.SecretForgeToken,
			Shared:     true,
			Reason:     "migration disabled: shared credential selected",
		},
	}
	joined := strings.Join(unmapped.Diagnostics(), "\n")
	assert.Contains(t, joined, "role=unmapped")
	assert.Contains(t, joined, "kind=none")

	rec := Registration{Credential: CredentialRef{ReuseOf: RoleCoder}}
	reused := Selection{
		Mode:         ModeEnforced,
		Registration: rec,
		Source: Source{
			Role:       Role("deployer"),
			Kind:       RoleKindCustom,
			SecretName: forge.SecretGitLabCoderToken,
			Reused:     true,
		},
	}
	joined = strings.Join(reused.Diagnostics(), "\n")
	assert.Contains(t, joined, "reuses credential of role coder")
}

func TestRegistrationForSourceUnknownRole(t *testing.T) {
	t.Parallel()
	rec := registrationForSource(BuiltinRegistry(), Source{Role: Role("nope")})
	assert.Empty(t, rec.Name)
}

func copyEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
