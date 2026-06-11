package harness

import (
	"net/url"
	"path/filepath"
	"strings"
)

// IsURL returns true if s is a valid HTTPS URL suitable for remote resource references.
func IsURL(s string) bool {
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" {
		return false
	}
	if u.Host == "" || u.User != nil {
		return false
	}
	if u.Hostname() == "" {
		return false
	}
	// Belt-and-suspenders: reject userinfo that url.Parse may not catch in all edge cases
	if strings.Contains(u.Host, "@") {
		return false
	}
	return true
}

// IsAbsPath returns true if s is an absolute file path.
func IsAbsPath(s string) bool {
	return filepath.IsAbs(s)
}

// IsRelPath returns true if s is a non-empty relative file path (not a URL and not absolute).
func IsRelPath(s string) bool {
	return s != "" && !IsURL(s) && !IsAbsPath(s)
}

// ParseIntegrityHash extracts the SHA256 hash from a URL fragment (#sha256=...).
// Returns the URL without the fragment, the hash value, and whether a valid hash was found.
// The hash is normalized to lowercase; both "sha256=ABC..." and "sha256=abc..." are accepted.
func ParseIntegrityHash(rawURL string) (cleanURL, hash string, hasHash bool) {
	idx := strings.LastIndex(rawURL, "#")
	if idx == -1 {
		return rawURL, "", false
	}
	fragment := rawURL[idx+1:]
	if !strings.HasPrefix(fragment, "sha256=") {
		return rawURL, "", false
	}
	hash = strings.ToLower(strings.TrimPrefix(fragment, "sha256="))
	if len(hash) != 64 {
		return rawURL, "", false
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return rawURL, "", false
		}
	}
	return rawURL[:idx], hash, true
}
