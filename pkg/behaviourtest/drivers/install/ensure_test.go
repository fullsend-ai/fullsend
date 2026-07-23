package install

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	key := org + "/" + repoName
	f.mu.Lock()
	if st, ok := f.cache[key]; ok {
		f.mu.Unlock()
		return st, nil
	}
	f.mu.Unlock()

	f.calls.Add(1)
	st := &perRepoState{org: org, repo: repoName}

	f.mu.Lock()
	f.cache[key] = st
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
	createRepoErr    error
	createRepoCalled atomic.Int32

	// installed controls whether GetFileContent returns valid
	// post-install files. When false, all paths return ErrNotFound.
	installed bool

	// ensureDelay, when non-zero, causes GetRepo to sleep before
	// returning. Used to test concurrent singleflight behaviour.
	ensureDelay time.Duration
}

func (s *stubClient) GetRepo(_ context.Context, _, _ string) (*forge.Repository, error) {
	if s.ensureDelay > 0 {
		time.Sleep(s.ensureDelay)
	}
	return &forge.Repository{}, s.getRepoErr
}

func (s *stubClient) CreateRepo(_ context.Context, _, _, _ string, _ bool) (*forge.Repository, error) {
	s.createRepoCalled.Add(1)
	if s.createRepoErr != nil {
		return nil, s.createRepoErr
	}
	return &forge.Repository{}, nil
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

func TestNewRepoEnsurer_ReturnsNonNil(t *testing.T) {
	sc := &stubClient{}
	e := NewRepoEnsurer(e2etest.EnvConfig{}, sc, "tok", "/bin/true", t.Logf)
	require.NotNil(t, e, "NewRepoEnsurer should return a non-nil RepoEnsurer")

	// Verify the returned value implements the interface.
	var _ RepoEnsurer = e
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

func TestRepoEnsurer_CacheKeyIncludesOrg(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	ctx := context.Background()
	st1, err := e.EnsureRepo(ctx, "org-a", "test-repo-01")
	require.NoError(t, err)

	st2, err := e.EnsureRepo(ctx, "org-b", "test-repo-01")
	require.NoError(t, err)

	// Same repo name but different orgs → different cache entries.
	assert.NotSame(t, st1, st2)
	assert.Equal(t, "org-a", st1.ConfigOwner())
	assert.Equal(t, "org-b", st2.ConfigOwner())
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

func TestRepoEnsurer_InstallsWhenValidationFails(t *testing.T) {
	// Start with installed=false to simulate a repo that exists but
	// has not yet been set up with fullsend. The mock CLI runner flips
	// sc.installed to true when "github setup" is invoked, simulating
	// a successful install.
	sc := &stubClient{installed: false}
	var cliCalls [][]string
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			// Simulate install success: flip the stub to "installed".
			if len(args) > 0 && args[0] == "github" && args[1] == "setup" {
				sc.installed = true
			}
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	st, err := e.EnsureRepo(context.Background(), "org", "test-repo-10")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal(t, "test-repo-10", st.TestRepo())
	assert.Equal(t, "org", st.ConfigOwner())

	// CLI should have been called for "github setup".
	require.Len(t, cliCalls, 1, "expected exactly one CLI call (github setup)")
	assert.Equal(t, "github", cliCalls[0][0])
	assert.Equal(t, "setup", cliCalls[0][1])
	assert.Contains(t, cliCalls[0], "--mint-url")
}

func TestRepoEnsurer_DoEnsure_RepoMissing_ThenInstalled(t *testing.T) {
	// Full flow: repo missing → created, validation fails → CLI invoked,
	// re-validation passes → State cached.
	sc := &stubClient{
		getRepoErr: forge.ErrNotFound,
		installed:  false,
	}
	var cliCalls [][]string
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			if len(args) >= 2 && args[0] == "github" && args[1] == "setup" {
				sc.installed = true
			}
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	ctx := context.Background()
	st, err := e.EnsureRepo(ctx, "org", "test-repo-new")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal(t, "test-repo-new", st.TestRepo())
	assert.Equal(t, int32(1), sc.createRepoCalled.Load(), "repo should be created")
	require.Len(t, cliCalls, 1)
	assert.Equal(t, "github", cliCalls[0][0])

	// Second call should hit cache — no additional CLI calls.
	st2, err := e.EnsureRepo(ctx, "org", "test-repo-new")
	require.NoError(t, err)
	assert.Same(t, st, st2, "second call should return cached State")
	assert.Len(t, cliCalls, 1, "cached call should not invoke CLI again")
}

func TestRepoEnsurer_DoEnsure_WithGCPProject(t *testing.T) {
	// When GCPProjectID is set, provisionInference should be called
	// before github setup.
	sc := &stubClient{installed: false}
	var cliCalls [][]string
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{
			MintURL:      "https://mint.test",
			GCPProjectID: "test-project",
		},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			if len(args) >= 2 && args[0] == "github" && args[1] == "setup" {
				sc.installed = true
			}
			if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
				return `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/p/locations/l/providers/wif"}`, nil
			}
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	st, err := e.EnsureRepo(context.Background(), "org", "test-repo-gcp")
	require.NoError(t, err)
	require.NotNil(t, st)

	// Expect: inference provision, inference status, github setup (3 calls).
	require.Len(t, cliCalls, 3, "expected 3 CLI calls (provision, status, setup)")
	assert.Equal(t, "inference", cliCalls[0][0])
	assert.Equal(t, "provision", cliCalls[0][1])
	assert.Equal(t, "inference", cliCalls[1][0])
	assert.Equal(t, "status", cliCalls[1][1])
	assert.Equal(t, "github", cliCalls[2][0])
	assert.Equal(t, "setup", cliCalls[2][1])
	// Verify inference flags were threaded to github setup.
	assert.Contains(t, cliCalls[2], "--inference-project")
	assert.Contains(t, cliCalls[2], "--inference-wif-provider")
}

func TestRepoEnsurer_InstallCLIError_Propagated(t *testing.T) {
	sc := &stubClient{installed: false}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			return "", fmt.Errorf("cli exploded")
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	_, err := e.EnsureRepo(context.Background(), "org", "test-repo-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github setup")
	assert.Contains(t, err.Error(), "cli exploded")
}

func TestRepoEnsurer_ProvisionInferenceError_Propagated(t *testing.T) {
	sc := &stubClient{installed: false}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{
			MintURL:      "https://mint.test",
			GCPProjectID: "test-project",
		},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "inference" && args[1] == "provision" {
				return "", fmt.Errorf("provision boom")
			}
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	_, err := e.EnsureRepo(context.Background(), "org", "test-repo-prov-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference provision")
	assert.Contains(t, err.Error(), "provision boom")
}

func TestRepoEnsurer_ConcurrentEnsureSameRepo(t *testing.T) {
	// Verify that concurrent EnsureRepo calls for the same repo only
	// perform create once (via singleflight deduplication).
	sc := &stubClient{
		getRepoErr:  forge.ErrNotFound,
		installed:   true,
		ensureDelay: 50 * time.Millisecond,
	}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	const goroutines = 5
	ctx := context.Background()
	results := make([]State, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = e.EnsureRepo(ctx, "org", "test-repo-race")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d failed", i)
		require.NotNil(t, results[i], "goroutine %d got nil State", i)
	}

	// singleflight ensures CreateRepo is called exactly once.
	assert.Equal(t, int32(1), sc.createRepoCalled.Load(),
		"concurrent callers should only create the repo once")
}

func TestEnsureRepoExists_AlreadyExists(t *testing.T) {
	sc := &stubClient{}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(0), sc.createRepoCalled.Load())
}

func TestEnsureRepoExists_CreatesWithAutoInit(t *testing.T) {
	sc := &stubClient{getRepoErr: forge.ErrNotFound}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "test-repo-01", "org/test-repo-01")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
	// No explicit seeding — auto_init provides the initial commit.
}

func TestEnsureRepoExists_NonNotFoundError(t *testing.T) {
	sc := &stubClient{getRepoErr: assert.AnError}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking repo")
}

func TestEnsureRepoExists_CreateRepoError(t *testing.T) {
	sc := &stubClient{
		getRepoErr:    forge.ErrNotFound,
		createRepoErr: fmt.Errorf("permission denied"),
	}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating repo")
	assert.Contains(t, err.Error(), "permission denied")
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
}

func TestDoEnsure_PostInstallStillFailsAfterInstall(t *testing.T) {
	// Simulates: repo exists, validation fails, CLI install runs
	// successfully, but re-validation still fails (installed stays false).
	sc := &stubClient{installed: false}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			// CLI succeeds but does NOT flip sc.installed — simulating
			// a case where setup ran but files are still missing.
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	_, err := e.EnsureRepo(context.Background(), "org", "test-repo-broken")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "post-install validation")
}

func TestProvisionInference_StatusCLIError(t *testing.T) {
	sc := &stubClient{installed: false}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{
			MintURL:      "https://mint.test",
			GCPProjectID: "test-project",
		},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
				return "", fmt.Errorf("status unreachable")
			}
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	_, err := e.EnsureRepo(context.Background(), "org", "test-repo-status-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference status")
	assert.Contains(t, err.Error(), "status unreachable")
}

func TestProvisionInference_ParseWIFProviderError(t *testing.T) {
	sc := &stubClient{installed: false}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{
			MintURL:      "https://mint.test",
			GCPProjectID: "test-project",
		},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
				// Return valid JSON but missing the WIF provider field.
				return `{"status":"healthy"}`, nil
			}
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	_, err := e.EnsureRepo(context.Background(), "org", "test-repo-parse-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference status")
}

func TestDoEnsure_EnsureRepoExistsError_Propagated(t *testing.T) {
	// When ensureRepoExists returns an error (non-NotFound from GetRepo),
	// doEnsure should propagate it without attempting install.
	sc := &stubClient{getRepoErr: fmt.Errorf("network timeout")}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	_, err := e.EnsureRepo(context.Background(), "org", "test-repo-net-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking repo")
	assert.Contains(t, err.Error(), "network timeout")
}

func TestDoEnsure_AlreadyInstalledSkipsCLI(t *testing.T) {
	// Exercises the doEnsure "already installed, skipping" path where
	// validation passes on the first check and installFullsend is never
	// invoked.
	sc := &stubClient{installed: true}
	cliCalled := false
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			cliCalled = true
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]State),
	}

	st, err := e.EnsureRepo(context.Background(), "org", "test-repo-skip")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal(t, "test-repo-skip", st.TestRepo())
	assert.False(t, cliCalled, "CLI should not be called when validation passes")
}
