package config

import "fmt"

// --- Sub-interfaces for common behaviors ---

// AgentLister provides read access to registered agent entries.
type AgentLister interface {
	AgentEntries() []AgentEntry
}

// KillSwitchReader provides read access to the kill switch state.
type KillSwitchReader interface {
	IsKillSwitchActive() bool
}

// AllowedResourcesReader provides read access to the allowed remote
// resources list.
type AllowedResourcesReader interface {
	AllowedResources() []string
}

// CreateIssuesReader provides read access to cross-repo issue creation
// configuration.
type CreateIssuesReader interface {
	IssueCreationConfig() *CreateIssuesConfig
}

// --- Composite read interface ---

// ConfigReader is the common read interface for fields shared by both
// OrgConfig and PerRepoConfig. Consumer packages should depend on this
// interface rather than accessing struct fields directly.
type ConfigReader interface {
	AgentLister
	KillSwitchReader
	AllowedResourcesReader
	CreateIssuesReader
	ConfigVersion() string
	IsOrgMode() bool
}

// --- Mode-specific read interfaces ---

// OrgConfigReader extends ConfigReader with org-mode-specific fields.
type OrgConfigReader interface {
	ConfigReader
	DispatchSettings() DispatchConfig
	InferenceSettings() InferenceConfig
	OrgRepoDefaults() RepoDefaults
	RepoMap() map[string]RepoConfig
	EnabledRepos() []string
	StatusNotifications() *StatusNotificationConfig
}

// PerRepoConfigReader extends ConfigReader with per-repo-specific
// fields. Methods are prefixed with "Config" to avoid conflicts with
// the struct field names (Roles and Runtime).
type PerRepoConfigReader interface {
	ConfigReader
	ConfigRoles() []string
	ConfigRuntime() string
}

// --- Write superset interfaces ---

// ConfigWriter extends ConfigReader with mutation methods shared by
// both config modes.
type ConfigWriter interface {
	ConfigReader
	SetKillSwitch(bool)
	SetAgents([]AgentEntry)
	SetAllowedRemoteResources([]string)
	Marshal() ([]byte, error)
	Validate() error
}

// OrgConfigWriter extends OrgConfigReader and ConfigWriter with
// org-specific mutation methods.
type OrgConfigWriter interface {
	OrgConfigReader
	ConfigWriter
	SetDispatch(DispatchConfig)
	SetInference(InferenceConfig)
}

// --- Compile-time assertions ---

var (
	_ ConfigReader        = (*OrgConfig)(nil)
	_ ConfigReader        = (*PerRepoConfig)(nil)
	_ ConfigReader        = (*DirConfig)(nil)
	_ OrgConfigReader     = (*OrgConfig)(nil)
	_ PerRepoConfigReader = (*PerRepoConfig)(nil)
	_ ConfigWriter        = (*OrgConfig)(nil)
	_ ConfigWriter        = (*PerRepoConfig)(nil)
	_ OrgConfigWriter     = (*OrgConfig)(nil)
)

// --- OrgConfig getter methods ---

// AgentEntries returns the registered agent entries.
func (c *OrgConfig) AgentEntries() []AgentEntry { return c.Agents }

// IsKillSwitchActive reports whether the kill switch is engaged.
func (c *OrgConfig) IsKillSwitchActive() bool { return c.KillSwitch }

// AllowedResources returns the allowed remote resource prefixes.
func (c *OrgConfig) AllowedResources() []string { return c.AllowedRemoteResources }

// IssueCreationConfig returns the cross-repo issue creation config.
func (c *OrgConfig) IssueCreationConfig() *CreateIssuesConfig { return c.CreateIssues }

// ConfigVersion returns the config schema version.
func (c *OrgConfig) ConfigVersion() string { return c.Version }

// IsOrgMode reports that this is an org-mode configuration.
func (c *OrgConfig) IsOrgMode() bool { return true }

// DispatchSettings returns the dispatch configuration.
func (c *OrgConfig) DispatchSettings() DispatchConfig { return c.Dispatch }

// InferenceSettings returns the inference provider configuration.
func (c *OrgConfig) InferenceSettings() InferenceConfig { return c.Inference }

// OrgRepoDefaults returns the default settings applied to all repos.
func (c *OrgConfig) OrgRepoDefaults() RepoDefaults { return c.Defaults }

// RepoMap returns the per-repo configuration map.
func (c *OrgConfig) RepoMap() map[string]RepoConfig { return c.Repos }

// StatusNotifications returns the status notification configuration.
func (c *OrgConfig) StatusNotifications() *StatusNotificationConfig {
	return c.Defaults.StatusNotifications
}

// --- OrgConfig setter methods ---

// SetKillSwitch sets the kill switch state.
func (c *OrgConfig) SetKillSwitch(v bool) { c.KillSwitch = v }

// SetAgents replaces the registered agent entries.
func (c *OrgConfig) SetAgents(agents []AgentEntry) { c.Agents = agents }

// SetAllowedRemoteResources replaces the allowed remote resource
// prefixes.
func (c *OrgConfig) SetAllowedRemoteResources(resources []string) {
	c.AllowedRemoteResources = resources
}

// SetDispatch replaces the dispatch configuration.
func (c *OrgConfig) SetDispatch(d DispatchConfig) { c.Dispatch = d }

// SetInference replaces the inference provider configuration.
func (c *OrgConfig) SetInference(i InferenceConfig) { c.Inference = i }

// --- PerRepoConfig getter methods ---

// AgentEntries returns the registered agent entries.
func (c *PerRepoConfig) AgentEntries() []AgentEntry { return c.Agents }

// IsKillSwitchActive reports whether the kill switch is engaged.
func (c *PerRepoConfig) IsKillSwitchActive() bool { return c.KillSwitch }

// AllowedResources returns the allowed remote resource prefixes.
func (c *PerRepoConfig) AllowedResources() []string { return c.AllowedRemoteResources }

// IssueCreationConfig returns the cross-repo issue creation config.
func (c *PerRepoConfig) IssueCreationConfig() *CreateIssuesConfig { return c.CreateIssues }

// ConfigVersion returns the config schema version.
func (c *PerRepoConfig) ConfigVersion() string { return c.Version }

// IsOrgMode reports that this is a per-repo configuration.
func (c *PerRepoConfig) IsOrgMode() bool { return false }

// ConfigRoles returns the configured agent roles.
func (c *PerRepoConfig) ConfigRoles() []string { return c.Roles }

// ConfigRuntime returns the configured agent runtime.
func (c *PerRepoConfig) ConfigRuntime() string { return c.Runtime }

// --- PerRepoConfig setter methods ---

// SetKillSwitch sets the kill switch state.
func (c *PerRepoConfig) SetKillSwitch(v bool) { c.KillSwitch = v }

// SetAgents replaces the registered agent entries.
func (c *PerRepoConfig) SetAgents(agents []AgentEntry) { c.Agents = agents }

// SetAllowedRemoteResources replaces the allowed remote resource
// prefixes.
func (c *PerRepoConfig) SetAllowedRemoteResources(resources []string) {
	c.AllowedRemoteResources = resources
}

// --- DirConfig getter methods (ConfigReader) ---

// AgentEntries returns the registered agent entries.
func (dc *DirConfig) AgentEntries() []AgentEntry { return dc.Agents }

// IsKillSwitchActive reports whether the kill switch is engaged.
func (dc *DirConfig) IsKillSwitchActive() bool { return dc.KillSwitch }

// AllowedResources returns the allowed remote resource prefixes.
func (dc *DirConfig) AllowedResources() []string { return dc.AllowedRemoteResources }

// IsOrgMode reports whether this is an org-mode configuration.
func (dc *DirConfig) IsOrgMode() bool { return dc.IsOrg }

// ConfigVersion returns the config schema version by delegating to the
// underlying OrgConfig or PerRepoConfig.
func (dc *DirConfig) ConfigVersion() string {
	if dc.Org != nil {
		return dc.Org.Version
	}
	if dc.PerRepo != nil {
		return dc.PerRepo.Version
	}
	return ""
}

// IssueCreationConfig returns the cross-repo issue creation config by
// delegating to the underlying OrgConfig or PerRepoConfig.
func (dc *DirConfig) IssueCreationConfig() *CreateIssuesConfig {
	if dc.Org != nil {
		return dc.Org.CreateIssues
	}
	if dc.PerRepo != nil {
		return dc.PerRepo.CreateIssues
	}
	return nil
}

// --- LoadConfig factory ---

// LoadConfig reads and parses config.yaml from dir, returning a
// ConfigReader. This is the preferred entry point for consumer packages
// that only need read access. It wraps LoadFromDir and returns the
// underlying OrgConfig or PerRepoConfig.
func LoadConfig(dir string, opts LoadOpts) (ConfigReader, error) {
	dc, err := LoadFromDir(dir, opts)
	if err != nil {
		return nil, err
	}
	if dc.IsOrg {
		if dc.Org == nil {
			return nil, fmt.Errorf("org config is nil despite IsOrg=true")
		}
		return dc.Org, nil
	}
	if dc.PerRepo == nil {
		return nil, fmt.Errorf("per-repo config is nil despite IsOrg=false")
	}
	return dc.PerRepo, nil
}
