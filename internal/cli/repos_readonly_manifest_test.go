package cli

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A read-only manifest is never written by install. A first install of a
// glob-covered repository that needs the upstream create_issues default
// fails before any write with the exact entry to add; dry run and live
// install agree (#8218). Selecting the repository adds nothing itself
// (#8219).
func TestRunReposInstall_ReadOnlyManifestGlobCoveredFirstInstallSuggestsEdit(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		name := "live"
		if dryRun {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
			require.NoError(t, os.Chmod(manifestPath, 0o444))
			before, err := os.ReadFile(manifestPath)
			require.NoError(t, err)
			fc := newInstallFakeClient("acme/api", "acme/web")

			opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
			opts.dryRun = dryRun
			opts.repoFilter = []string{"acme/api"}

			err = runReposInstall(context.Background(), opts)
			require.Error(t, err)
			msg := err.Error()
			assert.Contains(t, msg, "read-only")
			for _, want := range []string{"github.repos", "name: acme/api", "- fullsend-ai/fullsend"} {
				assert.Contains(t, msg, want)
			}
			after, err := os.ReadFile(manifestPath)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "the manifest was not written")
			assertNoInstallWrites(t, fc)
		})
	}
}

// An installed glob-covered repository needs no manifest edit, so a
// read-only manifest converges it.
func TestRunReposInstall_ReadOnlyManifestInstalledGlobCoveredRepoConverges(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	require.NoError(t, os.Chmod(manifestPath, 0o444))
	fc := newInstalledFakeClientCLI("acme/api")
	fc.Secrets["acme/api/FULLSEND_OPENAI_API_KEY"] = true

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.openAIAPIKey = ""
	opts.repoFilter = []string{"acme/api"}
	require.NoError(t, runReposInstall(context.Background(), opts))

	after, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, globInferenceAuthManifestYAML, string(after))
}
