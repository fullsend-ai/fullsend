package harness

import (
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/urlutil"
)

// IsURL returns true if s is a valid HTTPS URL suitable for remote resource references.
func IsURL(s string) bool {
	return urlutil.IsURL(s)
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
	return urlutil.ParseIntegrityHash(rawURL)
}
