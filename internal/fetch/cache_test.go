package fetch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheRoundTrip(t *testing.T) {
	root := t.TempDir()
	content := []byte("hello, cache!")
	url := "https://example.com/resource.txt"

	err := CachePut(root, url, content)
	require.NoError(t, err)

	got, entry, err := CacheGet(root, ComputeSHA256(content))
	require.NoError(t, err)
	require.NotNil(t, entry)

	assert.Equal(t, content, got)
	assert.Equal(t, url, entry.URL)
	assert.Equal(t, ComputeSHA256(content), entry.SHA256)
	assert.False(t, entry.FetchTime.IsZero())
}

func TestCacheMiss(t *testing.T) {
	root := t.TempDir()
	hash := ComputeSHA256([]byte("nonexistent"))

	got, entry, err := CacheGet(root, hash)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Nil(t, entry)
}

func TestCachePartialEntry(t *testing.T) {
	root := t.TempDir()
	hash := ComputeSHA256([]byte("some content"))
	dir, err := CachePath(root, hash)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(dir, 0o700))

	// Write only metadata, no content file.
	meta := CacheEntry{URL: "https://example.com/partial", SHA256: hash}
	data, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o600))

	got, entry, err := CacheGet(root, hash)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Nil(t, entry)
}

func TestCacheIntegrityFailure(t *testing.T) {
	root := t.TempDir()
	content := []byte("original content")
	url := "https://example.com/integrity.txt"

	err := CachePut(root, url, content)
	require.NoError(t, err)

	hash := ComputeSHA256(content)
	dir, err := CachePath(root, hash)
	require.NoError(t, err)
	contentPath := filepath.Join(dir, "content")

	// Tamper with the cached content.
	require.NoError(t, os.WriteFile(contentPath, []byte("tampered!"), 0o600))

	got, entry, err := CacheGet(root, hash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache integrity check failed")
	assert.Nil(t, got)
	assert.Nil(t, entry)
}

func TestCacheMetadataCorruption(t *testing.T) {
	root := t.TempDir()
	content := []byte("original content")
	url := "https://example.com/integrity.txt"

	err := CachePut(root, url, content)
	require.NoError(t, err)

	originalHash := ComputeSHA256(content)
	dir, err := CachePath(root, originalHash)
	require.NoError(t, err)

	// Replace both content and metadata with a different but internally-consistent file.
	replacement := []byte("replaced content")
	replacementHash := ComputeSHA256(replacement)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "content"), replacement, 0o600))
	meta := CacheEntry{URL: url, SHA256: replacementHash}
	data, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o600))

	// CacheGet should detect that content doesn't match the requested hash.
	got, entry, err := CacheGet(root, originalHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache integrity check failed")
	assert.Nil(t, got)
	assert.Nil(t, entry)
}

func TestCacheSameContentDedup(t *testing.T) {
	root := t.TempDir()
	content := []byte("identical content")

	err := CachePut(root, "https://example.com/a", content)
	require.NoError(t, err)

	err = CachePut(root, "https://example.com/b", content)
	require.NoError(t, err)

	hash := ComputeSHA256(content)
	path1, err := CachePath(root, hash)
	require.NoError(t, err)
	path2, err := CachePath(root, hash)
	require.NoError(t, err)
	assert.Equal(t, path1, path2)

	got, entry, err := CacheGet(root, hash)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, content, got)

	// The second CachePut overwrites metadata, so URL reflects the last write.
	assert.Equal(t, "https://example.com/b", entry.URL)
}

func TestCachePathFormat(t *testing.T) {
	hash := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	got, err := CachePath("/workspace", hash)
	require.NoError(t, err)
	expected := filepath.Join("/workspace", ".fullsend-cache", "resources", "sha256", hash)
	assert.Equal(t, expected, got)
}

func TestCachePathValidation(t *testing.T) {
	t.Run("TraversalRejected", func(t *testing.T) {
		_, err := CachePath("/workspace", "../../etc/passwd")
		require.Error(t, err)
		assert.True(t, errors.Is(err, errInvalidHash))
	})

	t.Run("ShortHashRejected", func(t *testing.T) {
		_, err := CachePath("/workspace", "abcdef")
		require.Error(t, err)
		assert.True(t, errors.Is(err, errInvalidHash))
	})

	t.Run("UppercaseRejected", func(t *testing.T) {
		_, err := CachePath("/workspace", "ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890")
		require.Error(t, err)
		assert.True(t, errors.Is(err, errInvalidHash))
	})

	t.Run("ValidHash", func(t *testing.T) {
		path, err := CachePath("/workspace", "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
		require.NoError(t, err)
		assert.Contains(t, path, "abcdef1234567890")
	})
}

func TestCacheSymlinkProtection(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	content := []byte("symlink test content")
	hash := ComputeSHA256(content)
	cacheDir := filepath.Join(root, ".fullsend-cache", "resources", "sha256")
	require.NoError(t, os.MkdirAll(cacheDir, 0o700))

	// Plant a symlink in the hash directory pointing outside the cache.
	require.NoError(t, os.Symlink(outside, filepath.Join(cacheDir, hash)))

	// CachePut: MkdirAll follows the symlink, then validateCachePath rejects it.
	err := CachePut(root, "https://example.com/symlink", content)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache path escapes cache root")

	// For CacheGet, plant metadata+content in the outside dir so reads succeed
	// and the symlink check fires after.
	meta := CacheEntry{URL: "https://example.com/symlink", SHA256: hash}
	data, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "metadata.json"), data, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "content"), content, 0o600))

	got, entry, err := CacheGet(root, hash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache path escapes cache root")
	assert.Nil(t, got)
	assert.Nil(t, entry)
}

func TestComputeTreeHash(t *testing.T) {
	t.Run("Deterministic", func(t *testing.T) {
		files := map[string][]byte{
			"a.txt": []byte("alpha"),
			"b.txt": []byte("bravo"),
			"c.txt": []byte("charlie"),
		}
		// Compute the hash multiple times — map iteration order varies but hash must be stable.
		hash1 := ComputeTreeHash(files)
		hash2 := ComputeTreeHash(files)
		hash3 := ComputeTreeHash(files)
		assert.Equal(t, hash1, hash2)
		assert.Equal(t, hash2, hash3)
	})

	t.Run("SingleFile", func(t *testing.T) {
		files := map[string][]byte{
			"SKILL.md": []byte("# My Skill"),
		}
		hash := ComputeTreeHash(files)
		assert.Len(t, hash, 64, "should be a 64-char hex SHA256")
	})

	t.Run("DifferentFilesProduceDifferentHashes", func(t *testing.T) {
		files1 := map[string][]byte{"a.txt": []byte("hello")}
		files2 := map[string][]byte{"a.txt": []byte("world")}
		files3 := map[string][]byte{"b.txt": []byte("hello")}
		hash1 := ComputeTreeHash(files1)
		hash2 := ComputeTreeHash(files2)
		hash3 := ComputeTreeHash(files3)
		assert.NotEqual(t, hash1, hash2, "different content should produce different hashes")
		assert.NotEqual(t, hash1, hash3, "different paths should produce different hashes")
	})

	t.Run("NestedPaths", func(t *testing.T) {
		files := map[string][]byte{
			"SKILL.md":           []byte("# Skill"),
			"scripts/helper.sh":  []byte("#!/bin/bash\necho hi"),
			"sub-agents/code.md": []byte("# Code agent"),
		}
		hash := ComputeTreeHash(files)
		assert.Len(t, hash, 64)
	})
}

func TestCachePutDir_CacheGetDir_RoundTrip(t *testing.T) {
	root := t.TempDir()
	url := "https://github.com/example/repo/tree/main/skills/review"
	files := map[string][]byte{
		"SKILL.md":             []byte("# Review Skill\nA skill for reviews."),
		"scripts/helper.sh":    []byte("#!/bin/bash\necho helper"),
		"sub-agents/triage.md": []byte("# Triage sub-agent"),
	}

	treeHash, err := CachePutDir(root, url, files)
	require.NoError(t, err)
	assert.Len(t, treeHash, 64)

	treeDir, entry, err := CacheGetDir(root, treeHash)
	require.NoError(t, err)
	require.NotNil(t, entry)

	// Verify metadata.
	assert.Equal(t, url, entry.URL)
	assert.Equal(t, treeHash, entry.SHA256)
	assert.Equal(t, "directory", entry.Type)
	assert.False(t, entry.FetchTime.IsZero())
	assert.Len(t, entry.Files, 3)

	// Files should be sorted by path in metadata.
	assert.Equal(t, "SKILL.md", entry.Files[0].Path)
	assert.Equal(t, "scripts/helper.sh", entry.Files[1].Path)
	assert.Equal(t, "sub-agents/triage.md", entry.Files[2].Path)

	// Verify file content on disk.
	for relPath, expectedContent := range files {
		got, err := os.ReadFile(filepath.Join(treeDir, relPath))
		require.NoError(t, err, "reading %s", relPath)
		assert.Equal(t, expectedContent, got, "content mismatch for %s", relPath)
	}
}

func TestCacheGetDir_Miss(t *testing.T) {
	root := t.TempDir()
	hash := ComputeSHA256([]byte("nonexistent dir"))

	treeDir, entry, err := CacheGetDir(root, hash)
	require.NoError(t, err)
	assert.Empty(t, treeDir)
	assert.Nil(t, entry)
}

func TestCacheGetDir_IntegrityVerification(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"SKILL.md": []byte("# Original content"),
	}

	treeHash, err := CachePutDir(root, "https://example.com/skill", files)
	require.NoError(t, err)

	// Tamper with the cached file.
	dir, err := CachePath(root, treeHash)
	require.NoError(t, err)
	tamperedPath := filepath.Join(dir, "tree", "SKILL.md")
	require.NoError(t, os.WriteFile(tamperedPath, []byte("# Tampered!"), 0o600))

	treeDir, entry, err := CacheGetDir(root, treeHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache integrity check failed")
	assert.Empty(t, treeDir)
	assert.Nil(t, entry)
}

func TestCachePutDir_NestedDirectories(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"SKILL.md":                       []byte("# Skill"),
		"scripts/helper.sh":              []byte("#!/bin/bash\necho hi"),
		"sub-agents/review.md":           []byte("# Review"),
		"sub-agents/deep/nested/file.md": []byte("# Deep nested"),
	}

	treeHash, err := CachePutDir(root, "https://example.com/nested-skill", files)
	require.NoError(t, err)

	treeDir, entry, err := CacheGetDir(root, treeHash)
	require.NoError(t, err)
	require.NotNil(t, entry)

	// Verify all files exist with correct content.
	for relPath, expectedContent := range files {
		got, err := os.ReadFile(filepath.Join(treeDir, relPath))
		require.NoError(t, err, "reading %s", relPath)
		assert.Equal(t, expectedContent, got, "content mismatch for %s", relPath)
	}

	// Verify metadata has all files.
	assert.Len(t, entry.Files, 4)
}

func TestCacheConcurrentPut(t *testing.T) {
	root := t.TempDir()
	content := []byte("concurrent content")
	hash := ComputeSHA256(content)

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = CachePut(root, fmt.Sprintf("https://example.com/%d", idx), content)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d", i)
	}

	got, entry, err := CacheGet(root, hash)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, content, got)
	assert.Equal(t, hash, entry.SHA256)
}
