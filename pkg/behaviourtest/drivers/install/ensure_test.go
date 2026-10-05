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

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// fakeEnsurer is a test double for ensurer that records calls.
// It lets callers verify caching and call-count behaviour without a
// real forge client or CLI binary.
type fakeEnsurer struct {
	calls       atomic.Int32
	deleteCalls atomic.Int32
	deleteErr   error
	mu          sync.Mutex
	cache       map[string]struct{}
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

func (f *fakeEnsurer) DeleteRepo(_ context.Context, org, repoName string) error {
	key := org + "/" + repoName
	f.mu.Lock()
	delete(f.cache, key)
	f.mu.Unlock()
	f.deleteCalls.Add(1)
	return f.deleteErr
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

func TestFakeEnsurer_DeleteRepoClearsCache(t *testing.T) {
	e := newFakeEnsurer()
	ctx := context.Background()

	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))
	require.NoError(t, e.DeleteRepo(ctx, "org", "test-repo-01"))
	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))

	assert.Equal(t, int32(1), e.deleteCalls.Load())
	assert.Equal(t, int32(2), e.calls.Load(), "ensure after delete should not hit cache")
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
	vendoredBinaryPathPerRepo:         []byte("binary"),
}

func TestVendoredBinaryPathMatchesLayers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, layers.VendoredBinaryPathPerRepo, vendoredBinaryPathPerRepo)
}

// stubClient implements the forge.Client methods used by repoEnsurer.
type stubClient struct {
	forge.Client // embed to satisfy interface; panics on uncovered methods

	getRepoErr       error
	createRepoErr    error
	createRepoErrSeq []error // per-call CreateRepo errors; nil entry = success
	createRepoCalled atomic.Int32

	deleteRepoErr    error
	deleteRepoCalled atomic.Int32

	// staleGetAfterDelete keeps GetRepo succeeding after DeleteRepo,
	// simulating GitHub serving a cached repo object whose deletion
	// has not yet propagated (#7839).
	staleGetAfterDelete bool

	// blockedByExistingRepo makes CreateRepo return forge.ErrAlreadyExists
	// until DeleteRepo(org, repoName) actually clears it. Unlike
	// createRepoErr/createRepoErrSeq (fixed per-call scripts), this ties
	// CreateRepo's outcome to whether the blocking repo was really
	// deleted, so a test using it fails if the delete call that clears
	// the name is removed (#7839).
	blockedByExistingRepo bool

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
	n := s.createRepoCalled.Add(1)
	if s.blockedByExistingRepo {
		return nil, forge.ErrAlreadyExists
	}
	if seq := s.createRepoErrSeq; seq != nil {
		idx := int(n - 1)
		if idx < len(seq) && seq[idx] != nil {
			return nil, seq[idx]
		}
	} else if s.createRepoErr != nil {
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
	s.blockedByExistingRepo = false
	if !s.staleGetAfterDelete {
		// Simulate eventual consistency: the repo is gone after delete,
		// so subsequent GetRepo calls should return ErrNotFound.
		s.getRepoErr = forge.ErrNotFound
	}
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
	e, err := newRepoEnsurer(e2etest.EnvConfig{}, sc, "tok", "/bin/true", t.Logf)
	require.NoError(t, err)
	require.NotNil(t, e, "newRepoEnsurer should return a non-nil ensurer")

	// Verify the returned value implements the interface.
	var _ ensurer = e
}

func TestNewRepoEnsurer_ConfigPresetFromEnv(t *testing.T) {
	t.Setenv("BEHAVIOUR_CONFIG_PRESET", "https://example.com/preset.yaml")
	sc := &stubClient{}
	e, err := newRepoEnsurer(e2etest.EnvConfig{}, sc, "tok", "/bin/true", t.Logf)
	require.NoError(t, err)
	re, ok := e.(*repoEnsurer)
	require.True(t, ok)
	assert.Equal(t, "https://example.com/preset.yaml", re.setupOpts.ConfigPreset)
}

func TestNewRepoEnsurer_ConfigPresetUnset(t *testing.T) {
	t.Setenv("BEHAVIOUR_CONFIG_PRESET", "")
	sc := &stubClient{}
	e, err := newRepoEnsurer(e2etest.EnvConfig{}, sc, "tok", "/bin/true", t.Logf)
	require.NoError(t, err)
	re, ok := e.(*repoEnsurer)
	require.True(t, ok)
	assert.Empty(t, re.setupOpts.ConfigPreset)
}

func TestEnsurer_CachesSuccessfulEnsure(t *testing.T) {
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-05")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
}

func TestEnsurer_DeletesAndRecreatesExistingRepo(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:    sc,
		binary:    "/usr/bin/fullsend",
		token:     "tok",
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-gcp")
	require.NoError(t, err)

	// A healthy provider skips provision: inference status, github setup.
	require.Len(t, cliCalls, 2, "expected 2 CLI calls (status, setup)")
	assert.Equal(t, "inference", cliCalls[0][0])
	assert.Equal(t, "status", cliCalls[0][1])
	assert.Equal(t, "github", cliCalls[1][0])
	assert.Equal(t, "setup", cliCalls[1][1])
	assert.Contains(t, cliCalls[1], "--inference-project")
	assert.Contains(t, cliCalls[1], "--inference-wif-provider")
}

const (
	testWIFProvider      = "projects/p/locations/l/providers/wif"
	healthyStatusJSON    = `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"` + testWIFProvider + `"}`
	notProvisionedStatus = `{"status":"not_provisioned"}`
)

func isInferenceCall(args []string, sub string) bool {
	return len(args) >= 2 && args[0] == "inference" && args[1] == sub
}

func isGitHubSetupCall(args []string) bool {
	return len(args) >= 2 && args[0] == "github" && args[1] == "setup"
}

// githubSetupRepoInfo404Err matches the CLI error from applyPerRepoScaffold
// when GetRepo 404s on a just-created repo.
func githubSetupRepoInfo404Err(target string) error {
	return fmt.Errorf("[cli] fullsend github setup %s failed: exit 1\nError: getting repo info: get repo %s: github api: 404 Not Found", target, target)
}

func TestEnsurer_WIFProvider_NotHealthy_ProvisionsOnce(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClient{installed: true}
	var cliCalls [][]string
	statusCalls := 0
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test", GCPProjectID: "test-project"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			if isInferenceCall(args, "status") {
				statusCalls++
				if statusCalls == 1 {
					return notProvisionedStatus, nil
				}
				return healthyStatusJSON, nil
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	require.NoError(t, e.EnsureRepo(context.Background(), "org", "test-repo-cold"))

	require.Len(t, cliCalls, 4, "expected status, provision, status, setup")
	assert.True(t, isInferenceCall(cliCalls[0], "status"))
	assert.True(t, isInferenceCall(cliCalls[1], "provision"))
	assert.True(t, isInferenceCall(cliCalls[2], "status"))
	assert.Equal(t, []string{"github", "setup"}, cliCalls[3][:2])
	assert.Contains(t, cliCalls[3], testWIFProvider)
}

// TestEnsurer_WIFProvider_SurvivesDeleteRepo covers the pool lifecycle
// after #7398: every lease ends in DeleteRepo, and the next lease of the
// same name must reuse the provider without any inference CLI call.
func TestEnsurer_WIFProvider_SurvivesDeleteRepo(t *testing.T) {
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{installed: true}
	var cliCalls [][]string
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test", GCPProjectID: "test-project"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			if isInferenceCall(args, "status") {
				return healthyStatusJSON, nil
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	ctx := context.Background()
	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))
	require.Len(t, cliCalls, 2, "first ensure: status, setup")

	require.NoError(t, e.DeleteRepo(ctx, "org", "test-repo-01"))
	cliCalls = nil
	createsBefore := sc.createRepoCalled.Load()

	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))
	assert.Greater(t, sc.createRepoCalled.Load(), createsBefore, "the repo is still recreated")
	require.Len(t, cliCalls, 1, "second ensure: setup only, provider reused from cache")
	assert.Equal(t, []string{"github", "setup"}, cliCalls[0][:2])
	assert.Contains(t, cliCalls[0], testWIFProvider)

	// A different name is not served from another name's cache entry.
	cliCalls = nil
	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-02"))
	require.Len(t, cliCalls, 2, "new name: status, setup")
	assert.True(t, isInferenceCall(cliCalls[0], "status"))
}

func TestEnsurer_WIFProvider_ProvisionErrorNotCached(t *testing.T) {
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{installed: true}
	var provisions atomic.Int32
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test", GCPProjectID: "test-project"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			switch {
			case isInferenceCall(args, "status"):
				if provisions.Load() < 2 {
					return notProvisionedStatus, nil
				}
				return healthyStatusJSON, nil
			case isInferenceCall(args, "provision"):
				if provisions.Add(1) == 1 {
					return "", fmt.Errorf("rate limited (HTTP 429)")
				}
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	ctx := context.Background()
	err := e.EnsureRepo(ctx, "org", "test-repo-01")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited (HTTP 429)")

	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))
	assert.Equal(t, int32(2), provisions.Load(), "a failed resolve must not be cached")
}

// The WIF gate tests below use the process-wide provisionGate. Do not
// mark them t.Parallel(): a test holding the gate would block the others.

func TestEnsurer_WIFProvider_CancelledContextWinsOverFreeGate(t *testing.T) {
	var calls atomic.Int32
	e := &repoEnsurer{
		runCLI: func(_, _ string, _ ...string) (string, error) {
			calls.Add(1)
			return notProvisionedStatus, nil
		},
		logf: t.Logf,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 20; i++ {
		_, err := e.resolveWIFProvider(ctx, "org/test-repo-01", "test-project")
		require.ErrorIs(t, err, context.Canceled)
		// The context can also be cancelled during the status read, so the
		// provision path checks it again before contending for the gate.
		_, err = e.provisionWIFProvider(ctx, "org/test-repo-01", "test-project")
		require.ErrorIs(t, err, context.Canceled)
	}
	assert.Zero(t, calls.Load(), "a cancelled context must not run any inference CLI call")
	assert.Zero(t, len(provisionGate), "gate must be free")
}

func TestEnsurer_WIFProvider_GateReleasedOnPanic(t *testing.T) {
	e := &repoEnsurer{
		runCLI: func(_, _ string, args ...string) (string, error) {
			if isInferenceCall(args, "provision") {
				panic("provision panicked")
			}
			return notProvisionedStatus, nil
		},
		logf: t.Logf,
	}

	assert.PanicsWithValue(t, "provision panicked", func() {
		_, _ = e.resolveWIFProvider(context.Background(), "org/test-repo-01", "test-project")
	})
	assert.Zero(t, len(provisionGate), "a panicking provision must release the gate")
}

func TestEnsurer_WIFProvider_CacheRecheckedUnderGate(t *testing.T) {
	e := &repoEnsurer{
		runCLI: func(_, _ string, args ...string) (string, error) {
			t.Fatalf("no CLI call expected, got %v", args)
			return "", nil
		},
		logf:         t.Logf,
		wifProviders: map[string]string{"org/test-repo-01": testWIFProvider},
	}

	got, err := e.provisionWIFProvider(context.Background(), "org/test-repo-01", "test-project")
	require.NoError(t, err)
	assert.Equal(t, testWIFProvider, got)
	assert.Zero(t, len(provisionGate), "the cache-hit return must release the gate")
}

// TestEnsurer_WIFProvider_SameNameWaiterReusesProvision resolves one name
// from two goroutines without singleflight: the second caller waits on the
// gate behind the first provision and must reuse its cached result.
func TestEnsurer_WIFProvider_SameNameWaiterReusesProvision(t *testing.T) {
	var provisions atomic.Int32
	statusStarted := make(chan struct{}, 2)
	releaseStatus := make(chan struct{})
	e := &repoEnsurer{
		runCLI: func(_, _ string, args ...string) (string, error) {
			switch {
			case isInferenceCall(args, "status"):
				if provisions.Load() > 0 {
					return healthyStatusJSON, nil
				}
				statusStarted <- struct{}{}
				<-releaseStatus
				return notProvisionedStatus, nil
			case isInferenceCall(args, "provision"):
				provisions.Add(1)
				// Hold the gate long enough for the other caller to queue on it.
				time.Sleep(30 * time.Millisecond)
			}
			return "", nil
		},
		logf: t.Logf,
	}

	results := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = e.resolveWIFProvider(context.Background(), "org/test-repo-01", "test-project")
		}(i)
	}
	// Both callers miss the cache and see "not provisioned" before either
	// provisions, so both reach the gate.
	<-statusStarted
	<-statusStarted
	close(releaseStatus)
	wg.Wait()

	for i := range errs {
		require.NoError(t, errs[i])
		assert.Equal(t, testWIFProvider, results[i])
	}
	assert.Equal(t, int32(1), provisions.Load(), "the waiter must reuse the first provision")
	assert.Zero(t, len(provisionGate), "both callers must release the gate")
}

func TestEnsurer_WIFProvider_WaitForGateHonoursContext(t *testing.T) {
	provisionGate <- struct{}{} // another provision holds the gate
	t.Cleanup(func() { <-provisionGate })

	var provisions atomic.Int32
	statusDone := make(chan struct{})
	e := &repoEnsurer{
		runCLI: func(_, _ string, args ...string) (string, error) {
			if isInferenceCall(args, "provision") {
				provisions.Add(1)
			} else {
				close(statusDone)
			}
			return notProvisionedStatus, nil
		},
		logf: t.Logf,
	}

	// The context is live when the caller reaches the gate, so it blocks
	// on the held gate and must return once the context is cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := e.resolveWIFProvider(ctx, "org/test-repo-01", "test-project")
		errCh <- err
	}()
	<-statusDone
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), "waiting to provision inference for org/test-repo-01")
	case <-time.After(5 * time.Second):
		t.Fatal("caller blocked on the gate did not return after cancellation")
	}
	assert.Zero(t, provisions.Load())
}

// lockedRepoClient is a stubClient whose repo existence is tracked per
// name under a mutex, so ensures of different repos can run concurrently
// under -race (stubClient shares one getRepoErr across all repos).
type lockedRepoClient struct {
	stubClient
	mu    sync.Mutex
	repos map[string]bool
}

func (c *lockedRepoClient) GetRepo(_ context.Context, org, repo string) (*forge.Repository, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.repos[org+"/"+repo] {
		return nil, forge.ErrNotFound
	}
	return &forge.Repository{}, nil
}

func (c *lockedRepoClient) CreateRepo(_ context.Context, org, repo, _ string, _ bool) (*forge.Repository, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repos[org+"/"+repo] = true
	return &forge.Repository{}, nil
}

func (c *lockedRepoClient) DeleteRepo(_ context.Context, org, repo string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.repos, org+"/"+repo)
	return nil
}

// TestEnsurer_WIFProvider_ConcurrentColdProvisionsSerialised ensures a
// cold pool concurrently: every slot must provision, but never more
// than one at a time.
func TestEnsurer_WIFProvider_ConcurrentColdProvisionsSerialised(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &lockedRepoClient{stubClient: stubClient{installed: true}, repos: map[string]bool{}}

	var inflight, maxInflight, provisions atomic.Int32
	var statusMu sync.Mutex
	provisioned := map[string]bool{}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test", GCPProjectID: "test-project"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			switch {
			case isInferenceCall(args, "status"):
				statusMu.Lock()
				defer statusMu.Unlock()
				if provisioned[args[2]] {
					return healthyStatusJSON, nil
				}
				return notProvisionedStatus, nil
			case isInferenceCall(args, "provision"):
				provisions.Add(1)
				n := inflight.Add(1)
				for {
					m := maxInflight.Load()
					if n <= m || maxInflight.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
				inflight.Add(-1)
				statusMu.Lock()
				provisioned[args[2]] = true
				statusMu.Unlock()
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	const slots = 6
	ctx := context.Background()
	errs := make([]error, slots)
	var wg sync.WaitGroup
	wg.Add(slots)
	for i := 0; i < slots; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = e.EnsureRepo(ctx, "org", fmt.Sprintf("test-repo-%02d", i+1))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "slot %d", i+1)
	}
	assert.Equal(t, int32(slots), provisions.Load(), "every cold slot provisions once")
	assert.Equal(t, int32(1), maxInflight.Load(), "at most one provision in flight")
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github setup")
	assert.Contains(t, err.Error(), "cli exploded")
}

func TestEnsurer_GitHubSetup_RepoInfo404_RetriesThenSucceeds(t *testing.T) {
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{installed: false}
	var setupCalls atomic.Int32
	const target = "org/test-repo-setup-404"
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			if isGitHubSetupCall(args) {
				n := setupCalls.Add(1)
				if n == 1 {
					return "", githubSetupRepoInfo404Err(target)
				}
				sc.installed = true
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-setup-404")
	require.NoError(t, err)
	assert.Equal(t, int32(2), setupCalls.Load(), "github setup should retry once after a repo-info 404")
}

func TestEnsurer_GitHubSetup_OtherError_NoRetry(t *testing.T) {
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{installed: false}
	var setupCalls atomic.Int32
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			if isGitHubSetupCall(args) {
				setupCalls.Add(1)
				return "", fmt.Errorf("cli exploded")
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-setup-err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github setup")
	assert.Contains(t, err.Error(), "cli exploded")
	assert.Equal(t, int32(1), setupCalls.Load(), "non-404 github setup errors must not be retried")
}

func TestEnsurer_GitHubSetup_RepoInfo404_ContextCancelledDuringBackoff(t *testing.T) {
	speedUpValidateRetries(t)
	orig := resetRetryDelay
	resetRetryDelay = 5 * time.Second
	t.Cleanup(func() { resetRetryDelay = orig })

	sc := &stubClient{installed: false}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstSetup := make(chan struct{})
	var setupCalls atomic.Int32
	const target = "org/test-repo-setup-404-cancel"
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			if isGitHubSetupCall(args) {
				n := setupCalls.Add(1)
				if n == 1 {
					close(firstSetup)
					return "", githubSetupRepoInfo404Err(target)
				}
				return "", fmt.Errorf("unexpected extra github setup call")
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.EnsureRepo(ctx, "org", "test-repo-setup-404-cancel")
	}()

	select {
	case <-firstSetup:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first github setup call")
	}
	cancel()

	select {
	case err := <-errCh:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), "github setup")
		assert.Equal(t, int32(1), setupCalls.Load(), "cancellation during backoff must not start another github setup")
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureRepo did not return after context cancellation during github setup backoff")
	}
}

func TestEnsurer_GitHubSetup_RepoInfo404_GivesUpAfterMaxAttempts(t *testing.T) {
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{installed: false}
	var setupCalls atomic.Int32
	const target = "org/test-repo-setup-404-max"
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			if isGitHubSetupCall(args) {
				setupCalls.Add(1)
				return "", githubSetupRepoInfo404Err(target)
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-setup-404-max")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting repo info:")
	assert.Contains(t, err.Error(), "404 Not Found")
	assert.Equal(t, int32(resetMaxAttempts), setupCalls.Load(), "repo-info 404 retries must stop at resetMaxAttempts")
	assert.Contains(t, err.Error(), fmt.Sprintf("after %d attempts", resetMaxAttempts))
}

func TestIsGitHubSetupRepoInfo404(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "cli 404",
			err:  githubSetupRepoInfo404Err("org/repo"),
			want: true,
		},
		{
			name: "wrapped 404",
			err:  fmt.Errorf("github setup org/repo: %w", githubSetupRepoInfo404Err("org/repo")),
			want: true,
		},
		{
			name: "getting repo info on a different read",
			err:  fmt.Errorf("getting repo info: list variables: github api: 404 Not Found"),
			want: false,
		},
		{
			name: "404 without getting repo info",
			err:  fmt.Errorf("github setup org/repo: workflow not found: 404 Not Found"),
			want: false,
		},
		{
			name: "getting repo info without 404",
			err:  fmt.Errorf("getting repo info: get repo org/repo: github api: 403 Forbidden"),
			want: false,
		},
		{
			name: "other error",
			err:  fmt.Errorf("cli exploded"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isGitHubSetupRepoInfo404(tt.err))
		})
	}
}

// TestEnsurer_GitHubSetup_RepoInfo404_DoesNotReprovisionWIF checks that a
// setup retry reuses the cached inference WIF provider: one status read,
// no provision, and the same provider on both setup attempts.
func TestEnsurer_GitHubSetup_RepoInfo404_DoesNotReprovisionWIF(t *testing.T) {
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{installed: false}
	var setupCalls, statusCalls, provisionCalls atomic.Int32
	var setupArgs [][]string
	const target = "org/test-repo-setup-404-wif"
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test", GCPProjectID: "test-project"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			switch {
			case isInferenceCall(args, "status"):
				statusCalls.Add(1)
				return healthyStatusJSON, nil
			case isInferenceCall(args, "provision"):
				provisionCalls.Add(1)
			case isGitHubSetupCall(args):
				setupArgs = append(setupArgs, append([]string(nil), args...))
				if setupCalls.Add(1) == 1 {
					return "", githubSetupRepoInfo404Err(target)
				}
				sc.installed = true
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	require.NoError(t, e.EnsureRepo(context.Background(), "org", "test-repo-setup-404-wif"))
	assert.Equal(t, int32(2), setupCalls.Load())
	assert.Equal(t, int32(1), statusCalls.Load(), "the retry must reuse the cached provider")
	assert.Zero(t, provisionCalls.Load())
	require.Len(t, setupArgs, 2)
	assert.Contains(t, setupArgs[0], testWIFProvider)
	assert.Contains(t, setupArgs[1], testWIFProvider)
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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

func TestEnsureRepoExists_CreatesWithAutoInit(t *testing.T) {
	sc := &stubClient{getRepoErr: forge.ErrNotFound}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "test-repo-01", "org/test-repo-01")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
}

func TestEnsureRepoExists_StaleGetRepoStillCreates(t *testing.T) {
	// GetRepo succeeding after delete is not proof the name is ready
	// (#7839). Create anyway so a stale cached object cannot skip
	// recreation and leave a later setup call to 404.
	sc := &stubClient{}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.createRepoCalled.Load(), "must CreateRepo even when GetRepo succeeds")
}

func TestEnsureRepoExists_RetriesAlreadyExistsThenSucceeds(t *testing.T) {
	speedUpResetRetries(t)
	sc := &stubClient{
		createRepoErrSeq: []error{forge.ErrAlreadyExists, forge.ErrAlreadyExists, nil},
	}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(3), sc.createRepoCalled.Load())
}

func TestEnsureRepoExists_AlreadyExistsExhausted_Errors(t *testing.T) {
	speedUpResetRetries(t)
	sc := &stubClient{createRepoErr: forge.ErrAlreadyExists}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name still taken")
	assert.Equal(t, int32(resetMaxAttempts), sc.createRepoCalled.Load(),
		"retry loop must terminate after resetMaxAttempts")
	assert.Equal(t, int32(resetMaxAttempts-1), sc.deleteRepoCalled.Load(),
		"a delete must be attempted before every retry (not after the final, exhausted attempt)")
}

// TestEnsureRepoExists_AlreadyExistsDeletesBlockingRepoThenSucceeds
// pins the actual fix for #7839: an already-exists error must delete
// the repo that is blocking creation, not just back off and retry the
// same failing call. blockedByExistingRepo ties CreateRepo's outcome
// to whether DeleteRepo was actually invoked, so removing the delete
// call (a mutation of the fix) makes this test fail rather than pass
// vacuously.
func TestEnsureRepoExists_AlreadyExistsDeletesBlockingRepoThenSucceeds(t *testing.T) {
	speedUpResetRetries(t)
	sc := &stubClient{blockedByExistingRepo: true}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(),
		"must delete the blocking repo, not just back off and retry")
	assert.Equal(t, int32(2), sc.createRepoCalled.Load(),
		"create should succeed on the retry after the delete")
}

// TestEnsureRepoExists_AlreadyExistsDeletesForkToo verifies the
// already-exists recovery path cleans up a leftover <repo>-fork the
// same way resetRepo does, so the fork isn't left orphaned pointing at
// a source repo that is about to be recreated.
func TestEnsureRepoExists_AlreadyExistsDeletesForkToo(t *testing.T) {
	speedUpResetRetries(t)
	sc := &stubClient{
		blockedByExistingRepo: true,
		forkExists:            true,
	}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.forkDeleteCalled.Load(),
		"leftover fork must be deleted before recreating the source")
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load())
	assert.Equal(t, int32(2), sc.createRepoCalled.Load())
}

// TestEnsureRepoExists_AlreadyExistsBacksOffEvenWhenGetRepoAlready404s pins
// the fix for the mirror-image race of #7839: deleteBlockingRepo's
// awaitDeletion can return immediately when GetRepo already 404s right
// after DeleteRepo (stubClient's default, non-stale behaviour), yet
// CreateRepo can keep rejecting the name as taken for a few seconds
// afterward. Without an explicit backoff in ensureRepoExists itself, the
// retry loop would fire back-to-back with no delay at all and could
// exhaust resetMaxAttempts well under the window the retry is meant to
// cover. blockedByExistingRepo exercises exactly this instant-404 path
// (staleGetAfterDelete is false), so the only source of elapsed time here
// is the explicit backoff — a regression to no backoff makes this test
// fail rather than pass vacuously.
func TestEnsureRepoExists_AlreadyExistsBacksOffEvenWhenGetRepoAlready404s(t *testing.T) {
	orig := resetRetryDelay
	resetRetryDelay = 40 * time.Millisecond
	t.Cleanup(func() { resetRetryDelay = orig })

	sc := &stubClient{blockedByExistingRepo: true}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	start := time.Now()
	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, int32(2), sc.createRepoCalled.Load())
	assert.GreaterOrEqual(t, elapsed, resetRetryDelay,
		"already-exists retry must back off even when GetRepo already 404s")
}

func TestEnsureRepoExists_CreateRepoError(t *testing.T) {
	sc := &stubClient{createRepoErr: fmt.Errorf("permission denied")}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.ensureRepoExists(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating repo")
	assert.Contains(t, err.Error(), "permission denied")
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
}

func TestEnsureRepoExists_ContextCancellation(t *testing.T) {
	speedUpResetRetries(t)
	sc := &stubClient{createRepoErr: forge.ErrAlreadyExists}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := e.ensureRepoExists(ctx, "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
	assert.Equal(t, int32(0), sc.createRepoCalled.Load())
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
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
		setupOpts: common.DefaultGitHubSetupOpts(),
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

func TestEnsurer_ConfigPreset_ForwardsConfigFlag(t *testing.T) {
	speedUpValidateRetries(t)

	var cliCalls [][]string
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: &stubClient{installed: true},
		binary: "/usr/bin/fullsend",
		token:  "tok",
		setupOpts: common.GitHubSetupOpts{
			Vendor:       true,
			ConfigPreset: "https://example.com/preset.yaml",
		},
		runCLI: func(_ string, _ string, args ...string) (string, error) {
			cliCalls = append(cliCalls, args)
			return "", nil
		},
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-preset")
	require.NoError(t, err)

	require.Len(t, cliCalls, 1)
	assert.Contains(t, cliCalls[0], "--config")
	assert.Contains(t, cliCalls[0], "https://example.com/preset.yaml")
	assert.NotContains(t, cliCalls[0], "--runtime")
	assert.Contains(t, cliCalls[0], "--vendor")
}

// --- InstallHooks tests ---

func TestDoEnsure_RunsBeforeAndAfterInstallHooksInOrder(t *testing.T) {
	speedUpValidateRetries(t)

	var order []string
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:    &stubClient{installed: true},
		binary:    "/usr/bin/fullsend",
		token:     "tok",
		setupOpts: common.DefaultGitHubSetupOpts(),
		runCLI: func(_, _ string, _ ...string) (string, error) {
			order = append(order, "install")
			return "", nil
		},
		hooks: InstallHooks{
			BeforeInstall: func(context.Context, forge.Client, string, string) (any, error) {
				order = append(order, "before")
				return "hook-state", nil
			},
			AfterInstall: func(_ context.Context, _ forge.Client, _, _ string, state any) error {
				order = append(order, fmt.Sprintf("after:%v", state))
				return nil
			},
		},
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-hooks")
	require.NoError(t, err)

	assert.Equal(t, []string{"before", "install", "after:hook-state"}, order,
		"BeforeInstall must run before github setup, AfterInstall after validation, threading hook state through")
}

func TestDoEnsure_BeforeInstallHookError_SkipsInstall(t *testing.T) {
	speedUpValidateRetries(t)

	installCalled := false
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:    &stubClient{installed: true},
		binary:    "/usr/bin/fullsend",
		token:     "tok",
		setupOpts: common.DefaultGitHubSetupOpts(),
		runCLI: func(_, _ string, _ ...string) (string, error) {
			installCalled = true
			return "", nil
		},
		hooks: InstallHooks{
			BeforeInstall: func(context.Context, forge.Client, string, string) (any, error) {
				return nil, fmt.Errorf("pre-install check failed")
			},
		},
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-hooks")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-install check failed")
	assert.False(t, installCalled, "github setup must not run when BeforeInstall fails")
}

func TestDoEnsure_AfterInstallHookError_Propagated(t *testing.T) {
	speedUpValidateRetries(t)

	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:    &stubClient{installed: true},
		binary:    "/usr/bin/fullsend",
		token:     "tok",
		setupOpts: common.DefaultGitHubSetupOpts(),
		runCLI:    noopCLI,
		hooks: InstallHooks{
			AfterInstall: func(context.Context, forge.Client, string, string, any) error {
				return fmt.Errorf("tracking issue setup failed")
			},
		},
		settle:  noopSettle,
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-hooks")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracking issue setup failed")
}

func TestDoEnsure_PlaybackRuntime_ValidatesAgainstInstalledRuntime(t *testing.T) {
	speedUpValidateRetries(t)

	playbackFiles := map[string][]byte{
		".github/workflows/fullsend.yaml": []byte("# shim"),
		".fullsend/config.yaml":           []byte("version: \"1\"\nruntime: dummy-playback\n"),
		scaffold.VendoredMarkerPath():     []byte("marker"),
		vendoredBinaryPathPerRepo:         []byte("binary"),
	}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: &stubClientWithCustomFiles{
			stubClient: stubClient{},
			files:      playbackFiles,
		},
		binary:    "/usr/bin/fullsend",
		token:     "tok",
		setupOpts: common.GitHubSetupOpts{Vendor: true, Runtime: "dummy-playback"},
		runCLI:    noopCLI,
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-playback")
	require.NoError(t, err)
}

func TestDoEnsure_PlaybackRuntime_DummyInstallFailsPlaybackValidation(t *testing.T) {
	speedUpValidateRetries(t)

	// Installed config still says "dummy" (e.g. a stale/incomplete
	// playback install), but the ensurer expects "dummy-playback" —
	// validation must catch the mismatch rather than silently pass.
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:    &stubClient{installed: true},
		binary:    "/usr/bin/fullsend",
		token:     "tok",
		setupOpts: common.GitHubSetupOpts{Vendor: true, Runtime: "dummy-playback"},
		runCLI:    noopCLI,
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-playback-mismatch")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want dummy-playback")
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
		setupOpts: common.DefaultGitHubSetupOpts(),
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

// --- DeleteRepo unit tests ---

func TestEnsurer_DeleteRepo_MissingRepo_OK(t *testing.T) {
	sc := &stubClient{getRepoErr: forge.ErrNotFound}
	e := &repoEnsurer{client: sc, logf: t.Logf, ensured: make(map[string]struct{})}

	err := e.DeleteRepo(context.Background(), "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, int32(0), sc.deleteRepoCalled.Load(), "should not delete missing repo")
}

func TestEnsurer_DeleteRepo_DeletesExistingAndClearsCache(t *testing.T) {
	sc := &stubClient{}
	e := &repoEnsurer{client: sc, logf: t.Logf, ensured: map[string]struct{}{"org/repo": {}}}

	err := e.DeleteRepo(context.Background(), "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load())

	e.mu.Lock()
	_, cached := e.ensured["org/repo"]
	e.mu.Unlock()
	assert.False(t, cached, "ensure cache must be invalidated on delete")
}

func TestEnsurer_DeleteRepo_InvalidatesCacheOnGetRepoError(t *testing.T) {
	sc := &stubClient{getRepoErr: assert.AnError}
	e := &repoEnsurer{client: sc, logf: t.Logf, ensured: map[string]struct{}{"org/repo": {}}}

	err := e.DeleteRepo(context.Background(), "org", "repo")
	require.Error(t, err)

	e.mu.Lock()
	_, cached := e.ensured["org/repo"]
	e.mu.Unlock()
	assert.False(t, cached, "cache must be invalidated even when delete fails")
}

func TestEnsurer_DeleteRepo_DeletesFork(t *testing.T) {
	sc := &stubClient{forkExists: true}
	e := &repoEnsurer{client: sc, logf: t.Logf, ensured: make(map[string]struct{})}

	err := e.DeleteRepo(context.Background(), "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.forkDeleteCalled.Load())
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load())
}

func TestEnsurer_DeleteThenEnsure_Recreates(t *testing.T) {
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{installed: true}
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	ctx := context.Background()
	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))
	createsAfterFirst := sc.createRepoCalled.Load()

	require.NoError(t, e.DeleteRepo(ctx, "org", "test-repo-01"))

	require.NoError(t, e.EnsureRepo(ctx, "org", "test-repo-01"))
	assert.Greater(t, sc.createRepoCalled.Load(), createsAfterFirst,
		"re-ensure after delete must recreate rather than hit the lease cache")
}

func TestEnsurer_StaleDeleteStillRecreates(t *testing.T) {
	// DeleteRepo succeeds but GetRepo keeps returning the old object.
	// Allocation must still CreateRepo rather than declare the stale
	// object ready and let a later github setup 404 (#7839).
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{
		installed:           true,
		staleGetAfterDelete: true,
	}
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-07")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load())
	assert.Equal(t, int32(1), sc.createRepoCalled.Load(),
		"stale GetRepo after delete must not skip CreateRepo")
}

func TestEnsurer_AlreadyExistsAfterStaleResetGetRepo_DeletesAndRecreates(t *testing.T) {
	// resetRepo's GetRepo sees a stale 404 for a repo that actually still
	// exists (a delete/create race, or a retried create POST that
	// succeeded server-side despite a client-side timeout), so it logs
	// "nothing to delete" and skips the delete entirely. CreateRepo then
	// reports already-exists; the fix must delete the blocking repo (and
	// any leftover fork) rather than backing off forever (#7839).
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	sc := &stubClient{
		installed:             true,
		getRepoErr:            forge.ErrNotFound, // resetRepo believes the repo is gone
		blockedByExistingRepo: true,              // but it is actually still there
	}
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-20")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load(),
		"the already-exists retry must delete the repo that resetRepo's stale GetRepo missed")
	assert.Equal(t, int32(2), sc.createRepoCalled.Load(),
		"create should retry once after the delete and succeed")
}

func TestEnsurer_DeleteNeverPropagates_Errors(t *testing.T) {
	// Deletion is accepted but the name never becomes free. The retry
	// loop must terminate with an error instead of hanging or treating
	// the leftover repo as allocation-ready.
	speedUpResetRetries(t)
	sc := &stubClient{
		staleGetAfterDelete: true,
		createRepoErr:       forge.ErrAlreadyExists,
	}
	e := &repoEnsurer{
		e2eCfg:    e2etest.EnvConfig{},
		client:    sc,
		runCLI:    noopCLI,
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		logf:      t.Logf,
		ensured:   make(map[string]struct{}),
	}

	err := e.EnsureRepo(context.Background(), "org", "test-repo-12")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name still taken")
	assert.Equal(t, int32(resetMaxAttempts), sc.createRepoCalled.Load())
	assert.Equal(t, int32(resetMaxAttempts), sc.deleteRepoCalled.Load(),
		"the already-exists retry loop must keep attempting deletes and still terminate when the name never frees up")
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
	require.NoError(t, err, "timeout hands off to ensureRepoExists create-with-retry")
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
