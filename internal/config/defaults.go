package config

// Operational code defaults for per-repo configs. These are the
// terminal values in the overlay → base → code defaults chain
// (ADR 0069 Decision 2). Callers that need the canonical default
// value without constructing a full config can reference these
// constants directly.
const (
	// DefaultPerRepoMintURL is the hosted public mint used when no
	// mint_url is configured in config.yaml or config.base.yaml.
	DefaultPerRepoMintURL = "https://mint.fullsend.sh"

	// DefaultPerRepoInferenceProvider is the default inference backend.
	DefaultPerRepoInferenceProvider = "vertex"

	// DefaultPerRepoInferenceRegion is the default GCP region for
	// inference requests.
	DefaultPerRepoInferenceRegion = "global"
)

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

// ConfigTracker returns the default issue tracker (empty — no default).
func (d *perRepoDefaults) ConfigTracker() string { return "" }

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

// StatusNotifications returns nil — no status notifications by default.
func (d *perRepoDefaults) StatusNotifications() *StatusNotificationConfig { return nil }

// IsOrgMode returns false — per-repo configs are never org mode.
func (d *perRepoDefaults) IsOrgMode() bool { return false }

// ConfigMintURL returns the default mint URL (hosted public mint).
func (d *perRepoDefaults) ConfigMintURL() string { return DefaultPerRepoMintURL }

// ConfigInferenceProvider returns the default inference provider.
func (d *perRepoDefaults) ConfigInferenceProvider() string { return DefaultPerRepoInferenceProvider }

// ConfigInferenceProject returns the default inference project (empty —
// must be provided by the installer or existing secret).
func (d *perRepoDefaults) ConfigInferenceProject() string { return "" }

// ConfigInferenceRegion returns the default inference region.
func (d *perRepoDefaults) ConfigInferenceRegion() string { return DefaultPerRepoInferenceRegion }

// ConfigInferenceWIFProvider returns the default WIF provider (empty —
// must be provided by the installer or existing secret).
func (d *perRepoDefaults) ConfigInferenceWIFProvider() string { return "" }

// ConfigInferenceOpenAI returns the default OpenAI WIF identifiers (none —
// set by `fullsend github setup --openai-*` or the FULLSEND_OPENAI_*
// runner variables).
func (d *perRepoDefaults) ConfigInferenceOpenAI() OpenAIWIFConfig { return OpenAIWIFConfig{} }

// ConfigModelAliases returns the default model aliases (none — fleet
// defaults are compiled into the runtimes).
func (d *perRepoDefaults) ConfigModelAliases() map[string]string { return nil }
