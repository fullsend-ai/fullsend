package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

func TestStartFetchService_StartsAndStops(t *testing.T) {
	cfg := fetchsvc.ServiceConfig{
		Harness:       &harness.Harness{Agent: "agents/test.md"},
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	}

	addr, token, shutdown, err := startFetchService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("startFetchService: %v", err)
	}
	defer shutdown()

	if addr == "" {
		t.Fatal("addr should not be empty")
	}
	if token == "" {
		t.Fatal("token should not be empty")
	}
	if len(token) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(token))
	}

	// Health endpoint should be reachable without auth.
	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", addr))
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

func TestStartFetchService_FetchEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	files := map[string][]byte{
		"SKILL.md": []byte("---\nname: srv-test\n---\n# Test\n"),
	}
	hash := fetch.ComputeTreeHash(files)
	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc/skills/test", files)

	cfg := fetchsvc.ServiceConfig{
		Harness: &harness.Harness{
			Agent:                  "agents/test.md",
			AllowedRemoteResources: []string{"https://github.com/org/repo/"},
		},
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		SkillDestDir:  "/sandbox/skills",
	}

	addr, token, shutdown, err := startFetchService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("startFetchService: %v", err)
	}
	defer shutdown()

	body, _ := json.Marshal(fetchsvc.FetchRequest{
		URL: "https://github.com/org/repo/tree/abc/skills/test#sha256=" + hash,
	})
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/fetch", addr), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var fetchResp fetchsvc.FetchResponse
	json.NewDecoder(resp.Body).Decode(&fetchResp)
	if fetchResp.LocalPath == "" {
		t.Fatal("expected non-empty LocalPath")
	}
	if fetchResp.Error != "" {
		t.Fatalf("unexpected error: %s", fetchResp.Error)
	}
}

func TestStartFetchService_RejectsWithoutAuth(t *testing.T) {
	cfg := fetchsvc.ServiceConfig{
		Harness:       &harness.Harness{Agent: "agents/test.md"},
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	}

	addr, _, shutdown, err := startFetchService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("startFetchService: %v", err)
	}
	defer shutdown()

	body, _ := json.Marshal(fetchsvc.FetchRequest{URL: "https://example.com/skill#sha256=aaa"})
	resp, err := http.Post(fmt.Sprintf("http://%s/fetch", addr), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestStartFetchService_RejectsWrongToken(t *testing.T) {
	cfg := fetchsvc.ServiceConfig{
		Harness:       &harness.Harness{Agent: "agents/test.md"},
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	}

	addr, _, shutdown, err := startFetchService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("startFetchService: %v", err)
	}
	defer shutdown()

	body, _ := json.Marshal(fetchsvc.FetchRequest{URL: "https://example.com/skill#sha256=aaa"})
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/fetch", addr), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWithBearerAuth_ValidToken(t *testing.T) {
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := withBearerAuth("my-secret", inner)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer my-secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("inner handler should have been called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestWithBearerAuth_InvalidToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called with invalid token")
	})

	handler := withBearerAuth("my-secret", inner)

	tests := []struct {
		name string
		auth string
	}{
		{"empty", ""},
		{"no bearer prefix", "my-secret"},
		{"wrong token", "Bearer wrong"},
		{"basic auth", "Basic dXNlcjpwYXNz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestGenerateToken_Unique(t *testing.T) {
	t1, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	t2, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}

	if t1 == t2 {
		t.Fatal("two generated tokens should not be equal")
	}
	if len(t1) != 64 {
		t.Fatalf("token length = %d, want 64", len(t1))
	}
	if strings.Trim(t1, "0123456789abcdef") != "" {
		t.Fatalf("token %q should be lowercase hex", t1)
	}
}
