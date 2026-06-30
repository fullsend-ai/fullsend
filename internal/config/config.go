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
type AgentEntry struct {
	Name   string `yaml:"name,omitempty"`
	Source string `yaml:"source"`
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

// InferenceConfig configures the inference provider used by agents.
type InferenceConfig struct {
	Provider string `yaml:"provider"`
}

// StatusNotificationConfig controls status comments posted on issues/PRs
// when agents start and complete.
type StatusNotificationConfig struct {
	Comment CommentNotificationConfig `yaml:"comment,omitempty"`
}

// CommentNotificationConfig controls start/completion comments.
// Valid values: "enabled" (default when parent is set), "disabled".
type CommentNotificationConfig struct {
	Start      string `yaml:"start,omitempty"`
	Completion string `yaml:"completion,omitempty"`
}

// RepoDefaults holds default settings applied to all repos.
type RepoDefaults struct {
	Roles                    []string                  `yaml:"roles"`
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

// OrgConfig is the top-level configuration for a fullsend organization.
type OrgConfig struct {
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

// ValidRoles returns the set of recognized agent roles.
func ValidRoles() []string {
	return []string{"fullsend", "triage", "coder", "review", "fix", "retro", "prioritize", "e2e"}
}

// ValidProviders returns the set of recognized inference providers.
func ValidProviders() []string {
	return []string{"vertex"}
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

// DefaultAgentEntries computes default agent URL entries for the given
// harness names at a specific commit SHA. Each entry is a pinned
// raw.githubusercontent.com URL with an integrity hash.
type AgentEntryBuilder func(harnessName, commitSHA string) (string, error)

// DefaultAgentEntries returns agent entries for the given harness names,
// using builder to compute each URL. When builder is nil, it returns
// nil (for callers that don't have access to the scaffold package).
// Called by install/scaffold in Phase 2 (ADR 0058); defined here in
// Phase 1 so the type and validation are co-located.
func DefaultAgentEntries(harnessNames []string, commitSHA string, builder AgentEntryBuilder) ([]AgentEntry, error) {
	if builder == nil || commitSHA == "" {
		return nil, nil
	}
	entries := make([]AgentEntry, 0, len(harnessNames))
	for _, name := range harnessNames {
		urlWithHash, err := builder(name, commitSHA)
		if err != nil {
			return nil, fmt.Errorf("building agent URL for %s: %w", name, err)
		}
		entries = append(entries, AgentEntry{Source: urlWithHash})
	}
	return entries, nil
}

// NewOrgConfig creates a new OrgConfig with sensible defaults.
func NewOrgConfig(allRepos, enabledRepos, roles []string, inferenceProvider, org string) *OrgConfig {
	repos := make(map[string]RepoConfig, len(allRepos))
	for _, r := range allRepos {
		repos[r] = RepoConfig{
			Enabled: slices.Contains(enabledRepos, r),
		}
	}

	cfg := &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			Roles:                    roles,
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

// ParseOrgConfig parses YAML bytes into an OrgConfig.
func ParseOrgConfig(data []byte) (*OrgConfig, error) {
	var cfg OrgConfig
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

// Marshal serializes the OrgConfig to YAML with a descriptive header comment.
func (c *OrgConfig) Marshal() ([]byte, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshaling org config: %w", err)
	}
	return []byte(configHeader + string(body)), nil
}

// Validate checks the OrgConfig for structural correctness.
func (c *OrgConfig) Validate() error {
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
	if err := validateStatusNotifications(c.Defaults.StatusNotifications); err != nil {
		return err
	}
	if err := validateAgentEntries(c.Agents, c.AllowedRemoteResources); err != nil {
		return err
	}
	if err := validateCreateIssues(c.CreateIssues); err != nil {
		return err
	}
	return nil
}

// validateAgentEntries checks agent entries for structural correctness.
// Uses urlutil.IsURL, urlutil.ParseIntegrityHash, and
// urlutil.MatchingAllowedPrefixInList for consistency with runtime
// resolution (case-insensitive scheme, percent-decoding, dot-segment
// cleaning).
func validateAgentEntries(agents []AgentEntry, allowlist []string) error {
	seen := make(map[string]bool, len(agents))
	for i, entry := range agents {
		if entry.Source == "" {
			return fmt.Errorf("agents[%d]: source must not be empty", i)
		}

		name := entry.DerivedName()
		if !validConfigAgentName.MatchString(name) {
			return fmt.Errorf("agents[%d] (%s): derived name is invalid, must start with alphanumeric and contain only [a-zA-Z0-9_-] (source: %q)", i, name, entry.Source)
		}
		lowerName := strings.ToLower(name)
		if seen[lowerName] {
			return fmt.Errorf("agents[%d] (%s): duplicate agent name (case-insensitive)", i, name)
		}
		seen[lowerName] = true

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
	validCommentValues := []string{"", "enabled", "disabled"}
	if !slices.Contains(validCommentValues, cfg.Comment.Start) {
		return fmt.Errorf("invalid status_notifications.comment.start %q: must be \"enabled\" or \"disabled\"", cfg.Comment.Start)
	}
	if !slices.Contains(validCommentValues, cfg.Comment.Completion) {
		return fmt.Errorf("invalid status_notifications.comment.completion %q: must be \"enabled\" or \"disabled\"", cfg.Comment.Completion)
	}
	return nil
}

// EnabledRepos returns a sorted list of repo names where Enabled is true.
func (c *OrgConfig) EnabledRepos() []string {
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
func (c *OrgConfig) DisabledRepos() []string {
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
func (c *OrgConfig) DefaultRoles() []string {
	return c.Defaults.Roles
}

// PerRepoConfig holds configuration for per-repo installation mode.
// Stored in .fullsend/config.yaml within the target repository.
type PerRepoConfig struct {
	Version                string              `yaml:"version"`
	KillSwitch             bool                `yaml:"kill_switch,omitempty"`
	Roles                  []string            `yaml:"roles,omitempty"`
	Agents                 []AgentEntry        `yaml:"agents,omitempty"`
	AllowedRemoteResources []string            `yaml:"allowed_remote_resources,omitempty"`
	CreateIssues           *CreateIssuesConfig `yaml:"create_issues,omitempty"`
}

const perRepoConfigHeader = `# fullsend per-repo configuration
# https://github.com/fullsend-ai/fullsend
#
# This file configures fullsend for per-repo installation mode.
# See ADR 0033 for details.
`

// NewPerRepoConfig creates a new PerRepoConfig with the given roles.
func NewPerRepoConfig(roles []string, targetRepo string) *PerRepoConfig {
	if roles == nil {
		roles = DefaultAgentRoles()
	}
	cfg := &PerRepoConfig{
		Version:                "1",
		Roles:                  roles,
		AllowedRemoteResources: DefaultAllowedRemoteResources(),
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

// ParsePerRepoConfig parses YAML bytes into a PerRepoConfig.
func ParsePerRepoConfig(data []byte) (*PerRepoConfig, error) {
	var cfg PerRepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing per-repo config: %w", err)
	}
	return &cfg, nil
}

// Marshal serializes the PerRepoConfig to YAML with a descriptive header.
func (c *PerRepoConfig) Marshal() ([]byte, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshaling per-repo config: %w", err)
	}
	return []byte(perRepoConfigHeader + string(body)), nil
}

// Validate checks the PerRepoConfig for structural correctness.
func (c *PerRepoConfig) Validate() error {
	if c.Version != "1" {
		return fmt.Errorf("unsupported version %q: must be \"1\"", c.Version)
	}
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
	if err := validateAgentEntries(c.Agents, c.AllowedRemoteResources); err != nil {
		return err
	}
	if err := validateCreateIssues(c.CreateIssues); err != nil {
		return err
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
