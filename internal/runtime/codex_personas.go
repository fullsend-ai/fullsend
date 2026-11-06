package runtime

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// codexDelegates is part one of the delegation gate: an agent gets native
// sub-agent roles when its tools include Agent (an absent tools: includes
// it, as on Claude Code and pi; Task is the alias codexToolMatcher maps to
// the same calls) and the harness has sandbox security enabled, because
// the spawn guard that admits only registered roles is installed with the
// hooks. Part two, codexParentCollabVersion, needs the pinned CLI's catalog
// and is decided in Bootstrap.
func codexDelegates(def *piAgentDef, securityEnabled bool) bool {
	return securityEnabled && piAgentToolEnabled(def)
}

// codexRole is one native Codex sub-agent role, written to
// $CODEX_HOME/agents/<Name>.toml. Instructions is the child's
// developer_instructions: the collaboration version's child note
// (codexChildNote, codexChildNoteV2), then the persona body; nothing of the
// dropped frontmatter (#6970).
type codexRole struct {
	Name, Description, Instructions string
	// Model is the child's model, set only for a skill persona with its own
	// subagents entry (the validated bare OpenAI id). Empty means no `model`
	// line: the child takes agents.default_subagent_model.
	Model string
}

// codexReservedRoleNames are persona names Codex treats specially: `enabled`
// is the [agents] table's own key; `explorer` and `worker` are Codex's
// built-in roles, accepted without a role file, which the spawn guard
// therefore denies. The runtime-neutral reserved list is discoverPersonas's.
var codexReservedRoleNames = []string{"enabled", "explorer", "worker"}

// codexDefaultSubagentModel is the last position of the child model chain
// (#6970): children run the cheap tier by default.
const codexDefaultSubagentModel = "gpt-5.6-luna"

// codexSubagentModelEnv is the runner-side run-level child model (#6970), read
// from the runner's own environment like FULLSEND_PI_SUBAGENT_THINKING.
const codexSubagentModelEnv = "FULLSEND_CODEX_SUBAGENT_MODEL"

// codexChildModel validates one configured child model and returns the bare
// OpenAI id: translateCodexModel's rules, less a gateway/ spec (a delegating
// run is on the direct route: codexParentCollabVersion declines a gateway/
// parent) and `inherit`, which codex does not resolve for a child. key names
// the entry so the error says what to fix.
func codexChildModel(key, value string) (string, error) {
	fix := "unset it so subagents.default or " + codexDefaultSubagentModel + " applies"
	if name, ok := strings.CutPrefix(key, "subagents."); ok {
		fix = fmt.Sprintf("set an OpenAI id (%s/<id>) or tombstone the entry (`%s: ~` under subagents:) so the run-level default applies",
			codexOpenAIProvider, name)
	}
	model, err := translateCodexModel(value, true)
	if err != nil || model.Gateway || strings.EqualFold(model.ID, "inherit") {
		return "", fmt.Errorf("%s: %q is not an OpenAI model id (%s/<id> or the bare id); %s", key, value, codexOpenAIProvider, fix)
	}
	return model.ID, nil
}

// codexSupportedEffort checks a child effort against a child model's
// supported reasoning levels in the bundled catalog: codex compares them
// exactly and refuses the spawn otherwise, with no clamping
// (validate_spawn_agent_reasoning_effort). key names the position that chose
// the model, effortSource where the effort came from.
func codexSupportedEffort(catalog codexCatalog, key, model, effort, effortSource string) error {
	efforts := catalog[model].Efforts
	if slices.Contains(efforts, effort) {
		return nil
	}
	supported := "none"
	if len(efforts) > 0 {
		supported = strings.Join(efforts, ", ")
	}
	return fmt.Errorf("%s: model %q does not support reasoning effort %q (%s), which codex would refuse at every spawn; it supports %s: set an effort every child model supports, or a child model that supports it",
		key, model, effort, effortSource, supported)
}

// codexListedModel is Bootstrap's catalog check: codex itself checks a
// run-level default only at each spawn and a role-file model not at all (the
// child fails on the provider's 404 instead), so every chain position is
// checked here, naming the position.
func codexListedModel(catalog codexCatalog, key, model string) error {
	if catalog.has(model) {
		return nil
	}
	return fmt.Errorf("%s: model %q is not in the bundled catalog of the pinned codex (`codex debug models --bundled`)", key, model)
}

// resolveCodexChildModels binds a persona's own subagents entry to its role
// and resolves the run-level default (FULLSEND_CODEX_SUBAGENT_MODEL, then
// subagents.default, then codexDefaultSubagentModel), which Bootstrap sets as
// agents.default_subagent_model. A nil entry is a tombstone and reads as
// absent; a key naming neither default nor a persona is an error, as on pi,
// carrying codexRoles's reason when the key names a skipped persona; every
// configured value is validated whichever position wins, and every
// resolved model must be in catalog (ADR 0126). The returned slice is a copy.
func resolveCodexChildModels(roles []codexRole, skipped map[string]string, subagents map[string]*string, envValue, runEffort string, catalog codexCatalog) ([]codexRole, string, string, error) {
	persona := make(map[string]bool, len(roles))
	for _, role := range roles {
		if !slices.Contains(config.ReservedSubagentKeys(), role.Name) {
			persona[role.Name] = true
		}
	}
	// Sorted, so a config with two bad keys names the same one every run.
	for _, key := range slices.Sorted(maps.Keys(subagents)) {
		if key == "default" || subagents[key] == nil || persona[key] {
			continue
		}
		discovered := "none"
		if len(persona) > 0 {
			discovered = strings.Join(slices.Sorted(maps.Keys(persona)), ", ")
		}
		if slices.Contains(config.ReservedSubagentKeys(), key) {
			return nil, "", "", fmt.Errorf("subagents.%s: %q is a built-in role, not a persona; it runs on the run-level child model; registered personas: %s",
				key, key, discovered)
		}
		if reason, ok := skipped[key]; ok {
			return nil, "", "", fmt.Errorf("subagents.%s: persona %q was not registered: %s", key, key, reason)
		}
		return nil, "", "", fmt.Errorf("subagents.%s: no persona %q is registered; registered personas: %s", key, key, discovered)
	}

	out := slices.Clone(roles)
	for i, role := range out {
		value := subagents[role.Name]
		if value == nil || !persona[role.Name] {
			continue
		}
		model, err := codexChildModel("subagents."+role.Name, *value)
		if err != nil {
			return nil, "", "", err
		}
		if err := codexListedModel(catalog, "subagents."+role.Name, model); err != nil {
			return nil, "", "", err
		}
		out[i].Model = model
	}

	defaultModel, source := codexDefaultSubagentModel, "built-in default"
	if value := subagents["default"]; value != nil {
		model, err := codexChildModel("subagents.default", *value)
		if err != nil {
			return nil, "", "", err
		}
		defaultModel, source = model, "subagents.default"
	}
	if strings.TrimSpace(envValue) != "" {
		model, err := codexChildModel(codexSubagentModelEnv, envValue)
		if err != nil {
			return nil, "", "", err
		}
		defaultModel, source = model, codexSubagentModelEnv
	}
	if err := codexListedModel(catalog, source, defaultModel); err != nil {
		return nil, "", "", err
	}

	// The child effort. Codex resets a child to the run-level child model's
	// catalog default unless agents.default_subagent_reasoning_effort is set
	// (child_config.rs, rust-v0.166.0), so the run's effort is written there
	// when the run has one. Either way codex checks the effort against the
	// run-level child model and against a persona's own model at spawn and
	// refuses the spawn when the model lacks it: each is checked here.
	effort, effortSource := "", ""
	if mapped, ok := codexEffortFor(runEffort); ok && mapped != "" {
		effort, effortSource = mapped, "the run's effort"
	}
	effective := effort
	if effective == "" {
		effective, effortSource = catalog[defaultModel].DefaultEffort, "the catalog default of "+defaultModel
	}
	if effort != "" {
		if err := codexSupportedEffort(catalog, source, defaultModel, effort, effortSource); err != nil {
			return nil, "", "", err
		}
	}
	if effective != "" {
		for _, role := range out {
			if role.Model == "" {
				continue
			}
			if err := codexSupportedEffort(catalog, "subagents."+role.Name, role.Model, effective, effortSource); err != nil {
				return nil, "", "", err
			}
		}
	}

	// Echoed as pi does, so a user sees why a child resolved as it did.
	fmt.Fprintf(os.Stderr, "subagents: default → %s (from %s; children that name no persona and personas without an entry)\n",
		defaultModel, source)
	for _, role := range out {
		if role.Model != "" {
			fmt.Fprintf(os.Stderr, "subagents: %s → %s (from subagents.%s)\n", role.Name, role.Model, role.Name)
		}
	}
	if effective != "" {
		fmt.Fprintf(os.Stderr, "subagents: effort → %s (from %s; every child)\n", effective, effortSource)
	}
	return out, defaultModel, effort, nil
}

// toml renders the role file: name, description, model for a persona with
// its own subagents entry (#6970) and developer_instructions, each through
// codexTOMLString, nothing else.
func (role codexRole) toml() []byte {
	model := ""
	if role.Model != "" {
		model = "model = " + codexTOMLString(role.Model) + "\n"
	}
	return []byte("name = " + codexTOMLString(role.Name) + "\n" +
		"description = " + codexTOMLString(role.Description) + "\n" +
		model +
		"developer_instructions = " + codexTOMLString(role.Instructions) + "\n")
}

// codexChildNote opens every V1 role's developer_instructions. A native
// child runs with its role's developer_instructions in place of the
// parent's, so nothing of codexNoSubagentNote reaches it: the note carries
// the child contract (pi's CHILD_SYSTEM_NOTE, word for word) and the
// chunked-read rule.
const codexChildNote = "## Runtime note\n\n" +
	"You are a sub-agent dispatched by another agent. Carry out the single task in the message you " +
	"were given and then stop. You have no sub-agent tool of your own and cannot dispatch further " +
	"work. Your final assistant message is the entire result handed back to the agent that " +
	"dispatched you, so make it self-contained.\n\n" +
	codexChunkedReadNote + "\n"

// codexChildNoteV2 is codexChildNote for a V2 child. The child inherits the
// feature table (ADR 0159 decision 1), and codex gives it the multi-agent
// tools only when its own model selects V2 in the catalog
// (collab_tools_enabled, codex-rs/core/src/tools/spec_plan.rs). Either way
// the hook denies every such call from a child (ADR 0159 decision 3), and
// the note says so where V1's says the child has no tool.
const codexChildNoteV2 = "## Runtime note\n\n" +
	"You are a sub-agent dispatched by another agent. Carry out the single task in the message you " +
	"were given and then stop. Do not spawn, wait on, list, message or interrupt agents; any such call " +
	"is denied. Your final assistant message is the entire result handed back to the agent that " +
	"dispatched you, so make it self-contained.\n\n" +
	codexChunkedReadNote + "\n"

// Built-in roles. `default` serves a spawn without agent_type, which is how
// retro's unnamed investigation tasks run; `explore` is Claude Code's
// Explore as an instruction to the child, not an enforced sandbox: the
// pinned Codex has no per-child tool restriction (#6970).
const (
	codexDefaultRoleDescription  = "Generic sub-agent for a task the parent describes in full in its message."
	codexDefaultRoleInstructions = "# Sub-agent\n\nDo the task in the message you were given, and only that task. " +
		"Put your findings, with file paths and line numbers where they apply, in your final message: " +
		"the parent owns the synthesis and the final result.\n"
	codexExploreRoleDescription  = "Investigation of the repository by reading only; reports findings. Instruction-only: nothing enforces read-only on a child."
	codexExploreRoleInstructions = "# Explore\n\nAnswer the question in the message you were given by reading the " +
		"repository: read files, search, and run commands that change nothing. Do not edit or create files. " +
		"Put what you found, with file paths and line numbers, in your final message.\n"
)

// codexRoles is the role list for a delegating agent: every skill persona
// discoverPersonas registered, plus the two built-ins, sorted by name. A
// persona that cannot become a role is skipped with a warning and never
// fails Bootstrap: a stray sub-agents/*.md must not stop a run that never
// dispatches it, the rule discoverPersonas already applies. The second
// value maps each skipped name to its reason, so a subagents key naming one
// fails with that reason, as on pi (resolveCodexChildModels). version is
// the collaboration version the gate admitted, which picks the child note.
func codexRoles(skillDirs []string, agentName, version string) ([]codexRole, map[string]string) {
	// discoverPersonas never returns a non-nil error; the value exists for
	// the pi call site's shape.
	personas, skipped, _ := discoverPersonas(skillDirs, agentName)
	reasons := make(map[string]string, len(skipped))
	for _, sk := range skipped {
		fmt.Fprintf(os.Stderr, "Warning: persona %q skipped: %s\n", sk.Path, sk.Reason)
		reasons[sk.Name] = sk.Path + ": " + sk.Reason
	}
	note := codexChildNote
	if version == "v2" {
		note = codexChildNoteV2
	}
	roles := []codexRole{
		{Name: "default", Description: codexDefaultRoleDescription, Instructions: note + codexDefaultRoleInstructions},
		{Name: "explore", Description: codexExploreRoleDescription, Instructions: note + codexExploreRoleInstructions},
	}
	for _, p := range personas {
		var reason string
		switch {
		case slices.Contains(codexReservedRoleNames, p.Name):
			reason = "name is reserved on codex (" + strings.Join(codexReservedRoleNames, ", ") + ")"
		case p.Description == "":
			reason = "frontmatter description: is required; the parent sees it in the spawn tool's role list"
		case p.Body == "":
			reason = "body is empty, so the role would carry no instructions"
		}
		if reason != "" {
			fmt.Fprintf(os.Stderr, "Warning: persona %q skipped: %s\n", p.Name, reason)
			reasons[p.Name] = reason
			continue
		}
		if keys := codexDroppedFrontmatterKeys(p.Frontmatter); len(keys) > 0 {
			fmt.Fprintf(os.Stderr, "Codex persona %q: frontmatter %s dropped; the frontmatter model is not applied on codex "+
				"(the role's model is its subagents entry, else the run-level child model) and the parent's tools apply\n",
				p.Name, strings.Join(keys, ", "))
		}
		roles = append(roles, codexRole{Name: p.Name, Description: p.Description, Instructions: note + p.Body + "\n"})
	}
	slices.SortFunc(roles, func(a, b codexRole) int { return strings.Compare(a.Name, b.Name) })
	return roles, reasons
}

// codexDroppedPersonaKeys are the Claude-specific frontmatter keys a role
// never carries (#6970). piAgentFrontmatter parses only the first two, so
// the raw frontmatter is read for the diagnostic.
var codexDroppedPersonaKeys = []string{"model", "tools", "permissionMode", "background", "maxTurns"}

// codexDroppedFrontmatterKeys returns which of codexDroppedPersonaKeys the
// frontmatter sets, in that order. A registered persona's frontmatter has
// parsed once already, so a parse failure here reports nothing.
func codexDroppedFrontmatterKeys(frontmatter []byte) []string {
	var raw map[string]any
	if err := yaml.Unmarshal(frontmatter, &raw); err != nil {
		return nil
	}
	var keys []string
	for _, k := range codexDroppedPersonaKeys {
		if _, ok := raw[k]; ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// codexCatalog is the pinned CLI's bundled model catalog reduced to what
// Bootstrap asks of it, read from `codex debug models --bundled`, which
// dumps the catalog compiled into the binary and makes no request.
type codexCatalog map[string]codexCatalogModel

// codexCatalogModel is one catalog entry: its multi_agent_version ("v1",
// "v2", "disabled", or "" when the entry sets none), its default_reasoning_level
// ("" for none) and the efforts of its supported_reasoning_levels, the set codex
// checks a child's effort against at spawn (validate_spawn_agent_reasoning_effort).
type codexCatalogModel struct {
	Version       string
	DefaultEffort string
	Efforts       []string
}

// has reports whether the catalog lists the model.
func (c codexCatalog) has(slug string) bool { _, ok := c[slug]; return ok }

// collabVersion is the collaboration version the gate takes from the model's
// catalog entry: "v1" for an entry that selects V1 or carries no version
// (codex's multi_agent feature, on by default, supplies V1), "v2" for an
// entry that selects V2, and "" where the gate takes none: an unlisted model
// and any other value, "disabled" included.
func (c codexCatalog) collabVersion(slug string) string {
	m, ok := c[slug]
	switch {
	case !ok:
		return ""
	case m.Version == "v1" || m.Version == "":
		return "v1"
	case m.Version == "v2":
		return "v2"
	default:
		return ""
	}
}

// parseCodexCatalog decodes the dump. Unknown keys are ignored; an empty
// model list is an error, because a binary with no catalog cannot run at all.
// A null multi_agent_version or default_reasoning_level is stored as "":
// encoding/json decodes null into a zero string, which is the "no version"
// collabVersion counts as V1.
func parseCodexCatalog(data []byte) (codexCatalog, error) {
	var dump struct {
		Models []struct {
			Slug                  string `json:"slug"`
			MultiAgentVersion     string `json:"multi_agent_version"`
			DefaultReasoningLevel string `json:"default_reasoning_level"`
			SupportedLevels       []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil, fmt.Errorf("decoding the model catalog: %w", err)
	}
	if len(dump.Models) == 0 {
		return nil, fmt.Errorf("the model catalog lists no model")
	}
	catalog := make(codexCatalog, len(dump.Models))
	for _, m := range dump.Models {
		efforts := make([]string, 0, len(m.SupportedLevels))
		for _, level := range m.SupportedLevels {
			efforts = append(efforts, level.Effort)
		}
		catalog[m.Slug] = codexCatalogModel{Version: m.MultiAgentVersion, DefaultEffort: m.DefaultReasoningLevel, Efforts: efforts}
	}
	return catalog, nil
}

// codexParentCollabVersion is part two of the delegation gate (ADR 0126,
// extended by ADR 0159): the collaboration version the gate takes for the
// parent Run will launch ("v1" or "v2", else ""), and the one-line reason
// Bootstrap prints when the parent runs in one context. Bootstrap delegates
// only a "v1" parent: the reason explains a decline and never decides one. A
// V2 parent is returned as "v2" with the ADR 0159 reason, and Bootstrap
// declines it until its child-result delivery path is implemented and
// verified live (ADR 0159 decision 4). A parent that does not translate is
// reported by Run with the same error; the gate only declines here. A
// gateway/ parent is declined whatever its id: children would inherit the
// gateway provider with bare OpenAI ids it may not serve, and native children
// have not run on that route.
func codexParentCollabVersion(catalog codexCatalog, parentModel, agentModel string) (version, reason string) {
	model, err := translateCodexModel(EffectiveModel(parentModel, agentModel), true)
	if err != nil {
		return "", err.Error()
	}
	id := model.ID
	if model.Gateway {
		return "", fmt.Sprintf("parent model %q takes the inference gateway route, where native sub-agents are not supported", "gateway/"+id)
	}
	if !catalog.has(id) {
		return "", fmt.Sprintf("parent model %q is not in the bundled catalog of the pinned codex", id)
	}
	switch catalog.collabVersion(id) {
	case "v1":
		return "v1", ""
	case "v2":
		return "v2", fmt.Sprintf("parent model %q selects collaboration \"v2\"; V2 delegation is off until its child-result delivery path is verified live (ADR 0159)", id)
	}
	if catalog[id].Version == "disabled" {
		return "", fmt.Sprintf("parent model %q has native sub-agents disabled in the bundled catalog", id)
	}
	return "", fmt.Sprintf("parent model %q selects collaboration %q, which the runner does not support", id, catalog[id].Version)
}
