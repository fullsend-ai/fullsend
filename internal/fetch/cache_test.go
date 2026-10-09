package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

func TestCacheGetDir_IgnoresAtomicWriteTempFiles(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"SKILL.md":          []byte("# Skill"),
		"scripts/helper.sh": []byte("#!/bin/bash\necho helper"),
	}

	treeHash, err := CachePutDir(root, "https://example.com/skill", files)
	require.NoError(t, err)

	// Plant leftover atomicWrite temp files, as a concurrent (or crashed)
	// writer materializing the same entry would: one at the tree root and one
	// in a subdirectory.
	dir, err := CachePath(root, treeHash)
	require.NoError(t, err)
	for _, stale := range []string{
		filepath.Join(dir, "tree", "SKILL.md.tmp.1337760552"),
		filepath.Join(dir, "tree", "scripts", "helper.sh.tmp.42"),
	} {
		require.NoError(t, os.WriteFile(stale, []byte("partial write"), 0o600))
	}

	treeDir, entry, err := CacheGetDir(root, treeHash)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, treeHash, entry.SHA256)
	assert.NotEmpty(t, treeDir)
}

func TestSkipVanished(t *testing.T) {
	assert.NoError(t, skipVanished(fs.ErrNotExist))
	assert.NoError(t, skipVanished(fmt.Errorf("lstat foo.tmp.42: %w", fs.ErrNotExist)))
	sentinel := errors.New("permission denied")
	assert.ErrorIs(t, skipVanished(sentinel), sentinel)
}

func TestCacheGetDir_NonVanishedWalkErrorPropagates(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod-based access denial does not apply to root")
	}
	root := t.TempDir()
	files := map[string][]byte{
		"SKILL.md":          []byte("# Skill"),
		"scripts/helper.sh": []byte("#!/bin/bash\necho helper"),
	}

	treeHash, err := CachePutDir(root, "https://example.com/skill", files)
	require.NoError(t, err)

	dir, err := CachePath(root, treeHash)
	require.NoError(t, err)

	// An unreadable subdirectory produces a non-ErrNotExist walk error,
	// which must propagate rather than be tolerated.
	locked := filepath.Join(dir, "tree", "scripts")
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	_, _, err = CacheGetDir(root, treeHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "walking cache tree")
}

func TestCacheGetDir_UnreadableFileErrorPropagates(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod-based access denial does not apply to root")
	}
	root := t.TempDir()
	files := map[string][]byte{
		"SKILL.md": []byte("# Skill"),
	}

	treeHash, err := CachePutDir(root, "https://example.com/skill", files)
	require.NoError(t, err)

	dir, err := CachePath(root, treeHash)
	require.NoError(t, err)

	// An unreadable file fails os.ReadFile with a non-ErrNotExist error,
	// which must propagate rather than be tolerated.
	locked := filepath.Join(dir, "tree", "SKILL.md")
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

	_, _, err = CacheGetDir(root, treeHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "walking cache tree")
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

func TestCacheNamedSymlink(t *testing.T) {
	t.Run("creates symlink with skill name", func(t *testing.T) {
		dir := t.TempDir()
		treePath := filepath.Join(dir, "tree")
		require.NoError(t, os.Mkdir(treePath, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(treePath, "SKILL.md"), []byte("hi"), 0o600))

		got, err := CacheNamedSymlink(treePath, "pr-review")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "pr-review"), got)
		assert.FileExists(t, filepath.Join(got, "SKILL.md"))
	})

	t.Run("idempotent on second call", func(t *testing.T) {
		dir := t.TempDir()
		treePath := filepath.Join(dir, "tree")
		require.NoError(t, os.Mkdir(treePath, 0o755))

		_, err := CacheNamedSymlink(treePath, "review")
		require.NoError(t, err)
		got, err := CacheNamedSymlink(treePath, "review")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "review"), got)
	})

	t.Run("reserved names fall back to tree", func(t *testing.T) {
		for _, name := range []string{"", ".", "..", "metadata.json"} {
			dir := t.TempDir()
			treePath := filepath.Join(dir, "tree")
			require.NoError(t, os.Mkdir(treePath, 0o755))

			got, err := CacheNamedSymlink(treePath, name)
			require.NoError(t, err)
			assert.Equal(t, treePath, got, "reserved name %q should return treePath unchanged", name)
		}
	})

	t.Run("concurrent creation tolerates EEXIST", func(t *testing.T) {
		dir := t.TempDir()
		treePath := filepath.Join(dir, "tree")
		require.NoError(t, os.Mkdir(treePath, 0o755))

		var wg sync.WaitGroup
		errs := make([]error, 10)
		for i := range errs {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, errs[idx] = CacheNamedSymlink(treePath, "skill")
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			assert.NoError(t, err, "goroutine %d", i)
		}
	})

	t.Run("creates symlink for content file", func(t *testing.T) {
		dir := t.TempDir()
		contentPath := filepath.Join(dir, "content")
		require.NoError(t, os.WriteFile(contentPath, []byte("id: my-profile\n"), 0o600))

		got, err := CacheNamedSymlink(contentPath, "my-profile.yaml")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "my-profile.yaml"), got)

		// Verify the symlink target is readable.
		data, err := os.ReadFile(got)
		require.NoError(t, err)
		assert.Equal(t, "id: my-profile\n", string(data))
	})

	t.Run("reserved names fall back to content for content paths", func(t *testing.T) {
		for _, name := range []string{"", ".", "..", "metadata.json"} {
			dir := t.TempDir()
			contentPath := filepath.Join(dir, "content")
			require.NoError(t, os.WriteFile(contentPath, []byte("data"), 0o600))

			got, err := CacheNamedSymlink(contentPath, name)
			require.NoError(t, err)
			assert.Equal(t, contentPath, got,
				"reserved name %q with content path should return contentPath unchanged", name)
		}
	})

	t.Run("recreates a symlink pointing at the wrong target", func(t *testing.T) {
		dir := t.TempDir()
		contentPath := filepath.Join(dir, "content")
		require.NoError(t, os.WriteFile(contentPath, []byte("id: my-profile\n"), 0o600))

		namedPath := filepath.Join(dir, "my-profile.yaml")
		require.NoError(t, os.Symlink("some-other-file", namedPath))

		got, err := CacheNamedSymlink(contentPath, "my-profile.yaml")
		require.NoError(t, err)
		assert.Equal(t, namedPath, got)

		target, err := os.Readlink(namedPath)
		require.NoError(t, err)
		assert.Equal(t, "content", target, "stale symlink should be recreated to point at content")

		data, err := os.ReadFile(got)
		require.NoError(t, err)
		assert.Equal(t, "id: my-profile\n", string(data))
	})

	t.Run("concurrent replacement of a stale symlink converges without error", func(t *testing.T) {
		dir := t.TempDir()
		contentPath := filepath.Join(dir, "content")
		require.NoError(t, os.WriteFile(contentPath, []byte("id: my-profile\n"), 0o600))

		namedPath := filepath.Join(dir, "my-profile.yaml")
		require.NoError(t, os.Symlink("some-other-file", namedPath))

		var wg sync.WaitGroup
		errs := make([]error, 30)
		for i := range errs {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, errs[idx] = CacheNamedSymlink(contentPath, "my-profile.yaml")
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			assert.NoError(t, err, "goroutine %d", i)
		}

		target, err := os.Readlink(namedPath)
		require.NoError(t, err)
		assert.Equal(t, "content", target)
	})

	t.Run("recreates when a non-symlink file occupies the named path", func(t *testing.T) {
		dir := t.TempDir()
		contentPath := filepath.Join(dir, "content")
		require.NoError(t, os.WriteFile(contentPath, []byte("id: my-profile\n"), 0o600))

		namedPath := filepath.Join(dir, "my-profile.yaml")
		require.NoError(t, os.WriteFile(namedPath, []byte("stale regular file, not a symlink"), 0o600))

		got, err := CacheNamedSymlink(contentPath, "my-profile.yaml")
		require.NoError(t, err)
		assert.Equal(t, namedPath, got)

		info, err := os.Lstat(namedPath)
		require.NoError(t, err)
		assert.True(t, info.Mode()&os.ModeSymlink != 0, "foreign regular file should be replaced with a symlink")

		data, err := os.ReadFile(got)
		require.NoError(t, err)
		assert.Equal(t, "id: my-profile\n", string(data))
	})

	t.Run("returns an error when the cache directory is not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root bypasses directory permission checks")
		}
		dir := t.TempDir()
		contentPath := filepath.Join(dir, "content")
		require.NoError(t, os.WriteFile(contentPath, []byte("data"), 0o600))
		require.NoError(t, os.Chmod(dir, 0o500)) // read+execute, no write
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		_, err := CacheNamedSymlink(contentPath, "my-profile.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "named symlink")
	})

	t.Run("rejects link names containing a path separator", func(t *testing.T) {
		dir := t.TempDir()
		contentPath := filepath.Join(dir, "content")
		require.NoError(t, os.WriteFile(contentPath, []byte("data"), 0o600))

		_, err := CacheNamedSymlink(contentPath, filepath.Join("sub", "evil.yaml"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "single path segment")

		// No entry should have been created for the rejected name.
		_, statErr := os.Lstat(filepath.Join(dir, "sub"))
		assert.True(t, os.IsNotExist(statErr))
	})
}

func TestMaterializedDirCacheIsSeparate(t *testing.T) {
	files := map[string][]byte{"plugin.json": []byte("{}"), "link-copy.txt": []byte("x")}
	const materializedURL = "https://github.com/example-org/sample-pipeline/tree/abc"
	const strictURL = "https://github.com/example-org/other-pipeline/tree/def"

	t.Run("materialized entry is invisible to the shared namespace", func(t *testing.T) {
		ws := t.TempDir()
		hash, err := CachePutMaterializedDir(context.Background(), ws, materializedURL, files)
		require.NoError(t, err)
		assert.Equal(t, ComputeTreeHash(files), hash)

		treePath, entry, err := CacheGetDir(ws, hash)
		require.NoError(t, err)
		assert.Empty(t, treePath, "a strict lookup never sees a materialized tree")
		assert.Nil(t, entry)

		treePath, entry, err = CacheGetMaterializedDir(ws, hash)
		require.NoError(t, err)
		require.NotNil(t, entry)
		assert.Equal(t, materializedURL, entry.URL)
		dir, err := MaterializedCachePath(ws, hash)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "tree"), treePath)
		assert.Equal(t, filepath.Join(ws, ".fullsend-cache", "resources", "materialized", "sha256", hash), dir)
	})

	// Two different URLs whose trees hash the same, one cached as a
	// materialized tree and one strictly, in both orders: each namespace
	// keeps its own entry and its own metadata.
	for _, materializedFirst := range []bool{true, false} {
		name := "strict first"
		if materializedFirst {
			name = "materialized first"
		}
		t.Run("same hash, two URLs, "+name, func(t *testing.T) {
			ws := t.TempDir()
			putMaterialized := func() {
				_, err := CachePutMaterializedDir(context.Background(), ws, materializedURL, files)
				require.NoError(t, err)
			}
			putStrict := func() {
				_, err := CachePutDir(ws, strictURL, files)
				require.NoError(t, err)
			}
			if materializedFirst {
				putMaterialized()
				putStrict()
			} else {
				putStrict()
				putMaterialized()
			}
			hash := ComputeTreeHash(files)

			strictPath, strictEntry, err := CacheGetDir(ws, hash)
			require.NoError(t, err)
			require.NotNil(t, strictEntry)
			assert.Equal(t, strictURL, strictEntry.URL, "the strict fetch did not relabel the materialized entry")

			matPath, matEntry, err := CacheGetMaterializedDir(ws, hash)
			require.NoError(t, err)
			require.NotNil(t, matEntry)
			assert.Equal(t, materializedURL, matEntry.URL, "the materialized fetch did not relabel the strict entry")
			assert.NotEqual(t, strictPath, matPath)
		})
	}

	t.Run("invalid hash", func(t *testing.T) {
		_, err := MaterializedCachePath(t.TempDir(), "abc")
		assert.ErrorIs(t, err, errInvalidHash)
		_, _, err = CacheGetMaterializedDir(t.TempDir(), "abc")
		assert.ErrorIs(t, err, errInvalidHash)
	})
}

// A materialized tree is uploaded whole, so a lookup must hit only when
// the tree holds exactly the hashed files.
func TestCacheGetMaterializedDir_StrayEntriesMiss(t *testing.T) {
	files := map[string][]byte{
		".claude-plugin/plugin.json": []byte(`{"name":"sample"}`),
		"scripts/helper.sh":          []byte("echo hi\n"),
	}
	const url = "https://github.com/example-org/sample-pipeline/tree/abc"

	plant := map[string]func(t *testing.T, treeDir string){
		"temp-named file": func(t *testing.T, treeDir string) {
			require.NoError(t, os.WriteFile(filepath.Join(treeDir, "scripts", "helper.tmp.123"), []byte("planted"), 0o600))
		},
		"symlink": func(t *testing.T, treeDir string) {
			require.NoError(t, os.Symlink("helper.sh", filepath.Join(treeDir, "scripts", "alias")))
		},
		"empty directory": func(t *testing.T, treeDir string) {
			require.NoError(t, os.Mkdir(filepath.Join(treeDir, "empty"), 0o700))
		},
	}
	for name, plantFn := range plant {
		t.Run(name, func(t *testing.T) {
			ws := t.TempDir()
			hash, err := CachePutMaterializedDir(context.Background(), ws, url, files)
			require.NoError(t, err)
			treeDir, entry, err := CacheGetMaterializedDir(ws, hash)
			require.NoError(t, err)
			require.NotNil(t, entry)

			plantFn(t, treeDir)

			got, entry, err := CacheGetMaterializedDir(ws, hash)
			require.NoError(t, err)
			assert.Empty(t, got, "a stray entry in a materialized tree is a miss")
			assert.Nil(t, entry)

			// Re-putting replaces the tree: the stray entry is gone and
			// the lookup hits again.
			_, err = CachePutMaterializedDir(context.Background(), ws, url, files)
			require.NoError(t, err)
			got, entry, err = CacheGetMaterializedDir(ws, hash)
			require.NoError(t, err)
			require.NotNil(t, entry)
			assert.Equal(t, treeDir, got)
			var paths []string
			require.NoError(t, filepath.WalkDir(got, func(p string, d fs.DirEntry, err error) error {
				require.NoError(t, err)
				if !d.IsDir() {
					rel, _ := filepath.Rel(got, p)
					paths = append(paths, filepath.ToSlash(rel))
				}
				return nil
			}))
			assert.ElementsMatch(t, []string{".claude-plugin/plugin.json", "scripts/helper.sh"}, paths)

			// No swap leftovers beside the tree; the entry lock file a
			// replacing writer takes is the only other name.
			dir, err := MaterializedCachePath(ws, hash)
			require.NoError(t, err)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			assert.ElementsMatch(t, []string{".tree.lock", "metadata.json", "tree"}, names)
		})
	}
}

// The shared namespace keeps skipping temp-named files: a crashed
// writer's leftover must not turn every skill or plugin lookup into a
// refetch.
func TestCacheGetDir_TempFileStillSkippedInSharedNamespace(t *testing.T) {
	ws := t.TempDir()
	files := map[string][]byte{"SKILL.md": []byte("# skill")}
	hash, err := CachePutDir(ws, "https://github.com/example-org/sample-skill/tree/abc", files)
	require.NoError(t, err)
	treeDir, _, err := CacheGetDir(ws, hash)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "SKILL.md.tmp.42"), []byte("partial"), 0o600))

	got, entry, err := CacheGetDir(ws, hash)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, treeDir, got)
}

// A good materialized tree is left in place on a re-put, so readers of
// it are not disturbed.
func TestCachePutMaterializedDir_KeepsExactTree(t *testing.T) {
	ws := t.TempDir()
	files := map[string][]byte{"a.txt": []byte("a"), "sub/b.txt": []byte("b")}
	hash, err := CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files)
	require.NoError(t, err)
	treeDir, _, err := CacheGetMaterializedDir(ws, hash)
	require.NoError(t, err)
	before, err := os.Stat(treeDir)
	require.NoError(t, err)

	_, err = CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files)
	require.NoError(t, err)
	after, err := os.Stat(treeDir)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "an exact tree is not replaced")
}

// A tree whose files hash differently is replaced too.
func TestCachePutMaterializedDir_ReplacesTamperedTree(t *testing.T) {
	ws := t.TempDir()
	files := map[string][]byte{"a.txt": []byte("a")}
	hash, err := CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files)
	require.NoError(t, err)
	treeDir, _, err := CacheGetMaterializedDir(ws, hash)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "a.txt"), []byte("tampered"), 0o600))
	_, _, err = CacheGetMaterializedDir(ws, hash)
	require.Error(t, err, "a changed file still fails the integrity check")

	_, err = CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files)
	require.NoError(t, err)
	got, _, err := CacheGetMaterializedDir(ws, hash)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(got, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "a", string(content))
}

func TestCachePutMaterializedDir_RefusesTempNamedFile(t *testing.T) {
	_, err := CachePutMaterializedDir(context.Background(), t.TempDir(), "https://example.com/x", map[string][]byte{
		"scripts/run.tmp.7": []byte("x"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scripts/run.tmp.7 is named like a cache temporary file")
	assert.Contains(t, err.Error(), "rename the file")
}

func TestCachePutMaterializedDir_ConcurrentReplace(t *testing.T) {
	ws := t.TempDir()
	files := map[string][]byte{"a.txt": []byte("a"), "sub/b.txt": []byte("b")}
	hash, err := CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files)
	require.NoError(t, err)
	treeDir, _, err := CacheGetMaterializedDir(ws, hash)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "stray.tmp.1"), []byte("x"), 0o600))

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		assert.NoError(t, err)
	}
	got, entry, err := CacheGetMaterializedDir(ws, hash)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, treeDir, got)
}

// TestCachePutMaterializedDir_ReadersKeepTheirTree checks the reader side
// of concurrent replacement: once a writer's put returns, the tree it
// installed or found stays in place while other writers are still
// running, so a reader that checks the tree right away always sees it
// whole.
func TestCachePutMaterializedDir_ReadersKeepTheirTree(t *testing.T) {
	ws := t.TempDir()
	files := map[string][]byte{"a.txt": []byte("a"), "sub/b.txt": []byte("b"), "sub/deep/c.txt": []byte("c")}
	hash, err := CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files)
	require.NoError(t, err)
	treeDir, _, err := CacheGetMaterializedDir(ws, hash)
	require.NoError(t, err)
	// Make the existing tree stale, so the first writers must replace it.
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "stray.tmp.1"), []byte("x"), 0o600))

	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := CachePutMaterializedDir(context.Background(), ws, "https://example.com/x", files); err != nil {
				errs[i] = err
				return
			}
			for round := 0; round < 20; round++ {
				got, entry, err := CacheGetMaterializedDir(ws, hash)
				if err != nil || entry == nil || got == "" {
					errs[i] = fmt.Errorf("round %d: tree missing after put returned (err=%v)", round, err)
					return
				}
				for rel, want := range files {
					data, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(rel)))
					if err != nil || string(data) != string(want) {
						errs[i] = fmt.Errorf("round %d: %s unreadable after put returned: %v", round, rel, err)
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		assert.NoError(t, err)
	}
}
