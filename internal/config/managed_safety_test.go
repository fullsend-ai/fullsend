package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustLayer(t *testing.T, overlayYAML, baseYAML string) PerRepoConfigWriter {
	t.Helper()
	var overlay PerRepoConfigWriter
	if strings.TrimSpace(overlayYAML) != "" {
		parsed, err := ParsePerRepoConfigWriter([]byte(overlayYAML))
		require.NoError(t, err)
		overlay = parsed
	} else {
		overlay = NewEmptyPerRepoOverlay()
	}
	var base []byte
	if strings.TrimSpace(baseYAML) != "" {
		base = []byte(baseYAML)
	}
	w, err := LayerOnBase(overlay, base)
	require.NoError(t, err)
	return w
}

func TestCheckManagedSafetyGate_FirstCreationEmptyLayer(t *testing.T) {
	current := mustLayer(t, "", "")
	candidate := mustLayer(t, "kill_switch: true\nroles: []\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"first creation against an empty managed layer is not a relaxation")
}

func TestCheckManagedSafetyGate_FallthroughKeepsParentRestriction(t *testing.T) {
	base := "kill_switch: true\nroles: []\nallowed_remote_resources: []\n"
	current := mustLayer(t, "", base)
	candidate := mustLayer(t, "", base)
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"omitting a key that falls through to the same parent is not a relaxation")
}

func TestCheckManagedSafetyGate_ImplicitKillSwitchDrop(t *testing.T) {
	current := mustLayer(t, "kill_switch: true\n", "")
	candidate := mustLayer(t, "", "")
	got := CheckManagedSafetyGate(current, candidate)
	require.Len(t, got, 1)
	assert.Equal(t, "kill_switch", got[0].Key)
	assert.Contains(t, got[0].String(), "not explicitly declared")
}

func TestCheckManagedSafetyGate_ExplicitKillSwitchFalseAllowed(t *testing.T) {
	current := mustLayer(t, "kill_switch: true\n", "")
	candidate := mustLayer(t, "kill_switch: false\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"manifest-declared kill_switch: false is an explicit relaxation")
}

func TestCheckManagedSafetyGate_ImplicitEmptyRolesDrop(t *testing.T) {
	current := mustLayer(t, "roles: []\n", "")
	candidate := mustLayer(t, "", "")
	got := CheckManagedSafetyGate(current, candidate)
	require.Len(t, got, 1)
	assert.Equal(t, "roles", got[0].Key)
	assert.Equal(t, "[]", got[0].Current)
}

func TestCheckManagedSafetyGate_ExplicitEmptyRolesNotARelaxation(t *testing.T) {
	current := mustLayer(t, "roles:\n  - triage\n", "")
	candidate := mustLayer(t, "roles: []\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate))
}

func TestCheckManagedSafetyGate_ImplicitAllowlistDenyAllDrop(t *testing.T) {
	current := mustLayer(t, "allowed_remote_resources: []\n", "")
	candidate := mustLayer(t, "", "")
	got := CheckManagedSafetyGate(current, candidate)
	require.Len(t, got, 1)
	assert.Equal(t, "allowed_remote_resources", got[0].Key)
	assert.Equal(t, "deny-all", got[0].Current)
}

func TestCheckManagedSafetyGate_ExplicitEmptyAllowlistNotEquivalentToOmitted(t *testing.T) {
	current := mustLayer(t, "", "")
	candidate := mustLayer(t, "allowed_remote_resources: []\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"explicit empty allowlist is deny-all, which is more restrictive")
}

func TestCheckManagedSafetyGate_AllowlistNarrowing(t *testing.T) {
	current := mustLayer(t, "allowed_remote_resources:\n  - https://extra.example.com/\n", "")
	candidate := mustLayer(t, "", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"dropping extra prefixes narrows the effective allowlist")
}

func TestCheckManagedSafetyGate_AllowlistWideningExplicit(t *testing.T) {
	current := mustLayer(t, "allowed_remote_resources: []\n", "")
	candidate := mustLayer(t, "allowed_remote_resources:\n  - https://github.com/\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"an explicit allowlist is a declared relaxation of deny-all")
}

func TestCheckManagedSafetyGate_AgentSuppressionImplicitDrop(t *testing.T) {
	current := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n", "")
	candidate := mustLayer(t, "", "")
	got := CheckManagedSafetyGate(current, candidate)
	require.Len(t, got, 1)
	assert.Equal(t, "agents.review.enabled", got[0].Key)
}

func TestCheckManagedSafetyGate_AgentSuppressionNameOnlyNotExplicit(t *testing.T) {
	current := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n", "")
	candidate := mustLayer(t, "agents:\n  - name: review\n", "")
	got := CheckManagedSafetyGate(current, candidate)
	require.Len(t, got, 1)
	assert.Equal(t, "agents.review.enabled", got[0].Key)
}

func TestCheckManagedSafetyGate_AgentSuppressionExplicitEnable(t *testing.T) {
	current := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n", "")
	candidate := mustLayer(t, "agents:\n  - name: review\n    enabled: true\n    runtime: claude\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate))
}

func TestCheckManagedSafetyGate_AgentSuppressionKept(t *testing.T) {
	current := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n", "")
	candidate := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate))
}

func TestCheckManagedSafetyGate_AgentSuppressionDisableThenOverrideOnlyStillRelaxes(t *testing.T) {
	current := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n", "")
	candidate := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n  - name: review\n    runtime: claude\n", "")
	got := CheckManagedSafetyGate(current, candidate)
	require.Len(t, got, 1,
		"last-writer-wins: the override-only entry doesn't restate enabled: false, so the agent is really enabled")
	assert.Equal(t, "agents.review.enabled", got[0].Key)
}

func TestCheckManagedSafetyGate_AgentSuppressionDisableThenExplicitEnableAllowed(t *testing.T) {
	current := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n", "")
	candidate := mustLayer(t, "agents:\n  - name: review\n    enabled: false\n  - name: review\n    enabled: true\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"the last entry explicitly re-enables the agent, an authorized relaxation")
}

func TestCheckManagedSafetyGate_CustomAgentRemovalNotFlaggedAsRelaxation(t *testing.T) {
	current := mustLayer(t, "agents:\n  - source: https://example.com/agents/custom.yaml\n    enabled: false\n", "")
	candidate := mustLayer(t, "", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"a disabled custom agent absent from the candidate was removed, not re-enabled")
}

func TestCheckManagedSafetyGate_CreateIssuesImplicitWidenViaParent(t *testing.T) {
	base := "create_issues:\n  allow_targets:\n    orgs:\n      - acme\n      - other\n"
	current := mustLayer(t, "create_issues:\n  allow_targets:\n    orgs:\n      - acme\n", base)
	candidate := mustLayer(t, "", base)
	got := CheckManagedSafetyGate(current, candidate)
	require.Len(t, got, 1)
	assert.Equal(t, "create_issues.allow_targets", got[0].Key)
}

func TestCheckManagedSafetyGate_CreateIssuesExplicitWiden(t *testing.T) {
	current := mustLayer(t, "create_issues:\n  allow_targets:\n    orgs:\n      - acme\n", "")
	candidate := mustLayer(t, "create_issues:\n  allow_targets:\n    orgs:\n      - acme\n      - other\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate))
}

func TestCheckManagedSafetyGate_CreateIssuesNarrowing(t *testing.T) {
	current := mustLayer(t, "create_issues:\n  allow_targets:\n    orgs:\n      - acme\n      - other\n", "")
	candidate := mustLayer(t, "create_issues:\n  allow_targets:\n    orgs:\n      - acme\n", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate))
}

func TestCheckManagedSafetyGate_CreateIssuesDropToUnsetIsNarrowing(t *testing.T) {
	current := mustLayer(t, "create_issues:\n  allow_targets:\n    orgs:\n      - acme\n", "")
	candidate := mustLayer(t, "", "")
	assert.Empty(t, CheckManagedSafetyGate(current, candidate),
		"omitting create_issues removes extra targets and is more restrictive")
}

func TestCheckManagedSafetyGate_NilInputs(t *testing.T) {
	got := CheckManagedSafetyGate(nil, NewEmptyPerRepoOverlay())
	require.Len(t, got, 1, "a missing comparison operand must fail closed, not read as \"no relaxations\"")
	assert.Equal(t, "managed_safety_gate", got[0].Key)

	got = CheckManagedSafetyGate(NewEmptyPerRepoOverlay(), nil)
	require.Len(t, got, 1, "a missing comparison operand must fail closed, not read as \"no relaxations\"")
	assert.Equal(t, "managed_safety_gate", got[0].Key)
}

func TestCheckManagedSafetyGateFromLayers_CurrentYAMLAndInvalid(t *testing.T) {
	candidate := NewEmptyPerRepoOverlay()
	got, err := CheckManagedSafetyGateFromLayers(
		[]byte("kill_switch: true\n"),
		candidate,
		nil,
	)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "kill_switch", got[0].Key)

	_, err = CheckManagedSafetyGateFromLayers([]byte(": not yaml"), candidate, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing current config")

	_, err = CheckManagedSafetyGateFromLayers(nil, candidate, []byte(": not yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "layering config")
}

func TestFormatAllowTargetsNil(t *testing.T) {
	assert.Equal(t, "unset", formatAllowTargets(nil))
}

func TestFormatSafetyRelaxationsAndKeys(t *testing.T) {
	assert.Empty(t, FormatSafetyRelaxations(nil))
	rs := []SafetyRelaxation{
		{Key: "kill_switch", Current: "true", Candidate: "false"},
		{Key: "roles", Current: "[]", Candidate: "[triage]"},
	}
	text := FormatSafetyRelaxations(rs)
	assert.Contains(t, text, "kill_switch")
	assert.Contains(t, text, "roles")
	assert.Equal(t, []string{"kill_switch", "roles"}, SafetyRelaxationKeys(rs))
}
