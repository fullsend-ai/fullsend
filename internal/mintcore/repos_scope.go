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

// Shape labels returned by validateReposScope. A non-empty shape signals that
// the caller must take path-specific action (see each constant's doc).
const (
	// reposScopeShapeForeignRepoScoped is returned for foreign (cross-org)
	// requests with non-empty repos. The caller MUST perform repo-level
	// FOREIGN grant authorization (via mintTokenCrossOrg) before minting.
	// Treating this shape as "fully authorized" without the follow-up check
	// would bypass authorization entirely.
	reposScopeShapeForeignRepoScoped = "foreign-repo-scoped"

	reposScopeShapeFullsendAny      = "fullsend-any"
	reposScopeShapeEnrolledFullsend = "enrolled-fullsend"
	reposScopeShapeEnrolledPair     = "enrolled-pair"
)

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
// Same-org requests differ based on whether the caller is per-repo or per-org:
//
//   - Per-repo callers (perRepo=true): must list exactly the requesting
//     repository. No broader shapes are allowed.
//   - Per-org callers (perRepo=false): may use org-mode shapes:
//     caller .fullsend: any non-empty validated list;
//     other callers: exactly [.fullsend] or {requestingBare, .fullsend}.
//     Same-org installation-wide (empty repos) is always denied.
//
// On success, a non-empty shape signals a path-specific authorization
// requirement (foreign-repo-scoped or org-mode exception).
func validateReposScope(isTargetForeign bool, requestingRepo string, repos []string, perRepo bool) (shape string, err error) {
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

	if perRepo {
		return "", errPerRepoCrossRepo
	}

	// Per-org callers get org-mode shapes.
	if strings.EqualFold(bare, ".fullsend") {
		return reposScopeShapeFullsendAny, nil
	}

	if len(repos) == 1 && strings.EqualFold(repos[0], ".fullsend") {
		return reposScopeShapeEnrolledFullsend, nil
	}

	if len(repos) == 2 {
		a, b := repos[0], repos[1]
		if (strings.EqualFold(a, bare) && strings.EqualFold(b, ".fullsend")) ||
			(strings.EqualFold(b, bare) && strings.EqualFold(a, ".fullsend")) {
			return reposScopeShapeEnrolledPair, nil
		}
	}

	return "", fmt.Errorf("repos scope not allowed for per-org caller")
}
