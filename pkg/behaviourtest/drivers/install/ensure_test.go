package install

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// fakeEnsurer is a test double for RepoEnsurer that records calls and
// returns a fixed perRepoState. It lets callers verify caching and
// call-count behaviour without a real forge client or CLI binary.
type fakeEnsurer struct {
	calls atomic.Int32
	mu    sync.Mutex
	cache map[string]State
}

func newFakeEnsurer() *fakeEnsurer {
	return &fakeEnsurer{cache: make(map[string]State)}
}

func (f *fakeEnsurer) EnsureRepo(_ context.Context, org, repoName string) (State, error) {
	f.mu.Lock()
	if st, ok := f.cache[repoName]; ok {
		f.mu.Unlock()
		return st, nil
	}
	f.mu.Unlock()

	f.calls.Add(1)
	st := &perRepoState{org: org, repo: repoName}

	f.mu.Lock()
	f.cache[repoName] = st
	f.mu.Unlock()

	return st, nil
}

var _ RepoEnsurer = (*fakeEnsurer)(nil)

func TestFakeEnsurer_ReturnsCorrectState(t *testing.T) {
	e := newFakeEnsurer()
	st, err := e.EnsureRepo(context.Background(), "org", "test-repo-01")
	require.NoError(t, err)
	assert.Equal(t, "test-repo-01", st.TestRepo())
	assert.Equal(t, "org", st.ConfigOwner())
	assert.Equal(t, "per-repo", st.Mode())
}

func TestFakeEnsurer_CachesResult(t *testing.T) {
	e := newFakeEnsurer()
	ctx := context.Background()

	st1, err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)

	st2, err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)

	// Same State pointer returned from cache.
	assert.Same(t, st1, st2)

	// Only one real ensure call.
	assert.Equal(t, int32(1), e.calls.Load())
}

func TestFakeEnsurer_IndependentRepos(t *testing.T) {
	e := newFakeEnsurer()
	ctx := context.Background()

	st1, err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)

	st2, err := e.EnsureRepo(ctx, "org", "test-repo-02")
	require.NoError(t, err)

	assert.NotSame(t, st1, st2)
	assert.Equal(t, "test-repo-01", st1.TestRepo())
	assert.Equal(t, "test-repo-02", st2.TestRepo())
	assert.Equal(t, int32(2), e.calls.Load())
}

// --- repoEnsurer unit tests (caching layer + create logic) ---

// validPerRepoConfig is the minimal YAML that passes
// config.ParsePerRepoConfig + Validate + Runtime == "dummy".
const validPerRepoConfig = `version: "1"
runtime: dummy
`

// installedStubFiles maps repo-relative paths to content. Paths not in
// the map return forge.ErrNotFound, simulating a not-yet-installed repo.
var installedStubFiles = map[string][]byte{
	".github/workflows/fullsend.yaml": []byte("# shim"),
	".fullsend/config.yaml":           []byte(validPerRepoConfig),
	scaffold.VendoredMarkerPath():     []byte("marker"),
	layers.VendoredBinaryPathPerRepo:  []byte("binary"),
}

// stubClient implements the forge.Client methods used by repoEnsurer.
type stubClient struct {
	forge.Client // embed to satisfy interface; panics on uncovered methods

	getRepoErr       error
	createRepoCalled atomic.Int32
	createFileCalled atomic.Int32

	// installed controls whether GetFileContent returns valid
	// post-install files. When false, all paths return ErrNotFound.
	installed bool
}

func (s *stubClient) GetRepo(_ context.Context, _, _ string) (*forge.Repository, error) {
	return &forge.Repository{}, s.getRepoErr
}

func (s *stubClient) CreateRepo(_ context.Context, _, _, _ string, _ bool) (*forge.Repository, error) {
	s.createRepoCalled.Add(1)
	return &forge.Repository{}, nil
}

func (s *stubClient) CreateFile(_ context.Context, _, _, _, _ string, _ []byte) error {
	s.createFileCalled.Add(1)
	return nil
}

func (s *stubClient) GetFileContent(_ context.Context, _, _, path string) ([]byte, error) {
	if !s.installed {
		return nil, forge.ErrNotFound
	}
	// Match paths case-insensitively and ignoring leading "./" for robustness.
	clean := strings.TrimPrefix(path, "./")
	if content, ok := installedStubFiles[clean]; ok {
		return content, nil
	}
	return nil, forge.ErrNotFound
}

func TestRepoEnsurer_CachesSuccessfulEnsure(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	ctx := context.Background()
	st1, err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)
	require.NotNil(t, st1)

	st2, err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)

	assert.Same(t, st1, st2, "second call should return cached State")
}

func TestRepoEnsurer_CreatesRepoWhenMissing(t *testing.T) {
	sc := &stubClient{
		getRepoErr: forge.ErrNotFound,
		installed:  true,
	}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	st, err := e.EnsureRepo(context.Background(), "org", "test-repo-05")
	require.NoError(t, err)
	assert.Equal(t, "test-repo-05", st.TestRepo())
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
	assert.Equal(t, int32(1), sc.createFileCalled.Load())
}

func TestRepoEnsurer_SkipsCreateWhenRepoExists(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	st, err := e.EnsureRepo(context.Background(), "org", "test-repo-03")
	require.NoError(t, err)
	assert.Equal(t, "test-repo-03", st.TestRepo())
	assert.Equal(t, int32(0), sc.createRepoCalled.Load(), "should not create existing repo")
}

func TestRepoEnsurer_PerRepoStateFields(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	st, err := e.EnsureRepo(context.Background(), "test-org", "test-repo-07")
	require.NoError(t, err)

	assert.Equal(t, "per-repo", st.Mode())
	assert.Equal(t, "test-repo-07", st.TestRepo())
	assert.Equal(t, "test-org", st.ConfigOwner())
	assert.Equal(t, "test-repo-07", st.ConfigRepo())
	assert.Equal(t, ".fullsend", st.ConfigPathPrefix())
	assert.Equal(t, "test-repo-07", st.TriageWorkflowRepo())
	assert.Equal(t, perRepoTriageWorkflow, st.TriageWorkflowFile())
	assert.Equal(t, perRepoAgentWorkflow, st.AgentWorkflowFile())
	assert.Equal(t, perRepoAgentArtifact, st.AgentArtifactName())
}

func TestEnsureRepoExists_AlreadyExists(t *testing.T) {
	sc := &stubClient{}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(0), sc.createRepoCalled.Load())
}

func TestEnsureRepoExists_CreatesAndSeeds(t *testing.T) {
	sc := &stubClient{getRepoErr: forge.ErrNotFound}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "test-repo-01", "org/test-repo-01")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
	assert.Equal(t, int32(1), sc.createFileCalled.Load())
}

func TestEnsureRepoExists_NonNotFoundError(t *testing.T) {
	sc := &stubClient{getRepoErr: assert.AnError}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking repo")
}
