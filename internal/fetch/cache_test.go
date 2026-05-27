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
