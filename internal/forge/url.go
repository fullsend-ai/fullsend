package forge

import (
	"fmt"
	"net/url"
	"strings"
)

// ForgeURLInfo contains the parsed components of a forge URL.
type ForgeURLInfo struct {
	Forge string // "github" (future: "gitlab")
	Owner string
	Repo  string
	Path  string // path within the repo (e.g., "skills/pr-review")
	Ref   string // commit SHA, tag, or branch name
}

// ParseForgeURL extracts forge, owner, repo, path, and ref from an HTTPS URL
// pointing to a supported git forge. Returns an error if the URL is not from a
// recognized forge or cannot be parsed.
//
// Any #sha256=... fragment is stripped before parsing — handle integrity hashes
// separately via ParseIntegrityHash.
//
// Accepted GitHub formats:
//
//	https://github.com/{owner}/{repo}/tree/{ref}/{path}   (directory)
//	https://github.com/{owner}/{repo}/blob/{ref}/{path}   (file)
func ParseForgeURL(rawURL string) (*ForgeURLInfo, error) {
	// Strip fragment (including #sha256=... integrity hashes) before parsing.
	if idx := strings.LastIndex(rawURL, "#"); idx != -1 {
		rawURL = rawURL[:idx]
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q: only https is accepted", u.Scheme)
	}

	hostname := u.Hostname()
	if !IsSupportedForge(hostname) {
		return nil, fmt.Errorf("unsupported forge host %q", hostname)
	}

	// Split the path into segments, filtering out empty strings from leading/trailing slashes.
	var segments []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}

	// Need at least 4 segments: owner, repo, type (tree/blob), ref.
	if len(segments) < 4 {
		return nil, fmt.Errorf("URL path too short: need at least /{owner}/{repo}/{tree|blob}/{ref}")
	}

	owner := segments[0]
	repo := segments[1]
	pathType := segments[2]
	ref := segments[3]

	if owner == "" {
		return nil, fmt.Errorf("empty owner in URL")
	}
	if repo == "" {
		return nil, fmt.Errorf("empty repo in URL")
	}
	if ref == "" {
		return nil, fmt.Errorf("empty ref in URL")
	}

	if pathType != "tree" && pathType != "blob" {
		return nil, fmt.Errorf("unsupported path type %q: expected \"tree\" or \"blob\"", pathType)
	}

	// Everything after the ref is the path within the repo.
	var repoPath string
	if len(segments) > 4 {
		repoPath = strings.Join(segments[4:], "/")
	}

	return &ForgeURLInfo{
		Forge: "github",
		Owner: owner,
		Repo:  repo,
		Path:  repoPath,
		Ref:   ref,
	}, nil
}

// IsSupportedForge returns true if the hostname belongs to a recognized forge.
func IsSupportedForge(hostname string) bool {
	return hostname == "github.com"
}
