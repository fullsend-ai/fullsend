package fetchsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// stubUploader records upload calls without requiring openshell.
type stubUploader struct {
	calls []uploadCall
}

type uploadCall struct {
	sandboxName, localPath, remotePath string
}

func (u *stubUploader) UploadSkillDir(sandboxName, localPath, remotePath string) error {
	u.calls = append(u.calls, uploadCall{sandboxName, localPath, remotePath})
	return nil
}

// testHarness returns a Harness with allowed_remote_resources set.
func testHarness(prefixes ...string) *harness.Harness {
	return &harness.Harness{
		Agent:                  "agents/code.md",
		AllowedRemoteResources: prefixes,
	}
}

// fakeSkillFiles returns a minimal skill directory for testing.
func fakeSkillFiles() map[string][]byte {
	return map[string][]byte{
		"SKILL.md": []byte("---\nname: test-skill\n---\n# Test Skill\n"),
	}
}

// fakeSkillHash returns the tree hash for fakeSkillFiles().
func fakeSkillHash() string {
	return fetch.ComputeTreeHash(fakeSkillFiles())
}

// setupFakeForge configures a FakeClient with a skill directory at the given owner/repo/path@ref.
func setupFakeForge(owner, repo, dirPath, ref string, files map[string][]byte) *forge.FakeClient {
	fc := forge.NewFakeClient()
	var entries []forge.DirectoryEntry
	for p := range files {
		entries = append(entries, forge.DirectoryEntry{Path: p, Type: "file", Size: len(files[p])})
	}
	fc.DirContents[owner+"/"+repo+"/"+dirPath+"@"+ref] = entries
	for p, content := range files {
		fc.FileContentsRef[owner+"/"+repo+"/"+dirPath+"/"+p+"@"+ref] = content
	}
	return fc
}

func TestHandleFetch_CacheHit(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()

	// Pre-populate cache.
	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/test-skill", files)

	uploader := &stubUploader{}
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		Uploader:      uploader,
		SkillDestDir:  "/sandbox/skills",
		AuditLogPath:  filepath.Join(tmpDir, "audit.jsonl"),
		TraceID:       "trace-1",
	})

	resp, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.LocalPath, "test-skill") {
		t.Fatalf("LocalPath %q should contain skill name", resp.LocalPath)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(uploader.calls))
	}

	// Verify audit log was written.
	auditData, err := os.ReadFile(filepath.Join(tmpDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	if !strings.Contains(string(auditData), `"fetch_type":"runtime"`) {
		t.Fatal("audit log should contain fetch_type runtime")
	}
	if !strings.Contains(string(auditData), `"cache_hit":true`) {
		t.Fatal("audit log should show cache_hit true")
	}
}

func TestHandleFetch_ForgeFetch(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()

	fc := setupFakeForge("org", "repo", "skills/test-skill", "abc123", files)
	uploader := &stubUploader{}
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		ForgeClient:   fc,
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		Uploader:      uploader,
		SkillDestDir:  "/sandbox/skills",
		AuditLogPath:  filepath.Join(tmpDir, "audit.jsonl"),
		TraceID:       "trace-2",
	})

	resp, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.LocalPath == "" {
		t.Fatal("expected non-empty LocalPath")
	}

	// Verify cache was populated.
	treePath, _, cacheErr := fetch.CacheGetDir(tmpDir, hash)
	if cacheErr != nil {
		t.Fatalf("cache lookup: %v", cacheErr)
	}
	if treePath == "" {
		t.Fatal("skill should be cached after fetch")
	}

	// Verify audit log shows cache miss.
	auditData, _ := os.ReadFile(filepath.Join(tmpDir, "audit.jsonl"))
	if !strings.Contains(string(auditData), `"cache_hit":false`) {
		t.Fatal("audit log should show cache_hit false for fresh fetch")
	}
}

func TestHandleFetch_NotInAllowlist(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/allowed-org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/other-org/repo/tree/abc123/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	if err == nil {
		t.Fatal("expected error for URL not in allowlist")
	}
	if !strings.Contains(err.Error(), "not in allowed_remote_resources") {
		t.Fatalf("error should mention allowlist: %v", err)
	}
}

func TestHandleFetch_MissingHash(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/foo",
	})

	if err == nil {
		t.Fatal("expected error for missing hash")
	}
	if !strings.Contains(err.Error(), "integrity hash") {
		t.Fatalf("error should mention integrity hash: %v", err)
	}
}

func TestHandleFetch_IntegrityMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	wrongHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	fc := setupFakeForge("org", "repo", "skills/test-skill", "abc123", files)
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		ForgeClient:   fc,
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + wrongHash,
	})

	if err == nil {
		t.Fatal("expected integrity error")
	}
	if !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("error should mention integrity check: %v", err)
	}
}

func TestHandleFetch_RateLimitExceeded(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()

	// Pre-populate cache so requests succeed until rate limit hits.
	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/test-skill", files)

	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    2,
		SkillDestDir:  "/sandbox/skills",
	})

	url := "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash

	// First 2 should succeed.
	for i := 0; i < 2; i++ {
		_, err := svc.HandleFetch(context.Background(), FetchRequest{URL: url})
		if err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
	}

	// 3rd should fail.
	_, err := svc.HandleFetch(context.Background(), FetchRequest{URL: url})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("error should mention rate limit: %v", err)
	}
}

func TestHandleFetch_RateLimitRollbackOnFailure(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    1,
	})

	// First request consumes a slot but fails (no forge client, cache miss).
	// The slot should be rolled back.
	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err == nil {
		t.Fatal("expected error for missing forge client")
	}

	// Second request should NOT be rate-limited because the first slot was released.
	_, err = svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/bar#sha256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if err == nil {
		t.Fatal("expected error (no forge client)")
	}
	if strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatal("should not be rate-limited; failed slots should be rolled back")
	}
}

func TestHandleFetch_NonForgeURL(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://example.com/skills/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://example.com/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	if err == nil {
		t.Fatal("expected error for non-forge URL")
	}
	if !strings.Contains(err.Error(), "supported forge") {
		t.Fatalf("error should mention forge: %v", err)
	}
}

func TestHandleFetch_OfflineMode(t *testing.T) {
	svc := New(ServiceConfig{
		Harness: testHarness("https://github.com/org/repo/"),
		FetchPolicy: fetch.FetchPolicy{
			Offline: true,
		},
		ForgeClient:   forge.NewFakeClient(),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	if err == nil {
		t.Fatal("expected error in offline mode with cache miss")
	}
	if !strings.Contains(err.Error(), "offline") {
		t.Fatalf("error should mention offline: %v", err)
	}
}

func TestHandleFetch_EmptyURL(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{URL: ""})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestHandleFetch_InvalidURL(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{URL: "not-a-url"})
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestServeHTTP_Success(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()

	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/test-skill", files)

	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		SkillDestDir:  "/sandbox/skills",
	})

	body, _ := json.Marshal(FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash,
	})
	req := httptest.NewRequest(http.MethodPost, "/fetch", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	svc.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp FetchResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != "" {
		t.Fatalf("unexpected error in response: %s", resp.Error)
	}
	if resp.LocalPath == "" {
		t.Fatal("expected non-empty LocalPath")
	}
}

func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	req := httptest.NewRequest(http.MethodGet, "/fetch", nil)
	rec := httptest.NewRecorder()

	svc.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestServeHTTP_BadJSON(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	req := httptest.NewRequest(http.MethodPost, "/fetch", strings.NewReader("not json"))
	rec := httptest.NewRecorder()

	svc.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeHTTP_OversizedBody(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	longURL := `{"url":"https://example.com/` + strings.Repeat("a", maxRequestBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/fetch", strings.NewReader(longURL))
	rec := httptest.NewRecorder()

	svc.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var resp FetchResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != "request body too large" {
		t.Fatalf("error = %q, want 'request body too large'", resp.Error)
	}
}

func TestServeHTTP_Forbidden(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/allowed-org/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	body, _ := json.Marshal(FetchRequest{
		URL: "https://github.com/other-org/repo/tree/abc123/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	req := httptest.NewRequest(http.MethodPost, "/fetch", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	svc.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestServeHTTP_RateLimit(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()
	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/test-skill", files)

	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    1,
		SkillDestDir:  "/sandbox/skills",
	})

	url := "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash

	// First request succeeds.
	body, _ := json.Marshal(FetchRequest{URL: url})
	req := httptest.NewRequest(http.MethodPost, "/fetch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	svc.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", rec.Code)
	}

	// Second request should be rate limited.
	body, _ = json.Marshal(FetchRequest{URL: url})
	req = httptest.NewRequest(http.MethodPost, "/fetch", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	svc.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: status = %d, want 429", rec.Code)
	}
}

func TestHandleFetch_NoForgeClient(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	if err == nil {
		t.Fatal("expected error when forge client is nil")
	}
	if !strings.Contains(err.Error(), "forge client is required") {
		t.Fatalf("error should mention forge client: %v", err)
	}
}

func TestHandleFetch_UploadCalled(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()
	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/my-skill", files)

	uploader := &stubUploader{}
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		Uploader:      uploader,
		SandboxName:   "sandbox-1",
		SkillDestDir:  "/sandbox/claude-config/skills",
	})

	resp, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/my-skill#sha256=" + hash,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(uploader.calls) != 1 {
		t.Fatalf("expected 1 upload, got %d", len(uploader.calls))
	}
	call := uploader.calls[0]
	if call.sandboxName != "sandbox-1" {
		t.Fatalf("sandboxName = %q, want sandbox-1", call.sandboxName)
	}
	if !strings.Contains(call.remotePath, "my-skill") {
		t.Fatalf("remotePath %q should contain skill name", call.remotePath)
	}
	if !strings.HasPrefix(call.remotePath, "/sandbox/claude-config/skills/") {
		t.Fatalf("remotePath %q should start with skill dest dir", call.remotePath)
	}
	// Hash prefix should be in the path.
	if !strings.Contains(call.remotePath, hash[:8]) {
		t.Fatalf("remotePath %q should contain hash prefix %s", call.remotePath, hash[:8])
	}
	_ = resp // LocalPath verified via uploader.calls
}
