package config

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// SafetyRelaxation is one candidate value that is less restrictive than
// the current effective configuration without an explicit local
// declaration in the managed configuration (ADR 0122).
type SafetyRelaxation struct {
	Key       string
	Current   string
	Candidate string
}

func (r SafetyRelaxation) String() string {
	return fmt.Sprintf("%s (current %s, candidate %s; not explicitly declared)", r.Key, r.Current, r.Candidate)
}

// FormatSafetyRelaxations joins relaxations for error and status output.
func FormatSafetyRelaxations(rs []SafetyRelaxation) string {
	if len(rs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, "; ")
}

// SafetyRelaxationKeys returns the affected configuration keys in order.
func SafetyRelaxationKeys(rs []SafetyRelaxation) []string {
	keys := make([]string, len(rs))
	for i, r := range rs {
		keys[i] = r.Key
	}
	return keys
}

// CheckManagedSafetyGateFromLayers layers currentYAML (the installed
// .fullsend/config.yaml, or empty when the file is missing) and candidate
// onto the same parent chain (baseYAML → code defaults) and reports
// implicit relaxations. Omitted candidate keys fall through the parent
// chain rather than being ignored because the sparse file lacks them.
func CheckManagedSafetyGateFromLayers(currentYAML []byte, candidate PerRepoConfigWriter, baseYAML []byte) ([]SafetyRelaxation, error) {
	var currentLayer PerRepoConfigWriter
	if len(bytes.TrimSpace(currentYAML)) == 0 {
		currentLayer = NewEmptyPerRepoOverlay()
	} else {
		parsed, err := ParsePerRepoConfigWriter(currentYAML)
		if err != nil {
			return nil, fmt.Errorf("parsing current config: %w", err)
		}
		currentLayer = parsed
	}
	currentEff, err := LayerOnBase(currentLayer, baseYAML)
	if err != nil {
		return nil, fmt.Errorf("layering config: %w", err)
	}
	candidateEff, err := LayerOnBase(candidate, baseYAML)
	if err != nil {
		return nil, fmt.Errorf("layering config: %w", err)
	}
	return CheckManagedSafetyGate(currentEff, candidateEff), nil
}

// CheckManagedSafetyGate compares current and candidate effective
// configurations (both already layered managed → base → code defaults).
// A less-restrictive candidate is reported unless candidate declares
// that relaxation locally. candidate must be the layered *perRepoConfig
// so omitted vs explicit local fields can be distinguished.
func CheckManagedSafetyGate(current PerRepoConfigReader, candidate PerRepoConfigWriter) []SafetyRelaxation {
	if current == nil || candidate == nil {
		// A missing comparison operand must never read as "no
		// relaxations" — callers treat an empty result as "gate passed"
		// and allow the write. Fail closed with a synthetic relaxation
		// naming the gate itself instead of silently permitting it.
		return []SafetyRelaxation{{
			Key:       "managed_safety_gate",
			Current:   "unavailable",
			Candidate: "unavailable",
		}}
	}
	local := asPerRepo(candidate)
	var out []SafetyRelaxation

	if current.IsKillSwitchActive() && !candidate.IsKillSwitchActive() {
		if local == nil || local.KillSwitch == nil {
			out = append(out, SafetyRelaxation{
				Key:       "kill_switch",
				Current:   "true",
				Candidate: "false",
			})
		}
	}

	currentRoles := current.ConfigRoles()
	candidateRoles := candidate.ConfigRoles()
	if setWidened(currentRoles, candidateRoles) {
		if local == nil || local.Roles == nil {
			out = append(out, SafetyRelaxation{
				Key:       "roles",
				Current:   formatStringList(currentRoles),
				Candidate: formatStringList(candidateRoles),
			})
		}
	}

	currentARR := current.AllowedResources()
	candidateARR := candidate.AllowedResources()
	if setWidened(currentARR, candidateARR) {
		if local == nil || local.AllowedRemoteResources == nil {
			out = append(out, SafetyRelaxation{
				Key:       "allowed_remote_resources",
				Current:   formatAllowlist(currentARR),
				Candidate: formatAllowlist(candidateARR),
			})
		}
	}

	currentAgents := current.AgentEntries()
	candidateAgents := candidate.AgentEntries()
	var localAgents []AgentEntry
	if local != nil {
		localAgents = local.LocalAgentEntries()
	}
	seenAgentNames := make(map[string]struct{}, len(currentAgents))
	for _, a := range currentAgents {
		name := a.DerivedName()
		key := strings.ToLower(name)
		if _, done := seenAgentNames[key]; done {
			// Duplicate same-name entries only occur when the raw local
			// list is returned unmerged (no parent agents to merge
			// against); evaluate each distinct name once, using
			// last-writer-wins, rather than once per raw entry.
			continue
		}
		seenAgentNames[key] = struct{}{}

		if !IsAgentExplicitlyDisabled(currentAgents, name) {
			continue
		}
		if agentEntryPresent(candidateAgents, name) {
			if IsAgentExplicitlyDisabled(candidateAgents, name) {
				continue
			}
		} else if !isBuiltinAgentName(name) {
			// A custom (source-declared, non-built-in) agent that is
			// entirely absent from the candidate was removed, not
			// re-enabled. Built-in agents keep resolving through the
			// agents-repo fallback even without an entry, so their
			// absence still relaxes the suppression.
			continue
		}
		if localAgentExplicitlyEnabled(localAgents, name) {
			continue
		}
		out = append(out, SafetyRelaxation{
			Key:       "agents." + name + ".enabled",
			Current:   "false",
			Candidate: "true",
		})
	}

	currentCI := current.IssueCreationConfig()
	candidateCI := candidate.IssueCreationConfig()
	if allowTargetsWidened(currentCI, candidateCI) {
		if local == nil || local.CreateIssues == nil {
			out = append(out, SafetyRelaxation{
				Key:       "create_issues.allow_targets",
				Current:   formatAllowTargets(currentCI),
				Candidate: formatAllowTargets(candidateCI),
			})
		}
	}
	return out
}

func setWidened(current, candidate []string) bool {
	if len(candidate) == 0 {
		return false
	}
	if len(current) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(current))
	for _, s := range current {
		have[s] = struct{}{}
	}
	for _, s := range candidate {
		if _, ok := have[s]; !ok {
			return true
		}
	}
	return false
}

// agentEntryPresent reports whether any entry in agents matches name,
// regardless of its enabled state. Used to distinguish "absent" (removal)
// from "present but enabled" when the agent was disabled in current.
func agentEntryPresent(agents []AgentEntry, name string) bool {
	lower := strings.ToLower(name)
	for _, a := range agents {
		if strings.ToLower(a.DerivedName()) == lower {
			return true
		}
	}
	return false
}

// isBuiltinAgentName reports whether name is one of the built-in agents
// fullsend dispatches by name (ValidAgentNames). Built-in agents keep
// resolving through the agents-repo fallback even without a config entry,
// so their absence from a candidate's agent list is not a removal.
func isBuiltinAgentName(name string) bool {
	return slices.Contains(ValidAgentNames(), strings.ToLower(name))
}

// localAgentExplicitlyEnabled reports whether the last entry matching name
// in local has Enabled explicitly set to true. Iterates in reverse to
// respect last-writer-wins ordering, like IsAgentExplicitlyDisabled.
func localAgentExplicitlyEnabled(local []AgentEntry, name string) bool {
	lower := strings.ToLower(name)
	for i := len(local) - 1; i >= 0; i-- {
		if strings.ToLower(local[i].DerivedName()) == lower {
			return local[i].Enabled != nil && *local[i].Enabled
		}
	}
	return false
}

func allowTargetsWidened(current, candidate *CreateIssuesConfig) bool {
	var currentOrgs, currentRepos, candidateOrgs, candidateRepos []string
	if current != nil {
		currentOrgs = current.AllowTargets.Orgs
		currentRepos = current.AllowTargets.Repos
	}
	if candidate != nil {
		candidateOrgs = candidate.AllowTargets.Orgs
		candidateRepos = candidate.AllowTargets.Repos
	}
	return setWidened(currentOrgs, candidateOrgs) || setWidened(currentRepos, candidateRepos)
}

func formatStringList(values []string) string {
	if values == nil {
		return "unset"
	}
	if len(values) == 0 {
		return "[]"
	}
	return "[" + strings.Join(values, ", ") + "]"
}

func formatAllowlist(values []string) string {
	if len(values) == 0 {
		return "deny-all"
	}
	return formatStringList(values)
}

func formatAllowTargets(ci *CreateIssuesConfig) string {
	if ci == nil {
		return "unset"
	}
	return "orgs=" + formatStringList(ci.AllowTargets.Orgs) + " repos=" + formatStringList(ci.AllowTargets.Repos)
}
