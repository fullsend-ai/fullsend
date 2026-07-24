package config

import "strings"

// IsAgentExplicitlyDisabled returns true if the last config entry matching
// name has Enabled explicitly set to false. Iterates in reverse to respect
// last-writer-wins ordering.
func IsAgentExplicitlyDisabled(agents []AgentEntry, name string) bool {
	lower := strings.ToLower(name)
	for i := len(agents) - 1; i >= 0; i-- {
		if strings.ToLower(agents[i].DerivedName()) == lower {
			return !agents[i].IsEnabled()
		}
	}
	return false
}
