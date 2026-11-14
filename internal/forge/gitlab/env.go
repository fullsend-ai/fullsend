package gitlab

import (
	"net/url"
	"os"
	"strings"
)

// URLEnvVars is the ordered list of environment variables consulted
// to resolve the GitLab instance URL. Precedence matches forge_client.go.
var URLEnvVars = []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"}

// ConfiguredURLSource is the source ResolveBaseURL reports for a URL the
// caller passed in (for example the manifest's gitlab.url).
const ConfiguredURLSource = "the configured GitLab URL"

// ResolveBaseURL returns the GitLab instance URL the clients use and where
// it came from. A non-empty configured URL wins (source
// ConfiguredURLSource). Otherwise the first non-empty variable of
// URLEnvVars is used as is, even when it is malformed: a later variable is
// never read. It returns ("", "") when nothing is set, in which case the
// client defaults to https://gitlab.com.
func ResolveBaseURL(configured string) (raw, source string) {
	if configured != "" {
		return configured, ConfiguredURLSource
	}
	for _, env := range URLEnvVars {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, env
		}
	}
	return "", ""
}

// HostPort returns the hostname and port of a GitLab URL. When the URL does
// not include an explicit port, the default is derived from the scheme
// ("80" for http, "443" otherwise). It returns ("", "") when raw does not
// parse as a URL with a host.
func HostPort(raw string) (host, port string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", ""
	}
	p := u.Port()
	if p == "" {
		if u.Scheme == "http" {
			p = "80"
		} else {
			p = "443"
		}
	}
	return u.Hostname(), p
}

// ResolveForgeHostPort returns the GitLab forge hostname and port from
// environment variables, using the same URL the clients use
// (ResolveBaseURL with no configured URL): FULLSEND_GITLAB_URL >
// GITLAB_API_URL > CI_SERVER_URL, first non-empty value only. Returns
// ("", "") when no variable is set, or when the first one set is not a URL
// with a host or is refused by ValidateBaseURL (http for a host that is
// not loopback), the rule the clients apply.
func ResolveForgeHostPort() (host, port string) {
	raw, _ := ResolveBaseURL("")
	if raw == "" || ValidateBaseURL(raw) != nil {
		return "", ""
	}
	return HostPort(raw)
}
