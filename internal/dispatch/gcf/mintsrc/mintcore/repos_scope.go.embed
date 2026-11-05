package mintcore

import (
	"errors"
	"fmt"
	"strings"
)

// errPerRepoCrossRepo is a sentinel returned when a per-repo caller
// requests repos beyond its own repository. The handler checks this
// sentinel to decide whether to try repo-level FOREIGN grants.
var errPerRepoCrossRepo = errors.New("per-repo mint requires repos to be exactly the requesting repository")

// reposScopeShapeForeignRepoScoped is returned by validateReposScope for
// foreign (cross-org) requests with non-empty repos. The caller MUST perform
// repo-level FOREIGN grant authorization (via mintTokenCrossOrg) before
// minting. Treating this shape as "fully authorized" without the follow-up
// check would bypass authorization entirely.
const reposScopeShapeForeignRepoScoped = "foreign-repo-scoped"

// normalizeMintRepos treats a single "*" entry as an alias for an empty
// repos list (installation-wide scope). Since repos is now required,
// ["*"] is the only path to installation-wide scope.
func normalizeMintRepos(repos []string) []string {
	if len(repos) == 1 && repos[0] == "*" {
		return nil
	}
	return repos
}

// EnvTruthy reports whether v is a truthy feature-flag value.
func EnvTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// repositoryBareName returns the repository name without the org prefix.
func repositoryBareName(repository string) string {
	parts := strings.Split(repository, "/")
	return parts[len(parts)-1]
}

// validateReposScope enforces mint repos authorization after OIDC verification.
//
// For foreign (cross-org) requests with non-empty repos, this function returns
// reposScopeShapeForeignRepoScoped to signal that repo-level FOREIGN grant
// authorization is required. Callers MUST check for this shape and invoke
// mintTokenCrossOrg — treating a nil error alone as "fully authorized" would
// bypass repo-level authorization entirely. Foreign requests with empty repos
// (installation-wide) return an empty shape; org-level FOREIGN authorization
// is handled by mintTokenCrossOrg's own empty-repos path.
//
// Same-org requests must list exactly the requesting repository. Same-org
// installation-wide (empty repos) is always denied. Any other same-org
// list returns errPerRepoCrossRepo so the handler can consult repo-level
// FOREIGN grants for the requested repos.
func validateReposScope(isTargetForeign bool, requestingRepo string, repos []string) (shape string, err error) {
	if isTargetForeign {
		if len(repos) > 0 {
			// Non-empty repos → repo-scoped. Return the sentinel shape
			// so the caller knows repo-level FOREIGN grant authorization
			// is required (performed in mintTokenCrossOrg).
			return reposScopeShapeForeignRepoScoped, nil
		}
		// Empty repos → installation-wide (org-level FOREIGN grant
		// checked in mintTokenCrossOrg).
		return "", nil
	}

	if len(repos) == 0 {
		return "", fmt.Errorf("same-org mint requires non-empty repos")
	}

	bare := repositoryBareName(requestingRepo)
	if len(repos) == 1 && strings.EqualFold(repos[0], bare) {
		return "", nil
	}

	return "", errPerRepoCrossRepo
}
