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
//
// Accepted GitLab formats (supports nested groups):
//
//	https://gitlab.com/{group}[/subgroup...]/{repo}/-/tree/{ref}/{path}
//	https://gitlab.com/{group}[/subgroup...]/{repo}/-/blob/{ref}/{path}
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
	if !isRecognizedForge(hostname) {
		return nil, fmt.Errorf("unsupported forge host %q", hostname)
	}

	// Determine forge name from hostname.
	forgeName := hostnameToForge(hostname)

	// Split the path into segments, filtering out empty strings from leading/trailing slashes.
	var segments []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}

	if forgeName == "gitlab" {
		return parseGitLabURL(segments)
	}

	// GitHub format: /{owner}/{repo}/{tree|blob}/{ref}/{path...}
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
		Forge: forgeName,
		Owner: owner,
		Repo:  repo,
		Path:  repoPath,
		Ref:   ref,
	}, nil
}

// parseGitLabURL parses a GitLab URL path (segments after the host).
// GitLab uses "/-/" as a separator between the project path and
// resource type. Nested groups are supported (e.g., group/subgroup/repo).
//
// Format: {group}[/subgroup...]/{repo}/-/{tree|blob}/{ref}/{path...}
func parseGitLabURL(segments []string) (*ForgeURLInfo, error) {
	// Find the "/-/" separator (represented as "-" in segments).
	dashIdx := -1
	for i, s := range segments {
		if s == "-" {
			dashIdx = i
			break
		}
	}

	if dashIdx < 0 {
		return nil, fmt.Errorf("URL path too short: need at least /{group}/{repo}/-/{tree|blob}/{ref}")
	}

	// Need at least 2 segments before dash (group + repo) and 2 after (type + ref).
	if dashIdx < 2 {
		return nil, fmt.Errorf("URL path too short: need at least /{group}/{repo}/-/{tree|blob}/{ref}")
	}
	if len(segments) < dashIdx+3 {
		return nil, fmt.Errorf("URL path too short: need at least /{group}/{repo}/-/{tree|blob}/{ref}")
	}

	// Everything before dash except the last segment is the owner (group/subgroups).
	// The last segment before dash is the repo.
	owner := strings.Join(segments[:dashIdx-1], "/")
	repo := segments[dashIdx-1]
	pathType := segments[dashIdx+1]
	ref := segments[dashIdx+2]

	if pathType != "tree" && pathType != "blob" {
		return nil, fmt.Errorf("unsupported path type %q: expected \"tree\" or \"blob\"", pathType)
	}

	var repoPath string
	if len(segments) > dashIdx+3 {
		repoPath = strings.Join(segments[dashIdx+3:], "/")
	}

	return &ForgeURLInfo{
		Forge: "gitlab",
		Owner: owner,
		Repo:  repo,
		Path:  repoPath,
		Ref:   ref,
	}, nil
}

// hostnameToForge maps a forge hostname to its short name.
func hostnameToForge(hostname string) string {
	switch hostname {
	case "github.com":
		return "github"
	case "gitlab.com":
		return "gitlab"
	default:
		return hostname
	}
}

// ParseRawContentURL extracts forge, owner, repo, path, and ref from a
// raw.githubusercontent.com URL.
//
// Accepted format:
//
//	https://raw.githubusercontent.com/{owner}/{repo}/{ref}/{path...}
//
// The ref is a commit SHA, tag, or branch name. Everything after it is the
// file/directory path within the repo (may be empty).
func ParseRawContentURL(rawURL string) (*ForgeURLInfo, error) {
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
	if u.Hostname() != "raw.githubusercontent.com" {
		return nil, fmt.Errorf("not a raw.githubusercontent.com URL: %s", u.Hostname())
	}

	var segments []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}

	// Need at least 3 segments: owner, repo, ref.
	if len(segments) < 3 {
		return nil, fmt.Errorf("URL path too short: need at least /{owner}/{repo}/{ref}")
	}

	owner := segments[0]
	repo := segments[1]
	ref := segments[2]

	if owner == "" {
		return nil, fmt.Errorf("empty owner in URL")
	}
	if repo == "" {
		return nil, fmt.Errorf("empty repo in URL")
	}
	if ref == "" {
		return nil, fmt.Errorf("empty ref in URL")
	}

	var repoPath string
	if len(segments) > 3 {
		repoPath = strings.Join(segments[3:], "/")
	}

	return &ForgeURLInfo{
		Forge: "github",
		Owner: owner,
		Repo:  repo,
		Path:  repoPath,
		Ref:   ref,
	}, nil
}

// CloneURL returns the HTTPS clone URL for the repository.
func (f *ForgeURLInfo) CloneURL() string {
	return fmt.Sprintf("https://%s/%s/%s.git", forgeHost(f.Forge), f.Owner, f.Repo)
}

func forgeHost(forge string) string {
	switch forge {
	case "github":
		return "github.com"
	case "gitlab":
		return "gitlab.com"
	default:
		return forge
	}
}

// IsSupportedForge returns true if the hostname belongs to a forge with
// full fetch/clone support. Use this for validating user-facing URLs
// (e.g., skill references) where the system must actually be able to
// retrieve content.
func IsSupportedForge(hostname string) bool {
	return hostname == "github.com"
}

// isRecognizedForge returns true for any forge whose URL format we can parse,
// even if fetch support has not landed yet.
func isRecognizedForge(hostname string) bool {
	return hostname == "github.com" || hostname == "gitlab.com"
}
