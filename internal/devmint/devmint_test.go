package devmint

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockOIDCVerifier struct {
	err           error
	repositoryOwner string // if set, overrides "myorg"
	returnNilClaims bool   // simulate (nil, nil) from Verify
}

func (m *mockOIDCVerifier) Verify(_ context.Context, _ string) (*mintcore.Claims, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.returnNilClaims {
		return nil, nil
	}
	owner := m.repositoryOwner
	if owner == "" {
		owner = "myorg"
	}
	return &mintcore.Claims{
		Issuer:          "https://token.actions.githubusercontent.com",
		RepositoryOwner: owner,
	}, nil
}

type testInstallationResponse struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

type testInstallationTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

func testPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dataDir := t.TempDir()
	return New(Options{
		DataDir:        dataDir,
		Bind:           "127.0.0.1",
		Port:           0,
		Logger:         log.Default(),
		InsecureNoAuth: true,
	})
}

func storePEM(t *testing.T, srv *Server, org, role, appID, pemData string) {
	t.Helper()
	pemsDir := filepath.Join(srv.dataDir, "pems")
	require.NoError(t, os.MkdirAll(pemsDir, 0o700))

	pemPath := filepath.Join(pemsDir, role+".pem")
	require.NoError(t, os.WriteFile(pemPath, []byte(pemData), 0o600))

	cfg := DiskConfig{
		Org:   org,
		Roles: make(map[string]DiskRoleConfig),
	}

	configPath := filepath.Join(srv.dataDir, "config.json")
	existingData, err := os.ReadFile(configPath)
	if err == nil {
		json.Unmarshal(existingData, &cfg)
		cfg.Org = org
	}
	cfg.Roles[role] = DiskRoleConfig{AppID: appID}

	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o600))

	require.NoError(t, srv.loadFromDisk())
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestHealthWrongMethod(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestTokenMissingPEM(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"my-repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp errorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "no PEM configured")
}

func TestTokenInvalidRole(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(tokenRequest{Role: "../evil", Repos: []string{"repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp errorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "invalid role format")
}

func TestTokenInvalidRepoName(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"myorg/repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp errorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "invalid repo name")
}

func TestTokenRepoPathTraversal(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name string
		repo string
	}{
		{"leading dots", "..repo"},
		{"embedded double dots", "foo..bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{tt.repo}})
			req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestTokenMissingRole(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(tokenRequest{Repos: []string{"repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp errorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "role")
}

func TestTokenMissingRepos(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(tokenRequest{Role: "coder"})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp errorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "repos")
}

func TestTokenWrongMethod(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/token", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestTokenInvalidJSON(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTokenNoAuth(t *testing.T) {
	srv := newTestServer(t)
	pemData := testPEM(t)
	storePEM(t, srv, "myorg", "coder", "12345", pemData)

	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/myorg/repo/installation":
			json.NewEncoder(w).Encode(testInstallationResponse{
				ID:      42,
				Account: struct{ Login string `json:"login"` }{Login: "myorg"},
			})
		case r.URL.Path == "/app/installations/42/access_tokens":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(testInstallationTokenResponse{
				Token:     "ghs_mock_token",
				ExpiresAt: "2099-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockGH.Close()
	srv.githubBaseURL = mockGH.URL

	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "dev mint with insecureNoAuth should not require auth")
}

func TestTokenRootPath(t *testing.T) {
	srv := newTestServer(t)
	pemData := testPEM(t)
	storePEM(t, srv, "myorg", "coder", "12345", pemData)

	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/myorg/repo/installation":
			json.NewEncoder(w).Encode(testInstallationResponse{
				ID:      42,
				Account: struct{ Login string `json:"login"` }{Login: "myorg"},
			})
		case r.URL.Path == "/app/installations/42/access_tokens":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(testInstallationTokenResponse{
				Token:     "ghs_mock_token",
				ExpiresAt: "2099-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockGH.Close()
	srv.githubBaseURL = mockGH.URL

	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "root path should also handle token requests")
}

func TestUnknownPath(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v2/unknown", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDevMintNoFingerprintHeader(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"health", http.MethodGet, "/health"},
		{"token", http.MethodPost, "/v1/token"},
		{"status", http.MethodGet, "/v1/status"},
		{"not-found", http.MethodGet, "/unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.method == http.MethodPost {
				body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"r"}})
				req = httptest.NewRequest(tt.method, tt.path, bytes.NewReader(body))
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Empty(t, w.Header().Get("X-Fullsend-Dev-Mint"),
				"response must not fingerprint the server as a dev mint instance")
		})
	}
}

func TestDiskPersistence(t *testing.T) {
	dataDir := t.TempDir()
	pemData := testPEM(t)

	srv1 := New(Options{DataDir: dataDir, Bind: "127.0.0.1", Port: 0, Logger: log.Default(), InsecureNoAuth: true})
	storePEM(t, srv1, "myorg", "coder", "12345", pemData)
	storePEM(t, srv1, "myorg", "triage", "67890", pemData)

	srv2 := New(Options{DataDir: dataDir, Bind: "127.0.0.1", Port: 0, Logger: log.Default(), InsecureNoAuth: true})
	err := srv2.loadFromDisk()
	require.NoError(t, err)

	assert.Equal(t, "myorg", srv2.org)
	assert.Equal(t, "12345", srv2.appIDs["coder"])
	assert.Equal(t, "67890", srv2.appIDs["triage"])
	assert.Equal(t, []byte(pemData), srv2.pems["coder"])
	assert.Equal(t, []byte(pemData), srv2.pems["triage"])
}

func TestLoadFromDisk_RejectsInvalidRoleName(t *testing.T) {
	dataDir := t.TempDir()
	pemsDir := filepath.Join(dataDir, "pems")
	require.NoError(t, os.MkdirAll(pemsDir, 0o700))

	cfg := DiskConfig{
		Org:   "myorg",
		Roles: map[string]DiskRoleConfig{"../escape": {AppID: "12345"}},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "config.json"), data, 0o600))

	srv := New(Options{DataDir: dataDir, Bind: "127.0.0.1", Port: 0, Logger: log.Default(), InsecureNoAuth: true})
	err = srv.loadFromDisk()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestStatus(t *testing.T) {
	srv := newTestServer(t)
	pemData := testPEM(t)
	storePEM(t, srv, "myorg", "coder", "12345", pemData)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp statusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "myorg", resp.Org)
	assert.Len(t, resp.Roles, 1)
	assert.Equal(t, "coder", resp.Roles[0].Role)
	assert.Equal(t, "12345", resp.Roles[0].AppID)
}

func TestStatusEmpty(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp statusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Org)
	assert.Empty(t, resp.Roles)
}

func TestStatusWrongMethod(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestTokenMintSuccess(t *testing.T) {
	srv := newTestServer(t)
	pemData := testPEM(t)
	storePEM(t, srv, "myorg", "coder", "12345", pemData)

	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/myorg/my-repo/installation":
			assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
			json.NewEncoder(w).Encode(testInstallationResponse{
				ID:      99,
				Account: struct{ Login string `json:"login"` }{Login: "myorg"},
			})
		case r.URL.Path == "/app/installations/99/access_tokens":
			assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			assert.Contains(t, body, "permissions")
			assert.Contains(t, body, "repositories")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(testInstallationTokenResponse{
				Token:     "ghs_real_token_here",
				ExpiresAt: "2099-01-01T00:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockGH.Close()
	srv.githubBaseURL = mockGH.URL

	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"my-repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer some-oidc-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp tokenResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ghs_real_token_here", resp.Token)
	assert.Equal(t, "2099-01-01T00:00:00Z", resp.ExpiresAt)
}

func TestTokenMintGitHubError(t *testing.T) {
	srv := newTestServer(t)
	pemData := testPEM(t)
	storePEM(t, srv, "myorg", "coder", "12345", pemData)

	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "GitHub is down")
	}))
	defer mockGH.Close()
	srv.githubBaseURL = mockGH.URL

	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"my-repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestPEMHotReload(t *testing.T) {
	srv := newTestServer(t)
	pemData := testPEM(t)

	storePEM(t, srv, "myorg", "coder", "12345", pemData)

	srv.mu.RLock()
	assert.Equal(t, "myorg", srv.org)
	assert.Equal(t, "12345", srv.appIDs["coder"])
	assert.Equal(t, []byte(pemData), srv.pems["coder"])
	srv.mu.RUnlock()

	newPEM := testPEM(t)
	storePEM(t, srv, "myorg", "triage", "67890", newPEM)

	srv.mu.RLock()
	assert.Equal(t, "67890", srv.appIDs["triage"])
	assert.Equal(t, []byte(newPEM), srv.pems["triage"])
	srv.mu.RUnlock()

	configPath := filepath.Join(srv.dataDir, "config.json")
	_, err := os.Stat(configPath)
	assert.NoError(t, err, "config.json should exist on disk")

	pemPath := filepath.Join(srv.dataDir, "pems", "triage.pem")
	_, err = os.Stat(pemPath)
	assert.NoError(t, err, "triage.pem should exist on disk")
}

func TestNoPEMEndpoint(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(map[string]string{"org": "test", "role": "coder", "pem": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/pem", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code, "/v1/pem endpoint should no longer exist")
}

func TestTokenOIDCVerification(t *testing.T) {
	setupServer := func(t *testing.T, verifier mintcore.OIDCVerifier) *Server {
		t.Helper()
		srv := New(Options{
			DataDir:        t.TempDir(),
			Bind:           "127.0.0.1",
			Port:           0,
			Logger:         log.Default(),
			InsecureNoAuth: false,
		})
		srv.oidcVerifier = verifier
		pemData := testPEM(t)
		storePEM(t, srv, "myorg", "coder", "12345", pemData)

		mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/repos/myorg/repo/installation":
				json.NewEncoder(w).Encode(testInstallationResponse{
					ID:      42,
					Account: struct{ Login string `json:"login"` }{Login: "myorg"},
				})
			case r.URL.Path == "/app/installations/42/access_tokens":
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(testInstallationTokenResponse{
					Token:     "ghs_mock_token",
					ExpiresAt: "2099-01-01T00:00:00Z",
				})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(mockGH.Close)
		srv.githubBaseURL = mockGH.URL
		return srv
	}

	t.Run("valid token", func(t *testing.T) {
		srv := setupServer(t, &mockOIDCVerifier{})
		body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
		req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer valid-oidc-token")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("invalid token", func(t *testing.T) {
		srv := setupServer(t, &mockOIDCVerifier{err: fmt.Errorf("token verification failed")})
		body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
		req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer bad-token")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("missing auth header", func(t *testing.T) {
		srv := setupServer(t, &mockOIDCVerifier{})
		body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
		req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestStatusOIDCVerification(t *testing.T) {
	srv := New(Options{
		DataDir:        t.TempDir(),
		Bind:           "127.0.0.1",
		Port:           0,
		Logger:         log.Default(),
		InsecureNoAuth: false,
	})
	srv.oidcVerifier = &mockOIDCVerifier{err: fmt.Errorf("forbidden")}

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestOrgMismatchReturns403(t *testing.T) {
	dataDir := t.TempDir()
	srv := New(Options{
		DataDir:        dataDir,
		Bind:           "127.0.0.1",
		Port:           0,
		Logger:         log.Default(),
		InsecureNoAuth: false,
	})
	storePEM(t, srv, "myorg", "coder", "12345", testPEM(t))
	// Verifier returns claims for a different org.
	srv.oidcVerifier = &mockOIDCVerifier{repositoryOwner: "other-org"}

	t.Run("handleToken org mismatch", func(t *testing.T) {
		body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
		req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer valid-token")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("handleStatus org mismatch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		req.Header.Set("Authorization", "Bearer valid-token")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestNilClaimsFailClosed(t *testing.T) {
	dataDir := t.TempDir()
	srv := New(Options{
		DataDir:        dataDir,
		Bind:           "127.0.0.1",
		Port:           0,
		Logger:         log.Default(),
		InsecureNoAuth: false,
	})
	storePEM(t, srv, "myorg", "coder", "12345", testPEM(t))
	srv.oidcVerifier = &mockOIDCVerifier{returnNilClaims: true}

	t.Run("handleToken nil claims", func(t *testing.T) {
		body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
		req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer valid-token")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("handleStatus nil claims", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		req.Header.Set("Authorization", "Bearer valid-token")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestNilVerifierReturns503(t *testing.T) {
	srv := New(Options{
		DataDir:        t.TempDir(),
		Bind:           "127.0.0.1",
		Port:           0,
		Logger:         log.Default(),
		InsecureNoAuth: false,
	})

	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
