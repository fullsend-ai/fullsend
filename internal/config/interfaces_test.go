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
	cfg := &orgConfig{Agents: agents}
	assert.Equal(t, agents, cfg.AgentEntries())
}

func TestOrgConfig_IsKillSwitchActive(t *testing.T) {
	cfg := &orgConfig{KillSwitch: true}
	assert.True(t, cfg.IsKillSwitchActive())
	cfg.KillSwitch = false
	assert.False(t, cfg.IsKillSwitchActive())
}

func TestOrgConfig_AllowedResources(t *testing.T) {
	resources := []string{"https://example.com/"}
	cfg := &orgConfig{AllowedRemoteResources: resources}
	assert.Equal(t, resources, cfg.AllowedResources())
}

func TestOrgConfig_IssueCreationConfig(t *testing.T) {
	ci := &CreateIssuesConfig{AllowTargets: AllowTargets{Orgs: []string{"my-org"}}}
	cfg := &orgConfig{CreateIssues: ci}
	assert.Equal(t, ci, cfg.IssueCreationConfig())
}

func TestOrgConfig_IssueCreationConfig_Nil(t *testing.T) {
	cfg := &orgConfig{}
	assert.Nil(t, cfg.IssueCreationConfig())
}

func TestOrgConfig_ConfigVersion(t *testing.T) {
	cfg := &orgConfig{Version: "1"}
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestOrgConfig_IsOrgMode(t *testing.T) {
	cfg := &orgConfig{}
	assert.True(t, cfg.IsOrgMode())
}

func TestOrgConfig_DispatchSettings(t *testing.T) {
	dispatch := DispatchConfig{Platform: "github-actions", MintURL: "https://mint.example.com"}
	cfg := &orgConfig{Dispatch: dispatch}
	assert.Equal(t, dispatch, cfg.DispatchSettings())
}

func TestOrgConfig_InferenceSettings(t *testing.T) {
	inference := InferenceConfig{Provider: "vertex"}
	cfg := &orgConfig{Inference: inference}
	assert.Equal(t, inference, cfg.InferenceSettings())
}

func TestOrgConfig_OrgRepoDefaults(t *testing.T) {
	defaults := RepoDefaults{Roles: []string{"triage"}, Runtime: "claude"}
	cfg := &orgConfig{Defaults: defaults}
	assert.Equal(t, defaults, cfg.OrgRepoDefaults())
}

func TestOrgConfig_RepoMap(t *testing.T) {
	repos := map[string]RepoConfig{
		"repo-a": {Enabled: true},
		"repo-b": {Enabled: false},
	}
	cfg := &orgConfig{Repos: repos}
	assert.Equal(t, repos, cfg.RepoMap())
}

func TestOrgConfig_StatusNotifications(t *testing.T) {
	sn := &StatusNotificationConfig{Comment: CommentNotificationConfig{Start: "enabled"}}
	cfg := &orgConfig{Defaults: RepoDefaults{StatusNotifications: sn}}
	assert.Equal(t, sn, cfg.StatusNotifications())
}

func TestOrgConfig_StatusNotifications_Nil(t *testing.T) {
	cfg := &orgConfig{}
	assert.Nil(t, cfg.StatusNotifications())
}

// --- OrgConfig setter tests ---

func TestOrgConfig_SetKillSwitch(t *testing.T) {
	cfg := &orgConfig{}
	cfg.SetKillSwitch(true)
	assert.True(t, cfg.KillSwitch)
	cfg.SetKillSwitch(false)
	assert.False(t, cfg.KillSwitch)
}

func TestOrgConfig_SetAgents(t *testing.T) {
	cfg := &orgConfig{}
	agents := []AgentEntry{{Source: "harness/code.yaml"}}
	cfg.SetAgents(agents)
	assert.Equal(t, agents, cfg.Agents)
}

func TestOrgConfig_SetAllowedRemoteResources(t *testing.T) {
	cfg := &orgConfig{}
	resources := []string{"https://example.com/"}
	cfg.SetAllowedRemoteResources(resources)
	assert.Equal(t, resources, cfg.AllowedRemoteResources)
}

func TestOrgConfig_SetDispatch(t *testing.T) {
	cfg := &orgConfig{}
	d := DispatchConfig{Platform: "github-actions", MintURL: "https://mint.example.com"}
	cfg.SetDispatch(d)
	assert.Equal(t, d, cfg.Dispatch)
}

func TestOrgConfig_SetInference(t *testing.T) {
	cfg := &orgConfig{}
	i := InferenceConfig{Provider: "vertex"}
	cfg.SetInference(i)
	assert.Equal(t, i, cfg.Inference)
}

// --- PerRepoConfig getter tests ---

func TestPerRepoConfig_AgentEntries(t *testing.T) {
	agents := []AgentEntry{{Source: "harness/triage.yaml"}}
	cfg := &perRepoConfig{Agents: agents}
	assert.Equal(t, agents, cfg.AgentEntries())
}

func TestPerRepoConfig_IsKillSwitchActive(t *testing.T) {
	cfg := &perRepoConfig{KillSwitch: true}
	assert.True(t, cfg.IsKillSwitchActive())
	cfg.KillSwitch = false
	assert.False(t, cfg.IsKillSwitchActive())
}

func TestPerRepoConfig_AllowedResources(t *testing.T) {
	resources := []string{"https://example.com/"}
	cfg := &perRepoConfig{AllowedRemoteResources: resources}
	assert.Equal(t, resources, cfg.AllowedResources())
}

func TestPerRepoConfig_IssueCreationConfig(t *testing.T) {
	ci := &CreateIssuesConfig{AllowTargets: AllowTargets{Repos: []string{"org/repo"}}}
	cfg := &perRepoConfig{CreateIssues: ci}
	assert.Equal(t, ci, cfg.IssueCreationConfig())
}

func TestPerRepoConfig_IssueCreationConfig_Nil(t *testing.T) {
	cfg := &perRepoConfig{}
	assert.Nil(t, cfg.IssueCreationConfig())
}

func TestPerRepoConfig_ConfigVersion(t *testing.T) {
	cfg := &perRepoConfig{Version: "1"}
	assert.Equal(t, "1", cfg.ConfigVersion())
}

func TestPerRepoConfig_IsOrgMode(t *testing.T) {
	cfg := &perRepoConfig{}
	assert.False(t, cfg.IsOrgMode())
}

func TestPerRepoConfig_ConfigRoles(t *testing.T) {
	roles := []string{"triage", "coder"}
	cfg := &perRepoConfig{Roles: roles}
	assert.Equal(t, roles, cfg.ConfigRoles())
}

func TestPerRepoConfig_ConfigRuntime(t *testing.T) {
	cfg := &perRepoConfig{Runtime: "claude"}
	assert.Equal(t, "claude", cfg.ConfigRuntime())
}

// --- PerRepoConfig setter tests ---

func TestPerRepoConfig_SetKillSwitch(t *testing.T) {
	cfg := &perRepoConfig{}
	cfg.SetKillSwitch(true)
	assert.True(t, cfg.KillSwitch)
	cfg.SetKillSwitch(false)
	assert.False(t, cfg.KillSwitch)
}

func TestPerRepoConfig_SetAgents(t *testing.T) {
	cfg := &perRepoConfig{}
	agents := []AgentEntry{{Source: "harness/code.yaml"}}
	cfg.SetAgents(agents)
	assert.Equal(t, agents, cfg.Agents)
}

func TestPerRepoConfig_SetAllowedRemoteResources(t *testing.T) {
	cfg := &perRepoConfig{}
	resources := []string{"https://example.com/"}
	cfg.SetAllowedRemoteResources(resources)
	assert.Equal(t, resources, cfg.AllowedRemoteResources)
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

// --- LoadConfigWriter factory tests ---

func TestLoadConfigWriter_OrgConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := NewOrgConfig(nil, nil, nil, "", "")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)
	assert.True(t, writer.IsOrgMode())
}

func TestLoadConfigWriter_PerRepoConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := NewPerRepoConfig(nil, "o/r")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)
	assert.False(t, writer.IsOrgMode())
}

func TestLoadConfigWriter_MissingOK(t *testing.T) {
	dir := t.TempDir()
	writer, err := LoadConfigWriter(dir, LoadOpts{MissingOK: true})
	require.NoError(t, err)
	assert.False(t, writer.IsOrgMode())
}

func TestLoadConfigWriter_MissingNotOK(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfigWriter(dir, LoadOpts{})
	require.Error(t, err)
}

func TestLoadConfigWriter_Mutate(t *testing.T) {
	dir := t.TempDir()
	cfg := NewPerRepoConfig(nil, "o/r")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	writer, err := LoadConfigWriter(dir, LoadOpts{})
	require.NoError(t, err)

	writer.SetAgents([]AgentEntry{{Source: "harness/test.yaml"}})
	assert.Len(t, writer.AgentEntries(), 1)

	out, err := writer.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(out), "harness/test.yaml")
}

// --- Interface satisfaction tests ---

func TestOrgConfig_SatisfiesConfigReader(t *testing.T) {
	var _ ConfigReader = (*orgConfig)(nil)
}

func TestPerRepoConfig_SatisfiesConfigReader(t *testing.T) {
	var _ ConfigReader = (*perRepoConfig)(nil)
}

func TestOrgConfig_SatisfiesOrgConfigReader(t *testing.T) {
	var _ OrgConfigReader = (*orgConfig)(nil)
}

func TestPerRepoConfig_SatisfiesPerRepoConfigReader(t *testing.T) {
	var _ PerRepoConfigReader = (*perRepoConfig)(nil)
}

func TestOrgConfig_SatisfiesConfigWriter(t *testing.T) {
	var _ ConfigWriter = (*orgConfig)(nil)
}

func TestPerRepoConfig_SatisfiesConfigWriter(t *testing.T) {
	var _ ConfigWriter = (*perRepoConfig)(nil)
}

func TestOrgConfig_SatisfiesOrgConfigWriter(t *testing.T) {
	var _ OrgConfigWriter = (*orgConfig)(nil)
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

func TestOrgConfig_SetDefaultRuntime(t *testing.T) {
	cfg := &orgConfig{Defaults: RepoDefaults{Runtime: "claude"}}
	cfg.SetDefaultRuntime("dummy")
	assert.Equal(t, "dummy", cfg.OrgRepoDefaults().Runtime)
}

func TestOrgConfig_SetRepo(t *testing.T) {
	cfg := &orgConfig{Repos: map[string]RepoConfig{
		"existing": {Enabled: true},
	}}
	// Update existing entry.
	cfg.SetRepo("existing", RepoConfig{Enabled: false})
	assert.False(t, cfg.RepoMap()["existing"].Enabled)
	// Add new entry.
	cfg.SetRepo("new-repo", RepoConfig{Enabled: true})
	assert.True(t, cfg.RepoMap()["new-repo"].Enabled)
}

func TestOrgConfig_SetRepo_NilMap(t *testing.T) {
	cfg := &orgConfig{}
	cfg.SetRepo("repo-a", RepoConfig{Enabled: true})
	assert.True(t, cfg.RepoMap()["repo-a"].Enabled)
}

func TestOrgConfigWriter_SetDefaultRuntime_RoundTrip(t *testing.T) {
	var w OrgConfigWriter = NewOrgConfig(nil, nil, nil, "", "")
	assert.Equal(t, "claude", w.OrgRepoDefaults().Runtime)
	w.SetDefaultRuntime("dummy")
	assert.Equal(t, "dummy", w.OrgRepoDefaults().Runtime)
}

func TestOrgConfigWriter_SetRepo_RoundTrip(t *testing.T) {
	var w OrgConfigWriter = NewOrgConfig(
		[]string{"repo-a"}, []string{"repo-a"}, nil, "", "",
	)
	assert.True(t, w.RepoMap()["repo-a"].Enabled)
	w.SetRepo("repo-a", RepoConfig{Enabled: false})
	assert.False(t, w.RepoMap()["repo-a"].Enabled)
}
