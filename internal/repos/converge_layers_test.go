package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConverge_DeclaredPresetCannotRelaxInheritedRestriction proves the
// shared pre-write check stops a repo whose declared preset would replace a
// base that supplies a restriction the proposed overlay does not declare.
// It runs for GitHub and GitLab, installed and fresh repos, dry run and
// live, and both direct and initialization PR/MR delivery.
func TestConverge_DeclaredPresetCannotRelaxInheritedRestriction(t *testing.T) {
	presetPath := writePresetFile(t, relaxingPreset)
	for _, forgeName := range layerForges {
		for _, installed := range []bool{true, false} {
			for _, dryRun := range []bool{false, true} {
				for _, direct := range []bool{true, false} {
					name := strings.Join([]string{forgeName, map[bool]string{true: "installed", false: "fresh"}[installed],
						map[bool]string{true: "dry", false: "live"}[dryRun], map[bool]string{true: "direct", false: "pr"}[direct]}, "/")
					t.Run(name, func(t *testing.T) {
						fc := newFakeClientForBatch("acme/api")
						var cfg ConvergeConfig
						switch forgeName {
						case ForgeGitLab:
							cfg = gitlabConvergeCfg("acme/api")
							cfg.Manifest.Defaults.ConfigBase.Source = presetPath
							if installed {
								populateGitLabInstalled(fc, "acme", "api")
							}
						default:
							m := newConvergeManifest("acme/api")
							m.Defaults.ConfigBase.Source = presetPath
							cfg = withoutInferenceInputs(convergeCfgWithDefaults(m))
							if installed {
								markFullyInstalled(fc, "acme", "api")
								populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
							}
						}
						cfg.DryRun = dryRun
						cfg.Direct = direct
						fc.FileContents["acme/api/"+preset.BasePath] = []byte(killSwitchBase)
						fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")

						committed := false
						commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
							committed = true
							return nil
						}
						varsBefore := len(fc.VariableValues) + len(fc.Secrets)
						result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
						require.NoError(t, err)
						failures := result.Failed()
						require.Len(t, failures, 1)
						assert.Contains(t, failures[0].Error.Error(), "kill_switch")
						assert.Contains(t, failures[0].Error.Error(), "less restrictive")
						assert.False(t, committed, "no commit or PR/MR is attempted")
						assert.Equal(t, varsBefore, len(fc.VariableValues)+len(fc.Secrets), "no variables or secrets are written")
						assert.Equal(t, killSwitchBase, string(fc.FileContents["acme/api/"+preset.BasePath]), "the existing base is untouched")
					})
				}
			}
		}
	}
}

func TestConverge_DeclaredPresetBlocksMalformedExistingBase(t *testing.T) {
	presetPath := writePresetFile(t, relaxingPreset)
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/"+preset.BasePath] = []byte(": bad [")
	m := newConvergeManifest("acme/api")
	m.Defaults.ConfigBase.Source = presetPath

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	result, err := Converge(context.Background(), withoutInferenceInputs(convergeCfgWithDefaults(m)), newTestClientFactory(fc), commitFn, noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Failed()[0].Error.Error(), "not a valid per-repo configuration")
	assert.False(t, committed)
}

func TestConverge_ZeroByteBaseWithNoPresetBlocks(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			var cfg ConvergeConfig
			if forgeName == ForgeGitLab {
				cfg = gitlabConvergeCfg("acme/api")
				populateGitLabInstalled(fc, "acme", "api")
			} else {
				cfg = withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
				markFullyInstalled(fc, "acme", "api")
				populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
			}
			fc.FileContents["acme/api/"+preset.BasePath] = []byte{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
				t.Error("an empty base with no preset must block writes")
				return nil
			}, noopProgress)
			require.NoError(t, err)
			require.Len(t, result.Failed(), 1)
			assert.Contains(t, result.Failed()[0].Error.Error(), "no config_base preset is declared")
		})
	}
}
