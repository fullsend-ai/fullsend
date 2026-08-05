package scaffold

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectInstallFiles_PerOrg(t *testing.T) {
	files, err := CollectInstallFiles(CollectInstallFilesOptions{
		RenderOptions: RenderOptionsForInstall(false, false, "", ""),
	})
	require.NoError(t, err)
	require.NotEmpty(t, files)

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	assert.Contains(t, paths, ".github/workflows/triage.yml")
}

func TestCollectInstallFiles_PerRepoPrefix(t *testing.T) {
	files, err := CollectInstallFiles(CollectInstallFilesOptions{
		RenderOptions: RenderOptionsForInstall(false, true, "", ""),
		PathPrefix:    ".fullsend/",
	})
	require.NoError(t, err)
	require.NotEmpty(t, files)

	found := false
	for _, f := range files {
		if f.Path == ".fullsend/.github/workflows/triage.yml" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected per-repo prefixed triage workflow")
}

func TestCollectPerRepoInstallFiles(t *testing.T) {
	files, err := CollectPerRepoInstallFiles(false, "", "")
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, ".github/workflows/fullsend.yaml", files[0].Path)
	assert.Equal(t, ".github/scripts/stop-agent.sh", files[1].Path)
	assert.Equal(t, "100755", files[1].Mode)
}

func TestManagedPaths(t *testing.T) {
	paths, err := ManagedPaths(false, "")
	require.NoError(t, err)
	assert.Contains(t, paths, ".github/workflows/triage.yml")
}

func TestCollectInstallFiles_Vendored(t *testing.T) {
	files, err := CollectInstallFiles(CollectInstallFilesOptions{
		RenderOptions: RenderOptionsForInstall(true, false, "", ""),
	})
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var triage string
	for _, f := range files {
		if f.Path == ".github/workflows/triage.yml" {
			triage = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, triage)
	assert.NotContains(t, triage, "__UPSTREAM_REF__")
}

func TestCollectPerRepoInstallFiles_Vendored(t *testing.T) {
	files, err := CollectPerRepoInstallFiles(true, "", "")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	assert.Contains(t, string(files[0].Content), "reusable-")
}

func TestNoCustomizedDirsInInstallFiles(t *testing.T) {
	files, err := CollectInstallFiles(CollectInstallFilesOptions{})
	require.NoError(t, err)
	for _, f := range files {
		assert.False(t, strings.Contains(f.Path, "customized/"),
			"install files should not include deprecated customized/ paths, got: %s", f.Path)
	}

	prFiles, err := CollectPerRepoInstallFiles(false, "", "")
	require.NoError(t, err)
	for _, f := range prFiles {
		assert.False(t, strings.Contains(f.Path, "customized/"),
			"per-repo install files should not include deprecated customized/ paths, got: %s", f.Path)
	}
}
