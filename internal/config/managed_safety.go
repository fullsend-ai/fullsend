package config

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/urlutil"
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
	return CheckManagedSafetyGateFromLayerPairs(currentYAML, baseYAML, candidate, baseYAML)
}

// CheckManagedSafetyGateFromLayerPairs compares the current effective
// configuration (currentYAML over currentBaseYAML → code defaults) with
// the proposed one (candidate over candidateBaseYAML → code defaults).
// Use it when the base layer itself would change (a declared preset
// replacing an existing .fullsend/config.base.yaml), so a replacement
// base cannot bypass restrictions through overlay fallthrough. Only
// fields the candidate overlay declares locally count as explicit
// relaxations; a base change alone never does.
func CheckManagedSafetyGateFromLayerPairs(currentYAML, currentBaseYAML []byte, candidate PerRepoConfigWriter, candidateBaseYAML []byte) ([]SafetyRelaxation, error) {
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
	currentEff, err := LayerOnBase(currentLayer, currentBaseYAML)
	if err != nil {
		return nil, fmt.Errorf("layering config: %w", err)
	}
	candidateEff, err := LayerOnBase(candidate, candidateBaseYAML)
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
	if newly := newlyAdmitted(currentARR, candidateARR); setWidened(currentARR, candidateARR) && len(newly) > 0 {
		// A candidate prefix already covered by a current prefix (for
		// example a narrower path under an existing entry) admits
		// nothing new and is not reported.
		//
		// A non-empty local list is unioned with the parent chain, so a
		// base replacement can admit prefixes the overlay never named.
		// Each newly admitted prefix must be declared locally. Code
		// defaults are unioned beneath a non-empty overlay, but they
		// count as declared only when the overlay's own list is being
		// changed (the author re-declared the allowlist). When the
		// overlay list is unchanged, defaults newly reachable through a
		// base replacement (e.g. an explicit empty base list replaced by
		// one omitting the key) were never declared. A default already
		// reachable in the current chain is not newly admitted.
		var declared []string
		declaresLocally := local != nil && local.AllowedRemoteResources != nil
		if declaresLocally {
			declared = slices.Clone(local.AllowedRemoteResources)
			var currentLocal []string
			if cur, ok := current.(*perRepoConfig); ok && cur != nil {
				currentLocal = cur.AllowedRemoteResources
			}
			if !slices.Equal(currentLocal, local.AllowedRemoteResources) {
				declared = append(declared, DefaultAllowedRemoteResources()...)
			}
		}
		if !declaresLocally || !allPrefixesDeclared(newly, declared) {
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

	currentGW := current.ConfigInferenceGateway().Trimmed()
	candidateGW := candidate.ConfigInferenceGateway().Trimmed()
	var localGW InferenceGatewayConfig
	if local != nil && local.Inference != nil && local.Inference.Gateway != nil {
		localGW = local.Inference.Gateway.Trimmed()
	}
	// The gateway URL is the inference destination and the audience is
	// the OIDC assertion binding, so changing an established value (or
	// dropping it through the parent chain) must be declared locally.
	// First configuration (no current value) is not a change.
	if currentGW.URL != "" && candidateGW.URL != currentGW.URL && localGW.URL == "" {
		out = append(out, SafetyRelaxation{
			Key:       "inference.gateway.url",
			Current:   currentGW.URL,
			Candidate: formatUnset(candidateGW.URL),
		})
	}
	if currentGW.Audience != "" && candidateGW.Audience != currentGW.Audience && localGW.Audience == "" {
		out = append(out, SafetyRelaxation{
			Key:       "inference.gateway.audience",
			Current:   currentGW.Audience,
			Candidate: formatUnset(candidateGW.Audience),
		})
	}
	return out
}

func formatUnset(v string) string {
	if v == "" {
		return "unset"
	}
	return v
}

// newlyAdmitted returns the candidate entries not covered by any current
// entry. Coverage uses the runtime's allowlist matching (case-insensitive,
// percent-decoded, dot-segment-normalized prefix match), so a candidate
// that only narrows a current prefix admits nothing new. A candidate the
// runtime normalization cannot evaluate is never covered (fails closed).
func newlyAdmitted(current, candidate []string) []string {
	var out []string
	for _, s := range candidate {
		if slices.Contains(current, s) || urlutil.MatchingAllowedPrefixInList(s, current) != "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// allPrefixesDeclared reports whether every entry in prefixes is covered
// by declared. Coverage uses the same runtime allowlist matching as
// newlyAdmitted, so a declared broader prefix authorizes a narrower
// prefix under it. A prefix the runtime normalization cannot evaluate is
// covered only by an exact string match (fails closed).
func allPrefixesDeclared(prefixes, declared []string) bool {
	for _, p := range prefixes {
		if slices.Contains(declared, p) || urlutil.MatchingAllowedPrefixInList(p, declared) != "" {
			continue
		}
		return false
	}
	return true
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

// allowTargetsWidened reports whether candidate grants issue-creation access
// current does not. Orgs and repos are compared by what they grant, not
// list by list: a candidate repository whose owner is already covered by a
// current org entry admits nothing new, so replacing an org with one of its
// repositories is a narrowing.
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
	if setWidened(currentOrgs, candidateOrgs) {
		return true
	}
	for _, repo := range candidateRepos {
		if slices.Contains(currentRepos, repo) {
			continue
		}
		if owner, _, ok := strings.Cut(repo, "/"); ok && slices.Contains(currentOrgs, owner) {
			continue
		}
		return true
	}
	return false
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
