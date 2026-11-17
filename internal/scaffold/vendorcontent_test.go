package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
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
		if isVendoredReusableWorkflow(f.Path) {
			assert.True(t, strings.HasPrefix(f.Path, ".github/workflows/"), "reusable workflows must be under .github/workflows/ for GitHub Actions: %s", f.Path)
			assert.False(t, strings.HasPrefix(f.Path, ".fullsend/"), "reusable workflows must not use .fullsend/ prefix: %s", f.Path)
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
	assert.True(t, isVendoredReusableWorkflow(".github/workflows/reusable-prioritize.yml"))
	assert.True(t, isVendoredReusableWorkflow(".github/workflows/reusable-poll-jira.yml"))
	assert.False(t, isVendoredReusableWorkflow(".github/workflows/triage.yml"))
	assert.False(t, isVendoredReusableWorkflow("action.yml"))
}

// Vendored installers source sibling helpers from their own directory; a
// helper missing from the allowlist would fail at runtime in consumer repos
// ("No such file or directory" under set -e).
func TestVendoredInstallersShipSourcedHelpers(t *testing.T) {
	root, err := moduleRootFromScaffold()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	sourceRe := regexp.MustCompile(`(?m)^\s*(?:source|\.)\s+"\$\{SCRIPT_DIR\}/([^"]+)"`)
	for _, installer := range []string{"install-podman.sh", "install-openshell.sh"} {
		installerPath := ".github/scripts/" + installer
		require.True(t, isVendoredDefaultsInfra(installerPath), installer)

		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(installerPath)))
		require.NoError(t, err)

		matches := sourceRe.FindAllStringSubmatch(string(data), -1)
		require.NotEmpty(t, matches, "%s should source retry-with-backoff.sh", installer)
		var sourcesRetry bool
		for _, m := range matches {
			helper := ".github/scripts/" + m[1]
			assert.True(t, isVendoredDefaultsInfra(helper), "%s sources %s, which must be vendored", installer, helper)
			if m[1] == "retry-with-backoff.sh" {
				sourcesRetry = true
			}
		}
		assert.True(t, sourcesRetry, "%s should source retry-with-backoff.sh", installer)
	}
}

func TestIsVendoredDefaultsInfra(t *testing.T) {
	assert.True(t, isVendoredDefaultsInfra("action.yml"))
	// Actions ship only via the explicit allowlist — an action that no
	// vendored reusable workflow executes must NOT ship.
	assert.True(t, isVendoredDefaultsInfra(".github/actions/mint-token/action.yml"))
	assert.True(t, isVendoredDefaultsInfra(".github/actions/setup-gcp/action.yml"))
	assert.False(t, isVendoredDefaultsInfra(".github/actions/check-e2e-authorization/action.yml"))
	assert.False(t, isVendoredDefaultsInfra(".github/actions/foo/action.yml"))
	// Scripts ship only via the explicit allowlist — an arbitrary file
	// under .github/scripts/ must NOT be vendored to consumer repos.
	assert.True(t, isVendoredDefaultsInfra(".github/scripts/check-fix-eligibility.sh"))
	assert.True(t, isVendoredDefaultsInfra(".github/scripts/count-fix-commits.jq"))
	assert.True(t, isVendoredDefaultsInfra(".github/scripts/install-podman.sh"))
	assert.True(t, isVendoredDefaultsInfra(".github/scripts/pre-fetch-review-body.sh"))
	// The installers source retry-with-backoff.sh, so it must ship with them.
	assert.True(t, isVendoredDefaultsInfra(".github/scripts/install-openshell.sh"))
	assert.True(t, isVendoredDefaultsInfra(".github/scripts/retry-with-backoff.sh"))
	assert.False(t, isVendoredDefaultsInfra(".github/scripts/retry-with-backoff-test.sh"))
	assert.False(t, isVendoredDefaultsInfra(".github/scripts/run.sh"))
	assert.False(t, isVendoredDefaultsInfra(".github/scripts/check-fix-eligibility-test.sh"))
	assert.False(t, isVendoredDefaultsInfra(".github/scripts/count-fix-commits-test.sh"))
	assert.False(t, isVendoredDefaultsInfra(".github/scripts/pre-fetch-review-body-test.sh"))
	assert.False(t, isVendoredDefaultsInfra(".github/workflows/reusable-prioritize.yml"))
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

// The layered scripts layer ships to consumer repos; its *-test.sh /
// *-test.py self-tests run in fullsend CI only and must stay out.
func TestWalkLayeredContent_ExcludesTestFiles(t *testing.T) {
	var paths []string
	require.NoError(t, WalkLayeredContent(func(path string, _ []byte) error {
		paths = append(paths, path)
		return nil
	}))
	assert.Contains(t, paths, "scripts/pre-fetch-prior-review.sh")
	assert.Contains(t, paths, "scripts/resolve-precommit-tools.py")
	for _, p := range paths {
		assert.False(t, isLayeredRepoTestFile(p), "test file shipped in layered content: %s", p)
	}
}
