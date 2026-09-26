package repos

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func mustOverlayConfig(t *testing.T, raw string) config.OverlayConfig {
	t.Helper()
	var o config.OverlayConfig
	require.NoError(t, yaml.Unmarshal([]byte(raw), &o))
	require.True(t, o.IsSet())
	return o
}

func mustDesiredOverlay(t *testing.T, m *Manifest, owner, repo string) []byte {
	t.Helper()
	cfg, ok := m.ResolveConfig(owner, repo)
	require.True(t, ok)
	body, managed, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	require.True(t, managed)
	return body
}

func overlayResolved(t *testing.T, fc *forge.FakeClient, m *Manifest, owner, repo string) ResolvedConfig {
	t.Helper()
	cfg, ok := m.ResolveConfig(owner, repo)
	require.True(t, ok)
	cfg.ForgeConfig.Client = fc
	return cfg
}

func TestMarshalManagedConfig_NilOverlay(t *testing.T) {
	body, err := marshalManagedConfig(nil)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "roles:")
	assert.NotContains(t, string(body), "runtime:")
}

func TestDesiredManagedConfig_NilWriterStillManaged(t *testing.T) {
	body, managed, err := desiredManagedConfig(ResolvedConfig{OverlayManaged: true})
	require.NoError(t, err)
	assert.True(t, managed)
	require.NotNil(t, body)
}

func TestMarshalManagedConfig_InvalidMintURL(t *testing.T) {
	_, err := marshalManagedConfig(mustOverlayConfig(t, "mint_url: http://insecure.example.com\n").Writer())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint_url")
}

func TestDesiredManagedConfig_Unmanaged(t *testing.T) {
	m := newConvergeManifest("acme/api")
	cfg, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	body, managed, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	assert.False(t, managed)
	assert.Nil(t, body)
}

func TestConvergeManagedConfigFiles_IdempotentWhenUnchanged(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = desired
	resolved := overlayResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)
}

func TestConvergeManagedConfigFiles_ReplacesChangedMarkedOverlay(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")
	resolved := overlayResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	require.Len(t, files, 1)
	assert.Equal(t, preset.OverlayPath, files[0].Path)
	assert.Equal(t, desired, files[0].Content)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
}

// TestConvergeManagedConfigFiles_UnmarkedExistingRequiresAdoption is the
// ADR-0122 adoption gate: an existing .fullsend/config.yaml that does not
// carry the ownership marker predates managed-overlay adoption (it may be
// hand-authored, with security-relevant settings the manifest never
// restates), so it must never be treated as ordinary drift and silently
// rewritten.
func TestConvergeManagedConfigFiles_UnmarkedExistingRequiresAdoption(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n")
	resolved := overlayResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	assert.Empty(t, files, "an unmarked existing overlay must not be overwritten")
	require.Len(t, actions, 1)
	assert.Equal(t, ActionAdoptionRequired, actions[0].Action)
	assert.Contains(t, actions[0].Detail, "adoption required")
}

func TestConvergeManagedConfigFiles_AddsMissingOverlay(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	resolved := overlayResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	require.Len(t, files, 1)
	assert.Equal(t, "add", actions[0].Action)
	assert.Equal(t, desired, files[0].Content)
}

func TestConvergeManagedConfigFiles_DryRunAddAndUpdate(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	resolved := overlayResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, true, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, "add", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "would add")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")
	files, actions = convergeManagedConfigFiles(context.Background(), resolved, desired, true, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "would update")
}

func TestConvergeManagedConfigFiles_UnmanagedIsNoOp(t *testing.T) {
	m := newConvergeManifest("acme/api")
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("version: \"1\"\n# keep me\n")
	resolved := overlayResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, []byte("kill_switch: true\n"), false, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)
}

func TestConvergeManagedConfigFiles_ReadError(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + preset.OverlayPath: fmt.Errorf("boom"),
	}
	resolved := overlayResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, "error", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "reading")
}

func TestCheckManagedConfigDrift_ReportsMissingAndChanged(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	status := RepoStatus{}
	cfg := overlayResolved(t, fc, m, "acme", "api")
	checkManagedConfigDrift(context.Background(), cfg, &status)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, preset.OverlayPath, status.Drifts[0].Field)
	assert.Equal(t, "managed configuration", status.Drifts[0].Expected)
	assert.Equal(t, "missing", status.Drifts[0].Actual)

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n")
	status = RepoStatus{}
	checkManagedConfigDrift(context.Background(), cfg, &status)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, "managed configuration (adoption required)", status.Drifts[0].Expected)
	assert.Contains(t, status.Drifts[0].Actual, "ownership marker")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")
	status = RepoStatus{}
	checkManagedConfigDrift(context.Background(), cfg, &status)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, "installed content differs", status.Drifts[0].Actual)

	fc.FileContents["acme/api/"+preset.OverlayPath] = desired
	status = RepoStatus{}
	checkManagedConfigDrift(context.Background(), cfg, &status)
	assert.Empty(t, status.Drifts)
}

func TestCheckManagedConfigDrift_UnmanagedDoesNotCompare(t *testing.T) {
	m := newConvergeManifest("acme/api")
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("version: \"1\"\n# local edit\n")
	status := RepoStatus{}
	cfg := overlayResolved(t, fc, m, "acme", "api")
	checkManagedConfigDrift(context.Background(), cfg, &status)
	assert.Empty(t, status.Drifts)
	assert.Empty(t, status.Error)
}

func TestCheckManagedConfigDrift_ReadError(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	fc := forge.NewFakeClient()
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + preset.OverlayPath: fmt.Errorf("boom"),
	}
	status := RepoStatus{}
	cfg := overlayResolved(t, fc, m, "acme", "api")
	checkManagedConfigDrift(context.Background(), cfg, &status)
	assert.Contains(t, status.Error, "reading")
}

func TestCheckManagedConfigDrift_InvalidOverlay(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.GitHub.Repos[0].Config = mustOverlayConfig(t, "roles:\n  - not-a-role\n")
	fc := forge.NewFakeClient()
	status := RepoStatus{}
	cfg := overlayResolved(t, fc, m, "acme", "api")
	checkManagedConfigDrift(context.Background(), cfg, &status)
	assert.Contains(t, status.Error, "rendering managed config")
	assert.Contains(t, status.Error, "invalid role")
}

func TestConverge_ManagedConfigFreshInstallWritesCanonicalFile(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed, got %d (err=%v)", len(result.Installed()), result.Results[0].Error)
	}

	var overlay []byte
	for _, f := range sc.files {
		if f.Path == preset.OverlayPath {
			overlay = f.Content
		}
	}
	if overlay == nil {
		t.Fatal("fresh overlay-managed install must write config.yaml")
	}
	if string(overlay) != string(desired) {
		t.Errorf("overlay = %q, want canonical managed bytes %q", overlay, desired)
	}
	if strings.Contains(string(overlay), "roles:") {
		t.Errorf("managed configuration must stay sparse, got %s", overlay)
	}
}

// TestConverge_ManagedConfigFreshInstallUnmarkedExistingRequiresAdoption is the
// ADR-0122 adoption gate on the isNew/fresh-install path (#7638 follow-up
// to the already-installed convergeManagedConfigFiles gate): a repository has no
// shim workflow yet (isNew) but already carries a hand-authored
// .fullsend/config.yaml without the ownership marker. Install must leave
// that file untouched and report adoption required, the same as
// convergeManagedConfigFiles does for an already-installed repo, instead of
// silently overwriting it via InstallConfig.ManagedConfig.
func TestConverge_ManagedConfigFreshInstallUnmarkedExistingRequiresAdoption(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	original := []byte("kill_switch: false\n# hand-authored\n")
	fc.FileContents["acme/api/"+preset.OverlayPath] = original
	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")

	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if result.Results[0].Error != nil {
		t.Fatalf("repo error: %v", result.Results[0].Error)
	}

	for _, f := range sc.files {
		if f.Path == preset.OverlayPath {
			t.Errorf("adoption-required fresh install must not write config.yaml, got content %q", f.Content)
		}
	}
	if got := fc.FileContents["acme/api/"+preset.OverlayPath]; string(got) != string(original) {
		t.Error("unmarked overlay bytes must be preserved until adopted")
	}
	var sawAdoptionRequired bool
	for _, a := range result.Results[0].Actions {
		if a.Component == preset.OverlayPath && strings.Contains(a.Detail, "adoption required") {
			sawAdoptionRequired = true
		}
	}
	if !sawAdoptionRequired {
		t.Errorf("expected adoption-required action for config.yaml, got %v", result.Results[0].Actions)
	}
}

func TestConverge_ManagedConfigUnmanagedFreshInstallKeepsInstallerOverlay(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(result.Installed()))
	}

	var overlay []byte
	for _, f := range sc.files {
		if f.Path == preset.OverlayPath {
			overlay = f.Content
		}
	}
	if overlay == nil {
		t.Fatal("unmanaged fresh install must still write installer overlay")
	}
	if !strings.Contains(string(overlay), "roles:") {
		t.Errorf("unmanaged installer overlay should include roles, got %s", overlay)
	}
}

// TestConverge_ManagedConfigUnmarkedManualEditRequiresAdoption is the ADR-0122
// adoption gate exercised through the full Converge() batch path: an
// installed repo's .fullsend/config.yaml was hand-edited (or predates
// managed-overlay adoption) and carries no ownership marker, so
// convergence must report adoption required and leave the file untouched
// rather than silently rewriting it — a hand-authored file can carry
// security-relevant settings (kill_switch, roles, allowed_remote_resources,
// a disabled agent) the manifest never restates.
func TestConverge_ManagedConfigUnmarkedManualEditRequiresAdoption(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	original := []byte("kill_switch: false\n# manual edit\n")
	fc.FileContents["acme/api/"+preset.OverlayPath] = original

	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if result.Results[0].Error != nil {
		t.Fatalf("repo error: %v", result.Results[0].Error)
	}

	for _, f := range committedFiles {
		if f.Path == preset.OverlayPath {
			t.Error("an unmarked hand-authored overlay must not be silently rewritten; it requires adoption")
		}
	}
	if got := fc.FileContents["acme/api/"+preset.OverlayPath]; string(got) != string(original) {
		t.Error("unmarked overlay bytes must be preserved until adopted")
	}
	var sawAdoptionRequired bool
	for _, a := range result.Results[0].Actions {
		if a.Component == preset.OverlayPath && strings.Contains(a.Detail, "adoption required") {
			sawAdoptionRequired = true
		}
	}
	if !sawAdoptionRequired {
		t.Errorf("expected adoption-required action for config.yaml, got %v", result.Results[0].Actions)
	}
	// A pending adoption is outstanding work, not "already current": repos
	// status simultaneously reports this repo as needing adoption, so
	// repos install must not exit as if nothing were left to do.
	if result.Results[0].AlreadyCurrent {
		t.Error("a repo with a pending managed-configuration adoption must not be reported AlreadyCurrent")
	}
}

// TestConverge_ManagedConfigRewriteOnMarkedDrift is the post-adoption counterpart
// of TestConverge_ManagedConfigUnmarkedManualEditRequiresAdoption: once a
// .fullsend/config.yaml carries the ownership marker (already adopted),
// convergence still rewrites it wholesale on drift.
func TestConverge_ManagedConfigRewriteOnMarkedDrift(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")

	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Converged()) != 1 {
		t.Fatalf("expected 1 converged, got %d (err=%v actions=%v)", len(result.Converged()), result.Results[0].Error, result.Results[0].Actions)
	}

	var sawOverlay bool
	for _, f := range committedFiles {
		if f.Path == preset.OverlayPath {
			sawOverlay = true
			if string(f.Content) != string(desired) {
				t.Errorf("overlay content = %q, want canonical %q", f.Content, desired)
			}
		}
	}
	if !sawOverlay {
		t.Error("a marked (already-adopted) overlay must still be rewritten on drift")
	}
}

func TestConverge_ManagedConfigExactMatchIsIdempotent(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = desired

	committed := false
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		for _, f := range files {
			if f.Path == preset.OverlayPath {
				t.Error("exact overlay match must not rewrite config.yaml")
			}
		}
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.AlreadyCurrent()) != 1 {
		t.Errorf("expected 1 already current, got %d (actions: %v)", len(result.AlreadyCurrent()), result.Results[0].Actions)
	}
	if committed {
		t.Error("should not commit when managed configuration matches installed file")
	}
}

func TestConverge_ManagedConfigUnmanagedLeavesExisting(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	overlay := []byte("version: \"1\"\n# keep me\n")
	fc.FileContents["acme/api/"+preset.OverlayPath] = overlay

	m := newConvergeManifest(repoNames...)
	committedFiles := []forge.TreeFile{}
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if result.Results[0].Error != nil {
		t.Fatalf("repo error: %v", result.Results[0].Error)
	}
	for _, f := range committedFiles {
		if f.Path == preset.OverlayPath {
			t.Error("unmanaged configuration must not be rewritten")
		}
	}
	if got := fc.FileContents["acme/api/"+preset.OverlayPath]; string(got) != string(overlay) {
		t.Error("unmanaged configuration bytes must be preserved")
	}
}

func TestConverge_ManagedConfigFreshInstallDryRun(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if committed {
		t.Error("dry-run must not commit")
	}
	var saw bool
	for _, a := range result.Results[0].Actions {
		if a.Component == preset.OverlayPath && a.Action == "add" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected dry-run add action for config.yaml, got %v", result.Results[0].Actions)
	}
}

// TestConverge_ManagedConfigFreshInstallDryRunAdoptionRequired is the dry-run
// counterpart of TestConverge_ManagedConfigFreshInstallUnmarkedExistingRequiresAdoption:
// dry-run must report the same adoption-required outcome a live run would
// take, not an "add" action that the live run would then refuse to honor.
func TestConverge_ManagedConfigFreshInstallDryRunAdoptionRequired(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n# hand-authored\n")
	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if committed {
		t.Error("dry-run must not commit")
	}
	var saw bool
	for _, a := range result.Results[0].Actions {
		if a.Component == preset.OverlayPath {
			if a.Action != ActionAdoptionRequired || !strings.Contains(a.Detail, "adoption required") {
				t.Errorf("expected adoption-required action for config.yaml, got %+v", a)
			}
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected a config action reporting adoption required, got %v", result.Results[0].Actions)
	}
}

func TestConverge_ManagedConfigDryRunDoesNotCommit(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")

	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if committed {
		t.Error("dry-run must not commit overlay changes")
	}
	var saw bool
	for _, a := range result.Results[0].Actions {
		if a.Component == preset.OverlayPath && a.Action == "update" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected dry-run update action for config.yaml, got %v", result.Results[0].Actions)
	}
}

func TestConverge_ManagedConfigInvalidFailsBeforeApply(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	m.GitHub.Repos[0].Config = mustOverlayConfig(t, "roles:\n  - not-a-role\n")

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err == nil {
		t.Fatal("expected overlay validation error from Converge")
	}
	if !strings.Contains(err.Error(), "invalid role") {
		t.Errorf("expected invalid role error, got %v", err)
	}
	if committed {
		t.Error("invalid overlay must fail before applying changes")
	}
}

func TestConverge_ManagedConfigReadErrorFailsRepo(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + preset.OverlayPath: fmt.Errorf("boom"),
	}

	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 1 {
		t.Fatalf("expected 1 failed, got %d (actions=%v)", len(result.Failed()), result.Results[0].Actions)
	}
	if result.Failed()[0].Error == nil || !strings.Contains(result.Failed()[0].Error.Error(), "reading") {
		t.Errorf("expected overlay read error, got %v", result.Failed()[0].Error)
	}
	if committed {
		t.Error("overlay read error must fail before committing")
	}
}

func TestConverge_ManagedConfigAndPresetIndependent(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\nruntime: pi\n")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")

	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme", "api")

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Converged()) != 1 {
		t.Fatalf("expected 1 converged, got %d (err=%v actions=%v)", len(result.Converged()), result.Results[0].Error, result.Results[0].Actions)
	}

	var sawBase, sawOverlay bool
	for _, f := range committedFiles {
		switch f.Path {
		case preset.BasePath:
			sawBase = true
			if string(f.Content) != testPresetYAML {
				t.Errorf("base content = %q, want new preset", f.Content)
			}
		case preset.OverlayPath:
			sawOverlay = true
			if string(f.Content) != string(desired) {
				t.Errorf("overlay content = %q, want canonical %q", f.Content, desired)
			}
		}
	}
	if !sawBase {
		t.Error("changed preset must still replace config.base.yaml")
	}
	if !sawOverlay {
		t.Error("changed overlay must still replace config.yaml")
	}
}

func TestConverge_GitLab_OverlayRewrite(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")

	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, cfg.Manifest, "acme", "api")

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = append(committedFiles, files...)
		return nil
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if result.Results[0].Error != nil {
		t.Fatalf("repo error: %v", result.Results[0].Error)
	}

	var sawOverlay bool
	for _, f := range committedFiles {
		if f.Path == preset.OverlayPath {
			sawOverlay = true
			if string(f.Content) != string(desired) {
				t.Errorf("gitlab overlay content = %q, want canonical %q", f.Content, desired)
			}
		}
	}
	if !sawOverlay {
		t.Error("GitLab converge must rewrite drifted managed configuration")
	}
}

func TestConverge_GitLab_OverlayFreshInstall(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, cfg.Manifest, "acme", "api")

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed, got %d (err=%v)", len(result.Installed()), result.Results[0].Error)
	}

	var overlay []byte
	for _, f := range sc.files {
		if f.Path == preset.OverlayPath {
			overlay = f.Content
		}
	}
	if overlay == nil {
		t.Fatal("GitLab fresh overlay-managed install must write config.yaml")
	}
	if string(overlay) != string(desired) {
		t.Errorf("gitlab overlay = %q, want canonical %q", overlay, desired)
	}
}

func TestStatus_OverlayDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")
	desired := mustDesiredOverlay(t, m, "acme-corp", "api-server")

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://mint.example.com", "us-central1")
	fc.FileContents["acme-corp/api-server/"+preset.OverlayPath] = desired
	fc.FileContents["acme-corp/web-frontend/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.Drifted != 1 {
		t.Errorf("drifted = %d, want 1", result.Summary.Drifted)
	}

	for _, s := range result.Repos {
		switch s.Repo {
		case "api-server":
			if len(s.Drifts) != 0 {
				t.Errorf("api-server: want no drifts, got %v", s.Drifts)
			}
		case "web-frontend":
			var found bool
			for _, d := range s.Drifts {
				if d.Field == preset.OverlayPath {
					found = true
					if d.Expected != "managed configuration" {
						t.Errorf("expected managed configuration, got %q", d.Expected)
					}
					if d.Actual != "installed content differs" {
						t.Errorf("actual = %q, want installed content differs", d.Actual)
					}
				}
			}
			if !found {
				t.Errorf("web-frontend: expected config.yaml drift, got %v", s.Drifts)
			}
		}
	}
}

func TestStatus_UnmanagedOverlayDoesNotCompare(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://mint.example.com", "us-central1")
	fc.FileContents["acme-corp/api-server/"+preset.OverlayPath] = []byte("kill_switch: true\n")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.Drifted != 0 {
		t.Errorf("drifted = %d, want 0 when overlay is unmanaged", result.Summary.Drifted)
	}
}

func TestStatus_GitLab_OverlayDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Config: mustOverlayConfig(t, "kill_switch: true\n")},
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	populateGitLabInstalled(fc, "acme", "api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, d := range result.Repos[0].Drifts {
		if d.Field == preset.OverlayPath {
			found = true
		}
	}
	if !found {
		t.Errorf("GitLab status must report config.yaml overlay drift, got %v", result.Repos[0].Drifts)
	}
}

func TestStatus_OverlayMissingFile(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	m.GitHub.Repos = []RepoEntry{{Name: "acme-corp/api-server"}}
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	delete(fc.FileContents, "acme-corp/api-server/"+preset.OverlayPath)

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, d := range result.Repos[0].Drifts {
		if d.Field == preset.OverlayPath && d.Actual == "missing" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing overlay drift, got %v", result.Repos[0].Drifts)
	}
}

// TestStatus_OverlayAdoptionRequired is the status-side counterpart of the
// ADR-0122 adoption gate: an installed repository whose .fullsend/config.yaml
// predates managed-overlay adoption (no ownership marker) must be reported
// as needing adoption, not as ordinary "installed content differs" drift.
func TestStatus_OverlayAdoptionRequired(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	m.GitHub.Repos = []RepoEntry{{Name: "acme-corp/api-server"}}
	m.Defaults.Config = mustOverlayConfig(t, "kill_switch: true\n")

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	fc.FileContents["acme-corp/api-server/"+preset.OverlayPath] = []byte("kill_switch: false\n# hand-authored\n")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, d := range result.Repos[0].Drifts {
		if d.Field == preset.OverlayPath {
			found = true
			if d.Expected != "managed configuration (adoption required)" {
				t.Errorf("expected = %q, want adoption-required wording", d.Expected)
			}
			if !strings.Contains(d.Actual, "ownership marker") {
				t.Errorf("actual = %q, want ownership-marker explanation", d.Actual)
			}
		}
	}
	if !found {
		t.Errorf("expected adoption-required overlay drift, got %v", result.Repos[0].Drifts)
	}
}

func TestConverge_ManagedConfigOnlyOneRepoManaged(t *testing.T) {
	fc := newFakeClientForBatch("acme/managed", "acme/unmanaged")
	for _, repo := range []string{"managed", "unmanaged"} {
		markFullyInstalled(fc, "acme", repo)
		populateScaffoldContent(t, fc, "acme", repo, "v1.0.0", "https://mint.example.com")
		fc.FileContents["acme/"+repo+"/"+preset.OverlayPath] = []byte("version: \"1\"\n# local\n")
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1.0.0",
			Repos: []RepoEntry{
				{Name: "acme/managed", Config: mustOverlayConfig(t, "kill_switch: true\n")},
				{Name: "acme/unmanaged"},
			},
		},
	}
	desired := mustDesiredOverlay(t, m, "acme", "managed")
	// The managed repo's overlay is already adopted (carries the ownership
	// marker) but drifted, so this test exercises ordinary drift-rewrite
	// for a managed repo alongside an untouched unmanaged sibling — not
	// the ADR-0122 adoption gate, covered separately above.
	fc.FileContents["acme/managed/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: false\n")

	var committedByRepo = map[string][]forge.TreeFile{}
	commitFn := func(_ context.Context, owner, repo string, files []forge.TreeFile, _ bool, _ bool) error {
		committedByRepo[owner+"/"+repo] = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if result.Results[0].Error != nil || result.Results[1].Error != nil {
		t.Fatalf("repo errors: %v %v", result.Results[0].Error, result.Results[1].Error)
	}

	var sawManaged bool
	for _, f := range committedByRepo["acme/managed"] {
		if f.Path == preset.OverlayPath {
			sawManaged = true
			if string(f.Content) != string(desired) {
				t.Errorf("managed configuration = %q, want %q", f.Content, desired)
			}
		}
	}
	if !sawManaged {
		t.Error("opted-in repository must rewrite overlay")
	}
	for _, f := range committedByRepo["acme/unmanaged"] {
		if f.Path == preset.OverlayPath {
			t.Error("unmanaged sibling must not rewrite overlay")
		}
	}
	if got := fc.FileContents["acme/unmanaged/"+preset.OverlayPath]; string(got) != "version: \"1\"\n# local\n" {
		t.Error("unmanaged sibling overlay bytes must be preserved")
	}
}
