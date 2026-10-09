package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var errInvalidHash = errors.New("cache: hash must be exactly 64 lowercase hex characters")

// atomicWriteTmpRe matches the temporary files os.CreateTemp produces for
// atomicWrite's "<name>.tmp.*" pattern before they are renamed into place.
var atomicWriteTmpRe = regexp.MustCompile(`\.tmp\.\d+$`)

// skipVanished maps fs.ErrNotExist to nil so a cache-tree walk tolerates
// entries that a concurrent writer renamed away between the directory read
// and the stat/read of the entry. All other errors pass through unchanged.
func skipVanished(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

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

// MaterializedCachePath returns the cache directory for a directory tree
// read with its symlinks replaced by their targets (a workflow definition,
// ADR 0130), keyed by its tree hash:
//
//	<workspaceRoot>/.fullsend-cache/resources/materialized/sha256/<hash>/
//
// It is a namespace of its own beside CachePath's: CacheGetDir never
// returns a tree stored here, and CachePutDir never writes here, whatever
// the hash.
func MaterializedCachePath(workspaceRoot, hash string) (string, error) {
	if err := validateHash(hash); err != nil {
		return "", err
	}
	return filepath.Join(workspaceRoot, ".fullsend-cache", "resources", "materialized", "sha256", hash), nil
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
	var putOpts DirCachePutOpts
	if len(opts) > 0 {
		putOpts = opts[0]
	}
	// Only a replacing write takes the entry lock, so this one never
	// waits and needs no caller context.
	return cachePutDirAt(context.Background(), workspaceRoot, CachePath, url, files, putOpts, false)
}

// CachePutMaterializedDir is CachePutDir for a tree read with its symlinks
// materialized: it stores the tree under MaterializedCachePath. The whole
// tree is uploaded to the sandbox, so the cached directory must hold
// exactly the given files: unless the tree already there is exactly that
// set of files, a fresh tree is built beside it and swapped into place,
// which drops any file left in the old one. A file named like the cache's
// own temporary files is refused, because CacheGetMaterializedDir treats
// such a file as a stray and would never hit. Waiting for another
// writer's lock on the entry stops when ctx is done.
func CachePutMaterializedDir(ctx context.Context, workspaceRoot, url string, files map[string][]byte) (string, error) {
	for relPath := range files {
		if atomicWriteTmpRe.MatchString(filepath.Base(relPath)) {
			return "", fmt.Errorf("%s is named like a cache temporary file (<name>.tmp.<digits>), which the cache cannot store; rename the file", relPath)
		}
	}
	return cachePutDirAt(ctx, workspaceRoot, MaterializedCachePath, url, files, DirCachePutOpts{}, true)
}

// cachePathFunc maps a hash to its cache directory in one namespace.
type cachePathFunc func(workspaceRoot, hash string) (string, error)

// cachePutDirAt stores files under cachePath. With replace false the files
// are written into the existing tree/ in place; with replace true tree/ is
// replaced as a whole unless it already holds exactly the given files (see
// CachePutMaterializedDir); ctx bounds the wait for the entry lock a
// replacing write takes.
func cachePutDirAt(ctx context.Context, workspaceRoot string, cachePath cachePathFunc, url string, files map[string][]byte, putOpts DirCachePutOpts, replace bool) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("cannot cache empty directory")
	}

	treeHash := ComputeTreeHash(files)
	dir, err := cachePath(workspaceRoot, treeHash)
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
	if replace {
		if err := replaceTree(ctx, dir, treeDir, treeHash, files); err != nil {
			return "", err
		}
	} else if err := writeTreeFiles(treeDir, files); err != nil {
		return "", err
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
	entry := DirCacheEntry{
		URL:         url,
		FetchTime:   time.Now().UTC(),
		SHA256:      treeHash,
		Type:        "directory",
		Files:       fileEntries,
		FullListing: putOpts.FullListing,
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

// writeTreeFiles writes each file under treeDir with atomic writes.
func writeTreeFiles(treeDir string, files map[string][]byte) error {
	for relPath, content := range files {
		fullPath := filepath.Join(treeDir, relPath)
		cleanFull := filepath.Clean(fullPath)
		cleanTree := filepath.Clean(treeDir) + string(filepath.Separator)
		if !strings.HasPrefix(cleanFull, cleanTree) {
			return fmt.Errorf("path traversal in file path: %s", relPath)
		}
		fileDir := filepath.Dir(fullPath)
		if err := os.MkdirAll(fileDir, 0o700); err != nil {
			return fmt.Errorf("creating directory for %s: %w", relPath, err)
		}
		if err := atomicWrite(fileDir, filepath.Base(fullPath), content); err != nil {
			return fmt.Errorf("writing %s: %w", relPath, err)
		}
	}
	return nil
}

// replaceTree makes treeDir hold exactly files. A tree that already does
// is left alone, so concurrent readers of a good tree are not disturbed.
// Otherwise the files are written to a fresh directory beside treeDir and
// swapped in under the entry's cross-process lock: the tree is checked
// again while the lock is held, so a writer never replaces a tree another
// writer has just installed, and treeDir is always either complete or
// absent (a reader then misses and refetches).
func replaceTree(ctx context.Context, dir, treeDir, treeHash string, files map[string][]byte) error {
	if ok, err := treeIsExact(treeDir, treeHash); err != nil {
		return err
	} else if ok {
		return nil
	}
	newTree, err := os.MkdirTemp(dir, "tree.new-")
	if err != nil {
		return fmt.Errorf("creating cache tree: %w", err)
	}
	defer os.RemoveAll(newTree)
	if err := writeTreeFiles(newTree, files); err != nil {
		return err
	}
	unlock, err := lockCacheEntry(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	if ok, err := treeIsExact(treeDir, treeHash); err != nil {
		return err
	} else if ok {
		return nil
	}
	const maxSwaps = 5
	for attempt := 0; ; attempt++ {
		// os.MkdirTemp only reserves a unique name: not every platform
		// renames a directory over an empty one, so the placeholder is
		// removed before the old tree takes its name.
		oldTree, err := os.MkdirTemp(dir, "tree.old-")
		if err != nil {
			return fmt.Errorf("replacing cache tree: %w", err)
		}
		if err := os.Remove(oldTree); err != nil {
			return fmt.Errorf("replacing cache tree: %w", err)
		}
		if err := os.Rename(treeDir, oldTree); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("replacing cache tree: %w", err)
		}
		renameErr := os.Rename(newTree, treeDir)
		_ = os.RemoveAll(oldTree)
		if renameErr == nil {
			return nil
		}
		if attempt+1 >= maxSwaps {
			return fmt.Errorf("replacing cache tree: %w", renameErr)
		}
	}
}

// treeIsExact reports whether treeDir holds exactly the files whose tree
// hash is treeHash: only regular files and the directories that lead to
// them, none named like a temporary file.
func treeIsExact(treeDir, treeHash string) (bool, error) {
	files, exact, err := readCacheTree(treeDir, true)
	if err != nil || !exact {
		return false, err
	}
	return ComputeTreeHash(files) == treeHash, nil
}

// readCacheTree reads every regular file under treeDir, keyed by its path
// relative to treeDir. Entries that vanish mid-walk are tolerated (a
// concurrent writer renaming its temp file away). With strict false,
// files named like atomicWrite's temp files are skipped (a crashed
// writer's leftover would otherwise poison the tree hash forever). With
// strict true, exact is false as soon as the tree holds such a file, an
// entry that is neither a regular file nor a directory, or an empty
// directory: none of these is in the hashed file set, yet each would be
// delivered with the tree.
func readCacheTree(treeDir string, strict bool) (files map[string][]byte, exact bool, err error) {
	files = make(map[string][]byte)
	exact = true
	errInexact := errors.New("inexact tree")
	err = filepath.WalkDir(treeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return skipVanished(err)
		}
		if d.IsDir() {
			if strict {
				entries, readErr := os.ReadDir(path)
				if readErr != nil {
					return skipVanished(readErr)
				}
				if len(entries) == 0 {
					return errInexact
				}
			}
			return nil
		}
		if atomicWriteTmpRe.MatchString(d.Name()) {
			if strict {
				return errInexact
			}
			return nil
		}
		if strict && !d.Type().IsRegular() {
			return errInexact
		}
		relPath, err := filepath.Rel(treeDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return skipVanished(err)
		}
		files[relPath] = content
		return nil
	})
	if errors.Is(err, errInexact) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return files, true, nil
}

// CacheGetDir retrieves a previously cached directory resource by its tree hash.
// Returns ("", nil, nil) on a cache miss. On a hit, returns the path to the
// tree/ subdirectory and the cache metadata. Re-verifies integrity by recomputing
// the tree hash from the cached files.
func CacheGetDir(workspaceRoot, hash string) (string, *DirCacheEntry, error) {
	return cacheGetDirAt(workspaceRoot, CachePath, hash, false)
}

// CacheGetMaterializedDir is CacheGetDir for trees stored by
// CachePutMaterializedDir, with a stricter check: the whole tree is
// uploaded to the sandbox, so it must hold exactly the hashed files. A
// file named like a temporary file, an entry that is neither a regular
// file nor a directory, or an empty directory makes the lookup a miss
// (the caller refetches and CachePutMaterializedDir replaces the tree)
// instead of being skipped.
func CacheGetMaterializedDir(workspaceRoot, hash string) (string, *DirCacheEntry, error) {
	return cacheGetDirAt(workspaceRoot, MaterializedCachePath, hash, true)
}

func cacheGetDirAt(workspaceRoot string, cachePath cachePathFunc, hash string, strict bool) (string, *DirCacheEntry, error) {
	dir, err := cachePath(workspaceRoot, hash)
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
	// Concurrent writers materialize files via atomicWrite, which creates
	// "<name>.tmp.<rand>" entries inside the tree before renaming them into
	// place. Skip those temp entries (a crashed writer's leftover would
	// otherwise poison the tree hash forever) and tolerate entries vanishing
	// mid-walk (a concurrent writer renaming its temp file away). The tree-hash
	// comparison below backstops both tolerances: skipping or losing a
	// legitimate file still fails the integrity check. A strict lookup
	// (materialized trees) misses on a temp-named file instead of skipping
	// it; see CacheGetMaterializedDir.
	files, exact, err := readCacheTree(treeDir, strict)
	if err != nil {
		return "", nil, fmt.Errorf("walking cache tree: %w", err)
	}
	if !exact {
		return "", nil, nil // stray entries: refetch and replace the tree
	}

	actualHash := ComputeTreeHash(files)
	if actualHash != hash {
		return "", nil, fmt.Errorf("cache integrity check failed: expected %s, got %s", hash, actualHash)
	}

	return treeDir, &entry, nil
}

// CacheNamedSymlink creates a symlink in a cache directory so that a
// cached resource can be referenced by a human-readable name instead of
// the cache-internal filename. For directory caches the internal name is
// "tree"; for single-file caches it is "content". The target is derived
// automatically from filepath.Base(cachePath), so the same function works
// for both layouts.
//
// linkName must be a single path segment. If it is empty or matches a
// reserved cache file (".", "..", "metadata.json") it falls back to the
// cache-internal name, which yields a no-op (the original cachePath is
// returned unchanged). A linkName containing a path separator is rejected
// so the link can never escape the cache directory — the safety property
// then holds regardless of caller discipline.
//
// The link is (re)created atomically: it is built at a unique temporary
// name in the same directory and renamed into place. rename(2) atomically
// swaps the entry, so a concurrent reader never observes a missing path,
// a stale or foreign entry is replaced without a read-modify-write race,
// and concurrent writers converge (each installs the same target). The
// function is idempotent: if a correct symlink already exists it returns
// immediately. Callers must not place a real subdirectory at the named
// path — renaming over a non-empty directory fails, surfacing a clear
// error rather than silent corruption.
func CacheNamedSymlink(cachePath, linkName string) (string, error) {
	target := filepath.Base(cachePath) // "tree" or "content"
	if linkName == "" || linkName == "." || linkName == ".." || linkName == "metadata.json" {
		linkName = target
	}
	if strings.ContainsRune(linkName, filepath.Separator) || strings.ContainsRune(linkName, '/') {
		return "", fmt.Errorf("cache link name %q must be a single path segment", linkName)
	}
	namedPath := filepath.Join(filepath.Dir(cachePath), linkName)
	if namedPath == cachePath {
		return cachePath, nil
	}
	// Idempotent fast path: a correct symlink already points at the target.
	if got, err := os.Readlink(namedPath); err == nil && got == target {
		return namedPath, nil
	}
	// Otherwise create it, or replace a stale/foreign entry, atomically:
	// build the symlink at a unique temp name in the same directory, then
	// rename it into place. The temp name is unique per call (os.CreateTemp),
	// so concurrent callers never collide on it, and the final rename is what
	// makes the swap observably atomic.
	dir := filepath.Dir(namedPath)
	tmp, err := os.CreateTemp(dir, ".named-*")
	if err != nil {
		return "", fmt.Errorf("creating named symlink: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	if err := os.Remove(tmpPath); err != nil {
		return "", fmt.Errorf("creating named symlink: %w", err)
	}
	if err := os.Symlink(target, tmpPath); err != nil {
		return "", fmt.Errorf("creating named symlink: %w", err)
	}
	if err := os.Rename(tmpPath, namedPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("installing named symlink: %w", err)
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
