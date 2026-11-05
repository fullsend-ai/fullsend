package scaffold

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectPerRepoInstallFiles(t *testing.T) {
	files, err := CollectPerRepoInstallFiles(false, "", "")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	assert.Equal(t, ".github/workflows/fullsend.yaml", files[0].Path)
	assert.NotContains(t, string(files[0].Content), "install_mode",
		"per-repo shim must rely on the reusable-dispatch.yml default instead of passing install_mode")

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}

	// prioritize.yml must be included for per-repo installs so the
	// org-level scheduler can dispatch to it.
	assert.Contains(t, paths, ".github/workflows/prioritize.yml",
		"per-repo install must include prioritize.yml")

	// The installed prioritize caller has no installation-mode compatibility input.
	for _, f := range files {
		if f.Path == ".github/workflows/prioritize.yml" {
			content := string(f.Content)
			assert.NotContains(t, content, "install_mode")
			break
		}
	}
}

func TestCollectPerRepoInstallFiles_BadThinCaller(t *testing.T) {
	orig := perRepoThinCallers
	perRepoThinCallers = []string{".github/workflows/does-not-exist.yml"}
	t.Cleanup(func() { perRepoThinCallers = orig })

	_, err := CollectPerRepoInstallFiles(false, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading per-repo thin caller")
}

func TestPerRepoThinCallerPaths(t *testing.T) {
	paths := PerRepoThinCallerPaths()
	assert.NotEmpty(t, paths)
	assert.Contains(t, paths, ".github/workflows/prioritize.yml")

	// Verify the returned slice is a copy, not the original.
	paths[0] = "mutated"
	fresh := PerRepoThinCallerPaths()
	assert.Equal(t, ".github/workflows/prioritize.yml", fresh[0],
		"PerRepoThinCallerPaths must return a copy to prevent mutation of the internal registry")
}

func TestPerRepoThinCallersAreValidStageWorkflows(t *testing.T) {
	// Cross-validate that every entry in perRepoThinCallers is a
	// recognised thin stage workflow, so the two registries stay in sync.
	for _, path := range perRepoThinCallers {
		assert.True(t, isThinStageCaller(path),
			"perRepoThinCallers entry %q is not in thinStageWorkflows — add it to render.go or remove it from installfiles.go", path)
	}
}

func TestCollectPerRepoInstallFiles_Vendored(t *testing.T) {
	files, err := CollectPerRepoInstallFiles(true, "", "")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	assert.Contains(t, string(files[0].Content), "reusable-")
}

func TestNoCustomizedDirsInInstallFiles(t *testing.T) {
	prFiles, err := CollectPerRepoInstallFiles(false, "", "")
	require.NoError(t, err)
	for _, f := range prFiles {
		assert.False(t, strings.Contains(f.Path, "customized/"),
			"per-repo install files should not include deprecated customized/ paths, got: %s", f.Path)
	}
}
