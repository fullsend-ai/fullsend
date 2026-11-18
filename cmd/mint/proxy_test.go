package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFallbackHandler_LocalRole(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"local-token"}`))
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "http://upstream.example.com")

	body := `{"role":"triage","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !localCalled {
		t.Fatal("expected local handler to be called for local role")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestFallbackHandler_ProxiedRole(t *testing.T) {
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("local handler should not be called for proxied role")
	})

	var receivedAuth string
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"upstream-token","expires_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer upstream.Close()

	h := newFallbackHandler(local, map[string]bool{"triage": true}, upstream.URL)

	body := `{"role":"coder","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer my-oidc-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if receivedAuth != "Bearer my-oidc-jwt" {
		t.Fatalf("expected Authorization header forwarded, got %q", receivedAuth)
	}

	var parsed struct {
		Role string `json:"role"`
	}
	json.Unmarshal([]byte(receivedBody), &parsed)
	if parsed.Role != "coder" {
		t.Fatalf("expected role 'coder' forwarded, got %q", parsed.Role)
	}

	var resp struct {
		Token string `json:"token"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "upstream-token" {
		t.Fatalf("expected upstream token, got %q", resp.Token)
	}
}

func TestFallbackHandler_UpstreamError(t *testing.T) {
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("local handler should not be called")
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"role not allowed"}`))
	}))
	defer upstream.Close()

	h := newFallbackHandler(local, map[string]bool{"triage": true}, upstream.URL)

	body := `{"role":"coder","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from upstream, got %d", rec.Code)
	}
}

func TestFallbackHandler_HealthAlwaysLocal(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "http://upstream.example.com")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !localCalled {
		t.Fatal("expected local handler for /health")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestFallbackHandler_InvalidJSON(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusBadRequest)
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "http://upstream.example.com")

	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !localCalled {
		t.Fatal("expected local handler for invalid JSON body")
	}
}

func TestFallbackHandler_EmptyRole(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusBadRequest)
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "http://upstream.example.com")

	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"repos":["foo"]}`))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !localCalled {
		t.Fatal("expected local handler for empty role")
	}
}

func TestFallbackHandler_UpstreamUnreachable(t *testing.T) {
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("local handler should not be called")
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "http://127.0.0.1:1")
	h.httpClient = &http.Client{Timeout: 100 * time.Millisecond}

	body := `{"role":"coder","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json Content-Type, got %q", ct)
	}
}

func TestFallbackHandler_CaseInsensitiveRole(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"local-token"}`))
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "http://upstream.example.com")

	body := `{"role":"Triage","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !localCalled {
		t.Fatal("expected local handler for case-insensitive role match")
	}
}

func TestFallbackHandler_UpstreamResponseReadError(t *testing.T) {
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("local handler should not be called")
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"partial`))
		// Close connection before full body, causing read error
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	h := newFallbackHandler(local, map[string]bool{"triage": true}, upstream.URL)

	body := `{"role":"coder","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for read error, got %d", rec.Code)
	}
}

func TestFallbackHandler_NonPostToken(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "http://upstream.example.com")

	req := httptest.NewRequest(http.MethodGet, "/v1/token", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !localCalled {
		t.Fatal("expected local handler for GET /v1/token")
	}
}

func TestFallbackHandler_InvalidFallbackURL(t *testing.T) {
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("local handler should not be called")
	})

	h := newFallbackHandler(local, map[string]bool{"triage": true}, "://invalid\x7f")

	body := `{"role":"coder","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestFallbackHandler_NoFallbackConfigured(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"role not allowed"}`))
	})

	// No fallback — localRoles has "triage", requesting "coder" goes to local
	// handler which returns its normal error
	h := newFallbackHandler(local, map[string]bool{"triage": true}, "")

	body := `{"role":"coder","repos":["my-repo"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !localCalled {
		t.Fatal("expected local handler when no fallback configured")
	}
}

// tracedFallbackChain builds a fallback handler wrapped by a tracer that
// exports to a fake collector, mirroring buildHandler's chain. Exports are
// asynchronous, so callers must call the returned drain function before
// asserting on the collector.
func tracedFallbackChain(t *testing.T, fallbackURL string, client *http.Client) (http.Handler, *int32, *[]string, func()) {
	t.Helper()
	var exports int32
	var mu sync.Mutex
	var bodies []string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		atomic.AddInt32(&exports, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)

	clearTracingEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	tracer, err := buildTracer()
	if err != nil || tracer == nil {
		t.Fatalf("buildTracer: %v, %v", tracer, err)
	}
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("local handler should not be called for proxied role")
	})
	h := newFallbackHandler(local, map[string]bool{"triage": true}, fallbackURL)
	if client != nil {
		h.httpClient = client
	}
	drain := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracer.Shutdown(ctx); err != nil {
			t.Fatalf("draining trace exports: %v", err)
		}
	}
	return tracer.Wrap(h), &exports, &bodies, drain
}

func TestFallbackHandler_TracesProxiedRequestAndForwardsTraceContext(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	var gotParent, gotState string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotParent = r.Header.Get("traceparent")
		gotState = r.Header.Get("tracestate")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"t"}`))
	}))
	defer upstream.Close()

	chain, exports, _, drain := tracedFallbackChain(t, upstream.URL, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder"}`))
	req.Header.Set("Authorization", "Bearer test-jwt")
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	req.Header.Set("tracestate", "vendor=abc")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	drain()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if atomic.LoadInt32(exports) != 1 {
		t.Fatalf("expected the proxied request to be exported once, got %d", atomic.LoadInt32(exports))
	}
	tr := rec.Header().Get("traceresponse")
	if tr == "" {
		t.Fatal("expected traceresponse on a fallback-routed request")
	}
	if gotParent != tr {
		t.Fatalf("upstream traceparent = %q, want the mint server span %q", gotParent, tr)
	}
	if !strings.HasPrefix(gotParent, "00-"+traceID+"-") {
		t.Fatalf("upstream left the caller trace: %q", gotParent)
	}
	if gotState != "vendor=abc" {
		t.Fatalf("upstream tracestate = %q", gotState)
	}
}

func TestFallbackHandler_TracesUpstreamFailure(t *testing.T) {
	chain, exports, bodies, drain := tracedFallbackChain(t, "http://127.0.0.1:1", &http.Client{Timeout: 100 * time.Millisecond})
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder"}`))
	req.Header.Set("Authorization", "Bearer test-jwt")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	drain()

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if rec.Header().Get("traceresponse") == "" {
		t.Fatal("expected traceresponse on a failed fallback request")
	}
	if atomic.LoadInt32(exports) != 1 {
		t.Fatalf("expected the failed request to be exported once, got %d", atomic.LoadInt32(exports))
	}
	if !strings.Contains((*bodies)[0], "fallback_proxy") {
		t.Fatalf("exported span missing the fallback_proxy failure stage: %s", (*bodies)[0])
	}
}
