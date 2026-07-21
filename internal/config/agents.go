package config

import (
	"sort"
	"strings"
)

// MergedAgent represents an agent in the merged set produced by combining
// scaffold-discovered agents with config-registered agents. Config entries
// override scaffold entries with the same name (case-insensitive).
type MergedAgent struct {
	Name     string // canonical agent name
	Source   string // URL (with #sha256=), local path, or scaffold URL
	IsConfig bool   // true = from config agents list; false = scaffold default
}

// MergedAgents builds the merged agent set from scaffold defaults and config
// overlay. Scaffold entries are constructed from scaffoldNames using builder
// (typically scaffold.HarnessBaseURLWithHash); config entries overlay by
// DerivedName, replacing scaffold entries with matching names
// (case-insensitive). The result is sorted by Name.
//
// When builder is nil or commitSHA is empty, scaffold entries are omitted
// (config-only mode). This mirrors the DefaultAgentEntries nil-builder
// pattern.
func MergedAgents(scaffoldNames []string, commitSHA string, configAgents []AgentEntry, builder AgentEntryBuilder) ([]MergedAgent, error) {
	byName := make(map[string]*MergedAgent)
	var order []string

	if builder != nil && commitSHA != "" {
		for _, name := range scaffoldNames {
			url, err := builder(name, commitSHA)
			if err != nil {
				return nil, err
			}
			lower := strings.ToLower(name)
			byName[lower] = &MergedAgent{
				Name:   name,
				Source: url,
			}
			order = append(order, lower)
		}
	}

	inOrder := make(map[string]bool, len(order))
	for _, key := range order {
		inOrder[key] = true
	}

	for _, entry := range configAgents {
		name := entry.DerivedName()
		lower := strings.ToLower(name)

		// A disabled entry suppresses the agent from the merged set.
		// If it overrides a scaffold entry, that scaffold entry is removed.
		if !entry.IsEnabled() {
			delete(byName, lower)
			continue
		}

		if !inOrder[lower] {
			order = append(order, lower)
			inOrder[lower] = true
		}
		byName[lower] = &MergedAgent{
			Name:     name,
			Source:   entry.Source,
			IsConfig: true,
		}
	}

	result := make([]MergedAgent, 0, len(byName))
	for _, key := range order {
		if agent, ok := byName[key]; ok {
			result = append(result, *agent)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	return result, nil
}

// IsAgentExplicitlyDisabled returns true if the last config entry matching
// name has Enabled explicitly set to false. Iterates in reverse to respect
// last-writer-wins ordering (matching MergedAgents semantics).
func IsAgentExplicitlyDisabled(agents []AgentEntry, name string) bool {
	lower := strings.ToLower(name)
	for i := len(agents) - 1; i >= 0; i-- {
		if strings.ToLower(agents[i].DerivedName()) == lower {
			return !agents[i].IsEnabled()
		}
	}
	return false
}

// LookupMergedAgent finds an agent by name (case-insensitive) in the merged set.
// Returns nil if not found.
func LookupMergedAgent(agents []MergedAgent, name string) *MergedAgent {
	lower := strings.ToLower(name)
	for i := range agents {
		if strings.ToLower(agents[i].Name) == lower {
			return &agents[i]
		}
	}
	return nil
}
