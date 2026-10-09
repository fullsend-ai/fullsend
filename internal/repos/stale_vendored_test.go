package repos

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const staleVendorManifestYAML = `version: "1"
binary_path: .fullsend/bin/fullsend
paths:
  - .fullsend/.defaults/action.yml
  - .github/workflows/reusable-dispatch.yml
`

// staleVendoredOwned lists the files a previous vendored install left in
// the fixture repo that cleanup owns, in sorted order.
var staleVendoredOwned = []string{
	".fullsend/.defaults/action.yml",
	".fullsend/bin/fullsend",
	".fullsend/vendor-manifest.yaml",
	".github/workflows/reusable-dispatch.yml",
}

// staleVendoredUserFile is a file under .fullsend the manifest does not
// record; cleanup must never touch it.
const staleVendoredUserFile = ".fullsend/.defaults/user-notes.md"

func putStaleVendoredAssets(fc *forge.FakeClient, owner, repo string) {
	full := owner + "/" + repo + "/"
	fc.FileContents[full+".fullsend/vendor-manifest.yaml"] = []byte(staleVendorManifestYAML)
	fc.FileContents[full+".fullsend/bin/fullsend"] = []byte("ELF")
	fc.FileContents[full+".fullsend/.defaults/action.yml"] = []byte("name: action")
	fc.FileContents[full+".github/workflows/reusable-dispatch.yml"] = []byte("name: dispatch")
	fc.FileContents[full+staleVendoredUserFile] = []byte("mine")
}

func deletedPaths(files []forge.TreeFile) []string {
	var out []string
	for _, f := range files {
		if f.Delete {
			out = append(out, f.Path)
		}
	}
	slices.Sort(out)
	return out
}

// applyTreeFiles lands committed files in the fake repo, as a merged
// scaffold commit would.
func applyTreeFiles(fc *forge.FakeClient, owner, repo string, files []forge.TreeFile) {
	for _, f := range files {
		key := owner + "/" + repo + "/" + f.Path
		if f.Delete {
			delete(fc.FileContents, key)
			continue
		}
		fc.FileContents[key] = f.Content
	}
}

func findAction(actions []ComponentAction, component string) (ComponentAction, bool) {
	for _, a := range actions {
		if a.Component == component {
			return a, true
		}
	}
	return ComponentAction{}, false
}

func newStaleVendoredInstalledRepo(t *testing.T) *forge.FakeClient {
	t.Helper()
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	putStaleVendoredAssets(fc, "acme", "api")
	return fc
}

func TestConverge_RemovesStaleVendoredAssetsWhenVendorOff(t *testing.T) {
	fc := newStaleVendoredInstalledRepo(t)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, result.Converged(), 1)

	assert.Equal(t, staleVendoredOwned, deletedPaths(spy.files), "only manifest-owned vendored paths are deleted, in the scaffold commit")
	action, ok := findAction(result.Results[0].Actions, staleVendoredComponent)
	require.True(t, ok, "expected a %s action", staleVendoredComponent)
	assert.Equal(t, "delete", action.Action)
	assert.Contains(t, action.Detail, "4 stale vendored file(s)")

	// Once the commit lands, a second run has nothing left to do.
	applyTreeFiles(fc, "acme", "api", spy.files)
	assert.Contains(t, fc.FileContents, "acme/api/"+staleVendoredUserFile, "user file must be preserved")

	again := &spyScaffoldCommit{}
	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), again.fn(), noopProgress)
	require.NoError(t, err)
	assert.Len(t, result.AlreadyCurrent(), 1)
	assert.Empty(t, again.files, "repeat run must not commit")
}

func TestConverge_StaleVendoredAssetsDryRun(t *testing.T) {
	fc := newStaleVendoredInstalledRepo(t)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
	cfg.DryRun = true
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, spy.files, "dry run must not commit")
	assert.Len(t, result.Converged(), 1, "pending removal is planned work")

	action, ok := findAction(result.Results[0].Actions, staleVendoredComponent)
	require.True(t, ok)
	assert.Equal(t, "delete", action.Action)
	assert.True(t, strings.HasPrefix(action.Detail, "would remove 4 stale vendored file(s)"), action.Detail)
	for _, p := range staleVendoredOwned {
		assert.Contains(t, action.Detail, p)
		assert.Contains(t, fc.FileContents, "acme/api/"+p, "dry run must not delete %s", p)
	}
}

func TestConverge_StaleVendoredManifestInvalidFails(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/.fullsend/vendor-manifest.yaml"] = []byte("version: \"9\"\n")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Failed()[0].Error.Error(), "checking stale vendored assets")
	assert.Empty(t, spy.files)
}

func TestConverge_FreshInstallRemovesStaleVendoredAssets(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	putStaleVendoredAssets(fc, "acme", "api")
	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, result.Installed(), 1)

	assert.Equal(t, staleVendoredOwned, deletedPaths(spy.files))
	var wrote bool
	for _, f := range spy.files {
		if f.Path == ".github/workflows/fullsend.yaml" && !f.Delete {
			wrote = true
		}
	}
	assert.True(t, wrote, "deletions ride along with the install commit")
	action, ok := findAction(result.Results[0].Actions, staleVendoredComponent)
	require.True(t, ok)
	assert.Equal(t, "delete", action.Action)
}

func TestConverge_FreshInstallStaleVendoredDryRun(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	putStaleVendoredAssets(fc, "acme", "api")
	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	cfg.DryRun = true
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, spy.files)
	action, ok := findAction(result.Results[0].Actions, staleVendoredComponent)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(action.Detail, "would remove"), action.Detail)
}

func TestConverge_FreshInstallStaleVendoredErrorBlocksInstall(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.fullsend/vendor-manifest.yaml"] = []byte("version: \"9\"\n")
	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Failed()[0].Error.Error(), "checking stale vendored assets")
	assert.Empty(t, spy.files)
}

func TestConvergeStaleVendoredFiles_SkipsVendoredAndGitLab(t *testing.T) {
	fc := forge.NewFakeClient()
	putStaleVendoredAssets(fc, "acme", "api")
	base := ResolvedConfig{
		Owner:       "acme",
		Repo:        "api",
		Forge:       ForgeGitHub,
		ForgeConfig: ForgeConfig{Client: fc},
	}

	vendored := base
	vendored.Vendor = true
	files, actions := convergeStaleVendoredFiles(context.Background(), vendored, ConvergeConfig{}, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)

	off := false
	on := true
	files, actions = convergeStaleVendoredFiles(context.Background(), base, ConvergeConfig{VendorOverride: &on}, noopProgress)
	assert.Empty(t, files, "--vendor override keeps vendored assets")
	assert.Empty(t, actions)

	files, _ = convergeStaleVendoredFiles(context.Background(), vendored, ConvergeConfig{VendorOverride: &off}, noopProgress)
	assert.Equal(t, staleVendoredOwned, deletedPaths(files), "--vendor=false override removes vendored assets")

	gitlab := base
	gitlab.Forge = ForgeGitLab
	files, actions = convergeStaleVendoredFiles(context.Background(), gitlab, ConvergeConfig{}, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)
}

func TestWithoutQueuedPaths(t *testing.T) {
	deletes := []forge.TreeFile{
		{Path: "a", Delete: true},
		{Path: "b", Delete: true},
	}
	queued := []forge.TreeFile{
		{Path: "a", Content: []byte("x")},
		{Path: "b", Delete: true},
	}
	assert.Equal(t, []forge.TreeFile{{Path: "b", Delete: true}}, withoutQueuedPaths(deletes, queued))
	assert.Nil(t, withoutQueuedPaths(nil, queued))
}

func TestStatus_ReportsStaleVendoredAssets(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0", "https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0", "https://mint.example.com", "us-central1")
	putStaleVendoredAssets(fc, "acme-corp", "api-server")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	for _, s := range result.Repos {
		if s.Repo == "api-server" {
			require.Empty(t, s.Error)
			assert.Contains(t, s.Drifts, Drift{
				Field:    staleVendoredComponent,
				Expected: "absent",
				Actual:   "4 stale vendored file(s) pending removal",
			})
		} else {
			assert.Empty(t, s.Drifts)
		}
	}

	// After cleanup lands, status is clean again.
	for _, p := range staleVendoredOwned {
		delete(fc.FileContents, "acme-corp/api-server/"+p)
	}
	result, err = Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Summary.Drifted)
}

func TestStatus_StaleVendoredAssetsIgnoredWhenVendored(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	vendor := true
	m.Defaults.Vendor = &vendor
	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0", "https://mint.example.com", "us-central1")
	putStaleVendoredAssets(fc, "acme-corp", "api-server")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, []string{"acme-corp/api-server"})
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	_, found := findDrift(result.Repos[0].Drifts, staleVendoredComponent)
	assert.False(t, found, "vendored repos keep their vendored assets")
}

func TestStatus_StaleVendoredAssetsOnNotInstalledRepo(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	fc.FileContents["acme-corp/web-frontend/.fullsend/bin/fullsend"] = []byte("ELF")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, []string{"acme-corp/web-frontend"})
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	assert.False(t, result.Repos[0].Installed)
	d, found := findDrift(result.Repos[0].Drifts, staleVendoredComponent)
	require.True(t, found)
	assert.Equal(t, "1 stale vendored file(s) pending removal", d.Actual)
}

func TestStatus_StaleVendoredAssetsError(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0", "https://mint.example.com", "us-central1")
	fc.FileContents["acme-corp/api-server/.fullsend/vendor-manifest.yaml"] = []byte("version: \"9\"\n")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, []string{"acme-corp/api-server"})
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	assert.Contains(t, result.Repos[0].Error, "checking stale vendored assets")
}

func findDrift(drifts []Drift, field string) (Drift, bool) {
	for _, d := range drifts {
		if d.Field == field {
			return d, true
		}
	}
	return Drift{}, false
}
