package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- OrgConfig getter tests ---

func TestOrgConfig_AgentEntries(t *testing.T) {
	agents := []AgentEntry{{Source: "harness/triage.yaml"}}
	cfg := &OrgConfig{Agents: agents}
	assert.Equal(t, agents, cfg.AgentEntries())
}

func TestOrgConfig_IsKillSwitchActive(t *testing.T) {
	cfg := &OrgConfig{KillSwitch: true}
	assert.True(t, cfg.IsKillSwitchActive())
	cfg.KillSwitch = false
	assert.False(t, cfg.IsKillSwitchActive())
}

func TestOrgConfig_AllowedResources(t *testing.T) {
	resources := []string{"https://example.com/"}
	cfg := &OrgConfig{AllowedRemoteResources: resources}
	assert.Equal(t, resources, cfg.AllowedResources())
}

func TestOrgConfig_IssueCreationConfig(t *testing.T) {
	ci := &CreateIssuesConfig{AllowTargets: AllowTargets{Orgs: []string{"my-org"}}}
	cfg := &OrgConfig{CreateIssues: ci}
	assert.Equal(t, ci, cfg.IssueCreationConfig())
}

func TestOrgConfig_IssueCreationConfig_Nil(t *testing.T) {
	cfg := &OrgConfig{}
	assert.Nil(t, cfg.IssueCreationConfig())
}

func TestOrgConfig_ConfigVersion(t *testing.T) {
	cfg := &OrgConfig{Version: "1"}
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestOrgConfig_IsOrgMode(t *testing.T) {
	cfg := &OrgConfig{}
	assert.True(t, cfg.IsOrgMode())
}

func TestOrgConfig_DispatchSettings(t *testing.T) {
	dispatch := DispatchConfig{Platform: "github-actions", MintURL: "https://mint.example.com"}
	cfg := &OrgConfig{Dispatch: dispatch}
	assert.Equal(t, dispatch, cfg.DispatchSettings())
}

func TestOrgConfig_InferenceSettings(t *testing.T) {
	inference := InferenceConfig{Provider: "vertex"}
	cfg := &OrgConfig{Inference: inference}
	assert.Equal(t, inference, cfg.InferenceSettings())
}

func TestOrgConfig_OrgRepoDefaults(t *testing.T) {
	defaults := RepoDefaults{Roles: []string{"triage"}, Runtime: "claude"}
	cfg := &OrgConfig{Defaults: defaults}
	assert.Equal(t, defaults, cfg.OrgRepoDefaults())
}

func TestOrgConfig_RepoMap(t *testing.T) {
	repos := map[string]RepoConfig{
		"repo-a": {Enabled: true},
		"repo-b": {Enabled: false},
	}
	cfg := &OrgConfig{Repos: repos}
	assert.Equal(t, repos, cfg.RepoMap())
}

func TestOrgConfig_StatusNotifications(t *testing.T) {
	sn := &StatusNotificationConfig{Comment: CommentNotificationConfig{Start: "enabled"}}
	cfg := &OrgConfig{Defaults: RepoDefaults{StatusNotifications: sn}}
	assert.Equal(t, sn, cfg.StatusNotifications())
}

func TestOrgConfig_StatusNotifications_Nil(t *testing.T) {
	cfg := &OrgConfig{}
	assert.Nil(t, cfg.StatusNotifications())
}

// --- OrgConfig setter tests ---

func TestOrgConfig_SetKillSwitch(t *testing.T) {
	cfg := &OrgConfig{}
	cfg.SetKillSwitch(true)
	assert.True(t, cfg.KillSwitch)
	cfg.SetKillSwitch(false)
	assert.False(t, cfg.KillSwitch)
}

func TestOrgConfig_SetAgents(t *testing.T) {
	cfg := &OrgConfig{}
	agents := []AgentEntry{{Source: "harness/code.yaml"}}
	cfg.SetAgents(agents)
	assert.Equal(t, agents, cfg.Agents)
}

func TestOrgConfig_SetAllowedRemoteResources(t *testing.T) {
	cfg := &OrgConfig{}
	resources := []string{"https://example.com/"}
	cfg.SetAllowedRemoteResources(resources)
	assert.Equal(t, resources, cfg.AllowedRemoteResources)
}

func TestOrgConfig_SetDispatch(t *testing.T) {
	cfg := &OrgConfig{}
	d := DispatchConfig{Platform: "github-actions", MintURL: "https://mint.example.com"}
	cfg.SetDispatch(d)
	assert.Equal(t, d, cfg.Dispatch)
}

func TestOrgConfig_SetInference(t *testing.T) {
	cfg := &OrgConfig{}
	i := InferenceConfig{Provider: "vertex"}
	cfg.SetInference(i)
	assert.Equal(t, i, cfg.Inference)
}

// --- PerRepoConfig getter tests ---

func TestPerRepoConfig_AgentEntries(t *testing.T) {
	agents := []AgentEntry{{Source: "harness/triage.yaml"}}
	cfg := &PerRepoConfig{Agents: agents}
	assert.Equal(t, agents, cfg.AgentEntries())
}

func TestPerRepoConfig_IsKillSwitchActive(t *testing.T) {
	cfg := &PerRepoConfig{KillSwitch: true}
	assert.True(t, cfg.IsKillSwitchActive())
	cfg.KillSwitch = false
	assert.False(t, cfg.IsKillSwitchActive())
}

func TestPerRepoConfig_AllowedResources(t *testing.T) {
	resources := []string{"https://example.com/"}
	cfg := &PerRepoConfig{AllowedRemoteResources: resources}
	assert.Equal(t, resources, cfg.AllowedResources())
}

func TestPerRepoConfig_IssueCreationConfig(t *testing.T) {
	ci := &CreateIssuesConfig{AllowTargets: AllowTargets{Repos: []string{"org/repo"}}}
	cfg := &PerRepoConfig{CreateIssues: ci}
	assert.Equal(t, ci, cfg.IssueCreationConfig())
}

func TestPerRepoConfig_IssueCreationConfig_Nil(t *testing.T) {
	cfg := &PerRepoConfig{}
	assert.Nil(t, cfg.IssueCreationConfig())
}

func TestPerRepoConfig_ConfigVersion(t *testing.T) {
	cfg := &PerRepoConfig{Version: "1"}
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestPerRepoConfig_IsOrgMode(t *testing.T) {
	cfg := &PerRepoConfig{}
	assert.False(t, cfg.IsOrgMode())
}

func TestPerRepoConfig_ConfigRoles(t *testing.T) {
	roles := []string{"triage", "coder"}
	cfg := &PerRepoConfig{Roles: roles}
	assert.Equal(t, roles, cfg.ConfigRoles())
}

func TestPerRepoConfig_ConfigRuntime(t *testing.T) {
	cfg := &PerRepoConfig{Runtime: "claude"}
	assert.Equal(t, "claude", cfg.ConfigRuntime())
}

// --- PerRepoConfig setter tests ---

func TestPerRepoConfig_SetKillSwitch(t *testing.T) {
	cfg := &PerRepoConfig{}
	cfg.SetKillSwitch(true)
	assert.True(t, cfg.KillSwitch)
	cfg.SetKillSwitch(false)
	assert.False(t, cfg.KillSwitch)
}

func TestPerRepoConfig_SetAgents(t *testing.T) {
	cfg := &PerRepoConfig{}
	agents := []AgentEntry{{Source: "harness/code.yaml"}}
	cfg.SetAgents(agents)
	assert.Equal(t, agents, cfg.Agents)
}

func TestPerRepoConfig_SetAllowedRemoteResources(t *testing.T) {
	cfg := &PerRepoConfig{}
	resources := []string{"https://example.com/"}
	cfg.SetAllowedRemoteResources(resources)
	assert.Equal(t, resources, cfg.AllowedRemoteResources)
}

// --- DirConfig getter tests ---

func TestDirConfig_AgentEntries(t *testing.T) {
	agents := []AgentEntry{{Source: "harness/triage.yaml"}}
	dc := &DirConfig{Agents: agents}
	assert.Equal(t, agents, dc.AgentEntries())
}

func TestDirConfig_IsKillSwitchActive(t *testing.T) {
	dc := &DirConfig{KillSwitch: true}
	assert.True(t, dc.IsKillSwitchActive())
	dc.KillSwitch = false
	assert.False(t, dc.IsKillSwitchActive())
}

func TestDirConfig_AllowedResources(t *testing.T) {
	resources := []string{"https://example.com/"}
	dc := &DirConfig{AllowedRemoteResources: resources}
	assert.Equal(t, resources, dc.AllowedResources())
}

func TestDirConfig_IsOrgMode(t *testing.T) {
	dc := &DirConfig{IsOrg: true}
	assert.True(t, dc.IsOrgMode())
	dc.IsOrg = false
	assert.False(t, dc.IsOrgMode())
}

func TestDirConfig_ConfigVersion_Org(t *testing.T) {
	dc := &DirConfig{Org: &OrgConfig{Version: "1"}}
	assert.Equal(t, "1", dc.ConfigVersion())
}

func TestDirConfig_ConfigVersion_PerRepo(t *testing.T) {
	dc := &DirConfig{PerRepo: &PerRepoConfig{Version: "1"}}
	assert.Equal(t, "1", dc.ConfigVersion())
}

func TestDirConfig_ConfigVersion_BothNil(t *testing.T) {
	dc := &DirConfig{}
	assert.Equal(t, "", dc.ConfigVersion())
}

func TestDirConfig_IssueCreationConfig_Org(t *testing.T) {
	ci := &CreateIssuesConfig{AllowTargets: AllowTargets{Orgs: []string{"my-org"}}}
	dc := &DirConfig{Org: &OrgConfig{CreateIssues: ci}}
	assert.Equal(t, ci, dc.IssueCreationConfig())
}

func TestDirConfig_IssueCreationConfig_PerRepo(t *testing.T) {
	ci := &CreateIssuesConfig{AllowTargets: AllowTargets{Repos: []string{"org/repo"}}}
	dc := &DirConfig{PerRepo: &PerRepoConfig{CreateIssues: ci}}
	assert.Equal(t, ci, dc.IssueCreationConfig())
}

func TestDirConfig_IssueCreationConfig_BothNil(t *testing.T) {
	dc := &DirConfig{}
	assert.Nil(t, dc.IssueCreationConfig())
}

// --- LoadConfig factory tests ---

func TestLoadConfig_OrgConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := NewOrgConfig(nil, nil, nil, "", "")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	reader, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	assert.True(t, reader.IsOrgMode())
}

func TestLoadConfig_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := NewPerRepoConfig(nil, "o/r")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	reader, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	assert.False(t, reader.IsOrgMode())
}

func TestLoadConfig_MissingOK(t *testing.T) {
	dir := t.TempDir()
	reader, err := LoadConfig(dir, LoadOpts{MissingOK: true})
	require.NoError(t, err)
	assert.False(t, reader.IsOrgMode())
}

func TestLoadConfig_MissingNotOK(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfig(dir, LoadOpts{})
	require.Error(t, err)
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("not: valid: yaml: ["), 0o644))
	_, err := LoadConfig(dir, LoadOpts{})
	require.Error(t, err)
}

// --- Interface satisfaction tests ---

func TestOrgConfig_SatisfiesConfigReader(t *testing.T) {
	var _ ConfigReader = (*OrgConfig)(nil)
}

func TestPerRepoConfig_SatisfiesConfigReader(t *testing.T) {
	var _ ConfigReader = (*PerRepoConfig)(nil)
}

func TestDirConfig_SatisfiesConfigReader(t *testing.T) {
	var _ ConfigReader = (*DirConfig)(nil)
}

func TestOrgConfig_SatisfiesOrgConfigReader(t *testing.T) {
	var _ OrgConfigReader = (*OrgConfig)(nil)
}

func TestPerRepoConfig_SatisfiesPerRepoConfigReader(t *testing.T) {
	var _ PerRepoConfigReader = (*PerRepoConfig)(nil)
}

func TestOrgConfig_SatisfiesConfigWriter(t *testing.T) {
	var _ ConfigWriter = (*OrgConfig)(nil)
}

func TestPerRepoConfig_SatisfiesConfigWriter(t *testing.T) {
	var _ ConfigWriter = (*PerRepoConfig)(nil)
}

func TestOrgConfig_SatisfiesOrgConfigWriter(t *testing.T) {
	var _ OrgConfigWriter = (*OrgConfig)(nil)
}

// --- ConfigWriter integration tests ---

func TestOrgConfig_ConfigWriter_RoundTrip(t *testing.T) {
	var w ConfigWriter = NewOrgConfig(nil, nil, nil, "", "")
	w.SetKillSwitch(true)
	assert.True(t, w.IsKillSwitchActive())

	agents := []AgentEntry{{Source: "harness/triage.yaml"}}
	w.SetAgents(agents)
	assert.Equal(t, agents, w.AgentEntries())

	resources := []string{"https://example.com/"}
	w.SetAllowedRemoteResources(resources)
	assert.Equal(t, resources, w.AllowedResources())

	data, err := w.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "kill_switch: true")
}

func TestPerRepoConfig_ConfigWriter_RoundTrip(t *testing.T) {
	var w ConfigWriter = NewPerRepoConfig(nil, "o/r")
	w.SetKillSwitch(true)
	assert.True(t, w.IsKillSwitchActive())

	agents := []AgentEntry{{Source: "harness/code.yaml"}}
	w.SetAgents(agents)
	assert.Equal(t, agents, w.AgentEntries())

	data, err := w.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "kill_switch: true")
}

func TestOrgConfigWriter_RoundTrip(t *testing.T) {
	var w OrgConfigWriter = NewOrgConfig(nil, nil, nil, "", "")
	d := DispatchConfig{Platform: "github-actions", MintURL: "https://mint.example.com"}
	w.SetDispatch(d)
	assert.Equal(t, d, w.DispatchSettings())

	i := InferenceConfig{Provider: "vertex"}
	w.SetInference(i)
	assert.Equal(t, i, w.InferenceSettings())
}
