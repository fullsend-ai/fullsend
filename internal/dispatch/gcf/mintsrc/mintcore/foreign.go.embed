package mintcore

import (
	"fmt"
	"strings"
)

const (
	foreignVarPrefix = "FULLSEND_FOREIGN_"
	foreignVarSuffix = "_REPOS"
)

// ForeignVariableName returns the org variable name for cross-org allowlist policy.
func ForeignVariableName(role string) string {
	return foreignVarPrefix + strings.ToUpper(role) + foreignVarSuffix
}

// ParseForeignAllowlist splits a comma-separated FOREIGN variable value into entries.
func ParseForeignAllowlist(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, entry := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// CallerAllowed reports whether repository/repositoryOwner matches an allowlist entry.
// Entries with a slash match the repository claim exactly; bare org names match repository_owner.
func CallerAllowed(allowlist []string, repository, repositoryOwner string) bool {
	for _, entry := range allowlist {
		if strings.Contains(entry, "/") {
			if strings.EqualFold(entry, repository) {
				return true
			}
		} else if strings.EqualFold(entry, repositoryOwner) {
			return true
		}
	}
	return false
}

// foreignCacheKey builds a cache key for target org + role policy lookups.
func foreignCacheKey(targetOrg, role string) string {
	return strings.ToLower(targetOrg) + "/" + strings.ToLower(role)
}

// validateTargetOrg checks target_org when cross-org mint is requested.
func validateTargetOrg(targetOrg string) error {
	if err := ValidateOrgName(targetOrg); err != nil {
		return fmt.Errorf("invalid target_org: %w", err)
	}
	return nil
}
