package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// githubLegacyTrackingManifestYAML declares issue tracking with the
// pre-#9074 scalar tracker and create_issues keys.
const githubLegacyTrackingManifestYAML = `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  inference:
    auth: vertex-wif
  repos:
    - name: acme/api
      config:
        kill_switch: true
        tracker: github
        create_issues:
          allow_targets:
            orgs: [acme]
            repos: [other/tools]
`

// legacyManagedOverlay is the managed .fullsend/config.yaml an earlier
// install rendered from githubLegacyTrackingManifestYAML.
const legacyManagedOverlay = managedConfigMarker + `kill_switch: true
tracker: github
create_issues:
  allow_targets:
    orgs:
      - acme
    repos:
      - other/tools
`

// TestRunReposInstall_MigratesLegacyTracking: install converts the legacy
// tracker/create_issues manifest config to the tracker mapping, persists
// it in repos.yaml, re-renders the managed overlay from it with the same
// targets, and a second install changes neither (#9075).
func TestRunReposInstall_MigratesLegacyTracking(t *testing.T) {
	manifestPath := writeTestManifest(t, githubLegacyTrackingManifestYAML)
	fc := newInstallFakeClientWithOverlay(t, "acme/api", []byte(legacyManagedOverlay))

	require.NoError(t, runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)))

	written, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.NotContains(t, string(written), "create_issues")
	m, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	assert.Empty(t, m.LegacyTrackingMigrations(), "the persisted manifest needs no further conversion")

	overlay := string(fc.FileContents[overlayPath("acme/api")])
	require.True(t, strings.HasPrefix(overlay, managedConfigMarker))
	assert.NotContains(t, overlay, "create_issues")
	assert.Contains(t, overlay, "tracker:\n    github:\n        allowed_targets:\n            orgs:\n                - acme\n            repos:\n                - other/tools\n")
	assert.Contains(t, overlay, "kill_switch: true")

	require.NoError(t, runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)))
	again, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, string(written), string(again), "a second install leaves repos.yaml unchanged")
	assert.Equal(t, overlay, string(fc.FileContents[overlayPath("acme/api")]), "a second install leaves the overlay unchanged")
}

func TestRunReposInstall_LegacyTrackingDryRunLeavesManifest(t *testing.T) {
	manifestPath := writeTestManifest(t, githubLegacyTrackingManifestYAML)
	fc := newInstallFakeClientWithOverlay(t, "acme/api", []byte(legacyManagedOverlay))
	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.dryRun = true

	require.NoError(t, runReposInstall(context.Background(), opts))

	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, githubLegacyTrackingManifestYAML, string(data))
	assert.Equal(t, legacyManagedOverlay, string(fc.FileContents[overlayPath("acme/api")]))
}

func TestRunReposInstall_LegacyTrackingJiraRepoTargetsFail(t *testing.T) {
	manifest := strings.Replace(githubLegacyTrackingManifestYAML, "tracker: github", "tracker: jira", 1)
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github.repos[acme/api].config")
	assert.Contains(t, err.Error(), "tracker.jira.allowed_targets.projects")
	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, manifest, string(data))
}

func TestReportLegacyTrackingMigrations(t *testing.T) {
	migrations := []repos.LegacyTrackingMigration{{
		Path:   "github.repos[acme/api].config",
		Result: "tracker.github.allowed_targets: orgs=[acme] repos=unset",
	}}
	for _, tc := range []struct {
		name     string
		writable bool
		dryRun   bool
		want     string
	}{
		{name: "written", writable: true, want: "Converted the legacy tracker/create_issues configuration in repos.yaml"},
		{name: "dry run", writable: true, dryRun: true, want: "Would convert the legacy tracker/create_issues configuration in repos.yaml"},
		{name: "read-only", want: "cannot be written; this run uses the converted values"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			reportLegacyTrackingMigrations(ui.New(&buf), "repos.yaml", migrations, tc.writable, tc.dryRun)
			assert.Contains(t, buf.String(), tc.want)
			assert.Contains(t, buf.String(), "github.repos[acme/api].config: tracker.github.allowed_targets: orgs=[acme] repos=unset")
		})
	}
}
