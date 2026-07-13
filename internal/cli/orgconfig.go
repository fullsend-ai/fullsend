package cli

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

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

// isPerRepoYAML probes raw YAML for structural markers that distinguish
// PerRepoConfig from OrgConfig. OrgConfig has org-only top-level keys
// (dispatch, repos, inference, defaults); PerRepoConfig never does. When
// no org-only key is present we default to per-repo: the shared fields
// parse identically under either parser, and this avoids silently
// dropping the per-repo Roles field on configs that omit it.
func isPerRepoYAML(data []byte) bool {
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

// tryLoadFullsendConfig attempts to load an org or per-repo config.yaml
// from the given path. Returns nil without error when the file is absent
// (best-effort). Per-repo config is adapted to OrgConfig via
// OrgConfigFromPerRepo so callers see a unified type.
func tryLoadFullsendConfig(path string, printer *ui.Printer) *config.OrgConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			printer.StepWarn("Fullsend config unreadable (remote resource allowlist unavailable): " + err.Error())
		}
		return nil
	}
	var probe interface{}
	if yamlErr := yaml.Unmarshal(data, &probe); yamlErr != nil {
		printer.StepWarn("Config malformed (remote resource allowlist unavailable): " + yamlErr.Error())
		return nil
	}
	if isPerRepoYAML(data) {
		perRepo, perRepoErr := config.ParsePerRepoConfig(data)
		if perRepoErr != nil {
			printer.StepWarn("Per-repo config malformed (remote resource allowlist unavailable): " + perRepoErr.Error())
			return nil
		}
		orgCfg := config.OrgConfigFromPerRepo(perRepo)
		orgCfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(orgCfg.AllowedRemoteResources)
		return orgCfg
	}
	cfg, parseErr := config.ParseOrgConfig(data)
	if parseErr != nil {
		printer.StepWarn("Org config malformed (remote resource allowlist unavailable): " + parseErr.Error())
		return nil
	}
	cfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(cfg.AllowedRemoteResources)
	return cfg
}

// tryLoadOrgConfig loads an org or per-repo config.yaml (best-effort).
var tryLoadOrgConfig = tryLoadFullsendConfig

// requireFullsendConfig loads an org or per-repo config.yaml from the
// given path with strict error handling. Returns differentiated errors
// for missing files, unreadable files, and parse failures.
func requireFullsendConfig(path string, printer *ui.Printer) (*config.OrgConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		printer.StepFail("Failed to load fullsend config")
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("URL-referenced resources require a config.yaml with allowed_remote_resources (expected at %s)", path)
		}
		return nil, fmt.Errorf("reading fullsend config for remote resource validation: %w", err)
	}
	var probe interface{}
	if yamlErr := yaml.Unmarshal(data, &probe); yamlErr != nil {
		printer.StepFail("Failed to parse config")
		return nil, fmt.Errorf("parsing config: %w", yamlErr)
	}
	if isPerRepoYAML(data) {
		perRepo, perRepoErr := config.ParsePerRepoConfig(data)
		if perRepoErr != nil {
			printer.StepFail("Failed to parse per-repo config")
			return nil, fmt.Errorf("parsing per-repo config: %w", perRepoErr)
		}
		orgCfg := config.OrgConfigFromPerRepo(perRepo)
		orgCfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(orgCfg.AllowedRemoteResources)
		return orgCfg, nil
	}
	cfg, parseErr := config.ParseOrgConfig(data)
	if parseErr != nil {
		printer.StepFail("Failed to parse org config")
		return nil, fmt.Errorf("parsing org config: %w", parseErr)
	}
	cfg.AllowedRemoteResources = config.EnsureDefaultAllowedRemoteResources(cfg.AllowedRemoteResources)
	return cfg, nil
}

// requireOrgConfig loads an org or per-repo config.yaml (strict).
var requireOrgConfig = requireFullsendConfig
