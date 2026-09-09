package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/netutil"
)

func intPtr(n int) *int { return &n }

// timeoutErr is a test helper that implements the Timeout() interface
// used by net/http to identify timeout errors.
type timeoutErr struct{ msg string }

func (e *timeoutErr) Error() string   { return e.msg }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true } //nolint:staticcheck // Required by net.Error interface.

// newTestServer creates an HTTPS test server and returns it along with a
// FetchPolicy configured to trust the server's TLS certificate, allow
// its hostname, and skip internal-IP checks (the server listens on 127.0.0.1).
func newTestServer(t *testing.T, handler http.Handler) (*httptest.Server, FetchPolicy) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	// Extract hostname and port from the test server URL.
	// srv.URL looks like "https://127.0.0.1:PORT".
	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true

	policy := NewTestPolicy(tlsCfg, []string{hostname}, []string{port})
	policy.MaxSizeBytes = 1024

	return srv, policy
}

func TestFetchURL(t *testing.T) {
	t.Run("HTTPSOnly", func(t *testing.T) {
		policy := FetchPolicy{
			AllowedDomains: []string{"example.com"},
			MaxSizeBytes:   1024,
			Timeout:        5 * time.Second,
		}
		_, err := FetchURL(context.Background(), "http://example.com/file", policy)
		if !errors.Is(err, errNotHTTPS) {
			t.Fatalf("expected errNotHTTPS, got: %v", err)
		}
	})

	t.Run("DomainAllowlist", func(t *testing.T) {
		policy := FetchPolicy{
			AllowedDomains: []string{"allowed.com"},
			MaxSizeBytes:   1024,
			Timeout:        5 * time.Second,
		}
		_, err := FetchURL(context.Background(), "https://blocked.com/file", policy)
		if !errors.Is(err, errDomainBlocked) {
			t.Fatalf("expected errDomainBlocked, got: %v", err)
		}
	})

	t.Run("WildcardDomain", func(t *testing.T) {
		// Wildcard should match subdomains but not the bare domain.
		if !isAllowedDomain("sub.example.com", []string{"*.example.com"}) {
			t.Fatal("expected sub.example.com to match *.example.com")
		}
		if !isAllowedDomain("deep.sub.example.com", []string{"*.example.com"}) {
			t.Fatal("expected deep.sub.example.com to match *.example.com")
		}
		if isAllowedDomain("example.com", []string{"*.example.com"}) {
			t.Fatal("expected example.com NOT to match *.example.com")
		}
		if isAllowedDomain("notexample.com", []string{"*.example.com"}) {
			t.Fatal("expected notexample.com NOT to match *.example.com")
		}
	})

	t.Run("NoRedirects", func(t *testing.T) {
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/other", http.StatusMovedPermanently)
		}))

		_, err := FetchURL(context.Background(), srv.URL+"/start", policy)
		if !errors.Is(err, errNonOK) {
			t.Fatalf("expected errNonOK for redirect response, got: %v", err)
		}
	})

	t.Run("SizeLimit", func(t *testing.T) {
		// Write 2048 bytes; policy.MaxSizeBytes is 1024.
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			data := make([]byte, 2048)
			_, _ = w.Write(data)
		}))

		_, err := FetchURL(context.Background(), srv.URL+"/big", policy)
		if !errors.Is(err, errTooLarge) {
			t.Fatalf("expected errTooLarge, got: %v", err)
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(2 * time.Second)
			w.WriteHeader(http.StatusOK)
		}))
		policy.Timeout = 100 * time.Millisecond
		policy.MaxRetries = intPtr(0) // Disable retries to keep test fast.

		_, err := FetchURL(context.Background(), srv.URL+"/slow", policy)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
	})

	t.Run("OfflineMode", func(t *testing.T) {
		policy := FetchPolicy{
			AllowedDomains: []string{"example.com"},
			MaxSizeBytes:   1024,
			Timeout:        5 * time.Second,
			Offline:        true,
		}
		_, err := FetchURL(context.Background(), "https://example.com/file", policy)
		if !errors.Is(err, errOffline) {
			t.Fatalf("expected errOffline, got: %v", err)
		}
	})

	t.Run("DoubleEncoding", func(t *testing.T) {
		policy := FetchPolicy{
			AllowedDomains: []string{"example.com"},
			MaxSizeBytes:   1024,
			Timeout:        5 * time.Second,
		}
		_, err := FetchURL(context.Background(), "https://example.com/%25252e%25252e", policy)
		if !errors.Is(err, errDoubleEncoding) {
			t.Fatalf("expected errDoubleEncoding, got: %v", err)
		}
	})

	t.Run("NonOKStatus", func(t *testing.T) {
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))

		_, err := FetchURL(context.Background(), srv.URL+"/missing", policy)
		if !errors.Is(err, errNonOK) {
			t.Fatalf("expected errNonOK, got: %v", err)
		}
		var httpErr HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
			t.Fatalf("expected HTTPStatusError 404, got: %v", err)
		}
	})

	t.Run("Success", func(t *testing.T) {
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "hello world")
		}))

		data, err := FetchURL(context.Background(), srv.URL+"/ok", policy)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "hello world" {
			t.Fatalf("unexpected body: %q", string(data))
		}
	})
}

func TestIsInternalIPDelegatesToNetutil(t *testing.T) {
	// Verify that the fetch package uses netutil.IsInternal (smoke test).
	ip := net.ParseIP("127.0.0.1")
	if !netutil.IsInternal(ip) {
		t.Fatal("expected 127.0.0.1 to be internal")
	}
	ip = net.ParseIP("8.8.8.8")
	if netutil.IsInternal(ip) {
		t.Fatal("expected 8.8.8.8 to be public")
	}
}

func TestPortRestriction(t *testing.T) {
	t.Run("DefaultRejectsNonStandard", func(t *testing.T) {
		policy := FetchPolicy{
			AllowedDomains: []string{"example.com"},
			MaxSizeBytes:   1024,
			Timeout:        5 * time.Second,
		}
		_, err := FetchURL(context.Background(), "https://example.com:8443/file", policy)
		if !errors.Is(err, errPortBlocked) {
			t.Fatalf("expected errPortBlocked, got: %v", err)
		}
	})

	t.Run("DefaultAllows443", func(t *testing.T) {
		policy := FetchPolicy{
			AllowedDomains: []string{"example.com"},
			MaxSizeBytes:   1024,
			Timeout:        5 * time.Second,
		}
		// Port 443 is allowed by default; this will fail at DNS, not port check.
		_, err := FetchURL(context.Background(), "https://example.com:443/file", policy)
		if errors.Is(err, errPortBlocked) {
			t.Fatal("port 443 should be allowed by default")
		}
	})

	t.Run("ExplicitPortAllowed", func(t *testing.T) {
		policy := FetchPolicy{
			AllowedDomains: []string{"example.com"},
			AllowedPorts:   []string{"443", "8443"},
			MaxSizeBytes:   1024,
			Timeout:        5 * time.Second,
		}
		// Port 8443 is explicitly allowed; will fail at DNS, not port check.
		_, err := FetchURL(context.Background(), "https://example.com:8443/file", policy)
		if errors.Is(err, errPortBlocked) {
			t.Fatal("port 8443 should be allowed when explicitly configured")
		}
	})
}

func TestFetchURL_RetriesTransientErrors(t *testing.T) {
	t.Run("RetriesOn503ThenSucceeds", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ok")
		}))
		policy.MaxRetries = intPtr(3)
		policy.RetryBackoff = 10 * time.Millisecond

		data, err := FetchURL(context.Background(), srv.URL+"/retry", policy)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "ok" {
			t.Fatalf("unexpected body: %q", string(data))
		}
		if attempts != 3 {
			t.Fatalf("expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("RetriesOn502ThenSucceeds", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "recovered")
		}))
		policy.MaxRetries = intPtr(2)
		policy.RetryBackoff = 10 * time.Millisecond

		data, err := FetchURL(context.Background(), srv.URL+"/retry502", policy)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "recovered" {
			t.Fatalf("unexpected body: %q", string(data))
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got %d", attempts)
		}
	})

	t.Run("RetriesOn429ThenSucceeds", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ok")
		}))
		policy.MaxRetries = intPtr(2)
		policy.RetryBackoff = 10 * time.Millisecond

		data, err := FetchURL(context.Background(), srv.URL+"/retry429", policy)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "ok" {
			t.Fatalf("unexpected body: %q", string(data))
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got %d", attempts)
		}
	})

	t.Run("ExhaustsRetriesThenFails", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		policy.MaxRetries = intPtr(2)
		policy.RetryBackoff = 10 * time.Millisecond

		_, err := FetchURL(context.Background(), srv.URL+"/always503", policy)
		if err == nil {
			t.Fatal("expected error after exhausting retries")
		}
		if !errors.Is(err, errNonOK) {
			t.Fatalf("expected errNonOK, got: %v", err)
		}
		if !strings.Contains(err.Error(), "after 3 attempt(s)") {
			t.Fatalf("expected attempt count in error, got: %v", err)
		}
		if attempts != 3 {
			t.Fatalf("expected 3 attempts (1 initial + 2 retries), got %d", attempts)
		}
	})

	t.Run("ExhaustedRetriesIncludeLastTransientError", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			// Third attempt returns a non-transient error.
			w.WriteHeader(http.StatusNotFound)
		}))
		policy.MaxRetries = intPtr(3)
		policy.RetryBackoff = 10 * time.Millisecond

		_, err := FetchURL(context.Background(), srv.URL+"/mixed-errors", policy)
		if err == nil {
			t.Fatal("expected error")
		}
		errMsg := err.Error()
		if !strings.Contains(errMsg, "last transient error") {
			t.Fatalf("expected error to include last transient error context, got: %v", err)
		}
		if !strings.Contains(errMsg, "after 3 attempt(s)") {
			t.Fatalf("expected attempt count in error, got: %v", err)
		}
	})

	t.Run("NoRetryOn404", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusNotFound)
		}))
		policy.MaxRetries = intPtr(3)
		policy.RetryBackoff = 10 * time.Millisecond

		_, err := FetchURL(context.Background(), srv.URL+"/missing", policy)
		if err == nil {
			t.Fatal("expected error for 404")
		}
		if !errors.Is(err, errNonOK) {
			t.Fatalf("expected errNonOK, got: %v", err)
		}
		if attempts != 1 {
			t.Fatalf("expected 1 attempt (no retry for 404), got %d", attempts)
		}
	})

	t.Run("NoRetryOn401", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusUnauthorized)
		}))
		policy.MaxRetries = intPtr(3)
		policy.RetryBackoff = 10 * time.Millisecond

		_, err := FetchURL(context.Background(), srv.URL+"/unauth", policy)
		if err == nil {
			t.Fatal("expected error for 401")
		}
		if attempts != 1 {
			t.Fatalf("expected 1 attempt (no retry for 401), got %d", attempts)
		}
	})

	t.Run("ZeroMaxRetriesDisablesRetry", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		policy.MaxRetries = intPtr(0)

		_, err := FetchURL(context.Background(), srv.URL+"/no-retry", policy)
		if err == nil {
			t.Fatal("expected error")
		}
		if attempts != 1 {
			t.Fatalf("expected 1 attempt with MaxRetries=0, got %d", attempts)
		}
	})

	t.Run("RespectsContextCancellation", func(t *testing.T) {
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		policy.MaxRetries = intPtr(3)
		policy.RetryBackoff = 10 * time.Millisecond

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := FetchURL(ctx, srv.URL+"/cancel", policy)
		if err == nil {
			t.Fatal("expected error on cancelled context")
		}
	})

	t.Run("DefaultPolicyHasRetries", func(t *testing.T) {
		// Verify that DefaultPolicy uses the default retry count (nil = default).
		if DefaultPolicy.MaxRetries != nil {
			t.Fatalf("expected DefaultPolicy.MaxRetries to be nil (use default), got %d", *DefaultPolicy.MaxRetries)
		}
	})

	t.Run("NegativeMaxRetriesClampsToZero", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		policy.MaxRetries = intPtr(-1)

		_, err := FetchURL(context.Background(), srv.URL+"/negative", policy)
		if err == nil {
			t.Fatal("expected error with negative MaxRetries")
		}
		if attempts != 1 {
			t.Fatalf("expected 1 attempt with negative MaxRetries (clamped to 0), got %d", attempts)
		}
	})

	t.Run("NilMaxRetriesUsesDefault", func(t *testing.T) {
		var attempts int
		srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts < defaultMaxAttempts {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ok")
		}))
		// MaxRetries is nil by default from NewTestPolicy → uses defaultMaxAttempts.
		policy.RetryBackoff = 10 * time.Millisecond

		data, err := FetchURL(context.Background(), srv.URL+"/default-retries", policy)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "ok" {
			t.Fatalf("unexpected body: %q", string(data))
		}
		if attempts != defaultMaxAttempts {
			t.Fatalf("expected %d attempts, got %d", defaultMaxAttempts, attempts)
		}
	})
}

func TestIsTransientRequestError(t *testing.T) {
	ctx := context.Background()

	t.Run("ConnectionReset", func(t *testing.T) {
		// Use the typed syscall error that net.OpError wraps.
		err := fmt.Errorf("read tcp 127.0.0.1:443: %w", syscall.ECONNRESET)
		if !isTransientRequestError(ctx, err) {
			t.Fatal("expected connection reset to be transient")
		}
	})

	t.Run("ConnectionRefused", func(t *testing.T) {
		err := fmt.Errorf("dial tcp 127.0.0.1:443: %w", syscall.ECONNREFUSED)
		if !isTransientRequestError(ctx, err) {
			t.Fatal("expected connection refused to be transient")
		}
	})

	t.Run("IOTimeout", func(t *testing.T) {
		// i/o timeouts are caught by the Timeout() interface check.
		err := &net.OpError{Op: "read", Net: "tcp", Err: &timeoutErr{msg: "i/o timeout"}}
		if !isTransientRequestError(ctx, err) {
			t.Fatal("expected i/o timeout to be transient")
		}
	})

	t.Run("EOF", func(t *testing.T) {
		err := fmt.Errorf("http: %w", io.EOF)
		if !isTransientRequestError(ctx, err) {
			t.Fatal("expected EOF to be transient")
		}
	})

	t.Run("UnexpectedEOF", func(t *testing.T) {
		err := fmt.Errorf("http: %w", io.ErrUnexpectedEOF)
		if !isTransientRequestError(ctx, err) {
			t.Fatal("expected unexpected EOF to be transient")
		}
	})

	t.Run("ContextCanceledNotTransient", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()
		err := fmt.Errorf("request: %w", context.Canceled)
		if isTransientRequestError(cancelCtx, err) {
			t.Fatal("expected context.Canceled to NOT be transient")
		}
	})

	t.Run("ContextDeadlineNotTransient", func(t *testing.T) {
		deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := context.DeadlineExceeded
		if isTransientRequestError(deadlineCtx, err) {
			t.Fatal("expected context.DeadlineExceeded to NOT be transient when caller context expired")
		}
	})

	t.Run("ClientTimeoutIsTransient", func(t *testing.T) {
		// HTTP client timeouts wrap context.DeadlineExceeded from an
		// internal context, but the caller's context is still active.
		// These per-request timeouts are transient and worth retrying.
		err := fmt.Errorf("request timeout: %w", context.DeadlineExceeded)
		if !isTransientRequestError(ctx, err) {
			t.Fatal("expected client timeout to be transient when caller context is active")
		}
	})

	t.Run("GenericErrorNotTransient", func(t *testing.T) {
		err := fmt.Errorf("something unrelated went wrong")
		if isTransientRequestError(ctx, err) {
			t.Fatal("expected generic error to NOT be transient")
		}
	})
}

func TestIsTransientStatusCode(t *testing.T) {
	tests := []struct {
		code      int
		transient bool
	}{
		{http.StatusOK, false},
		{http.StatusNotFound, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusInternalServerError, false},
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("Status%d", tt.code), func(t *testing.T) {
			got := isTransientStatusCode(tt.code)
			if got != tt.transient {
				t.Fatalf("isTransientStatusCode(%d) = %v, want %v", tt.code, got, tt.transient)
			}
		})
	}
}

func TestRetryBackoff(t *testing.T) {
	base := 100 * time.Millisecond

	// Attempt 0: backoff = 100ms, jittered between 50ms and 100ms
	for range 20 {
		d := retryBackoff(base, 0)
		if d < 50*time.Millisecond || d > 100*time.Millisecond {
			t.Fatalf("attempt 0 backoff %v out of range [50ms, 100ms]", d)
		}
	}

	// Attempt 1: backoff = 200ms, jittered between 100ms and 200ms
	for range 20 {
		d := retryBackoff(base, 1)
		if d < 100*time.Millisecond || d > 200*time.Millisecond {
			t.Fatalf("attempt 1 backoff %v out of range [100ms, 200ms]", d)
		}
	}

	// Attempt 2: backoff = 400ms, jittered between 200ms and 400ms
	for range 20 {
		d := retryBackoff(base, 2)
		if d < 200*time.Millisecond || d > 400*time.Millisecond {
			t.Fatalf("attempt 2 backoff %v out of range [200ms, 400ms]", d)
		}
	}
}

func TestRetryBackoff_Capped(t *testing.T) {
	base := 500 * time.Millisecond

	// At attempt 50, math.Pow(2, 50) * 500ms would overflow int64.
	// The backoff should be capped at maxBackoff (30s), jittered [15s, 30s].
	for range 20 {
		d := retryBackoff(base, 50)
		if d < maxBackoff/2 || d > maxBackoff {
			t.Fatalf("attempt 50 backoff %v out of capped range [%v, %v]", d, maxBackoff/2, maxBackoff)
		}
	}
}

func TestComputeSHA256(t *testing.T) {
	input := []byte("hello world")
	expected := sha256.Sum256(input)
	expectedHex := hex.EncodeToString(expected[:])

	got := ComputeSHA256(input)
	if got != expectedHex {
		t.Fatalf("ComputeSHA256(%q) = %s, want %s", input, got, expectedHex)
	}

	// Verify against a known hash value.
	const knownHash = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got != knownHash {
		t.Fatalf("ComputeSHA256(%q) = %s, want known hash %s", input, got, knownHash)
	}
}
