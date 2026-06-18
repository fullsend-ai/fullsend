package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectVendoredAssets_FromCheckout(t *testing.T) {
	root, err := moduleRootFromScaffold()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	files, err := CollectVendoredAssets(root, "")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var hasReusable, hasDefaults bool
	for _, f := range files {
		if strings.HasPrefix(f.Path, ".github/workflows/reusable-") {
			hasReusable = true
		}
		if strings.HasPrefix(f.Path, ".defaults/") {
			hasDefaults = true
		}
	}
	assert.True(t, hasReusable, "expected reusable workflow files")
	assert.True(t, hasDefaults, "expected .defaults/ files")
}

func TestCollectVendoredAssets_PerRepoPrefix(t *testing.T) {
	root, err := moduleRootFromScaffold()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	files, err := CollectVendoredAssets(root, ".fullsend/")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.HasPrefix(f.Path, ".github/workflows/") {
			assert.True(t, strings.HasPrefix(f.Path, ".fullsend/.github/workflows/"), "workflows should use per-repo prefix: %s", f.Path)
		}
	}
}

func TestCollectVendoredAssets_InvalidRoot(t *testing.T) {
	dir := t.TempDir()
	_, err := CollectVendoredAssets(dir, "")
	require.Error(t, err)
}

func TestVendoredInfraFileMode(t *testing.T) {
	assert.Equal(t, "100755", vendoredInfraFileMode(".github/scripts/prepare-agent-workspace.sh"))
	assert.Equal(t, "100644", vendoredInfraFileMode("action.yml"))
}

func TestIsVendoredReusableWorkflow(t *testing.T) {
	assert.True(t, isVendoredReusableWorkflow(".github/workflows/reusable-triage.yml"))
	assert.False(t, isVendoredReusableWorkflow(".github/workflows/triage.yml"))
	assert.False(t, isVendoredReusableWorkflow("action.yml"))
}

func TestIsVendoredDefaultsInfra(t *testing.T) {
	assert.True(t, isVendoredDefaultsInfra("action.yml"))
	assert.True(t, isVendoredDefaultsInfra(".github/actions/foo/action.yml"))
	assert.True(t, isVendoredDefaultsInfra(".github/scripts/run.sh"))
	assert.False(t, isVendoredDefaultsInfra(".github/workflows/reusable-triage.yml"))
}

func TestWalkVendoredUpstreamFromRoot_SkipsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("ok"), 0o644))
	link := filepath.Join(root, "action.yml")
	require.NoError(t, os.Symlink(target, link))

	var seen []string
	err := walkVendoredUpstreamFromRoot(root, func(path string, _ []byte) error {
		seen = append(seen, path)
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, seen, "symlinks should be skipped")
}
