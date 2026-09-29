package mintcore

import "testing"

func TestRoleIdentifier(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{"triage", "TRIAGE"},
		{"ci-check", "CI_CHECK"},
		{"deploy-prod", "DEPLOY_PROD"},
		{"my-role", "MY_ROLE"},
		{"my_role", "MY_ROLE"},
		{"my-custom_role", "MY_CUSTOM_ROLE"},
		{"e2e", "E2E"},
	}
	for _, tc := range tests {
		if got := RoleIdentifier(tc.role); got != tc.want {
			t.Errorf("RoleIdentifier(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
}
