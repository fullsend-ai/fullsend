package mintcore

import (
	"strings"
)

// SplitCSV splits a comma-separated string into trimmed, non-empty entries.
// Shared by all entrypoints for parsing config fields like
// AllowedWorkflowFiles, PerRepoWIFRepos, and WorkflowHostRepos.
func SplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, entry := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
