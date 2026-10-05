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
	reg := BuiltinRegistry()
	job := JobForAgent(reg, "grillme", "review")
	assert.Equal(t, "review", job.Name)

	unmapped := JobForAgent(reg, "e2e", "unknown")
	assert.Equal(t, "e2e", unmapped.Name)
}

func TestSelectBuiltinAndCustomRoles(t *testing.T) {
	env := map[string]string{
		forge.SecretGitLabPollerToken:     "poller-token",
		forge.SecretGitLabAnalystToken:    "analyst-token",
		forge.SecretGitLabCoderToken:      "coder-token",
		forge.VarGitLabRoleRegistry:       `{"roles":[{"name":"scanner","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`,
		CustomSecretName(Role("scanner")): "scanner-token",
	}
	for _, tc := range []struct {
		job    Job
		role   Role
		secret string
	}{
		{PollerJob(), RolePoller, forge.SecretGitLabPollerToken},
		{AgentJob("review"), RoleAnalyst, forge.SecretGitLabAnalystToken},
		{AgentJob("code"), RoleCoder, forge.SecretGitLabCoderToken},
		{AgentJob("scanner"), Role("scanner"), CustomSecretName(Role("scanner"))},
	} {
		t.Run(string(tc.job.Kind)+"/"+tc.job.Name, func(t *testing.T) {
			sel, err := Select(tc.job, testGetenv(env))
			require.NoError(t, err)
			assert.Equal(t, tc.role, sel.Source.Role)
			assert.Equal(t, tc.secret, sel.Source.SecretName)
			token, err := sel.Token(testGetenv(env))
			require.NoError(t, err)
			assert.NotContains(t, strings.Join(sel.Diagnostics(), "\n"), token)
		})
	}
}

func TestSelectIgnoresLegacyEnvironment(t *testing.T) {
	env := map[string]string{
		forge.SecretForgeToken:        "shared-token",
		forge.SecretGitLabPollerToken: "poller-token",
	}
	sel, err := Select(PollerJob(), testGetenv(env))
	require.NoError(t, err)
	assert.Equal(t, forge.SecretGitLabPollerToken, sel.Source.SecretName)
}

func TestSelectFailsClosed(t *testing.T) {
	_, err := SelectAgent("unknown", "", testGetenv(map[string]string{}))
	assert.ErrorIs(t, err, ErrUnregistered)
	_, err = Select(AgentJob("review"), testGetenv(map[string]string{
		forge.SecretForgeToken: "shared",
	}))
	assert.ErrorIs(t, err, ErrUnconfigured)
	assert.Contains(t, err.Error(), forge.SecretGitLabAnalystToken)

	_, err = Select(AgentJob("review"), testGetenv(map[string]string{
		forge.SecretGitLabAnalystToken: "analyst",
	}))
	assert.NoError(t, err)

	err = AuthFailed(RoleAnalyst, forge.SecretGitLabAnalystToken)
	assert.ErrorIs(t, err, ErrAuthFailed)
	assert.Contains(t, err.Error(), forge.SecretGitLabAnalystToken)
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestSelectInvalidRegistryFailsClosed(t *testing.T) {
	_, err := Select(PollerJob(), testGetenv(map[string]string{
		forge.VarGitLabRoleRegistry: `{"roles":[{"name":"x","token":"glpat-LEAKED"}]}`,
	}))
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestTokenAndDiagnostics(t *testing.T) {
	_, err := (Selection{}).Token(testGetenv(nil))
	assert.ErrorIs(t, err, ErrUnconfigured)
	sel := Selection{Source: Source{Role: RoleAnalyst, SecretName: forge.SecretGitLabAnalystToken}}
	_, err = sel.Token(testGetenv(map[string]string{forge.SecretGitLabAnalystToken: "  "}))
	assert.ErrorIs(t, err, ErrUnconfigured)
	assert.Contains(t, strings.Join(sel.Diagnostics(), "\n"), "role=analyst")
}

func TestRequireCapabilities(t *testing.T) {
	reg := BuiltinRegistry()
	poller, _ := reg.Lookup(RolePoller)
	analyst, _ := reg.Lookup(RoleAnalyst)
	coder, _ := reg.Lookup(RoleCoder)
	require.NoError(t, Require(poller, CapWritePollState))
	require.NoError(t, Require(analyst, CapApproveMergeRequest))
	require.NoError(t, Require(coder, CapWriteRepository))
	assert.ErrorIs(t, Require(analyst, CapWriteRepository), ErrCapabilityDenied)
	assert.ErrorIs(t, Require(coder, CapApproveMergeRequest), ErrCapabilityDenied)
}
