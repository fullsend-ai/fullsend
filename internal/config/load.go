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
//
// For per-repo configs loaded via LoadFromDir, the Layered field provides
// accessor-based lookup with merge order overlay -> base -> code defaults
// (ADR 0069 Decision 2). The top-level Agents, AllowedRemoteResources, and
// KillSwitch fields are resolved through the layered accessor when a base
// file is present.
type DirConfig struct {
	Agents                 []AgentEntry
	AllowedRemoteResources []string
	KillSwitch             bool
	IsOrg                  bool
	Org                    *OrgConfig
	PerRepo                *PerRepoConfig
	// Base is the config.base.yaml layer, nil when absent.
	// Only set for per-repo configs loaded via LoadFromDir.
	Base *PerRepoConfig
	// Layered provides accessor-based lookup across overlay (config.yaml),
	// base (config.base.yaml), and compiled-in defaults. Nil for org configs.
	Layered *LayeredConfig
}

// LoadOpts controls how LoadFromDir handles a missing config.yaml.
type LoadOpts struct {
	// MissingOK returns an empty DirConfig when config.yaml is absent.
	MissingOK bool
}

// LoadFromDir reads and parses config.yaml and optionally config.base.yaml
// from dir. For per-repo configs, when config.base.yaml exists it is loaded
// as the base layer with merge order: config.yaml (overlay) ->
// config.base.yaml (base) -> compiled-in defaults (ADR 0069 Decision 2).
// Missing config.base.yaml is treated as an empty layer (backward compatible
// with existing single-file installs).
func LoadFromDir(dir string, opts LoadOpts) (*DirConfig, error) {
	configPath := filepath.Join(dir, "config.yaml")
	basePath := filepath.Join(dir, "config.base.yaml")

	// Load base layer first (always optional).
	base, err := loadBaseConfig(basePath)
	if err != nil {
		return nil, err
	}

	// Load overlay (config.yaml).
	overlayData, overlayErr := os.ReadFile(configPath)
	overlayMissing := overlayErr != nil && os.IsNotExist(overlayErr)

	if overlayErr != nil && !overlayMissing {
		return nil, fmt.Errorf("reading config: %w", overlayErr)
	}
	if overlayMissing && !opts.MissingOK {
		return nil, fmt.Errorf("reading config: %w", overlayErr)
	}

	var dc *DirConfig
	if overlayMissing {
		if base != nil {
			// Base-only: resolve from base then defaults.
			dc = dirConfigFromPerRepo(base)
			dc.PerRepo = &PerRepoConfig{Version: base.Version}
			dc.Base = base
			dc.Layered = NewLayeredConfig(dc.PerRepo, base)
			dc.Agents = dc.Layered.Agents()
			dc.AllowedRemoteResources = dc.Layered.AllowedRemoteResources()
			dc.KillSwitch = dc.Layered.KillSwitch()
		} else {
			// Neither file: use defaults (backward compatible).
			dc = emptyDirConfig()
			dc.Layered = NewLayeredConfig(dc.PerRepo)
		}
		return dc, nil
	}

	// Parse overlay.
	dc, err = parseConfigData(overlayData)
	if err != nil {
		return nil, err
	}

	// Org configs do not use layering.
	if dc.IsOrg {
		return dc, nil
	}

	// Build layered accessor for per-repo configs.
	dc.Base = base
	if base != nil {
		dc.Layered = NewLayeredConfig(dc.PerRepo, base)
		// Re-resolve top-level fields through layered accessor.
		dc.Agents = dc.Layered.Agents()
		dc.AllowedRemoteResources = dc.Layered.AllowedRemoteResources()
		dc.KillSwitch = dc.Layered.KillSwitch()
	} else {
		dc.Layered = NewLayeredConfig(dc.PerRepo)
	}

	return dc, nil
}

// loadBaseConfig reads and parses config.base.yaml. Returns nil when
// the file does not exist (missing base is valid -- treated as an empty
// layer per ADR 0069).
func loadBaseConfig(path string) (*PerRepoConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading base config: %w", err)
	}
	cfg, err := ParsePerRepoConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parsing base config: %w", err)
	}
	return cfg, nil
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
