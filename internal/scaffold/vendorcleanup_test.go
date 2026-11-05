package scaffold

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func putRepoFile(fc *forge.FakeClient, path, content string) {
	fc.FileContents["acme/api/"+path] = []byte(content)
}

func TestFindStaleVendoredAssets_NoMarkers(t *testing.T) {
	fc := forge.NewFakeClient()
	// Vendor-like user files without the manifest or binary are not claimed.
	putRepoFile(fc, ".defaults/action.yml", "name: mine")
	putRepoFile(fc, ".github/workflows/reusable-dispatch.yml", "name: mine")

	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Empty(t, got.Paths)
}

func TestFindStaleVendoredAssets_ManifestOwnershipIsLimited(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", `version: "1"
binary_path: .fullsend/bin/fullsend
paths:
  - .defaults/action.yml
  - .defaults/.github/scripts/check-agents-gate-pin.sh
  - .defaults/internal/scaffold/fullsend-repo/agents/code.md
  - .fullsend/.defaults/internal/scaffold/fullsend-repo/skills/pr-review/SKILL.md
  - .defaults/.github/scripts/my-script.sh
  - .defaults/internal/scaffold/fullsend-repo/agents/my-agent.md
  - .fullsend/.defaults/.github/actions/my-action/action.yml
  - .fullsend/.defaults/internal/scaffold/fullsend-repo/scripts/my-helper.sh
  - .github/workflows/reusable-dispatch.yml
  - .github/workflows/reusable-custom.yml
  - .fullsend/config.yaml
  - .fullsend/config.base.yaml
  - .github/actions/custom/action.yml
  - action.yml
  - .defaults/.github/scripts/listed-but-absent.sh
  - .defaults/user-notes.md
  - .fullsend/.defaults/user-notes.md
`)
	for _, p := range []string{
		".fullsend/bin/fullsend",
		".defaults/action.yml",
		".defaults/.github/scripts/check-agents-gate-pin.sh",
		".defaults/internal/scaffold/fullsend-repo/agents/code.md",
		".fullsend/.defaults/internal/scaffold/fullsend-repo/skills/pr-review/SKILL.md",
		".defaults/.github/scripts/my-script.sh",
		".defaults/internal/scaffold/fullsend-repo/agents/my-agent.md",
		".fullsend/.defaults/.github/actions/my-action/action.yml",
		".fullsend/.defaults/internal/scaffold/fullsend-repo/scripts/my-helper.sh",
		".github/workflows/reusable-dispatch.yml",
		".github/workflows/reusable-custom.yml",
		".fullsend/config.yaml",
		".fullsend/config.base.yaml",
		".github/actions/custom/action.yml",
		"action.yml",
		".defaults/user-notes.md",
		".fullsend/.defaults/user-notes.md",
	} {
		putRepoFile(fc, p, "x")
	}

	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, []string{
		".defaults/.github/scripts/check-agents-gate-pin.sh",
		".defaults/action.yml",
		".defaults/internal/scaffold/fullsend-repo/agents/code.md",
		".fullsend/.defaults/internal/scaffold/fullsend-repo/skills/pr-review/SKILL.md",
		".fullsend/bin/fullsend",
		".fullsend/vendor-manifest.yaml",
		".github/workflows/reusable-dispatch.yml",
	}, got.Paths)
	assert.Equal(t, 12, got.Ignored, "custom workflow, both config layers, custom action, root action.yml, an unknown listed path and user-created files under both mirrors are kept")
	for _, kept := range []string{
		".defaults/.github/scripts/my-script.sh",
		".defaults/internal/scaffold/fullsend-repo/agents/my-agent.md",
		".fullsend/.defaults/.github/actions/my-action/action.yml",
		".fullsend/.defaults/internal/scaffold/fullsend-repo/scripts/my-helper.sh",
	} {
		assert.NotContains(t, got.Paths, kept)
	}
}

func TestFindStaleVendoredAssets_ManifestOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", "version: \"1\"\nbinary_path: .fullsend/bin/fullsend\npaths:\n  - .defaults/action.yml\n")
	putRepoFile(fc, ".defaults/action.yml", "x")

	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, []string{".defaults/action.yml", ".fullsend/vendor-manifest.yaml"}, got.Paths)
}

func TestFindStaleVendoredAssets_BinaryOnlyIsConservative(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/bin/fullsend", "ELF")
	putRepoFile(fc, ".defaults/action.yml", "x")
	putRepoFile(fc, ".github/workflows/reusable-dispatch.yml", "x")
	putRepoFile(fc, ".github/workflows/reusable-triage.yml", "x")
	// Un-prefixed paths a legacy layout used must not be claimed on the
	// strength of a leftover binary.
	putRepoFile(fc, "action.yml", "x")
	putRepoFile(fc, ".github/actions/mint-token/action.yml", "x")
	putRepoFile(fc, ".github/scripts/install-podman.sh", "x")
	putRepoFile(fc, ".fullsend/config.yaml", "x")

	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, []string{
		".defaults/action.yml",
		".fullsend/bin/fullsend",
		".github/workflows/reusable-dispatch.yml",
		".github/workflows/reusable-triage.yml",
	}, got.Paths)
	assert.Zero(t, got.Ignored)
}

func TestFindStaleVendoredAssets_MalformedManifestFails(t *testing.T) {
	for name, body := range map[string]string{
		"bad version":    "version: \"9\"\nbinary_path: .fullsend/bin/fullsend\n",
		"not yaml":       "version: [\n",
		"traversal":      "version: \"1\"\nbinary_path: .fullsend/bin/fullsend\npaths:\n  - ../etc/passwd\n",
		"outside layout": "version: \"1\"\nbinary_path: .fullsend/bin/fullsend\npaths:\n  - src/main.go\n",
	} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			putRepoFile(fc, ".fullsend/vendor-manifest.yaml", body)
			_, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be trusted")
		})
	}
}

func TestFindStaleVendoredAssets_ControlCharactersAreNotOwnedOrDisplayed(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", "version: \"1\"\nbinary_path: .fullsend/bin/fullsend\npaths:\n  - \".defaults/evil\\x1b[31m.yml\"\n")
	putRepoFile(fc, ".defaults/evil\x1b[31m.yml", "x")

	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, []string{".fullsend/vendor-manifest.yaml"}, got.Paths)
	assert.Equal(t, 1, got.Ignored)
}

func TestFindStaleVendoredAssets_ReadErrors(t *testing.T) {
	boom := errors.New("boom")

	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/bin/fullsend", "ELF")
	fc.Errors = map[string]error{"ListRepositoryFiles": boom}
	_, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.ErrorIs(t, err, boom)

	fc = forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/vendor-manifest.yaml", "version: \"1\"\n")
	fc.GetFileContentErrors = map[string]error{"acme/api/.fullsend/vendor-manifest.yaml": boom}
	_, err = FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.ErrorIs(t, err, boom)
}

func TestFindStaleVendoredAssets_EmptyRepositoryDoesNotListTree(t *testing.T) {
	// A repository with no commits cannot list its tree; with no vendoring
	// markers the tree is never listed, so that failure is not surfaced.
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"ListRepositoryFiles": errors.New("no default branch")}
	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Empty(t, got.Paths)
	assert.Empty(t, got.RepoFiles)
}

func TestFindStaleVendoredAssets_TruncatedTree(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"ListRepositoryFiles": forge.ErrTreeTruncated}
	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err, "no markers means nothing to verify")
	assert.Empty(t, got.Paths)

	putRepoFile(fc, ".fullsend/bin/fullsend", "ELF")
	_, err = FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.Error(t, err, "markers in an unlistable tree fail safely")
	assert.Contains(t, err.Error(), "too large")

	fc.GetFileContentErrors = map[string]error{"acme/api/.fullsend/vendor-manifest.yaml": errors.New("boom")}
	_, err = FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.Error(t, err)
}

func TestIsFullsendOwnedVendoredPath(t *testing.T) {
	owned := []string{
		".fullsend/bin/fullsend",
		".fullsend/vendor-manifest.yaml",
		".defaults/action.yml",
		".fullsend/.defaults/action.yml",
		".defaults/.github/actions/mint-token/action.yml",
		".defaults/.github/scripts/install-podman.sh",
		".defaults/.github/scripts/check-agents-gate-pin.sh",
		".defaults/internal/scaffold/fullsend-repo/agents/code.md",
		".defaults/internal/scaffold/fullsend-repo/scripts/pre-fetch-prior-review.sh",
		".fullsend/.defaults/internal/scaffold/fullsend-repo/scripts/pre-fetch-prior-review.sh",
		".github/workflows/reusable-prioritize.yml",
		".github/workflows/reusable-code.yml",
		".fullsend/.github/workflows/reusable-dispatch.yml",
	}
	notOwned := []string{
		"",
		".defaults/",
		".defaults",
		"./.defaults/action.yml",
		".defaults/../action.yml",
		"/.defaults/action.yml",
		".defaults\\x",
		".defaults/a\tb",
		".defaults/user-notes.md",
		".fullsend/.defaults/user-notes.md",
		".defaults/.github/workflows/custom.yml",
		".defaults/.github/scripts/",
		".defaults/.github/scripts/my-script.sh",
		".defaults/.github/actions/my-action/action.yml",
		".defaults/internal/scaffold/fullsend-repo/agents/my-agent.md",
		".fullsend/.defaults/internal/scaffold/fullsend-repo/scripts/my-helper.sh",
		".github/workflows/reusable-custom.yml",
		".github/workflows/sub/reusable-dispatch.yml",
		".fullsend/config.yaml",
		".fullsend/bin/other",
		"action.yml",
		"vendor-manifest.yaml",
	}
	for _, p := range owned {
		assert.True(t, isFullsendOwnedVendoredPath(p), p)
	}
	for _, p := range notOwned {
		assert.False(t, isFullsendOwnedVendoredPath(p), p)
	}
}

func TestLocalReusableWorkflowTargets(t *testing.T) {
	got := LocalReusableWorkflowTargets([]string{
		".defaults/action.yml",
		".fullsend/.github/workflows/reusable-dispatch.yml",
		".github/workflows/reusable-dispatch.yml",
	})
	assert.Equal(t, []string{"./.github/workflows/reusable-dispatch.yml"}, got)
}

func TestSanitizeDisplayText(t *testing.T) {
	assert.Equal(t, "a b?[31m?", SanitizeDisplayText("a b\x1b[31m\n"))
	assert.Equal(t, "plain/path.yml", SanitizeDisplayText("plain/path.yml"))
}

func TestFindStaleVendoredAssets_BinaryOnlyCoversEmbeddedLayout(t *testing.T) {
	fc := forge.NewFakeClient()
	putRepoFile(fc, ".fullsend/bin/fullsend", "ELF")
	layout, err := enumerateVendoredPaths()
	require.NoError(t, err)
	for _, p := range layout {
		putRepoFile(fc, p, "x")
	}
	got, err := FindStaleVendoredAssets(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	for _, p := range layout {
		assert.True(t, slices.Contains(got.Paths, p), p)
	}
}
