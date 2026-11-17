package cli

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// lateRepoClient adds a repository to the fake organization listing on the
// first file read, which the configuration preflights make right after
// expanding the manifest's globs. Every later listing names the new
// repository, as when it is created in the org while install runs.
type lateRepoClient struct {
	*forge.FakeClient
	late forge.Repository
	once sync.Once
}

func (c *lateRepoClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	c.once.Do(func() { c.Repos = append(c.Repos, c.late) })
	return c.FakeClient.GetFileContent(ctx, owner, repo, path)
}

// Convergence acts on the glob expansion the preflights checked: a
// repository that appears in a glob org after the preflights listed it is
// not converged (#9571).
func TestRunReposInstall_GlobRepoCreatedAfterPreflightsIsNotConverged(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")
	var late forge.Repository
	for i, r := range fc.Repos {
		if r.FullName == "acme/web" {
			late = r
			fc.Repos = append(fc.Repos[:i:i], fc.Repos[i+1:]...)
			break
		}
	}
	require.Equal(t, "acme/web", late.FullName)
	client := &lateRepoClient{FakeClient: fc, late: late}

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.testClient = client
	require.NoError(t, runReposInstall(context.Background(), opts))

	assert.True(t, fc.Secrets["acme/api/FULLSEND_OPENAI_API_KEY"], "the preflighted repository is converged")
	assert.False(t, fc.Secrets["acme/web/FULLSEND_OPENAI_API_KEY"], "a repository the preflights never checked is not converged")
	assert.Len(t, client.Repos, 2, "the late repository is in later listings")
}
