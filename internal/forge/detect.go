package forge

import (
	"fmt"
	"net/url"
	"strings"
)

// DetectForge identifies which forge platform a git remote URL points to.
// Returns "github" or "gitlab". Returns an error for unknown hosts
// with a suggestion to use the --forge flag.
//
// Note: this detects the forge from a remote URL, which is distinct from
// IsSupportedForge (used by ParseForgeURL for full URL parsing support).
// A forge may be detectable here before full URL parsing is implemented.
func DetectForge(remoteURL string) (string, error) {
	host := extractHost(remoteURL)
	if host == "" {
		return "", fmt.Errorf("cannot extract host from remote URL %q: use --forge flag", remoteURL)
	}

	switch strings.ToLower(host) {
	case "github.com":
		return "github", nil
	case "gitlab.com":
		return "gitlab", nil
	default:
		return "", fmt.Errorf("unknown forge host %q: use --forge flag for self-hosted instances", host)
	}
}

// extractHost handles both HTTPS and SSH remote URL formats:
//   - HTTPS: https://github.com/org/repo.git → github.com
//   - SSH:   git@github.com:org/repo.git     → github.com
func extractHost(remoteURL string) string {
	remoteURL = strings.TrimSpace(remoteURL)
	if u, err := url.Parse(remoteURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	// SSH format: user@host:path
	if at := strings.Index(remoteURL, "@"); at >= 0 {
		rest := remoteURL[at+1:]
		if colon := strings.Index(rest, ":"); colon > 0 {
			return rest[:colon]
		}
	}
	return ""
}
