package runtime

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// Persona routing on Claude Code (ADR 0104, #9692). With a live
// agents[].subagents entry, Bootstrap registers each skill persona
// (sub-agents/*.md) as a Claude Code agent, $CLAUDE_CONFIG_DIR/agents/<name>.md,
// whose model: is the one the runner resolved, in pi's order: repo
// subagents.<persona> > frontmatter model: > subagents.default > the
// parent's model (model: inherit). The orchestrator is told, in a runtime
// note appended to the agent definition, to dispatch a persona by
// subagent_type and omit model. Without a live entry nothing changes:
// no discovery, no persona files, no note.

// claudeSubagentsOptIn reports whether the repo's agents[].subagents map
// has at least one live entry. A map of tombstones says nothing, so it
// keeps today's behaviour, as no map does.
func claudeSubagentsOptIn(subagentsCfg map[string]*string) bool {
	for _, v := range subagentsCfg {
		if v != nil {
			return true
		}
	}
	return false
}

// claudePersonaFile is one persona Bootstrap writes as a Claude Code agent.
type claudePersonaFile struct {
	Name string
	// Model is the resolved model id; "" means the persona inherits the
	// parent's (model: inherit).
	Model  string
	Source string
	// Data is the agent file written to agents/<Name>.md.
	Data []byte
}

// claudePersonaCanonical is the model Claude Code is given for a persona's
// configured model: "" for inherit (the parent's model); on the gateway
// route the id after a gateway/ prefix, as --model gets it; otherwise
// claudeModelID (the models.aliases target, else the built-in default id
// for opus, sonnet and haiku, else the value as written).
func claudePersonaCanonical(model string, configAliases map[string]string, gateway bool) string {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "inherit") {
		return ""
	}
	if gateway {
		if id, ok := cutGatewayModel(remapModel(model, configAliases)); ok {
			return id
		}
	}
	return claudeModelID(model, configAliases)
}

// claudeTrustedModels is the closed set a persona's model is checked
// against, built before any persona is resolved and never widened by one.
// Off the gateway route it is what the run itself can resolve to: each
// alias's id (claudeModelID: a models.aliases target, the built-in default,
// or fable as Claude Code maps it), the built-in fallback chains, and the
// parent's own model. On the gateway route Claude Code sends every child
// to the gateway, whose model list it does not have, so only the parent's
// gateway id is trusted.
func claudeTrustedModels(configAliases map[string]string, parentModel string, gateway bool) map[string]string {
	trusted := map[string]string{}
	add := func(id string) {
		if config.ValidModelRef(id) {
			trusted[strings.ToLower(id)] = id
		}
	}
	parentModel = strings.TrimSpace(parentModel)
	if gateway {
		if id, ok := cutGatewayModel(remapModel(parentModel, configAliases)); ok {
			add(id)
		}
		return trusted
	}
	for _, alias := range config.ValidModelAliasKeys() {
		add(claudeModelID(alias, configAliases))
		for _, fb := range ImplicitFallbackModels(alias, configAliases) {
			add(claudeModelID(fb, configAliases))
		}
	}
	if parentModel != "" {
		add(claudeModelID(parentModel, configAliases))
	}
	return trusted
}

// claudePersonas discovers and resolves the personas for a Claude Code
// run, and returns the persona agent files and the agent definition with
// the runtime note appended. It returns agentData unchanged and no files
// when the repo has no live subagents entry, or the agent cannot dispatch
// (its tools: leave out Agent and Task; the whole subagents block is then
// ignored, as on pi, so its keys and models are not validated). A live
// subagents.default with no persona registered is an error.
func claudePersonas(input BootstrapInput, agentData []byte, gateway bool) ([]claudePersonaFile, []byte, error) {
	subagentsCfg := input.AgentSubagents()
	if !claudeSubagentsOptIn(subagentsCfg) {
		return nil, agentData, nil
	}
	parent, err := parsePiAgent(agentData)
	if err != nil {
		return nil, nil, fmt.Errorf("subagents: reading the agent definition: %w", err)
	}
	if !piAgentToolEnabled(parent) {
		fmt.Fprintf(os.Stderr, "subagents: ignored — the agent's tools: leave out Agent, so it dispatches no sub-agents\n")
		return nil, agentData, nil
	}
	parentModel := strings.TrimSpace(input.ParentModel())
	if parentModel == "" {
		parentModel = parent.Model
	}
	personas, skipped, _ := discoverPersonas(input.SkillDirs(), input.AgentName())
	trusted := claudeTrustedModels(input.ModelAliases(), parentModel, gateway)
	files, err := resolveClaudePersonas(personas, skipped, subagentsCfg, input.ModelAliases(), trusted, gateway, parent)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		// subagents.default reaches only registered personas on Claude Code;
		// children that name no persona keep Claude Code's own model
		// resolution. With no persona to apply it to, it would be accepted
		// and silently do nothing, so fail instead.
		if subagentsCfg["default"] != nil {
			return nil, nil, fmt.Errorf("subagents.default: no persona was registered for it to apply to, and Claude Code has no per-run default for children that name no persona; remove subagents.default, or add a persona under the agent's skills")
		}
		return nil, agentData, nil
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	out := append(bytes.TrimRight(slices.Clone(agentData), "\r\n"), claudePersonaNote(names)...)
	return files, out, nil
}

// claudePersonaNote tells the orchestrator which personas are registered
// and how to dispatch them. Skills choose their dispatch shape from the
// "Runtime note" at the end of the system prompt; without one they pass a
// model alias that Claude Code resolves with its own table.
func claudePersonaNote(names []string) string {
	return "\n\n## Runtime note\n\n" +
		"This agent runs on the Claude Code runtime. Registered sub-agent personas: " +
		strings.Join(names, ", ") + ". Dispatch a persona with the Agent tool by setting " +
		"`subagent_type` to the persona name; omit `model` — the runner resolves the correct " +
		"model for each persona, and a `model` argument would override it.\n"
}

// resolveClaudePersonas resolves each persona's model and tools and renders
// its agent file. The rules are pi's (resolvePersonaModels): a subagents key
// must name a registered persona; a file discovery skipped, or a persona
// that fails here, is fatal only when the config names it or
// subagents.default would apply to it, and is otherwise warned about and
// left out, so a dispatch naming it fails as an unknown agent type rather
// than running unrestricted. Every resolved model must be in trusted.
func resolveClaudePersonas(
	personas []piPersona,
	skipped []piSkippedPersona,
	subagentsCfg map[string]*string,
	configAliases map[string]string,
	trusted map[string]string,
	gateway bool,
	parent *piAgentDef,
) ([]claudePersonaFile, error) {
	canonicalise := func(what, model string) (string, error) {
		id := claudePersonaCanonical(model, configAliases, gateway)
		if id == "" {
			return "", nil
		}
		if t, ok := trusted[strings.ToLower(id)]; ok {
			return t, nil
		}
		accepted := "none"
		if len(trusted) > 0 {
			accepted = strings.Join(trustedSpecNames(trusted), ", ")
		}
		if gateway {
			return "", fmt.Errorf("%s: resolved model %q is not available in this run; on the Claude Code gateway route a sub-agent runs on the agent's own gateway model or inherits it; accepted: %s, inherit", what, id, accepted)
		}
		return "", fmt.Errorf("%s: resolved model %q is not available in this run; accepted: %s, inherit", what, id, accepted)
	}

	var defaultModel string
	if v := subagentsCfg["default"]; v != nil {
		defaultModel = *v
		id, err := canonicalise("subagents.default", defaultModel)
		if err != nil {
			return nil, err
		}
		if id == "" {
			id = "<parent's model>"
		}
		fmt.Fprintf(os.Stderr, "subagents: default → %s (personas that name no model)\n", id)
	}

	registered := make(map[string]bool, len(personas))
	for _, p := range personas {
		registered[p.Name] = true
	}
	skippedByName := make(map[string]piSkippedPersona, len(skipped))
	for _, sk := range skipped {
		skippedByName[sk.Name] = sk
	}
	for _, key := range slices.Sorted(maps.Keys(subagentsCfg)) {
		if key == "default" || subagentsCfg[key] == nil || registered[key] {
			continue
		}
		if sk, ok := skippedByName[key]; ok {
			return nil, fmt.Errorf("subagents.%s: persona %s was not registered: %s", key, sk.Path, sk.Reason)
		}
		discovered := "none"
		if len(personas) > 0 {
			discovered = strings.Join(discoveredPersonaNames(personas), ", ")
		}
		return nil, fmt.Errorf("subagents.%s: no persona %q was discovered; discovered personas: %s", key, key, discovered)
	}
	for _, sk := range skipped {
		if defaultModel != "" && sk.Model == "" && !sk.Unparsed {
			return nil, fmt.Errorf("persona %s was not registered (%s) and subagents.default would have applied to it", sk.Path, sk.Reason)
		}
		fmt.Fprintf(os.Stderr, "Warning: persona %s skipped: %s\n", sk.Path, sk.Reason)
	}

	agentAllowed, agentRestricted := claudeAgentDispatchAllowlist(parent.Frontmatter)
	parentDenied, err := claudeDisallowedToolSpecs(parent.Frontmatter)
	if err != nil {
		return nil, fmt.Errorf("subagents: reading the agent definition: %w", err)
	}

	var out []claudePersonaFile
	for _, p := range personas {
		referenced := subagentsCfg[p.Name] != nil || (defaultModel != "" && p.Model == "")
		skip := func(reason string) error {
			if referenced {
				return fmt.Errorf("persona %q: %s", p.Name, reason)
			}
			fmt.Fprintf(os.Stderr, "Warning: persona %q skipped: %s\n", p.Name, reason)
			return nil
		}

		if agentRestricted && !agentAllowed[p.Name] {
			if err := skip("the agent's tools: restrict Agent(...) to other sub-agent types, so Claude Code would refuse to dispatch it; add it to the agent's Agent(...) list"); err != nil {
				return nil, err
			}
			continue
		}

		if len(p.BashAllowlist) > 0 {
			if err := skip("a Bash(...) allowlist in persona frontmatter is not supported yet; declare plain `Bash` and restrict it on the agent instead"); err != nil {
				return nil, err
			}
			continue
		}

		model, source := piPersonaModelSource(p, subagentsCfg, defaultModel)
		id, err := canonicalise(fmt.Sprintf("persona %q", p.Name), model)
		if err != nil {
			if err := skip(strings.TrimPrefix(err.Error(), fmt.Sprintf("persona %q: ", p.Name))); err != nil {
				return nil, err
			}
			continue
		}

		tools, reason := claudePersonaTools(p, parent)
		if reason != "" {
			if err := skip(reason); err != nil {
				return nil, err
			}
			continue
		}

		denied, err := claudePersonaDisallowedTools(p, parentDenied)
		if err != nil {
			if err := skip(err.Error()); err != nil {
				return nil, err
			}
			continue
		}

		data, err := claudePersonaAgentFile(p, id, tools, denied)
		if err != nil {
			if err := skip(err.Error()); err != nil {
				return nil, err
			}
			continue
		}
		shown := id
		if shown == "" {
			shown = "<parent's model>"
		}
		fmt.Fprintf(os.Stderr, "subagents: %s → %s (from %s)\n", p.Name, shown, source)
		out = append(out, claudePersonaFile{Name: p.Name, Model: id, Source: source, Data: data})
	}
	return out, nil
}

// claudePersonaTools returns the tools: specs to write for persona p, nil
// to keep its frontmatter as it is, or a reason p cannot be registered. A
// persona's tools are never wider than the agent's: when the agent
// declares tools:, the persona gets the agent's specs (less Agent and
// Task) when it declares none, and otherwise those of its own tools the
// agent also has — the persona's spec where the agent's names the tool
// without arguments, else the agent's. Matching is by exact tool name.
// The agent's disallowedTools: are handled apart, by
// claudePersonaDisallowedTools.
func claudePersonaTools(p piPersona, parent *piAgentDef) ([]string, string) {
	if p.Tools != nil && len(p.Tools) == 0 {
		return nil, "declares an empty tool set; give it the tools it needs, or omit tools: to inherit the agent's"
	}
	if parent.Tools == nil {
		return nil, ""
	}
	parentSpecs := claudeToolSpecsByName(parent.Frontmatter)
	var out []string
	if p.Tools == nil {
		for _, name := range parent.Tools {
			if name != piAgentToolName && name != piAgentToolAlias {
				out = append(out, parentSpecs[name])
			}
		}
	} else {
		own := claudeToolSpecsByName(p.Frontmatter)
		var dropped []string
		for _, name := range p.Tools {
			if name == piAgentToolName || name == piAgentToolAlias {
				continue
			}
			ps, ok := parentSpecs[name]
			if !ok {
				dropped = append(dropped, name)
				continue
			}
			if ps == name {
				ps = own[name]
			}
			out = append(out, ps)
		}
		if len(dropped) > 0 {
			fmt.Fprintf(os.Stderr, "Warning: persona %q: tools %s dropped — the agent does not have them\n", p.Name, strings.Join(dropped, ", "))
		}
	}
	if len(out) == 0 {
		return nil, "has no tool the agent also has; a persona's tools are limited to the agent's"
	}
	return out, ""
}

// claudeDisallowedToolSpecs returns the specs in a frontmatter's
// disallowedTools:, a comma-separated string or a YAML list as for tools:.
func claudeDisallowedToolSpecs(frontmatter []byte) ([]string, error) {
	var fm struct {
		DisallowedTools any `yaml:"disallowedTools"`
	}
	if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
		return nil, fmt.Errorf("frontmatter does not parse: %w", err)
	}
	specs, err := piToolSpecs(fm.DisallowedTools)
	if err != nil {
		return nil, fmt.Errorf("disallowedTools: %w", err)
	}
	var out []string
	for _, spec := range specs {
		if spec = strings.TrimSpace(spec); spec != "" {
			out = append(out, spec)
		}
	}
	return out, nil
}

// claudePersonaDisallowedTools returns the disallowedTools: specs to write
// for persona p, nil to keep its frontmatter as it is. The agent's denials
// carry over to every persona, ahead of the persona's own, so a persona
// never has a tool the agent is denied, whether or not the agent declares
// tools:.
func claudePersonaDisallowedTools(p piPersona, parentDenied []string) ([]string, error) {
	if len(parentDenied) == 0 {
		return nil, nil
	}
	own, err := claudeDisallowedToolSpecs(p.Frontmatter)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, spec := range slices.Concat(parentDenied, own) {
		if !seen[spec] {
			seen[spec] = true
			out = append(out, spec)
		}
	}
	return out, nil
}

// claudeToolSpecsByName maps each tool name in a frontmatter's tools: to
// its spec as written ("Bash(gh,jq)" under "Bash"), first one winning, as
// parseClaudeToolSpecs keeps the first.
func claudeToolSpecsByName(frontmatter []byte) map[string]string {
	var fm piAgentFrontmatter
	out := map[string]string{}
	if yaml.Unmarshal(frontmatter, &fm) != nil {
		return out
	}
	specs, err := piToolSpecs(fm.Tools)
	if err != nil {
		return out
	}
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		name := spec
		if i := strings.IndexByte(spec, '('); i >= 0 && strings.HasSuffix(spec, ")") {
			name = strings.TrimSpace(spec[:i])
		}
		if _, ok := out[name]; name != "" && !ok {
			out[name] = spec
		}
	}
	return out
}

// claudeAgentDispatchAllowlist reports which sub-agent types the agent's
// tools: let it dispatch. restricted is true when every Agent/Task spec
// carries parentheses, as in Agent(Explore, worker): only the named types
// may then be dispatched, so an empty list, Agent(), allows none. A bare
// Agent or Task, or no such spec at all, leaves dispatch unrestricted.
func claudeAgentDispatchAllowlist(frontmatter []byte) (allowed map[string]bool, restricted bool) {
	var fm piAgentFrontmatter
	if yaml.Unmarshal(frontmatter, &fm) != nil {
		return nil, false
	}
	specs, err := piToolSpecs(fm.Tools)
	if err != nil {
		return nil, false
	}
	allowed = map[string]bool{}
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		name, args, parens := spec, "", false
		if i := strings.IndexByte(spec, '('); i >= 0 && strings.HasSuffix(spec, ")") {
			name = strings.TrimSpace(spec[:i])
			args = spec[i+1 : len(spec)-1]
			parens = true
		}
		if name != piAgentToolName && name != piAgentToolAlias {
			continue
		}
		if !parens {
			return nil, false
		}
		restricted = true
		for _, a := range strings.Split(args, ",") {
			if a = strings.TrimSpace(a); a != "" {
				allowed[a] = true
			}
		}
	}
	return allowed, restricted
}

// claudePersonaAgentFile renders persona p as a Claude Code agent file: its
// frontmatter with model: set to model (inherit when ""), tools: set to
// tools when non-nil, disallowedTools: set to disallowed when non-nil, and
// a description: when it has none (Claude Code does not load an agent
// without one), then its body.
func claudePersonaAgentFile(p piPersona, model string, tools, disallowed []string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(p.Frontmatter, &doc); err != nil {
		return nil, fmt.Errorf("frontmatter does not parse: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("frontmatter must be a YAML mapping")
	}
	mapping := doc.Content[0]
	set := func(key, value string) {
		node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
		found := false
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				mapping.Content[i+1] = node
				found = true
			}
		}
		if !found {
			mapping.Content = append(mapping.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
		}
	}
	if model == "" {
		model = "inherit"
	}
	set("model", model)
	if tools != nil {
		set("tools", strings.Join(tools, ", "))
	}
	if disallowed != nil {
		set("disallowedTools", strings.Join(disallowed, ", "))
	}
	if p.Description == "" {
		set("description", "Sub-agent persona "+p.Name)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("rendering frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("rendering frontmatter: %w", err)
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(buf.Bytes())
	out.WriteString("---\n\n")
	out.WriteString(p.Body)
	out.WriteString("\n")
	return out.Bytes(), nil
}
