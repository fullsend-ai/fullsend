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
	require.NotEmpty(t, files)
	assert.Equal(t, ".github/workflows/fullsend.yaml", files[0].Path)

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}

	// prioritize.yml must be included for per-repo installs so the
	// org-level scheduler can dispatch to it.
	assert.Contains(t, paths, ".github/workflows/prioritize.yml",
		"per-repo install must include prioritize.yml")

	// Verify the installed prioritize.yml uses per-repo install mode.
	for _, f := range files {
		if f.Path == ".github/workflows/prioritize.yml" {
			content := string(f.Content)
			assert.Contains(t, content, "install_mode: per-repo",
				"per-repo prioritize.yml must use install_mode: per-repo")
			assert.NotContains(t, content, "install_mode: per-org",
				"per-repo prioritize.yml must not use install_mode: per-org")
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
