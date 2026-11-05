package repos

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const staleVendorManifestYAML = `version: "1"
binary_path: .fullsend/bin/fullsend
paths:
  - .defaults/action.yml
  - .github/workflows/reusable-dispatch.yml
  - .fullsend/config.yaml
`

// staleVendoredOwned lists, sorted, the files an earlier vendored install
// left in the fixture repo that cleanup owns.
var staleVendoredOwned = []string{
	".defaults/action.yml",
	".fullsend/bin/fullsend",
	".fullsend/vendor-manifest.yaml",
	".github/workflows/reusable-dispatch.yml",
}

// staleVendoredUserFile sits under .defaults but is not recorded in the
// manifest; cleanup must never touch it.
const staleVendoredUserFile = ".defaults/user-notes.md"

func putStaleVendoredAssets(fc *forge.FakeClient, owner, repo string) {
	full := owner + "/" + repo + "/"
	fc.FileContents[full+".fullsend/vendor-manifest.yaml"] = []byte(staleVendorManifestYAML)
	fc.FileContents[full+".fullsend/bin/fullsend"] = []byte("ELF")
	fc.FileContents[full+".defaults/action.yml"] = []byte("name: action")
	fc.FileContents[full+".github/workflows/reusable-dispatch.yml"] = []byte("name: dispatch")
	fc.FileContents[full+staleVendoredUserFile] = []byte("mine")
}

// putVendoredCallers replaces the installed shim and thin callers with the
// vendored variants, which call ./.github/workflows/reusable-*.yml.
func putVendoredCallers(t *testing.T, fc *forge.FakeClient, owner, repo, ref string) {
	t.Helper()
	files, err := BuildScaffoldFiles(InstallConfig{
		Owner: owner, Repo: repo, Forge: ForgeGitHub, Roles: []string{"triage"},
		MintURL: "https://mint.example.com", UpstreamRef: ref, UpstreamTag: ref,
		VendorBinary: true, ManagedConfig: emptyManagedOverlay(t),
	})
	require.NoError(t, err)
	n := 0
	for _, f := range files {
		if isWorkflowFile(f.Path) && strings.Contains(string(f.Content), "./.github/workflows/reusable-") {
			fc.FileContents[owner+"/"+repo+"/"+f.Path] = f.Content
			n++
		}
	}
	require.NotZero(t, n, "vendored fixture must contain local callers")
}

type recordingCommit struct {
	mu     sync.Mutex
	files  []forge.TreeFile
	direct []bool
}

func (r *recordingCommit) fn() ScaffoldCommitFunc {
	return func(_ context.Context, _, _ string, files []forge.TreeFile, direct bool, _ bool) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.files = append(r.files, files...)
		r.direct = append(r.direct, direct)
		return nil
	}
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

func findStaleAction(actions []ComponentAction) (ComponentAction, bool) {
	for _, a := range actions {
		if a.Component == staleVendoredComponent {
			return a, true
		}
	}
	return ComponentAction{}, false
}

func assertNoLocalCallers(t *testing.T, fc *forge.FakeClient, owner, repo string) {
	t.Helper()
	for key, content := range fc.FileContents {
		p := strings.TrimPrefix(key, owner+"/"+repo+"/")
		if isWorkflowFile(p) {
			assert.NotContains(t, string(content), "./.github/workflows/reusable-", "%s still calls a local reusable workflow", p)
		}
	}
}

func newStaleVendoredInstalledRepo(t *testing.T) *forge.FakeClient {
	t.Helper()
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	putStaleVendoredAssets(fc, "acme", "api")
	return fc
}

func TestConverge_StaleVendoredAssetsRemovedInScaffoldCommit(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(map[bool]string{true: "direct", false: "pr"}[direct], func(t *testing.T) {
			fc := newStaleVendoredInstalledRepo(t)
			putVendoredCallers(t, fc, "acme", "api", "v1.0.0")
			cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
			cfg.Direct = direct
			rec := &recordingCommit{}

			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed())
			require.Len(t, result.Converged(), 1)
			require.Equal(t, []bool{direct}, rec.direct, "one scaffold commit, using the requested delivery")

			assert.Equal(t, staleVendoredOwned, deletedPaths(rec.files), "only owned vendored paths are deleted")
			action, ok := findStaleAction(result.Results[0].Actions)
			require.True(t, ok)
			assert.Equal(t, "delete", action.Action)
			assert.Contains(t, action.Detail, "4 stale vendored file(s)")
			assert.Contains(t, action.Detail, "kept 1 vendor-manifest entry not owned by Fullsend")

			applyTreeFiles(fc, "acme", "api", rec.files)
			assert.Contains(t, fc.FileContents, "acme/api/"+staleVendoredUserFile, "user file must be preserved")
			assert.Contains(t, fc.FileContents, "acme/api/"+preset.OverlayPath, "config.yaml listed by the manifest is kept")
			assertNoLocalCallers(t, fc, "acme", "api")

			again := &recordingCommit{}
			result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), again.fn(), noopProgress)
			require.NoError(t, err)
			assert.Len(t, result.AlreadyCurrent(), 1, "repeat run is idempotent")
			assert.Empty(t, again.files)
		})
	}
}

// The live ref upgrade only rewrites the shim's version marker; the caller
// must still be rewritten away from its local target before deletion.
func TestConverge_StaleVendoredRefUpgradeRewritesShim(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v0.9.0", "https://mint.example.com")
	putVendoredCallers(t, fc, "acme", "api", "v0.9.0")
	putStaleVendoredAssets(fc, "acme", "api")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))

	dry := cfg
	dry.DryRun = true
	dryResult, err := Converge(context.Background(), dry, newTestClientFactory(fc), (&recordingCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, dryResult.Failed())
	dryAction, ok := findStaleAction(dryResult.Results[0].Actions)
	require.True(t, ok)
	assert.Equal(t, "delete", dryAction.Action)

	rec := &recordingCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, staleVendoredOwned, deletedPaths(rec.files))
	applyTreeFiles(fc, "acme", "api", rec.files)
	assertNoLocalCallers(t, fc, "acme", "api")
}

func TestConverge_StaleVendoredDryRunMatchesLive(t *testing.T) {
	fc := newStaleVendoredInstalledRepo(t)
	putVendoredCallers(t, fc, "acme", "api", "v1.0.0")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
	cfg.DryRun = true
	before := len(fc.FileContents)
	rec := &recordingCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, rec.files, "dry run must not commit")
	assertNoForgeWrites(t, fc)
	assert.Len(t, fc.FileContents, before)
	assert.Len(t, result.Converged(), 1, "pending removal is planned work")

	action, ok := findStaleAction(result.Results[0].Actions)
	require.True(t, ok)
	assert.Equal(t, "delete", action.Action)
	assert.True(t, strings.HasPrefix(action.Detail, "would remove 4 stale vendored file(s)"), action.Detail)
	for _, p := range staleVendoredOwned {
		assert.Contains(t, action.Detail, p)
	}
	assert.NotContains(t, action.Detail, staleVendoredUserFile)
}

// With neither a manifest fullsend_ref nor a release build, nothing can
// render vendor-free callers: the local targets are kept and the pending
// work is reported instead of claiming the repository is current.
func TestConverge_StaleVendoredNoRefKeepsLocalTargets(t *testing.T) {
	fc := newStaleVendoredInstalledRepo(t)
	putVendoredCallers(t, fc, "acme", "api", "v1.0.0")
	m := newConvergeManifest("acme/api")
	m.GitHub.FullsendRef = ""
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))

	for _, dryRun := range []bool{true, false} {
		cfg.DryRun = dryRun
		rec := &recordingCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		assert.Empty(t, deletedPaths(rec.files))
		assert.Empty(t, result.AlreadyCurrent(), "pending cleanup is not current")
		action, ok := findStaleAction(result.Results[0].Actions)
		require.True(t, ok)
		assert.Equal(t, ActionPending, action.Action)
		assert.Contains(t, action.Detail, "fullsend_ref")
		assert.Contains(t, action.Detail, ".github/workflows/fullsend.yaml")
	}

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	drift := findStaleDrift(status.Repos[0].Drifts)
	require.NotNil(t, drift)
	assert.Contains(t, drift.Actual, "kept until")
}

func TestConverge_StaleVendoredCustomCallerBlocksCleanup(t *testing.T) {
	fc := newStaleVendoredInstalledRepo(t)
	fc.FileContents["acme/api/.github/workflows/custom\x1b[2J.yml"] = []byte("jobs:\n  x:\n    uses: ./.github/workflows/reusable-dispatch.yml\n")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
	rec := &recordingCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, deletedPaths(rec.files))
	action, ok := findStaleAction(result.Results[0].Actions)
	require.True(t, ok)
	assert.Equal(t, ActionPending, action.Action)
	assert.Contains(t, action.Detail, ".github/workflows/custom?[2J.yml")
	assert.NotContains(t, action.Detail, "\x1b", "repository paths are sanitized")
}

func TestConverge_StaleVendoredManifestInvalidFailsBeforeWrites(t *testing.T) {
	for _, installed := range []bool{true, false} {
		t.Run(map[bool]string{true: "established", false: "fresh"}[installed], func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
			if installed {
				markFullyInstalled(fc, "acme", "api")
				populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
				cfg = withoutInferenceInputs(cfg)
			}
			fc.FileContents["acme/api/.fullsend/vendor-manifest.yaml"] = []byte("version: \"9\"\n")
			rec := &recordingCommit{}

			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
			require.NoError(t, err)
			require.Len(t, result.Failed(), 1)
			assert.Contains(t, result.Failed()[0].Error.Error(), "checking stale vendored assets")
			assert.Empty(t, rec.files)
			assertNoForgeWrites(t, fc)
		})
	}
}

func TestConverge_FreshInstallRemovesStaleVendoredAssets(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	putStaleVendoredAssets(fc, "acme", "api")
	// A partial earlier install left a vendored thin caller behind; the
	// install rewrites it in the same commit.
	putVendoredCallers(t, fc, "acme", "api", "v1.0.0")
	delete(fc.FileContents, "acme/api/.github/workflows/fullsend.yaml")
	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))

	dry := cfg
	dry.DryRun = true
	dryResult, err := Converge(context.Background(), dry, newTestClientFactory(fc), (&recordingCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, dryResult.Failed())
	dryAction, ok := findStaleAction(dryResult.Results[0].Actions)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(dryAction.Detail, "would remove 4 stale vendored file(s)"), dryAction.Detail)

	status, err := Status(context.Background(), cfg.Manifest, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	drift := findStaleDrift(status.Repos[0].Drifts)
	require.NotNil(t, drift, "status reports a partial installation's stale assets")
	assert.NotContains(t, drift.Actual, "kept until")

	rec := &recordingCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, result.Installed(), 1)
	assert.Equal(t, staleVendoredOwned, deletedPaths(rec.files))
	wrote := slices.ContainsFunc(rec.files, func(f forge.TreeFile) bool {
		return f.Path == ".github/workflows/fullsend.yaml" && !f.Delete
	})
	assert.True(t, wrote, "deletions ride along with the install commit")
	action, ok := findStaleAction(result.Results[0].Actions)
	require.True(t, ok)
	assert.Equal(t, "delete", action.Action)
	applyTreeFiles(fc, "acme", "api", rec.files)
	assertNoLocalCallers(t, fc, "acme", "api")
}

func TestConverge_FreshInstallAdoptionBlocksStaleVendoredCleanup(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		fc := newFakeClientForBatch("acme/api")
		putStaleVendoredAssets(fc, "acme", "api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n# hand-authored\n")
		cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
		cfg.DryRun = dryRun
		rec := &recordingCommit{}

		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Empty(t, rec.files)
		assertNoForgeWrites(t, fc)
		for _, p := range staleVendoredOwned {
			assert.Contains(t, fc.FileContents, "acme/api/"+p)
		}
	}
}

func findStaleDrift(drifts []Drift) *Drift {
	for i := range drifts {
		if drifts[i].Field == staleVendoredComponent {
			return &drifts[i]
		}
	}
	return nil
}

func TestStatus_StaleVendoredAssetsAgreeWithInstall(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	m.GitHub.Repos = m.GitHub.Repos[:1]
	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0", "https://mint.example.com", "us-central1")
	putStaleVendoredAssets(fc, "acme-corp", "api-server")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	drift := findStaleDrift(result.Repos[0].Drifts)
	require.NotNil(t, drift)
	assert.Equal(t, "4 stale vendored file(s) pending removal", drift.Actual)

	for _, p := range staleVendoredOwned {
		delete(fc.FileContents, "acme-corp/api-server/"+p)
	}
	result, err = Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	assert.Nil(t, findStaleDrift(result.Repos[0].Drifts), "no drift after cleanup")

	// A malformed manifest is a status error, not a clean report.
	fc.FileContents["acme-corp/api-server/.fullsend/vendor-manifest.yaml"] = []byte("version: [\n")
	result, err = Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Repos[0].Error, "checking stale vendored assets")
}

func TestFindStaleVendored_SkipsVendoredAndGitLab(t *testing.T) {
	fc := forge.NewFakeClient()
	putStaleVendoredAssets(fc, "acme", "api")
	base := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, ForgeConfig: ForgeConfig{Client: fc}}

	assets, err := findStaleVendored(context.Background(), base)
	require.NoError(t, err)
	assert.Equal(t, staleVendoredOwned, assets.Paths)

	vendored := base
	vendored.Vendor = true
	assets, err = findStaleVendored(context.Background(), vendored)
	require.NoError(t, err)
	assert.Nil(t, assets)

	gitlab := base
	gitlab.Forge = ForgeGitLab
	assets, err = findStaleVendored(context.Background(), gitlab)
	require.NoError(t, err)
	assert.Nil(t, assets, "GitLab vendor mode is unsupported")
}

func TestStaleVendored_ExplicitEntryOverridesGlob(t *testing.T) {
	yes, no := true, false
	m := newConvergeManifest()
	m.Defaults.Vendor = &yes
	m.GitHub.Repos = []RepoEntry{
		{Name: "acme/*", Vendor: &yes},
		{Name: "acme/api", Vendor: &no},
	}
	fc := forge.NewFakeClient()
	putStaleVendoredAssets(fc, "acme", "api")
	putStaleVendoredAssets(fc, "acme", "web")

	for repo, wantCleanup := range map[string]bool{"api": true, "web": false} {
		rc, ok := m.ResolveConfigWithGlobs("acme", repo)
		require.True(t, ok)
		rc.ForgeConfig = ForgeConfig{Client: fc}
		assets, err := findStaleVendored(context.Background(), rc)
		require.NoError(t, err)
		if wantCleanup {
			require.NotNil(t, assets)
			assert.Equal(t, staleVendoredOwned, assets.Paths)
		} else {
			assert.Nil(t, assets, "glob-covered vendored repo keeps its assets")
		}
	}
}

func TestPlanStaleVendoredCleanup_Unit(t *testing.T) {
	fc := forge.NewFakeClient()
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub,
		ForgeConfig: ForgeConfig{Client: fc, WorkflowPaths: []string{".github/workflows/fullsend.yaml", ".github/workflows/fullsend.yml"}}}

	plan, err := planStaleVendoredCleanup(context.Background(), resolved, nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, plan)

	// No reusable workflow among the stale files: nothing to check.
	assets := &scaffold.StaleVendoredAssets{Paths: []string{".defaults/action.yml"}}
	plan, err = planStaleVendoredCleanup(context.Background(), resolved, assets, nil, nil)
	require.NoError(t, err)
	assert.True(t, plan.deletable())

	// The shim installed as .yml is rewritten from the .yaml rendering.
	local := []byte("uses: ./.github/workflows/reusable-dispatch.yml\n")
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = local
	assets = &scaffold.StaleVendoredAssets{
		Paths:     []string{".github/workflows/reusable-dispatch.yml"},
		RepoFiles: []string{".github/workflows/fullsend.yml", ".github/workflows/reusable-dispatch.yml", ".github/workflows/notes.txt"},
	}
	expected := func() (map[string][]byte, error) {
		return map[string][]byte{".github/workflows/fullsend.yaml": []byte("uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1\n")}, nil
	}
	plan, err = planStaleVendoredCleanup(context.Background(), resolved, assets, nil, expected)
	require.NoError(t, err)
	require.True(t, plan.deletable())
	require.Len(t, plan.rewrites, 1)
	assert.Equal(t, ".github/workflows/fullsend.yml", plan.rewrites[0].Path)

	files, actions := applyStaleVendoredPlan([]forge.TreeFile{{Path: ".github/workflows/fullsend.yml", Content: local}}, plan, true, "acme/api", noopProgress)
	require.Len(t, files, 2)
	assert.NotContains(t, string(files[0].Content), "./.github")
	assert.True(t, files[1].Delete)
	assert.Equal(t, "update", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "would rewrite")

	// A planned deletion of the caller removes it from consideration.
	plan, err = planStaleVendoredCleanup(context.Background(), resolved, assets, []forge.TreeFile{{Path: ".github/workflows/fullsend.yml", Delete: true}}, nil)
	require.NoError(t, err)
	assert.True(t, plan.deletable())
	assert.Empty(t, plan.rewrites)

	// Read and render failures fail safely.
	fc.GetFileContentErrors = map[string]error{"acme/api/.github/workflows/fullsend.yml": assert.AnError}
	_, err = planStaleVendoredCleanup(context.Background(), resolved, assets, nil, expected)
	require.ErrorIs(t, err, assert.AnError)
	fc.GetFileContentErrors = nil
	_, err = planStaleVendoredCleanup(context.Background(), resolved, assets, nil, func() (map[string][]byte, error) { return nil, assert.AnError })
	require.ErrorIs(t, err, assert.AnError)

	// A caller the plan already writes is not re-read.
	plan, err = planStaleVendoredCleanup(context.Background(), resolved, assets,
		[]forge.TreeFile{{Path: ".github/workflows/fullsend.yml", Content: []byte("uses: remote\n")}}, nil)
	require.NoError(t, err)
	assert.True(t, plan.deletable())

	// A workflow listed in the tree that cannot be read (404) fails closed.
	assets.RepoFiles = append(assets.RepoFiles, ".github/workflows/gone.yml")
	_, err = planStaleVendoredCleanup(context.Background(), resolved, assets,
		[]forge.TreeFile{{Path: ".github/workflows/fullsend.yml", Content: []byte("uses: remote\n")}}, nil)
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestCallsAny_DecodesYAMLScalars(t *testing.T) {
	targets := []string{"./.github/workflows/reusable-dispatch.yml"}
	assert.True(t, callsAny([]byte("uses: ./.github/workflows/reusable-dispatch.yml\n"), targets))
	assert.True(t, callsAny([]byte("jobs:\n  a:\n    uses: \"./.github/workflows/reusable-\\u0064ispatch.yml\"\n"), targets),
		"an escaped scalar decodes to the target")
	assert.False(t, callsAny([]byte("jobs:\n  a:\n    uses: owner/repo/.github/workflows/reusable-dispatch.yml@v1\n"), targets))
	assert.False(t, callsAny([]byte("not: [valid"), targets))
}

func TestCallerContentFuncs(t *testing.T) {
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, MintURL: "https://mint.example.com",
		InferenceAuth: InferenceAuthVertexWIF}

	none, err := establishedCallerContent(context.Background(), resolved, DriftConfig{}, nil)()
	require.NoError(t, err)
	assert.Nil(t, none, "no ref renders nothing")

	files, err := establishedCallerContent(context.Background(), resolved, DriftConfig{UpstreamRef: "v1.2.3", UpstreamTag: "v1.2.3"}, nil)()
	require.NoError(t, err)
	assert.Contains(t, string(files[".github/workflows/fullsend.yaml"]), "reusable-dispatch.yml@v1.2.3")

	fresh, err := freshCallerContent(resolved, DriftConfig{})()
	require.NoError(t, err)
	assert.NotContains(t, string(fresh[".github/workflows/fullsend.yaml"]), "./.github/workflows/reusable-")
}

// A manifest is repository content, so a user-created file it lists is not
// Fullsend-owned just because it sits under an allowed .defaults/ subtree.
func TestConverge_StaleVendoredKeepsManifestListedUserFiles(t *testing.T) {
	userFiles := []string{
		".defaults/internal/scaffold/fullsend-repo/agents/mine.md",
		".fullsend/.defaults/.github/scripts/mine.sh",
	}
	fc := newStaleVendoredInstalledRepo(t)
	putVendoredCallers(t, fc, "acme", "api", "v1.0.0")
	manifest := staleVendorManifestYAML
	for _, p := range userFiles {
		manifest += "  - " + p + "\n"
		fc.FileContents["acme/api/"+p] = []byte("mine")
	}
	fc.FileContents["acme/api/.fullsend/vendor-manifest.yaml"] = []byte(manifest)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
	rec := &recordingCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, staleVendoredOwned, deletedPaths(rec.files))
	action, ok := findStaleAction(result.Results[0].Actions)
	require.True(t, ok)
	assert.Contains(t, action.Detail, "kept 3 vendor-manifest entries not owned by Fullsend")
	for _, p := range userFiles {
		assert.NotContains(t, action.Detail, p, "ignored entries come from repository content and are not named")
	}

	applyTreeFiles(fc, "acme", "api", rec.files)
	for _, p := range userFiles {
		assert.Contains(t, fc.FileContents, "acme/api/"+p, "user-created file must be preserved")
	}
}

// With credential updates pending, a failure reading a caller workflow for
// the stale-vendored plan must stop the run before any variable, secret or
// scaffold write, and the same run succeeds once the read works.
func TestConverge_StaleVendoredCallerReadFailureBlocksCredentialWrites(t *testing.T) {
	fc := newStaleVendoredInstalledRepo(t)
	putVendoredCallers(t, fc, "acme", "api", "v1.0.0")
	const caller = ".github/workflows/custom-caller.yml"
	fc.FileContents["acme/api/"+caller] = []byte("jobs:\n  x:\n    runs-on: ubuntu-latest\n")
	fc.GetFileContentErrors = map[string]error{"acme/api/" + caller: errors.New("boom")}
	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	rec := &recordingCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Failed()[0].Error.Error(), "checking stale vendored assets")
	assert.Empty(t, rec.files, "no scaffold commit")
	assert.Empty(t, fc.CreatedSecrets, "no secret writes")
	assert.Empty(t, fc.DeletedSecrets, "no secret deletions")
	assert.Empty(t, fc.UpdatedVariables, "no variable updates")
	assert.Empty(t, fc.DeletedVariables, "no variable deletions")
	assert.Empty(t, fc.CreatedFiles, "no file writes")
	assert.Empty(t, fc.CommittedFiles, "no commits")
	assert.Empty(t, fc.CreatedProposals, "no PR")

	// The credential updates were genuinely pending: once the read works,
	// the same configuration writes them along with the cleanup commit.
	fc.GetFileContentErrors = nil
	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), rec.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.NotEmpty(t, fc.CreatedSecrets, "pending credentials are written once planning succeeds")
	assert.Equal(t, staleVendoredOwned, deletedPaths(rec.files))
}
