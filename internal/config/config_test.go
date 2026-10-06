package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

func TestValidRoles(t *testing.T) {
	roles := ValidRoles()
	assert.Len(t, roles, 8)
	assert.Contains(t, roles, "fullsend")
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "coder")
	assert.Contains(t, roles, "review")
	assert.Contains(t, roles, "fix")
	assert.Contains(t, roles, "retro")
	assert.Contains(t, roles, "prioritize")
	assert.Contains(t, roles, "e2e")
	assert.NotContains(t, roles, "scribe",
		"scribe is mint-only until scaffold/workflow wiring lands; must not pass roles: config validation")
}

func TestValidRoles_RecognizedByMintcore(t *testing.T) {
	for _, role := range ValidRoles() {
		assert.True(t, mintcore.HasRole(role),
			"ValidRoles() contains %q but mintcore.HasRole is false — role lists may have drifted (see issue tracking consolidation)", role)
	}
}

func TestPerRepoConfigValidate_RejectsMintOnlyScribeRole(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"triage", "scribe"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid role "scribe"`)
}

func TestPerRepoDefaultRoles(t *testing.T) {
	roles := PerRepoDefaultRoles()
	assert.Len(t, roles, 6)
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "coder")
	assert.Contains(t, roles, "review")
	assert.Contains(t, roles, "fix")
	assert.Contains(t, roles, "retro")
	assert.Contains(t, roles, "prioritize")
	// "fullsend" dispatch role must be excluded in per-repo mode.
	assert.NotContains(t, roles, "fullsend")
}

func TestValidProviders(t *testing.T) {
	providers := ValidProviders()
	assert.Equal(t, []string{"vertex"}, providers)
}

func TestValidRuntimes(t *testing.T) {
	runtimes := ValidRuntimes()
	assert.Contains(t, runtimes, "claude")
	assert.Contains(t, runtimes, "pi")
	assert.Contains(t, runtimes, "dummy")
	assert.Contains(t, runtimes, "dummy-playback")
	assert.Contains(t, runtimes, "codex")
	assert.Contains(t, runtimes, "opencode", "opencode is user-selectable (unbound-force#510)")
}

func TestNewPerRepoConfig_DefaultRoles(t *testing.T) {
	cfg := NewPerRepoConfig(nil, "")
	assert.Equal(t, "1", cfg.ConfigVersion())
	assert.Equal(t, DefaultAgentRoles(), cfg.(*perRepoConfig).Roles)
	assert.False(t, cfg.IsKillSwitchActive())
}

func TestNewPerRepoConfig_CustomRoles(t *testing.T) {
	cfg := NewPerRepoConfig([]string{"triage", "review"}, "")
	assert.Equal(t, []string{"triage", "review"}, cfg.(*perRepoConfig).Roles)
}

func TestPerRepoConfigValidate_Valid(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "triage", "coder"},
	}
	assert.NoError(t, cfg.Validate())
}

func TestPerRepoConfigValidate_InvalidVersion(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "2",
		Roles:   []string{"fullsend"},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported version")
}

func TestPerRepoConfigValidate_InvalidRole(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "invalid-role"},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestPerRepoConfigValidate_DuplicateRole(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "triage", "fullsend"},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate role")
}

func TestPerRepoConfigValidate_EmptyRoles(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{},
	}
	assert.NoError(t, cfg.Validate())
}

func TestPerRepoConfigValidate_Runtime(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"triage"},
		Runtime: "dummy",
	}
	assert.NoError(t, cfg.Validate())

	cfg.Runtime = "pi"
	assert.NoError(t, cfg.Validate(), "pi is user-selectable (#6464)")

	cfg.Runtime = "codex"
	assert.NoError(t, cfg.Validate(), "codex is user-selectable (#6920)")

	// opencode became user-selectable once the runtime was implemented
	// (unbound-force#510); it is now in ValidRuntimes().
	cfg.Runtime = "opencode"
	assert.NoError(t, cfg.Validate(), "opencode is user-selectable (unbound-force#510)")

	cfg.Runtime = "invalid"
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid runtime")
}

func TestPerRepoConfigValidate_AuthorizationValidProvider(t *testing.T) {
	cfg := &perRepoConfig{
		Version:       "1",
		Authorization: []AuthorizationProvider{{Provider: "owners_file"}},
	}
	assert.NoError(t, cfg.Validate())
}

func TestPerRepoConfigValidate_AuthorizationInvalidProvider(t *testing.T) {
	cfg := &perRepoConfig{
		Version:       "1",
		Authorization: []AuthorizationProvider{{Provider: "ldap"}},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid provider")
}

func TestPerRepoConfigValidate_AuthorizationDuplicateProvider(t *testing.T) {
	cfg := &perRepoConfig{
		Version:       "1",
		Authorization: []AuthorizationProvider{{Provider: "owners_file"}, {Provider: "owners_file"}},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate provider")
}

func TestParsePerRepoConfig(t *testing.T) {
	yamlData := `
version: "1"
kill_switch: true
roles:
  - fullsend
  - triage
  - review
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.Equal(t, "1", cfg.ConfigVersion())
	assert.True(t, cfg.IsKillSwitchActive())
	assert.Equal(t, []string{"fullsend", "triage", "review"}, cfg.ConfigRoles())
}

func TestParsePerRepoConfig_Invalid(t *testing.T) {
	_, err := ParsePerRepoConfig([]byte("not: [valid: yaml"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing per-repo config")
}

func TestPerRepoConfigMarshal(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "triage"},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "fullsend per-repo configuration")
	assert.Contains(t, string(data), "version: \"1\"")
	assert.Contains(t, string(data), "- fullsend")
	assert.Contains(t, string(data), "- triage")
}

func TestPerRepoConfigMarshal_KillSwitchOmitted(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend"},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "kill_switch")
}

func TestPerRepoConfigHeaderPointsToUserDocs(t *testing.T) {
	assert.NotContains(t, perRepoConfigHeader, "ADR",
		"per-repo config header must not reference internal ADRs")
	assert.Contains(t, perRepoConfigHeader, "https://fullsend.sh/",
		"per-repo config header should link to user-facing docs")
}

func TestPerRepoConfig_RoundTrip(t *testing.T) {
	original := NewPerRepoConfig([]string{"fullsend", "triage", "coder", "review", "fix"}, "")
	data, err := original.Marshal()
	require.NoError(t, err)

	headerEnd := strings.Index(string(data), "version:")
	require.True(t, headerEnd > 0)

	parsed, err := ParsePerRepoConfig(data[headerEnd:])
	require.NoError(t, err)
	assert.Equal(t, original.ConfigVersion(), parsed.ConfigVersion())
	assert.Equal(t, original.(*perRepoConfig).Roles, parsed.(*perRepoConfig).Roles)
	assert.Equal(t, original.IsKillSwitchActive(), parsed.IsKillSwitchActive())
}

// --- StatusNotifications tests ---

func TestPerRepoConfigValidate_StatusNotificationValues(t *testing.T) {
	cases := []struct {
		name    string
		sn      StatusNotificationConfig
		wantErr string
	}{
		{name: "comment completion bogus", sn: StatusNotificationConfig{Comment: CommentNotificationConfig{Completion: "bogus"}}, wantErr: "status_notifications.comment.completion"},
		{name: "comment completion on_failure", sn: StatusNotificationConfig{Comment: CommentNotificationConfig{Completion: "on_failure"}}},
		{name: "comment start on_failure", sn: StatusNotificationConfig{Comment: CommentNotificationConfig{Start: "on_failure"}}, wantErr: "status_notifications.comment.start"},
		{name: "reaction valid", sn: StatusNotificationConfig{Reaction: ReactionNotificationConfig{Start: "enabled", Completion: "disabled"}}},
		{name: "reaction start bogus", sn: StatusNotificationConfig{Reaction: ReactionNotificationConfig{Start: "bogus"}}, wantErr: "status_notifications.reaction.start"},
		{name: "reaction completion bogus", sn: StatusNotificationConfig{Reaction: ReactionNotificationConfig{Completion: "bogus"}}, wantErr: "status_notifications.reaction.completion"},
		{name: "reaction completion on_failure", sn: StatusNotificationConfig{Reaction: ReactionNotificationConfig{Completion: "on_failure"}}},
		{name: "reaction start on_failure", sn: StatusNotificationConfig{Reaction: ReactionNotificationConfig{Start: "on_failure"}}, wantErr: "status_notifications.reaction.start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sn := tc.sn
			cfg := &perRepoConfig{Version: "1", Notifications: &sn}
			err := cfg.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestParsePerRepoConfig_WithStatusNotifications(t *testing.T) {
	yamlData := `
version: "1"
roles:
  - triage
status_notifications:
  comment:
    start: enabled
    completion: disabled
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, cfg.StatusNotifications())
	assert.Equal(t, "enabled", cfg.StatusNotifications().Comment.Start)
	assert.Equal(t, "disabled", cfg.StatusNotifications().Comment.Completion)
}

func TestParsePerRepoConfig_WithoutStatusNotifications(t *testing.T) {
	yamlData := `
version: "1"
roles:
  - triage
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	assert.Nil(t, cfg.StatusNotifications())
}

func TestPerRepoConfig_StatusNotifications_FallsThroughToParent(t *testing.T) {
	base, err := ParsePerRepoConfig([]byte(`
version: "1"
status_notifications:
  comment:
    start: enabled
`))
	require.NoError(t, err)

	overlay := &perRepoConfig{parent: base}
	require.NotNil(t, overlay.StatusNotifications())
	assert.Equal(t, "enabled", overlay.StatusNotifications().Comment.Start)
}

func TestPerRepoConfigValidate_ValidStatusNotifications(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Notifications: &StatusNotificationConfig{
			Comment: CommentNotificationConfig{Start: "enabled", Completion: "disabled"},
		},
	}
	assert.NoError(t, cfg.Validate())
}

func TestPerRepoConfigValidate_InvalidCommentStart(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Notifications: &StatusNotificationConfig{
			Comment: CommentNotificationConfig{Start: "bogus"},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status_notifications.comment.start")
}

func TestPerRepoConfigMarshal_WithStatusNotifications(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Notifications: &StatusNotificationConfig{
			Comment: CommentNotificationConfig{Start: "enabled"},
		},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "status_notifications:")
	assert.Contains(t, string(data), "start: enabled")
}

func TestPerRepoConfigMarshal_WithoutStatusNotifications(t *testing.T) {
	cfg := &perRepoConfig{Version: "1"}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "status_notifications")
}

// --- CreateIssues tests ---

func TestPerRepoConfigValidate_CreateIssues(t *testing.T) {
	cases := []struct {
		name    string
		ci      *CreateIssuesConfig
		wantErr string
	}{
		{name: "nil", ci: nil},
		{name: "valid", ci: &CreateIssuesConfig{AllowTargets: AllowTargets{Orgs: []string{"my-org"}, Repos: []string{"other/repo"}}}},
		{name: "repo without slash", ci: &CreateIssuesConfig{AllowTargets: AllowTargets{Repos: []string{"no-slash-here"}}}, wantErr: "no-slash-here"},
		{name: "empty org", ci: &CreateIssuesConfig{AllowTargets: AllowTargets{Orgs: []string{"valid-org", ""}}}, wantErr: "empty org"},
	}
	for _, repo := range []string{"/", "/repo", "owner/", "//"} {
		cases = append(cases, struct {
			name    string
			ci      *CreateIssuesConfig
			wantErr string
		}{name: "malformed " + repo, ci: &CreateIssuesConfig{AllowTargets: AllowTargets{Repos: []string{repo}}}, wantErr: "owner/name"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &perRepoConfig{Version: "1", CreateIssues: tc.ci}
			err := cfg.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestPerRepoConfig_CreateIssues_ParseYAML(t *testing.T) {
	yamlData := `
version: "1"
roles:
  - fullsend
  - triage
create_issues:
  allow_targets:
    repos:
      - my-org/my-repo
      - fullsend-ai/fullsend
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, cfg.IssueCreationConfig())
	assert.Equal(t, []string{"my-org/my-repo", "fullsend-ai/fullsend"}, cfg.IssueCreationConfig().AllowTargets.Repos)
}

func TestNewPerRepoConfig_CreateIssuesDefaults(t *testing.T) {
	cfg := NewPerRepoConfig(nil, "my-org/my-repo")
	require.NotNil(t, cfg.IssueCreationConfig())
	assert.Equal(t, []string{"my-org/my-repo", "fullsend-ai/fullsend"}, cfg.IssueCreationConfig().AllowTargets.Repos)
}

// --- AgentEntry tests ---

func TestAgentEntry_UnmarshalYAML_StringShorthand(t *testing.T) {
	yamlData := `
agents:
  - https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890
`
	var out struct {
		Agents []AgentEntry `yaml:"agents"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(yamlData), &out))
	require.Len(t, out.Agents, 1)
	assert.Empty(t, out.Agents[0].Name)
	assert.Contains(t, out.Agents[0].Source, "triage.yaml")
}

func TestAgentEntry_UnmarshalYAML_ObjectForm(t *testing.T) {
	yamlData := `
agents:
  - name: lint
    source: harness/my-linter.yaml
`
	var out struct {
		Agents []AgentEntry `yaml:"agents"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(yamlData), &out))
	require.Len(t, out.Agents, 1)
	assert.Equal(t, "lint", out.Agents[0].Name)
	assert.Equal(t, "harness/my-linter.yaml", out.Agents[0].Source)
}

func TestAgentEntry_UnmarshalYAML_MixedForms(t *testing.T) {
	yamlData := `
agents:
  - https://example.com/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890
  - name: lint
    source: harness/my-linter.yaml
`
	var out struct {
		Agents []AgentEntry `yaml:"agents"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(yamlData), &out))
	require.Len(t, out.Agents, 2)
	assert.Empty(t, out.Agents[0].Name)
	assert.Equal(t, "lint", out.Agents[1].Name)
}

func TestAgentEntry_UnmarshalYAML_InvalidNodeType(t *testing.T) {
	yamlData := `
agents:
  - [not, a, string, or, mapping]
`
	var out struct {
		Agents []AgentEntry `yaml:"agents"`
	}
	err := yaml.Unmarshal([]byte(yamlData), &out)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be a string or mapping")
}

func TestAgentEntry_DerivedName_ExplicitName(t *testing.T) {
	e := AgentEntry{Name: "custom", Source: "harness/triage.yaml"}
	assert.Equal(t, "custom", e.DerivedName())
}

func TestAgentEntry_DerivedName_DerivedFromFilename(t *testing.T) {
	e := AgentEntry{Source: "harness/triage.yaml"}
	assert.Equal(t, "triage", e.DerivedName())
}

func TestAgentEntry_DerivedName_DerivedFromURL(t *testing.T) {
	e := AgentEntry{Source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}
	assert.Equal(t, "triage", e.DerivedName())
}

func TestAgentEntry_DerivedName_DerivedFromLocalPath(t *testing.T) {
	e := AgentEntry{Source: "my-linter.yaml"}
	assert.Equal(t, "my-linter", e.DerivedName())
}

func TestAgentEntry_MarshalRoundTrip(t *testing.T) {
	original := []AgentEntry{
		{Source: "https://example.com/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
		{Name: "lint", Source: "harness/my-linter.yaml"},
	}
	data, err := yaml.Marshal(struct {
		Agents []AgentEntry `yaml:"agents"`
	}{Agents: original})
	require.NoError(t, err)

	var parsed struct {
		Agents []AgentEntry `yaml:"agents"`
	}
	require.NoError(t, yaml.Unmarshal(data, &parsed))
	require.Len(t, parsed.Agents, 2)
	assert.Equal(t, original[0].Source, parsed.Agents[0].Source)
	assert.Equal(t, original[1].Name, parsed.Agents[1].Name)
	assert.Equal(t, original[1].Source, parsed.Agents[1].Source)
}

// --- Agent entry validation tests ---

// agentEntriesValidator runs ValidateAgentEntries through a Validate()
// method so each test reads as "build cfg, validate".
type agentEntriesValidator struct {
	agents    []AgentEntry
	allowlist []string
}

func (v agentEntriesValidator) Validate() error {
	return ValidateAgentEntries(v.agents, v.allowlist)
}

func TestValidateAgentEntries_Valid(t *testing.T) {
	allowlist := []string{"https://raw.githubusercontent.com/fullsend-ai/agents/"}
	agents := []AgentEntry{
		{Source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
		{Name: "lint", Source: "harness/my-linter.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents, allowlist: allowlist}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_DuplicateName(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/triage.yaml"},
		{Source: "other/triage.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestValidateAgentEntries_DuplicateNameCaseInsensitive(t *testing.T) {
	agents := []AgentEntry{
		{Name: "Triage", Source: "harness/a.yaml"},
		{Name: "triage", Source: "harness/b.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestValidateAgentEntries_MissingHash(t *testing.T) {
	allowlist := []string{"https://raw.githubusercontent.com/fullsend-ai/agents/"}
	agents := []AgentEntry{
		{Source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents, allowlist: allowlist}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "#sha256=")
}

func TestValidateAgentEntries_NonHTTPS(t *testing.T) {
	agents := []AgentEntry{
		{Source: "http://example.com/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "https")
}

func TestValidateAgentEntries_URLNotInAllowlist(t *testing.T) {
	allowlist := []string{"https://raw.githubusercontent.com/fullsend-ai/fullsend/"}
	agents := []AgentEntry{
		{Source: "https://raw.githubusercontent.com/other-org/repo/abc123/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
	}
	cfg := agentEntriesValidator{agents: agents, allowlist: allowlist}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not covered by allowed_remote_resources")
}

func TestValidateAgentEntries_PathTraversal(t *testing.T) {
	agents := []AgentEntry{
		{Source: "../../../etc/passwd"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

func TestValidateAgentEntries_EmptySource(t *testing.T) {
	agents := []AgentEntry{
		{Name: "empty"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "enabled agent entry must have a source")
}

func TestValidateAgentEntries_LocalPathAcceptedWithoutHash(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/my-agent.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_InvalidHashLength(t *testing.T) {
	allowlist := []string{"https://raw.githubusercontent.com/fullsend-ai/agents/"}
	agents := []AgentEntry{
		{Source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc/harness/triage.yaml#sha256=tooshort"},
	}
	cfg := agentEntriesValidator{agents: agents, allowlist: allowlist}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "integrity fragment")
}

func TestValidateAgentEntries_InvalidHashChars(t *testing.T) {
	allowlist := []string{"https://raw.githubusercontent.com/fullsend-ai/agents/"}
	agents := []AgentEntry{
		{Source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc/harness/triage.yaml#sha256=zzzzzz1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
	}
	cfg := agentEntriesValidator{agents: agents, allowlist: allowlist}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "integrity fragment")
}

func TestValidateAgentEntries_EmptyDerivedName(t *testing.T) {
	agents := []AgentEntry{
		{Source: ".yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is invalid")
}

func TestValidateAgentEntries_MixedCaseHTTP_Rejected(t *testing.T) {
	agents := []AgentEntry{
		{Source: "HTTP://example.com/harness/triage.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "https")
}

func TestValidateAgentEntries_UnsupportedScheme_Rejected(t *testing.T) {
	agents := []AgentEntry{
		{Source: "ftp://example.com/harness/triage.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestValidateAgentEntries_BackslashPath_Rejected(t *testing.T) {
	agents := []AgentEntry{
		{Name: "triage", Source: "harness\\triage.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "backslash")
}

func TestValidateAgentEntries_AbsolutePath_Rejected(t *testing.T) {
	agents := []AgentEntry{
		{Source: "/etc/agents/triage.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "absolute paths")
}

func TestValidateAgentEntries_DegenerateName_Rejected(t *testing.T) {
	agents := []AgentEntry{
		{Source: "#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is invalid")
}

func TestValidateAgentEntries_SuppressionOnlyEntry_Valid(t *testing.T) {
	f := false
	agents := []AgentEntry{
		{Name: "retro", Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_SuppressionWithoutName_Invalid(t *testing.T) {
	f := false
	agents := []AgentEntry{
		{Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "disabled agent entry with no source must have an explicit name")
}

func TestValidateAgentEntries_SuppressionInvalidName_Rejected(t *testing.T) {
	f := false
	agents := []AgentEntry{
		{Name: "-bad", Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name is invalid")
}

func TestValidateAgentEntries_DuplicateSuppression_Rejected(t *testing.T) {
	f := false
	agents := []AgentEntry{
		{Name: "retro", Enabled: &f},
		{Name: "retro", Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestValidateAgentEntries_DisableThenEnable_Accepted(t *testing.T) {
	f := false
	tr := true
	agents := []AgentEntry{
		{Name: "retro", Enabled: &f},
		{Name: "retro", Source: "harness/retro-custom.yaml", Enabled: &tr},
	}
	cfg := agentEntriesValidator{agents: agents}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_EnableThenDisable_Accepted(t *testing.T) {
	f := false
	tr := true
	agents := []AgentEntry{
		{Name: "retro", Source: "harness/retro-custom.yaml", Enabled: &tr},
		{Name: "retro", Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_DisabledWithSourceNoName_Rejected(t *testing.T) {
	f := false
	agents := []AgentEntry{
		{Source: "harness/retro.yaml", Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "disabled agent entry must have an explicit name")
}

func TestValidateAgentEntries_DisabledWithSourceAndName_Valid(t *testing.T) {
	f := false
	agents := []AgentEntry{
		{Name: "retro", Source: "harness/retro.yaml", Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_EnabledWithSource_Valid(t *testing.T) {
	tr := true
	agents := []AgentEntry{
		{Source: "harness/my-agent.yaml", Enabled: &tr},
	}
	cfg := agentEntriesValidator{agents: agents}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_EnabledOmittedWithSource_Valid(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/my-agent.yaml"},
	}
	cfg := agentEntriesValidator{agents: agents}
	assert.NoError(t, cfg.Validate())
}

func TestValidateAgentEntries_ThreeEntryChain_Rejected(t *testing.T) {
	f := false
	tr := true
	agents := []AgentEntry{
		{Name: "retro", Source: "harness/retro-v1.yaml", Enabled: &tr},
		{Name: "retro", Enabled: &f},
		{Name: "retro", Source: "harness/retro-v2.yaml", Enabled: &tr},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestValidateAgentEntries_ThreeEntryDisableChain_Rejected(t *testing.T) {
	f := false
	tr := true
	agents := []AgentEntry{
		{Name: "retro", Enabled: &f},
		{Name: "retro", Source: "harness/retro-custom.yaml", Enabled: &tr},
		{Name: "retro", Enabled: &f},
	}
	cfg := agentEntriesValidator{agents: agents}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestAgentEntry_IsEnabled(t *testing.T) {
	t.Run("nil defaults to true", func(t *testing.T) {
		e := AgentEntry{Source: "harness/test.yaml"}
		assert.True(t, e.IsEnabled())
	})
	t.Run("explicit true", func(t *testing.T) {
		tr := true
		e := AgentEntry{Source: "harness/test.yaml", Enabled: &tr}
		assert.True(t, e.IsEnabled())
	})
	t.Run("explicit false", func(t *testing.T) {
		f := false
		e := AgentEntry{Source: "harness/test.yaml", Enabled: &f}
		assert.False(t, e.IsEnabled())
	})
}

func TestPerRepoConfig_ParseYAML_WithDisabledAgent(t *testing.T) {
	yamlData := `
version: "1"
roles:
  - fullsend
agents:
  - name: retro
    enabled: false
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "retro", cfg.AgentEntries()[0].Name)
	assert.False(t, *cfg.AgentEntries()[0].Enabled)
	assert.NoError(t, cfg.(*perRepoConfig).Validate())
}

// --- PerRepoConfig agents and allowlist tests ---

func TestPerRepoConfig_ParseYAML_WithAgentsAndAllowlist(t *testing.T) {
	yamlData := `
version: "1"
roles:
  - fullsend
  - triage
agents:
  - https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890
  - name: lint
    source: harness/lint.yaml
allowed_remote_resources:
  - https://raw.githubusercontent.com/fullsend-ai/agents/
`
	cfg, err := ParsePerRepoConfig([]byte(yamlData))
	require.NoError(t, err)
	require.Len(t, cfg.AgentEntries(), 2)
	assert.Contains(t, cfg.AgentEntries()[0].Source, "triage.yaml")
	assert.Equal(t, "lint", cfg.AgentEntries()[1].Name)
	// AllowedResources now unions with parent defaults (code defaults).
	resources := cfg.AllowedResources()
	assert.Contains(t, resources, "https://raw.githubusercontent.com/fullsend-ai/agents/")
	for _, d := range DefaultAllowedRemoteResources() {
		assert.Contains(t, resources, d)
	}
}

func TestPerRepoConfig_Validate_WithAgents(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend"},
		Agents: []AgentEntry{
			{Source: "harness/my-agent.yaml"},
		},
	}
	assert.NoError(t, cfg.Validate())
}

func TestPerRepoConfig_Validate_AgentDuplicate(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend"},
		Agents: []AgentEntry{
			{Source: "harness/triage.yaml"},
			{Source: "other/triage.yaml"},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestNewPerRepoConfig_AllowedRemoteResources(t *testing.T) {
	cfg := NewPerRepoConfig(nil, "")
	assert.Equal(t, DefaultAllowedRemoteResources(), cfg.AllowedResources())
}

func TestPerRepoConfig_Marshal_WithAgents(t *testing.T) {
	cfg := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend"},
		Agents: []AgentEntry{
			{Source: "harness/my-agent.yaml"},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "agents:")
	assert.Contains(t, string(data), "my-agent.yaml")
	assert.Contains(t, string(data), "allowed_remote_resources:")
}

// --- DefaultAllowedRemoteResources tests ---

func TestDefaultAllowedRemoteResources(t *testing.T) {
	resources := DefaultAllowedRemoteResources()
	assert.Len(t, resources, 2)
	assert.Contains(t, resources, "https://raw.githubusercontent.com/fullsend-ai/fullsend/")
	assert.Contains(t, resources, "https://raw.githubusercontent.com/fullsend-ai/agents/")
}

func TestPerRepoConfig_RoundTrip_WithAgents(t *testing.T) {
	original := &perRepoConfig{
		Version: "1",
		Roles:   []string{"fullsend", "triage"},
		Agents: []AgentEntry{
			{Source: "https://example.com/harness/triage.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
			{Name: "lint", Source: "harness/lint.yaml"},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	data, err := original.Marshal()
	require.NoError(t, err)

	headerEnd := strings.Index(string(data), "version:")
	require.True(t, headerEnd > 0)

	parsed, err := ParsePerRepoConfig(data[headerEnd:])
	require.NoError(t, err)
	require.Len(t, parsed.AgentEntries(), 2)
	assert.Equal(t, original.Agents[0].Source, parsed.AgentEntries()[0].Source)
	assert.Equal(t, original.Agents[1].Name, parsed.AgentEntries()[1].Name)
	// Parsed config has a parent so AllowedResources unions with
	// code defaults. Verify local resource is present.
	resources := parsed.AllowedResources()
	assert.Contains(t, resources, "https://example.com/")
	// Verify the raw struct field was preserved.
	assert.Equal(t, original.AllowedRemoteResources, parsed.(*perRepoConfig).AllowedRemoteResources)
}

func TestEnsureDefaultAllowedRemoteResources(t *testing.T) {
	defaults := DefaultAllowedRemoteResources()

	t.Run("nil input returns defaults", func(t *testing.T) {
		result := EnsureDefaultAllowedRemoteResources(nil)
		assert.Equal(t, defaults, result)
	})

	t.Run("explicit empty preserves deny-all", func(t *testing.T) {
		result := EnsureDefaultAllowedRemoteResources([]string{})
		assert.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("custom entries preserved with defaults appended", func(t *testing.T) {
		custom := []string{"https://example.com/foo/"}
		result := EnsureDefaultAllowedRemoteResources(custom)
		expected := []string{
			"https://example.com/foo/",
			"https://raw.githubusercontent.com/fullsend-ai/fullsend/",
			"https://raw.githubusercontent.com/fullsend-ai/agents/",
		}
		assert.Equal(t, expected, result)
	})

	t.Run("already has defaults produces no duplicates", func(t *testing.T) {
		result := EnsureDefaultAllowedRemoteResources(defaults)
		assert.Equal(t, defaults, result)
	})

	t.Run("partial overlap adds only missing default", func(t *testing.T) {
		partial := []string{defaults[0], "https://example.com/bar/"}
		result := EnsureDefaultAllowedRemoteResources(partial)
		expected := []string{defaults[0], "https://example.com/bar/", defaults[1]}
		assert.Equal(t, expected, result)
	})

	t.Run("idempotent", func(t *testing.T) {
		first := EnsureDefaultAllowedRemoteResources([]string{"https://example.com/"})
		second := EnsureDefaultAllowedRemoteResources(first)
		assert.Equal(t, first, second)
	})

	t.Run("does not mutate input", func(t *testing.T) {
		input := []string{"https://example.com/"}
		inputCopy := make([]string, len(input))
		copy(inputCopy, input)
		_ = EnsureDefaultAllowedRemoteResources(input)
		assert.Equal(t, inputCopy, input)
	})
}

func TestValidModelRef(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"opus", true},
		{"sonnet", true},
		{"claude-opus-4-6", true},
		{"claude-sonnet-4-6@20250514", true},
		{"google-vertex/gemini-3.8-flash", true},
		{"xai-vertex/xai/grok-4.6", true},
		{"anthropic-vertex/claude-opus-4-6", true},
		{"", false},
		{"/leading", false},
		{"trailing/", false},
		{"a//b", false},
		{"has space", false},
		{"has$special", false},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidModelRef(tc.ref), "ValidModelRef(%q)", tc.ref)
		})
	}
}

func TestValidAgentNames(t *testing.T) {
	names := ValidAgentNames()
	assert.Contains(t, names, "triage")
	assert.Contains(t, names, "code")
	assert.Contains(t, names, "review")
	assert.Contains(t, names, "fix")
	assert.Contains(t, names, "retro")
	assert.Contains(t, names, "prioritize")
	// "coder" is NOT a valid agent name (it's a role name); the
	// validation should hint "did you mean code" for it.
	assert.NotContains(t, names, "coder")
}

func TestValidEffort(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, ValidEffortLevels())
	for _, level := range ValidEffortLevels() {
		assert.True(t, ValidEffort(level), level)
	}
	assert.False(t, ValidEffort(""))
	assert.False(t, ValidEffort("turbo"))
	assert.False(t, ValidEffort("High"))
}

// --- per-agent settings on agents: entries (ADR 0091) ---

func parseAgentSettingsConfig(t *testing.T, doc string) PerRepoConfigReader {
	t.Helper()
	cfg, err := ParsePerRepoConfig([]byte("# fullsend per-repo configuration\nversion: \"1\"\n" + doc))
	require.NoError(t, err)
	return cfg.(PerRepoConfigReader)
}

func TestAgentSettings_ParseAndValidate(t *testing.T) {
	t.Parallel()
	cfg := parseAgentSettingsConfig(t, `runtime: pi
agents:
  - name: triage
    model: xai-vertex/xai/grok-4.6
  - name: code
    runtime: claude
    model: sonnet
    effort: high
  - source: harness/lint.yaml
    model: haiku
`)
	require.NoError(t, cfg.(ConfigWriter).Validate())
	assert.Equal(t, "pi", cfg.ConfigRuntime())

	triage, ok := AgentSettingsFor(cfg.AgentEntries(), "triage")
	require.True(t, ok)
	assert.Equal(t, "xai-vertex/xai/grok-4.6", triage.Model)
	assert.Empty(t, triage.Runtime, "repo-wide runtime applies")
	assert.True(t, triage.IsOverrideOnly())

	code, ok := AgentSettingsFor(cfg.AgentEntries(), "Code")
	require.True(t, ok, "lookup is case-insensitive")
	assert.Equal(t, AgentEntry{Name: "code", Runtime: "claude", Model: "sonnet", Effort: "high"}, code)

	lint, ok := AgentSettingsFor(cfg.AgentEntries(), "lint")
	require.True(t, ok)
	assert.Equal(t, "harness/lint.yaml", lint.Source, "a sourced custom agent carries settings too")
	assert.Equal(t, "haiku", lint.Model)
	assert.False(t, lint.IsOverrideOnly())

	_, ok = AgentSettingsFor(cfg.AgentEntries(), "review")
	assert.False(t, ok)
}

func TestAgentSettings_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, doc, want string
	}{
		{"unknown built-in with hint", "agents:\n  - name: coder\n    model: sonnet\n", `did you mean "code"`},
		{"unknown custom without source", "agents:\n  - name: lint\n    model: sonnet\n", "give a custom agent its source"},
		{"name-only entry without settings", "agents:\n  - name: triage\n", "must have a source"},
		{"settings without a name", "agents:\n  - model: sonnet\n", "must name the agent"},
		{"invalid model", "agents:\n  - name: triage\n    model: bad//id\n", `invalid model "bad//id"`},
		{"leading slash model", "agents:\n  - name: triage\n    model: /leading\n", "invalid model"},
		{"invalid runtime", "agents:\n  - name: triage\n    runtime: nonexistent\n", `invalid runtime "nonexistent"`},
		{"invalid effort", "agents:\n  - name: triage\n    effort: turbo\n", `invalid effort "turbo"`},
		{"invalid effort on sourced entry", "agents:\n  - source: harness/lint.yaml\n    effort: turbo\n", `invalid effort "turbo"`},
		{"duplicate built-in tuning", "agents:\n  - name: triage\n    model: sonnet\n  - name: Triage\n    model: haiku\n", "duplicate agent name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := parseAgentSettingsConfig(t, tc.doc)
			err := cfg.(ConfigWriter).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	for _, model := range []string{"opus", "claude-haiku-4-5@20251001", "google-vertex/gemini-3.8-flash", "xai-vertex/xai/grok-4.6"} {
		cfg := parseAgentSettingsConfig(t, "agents:\n  - name: triage\n    model: "+model+"\n")
		assert.NoError(t, cfg.(ConfigWriter).Validate(), model)
	}
}

func TestAgentSettings_MarshalRoundTrip(t *testing.T) {
	t.Parallel()
	cfg := NewPerRepoConfig([]string{"triage"}, "")
	cfg.SetAgents(UpsertAgentSettings(nil, "code", "claude", "sonnet", "high", nil))
	cfg.SetAgents(UpsertAgentSettings(cfg.AgentEntries(), "triage", "", "xai-vertex/xai/grok-4.6", "", nil))
	require.NoError(t, cfg.Validate())
	data, err := cfg.Marshal()
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, "name: code")
	assert.Contains(t, s, "runtime: claude")
	assert.NotContains(t, s, "source: \"\"", "override-only entries carry no source key")

	back, err := ParsePerRepoConfig(data)
	require.NoError(t, err)
	code, ok := AgentSettingsFor(back.AgentEntries(), "code")
	require.True(t, ok)
	assert.Equal(t, AgentEntry{Name: "code", Runtime: "claude", Model: "sonnet", Effort: "high"}, code)

	// Upsert replaces settings on the existing entry; empty clears.
	cfg.SetAgents(UpsertAgentSettings(cfg.AgentEntries(), "CODE", "", "haiku", "", nil))
	code, _ = AgentSettingsFor(cfg.AgentEntries(), "code")
	assert.Equal(t, AgentEntry{Name: "code", Model: "haiku"}, code)
	assert.Len(t, cfg.AgentEntries(), 2)
}

func TestAgentSettings_LayeredMerge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
runtime: pi
agents:
  - source: harness/lint.yaml
    model: opus
    effort: high
  - name: triage
    model: opus
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: lint
    effort: medium
  - name: Triage
    runtime: claude
  - name: code
    model: sonnet
`), 0o644))
	cfg, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)
	// An overlay entry that only tunes a base-registered custom agent is
	// valid: the merged entry carries the base's source.
	require.NoError(t, cfg.Validate())
	agents := cfg.AgentEntries()

	lint, ok := AgentSettingsFor(agents, "lint")
	require.True(t, ok)
	assert.Equal(t, "harness/lint.yaml", lint.Source)
	assert.Equal(t, "opus", lint.Model, "base model inherited (empty overlay value does not unset)")
	assert.Equal(t, "medium", lint.Effort, "overlay wins per field")

	triage, ok := AgentSettingsFor(agents, "triage")
	require.True(t, ok)
	assert.Equal(t, "claude", triage.Runtime)
	assert.Equal(t, "opus", triage.Model)

	code, ok := AgentSettingsFor(agents, "code")
	require.True(t, ok)
	assert.Equal(t, "sonnet", code.Model)

	// A bad entry in the base layer is caught by Validate on the overlay.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("# fullsend per-repo configuration\nversion: \"1\"\nagents:\n  - name: coder\n    model: sonnet\n"), 0o644))
	cfg, err = LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `did you mean "code"`)
}

func TestAgentSettings_DisabledEntryStillValid(t *testing.T) {
	t.Parallel()
	cfg := parseAgentSettingsConfig(t, "agents:\n  - name: retro\n    enabled: false\n")
	require.NoError(t, cfg.(ConfigWriter).Validate())
	assert.True(t, IsAgentExplicitlyDisabled(cfg.AgentEntries(), "retro"))
}

// --- models.aliases tests (#6882) ---

func TestModelsAliases_PerKeyMerge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// base sets sonnet, overlay sets fable — both effective in the merged config.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(`version: "1"
models:
  aliases:
    sonnet: claude-sonnet-5
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
models:
  aliases:
    fable: claude-fable-5-1
`), 0o644))
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	pr := cfg.(PerRepoConfigReader)
	aliases := pr.ConfigModelAliases()
	assert.Equal(t, "claude-sonnet-5", aliases["sonnet"], "base layer's alias")
	assert.Equal(t, "claude-fable-5-1", aliases["fable"], "overlay layer's alias")
}

func TestModelsAliases_OverlayOverridesBase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(`version: "1"
models:
  aliases:
    sonnet: claude-sonnet-4-6
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
models:
  aliases:
    sonnet: claude-sonnet-5
`), 0o644))
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	pr := cfg.(PerRepoConfigReader)
	aliases := pr.ConfigModelAliases()
	assert.Equal(t, "claude-sonnet-5", aliases["sonnet"], "overlay wins over base")
}

func TestModelsAliases_UnknownKeyRejected(t *testing.T) {
	t.Parallel()
	cfg := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{"grok": "grok-4.6"},
		},
		parent: &perRepoDefaults{},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown alias key")
	assert.Contains(t, err.Error(), "grok")
}

func TestModelsAliases_InvalidModelRefRejected(t *testing.T) {
	t.Parallel()
	cfg := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{"sonnet": "bad//id"},
		},
		parent: &perRepoDefaults{},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid model reference")
	assert.Contains(t, err.Error(), "bad//id")
}

func TestModelsAliases_ValidConfigPasses(t *testing.T) {
	t.Parallel()
	cfg := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{
				"sonnet": "claude-sonnet-5",
				"fable":  "claude-fable-5-1",
			},
		},
		parent: &perRepoDefaults{},
	}
	require.NoError(t, cfg.Validate())
}

func TestModelsAliases_ProviderIDAccepted(t *testing.T) {
	t.Parallel()
	cfg := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{
				"sonnet": "anthropic-vertex/claude-sonnet-5",
			},
		},
		parent: &perRepoDefaults{},
	}
	require.NoError(t, cfg.Validate())
}

func TestModelsAliases_AliasNameAsValueRejected(t *testing.T) {
	t.Parallel()
	// Aliases resolve once: `sonnet: opus` would reach the provider as the
	// literal id "opus", so the value must be a model id, not another alias.
	cfg := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{"sonnet": "opus"},
		},
		parent: &perRepoDefaults{},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is the alias name")
	assert.Contains(t, err.Error(), "models.aliases.sonnet")

	// The check is case-insensitive: "Opus" passes ValidModelRef and would
	// otherwise be sent to the provider as a literal id.
	cfg.Models.Aliases["sonnet"] = "Opus"
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is the alias name")

	// …and it looks at the id segment of a provider/id spec: pi passes a
	// "/" value straight through, so "anthropic-vertex/opus" would send the
	// wire id "opus".
	cfg.Models.Aliases["sonnet"] = "anthropic-vertex/opus"
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is the alias name")

	// A real id whose segment merely contains an alias name is fine.
	cfg.Models.Aliases["sonnet"] = "anthropic-vertex/claude-opus-4-6"
	require.NoError(t, cfg.Validate())
}

func TestModelsAliases_ValidateSeesBaseLayer(t *testing.T) {
	t.Parallel()
	// Validate checks the merged map, so a bad key in config.base.yaml is
	// caught even when the overlay omits models: entirely.
	base := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{"grok": "grok-4.6"},
		},
		parent: &perRepoDefaults{},
	}
	overlay := &perRepoConfig{Version: "1", parent: base}
	err := overlay.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown alias key")
	assert.Contains(t, err.Error(), "grok")
}

func TestValidateModelAliases_NilIsValid(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateModelAliases(nil))
	require.NoError(t, ValidateModelAliases(map[string]string{}))
}

func TestModelsAliases_NilReturnsParent(t *testing.T) {
	t.Parallel()
	parent := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{"sonnet": "claude-sonnet-5"},
		},
		parent: &perRepoDefaults{},
	}
	child := &perRepoConfig{
		Version: "1",
		parent:  parent,
	}
	aliases := child.ConfigModelAliases()
	assert.Equal(t, "claude-sonnet-5", aliases["sonnet"], "parent's alias inherited")
}

func TestModelsAliases_EmptyMapReturnsParent(t *testing.T) {
	t.Parallel()
	parent := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{"sonnet": "claude-sonnet-5"},
		},
		parent: &perRepoDefaults{},
	}
	child := &perRepoConfig{
		Version: "1",
		Models:  &ModelsConfig{},
		parent:  parent,
	}
	aliases := child.ConfigModelAliases()
	assert.Equal(t, "claude-sonnet-5", aliases["sonnet"], "parent's alias inherited with empty overlay")
}

func TestModelsAliases_DefaultsReturnNil(t *testing.T) {
	t.Parallel()
	d := &perRepoDefaults{}
	assert.Nil(t, d.ConfigModelAliases())
}

func TestModelsAliases_MarshalRoundtrip(t *testing.T) {
	t.Parallel()
	cfg := &perRepoConfig{
		Version: "1",
		Models: &ModelsConfig{
			Aliases: map[string]string{"sonnet": "claude-sonnet-5"},
		},
		parent: &perRepoDefaults{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "models:")
	assert.Contains(t, string(data), "aliases:")
	assert.Contains(t, string(data), "sonnet: claude-sonnet-5")

	parsed, parseErr := ParsePerRepoConfig(data)
	require.NoError(t, parseErr)
	assert.Equal(t, "claude-sonnet-5", parsed.ConfigModelAliases()["sonnet"])
}

func TestModelsAliases_OmittedWhenEmpty(t *testing.T) {
	t.Parallel()
	cfg := &perRepoConfig{
		Version: "1",
		parent:  &perRepoDefaults{},
	}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "models:")
}

func TestModelsAliases_SetterAndGetter(t *testing.T) {
	t.Parallel()
	cfg := NewPerRepoConfig(nil, "")
	pw := cfg.(PerRepoConfigWriter)
	pw.SetModelAliases(map[string]string{"opus": "claude-opus-5"})
	pr := cfg.(PerRepoConfigReader)
	assert.Equal(t, "claude-opus-5", pr.ConfigModelAliases()["opus"])

	// Clear with nil.
	pw.SetModelAliases(nil)
	assert.Nil(t, pr.ConfigModelAliases())
}

// --- Subagent config tests (#7031) ---

func TestValidSubagentKey(t *testing.T) {
	t.Parallel()
	valid := []string{"default", "correctness", "security", "style-conventions", "a1-b2-c3"}
	for _, k := range valid {
		assert.True(t, ValidSubagentKey(k), "expected valid: %s", k)
	}
	invalid := []string{"", "A", "Correctness", "has_underscore", "-leading", "trailing-", "a--b", strings.Repeat("a", 65)}
	for _, k := range invalid {
		assert.False(t, ValidSubagentKey(k), "expected invalid: %q", k)
	}
}

func TestValidateAgentSettings_SubagentKeys(t *testing.T) {
	t.Parallel()
	// Valid subagent entries.
	good := AgentEntry{
		Name: "review",
		Subagents: map[string]*string{
			"default":     strPtr("haiku"),
			"correctness": strPtr("opus"),
		},
	}
	assert.NoError(t, validateAgentSettings(0, good))

	// Invalid key.
	bad := AgentEntry{
		Name: "review",
		Subagents: map[string]*string{
			"Bad-Key": strPtr("opus"),
		},
	}
	err := validateAgentSettings(0, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Bad-Key")

	// Invalid value (not a valid model ref).
	badVal := AgentEntry{
		Name: "review",
		Subagents: map[string]*string{
			"correctness": strPtr("not a valid ref!"),
		},
	}
	err = validateAgentSettings(0, badVal)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "correctness")

	// Nil value (tombstone) is valid.
	tombstone := AgentEntry{
		Name: "review",
		Subagents: map[string]*string{
			"correctness": nil,
		},
	}
	assert.NoError(t, validateAgentSettings(0, tombstone))
}

func TestUpsertAgentSettings_Subagents(t *testing.T) {
	t.Parallel()
	subs := map[string]*string{
		"default":     strPtr("haiku"),
		"correctness": strPtr("opus"),
	}
	entries := UpsertAgentSettings(nil, "review", "", "", "", subs)
	require.Len(t, entries, 1)
	assert.Equal(t, subs, entries[0].Subagents)
}

func TestHasSettings_IncludesSubagents(t *testing.T) {
	t.Parallel()
	empty := AgentEntry{Name: "review"}
	assert.False(t, empty.HasSettings())

	withSubs := AgentEntry{
		Name: "review",
		Subagents: map[string]*string{
			"default": strPtr("haiku"),
		},
	}
	assert.True(t, withSubs.HasSettings())
}

func TestSubagentsMerge_PerKeyOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: review
    subagents:
      default: haiku
      correctness: opus
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: review
    subagents:
      correctness: sonnet
`), 0o644))
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	entries := cfg.AgentEntries()
	require.Len(t, entries, 1)
	assert.Equal(t, "review", entries[0].Name)
	require.NotNil(t, entries[0].Subagents)
	// "default" inherited from base.
	assert.Equal(t, "haiku", *entries[0].Subagents["default"])
	// "correctness" overridden by overlay.
	assert.Equal(t, "sonnet", *entries[0].Subagents["correctness"])
}

func TestSubagentsMerge_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: review
    subagents:
      default: haiku
      correctness: opus
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: review
    subagents:
      correctness: sonnet
`), 0o644))
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)

	// Writing into a merged result must not reach the parent layer's own
	// map. Comparing two merged results cannot detect that: the mutation
	// writes exactly the value the second merge would compute anyway, so
	// the guard has to mutate and then re-read.
	entries1 := cfg.AgentEntries()
	require.Len(t, entries1, 1)
	require.NotNil(t, entries1[0].Subagents)
	entries1[0].Subagents["default"] = strPtrCfg("mutated")

	entries2 := cfg.AgentEntries()
	require.Len(t, entries2, 1)
	assert.Equal(t, "haiku", *entries2[0].Subagents["default"],
		"the base layer's map was mutated through the merged result")
	assert.Equal(t, "sonnet", *entries2[0].Subagents["correctness"])
}

func strPtrCfg(s string) *string { return &s }

func TestSubagentsMerge_TombstonePreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: review
    subagents:
      default: haiku
      correctness: opus
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`# fullsend per-repo configuration
version: "1"
agents:
  - name: review
    subagents:
      correctness: ~
`), 0o644))
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	entries := cfg.AgentEntries()
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].Subagents)
	// "default" inherited from base.
	assert.Equal(t, "haiku", *entries[0].Subagents["default"])
	// "correctness" tombstoned by overlay (nil pointer).
	val, exists := entries[0].Subagents["correctness"]
	assert.True(t, exists, "tombstone key should be present")
	assert.Nil(t, val, "tombstone value should be nil")
}

func strPtr(s string) *string { return &s }

func TestPerRepoConfig_LocalAgentEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("# fullsend per-repo configuration\nversion: \"1\"\nagents:\n  - source: harness/lint.yaml\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("# fullsend per-repo configuration\nversion: \"1\"\nagents:\n  - name: code\n    model: sonnet\n"), 0o644))
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	local := cfg.(interface{ LocalAgentEntries() []AgentEntry }).LocalAgentEntries()
	assert.Equal(t, []AgentEntry{{Name: "code", Model: "sonnet"}}, local, "only the overlay's own entries")
	assert.Len(t, cfg.AgentEntries(), 2, "merged view includes the base")
}
