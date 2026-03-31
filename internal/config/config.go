// Package config defines the fullsend configuration types and defaults.
//
// The primary configuration lives in a .fullsend repo within the target
// GitHub organization as config.yaml. This package handles both the
// generation of initial configs and the (future) reading of existing ones.
package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// OrgConfig is the top-level configuration stored in .fullsend/config.yaml.
type OrgConfig struct {
	// Repos holds per-repository configuration overrides.
	Repos map[string]RepoConfig `yaml:"repos"`

	// Version of the config schema.
	Version string `yaml:"version"`

	// Dispatch configures the execution platform for agents.
	Dispatch DispatchConfig `yaml:"dispatch"`

	// Defaults apply to all repos unless overridden.
	Defaults RepoDefaults `yaml:"defaults"`
}

// DispatchConfig specifies the agent execution platform.
type DispatchConfig struct {
	// Platform is the execution backend. Currently only "github-actions" is supported.
	Platform string `yaml:"platform"`
}

// RepoDefaults provides default settings applied to all repositories.
type RepoDefaults struct {
	// Agents lists which agent roles are enabled by default.
	Agents []string `yaml:"agents"`

	// MaxImplementationRetries caps implement-test loop iterations.
	MaxImplementationRetries int `yaml:"max_implementation_retries"`

	// AutoMerge controls whether agents can merge PRs automatically.
	AutoMerge bool `yaml:"auto_merge"`
}

// RepoConfig holds per-repository settings.
type RepoConfig struct {
	// Agents overrides the default agent list for this repository.
	// If empty, the org defaults are used.
	Agents []string `yaml:"agents,omitempty"`

	// Enabled controls whether fullsend operates on this repository.
	Enabled bool `yaml:"enabled"`
}

// DefaultAgents returns the standard set of agent roles.
func DefaultAgents() []string {
	return []string{"triage", "implementation", "review"}
}

// NewOrgConfig creates a new OrgConfig with safe defaults.
// All discovered repos are listed but disabled by default.
func NewOrgConfig(repos []string, enabledRepos []string, agents []string) *OrgConfig {
	if len(agents) == 0 {
		agents = DefaultAgents()
	}

	enabledSet := make(map[string]bool, len(enabledRepos))
	for _, r := range enabledRepos {
		enabledSet[r] = true
	}

	repoConfigs := make(map[string]RepoConfig, len(repos))
	for _, r := range repos {
		repoConfigs[r] = RepoConfig{
			Enabled: enabledSet[r],
		}
	}

	return &OrgConfig{
		Version: "1",
		Dispatch: DispatchConfig{
			Platform: "github-actions",
		},
		Defaults: RepoDefaults{
			AutoMerge:                false,
			MaxImplementationRetries: 2,
			Agents:                   agents,
		},
		Repos: repoConfigs,
	}
}

// Marshal serializes the config to YAML with a descriptive header comment.
func (c *OrgConfig) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshalling config: %w", err)
	}

	header := `# fullsend organization configuration
# https://github.com/fullsend-ai/fullsend
#
# This file controls how fullsend operates across your organization.
# Edit repo entries to enable/disable agent processing per repository.
# See docs at https://github.com/fullsend-ai/fullsend for details.
`
	return []byte(header + string(data)), nil
}

// Validate checks the config for common errors.
func (c *OrgConfig) Validate() error {
	if c.Version == "" {
		return fmt.Errorf("version is required")
	}

	validPlatforms := map[string]bool{"github-actions": true}
	if !validPlatforms[c.Dispatch.Platform] {
		return fmt.Errorf("unsupported dispatch platform: %q (supported: %s)",
			c.Dispatch.Platform, strings.Join(keys(validPlatforms), ", "))
	}

	if c.Defaults.MaxImplementationRetries < 0 {
		return fmt.Errorf("max_implementation_retries must be non-negative, got %d",
			c.Defaults.MaxImplementationRetries)
	}

	validAgents := map[string]bool{"triage": true, "implementation": true, "review": true}
	for _, a := range c.Defaults.Agents {
		if !validAgents[a] {
			return fmt.Errorf("unknown agent role %q (valid: %s)",
				a, strings.Join(keys(validAgents), ", "))
		}
	}

	for repoName, repo := range c.Repos {
		for _, a := range repo.Agents {
			if !validAgents[a] {
				return fmt.Errorf("repo %q: unknown agent role %q (valid: %s)",
					repoName, a, strings.Join(keys(validAgents), ", "))
			}
		}
	}

	return nil
}

// EnabledRepos returns the list of repos that have enabled: true.
func (c *OrgConfig) EnabledRepos() []string {
	var enabled []string
	for name, repo := range c.Repos {
		if repo.Enabled {
			enabled = append(enabled, name)
		}
	}
	return enabled
}

func keys(m map[string]bool) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}
