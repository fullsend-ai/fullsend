//go:build unix

package repos

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A newly created manifest honors the process umask, as os.WriteFile did.
func TestSetManifestValue_NewManifestHonorsUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	path := filepath.Join(t.TempDir(), "repos.yaml")
	changed, err := SetManifestValue(path, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)
	assert.True(t, changed)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// Replacing an existing manifest keeps its permission bits regardless of umask.
func TestSetManifestValue_ExistingManifestKeepsMode(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	path := writeSetManifest(t, "version: 1\n")
	require.NoError(t, os.Chmod(path, 0o644))
	_, err := SetManifestValue(path, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

// The temp file is private while the manifest is written, whatever the umask.
func TestCreateManifestTemp_IsPrivate(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	f, err := createManifestTemp(t.TempDir(), 0o600)
	require.NoError(t, err)
	defer f.Close()
	info, err := f.Stat()
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// Under umask 022 a restrictive manifest stays restrictive and a new one is
// created 0644, with no probe or temp file left behind.
func TestSetManifestValue_RestrictiveManifestUnderUmask022(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	path := writeSetManifest(t, "version: 1\n")
	require.NoError(t, os.Chmod(path, 0o600))
	_, err := SetManifestValue(path, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dir := t.TempDir()
	fresh := filepath.Join(dir, "repos.yaml")
	_, err = SetManifestValue(fresh, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)
	info, err = os.Stat(fresh)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}
