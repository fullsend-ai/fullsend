package mintcore

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
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

func TestZeroSlice(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03, 0x04}
	zeroSlice(b)
	for i, v := range b {
		assert.Equal(t, byte(0), v, "byte %d not zeroed", i)
	}
}

func TestZeroSlice_Empty(t *testing.T) {
	// Must not panic on empty or nil slices.
	zeroSlice(nil)
	zeroSlice([]byte{})
}

func TestZeroBigInt(t *testing.T) {
	n := new(big.Int).SetInt64(123456789)
	require.True(t, n.Sign() != 0, "precondition: n is non-zero")

	zeroBigInt(n)
	assert.True(t, n.Sign() == 0, "big.Int should be zero after zeroBigInt")
}

func TestZeroBigInt_Nil(t *testing.T) {
	// Must not panic on nil.
	zeroBigInt(nil)
}

func TestZeroRSAKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	key.Precompute()

	// Verify preconditions: private fields are non-zero.
	require.True(t, key.D.Sign() != 0, "precondition: D is non-zero")
	require.True(t, len(key.Primes) >= 2, "precondition: at least 2 primes")
	for _, p := range key.Primes {
		require.True(t, p.Sign() != 0, "precondition: prime is non-zero")
	}

	zeroRSAKey(key)

	assert.True(t, key.D.Sign() == 0, "D should be zeroed")
	for i, p := range key.Primes {
		assert.True(t, p.Sign() == 0, "prime %d should be zeroed", i)
	}
	assert.True(t, key.Precomputed.Dp.Sign() == 0, "Dp should be zeroed")
	assert.True(t, key.Precomputed.Dq.Sign() == 0, "Dq should be zeroed")
	assert.True(t, key.Precomputed.Qinv.Sign() == 0, "Qinv should be zeroed")
}

func TestZeroRSAKey_Nil(t *testing.T) {
	// Must not panic on nil.
	zeroRSAKey(nil)
}

func TestGenerateAppJWT_DeferredZeroDoesNotBreakSigning(t *testing.T) {
	// Verify that the deferred zeroSlice/zeroRSAKey calls added in
	// GenerateAppJWT do not interfere with signing. The zeroing helpers
	// are tested independently (TestZeroSlice, TestZeroBigInt,
	// TestZeroRSAKey); this test confirms the defer ordering is correct
	// — i.e., zeroing runs after the JWT is fully assembled.
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

func TestRolePermissionsForLevel_AllRolesPresent(t *testing.T) {
	expectedRoles := []string{"triage", "scribe", "coder", "review", "fix", "retro", "prioritize", "fullsend", "e2e"}
	for _, role := range expectedRoles {
		writePerms, err := RolePermissionsForLevel(role, LevelWrite)
		require.NoError(t, err, "write level missing for role %q", role)
		assert.NotEmpty(t, writePerms, "empty write permissions for role %q", role)
		_, hasMetadata := writePerms["metadata"]
		assert.True(t, hasMetadata, "role %q should have metadata permission", role)

		readPerms, err := RolePermissionsForLevel(role, LevelRead)
		require.NoError(t, err, "read level missing for role %q", role)
		assert.NotEmpty(t, readPerms, "empty read permissions for role %q", role)
	}
}

func TestRolePermissionsForLevel_Scribe(t *testing.T) {
	perms, err := RolePermissionsForLevel("scribe", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "read", perms["metadata"])
	assert.Empty(t, perms["organization_projects"])
	assert.Empty(t, perms["pull_requests"])
}

func TestRolePermissionsForLevel_E2e(t *testing.T) {
	perms, err := RolePermissionsForLevel("e2e", LevelWrite)
	require.NoError(t, err)
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

func TestRolePermissionsForLevel_Retro(t *testing.T) {
	perms, err := RolePermissionsForLevel("retro", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "read", perms["actions"])
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["pull_requests"])
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "read", perms["metadata"])
	assert.Len(t, perms, 5, "retro role should have exactly 5 permissions")
}

func TestRolePermissionsForLevel_Prioritize(t *testing.T) {
	perms, err := RolePermissionsForLevel("prioritize", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["issues"])
	assert.Equal(t, "write", perms["organization_projects"])
	assert.Equal(t, "read", perms["metadata"])
	assert.Len(t, perms, 4, "prioritize role should have exactly 4 permissions")
}

func TestRolePermissionsForLevel_ReturnsCopy(t *testing.T) {
	// Mutating the returned map must not affect the canonical definitions.
	perms, err := RolePermissionsForLevel("triage", LevelWrite)
	require.NoError(t, err)
	perms["contents"] = "write"
	fresh, err := RolePermissionsForLevel("triage", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "read", fresh["contents"], "RolePermissionsForLevel should return a fresh copy")
}

func TestRolePermissionsForLevel_CoderWrite(t *testing.T) {
	perms, err := RolePermissionsForLevel("coder", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "write", perms["contents"])

	_, err = RolePermissionsForLevel("nonexistent", LevelWrite)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no permissions defined")
}

func TestHasRole(t *testing.T) {
	assert.True(t, HasRole("coder"))
	assert.False(t, HasRole("nonexistent"))
}

func TestValidateLevelName(t *testing.T) {
	// Valid level names.
	for _, name := range []string{"", "read", "write", "admin", "deploy-ro", "a", "abcdefghijklmnopqrstuvwxyz012345"} {
		assert.NoError(t, ValidateLevelName(name), "expected valid: %q", name)
	}
	// Invalid level names.
	for _, name := range []string{"Write", "READ", "1read", "-read", "re ad", "re.ad", "abcdefghijklmnopqrstuvwxyz0123456", "UPPER"} {
		assert.Error(t, ValidateLevelName(name), "expected invalid: %q", name)
	}
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
	_, err := RolePermissionsForLevel("scanner", LevelWrite)
	assert.Error(t, err)

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"contents": "read", "security_events": "write"},
	}))

	assert.True(t, HasRole("scanner"))
	perms, err := RolePermissionsForLevel("scanner", LevelWrite)
	require.NoError(t, err)
	require.NotNil(t, perms)
	assert.Equal(t, "read", perms["contents"])
	assert.Equal(t, "write", perms["security_events"])

	// Built-in roles still work
	assert.True(t, HasRole("coder"))
	coderPerms, err := RolePermissionsForLevel("coder", LevelWrite)
	require.NoError(t, err)
	assert.NotNil(t, coderPerms)
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
	perms, err := RolePermissionsForLevel("scanner", LevelRead)
	require.NoError(t, err)
	assert.Equal(t, "read", perms["contents"], "stored permissions should not be affected by caller mutation")
}

func TestCustomRolePermissions_ReturnsCopy(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"contents": "read"},
	}))

	perms, err := RolePermissionsForLevel("scanner", LevelRead)
	require.NoError(t, err)
	perms["contents"] = "write"
	fresh, err := RolePermissionsForLevel("scanner", LevelRead)
	require.NoError(t, err)
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

func TestRolePermissionsForLevel_BuiltInExtraLevelErrors(t *testing.T) {
	// Built-in roles only define read and write. An unknown level
	// on a known built-in role must return a descriptive error.
	_, err := RolePermissionsForLevel("coder", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `role "coder" has no level "admin"`)
}

func TestRolePermissionsForLevel_BuiltIn(t *testing.T) {
	// Write level returns canonical permissions unchanged.
	writePerms, err := RolePermissionsForLevel("coder", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "write", writePerms["contents"])
	assert.Equal(t, "write", writePerms["pull_requests"])
	assert.Equal(t, "read", writePerms["metadata"])

	// Read level returns statically defined read permissions (table lookup,
	// not derived from write at runtime).
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

			// Read and write levels must have the same keys (static table,
			// not derived at runtime).
			assert.Equal(t, len(writePerms), len(readPerms),
				"read and write levels should have the same number of permissions")

			// Every read permission value must be "read" (baked into the
			// table, not computed from write).
			for k, rVal := range readPerms {
				assert.Equal(t, "read", rVal, "permission %s: read level should have value \"read\"", k)
				// The key must also exist in write.
				_, ok := writePerms[k]
				assert.True(t, ok, "permission %s in read level missing from write level", k)
			}
		})
	}
}

func TestRolePermissionsForLevel_UnknownLevel(t *testing.T) {
	_, err := RolePermissionsForLevel("coder", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no level")
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

	// Both read and write return the same stored permission map.
	readPerms, err := RolePermissionsForLevel("scanner", LevelRead)
	require.NoError(t, err)
	assert.Equal(t, "read", readPerms["contents"])
	assert.Equal(t, "write", readPerms["security_events"])

	writePerms, err := RolePermissionsForLevel("scanner", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, readPerms, writePerms, "flat custom role: read and write should return the same map")
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

func TestRegisterCustomRoleLevels_MissingMandatoryLevel(t *testing.T) {
	// A custom role missing the mandatory read or write level is rejected.
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	// Missing read.
	err := RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"deployer-wo": {
			LevelWrite: {"contents": "write", "metadata": "read"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing mandatory level")
	assert.Contains(t, err.Error(), "read")

	// Missing write.
	err = RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"deployer-ro": {
			LevelRead: {"contents": "read", "metadata": "read"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing mandatory level")
	assert.Contains(t, err.Error(), "write")
}

func TestParseCustomRolePermissions_FlatFormat(t *testing.T) {
	raw := `{"my-role": {"contents": "read", "issues": "write"}}`
	levels, err := ParseCustomRolePermissions(raw)
	require.NoError(t, err)

	roleLevels, ok := levels["my-role"]
	require.True(t, ok)

	// Flat format is stored under both read and write.
	readPerms, hasRead := roleLevels[LevelRead]
	require.True(t, hasRead, "flat format should have read level")
	assert.Equal(t, "read", readPerms["contents"])
	assert.Equal(t, "write", readPerms["issues"])

	writePerms, hasWrite := roleLevels[LevelWrite]
	require.True(t, hasWrite, "flat format should have write level")
	assert.Equal(t, readPerms, writePerms, "flat format: read and write should be equal")
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

	// Flat role has both read and write (same map).
	assert.Contains(t, levels["flat-role"], LevelRead)
	assert.Contains(t, levels["flat-role"], LevelWrite)
	assert.Equal(t, levels["flat-role"][LevelRead], levels["flat-role"][LevelWrite])

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

func TestRegisterCustomRoleLevels_ExtraLevelsAllowed(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	// Custom role with read, write, and an extra "admin" level.
	err := RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"deployer": {
			LevelRead:  {"contents": "read", "metadata": "read"},
			LevelWrite: {"contents": "write", "metadata": "read", "deployments": "write"},
			"admin":    {"contents": "write", "metadata": "write", "deployments": "write", "environments": "write"},
		},
	})
	require.NoError(t, err)

	// All three levels are accessible.
	readPerms, err := RolePermissionsForLevel("deployer", LevelRead)
	require.NoError(t, err)
	assert.Equal(t, "read", readPerms["contents"])

	writePerms, err := RolePermissionsForLevel("deployer", LevelWrite)
	require.NoError(t, err)
	assert.Equal(t, "write", writePerms["contents"])

	adminPerms, err := RolePermissionsForLevel("deployer", "admin")
	require.NoError(t, err)
	assert.Equal(t, "write", adminPerms["environments"])

	// An undefined level on this custom role errors.
	_, err = RolePermissionsForLevel("deployer", "superadmin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no level")
}

func TestRegisterCustomRoleLevels_NoSupersetEnforcement(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	// Write-superset-of-read is NOT enforced. This is valid even
	// though read has a permission missing from write.
	err := RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"asymmetric": {
			LevelRead:  {"contents": "read", "extra": "read"},
			LevelWrite: {"contents": "write"},
		},
	})
	require.NoError(t, err)
}

func TestRegisterCustomRoleLevels_ReservedLevelName(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	// "levels" is reserved as the JSON discriminator key.
	err := RegisterCustomRoleLevels(map[string]map[string]map[string]string{
		"bad-role": {
			LevelRead:  {"contents": "read"},
			LevelWrite: {"contents": "write"},
			"levels":   {"contents": "read"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved key")
}

func TestRegisterCustomRoleLevels_InvalidLevelName(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRoleLevels(nil) })

	cases := []struct {
		name  string
		level string
	}{
		{"uppercase", "Write"},
		{"starts-with-digit", "1read"},
		{"starts-with-dash", "-read"},
		{"too-long", "abcdefghijklmnopqrstuvwxyz0123456"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RegisterCustomRoleLevels(map[string]map[string]map[string]string{
				"testrole": {
					LevelRead:  {"contents": "read"},
					LevelWrite: {"contents": "write"},
					tc.level:   {"contents": "read"},
				},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid level name")
		})
	}
}

func TestParseCustomRolePermissions_InvalidLevelName(t *testing.T) {
	// Multi-level format with an invalid level name should fail at parse time.
	raw := `{"my-role": {"levels": {"read": {"contents": "read"}, "write": {"contents": "write"}, "Write": {"contents": "write"}}}}`
	_, err := ParseCustomRolePermissions(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid level name")
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
