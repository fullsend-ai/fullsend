package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

func setupTestPEMDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	for _, role := range []string{"coder", "triage", "review", "retro", "prioritize", "fullsend"} {
		if err := os.WriteFile(filepath.Join(dir, role+".pem"), pemData, 0600); err != nil {
			t.Fatalf("writing %s.pem: %v", role, err)
		}
	}
	return dir
}

func TestCheckRequired(t *testing.T) {
	t.Setenv("TEST_SET_VAR", "value")

	t.Run("all set", func(t *testing.T) {
		missing := checkRequired("TEST_SET_VAR")
		if len(missing) != 0 {
			t.Fatalf("expected no missing, got %v", missing)
		}
	})

	t.Run("some missing", func(t *testing.T) {
		missing := checkRequired("TEST_SET_VAR", "TOTALLY_UNSET_VAR")
		if len(missing) != 1 || missing[0] != "TOTALLY_UNSET_VAR" {
			t.Fatalf("expected [TOTALLY_UNSET_VAR], got %v", missing)
		}
	})

	t.Run("all missing", func(t *testing.T) {
		missing := checkRequired("UNSET_A", "UNSET_B")
		if len(missing) != 2 {
			t.Fatalf("expected 2 missing, got %v", missing)
		}
	})
}

func TestSplitCSV_ViaExported(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		result := mintcore.SplitCSV("")
		if result != nil {
			t.Fatalf("expected nil, got %v", result)
		}
	})

	t.Run("single value", func(t *testing.T) {
		result := mintcore.SplitCSV("foo")
		if len(result) != 1 || result[0] != "foo" {
			t.Fatalf("expected [foo], got %v", result)
		}
	})

	t.Run("multiple values with spaces", func(t *testing.T) {
		result := mintcore.SplitCSV("foo, bar , baz")
		if len(result) != 3 || result[0] != "foo" || result[1] != "bar" || result[2] != "baz" {
			t.Fatalf("expected [foo bar baz], got %v", result)
		}
	})

	t.Run("trailing comma", func(t *testing.T) {
		result := mintcore.SplitCSV("foo,bar,")
		if len(result) != 2 {
			t.Fatalf("expected 2 items, got %v", result)
		}
	})
}

func TestParseLocalRoles(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		roles, err := parseLocalRoles("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(roles) != 0 {
			t.Fatalf("expected empty map, got %v", roles)
		}
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		_, err := parseLocalRoles("not-json")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
		if !strings.Contains(err.Error(), "ROLE_APP_IDS") {
			t.Fatalf("expected error mentioning ROLE_APP_IDS, got %v", err)
		}
		if !strings.Contains(err.Error(), "failed to parse") {
			t.Fatalf("expected 'failed to parse' prefix, got %v", err)
		}
	})

	t.Run("plain role keys", func(t *testing.T) {
		roles, err := parseLocalRoles(`{"triage":"100","coder":"200"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !roles["triage"] || !roles["coder"] {
			t.Fatalf("expected triage and coder, got %v", roles)
		}
	})

	t.Run("org-prefixed keys are skipped", func(t *testing.T) {
		roles, err := parseLocalRoles(`{"my-org/triage":"100"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if roles["triage"] {
			t.Fatalf("expected org-prefixed key to be skipped, got %v", roles)
		}
		if len(roles) != 0 {
			t.Fatalf("expected empty map, got %v", roles)
		}
	})

	t.Run("mixed plain and org-prefixed keys", func(t *testing.T) {
		roles, err := parseLocalRoles(`{"triage":"100","my-org/coder":"200"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !roles["triage"] {
			t.Fatalf("expected triage, got %v", roles)
		}
		if roles["coder"] {
			t.Fatalf("expected org-prefixed coder to be skipped, got %v", roles)
		}
		if len(roles) != 1 {
			t.Fatalf("expected 1 role, got %v", roles)
		}
	})

	t.Run("filters unknown roles", func(t *testing.T) {
		roles, err := parseLocalRoles(`{"triage":"100","unknownrole":"200"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !roles["triage"] {
			t.Fatalf("expected triage, got %v", roles)
		}
		if roles["unknownrole"] {
			t.Fatalf("expected unknownrole to be filtered out, got %v", roles)
		}
	})
}

func TestSortedKeys(t *testing.T) {
	keys := sortedKeys(map[string]bool{"c": true, "a": true, "b": true})
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Fatalf("expected [a b c], got %v", keys)
	}

	empty := sortedKeys(map[string]bool{})
	if len(empty) != 0 {
		t.Fatalf("expected empty, got %v", empty)
	}
}

func TestRun_MissingEnvVars(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "")
	t.Setenv("ROLE_APP_IDS", "")
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("PEM_DIR", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := run(ctx)
	if err == nil {
		t.Fatal("expected error for missing env vars")
	}
	if !strings.Contains(err.Error(), "required environment variables not set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_InvalidPEMDir(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("PEM_DIR", "/nonexistent/path")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := run(ctx)
	if err == nil {
		t.Fatal("expected error for invalid PEM dir")
	}
}

func TestRun_SuccessfulStartAndShutdown(t *testing.T) {
	pemDir := setupTestPEMDir(t)

	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("PEM_DIR", pemDir)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("PORT", "0")
	t.Setenv("FALLBACK_MINT_URL", "")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- run(ctx)
	}()

	// Give server a moment to start, then cancel to trigger shutdown
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after context cancellation")
	}
}

func TestRun_CustomPort(t *testing.T) {
	pemDir := setupTestPEMDir(t)

	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("PEM_DIR", pemDir)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("PORT", "19876")
	t.Setenv("FALLBACK_MINT_URL", "")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after context cancellation")
	}
}

func TestRun_WithFallback(t *testing.T) {
	pemDir := setupTestPEMDir(t)

	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("PEM_DIR", pemDir)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("PORT", "0")
	t.Setenv("FALLBACK_MINT_URL", "https://upstream.example.com")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after context cancellation")
	}
}

func TestBuildHandler(t *testing.T) {
	pemDir := setupTestPEMDir(t)

	t.Run("without fallback", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("FALLBACK_MINT_URL", "")

		h, err := buildHandler()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected non-nil handler")
		}
	})

	t.Run("with fallback", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("FALLBACK_MINT_URL", "https://upstream.example.com")

		h, err := buildHandler()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected non-nil handler")
		}
	})

	t.Run("with per-repo WIF repos", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("FALLBACK_MINT_URL", "")
		t.Setenv("PER_REPO_WIF_REPOS", "test-org/my-repo,test-org/other")

		h, err := buildHandler()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected non-nil handler")
		}
	})

	t.Run("invalid PEM dir", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", "/nonexistent/path")
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for invalid PEM dir")
		}
	})

	t.Run("with custom role permissions", func(t *testing.T) {
		t.Cleanup(func() { _ = mintcore.RegisterCustomRolePermissions(nil) })
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200","scanner":"300"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("FALLBACK_MINT_URL", "")
		t.Setenv("CUSTOM_ROLE_PERMISSIONS", `{"scanner":{"contents":"read","security_events":"write"}}`)

		h, err := buildHandler()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected non-nil handler")
		}
	})

	t.Run("custom role collides with built-in", func(t *testing.T) {
		t.Cleanup(func() { _ = mintcore.RegisterCustomRolePermissions(nil) })
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("CUSTOM_ROLE_PERMISSIONS", `{"triage":{"contents":"write"}}`)

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for built-in role collision")
		}
		if !strings.Contains(err.Error(), "collides with built-in role") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("http fallback URL rejected", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("FALLBACK_MINT_URL", "http://insecure.example.com")

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for non-HTTPS fallback URL")
		}
		if !strings.Contains(err.Error(), "https://") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("empty host fallback URL rejected", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("FALLBACK_MINT_URL", "https://")

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for empty-host fallback URL")
		}
	})

	t.Run("custom role with invalid permission level", func(t *testing.T) {
		t.Cleanup(func() { _ = mintcore.RegisterCustomRolePermissions(nil) })
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("CUSTOM_ROLE_PERMISSIONS", `{"scanner":{"contents":"admin"}}`)

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for invalid permission level")
		}
		if !strings.Contains(err.Error(), "invalid level") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("custom role with invalid name", func(t *testing.T) {
		t.Cleanup(func() { _ = mintcore.RegisterCustomRolePermissions(nil) })
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("CUSTOM_ROLE_PERMISSIONS", `{"Invalid-Name":{"contents":"read"}}`)

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for invalid custom role name")
		}
		if !strings.Contains(err.Error(), "invalid role name") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid ROLE_APP_IDS with fallback", func(t *testing.T) {
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `not-json`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("FALLBACK_MINT_URL", "https://upstream.example.com")

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for invalid ROLE_APP_IDS with fallback")
		}
		if !strings.Contains(err.Error(), "ROLE_APP_IDS") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid custom role permissions JSON", func(t *testing.T) {
		t.Cleanup(func() { _ = mintcore.RegisterCustomRolePermissions(nil) })
		t.Setenv("ALLOWED_ORGS", "test-org")
		t.Setenv("ROLE_APP_IDS", `{"triage":"200"}`)
		t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
		t.Setenv("PEM_DIR", pemDir)
		t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
		t.Setenv("CUSTOM_ROLE_PERMISSIONS", `not-json`)

		_, err := buildHandler()
		if err == nil {
			t.Fatal("expected error for invalid CUSTOM_ROLE_PERMISSIONS")
		}
		if !strings.Contains(err.Error(), "CUSTOM_ROLE_PERMISSIONS") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestStandaloneWiring(t *testing.T) {
	pemDir := setupTestPEMDir(t)

	t.Setenv("ROLE_APP_IDS", `{"coder":"100","triage":"200","review":"300","fullsend":"500"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	verifier := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
		IssuerURL:            "https://token.actions.githubusercontent.com",
		Audience:             "fullsend-mint",
		HTTPClient:           &http.Client{Timeout: 5 * time.Second},
		AllowedOrgs:          []string{"test-org"},
		AllowedWorkflowFiles: []string{"*"},
	})

	pemAccessor, err := mintcore.NewFilesystemPEMAccessor(pemDir)
	if err != nil {
		t.Fatalf("NewFilesystemPEMAccessor: %v", err)
	}

	handler, err := mintcore.NewHandler(pemAccessor, verifier)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
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
}
