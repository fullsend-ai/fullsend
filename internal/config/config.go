package config

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/urlutil"
	"gopkg.in/yaml.v3"
)

// validConfigAgentName requires an alphanumeric first character, stricter
// than harness.validAgentName which also allows leading underscores/hyphens.
// Config names may be used as YAML keys or filesystem identifiers downstream.
var validConfigAgentName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// AgentEntry represents a registered agent source in config.
// It supports both string shorthand (just the source URL/path) and
// object form (with an explicit name override).
//
// Enabled controls whether the agent participates in the merged agent
// set. When nil (omitted) the agent defaults to enabled. When
// explicitly set to false the agent is suppressed — this allows
// disabling built-in scaffold agents without removing their role.
// A suppression-only entry (Enabled=false, no Source) is valid.
type AgentEntry struct {
	Name    string `yaml:"name,omitempty"`
	Source  string `yaml:"source"`
	Enabled *bool  `yaml:"enabled,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler so that a plain string
// is treated as a source-only entry, while a mapping decodes normally.
// Old-format entries (role/name/slug identity tuples from pre-ADR-0058
// config) are detected and rejected with a clear error message.
func (a *AgentEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		a.Source = value.Value
		return nil
	}
	if value.Kind == yaml.MappingNode {
		// Detect old-format entries (have "role" key but no "source" key).
		hasRole := false
		hasSource := false
		for i := 0; i < len(value.Content)-1; i += 2 {
			if value.Content[i].Value == "role" {
				hasRole = true
			}
			if value.Content[i].Value == "source" {
				hasSource = true
			}
		}
		if hasRole && !hasSource {
			return fmt.Errorf("agents entry uses legacy role/name/slug format (removed by ADR 0045 Phase 4); use source URL or path instead")
		}

		type plain AgentEntry
		return value.Decode((*plain)(a))
	}
	return fmt.Errorf("agents entry must be a string or mapping, got %v", value.Kind)
}

// IsEnabled returns whether the agent entry is enabled.
// A nil Enabled pointer (field omitted) defaults to true.
func (a AgentEntry) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// DerivedName returns the explicit Name if set, otherwise derives one
// from the Source filename (e.g. "triage.yaml" → "triage").
func (a AgentEntry) DerivedName() string {
	if a.Name != "" {
		return a.Name
	}
	src := a.Source
	// Strip fragment (e.g. #sha256=...) before extracting filename.
	if idx := strings.LastIndex(src, "#"); idx >= 0 {
		src = src[:idx]
	}
	base := path.Base(src)
	return strings.TrimSuffix(base, path.Ext(base))
}

const (
	// DefaultUpstreamRepo is the canonical fullsend repository for layered workflow calls.
	DefaultUpstreamRepo = "fullsend-ai/fullsend"
	// DefaultUpstreamRef is the default tag for layered upstream workflow calls.
	DefaultUpstreamRef = "v0"
	// DefaultGHRunner is the default GitHub Actions runner image for scaffold workflows.
	DefaultGHRunner = "ubuntu-24.04"
)

// DispatchConfig configures how agent work is dispatched.
type DispatchConfig struct {
	Platform string `yaml:"platform"`
	Mode     string `yaml:"mode,omitempty"`     // "oidc-mint"
	MintURL  string `yaml:"mint_url,omitempty"` // informational, set when mode=oidc-mint
}

// InferenceConfig configures the inference provider used by agents
// (org-mode config).
type InferenceConfig struct {
	Provider string `yaml:"provider"`
}

// PerRepoInferenceConfig groups inference backend settings for
// per-repo configs. The Provider field identifies the inference
// backend (currently only "vertex"); Project, Region, and WIFProvider
// hold provider-specific connection details.
type PerRepoInferenceConfig struct {
	Provider    string `yaml:"provider,omitempty"`
	Project     string `yaml:"project,omitempty"`
	Region      string `yaml:"region,omitempty"`
	WIFProvider string `yaml:"wif_provider,omitempty"`
}

// StatusNotificationConfig controls status comments posted on issues/PRs
// when agents start and complete.
type StatusNotificationConfig struct {
	Comment CommentNotificationConfig `yaml:"comment,omitempty"`
}

// CommentNotificationConfig controls start/completion comments.
// Valid start values: "enabled" (default), "disabled".
// Valid completion values: "enabled" (default), "on_failure", "disabled".
type CommentNotificationConfig struct {
	Start      string `yaml:"start,omitempty"`
	Completion string `yaml:"completion,omitempty"`
}

// RepoDefaults holds default settings applied to all repos.
type RepoDefaults struct {
	Roles                    []string                  `yaml:"roles"`
	Runtime                  string                    `yaml:"runtime,omitempty"`
	MaxImplementationRetries int                       `yaml:"max_implementation_retries"`
	AutoMerge                bool                      `yaml:"auto_merge"`
	StatusNotifications      *StatusNotificationConfig `yaml:"status_notifications,omitempty"`
}

// RepoConfig holds per-repo configuration.
// StatusNotifications is intentionally absent here — notification style is an
// org-wide UX decision (consistent appearance across all repos), unlike roles
// and auto_merge which are operationally per-repo.
type RepoConfig struct {
	Roles   []string `yaml:"roles,omitempty"`
	Enabled bool     `yaml:"enabled"`
}

// AllowTargets defines which orgs and repos agents may create issues in.
type AllowTargets struct {
	Orgs  []string `yaml:"orgs,omitempty"`
	Repos []string `yaml:"repos,omitempty"`
}

// CreateIssuesConfig controls cross-repo issue creation by agents.
type CreateIssuesConfig struct {
	AllowTargets AllowTargets `yaml:"allow_targets"`
}

// orgConfig is the top-level configuration for a fullsend organization.
// Consumer packages should use the OrgConfigReader or OrgConfigWriter
// interfaces rather than referencing this type directly.
type orgConfig struct {
	Version                string                `yaml:"version"`
	KillSwitch             bool                  `yaml:"kill_switch,omitempty"`
	Dispatch               DispatchConfig        `yaml:"dispatch"`
	Inference              InferenceConfig       `yaml:"inference,omitempty"`
	Defaults               RepoDefaults          `yaml:"defaults"`
	Repos                  map[string]RepoConfig `yaml:"repos"`
	Agents                 []AgentEntry          `yaml:"agents,omitempty"`
	AllowedRemoteResources []string              `yaml:"allowed_remote_resources,omitempty"`
	CreateIssues           *CreateIssuesConfig   `yaml:"create_issues,omitempty"`
}

// ValidRoles returns the set of agent roles accepted in `.fullsend`
// `roles:` / `defaults.roles` config. This is intentionally narrower than
// mintcore's canonical roles: mint-only dogfood roles (e.g. scribe) can be
// registered with `fullsend mint add-role` before scaffold/workflow wiring
// lands, and must not silently pass config validation.
func ValidRoles() []string {
	return []string{"fullsend", "triage", "coder", "review", "fix", "retro", "prioritize", "e2e"}
}

// ValidProviders returns the set of recognized inference providers.
func ValidProviders() []string {
	return []string{"vertex"}
}

// ValidRuntimes returns the set of recognized agent runtimes. "pi" is
// opt-in per org/repo (#6464); "dummy" is for behaviour test orgs only.
func ValidRuntimes() []string {
	return []string{"claude", "pi", "dummy"}
}

// DefaultAgentRoles returns the standard set of agent roles installed
// when no custom roles are specified. The fix stage reuses the coder
// app (role: coder) so it does not need a separate app or PEM.
func DefaultAgentRoles() []string {
	return []string{"fullsend", "triage", "coder", "review", "retro", "prioritize"}
}

// PerRepoDefaultRoles returns agent roles for per-repo installation.
// The "fullsend" dispatch role is excluded because per-repo mode uses
// the target repo's shim workflow for dispatch instead of a separate app.
func PerRepoDefaultRoles() []string {
	return []string{"triage", "coder", "review", "fix", "retro", "prioritize"}
}

// DefaultAllowedRemoteResources returns the standard allowlist prefixes
// for base composition and agent registration URLs.
func DefaultAllowedRemoteResources() []string {
	return []string{
		"https://raw.githubusercontent.com/fullsend-ai/fullsend/",
		"https://raw.githubusercontent.com/fullsend-ai/agents/",
	}
}

// EnsureDefaultAllowedRemoteResources returns a new slice with default
// allowed-remote-resources prefixes merged into the provided list.
// Nil input (field omitted from YAML) returns defaults alone.
// An explicit empty slice (allowed_remote_resources: []) is returned
// unchanged to preserve deny-all semantics. Non-empty input gets any
// missing defaults appended (preserving the caller's original ordering).
func EnsureDefaultAllowedRemoteResources(existing []string) []string {
	if existing == nil {
		return DefaultAllowedRemoteResources()
	}
	if len(existing) == 0 {
		return existing
	}
	defaults := DefaultAllowedRemoteResources()
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		seen[e] = true
	}
	result := make([]string, len(existing))
	copy(result, existing)
	for _, d := range defaults {
		if !seen[d] {
			result = append(result, d)
		}
	}
	return result
}

// NewOrgConfig creates a new orgConfig with sensible defaults.
// The returned OrgConfigWriter provides full read-write access.
func NewOrgConfig(allRepos, enabledRepos, roles []string, inferenceProvider, org string) OrgConfigWriter {
	repos := make(map[string]RepoConfig, len(allRepos))
	for _, r := range allRepos {
		repos[r] = RepoConfig{
			Enabled: slices.Contains(enabledRepos, r),
		}
	}

	cfg := &orgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    roles,
			Runtime:                  "claude",
			MaxImplementationRetries: 2,
			AutoMerge:                false,
		},
		Repos:                  repos,
		AllowedRemoteResources: DefaultAllowedRemoteResources(),
	}
	if inferenceProvider != "" {
		cfg.Inference = InferenceConfig{Provider: inferenceProvider}
	}
	if org != "" {
		cfg.CreateIssues = &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Orgs:  []string{org},
				Repos: []string{"fullsend-ai/fullsend"},
			},
		}
	}
	return cfg
}

// ParseOrgConfig parses YAML bytes into an OrgConfigReader.
func ParseOrgConfig(data []byte) (OrgConfigReader, error) {
	var cfg orgConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing org config: %w", err)
	}
	return &cfg, nil
}

// ParseOrgConfigWriter parses YAML bytes into an OrgConfigWriter
// for callers that need to modify the config after parsing.
func ParseOrgConfigWriter(data []byte) (OrgConfigWriter, error) {
	var cfg orgConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing org config: %w", err)
	}
	return &cfg, nil
}

const configHeader = `# fullsend organization configuration
# https://github.com/fullsend-ai/fullsend
#
# This file is managed by fullsend. Manual edits may be overwritten.
`

// Marshal serializes the orgConfig to YAML with a descriptive header comment.
func (c *orgConfig) Marshal() ([]byte, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshaling org config: %w", err)
	}
	return []byte(configHeader + string(body)), nil
}

// Validate checks the orgConfig for structural correctness.
func (c *orgConfig) Validate() error {
	if c.Version != "1" {
		return fmt.Errorf("unsupported version %q: must be \"1\"", c.Version)
	}
	if c.Dispatch.Platform != "github-actions" {
		return fmt.Errorf("unsupported platform %q: must be \"github-actions\"", c.Dispatch.Platform)
	}
	if c.Dispatch.Mode != "" && c.Dispatch.Mode != "oidc-mint" {
		return fmt.Errorf("unsupported dispatch mode %q: must be \"oidc-mint\"", c.Dispatch.Mode)
	}
	if c.Defaults.MaxImplementationRetries < 0 {
		return fmt.Errorf("max_implementation_retries must be >= 0, got %d", c.Defaults.MaxImplementationRetries)
	}
	valid := ValidRoles()
	seen := make(map[string]bool, len(c.Defaults.Roles))
	for _, role := range c.Defaults.Roles {
		if !slices.Contains(valid, role) {
			return fmt.Errorf("invalid role %q: must be one of %s", role, strings.Join(valid, ", "))
		}
		if seen[role] {
			return fmt.Errorf("duplicate role %q in defaults.roles", role)
		}
		seen[role] = true
	}
	if c.Inference.Provider != "" {
		validProviders := ValidProviders()
		if !slices.Contains(validProviders, c.Inference.Provider) {
			return fmt.Errorf("invalid inference provider %q: must be one of %s", c.Inference.Provider, strings.Join(validProviders, ", "))
		}
	}
	if rt := c.Defaults.Runtime; rt != "" {
		validRuntimes := ValidRuntimes()
		if !slices.Contains(validRuntimes, rt) {
			return fmt.Errorf("invalid runtime %q: must be one of %s", rt, strings.Join(validRuntimes, ", "))
		}
	}
	if err := validateStatusNotifications(c.Defaults.StatusNotifications); err != nil {
		return err
	}
	if err := ValidateAgentEntries(c.Agents, c.AllowedRemoteResources); err != nil {
		return err
	}
	if err := validateCreateIssues(c.CreateIssues); err != nil {
		return err
	}
	return nil
}

// ValidateAgentEntries checks agent entries for structural correctness.
// Uses urlutil.IsURL, urlutil.ParseIntegrityHash, and
// urlutil.MatchingAllowedPrefixInList for consistency with runtime
// resolution (case-insensitive scheme, percent-decoding, dot-segment
// cleaning).
func ValidateAgentEntries(agents []AgentEntry, allowlist []string) error {
	// seen tracks agent names for duplicate detection. Each state
	// (enabled/disabled) is tracked independently so that exactly one
	// disable-then-enable or enable-then-disable pair is accepted while
	// three-or-more entries with the same name are rejected.
	type seenState struct {
		seenEnabled  bool
		seenDisabled bool
	}
	seen := make(map[string]seenState, len(agents))
	for i, entry := range agents {
		// A suppression-only entry (enabled: false, no source) is valid —
		// it exists solely to disable a scaffold default by name.
		if entry.Source == "" && !entry.IsEnabled() {
			if entry.Name == "" {
				return fmt.Errorf("agents[%d]: disabled agent entry with no source must have an explicit name", i)
			}
			if !validConfigAgentName.MatchString(entry.Name) {
				return fmt.Errorf("agents[%d] (%s): name is invalid, must start with alphanumeric and contain only [a-zA-Z0-9_-]", i, entry.Name)
			}
			lowerName := strings.ToLower(entry.Name)
			if prev, exists := seen[lowerName]; exists {
				if prev.seenDisabled {
					return fmt.Errorf("agents[%d] (%s): duplicate agent name (case-insensitive)", i, entry.Name)
				}
			}
			prev := seen[lowerName]
			prev.seenDisabled = true
			seen[lowerName] = prev
			continue
		}
		// Disabled entries with a source must also have an explicit name so
		// dispatch workflows can match by name via yq without deriving it.
		if !entry.IsEnabled() && entry.Name == "" {
			return fmt.Errorf("agents[%d]: disabled agent entry must have an explicit name", i)
		}
		if entry.Source == "" {
			return fmt.Errorf("agents[%d]: enabled agent entry must have a source", i)
		}

		name := entry.DerivedName()
		if !validConfigAgentName.MatchString(name) {
			return fmt.Errorf("agents[%d] (%s): derived name is invalid, must start with alphanumeric and contain only [a-zA-Z0-9_-] (source: %q)", i, name, entry.Source)
		}
		lowerName := strings.ToLower(name)
		currentDisabled := !entry.IsEnabled()
		if prev, exists := seen[lowerName]; exists {
			if currentDisabled && prev.seenDisabled {
				return fmt.Errorf("agents[%d] (%s): duplicate agent name (case-insensitive)", i, name)
			}
			if !currentDisabled && prev.seenEnabled {
				return fmt.Errorf("agents[%d] (%s): duplicate agent name (case-insensitive)", i, name)
			}
		}
		prev := seen[lowerName]
		if currentDisabled {
			prev.seenDisabled = true
		} else {
			prev.seenEnabled = true
		}
		seen[lowerName] = prev

		if urlutil.IsURL(entry.Source) {
			cleanURL, _, hasHash := urlutil.ParseIntegrityHash(entry.Source)
			if !hasHash {
				return fmt.Errorf("agents[%d] (%s): URL source must include a valid #sha256=<64-hex-char> integrity fragment", i, name)
			}
			if urlutil.MatchingAllowedPrefixInList(cleanURL, allowlist) == "" {
				return fmt.Errorf("agents[%d] (%s): URL %q is not covered by allowed_remote_resources", i, name, cleanURL)
			}
		} else if strings.HasPrefix(strings.ToLower(entry.Source), "http://") {
			return fmt.Errorf("agents[%d] (%s): URL scheme must be https, got http", i, name)
		} else {
			if strings.Contains(entry.Source, "://") {
				return fmt.Errorf("agents[%d] (%s): unsupported URL scheme, only https is allowed", i, name)
			}
			if strings.HasPrefix(entry.Source, "/") {
				return fmt.Errorf("agents[%d] (%s): absolute paths are not allowed", i, name)
			}
			if strings.ContainsRune(entry.Source, '\\') {
				return fmt.Errorf("agents[%d] (%s): local path must not contain backslashes", i, name)
			}
			for _, seg := range strings.Split(entry.Source, "/") {
				if seg == ".." {
					return fmt.Errorf("agents[%d] (%s): local path must not contain path traversal (..)", i, name)
				}
			}
		}
	}
	return nil
}

func validateStatusNotifications(cfg *StatusNotificationConfig) error {
	if cfg == nil {
		return nil
	}
	validStartValues := []string{"", "enabled", "disabled"}
	if !slices.Contains(validStartValues, cfg.Comment.Start) {
		return fmt.Errorf("invalid status_notifications.comment.start %q: must be \"enabled\" or \"disabled\"", cfg.Comment.Start)
	}
	validCompletionValues := []string{"", "enabled", "disabled", "on_failure"}
	if !slices.Contains(validCompletionValues, cfg.Comment.Completion) {
		return fmt.Errorf("invalid status_notifications.comment.completion %q: must be \"enabled\", \"on_failure\", or \"disabled\"", cfg.Comment.Completion)
	}
	return nil
}

// EnabledRepos returns a sorted list of repo names where Enabled is true.
func (c *orgConfig) EnabledRepos() []string {
	var enabled []string
	for name, rc := range c.Repos {
		if rc.Enabled {
			enabled = append(enabled, name)
		}
	}
	sort.Strings(enabled)
	return enabled
}

// DisabledRepos returns a sorted list of repo names where Enabled is false.
func (c *orgConfig) DisabledRepos() []string {
	var disabled []string
	for name, rc := range c.Repos {
		if !rc.Enabled {
			disabled = append(disabled, name)
		}
	}
	sort.Strings(disabled)
	return disabled
}

// DefaultRoles returns the default roles configured for the organization.
func (c *orgConfig) DefaultRoles() []string {
	return c.Defaults.Roles
}

// perRepoConfig holds configuration for per-repo installation mode.
// Stored in .fullsend/config.yaml within the target repository.
// Consumer packages should use the PerRepoConfigReader or ConfigWriter
// interfaces rather than referencing this type directly.
//
// The parent field implements a fallback chain per ADR 0069 Decision 2:
// accessors check the local struct first, then fall through to parent
// when the local value is unset. The terminal parent is perRepoDefaults,
// which returns compiled-in code defaults.
type perRepoConfig struct {
	// omitempty so unset version is not marshaled (unlike orgConfig,
	// where version is always required). This allows the fallback
	// chain to inherit version from the parent layer.
	Version string `yaml:"version,omitempty"`
	Forge   string `yaml:"forge,omitempty"`
	// Tracker is the default issue tracker backend (github, gitlab, or
	// jira) for `fullsend issues` commands' --tracker flag. Distinct
	// from Forge, which is the repo's hosting platform — a repo can be
	// hosted on GitHub but track issues in Jira.
	Tracker    string       `yaml:"tracker,omitempty"`
	KillSwitch *bool        `yaml:"kill_switch,omitempty"`
	Runtime    string       `yaml:"runtime,omitempty"`
	Roles      []string     `yaml:"roles,omitempty"`
	Agents     []AgentEntry `yaml:"agents,omitempty"`
	// AllowedRemoteResources holds the locally-set allowed remote
	// resource prefixes. MarshalYAML preserves the nil-vs-empty
	// distinction: nil (unset) is omitted, empty (deny-all) is
	// marshaled as `allowed_remote_resources: []`.
	AllowedRemoteResources []string            `yaml:"allowed_remote_resources,omitempty"`
	CreateIssues           *CreateIssuesConfig `yaml:"create_issues,omitempty"`
	// Notifications backs the StatusNotifications() accessor. Named
	// distinctly from the method (unlike CreateIssues/IssueCreationConfig)
	// because "StatusNotifications" is the established accessor name
	// shared with orgConfig via ConfigReader, and Go forbids a field and
	// method sharing a name on the same type.
	Notifications *StatusNotificationConfig `yaml:"status_notifications,omitempty"`

	// Mint URL for token minting (ADR 0069 Decision 1).
	MintURL string `yaml:"mint_url,omitempty"`

	// Inference groups the inference backend settings under a single
	// top-level key. The nested struct allows future provider types
	// beyond "vertex" without flat-key proliferation.
	Inference *PerRepoInferenceConfig `yaml:"inference,omitempty"`

	// parent is the next layer in the fallback chain. Getters consult
	// parent when the local field is unset. Excluded from YAML
	// serialization so Marshal emits only locally-set values.
	parent PerRepoConfigReader `yaml:"-"`
}

const perRepoConfigHeader = `# fullsend per-repo configuration
# https://github.com/fullsend-ai/fullsend
#
# This file configures fullsend for per-repo installation mode.
# See ADR 0033 for details.
`

// NewPerRepoConfig creates a new perRepoConfig with the given roles.
// The returned ConfigWriter provides read-write access to shared config
// fields; use PerRepoConfigReader type assertion for per-repo-specific
// methods.
func NewPerRepoConfig(roles []string, targetRepo string) PerRepoConfigWriter {
	if roles == nil {
		roles = DefaultAgentRoles()
	}
	cfg := &perRepoConfig{
		Version:                "1",
		Roles:                  roles,
		AllowedRemoteResources: DefaultAllowedRemoteResources(),
		parent:                 &perRepoDefaults{},
	}
	if targetRepo != "" {
		cfg.CreateIssues = &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Repos: []string{targetRepo, "fullsend-ai/fullsend"},
			},
		}
	}
	return cfg
}

// NewEmptyPerRepoOverlay creates an empty per-repo config suitable for
// use as a stub overlay when a preset base layer is provided. No roles,
// agents, or version are populated; the overlay inherits everything from
// the base layer via the parent fallback chain.
func NewEmptyPerRepoOverlay() PerRepoConfigWriter {
	return &perRepoConfig{
		parent: &perRepoDefaults{},
	}
}

// NewPerRepoConfigFromOrg creates a per-repo config by mapping portable
// fields from an org config. Per-repo role overrides (repos.<name>.roles)
// take precedence over defaults.roles. Non-portable fields
// (max_implementation_retries, auto_merge) are not carried over —
// callers should warn separately.
func NewPerRepoConfigFromOrg(orgCfg OrgConfigReader, repoName, targetRepo string) PerRepoConfigWriter {
	// Determine roles: per-repo overrides take precedence over defaults.
	roles := orgCfg.OrgRepoDefaults().Roles
	if repoMap := orgCfg.RepoMap(); repoMap != nil {
		if rc, ok := repoMap[repoName]; ok && len(rc.Roles) > 0 {
			roles = rc.Roles
		}
	}
	if roles == nil {
		roles = PerRepoDefaultRoles()
	} else {
		rolesCopy := make([]string, len(roles))
		copy(rolesCopy, roles)
		roles = rolesCopy
	}

	cfg := &perRepoConfig{
		Version: "1",
		Roles:   roles,
		parent:  &perRepoDefaults{},
	}

	// Agents: deep-copy org agent entries (AgentEntry.Enabled is *bool).
	if agents := orgCfg.AgentEntries(); len(agents) > 0 {
		copied := make([]AgentEntry, len(agents))
		copy(copied, agents)
		for i, a := range copied {
			if a.Enabled != nil {
				e := *a.Enabled
				copied[i].Enabled = &e
			}
		}
		cfg.Agents = copied
	}

	// AllowedRemoteResources: copy from org config with defaults ensured.
	if arr := orgCfg.AllowedResources(); len(arr) > 0 {
		cfg.AllowedRemoteResources = EnsureDefaultAllowedRemoteResources(arr)
	} else {
		cfg.AllowedRemoteResources = DefaultAllowedRemoteResources()
	}

	// CreateIssues: deep-copy from org config to avoid pointer aliasing.
	if ci := orgCfg.IssueCreationConfig(); ci != nil {
		ciCopy := *ci
		ciCopy.AllowTargets = AllowTargets{
			Orgs:  append([]string(nil), ci.AllowTargets.Orgs...),
			Repos: append([]string(nil), ci.AllowTargets.Repos...),
		}
		cfg.CreateIssues = &ciCopy
	} else if targetRepo != "" {
		cfg.CreateIssues = &CreateIssuesConfig{
			AllowTargets: AllowTargets{
				Repos: []string{targetRepo, "fullsend-ai/fullsend"},
			},
		}
	}

	// KillSwitch: only set when active (false is the default).
	if orgCfg.IsKillSwitchActive() {
		ks := true
		cfg.KillSwitch = &ks
	}

	// Runtime: copy when explicitly set.
	if rt := orgCfg.OrgRepoDefaults().Runtime; rt != "" {
		cfg.Runtime = rt
	}

	// StatusNotifications: deep-copy from org config to avoid pointer aliasing.
	if sn := orgCfg.StatusNotifications(); sn != nil {
		snCopy := *sn
		cfg.Notifications = &snCopy
	}

	return cfg
}

// ParsePerRepoConfig parses YAML bytes into a PerRepoConfigReader.
func ParsePerRepoConfig(data []byte) (PerRepoConfigReader, error) {
	var cfg perRepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing per-repo config: %w", err)
	}
	cfg.parent = &perRepoDefaults{}
	return &cfg, nil
}

// ParsePerRepoConfigWriter parses YAML bytes into a ConfigWriter for
// callers that need to modify the config after parsing.
func ParsePerRepoConfigWriter(data []byte) (ConfigWriter, error) {
	var cfg perRepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing per-repo config: %w", err)
	}
	cfg.parent = &perRepoDefaults{}
	return &cfg, nil
}

// Marshal serializes the PerRepoConfig to YAML with a descriptive header.
func (c *perRepoConfig) Marshal() ([]byte, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshaling per-repo config: %w", err)
	}
	return []byte(perRepoConfigHeader + string(body)), nil
}

// perRepoConfigMarshal is a shadow struct used by MarshalYAML to
// preserve the nil-vs-empty distinction for slice fields where nil
// (unset, inherit parent) and empty (explicitly no values) carry
// different semantics.
//
// With the plain []string + omitempty tag, yaml.v3 omits both nil
// and empty slices. Using *[]string means a nil pointer (unset)
// is omitted while a non-nil pointer to an empty slice is marshaled
// as an empty YAML sequence (e.g. `roles: []`,
// `allowed_remote_resources: []`).
type perRepoConfigMarshal struct {
	Version                string                    `yaml:"version,omitempty"`
	Forge                  string                    `yaml:"forge,omitempty"`
	Tracker                string                    `yaml:"tracker,omitempty"`
	KillSwitch             *bool                     `yaml:"kill_switch,omitempty"`
	Runtime                string                    `yaml:"runtime,omitempty"`
	Roles                  *[]string                 `yaml:"roles,omitempty"`
	Agents                 []AgentEntry              `yaml:"agents,omitempty"`
	AllowedRemoteResources *[]string                 `yaml:"allowed_remote_resources,omitempty"`
	CreateIssues           *CreateIssuesConfig       `yaml:"create_issues,omitempty"`
	StatusNotifications    *StatusNotificationConfig `yaml:"status_notifications,omitempty"`
	MintURL                string                    `yaml:"mint_url,omitempty"`
	Inference              *PerRepoInferenceConfig   `yaml:"inference,omitempty"`
}

// MarshalYAML implements yaml.Marshaler to preserve the nil-vs-empty
// distinction for Roles and AllowedRemoteResources through YAML
// roundtrips. nil (unset) is omitted so the field inherits from
// parent; an explicit empty slice is marshaled as an empty YAML
// sequence (e.g. `roles: []`, `allowed_remote_resources: []`).
func (c *perRepoConfig) MarshalYAML() (interface{}, error) {
	h := perRepoConfigMarshal{
		Version:             c.Version,
		Forge:               c.Forge,
		Tracker:             c.Tracker,
		KillSwitch:          c.KillSwitch,
		Runtime:             c.Runtime,
		Agents:              c.Agents,
		CreateIssues:        c.CreateIssues,
		StatusNotifications: c.Notifications,
		MintURL:             c.MintURL,
	}
	// Only emit inference block when at least one field is set locally.
	if c.Inference != nil && *c.Inference != (PerRepoInferenceConfig{}) {
		h.Inference = c.Inference
	}
	if c.Roles != nil {
		h.Roles = &c.Roles
	}
	if c.AllowedRemoteResources != nil {
		h.AllowedRemoteResources = &c.AllowedRemoteResources
	}
	return &h, nil
}

// Validate checks the PerRepoConfig for structural correctness.
// Locally-set fields are validated; resolved values (e.g.,
// AllowedResources) are used where validation requires the full
// effective config.
func (c *perRepoConfig) Validate() error {
	// Version: empty means "inherit from parent"; non-empty must be "1".
	if c.Version != "" && c.Version != "1" {
		return fmt.Errorf("unsupported version %q: must be \"1\"", c.Version)
	}
	// Roles: nil means "inherit from parent"; non-nil (including empty)
	// is locally set and validated.
	if c.Roles != nil {
		valid := ValidRoles()
		seen := make(map[string]bool, len(c.Roles))
		for _, role := range c.Roles {
			if !slices.Contains(valid, role) {
				return fmt.Errorf("invalid role %q: must be one of %s", role, strings.Join(valid, ", "))
			}
			if seen[role] {
				return fmt.Errorf("duplicate role %q in roles", role)
			}
			seen[role] = true
		}
	}
	// Agents are validated against the resolved allowlist (including
	// parent resources) so that URL agents covered by a parent or
	// default prefix pass validation.
	if err := ValidateAgentEntries(c.Agents, c.AllowedResources()); err != nil {
		return err
	}
	if err := validateCreateIssues(c.CreateIssues); err != nil {
		return err
	}
	if rt := c.Runtime; rt != "" {
		validRuntimes := ValidRuntimes()
		if !slices.Contains(validRuntimes, rt) {
			return fmt.Errorf("invalid runtime %q: must be one of %s", rt, strings.Join(validRuntimes, ", "))
		}
	}
	if err := validateStatusNotifications(c.Notifications); err != nil {
		return err
	}
	if c.Inference != nil && c.Inference.Provider != "" {
		validProviders := ValidProviders()
		if !slices.Contains(validProviders, c.Inference.Provider) {
			return fmt.Errorf("invalid inference provider %q: must be one of %s", c.Inference.Provider, strings.Join(validProviders, ", "))
		}
	}
	return nil
}

func validateCreateIssues(cfg *CreateIssuesConfig) error {
	if cfg == nil {
		return nil
	}
	for _, org := range cfg.AllowTargets.Orgs {
		if org == "" {
			return fmt.Errorf("create_issues: empty org in allow_targets.orgs")
		}
	}
	for _, repo := range cfg.AllowTargets.Repos {
		parts := strings.SplitN(repo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("create_issues: repo %q in allow_targets.repos must contain owner/name", repo)
		}
	}
	return nil
}
