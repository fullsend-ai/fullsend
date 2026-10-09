package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/preset"
)

// customUnmanagedConfig is a hand-authored overlay carrying settings,
// an allowlist and comments the generated installer overlay would drop.
const customUnmanagedConfig = `# hand-authored: keep this comment
version: "1"
runtime: pi
roles:
  - triage
  - review
allowed_remote_resources:
  - https://example.com/skills/
`

func committedFile(sc *spyScaffoldCommit, path string) ([]byte, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, f := range sc.files {
		if f.Path == path {
			return f.Content, true
		}
	}
	return nil, false
}

func overlayAction(actions []ComponentAction) (ComponentAction, bool) {
	for _, a := range actions {
		if a.Component == preset.OverlayPath {
			return a, true
		}
	}
	return ComponentAction{}, false
}

// TestConverge_FreshInstallPreservesUnmanagedConfig covers #8218: a fresh
// install of an unmanaged repository delivers an existing hand-authored
// .fullsend/config.yaml unchanged, and the dry run reports the same
// decision.
func TestConverge_FreshInstallPreservesUnmanagedConfig(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		name := "live"
		if dryRun {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(customUnmanagedConfig)
			cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
			cfg.DryRun = dryRun

			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed())
			require.Len(t, result.Installed(), 1)

			if dryRun {
				_, committed := committedFile(sc, preset.OverlayPath)
				assert.False(t, committed, "dry-run must not commit")
				a, ok := overlayAction(result.Results[0].Actions)
				require.True(t, ok, "dry-run must report the overlay decision: %v", result.Results[0].Actions)
				assert.Equal(t, "none", a.Action)
				assert.Contains(t, a.Detail, "keep existing")
				return
			}
			delivered, ok := committedFile(sc, preset.OverlayPath)
			require.True(t, ok, "scaffold commit must deliver %s", preset.OverlayPath)
			assert.Equal(t, customUnmanagedConfig, string(delivered), "existing configuration must survive unchanged")
		})
	}
}

// TestConverge_FreshInstallPresetLeavesUnmanagedOverlay checks that a
// declared preset is still written as config.base.yaml while the existing
// overlay is delivered unchanged.
func TestConverge_FreshInstallPresetLeavesUnmanagedOverlay(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		name := "live"
		if dryRun {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(customUnmanagedConfig)
			m := newConvergeManifest("acme/api")
			m.Defaults.ConfigBase.Source = writePresetFile(t, testPresetYAML)
			cfg := convergeCfgWithDefaults(m)
			cfg.DryRun = dryRun

			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed())

			if dryRun {
				a, ok := overlayAction(result.Results[0].Actions)
				require.True(t, ok)
				assert.Equal(t, "none", a.Action)
				var sawBase bool
				for _, act := range result.Results[0].Actions {
					if act.Component == preset.BasePath && act.Action == "add" {
						sawBase = true
					}
				}
				assert.True(t, sawBase, "dry-run must still report the preset write")
				return
			}
			base, ok := committedFile(sc, preset.BasePath)
			require.True(t, ok, "preset must be written as %s", preset.BasePath)
			assert.Equal(t, testPresetYAML, string(base))
			overlay, ok := committedFile(sc, preset.OverlayPath)
			require.True(t, ok)
			assert.Equal(t, customUnmanagedConfig, string(overlay), "overlay must be independent of the preset")
		})
	}
}

// TestConverge_FreshInstallRejectsMalformedUnmanagedConfig checks that a
// malformed existing overlay fails the repository before any write rather
// than being silently replaced, in dry-run and live mode alike.
func TestConverge_FreshInstallRejectsMalformedUnmanagedConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{"invalid yaml", "runtime: [unclosed\n", "not a valid per-repo config"},
		{"per-org leftover", "dispatch:\n  platform: github\n", "not a valid per-repo config"},
		{"wrong field type", "roles:\n  triage: true\n", "parsing per-repo config"},
	} {
		for _, dryRun := range []bool{false, true} {
			name := tc.name + "/live"
			if dryRun {
				name = tc.name + "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				fc := newFakeClientForBatch("acme/api")
				fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(tc.content)
				cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
				cfg.DryRun = dryRun

				sc := &fakeScaffoldCommit{}
				result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
				require.NoError(t, err)
				failed := result.Failed()
				require.Len(t, failed, 1)
				assert.Contains(t, failed[0].Error.Error(), preset.OverlayPath)
				assert.Contains(t, failed[0].Error.Error(), tc.want)
				assert.Contains(t, failed[0].Error.Error(), "fix or remove it")
				assert.False(t, sc.called, "no scaffold commit")
				assert.Empty(t, fc.CreatedSecrets, "no secret writes")
				assert.Equal(t, tc.content, string(fc.FileContents["acme/api/"+preset.OverlayPath]), "existing file untouched")
			})
		}
	}
}

// TestConverge_FreshInstallRejectsUnreadableCommittedBase checks that the
// overlay is validated against the committed base when no preset is
// declared, so a broken base is reported instead of ignored.
func TestConverge_FreshInstallRejectsUnreadableCommittedBase(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(customUnmanagedConfig)
	fc.FileContents["acme/api/"+preset.BasePath] = []byte("roles:\n  triage: true\n")

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), convergeCfgWithDefaults(newConvergeManifest("acme/api")), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "parsing base config")
	assert.False(t, sc.called)
}

// TestConverge_FreshInstallReadErrorOnUnmanagedConfig checks that a read
// failure other than not-found (on the overlay, or on the base it is
// validated against) stops the install instead of falling back to a
// generated overlay that could replace the file.
func TestConverge_FreshInstallReadErrorOnUnmanagedConfig(t *testing.T) {
	for _, path := range []string{preset.OverlayPath, preset.BasePath} {
		t.Run(path, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(customUnmanagedConfig)
			fc.GetFileContentErrors = map[string]error{"acme/api/" + path: assert.AnError}

			sc := &fakeScaffoldCommit{}
			result, err := Converge(context.Background(), convergeCfgWithDefaults(newConvergeManifest("acme/api")), newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			failed := result.Failed()
			require.Len(t, failed, 1)
			assert.ErrorIs(t, failed[0].Error, assert.AnError)
			assert.False(t, sc.called)
		})
	}
}

// TestConverge_FreshInstallWithoutConfigGeneratesOverlay checks that a
// repository with no existing config.yaml (or an empty one) still
// receives the generated installer overlay.
func TestConverge_FreshInstallWithoutConfigGeneratesOverlay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing []byte
	}{
		{"missing", nil},
		{"empty", []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			if tc.existing != nil {
				fc.FileContents["acme/api/"+preset.OverlayPath] = tc.existing
			}
			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), convergeCfgWithDefaults(newConvergeManifest("acme/api")), newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed())

			delivered, ok := committedFile(sc, preset.OverlayPath)
			require.True(t, ok)
			assert.NotEmpty(t, delivered, "generated overlay must be written")
			assert.Contains(t, string(delivered), "triage")

			_, kept := overlayAction(result.Results[0].Actions)
			assert.False(t, kept, "no keep action without an existing file")
		})
	}
}
