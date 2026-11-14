package gitlab

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// URLEnvVars is the ordered list of environment variables consulted
// to resolve the GitLab instance URL: the first non-empty one wins.
// ResolveURL is the only reader; the forge client, the status client,
// the forge profile generator and the provider profile binding all go
// through it.
var URLEnvVars = []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"}

// ResolveURL returns the GitLab instance URL from the first non-empty of
// URLEnvVars (values are trimmed) and the name of the variable it came
// from. It returns ("", "", nil) when none is set, so each caller decides
// what an unset URL means.
//
// A set URL must be absolute with a host, use https (or http for a
// loopback host only: localhost or a loopback IP), and carry a port in
// 1-65535 if it names one. Anything else is an error naming the variable,
// rather than a fall-through to the next variable: a typo in
// FULLSEND_GITLAB_URL must not silently select CI_SERVER_URL.
func ResolveURL() (rawURL, envVar string, err error) {
	for _, env := range URLEnvVars {
		raw := strings.TrimSpace(os.Getenv(env))
		if raw == "" {
			continue
		}
		if _, err := parseInstanceURL(raw); err != nil {
			return "", env, fmt.Errorf("%s: %w", env, err)
		}
		return raw, env, nil
	}
	return "", "", nil
}

// ResolveForgeHostPort returns the hostname and port of the GitLab
// instance URL that ResolveURL selects. When the URL names no port, it is
// derived from the scheme ("80" for http, "443" for https). It returns
// ("", "", nil) when no URL is set, and ResolveURL's error when the URL
// is invalid.
func ResolveForgeHostPort() (host, port string, err error) {
	raw, _, err := ResolveURL()
	if err != nil || raw == "" {
		return "", "", err
	}
	u, err := parseInstanceURL(raw)
	if err != nil {
		return "", "", err
	}
	port = u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "http") {
			port = "80"
		} else {
			port = "443"
		}
	}
	return u.Hostname(), port, nil
}

// parseInstanceURL parses and validates a GitLab instance URL (see
// ResolveURL for the rules).
func parseInstanceURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("GitLab URL is not a valid URL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("GitLab URL %q has no host", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isLoopbackHost(host) {
			return nil, fmt.Errorf("GitLab URL %q uses http, which is allowed only for a loopback host; use https", raw)
		}
	default:
		return nil, fmt.Errorf("GitLab URL %q must use https", raw)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("GitLab URL %q has a port outside 1-65535", raw)
		}
	}
	return u, nil
}

// isLoopbackHost reports whether host is localhost or a loopback IP.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
