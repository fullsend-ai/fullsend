package mintcore

import "regexp"

// GitHubOrgPattern validates GitHub org/user names: alphanumeric or single
// hyphens, cannot start or end with a hyphen, max 39 characters.
var GitHubOrgPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

// RepoNamePattern validates individual repo names (no org prefix).
// GitHub allows repos starting with dot (e.g., .fullsend, .github).
var RepoNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.][a-zA-Z0-9._-]{0,99}$`)

// RolePattern restricts role to safe lowercase identifiers.
var RolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
