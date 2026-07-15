package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// configAgentNames extracts derived names from a list of agent entries.
func configAgentNames(agents []config.AgentEntry) []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.DerivedName()
	}
	return names
}

// tryLoadFullsendConfig attempts to load an org or per-repo config.yaml
// from the given path. Returns nil without error when the file is absent
// (best-effort). Per-repo config is adapted to OrgConfig via
// OrgConfigFromPerRepo so callers see a unified type.
func tryLoadFullsendConfig(path string, printer *ui.Printer) *config.OrgConfig {
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			printer.StepWarn("Fullsend config unreadable (remote resource allowlist unavailable): " + err.Error())
		}
		return nil
	}
	dirCfg, err := config.LoadFromDir(filepath.Dir(path), config.LoadOpts{MissingOK: false})
	if err != nil {
		printer.StepWarn("Config malformed (remote resource allowlist unavailable): " + err.Error())
		return nil
	}
	if dirCfg.IsOrg {
		cfg := dirCfg.Org
		cfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(cfg.AllowedRemoteResources)
		return cfg
	}
	orgCfg := config.OrgConfigFromPerRepo(dirCfg.PerRepo)
	orgCfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(orgCfg.AllowedRemoteResources)
	return orgCfg
}

// tryLoadOrgConfig loads an org or per-repo config.yaml (best-effort).
var tryLoadOrgConfig = tryLoadFullsendConfig

// requireFullsendConfig loads an org or per-repo config.yaml from the
// given path with strict error handling. Returns differentiated errors
// for missing files, unreadable files, and parse failures.
func requireFullsendConfig(path string, printer *ui.Printer) (*config.OrgConfig, error) {
	dirCfg, err := config.LoadFromDir(filepath.Dir(path), config.LoadOpts{MissingOK: false})
	if err != nil {
		printer.StepFail("Failed to load fullsend config")
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("URL-referenced resources require a config.yaml with allowed_remote_resources (expected at %s)", path)
		}
		return nil, fmt.Errorf("reading fullsend config for remote resource validation: %w", err)
	}
	if dirCfg.IsOrg {
		cfg := dirCfg.Org
		cfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(cfg.AllowedRemoteResources)
		return cfg, nil
	}
	orgCfg := config.OrgConfigFromPerRepo(dirCfg.PerRepo)
	orgCfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(orgCfg.AllowedRemoteResources)
	return orgCfg, nil
}

// requireOrgConfig loads an org or per-repo config.yaml (strict).
var requireOrgConfig = requireFullsendConfig
