package harness

import (
	"fmt"
	"sort"
	"strings"
)

// ForgeConfig holds platform-specific harness configuration.
// This is purely declarative YAML config — it selects which
// scripts, skills, and env vars to use per platform. It is
// distinct from the forge.Client interface (internal/forge/),
// which is the runtime abstraction for forge API operations.
type ForgeConfig struct {
	PreScript      string            `yaml:"pre_script,omitempty"`
	PostScript     string            `yaml:"post_script,omitempty"`
	Skills         []string          `yaml:"skills,omitempty"`
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
		if fc.PreScript != "" && IsURL(fc.PreScript) {
			return fmt.Errorf("forge.%s.pre_script must be a local path, not a URL", key)
		}
		if fc.PostScript != "" && IsURL(fc.PostScript) {
			return fmt.Errorf("forge.%s.post_script must be a local path, not a URL", key)
		}
		for i, s := range fc.Skills {
			if IsURL(s) {
				if _, _, hasHash := ParseIntegrityHash(s); !hasHash {
					return fmt.Errorf("forge.%s.skills[%d] URL must include #sha256=... integrity hash", key, i)
				}
			}
		}
		if fc.ValidationLoop != nil {
			if fc.ValidationLoop.Script == "" {
				return fmt.Errorf("forge.%s.validation_loop.script is required when validation_loop is set", key)
			}
			if IsURL(fc.ValidationLoop.Script) {
				return fmt.Errorf("forge.%s.validation_loop.script must be a local path, not a URL", key)
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
//   - RunnerEnv: top-level merged with forge; forge keys win
//   - ValidationLoop: forge replaces entirely if non-nil
func mergeForgeConfig(h *Harness, fc *ForgeConfig) {
	if fc.PreScript != "" {
		h.PreScript = fc.PreScript
	}
	if fc.PostScript != "" {
		h.PostScript = fc.PostScript
	}

	if fc.Skills != nil {
		h.Skills = append(h.Skills, fc.Skills...)
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
		h.ValidationLoop = fc.ValidationLoop
	}
}

func forgeKeyList(m map[string]*ForgeConfig) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}
