package devmint

import (
	"bytes"
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

	cfg := diskConfig{
		Org:   org,
		Roles: make(map[string]diskRoleConfig),
	}

	configPath := filepath.Join(srv.dataDir, "config.json")
	existingData, err := os.ReadFile(configPath)
	if err == nil {
		json.Unmarshal(existingData, &cfg)
		cfg.Org = org
	}
	cfg.Roles[role] = diskRoleConfig{AppID: appID}

	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o600))

	srv.mu.Lock()
	defer srv.mu.Unlock()
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
	body, _ := json.Marshal(tokenRequest{Role: "coder", Repos: []string{"..repo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
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
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
				ID:      42,
				Account: struct{ Login string `json:"login"` }{Login: "myorg"},
			})
		case r.URL.Path == "/app/installations/42/access_tokens":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
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
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
				ID:      42,
				Account: struct{ Login string `json:"login"` }{Login: "myorg"},
			})
		case r.URL.Path == "/app/installations/42/access_tokens":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
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

func TestDevMintHeader(t *testing.T) {
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

			assert.Equal(t, "true", w.Header().Get("X-Fullsend-Dev-Mint"))
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
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
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
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
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
