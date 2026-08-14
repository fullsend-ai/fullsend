package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// StatusNotificationsReader provides read access to status notification
// configuration (comment start & completion).
type StatusNotificationsReader interface {
	StatusNotifications() *StatusNotificationConfig
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
	StatusNotificationsReader
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
}

// PerRepoConfigReader extends ConfigReader with per-repo-specific
// fields. Methods are prefixed with "Config" to avoid conflicts with
// the struct field names (Roles and Runtime).
type PerRepoConfigReader interface {
	ConfigReader
	ConfigRoles() []string
	ConfigRuntime() string
	ConfigForge() string
	ConfigTracker() string
	ConfigMintURL() string
	ConfigInferenceProvider() string
	ConfigInferenceProject() string
	ConfigInferenceRegion() string
	ConfigInferenceWIFProvider() string
}

// --- Write superset interfaces ---

// ConfigWriter extends ConfigReader with mutation methods shared by
// both config modes.
type ConfigWriter interface {
	ConfigReader
	SetKillSwitch(bool)
	SetAgents([]AgentEntry)
	SetAllowedRemoteResources([]string)
	SetStatusNotifications(*StatusNotificationConfig)
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
	SetDefaultRuntime(string)
	SetRepo(name string, rc RepoConfig)
}

// PerRepoConfigWriter extends PerRepoConfigReader and ConfigWriter with
// per-repo-specific mutation methods.
type PerRepoConfigWriter interface {
	PerRepoConfigReader
	ConfigWriter
	SetRoles([]string)
	SetRuntime(string)
	SetMintURL(string)
	SetInferenceProvider(string)
	SetInferenceProject(string)
	SetInferenceRegion(string)
	SetInferenceWIFProvider(string)
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

// SetDefaultRuntime replaces the default agent runtime.
func (c *orgConfig) SetDefaultRuntime(rt string) { c.Defaults.Runtime = rt }

// SetStatusNotifications sets the status notification configuration.
func (c *orgConfig) SetStatusNotifications(sn *StatusNotificationConfig) {
	c.Defaults.StatusNotifications = sn
}

// SetRepo adds or replaces a per-repo configuration entry.
// Callers should use this method instead of mutating the map returned
// by RepoMap() to keep mutations on the writer interface.
func (c *orgConfig) SetRepo(name string, rc RepoConfig) {
	if c.Repos == nil {
		c.Repos = make(map[string]RepoConfig)
	}
	c.Repos[name] = rc
}

// --- perRepoConfig getter methods ---
//
// All getters implement fallback: local value -> parent -> zero value.
// The parent chain terminates at perRepoDefaults which returns
// compiled-in code defaults (ADR 0069 Decision 2).

// AgentEntries returns the merged agent set. When the local Agents
// field is nil (key omitted from YAML), parent agents are returned
// unchanged. When local Agents is non-nil (including empty), a keyed
// merge by DerivedName is performed: overlay entries can toggle
// enable/disable or replace the source of a parent agent without
// replacing the entire agent list.
func (c *perRepoConfig) AgentEntries() []AgentEntry {
	var parentAgents []AgentEntry
	if c.parent != nil {
		parentAgents = c.parent.AgentEntries()
	}
	if c.Agents == nil {
		return parentAgents
	}
	if len(parentAgents) == 0 {
		return c.Agents
	}
	// Build overlay index by lowercase DerivedName. Multiple entries
	// with the same name may exist (disable-then-enable pairs).
	type overlayInfo struct {
		entry AgentEntry
		used  bool
	}
	overlayByName := make(map[string][]*overlayInfo, len(c.Agents))
	overlayOrder := make([]*overlayInfo, 0, len(c.Agents))
	for _, a := range c.Agents {
		oi := &overlayInfo{entry: a}
		key := strings.ToLower(a.DerivedName())
		overlayByName[key] = append(overlayByName[key], oi)
		overlayOrder = append(overlayOrder, oi)
	}

	// For each parent agent, apply matching overlay entries.
	result := make([]AgentEntry, 0, len(parentAgents)+len(c.Agents))
	for _, pa := range parentAgents {
		key := strings.ToLower(pa.DerivedName())
		overlays, hasOverlay := overlayByName[key]
		if !hasOverlay {
			result = append(result, pa)
			continue
		}
		// Apply overlays in order (last wins per field).
		merged := pa
		for _, oi := range overlays {
			oi.used = true
			if oi.entry.Source != "" {
				merged.Source = oi.entry.Source
			}
			if oi.entry.Enabled != nil {
				merged.Enabled = oi.entry.Enabled
			}
			if oi.entry.Name != "" {
				merged.Name = oi.entry.Name
			}
		}
		result = append(result, merged)
	}

	// Append overlay entries that did not match any parent agent.
	for _, oi := range overlayOrder {
		if !oi.used {
			result = append(result, oi.entry)
		}
	}

	return result
}

// IsKillSwitchActive reports whether the kill switch is engaged.
// KillSwitch is a *bool: nil falls through to parent, non-nil
// (including explicit false) is the local decision.
func (c *perRepoConfig) IsKillSwitchActive() bool {
	if c.KillSwitch != nil {
		return *c.KillSwitch
	}
	if c.parent != nil {
		return c.parent.IsKillSwitchActive()
	}
	return false
}

// AllowedResources returns the effective allowed remote resource
// prefixes. nil (key omitted) falls through to parent. An explicit
// empty slice signals deny-all with no fallthrough. A non-empty
// slice is unioned with parent resources when a parent is present;
// without a parent, the local list is returned as-is for backwards
// compatibility. Code defaults surface only through the terminal
// perRepoDefaults parent — intermediate parents may omit them.
func (c *perRepoConfig) AllowedResources() []string {
	if c.AllowedRemoteResources == nil {
		if c.parent != nil {
			return c.parent.AllowedResources()
		}
		return nil
	}
	if len(c.AllowedRemoteResources) == 0 {
		return c.AllowedRemoteResources
	}
	if c.parent == nil {
		return c.AllowedRemoteResources
	}
	// Non-empty with parent: union overlay + parent.
	seen := make(map[string]bool, len(c.AllowedRemoteResources))
	result := make([]string, len(c.AllowedRemoteResources))
	copy(result, c.AllowedRemoteResources)
	for _, r := range c.AllowedRemoteResources {
		seen[r] = true
	}
	for _, r := range c.parent.AllowedResources() {
		if !seen[r] {
			result = append(result, r)
			seen[r] = true
		}
	}
	return result
}

// IssueCreationConfig returns the cross-repo issue creation config.
// nil falls through to parent.
func (c *perRepoConfig) IssueCreationConfig() *CreateIssuesConfig {
	if c.CreateIssues != nil {
		return c.CreateIssues
	}
	if c.parent != nil {
		return c.parent.IssueCreationConfig()
	}
	return nil
}

// StatusNotifications returns the status notification configuration.
// nil falls through to parent.
func (c *perRepoConfig) StatusNotifications() *StatusNotificationConfig {
	if c.Notifications != nil {
		return c.Notifications
	}
	if c.parent != nil {
		return c.parent.StatusNotifications()
	}
	return nil
}

// ConfigVersion returns the config schema version. Empty falls
// through to parent (code default "1").
func (c *perRepoConfig) ConfigVersion() string {
	if c.Version != "" {
		return c.Version
	}
	if c.parent != nil {
		return c.parent.ConfigVersion()
	}
	return ""
}

// IsOrgMode reports that this is a per-repo configuration.
func (c *perRepoConfig) IsOrgMode() bool { return false }

// ConfigRoles returns the configured agent roles. nil (key omitted)
// falls through to parent. Non-nil (including empty) replaces the
// parent list entirely.
func (c *perRepoConfig) ConfigRoles() []string {
	if c.Roles != nil {
		return c.Roles
	}
	if c.parent != nil {
		return c.parent.ConfigRoles()
	}
	return nil
}

// ConfigRuntime returns the configured agent runtime. Empty falls
// through to parent (code default "claude").
func (c *perRepoConfig) ConfigRuntime() string {
	if c.Runtime != "" {
		return c.Runtime
	}
	if c.parent != nil {
		return c.parent.ConfigRuntime()
	}
	return ""
}

// ConfigForge returns the configured forge type (e.g. "github", "gitlab").
func (c *perRepoConfig) ConfigForge() string {
	if c.Forge != "" {
		return c.Forge
	}
	if c.parent != nil {
		return c.parent.ConfigForge()
	}
	return ""
}

// ConfigTracker returns the configured default issue tracker (e.g.
// "github", "gitlab", "jira"), used as the default for `fullsend
// issues` commands' --tracker flag when it is not set explicitly.
// Distinct from ConfigForge: a repo's hosting forge (github/gitlab)
// does not imply its issue tracker (e.g. a GitHub-hosted repo may
// track issues in Jira).
func (c *perRepoConfig) ConfigTracker() string {
	if c.Tracker != "" {
		return c.Tracker
	}
	if c.parent != nil {
		return c.parent.ConfigTracker()
	}
	return ""
}

// ConfigMintURL returns the configured token mint URL.
func (c *perRepoConfig) ConfigMintURL() string {
	if c.MintURL != "" {
		return c.MintURL
	}
	if c.parent != nil {
		return c.parent.ConfigMintURL()
	}
	return ""
}

// ConfigInferenceProvider returns the inference provider (e.g. "vertex").
func (c *perRepoConfig) ConfigInferenceProvider() string {
	if c.Inference != nil && c.Inference.Provider != "" {
		return c.Inference.Provider
	}
	if c.parent != nil {
		return c.parent.ConfigInferenceProvider()
	}
	return ""
}

// ConfigInferenceProject returns the GCP project ID for inference.
func (c *perRepoConfig) ConfigInferenceProject() string {
	if c.Inference != nil && c.Inference.Project != "" {
		return c.Inference.Project
	}
	if c.parent != nil {
		return c.parent.ConfigInferenceProject()
	}
	return ""
}

// ConfigInferenceRegion returns the GCP region for inference.
func (c *perRepoConfig) ConfigInferenceRegion() string {
	if c.Inference != nil && c.Inference.Region != "" {
		return c.Inference.Region
	}
	if c.parent != nil {
		return c.parent.ConfigInferenceRegion()
	}
	return ""
}

// ConfigInferenceWIFProvider returns the WIF provider resource name.
func (c *perRepoConfig) ConfigInferenceWIFProvider() string {
	if c.Inference != nil && c.Inference.WIFProvider != "" {
		return c.Inference.WIFProvider
	}
	if c.parent != nil {
		return c.parent.ConfigInferenceWIFProvider()
	}
	return ""
}

// --- perRepoConfig setter methods ---

// SetKillSwitch sets the kill switch state. Stores a *bool so that
// an explicit false is distinguishable from unset (nil) across layers.
func (c *perRepoConfig) SetKillSwitch(v bool) { c.KillSwitch = &v }

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

// SetMintURL sets the token mint URL.
func (c *perRepoConfig) SetMintURL(mintURL string) { c.MintURL = mintURL }

// SetInferenceProvider sets the inference provider.
func (c *perRepoConfig) SetInferenceProvider(provider string) {
	c.ensureInference().Provider = provider
}

// SetInferenceProject sets the GCP project ID for inference.
func (c *perRepoConfig) SetInferenceProject(project string) { c.ensureInference().Project = project }

// SetInferenceRegion sets the GCP region for inference.
func (c *perRepoConfig) SetInferenceRegion(region string) { c.ensureInference().Region = region }

// SetStatusNotifications sets the status notification configuration.
func (c *perRepoConfig) SetStatusNotifications(sn *StatusNotificationConfig) {
	c.Notifications = sn
}

// SetInferenceWIFProvider sets the WIF provider resource name.
func (c *perRepoConfig) SetInferenceWIFProvider(wifProvider string) {
	c.ensureInference().WIFProvider = wifProvider
}

// ensureInference lazily initializes the Inference struct.
func (c *perRepoConfig) ensureInference() *PerRepoInferenceConfig {
	if c.Inference == nil {
		c.Inference = &PerRepoInferenceConfig{}
	}
	return c.Inference
}

// --- LoadConfig / LoadConfigWriter factories ---

// LoadOpts controls how LoadConfig handles a missing config.yaml.
type LoadOpts struct {
	// MissingOK returns a default config when config.yaml is absent.
	MissingOK bool
}

// LoadConfig reads config.yaml (and config.base.yaml if present) from
// dir, returning a ConfigReader. For per-repo configs the parent chain
// is wired as overlay (config.yaml) → base (config.base.yaml) →
// code defaults. This is the preferred entry point for consumer
// packages that only need read access.
func LoadConfig(dir string, opts LoadOpts) (ConfigReader, error) {
	overlayData, haveOverlay, baseData, haveBase, err := readConfigFiles(dir)
	if err != nil {
		return nil, err
	}

	// Neither file exists.
	if !haveOverlay && !haveBase {
		if opts.MissingOK {
			return NewPerRepoConfig(nil, ""), nil
		}
		return nil, fmt.Errorf("reading config: %w",
			&os.PathError{Op: "open", Path: filepath.Join(dir, "config.yaml"), Err: os.ErrNotExist})
	}

	// Detect malformed YAML before type detection so the error message
	// names config.yaml rather than the misleading "parsing org config".
	if haveOverlay {
		var probe interface{}
		if err := yaml.Unmarshal(overlayData, &probe); err != nil {
			return nil, fmt.Errorf("parsing config.yaml: %w", err)
		}
	}

	// Org-mode overlay: base layering does not apply.
	if haveOverlay && !IsPerRepoYAML(overlayData) {
		return ParseOrgConfig(overlayData)
	}

	return loadPerRepoLayers(overlayData, haveOverlay, baseData, haveBase)
}

// LoadConfigWriter reads config.yaml (and config.base.yaml if present)
// from dir, returning a ConfigWriter. For per-repo configs the parent
// chain is wired as overlay (config.yaml) → base (config.base.yaml) →
// code defaults. Only the overlay layer is mutable; base and defaults
// are read-only via the parent pointer. This is the preferred entry
// point for consumer packages that need read-write access (e.g. CLI
// commands that modify and write-back config).
func LoadConfigWriter(dir string, opts LoadOpts) (ConfigWriter, error) {
	overlayData, haveOverlay, baseData, haveBase, err := readConfigFiles(dir)
	if err != nil {
		return nil, err
	}

	// Neither file exists.
	if !haveOverlay && !haveBase {
		if opts.MissingOK {
			return NewPerRepoConfig(nil, ""), nil
		}
		return nil, fmt.Errorf("reading config: %w",
			&os.PathError{Op: "open", Path: filepath.Join(dir, "config.yaml"), Err: os.ErrNotExist})
	}

	// Detect malformed YAML before type detection so the error message
	// names config.yaml rather than the misleading "parsing org config".
	if haveOverlay {
		var probe interface{}
		if err := yaml.Unmarshal(overlayData, &probe); err != nil {
			return nil, fmt.Errorf("parsing config.yaml: %w", err)
		}
	}

	// Org-mode overlay: base layering does not apply.
	if haveOverlay && !IsPerRepoYAML(overlayData) {
		return ParseOrgConfigWriter(overlayData)
	}

	return loadPerRepoLayers(overlayData, haveOverlay, baseData, haveBase)
}

// readConfigFiles reads config.yaml and config.base.yaml from dir.
// Returns data and existence flags for each file. Genuine I/O errors
// (not "file not found") are returned immediately.
func readConfigFiles(dir string) (overlayData []byte, haveOverlay bool, baseData []byte, haveBase bool, err error) {
	overlayData, overlayErr := os.ReadFile(filepath.Join(dir, "config.yaml"))
	baseData, baseErr := os.ReadFile(filepath.Join(dir, "config.base.yaml"))

	if overlayErr != nil && !os.IsNotExist(overlayErr) {
		return nil, false, nil, false, fmt.Errorf("reading config: %w", overlayErr)
	}
	if baseErr != nil && !os.IsNotExist(baseErr) {
		return nil, false, nil, false, fmt.Errorf("reading base config: %w", baseErr)
	}
	return overlayData, overlayErr == nil, baseData, baseErr == nil, nil
}

// loadPerRepoLayers parses per-repo config layers and wires the parent
// chain: overlay → base → code defaults. When haveOverlay is false an
// empty overlay is created so writes target config.yaml. The returned
// *perRepoConfig satisfies both ConfigReader and ConfigWriter.
func loadPerRepoLayers(overlayData []byte, haveOverlay bool, baseData []byte, haveBase bool) (*perRepoConfig, error) {
	// Parse base layer; parent = code defaults.
	var baseReader PerRepoConfigReader = &perRepoDefaults{}
	if haveBase {
		var base perRepoConfig
		if err := yaml.Unmarshal(baseData, &base); err != nil {
			return nil, fmt.Errorf("parsing base config: %w", err)
		}
		base.parent = &perRepoDefaults{}
		baseReader = &base
	}

	// Parse or create overlay layer; parent = base (or defaults).
	var overlay perRepoConfig
	if haveOverlay {
		if err := yaml.Unmarshal(overlayData, &overlay); err != nil {
			return nil, fmt.Errorf("parsing per-repo config: %w", err)
		}
	}
	overlay.parent = baseReader
	return &overlay, nil
}

// IsPerRepoYAML probes raw YAML for structural markers that distinguish
// perRepoConfig from orgConfig. orgConfig has org-only top-level keys
// (dispatch, repos, defaults); perRepoConfig never does. The "inference"
// key is shared: orgConfig uses it for the provider name, perRepoConfig
// uses it for nested inference backend settings. Org configs always
// contain dispatch/repos/defaults (non-omitempty), so the remaining
// markers are sufficient for detection.
func IsPerRepoYAML(data []byte) bool {
	var probe map[string]interface{}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	for _, key := range []string{"dispatch", "repos", "defaults"} {
		if _, ok := probe[key]; ok {
			return false
		}
	}
	return true
}
