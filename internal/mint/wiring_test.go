package function

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// TestInitWiring verifies that the same composition used by init() —
// STSVerifier + GCPSecretPEMAccessor + NewHandler — produces a handler
// that routes requests correctly. This catches wiring regressions that
// unit tests with fakes cannot.
func TestInitWiring(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"100"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("WIF_POOL_NAME", "test-pool")
	t.Setenv("WIF_PROVIDER_NAME", "test-provider")

	httpClient := &http.Client{Timeout: 5 * time.Second}

	// Use a direct factory wrapping NewSTSVerifier so we can inject
	// a specific HTTP client for tests (matching init()'s composition).
	stsFactory := func() (mintcore.OIDCVerifier, error) {
		return mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
			HTTPClient:         httpClient,
			Audience:           "fullsend-mint",
			GCPProjectNum:      "123456",
			WIFPoolName:        "test-pool",
			DefaultWIFProvider: "test-provider",
		})
	}

	pemAccessor := mintcore.NewGCPSecretPEMAccessor(
		&http.Client{Timeout: 5 * time.Second},
		"123456",
	)

	handler, err := mintcore.NewHandler(stsFactory, pemAccessor)
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	t.Run("health", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("token without auth returns 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/token", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("status without auth returns 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/nonexistent", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("starts without ALLOWED_ORGS", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "")
		t.Setenv("PER_REPO_WIF_REPOS", "test-org/my-repo")

		h, err := mintcore.NewHandler(stsFactory, pemAccessor)
		if err != nil {
			t.Fatalf("NewHandler should succeed without ALLOWED_ORGS: %v", err)
		}

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("status with invalid token returns 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		var resp map[string]string
		json.NewDecoder(rec.Body).Decode(&resp)
		if resp["error"] != "authentication failed" {
			t.Fatalf("expected 'authentication failed', got %q", resp["error"])
		}
	})
}
