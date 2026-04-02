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
	Version  string                `yaml:"version"`
	Dispatch DispatchConfig        `yaml:"dispatch"`
	Defaults RepoDefaults          `yaml:"defaults"`
	Agents   []AgentEntry          `yaml:"agents"`
	Repos    map[string]RepoConfig `yaml:"repos"`
}

// ValidRoles returns the set of recognized agent roles.
func ValidRoles() []string {
	return []string{"fullsend", "triage", "coder", "review"}
}

// DefaultAgentRoles returns the standard set of agent roles used
// when no custom roles are specified. This is the same as ValidRoles
// but exists as a separate function for semantic clarity.
func DefaultAgentRoles() []string {
	return ValidRoles()
}

// NewOrgConfig creates a new OrgConfig with sensible defaults.
func NewOrgConfig(allRepos, enabledRepos, roles []string, agents []AgentEntry) *OrgConfig {
	repos := make(map[string]RepoConfig, len(allRepos))
	for _, r := range allRepos {
		repos[r] = RepoConfig{
			Enabled: slices.Contains(enabledRepos, r),
		}
	}

	return &OrgConfig{
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
	if c.Defaults.MaxImplementationRetries < 0 {
		return fmt.Errorf("max_implementation_retries must be >= 0, got %d", c.Defaults.MaxImplementationRetries)
	}
	valid := ValidRoles()
	for _, role := range c.Defaults.Roles {
		if !slices.Contains(valid, role) {
			return fmt.Errorf("invalid role %q: must be one of %s", role, strings.Join(valid, ", "))
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
