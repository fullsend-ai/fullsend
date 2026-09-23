package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestYamlKeysOf_PerRepoConfigIncludesForbiddenShorthands(t *testing.T) {
	keys := yamlKeysOf(perRepoConfig{})
	assert.True(t, keys["runtime"], "runtime is a config.yaml field")
	assert.True(t, keys["allowed_remote_resources"], "allowed_remote_resources is a config.yaml field")
	assert.True(t, keys["kill_switch"])
	assert.True(t, keys["roles"])
	assert.True(t, keys["agents"])
	assert.True(t, keys["inference"])
	assert.True(t, keys["models"])
	assert.False(t, keys["parent"], "parent must stay unexported from YAML")
}

func TestOverlayConfig_DecodeSparseAndRoundTrip(t *testing.T) {
	input := `
kill_switch: true
roles:
  - triage
  - review
inference:
  region: us-east1
models:
  aliases:
    opus: anthropic-vertex/claude-opus
`
	var o OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(input), &o))
	require.True(t, o.IsSet())
	w := o.Writer()
	require.NotNil(t, w)
	assert.True(t, w.IsKillSwitchActive())
	assert.Equal(t, []string{"triage", "review"}, w.ConfigRoles())
	assert.Equal(t, "us-east1", w.ConfigInferenceRegion())
	assert.Equal(t, "anthropic-vertex/claude-opus", w.ConfigModelAliases()["opus"])

	encoded, err := yaml.Marshal(&o)
	require.NoError(t, err)
	text := string(encoded)
	assert.Contains(t, text, "kill_switch: true")
	assert.Contains(t, text, "triage")
	assert.NotContains(t, text, "runtime:")
	assert.NotContains(t, text, "allowed_remote_resources:")
	assert.NotContains(t, text, "version:")
}

func TestOverlayConfig_EmptyMappingIsSet(t *testing.T) {
	var o OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte("{}\n"), &o))
	assert.True(t, o.IsSet(), "config: {} opts in with no values")
	assert.False(t, o.IsZero())
}

func TestOverlayConfig_MarshalYAMLNil(t *testing.T) {
	v, err := OverlayConfig{}.MarshalYAML()
	require.NoError(t, err)
	assert.Nil(t, v)
}

func TestOverlayConfig_NullIsUnset(t *testing.T) {
	var o OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte("null\n"), &o))
	assert.False(t, o.IsSet(), "config: null is treated as omitted")
}

func TestOverlayConfig_OmittedIsUnset(t *testing.T) {
	type wrap struct {
		Config OverlayConfig `yaml:"config,omitempty"`
	}
	var w wrap
	require.NoError(t, yaml.Unmarshal([]byte("other: 1\n"), &w))
	assert.False(t, w.Config.IsSet())
	assert.True(t, w.Config.IsZero())
}

func TestOverlayConfig_RejectsForbiddenAndUnknownFields(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "runtime",
			yaml:    "runtime: pi\nkill_switch: true\n",
			wantErr: "runtime is not allowed inside config",
		},
		{
			name:    "allowed_remote_resources",
			yaml:    "allowed_remote_resources: []\n",
			wantErr: "allowed_remote_resources is not allowed inside config",
		},
		{
			name:    "unknown field",
			yaml:    "bogus: true\n",
			wantErr: `unknown field "bogus"`,
		},
		{
			name:    "scalar",
			yaml:    "https://example.com/preset.yaml\n",
			wantErr: "must be a YAML mapping",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o OverlayConfig
			err := yaml.Unmarshal([]byte(tt.yaml), &o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestOverlayConfig_RejectsBothForbiddenFields(t *testing.T) {
	var o OverlayConfig
	err := yaml.Unmarshal([]byte("runtime: pi\nallowed_remote_resources:\n  - https://example.com/\n"), &o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtime is not allowed")
	assert.Contains(t, err.Error(), "allowed_remote_resources is not allowed")
}

func TestOverlayConfig_ExplicitFalseAndEmptySurviveMarshal(t *testing.T) {
	input := `
kill_switch: false
keep_history: false
roles: []
`
	var o OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(input), &o))
	w := o.Writer()
	require.NotNil(t, w)
	assert.False(t, w.IsKillSwitchActive())
	assert.False(t, w.ConfigKeepHistory())
	assert.NotNil(t, w.ConfigRoles())
	assert.Empty(t, w.ConfigRoles())

	body, err := w.Marshal()
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, "kill_switch: false")
	assert.Contains(t, text, "keep_history: false")
	assert.Contains(t, text, "roles: []")
}

func TestMergeOverlays_ChildWinsAndParentFillsGaps(t *testing.T) {
	var parent OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
kill_switch: true
roles:
  - triage
inference:
  region: us-east1
  project: parent-proj
models:
  aliases:
    opus: parent-opus
    sonnet: parent-sonnet
`), &parent))
	var child OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
kill_switch: false
inference:
  project: child-proj
models:
  aliases:
    sonnet: child-sonnet
`), &child))

	merged := MergeOverlays(parent.Writer(), child.Writer())
	require.NotNil(t, merged)
	assert.False(t, merged.IsKillSwitchActive(), "child explicit false wins")
	assert.Equal(t, []string{"triage"}, merged.ConfigRoles(), "unset child inherits parent roles")
	assert.Equal(t, "us-east1", merged.ConfigInferenceRegion(), "parent nested field kept")
	assert.Equal(t, "child-proj", merged.ConfigInferenceProject(), "child nested field wins")
	assert.Equal(t, "parent-opus", merged.ConfigModelAliases()["opus"])
	assert.Equal(t, "child-sonnet", merged.ConfigModelAliases()["sonnet"])

	body, err := merged.Marshal()
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, "kill_switch: false")
	assert.Contains(t, text, "triage")
	assert.Contains(t, text, "us-east1")
	assert.Contains(t, text, "child-proj")
	assert.NotContains(t, text, "parent-proj")
	assert.NotContains(t, text, "runtime:")
}

func TestMergeOverlays_AgentsKeyedMerge(t *testing.T) {
	var parent OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - name: code
    model: opus
`), &parent))
	var child OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - name: code
    effort: high
`), &child))

	merged := MergeOverlays(parent.Writer(), child.Writer())
	entries := merged.AgentEntries()
	require.Len(t, entries, 1)
	assert.Equal(t, "code", entries[0].Name)
	assert.Equal(t, "opus", entries[0].Model)
	assert.Equal(t, "high", entries[0].Effort)
}

func TestMergeOverlays_NilLayers(t *testing.T) {
	var only OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte("mint_url: https://mint.example.com\n"), &only))

	assert.Nil(t, MergeOverlays(nil, nil))

	fromParent := MergeOverlays(only.Writer(), nil)
	require.NotNil(t, fromParent)
	assert.Equal(t, "https://mint.example.com", fromParent.ConfigMintURL())

	fromChild := MergeOverlays(nil, only.Writer())
	require.NotNil(t, fromChild)
	assert.Equal(t, "https://mint.example.com", fromChild.ConfigMintURL())
}

func TestApplyOverlayShorthands_OnlySetsPresentValues(t *testing.T) {
	overlay := NewEmptyPerRepoOverlay()
	ApplyOverlayShorthands(overlay, "", nil)
	body, err := overlay.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(body), "runtime:")
	assert.NotContains(t, string(body), "allowed_remote_resources:")

	ApplyOverlayShorthands(overlay, "pi", []string{})
	assert.Equal(t, "pi", overlay.ConfigRuntime())
	assert.NotNil(t, overlay.AllowedResources())
	assert.Empty(t, overlay.AllowedResources())
	body, err = overlay.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(body), "runtime: pi")
	assert.Contains(t, string(body), "allowed_remote_resources: []")
}

func TestLayerOnBase_OverlayWinsOverBaseAndCodeDefaults(t *testing.T) {
	var overlay OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
kill_switch: false
roles:
  - review
`), &overlay))
	base := []byte(`
version: "1"
kill_switch: true
roles:
  - triage
  - coder
mint_url: https://base.example.com
`)
	effective, err := LayerOnBase(overlay.Writer(), base)
	require.NoError(t, err)
	assert.False(t, effective.IsKillSwitchActive(), "overlay false wins over base true")
	assert.Equal(t, []string{"review"}, effective.ConfigRoles())
	assert.Equal(t, "https://base.example.com", effective.ConfigMintURL(), "unset overlay inherits base")
	assert.Equal(t, "claude", effective.ConfigRuntime(), "unset overlay and base fall through to code default")

	body, err := effective.Marshal()
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, "kill_switch: false")
	assert.Contains(t, text, "review")
	assert.NotContains(t, text, "mint_url:", "base values must not be baked into the marshaled overlay")
	assert.NotContains(t, text, "runtime:")
}

func TestLayerOnBase_EmptyBaseUsesCodeDefaults(t *testing.T) {
	effective, err := LayerOnBase(NewEmptyPerRepoOverlay(), nil)
	require.NoError(t, err)
	assert.Equal(t, "claude", effective.ConfigRuntime())
	assert.Equal(t, PerRepoDefaultRoles(), effective.ConfigRoles())
}

func TestOverlayConfig_RejectsNestedUnknownField(t *testing.T) {
	var o OverlayConfig
	err := yaml.Unmarshal([]byte("inference:\n  bogus: true\n"), &o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestOverlayConfig_InvalidRoleIsSemanticError(t *testing.T) {
	var o OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte("roles: [not-a-role]\n"), &o))
	err := o.Writer().Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid role "not-a-role"`)
}

func TestOverlayConfig_RejectsUnknownAgentEntryField(t *testing.T) {
	var o OverlayConfig
	err := yaml.Unmarshal([]byte("agents:\n  - name: code\n    efort: high\n"), &o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agents[0]: unknown field "efort"`)
}

func TestOverlayConfig_AgentEntryStringShorthandUnaffectedByUnknownFieldCheck(t *testing.T) {
	var o OverlayConfig
	err := yaml.Unmarshal([]byte("agents:\n  - https://example.com/harness/custom.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890\n"), &o)
	require.NoError(t, err)
}

// ValidateOverlayLayer validates a single, isolated overlay layer (ADR
// 0122) — before defaults.config/repo config are merged and before the
// repos.yaml allowed_remote_resources shorthand is applied. It must defer
// the agent-allowlist and override-only-built-in-name checks, which need
// information this layer alone doesn't have; ValidateMergedOverlay runs
// them once that information is available (except the built-in-name
// check, which needs config.base.yaml and so is never enforced by
// either — see internal/repos/overlay.go).
func TestValidateOverlayLayer_DefersAllowlistAndBuiltinNameChecks(t *testing.T) {
	var o OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - source: "https://example.com/harness/custom.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
  - name: mycustom
    runtime: pi
`), &o))
	w := o.Writer()

	require.NoError(t, ValidateOverlayLayer(w),
		"isolated layer must not reject a URL agent for lacking an allowlist, or an override-only entry for not naming a built-in agent")

	// The same agents fail strict, full validation (used for a complete,
	// self-contained config), which requires both.
	assert.Error(t, w.Validate())
}

func TestValidateMergedOverlay_EnforcesAllowlistButNotBuiltinName(t *testing.T) {
	var o OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - source: "https://example.com/harness/custom.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
  - name: mycustom
    runtime: pi
`), &o))
	w := o.Writer()

	// Before the allowed_remote_resources shorthand is merged in, the URL
	// agent still fails.
	err := ValidateMergedOverlay(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not covered by allowed_remote_resources")

	// Once the shorthand is applied (as managedOverlay does), the URL
	// agent passes; the override-only entry naming "mycustom" still isn't
	// required to be a built-in agent — config.base.yaml, which may
	// register it, isn't layered on until install/converge time.
	w.SetAllowedRemoteResources([]string{"https://example.com/"})
	assert.NoError(t, ValidateMergedOverlay(w))
}

func TestMergeOverlays_AllFieldsAndAgentClone(t *testing.T) {
	enabled := false
	sub := "sonnet"
	parent := &perRepoConfig{
		Version:     "1",
		Forge:       "github",
		Tracker:     "jira",
		Runtime:     "claude",
		MintURL:     "https://parent.example.com",
		KillSwitch:  boolPtr(true),
		KeepHistory: boolPtr(true),
		Roles:       []string{"triage"},
		Agents: []AgentEntry{{
			Name:    "code",
			Source:  "harness/code.yaml",
			Ref:     "main",
			Enabled: boolPtr(true),
			Model:   "opus",
			Subagents: map[string]*string{
				"default": &sub,
				"old":     nil,
			},
		}},
		AllowedRemoteResources: []string{"https://parent.example.com/"},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{Orgs: []string{"acme"}, Repos: []string{"acme/app"}},
		},
		Authorization: []AuthorizationProvider{{Provider: "owners_file"}},
		Notifications: &StatusNotificationConfig{Comment: CommentNotificationConfig{Start: "enabled"}},
		Inference: &PerRepoInferenceConfig{
			Provider:    "vertex",
			Project:     "parent-proj",
			Region:      "us-east1",
			WIFProvider: "parent-wif",
			OpenAI:      &OpenAIWIFConfig{Audience: "parent-aud", IdentityProviderID: "parent-idp", ServiceAccountID: "parent-sa"},
		},
		Models: &ModelsConfig{Aliases: map[string]string{"opus": "parent-opus"}},
		parent: &perRepoDefaults{},
	}
	child := &perRepoConfig{
		Version:     "1",
		Forge:       "gitlab",
		Tracker:     "gitlab",
		Runtime:     "pi",
		MintURL:     "https://child.example.com",
		KillSwitch:  boolPtr(false),
		KeepHistory: boolPtr(false),
		Roles:       []string{"review"},
		Agents: []AgentEntry{{
			Name:    "code",
			Enabled: &enabled,
			Effort:  "high",
			Subagents: map[string]*string{
				"reviewer": &sub,
			},
		}},
		AllowedRemoteResources: []string{"https://child.example.com/"},
		CreateIssues: &CreateIssuesConfig{
			AllowTargets: AllowTargets{Repos: []string{"acme/other"}},
		},
		Authorization: []AuthorizationProvider{{Provider: "owners_file"}},
		Notifications: &StatusNotificationConfig{Comment: CommentNotificationConfig{Start: "disabled"}},
		Inference: &PerRepoInferenceConfig{
			Project:     "child-proj",
			WIFProvider: "child-wif",
			OpenAI:      &OpenAIWIFConfig{Audience: "child-aud"},
		},
		Models: &ModelsConfig{Aliases: map[string]string{"sonnet": "child-sonnet"}},
		parent: &perRepoDefaults{},
	}

	merged := MergeOverlays(parent, child)
	require.NotNil(t, merged)
	assert.Equal(t, "gitlab", merged.ConfigForge())
	assert.Equal(t, "gitlab", merged.ConfigTracker())
	assert.Equal(t, "pi", merged.ConfigRuntime())
	assert.Equal(t, "https://child.example.com", merged.ConfigMintURL())
	assert.False(t, merged.IsKillSwitchActive())
	assert.False(t, merged.ConfigKeepHistory())
	assert.Equal(t, []string{"review"}, merged.ConfigRoles())
	body, err := merged.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(body), "https://child.example.com/")
	assert.Contains(t, string(body), "https://parent.example.com/")
	require.NotNil(t, merged.IssueCreationConfig())
	assert.Equal(t, []string{"acme/other"}, merged.IssueCreationConfig().AllowTargets.Repos)
	assert.True(t, merged.IsOwnersFileAuthEnabled())
	require.NotNil(t, merged.StatusNotifications())
	assert.Equal(t, "disabled", merged.StatusNotifications().Comment.Start)
	assert.Equal(t, "vertex", merged.ConfigInferenceProvider())
	assert.Equal(t, "child-proj", merged.ConfigInferenceProject())
	assert.Equal(t, "us-east1", merged.ConfigInferenceRegion())
	assert.Equal(t, "child-wif", merged.ConfigInferenceWIFProvider())
	assert.Equal(t, "child-aud", merged.ConfigInferenceOpenAI().Audience)
	assert.Equal(t, "parent-idp", merged.ConfigInferenceOpenAI().IdentityProviderID)
	assert.Equal(t, "parent-opus", merged.ConfigModelAliases()["opus"])
	assert.Equal(t, "child-sonnet", merged.ConfigModelAliases()["sonnet"])

	entries := merged.AgentEntries()
	require.Len(t, entries, 1)
	assert.Equal(t, "harness/code.yaml", entries[0].Source)
	assert.Equal(t, "high", entries[0].Effort)
	assert.False(t, entries[0].IsEnabled())
	require.Contains(t, entries[0].Subagents, "reviewer")

	// cloneOverlay must not alias nested pointers from the parent.
	parent.Inference.Project = "mutated"
	assert.Equal(t, "child-proj", merged.ConfigInferenceProject())
}

func TestMergeOverlays_ModelsOntoEmptyParentAndShorthandNil(t *testing.T) {
	child := &perRepoConfig{
		Models: &ModelsConfig{Aliases: map[string]string{"haiku": "child-haiku"}},
		parent: &perRepoDefaults{},
	}
	merged := MergeOverlays(NewEmptyPerRepoOverlay(), child)
	require.NotNil(t, merged)
	assert.Equal(t, "child-haiku", merged.ConfigModelAliases()["haiku"])

	ApplyOverlayShorthands(nil, "pi", []string{"https://example.com/"})
	assert.Nil(t, OverlayConfig{}.Writer())

	_, err := LayerOnBase(NewEmptyPerRepoOverlay(), []byte(": not yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing base config")
}

func boolPtr(v bool) *bool { return &v }

func TestOverlayConfig_MarshalOmitsUnset(t *testing.T) {
	type wrap struct {
		Config OverlayConfig `yaml:"config,omitempty"`
	}
	encoded, err := yaml.Marshal(wrap{})
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(encoded), "config:"), "unset overlay must be omitted")
}
