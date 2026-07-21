package fetchsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
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

// fakeTreeFetcher returns a TreeFetchFunc that returns the given files for any request.
func fakeTreeFetcher(files map[string][]byte) gitfetch.TreeFetchFunc {
	return func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return files, nil
	}
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

	uploader := &stubUploader{}
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		TreeFetcher:   fakeTreeFetcher(files),
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

	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		TreeFetcher:   fakeTreeFetcher(files),
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
		TreeFetcher: func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
			return nil, fmt.Errorf("fetch failed")
		},
	})

	// First request consumes a slot but fails (fetch error, cache miss).
	// The slot should be rolled back.
	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err == nil {
		t.Fatal("expected error for fetch failure")
	}

	// Second request should NOT be rate-limited because the first slot was released.
	_, err = svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/bar#sha256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if err == nil {
		t.Fatal("expected error (fetch failure)")
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

func TestHandleFetch_GitLabURLRejected(t *testing.T) {
	svc := New(ServiceConfig{
		Harness:       testHarness("https://gitlab.com/org/repo/"),
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://gitlab.com/org/repo/-/tree/main/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	if err == nil {
		t.Fatal("expected error for GitLab URL")
	}
	if !strings.Contains(err.Error(), "fetch support has not landed yet") {
		t.Fatalf("error should mention fetch support: %v", err)
	}
}

func TestHandleFetch_OfflineMode(t *testing.T) {
	svc := New(ServiceConfig{
		Harness: testHarness("https://github.com/org/repo/"),
		FetchPolicy: fetch.FetchPolicy{
			Offline: true,
		},
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

func TestHandleFetch_TreeFetcherError(t *testing.T) {
	svc := New(ServiceConfig{
		Harness: testHarness("https://github.com/org/repo/"),
		TreeFetcher: func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
			return nil, fmt.Errorf("git fetch failed")
		},
		WorkspaceRoot: t.TempDir(),
		MaxFetches:    10,
	})

	_, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/foo#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	if err == nil {
		t.Fatal("expected error when tree fetcher fails")
	}
	if !strings.Contains(err.Error(), "failed to fetch skill directory") {
		t.Fatalf("error should mention fetch failure: %v", err)
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

func TestHandleFetch_AuditLogWriteFailure(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fetch.ComputeTreeHash(files)

	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		TreeFetcher:   fakeTreeFetcher(files),
		FetchPolicy:   fetch.DefaultPolicy,
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		AuditLogPath:  "/no-such-dir/audit.jsonl",
		Uploader:      &stubUploader{},
		SkillDestDir:  "/sandbox/skills",
	})

	resp, err := svc.HandleFetch(context.Background(), FetchRequest{
		URL: "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash,
	})

	// Fetch succeeds even when audit log write fails (best-effort logging).
	if err != nil {
		t.Fatalf("expected success despite audit log failure: %v", err)
	}
	if resp.LocalPath == "" {
		t.Fatal("expected non-empty LocalPath")
	}
}

// threadSafeUploader is a concurrency-safe variant of stubUploader.
type threadSafeUploader struct {
	mu    sync.Mutex
	calls []uploadCall
	count atomic.Int32
}

func (u *threadSafeUploader) UploadSkillDir(sandboxName, localPath, remotePath string) error {
	u.count.Add(1)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, uploadCall{sandboxName, localPath, remotePath})
	return nil
}

func TestHandleFetch_DuplicateSkipsUpload(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()

	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/test-skill", files)

	uploader := &stubUploader{}
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		Uploader:      uploader,
		SandboxName:   "sandbox-1",
		SkillDestDir:  "/sandbox/skills",
	})

	url := "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash

	// First fetch — should upload.
	resp1, err := svc.HandleFetch(context.Background(), FetchRequest{URL: url})
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("after first fetch: expected 1 upload, got %d", len(uploader.calls))
	}

	// Second fetch (same URL) — should skip upload.
	resp2, err := svc.HandleFetch(context.Background(), FetchRequest{URL: url})
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("after second fetch: expected still 1 upload, got %d", len(uploader.calls))
	}
	if resp2.LocalPath != resp1.LocalPath {
		t.Fatalf("LocalPath mismatch: %q vs %q", resp2.LocalPath, resp1.LocalPath)
	}

	// Third fetch — still only 1 upload.
	_, err = svc.HandleFetch(context.Background(), FetchRequest{URL: url})
	if err != nil {
		t.Fatalf("third fetch: %v", err)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("after third fetch: expected still 1 upload, got %d", len(uploader.calls))
	}
}

func TestHandleFetch_ConcurrentSameURLUploadsOnce(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()

	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/test-skill", files)

	uploader := &threadSafeUploader{}
	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    20,
		Uploader:      uploader,
		SandboxName:   "sandbox-1",
		SkillDestDir:  "/sandbox/skills",
	})

	url := "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash
	const goroutines = 10

	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = svc.HandleFetch(context.Background(), FetchRequest{URL: url})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	uploadCount := int(uploader.count.Load())
	if uploadCount != 1 {
		t.Fatalf("expected exactly 1 upload across %d concurrent fetches, got %d", goroutines, uploadCount)
	}
}

func TestHandleFetch_UploadFailureAllowsRetry(t *testing.T) {
	tmpDir := t.TempDir()
	files := fakeSkillFiles()
	hash := fakeSkillHash()

	fetch.CachePutDir(tmpDir, "https://github.com/org/repo/tree/abc123/skills/test-skill", files)

	callCount := 0
	failingUploader := &callbackUploader{
		fn: func(_, _, _ string) error {
			callCount++
			if callCount == 1 {
				return fmt.Errorf("transient upload failure")
			}
			return nil
		},
	}

	svc := New(ServiceConfig{
		Harness:       testHarness("https://github.com/org/repo/"),
		WorkspaceRoot: tmpDir,
		MaxFetches:    10,
		Uploader:      failingUploader,
		SandboxName:   "sandbox-1",
		SkillDestDir:  "/sandbox/skills",
	})

	url := "https://github.com/org/repo/tree/abc123/skills/test-skill#sha256=" + hash

	// First attempt — upload fails.
	_, err := svc.HandleFetch(context.Background(), FetchRequest{URL: url})
	if err == nil {
		t.Fatal("expected error on first attempt")
	}

	// Second attempt — upload succeeds; path should not be stuck as
	// "already uploaded" from the failed first attempt.
	resp, err := svc.HandleFetch(context.Background(), FetchRequest{URL: url})
	if err != nil {
		t.Fatalf("second attempt should succeed: %v", err)
	}
	if resp.LocalPath == "" {
		t.Fatal("expected non-empty LocalPath")
	}
	if callCount != 2 {
		t.Fatalf("expected 2 upload attempts, got %d", callCount)
	}
}

// callbackUploader delegates UploadSkillDir to a user-supplied function.
type callbackUploader struct {
	fn func(sandboxName, localPath, remotePath string) error
}

func (u *callbackUploader) UploadSkillDir(sandboxName, localPath, remotePath string) error {
	return u.fn(sandboxName, localPath, remotePath)
}
