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

func TestManagedConfig_DecodeSparseAndRoundTrip(t *testing.T) {
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
	var o ManagedConfig
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

func TestManagedConfig_EmptyMappingIsSet(t *testing.T) {
	var o ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("{}\n"), &o))
	assert.True(t, o.IsSet(), "config: {} opts in with no values")
	assert.False(t, o.IsZero())
}

func TestManagedConfig_MarshalYAMLNil(t *testing.T) {
	v, err := ManagedConfig{}.MarshalYAML()
	require.NoError(t, err)
	assert.Nil(t, v)
}

func TestManagedConfig_NullIsUnset(t *testing.T) {
	var o ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("null\n"), &o))
	assert.False(t, o.IsSet(), "config: null is treated as omitted")
}

func TestManagedConfig_OmittedIsUnset(t *testing.T) {
	type wrap struct {
		Config ManagedConfig `yaml:"config,omitempty"`
	}
	var w wrap
	require.NoError(t, yaml.Unmarshal([]byte("other: 1\n"), &w))
	assert.False(t, w.Config.IsSet())
	assert.True(t, w.Config.IsZero())
}

func TestManagedConfig_RejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
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
		{
			name:    "invalid runtime",
			yaml:    "runtime: bogus\n",
			wantErr: `invalid runtime "bogus"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o ManagedConfig
			err := yaml.Unmarshal([]byte(tt.yaml), &o)
			if err == nil {
				err = ValidateManagedLayer(o.Writer())
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestManagedConfig_AcceptsRuntimeAndAllowedRemoteResources covers #8330:
// every per-repo config field, including runtime and
// allowed_remote_resources, is valid inside a repos.yaml config block, and
// an explicit empty allowlist survives a marshal round trip.
func TestManagedConfig_AcceptsRuntimeAndAllowedRemoteResources(t *testing.T) {
	var o ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("runtime: pi\nallowed_remote_resources: []\n"), &o))
	require.NoError(t, ValidateManagedLayer(o.Writer()))
	assert.Equal(t, "pi", o.ExplicitRuntime())
	assert.Equal(t, "pi", o.Writer().ConfigRuntime())
	assert.NotNil(t, o.Writer().AllowedResources())
	assert.Empty(t, o.Writer().AllowedResources())

	body, err := yaml.Marshal(o)
	require.NoError(t, err)
	assert.Contains(t, string(body), "runtime: pi")
	assert.Contains(t, string(body), "allowed_remote_resources: []")

	assert.Empty(t, ManagedConfig{}.ExplicitRuntime())
	var noRuntime ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("kill_switch: true\n"), &noRuntime))
	assert.Empty(t, noRuntime.ExplicitRuntime(), "ExplicitRuntime does not fall back to code defaults")
}

func TestManagedConfig_ExplicitFalseAndEmptySurviveMarshal(t *testing.T) {
	input := `
kill_switch: false
keep_history: false
roles: []
`
	var o ManagedConfig
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

func TestMergeManaged_ExplicitEmptyAuthorizationSurvivesRoundTrip(t *testing.T) {
	var parent ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
authorization:
  - provider: owners_file
`), &parent))
	var child ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
authorization: []
`), &child))

	merged := MergeManaged(parent.Writer(), child.Writer())
	require.NotNil(t, merged)
	assert.False(t, merged.IsOwnersFileAuthEnabled(), "explicit empty authorization overrides parent's owners_file")

	body, err := merged.Marshal()
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, "authorization: []", "explicit empty authorization must round-trip as an empty list, not be omitted")

	// Re-decoding the marshaled overlay must still carry an explicit
	// empty (non-nil) authorization rather than an omitted/nil one, so a
	// second merge against a fresh owners_file parent still loses.
	var reloaded ManagedConfig
	require.NoError(t, yaml.Unmarshal(body, &reloaded))
	remerged := MergeManaged(parent.Writer(), reloaded.Writer())
	assert.False(t, remerged.IsOwnersFileAuthEnabled(), "re-decoded empty authorization must still win over parent owners_file")
}

func TestMergeManaged_ChildWinsAndParentFillsGaps(t *testing.T) {
	var parent ManagedConfig
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
	var child ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
kill_switch: false
inference:
  project: child-proj
models:
  aliases:
    sonnet: child-sonnet
`), &child))

	merged := MergeManaged(parent.Writer(), child.Writer())
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

func TestMergeManaged_AgentsKeyedMerge(t *testing.T) {
	var parent ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - name: code
    model: opus
`), &parent))
	var child ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - name: code
    effort: high
`), &child))

	merged := MergeManaged(parent.Writer(), child.Writer())
	entries := merged.AgentEntries()
	require.Len(t, entries, 1)
	assert.Equal(t, "code", entries[0].Name)
	assert.Equal(t, "opus", entries[0].Model)
	assert.Equal(t, "high", entries[0].Effort)
}

func TestMergeManaged_NilLayers(t *testing.T) {
	var only ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("mint_url: https://mint.example.com\n"), &only))

	var nilLayers PerRepoConfigWriter = MergeManaged(nil, nil)
	assert.True(t, nilLayers == nil, "MergeManaged(nil, nil) must return an untyped nil interface, not a typed-nil pointer boxed in a non-nil interface")

	fromParent := MergeManaged(only.Writer(), nil)
	require.NotNil(t, fromParent)
	assert.Equal(t, "https://mint.example.com", fromParent.ConfigMintURL())

	fromChild := MergeManaged(nil, only.Writer())
	require.NotNil(t, fromChild)
	assert.Equal(t, "https://mint.example.com", fromChild.ConfigMintURL())
}

func TestLayerOnBase_OverlayWinsOverBaseAndCodeDefaults(t *testing.T) {
	var overlay ManagedConfig
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

func TestDeclaredInfrastructureValues_OverlayWinsOverBaseWithoutCodeDefaults(t *testing.T) {
	var overlay ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
inference:
  region: us-east5
`), &overlay))
	base := []byte(`
mint_url: https://base.example.com
inference:
  project: base-project
  region: europe-west1
  wif_provider: projects/1/locations/global/workloadIdentityPools/p/providers/x
`)
	got, err := DeclaredInfrastructureValues(overlay.Writer(), base)
	require.NoError(t, err)
	assert.Equal(t, DeclaredInfrastructure{
		MintURL:              "https://base.example.com",
		InferenceProject:     "base-project",
		InferenceRegion:      "us-east5",
		InferenceWIFProvider: "projects/1/locations/global/workloadIdentityPools/p/providers/x",
	}, got)
	// The overlay is not mutated by the computation.
	assert.Equal(t, DefaultPerRepoMintURL, overlay.Writer().ConfigMintURL())
}

func TestDeclaredInfrastructureValues_NothingDeclaredIsEmpty(t *testing.T) {
	got, err := DeclaredInfrastructureValues(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, DeclaredInfrastructure{}, got, "code defaults (public mint, global region) are not declared values")

	got, err = DeclaredInfrastructureValues(NewEmptyPerRepoOverlay(), []byte("roles: [triage]\n"))
	require.NoError(t, err)
	assert.Equal(t, DeclaredInfrastructure{}, got)
}

func TestDeclaredInfrastructureValues_InvalidBase(t *testing.T) {
	_, err := DeclaredInfrastructureValues(nil, []byte("mint_url: [unterminated"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing base config")
}

func TestManagedConfig_RejectsNestedUnknownField(t *testing.T) {
	var o ManagedConfig
	err := yaml.Unmarshal([]byte("inference:\n  bogus: true\n"), &o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestManagedConfig_InvalidRoleIsSemanticError(t *testing.T) {
	var o ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("roles: [not-a-role]\n"), &o))
	err := o.Writer().Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid role "not-a-role"`)
}

func TestManagedConfig_RejectsUnknownAgentEntryField(t *testing.T) {
	var o ManagedConfig
	err := yaml.Unmarshal([]byte("agents:\n  - name: code\n    efort: high\n"), &o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agents[0]: unknown field "efort"`)
}

func TestManagedConfig_AgentEntryStringShorthandUnaffectedByUnknownFieldCheck(t *testing.T) {
	var o ManagedConfig
	err := yaml.Unmarshal([]byte("agents:\n  - https://example.com/harness/custom.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890\n"), &o)
	require.NoError(t, err)
}

// ValidateManagedLayer validates a single, isolated overlay layer (ADR
// 0122) — before defaults.config/repo config are merged and before the
// repos.yaml allowed_remote_resources shorthand is applied. It must defer
// the agent-allowlist and override-only-built-in-name checks, which need
// information this layer alone doesn't have; ValidateMergedManaged runs
// them once that information is available (except the built-in-name
// check, which needs config.base.yaml and so is never enforced by
// either — see internal/repos/managed_config.go).
func TestValidateManagedLayer_DefersAllowlistAndBuiltinNameChecks(t *testing.T) {
	var o ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - source: "https://example.com/harness/custom.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
  - name: mycustom
    runtime: pi
`), &o))
	w := o.Writer()

	require.NoError(t, ValidateManagedLayer(w),
		"isolated layer must not reject a URL agent for lacking an allowlist, or an override-only entry for not naming a built-in agent")

	// The same agents fail strict, full validation (used for a complete,
	// self-contained config), which requires both.
	assert.Error(t, w.Validate())
}

func TestValidateMergedManaged_EnforcesAllowlistButNotBuiltinName(t *testing.T) {
	var o ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
agents:
  - source: "https://example.com/harness/custom.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
  - name: mycustom
    runtime: pi
`), &o))
	w := o.Writer()

	// Before a layer supplying allowed_remote_resources is merged in, the
	// URL agent still fails.
	err := ValidateMergedManaged(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not covered by allowed_remote_resources")

	// Once that layer is merged (as mergeManagedConfig does), the URL
	// agent passes; the override-only entry naming "mycustom" still isn't
	// required to be a built-in agent — config.base.yaml, which may
	// register it, isn't layered on until install/converge time.
	w.SetAllowedRemoteResources([]string{"https://example.com/"})
	assert.NoError(t, ValidateMergedManaged(w))
}

func TestMergeManaged_AllFieldsAndAgentClone(t *testing.T) {
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

	merged := MergeManaged(parent, child)
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

	// clonePerRepo must not alias nested pointers from the parent.
	parent.Inference.Project = "mutated"
	assert.Equal(t, "child-proj", merged.ConfigInferenceProject())
}

func TestMergeManaged_ModelsOntoEmptyParentAndNilWriter(t *testing.T) {
	child := &perRepoConfig{
		Models: &ModelsConfig{Aliases: map[string]string{"haiku": "child-haiku"}},
		parent: &perRepoDefaults{},
	}
	merged := MergeManaged(NewEmptyPerRepoOverlay(), child)
	require.NotNil(t, merged)
	assert.Equal(t, "child-haiku", merged.ConfigModelAliases()["haiku"])

	assert.Nil(t, ManagedConfig{}.Writer())

	_, err := LayerOnBase(NewEmptyPerRepoOverlay(), []byte(": not yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing base config")
}

func boolPtr(v bool) *bool { return &v }

func TestManagedConfig_MarshalOmitsUnset(t *testing.T) {
	type wrap struct {
		Config ManagedConfig `yaml:"config,omitempty"`
	}
	encoded, err := yaml.Marshal(wrap{})
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(encoded), "config:"), "unset overlay must be omitted")
}

func TestManagedConfig_WithCreateIssuesRepos(t *testing.T) {
	var base ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("keep_history: false\n"), &base))

	withIssues := base.WithCreateIssuesRepos([]string{"fullsend-ai/fullsend"})
	require.True(t, withIssues.IsSet())
	ci := withIssues.Writer().IssueCreationConfig()
	require.NotNil(t, ci)
	assert.Equal(t, []string{"fullsend-ai/fullsend"}, ci.AllowTargets.Repos)
	assert.False(t, withIssues.Writer().ConfigKeepHistory(), "existing values are kept")
	assert.Nil(t, base.Writer().IssueCreationConfig(), "the receiver is not modified")

	// An unset receiver yields a set, otherwise-empty configuration.
	fromZero := ManagedConfig{}.WithCreateIssuesRepos([]string{"fullsend-ai/fullsend"})
	require.True(t, fromZero.IsSet())
	assert.NotNil(t, fromZero.Writer().IssueCreationConfig())
	assert.Equal(t, PerRepoDefaultRoles(), fromZero.Writer().ConfigRoles(), "roles are not declared")
}

func TestManagedConfig_ExplicitAllowedRemoteResources(t *testing.T) {
	var unset ManagedConfig
	_, declared := unset.ExplicitAllowedRemoteResources()
	assert.False(t, declared, "an unset config declares nothing")

	var omitted ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("runtime: pi\n"), &omitted))
	_, declared = omitted.ExplicitAllowedRemoteResources()
	assert.False(t, declared, "an omitted key is not declared and does not surface code defaults")

	var empty ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("allowed_remote_resources: []\n"), &empty))
	list, declared := empty.ExplicitAllowedRemoteResources()
	assert.True(t, declared)
	assert.Empty(t, list)

	var set ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("allowed_remote_resources: [\"https://a.example.com/\"]\n"), &set))
	list, declared = set.ExplicitAllowedRemoteResources()
	assert.True(t, declared)
	assert.Equal(t, []string{"https://a.example.com/"}, list, "local list only, no union with code defaults")
}

func TestManagedConfig_CloneIsDeep(t *testing.T) {
	assert.False(t, ManagedConfig{}.Clone().IsSet(), "an unset config stays unset")

	var orig ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("allowed_remote_resources: [\"https://a.example.com/\"]\n"), &orig))
	cp := orig.Clone()
	require.True(t, cp.IsSet())
	cp.Writer().SetAllowedRemoteResources([]string{"https://b.example.com/"})

	list, _ := orig.ExplicitAllowedRemoteResources()
	assert.Equal(t, []string{"https://a.example.com/"}, list)
}
