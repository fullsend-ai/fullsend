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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/netutil"
)

const (
	fetchTimeout = 30 * time.Second
	maxSize      = 1 << 20 // 1 MiB

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
// internal/repos.safeDialContext, applied here to preset fetches.
func safeDialContext(d *net.Dialer, skipIPCheck bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
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
			return nil, fmt.Errorf("all resolved addresses for %q are blocked", host)
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

	client := &http.Client{
		Timeout:   fetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("preset redirect to non-HTTPS URL %q is not allowed", req.URL.Redacted())
			}
			if req.URL.User != nil {
				return fmt.Errorf("preset redirect URL must not contain userinfo, got %q", req.URL.Redacted())
			}
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching preset from %q: %w", rawURL, err)
	}
	resp, err := client.Do(req) //nolint:gosec // URL is user-provided via --config / repos.yaml; SSRF-hardened via safeDialContext
	if err != nil {
		return nil, fmt.Errorf("fetching preset from %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching preset from %q: HTTP %d", rawURL, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading preset from %q: %w", rawURL, err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("preset from %q exceeds maximum size (%d bytes)", rawURL, maxSize)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("preset from %q is empty", rawURL)
	}
	return data, nil
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
