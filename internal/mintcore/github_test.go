package mintcore

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
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

func TestFindInstallationDetails_DecodesPermissions(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(installationResponse{
			ID:          42,
			Permissions: map[string]string{"contents": "write", "packages": "read"},
			Account: struct {
				Login string `json:"login"`
			}{Login: "myorg"},
		})
	}))
	defer mockGH.Close()

	inst, err := findInstallationDetails(t.Context(), mockGH.URL, "fake-jwt", "myorg", "my-repo")
	require.NoError(t, err)
	assert.Equal(t, int64(42), inst.ID)
	assert.Equal(t, map[string]string{"contents": "write", "packages": "read"}, inst.Permissions)
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

	token, expiresAt, granted, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "test-org", "coder", nil)
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

	token, expiresAt, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "test-org", "coder", []string{"my-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghs_test_token", token)
	assert.Equal(t, "2099-01-01T00:00:00Z", expiresAt)
}

func TestCreateInstallationToken_UnknownRole(t *testing.T) {
	_, _, _, err := CreateInstallationToken(t.Context(), "http://unused", "fake-jwt", 42, "test-org", "nonexistent", []string{"repo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no permissions defined")
}

func TestCreateInstallationTokenWithGrantedPermissions_DownscopesWithoutRetry(t *testing.T) {
	var calls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		perms := body["permissions"].(map[string]interface{})
		_, hasPackages := perms["packages"]
		assert.False(t, hasPackages, "ungranted packages permission must be omitted before POST")
		assert.Equal(t, "write", perms["contents"])
		assert.Equal(t, "read", perms["metadata"], "metadata:read remains in the token scope when omitted from the installation response")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(installationTokenResponse{
			Token:     "ghs_downscoped_token",
			ExpiresAt: "2099-01-01T00:00:00Z",
			Permissions: map[string]string{
				"contents": "write",
				"metadata": "read",
			},
		})
	}))
	defer mockGH.Close()

	token, expiresAt, granted, err := CreateInstallationTokenWithGrantedPermissions(t.Context(), mockGH.URL, "fake-jwt", 99, "test-org", "coder", []string{"repo"}, map[string]string{
		"contents":      "write",
		"issues":        "write",
		"pull_requests": "write",
		"checks":        "read",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, "ghs_downscoped_token", token)
	assert.Equal(t, "2099-01-01T00:00:00Z", expiresAt)
	require.NotNil(t, granted)
	assert.Equal(t, "write", granted.Permissions["contents"])
	assert.Empty(t, granted.Permissions["packages"])
}

func TestCreateInstallationTokenWithGrantedPermissions_MissingRequiredFailsBeforePost(t *testing.T) {
	var calls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatalf("token POST should not happen when required permissions are missing")
	}))
	defer mockGH.Close()

	_, _, _, err := CreateInstallationTokenWithGrantedPermissions(t.Context(), mockGH.URL, "fake-jwt", 99, "test-org", "coder", nil, map[string]string{
		"packages": "read",
	})
	require.Error(t, err)
	assert.Equal(t, 0, calls)
	assert.ErrorIs(t, err, ErrRequiredPermissionsMissing)
	assert.Contains(t, err.Error(), "contents:write")
}

func TestCreateInstallationTokenWithGrantedPermissions_NonOptionalPermissionFailsHard(t *testing.T) {
	var calls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatalf("token POST should not happen when a non-optional permission is missing")
	}))
	defer mockGH.Close()

	_, _, _, err := CreateInstallationTokenWithGrantedPermissions(t.Context(), mockGH.URL, "fake-jwt", 99, "test-org", "coder", nil, map[string]string{
		"contents": "write",
		"packages": "read",
		"metadata": "read",
		// missing: issues, pull_requests, checks — all non-optional
	})
	require.Error(t, err)
	assert.Equal(t, 0, calls)
	assert.ErrorIs(t, err, ErrRequiredPermissionsMissing)
	assert.Contains(t, err.Error(), "issues:write")
	assert.Contains(t, err.Error(), "pull_requests:write")
	assert.Contains(t, err.Error(), "checks:read")
	assert.Contains(t, err.Error(), "installation_id=99")
}

func TestCreateInstallationToken_422UnrelatedBodyDoesNotFallback(t *testing.T) {
	var calls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Installation","code":"invalid"}]}`))
	}))
	defer mockGH.Close()

	_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 99, "test-org", "coder", nil)
	require.Error(t, err)
	assert.Equal(t, 1, calls, "unrelated 422 must not trigger packages fallback")
	assert.Contains(t, err.Error(), "status 422")
	assert.NotContains(t, err.Error(), "packages fallback")
}

func TestCreateInstallationToken_Non422DoesNotFallback(t *testing.T) {
	var calls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer mockGH.Close()

	_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 99, "test-org", "coder", nil)
	require.Error(t, err)
	assert.Equal(t, 1, calls, "non-422 must not trigger packages fallback")
	assert.Contains(t, err.Error(), "status 403")
	assert.NotContains(t, err.Error(), "packages fallback")
}

func TestCreateInstallationToken_5xxDoesNotRetry(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			var calls int
			mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(code)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"message":"server error %d"}`, code)))
			}))
			defer mockGH.Close()

			_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 99, "test-org", "coder", nil)
			require.Error(t, err)
			assert.Equal(t, 1, calls, "%d must not trigger retry", code)
			assert.Contains(t, err.Error(), fmt.Sprintf("status %d", code))
		})
	}
}

func TestPermissionLevelAtLeast(t *testing.T) {
	tests := []struct {
		granted, requested string
		want               bool
	}{
		{"read", "read", true},
		{"write", "read", true},
		{"write", "write", true},
		{"admin", "read", true},
		{"admin", "write", true},
		{"admin", "admin", true},
		{"read", "write", false},
		{"read", "admin", false},
		{"write", "admin", false},
		{"", "read", false},
		{"", "write", false},
		{"bogus", "read", false},
		{"maintain", "read", false},
		{"read", "", false},
		{"read", "bogus", false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, PermissionLevelAtLeast(tc.granted, tc.requested),
			"PermissionLevelAtLeast(%q, %q)", tc.granted, tc.requested)
	}
}

func TestIsOptionalRolePermission(t *testing.T) {
	tests := []struct {
		role, permission string
		want             bool
	}{
		{"coder", "packages", true},
		{"coder", "contents", false},
		{"fix", "packages", true},
		{"fix", "contents", false},
		{"triage", "packages", false},
		{"nosuchrole", "packages", false},
		{"", "", false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, IsOptionalRolePermission(tc.role, tc.permission),
			"IsOptionalRolePermission(%q, %q)", tc.role, tc.permission)
	}
}

func TestEffectiveInstallationPermissions_AdminSatisfiesWrite(t *testing.T) {
	effective, dropped, err := effectiveInstallationPermissions("prioritize",
		map[string]string{"contents": "read", "issues": "write", "organization_projects": "write", "metadata": "read"},
		map[string]string{"contents": "read", "issues": "write", "organization_projects": "admin", "metadata": "read"})
	require.NoError(t, err)
	assert.Empty(t, dropped)
	assert.Equal(t, "write", effective["organization_projects"])
}

func TestEffectiveInstallationPermissions_NilGrantedPreservesRequested(t *testing.T) {
	requested := map[string]string{"contents": "write", "packages": "read"}
	effective, dropped, err := effectiveInstallationPermissions("coder", requested, nil)
	require.NoError(t, err)
	assert.Nil(t, dropped)
	assert.Equal(t, requested, effective)
}

func TestEffectiveInstallationPermissions_EmptyGrantedPreservesRequested(t *testing.T) {
	// GitHub always grants at least metadata:read, so a non-nil but empty
	// permissions object carries no grant data — it must not be intersected.
	requested := map[string]string{"contents": "write", "packages": "read"}
	effective, dropped, err := effectiveInstallationPermissions("coder", requested, map[string]string{})
	require.NoError(t, err)
	assert.Nil(t, dropped)
	assert.Equal(t, requested, effective)
}

func TestEffectiveInstallationPermissions_OnlyOptionalDropped(t *testing.T) {
	effective, dropped, err := effectiveInstallationPermissions("coder",
		map[string]string{"contents": "write", "packages": "read", "issues": "write", "pull_requests": "write", "checks": "read", "metadata": "read"},
		map[string]string{"contents": "write", "issues": "write", "pull_requests": "write", "checks": "read", "metadata": "read"})
	require.NoError(t, err)
	assert.Equal(t, []string{"packages:read"}, dropped)
	assert.Equal(t, "write", effective["contents"])
	_, hasPackages := effective["packages"]
	assert.False(t, hasPackages)
}

func TestEffectiveInstallationPermissions_NonOptionalMissingFails(t *testing.T) {
	_, _, err := effectiveInstallationPermissions("coder",
		map[string]string{"contents": "write", "packages": "read", "issues": "write", "pull_requests": "write", "checks": "read", "metadata": "read"},
		map[string]string{"contents": "write", "packages": "read", "metadata": "read"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRequiredPermissionsMissing)
	assert.Contains(t, err.Error(), "issues:write")
	assert.Contains(t, err.Error(), "pull_requests:write")
	assert.Contains(t, err.Error(), "checks:read")
}

func TestEffectiveInstallationPermissions_CustomRoleAllMissing(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })
	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"scanner": {"security_events": "write"},
	}))

	_, _, err := effectiveInstallationPermissions("scanner",
		map[string]string{"security_events": "write"},
		map[string]string{"contents": "read", "metadata": "read"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRequiredPermissionsMissing)
	assert.Contains(t, err.Error(), "security_events:write")
}

func TestEffectiveInstallationPermissions_EmptyRequestedFails(t *testing.T) {
	_, _, err := effectiveInstallationPermissions("scanner", map[string]string{}, map[string]string{
		"contents": "read",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRequiredPermissionsMissing)
	assert.Contains(t, err.Error(), "no permissions remain")
}

func TestInstallationAcceptHint(t *testing.T) {
	assert.True(t, isPublicGitHubAPI(""))
	assert.True(t, isPublicGitHubAPI("https://api.github.com"))
	assert.True(t, isPublicGitHubAPI("https://api.github.com/"))
	assert.False(t, isPublicGitHubAPI("https://github.example.com/api/v3"))
	assert.False(t, isPublicGitHubAPI("https://evil.example/?x=api.github.com"))
	assert.Contains(t, InstallationAcceptHint("https://api.github.com", "myorg", 42), "https://github.com/organizations/myorg/settings/installations/42")
	assert.Contains(t, InstallationAcceptHint("https://api.github.com", "myorg", 42), "App owner must add them first")
	assert.NotContains(t, InstallationAcceptHint("https://github.example.com/api/v3", "myorg", 42), "https://github.com/")
	assert.Contains(t, InstallationAcceptHint("https://github.example.com/api/v3", "myorg", 42), "installation_id=42")
	assert.Contains(t, InstallationAcceptHint("https://github.example.com/api/v3", "myorg", 42), `org="myorg"`)
}

func TestCreateInstallationToken_LargeSuccessBody(t *testing.T) {
	// Success responses must not be capped at 4KiB — repo objects in the token
	// payload can exceed that. Build a body larger than the error-body limit.
	repos := make([]installationTokenRepository, 0, 200)
	for i := 0; i < 200; i++ {
		repos = append(repos, installationTokenRepository{FullName: fmt.Sprintf("org/very-long-repository-name-%03d-xxxxxxxxxxxxxxxx", i)})
	}
	payload, err := json.Marshal(installationTokenResponse{
		Token:        "ghs_large",
		ExpiresAt:    "2099-01-01T00:00:00Z",
		Repositories: repos,
		Permissions:  map[string]string{"contents": "write"},
	})
	require.NoError(t, err)
	require.Greater(t, len(payload), 4096)

	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(payload)
	}))
	defer mockGH.Close()

	token, _, granted, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "test-org", "coder", []string{"repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghs_large", token)
	require.NotNil(t, granted)
	assert.Len(t, granted.Repos, 200)
}

func TestCreateInstallationToken_422ReturnsError(t *testing.T) {
	var calls int
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"The permissions requested are not granted to this installation."}`))
	}))
	defer mockGH.Close()

	_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 99, "test-org", "triage", nil)
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, err.Error(), "status 422")
}

func TestCreateInstallationToken_EmptyToken(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(installationTokenResponse{
			Token:     "",
			ExpiresAt: "2099-01-01T00:00:00Z",
		})
	}))
	defer mockGH.Close()

	_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "test-org", "coder", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty installation token")
}

func TestCreateInstallationToken_InvalidJSON(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer mockGH.Close()

	_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "test-org", "coder", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding token response")
}

func TestCreateInstallationToken_HTTPError(t *testing.T) {
	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})
	_, _, _, err := CreateInstallationToken(t.Context(), "http://example.invalid", "fake-jwt", 42, "test-org", "coder", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating installation token")
}

func TestCopyPermissions(t *testing.T) {
	in := map[string]string{"contents": "write", "packages": "read", "metadata": "read"}
	out := copyPermissions(in)
	assert.Equal(t, in, out)
	out["packages"] = "write"
	assert.Equal(t, "read", in["packages"], "input map must not be mutated")
}

func TestTruncateForLog(t *testing.T) {
	assert.Equal(t, "short", truncateForLog("short", 10))
	assert.Equal(t, "hello…", truncateForLog("hello world", 5))
	assert.Equal(t, "unchanged", truncateForLog("unchanged", 0))
	// Rune-aware: a multi-byte character must not be split.
	assert.Equal(t, "héllo…", truncateForLog("héllo world", 5))
	assert.Equal(t, "日本語…", truncateForLog("日本語テスト", 3))
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

func TestRolePermissions_CoderAndFixIncludePackagesRead(t *testing.T) {
	for _, role := range []string{"coder", "fix"} {
		perms := RolePermissionsFor(role)
		require.NotNil(t, perms, "role %q should exist", role)
		assert.Equal(t, "read", perms["packages"], "role %q: expected packages=read, got %q", role, perms["packages"])
	}
	// Assert expected permission counts to catch accidental additions/removals.
	assert.Len(t, RolePermissionsFor("coder"), 6, "coder role should have exactly 6 permissions")
	assert.Len(t, RolePermissionsFor("fix"), 5, "fix role should have exactly 5 permissions")
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

func TestCustomRolePermissions_AllowsAdminLevel(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	require.NoError(t, RegisterCustomRolePermissions(map[string]map[string]string{
		"project-admin": {"organization_projects": "admin"},
	}))
	assert.Equal(t, "admin", RolePermissionsFor("project-admin")["organization_projects"])
}

func TestCustomRolePermissions_RejectsEmptyPermissions(t *testing.T) {
	t.Cleanup(func() { _ = RegisterCustomRolePermissions(nil) })

	for _, permissions := range []map[string]map[string]string{
		{"scanner": {}},
		{"scanner": nil},
	} {
		err := RegisterCustomRolePermissions(permissions)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `custom role "scanner": no permissions defined`)
	}
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

	token, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "fake-jwt", 42, "test-org", "scanner", []string{"my-repo"})
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

		_, _, _, err := CreateInstallationToken(t.Context(), mockGH.URL, "jwt", 1, "test-org", "coder", nil)
		require.NoError(t, err)
	})
}
