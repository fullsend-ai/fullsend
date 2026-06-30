package mintcore

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// maxClockSkew is the maximum allowed clock skew when validating JWT
// timestamps (exp, iat). Used by both STSVerifier and JWKSVerifier.
const maxClockSkew = 30 * time.Second

// GitHubOrgPattern validates GitHub org/user names: alphanumeric or single
// hyphens, cannot start or end with a hyphen, max 39 characters.
var GitHubOrgPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

// RepoNamePattern validates individual repo names (no org prefix).
// GitHub allows repos starting with dot (e.g., .fullsend, .github).
var RepoNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.][a-zA-Z0-9._-]{0,99}$`)

// RolePattern restricts role to safe lowercase identifiers.
var RolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ValidateOrgName checks that an org name matches GitHubOrgPattern and
// does not contain double-hyphens (which would be ambiguous in secret names).
func ValidateOrgName(org string) error {
	if !GitHubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
		return fmt.Errorf("invalid org name %q", org)
	}
	return nil
}

// ValidateRoleName checks that a role name matches RolePattern and
// does not contain double-hyphens.
func ValidateRoleName(role string) error {
	if !RolePattern.MatchString(role) || strings.Contains(role, "--") {
		return fmt.Errorf("invalid role name %q", role)
	}
	return nil
}
