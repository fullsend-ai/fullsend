// Package fetch provides an SSRF-hardened HTTP client for fetching external URLs.
//
// It enforces HTTPS-only, domain allowlisting, DNS rebinding protection,
// internal IP rejection, redirect blocking, and response size limits.
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
	"net/url"
	"strings"
	"time"
)

// FetchPolicy controls which URLs may be fetched and under what constraints.
type FetchPolicy struct {
	// AllowedDomains is the list of domains permitted for fetching.
	// Entries may be exact hostnames (e.g. "github.com") or wildcard
	// patterns (e.g. "*.github.com") matching any subdomain.
	AllowedDomains []string

	// MaxSizeBytes is the maximum response body size in bytes.
	MaxSizeBytes int64

	// Timeout is the maximum duration for the entire fetch operation.
	Timeout time.Duration

	// Offline disables all network access. FetchURL returns an error immediately.
	Offline bool
}

// DefaultPolicy is a reasonable default for fetching GitHub-hosted content.
var DefaultPolicy = FetchPolicy{
	AllowedDomains: []string{"github.com", "raw.githubusercontent.com"},
	MaxSizeBytes:   10 * 1024 * 1024, // 10 MB
	Timeout:        30 * time.Second,
}

var (
	// ErrOffline is returned when the policy disables network access.
	ErrOffline = errors.New("fetch: network access disabled (offline mode)")

	// ErrNotHTTPS is returned when the URL scheme is not https.
	ErrNotHTTPS = errors.New("fetch: only https URLs are allowed")

	// ErrDomainNotAllowed is returned when the URL host is not in the allowlist.
	ErrDomainNotAllowed = errors.New("fetch: domain not in allowlist")

	// ErrInternalIP is returned when DNS resolves to an internal/private IP.
	ErrInternalIP = errors.New("fetch: resolved IP is internal/private")

	// ErrDoubleEncoding is returned when the URL contains double-encoded characters.
	ErrDoubleEncoding = errors.New("fetch: URL contains double-encoded characters")

	// ErrRedirect is returned when the server responds with a redirect.
	ErrRedirect = errors.New("fetch: redirects are not allowed")

	// ErrNonOK is returned when the server responds with a non-200 status.
	ErrNonOK = errors.New("fetch: non-200 status code")

	// ErrNoDNS is returned when DNS resolution yields no addresses.
	ErrNoDNS = errors.New("fetch: DNS resolution returned no addresses")
)

// Pre-parsed CIDRs for internal IP ranges not covered by net.IP methods.
var internalCIDRs []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8",     // "This network" (RFC 1122)
		"100.64.0.0/10", // Carrier-grade NAT (RFC 6598)
		"198.18.0.0/15", // Benchmarking (RFC 2544)
	} {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("fetch: invalid internal CIDR %q: %v", cidr, err))
		}
		internalCIDRs = append(internalCIDRs, ipNet)
	}
}

// FetchURL fetches the content at rawURL subject to the given policy.
// It returns the response body bytes or an error.
func FetchURL(ctx context.Context, rawURL string, policy FetchPolicy) ([]byte, error) {
	if policy.Offline {
		return nil, ErrOffline
	}

	// Reject double-encoded percent signs before any URL parsing.
	if strings.Contains(rawURL, "%25") {
		return nil, ErrDoubleEncoding
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: invalid URL: %w", err)
	}

	if parsed.Scheme != "https" {
		return nil, ErrNotHTTPS
	}

	hostname := parsed.Hostname()
	if !isAllowedDomain(hostname, policy.AllowedDomains) {
		return nil, ErrDomainNotAllowed
	}

	// DNS resolution with the context timeout.
	resolveCtx, resolveCancel := context.WithTimeout(ctx, policy.Timeout)
	defer resolveCancel()

	addrs, err := net.DefaultResolver.LookupIPAddr(resolveCtx, hostname)
	if err != nil {
		return nil, fmt.Errorf("fetch: DNS lookup failed: %w", err)
	}
	if len(addrs) == 0 {
		return nil, ErrNoDNS
	}

	// Validate every resolved IP before connecting.
	for _, addr := range addrs {
		if isInternalIP(addr.IP) {
			return nil, fmt.Errorf("%w: %s", ErrInternalIP, addr.IP)
		}
	}

	// Pick the first validated address for connection pinning.
	pinnedAddr := addrs[0].IP.String()

	// Determine port from URL (default 443 for HTTPS).
	port := parsed.Port()
	if port == "" {
		port = "443"
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Pin to the pre-validated IP to prevent DNS rebinding.
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(pinnedAddr, port))
		},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   policy.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Reject redirects.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("%w: status %d", ErrRedirect, resp.StatusCode)
	}

	// Only accept 200 OK.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", ErrNonOK, resp.StatusCode)
	}

	// Read with size limit (LimitReader reads at most MaxSizeBytes+1 to detect overflow).
	limited := io.LimitReader(resp.Body, policy.MaxSizeBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("fetch: failed to read response body: %w", err)
	}
	if int64(len(body)) > policy.MaxSizeBytes {
		return nil, fmt.Errorf("fetch: response body exceeds %d bytes", policy.MaxSizeBytes)
	}

	return body, nil
}

// isAllowedDomain checks whether hostname matches any entry in the allowed list.
// Entries may be exact matches or wildcard patterns like "*.example.com".
func isAllowedDomain(hostname string, allowed []string) bool {
	hostname = strings.ToLower(hostname)
	for _, pattern := range allowed {
		pattern = strings.ToLower(pattern)
		if strings.HasPrefix(pattern, "*.") {
			// Wildcard: match the suffix (e.g. "*.example.com" matches "sub.example.com").
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(hostname, suffix) {
				return true
			}
		} else if hostname == pattern {
			return true
		}
	}
	return false
}

// isInternalIP returns true if ip is a loopback, private, link-local,
// unspecified, multicast, or otherwise internal address.
func isInternalIP(ip net.IP) bool {
	// Normalize IPv4-mapped IPv6 addresses to 4-byte form.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}

	for _, cidr := range internalCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ComputeSHA256 returns the hex-encoded SHA-256 hash of data.
func ComputeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
