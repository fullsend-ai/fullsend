package security

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// SSRFValidator validates URLs against blocked networks, hostnames,
// and schemes to prevent Server-Side Request Forgery. Adapted from
// Hermes Agent's URL validation.
type SSRFValidator struct {
	blockedHosts map[string]bool
}

// NewSSRFValidator creates a validator with the default blocklists.
func NewSSRFValidator() *SSRFValidator {
	return &SSRFValidator{
		blockedHosts: map[string]bool{
			"metadata.google.internal": true,
			"metadata.goog":           true,
			"169.254.169.254":         true,
			"100.100.100.200":         true,
			"fd00:ec2::254":           true,
		},
	}
}

func (s *SSRFValidator) Name() string { return "ssrf_validator" }

// blockedSchemes are URL schemes that should never be followed.
var blockedSchemes = map[string]bool{
	"file":   true,
	"ftp":    true,
	"gopher": true,
	"data":   true,
	"dict":   true,
	"ldap":   true,
	"tftp":   true,
}

// ValidateURL checks a single URL for SSRF risk.
// Set resolveDNS to true to resolve hostnames and check resolved IPs.
func (s *SSRFValidator) ValidateURL(rawURL string, resolveDNS bool) ScanResult {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ScanResult{
			Safe:     false,
			Findings: []Finding{{Scanner: "ssrf_validator", Name: "malformed_url", Severity: "high", Detail: "Malformed URL"}},
		}
	}

	scheme := strings.ToLower(parsed.Scheme)

	// Check scheme
	if blockedSchemes[scheme] {
		return ScanResult{
			Safe: false,
			Findings: []Finding{{
				Scanner:  "ssrf_validator",
				Name:     "blocked_scheme",
				Severity: "high",
				Detail:   fmt.Sprintf("Blocked scheme: %s", scheme),
			}},
		}
	}
	if scheme != "http" && scheme != "https" {
		return ScanResult{
			Safe: false,
			Findings: []Finding{{
				Scanner:  "ssrf_validator",
				Name:     "disallowed_scheme",
				Severity: "medium",
				Detail:   fmt.Sprintf("Disallowed scheme: %s", scheme),
			}},
		}
	}

	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return ScanResult{
			Safe: false,
			Findings: []Finding{{
				Scanner:  "ssrf_validator",
				Name:     "empty_hostname",
				Severity: "high",
				Detail:   "No hostname in URL",
			}},
		}
	}

	// Check blocked hostnames
	if s.blockedHosts[hostname] {
		return ScanResult{
			Safe: false,
			Findings: []Finding{{
				Scanner:  "ssrf_validator",
				Name:     "blocked_hostname",
				Severity: "critical",
				Detail:   fmt.Sprintf("Blocked hostname: %s", hostname),
			}},
		}
	}

	// Check if hostname is a raw IP
	if ip := net.ParseIP(hostname); ip != nil {
		if reason := checkIP(ip); reason != "" {
			return ScanResult{
				Safe: false,
				Findings: []Finding{{
					Scanner:  "ssrf_validator",
					Name:     "blocked_ip",
					Severity: "critical",
					Detail:   fmt.Sprintf("IP %s: %s", ip, reason),
				}},
			}
		}
	}

	// DNS resolution check (fail-closed)
	if resolveDNS {
		addrs, err := net.LookupHost(hostname)
		if err != nil {
			return ScanResult{
				Safe: false,
				Findings: []Finding{{
					Scanner:  "ssrf_validator",
					Name:     "dns_failure",
					Severity: "high",
					Detail:   fmt.Sprintf("DNS resolution failed for %s (fail-closed)", hostname),
				}},
			}
		}
		for _, addr := range addrs {
			if ip := net.ParseIP(addr); ip != nil {
				if reason := checkIP(ip); reason != "" {
					return ScanResult{
						Safe: false,
						Findings: []Finding{{
							Scanner:  "ssrf_validator",
							Name:     "blocked_resolved_ip",
							Severity: "critical",
							Detail:   fmt.Sprintf("DNS for %s resolved to blocked IP %s: %s", hostname, addr, reason),
						}},
					}
				}
			}
		}
	}

	return ScanResult{Safe: true}
}

// ValidateRedirectChain checks each URL in a redirect chain.
func (s *SSRFValidator) ValidateRedirectChain(urls []string) ScanResult {
	for i, u := range urls {
		result := s.ValidateURL(u, false)
		if !result.Safe {
			for j := range result.Findings {
				result.Findings[j].Detail = fmt.Sprintf("Redirect hop %d: %s", i, result.Findings[j].Detail)
			}
			return result
		}
	}
	return ScanResult{Safe: true}
}

// Scan implements the Scanner interface. Extracts URLs from text and
// validates each one.
func (s *SSRFValidator) Scan(text string) ScanResult {
	// Simple URL extraction — finds http:// and https:// URLs
	result := ScanResult{Safe: true}

	words := strings.Fields(text)
	for _, word := range words {
		// Strip common punctuation that wraps URLs
		word = strings.Trim(word, "\"'<>()[]{}`,;")
		if !strings.HasPrefix(word, "http://") && !strings.HasPrefix(word, "https://") {
			continue
		}
		r := s.ValidateURL(word, false)
		if !r.Safe {
			result.Safe = false
			result.Findings = append(result.Findings, r.Findings...)
		}
	}

	return result
}

func checkIP(ip net.IP) string {
	if ip.IsLoopback() {
		return "loopback address"
	}
	if ip.IsPrivate() {
		return "private address (RFC 1918)"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "link-local address"
	}
	if ip.IsMulticast() {
		return "multicast address"
	}
	if ip.IsUnspecified() {
		return "unspecified address"
	}

	// CGNAT / shared address space (RFC 6598): 100.64.0.0/10
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	if cgnat.Contains(ip) {
		return "CGNAT address (RFC 6598)"
	}

	// Documentation ranges
	docRanges := []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"}
	for _, cidr := range docRanges {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return "documentation address"
		}
	}

	return ""
}
