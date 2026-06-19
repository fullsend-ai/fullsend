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
	expectedRoles := []string{"triage", "coder", "review", "fix", "retro", "prioritize", "fullsend"}
	allPerms := RolePermissions()
	for _, role := range expectedRoles {
		perms, ok := allPerms[role]
		assert.True(t, ok, "missing permissions for role %q", role)
		assert.NotEmpty(t, perms, "empty permissions for role %q", role)
		_, hasMetadata := perms["metadata"]
		assert.True(t, hasMetadata, "role %q should have metadata permission", role)
	}
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
