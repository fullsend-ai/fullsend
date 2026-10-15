package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// codexDelegates is part one of the ADR 0126 gate: an agent gets native
// sub-agent roles when its tools include Agent (an absent tools: includes
// it, as on Claude Code and pi; Task is the alias codexToolMatcher already
// maps to spawn_agent) and the harness has sandbox security enabled, because
// the spawn guard that admits only registered roles is installed with the
// hooks. Part two, codexParentResolvesV1, needs the pinned CLI's catalog and
// is decided in Bootstrap.
func codexDelegates(def *piAgentDef, securityEnabled bool) bool {
	return securityEnabled && piAgentToolEnabled(def)
}

// codexRole is one native Codex sub-agent role, written to
// $CODEX_HOME/agents/<Name>.toml. Instructions is the child's
// developer_instructions: codexChildNote, then the persona body; nothing of
// the dropped frontmatter (#6970).
type codexRole struct {
	Name, Description, Instructions string
}

// codexReservedRoleNames are persona names Codex treats specially: `enabled`
// is the [agents] table's own key; `explorer` and `worker` are Codex's
// built-in roles, accepted without a role file, which the spawn guard
// therefore denies. The runtime-neutral reserved list is discoverPersonas's.
var codexReservedRoleNames = []string{"enabled", "explorer", "worker"}

// toml renders the role file: name, description and developer_instructions,
// each through codexTOMLString, nothing else. No model line in this PR: ADR
// 0126's per-persona model arrives with the PR of #6970 that makes delegation
// reachable; default and explore never carry one.
func (role codexRole) toml() []byte {
	return []byte("name = " + codexTOMLString(role.Name) + "\n" +
		"description = " + codexTOMLString(role.Description) + "\n" +
		"developer_instructions = " + codexTOMLString(role.Instructions) + "\n")
}

// codexChildNote opens every role's developer_instructions. A native child
// runs with its role's developer_instructions in place of the parent's, so
// nothing of codexNoSubagentNote reaches it: the note carries the child
// contract (pi's CHILD_SYSTEM_NOTE, word for word) and the chunked-read rule.
const codexChildNote = "## Runtime note\n\n" +
	"You are a sub-agent dispatched by another agent. Carry out the single task in the message you " +
	"were given and then stop. You have no sub-agent tool of your own and cannot dispatch further " +
	"work. Your final assistant message is the entire result handed back to the agent that " +
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
// dispatches it, the rule discoverPersonas already applies.
func codexRoles(skillDirs []string, agentName string) []codexRole {
	// discoverPersonas never returns a non-nil error; the value exists for
	// the pi call site's shape.
	personas, skipped, _ := discoverPersonas(skillDirs, agentName)
	for _, sk := range skipped {
		fmt.Fprintf(os.Stderr, "Warning: persona %q skipped: %s\n", sk.Path, sk.Reason)
	}
	roles := []codexRole{
		{Name: "default", Description: codexDefaultRoleDescription, Instructions: codexChildNote + codexDefaultRoleInstructions},
		{Name: "explore", Description: codexExploreRoleDescription, Instructions: codexChildNote + codexExploreRoleInstructions},
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
			continue
		}
		if keys := codexDroppedFrontmatterKeys(p.Frontmatter); len(keys) > 0 {
			fmt.Fprintf(os.Stderr, "Codex persona %q: frontmatter %s dropped; a role carries no model or tool "+
				"restriction, so the run-level child model and the parent's tools apply\n",
				p.Name, strings.Join(keys, ", "))
		}
		roles = append(roles, codexRole{Name: p.Name, Description: p.Description, Instructions: codexChildNote + p.Body + "\n"})
	}
	slices.SortFunc(roles, func(a, b codexRole) int { return strings.Compare(a.Name, b.Name) })
	return roles
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
// Bootstrap asks of it: slug → multi_agent_version ("v1", "v2", or "" when
// the entry sets none), read from `codex debug models --bundled`, which
// dumps the catalog compiled into the binary and makes no request.
type codexCatalog map[string]string

// has reports whether the catalog lists the model.
func (c codexCatalog) has(slug string) bool { _, ok := c[slug]; return ok }

// resolvesV1 reports whether codex resolves the model to collaboration V1,
// the only version whose spawn tool the guard admits (ADR 0126): the entry
// selects V1, or carries no version and codex's multi_agent feature, on by
// default, supplies V1.
func (c codexCatalog) resolvesV1(slug string) bool {
	v, ok := c[slug]
	return ok && (v == "v1" || v == "")
}

// parseCodexCatalog decodes the dump. Unknown keys are ignored; an empty
// model list is an error, because a binary with no catalog cannot run at all.
// A null multi_agent_version is stored as "": encoding/json decodes null
// into a zero string, which is the "no version" resolvesV1 counts as V1.
func parseCodexCatalog(data []byte) (codexCatalog, error) {
	var dump struct {
		Models []struct {
			Slug              string `json:"slug"`
			MultiAgentVersion string `json:"multi_agent_version"`
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
		catalog[m.Slug] = m.MultiAgentVersion
	}
	return catalog, nil
}

// codexParentResolvesV1 is part two of the ADR 0126 gate. The parent is the
// model Run will launch — EffectiveModel(parentModel, agentModel), the
// expression Run launches with, translated by translateCodexModel — and
// codex must resolve it to collaboration V1 (resolvesV1). When it does not,
// a V2 or unlisted parent, the reason is returned for one stderr line and
// Bootstrap keeps codex's multi-agent tools off: the agent runs in one
// context, as every agent does today. A parent that does not translate is
// reported by Run with the same error; the gate only declines here. The
// route is Run's concern too: the catalog is asked about the id whichever
// provider serves it, so a gateway/ parent is accepted here and declined
// by the catalog.
func codexParentResolvesV1(catalog codexCatalog, parentModel, agentModel string) (bool, string) {
	model, err := translateCodexModel(EffectiveModel(parentModel, agentModel), true)
	if err != nil {
		return false, err.Error()
	}
	id := model.ID
	switch {
	case !catalog.has(id):
		return false, fmt.Sprintf("parent model %q is not in the bundled catalog of the pinned codex", id)
	case catalog.resolvesV1(id):
		return true, ""
	default:
		return false, fmt.Sprintf("parent model %q selects collaboration %q", id, catalog[id])
	}
}
