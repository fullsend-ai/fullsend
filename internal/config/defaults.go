package config

// perRepoDefaults implements PerRepoConfigReader with compiled-in
// code defaults. It serves as the terminal node in the parent
// fallback chain (ADR 0069 Decision 2).
//
// Lookup order for all accessors: overlay -> base -> code defaults.
// perRepoDefaults is the "code defaults" layer.
type perRepoDefaults struct{}

// Compile-time assertion that perRepoDefaults satisfies PerRepoConfigReader.
var _ PerRepoConfigReader = (*perRepoDefaults)(nil)

// ConfigVersion returns the default schema version.
func (d *perRepoDefaults) ConfigVersion() string { return "1" }

// ConfigRuntime returns the default runtime.
func (d *perRepoDefaults) ConfigRuntime() string { return "claude" }

// ConfigForge returns the default forge (empty — auto-detected).
func (d *perRepoDefaults) ConfigForge() string { return "" }

// IsKillSwitchActive returns the default kill switch state (off).
func (d *perRepoDefaults) IsKillSwitchActive() bool { return false }

// ConfigRoles returns the default agent roles.
func (d *perRepoDefaults) ConfigRoles() []string { return PerRepoDefaultRoles() }

// AgentEntries returns nil — no agents are configured by default.
func (d *perRepoDefaults) AgentEntries() []AgentEntry { return nil }

// AllowedResources returns the default allowed remote resource prefixes.
func (d *perRepoDefaults) AllowedResources() []string { return DefaultAllowedRemoteResources() }

// IssueCreationConfig returns nil — no issue creation config by default.
func (d *perRepoDefaults) IssueCreationConfig() *CreateIssuesConfig { return nil }

// IsOrgMode returns false — per-repo configs are never org mode.
func (d *perRepoDefaults) IsOrgMode() bool { return false }
