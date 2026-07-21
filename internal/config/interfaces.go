package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

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
// orgConfig and perRepoConfig. Consumer packages should depend on this
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
	DisabledRepos() []string
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

// PerRepoConfigWriter extends PerRepoConfigReader and ConfigWriter with
// per-repo-specific mutation methods.
type PerRepoConfigWriter interface {
	PerRepoConfigReader
	ConfigWriter
	SetRoles([]string)
	SetRuntime(string)
}

// --- Compile-time assertions ---

var (
	_ ConfigReader        = (*orgConfig)(nil)
	_ ConfigReader        = (*perRepoConfig)(nil)
	_ OrgConfigReader     = (*orgConfig)(nil)
	_ PerRepoConfigReader = (*perRepoConfig)(nil)
	_ ConfigWriter        = (*orgConfig)(nil)
	_ ConfigWriter        = (*perRepoConfig)(nil)
	_ OrgConfigWriter     = (*orgConfig)(nil)
	_ PerRepoConfigWriter = (*perRepoConfig)(nil)
)

// --- orgConfig getter methods ---

// AgentEntries returns the registered agent entries.
func (c *orgConfig) AgentEntries() []AgentEntry { return c.Agents }

// IsKillSwitchActive reports whether the kill switch is engaged.
func (c *orgConfig) IsKillSwitchActive() bool { return c.KillSwitch }

// AllowedResources returns the allowed remote resource prefixes.
func (c *orgConfig) AllowedResources() []string { return c.AllowedRemoteResources }

// IssueCreationConfig returns the cross-repo issue creation config.
func (c *orgConfig) IssueCreationConfig() *CreateIssuesConfig { return c.CreateIssues }

// ConfigVersion returns the config schema version.
func (c *orgConfig) ConfigVersion() string { return c.Version }

// IsOrgMode reports that this is an org-mode configuration.
func (c *orgConfig) IsOrgMode() bool { return true }

// DispatchSettings returns the dispatch configuration.
func (c *orgConfig) DispatchSettings() DispatchConfig { return c.Dispatch }

// InferenceSettings returns the inference provider configuration.
func (c *orgConfig) InferenceSettings() InferenceConfig { return c.Inference }

// OrgRepoDefaults returns the default settings applied to all repos.
func (c *orgConfig) OrgRepoDefaults() RepoDefaults { return c.Defaults }

// RepoMap returns the per-repo configuration map.
func (c *orgConfig) RepoMap() map[string]RepoConfig { return c.Repos }

// StatusNotifications returns the status notification configuration.
func (c *orgConfig) StatusNotifications() *StatusNotificationConfig {
	return c.Defaults.StatusNotifications
}

// --- orgConfig setter methods ---

// SetKillSwitch sets the kill switch state.
func (c *orgConfig) SetKillSwitch(v bool) { c.KillSwitch = v }

// SetAgents replaces the registered agent entries.
func (c *orgConfig) SetAgents(agents []AgentEntry) { c.Agents = agents }

// SetAllowedRemoteResources replaces the allowed remote resource
// prefixes.
func (c *orgConfig) SetAllowedRemoteResources(resources []string) {
	c.AllowedRemoteResources = resources
}

// SetDispatch replaces the dispatch configuration.
func (c *orgConfig) SetDispatch(d DispatchConfig) { c.Dispatch = d }

// SetInference replaces the inference provider configuration.
func (c *orgConfig) SetInference(i InferenceConfig) { c.Inference = i }

// --- perRepoConfig getter methods ---

// AgentEntries returns the registered agent entries.
func (c *perRepoConfig) AgentEntries() []AgentEntry { return c.Agents }

// IsKillSwitchActive reports whether the kill switch is engaged.
func (c *perRepoConfig) IsKillSwitchActive() bool { return c.KillSwitch }

// AllowedResources returns the allowed remote resource prefixes.
func (c *perRepoConfig) AllowedResources() []string { return c.AllowedRemoteResources }

// IssueCreationConfig returns the cross-repo issue creation config.
func (c *perRepoConfig) IssueCreationConfig() *CreateIssuesConfig { return c.CreateIssues }

// ConfigVersion returns the config schema version.
func (c *perRepoConfig) ConfigVersion() string { return c.Version }

// IsOrgMode reports that this is a per-repo configuration.
func (c *perRepoConfig) IsOrgMode() bool { return false }

// ConfigRoles returns the configured agent roles.
func (c *perRepoConfig) ConfigRoles() []string { return c.Roles }

// ConfigRuntime returns the configured agent runtime.
func (c *perRepoConfig) ConfigRuntime() string { return c.Runtime }

// --- perRepoConfig setter methods ---

// SetKillSwitch sets the kill switch state.
func (c *perRepoConfig) SetKillSwitch(v bool) { c.KillSwitch = v }

// SetAgents replaces the registered agent entries.
func (c *perRepoConfig) SetAgents(agents []AgentEntry) { c.Agents = agents }

// SetAllowedRemoteResources replaces the allowed remote resource
// prefixes.
func (c *perRepoConfig) SetAllowedRemoteResources(resources []string) {
	c.AllowedRemoteResources = resources
}

// SetRoles replaces the configured agent roles.
func (c *perRepoConfig) SetRoles(roles []string) { c.Roles = roles }

// SetRuntime replaces the configured agent runtime.
func (c *perRepoConfig) SetRuntime(runtime string) { c.Runtime = runtime }

// --- LoadConfig / LoadConfigWriter factories ---

// LoadOpts controls how LoadConfig handles a missing config.yaml.
type LoadOpts struct {
	// MissingOK returns a default config when config.yaml is absent.
	MissingOK bool
}

// LoadConfig reads and parses config.yaml from dir, returning a
// ConfigReader. This is the preferred entry point for consumer packages
// that only need read access.
func LoadConfig(dir string, opts LoadOpts) (ConfigReader, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) && opts.MissingOK {
			return NewPerRepoConfig(nil, ""), nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return parseConfigReader(data)
}

// LoadConfigWriter reads and parses config.yaml from dir, returning a
// ConfigWriter. This is the preferred entry point for consumer packages
// that need read-write access (e.g. CLI commands that modify and
// write-back config). It wraps parseConfigData and returns the
// underlying orgConfig or perRepoConfig.
func LoadConfigWriter(dir string, opts LoadOpts) (ConfigWriter, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) && opts.MissingOK {
			return NewPerRepoConfig(nil, ""), nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return parseConfigWriter(data)
}

// parseConfigReader parses raw config YAML into a ConfigReader.
func parseConfigReader(data []byte) (ConfigReader, error) {
	var probe interface{}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if IsPerRepoYAML(data) {
		return ParsePerRepoConfig(data)
	}
	return ParseOrgConfig(data)
}

// parseConfigWriter parses raw config YAML into a ConfigWriter.
func parseConfigWriter(data []byte) (ConfigWriter, error) {
	var probe interface{}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if IsPerRepoYAML(data) {
		return ParsePerRepoConfigWriter(data)
	}
	return ParseOrgConfigWriter(data)
}

// IsPerRepoYAML probes raw YAML for structural markers that distinguish
// perRepoConfig from orgConfig. orgConfig has org-only top-level keys
// (dispatch, repos, inference, defaults); perRepoConfig never does. When
// no org-only key is present we default to per-repo: the shared fields
// parse identically under either parser, and this avoids silently
// dropping the per-repo Roles field on configs that omit it.
func IsPerRepoYAML(data []byte) bool {
	var probe map[string]interface{}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	for _, key := range []string{"dispatch", "repos", "inference", "defaults"} {
		if _, ok := probe[key]; ok {
			return false
		}
	}
	return true
}
