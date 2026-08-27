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
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
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

func TestEnsurer_SkipsCreateWhenRepoExists(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:  e2etest.EnvConfig{},
		client:  sc,
		runCLI:  noopCLI,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-03")
	require.NoError(t, err)
	assert.Equal(t, int32(0), sc.createRepoCalled.Load(), "should not create existing repo")
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

func TestDoEnsure_AlreadyInstalledReVendors(t *testing.T) {
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
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-revendor")
	require.NoError(t, err)
	assert.True(t, cliCalled, "CLI should be called to re-vendor even when validation passes")
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

func TestDoEnsure_SettleNotCalledWhenAlreadyInstalled(t *testing.T) {
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

	err := e.EnsureRepo(context.Background(), "org", "test-repo-no-settle")
	require.NoError(t, err)
	assert.False(t, settleCalled, "settle should not be called when already installed")
}

func TestEnsurer_NonVendoredMode_UsesNonVendoredValidation(t *testing.T) {
	// Non-vendored mode should pass validation without vendored
	// marker and binary files.
	speedUpValidateRetries(t)
	// Override GetFileContent to return only shim + config (no marker/binary).
	nonVendoredFiles := map[string][]byte{
		".github/workflows/fullsend.yaml": []byte("# shim"),
		".fullsend/config.yaml":           []byte(validPerRepoConfig),
	}

	var cliCalls [][]string
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: &stubClientWithCustomFiles{
			stubClient: stubClient{},
			files:      nonVendoredFiles,
		},
		binary: "/usr/bin/fullsend",
		token:  "tok",
		setupOpts: common.GitHubSetupOpts{
			Vendor:      false,
			FullsendRef: "main",
		},
		runCLI: func(binary, token string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			return "", nil
		},
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-nonvendored")
	require.NoError(t, err)

	// CLI should have been called for "github setup" with --fullsend-ref.
	require.Len(t, cliCalls, 1)
	assert.Equal(t, "github", cliCalls[0][0])
	assert.Equal(t, "setup", cliCalls[0][1])
	assert.Contains(t, cliCalls[0], "--fullsend-ref")
	assert.Contains(t, cliCalls[0], "main")
	assert.NotContains(t, cliCalls[0], "--vendor")
}

// stubClientWithCustomFiles is a test double that returns custom file
// contents instead of using the global installedStubFiles map.
type stubClientWithCustomFiles struct {
	stubClient
	files map[string][]byte
}

func (s *stubClientWithCustomFiles) GetFileContent(_ context.Context, _, _, path string) ([]byte, error) {
	clean := strings.TrimPrefix(path, "./")
	if content, ok := s.files[clean]; ok {
		return content, nil
	}
	return nil, forge.ErrNotFound
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
