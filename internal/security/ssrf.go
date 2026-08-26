package security

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/netutil"
)

// reURLPattern matches HTTP(S) and dangerous-scheme URLs in free text.
// Mirrors the Python hook's URL_PATTERN for consistency.
var reURLPattern = regexp.MustCompile(`(?i)(?:https?|file|ftp|gopher|data|dict|ldap|tftp)://[^\s"'` + "`" + `|;<>()]+`)

// SSRFValidator validates URLs against blocked networks, hostnames,
// and schemes to prevent Server-Side Request Forgery. Adapted from
// Hermes Agent's URL validation.
type SSRFValidator struct {
	blockedHosts map[string]bool
	// allowedHosts maps "host:port" to true for hosts on the egress
	// allowlist.  When DNS resolution fails for an allowlisted host,
	// the validator defers to the L7 egress proxy instead of failing
	// closed.  A port of "0" acts as a wildcard (any port).
	allowedHosts map[string]bool
}

// NewSSRFValidator creates a validator with the default blocklists.
func NewSSRFValidator() *SSRFValidator {
	return &SSRFValidator{
		blockedHosts: map[string]bool{
			"metadata.google.internal": true,
			"metadata.goog":            true,
			"169.254.169.254":          true,
			"100.100.100.200":          true,
			"fd00:ec2::254":            true,
		},
	}
}

// NewSSRFValidatorWithAllowlist creates a validator with an egress
// allowlist.  The allowlist is a comma-separated string of host:port
// entries (e.g. "gitlab.internal:443,other.host:8443").  Hosts on the
// allowlist bypass DNS-failure fail-closed — all other checks
// (scheme, blocked hostnames, IP checks) still apply.
func NewSSRFValidatorWithAllowlist(allowlist string) *SSRFValidator {
	v := NewSSRFValidator()
	v.allowedHosts = ParseEgressAllowlist(allowlist)
	return v
}

// ParseEgressAllowlist parses a comma-separated "host:port" allowlist
// string into a lookup map.
func ParseEgressAllowlist(raw string) map[string]bool {
	m := make(map[string]bool)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return m
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// Wildcard entries are not supported — skip silently.
		// The Python hook mirrors this with a stderr warning; the Go
		// validator avoids side-effects (no other scanner writes stderr).
		if strings.Contains(entry, "*") {
			continue
		}
		// Use LastIndex instead of net.SplitHostPort because SplitHostPort
		// requires bracket notation for IPv6 and errors on entries without a
		// port — both of which are valid allowlist formats here.
		if idx := strings.LastIndex(entry, ":"); idx > 0 {
			host := strings.Trim(strings.ToLower(strings.TrimSuffix(entry[:idx], ".")), "[]")
			port := entry[idx+1:]
			if _, err := strconv.Atoi(port); err == nil {
				m[host+":"+port] = true
				continue
			}
		}
		// No port or invalid port — wildcard (port 0).
		m[strings.ToLower(strings.TrimSuffix(entry, "."))+":"+"0"] = true
	}
	return m
}

// isHostAllowlisted reports whether hostname:port is on the egress
// allowlist.
func (s *SSRFValidator) isHostAllowlisted(hostname string, port string) bool {
	if len(s.allowedHosts) == 0 {
		return false
	}
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	if s.allowedHosts[hostname+":"+port] {
		return true
	}
	// Wildcard (any port).
	return s.allowedHosts[hostname+":0"]
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
		if reason := netutil.CheckIP(ip); reason != "" {
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
		// Determine the port for allowlist matching.
		port := parsed.Port()
		if port == "" {
			if scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}

		addrs, err := net.LookupHost(hostname)
		if err != nil {
			// DNS resolution failed — allow if the host is on the
			// egress allowlist (the L7 proxy will resolve and enforce).
			if !s.isHostAllowlisted(hostname, port) {
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
			// Audit logging for the allowlist bypass lives in the Python
			// hook (log_finding to findings.jsonl). The Go validator has
			// no production caller until #6586.
			return ScanResult{Safe: true}
		}
		for _, addr := range addrs {
			if ip := net.ParseIP(addr); ip != nil {
				if reason := netutil.CheckIP(ip); reason != "" {
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
		result := s.ValidateURL(u, true)
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
// validates each one. Uses regex-based extraction (matching the Python
// hook's URL_PATTERN) to handle URLs embedded in markdown, JSON, etc.
func (s *SSRFValidator) Scan(text string) ScanResult {
	result := ScanResult{Safe: true}

	for _, match := range reURLPattern.FindAllString(text, -1) {
		// Strip trailing punctuation that may be part of surrounding text.
		match = strings.TrimRight(match, ".,;:!?")
		r := s.ValidateURL(match, true)
		if !r.Safe {
			result.Safe = false
			result.Findings = append(result.Findings, r.Findings...)
		}
	}

	return result
}
