package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --adopt replaces an installed repository's markerless overlay with the
// managed file (marker included) when the safety gate passes, and a second
// run finds it current (#8512). A dry run shows the plan and writes nothing.
func TestRunReposInstall_AdoptReplacesMarkerlessOverlay(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "dry run"}[dryRun], func(t *testing.T) {
			manifestPath := writeTestManifest(t, githubManagedManifestYAML)
			fc := newInstalledFakeClientCLI("acme/api")
			original := []byte("kill_switch: false\n# hand-authored\n")
			fc.FileContents[overlayPath("acme/api")] = original

			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.adopt = []string{"acme/api"}
			opts.dryRun = dryRun
			var err error
			out := captureStdout(t, func() {
				err = runReposInstall(context.Background(), opts)
			})
			require.NoError(t, err, out)
			assert.Contains(t, out, "[acme/api] "+preset.OverlayPath+" exists without the managed-configuration ownership marker; adopting it (--adopt, ADR-0122)")
			assert.Contains(t, out, "file settings difference")
			assert.Contains(t, out, "+ kill_switch: true")
			assert.Contains(t, out, "with --adopt, `fullsend repos install` writes the managed file")
			assert.NotContains(t, out, "[acme/api] skipped:")
			assert.NotContains(t, out, "nothing to adopt")
			data, readErr := os.ReadFile(manifestPath)
			require.NoError(t, readErr)
			assert.Equal(t, githubManagedManifestYAML, string(data), "--adopt is never written to the manifest")

			if dryRun {
				assert.Contains(t, out, "would adopt "+preset.OverlayPath)
				assert.Equal(t, original, fc.FileContents[overlayPath("acme/api")], "a dry run writes nothing")
				return
			}
			overlay := fc.FileContents[overlayPath("acme/api")]
			require.True(t, bytes.HasPrefix(overlay, []byte(managedConfigMarker)), "the adopted overlay carries the marker, got %q", overlay)
			assert.Contains(t, string(overlay), "kill_switch: true")
			assert.NotContains(t, string(overlay), "hand-authored")

			// A second run, with or without --adopt, finds the file current.
			for _, adopt := range [][]string{{"acme/api"}, nil} {
				again := githubManagedInstallOpts(manifestPath, fc)
				again.adopt = adopt
				out = captureStdout(t, func() {
					err = runReposInstall(context.Background(), again)
				})
				require.NoError(t, err, out)
				assert.Equal(t, overlay, fc.FileContents[overlayPath("acme/api")])
				assert.NotContains(t, out, "adoption required")
				assert.NotContains(t, out, "adopted "+preset.OverlayPath)
			}
			assert.Contains(t, out, "already current")
		})
	}
}

// A repository that is not installed yet takes the adopted managed overlay
// in its fresh install instead of being blocked.
func TestRunReposInstall_AdoptOnFreshInstall(t *testing.T) {
	manifestPath := writeTestManifest(t, githubManagedManifestYAML)
	fc := newInstallFakeClientWithOverlay(t, "acme/api", []byte{})

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.adopt = []string{"ACME/api"}
	var err error
	out := captureStdout(t, func() {
		err = runReposInstall(context.Background(), opts)
	})
	require.NoError(t, err, out)
	overlay := fc.FileContents[overlayPath("acme/api")]
	assert.True(t, bytes.HasPrefix(overlay, []byte(managedConfigMarker)), "the zero-byte overlay is adopted, got %q", overlay)
	assert.Contains(t, out, "1 installed")
}

// The safety gate still applies with --adopt: a managed file that would
// drop the existing kill switch without a manifest declaration is rejected,
// the keys are named, and nothing is written.
func TestRunReposInstall_AdoptSafetyGateRejectsRelaxation(t *testing.T) {
	for name, fc := range map[string]*forge.FakeClient{
		"installed": newInstalledFakeClientCLI("acme/api"),
		"fresh":     newInstallFakeClient("acme/api"),
	} {
		t.Run(name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
			original := []byte("kill_switch: true\n")
			fc.FileContents[overlayPath("acme/api")] = original

			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.adopt = []string{"acme/api"}
			var err error
			out := captureStdout(t, func() {
				err = runReposInstall(context.Background(), opts)
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "kill_switch")
			assert.Contains(t, err.Error(), "less restrictive")
			assert.NotContains(t, out, "adopted "+preset.OverlayPath)
			assert.Equal(t, original, fc.FileContents[overlayPath("acme/api")], "a rejected adoption leaves the file untouched")
			data, readErr := os.ReadFile(manifestPath)
			require.NoError(t, readErr)
			assert.Equal(t, githubUnmanagedManifestYAML, string(data), "a rejected adoption leaves the manifest untouched")
		})
	}
}

// Only the named repository is adopted: another markerless repository is
// still skipped as adoption required, and the run exits non-zero.
func TestRunReposInstall_AdoptOnlyNamedRepos(t *testing.T) {
	manifestYAML := strings.Replace(githubManagedManifestYAML, "    - name: acme/api\n",
		"    - name: acme/web\n      config:\n        kill_switch: true\n    - name: acme/api\n", 1)
	manifestPath := writeTestManifest(t, manifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	original := []byte("kill_switch: false\n")
	fc.FileContents[overlayPath("acme/api")] = original
	fc.FileContents[overlayPath("acme/web")] = original

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.adopt = []string{"acme/api"}
	var err error
	out := captureStdout(t, func() {
		err = runReposInstall(context.Background(), opts)
	})
	require.Error(t, err)
	assert.Equal(t, "1 repos failed", err.Error())
	assert.True(t, bytes.HasPrefix(fc.FileContents[overlayPath("acme/api")], []byte(managedConfigMarker)), "acme/api is adopted")
	assert.Equal(t, original, fc.FileContents[overlayPath("acme/web")], "acme/web is not adopted")
	assert.Contains(t, out, "[acme/web] skipped: ")
	assert.Contains(t, out, "re-run with --adopt acme/web")
}

// --adopt for a selected repository with no markerless overlay is a no-op
// reported as a warning.
func TestRunReposInstall_AdoptNothingToAdopt(t *testing.T) {
	manifestPath := writeTestManifest(t, githubManagedManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.adopt = []string{"acme/api", "acme/api"}
	var err error
	out := captureStdout(t, func() {
		err = runReposInstall(context.Background(), opts)
	})
	require.NoError(t, err, out)
	assert.Equal(t, 1, strings.Count(out, "nothing to adopt"), out)
	assert.Contains(t, out, "[acme/api] --adopt: no "+preset.OverlayPath+" without the ownership marker")
}

// A malformed markerless overlay is reported as an error, not adopted.
func TestRunReposInstall_AdoptMalformedOverlayFails(t *testing.T) {
	manifestPath := writeTestManifest(t, githubManagedManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")
	original := []byte("- not\n- a mapping\n")
	fc.FileContents[overlayPath("acme/api")] = original

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.adopt = []string{"acme/api"}
	err := runReposInstall(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuration preflight")
	assert.Equal(t, original, fc.FileContents[overlayPath("acme/api")])
}

// --adopt values are validated before any forge access: each must be an
// exact, declared and selected owner/repo.
func TestRunReposInstall_AdoptValidation(t *testing.T) {
	manifestYAML := strings.Replace(githubManagedManifestYAML, "    - name: acme/api\n",
		"    - name: acme/api\n    - name: acme/web\n", 1)
	for name, tc := range map[string]struct {
		filter []string
		adopt  string
		want   string
	}{
		"glob":         {adopt: "acme/*", want: "patterns are not accepted"},
		"no owner":     {adopt: "api", want: "must name one repository as owner/repo"},
		"empty":        {adopt: " ", want: "must name one repository as owner/repo"},
		"trailing":     {adopt: "acme/", want: "must name one repository as owner/repo"},
		"undeclared":   {adopt: "acme/other", want: "not declared in repos.yaml"},
		"not selected": {filter: []string{"acme/web"}, adopt: "acme/api", want: "not among the repositories selected by acme/web"},
	} {
		t.Run(name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, manifestYAML)
			fc := newInstalledFakeClientCLI("acme/api", "acme/web")
			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.repoFilter = tc.filter
			opts.adopt = []string{tc.adopt}
			var err error
			captureStdout(t, func() {
				err = runReposInstall(context.Background(), opts)
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "nothing was changed")
		})
	}
}

func TestReposInstallCmd_AdoptFlag(t *testing.T) {
	cmd := newReposInstallCmd()
	flag := cmd.Flags().Lookup("adopt")
	require.NotNil(t, flag)
	assert.Equal(t, "stringArray", flag.Value.Type())
}
