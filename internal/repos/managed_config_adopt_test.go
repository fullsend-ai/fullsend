package repos

import (
	"bytes"
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetAdoptOverlays_ResolvesAdoptOverlay(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	m.SetAdoptOverlays([]string{" ACME/Api "})
	api, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.True(t, api.AdoptOverlay, "names match case-insensitively")
	web, ok := m.ResolveConfig("acme", "web")
	require.True(t, ok)
	assert.False(t, web.AdoptOverlay, "only named repositories are adopted")

	m.SetAdoptOverlays(nil)
	api, _ = m.ResolveConfig("acme", "api")
	assert.False(t, api.AdoptOverlay)
}

func TestValidateAdoptSelections(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	require.NoError(t, ValidateAdoptSelections(m, nil, nil))
	require.NoError(t, ValidateAdoptSelections(m, nil, []string{"acme/api", "ACME/WEB"}))
	require.NoError(t, ValidateAdoptSelections(m, []string{"acme/*"}, []string{"acme/api"}))

	for name, tc := range map[string]struct {
		filter, adopt []string
		want          string
	}{
		"glob":         {adopt: []string{"acme/*"}, want: "patterns are not accepted"},
		"no slash":     {adopt: []string{"api"}, want: "owner/repo"},
		"leading":      {adopt: []string{"/api"}, want: "owner/repo"},
		"undeclared":   {adopt: []string{"other/repo"}, want: "not declared in repos.yaml"},
		"not selected": {filter: []string{"acme/web"}, adopt: []string{"acme/api"}, want: "not among the repositories selected"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateAdoptSelections(m, tc.filter, tc.adopt)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "nothing was changed")
		})
	}
	t.Run("several problems are reported together", func(t *testing.T) {
		err := ValidateAdoptSelections(m, nil, []string{"acme/*", "other/repo"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "acme/*")
		assert.Contains(t, err.Error(), "other/repo")
	})
	t.Run("invalid selector glob", func(t *testing.T) {
		err := ValidateAdoptSelections(m, []string{"acme/["}, []string{"acme/api"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid glob pattern")
	})
}

// With AdoptOverlay a markerless overlay is replaced by the managed file
// once the safety gate passes; a dry run plans the replacement.
func TestConvergeManagedConfigFiles_AdoptReplacesMarkerless(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	m.SetAdoptOverlays([]string{"acme/api"})
	desired := mustDesiredManaged(t, m, "acme", "api")

	for _, existing := range [][]byte{[]byte("kill_switch: false\n"), {}} {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/"+preset.OverlayPath] = existing
		resolved := managedResolved(t, fc, m, "acme", "api")

		files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
		require.Len(t, files, 1)
		assert.Equal(t, desired, files[0].Content)
		require.Len(t, actions, 1)
		assert.Equal(t, "update", actions[0].Action)
		assert.Contains(t, actions[0].Detail, "adopted "+preset.OverlayPath)

		files, actions = convergeManagedConfigFiles(context.Background(), resolved, desired, true, noopProgress)
		assert.Empty(t, files, "a dry run writes nothing")
		require.Len(t, actions, 1)
		assert.Contains(t, actions[0].Detail, "would adopt "+preset.OverlayPath)
	}
}

func TestConvergeManagedConfigFiles_AdoptKeepsSafetyGate(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "{}")
	m.SetAdoptOverlays([]string{"acme/api"})
	desired := mustDesiredManaged(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: true\n")
	resolved := managedResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, ActionSafetyRejected, actions[0].Action)
	assert.Contains(t, actions[0].Detail, "kill_switch")

	err := checkEstablishedOverlayGate(context.Background(), resolved, desired)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kill_switch")
	assert.NotContains(t, err.Error(), "adoption required")
}

func TestCheckEstablishedOverlayGate_AdoptPasses(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	desired := mustDesiredManaged(t, m, "acme", "api")
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n")

	err := checkEstablishedOverlayGate(context.Background(), managedResolved(t, fc, m, "acme", "api"), desired)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adoption required")

	m.SetAdoptOverlays([]string{"acme/api"})
	require.NoError(t, checkEstablishedOverlayGate(context.Background(), managedResolved(t, fc, m, "acme", "api"), desired))
}

// The base-replacement gate judges an adopted overlay as the managed file
// it becomes, not as the existing file it replaces.
func TestPresetReplacementRelaxations_AdoptUsesManagedOverlay(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "{}")
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: true\n")

	relaxations, err := presetReplacementRelaxations(context.Background(), managedResolved(t, fc, m, "acme", "api"), nil, true, nil)
	require.NoError(t, err)
	assert.Empty(t, relaxations, "an overlay awaiting adoption stays as installed")

	m.SetAdoptOverlays([]string{"acme/api"})
	relaxations, err = presetReplacementRelaxations(context.Background(), managedResolved(t, fc, m, "acme", "api"), nil, true, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, relaxations, "the adopted managed overlay drops kill_switch")
}

func TestEffectiveConfigOverlay_Adopt(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	m.SetAdoptOverlays([]string{"acme/api"})
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n")
	desired := mustDesiredManaged(t, m, "acme", "api")
	d := convergeDiscovery{resolved: managedResolved(t, fc, m, "acme", "api"), repo: ResolvedRepo{Owner: "acme", Repo: "api"}, managedConfig: desired}

	got, err := effectiveConfigOverlay(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, desired, got, "the adopted managed overlay is the one in effect")
}

func TestPreflightManagedConfig_AdoptReturnsPlan(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	m.SetAdoptOverlays([]string{"acme/api"})
	fc := newFakeClientForBatch("acme/api", "acme/web")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n")
	fc.FileContents["acme/web/"+preset.OverlayPath] = []byte("kill_switch: false\n")

	adoption, err := PreflightManagedConfig(context.Background(), m, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	require.Len(t, adoption, 2)
	byName := map[string]AdoptionRequiredRepo{}
	for _, a := range adoption {
		byName[a.Name()] = a
	}
	api := byName["acme/api"]
	assert.True(t, api.Adopting)
	assert.Contains(t, api.Guidance, "adopting it (--adopt, ADR-0122)")
	assert.Contains(t, api.Guidance, "with --adopt, `fullsend repos install` writes the managed file")
	web := byName["acme/web"]
	assert.False(t, web.Adopting)
	assert.Contains(t, web.Guidance, "adoption required")
	assert.Contains(t, web.Guidance, "fullsend repos install --adopt acme/web")
}

// A fresh install with an adopted markerless overlay runs the safety
// comparison in preflight instead of skipping it, and convergence installs
// the managed overlay in place of the existing file.
func TestConverge_AdoptOnFreshInstall(t *testing.T) {
	ctx := context.Background()
	t.Run("safety preflight rejects a relaxation", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: true\n")
		m := newConvergeManifest("acme/api")
		require.NoError(t, PreflightProposedManagedSafety(ctx, m, newTestClientFactory(fc), nil, nil), "an overlay awaiting adoption is skipped")
		m.SetAdoptOverlays([]string{"acme/api"})
		err := PreflightProposedManagedSafety(ctx, m, newTestClientFactory(fc), nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "kill_switch")
	})
	t.Run("install writes the managed overlay", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: false\n# hand-authored\n")
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
		m.SetAdoptOverlays([]string{"acme/api"})
		desired := mustDesiredManaged(t, m, "acme", "api")
		require.NoError(t, PreflightProposedManagedSafety(ctx, m, newTestClientFactory(fc), nil, nil))

		sc := &spyScaffoldCommit{}
		result, err := Converge(ctx, convergeCfgWithDefaults(m), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.NoError(t, result.Results[0].Error)
		var overlay []byte
		for _, f := range sc.files {
			if f.Path == preset.OverlayPath {
				overlay = f.Content
			}
		}
		assert.Equal(t, desired, overlay)
		assert.True(t, bytes.HasPrefix(overlay, []byte(managedConfigMarker)))
	})
}
