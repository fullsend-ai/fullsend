package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentEntry represents a configured agent with its role and app identity.
type AgentEntry struct {
	Role string `yaml:"role"`
	Name string `yaml:"name"`
	Slug string `yaml:"slug"`
}

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

// RepoDefaults holds default settings applied to all repos.
type RepoDefaults struct {
	Roles                    []string `yaml:"roles"`
	MaxImplementationRetries int      `yaml:"max_implementation_retries"`
	AutoMerge                bool     `yaml:"auto_merge"`
}

// RepoConfig holds per-repo configuration.
type RepoConfig struct {
	Roles   []string `yaml:"roles,omitempty"`
	Enabled bool     `yaml:"enabled"`
}

// OrgConfig is the top-level configuration for a fullsend organization.
type OrgConfig struct {
	Version    string                `yaml:"version"`
	KillSwitch bool                  `yaml:"kill_switch,omitempty"`
	Dispatch   DispatchConfig        `yaml:"dispatch"`
	Inference  InferenceConfig       `yaml:"inference,omitempty"`
	Defaults   RepoDefaults          `yaml:"defaults"`
	Agents     []AgentEntry          `yaml:"agents"`
	Repos      map[string]RepoConfig `yaml:"repos"`
}

// ValidRoles returns the set of recognized agent roles.
func ValidRoles() []string {
	return []string{"fullsend", "triage", "coder", "review", "fix", "retro", "prioritize"}
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
	return []string{"triage", "coder", "review", "retro", "prioritize"}
}

// NewOrgConfig creates a new OrgConfig with sensible defaults.
func NewOrgConfig(allRepos, enabledRepos, roles []string, agents []AgentEntry, inferenceProvider string) *OrgConfig {
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
		Agents: agents,
		Repos:  repos,
	}
	if inferenceProvider != "" {
		cfg.Inference = InferenceConfig{Provider: inferenceProvider}
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

// AgentSlugs returns a map of role to slug from the configured agents.
func (c *OrgConfig) AgentSlugs() map[string]string {
	slugs := make(map[string]string, len(c.Agents))
	for _, a := range c.Agents {
		slugs[a.Role] = a.Slug
	}
	return slugs
}

// DefaultRoles returns the default roles configured for the organization.
func (c *OrgConfig) DefaultRoles() []string {
	return c.Defaults.Roles
}

// PerRepoConfig holds configuration for per-repo installation mode.
// Stored in .fullsend/config.yaml within the target repository.
type PerRepoConfig struct {
	Version    string   `yaml:"version"`
	KillSwitch bool     `yaml:"kill_switch,omitempty"`
	Roles      []string `yaml:"roles,omitempty"`
}

const perRepoConfigHeader = `# fullsend per-repo configuration
# https://github.com/fullsend-ai/fullsend
#
# This file configures fullsend for per-repo installation mode.
# See ADR 0033 for details.
`

// NewPerRepoConfig creates a new PerRepoConfig with the given roles.
func NewPerRepoConfig(roles []string) *PerRepoConfig {
	if roles == nil {
		roles = DefaultAgentRoles()
	}
	return &PerRepoConfig{
		Version: "1",
		Roles:   roles,
	}
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
	return nil
}
