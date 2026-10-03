package forge

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type githubPermissionFixture struct {
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	Want      string          `json:"want"`
	WantError bool            `json:"want_error"`
}

func loadGitHubPermissionFixtures(t *testing.T) []githubPermissionFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/github_permission_cases.json")
	require.NoError(t, err)
	var fixtures []githubPermissionFixture
	require.NoError(t, json.Unmarshal(data, &fixtures))
	return fixtures
}

func TestResolveGitHubCollaboratorPermission_SharedFixtures(t *testing.T) {
	for _, fixture := range loadGitHubPermissionFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			var permission GitHubCollaboratorPermission
			decodeErr := json.Unmarshal(fixture.Payload, &permission)
			if fixture.WantError && decodeErr != nil {
				return
			}
			require.NoError(t, decodeErr)
			got, err := ResolveGitHubCollaboratorPermission(permission)
			if fixture.WantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, fixture.Want, got)
		})
	}
}

func TestGitHubPermissionUserUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name               string
		payload            string
		wantErr            string
		wantPresent        bool
		wantPermissionsNil bool
	}{
		{name: "empty object", payload: `{}`, wantPermissionsNil: true},
		{name: "null user", payload: `null`, wantErr: "user must be an object", wantPermissionsNil: true},
		{name: "non-object user", payload: `"alice"`, wantErr: "decode user", wantPermissionsNil: true},
		{name: "absent permissions", payload: `{"login":"alice"}`, wantPermissionsNil: true},
		{name: "null permissions", payload: `{"permissions":null}`, wantPresent: true, wantPermissionsNil: true},
		{name: "malformed permissions", payload: `{"permissions":"write"}`, wantErr: "decode user.permissions", wantPresent: true, wantPermissionsNil: true},
		{name: "valid permissions", payload: `{"permissions":{"admin":false,"maintain":true,"push":true,"triage":true,"pull":true}}`, wantPresent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var user GitHubPermissionUser
			err := json.Unmarshal([]byte(tt.payload), &user)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPresent, user.permissionsPresent)
			if tt.wantPermissionsNil {
				assert.Nil(t, user.Permissions)
			} else {
				require.NotNil(t, user.Permissions)
				assert.True(t, *user.Permissions.Maintain)
			}
		})
	}
}

func permissionFlags(admin, maintain, push, triage, pull bool) *GitHubPermissionFlags {
	return &GitHubPermissionFlags{
		Admin:    &admin,
		Maintain: &maintain,
		Push:     &push,
		Triage:   &triage,
		Pull:     &pull,
	}
}

func TestResolveGitHubCollaboratorPermission(t *testing.T) {
	tests := []struct {
		name    string
		input   GitHubCollaboratorPermission
		want    string
		wantErr string
	}{
		{
			name:  "built-in role remains supported without extra signals",
			input: GitHubCollaboratorPermission{RoleName: "write"},
			want:  "write",
		},
		{
			name: "reported custom maintain role",
			input: GitHubCollaboratorPermission{
				RoleName:   "ODH Repo Maintainer",
				Permission: "write",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, true, true, true, true),
				},
			},
			want: "maintain",
		},
		{
			name: "custom maintain role from precise signals alone",
			input: GitHubCollaboratorPermission{
				RoleName: "ODH Repo Maintainer",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, true, true, true, true),
				},
			},
			want: "maintain",
		},
		{
			name: "custom triage role",
			input: GitHubCollaboratorPermission{
				RoleName:   "Support Triage",
				Permission: "read",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, false, false, true, true),
				},
			},
			want: "triage",
		},
		{
			name: "custom admin role",
			input: GitHubCollaboratorPermission{
				RoleName:   "Security Administrator",
				Permission: "admin",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(true, true, true, true, true),
				},
			},
			want: "admin",
		},
		{
			name: "custom read role",
			input: GitHubCollaboratorPermission{
				RoleName:   "Documentation Reader",
				Permission: "read",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, false, false, false, true),
				},
			},
			want: "read",
		},
		{
			name: "custom role without access",
			input: GitHubCollaboratorPermission{
				RoleName:   "No Access",
				Permission: "none",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, false, false, false, false),
				},
			},
			want: "none",
		},
		{
			name: "built-in maintain accepts collapsed legacy and precise signals",
			input: GitHubCollaboratorPermission{
				RoleName:   "maintain",
				Permission: "write",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, true, true, true, true),
				},
			},
			want: "maintain",
		},
		{
			name: "built-in role and precise flags conflict",
			input: GitHubCollaboratorPermission{
				RoleName:   "write",
				Permission: "write",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, true, true, true, true),
				},
			},
			wantErr: "conflicts with user.permissions",
		},
		{
			name:  "custom role falls back to conservative legacy write",
			input: GitHubCollaboratorPermission{RoleName: "Custom Write", Permission: "write"},
			want:  "write",
		},
		{
			name:  "custom role falls back to conservative legacy read",
			input: GitHubCollaboratorPermission{RoleName: "Custom Triage", Permission: "read"},
			want:  "read",
		},
		{
			name:    "custom role without effective permission fails closed",
			input:   GitHubCollaboratorPermission{RoleName: "Custom"},
			wantErr: "no effective permission",
		},
		{
			name: "incomplete flags fail closed",
			input: GitHubCollaboratorPermission{
				RoleName:   "Custom",
				Permission: "write",
				User: GitHubPermissionUser{
					Permissions: &GitHubPermissionFlags{},
				},
			},
			wantErr: "missing required boolean fields",
		},
		{
			name: "explicit null flags fail closed",
			input: GitHubCollaboratorPermission{
				RoleName:   "Custom",
				Permission: "write",
				User: GitHubPermissionUser{
					permissionsPresent: true,
				},
			},
			wantErr: "missing required boolean fields",
		},
		{
			name: "contradictory hierarchy fails closed",
			input: GitHubCollaboratorPermission{
				RoleName:   "Custom",
				Permission: "write",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, true, false, true, true),
				},
			},
			wantErr: "contradictory capability flags",
		},
		{
			name: "legacy and precise signals conflict",
			input: GitHubCollaboratorPermission{
				RoleName:   "Custom",
				Permission: "read",
				User: GitHubPermissionUser{
					Permissions: permissionFlags(false, false, true, true, true),
				},
			},
			wantErr: "conflicts with permission",
		},
		{
			name:    "built-in and legacy signals conflict",
			input:   GitHubCollaboratorPermission{RoleName: "admin", Permission: "write"},
			wantErr: "conflicts with permission",
		},
		{
			name:    "unknown legacy permission fails closed",
			input:   GitHubCollaboratorPermission{RoleName: "Custom", Permission: "maintain"},
			wantErr: "unknown legacy permission",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveGitHubCollaboratorPermission(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
