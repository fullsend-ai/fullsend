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

// fakeEnsurer is a test double for ensurer that records calls.
// It lets callers verify caching and call-count behaviour without a
// real forge client or CLI binary.
type fakeEnsurer struct {
	calls atomic.Int32
	mu    sync.Mutex
	cache map[string]struct{}
}

func newFakeEnsurer() *fakeEnsurer {
	return &fakeEnsurer{cache: make(map[string]struct{})}
}

func (f *fakeEnsurer) EnsureRepo(_ context.Context, org, repoName string) error {
	key := org + "/" + repoName
	f.mu.Lock()
	if _, ok := f.cache[key]; ok {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	f.calls.Add(1)

	f.mu.Lock()
	f.cache[key] = struct{}{}
	f.mu.Unlock()

	return nil
}

var _ ensurer = (*fakeEnsurer)(nil)

func TestFakeEnsurer_Succeeds(t *testing.T) {
	e := newFakeEnsurer()
	err := e.EnsureRepo(context.Background(), "org", "test-repo-01")
	require.NoError(t, err)
}

func TestFakeEnsurer_CachesResult(t *testing.T) {
	e := newFakeEnsurer()
	ctx := context.Background()

	err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)

	err = e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)

	// Only one real ensure call.
	assert.Equal(t, int32(1), e.calls.Load())
}

func TestFakeEnsurer_IndependentRepos(t *testing.T) {
	e := newFakeEnsurer()
	ctx := context.Background()

	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))
	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-02"))

	assert.Equal(t, int32(2), e.calls.Load())
}

// --- repoEnsurer unit tests (caching layer + create logic) ---

// noopCLI is a CLIRunnerFunc that succeeds without doing anything.
// Used in tests that exercise caching/create logic but don't test
// the install flow itself.
func noopCLI(_, _ string, _ ...string) (string, error) { return "", nil }

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

	deleteRepoErr    error
	deleteRepoCalled atomic.Int32

	// forkExists controls whether GetRepo returns success for fork
	// repos (names ending in "-fork"). When false (default), fork
	// repos return ErrNotFound, preventing fork cleanup from
	// interfering with source-repo-focused tests.
	forkExists       bool
	forkDeleteErr    error
	forkDeleteCalled atomic.Int32

	// installed controls whether GetFileContent returns valid
	// post-install files. When false, all paths return ErrNotFound.
	installed bool

	// ensureDelay, when non-zero, causes GetRepo to sleep before
	// returning. Used to test concurrent singleflight behaviour.
	ensureDelay time.Duration

	// getWorkflowErr, when set, is returned by GetWorkflow.
	// When nil and installed is true, GetWorkflow returns a valid Workflow.
	getWorkflowErr    error
	getWorkflowCalled atomic.Int32
}

func (s *stubClient) GetRepo(_ context.Context, _, repo string) (*forge.Repository, error) {
	if s.ensureDelay > 0 {
		time.Sleep(s.ensureDelay)
	}
	if strings.HasSuffix(repo, "-fork") {
		if !s.forkExists {
			return nil, forge.ErrNotFound
		}
		return &forge.Repository{}, nil
	}
	return &forge.Repository{}, s.getRepoErr
}

func (s *stubClient) CreateRepo(_ context.Context, _, _, _ string, _ bool) (*forge.Repository, error) {
	s.createRepoCalled.Add(1)
	if s.createRepoErr != nil {
		return nil, s.createRepoErr
	}
	// Simulate eventual consistency: the repo is available after create,
	// so subsequent GetRepo calls should succeed.
	s.getRepoErr = nil
	return &forge.Repository{}, nil
}

func (s *stubClient) DeleteRepo(_ context.Context, _, repo string) error {
	if strings.HasSuffix(repo, "-fork") {
		s.forkDeleteCalled.Add(1)
		if s.forkDeleteErr != nil {
			return s.forkDeleteErr
		}
		s.forkExists = false
		return nil
	}
	s.deleteRepoCalled.Add(1)
	if s.deleteRepoErr != nil {
		return s.deleteRepoErr
	}
	// Simulate eventual consistency: the repo is gone after delete,
	// so subsequent GetRepo calls should return ErrNotFound.
	s.getRepoErr = forge.ErrNotFound
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

func (s *stubClient) GetWorkflow(_ context.Context, _, _, _ string) (*forge.Workflow, error) {
	s.getWorkflowCalled.Add(1)
	if s.getWorkflowErr != nil {
		return nil, s.getWorkflowErr
	}
	if !s.installed {
		return nil, forge.ErrNotFound
	}
	return &forge.Workflow{ID: 1, Name: "fullsend", Path: ".github/workflows/fullsend.yaml", State: "active"}, nil
}

func TestNewRepoEnsurer_ReturnsNonNil(t *testing.T) {
	sc := &stubClient{}
	e := newRepoEnsurer(e2etest.EnvConfig{}, sc, "tok", "/bin/true", t.Logf)
	require.NotNil(t, e, "newRepoEnsurer should return a non-nil ensurer")

	// Verify the returned value implements the interface.
	var _ ensurer = e
}

func TestEnsurer_CachesSuccessfulEnsure(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		runCLI:  noopCLI,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	ctx := context.Background()
	err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)

	err = e.EnsureRepo(ctx, "org", "test-repo-01")
	require.NoError(t, err)
}

func TestEnsurer_CacheKeyIncludesOrg(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		runCLI:  noopCLI,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	ctx := context.Background()
	require.NoError(t, e.EnsureRepo(ctx, "org-a", "test-repo-01"))
	require.NoError(t, e.EnsureRepo(ctx, "org-b", "test-repo-01"))

	// Same repo name but different orgs → different cache entries.
	e.mu.Lock()
	_, aExists := e.ensured["org-a/test-repo-01"]
	_, bExists := e.ensured["org-b/test-repo-01"]
	e.mu.Unlock()
	assert.True(t, aExists, "org-a should be cached")
	assert.True(t, bExists, "org-b should be cached")
}

func TestEnsurer_CreatesRepoWhenMissing(t *testing.T) {
	sc := &stubClient{
		getRepoErr: forge.ErrNotFound,
		installed:  true,
	}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		runCLI:  noopCLI,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-05")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
}

func TestEnsurer_DeletesAndRecreatesExistingRepo(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:  sc,
		binary:  "/usr/bin/fullsend",
		token:   "tok",
		runCLI:  noopCLI,
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-03")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(), "should delete existing repo to reset history")
}

func TestEnsurer_InstallsWhenValidationFails(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClient{installed: false}
	var cliCalls [][]string
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			if len(args) > 0 && args[0] == "github" && args[1] == "setup" {
				sc.installed = true
			}
			return "", nil
		},
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-10")
	require.NoError(t, err)

	// CLI should have been called for "github setup".
	require.Len(t, cliCalls, 1, "expected exactly one CLI call (github setup)")
	assert.Equal(t, "github", cliCalls[0][0])
	assert.Equal(t, "setup", cliCalls[0][1])
	assert.Contains(t, cliCalls[0], "--mint-url")
}

func TestEnsurer_DoEnsure_RepoMissing_ThenInstalled(t *testing.T) {
	speedUpValidateRetries(t)
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
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	ctx := context.Background()
	err := e.EnsureRepo(ctx, "org", "test-repo-new")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.createRepoCalled.Load(), "repo should be created")
	require.Len(t, cliCalls, 1)
	assert.Equal(t, "github", cliCalls[0][0])

	// Second call should hit cache — no additional CLI calls.
	err = e.EnsureRepo(ctx, "org", "test-repo-new")
	require.NoError(t, err)
	assert.Len(t, cliCalls, 1, "cached call should not invoke CLI again")
}

func TestEnsurer_DoEnsure_WithGCPProject(t *testing.T) {
	speedUpValidateRetries(t)
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
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-gcp")
	require.NoError(t, err)

	// Expect: inference provision, inference status, github setup (3 calls).
	require.Len(t, cliCalls, 3, "expected 3 CLI calls (provision, status, setup)")
	assert.Equal(t, "inference", cliCalls[0][0])
	assert.Equal(t, "provision", cliCalls[0][1])
	assert.Equal(t, "inference", cliCalls[1][0])
	assert.Equal(t, "status", cliCalls[1][1])
	assert.Equal(t, "github", cliCalls[2][0])
	assert.Equal(t, "setup", cliCalls[2][1])
	assert.Contains(t, cliCalls[2], "--inference-project")
	assert.Contains(t, cliCalls[2], "--inference-wif-provider")
}

func TestEnsurer_InstallCLIError_Propagated(t *testing.T) {
	speedUpValidateRetries(t)
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
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github setup")
	assert.Contains(t, err.Error(), "cli exploded")
}

func TestEnsurer_ProvisionInferenceError_Propagated(t *testing.T) {
	speedUpValidateRetries(t)
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
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-prov-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference provision")
	assert.Contains(t, err.Error(), "provision boom")
}

func TestEnsurer_ConcurrentEnsureSameRepo(t *testing.T) {
	sc := &stubClient{
		getRepoErr:  forge.ErrNotFound,
		installed:   true,
		ensureDelay: 50 * time.Millisecond,
	}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		runCLI:  noopCLI,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	const goroutines = 5
	ctx := context.Background()
	errs := make([]error, goroutines)
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = e.EnsureRepo(ctx, "org", "test-repo-race")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d failed", i)
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
	speedUpValidateRetries(t)
	sc := &stubClient{installed: false}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-broken")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "post-install validation")
}

func TestProvisionInference_StatusCLIError(t *testing.T) {
	speedUpValidateRetries(t)
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
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-status-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference status")
	assert.Contains(t, err.Error(), "status unreachable")
}

func TestProvisionInference_ParseWIFProviderError(t *testing.T) {
	speedUpValidateRetries(t)
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
				return `{"status":"healthy"}`, nil
			}
			return "", nil
		},
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-parse-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference status")
}

func TestDoEnsure_EnsureRepoExistsError_Propagated(t *testing.T) {
	sc := &stubClient{getRepoErr: fmt.Errorf("network timeout")}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-net-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking repo")
	assert.Contains(t, err.Error(), "network timeout")
}

func TestDoEnsure_AlwaysInstallsAfterReset(t *testing.T) {
	speedUpValidateRetries(t)
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
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-revendor")
	require.NoError(t, err)
	assert.True(t, cliCalled, "CLI should be called to install after repo reset")
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(), "existing repo should be deleted for reset")
}

// --- awaitWorkflowReady unit tests ---

// noopSettle is a SettleFunc that does nothing.
func noopSettle(_ context.Context, _ forge.Client, _, _, _ string, _ func(string, ...any)) error {
	return nil
}

func TestAwaitWorkflowReady_ImmediateSuccess(t *testing.T) {
	sc := &stubClient{installed: true}
	err := awaitWorkflowReady(context.Background(), sc, "org", "repo", "fullsend.yaml", t.Logf)
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.getWorkflowCalled.Load(), "should succeed on first poll")
}

func TestAwaitWorkflowReady_ContextCancelled(t *testing.T) {
	sc := &stubClient{installed: false, getWorkflowErr: forge.ErrNotFound}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := awaitWorkflowReady(ctx, sc, "org", "repo", "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

func TestDoEnsure_SettleCalledAfterInstall(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClient{installed: false}
	settleCalled := false
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "github" && args[1] == "setup" {
				sc.installed = true
			}
			return "", nil
		},
		settle: func(_ context.Context, _ forge.Client, _, _, _ string, _ func(string, ...any)) error {
			settleCalled = true
			return nil
		},
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-settle")
	require.NoError(t, err)
	assert.True(t, settleCalled, "settle should be called after install")
}

func TestDoEnsure_SettleAlwaysCalledAfterReset(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClient{installed: true}
	settleCalled := false
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			return "", nil
		},
		settle: func(_ context.Context, _ forge.Client, _, _, _ string, _ func(string, ...any)) error {
			settleCalled = true
			return nil
		},
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-settle-after-reset")
	require.NoError(t, err)
	assert.True(t, settleCalled, "settle should always be called after repo reset")
}

func TestDoEnsure_SettleError_Propagated(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClient{installed: false}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(binary, token string, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "github" && args[1] == "setup" {
				sc.installed = true
			}
			return "", nil
		},
		settle: func(_ context.Context, _ forge.Client, _, _, _ string, _ func(string, ...any)) error {
			return fmt.Errorf("Actions not ready")
		},
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-settle-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "waiting for Actions readiness")
	assert.Contains(t, err.Error(), "Actions not ready")
}

// --- resetRepo unit tests ---

func TestResetRepo_DeletesExistingRepo(t *testing.T) {
	sc := &stubClient{} // GetRepo returns success (repo exists)
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(), "should delete existing repo")
}

func TestResetRepo_SkipsDeleteWhenRepoMissing(t *testing.T) {
	sc := &stubClient{getRepoErr: forge.ErrNotFound}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(0), sc.deleteRepoCalled.Load(), "should not delete missing repo")
}

func TestResetRepo_PropagatesGetRepoError(t *testing.T) {
	sc := &stubClient{getRepoErr: assert.AnError}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking repo")
}

func TestResetRepo_PropagatesDeleteError(t *testing.T) {
	sc := &stubClient{deleteRepoErr: fmt.Errorf("permission denied")}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting repo")
	assert.Contains(t, err.Error(), "permission denied")
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load())
}

func TestResetRepo_DeleteNotFound_IsIdempotent(t *testing.T) {
	sc := &stubClient{deleteRepoErr: forge.ErrNotFound}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err, "ErrNotFound from delete should be treated as success (race-safe)")
}

// --- resetRepo fork cleanup tests ---

func TestResetRepo_DeletesForkBeforeSource(t *testing.T) {
	speedUpResetRetries(t)
	sc := &stubClient{forkExists: true}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.forkDeleteCalled.Load(), "fork should be deleted before source reset")
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(), "source should be deleted")
}

func TestResetRepo_SkipsForkDeleteWhenForkMissing(t *testing.T) {
	sc := &stubClient{} // forkExists defaults to false
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(0), sc.forkDeleteCalled.Load(), "fork should not be deleted when absent")
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(), "source should still be deleted")
}

func TestResetRepo_PropagatesForkDeleteError(t *testing.T) {
	sc := &stubClient{
		forkExists:    true,
		forkDeleteErr: fmt.Errorf("permission denied"),
	}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting fork repo")
	assert.Contains(t, err.Error(), "permission denied")
	assert.Equal(t, int32(1), sc.forkDeleteCalled.Load())
}

func TestResetRepo_ForkDeleteNotFound_ContinuesToSource(t *testing.T) {
	speedUpResetRetries(t)
	sc := &stubClient{
		forkExists:    true,
		forkDeleteErr: forge.ErrNotFound,
	}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.resetRepo(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err, "ErrNotFound from fork delete should not block source reset")
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(), "source should still be deleted")
}

// speedUpResetRetries sets resetRetryDelay to zero for fast tests
// and returns a cleanup function that restores the original value.
func speedUpResetRetries(t *testing.T) {
	t.Helper()
	orig := resetRetryDelay
	resetRetryDelay = 0
	t.Cleanup(func() { resetRetryDelay = orig })
}

// countingRepoClient returns different GetRepo results after a
// configurable number of calls. Used to test awaitDeletion and
// awaitCreation retry behaviour.
type countingRepoClient struct {
	forge.Client
	mu           sync.Mutex
	getRepoCalls int
	switchAfter  int   // after this many calls, switch to switchedErr
	initialErr   error // returned for calls 1..switchAfter
	switchedErr  error // returned for calls switchAfter+1..
}

func (c *countingRepoClient) GetRepo(_ context.Context, _, _ string) (*forge.Repository, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getRepoCalls++
	if c.getRepoCalls > c.switchAfter {
		return &forge.Repository{}, c.switchedErr
	}
	return &forge.Repository{}, c.initialErr
}

// --- awaitDeletion unit tests ---

func TestAwaitDeletion_ConfirmsImmediately(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: 0,
		initialErr:  forge.ErrNotFound,
		switchedErr: forge.ErrNotFound,
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitDeletion(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, 1, client.getRepoCalls, "should confirm on first poll")
}

func TestAwaitDeletion_RetriesUntilConfirmed(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: 3,
		initialErr:  nil,               // repo still visible for 3 calls
		switchedErr: forge.ErrNotFound, // then confirmed deleted
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitDeletion(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, 4, client.getRepoCalls, "should retry until 404 confirmed")
}

func TestAwaitDeletion_ProceedsAfterMaxAttempts(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: resetMaxAttempts + 1, // never switches
		initialErr:  nil,                  // repo stays visible
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitDeletion(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err, "should not error when max attempts exhausted")
	assert.Equal(t, resetMaxAttempts, client.getRepoCalls)
}

func TestAwaitDeletion_PropagatesNonNotFoundError(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: 0,
		initialErr:  assert.AnError,
		switchedErr: assert.AnError,
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitDeletion(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking deletion")
}

func TestAwaitDeletion_ContextCancellation(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: resetMaxAttempts + 1, // never switches
		initialErr:  nil,                  // repo stays visible
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := e.awaitDeletion(ctx, "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

// --- awaitCreation unit tests ---

func TestAwaitCreation_ConfirmsImmediately(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: 0,
		initialErr:  nil, // repo visible immediately
		switchedErr: nil,
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitCreation(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, 1, client.getRepoCalls, "should confirm on first poll")
}

func TestAwaitCreation_RetriesUntilVisible(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: 2,
		initialErr:  forge.ErrNotFound, // not visible for 2 calls
		switchedErr: nil,               // then visible
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitCreation(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, 3, client.getRepoCalls, "should retry until repo visible")
}

func TestAwaitCreation_FailsAfterMaxAttempts(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: resetMaxAttempts + 1, // never switches
		initialErr:  forge.ErrNotFound,    // never visible
		switchedErr: forge.ErrNotFound,
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitCreation(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err, "should error when repo never becomes visible")
	assert.Contains(t, err.Error(), "not visible after")
}

func TestAwaitCreation_PropagatesNonNotFoundError(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: 0,
		initialErr:  assert.AnError,
		switchedErr: assert.AnError,
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.awaitCreation(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking creation")
}

func TestAwaitCreation_ContextCancellation(t *testing.T) {
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: resetMaxAttempts + 1,
		initialErr:  forge.ErrNotFound,
		switchedErr: forge.ErrNotFound,
	}
	e := &repoEnsurer{client: client, logf: t.Logf}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := e.awaitCreation(ctx, "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}
