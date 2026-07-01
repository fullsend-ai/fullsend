package mintcore

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestGenerateAppJWT(t *testing.T) {
	pemData := testPEM(t)

	jwt, err := GenerateAppJWT("12345", pemData)
	require.NoError(t, err)
	assert.NotEmpty(t, jwt)

	parts := bytes.Split([]byte(jwt), []byte("."))
	assert.Len(t, parts, 3, "JWT should have 3 parts")
}

func TestGenerateAppJWT_InvalidPEM(t *testing.T) {
	_, err := GenerateAppJWT("12345", []byte("not a pem"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "PEM")
}

func TestFindInstallation(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/myorg/my-repo/installation", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
		json.NewEncoder(w).Encode(installationResponse{
			ID: 42,
			Account: struct {
				Login string `json:"login"`
			}{Login: "myorg"},
		})
	}))
	defer mockGH.Close()

	id, err := FindInstallation(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", "myorg", "my-repo")
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)
}

func TestFindInstallation_OrgMismatch(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(installationResponse{
			ID: 42,
			Account: struct {
				Login string `json:"login"`
			}{Login: "other-org"},
		})
	}))
	defer mockGH.Close()

	_, err := FindInstallation(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", "myorg", "my-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to other-org")
}

func TestCreateInstallationToken_Unscoped(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/app/installations/42/access_tokens", r.URL.Path)
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		assert.Contains(t, body, "permissions")
		assert.NotContains(t, body, "repositories")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationTokenResponse{
			Token:               "ghs_test_token",
			ExpiresAt:           "2099-01-01T00:00:00Z",
			RepositorySelection: "all",
		})
	}))
	defer mockGH.Close()

	token, expiresAt, granted, err := CreateInstallationToken(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", 42, "coder", nil)
	require.NoError(t, err)
	assert.Equal(t, "ghs_test_token", token)
	assert.Equal(t, "2099-01-01T00:00:00Z", expiresAt)
	require.NotNil(t, granted)
	assert.Equal(t, "all", granted.RepoSelection)
}

func TestFindOrgInstallation(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/orgs/myorg/installation", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
		json.NewEncoder(w).Encode(installationResponse{
			ID: 42,
			Account: struct {
				Login string `json:"login"`
			}{Login: "myorg"},
		})
	}))
	defer mockGH.Close()

	id, err := FindOrgInstallation(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", "myorg")
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)
}

func TestCreateInstallationToken(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/app/installations/42/access_tokens", r.URL.Path)
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		assert.Contains(t, body, "permissions")
		assert.Contains(t, body, "repositories")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationTokenResponse{
			Token:     "ghs_test_token",
			ExpiresAt: "2099-01-01T00:00:00Z",
		})
	}))
	defer mockGH.Close()

	token, expiresAt, _, err := CreateInstallationToken(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", 42, "coder", []string{"my-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghs_test_token", token)
	assert.Equal(t, "2099-01-01T00:00:00Z", expiresAt)
}

func TestCreateInstallationToken_UnknownRole(t *testing.T) {
	_, _, _, err := CreateInstallationToken(t.Context(), http.DefaultClient, "http://unused", "fake-jwt", 42, "nonexistent", []string{"repo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no permissions defined")
}

func TestRolePermissions_AllRolesPresent(t *testing.T) {
	expectedRoles := []string{"triage", "coder", "review", "fix", "retro", "prioritize", "fullsend", "e2e"}
	allPerms := RolePermissions()
	for _, role := range expectedRoles {
		perms, ok := allPerms[role]
		assert.True(t, ok, "missing permissions for role %q", role)
		assert.NotEmpty(t, perms, "empty permissions for role %q", role)
		_, hasMetadata := perms["metadata"]
		assert.True(t, hasMetadata, "role %q should have metadata permission", role)
	}
}

func TestRolePermissions_E2e(t *testing.T) {
	perms := RolePermissionsFor("e2e")
	require.NotNil(t, perms)
	assert.Equal(t, "write", perms["actions"])
	assert.Equal(t, "write", perms["actions_variables"])
	assert.Equal(t, "write", perms["organization_actions_variables"])
	assert.Equal(t, "write", perms["administration"])
	assert.Equal(t, "write", perms["contents"])
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "write", perms["members"])
	assert.Equal(t, "read", perms["metadata"])
	assert.Equal(t, "write", perms["organization_administration"])
	assert.Equal(t, "write", perms["pull_requests"])
	assert.Equal(t, "write", perms["secrets"])
	assert.Equal(t, "write", perms["workflows"])
}

func TestRolePermissions_ReturnsCopy(t *testing.T) {
	// Mutating the returned map must not affect the canonical definitions.
	perms := RolePermissions()
	perms["triage"]["contents"] = "write"
	fresh := RolePermissions()
	assert.Equal(t, "read", fresh["triage"]["contents"], "RolePermissions should return a fresh copy")
}

func TestRolePermissionsFor(t *testing.T) {
	perms := RolePermissionsFor("coder")
	require.NotNil(t, perms)
	assert.Equal(t, "write", perms["contents"])

	assert.Nil(t, RolePermissionsFor("nonexistent"))
}

func TestHasRole(t *testing.T) {
	assert.True(t, HasRole("coder"))
	assert.False(t, HasRole("nonexistent"))
}

func TestCustomRolePermissions(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	assert.False(t, HasRole("scanner"))
	assert.Nil(t, RolePermissionsFor("scanner"))

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"contents": "read", "security_events": "write"},
	}))

	assert.True(t, HasRole("scanner"))
	perms := RolePermissionsFor("scanner")
	require.NotNil(t, perms)
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["security_events"])

	// Built-in roles still work
	assert.True(t, HasRole("coder"))
	assert.NotNil(t, RolePermissionsFor("coder"))

	// RolePermissions() includes custom roles
	allPerms := RolePermissions()
	assert.Contains(t, allPerms, "scanner", "RolePermissions should include custom roles")
	assert.Contains(t, allPerms, "coder", "RolePermissions should still include built-in roles")
	assert.Equal(t, "write", allPerms["scanner"]["security_events"])
}

func TestCustomRolePermissions_RejectsBuiltinCollision(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	err := RegisterCustomRolePermissions(map[string]map[string]string{
		"triage": {"contents": "write", "issues": "write"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with built-in role")
}

func TestCustomRolePermissions_RejectsInvalidName(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	err := RegisterCustomRolePermissions(map[string]map[string]string{
		"Invalid-Role": {"contents": "read"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestCustomRolePermissions_DeepCopiesInput(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	input := map[string]map[string]string{
		"scanner": {"contents": "read"},
	}
	require.NoError(t, RegisterCustomRolePermissions(input))

	input["scanner"]["contents"] = "write"
	perms := RolePermissionsFor("scanner")
	assert.Equal(t, "read", perms["contents"], "stored permissions should not be affected by caller mutation")
}

func TestCustomRolePermissions_ReturnsCopy(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"contents": "read"},
	}))

	perms := RolePermissionsFor("scanner")
	perms["contents"] = "write"
	fresh := RolePermissionsFor("scanner")
	assert.Equal(t, "read", fresh["contents"], "should return a copy")
}

func TestCustomRolePermissions_Clear(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"contents": "read"},
	}))
	assert.True(t, HasRole("scanner"))

	require.NoError(t, RegisterCustomRolePermissions(nil))
	assert.False(t, HasRole("scanner"))
}

func TestCreateInstallationToken_CustomRole(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"contents": "read", "security_events": "write"},
	}))

	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		perms := body["permissions"].(map[string]interface{})
		assert.Equal(t, "read", perms["contents"])
		assert.Equal(t, "write", perms["security_events"])
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationTokenResponse{
			Token:     "ghs_custom_token",
			ExpiresAt: "2099-01-01T00:00:00Z",
		})
	}))
	defer mockGH.Close()

	token, _, _, err := CreateInstallationToken(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", 42, "scanner", []string{"my-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghs_custom_token", token)
}

func TestFindOrgInstallation_OrgMismatch(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(installationResponse{
			ID: 99,
			Account: struct {
				Login string `json:"login"`
			}{Login: "other-org"},
		})
	}))
	defer mockGH.Close()

	_, err := FindOrgInstallation(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", "myorg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to other-org")
}

func TestGetOrgVariable(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS", r.URL.Path)
		json.NewEncoder(w).Encode(orgVariableResponse{
			Name:  "FULLSEND_FOREIGN_E2E_REPOS",
			Value: "fullsend-ai/fullsend",
		})
	}))
	defer mockGH.Close()

	value, exists, err := GetOrgVariable(t.Context(), http.DefaultClient, mockGH.URL, "ghs_policy", "pool-org", "FULLSEND_FOREIGN_E2E_REPOS")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "fullsend-ai/fullsend", value)
}

func TestGetOrgVariable_NotFound(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGH.Close()

	_, exists, err := GetOrgVariable(t.Context(), http.DefaultClient, mockGH.URL, "ghs_policy", "pool-org", "FULLSEND_FOREIGN_E2E_REPOS")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestReadForeignAllowlist(t *testing.T) {
	var tokenCalls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/app/installations/42/access_tokens") && r.Method == http.MethodPost:
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy"})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS":
			json.NewEncoder(w).Encode(orgVariableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend, fullsend-ai",
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer mockGH.Close()

	got, err := ReadForeignAllowlist(t.Context(), http.DefaultClient, mockGH.URL, "app-jwt", 42, "pool-org", "e2e")
	require.NoError(t, err)
	assert.Equal(t, []string{"fullsend-ai/fullsend", "fullsend-ai"}, got)
	assert.Equal(t, 1, tokenCalls)
}

func TestReadForeignAllowlist_EmptyVariable(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/app/installations/42/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy"})
		case strings.Contains(r.URL.Path, "/actions/variables/"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer mockGH.Close()

	got, err := ReadForeignAllowlist(t.Context(), http.DefaultClient, mockGH.URL, "app-jwt", 42, "pool-org", "e2e")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindOrgInstallation_NotFound(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGH.Close()

	_, err := FindOrgInstallation(t.Context(), http.DefaultClient, mockGH.URL, "fake-jwt", "myorg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 404")
}

func TestGetOrgVariable_ErrorStatus(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mockGH.Close()

	_, _, err := GetOrgVariable(t.Context(), http.DefaultClient, mockGH.URL, "ghs_policy", "pool-org", "VAR")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 403")
}
