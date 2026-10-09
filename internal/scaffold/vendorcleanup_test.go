package scaffold

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const testVendorManifest = `version: "1"
binary_path: .fullsend/bin/fullsend
paths:
  - .fullsend/.defaults/action.yml
  - .fullsend/.defaults/agents/code.md
  - .github/workflows/reusable-dispatch.yml
`

func putRepoFile(fc *forge.FakeClient, path, content string) {
	fc.FileContents["acme/api/"+path] = []byte(content)
}

func pendingCleanup(t *testing.T, fc *forge.FakeClient) ([]string, error) {
	t.Helper()
	return PendingVendoredCleanupPaths(context.Background(), fc, "acme", "api", PerRepoVendorPrefix, PerRepoVendoredBinaryPath)
}

func TestPendingVendoredCleanupPaths_NothingVendored(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/config.yaml", "roles: []")
	putRepoFile(fc, ".github/workflows/reusable-dispatch.yml", "user workflow")

	paths, err := pendingCleanup(t, fc)
	require.NoError(t, err)
	assert.Empty(t, paths, "no manifest and no binary must claim nothing")
}

func TestPendingVendoredCleanupPaths_ManifestOwnsOnlyRecordedPaths(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", testVendorManifest)
	putRepoFile(fc, ".fullsend/bin/fullsend", "ELF")
	putRepoFile(fc, ".fullsend/.defaults/action.yml", "a")
	putRepoFile(fc, ".github/workflows/reusable-dispatch.yml", "w")
	// Recorded in the manifest but already gone: not reported.
	// Not recorded in the manifest: user files are kept.
	putRepoFile(fc, ".fullsend/config.yaml", "roles: []")
	putRepoFile(fc, ".fullsend/.defaults/user-notes.md", "mine")

	paths, err := pendingCleanup(t, fc)
	require.NoError(t, err)
	assert.Equal(t, []string{
		".fullsend/.defaults/action.yml",
		".fullsend/bin/fullsend",
		".fullsend/vendor-manifest.yaml",
		".github/workflows/reusable-dispatch.yml",
	}, paths)
}

func TestPendingVendoredCleanupPaths_ManifestOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", testVendorManifest)

	paths, err := pendingCleanup(t, fc)
	require.NoError(t, err)
	assert.Equal(t, []string{".fullsend/vendor-manifest.yaml"}, paths)
}

func TestPendingVendoredCleanupPaths_BinaryOnlyUsesLegacyLayout(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/bin/fullsend", "ELF")
	putRepoFile(fc, ".github/workflows/reusable-dispatch.yml", "w")
	putRepoFile(fc, ".fullsend/config.yaml", "roles: []")

	paths, err := pendingCleanup(t, fc)
	require.NoError(t, err)
	assert.Equal(t, []string{
		".fullsend/bin/fullsend",
		".github/workflows/reusable-dispatch.yml",
	}, paths)
}

func TestPendingVendoredCleanupPaths_TruncatedTreeFallsBack(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepositoryFiles"] = fmt.Errorf("too large: %w", forge.ErrTreeTruncated)
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", testVendorManifest)

	paths, err := pendingCleanup(t, fc)
	require.NoError(t, err)
	// Without a listing, every owned path is returned; absent paths are
	// skipped when the deletion is committed.
	assert.Equal(t, []string{
		".fullsend/.defaults/action.yml",
		".fullsend/.defaults/agents/code.md",
		".fullsend/bin/fullsend",
		".fullsend/vendor-manifest.yaml",
		".github/workflows/reusable-dispatch.yml",
	}, paths)
}

func TestPendingVendoredCleanupPaths_TruncatedTreeBinaryOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepositoryFiles"] = fmt.Errorf("too large: %w", forge.ErrTreeTruncated)
	putRepoFile(fc, ".fullsend/bin/fullsend", "ELF")

	paths, err := pendingCleanup(t, fc)
	require.NoError(t, err)
	assert.Contains(t, paths, ".fullsend/bin/fullsend")
}

func TestPendingVendoredCleanupPaths_TruncatedTreeNothingVendored(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepositoryFiles"] = fmt.Errorf("too large: %w", forge.ErrTreeTruncated)

	paths, err := pendingCleanup(t, fc)
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestPendingVendoredCleanupPaths_TruncatedTreeReadError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepositoryFiles"] = fmt.Errorf("too large: %w", forge.ErrTreeTruncated)
	fc.Errors["GetFileContent"] = errors.New("boom")

	_, err := pendingCleanup(t, fc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking vendor manifest")
}

func TestPendingVendoredCleanupPaths_ListError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepositoryFiles"] = errors.New("boom")

	_, err := pendingCleanup(t, fc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing repository files")
}

func TestPendingVendoredCleanupPaths_InvalidManifest(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", "version: \"9\"\n")

	_, err := pendingCleanup(t, fc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving vendored cleanup paths")
}
