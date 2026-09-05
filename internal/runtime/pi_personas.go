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

// piSkippedPersona is a sub-agents/*.md file that failed validation and was
// not registered. It is fatal only when the repo's subagents config
// references it; otherwise a run that never asked for personas is
// unaffected by a stray file.
type piSkippedPersona struct {
	Name     string // file basename, the name a config key would use
	Path     string
	Model    string // frontmatter model, to tell whether default would apply
	Unparsed bool   // frontmatter did not parse, so Model is unknown
	Reason   string
}

// Note: persona name validation reuses config.ValidSubagentKey (same
// pattern: lowercase alphanumeric segments joined by hyphens, ≤64 chars)
// and config.ReservedSubagentKeys (same list: "default", "explore")
// to avoid duplicating the regex and reserved list.

// discoverPersonas scans each skill directory for sub-agents/*.md files
// and returns the personas that pass validation, sorted by name, plus the
// files that did not, each with the rule it broke. Validation failures are
// never fatal here: a repo with a custom skill must not start failing runs
// over frontmatter that was inert before personas existed. The caller
// makes a skipped persona fatal only when the repo's config names it.
//
// Rules: frontmatter name present and equal to the file basename; shape
// ^[a-z0-9]+(-[a-z0-9]+)*$ and at most 64 characters; not reserved
// ("default", "explore", "agent", the run's own agent name,
// ValidAgentNames(), Claude Code's built-in types); unique across skills
// (first wins).
func discoverPersonas(skillDirs []string, agentName string) ([]piPersona, []piSkippedPersona, error) {
	// Nothing here is fatal; the error return is kept for the call site's shape.
	builtinAgents := config.ValidAgentNames()
	// Claude Code built-in agent types (2.1.258 roster; re-check on a pin
	// bump): a persona of this name could never be dispatched by it.
	claudeBuiltins := []string{"claude", "general-purpose", "plan", "statusline-setup"}

	seen := map[string]string{} // persona name → skill that declared it
	var personas []piPersona
	var skipped []piSkippedPersona

	for _, skillDir := range skillDirs {
		subagentsDir := filepath.Join(skillDir, "sub-agents")
		entries, err := os.ReadDir(subagentsDir)
		if err != nil {
			if !os.IsNotExist(err) {
				// A file named sub-agents, a permission problem: not a
				// persona and not this run's concern.
				fmt.Fprintf(os.Stderr, "Warning: %s is not a readable directory, no personas from this skill: %v\n", subagentsDir, err)
			}
			continue
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
				skipped = append(skipped, piSkippedPersona{Name: basename, Path: path, Unparsed: true,
					Reason: fmt.Sprintf("cannot be read: %v", err)})
				continue
			}
			skip := func(model, reason string) {
				skipped = append(skipped, piSkippedPersona{Name: basename, Path: path, Model: model, Reason: reason})
			}
			def, err := parsePiAgent(data)
			if err != nil {
				// The model may have been set before the parse failed, so
				// it cannot be used to decide whether default would apply.
				skipped = append(skipped, piSkippedPersona{Name: basename, Path: path, Unparsed: true,
					Reason: fmt.Sprintf("frontmatter does not parse: %v", err)})
				continue
			}
			name := def.Name
			switch {
			case name == "":
				skip(def.Model, "frontmatter name: is required")
			case name != basename:
				skip(def.Model, fmt.Sprintf("frontmatter name %q must equal file basename %q", name, basename))
			case !config.ValidSubagentKey(name):
				skip(def.Model, "name must be lowercase alphanumeric segments joined by hyphens (max 64 chars)")
			case name == "agent":
				skip(def.Model, "name is reserved for anonymous sub-agent sessions")
			case slices.Contains(config.ReservedSubagentKeys(), name):
				skip(def.Model, "name is reserved")
			case strings.EqualFold(name, agentName):
				skip(def.Model, "name collides with the agent's own name")
			case slices.Contains(builtinAgents, name):
				skip(def.Model, "name collides with a built-in agent name")
			case slices.Contains(claudeBuiltins, name):
				skip(def.Model, "name collides with a Claude Code built-in agent type")
			default:
				if prevSkill, dup := seen[name]; dup {
					skip(def.Model, fmt.Sprintf("name already declared by skill %q", prevSkill))
					continue
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
	}

	slices.SortFunc(personas, func(a, b piPersona) int {
		return strings.Compare(a.Name, b.Name)
	})
	return personas, skipped, nil
}

// resolvePersonaModels resolves each persona's model using the
// resolution order: repo subagents.<persona> > frontmatter model
// (alias-resolved) > repo subagents.default > parent model. Each result is
// canonicalised and checked against the trusted closed set.
//
// skipped are the files discovery could not register; one becomes fatal
// only when subagentsCfg names it, or default is set and would have
// applied to it. A persona that fails here (Bash(...), unservable or empty
// tools, a model outside the closed set) is treated the same way: warned
// and left out of the manifest unless the config references it. Nothing
// unvalidated ever reaches the manifest either way.
// subagentsCfg is the merged agents[].subagents map from the config.
// modelsTable is the manifest's alias -> spec table.
// trustedSpecs is the set of model specs the run can serve.
//
// It also returns the canonicalised subagents.default spec. The manifest
// carries that separately so a child naming no persona can use it, which
// is what makes `subagents: {default: ...}` work for retro's anonymous
// children (#7031).
//
// A config key that names no discovered persona is an error (the
// issue's "repo's own layers" rule); keys from inherited/preset
// layers should warn instead, but layer provenance is not available
// here. Until it is, an ADR 0103 preset must not ship a subagents block.
func resolvePersonaModels(
	personas []piPersona,
	skipped []piSkippedPersona,
	subagentsCfg map[string]*string,
	modelsTable map[string]string,
	trustedSpecs map[string]string,
) (map[string]piPersonaManifestEntry, string, map[string]string, error) {
	result := make(map[string]piPersonaManifestEntry, len(personas))
	// skippedOut is every persona that did not register, by name, for the
	// manifest: a dispatch naming one is rejected with the reason instead
	// of falling through to an anonymous child with the parent's tools.
	skippedOut := make(map[string]string, len(skipped))
	for _, sk := range skipped {
		skippedOut[sk.Name] = sk.Reason
	}

	// Bare values resolve against the manifest's model table, as the
	// extension does. Not translatePiModel: it prefixes with
	// FULLSEND_PI_PROVIDER, turning a bare `opus` into
	// `xai-vertex/xai/claude-opus-4-6` under a Grok run.
	// Both tables come from the alias entries only, in sorted key order:
	// "default" is the agent's own model under whatever provider the run
	// selected, so its bare id must not shadow an alias entry's, and two
	// aliases sharing a trailing id must resolve the same way every run.
	byAlias := make(map[string]string, len(modelsTable))
	byBareID := make(map[string]string, len(modelsTable))
	aliases := make([]string, 0, len(modelsTable))
	for alias := range modelsTable {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	for _, alias := range aliases {
		spec := modelsTable[alias]
		if spec == "" || alias == "default" {
			continue
		}
		byAlias[strings.ToLower(alias)] = spec
		id := spec
		if i := strings.LastIndex(spec, "/"); i >= 0 {
			id = spec[i+1:]
		}
		if _, taken := byBareID[strings.ToLower(id)]; !taken {
			byBareID[strings.ToLower(id)] = spec
		}
	}

	// canonicalise resolves a model to a spec this run serves, or refuses
	// it. The "@suffix" strip mirrors piAgentModels: ValidModelRef admits
	// `opus@20250101`.
	canonicalise := func(what, model string) (string, error) {
		base, _, _ := strings.Cut(strings.TrimSpace(model), "@")
		base = strings.TrimSpace(base)
		spec := base
		if !strings.Contains(base, "/") {
			key := strings.ToLower(base)
			if s, ok := byAlias[key]; ok {
				spec = s
			} else if s, ok := byBareID[key]; ok {
				spec = s
			}
		} else if head, _, _ := strings.Cut(base, "/"); true {
			// A qualified spec names its own provider, so read it from the
			// string rather than the environment.
			if s, ok := normalizeXaiVertexModel(strings.ToLower(head), base); ok {
				spec = s
			}
		}
		if _, ok := trustedSpecs[strings.ToLower(spec)]; !ok {
			return "", fmt.Errorf("%s: resolved model %q is not available in this run; accepted: %s",
				what, spec, strings.Join(trustedSpecNames(trustedSpecs), ", "))
		}
		return trustedSpecs[strings.ToLower(spec)], nil
	}

	// Resolve the default subagent model, if configured.
	var defaultModel, defaultSpec string
	if v, ok := subagentsCfg["default"]; ok && v != nil {
		defaultModel = *v
		var err error
		if defaultSpec, err = canonicalise("subagents.default", defaultModel); err != nil {
			return nil, "", nil, err
		}
		fmt.Fprintf(os.Stderr, "subagents: default → %s (children that name no persona)\n", defaultSpec)
	}

	personaNames := make(map[string]bool, len(personas))
	for _, p := range personas {
		personaNames[p.Name] = true
	}
	skippedByName := make(map[string]piSkippedPersona, len(skipped))
	for _, sk := range skipped {
		skippedByName[sk.Name] = sk
	}

	// A config key must name a registered persona. This runs even when
	// discovery found none, so a typo is caught rather than silently
	// having no effect; a key naming a file discovery skipped is fatal,
	// because the repo asked for it.
	for key, val := range subagentsCfg {
		if key == "default" || val == nil {
			continue
		}
		if personaNames[key] {
			continue
		}
		if sk, ok := skippedByName[key]; ok {
			return nil, "", nil, fmt.Errorf("subagents.%s: persona %s was not registered: %s", key, sk.Path, sk.Reason)
		}
		discovered := "none"
		if len(personas) > 0 {
			discovered = strings.Join(discoveredPersonaNames(personas), ", ")
		}
		return nil, "", nil, fmt.Errorf("subagents.%s: no persona %q was discovered; discovered personas: %s",
			key, key, discovered)
	}
	// A file skipped at discovery that default would have applied to is
	// fatal too: the repo asked for a model on it.
	for _, sk := range skipped {
		if defaultModel != "" && sk.Model == "" && !sk.Unparsed {
			return nil, "", nil, fmt.Errorf("persona %s was not registered (%s) and subagents.default would have applied to it", sk.Path, sk.Reason)
		}
		fmt.Fprintf(os.Stderr, "Warning: persona %s skipped: %s\n", sk.Path, sk.Reason)
	}

	for _, p := range personas {
		// referenced: the repo's config decides this persona's model, so
		// a defect in it must fail the run rather than be skipped.
		referenced := subagentsCfg[p.Name] != nil || (defaultModel != "" && p.Model == "")
		skip := func(reason string) error {
			if referenced {
				return fmt.Errorf("persona %q: %s", p.Name, reason)
			}
			fmt.Fprintf(os.Stderr, "Warning: persona %q skipped: %s\n", p.Name, reason)
			skippedOut[p.Name] = reason
			return nil
		}

		// A Bash(...) allowlist would be recorded but not enforced on the
		// child; refuse it rather than honour it silently.
		if len(p.BashAllowlist) > 0 {
			if err := skip("a Bash(...) allowlist in persona frontmatter is not supported yet and would not restrict the child; declare plain `Bash` and restrict it on the agent instead"); err != nil {
				return nil, "", nil, err
			}
			continue
		}

		// Resolution order: repo subagents.<persona> > frontmatter model >
		// repo subagents.default > the parent's live model. `default` is a
		// floor for personas that say nothing, not an override of ones
		// that do. The source is echoed to stderr so a user can see why a
		// persona resolved as it did.
		model, source := "", "parent"
		switch {
		case subagentsCfg[p.Name] != nil:
			model, source = *subagentsCfg[p.Name], "subagents."+p.Name
		case p.Model != "":
			model, source = p.Model, "frontmatter"
		case defaultModel != "":
			model, source = defaultModel, "subagents.default"
		}

		var entry piPersonaManifestEntry
		if model != "" {
			spec, err := canonicalise(fmt.Sprintf("persona %q", p.Name), model)
			if err != nil {
				if err := skip(strings.TrimPrefix(err.Error(), fmt.Sprintf("persona %q: ", p.Name))); err != nil {
					return nil, "", nil, err
				}
				continue
			}
			entry.Model = spec
		}
		// An empty entry.Model means nothing configured this persona; the
		// extension then inherits the parent's *live* model, as an
		// anonymous child does.

		// Tools: the manifest carries the persona's declared set and the
		// extension intersects it with the parent's at dispatch.
		if p.Tools != nil {
			piTools, unsupported := piToolsFor(p.Tools)
			// Fail rather than drop: dropping every declared tool leaves
			// an empty set, which must never widen to the parent's.
			if len(unsupported) > 0 {
				if err := skip(fmt.Sprintf("tools %s cannot be served on the pi runtime; use one of %s",
					strings.Join(unsupported, ", "), strings.Join(piClaudeToolNames(), ", "))); err != nil {
					return nil, "", nil, err
				}
				continue
			}
			filtered := make([]string, 0, len(piTools))
			for _, t := range piTools {
				if t != piAgentToolName && t != piAgentToolAlias {
					filtered = append(filtered, t)
				}
			}
			if len(filtered) == 0 {
				if err := skip("declares an empty tool set; give it the tools it needs, or omit tools: to inherit the agent's"); err != nil {
					return nil, "", nil, err
				}
				continue
			}
			entry.Tools = filtered
		}

		resolvedSpec := entry.Model
		if resolvedSpec == "" {
			resolvedSpec = "<parent's model>"
		}
		fmt.Fprintf(os.Stderr, "subagents: %s → %s (from %s)\n", p.Name, resolvedSpec, source)
		result[p.Name] = entry
	}
	return result, defaultSpec, skippedOut, nil
}

// piClaudeToolNames returns the Claude-vocabulary tool names the pi runtime
// can serve, sorted, for the persona validation errors.
func piClaudeToolNames() []string {
	names := make([]string, 0, len(piToolForClaude))
	for k := range piToolForClaude {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// piTrustedSpecs is the closed set a persona's model is checked against:
// the alias table, every provider-model id, the agent definition's model
// and the model the parent actually runs on. The parent's effective model
// is included because an anonymous child may inherit it, so a persona
// pinned to the same spec must not be the one dispatch that fails. It is
// built before any persona is resolved and never widened by one.
func piTrustedSpecs(models map[string]string, providerModels map[string][]string, parentModel string, configAliases map[string]string) map[string]string {
	trusted := make(map[string]string)
	for _, spec := range models {
		trusted[strings.ToLower(spec)] = spec
	}
	for provider, ids := range providerModels {
		for _, id := range ids {
			full := provider + "/" + id
			trusted[strings.ToLower(full)] = full
		}
	}
	if p := strings.TrimSpace(parentModel); p != "" {
		// Canonicalised the way the parent itself is launched.
		spec := translatePiModel(p, configAliases)
		trusted[strings.ToLower(spec)] = spec
	}
	return trusted
}

// registeredPersonaNames lists the personas that made it into the
// manifest, sorted, for the runtime note.
func registeredPersonaNames(m map[string]piPersonaManifestEntry) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
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
