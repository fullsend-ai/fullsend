package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// GitHubPermissionFlags are the effective repository capabilities returned in
// the user.permissions object of GitHub's collaborator permission response.
// Pointers distinguish an explicit false value from a malformed missing field.
type GitHubPermissionFlags struct {
	Admin    *bool `json:"admin"`
	Maintain *bool `json:"maintain"`
	Push     *bool `json:"push"`
	Triage   *bool `json:"triage"`
	Pull     *bool `json:"pull"`
}

// GitHubPermissionUser is the user portion of GitHub's collaborator
// permission response.
type GitHubPermissionUser struct {
	Permissions        *GitHubPermissionFlags `json:"permissions,omitempty"`
	permissionsPresent bool
}

// UnmarshalJSON preserves whether permissions was absent or explicitly null.
// GitHub may omit the field, which permits conservative legacy fallback, but
// a present null value is malformed and must fail closed.
func (u *GitHubPermissionUser) UnmarshalJSON(data []byte) error {
	*u = GitHubPermissionUser{}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("user must be an object")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode user: %w", err)
	}
	raw, ok := fields["permissions"]
	if !ok {
		return nil
	}
	u.permissionsPresent = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}

	var permissions GitHubPermissionFlags
	if err := json.Unmarshal(raw, &permissions); err != nil {
		return fmt.Errorf("decode user.permissions: %w", err)
	}
	u.Permissions = &permissions
	return nil
}

// GitHubCollaboratorPermission is GitHub's calculated repository permission
// response. Permission is the legacy base role, while RoleName may contain an
// organization-defined custom role name.
type GitHubCollaboratorPermission struct {
	Permission string               `json:"permission"`
	RoleName   string               `json:"role_name"`
	User       GitHubPermissionUser `json:"user"`
}

// ResolveGitHubCollaboratorPermission returns a built-in GitHub base role from
// a collaborator permission response. Custom role names are never trusted;
// they require GitHub's effective permission fields. Contradictory or
// malformed signals fail closed with an error.
func ResolveGitHubCollaboratorPermission(p GitHubCollaboratorPermission) (string, error) {
	roleName := normalizeGitHubPermission(p.RoleName)
	if roleName == "" {
		return "", fmt.Errorf("missing role_name")
	}

	legacy := normalizeGitHubPermission(p.Permission)
	if legacy != "" && !isGitHubLegacyPermission(legacy) {
		return "", fmt.Errorf("unknown legacy permission %q", p.Permission)
	}

	permissionsPresent := p.User.permissionsPresent || p.User.Permissions != nil
	if permissionsPresent && p.User.Permissions == nil {
		return "", fmt.Errorf("user.permissions is missing required boolean fields")
	}

	if isGitHubBaseRole(roleName) {
		if legacy != "" && !githubPermissionSignalsCompatible(roleName, legacy) {
			return "", fmt.Errorf("role_name %q conflicts with permission %q", p.RoleName, p.Permission)
		}
		if permissionsPresent {
			flagsRole, err := resolveGitHubPermissionFlags(p.User.Permissions)
			if err != nil {
				return "", err
			}
			if flagsRole != roleName {
				return "", fmt.Errorf("role_name %q conflicts with user.permissions role %q", p.RoleName, flagsRole)
			}
		}
		return roleName, nil
	}

	if !permissionsPresent {
		if legacy == "" {
			return "", fmt.Errorf("custom role_name %q has no effective permission", p.RoleName)
		}
		return legacy, nil
	}

	flagsRole, err := resolveGitHubPermissionFlags(p.User.Permissions)
	if err != nil {
		return "", err
	}
	if legacy != "" && !githubPermissionSignalsCompatible(flagsRole, legacy) {
		return "", fmt.Errorf("user.permissions role %q conflicts with permission %q", flagsRole, p.Permission)
	}
	return flagsRole, nil
}

func resolveGitHubPermissionFlags(flags *GitHubPermissionFlags) (string, error) {
	if flags == nil || flags.Admin == nil || flags.Maintain == nil || flags.Push == nil || flags.Triage == nil || flags.Pull == nil {
		return "", fmt.Errorf("user.permissions is missing required boolean fields")
	}

	admin := *flags.Admin
	maintain := *flags.Maintain
	push := *flags.Push
	triage := *flags.Triage
	pull := *flags.Pull
	if (admin && !(maintain && push && triage && pull)) ||
		(maintain && !(push && triage && pull)) ||
		(push && !(triage && pull)) ||
		(triage && !pull) {
		return "", fmt.Errorf("user.permissions contains contradictory capability flags")
	}

	switch {
	case admin:
		return "admin", nil
	case maintain:
		return "maintain", nil
	case push:
		return "write", nil
	case triage:
		return "triage", nil
	case pull:
		return "read", nil
	default:
		return "none", nil
	}
}

func normalizeGitHubPermission(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isGitHubBaseRole(role string) bool {
	switch role {
	case "admin", "maintain", "write", "triage", "read", "none":
		return true
	default:
		return false
	}
}

func isGitHubLegacyPermission(permission string) bool {
	switch permission {
	case "admin", "write", "read", "none":
		return true
	default:
		return false
	}
}

func githubPermissionSignalsCompatible(role, legacy string) bool {
	switch role {
	case "admin":
		return legacy == "admin"
	case "maintain", "write":
		return legacy == "write"
	case "triage", "read":
		return legacy == "read"
	case "none":
		return legacy == "none"
	default:
		return false
	}
}
