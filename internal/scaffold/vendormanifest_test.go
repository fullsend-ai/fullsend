package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestVendorManifestRoundTrip(t *testing.T) {
	m := NewVendorManifest("0.4.0", "/src/fullsend", "bin/fullsend", []string{
		".defaults/action.yml",
		".github/workflows/reusable-triage.yml",
	})
	data, err := m.MarshalYAML()
	require.NoError(t, err)

	parsed, err := ParseVendorManifest(data)
	require.NoError(t, err)
	assert.Equal(t, vendorManifestVersion, parsed.Version)
	assert.Equal(t, "0.4.0", parsed.CLIVersion)
	assert.Equal(t, "/src/fullsend", parsed.SourceRef)
	assert.Equal(t, "bin/fullsend", parsed.BinaryPath)
	assert.Equal(t, m.Paths, parsed.Paths)
}

func TestVendorManifestCleanupPaths(t *testing.T) {
	m := NewVendorManifest("dev", "", "bin/fullsend", []string{".defaults/action.yml"})
	paths := m.CleanupPaths("")
	assert.Contains(t, paths, "bin/fullsend")
	assert.Contains(t, paths, ".defaults/action.yml")
	assert.Contains(t, paths, "vendor-manifest.yaml")
}

func TestEnumerateVendoredPathsWithoutCheckout(t *testing.T) {
	paths, err := enumerateVendoredPaths("")
	require.NoError(t, err)
	assert.Contains(t, paths, ".defaults/action.yml")
	assert.Contains(t, paths, ".github/workflows/reusable-triage.yml")
	assert.Contains(t, paths, ".defaults/internal/scaffold/fullsend-repo/agents/triage.md")
}

func TestEnumerateVendoredPathsMatchesCollectInCheckout(t *testing.T) {
	root, err := moduleRootFromScaffold()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	embedPaths, err := enumerateVendoredPaths("")
	require.NoError(t, err)

	files, err := CollectVendoredAssets(root, "")
	require.NoError(t, err)
	collectPaths := PathsFromInstallFiles(files)

	assert.Equal(t, embedPaths, collectPaths)
}

func TestResolveVendoredCleanupPathsUsesManifest(t *testing.T) {
	m := NewVendorManifest("dev", "", "bin/fullsend", []string{".defaults/action.yml"})
	data, err := m.MarshalYAML()
	require.NoError(t, err)

	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"org/.fullsend/vendor-manifest.yaml": data,
		},
	}

	paths, err := ResolveVendoredCleanupPaths(context.Background(), client, "org", ".fullsend", "", "bin/fullsend")
	require.NoError(t, err)
	assert.Contains(t, paths, ".defaults/action.yml")
	assert.Contains(t, paths, "vendor-manifest.yaml")
}

func TestResolveVendoredCleanupPathsEmbedFallback(t *testing.T) {
	client := &forge.FakeClient{FileContents: map[string][]byte{}}
	paths, err := ResolveVendoredCleanupPaths(context.Background(), client, "org", ".fullsend", "", "bin/fullsend")
	require.NoError(t, err)
	assert.Contains(t, paths, "bin/fullsend")
	assert.Contains(t, paths, ".defaults/action.yml")
}

func TestVendoredReusableWorkflowsMatchRepo(t *testing.T) {
	root, err := moduleRootFromScaffold()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	workflowDir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	require.NoError(t, err)

	onDisk := map[string]struct{}{}
	for _, e := range entries {
		name := e.Name()
		if isVendoredReusableWorkflow(".github/workflows/" + name) {
			onDisk[name] = struct{}{}
		}
	}

	assert.Len(t, onDisk, len(vendoredReusableWorkflows))
	for _, name := range vendoredReusableWorkflows {
		assert.Contains(t, onDisk, name)
	}
}

func TestCollectVendoredAssetsUsesDefaultsMirror(t *testing.T) {
	root, err := moduleRootFromScaffold()
	require.NoError(t, err)

	files, err := CollectVendoredAssets(root, "")
	require.NoError(t, err)

	paths := PathsFromInstallFiles(files)
	assert.Contains(t, paths, ".defaults/action.yml")
	assert.Contains(t, paths, ".defaults/.github/actions/mint-token/action.yml")
	assert.Contains(t, paths, ".defaults/internal/scaffold/fullsend-repo/agents/triage.md")
	assert.Contains(t, paths, ".github/workflows/reusable-triage.yml")
	assert.NotContains(t, paths, "action.yml")
	assert.NotContains(t, paths, "agents/triage.md")
}

func TestVendoredMarkerPath(t *testing.T) {
	assert.Equal(t, ".defaults/action.yml", VendoredMarkerPath())
}
