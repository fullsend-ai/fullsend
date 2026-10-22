package cli

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installReadCounter counts the repository-state reads `repos install`
// makes through the forge client, keyed "<kind>:<target>" (#8501).
type installReadCounter struct {
	forge.Client

	mu    sync.Mutex
	calls map[string]int
}

func newInstallReadCounter(c forge.Client) *installReadCounter {
	return &installReadCounter{Client: c, calls: make(map[string]int)}
}

func (c *installReadCounter) record(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[key]++
}

func (c *installReadCounter) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

func (c *installReadCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

func (c *installReadCounter) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	c.record("file:" + owner + "/" + repo + "/" + path)
	return c.Client.GetFileContent(ctx, owner, repo, path)
}

func (c *installReadCounter) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	c.record("var:" + owner + "/" + repo + "/" + name)
	return c.Client.GetRepoVariable(ctx, owner, repo, name)
}

func (c *installReadCounter) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	c.record("secret:" + owner + "/" + repo + "/" + name)
	return c.Client.RepoSecretExists(ctx, owner, repo, name)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// behaviourTestManifestYAML is the entry shape the behaviour-test install
// driver generates for `repos install` (#8483), with create_issues declared.
const behaviourTestManifestYAML = `version: 1
github:
  repos:
    - name: acme/api
      fullsend_ref: v1.0.0
      inference:
        auth: vertex-wif
      config:
        runtime: dummy
        mint_url: https://mint.example.com
        create_issues:
          allow_targets:
            repos: [fullsend-ai/fullsend]
`

// The read-only preflights of `repos install` share one read cache, so a
// fresh repository's configuration layers and installation components are
// read once before the manifest write. Convergence still makes its own
// final check through uncached clients after that write, in both dry-run
// and normal installs, and FULLSEND_GCP_REGION is read once per install.
//
// Request counts (file, variable and secret reads) for one fresh GitHub
// install, before #8501 → now, are the same for both manifest shapes:
// 38 → 26 for a normal install and 37 → 25 for a dry run (which skips the
// .gitlint read of the scaffold commit). Per key: the overlay 6 → 3, the
// base 5 → 3, each shim workflow path, thin caller, inference secret and
// FULLSEND_MINT_URL 3 → 2, FULLSEND_APP_SET 4 → 3. The remaining reads of
// each key are convergence's own, made after the manifest write.
func TestRunReposInstall_PreflightReadsOnceConvergenceRechecks(t *testing.T) {
	overlayKey := "file:acme/api/" + preset.OverlayPath
	baseKey := "file:acme/api/" + preset.BasePath
	workflowKey := "file:acme/api/.github/workflows/fullsend.yaml"
	mintVarKey := "var:acme/api/" + forge.VarMintURL
	regionKey := "var:acme/api/" + forge.VarGCPRegion

	cases := []struct {
		name     string
		manifest string
		dryRun   bool
		// writeback is whether the install persists the initial
		// create_issues default (a manifest write before convergence).
		writeback bool
		total     int
	}{
		{name: "pristine no-preset install", manifest: githubUnmanagedManifestYAML, writeback: true, total: 26},
		{name: "pristine no-preset dry run", manifest: githubUnmanagedManifestYAML, dryRun: true, writeback: true, total: 25},
		{name: "behaviour-test manifest", manifest: behaviourTestManifestYAML, total: 26},
		{name: "behaviour-test manifest dry run", manifest: behaviourTestManifestYAML, dryRun: true, total: 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, tc.manifest)
			fc := newInstallFakeClient("acme/api")
			counter := newInstallReadCounter(fc)
			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.testClient = counter
			opts.dryRun = tc.dryRun
			require.NoError(t, runReposInstall(context.Background(), opts))

			// One shared preflight read plus convergence's own reads.
			assert.Equal(t, 3, counter.count(overlayKey), "overlay reads: %v", counter.calls)
			assert.Equal(t, 3, counter.count(baseKey), "base reads: %v", counter.calls)
			assert.Equal(t, 2, counter.count(workflowKey), "workflow reads: %v", counter.calls)
			assert.Equal(t, 2, counter.count(mintVarKey), "mint URL reads: %v", counter.calls)
			assert.Equal(t, tc.total, counter.total(), "all reads: %v", counter.calls)
			// The undeclared region is looked up once, by convergence.
			assert.Equal(t, 1, counter.count(regionKey), "region reads: %v", counter.calls)

			overlay := string(fc.FileContents[overlayPath("acme/api")])
			reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
			require.NoError(t, err)
			if tc.dryRun {
				assert.Empty(t, overlay, "a dry run writes nothing")
				assert.Equal(t, tc.manifest, mustReadFile(t, manifestPath), "a dry run leaves the manifest unchanged")
				return
			}
			// Convergence rendered the overlay from the manifest written
			// after the preflights, not from a preflight snapshot.
			assert.Contains(t, overlay, "fullsend-ai/fullsend")
			assert.Equal(t, tc.writeback, tc.manifest != mustReadFile(t, manifestPath))
			assert.True(t, reloaded.GitHub.Repos[0].Config.IsSet())
		})
	}
}

// A declared region is read by convergence's component probe and nowhere
// else; an existing undeclared region is preserved from a single lookup.
func TestRunReposInstall_RegionReadOnce(t *testing.T) {
	regionKey := "var:acme/api/" + forge.VarGCPRegion

	t.Run("declared region", func(t *testing.T) {
		manifest := strings.Replace(behaviourTestManifestYAML, "        runtime: dummy\n",
			"        runtime: dummy\n        inference:\n          region: europe-west4\n", 1)
		manifestPath := writeTestManifest(t, manifest)
		fc := newInstallFakeClient("acme/api")
		counter := newInstallReadCounter(fc)
		opts := githubManagedInstallOpts(manifestPath, fc)
		opts.testClient = counter
		require.NoError(t, runReposInstall(context.Background(), opts))
		assert.Equal(t, 1, counter.count(regionKey))
		assert.Equal(t, "europe-west4", fc.VariableValues["acme/api/"+forge.VarGCPRegion])
	})
	t.Run("existing undeclared region is preserved", func(t *testing.T) {
		manifestPath := writeTestManifest(t, behaviourTestManifestYAML)
		fc := newInstallFakeClient("acme/api")
		fc.VariableValues = map[string]string{"acme/api/" + forge.VarGCPRegion: "asia-east1"}
		counter := newInstallReadCounter(fc)
		opts := githubManagedInstallOpts(manifestPath, fc)
		opts.testClient = counter
		require.NoError(t, runReposInstall(context.Background(), opts))
		assert.Equal(t, 1, counter.count(regionKey))
		assert.Equal(t, "asia-east1", fc.VariableValues["acme/api/"+forge.VarGCPRegion])
	})
}
