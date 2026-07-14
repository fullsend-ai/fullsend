package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DirConfig is the unified view of a fullsend config directory used by agent
// list and dispatch. It preserves the underlying org or per-repo struct for
// CLI write-back.
type DirConfig struct {
	Agents                 []AgentEntry
	AllowedRemoteResources []string
	KillSwitch             bool
	IsOrg                  bool
	Org                    *OrgConfig
	PerRepo                *PerRepoConfig
}

// LoadOpts controls how LoadFromDir handles a missing config.yaml.
type LoadOpts struct {
	// MissingOK returns an empty DirConfig when config.yaml is absent.
	MissingOK bool
}

// LoadFromDir reads and parses config.yaml from dir.
func LoadFromDir(dir string, opts LoadOpts) (*DirConfig, error) {
	return LoadFromFile(filepath.Join(dir, "config.yaml"), opts)
}

// LoadFromFile reads and parses a config file at path.
func LoadFromFile(path string, opts LoadOpts) (*DirConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && opts.MissingOK {
			return emptyDirConfig(), nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return parseConfigData(data)
}

func emptyDirConfig() *DirConfig {
	pr := NewPerRepoConfig(nil, "")
	return &DirConfig{
		Agents:                 pr.Agents,
		AllowedRemoteResources: pr.AllowedRemoteResources,
		KillSwitch:             pr.KillSwitch,
		IsOrg:                  false,
		PerRepo:                pr,
	}
}

func parseConfigData(data []byte) (*DirConfig, error) {
	var probe interface{}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if IsPerRepoYAML(data) {
		perRepo, err := ParsePerRepoConfig(data)
		if err != nil {
			return nil, fmt.Errorf("parsing per-repo config: %w", err)
		}
		return dirConfigFromPerRepo(perRepo), nil
	}
	org, err := ParseOrgConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parsing org config: %w", err)
	}
	return dirConfigFromOrg(org), nil
}

func dirConfigFromPerRepo(pr *PerRepoConfig) *DirConfig {
	return &DirConfig{
		Agents:                 pr.Agents,
		AllowedRemoteResources: pr.AllowedRemoteResources,
		KillSwitch:             pr.KillSwitch,
		IsOrg:                  false,
		PerRepo:                pr,
	}
}

func dirConfigFromOrg(org *OrgConfig) *DirConfig {
	return &DirConfig{
		Agents:                 org.Agents,
		AllowedRemoteResources: org.AllowedRemoteResources,
		KillSwitch:             org.KillSwitch,
		IsOrg:                  true,
		Org:                    org,
	}
}

// IsPerRepoYAML probes raw YAML for structural markers that distinguish
// PerRepoConfig from OrgConfig. OrgConfig has org-only top-level keys
// (dispatch, repos, inference, defaults); PerRepoConfig never does. When
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
