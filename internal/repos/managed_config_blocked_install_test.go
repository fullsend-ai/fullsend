package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wideningPreset supplies a cross-repo create_issues target that the code
// defaults do not allow, so a repository that only gains this preset
// widens its effective configuration through overlay fallthrough.
const wideningPreset = "version: \"1\"\ncreate_issues:\n  allow_targets:\n    repos:\n      - acme/other\n"

// assertNoForgeWrites fails when the fake client recorded any file,
// branch, PR/MR, variable or secret write.
func assertNoForgeWrites(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	assert.Empty(t, fc.CreatedFiles, "no file writes")
	assert.Empty(t, fc.CommittedFiles, "no commits")
	assert.Empty(t, fc.CommittedFilesToBranch, "no branch commits")
	assert.Empty(t, fc.ForceCommittedFiles, "no forced commits")
	assert.Empty(t, fc.CreatedBranches, "no branches")
	assert.Empty(t, fc.CreatedProposals, "no PR/MR")
	assert.Empty(t, fc.CreatedSecrets, "no secret writes")
	assert.Empty(t, fc.DeletedSecrets, "no secret deletions")
	assert.Empty(t, fc.Variables, "no variable writes")
	assert.Empty(t, fc.UpdatedVariables, "no variable updates")
	assert.Empty(t, fc.DeletedVariables, "no variable deletions")
}

// blockedInstallFixture builds a single-repo convergence for forgeName,
// installed or fresh, with an optional declared preset.
func blockedInstallFixture(t *testing.T, forgeName string, installed bool, presetPath string) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc := newFakeClientForBatch("acme/api")
	var cfg ConvergeConfig
	if forgeName == ForgeGitLab {
		cfg = gitlabConvergeCfg("acme/api")
		cfg.Manifest.Defaults.ConfigBase.Source = presetPath
		if installed {
			populateGitLabInstalled(fc, "acme", "api")
		}
		return fc, cfg
	}
	m := newConvergeManifest("acme/api")
	m.Defaults.ConfigBase.Source = presetPath
	cfg = convergeCfgWithDefaults(m)
	if installed {
		cfg = withoutInferenceInputs(cfg)
		markFullyInstalled(fc, "acme", "api")
		populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	}
	return fc, cfg
}

// An installed repository that gains a declared preset but has no base file
// is judged like a base replacement, even when its marked overlay already
// equals the rendered managed overlay and so would not be rewritten.
func TestConverge_InstalledRepoGainingPresetWithoutBaseIsSafetyChecked(t *testing.T) {
	presetPath := writePresetFile(t, wideningPreset)
	for _, forgeName := range layerForges {
		for _, dryRun := range []bool{false, true} {
			name := forgeName + "/" + map[bool]string{true: "dry", false: "live"}[dryRun]
			t.Run(name, func(t *testing.T) {
				fc, cfg := blockedInstallFixture(t, forgeName, true, presetPath)
				cfg.DryRun = dryRun
				desired := mustDesiredManaged(t, cfg.Manifest, "acme", "api")
				fc.FileContents["acme/api/"+preset.OverlayPath] = desired

				committed := false
				commitFn := func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
					committed = true
					return nil
				}
				result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
				require.NoError(t, err)
				failures := result.Failed()
				require.Len(t, failures, 1)
				assert.Contains(t, failures[0].Error.Error(), "create_issues.allow_targets")
				assert.Contains(t, failures[0].Error.Error(), "adding "+preset.BasePath)
				assert.False(t, committed, "no commit or PR/MR is attempted")
				assertNoForgeWrites(t, fc)
				assert.NotContains(t, fc.FileContents, "acme/api/"+preset.BasePath, "the base is not delivered")
				assert.Equal(t, string(desired), string(fc.FileContents["acme/api/"+preset.OverlayPath]))
			})
		}
	}

	t.Run("an explicit declaration is not an implicit relaxation", func(t *testing.T) {
		fc, cfg := blockedInstallFixture(t, ForgeGitHub, true, presetPath)
		cfg.DryRun = true
		cfg.Manifest.Defaults.Config = mustManagedConfig(t, "create_issues:\n  allow_targets:\n    repos: [acme/other]\n")
		fc.FileContents["acme/api/"+preset.OverlayPath] = mustDesiredManaged(t, cfg.Manifest, "acme", "api")
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), noopCommit, noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
	})
}

func noopCommit(context.Context, string, string, []forge.TreeFile, bool, bool) error { return nil }

// A fresh install blocked by overlay adoption or by the layered safety gate
// stops before Install: no variable, secret, scaffold file or PR/MR write,
// in a dry run and a live run alike.
func TestConverge_BlockedFreshInstallMakesNoWrites(t *testing.T) {
	cases := []struct {
		name     string
		overlay  string
		wantErr  string
		wantKind string
	}{
		{name: "markerless overlay", overlay: "kill_switch: false\n# hand-authored\n", wantErr: "adoption required", wantKind: ActionAdoptionRequired},
		{name: "zero-byte overlay", overlay: "", wantErr: "adoption required", wantKind: ActionAdoptionRequired},
		{name: "safety gate rejects overlay", overlay: managedConfigMarker + "kill_switch: true\n", wantErr: "kill_switch", wantKind: ActionSafetyRejected},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			for _, dryRun := range []bool{false, true} {
				for _, direct := range []bool{true, false} {
					name := strings.Join([]string{forgeName, tc.name,
						map[bool]string{true: "dry", false: "live"}[dryRun],
						map[bool]string{true: "direct", false: "pr"}[direct]}, "/")
					t.Run(name, func(t *testing.T) {
						fc, cfg := blockedInstallFixture(t, forgeName, false, "")
						cfg.DryRun = dryRun
						cfg.Direct = direct
						fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(tc.overlay)

						committed := false
						commitFn := func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
							committed = true
							return nil
						}
						result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
						require.NoError(t, err)
						failures := result.Failed()
						require.Len(t, failures, 1)
						assert.Contains(t, failures[0].Error.Error(), tc.wantErr)
						assert.False(t, failures[0].Installed)
						assert.False(t, committed, "no scaffold commit or PR/MR is attempted")
						assertNoForgeWrites(t, fc)
						assert.Equal(t, tc.overlay, string(fc.FileContents["acme/api/"+preset.OverlayPath]), "the existing overlay is untouched")
						require.NotEmpty(t, failures[0].Actions)
						var kinds []string
						for _, a := range failures[0].Actions {
							kinds = append(kinds, a.Action)
						}
						assert.Contains(t, kinds, tc.wantKind)
						for _, a := range failures[0].Actions {
							assert.NotEqual(t, "all", a.Component, "a blocked fresh install is never reported as an install plan")
						}
					})
				}
			}
		}
	}
}

// A pristine first install (no installation, no overlay, no base) inherits
// create_issues targets from its declared preset: there is no existing
// effective configuration to relax, so the layered gate must not reject it.
func TestConverge_PristineFirstInstallInheritsPresetPermissions(t *testing.T) {
	presetPath := writePresetFile(t, wideningPreset)
	for _, forgeName := range layerForges {
		for _, dryRun := range []bool{false, true} {
			name := forgeName + "/" + map[bool]string{true: "dry", false: "live"}[dryRun]
			t.Run(name, func(t *testing.T) {
				fc, cfg := blockedInstallFixture(t, forgeName, false, presetPath)
				cfg.DryRun = dryRun
				sc := &spyScaffoldCommit{}
				result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
				require.NoError(t, err)
				require.Empty(t, result.Failed(), "pristine install must not be rejected by the safety gate")
				require.Len(t, result.Installed(), 1)

				if dryRun {
					var sawBase, sawOverlay bool
					for _, a := range result.Results[0].Actions {
						sawBase = sawBase || (a.Component == preset.BasePath && a.Action == "add")
						sawOverlay = sawOverlay || (a.Component == preset.OverlayPath && a.Action == "add")
					}
					assert.True(t, sawBase && sawOverlay, "dry run plans both layers: %+v", result.Results[0].Actions)
					assertNoForgeWrites(t, fc)
					return
				}
				sc.mu.Lock()
				defer sc.mu.Unlock()
				var paths []string
				for _, f := range sc.files {
					paths = append(paths, f.Path)
				}
				assert.Contains(t, paths, preset.BasePath)
				assert.Contains(t, paths, preset.OverlayPath)
			})
		}
	}

	t.Run("a pre-existing base is not pristine", func(t *testing.T) {
		fc, cfg := blockedInstallFixture(t, ForgeGitHub, false, presetPath)
		cfg.DryRun = true
		fc.FileContents["acme/api/"+preset.BasePath] = []byte(killSwitchBase)
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), noopCommit, noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "kill_switch")
		assertNoForgeWrites(t, fc)
	})
}

// A first install whose initialization PR/MR is still unmerged has already
// written variables and secrets but neither configuration file nor the shim
// workflow. Re-running it is a retry of the same pending initialization, so
// a preset that supplies cross-repository permissions must not be rejected
// as an implicit relaxation, and a failed scaffold delivery after the
// credential writes must be recoverable the same way.
func TestConverge_PendingInitializationRetryKeepsPresetPermissions(t *testing.T) {
	presetPath := writePresetFile(t, wideningPreset)
	for _, forgeName := range layerForges {
		t.Run(forgeName+"/second run", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, false, presetPath)
			cfg.Direct = false
			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed())

			result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "a retried pending initialization must not be gated as a relaxation")
		})
		t.Run(forgeName+"/after failed scaffold delivery", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, false, presetPath)
			cfg.Direct = false
			failing := func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
				return assert.AnError
			}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), failing, noopProgress)
			require.NoError(t, err)
			require.NotEmpty(t, result.Failed(), "the scaffold delivery failure is reported")

			sc := &spyScaffoldCommit{}
			result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "recovery after a failed scaffold delivery must not be gated as a relaxation")
		})
		// A failure before the initialization branch exists (here a secret
		// write after the variables were written) leaves only credentials, so
		// the unchanged manifest must complete on retry.
		t.Run(forgeName+"/after failed credential write", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, false, presetPath)
			cfg.Direct = false
			fc.Errors["CreateRepoSecret"] = assert.AnError
			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.NotEmpty(t, result.Failed(), "the credential write failure is reported")
			_, branchErr := fc.GetBranchRef(context.Background(), "acme", "api", DefaultScaffoldBranch)
			require.Error(t, branchErr, "the failure came before the initialization branch")

			delete(fc.Errors, "CreateRepoSecret")
			result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "retry before branch creation must not be gated as a relaxation")
		})
	}

	t.Run("a stale initialization branch does not exempt a live thin caller", func(t *testing.T) {
		fc, cfg := blockedInstallFixture(t, ForgeGitHub, false, presetPath)
		fc.BranchRefs["acme/api/"+DefaultScaffoldBranch] = "abc123"
		addThinCallerFiles(fc, "acme", "api")
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), noopCommit, noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "create_issues.allow_targets")
	})

	t.Run("a stale initialization branch does not exempt a live shim workflow", func(t *testing.T) {
		fc, cfg := blockedInstallFixture(t, ForgeGitHub, false, presetPath)
		fc.BranchRefs["acme/api/"+DefaultScaffoldBranch] = "abc123"
		fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
		fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), noopCommit, noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "create_issues.allow_targets")
	})

	t.Run("an established installation keeps the gate", func(t *testing.T) {
		fc, cfg := blockedInstallFixture(t, ForgeGitHub, true, presetPath)
		cfg.DryRun = true
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), noopCommit, noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "less restrictive")
	})
}

func TestStatus_PresetGainWithoutBase(t *testing.T) {
	ctx := context.Background()
	presetPath := writePresetFile(t, wideningPreset)

	t.Run("pristine pre-install reports nothing", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.ConfigBase = ConfigBase{Source: presetPath}
		fc := newFakeClientForBatch("acme/api")
		result, err := Status(ctx, m, newTestClientFactory(fc), 1, []string{"acme/api"})
		require.NoError(t, err)
		r := result.Repos[0]
		require.Empty(t, r.Error)
		assert.False(t, r.Installed)
		assert.Empty(t, r.Drifts)
	})

	t.Run("pre-install overlay without a base reports the relaxation", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.ConfigBase = ConfigBase{Source: presetPath}
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = mustDesiredManaged(t, m, "acme", "api")
		result, err := Status(ctx, m, newTestClientFactory(fc), 1, []string{"acme/api"})
		require.NoError(t, err)
		r := result.Repos[0]
		require.Empty(t, r.Error)
		require.Len(t, r.Drifts, 1)
		assert.Equal(t, preset.BasePath, r.Drifts[0].Field)
		assert.Contains(t, r.Drifts[0].Actual, "create_issues.allow_targets")
		assert.Contains(t, r.Drifts[0].Actual, "adding it")
	})

	t.Run("installed overlay without a base reports the relaxation", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.ConfigBase = ConfigBase{Source: presetPath}
		fc := newFakeClientForBatch("acme/api")
		markFullyInstalled(fc, "acme", "api")
		populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
		fc.FileContents["acme/api/"+preset.OverlayPath] = mustDesiredManaged(t, m, "acme", "api")
		result, err := Status(ctx, m, newTestClientFactory(fc), 1, []string{"acme/api"})
		require.NoError(t, err)
		r := result.Repos[0]
		require.Empty(t, r.Error)
		var base *Drift
		for i := range r.Drifts {
			if r.Drifts[i].Field == preset.BasePath {
				base = &r.Drifts[i]
			}
		}
		require.NotNil(t, base, "drifts: %+v", r.Drifts)
		assert.Contains(t, base.Actual, "missing; adding it would make the effective configuration less restrictive without an explicit manifest declaration (ADR-0122): create_issues.allow_targets")
		assert.Contains(t, base.Actual, "acme/other")
	})
}

func TestStatus_MarkerlessOverlayErrorsAndGuidance(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		overlay string
	}{
		{name: "per-repo shaped but unparseable", overlay: "version: \"1\"\nroles: 5\n"},
		{name: "not a per-repo configuration", overlay: "version: \"1\"\nroles: [triage]\ndispatch:\n  platform: github\n"},
		{name: "malformed yaml", overlay: ": bad ["},
	} {
		for _, installed := range []bool{false, true} {
			name := tc.name + "/" + map[bool]string{true: "installed", false: "pre-install"}[installed]
			t.Run(name, func(t *testing.T) {
				m := newConvergeManifest("acme/api")
				fc := newFakeClientForBatch("acme/api")
				if installed {
					markFullyInstalled(fc, "acme", "api")
					populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
				}
				fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(tc.overlay)
				result, err := Status(ctx, m, newTestClientFactory(fc), 1, []string{"acme/api"})
				require.NoError(t, err)
				r := result.Repos[0]
				assert.NotEmpty(t, r.Error, "an invalid markerless overlay is an error, not adoption guidance")
				for _, d := range r.Drifts {
					assert.NotEqual(t, preset.OverlayPath, d.Field, "no adoption drift for an invalid overlay")
				}
			})
		}
	}

	t.Run("adoption guidance shows the effective layered change", func(t *testing.T) {
		presetPath := writePresetFile(t, wideningPreset)
		m := newConvergeManifest("acme/api")
		m.Defaults.ConfigBase = ConfigBase{Source: presetPath}
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("keep_history: false\n")
		fc.FileContents["acme/api/"+preset.BasePath] = []byte(wideningPreset)
		result, err := Status(ctx, m, newTestClientFactory(fc), 1, []string{"acme/api"})
		require.NoError(t, err)
		r := result.Repos[0]
		require.Empty(t, r.Error)
		var detail string
		for _, d := range r.Drifts {
			if d.Field == preset.OverlayPath {
				detail = d.Detail
			}
		}
		require.NotEmpty(t, detail, "drifts: %+v", r.Drifts)
		assert.Contains(t, detail, "file settings difference")
		assert.Contains(t, detail, "effective layered configuration change")
		// keep_history is dropped by adoption, so the layered default returns.
		assert.Contains(t, detail, "- keep_history: false")
		assert.Contains(t, detail, "+ keep_history: true")
	})
}

func TestPreflightManagedConfig_AdoptionGuidanceUsesDeclaredPreset(t *testing.T) {
	presetPath := writePresetFile(t, "version: \"1\"\nkeep_history: false\n")
	m := newConvergeManifest("acme/api")
	m.Defaults.ConfigBase = ConfigBase{Source: presetPath}
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("keep_history: true\n")

	err := PreflightManagedConfig(context.Background(), m, newTestClientFactory(fc), nil)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "effective layered configuration change")
	// The preset the install would add changes the effective value once the
	// overlay stops declaring keep_history.
	assert.Contains(t, msg, "- keep_history: true")
	assert.Contains(t, msg, "+ keep_history: false")
}

func TestAdoptionProposal_EffectiveChangeLayersBases(t *testing.T) {
	m := newConvergeManifest("acme/api")
	desired := desiredFor(t, m)
	currentBase := []byte("version: \"1\"\nruntime: pi\n")
	proposedBase := []byte("version: \"1\"\nruntime: claude\n")

	got := adoptionProposal(adoptionCfg(), []byte("keep_history: true\n"), desired, currentBase, proposedBase)
	assert.Contains(t, got, "effective layered configuration change")
	assert.Contains(t, got, "- runtime: pi")
	assert.Contains(t, got, "+ runtime: claude")

	same := adoptionProposal(adoptionCfg(), []byte("runtime: pi\n"), desired, currentBase, currentBase)
	// The file stops declaring runtime, but the unchanged base layer still
	// supplies it: the file-level difference shows the removal, the
	// effective change shows there is none.
	assert.Contains(t, same, "- runtime: pi   # removed: falls back to the base layer or code default")
	assert.Contains(t, same, "the effective configuration does not change")

	// An unparseable layer reports the comparison as unavailable instead of
	// dropping the section.
	bad := adoptionProposal(adoptionCfg(), []byte("keep_history: true\n"), desired, []byte(": bad ["), nil)
	assert.Contains(t, bad, "effective layered configuration change")
	assert.Contains(t, bad, "unavailable")

	none := adoptionProposal(adoptionCfg(), []byte("{}\n"), desired, nil, nil)
	assert.Contains(t, none, "the effective configuration does not change")
}

// A marked overlay that fails the safety gate stops an installed repository
// before any credential or variable write, with and without a declared
// preset, even though a missing variable would otherwise be written first.
func TestConverge_InstalledRepoOverlayGateRunsBeforeCredentialWrites(t *testing.T) {
	presetPath := writePresetFile(t, relaxingPreset)
	cases := []struct {
		name   string
		preset string
	}{
		{name: "no preset"},
		{name: "unchanged preset", preset: presetPath},
	}
	for _, tc := range cases {
		for _, dryRun := range []bool{false, true} {
			name := tc.name + "/" + map[bool]string{true: "dry", false: "live"}[dryRun]
			t.Run(name, func(t *testing.T) {
				fc, cfg := blockedInstallFixture(t, ForgeGitHub, true, tc.preset)
				cfg.DryRun = dryRun
				// The installed overlay carries a restriction the rendered
				// managed overlay does not restate, and a variable
				// has drifted so credential convergence has something to write.
				fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: true\n")
				if tc.preset != "" {
					fc.FileContents["acme/api/"+preset.BasePath] = []byte(relaxingPreset)
				}
				fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://stale.example.com"

				committed := false
				commitFn := func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
					committed = true
					return nil
				}
				result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
				require.NoError(t, err)
				failures := result.Failed()
				require.Len(t, failures, 1)
				assert.Contains(t, failures[0].Error.Error(), "kill_switch")
				assert.False(t, committed, "no commit or PR/MR is attempted")
				assertNoForgeWrites(t, fc)
				assert.Equal(t, "https://stale.example.com", fc.VariableValues["acme/api/FULLSEND_MINT_URL"], "no variable is written")
			})
		}
	}
}

// A repository with Fullsend variables and secrets but no shim workflow and
// neither configuration layer is not pristine: convergence applies the same
// safety gate status does instead of exempting it.
func TestConverge_PartialInstallWithoutConfigLayersIsNotPristine(t *testing.T) {
	presetPath := writePresetFile(t, wideningPreset)
	fc, cfg := blockedInstallFixture(t, ForgeGitHub, false, presetPath)
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	// Surviving thin callers without the shim workflow or a config layer are
	// a partially delivered installation, not a credential-only retry.
	addThinCallerFiles(fc, "acme", "api")

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), noopCommit, noopProgress)
	require.NoError(t, err)
	failures := result.Failed()
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Error.Error(), "create_issues.allow_targets")
	assertNoForgeWrites(t, fc)
}

// Converge itself rejects an established repository whose overlay is
// markerless or empty before any credential, variable, schedule or scaffold
// write, whether or not the CLI preflight ran first (#8218). The repository
// also has credential and scaffold drift that a continuing run would repair.
func TestConverge_EstablishedMarkerlessOverlayFailsWithoutWrites(t *testing.T) {
	overlays := map[string][]byte{
		"markerless": []byte("kill_switch: false\n# hand-authored\n"),
		"empty":      {},
	}
	for _, forgeName := range layerForges {
		for name, overlay := range overlays {
			for _, dryRun := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/dryRun=%t", forgeName, name, dryRun), func(t *testing.T) {
					fc, cfg := blockedInstallFixture(t, forgeName, true, "")
					cfg.DryRun = dryRun
					fc.FileContents["acme/api/"+preset.OverlayPath] = overlay
					// Credential and scaffold drift.
					if forgeName == ForgeGitLab {
						delete(fc.Secrets, "acme/api/"+forge.SecretOpenAIAPIKey)
						fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte("  ref: v1.0.0\n")
						delete(fc.PipelineSchedules, "acme/api")
					} else {
						delete(fc.Secrets, "acme/api/FULLSEND_GCP_PROJECT_ID")
						fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://stale.example.com"
						for _, path := range scaffold.PerRepoThinCallerPaths() {
							fc.FileContents["acme/api/"+path] = []byte("name: stale")
						}
					}
					secretsBefore := len(fc.Secrets)

					committed := false
					commitFn := func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
						committed = true
						return nil
					}
					result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
					require.NoError(t, err)
					failures := result.Failed()
					require.Len(t, failures, 1)
					msg := failures[0].Error.Error()
					assert.Contains(t, msg, "adoption required")
					assert.Contains(t, msg, "proposed repos.yaml entry")
					assert.False(t, committed, "no scaffold commit or PR/MR is attempted")
					assertNoForgeWrites(t, fc)
					assert.Empty(t, fc.PipelineSchedules["acme/api"], "no schedule is created")
					assert.Len(t, fc.Secrets, secretsBefore, "no credential is written")
					assert.Equal(t, string(overlay), string(fc.FileContents["acme/api/"+preset.OverlayPath]))
				})
			}
		}
	}
}

// PreflightProposedManagedSafety applies convergence's layered comparison to
// the proposed manifest without writing anything: an installed repository
// whose overlay or base would relax is rejected, a pristine first install and
// a safe proposal pass, and an unloadable preset is reported.
func TestPreflightProposedManagedSafety(t *testing.T) {
	presetPath := writePresetFile(t, wideningPreset)
	for _, forgeName := range layerForges {
		t.Run(forgeName+"/overlay relaxation on an installed repo", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, true, "")
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: true\n")
			err := PreflightProposedManagedSafety(context.Background(), cfg.Manifest, newTestClientFactory(fc), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "acme/api")
			assert.Contains(t, err.Error(), "kill_switch")
			assertNoForgeWrites(t, fc)
		})
		t.Run(forgeName+"/base relaxation on an installed repo", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, true, presetPath)
			fc.FileContents["acme/api/"+preset.OverlayPath] = mustDesiredManaged(t, cfg.Manifest, "acme", "api")
			err := PreflightProposedManagedSafety(context.Background(), cfg.Manifest, newTestClientFactory(fc), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "create_issues.allow_targets")
		})
		t.Run(forgeName+"/pristine first install passes", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, false, presetPath)
			require.NoError(t, PreflightProposedManagedSafety(context.Background(), cfg.Manifest, newTestClientFactory(fc), nil))
		})
		t.Run(forgeName+"/pending install with a relaxing overlay is rejected", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, false, "")
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: true\n")
			err := PreflightProposedManagedSafety(context.Background(), cfg.Manifest, newTestClientFactory(fc), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "kill_switch")
		})
		t.Run(forgeName+"/unloadable preset is reported", func(t *testing.T) {
			fc, cfg := blockedInstallFixture(t, forgeName, true, presetPath+".missing")
			err := PreflightProposedManagedSafety(context.Background(), cfg.Manifest, newTestClientFactory(fc), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "loading config preset")
		})
	}
}

func TestWriteManifest_PersistsAndRejectsReadOnlySource(t *testing.T) {
	m := newConvergeManifest("acme/api")
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, WriteManifest(path, m))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "acme/api")
	assert.Error(t, WriteManifest("https://example.invalid/repos.yaml", m))
}
