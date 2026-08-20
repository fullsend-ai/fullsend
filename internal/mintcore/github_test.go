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
	"sort"
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

	id, err := FindInstallation(t.Context(), mockGH.URL, "fake-jwt", "myorg", "my-repo")
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

	_, err := FindInstallation(t.Context(), mockGH.URL, "fake-jwt", "myorg", "my-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to other-org")
}

func TestFindInstallation_NotFound(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGH.Close()

	_, err := FindInstallation(t.Context(), mockGH.URL, "fake-jwt", "myorg", "my-repo")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInstallationNotFound)
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

	token, expiresAt, granted, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "coder", LevelWrite, nil)
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

	id, err := FindOrgInstallation(t.Context(), mockGH.URL, "fake-jwt", "myorg")
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

	token, expiresAt, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "coder", LevelWrite, []string{"my-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghs_test_token", token)
	assert.Equal(t, "2099-01-01T00:00:00Z", expiresAt)
}

func TestCreateInstallationToken_UnknownRole(t *testing.T) {
	_, _, _, err := CreateInstallationToken(t.Context(), "http://unused", "fake-jwt", 42, "nonexistent", LevelWrite, []string{"repo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no permissions defined")
}

func TestRolePermissions_AllRolesPresent(t *testing.T) {
	expectedRoles := []string{"triage", "scribe", "coder", "review", "fix", "retro", "prioritize", "fullsend", "e2e"}
	allPerms := RolePermissions()
	for _, role := range expectedRoles {
		perms, ok := allPerms[role]
		assert.True(t, ok, "missing permissions for role %q", role)
		assert.NotEmpty(t, perms, "empty permissions for role %q", role)
		_, hasMetadata := perms["metadata"]
		assert.True(t, hasMetadata, "role %q should have metadata permission", role)
	}
}

func TestRolePermissions_Scribe(t *testing.T) {
	perms := RolePermissionsFor("scribe")
	require.NotNil(t, perms)
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "read", perms["metadata"])
	assert.Empty(t, perms["organization_projects"])
	assert.Empty(t, perms["pull_requests"])
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

func TestRolePermissions_Retro(t *testing.T) {
	perms := RolePermissionsFor("retro")
	require.NotNil(t, perms)
	assert.Equal(t, "read", perms["actions"])
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["pull_requests"])
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "read", perms["metadata"])
	assert.Len(t, perms, 5, "retro role should have exactly 5 permissions")
}

func TestRolePermissions_Prioritize(t *testing.T) {
	perms := RolePermissionsFor("prioritize")
	require.NotNil(t, perms)
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "write", perms["organization_projects"])
	assert.Equal(t, "read", perms["metadata"])
	assert.Len(t, perms, 4, "prioritize role should have exactly 4 permissions")
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

func TestBuiltInRoles_IncludesScribe(t *testing.T) {
	roles := BuiltInRoles()
	assert.Contains(t, roles, "scribe")
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "coder")
	// BuiltInRoles is sorted for stable CLI error messages.
	assert.Equal(t, append([]string(nil), roles...), func() []string {
		cp := append([]string(nil), roles...)
		sort.Strings(cp)
		return cp
	}())
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

	token, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "scanner", LevelRead, []string{"my-repo"})
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

	_, err := FindOrgInstallation(t.Context(), mockGH.URL, "fake-jwt", "myorg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to other-org")
}

func TestGetOrgVariable(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS", r.URL.Path)
		json.NewEncoder(w).Encode(variableResponse{
			Name:  "FULLSEND_FOREIGN_E2E_REPOS",
			Value: "fullsend-ai/fullsend",
		})
	}))
	defer mockGH.Close()

	value, exists, err := GetOrgVariable(t.Context(), mockGH.URL, "ghs_policy", "pool-org", "FULLSEND_FOREIGN_E2E_REPOS")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "fullsend-ai/fullsend", value)
}

func TestGetOrgVariable_NotFound(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGH.Close()

	_, exists, err := GetOrgVariable(t.Context(), mockGH.URL, "ghs_policy", "pool-org", "FULLSEND_FOREIGN_E2E_REPOS")
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
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend, fullsend-ai",
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer mockGH.Close()

	got, err := ReadForeignAllowlist(t.Context(), mockGH.URL, "app-jwt", 42, "pool-org", "e2e")
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

	got, err := ReadForeignAllowlist(t.Context(), mockGH.URL, "app-jwt", 42, "pool-org", "e2e")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindOrgInstallation_NotFound(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGH.Close()

	_, err := FindOrgInstallation(t.Context(), mockGH.URL, "fake-jwt", "myorg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 404")
}

func TestGetOrgVariable_ErrorStatus(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mockGH.Close()

	_, _, err := GetOrgVariable(t.Context(), mockGH.URL, "ghs_policy", "pool-org", "VAR")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 403")
}

func TestGetRepoVariable(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/target-org/target-repo/actions/variables/MY_VAR" && r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "MY_VAR",
				Value: "hello",
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer mockGH.Close()

	val, exists, err := GetRepoVariable(t.Context(), mockGH.URL, "ghs_tok", "target-org", "target-repo", "MY_VAR")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "hello", val)
}

func TestGetRepoVariable_NotFound(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGH.Close()

	_, exists, err := GetRepoVariable(t.Context(), mockGH.URL, "ghs_tok", "target-org", "target-repo", "MY_VAR")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestGetRepoVariable_ErrorStatus(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mockGH.Close()

	_, _, err := GetRepoVariable(t.Context(), mockGH.URL, "ghs_tok", "target-org", "target-repo", "MY_VAR")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 403")
}

func TestReadForeignAllowlistFromRepo(t *testing.T) {
	var tokenCalls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/app/installations/42/access_tokens") && r.Method == http.MethodPost:
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_repo_policy"})
		case r.URL.Path == "/repos/pool-org/target-repo/actions/variables/FULLSEND_FOREIGN_E2E_REPOS":
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend, caller-org",
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer mockGH.Close()

	got, err := ReadForeignAllowlistFromRepo(t.Context(), mockGH.URL, "app-jwt", 42, "pool-org", "target-repo", "e2e")
	require.NoError(t, err)
	assert.Equal(t, []string{"fullsend-ai/fullsend", "caller-org"}, got)
	assert.Equal(t, 1, tokenCalls)
}

func TestReadForeignAllowlistFromRepo_Empty(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/app/installations/42/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_repo_policy"})
		case strings.Contains(r.URL.Path, "/actions/variables/"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer mockGH.Close()

	got, err := ReadForeignAllowlistFromRepo(t.Context(), mockGH.URL, "app-jwt", 42, "pool-org", "target-repo", "e2e")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestGitHubUserAgent(t *testing.T) {
	t.Run("without version", func(t *testing.T) {
		origVersion := Version
		Version = ""
		t.Cleanup(func() { Version = origVersion })
		assert.Equal(t, "fullsend-mint", githubUserAgent())
	})
	t.Run("with version", func(t *testing.T) {
		origVersion := Version
		Version = "0.42.0"
		t.Cleanup(func() { Version = origVersion })
		assert.Equal(t, "fullsend-mint/0.42.0", githubUserAgent())
	})
}

func TestGitHubRequests_IncludeUserAgent(t *testing.T) {
	// Verify that all GitHub API helpers set a User-Agent header.
	// Without User-Agent, Cloudflare Worker HostFetchDoer requests
	// hit GitHub with no UA and receive 403.
	assertUA := func(t *testing.T, r *http.Request) {
		t.Helper()
		ua := r.Header.Get("User-Agent")
		assert.NotEmpty(t, ua, "User-Agent header must be set")
		assert.Contains(t, ua, "fullsend-mint", "User-Agent should contain fullsend-mint")
	}

	t.Run("FindInstallation", func(t *testing.T) {
		mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertUA(t, r)
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "org"},
			})
		}))
		defer mockGH.Close()

		_, err := FindInstallation(t.Context(), mockGH.URL, "jwt", "org", "repo")
		require.NoError(t, err)
	})

	t.Run("FindOrgInstallation", func(t *testing.T) {
		mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertUA(t, r)
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "org"},
			})
		}))
		defer mockGH.Close()

		_, err := FindOrgInstallation(t.Context(), mockGH.URL, "jwt", "org")
		require.NoError(t, err)
	})

	t.Run("GetOrgVariable", func(t *testing.T) {
		mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertUA(t, r)
			json.NewEncoder(w).Encode(variableResponse{Name: "VAR", Value: "val"})
		}))
		defer mockGH.Close()

		_, _, err := GetOrgVariable(t.Context(), mockGH.URL, "tok", "org", "VAR")
		require.NoError(t, err)
	})

	t.Run("GetRepoVariable", func(t *testing.T) {
		mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertUA(t, r)
			json.NewEncoder(w).Encode(variableResponse{Name: "VAR", Value: "val"})
		}))
		defer mockGH.Close()

		_, _, err := GetRepoVariable(t.Context(), mockGH.URL, "tok", "org", "repo", "VAR")
		require.NoError(t, err)
	})

	t.Run("CreateInstallationToken", func(t *testing.T) {
		mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertUA(t, r)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_tok",
				ExpiresAt: "2099-01-01T00:00:00Z",
			})
		}))
		defer mockGH.Close()

		_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "jwt", 1, "coder", LevelWrite, nil)
		require.NoError(t, err)
	})
}

func TestValidLevel(t *testing.T) {
	assert.True(t, ValidLevel("read"))
	assert.True(t, ValidLevel("write"))
	assert.False(t, ValidLevel(""))
	assert.False(t, ValidLevel("admin"))
	assert.False(t, ValidLevel("READ"))
}

func TestDeriveReadPermissions(t *testing.T) {
	perms := map[string]string{
		"contents":      "write",
		"pull_requests": "write",
		"issues":        "write",
		"checks":        "read",
		"metadata":      "read",
	}
	got := deriveReadPermissions(perms)
	assert.Equal(t, "read", got["contents"])
	assert.Equal(t, "read", got["pull_requests"])
	assert.Equal(t, "read", got["issues"])
	assert.Equal(t, "read", got["checks"])
	assert.Equal(t, "read", got["metadata"])

	// Original unchanged.
	assert.Equal(t, "write", perms["contents"])
}

func TestRolePermissionsForLevel_BuiltIn(t *testing.T) {
	// Write level returns canonical permissions unchanged.
	writePerms, err := RolePermissionsForLevel("coder", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "write", writePerms["contents"])
	assert.Equal(t, "write", writePerms["pull_requests"])
	assert.Equal(t, "read", writePerms["metadata"])

	// Read level downgrades write→read.
	readPerms, err := RolePermissionsForLevel("coder", LevelRead)
	require.NoError(t, err)
	assert.Equal(t, "read", readPerms["contents"])
	assert.Equal(t, "read", readPerms["pull_requests"])
	assert.Equal(t, "read", readPerms["issues"])
	assert.Equal(t, "read", readPerms["metadata"])
}

func TestRolePermissionsForLevel_AllBuiltInRoles(t *testing.T) {
	for _, role := range BuiltInRoles() {
		t.Run(role, func(t *testing.T) {
			writePerms, err := RolePermissionsForLevel(role, LevelWrite)
			require.NoError(t, err)
			assert.NotEmpty(t, writePerms)

			readPerms, err := RolePermissionsForLevel(role, LevelRead)
			require.NoError(t, err)
			assert.NotEmpty(t, readPerms)

			// Read level must have same keys as write.
			assert.Equal(t, len(writePerms), len(readPerms),
				"read and write levels should have the same number of permissions")

			// Every read permission must be ≤ the write permission.
			for k, rVal := range readPerms {
				wVal := writePerms[k]
				if wVal == "read" {
					assert.Equal(t, "read", rVal, "permission %s: read level should be read when write level is read", k)
				}
				// When write level is "write", read level must be "read".
				if wVal == "write" {
					assert.Equal(t, "read", rVal, "permission %s: read level should downgrade write to read", k)
				}
			}
		})
	}
}

func TestRolePermissionsForLevel_UnknownLevel(t *testing.T) {
	_, err := RolePermissionsForLevel("coder", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown level")
}

func TestRolePermissionsForLevel_UnknownRole(t *testing.T) {
	_, err := RolePermissionsForLevel("nonexistent", LevelRead)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no permissions defined")
}

func TestRolePermissionsForLevel_CustomFlat(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"contents": "read", "security_events": "write"},
	}))

	// Read level returns the flat permissions.
	readPerms, err := RolePermissionsForLevel("scanner", LevelRead)
	require.NoError(t, err)
	assert.Equal(t, "read", readPerms["contents"])
	assert.Equal(t, "write", readPerms["security_events"])

	// Write level falls back to read for flat-format custom roles.
	writePerms, err := RolePermissionsForLevel("scanner", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, readPerms, writePerms)
}

func TestRolePermissionsForLevel_CustomMultiLevel(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	require.NoError(t, RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"deployer": {
			LevelRead:  {"contents": "read", "metadata": "read"},
			LevelWrite: {"contents": "write", "metadata": "read", "deployments": "write"},
		},
	}))

	readPerms, err := RolePermissionsForLevel("deployer", LevelRead)
	require.NoError(t, err)
	assert.Equal(t, "read", readPerms["contents"])
	assert.Empty(t, readPerms["deployments"])

	writePerms, err := RolePermissionsForLevel("deployer", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "write", writePerms["contents"])
	assert.Equal(t, "write", writePerms["deployments"])
}

func TestRolePermissionsForLevel_CustomWriteOnly(t *testing.T) {
	// A custom role defined with only a write level should still work for
	// read requests by deriving read from write (downgrading write→read).
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	require.NoError(t, RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"deployer-wo": {
			LevelWrite: {"contents": "write", "metadata": "read", "deployments": "write"},
		},
	}))

	// Write level returns the full set.
	writePerms, err := RolePermissionsForLevel("deployer-wo", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "write", writePerms["contents"])
	assert.Equal(t, "write", writePerms["deployments"])

	// Read level derives from write by downgrading.
	readPerms, err := RolePermissionsForLevel("deployer-wo", LevelRead)
	require.NoError(t, err)
	assert.Equal(t, "read", readPerms["contents"])
	assert.Equal(t, "read", readPerms["deployments"])
	assert.Equal(t, "read", readPerms["metadata"])
}

func TestParseCustomRolePermissions_FlatFormat(t *testing.T) {
	raw := `{"my-role": {"contents": "read", "issues": "write"}}`
	levels, err := ParseCustomRolePermissions(raw)
	require.NoError(t, err)

	roleLevels, ok := levels["my-role"]
	require.True(t, ok)
	readPerms, ok := roleLevels[LevelRead]
	require.True(t, ok)
	assert.Equal(t, "read", readPerms["contents"])
	assert.Equal(t, "write", readPerms["issues"])
	_, hasWrite := roleLevels[LevelWrite]
	assert.False(t, hasWrite, "flat format should only have read level")
}

func TestParseCustomRolePermissions_MultiLevel(t *testing.T) {
	raw := `{"my-role": {"levels": {"read": {"contents": "read"}, "write": {"contents": "read", "issues": "write"}}}}`
	levels, err := ParseCustomRolePermissions(raw)
	require.NoError(t, err)

	roleLevels, ok := levels["my-role"]
	require.True(t, ok)
	assert.Equal(t, "read", roleLevels[LevelRead]["contents"])
	assert.Equal(t, "write", roleLevels[LevelWrite]["issues"])
}

func TestParseCustomRolePermissions_Mixed(t *testing.T) {
	raw := `{
		"flat-role": {"contents": "read"},
		"leveled-role": {"levels": {"read": {"contents": "read"}, "write": {"contents": "write"}}}
	}`
	levels, err := ParseCustomRolePermissions(raw)
	require.NoError(t, err)

	// Flat role has only read level.
	assert.Contains(t, levels["flat-role"], LevelRead)
	assert.NotContains(t, levels["flat-role"], LevelWrite)

	// Leveled role has both.
	assert.Contains(t, levels["leveled-role"], LevelRead)
	assert.Contains(t, levels["leveled-role"], LevelWrite)
}

func TestParseCustomRolePermissions_InvalidJSON(t *testing.T) {
	_, err := ParseCustomRolePermissions("not-json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON")
}

func TestParseCustomRolePermissions_MultiLevelInvalidValue(t *testing.T) {
	raw := `{"my-role": {"levels": {"read": {"contents": "admin"}}}}`
	_, err := ParseCustomRolePermissions(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
	assert.Contains(t, err.Error(), "custom role")
	assert.Contains(t, err.Error(), "my-role")
	assert.Contains(t, err.Error(), "contents")
}

func TestParseCustomRolePermissions_FlatFormatInvalidValue(t *testing.T) {
	raw := `{"my-role": {"contents": "admin"}}`
	_, err := ParseCustomRolePermissions(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
	assert.Contains(t, err.Error(), "custom role")
	assert.Contains(t, err.Error(), "my-role")
	assert.Contains(t, err.Error(), "contents")
}

func TestParseCustomRolePermissions_FlatFormatInvalidValueThroughRegister(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	// Parse a flat-format role with an invalid permission value, then
	// confirm it is rejected at parse time — not deferred to register.
	raw := `{"bad-flat": {"contents": "admin", "issues": "write"}}`
	_, err := ParseCustomRolePermissions(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
	assert.Contains(t, err.Error(), "bad-flat")
}

func TestRegisterCustomRoleLevels_WriteSupersetOfRead(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	// Valid: write is a superset of read.
	err := RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"deployer": {
			LevelRead:  {"contents": "read", "metadata": "read"},
			LevelWrite: {"contents": "write", "metadata": "read", "deployments": "write"},
		},
	})
	require.NoError(t, err)

	// Invalid: read has a permission missing from write.
	err = RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"bad-role": {
			LevelRead:  {"contents": "read", "extra": "read"},
			LevelWrite: {"contents": "write"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "superset")
	assert.Contains(t, err.Error(), "extra")

	// Invalid: read has higher access than write for a permission.
	err = RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"bad-role2": {
			LevelRead:  {"contents": "write"},
			LevelWrite: {"contents": "read"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "superset")
	assert.Contains(t, err.Error(), "contents")
}

func TestCreateInstallationToken_ReadLevel(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		perms := body["permissions"].(map[string]interface{})
		// Read level should downgrade all write→read.
		assert.Equal(t, "read", perms["contents"], "read level should downgrade contents:write to contents:read")
		assert.Equal(t, "read", perms["pull_requests"], "read level should downgrade pull_requests:write to read")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationTokenResponse{
			Token:     "ghs_read_token",
			ExpiresAt: "2099-01-01T00:00:00Z",
		})
	}))
	defer mockGH.Close()

	token, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "coder", LevelRead, []string{"my-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghs_read_token", token)
}

func TestCreateInstallationToken_DefaultsToRead(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		perms := body["permissions"].(map[string]interface{})
		// Empty level defaults to read → all permissions should be read.
		assert.Equal(t, "read", perms["contents"])
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(installationTokenResponse{
			Token:     "ghs_default_token",
			ExpiresAt: "2099-01-01T00:00:00Z",
		})
	}))
	defer mockGH.Close()

	token, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "coder", "", []string{"my-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghs_default_token", token)
}
