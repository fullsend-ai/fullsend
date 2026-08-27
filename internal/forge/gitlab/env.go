package gitlab

import (
	"net/url"
	"os"
	"strings"
)

// URLEnvVars is the ordered list of environment variables consulted
// to resolve the GitLab instance URL. Precedence matches forge_client.go.
var URLEnvVars = []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"}

// ResolveForgeHostPort returns the GitLab forge hostname and port from
// environment variables. URL precedence matches forge_client.go:
// FULLSEND_GITLAB_URL > GITLAB_API_URL > CI_SERVER_URL.
// When the URL does not include an explicit port, the default is
// derived from the scheme ("80" for http, "443" otherwise).
// Returns ("", "") when no URL can be resolved.
func ResolveForgeHostPort() (host, port string) {
	for _, env := range URLEnvVars {
		raw := strings.TrimSpace(os.Getenv(env))
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			continue
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
	return "", ""
}
