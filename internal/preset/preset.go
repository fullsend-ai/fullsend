// Package preset loads and installs layered-configuration presets
// independently of any forge. Callers fetch a local file or HTTPS URL,
// optionally validate a SHA-256 digest, and install the bytes unchanged
// as .fullsend/config.base.yaml while leaving .fullsend/config.yaml as
// the repository-local overlay (ADR 0069).
package preset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/netutil"
)

// fetchRetryDelay is the pause before retrying a failed HTTPS fetch. A
// variable so tests can shorten it.
var fetchRetryDelay = 500 * time.Millisecond

const (
	fetchTimeout = 30 * time.Second
	// fetchAttempts bounds HTTPS fetch attempts: one retry after a
	// transport failure.
	fetchAttempts = 2
	maxSize       = 1 << 20 // 1 MiB

	// BasePath is the repository-relative path of the vendor/base layer.
	BasePath = ".fullsend/config.base.yaml"
	// OverlayPath is the repository-relative path of the repo-local overlay.
	OverlayPath = ".fullsend/config.yaml"
)

// Plan is the forge-independent result of installing a preset as the
// base layer. Overlay is never rewritten by preset installation.
type Plan struct {
	// Base is the exact preset bytes to write to BasePath.
	Base []byte
	// Overlay is the existing overlay, copied unchanged. Nil if none existed.
	Overlay []byte
	// BaseChanged is true when Base differs from the previous base
	// (including when no previous base existed).
	BaseChanged bool
}

// Fetch reads a preset configuration from a local file path or an
// HTTPS URL. Returns the raw content bytes.
func Fetch(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(strings.ToLower(source), "https://") {
		return fetchHTTPS(ctx, source, false)
	}
	if strings.Contains(source, "://") {
		return nil, fmt.Errorf("unsupported URL scheme in preset source %q: only local paths and https:// URLs are supported", source)
	}
	return fetchLocal(source)
}

// Load fetches a preset, optionally validates a SHA-256 hex digest, and
// checks that the content is syntactically valid YAML. hash may be empty
// to skip digest verification. Layer-specific semantic validation is
// left to the caller.
func Load(ctx context.Context, source, hash string) ([]byte, error) {
	data, err := Fetch(ctx, source)
	if err != nil {
		return nil, err
	}
	if hash != "" {
		if err := ValidateHash(data, hash); err != nil {
			return nil, err
		}
	}
	if err := ValidateYAML(data); err != nil {
		return nil, err
	}
	return data, nil
}

// Apply returns the layered-config files after installing preset as the
// base layer. Overlay is preserved byte-for-byte. Base is replaced
// wholesale with a copy of preset; it is never merged with the overlay
// or with the previous base. Callers that commit via a forge API write
// Plan.Base to BasePath when Plan.BaseChanged (or always, for
// compatibility with github setup --config).
func Apply(preset, existingBase, existingOverlay []byte) Plan {
	base := bytes.Clone(preset)
	return Plan{
		Base:        base,
		Overlay:     bytes.Clone(existingOverlay),
		BaseChanged: !bytes.Equal(existingBase, base),
	}
}

func fetchLocal(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading preset file %q: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("preset file %q is empty", path)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("preset file %q exceeds maximum size (%d bytes)", path, maxSize)
	}
	return data, nil
}

// safeDialContext wraps a net.Dialer to reject connections to
// internal/reserved IP addresses (loopback, link-local, private, etc.).
// It resolves the target host, validates every resolved address, and
// dials only the addresses that pass. Mirrors
// internal/repos.SafeDialContext, applied here to preset fetches.
func safeDialContext(d *net.Dialer, skipIPCheck bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, policyError{fmt.Errorf("invalid address %q: %w", addr, err)}
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses found for %q", host)
		}
		var safeIPs []net.IPAddr
		for _, ip := range ips {
			if skipIPCheck {
				safeIPs = append(safeIPs, ip)
			} else if reason := netutil.CheckIP(ip.IP); reason != "" {
				continue
			} else {
				safeIPs = append(safeIPs, ip)
			}
		}
		if len(safeIPs) == 0 {
			return nil, policyError{fmt.Errorf("all resolved addresses for %q are blocked", host)}
		}
		var lastErr error
		for _, ip := range safeIPs {
			conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// fetchHTTPS retrieves preset YAML from an HTTPS URL with timeout, size
// limit, and SSRF protections matching internal/repos.fetchManifestURL:
// HTTP(S)_PROXY environment variables are ignored, DNS is resolved and
// the resolved IPs are validated (rejecting loopback/link-local/private/
// metadata addresses) before every dial — both the initial connection
// and any HTTPS redirect hop share the same dialer, so redirects and
// DNS-rebinding are covered too — and URLs carrying userinfo are
// rejected. skipIPCheck bypasses the resolved-IP validation for tests
// using httptest servers on loopback; production callers must always
// go through Fetch, which passes skipIPCheck=false.
func fetchHTTPS(ctx context.Context, rawURL string, skipIPCheck bool) ([]byte, error) {
	return fetchHTTPSWithTLSConfig(ctx, rawURL, skipIPCheck, nil)
}

// fetchHTTPSWithTLSConfig is split out so tests can provide the trust roots
// of an httptest TLS server without mutating http.DefaultTransport globally.
// Production callers must use fetchHTTPS.
func fetchHTTPSWithTLSConfig(ctx context.Context, rawURL string, skipIPCheck bool, tlsConfig *tls.Config) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetching preset from %q: %w", rawURL, err)
	}
	if u.User != nil {
		return nil, fmt.Errorf("preset URL must not contain userinfo, got %q", u.Redacted())
	}

	transport := &http.Transport{
		Proxy: nil, // ignore HTTP(S)_PROXY env vars: a proxy could redirect
		// the request to an internal host regardless of DNS/IP validation.
		DialContext: safeDialContext(&net.Dialer{
			Timeout: 10 * time.Second,
		}, skipIPCheck),
	}
	// Preserve TLS trust settings (e.g. a test CA pool) from the default
	// transport; only dialing and proxying behavior are hardened here.
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig.Clone()
	} else if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport.TLSClientConfig = base.TLSClientConfig.Clone()
	}

	// The transport is private to this fetch; release its connections.
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Timeout:   fetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return policyError{fmt.Errorf("preset redirect to non-HTTPS URL %q is not allowed", req.URL.Redacted())}
			}
			if req.URL.User != nil {
				return policyError{fmt.Errorf("preset redirect URL must not contain userinfo, got %q", req.URL.Redacted())}
			}
			if len(via) >= 10 {
				return policyError{fmt.Errorf("too many redirects")}
			}
			return nil
		},
	}

	// The GET is idempotent, so a transport failure (a dropped or
	// half-open connection surfacing as a bare EOF, a reset, a timeout
	// before the response) is retried once. HTTP status, size and policy
	// failures are deterministic and returned immediately, as is any
	// failure once ctx is done.
	var attemptErrs []error
	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		if attempt > 1 {
			timer := time.NewTimer(fetchRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, joinAttemptErrors(attemptErrs, context.Cause(ctx))
			case <-timer.C:
			}
		}
		data, retryable, err := fetchHTTPSAttempt(ctx, client, rawURL)
		if err == nil {
			return data, nil
		}
		attemptErrs = append(attemptErrs, err)
		if !retryable {
			break
		}
	}
	return nil, joinAttemptErrors(attemptErrs, nil)
}

// joinAttemptErrors reports every failed attempt so the cause of the first
// failure is not hidden by the retry. abandoned, when set, is why the
// retry was not made.
func joinAttemptErrors(errs []error, abandoned error) error {
	format := "%w"
	args := []any{errs[0]}
	for _, err := range errs[1:] {
		format += "; retried once: %w"
		args = append(args, err)
	}
	if abandoned != nil {
		format += "; retry abandoned: %w"
		args = append(args, abandoned)
	}
	return fmt.Errorf(format, args...)
}

// fetchHTTPSAttempt performs one GET. retryable reports whether the
// failure is a transport-level one worth a single retry.
func fetchHTTPSAttempt(ctx context.Context, client *http.Client, rawURL string) (data []byte, retryable bool, err error) {
	trace := newFetchTrace()
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace.clientTrace()), http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("fetching preset from %q: %w", rawURL, err)
	}
	resp, err := client.Do(req) //nolint:gosec // URL is user-provided via --config / repos.yaml; SSRF-hardened via safeDialContext
	if err != nil {
		var policy policyError
		if errors.As(err, &policy) {
			return nil, false, fmt.Errorf("fetching preset from %q: %w", rawURL, err)
		}
		return nil, ctx.Err() == nil, fmt.Errorf("fetching preset from %q (%s): %w", rawURL, trace.describe(ctx), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetching preset from %q: HTTP %d", rawURL, resp.StatusCode)
	}

	data, err = io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, ctx.Err() == nil, fmt.Errorf("reading preset from %q (%s): %w", rawURL, trace.describe(ctx), err)
	}
	if len(data) > maxSize {
		return nil, false, fmt.Errorf("preset from %q exceeds maximum size (%d bytes)", rawURL, maxSize)
	}
	if len(data) == 0 {
		return nil, false, fmt.Errorf("preset from %q is empty", rawURL)
	}
	return data, false, nil
}

// policyError marks a fetch failure caused by the SSRF or redirect policy
// rather than the network. Such failures are never retried.
type policyError struct{ err error }

func (e policyError) Error() string { return e.err.Error() }
func (e policyError) Unwrap() error { return e.err }

// fetchTrace records how far one HTTP attempt progressed, so a transport
// error that net/http reports only as "EOF" or "connection reset" names
// the phase it happened in (DNS, connect, TLS handshake, waiting for the
// response, reading the body), the remote address and the elapsed time.
// Trace hooks may run on transport goroutines, hence the mutex.
type fetchTrace struct {
	mu     sync.Mutex
	start  time.Time
	phase  string
	remote string
}

func newFetchTrace() *fetchTrace {
	return &fetchTrace{start: time.Now(), phase: "starting the request"}
}

func (t *fetchTrace) set(phase, remote string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase = phase
	if remote != "" {
		t.remote = remote
	}
}

func (t *fetchTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { t.set("resolving DNS", "") },
		ConnectStart:      func(_, addr string) { t.set("connecting", addr) },
		TLSHandshakeStart: func() { t.set("TLS handshake", "") },
		GotConn: func(info httptrace.GotConnInfo) {
			remote := ""
			if info.Conn != nil {
				remote = info.Conn.RemoteAddr().String()
			}
			t.set("sending the request", remote)
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { t.set("awaiting response headers", "") },
		GotFirstResponseByte: func() { t.set("reading the response", "") },
	}
}

// describe summarizes the attempt's progress for an error message. When
// ctx is done its cause is included, since a connection torn down at a
// deadline can surface from net/http as a bare EOF.
func (t *fetchTrace) describe(ctx context.Context) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	msg := "failed while " + t.phase
	if t.remote != "" {
		msg += " with " + t.remote
	}
	msg += " after " + time.Since(t.start).Round(time.Millisecond).String()
	if ctx.Err() != nil {
		msg += "; context: " + context.Cause(ctx).Error()
	}
	return msg
}

// IsRemote reports whether source is a URL rather than a local path,
// matching the schemes Fetch itself treats as remote (https:// only).
// A bare scheme check via url.Parse would misclassify strings such as a
// Windows-style path ("C:\presets\org.yaml", scheme "C") as remote even
// though Fetch treats them as local paths.
func IsRemote(source string) bool {
	return strings.HasPrefix(strings.ToLower(source), "https://")
}

// ValidateYAML checks that data is syntactically valid YAML.
func ValidateYAML(data []byte) error {
	var probe any
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("preset is not valid YAML: %w", err)
	}
	return nil
}

// ValidateHash checks that the SHA-256 hash of data matches the
// expected hex-encoded hash string. Returns nil on match.
func ValidateHash(data []byte, expectedHex string) error {
	expectedHex = strings.ToLower(strings.TrimSpace(expectedHex))
	if len(expectedHex) != 64 {
		return fmt.Errorf("preset hash must be a 64-character hex-encoded SHA-256 hash, got %d characters", len(expectedHex))
	}
	if _, err := hex.DecodeString(expectedHex); err != nil {
		return fmt.Errorf("preset hash is not valid hex: %w", err)
	}

	actual := sha256.Sum256(data)
	actualHex := hex.EncodeToString(actual[:])

	if actualHex != expectedHex {
		return fmt.Errorf("preset hash mismatch: expected %s, got %s", expectedHex, actualHex)
	}
	return nil
}
