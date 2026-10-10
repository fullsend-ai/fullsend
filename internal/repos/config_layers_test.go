package repos

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The base/overlay contract is forge-neutral (both layers live in the
// repository's .fullsend/ directory), so these cases run for GitHub and
// GitLab resolved configs.
var layerForges = []string{ForgeGitHub, ForgeGitLab}

const (
	killSwitchBase   = "kill_switch: true\n"
	relaxingPreset   = "version: \"1\"\nruntime: claude\n"
	restrictedPreset = "version: \"1\"\nkill_switch: true\nruntime: claude\n"
)

func layerCfg(forgeName string, fc *forge.FakeClient, presetSource string, proposed []byte) ResolvedConfig {
	return ResolvedConfig{
		Owner:         "acme",
		Repo:          "api",
		Forge:         forgeName,
		Config:        presetSource,
		ProposedBase:  proposed,
		ConfigManaged: true,
		Managed:       config.NewEmptyPerRepoOverlay(),
		ForgeConfig:   ForgeConfig{Client: fc},
	}
}

func TestCheckUndeclaredBase_ZeroByteBaseBlocks(t *testing.T) {
	for _, f := range layerForges {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/"+preset.BasePath] = []byte{}
		err := checkUndeclaredBase(context.Background(), layerCfg(f, fc, "", nil))
		require.Error(t, err, f)
		assert.Contains(t, err.Error(), "no config_base preset is declared")

		status := RepoStatus{}
		checkPresetDrift(context.Background(), layerCfg(f, fc, "", nil), newPresetCache(), &status)
		require.Len(t, status.Drifts, 1, "an empty base file is drift when no preset resolves (%s)", f)
		assert.Equal(t, undeclaredBaseExpected, status.Drifts[0].Expected)
	}
}

func TestCheckDeclaredBase(t *testing.T) {
	src := writePresetFile(t, relaxingPreset)
	tests := []struct {
		name     string
		existing []byte // nil: no base file
		preset   string
		wantErr  string
	}{
		{name: "no base file", preset: relaxingPreset},
		{name: "matching base", existing: []byte(relaxingPreset), preset: relaxingPreset},
		{name: "zero-byte base is replaceable", existing: []byte{}, preset: relaxingPreset},
		{name: "differing safe base", existing: []byte("version: \"1\"\nruntime: pi\n"), preset: relaxingPreset},
		{name: "malformed base", existing: []byte(": not yaml ["), preset: relaxingPreset, wantErr: "not a valid per-repo configuration"},
		{name: "non per-repo base", existing: []byte("version: \"1\"\nroles: [triage]\ndispatch:\n  platform: github\n"), preset: relaxingPreset, wantErr: "not a valid per-repo configuration"},
		{name: "base kill switch dropped by preset", existing: []byte(killSwitchBase), preset: relaxingPreset, wantErr: "kill_switch"},
		{name: "preset keeps kill switch", existing: []byte(killSwitchBase), preset: restrictedPreset},
	}
	for _, f := range layerForges {
		for _, tt := range tests {
			t.Run(f+"/"+tt.name, func(t *testing.T) {
				fc := forge.NewFakeClient()
				if tt.existing != nil {
					fc.FileContents["acme/api/"+preset.BasePath] = tt.existing
				}
				err := checkDeclaredBase(context.Background(), layerCfg(f, fc, src, []byte(tt.preset)), []byte(tt.preset))
				if tt.wantErr == "" {
					assert.NoError(t, err)
					return
				}
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			})
		}
	}
}

func TestCheckDeclaredBase_NoPresetAndReadError(t *testing.T) {
	fc := forge.NewFakeClient()
	assert.NoError(t, checkDeclaredBase(context.Background(), layerCfg(ForgeGitHub, fc, "", nil), nil), "no preset is checkUndeclaredBase's concern")

	fc.GetFileContentErrors = map[string]error{"acme/api/" + preset.BasePath: os.ErrPermission}
	err := checkDeclaredBase(context.Background(), layerCfg(ForgeGitHub, fc, "preset.yaml", []byte(relaxingPreset)), []byte(relaxingPreset))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading "+preset.BasePath)
}

func TestCheckDeclaredBase_MarkerlessOverlayJudgesOnlyTheBaseChange(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.BasePath] = []byte(killSwitchBase)
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("runtime: pi\n")
	err := checkDeclaredBase(context.Background(), layerCfg(ForgeGitHub, fc, "p.yaml", []byte(relaxingPreset)), []byte(relaxingPreset))
	require.Error(t, err, "the markerless overlay does not declare kill_switch, so the base cannot drop it")
	assert.Contains(t, err.Error(), "kill_switch")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("kill_switch: true\n")
	require.NoError(t, checkDeclaredBase(context.Background(), layerCfg(ForgeGitHub, fc, "p.yaml", []byte(relaxingPreset)), []byte(relaxingPreset)),
		"the existing overlay itself keeps the restriction")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte{}
	require.Error(t, checkDeclaredBase(context.Background(), layerCfg(ForgeGitHub, fc, "p.yaml", []byte(relaxingPreset)), []byte(relaxingPreset)),
		"a zero-byte overlay declares nothing")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(": bad [")
	err = checkDeclaredBase(context.Background(), layerCfg(ForgeGitHub, fc, "p.yaml", []byte(relaxingPreset)), []byte(relaxingPreset))
	require.Error(t, err)
	assert.Contains(t, err.Error(), preset.OverlayPath)
}

func TestEvaluateManagedSafetyGate_UsesProposedPresetNotInstalledBase(t *testing.T) {
	for _, f := range layerForges {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/"+preset.BasePath] = []byte(killSwitchBase)
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")

		// Without a loaded preset the installed base is layered under both
		// sides, so nothing looks relaxed.
		got, err := evaluateManagedSafetyGate(context.Background(), layerCfg(f, fc, "p.yaml", nil))
		require.NoError(t, err)
		assert.Empty(t, got)

		// With the proposed preset the proposed overlay falls through to it.
		got, err = evaluateManagedSafetyGate(context.Background(), layerCfg(f, fc, "p.yaml", []byte(relaxingPreset)))
		require.NoError(t, err)
		require.Len(t, got, 1, f)
		assert.Equal(t, "kill_switch", got[0].Key)

		got, err = evaluateManagedSafetyGate(context.Background(), layerCfg(f, fc, "p.yaml", []byte(restrictedPreset)))
		require.NoError(t, err)
		assert.Empty(t, got)
	}
}

func TestEvaluateManagedSafetyGate_PresetIntroducingGatewayOnEstablishedInstall(t *testing.T) {
	const gatewayPreset = "inference:\n  gateway:\n    url: https://gw.example.com/v1\n    audience: gw-aud\n"
	for _, f := range layerForges {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/"+preset.BasePath] = []byte("{}\n")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")

		// The established install has no gateway and the overlay declares
		// none, so a preset that adds one is a relaxation.
		got, err := evaluateManagedSafetyGate(context.Background(), layerCfg(f, fc, "p.yaml", []byte(gatewayPreset)))
		require.NoError(t, err)
		assert.Equal(t, []string{"inference.gateway.url", "inference.gateway.audience"}, config.SafetyRelaxationKeys(got), f)
	}
}

func TestConvergePresetFiles_ZeroByteBaseIsUpdatedNotAdded(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.BasePath] = []byte{}
	files, actions := convergePresetFiles(context.Background(), layerCfg(ForgeGitHub, fc, "p.yaml", nil), []byte(testPresetYAML), true, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "would update")

	files, actions = convergePresetFiles(context.Background(), layerCfg(ForgeGitHub, fc, "p.yaml", nil), []byte(testPresetYAML), false, noopProgress)
	require.Len(t, files, 1)
	assert.Equal(t, "update", actions[0].Action)
}

func TestCheckPresetDrift_DeclaredBaseSafetyAndValidity(t *testing.T) {
	src := writePresetFile(t, relaxingPreset)
	for _, f := range layerForges {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/"+preset.BasePath] = []byte(killSwitchBase)
		status := RepoStatus{}
		status.Installed = true
		checkPresetDrift(context.Background(), layerCfg(f, fc, src, nil), newPresetCache(), &status)
		require.Len(t, status.Drifts, 1, f)
		assert.Contains(t, status.Drifts[0].Actual, "less restrictive")
		assert.Contains(t, status.Drifts[0].Actual, "kill_switch")

		fc.FileContents["acme/api/"+preset.BasePath] = []byte{}
		status = RepoStatus{}
		checkPresetDrift(context.Background(), layerCfg(f, fc, src, nil), newPresetCache(), &status)
		require.Len(t, status.Drifts, 1, "an empty base differs from the declared preset before install too")
		assert.Contains(t, status.Drifts[0].Actual, "empty")

		fc.FileContents["acme/api/"+preset.BasePath] = []byte(": bad [")
		status = RepoStatus{}
		status.Installed = true
		checkPresetDrift(context.Background(), layerCfg(f, fc, src, nil), newPresetCache(), &status)
		assert.Contains(t, status.Error, "not a valid per-repo configuration")
	}
}

func TestPreflightManagedConfig_ReportsBothLayersAndAdoptionDetail(t *testing.T) {
	ctx := context.Background()
	m := newConvergeManifest("acme/api")
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("keep_history: true\n# note\n")
	fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\n")

	err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, preset.OverlayPath+" exists without the managed-configuration ownership marker")
	assert.Contains(t, msg, preset.BasePath+" exists but no config_base preset is declared", "both problems are reported together")
	assert.Contains(t, msg, "proposed repos.yaml entry", "the adoption proposal is part of the install failure")
	assert.Contains(t, msg, "file settings difference")
	assert.Contains(t, msg, "effective layered configuration change")
	assert.Contains(t, msg, "keep_history")
}

func TestPreflightManagedConfig_MalformedLayers(t *testing.T) {
	ctx := context.Background()
	src := writePresetFile(t, relaxingPreset)
	m := newConvergeManifest("acme/api")
	m.Defaults.ConfigBase.Source = src

	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(": bad [")
	fc.FileContents["acme/api/"+preset.BasePath] = []byte(": bad [")
	err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a per-repo configuration", "malformed overlay")
	assert.Contains(t, err.Error(), "not a valid per-repo configuration", "malformed base")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("version: \"1\"\nroles: 5\n")
	err = PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be parsed", "per-repo shaped overlay that fails to parse")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("version: \"1\"\nroles: [triage]\ndispatch:\n  platform: github\n")
	delete(fc.FileContents, "acme/api/"+preset.BasePath)
	err = PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a per-repo configuration")

	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")
	fc.GetFileContentErrors = map[string]error{"acme/api/" + preset.BasePath: os.ErrPermission}
	err = PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading "+preset.BasePath)
}

func TestManifestReadOnly_LocalFileWithoutWritePermission(t *testing.T) {
	m := newConvergeManifest("acme/api")
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\n"), 0o644))
	assert.False(t, ManifestReadOnly(m, path))
	assert.False(t, ManifestReadOnly(m, ""))
	assert.False(t, ManifestReadOnly(m, filepath.Join(t.TempDir(), "absent.yaml")))
	assert.False(t, ManifestReadOnly(m, t.TempDir()), "a directory is not a manifest file")

	require.NoError(t, os.Chmod(path, 0o444))
	assert.True(t, ManifestReadOnly(m, path))
	err := writeManifest(path, m)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
}

func TestEnsureManagedConfigDefaults_ReadOnlyLocalManifestFailsBeforeWrite(t *testing.T) {
	m := newConvergeManifest("acme/api")
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\n"), 0o444))
	for _, dry := range []bool{false, true} {
		_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
			Manifest: m, ManifestPath: path, DryRun: dry,
		}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.Error(t, err, "dry-run and live agree (dry=%v)", dry)
		assert.Contains(t, err.Error(), "read-only")
		assert.Contains(t, err.Error(), "- fullsend-ai/fullsend")
	}
}

func TestRequireWritableManifestForCarved(t *testing.T) {
	ctx := context.Background()
	for _, f := range layerForges {
		t.Run(f, func(t *testing.T) {
			m := &Manifest{Version: 1, Defaults: testInferenceDefaults()}
			platform := m.EnsurePlatform(f)
			platform.Repos = []RepoEntry{
				{Name: "acme/*", Runtime: "pi"},
				{Name: "acme/api", Runtime: "pi", Vendor: boolPtr(true)},
			}
			fc := newFakeClientForBatch("acme/api")

			require.NoError(t, RequireWritableManifestForCarved(ctx, m, filepath.Join(t.TempDir(), "repos.yaml"), []string{"acme/api"}, newTestClientFactory(fc), nil, false),
				"a writable manifest needs no suggestion")
			require.NoError(t, RequireWritableManifestForCarved(ctx, m, "https://example.com/repos.yaml", nil, newTestClientFactory(fc), nil, false))

			err := RequireWritableManifestForCarved(ctx, m, "https://example.com/repos.yaml", []string{"acme/api"}, newTestClientFactory(fc), []string{"triage"}, true)
			require.Error(t, err)
			msg := err.Error()
			assert.Contains(t, msg, "read-only")
			assert.Contains(t, msg, f+".repos")
			for _, want := range []string{"name: acme/api", "runtime: pi", "vendor: true", "- fullsend-ai/fullsend", "- triage"} {
				assert.Contains(t, msg, want, "the exact carved entry with other per-entry flags and install defaults")
			}

			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")
			err = RequireWritableManifestForCarved(ctx, m, "https://example.com/repos.yaml", []string{"acme/api"}, newTestClientFactory(fc), nil, false)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "fullsend-ai/fullsend", "no install defaults once an overlay exists")
		})
	}

	m := newConvergeManifest("acme/api")
	err := RequireWritableManifestForCarved(ctx, m, "https://example.com/repos.yaml", []string{"acme/api"}, failingFactory{}, nil, false)
	require.Error(t, err)
}

func boolPtr(b bool) *bool { return &b }

func TestStatus_BothLayersReportedBeforeInstall(t *testing.T) {
	ctx := context.Background()
	presetPath := writePresetFile(t, relaxingPreset)
	gitlab := func(m *Manifest) *Manifest {
		m.GitLab = &PlatformConfig{URL: "https://gitlab.example.com", FullsendRef: "v2.5.0", Repos: []RepoEntry{{Name: "acme/api"}}}
		m.GitHub = nil
		return m
	}
	for _, f := range layerForges {
		build := func() (*Manifest, *forge.FakeClient) {
			m := newConvergeManifest("acme/api")
			if f == ForgeGitLab {
				m = gitlab(m)
			}
			return m, newFakeClientForBatch("acme/api")
		}

		t.Run(f+"/overlay adoption and zero-byte undeclared base together", func(t *testing.T) {
			m, fc := build()
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("keep_history: true\n")
			fc.FileContents["acme/api/"+preset.BasePath] = []byte{}
			result, err := Status(ctx, m, newTestClientFactory(fc), 1, []string{"acme/api"})
			require.NoError(t, err)
			r := result.Repos[0]
			require.Empty(t, r.Error)
			assert.False(t, r.Installed)
			require.Len(t, r.Drifts, 2, "both problems are reported")
			assert.Equal(t, preset.BasePath, r.Drifts[0].Field)
			assert.Equal(t, preset.OverlayPath, r.Drifts[1].Field)
			assert.Contains(t, r.Drifts[1].Detail, "proposed repos.yaml entry")
		})

		t.Run(f+"/declared preset would relax the base", func(t *testing.T) {
			m, fc := build()
			m.Defaults.ConfigBase = ConfigBase{Source: presetPath}
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")
			fc.FileContents["acme/api/"+preset.BasePath] = []byte(killSwitchBase)
			result, err := Status(ctx, m, newTestClientFactory(fc), 1, []string{"acme/api"})
			require.NoError(t, err)
			r := result.Repos[0]
			require.Empty(t, r.Error)
			var sawBase bool
			for _, d := range r.Drifts {
				if d.Field == preset.BasePath {
					sawBase = true
					assert.Contains(t, d.Actual, "kill_switch")
				}
			}
			assert.True(t, sawBase, "base drift names the implicit relaxation: %+v", r.Drifts)
		})
	}
}
