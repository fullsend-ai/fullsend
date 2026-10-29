package e2eauth

import "fmt"

// ResolveCollaboratorRole maps GitHub collaborator permission API fields to a
// canonical role string for the e2e CI gate. Built-in role_name values are
// cross-checked against permission; custom roles fall back to permission alone.
func ResolveCollaboratorRole(roleName, permission string) (string, error) {
	if permission == "" {
		return "", fmt.Errorf("missing permission field")
	}
	switch roleName {
	case "admin":
		if permission == "admin" {
			return roleName, nil
		}
		return "none", nil
	case "maintain", "write":
		if permission == "write" {
			return roleName, nil
		}
		return "none", nil
	case "triage", "read":
		if permission == "read" {
			return roleName, nil
		}
		return "none", nil
	default:
		switch permission {
		case "admin", "write", "read", "none":
			return permission, nil
		default:
			return "", fmt.Errorf("unknown permission %q", permission)
		}
	}
}

func isWriteRole(role string) bool {
	switch role {
	case "admin", "maintain", "write":
		return true
	default:
		return false
	}
}
