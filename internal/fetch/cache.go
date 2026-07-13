package fetch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// DirCacheEntry is metadata for a cached directory resource (e.g., a skill).
type DirCacheEntry struct {
	URL         string         `json:"url"`
	FetchTime   time.Time      `json:"fetch_time"`
	SHA256      string         `json:"sha256"` // tree hash
	Type        string         `json:"type"`   // always "directory"
	Files       []DirFileEntry `json:"files"`
	FullListing bool           `json:"full_listing,omitempty"` // true when fetched via ForgeClient directory listing
}

// DirFileEntry records one file within a cached directory tree.
type DirFileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ComputeTreeHash computes a deterministic SHA256 hash for a directory tree.
// The hash is SHA256 of the sorted concatenation of "path:sha256(content)\n"
// for all files. This is forge-agnostic and deterministic — any implementation
// can reproduce it from the same file set.
func ComputeTreeHash(files map[string][]byte) string {
	entries := make([]string, 0, len(files))
	for path, content := range files {
		entries = append(entries, path+":"+ComputeSHA256(content))
	}
	sort.Strings(entries)
	joined := strings.Join(entries, "\n") + "\n"
	return ComputeSHA256([]byte(joined))
}

// DirCachePutOpts controls optional behavior for CachePutDir.
type DirCachePutOpts struct {
	FullListing bool // mark entry as fetched via directory listing (not single-file fallback)
}

// CachePutDir stores a directory tree in the content-addressed cache.
// files maps relative paths to their content bytes. Returns the computed
// tree hash. The directory is stored under:
//
//	<workspaceRoot>/.fullsend-cache/resources/sha256/<treeHash>/tree/<path>
//
// Uses atomic file writes within the tree directory.
func CachePutDir(workspaceRoot, url string, files map[string][]byte, opts ...DirCachePutOpts) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("cannot cache empty directory")
	}

	treeHash := ComputeTreeHash(files)
	dir, err := CachePath(workspaceRoot, treeHash)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating cache directory: %w", err)
	}

	if err := validateCachePath(workspaceRoot, dir); err != nil {
		return "", err
	}

	// Build the tree directory.
	treeDir := filepath.Join(dir, "tree")

	// Write each file.
	for relPath, content := range files {
		fullPath := filepath.Join(treeDir, relPath)
		cleanFull := filepath.Clean(fullPath)
		cleanTree := filepath.Clean(treeDir) + string(filepath.Separator)
		if !strings.HasPrefix(cleanFull, cleanTree) {
			return "", fmt.Errorf("path traversal in file path: %s", relPath)
		}
		fileDir := filepath.Dir(fullPath)
		if err := os.MkdirAll(fileDir, 0o700); err != nil {
			return "", fmt.Errorf("creating directory for %s: %w", relPath, err)
		}
		if err := atomicWrite(fileDir, filepath.Base(fullPath), content); err != nil {
			return "", fmt.Errorf("writing %s: %w", relPath, err)
		}
	}

	// Build file manifest for metadata.
	fileEntries := make([]DirFileEntry, 0, len(files))
	for relPath, content := range files {
		fileEntries = append(fileEntries, DirFileEntry{
			Path:   relPath,
			SHA256: ComputeSHA256(content),
		})
	}
	sort.Slice(fileEntries, func(i, j int) bool {
		return fileEntries[i].Path < fileEntries[j].Path
	})

	// Write metadata.
	var fullListing bool
	if len(opts) > 0 {
		fullListing = opts[0].FullListing
	}
	entry := DirCacheEntry{
		URL:         url,
		FetchTime:   time.Now().UTC(),
		SHA256:      treeHash,
		Type:        "directory",
		Files:       fileEntries,
		FullListing: fullListing,
	}
	metadataBytes, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling cache metadata: %w", err)
	}
	if err := atomicWrite(dir, "metadata.json", metadataBytes); err != nil {
		return "", fmt.Errorf("writing cache metadata: %w", err)
	}

	return treeHash, nil
}

// CacheGetDir retrieves a previously cached directory resource by its tree hash.
// Returns ("", nil, nil) on a cache miss. On a hit, returns the path to the
// tree/ subdirectory and the cache metadata. Re-verifies integrity by recomputing
// the tree hash from the cached files.
func CacheGetDir(workspaceRoot, hash string) (string, *DirCacheEntry, error) {
	dir, err := CachePath(workspaceRoot, hash)
	if err != nil {
		return "", nil, err
	}

	// Read metadata.
	metadataBytes, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, nil // cache miss
		}
		return "", nil, fmt.Errorf("reading cache metadata: %w", err)
	}

	var entry DirCacheEntry
	if err := json.Unmarshal(metadataBytes, &entry); err != nil {
		return "", nil, fmt.Errorf("unmarshaling cache metadata: %w", err)
	}

	if entry.Type != "directory" {
		return "", nil, nil // not a directory cache entry
	}

	treeDir := filepath.Join(dir, "tree")
	if _, err := os.Stat(treeDir); os.IsNotExist(err) {
		return "", nil, nil // partial cache entry
	}

	if err := validateCachePath(workspaceRoot, dir); err != nil {
		return "", nil, err
	}

	// Re-verify integrity: walk the tree directory and recompute the tree hash.
	files := make(map[string][]byte)
	err = filepath.Walk(treeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(treeDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relPath] = content
		return nil
	})
	if err != nil {
		return "", nil, fmt.Errorf("walking cache tree: %w", err)
	}

	actualHash := ComputeTreeHash(files)
	if actualHash != hash {
		return "", nil, fmt.Errorf("cache integrity check failed: expected %s, got %s", hash, actualHash)
	}

	return treeDir, &entry, nil
}

// CacheNamedSymlink creates a symlink in a cache directory so that the
// directory can be referenced by a human-readable skill name instead of the
// cache-internal "tree" directory name. It sanitizes the skill name, handles
// race conditions (concurrent symlink creation), and returns the symlinked
// path. If the skill name is invalid or matches a reserved cache file, the
// original treePath is returned unchanged.
func CacheNamedSymlink(treePath, skillName string) (string, error) {
	if skillName == "" || skillName == "." || skillName == ".." || skillName == "metadata.json" {
		skillName = "tree"
	}
	namedPath := filepath.Join(filepath.Dir(treePath), skillName)
	if namedPath == treePath {
		return treePath, nil
	}
	if _, err := os.Lstat(namedPath); os.IsNotExist(err) {
		if err := os.Symlink("tree", namedPath); err != nil && !os.IsExist(err) {
			return "", fmt.Errorf("creating named symlink: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("checking named symlink: %w", err)
	}
	return namedPath, nil
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
