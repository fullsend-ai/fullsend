package cli

import (
	"context"
	"os"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A read-only manifest must fail the glob carve-out path, including with
// other per-entry flags, before any manifest write is attempted and with
// the exact entry to add. Dry run and live install agree (#8218).
func TestRunReposInstall_ReadOnlyManifestGlobCarveFailsWithSuggestedEdit(t *testing.T) {
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

			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.dryRun = dryRun
			opts.repoFilter = []string{"acme/api"}
			opts.forge = repos.ForgeGitHub
			opts.fullsendRef = "v2.0.0"
			opts.mintURL = "https://mint-override.example.com"
			opts.allowedRemoteResources = []string{"https://github.com/acme/"}
			useOpenAIInputs(opts)

			err = runReposInstall(context.Background(), opts)
			require.Error(t, err)
			msg := err.Error()
			assert.Contains(t, msg, "read-only")
			for _, want := range []string{"github.repos", "name: acme/api", "fullsend_ref: v2.0.0", "mint_url: https://mint-override.example.com",
				"https://github.com/acme/", "- fullsend-ai/fullsend"} {
				assert.Contains(t, msg, want)
			}
			after, err := os.ReadFile(manifestPath)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "the manifest was not written")
		})
	}
}

// The suggested entry for a read-only manifest must carry --inference-auth
// and --app-set together with another carve-triggering flag, so applying
// it reproduces the requested install (#8218).
func TestRunReposInstall_ReadOnlyManifestGlobCarveSuggestsInferenceAuthAndAppSet(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	require.NoError(t, os.Chmod(manifestPath, 0o444))
	fc := newInstallFakeClient("acme/api", "acme/web")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	opts.forge = repos.ForgeGitHub
	opts.fullsendRef = "v2.0.0"
	useOpenAIInputs(opts)
	opts.inferenceAuth = "vertex-wif"
	opts.appSet = "myset"

	err := runReposInstall(context.Background(), opts)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "read-only")
	for _, want := range []string{"name: acme/api", "fullsend_ref: v2.0.0", "auth: vertex-wif", "app_set: myset"} {
		assert.Contains(t, msg, want)
	}
	assert.NotContains(t, msg, "openai-api-key", "the glob's inference.auth must be replaced by the requested one")
}
