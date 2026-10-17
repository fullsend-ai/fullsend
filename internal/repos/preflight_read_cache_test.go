package repos

import (
	"context"
	"errors"
	"maps"
	"sort"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readCountingClient counts the repository-state reads that reach the
// wrapped client, keyed "<kind>:<target>", so tests can assert exact
// forge-call counts (#8501).
type readCountingClient struct {
	forge.Client

	mu    sync.Mutex
	calls map[string]int
}

func newReadCountingClient(c forge.Client) *readCountingClient {
	return &readCountingClient{Client: c, calls: make(map[string]int)}
}

func (c *readCountingClient) record(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[key]++
}

func (c *readCountingClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	c.record("file:" + owner + "/" + repo + "/" + path)
	return c.Client.GetFileContent(ctx, owner, repo, path)
}

func (c *readCountingClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	c.record("var:" + owner + "/" + repo + "/" + name)
	return c.Client.GetRepoVariable(ctx, owner, repo, name)
}

func (c *readCountingClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	c.record("secret:" + owner + "/" + repo + "/" + name)
	return c.Client.RepoSecretExists(ctx, owner, repo, name)
}

func (c *readCountingClient) ListOrgRepos(ctx context.Context, org string, includePrivate bool) ([]forge.Repository, error) {
	c.record("list:" + org)
	return c.Client.ListOrgRepos(ctx, org, includePrivate)
}

// total returns the number of counted reads.
func (c *readCountingClient) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

// count returns the number of reads of key.
func (c *readCountingClient) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

// repeated returns the keys read more than once, sorted.
func (c *readCountingClient) repeated() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var keys []string
	for k, v := range c.calls {
		if v > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func (c *readCountingClient) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = make(map[string]int)
}

func TestPreflightReadCache_FileContent(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/a.yaml"] = []byte("one")
	fc.FileContents["acme/api/empty.yaml"] = []byte{}
	counter := newReadCountingClient(fc)
	cfg, err := NewPreflightReadCache(newTestClientFactory(counter)).ConfigFor(ForgeGitHub)
	require.NoError(t, err)

	for range 3 {
		data, err := cfg.Client.GetFileContent(ctx, "acme", "api", "a.yaml")
		require.NoError(t, err)
		assert.Equal(t, "one", string(data))
	}
	assert.Equal(t, 1, counter.count("file:acme/api/a.yaml"))

	// Callers get their own copy: mutating one answer does not change the next.
	data, err := cfg.Client.GetFileContent(ctx, "acme", "api", "a.yaml")
	require.NoError(t, err)
	data[0] = 'X'
	again, err := cfg.Client.GetFileContent(ctx, "acme", "api", "a.yaml")
	require.NoError(t, err)
	assert.Equal(t, "one", string(again))

	// A zero-byte file stays found on a cached read.
	for range 2 {
		_, found, err := readExistingFile(ctx, cfg.Client, "acme", "api", "empty.yaml")
		require.NoError(t, err)
		assert.True(t, found)
	}
	assert.Equal(t, 1, counter.count("file:acme/api/empty.yaml"))

	// Not-found answers are cached too.
	for range 2 {
		_, err := cfg.Client.GetFileContent(ctx, "acme", "api", "missing.yaml")
		require.Error(t, err)
		assert.True(t, forge.IsNotFound(err))
	}
	assert.Equal(t, 1, counter.count("file:acme/api/missing.yaml"))

	// Any other error is returned and retried, never cached.
	boom := errors.New("boom")
	fc.GetFileContentErrors = map[string]error{"acme/api/flaky.yaml": boom}
	for range 2 {
		_, err := cfg.Client.GetFileContent(ctx, "acme", "api", "flaky.yaml")
		require.ErrorIs(t, err, boom)
	}
	assert.Equal(t, 2, counter.count("file:acme/api/flaky.yaml"))
}

func TestPreflightReadCache_VariablesSecretsAndListings(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClientForBatch("acme/api", "acme/web")
	fc.VariableValues = map[string]string{"acme/api/" + forge.VarMintURL: "https://mint.example.com"}
	fc.Secrets = map[string]bool{"acme/api/" + forge.SecretGCPProjectID: true}
	counter := newReadCountingClient(fc)
	clients := NewPreflightReadCache(newTestClientFactory(counter))

	for range 2 {
		// Each ConfigFor call shares the same cache for a forge.
		cfg, err := clients.ConfigFor(ForgeGitHub)
		require.NoError(t, err)
		val, ok, err := cfg.Client.GetRepoVariable(ctx, "acme", "api", forge.VarMintURL)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "https://mint.example.com", val)
		_, ok, err = cfg.Client.GetRepoVariable(ctx, "acme", "api", forge.VarAppSet)
		require.NoError(t, err)
		assert.False(t, ok)
		exists, err := cfg.Client.RepoSecretExists(ctx, "acme", "api", forge.SecretGCPProjectID)
		require.NoError(t, err)
		assert.True(t, exists)
		repos, err := cfg.Client.ListOrgRepos(ctx, "acme", true)
		require.NoError(t, err)
		assert.Len(t, repos, 2)
		repos[0].FullName = "mutated"
	}
	assert.Equal(t, 1, counter.count("var:acme/api/"+forge.VarMintURL))
	assert.Equal(t, 1, counter.count("var:acme/api/"+forge.VarAppSet))
	assert.Equal(t, 1, counter.count("secret:acme/api/"+forge.SecretGCPProjectID))
	assert.Equal(t, 1, counter.count("list:acme"))
	cfg, err := clients.ConfigFor(ForgeGitHub)
	require.NoError(t, err)
	repos, err := cfg.Client.ListOrgRepos(ctx, "acme", true)
	require.NoError(t, err)
	assert.Equal(t, "acme/api", repos[0].FullName, "callers get their own copy of a cached listing")

	// Errors are returned and retried, never cached.
	boom := errors.New("boom")
	fc.Errors = map[string]error{"GetRepoVariable": boom, "RepoSecretExists": boom, "ListOrgRepos": boom}
	for range 2 {
		_, _, err := cfg.Client.GetRepoVariable(ctx, "acme", "web", forge.VarMintURL)
		require.ErrorIs(t, err, boom)
		_, err = cfg.Client.RepoSecretExists(ctx, "acme", "web", forge.SecretGCPProjectID)
		require.ErrorIs(t, err, boom)
		_, err = cfg.Client.ListOrgRepos(ctx, "other", true)
		require.ErrorIs(t, err, boom)
	}
	assert.Equal(t, 2, counter.count("var:acme/web/"+forge.VarMintURL))
	assert.Equal(t, 2, counter.count("secret:acme/web/"+forge.SecretGCPProjectID))
	assert.Equal(t, 2, counter.count("list:other"))
}

func TestPreflightReadCache_FactoryPassthrough(t *testing.T) {
	_, err := NewPreflightReadCache(failingFactory{}).ConfigFor(ForgeGitHub)
	require.Error(t, err)

	cfg, err := NewPreflightReadCache(nilClientFactory{}).ConfigFor(ForgeGitHub)
	require.NoError(t, err)
	assert.Nil(t, cfg.Client, "a pattern-only config keeps its nil client")
}

type nilClientFactory struct{}

func (nilClientFactory) ConfigFor(forgeName string) (ForgeConfig, error) {
	return ForgeConfigFor(forgeName), nil
}

// repoStates are the installation states the install-default decision
// distinguishes: fresh (nothing installed), partially installed (a thin
// caller but no shim workflow) and installed (shim workflow present).
func repoStates(t *testing.T) map[string]map[string][]byte {
	t.Helper()
	thinCallers := scaffold.PerRepoThinCallerPaths()
	require.NotEmpty(t, thinCallers)
	return map[string]map[string][]byte{
		"fresh":               nil,
		"partially installed": {"acme/api/" + thinCallers[0]: []byte("caller")},
		"installed":           {"acme/api/.github/workflows/fullsend.yaml": []byte("shim")},
	}
}

// An explicit create_issues setting or a declared preset rules out the
// initial create_issues writeback, so EnsureManagedConfigDefaults decides
// without any forge read (#8501). Before #8501 the same repositories each
// cost an overlay read, a base read and a full component probe (workflow
// files, thin callers, variables and secrets) just to discover that no
// default could be added — the same reads the pristine case below makes.
func TestEnsureManagedConfigDefaults_NoForgeReadsWhenNoDefaultPossible(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	shapes := map[string]func(t *testing.T, m *Manifest){
		"explicit create_issues": func(t *testing.T, m *Manifest) {
			m.GitHub.Repos[0].Config = mustManagedConfig(t, "create_issues:\n  allow_targets:\n    repos: [acme/other]\n")
		},
		"explicit defaults create_issues": func(t *testing.T, m *Manifest) {
			m.Defaults.Config = mustManagedConfig(t, "create_issues:\n  allow_targets:\n    orgs: [acme]\n")
		},
		"declared preset": func(_ *testing.T, m *Manifest) {
			m.Defaults.ConfigBase.Source = presetPath
		},
	}
	for shapeName, shape := range shapes {
		for stateName, files := range repoStates(t) {
			t.Run(shapeName+"/"+stateName, func(t *testing.T) {
				m := newConvergeManifest("acme/api")
				shape(t, m)
				fc := newFakeClientForBatch("acme/api")
				maps.Copy(fc.FileContents, files)
				counter := newReadCountingClient(fc)
				names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m},
					newTestClientFactory(counter), nil)
				require.NoError(t, err)
				assert.Empty(t, names)
				assert.Zero(t, counter.total(), "no forge read is needed to rule out the default: %v", counter.calls)
			})
		}
	}
}

// Without a preset or an explicit create_issues setting the decision still
// reads the layers and probes components, and only a pristine repository
// gains the upstream issue target.
func TestEnsureManagedConfigDefaults_ProbesOnlyWhenDefaultPossible(t *testing.T) {
	for stateName, files := range repoStates(t) {
		t.Run(stateName, func(t *testing.T) {
			m := newConvergeManifest("acme/api")
			fc := newFakeClientForBatch("acme/api")
			maps.Copy(fc.FileContents, files)
			counter := newReadCountingClient(fc)
			names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m},
				newTestClientFactory(counter), nil)
			require.NoError(t, err)
			assert.Equal(t, 1, counter.count("file:acme/api/"+preset.OverlayPath))
			assert.Equal(t, 1, counter.count("file:acme/api/"+preset.BasePath))
			assert.Equal(t, 1, counter.count("var:acme/api/"+forge.VarMintURL), "the component probe ran")
			if stateName == "fresh" {
				assert.Equal(t, []string{"acme/api"}, names, "a pristine install needs the initial writeback")
				assert.True(t, m.GitHub.Repos[0].Config.IsSet())
			} else {
				assert.Empty(t, names, "an existing or partial install never gains the permission implicitly")
				assert.False(t, m.GitHub.Repos[0].Config.IsSet())
			}
		})
	}
}

// runInstallPreflights runs the read-only configuration preflights in the
// order `repos install` does, all through clients. Repositories that need
// adoption are returned and skipped by the later preflights, as install
// skips them (#8511).
func runInstallPreflights(ctx context.Context, m *Manifest, clients ForgeClientFactory) ([]string, []AdoptionRequiredRepo, error) {
	adoption, err := PreflightManagedConfig(ctx, m, clients, nil)
	if err != nil {
		return nil, nil, err
	}
	skip := make([]string, 0, len(adoption))
	for _, a := range adoption {
		skip = append(skip, a.Name())
	}
	names, err := EnsureManagedConfigDefaults(ctx, ManagedConfigWritebackConfig{Manifest: m, DryRun: true, Deferred: true, ExcludeRepos: skip}, clients, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := PreflightProposedManagedSafety(ctx, m, clients, nil, skip); err != nil {
		return names, adoption, err
	}
	return names, adoption, PreflightEffectiveInfrastructure(ctx, m, clients, nil, skip, "", "")
}

// behaviourTestManifest is the entry shape the behaviour-test install
// driver generates for `repos install` (#8483) with create_issues declared.
func behaviourTestManifest(t *testing.T) *Manifest {
	t.Helper()
	m := newConvergeManifest("acme/api")
	m.GitHub.MintURL = ""
	m.GitHub.Repos[0].Config = mustManagedConfig(t, "runtime: dummy\nmint_url: https://mint.example.com\n"+
		"create_issues:\n  allow_targets:\n    repos: [fullsend-ai/fullsend]\n")
	return m
}

// The preflights share one read cache in `repos install`, so each
// repository-state read is made once across them (#8501). Request counts
// for one fresh GitHub repository are recorded below for the pristine
// no-preset shape that needs the initial writeback and for the #8483
// behaviour-test shape with create_issues declared. The 9 distinct reads
// are the overlay, the base, two shim workflow paths, one thin caller, two
// variables and two secrets. Before #8501 both shapes made 21 reads: the
// overlay 4 times, the base 3 times and every probed component twice.
func TestInstallPreflights_SharedCacheReadsEachStateOnce(t *testing.T) {
	cases := []struct {
		name     string
		manifest func(t *testing.T) *Manifest
		// before is the request count without the shared cache and
		// without the early install-default decision (pre-#8501).
		before int
		// uncached is the request count without the shared cache: the
		// early decision alone.
		uncached int
		// cached is the request count `repos install` now makes.
		cached    int
		writeback bool
	}{
		{name: "pristine no-preset install", manifest: func(*testing.T) *Manifest { return newConvergeManifest("acme/api") },
			before: 21, uncached: 21, cached: 9, writeback: true},
		{name: "behaviour-test manifest with create_issues", manifest: behaviourTestManifest,
			before: 21, uncached: 12, cached: 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			counter := newReadCountingClient(newFakeClientForBatch("acme/api"))
			names, _, err := runInstallPreflights(ctx, tc.manifest(t), newTestClientFactory(counter))
			require.NoError(t, err)
			assert.Equal(t, tc.writeback, len(names) == 1)
			assert.Equal(t, tc.uncached, counter.total(), "uncached reads: %v", counter.calls)
			if tc.writeback {
				assert.Equal(t, tc.before, counter.total(), "a needed writeback still probes")
			} else {
				assert.Less(t, counter.total(), tc.before, "the early decision skips the writeback reads")
			}

			counter.reset()
			names, _, err = runInstallPreflights(ctx, tc.manifest(t), NewPreflightReadCache(newTestClientFactory(counter)))
			require.NoError(t, err)
			assert.Equal(t, tc.writeback, len(names) == 1)
			assert.Empty(t, counter.repeated(), "every state is read once across the preflights")
			assert.Equal(t, tc.cached, counter.total(), "cached reads: %v", counter.calls)
			t.Logf("preflight forge reads for one fresh GitHub repo: before #8501 %d, now %d", tc.before, counter.total())
		})
	}
}

// A shared cache does not hide a problem from a later preflight: each
// rejection still fails the preflights before any write, names the
// repository and condition, and leaves the forge unwritten.
func TestInstallPreflights_SharedCacheKeepsRejections(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		expect string
	}{
		{"undeclared base", func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\n")
		}, preset.BasePath},
		{"unsafe configuration change", func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: true\n")
		}, "kill_switch"},
		{"infrastructure conflict", func(t *testing.T, m *Manifest, _ *forge.FakeClient) {
			m.GitHub.Repos[0].Config = mustManagedConfig(t, "mint_url: https://other.example.com\n"+
				"create_issues:\n  allow_targets:\n    repos: [acme/other]\n")
			m.GitHub.Repos[0].MintURL = "https://mint.example.com"
		}, "mint URL conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newConvergeManifest("acme/api")
			fc := newFakeClientForBatch("acme/api")
			tc.setup(t, m, fc)
			before := len(fc.FileContents)
			_, _, err := runInstallPreflights(context.Background(), m, NewPreflightReadCache(newTestClientFactory(fc)))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "acme/api")
			assert.Contains(t, err.Error(), tc.expect)
			assert.Len(t, fc.FileContents, before, "a failed preflight writes nothing")
			assertNoForgeWrites(t, fc)
		})
	}
}

// A markerless overlay is a failure of that repository only: the shared
// cache preflights return it in the adoption set and the later preflights
// skip it, so the other repository still gets its install default (#8511).
func TestInstallPreflights_SharedCacheReportsAdoptionPerRepo(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	fc := newFakeClientForBatch("acme/api", "acme/web")
	original := []byte("runtime: claude\n")
	fc.FileContents["acme/api/"+preset.OverlayPath] = original

	names, adoption, err := runInstallPreflights(context.Background(), m, NewPreflightReadCache(newTestClientFactory(fc)))
	require.NoError(t, err)
	require.Len(t, adoption, 1)
	assert.Equal(t, "acme/api", adoption[0].Name())
	assert.Contains(t, adoption[0].Guidance, "adoption required")
	assert.Contains(t, adoption[0].Guidance, "proposed repos.yaml entry")
	assert.Equal(t, []string{"acme/web"}, names, "only the repository being installed gains the install default")
	assert.Equal(t, original, fc.FileContents["acme/api/"+preset.OverlayPath])
	assertNoForgeWrites(t, fc)
}
