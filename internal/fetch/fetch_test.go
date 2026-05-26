package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/netutil"
)

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

	// The httptest server listens on 127.0.0.1 which is loopback, so we
	// must skip the internal-IP check for integration tests.
	policy := FetchPolicy{
		AllowedDomains: []string{hostname},
		AllowedPorts:   []string{port},
		MaxSizeBytes:   1024,
		Timeout:        5 * time.Second,
		tlsConfig:      srv.TLS.Clone(),
		skipIPCheck:    true,
	}
	// Skip TLS verification — httptest servers use self-signed certificates.
	policy.tlsConfig.InsecureSkipVerify = true

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
