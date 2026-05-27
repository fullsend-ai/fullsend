package fetch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errInvalidHash = errors.New("cache: hash must be exactly 64 lowercase hex characters")

// CacheEntry is metadata for a cached remote resource.
type CacheEntry struct {
	URL       string    `json:"url"`
	FetchTime time.Time `json:"fetch_time"`
	SHA256    string    `json:"sha256"`
}

// CachePath returns the filesystem path for the cache directory keyed by
// the given content hash. The layout is:
//
//	<workspaceRoot>/.fullsend-cache/resources/sha256/<hash>/
//
// The hash must be exactly 64 lowercase hex characters (SHA-256 digest).
func CachePath(workspaceRoot, hash string) (string, error) {
	if err := validateHash(hash); err != nil {
		return "", err
	}
	return filepath.Join(workspaceRoot, ".fullsend-cache", "resources", "sha256", hash), nil
}

func validateHash(hash string) error {
	if len(hash) != 64 {
		return errInvalidHash
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errInvalidHash
		}
	}
	return nil
}

// CacheGet retrieves a previously cached resource by its content hash.
// It returns (nil, nil, nil) on a cache miss (directory or files missing).
// If the cached content fails integrity re-verification, it returns an error.
func CacheGet(workspaceRoot, hash string) ([]byte, *CacheEntry, error) {
	dir, err := CachePath(workspaceRoot, hash)
	if err != nil {
		return nil, nil, err
	}

	metadataBytes, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading cache metadata: %w", err)
	}

	var entry CacheEntry
	if err := json.Unmarshal(metadataBytes, &entry); err != nil {
		return nil, nil, fmt.Errorf("unmarshaling cache metadata: %w", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "content"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading cached content: %w", err)
	}

	if err := validateCachePath(workspaceRoot, dir); err != nil {
		return nil, nil, err
	}

	// Re-verify integrity against the caller's requested hash (the content
	// address), not the stored metadata hash — if both content and metadata
	// were replaced by an attacker, checking only entry.SHA256 would pass.
	if got := ComputeSHA256(content); got != hash {
		return nil, nil, fmt.Errorf("cache integrity check failed: expected %s, got %s", hash, got)
	}
	if entry.SHA256 != hash {
		return nil, nil, fmt.Errorf("cache metadata corruption: metadata hash %s does not match requested hash %s", entry.SHA256, hash)
	}

	return content, &entry, nil
}

// CachePut stores content in the content-addressed cache. The content is keyed
// by its SHA-256 hash, so identical content from different URLs shares a single
// cache entry (the last URL wins in metadata — provenance of all source URLs
// is tracked via fetch audit logging, not cache metadata). Both the content
// and metadata files are written atomically using a temp-file-then-rename
// pattern with fsync for durability.
func CachePut(workspaceRoot, url string, content []byte) error {
	hash := ComputeSHA256(content)
	dir, err := CachePath(workspaceRoot, hash)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	if err := validateCachePath(workspaceRoot, dir); err != nil {
		return err
	}

	entry := CacheEntry{
		URL:       url,
		FetchTime: time.Now().UTC(),
		SHA256:    hash,
	}

	// Write content atomically.
	if err := atomicWrite(dir, "content", content); err != nil {
		return fmt.Errorf("writing cached content: %w", err)
	}

	// Write metadata atomically.
	metadataBytes, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling cache metadata: %w", err)
	}
	if err := atomicWrite(dir, "metadata.json", metadataBytes); err != nil {
		return fmt.Errorf("writing cache metadata: %w", err)
	}

	return nil
}

// validateCachePath resolves symlinks on the cache directory and verifies
// the resolved path stays within the workspace's cache root.
func validateCachePath(workspaceRoot, dir string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolving cache path: %w", err)
	}
	cacheRoot := filepath.Join(workspaceRoot, ".fullsend-cache")
	resolvedRoot, err := filepath.EvalSymlinks(cacheRoot)
	if err != nil {
		return fmt.Errorf("resolving cache root: %w", err)
	}
	if !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return fmt.Errorf("cache path escapes cache root: %s", resolved)
	}
	return nil
}

// atomicWrite writes data to a temporary file in dir, then renames it to the
// final name. This ensures readers never see a partially-written file.
func atomicWrite(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, name+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
