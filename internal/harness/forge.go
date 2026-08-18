package harness

import (
	"fmt"
	"sort"
	"strings"
)

// ForgeConfig holds platform-specific harness configuration.
// This is purely declarative YAML config — it selects which
// scripts, skills, host files, and env vars to use per platform. It is
// distinct from the forge.Client interface (internal/forge/),
// which is the runtime abstraction for forge API operations.
type ForgeConfig struct {
	PreScript      string            `yaml:"pre_script,omitempty"`
	PostScript     string            `yaml:"post_script,omitempty"`
	Policy         string            `yaml:"policy,omitempty"`
	Skills         []SkillEntry      `yaml:"skills,omitempty"` // SkillEntry (not string) to support file-level overrides
	Providers      []string          `yaml:"providers,omitempty"`
	OpenShell      *OpenShellConfig  `yaml:"openshell,omitempty"`
	HostFiles      []HostFile        `yaml:"host_files,omitempty"`
	ValidationLoop *ValidationLoop   `yaml:"validation_loop,omitempty"`
	RunnerEnv      map[string]string `yaml:"runner_env,omitempty"`
	Env            *EnvConfig        `yaml:"env,omitempty"`
}

var validForgeKeys = map[string]bool{
	"github": true,
	"gitlab": true,
}

// ValidForgePlatform reports whether platform is a recognized forge key.
func ValidForgePlatform(platform string) bool {
	return validForgeKeys[platform]
}

// ForgeKeyList returns a comma-separated list of valid forge platform keys.
func ForgeKeyList() string {
	keys := make([]string, 0, len(validForgeKeys))
	for k := range validForgeKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// validateForge checks that the forge section contains only recognized keys
// and that each ForgeConfig uses valid field values.
func (h *Harness) validateForge() error {
	for key, fc := range h.Forge {
		if !validForgeKeys[key] {
			return fmt.Errorf("forge: unrecognized key %q (valid: %s)", key, ForgeKeyList())
		}
		if fc == nil {
			continue
		}
		if fc.Policy != "" && IsURL(fc.Policy) {
			if _, _, hasHash := ParseIntegrityHash(fc.Policy); !hasHash {
				return fmt.Errorf("forge.%s.policy URL must include #sha256=... integrity hash", key)
			}
		}
		if fc.PreScript != "" && IsURL(fc.PreScript) {
			return fmt.Errorf("forge.%s.pre_script must be a local path, not a URL", key)
		}
		if fc.PostScript != "" && IsURL(fc.PostScript) {
			return fmt.Errorf("forge.%s.post_script must be a local path, not a URL", key)
		}
		for i, s := range fc.Skills {
			if IsURL(s.Source) {
				if _, _, hasHash := ParseIntegrityHash(s.Source); !hasHash {
					return fmt.Errorf("forge.%s.skills[%d] URL must include #sha256=... integrity hash", key, i)
				}
			}
		}
		if err := ValidateSkillOverrides(fc.Skills); err != nil {
			return fmt.Errorf("forge.%s: %w", key, err)
		}
		for i, p := range fc.Providers {
			if IsURL(p) {
				if _, _, hasHash := ParseIntegrityHash(p); !hasHash {
					return fmt.Errorf("forge.%s.providers[%d] URL must include #sha256=... integrity hash", key, i)
				}
			}
		}
		if fc.OpenShell != nil {
			for i, p := range fc.OpenShell.Profiles {
				if IsURL(p) {
					if _, _, hasHash := ParseIntegrityHash(p); !hasHash {
						return fmt.Errorf("forge.%s.openshell.profiles[%d] URL must include #sha256=... integrity hash", key, i)
					}
				}
			}
		}
		for i, hf := range fc.HostFiles {
			if hf.Src == "" {
				return fmt.Errorf("forge.%s.host_files[%d]: src is required", key, i)
			}
			if hf.Dest == "" {
				return fmt.Errorf("forge.%s.host_files[%d]: dest is required", key, i)
			}
			if IsURL(hf.Src) {
				return fmt.Errorf("forge.%s.host_files[%d].src must be a local path, not a URL", key, i)
			}
		}
		if fc.ValidationLoop != nil {
			if fc.ValidationLoop.Script == "" {
				return fmt.Errorf("forge.%s.validation_loop.script is required when validation_loop is set", key)
			}
			if IsURL(fc.ValidationLoop.Script) {
				return fmt.Errorf("forge.%s.validation_loop.script must be a local path, not a URL", key)
			}
			if fc.ValidationLoop.Schema != "" && IsURL(fc.ValidationLoop.Schema) {
				return fmt.Errorf("forge.%s.validation_loop.schema must be a local path, not a URL", key)
			}
		}
	}
	return nil
}

// ResolveForge merges forge-specific overrides into the harness in place.
// After merging, h.Forge is set to nil (consumed). If platform is empty or
// h.Forge is nil, this is a no-op. If platform is not present in h.Forge,
// an error is returned.
//
// Pipeline ordering: LoadWithOpts calls validateForge → ResolveForge →
// Validate. validateForge must run first because ResolveForge consumes
// h.Forge (sets it to nil). After ResolveForge, Validate's validateForge
// call sees nil and is a no-op, which is correct because the forge map
// was already validated before merging.
func (h *Harness) ResolveForge(platform string) error {
	if platform == "" || h.Forge == nil {
		return nil
	}
	if !validForgeKeys[platform] {
		return fmt.Errorf("forge platform %q is not valid (valid: %s)", platform, ForgeKeyList())
	}
	fc, ok := h.Forge[platform]
	if !ok {
		return fmt.Errorf("forge platform %q not configured (available: %s)", platform, forgeKeyList(h.Forge))
	}
	if fc != nil {
		mergeForgeConfig(h, fc)
	}
	h.Forge = nil
	return nil
}

// mergeForgeConfig applies forge-specific overrides to the harness.
//
// Merge rules per ADR-0045:
//   - Scalars: forge overrides if non-empty
//   - Skills: top-level + forge (concatenated)
//   - Providers: top-level + forge (concatenated)
//   - OpenShell.Profiles: top-level + forge (concatenated)
//   - HostFiles: top-level + forge (concatenated with last-writer-wins dedup by Dest)
//   - RunnerEnv: top-level merged with forge; forge keys win
//   - ValidationLoop: top-level merged with forge; forge fields win
func mergeForgeConfig(h *Harness, fc *ForgeConfig) {
	if fc.PreScript != "" {
		h.PreScript = fc.PreScript
	}
	if fc.PostScript != "" {
		h.PostScript = fc.PostScript
	}
	if fc.Policy != "" {
		h.Policy = fc.Policy
	}

	if fc.Skills != nil {
		h.Skills = mergeSkills(h.Skills, fc.Skills)
	}

	if fc.Providers != nil {
		merged := make([]string, 0, len(h.Providers)+len(fc.Providers))
		merged = append(merged, h.Providers...)
		merged = append(merged, fc.Providers...)
		h.Providers = merged
	}

	if fc.OpenShell != nil && len(fc.OpenShell.Profiles) > 0 {
		if h.OpenShell == nil {
			h.OpenShell = &OpenShellConfig{}
		}
		merged := make([]string, 0, len(h.OpenShell.Profiles)+len(fc.OpenShell.Profiles))
		merged = append(merged, h.OpenShell.Profiles...)
		merged = append(merged, fc.OpenShell.Profiles...)
		h.OpenShell.Profiles = merged
	}

	if fc.HostFiles != nil {
		h.HostFiles = mergeHostFiles(h.HostFiles, fc.HostFiles)
	}

	if fc.RunnerEnv != nil {
		if h.RunnerEnv == nil {
			h.RunnerEnv = make(map[string]string, len(fc.RunnerEnv))
		}
		for k, v := range fc.RunnerEnv {
			h.RunnerEnv[k] = v
		}
	}

	if fc.ValidationLoop != nil {
		if h.ValidationLoop == nil {
			h.ValidationLoop = fc.ValidationLoop
		} else {
			merged := *fc.ValidationLoop
			mergeValidationLoopInto(h.ValidationLoop, &merged)
			h.ValidationLoop = &merged
		}
	}

	// Env: merge sub-maps independently; forge keys win (ADR 0055)
	if fc.Env != nil {
		if h.Env == nil {
			h.Env = &EnvConfig{}
		}
		h.Env.mergeEnvFrom(fc.Env, true)
	}
}

func forgeKeyList(m map[string]*ForgeConfig) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}
