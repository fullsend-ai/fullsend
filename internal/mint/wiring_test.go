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

	verifier := mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
		HTTPClient:         &http.Client{Timeout: 5 * time.Second},
		GCPProjectNum:      "123456",
		WIFPoolName:        "test-pool",
		DefaultWIFProvider: "test-provider",
		AllowedOrgs:        []string{"test-org"},
		AllowedWorkflows:   []string{"*"},
		OIDCAudience:       "fullsend-mint",
	})

	pemAccessor := mintcore.NewGCPSecretPEMAccessor(
		&http.Client{Timeout: 5 * time.Second},
		"123456",
	)

	handler, err := mintcore.NewHandler(pemAccessor, verifier)
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
