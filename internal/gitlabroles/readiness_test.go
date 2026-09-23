package gitlabroles

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func allBuiltinSecretsPresent() map[string]bool {
	return map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
}

func TestCheckBuiltinReadinessAllReady(t *testing.T) {
	t.Parallel()
	got := CheckBuiltinReadiness(allBuiltinSecretsPresent(), Registry{})
	assert.True(t, got.Ready)
	assert.True(t, got.SharedPresent)
	assert.Empty(t, got.Missing)
	require.Len(t, got.Roles, 3)
	assert.Equal(t, []Role{RolePoller, RoleAnalyst, RoleCoder}, []Role{got.Roles[0].Name, got.Roles[1].Name, got.Roles[2].Name})
	for _, c := range got.Roles {
		assert.True(t, c.Ready, string(c.Name))
		assert.True(t, c.Present)
		assert.Empty(t, c.Reasons)
	}
	joined := strings.Join(got.Diagnostics, "\n")
	assert.Contains(t, joined, "builtin poller: ready")
	assert.Contains(t, joined, "builtin analyst: ready")
	assert.Contains(t, joined, "builtin coder: ready")
	assert.Contains(t, joined, "builtin roles ready: 3/3")
	assert.NotContains(t, joined, "not a substitute")
	assertNoSecretLeak(t, got.Diagnostics)
}

func TestCheckBuiltinReadinessIdentityAttribution(t *testing.T) {
	t.Parallel()
	got := CheckBuiltinReadiness(allBuiltinSecretsPresent(), BuiltinRegistry())
	require.True(t, got.Ready)
	byName := map[Role]BuiltinRoleCheck{}
	for _, c := range got.Roles {
		byName[c.Name] = c
	}
	assert.Equal(t, []string{"poller"}, byName[RolePoller].Jobs)
	assert.Contains(t, byName[RoleAnalyst].Jobs, "review")
	assert.Contains(t, byName[RoleAnalyst].Jobs, "triage")
	assert.Contains(t, byName[RoleCoder].Jobs, "code")
	assert.Contains(t, byName[RoleCoder].Jobs, "fix")
	assert.Contains(t, byName[RoleAnalyst].Capabilities, CapApproveMergeRequest)
	assert.NotContains(t, byName[RoleAnalyst].Capabilities, CapWriteRepository)
	assert.Contains(t, byName[RoleCoder].Capabilities, CapWriteRepository)
	assert.NotContains(t, byName[RoleCoder].Capabilities, CapApproveMergeRequest)
	assert.Contains(t, byName[RolePoller].Capabilities, CapDispatchPipeline)
	assert.NotContains(t, byName[RolePoller].Capabilities, CapWriteRepository)
}

func TestCheckBuiltinReadinessMissingEachRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		drop    string
		missing Role
	}{
		{name: "poller", drop: forge.SecretGitLabPollerToken, missing: RolePoller},
		{name: "analyst", drop: forge.SecretGitLabAnalystToken, missing: RoleAnalyst},
		{name: "coder", drop: forge.SecretGitLabCoderToken, missing: RoleCoder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			present := allBuiltinSecretsPresent()
			present[tc.drop] = false
			got := CheckBuiltinReadiness(present, Registry{})
			assert.False(t, got.Ready)
			assert.Equal(t, []Role{tc.missing}, got.Missing)
			joined := strings.Join(got.Diagnostics, "\n")
			assert.Contains(t, joined, "credential not provisioned ("+tc.drop+")")
			assert.Contains(t, joined, "builtin roles ready: 2/3; missing="+string(tc.missing))
			assert.Contains(t, joined, "not a substitute")
			assertNoSecretLeak(t, got.Diagnostics)
		})
	}
}

func TestCheckBuiltinReadinessSharedTokenDoesNotSubstitute(t *testing.T) {
	t.Parallel()
	got := CheckBuiltinReadiness(map[string]bool{
		forge.SecretForgeToken: true,
	}, Registry{})
	assert.False(t, got.Ready)
	assert.True(t, got.SharedPresent)
	assert.Equal(t, []Role{RolePoller, RoleAnalyst, RoleCoder}, got.Missing)
	joined := strings.Join(got.Diagnostics, "\n")
	assert.Contains(t, joined, "builtin roles ready: 0/3; missing=poller,analyst,coder")
	assert.Contains(t, joined, "shared credential FULLSEND_FORGE_TOKEN is present and is not a substitute")
	assert.NotContains(t, joined, "glpat-")
	for _, c := range got.Roles {
		assert.False(t, c.Ready)
		assert.False(t, c.Present)
	}
}

func TestCheckBuiltinReadinessPartialWithoutShared(t *testing.T) {
	t.Parallel()
	got := CheckBuiltinReadiness(map[string]bool{
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
	}, Registry{})
	assert.False(t, got.Ready)
	assert.False(t, got.SharedPresent)
	assert.Equal(t, []Role{RoleCoder}, got.Missing)
	joined := strings.Join(got.Diagnostics, "\n")
	assert.Contains(t, joined, "builtin poller: ready")
	assert.Contains(t, joined, "builtin analyst: ready")
	assert.Contains(t, joined, "builtin coder: not ready")
	assert.NotContains(t, joined, "not a substitute")
	assertNoSecretLeak(t, got.Diagnostics)
}

func TestCheckBuiltinReadinessNilPresent(t *testing.T) {
	t.Parallel()
	got := CheckBuiltinReadiness(nil, BuiltinRegistry())
	assert.False(t, got.Ready)
	assert.False(t, got.SharedPresent)
	assert.Len(t, got.Missing, 3)
}

func TestCheckBuiltinRoleUnregistered(t *testing.T) {
	t.Parallel()
	empty := Registry{
		roles:   []Registration{{Name: Role("scanner")}},
		byName:  map[Role]int{Role("scanner"): 0},
		byAgent: map[string]Role{},
	}
	got := checkBuiltinRole(allBuiltinSecretsPresent(), empty, builtinReadinessSpecs()[0])
	assert.False(t, got.Ready)
	assert.Equal(t, RolePoller, got.Name)
	require.NotEmpty(t, got.Reasons)
	assert.Equal(t, "role is not registered", got.Reasons[0])
}

func TestCheckBuiltinRoleCapabilityAndIdentityFailures(t *testing.T) {
	t.Parallel()
	reg := BuiltinRegistry()
	present := allBuiltinSecretsPresent()
	analyst := builtinReadinessSpecs()[1]

	t.Run("lacks required", func(t *testing.T) {
		t.Parallel()
		spec := analyst
		spec.required = []Capability{CapWriteRepository}
		got := checkBuiltinRole(present, reg, spec)
		assert.False(t, got.Ready)
		assert.Contains(t, strings.Join(got.Reasons, "\n"), "lacks required capability write_repository")
	})
	t.Run("forbidden capability", func(t *testing.T) {
		t.Parallel()
		spec := analyst
		spec.forbidden = []Capability{CapApproveMergeRequest}
		got := checkBuiltinRole(present, reg, spec)
		assert.False(t, got.Ready)
		assert.Contains(t, strings.Join(got.Reasons, "\n"), "must not declare capability approve_merge_request")
	})
	t.Run("unmapped agent", func(t *testing.T) {
		t.Parallel()
		spec := analyst
		spec.agents = []string{"no-such-agent"}
		got := checkBuiltinRole(present, reg, spec)
		assert.False(t, got.Ready)
		assert.Contains(t, strings.Join(got.Reasons, "\n"), `agent "no-such-agent" is not mapped`)
	})
	t.Run("wrong identity", func(t *testing.T) {
		t.Parallel()
		spec := analyst
		spec.agents = []string{"code"}
		got := checkBuiltinRole(present, reg, spec)
		assert.False(t, got.Ready)
		assert.Contains(t, strings.Join(got.Reasons, "\n"), `agent "code" maps to role "coder" (want "analyst")`)
	})
}

func TestCheckBuiltinRoleEnforcedResolveFailures(t *testing.T) {
	t.Parallel()
	reg := BuiltinRegistry()
	present := allBuiltinSecretsPresent()
	coder := builtinReadinessSpecs()[2]

	t.Run("unregistered job", func(t *testing.T) {
		t.Parallel()
		spec := coder
		spec.job = AgentJob("e2e")
		got := checkBuiltinRole(present, reg, spec)
		assert.False(t, got.Ready)
		assert.Contains(t, strings.Join(got.Reasons, "\n"), "enforced resolve failed for a provisioned role")
	})
	t.Run("wrong job identity", func(t *testing.T) {
		t.Parallel()
		spec := coder
		spec.job = AgentJob("review")
		got := checkBuiltinRole(present, reg, spec)
		assert.False(t, got.Ready)
		joined := strings.Join(got.Reasons, "\n")
		assert.Contains(t, joined, `enforced resolve selected role "analyst"`)
		assert.Contains(t, joined, `want role "coder"`)
		assert.Contains(t, joined, forge.SecretGitLabAnalystToken)
		assert.Contains(t, joined, forge.SecretGitLabCoderToken)
	})
	t.Run("missing does not fail as unconfigured", func(t *testing.T) {
		t.Parallel()
		spec := coder
		spec.job = AgentJob("e2e")
		got := checkBuiltinRole(map[string]bool{}, reg, spec)
		assert.False(t, got.Ready)
		joined := strings.Join(got.Reasons, "\n")
		assert.Contains(t, joined, "credential not provisioned")
		assert.Contains(t, joined, "missing credential did not fail closed as unconfigured")
	})
}

func TestCheckBuiltinReadinessIgnoresCustomRoles(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "scanner",
			"credential": "own",
			"capabilities": ["read_issues"],
			"agents": ["scanner"]
		}]
	}`)
	present := allBuiltinSecretsPresent()
	present[CustomSecretName(Role("scanner"))] = false
	got := CheckBuiltinReadiness(present, reg)
	assert.True(t, got.Ready, "custom role absence must not fail built-in readiness")
	assert.Empty(t, got.Missing)
	joined := strings.Join(got.Diagnostics, "\n")
	assert.NotContains(t, joined, "scanner")
	assertNoSecretLeak(t, got.Diagnostics)
}

func TestCheckRegisteredReadinessRequiresCustomMappingAndCredential(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "scanner",
			"credential": "own",
			"capabilities": ["read_issues"],
			"agents": ["scanner"]
		}]
	}`)
	present := allBuiltinSecretsPresent()
	present[CustomSecretName(Role("scanner"))] = true
	got := CheckRegisteredReadiness(present, reg)
	assert.True(t, got.Ready)
	assert.Contains(t, strings.Join(got.Diagnostics, "\n"), "registered role scanner: ready")

	present[CustomSecretName(Role("scanner"))] = false
	got = CheckRegisteredReadiness(present, reg)
	assert.False(t, got.Ready)
	assert.Equal(t, []Role{"scanner"}, got.Missing)
	assert.Contains(t, strings.Join(got.Diagnostics, "\n"), "credential not provisioned")
}

func TestCheckRegisteredReadinessRejectsUnmappedCustomRole(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "scanner",
			"credential": "own",
			"capabilities": ["read_issues"],
			"agents": []
		}]
	}`)
	present := allBuiltinSecretsPresent()
	present[CustomSecretName(Role("scanner"))] = true
	got := CheckRegisteredReadiness(present, reg)
	assert.False(t, got.Ready)
	assert.Contains(t, strings.Join(got.Diagnostics, "\n"), "no agent mapping")
}

func TestCheckBuiltinReadinessNoSecretLeak(t *testing.T) {
	t.Parallel()
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	got := CheckBuiltinReadiness(present, Registry{})
	assertNoSecretLeak(t, got.Diagnostics)
	for _, c := range got.Roles {
		assert.NotContains(t, c.SecretName, "glpat-")
		for _, reason := range c.Reasons {
			assert.NotRegexp(t, `glpat-|sk-|ghp_`, reason)
		}
	}
}

func TestBuiltinReadinessWithLifecycle(t *testing.T) {
	t.Parallel()
	base := CheckBuiltinReadiness(allBuiltinSecretsPresent(), Registry{})

	for _, state := range []LifecycleState{LifecycleExpired, LifecycleRevoked, LifecycleUnverified} {
		t.Run(string(state), func(t *testing.T) {
			got := base.WithLifecycle(map[Role]LifecycleState{RolePoller: state})
			assert.False(t, got.Ready)
			assert.Equal(t, []Role{RolePoller}, got.Missing)
			poller := got.Roles[0]
			assert.False(t, poller.Ready)
			assert.Contains(t, poller.Reasons, "credential lifecycle is "+string(state))
			assert.Contains(t, strings.Join(got.Diagnostics, "\n"), "builtin roles ready: 2/3; missing=poller")
		})
	}

	for _, state := range []LifecycleState{LifecycleExpiring, LifecycleOverlapping, LifecycleOK} {
		t.Run(string(state)+" does not block", func(t *testing.T) {
			got := base.WithLifecycle(map[Role]LifecycleState{RolePoller: state})
			assert.True(t, got.Ready)
			assert.Empty(t, got.Missing)
			assert.NotContains(t, strings.Join(got.Roles[0].Reasons, "\n"), "credential lifecycle")
		})
	}

	assert.Equal(t, base, base.WithLifecycle(nil))
	assert.Equal(t, base, base.WithLifecycle(map[Role]LifecycleState{}))

	partial := base
	partial.Roles = append([]BuiltinRoleCheck(nil), base.Roles...)
	partial.Roles[0].Ready = false
	partial.Roles[0].Reasons = []string{"credential not provisioned"}
	partial.Ready = false
	partial.Missing = []Role{RolePoller}
	got := partial.WithLifecycle(map[Role]LifecycleState{RolePoller: LifecycleExpired})
	assert.False(t, got.Ready)
	assert.Equal(t, []Role{RolePoller}, got.Missing)
	assert.Equal(t, []string{"credential not provisioned"}, got.Roles[0].Reasons)

	baseWithReasons := base
	baseWithReasons.Roles = append([]BuiltinRoleCheck(nil), base.Roles...)
	baseWithReasons.Roles[0].Reasons = []string{"existing reason"}
	got = baseWithReasons.WithLifecycle(map[Role]LifecycleState{RolePoller: LifecycleExpired})
	assert.Equal(t, []string{"existing reason"}, baseWithReasons.Roles[0].Reasons)
	assert.Equal(t, []string{"existing reason", "credential lifecycle is expired"}, got.Roles[0].Reasons)
}

func TestRegisteredReadinessWithLifecycle(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{"roles":[{"name":"scanner","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`)
	present := allBuiltinSecretsPresent()
	present[CustomSecretName(Role("scanner"))] = true
	base := CheckRegisteredReadiness(present, reg)
	assert.True(t, base.Ready)

	got := base.WithLifecycle(map[Role]LifecycleState{Role("scanner"): LifecycleRevoked})
	assert.False(t, got.Ready)
	assert.Equal(t, []Role{Role("scanner")}, got.Missing)
	assert.Contains(t, got.Roles[3].Reasons, "credential lifecycle is revoked")
	assert.Contains(t, strings.Join(got.Diagnostics, "\n"), "registered role scanner: not ready")
	assert.NotContains(t, strings.Join(got.Diagnostics, "\n"), "registered role scanner: ready")
	assert.Equal(t, base, base.WithLifecycle(nil))
}

func assertNoSecretLeak(t *testing.T, lines []string) {
	t.Helper()
	for _, line := range lines {
		assert.NotRegexp(t, `glpat-|sk-|ghp_`, line)
		assert.NotContains(t, line, "token-value")
	}
}
