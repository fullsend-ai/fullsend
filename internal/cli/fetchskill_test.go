package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
)

func TestRunFetchSkill_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{
			LocalPath: "/sandbox/claude-config/skills/abc12345-my-skill",
		})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/org/repo/tree/abc/skills/my-skill#sha256=abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := stdout.String()
	want := "/sandbox/claude-config/skills/abc12345-my-skill\n"
	if got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunFetchSkill_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{
			Error: "url not in allowed_remote_resources",
		})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/evil/repo/tree/abc/skills/bad#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for forbidden URL")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty on error, got %q", stdout.String())
	}
	if !strings.Contains(err.Error(), "not in allowed_remote_resources") {
		t.Fatalf("error should mention allowlist: %v", err)
	}
}

func TestRunFetchSkill_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{
			Error: "runtime fetch rate limit exceeded",
		})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for rate-limited request")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("error should mention rate limit: %v", err)
	}
}

func TestRunFetchSkill_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{
			Error: "failed to cache skill directory",
		})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for server error")
	}
	if !strings.Contains(err.Error(), "cache skill directory") {
		t.Fatalf("error should contain server message: %v", err)
	}
}

func TestRunFetchSkill_NonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error should include status code: %v", err)
	}
}

func TestRunFetchSkill_ErrorWithEmptyMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("error should include status code: %v", err)
	}
}

func TestRunFetchSkill_MissingFetchURL(t *testing.T) {
	t.Setenv("FULLSEND_FETCH_URL", "")
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://example.com/skill#sha256=aaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for missing FULLSEND_FETCH_URL")
	}
	if !strings.Contains(err.Error(), "FULLSEND_FETCH_URL") {
		t.Fatalf("error should mention env var: %v", err)
	}
}

func TestRunFetchSkill_MissingToken(t *testing.T) {
	t.Setenv("FULLSEND_FETCH_URL", "http://localhost:9999/fetch")
	t.Setenv("FULLSEND_FETCH_TOKEN", "")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://example.com/skill#sha256=aaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for missing FULLSEND_FETCH_TOKEN")
	}
	if !strings.Contains(err.Error(), "FULLSEND_FETCH_TOKEN") {
		t.Fatalf("error should mention env var: %v", err)
	}
}

func TestRunFetchSkill_AuthTokenSent(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{LocalPath: "/path"})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "secret-abc-123")

	var stdout, stderr bytes.Buffer
	_ = runFetchSkill("https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)

	if receivedAuth != "Bearer secret-abc-123" {
		t.Fatalf("Authorization header = %q, want %q", receivedAuth, "Bearer secret-abc-123")
	}
}

func TestRunFetchSkill_RequestBody(t *testing.T) {
	var receivedReq fetchsvc.FetchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{LocalPath: "/path"})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "t")

	skillURL := "https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var stdout, stderr bytes.Buffer
	_ = runFetchSkill(skillURL, &stdout, &stderr)

	if receivedReq.URL != skillURL {
		t.Fatalf("request URL = %q, want %q", receivedReq.URL, skillURL)
	}
}

func TestRunFetchSkill_ConnectionRefused(t *testing.T) {
	t.Setenv("FULLSEND_FETCH_URL", "http://127.0.0.1:1/fetch")
	t.Setenv("FULLSEND_FETCH_TOKEN", "t")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
	if !strings.Contains(err.Error(), "fetch request failed") {
		t.Fatalf("error should mention fetch failure: %v", err)
	}
}

func TestRunFetchSkill_InvalidFetchURL(t *testing.T) {
	t.Setenv("FULLSEND_FETCH_URL", "://bad-url")
	t.Setenv("FULLSEND_FETCH_TOKEN", "t")

	var stdout, stderr bytes.Buffer
	err := runFetchSkill("https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	if !strings.Contains(err.Error(), "failed to create request") {
		t.Fatalf("error should mention request creation failure: %v", err)
	}
}

func TestNewFetchSkillCmd_Registration(t *testing.T) {
	cmd := newFetchSkillCmd()
	if cmd.Use != "fetch-skill <url>" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "fetch-skill <url>")
	}
	if !cmd.SilenceUsage {
		t.Fatal("SilenceUsage should be true")
	}
	if !cmd.SilenceErrors {
		t.Fatal("SilenceErrors should be true")
	}
}

func TestNewFetchSkillCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newFetchSkillCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for no args")
	}

	cmd = newFetchSkillCmd()
	cmd.SetArgs([]string{"url1", "url2"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for too many args")
	}
}

func TestNewFetchSkillCmd_ExecutesThroughRunE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fetchsvc.FetchResponse{LocalPath: "/skill/path"})
	}))
	defer srv.Close()

	t.Setenv("FULLSEND_FETCH_URL", srv.URL)
	t.Setenv("FULLSEND_FETCH_TOKEN", "test-token")

	var stdout bytes.Buffer
	cmd := newFetchSkillCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"https://github.com/org/repo/tree/abc/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "/skill/path") {
		t.Fatalf("stdout should contain skill path, got %q", stdout.String())
	}
}
