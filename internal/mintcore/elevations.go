package mintcore

import "fmt"

type elevationGate struct {
	permissions map[string]string
	roles       map[string]bool
}

var elevationGates = map[string]elevationGate{
	"workflow-change": {
		permissions: map[string]string{"workflows": "write"},
		roles:       map[string]bool{"coder": true},
	},
}

// MergeRoleElevations returns a copy of the role's base permissions with
// requested gate elevations merged in. Unknown gates or disallowed role/gate
// combinations return an error.
func MergeRoleElevations(role string, elevations []string) (map[string]string, error) {
	perms := RolePermissionsFor(role)
	if perms == nil {
		return nil, fmt.Errorf("no permissions defined for role %q", role)
	}
	if len(elevations) == 0 {
		return perms, nil
	}

	merged := make(map[string]string, len(perms)+len(elevations))
	for k, v := range perms {
		merged[k] = v
	}

	for _, name := range elevations {
		gate, ok := elevationGates[name]
		if !ok {
			return nil, fmt.Errorf("unknown elevation gate %q", name)
		}
		if !gate.roles[role] {
			return nil, fmt.Errorf("elevation %q is not allowed for role %q", name, role)
		}
		for perm, level := range gate.permissions {
			merged[perm] = level
		}
	}
	return merged, nil
}
