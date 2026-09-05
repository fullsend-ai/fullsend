package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// piPersona is one discovered sub-agent persona: a file under
// sub-agents/*.md in a skill root, whose frontmatter declares a name,
// optional model and optional tools restriction.
type piPersona struct {
	Name          string
	Model         string   // frontmatter model (alias or id); empty = inherit parent
	Tools         []string // Claude tool names; nil = parent's set
	BashAllowlist []string // Bash(a,b) prefixes; nil = unrestricted
}

// piPersonaManifestEntry is one entry in the manifest's personas table,
// consumed by fullsend-agent.js.
type piPersonaManifestEntry struct {
	Model         string   `json:"model"`
	Tools         []string `json:"tools"`
	BashAllowlist []string `json:"bashAllowlist,omitempty"`
}

// Note: persona name validation reuses config.ValidSubagentKey (same
// pattern: lowercase alphanumeric segments joined by hyphens, ≤64 chars)
// and config.ReservedSubagentKeys (same list: "default", "explore")
// to avoid duplicating the regex and reserved list.

// discoverPersonas scans each skill directory for sub-agents/*.md files,
// parses their frontmatter, validates naming, and returns the discovered
// personas. The result is ordered deterministically (by name).
//
// Validation:
//   - frontmatter name: must be present, equal to the file basename (sans .md)
//   - name shape: ^[a-z0-9]+(-[a-z0-9]+)*$ and ≤64 characters
//   - unique across all skill roots
//   - not reserved: "default", "explore", the run's own agent name,
//     ValidAgentNames(), or Claude Code built-in agent types
func discoverPersonas(skillDirs []string, agentName string) ([]piPersona, error) {
	builtinAgents := config.ValidAgentNames()
	// Claude Code built-in agent types that subagent_type cannot collide
	// with (the pinned CLI rejects unknown types with this list).
	claudeBuiltins := []string{"claude", "code", "general-purpose"}

	seen := map[string]string{} // persona name → skill that declared it
	var personas []piPersona

	for _, skillDir := range skillDirs {
		subagentsDir := filepath.Join(skillDir, "sub-agents")
		entries, err := os.ReadDir(subagentsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading sub-agents directory %s: %w", subagentsDir, err)
		}
		skillName := filepath.Base(skillDir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			basename := strings.TrimSuffix(e.Name(), ".md")
			path := filepath.Join(subagentsDir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("reading persona file %s: %w", path, err)
			}
			def, err := parsePiAgent(data)
			if err != nil {
				return nil, fmt.Errorf("parsing persona %s: %w", path, err)
			}
			// Validation: frontmatter name must be present.
			if def.Name == "" {
				return nil, fmt.Errorf("persona %s: frontmatter name: is required", path)
			}
			// Validation: frontmatter name must equal file basename.
			if def.Name != basename {
				return nil, fmt.Errorf("persona %s: frontmatter name %q must equal file basename %q", path, def.Name, basename)
			}
			name := def.Name
			// Validation: name shape (reuses config.ValidSubagentKey).
			if !config.ValidSubagentKey(name) {
				return nil, fmt.Errorf("persona %s: name %q must be lowercase alphanumeric segments joined by hyphens (max 64 chars)", path, name)
			}
			// Validation: reserved names (reuses config.ReservedSubagentKeys).
			if slices.Contains(config.ReservedSubagentKeys(), name) {
				return nil, fmt.Errorf("persona %s: name %q is reserved", path, name)
			}
			if strings.EqualFold(name, agentName) {
				return nil, fmt.Errorf("persona %s: name %q collides with the agent's own name", path, name)
			}
			if slices.Contains(builtinAgents, name) {
				return nil, fmt.Errorf("persona %s: name %q collides with a built-in agent name", path, name)
			}
			if slices.Contains(claudeBuiltins, name) {
				return nil, fmt.Errorf("persona %s: name %q collides with a Claude Code built-in agent type", path, name)
			}
			// Validation: uniqueness across skills.
			if prevSkill, dup := seen[name]; dup {
				return nil, fmt.Errorf("persona %s: name %q already declared by skill %q", path, name, prevSkill)
			}
			seen[name] = skillName

			personas = append(personas, piPersona{
				Name:          name,
				Model:         def.Model,
				Tools:         def.Tools,
				BashAllowlist: def.BashAllowlist,
			})
		}
	}

	// Sort by name for deterministic ordering.
	slices.SortFunc(personas, func(a, b piPersona) int {
		return strings.Compare(a.Name, b.Name)
	})
	return personas, nil
}

// resolvePersonaModels resolves each persona's model using the
// resolution order: repo subagents.<persona> > repo subagents.default >
// frontmatter model (alias-resolved) > parent model. Each result is
// canonicalised and checked against the trusted closed set.
//
// subagentsCfg is the merged agents[].subagents map from the config.
// parentModelSpec is the agent definition's translated model spec.
// configAliases is the merged models.aliases map.
// trustedSpecs is the set of model specs the run can serve.
//
// A config key that names no discovered persona is an error (the
// issue's "repo's own layers" rule); keys from inherited/preset
// layers should warn instead, but this PR does not distinguish layers.
func resolvePersonaModels(
	personas []piPersona,
	subagentsCfg map[string]*string,
	parentModelSpec string,
	configAliases map[string]string,
	trustedSpecs map[string]string,
) (map[string]piPersonaManifestEntry, error) {
	result := make(map[string]piPersonaManifestEntry, len(personas))

	// Resolve the default subagent model, if configured.
	var defaultModel string
	if v, ok := subagentsCfg["default"]; ok && v != nil {
		defaultModel = *v
	}

	personaNames := make(map[string]bool, len(personas))
	for _, p := range personas {
		personaNames[p.Name] = true
	}

	// Validate that config keys reference discovered personas.
	for key, val := range subagentsCfg {
		if key == "default" {
			continue
		}
		// A nil value is a tombstone — it removes an inherited entry,
		// so it does not need to name a persona.
		if val == nil {
			continue
		}
		if !personaNames[key] {
			return nil, fmt.Errorf("subagents.%s: no persona %q was discovered; discovered personas: %s",
				key, key, strings.Join(discoveredPersonaNames(personas), ", "))
		}
	}

	for _, p := range personas {
		// Resolution order: repo subagents.<persona> > repo subagents.default
		// > frontmatter model > parent model.
		model := ""
		if v, ok := subagentsCfg[p.Name]; ok && v != nil {
			model = *v
		} else if defaultModel != "" {
			model = defaultModel
		} else if p.Model != "" {
			model = p.Model
		}

		var spec string
		if model != "" {
			spec = translatePiModel(model, configAliases)
		} else {
			spec = parentModelSpec
		}

		// Verify the resolved spec is in the trusted set.
		if _, ok := trustedSpecs[strings.ToLower(spec)]; !ok {
			accepted := trustedSpecNames(trustedSpecs)
			return nil, fmt.Errorf("persona %q: resolved model %q is not available in this run; accepted: %s",
				p.Name, spec, strings.Join(accepted, ", "))
		}

		// Compute persona's tools: intersection with parent when
		// the persona declares its own set; parent's set otherwise.
		// Tools resolution is deferred to the JS side for simplicity —
		// the manifest carries the persona's declared tools and the JS
		// extension intersects them with the parent at dispatch time.
		entry := piPersonaManifestEntry{
			Model:         spec,
			BashAllowlist: p.BashAllowlist,
		}
		if p.Tools != nil {
			piTools, _ := piToolsFor(p.Tools)
			// Remove Agent/Task from persona tools — personas cannot dispatch.
			filtered := make([]string, 0, len(piTools))
			for _, t := range piTools {
				if t != piAgentToolName && t != piAgentToolAlias {
					filtered = append(filtered, t)
				}
			}
			entry.Tools = filtered
		}
		result[p.Name] = entry
	}
	return result, nil
}

func discoveredPersonaNames(personas []piPersona) []string {
	names := make([]string, len(personas))
	for i, p := range personas {
		names[i] = p.Name
	}
	return names
}

func trustedSpecNames(specs map[string]string) []string {
	seen := make(map[string]bool, len(specs))
	var names []string
	for _, v := range specs {
		if !seen[v] {
			seen[v] = true
			names = append(names, v)
		}
	}
	slices.Sort(names)
	return names
}
