package install

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// --- generateRepoName unit tests ---

func TestGenerateRepoName_ContainsPrefix(t *testing.T) {
	name := generateRepoName("test scenario")
	assert.True(t, strings.HasPrefix(name, "bt-"), "name should start with bt- prefix")
}

func TestGenerateRepoName_Format(t *testing.T) {
	name := generateRepoName("test scenario")
	// Should be "bt-{8hex}-{8hex}" = 20 chars.
	assert.Len(t, name, 20)
}

func TestGenerateRepoName_UniquePerCall(t *testing.T) {
	a := generateRepoName("test scenario")
	b := generateRepoName("test scenario")
	assert.NotEqual(t, a, b, "each call should produce a unique name (different UUID)")
}

func TestGenerateRepoName_SameHashForSameHint(t *testing.T) {
	a := generateRepoName("test scenario")
	b := generateRepoName("test scenario")
	// Different UUIDs but same hash suffix.
	assert.Equal(t, a[12:], b[12:], "hash suffix should be deterministic for the same hint")
}

func TestGenerateRepoName_DifferentHashForDifferentHint(t *testing.T) {
	a := generateRepoName("scenario A")
	b := generateRepoName("scenario B")
	assert.NotEqual(t, a[12:], b[12:], "different hints should produce different hash suffixes")
}

func TestScenarioHash_Deterministic(t *testing.T) {
	a := scenarioHash("test scenario")
	b := scenarioHash("test scenario")
	assert.Equal(t, a, b)
	assert.Len(t, a, 8)
}

// --- stubClient for ensure tests ---

type stubClient struct {
	forge.Client

	createRepoErr    error
	createRepoCalled atomic.Int32

	getRefFailures int // how many GetRef calls fail before succeeding (-1 = always fail)
	getRefCalls    atomic.Int32

	getWorkflowErr      error
	getWorkflowCalled   atomic.Int32
	getWorkflowFailures int // how many GetWorkflow calls fail before succeeding (-1 = always fail)
	workflowReady       bool

	getFileFn func(ctx context.Context, owner, repo, path string) ([]byte, error)

	createIssueErr         error
	createCommentErr       error
	listWorkflowRunsResult []forge.WorkflowRun
	listWorkflowRunsErr    error
}

func (s *stubClient) CreateRepo(_ context.Context, _, _, _ string, _ bool) (*forge.Repository, error) {
	s.createRepoCalled.Add(1)
	if s.createRepoErr != nil {
		return nil, s.createRepoErr
	}
	return &forge.Repository{}, nil
}

func (s *stubClient) GetRef(_ context.Context, _, _, _ string) (string, error) {
	call := int(s.getRefCalls.Add(1))
	if s.getRefFailures == -1 {
		return "", fmt.Errorf("github api: 409 Git Repository is empty")
	}
	if call <= s.getRefFailures {
		return "", fmt.Errorf("github api: 409 Git Repository is empty")
	}
	return "abc123", nil
}

func (s *stubClient) GetWorkflow(_ context.Context, _, _, _ string) (*forge.Workflow, error) {
	call := int(s.getWorkflowCalled.Add(1))
	if s.getWorkflowErr != nil {
		return nil, s.getWorkflowErr
	}
	if s.getWorkflowFailures == -1 {
		return nil, forge.ErrNotFound
	}
	if s.getWorkflowFailures > 0 && call <= s.getWorkflowFailures {
		return nil, forge.ErrNotFound
	}
	if !s.workflowReady && s.getWorkflowFailures == 0 {
		return nil, forge.ErrNotFound
	}
	return &forge.Workflow{ID: 1, Name: "fullsend", Path: ".github/workflows/fullsend.yaml", State: "active"}, nil
}

func (s *stubClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	if s.getFileFn != nil {
		return s.getFileFn(ctx, owner, repo, path)
	}
	return nil, forge.ErrNotFound
}

func (s *stubClient) CreateIssue(_ context.Context, _, _, _, _ string, _ ...string) (*forge.Issue, error) {
	if s.createIssueErr != nil {
		return nil, s.createIssueErr
	}
	return &forge.Issue{Number: 1}, nil
}

func (s *stubClient) CreateIssueComment(_ context.Context, _, _ string, _ int, _ string) (*forge.IssueComment, error) {
	if s.createCommentErr != nil {
		return nil, s.createCommentErr
	}
	return &forge.IssueComment{ID: 1}, nil
}

func (s *stubClient) ListWorkflowRuns(_ context.Context, _, _, _ string) ([]forge.WorkflowRun, error) {
	if s.listWorkflowRunsErr != nil {
		return nil, s.listWorkflowRunsErr
	}
	return s.listWorkflowRunsResult, nil
}

// noopCLI is a CLIRunnerFunc that succeeds without doing anything.
func noopCLI(_, _ string, _ ...string) (string, error) { return "", nil }

// noopSettle is a SettleFunc that does nothing.
func noopSettle(_ context.Context, _ forge.Client, _, _, _ string, _ func(string, ...any)) error {
	return nil
}

// noopEventDelivery is an EventDeliveryFunc that does nothing.
func noopEventDelivery(_ context.Context, _ forge.Client, _, _ string, _ int, _ string, _ func(string, ...any)) error {
	return nil
}

// speedUpGitReadyPoll sets gitReadyPoll to zero for fast tests.
func speedUpGitReadyPoll(t *testing.T) {
	t.Helper()
	orig := gitReadyPoll
	gitReadyPoll = 0
	t.Cleanup(func() { gitReadyPoll = orig })
}

// speedUpSettlePoll sets settlePoll to zero for fast tests.
func speedUpSettlePoll(t *testing.T) {
	t.Helper()
	orig := settlePoll
	settlePoll = 0
	t.Cleanup(func() { settlePoll = orig })
}

// validPerRepoConfig is the minimal YAML that passes
// config.ParsePerRepoConfig + Validate + Runtime == "dummy".
const validPerRepoConfig = `version: "1"
runtime: dummy
`

// installedGetFileFn returns valid post-install files for validation.
func installedGetFileFn(_ context.Context, _, _, path string) ([]byte, error) {
	switch path {
	case ".github/workflows/fullsend.yaml":
		return []byte("# shim"), nil
	case ".fullsend/config.yaml":
		return []byte(validPerRepoConfig), nil
	case ".defaults/action.yml":
		return []byte("# vendored marker"), nil
	case ".fullsend/bin/fullsend":
		return []byte("#!/bin/sh"), nil
	default:
		return nil, forge.ErrNotFound
	}
}

// --- newRepoEnsurer tests ---

func TestNewRepoEnsurer_ReturnsNonNil(t *testing.T) {
	sc := &stubClient{}
	e := newRepoEnsurer(e2etest.EnvConfig{}, sc, "tok", "/bin/true", "", t.Logf)
	require.NotNil(t, e)
	var _ ensurer = e
}

// --- CreateRepo tests ---

func TestCreateRepo_Success(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{
		workflowReady: true,
		getFileFn:     installedGetFileFn,
	}
	e := &repoEnsurer{
		e2eCfg:        e2etest.EnvConfig{},
		client:        sc,
		runCLI:        noopCLI,
		settle:        noopSettle,
		eventDelivery: noopEventDelivery,
		logf:          t.Logf,
	}

	name, err := e.CreateRepo(context.Background(), "org", "triage scenario")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(name, "bt-"))
	assert.Equal(t, int32(1), sc.createRepoCalled.Load())
}

func TestCreateRepo_CreateRepoError(t *testing.T) {
	sc := &stubClient{createRepoErr: fmt.Errorf("permission denied")}
	e := &repoEnsurer{
		client: sc,
		runCLI: noopCLI,
		logf:   t.Logf,
	}

	_, err := e.CreateRepo(context.Background(), "org", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating repo")
	assert.Contains(t, err.Error(), "permission denied")
}

func TestCreateRepo_InstallCLIError(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, _ ...string) (string, error) {
			return "", fmt.Errorf("cli exploded")
		},
		logf: t.Logf,
	}

	_, err := e.CreateRepo(context.Background(), "org", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cli exploded")
}

func TestCreateRepo_SettleError(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{getFileFn: installedGetFileFn}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{},
		client: sc,
		runCLI: noopCLI,
		settle: func(_ context.Context, _ forge.Client, _, _, _ string, _ func(string, ...any)) error {
			return fmt.Errorf("Actions not ready")
		},
		logf: t.Logf,
	}

	_, err := e.CreateRepo(context.Background(), "org", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "waiting for Actions readiness")
}

// --- awaitGitReady tests ---

func TestAwaitGitReady_ImmediateSuccess(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.awaitGitReady(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.getRefCalls.Load())
}

func TestAwaitGitReady_RetriesThenSucceeds(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{getRefFailures: 3}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.awaitGitReady(context.Background(), "org", "repo", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, int32(4), sc.getRefCalls.Load())
}

func TestAwaitGitReady_ExhaustsAttempts(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{getRefFailures: -1}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	err := e.awaitGitReady(context.Background(), "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready after")
	assert.Equal(t, int32(gitReadyMaxAttempts), sc.getRefCalls.Load())
}

func TestAwaitGitReady_ContextCancelled(t *testing.T) {
	sc := &stubClient{getRefFailures: -1}
	e := &repoEnsurer{client: sc, logf: t.Logf}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.awaitGitReady(ctx, "org", "repo", "org/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

// --- awaitWorkflowReady tests ---

func TestAwaitWorkflowReady_ImmediateSuccess(t *testing.T) {
	sc := &stubClient{workflowReady: true}
	err := awaitWorkflowReady(context.Background(), sc, "org", "repo", "fullsend.yaml", t.Logf)
	require.NoError(t, err)
	assert.Equal(t, int32(1), sc.getWorkflowCalled.Load())
}

func TestAwaitWorkflowReady_ContextCancelled(t *testing.T) {
	sc := &stubClient{getWorkflowErr: forge.ErrNotFound}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := awaitWorkflowReady(ctx, sc, "org", "repo", "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

func TestAwaitWorkflowReady_RetriesThenSucceeds(t *testing.T) {
	speedUpSettlePoll(t)
	sc := &stubClient{getWorkflowFailures: 3}

	err := awaitWorkflowReady(context.Background(), sc, "org", "repo", "fullsend.yaml", t.Logf)
	require.NoError(t, err)
	assert.Equal(t, int32(4), sc.getWorkflowCalled.Load())
}

func TestAwaitWorkflowReady_ExhaustsAttempts(t *testing.T) {
	speedUpSettlePoll(t)
	sc := &stubClient{getWorkflowFailures: -1}

	err := awaitWorkflowReady(context.Background(), sc, "org", "repo", "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not visible")
	assert.Contains(t, err.Error(), fmt.Sprintf("%d attempts", settleMaxAttempts))
}

func TestCreateRepo_ValidationError(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{
		getFileFn: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return nil, forge.ErrNotFound
		},
	}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{},
		client: sc,
		runCLI: noopCLI,
		settle: noopSettle,
		logf:   t.Logf,
	}

	_, err := e.CreateRepo(context.Background(), "org", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "post-install validation")
}

func TestCreateRepo_NilSettle(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{getFileFn: installedGetFileFn}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{},
		client: sc,
		runCLI: noopCLI,
		settle: nil,
		logf:   t.Logf,
	}

	name, err := e.CreateRepo(context.Background(), "org", "test")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(name, "bt-"))
}

// --- fakeEnsurer ---

type fakeEnsurer struct {
	calls atomic.Int32
}

func (f *fakeEnsurer) CreateRepo(_ context.Context, _, hint string) (string, error) {
	f.calls.Add(1)
	return fmt.Sprintf("bt-fake-%s", hint), nil
}

var _ ensurer = (*fakeEnsurer)(nil)

func TestFakeEnsurer_Succeeds(t *testing.T) {
	e := &fakeEnsurer{}
	name, err := e.CreateRepo(context.Background(), "org", "test")
	require.NoError(t, err)
	assert.Equal(t, "bt-fake-test", name)
}

// --- awaitEventDelivery tests ---

func TestAwaitEventDelivery_ImmediateSuccess(t *testing.T) {
	speedUpSettlePoll(t)
	sc := &stubClient{
		listWorkflowRunsResult: []forge.WorkflowRun{
			{ID: 42, Status: "completed", CreatedAt: time.Now().Format(time.RFC3339)},
		},
	}
	err := awaitEventDelivery(context.Background(), sc, "org", "repo", 1, "fullsend.yaml", t.Logf)
	require.NoError(t, err)
}

func TestAwaitEventDelivery_CommentError(t *testing.T) {
	sc := &stubClient{
		createCommentErr: fmt.Errorf("comment failed"),
	}
	err := awaitEventDelivery(context.Background(), sc, "org", "repo", 1, "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "posting warmup comment")
}

func TestAwaitEventDelivery_ExhaustsAttempts(t *testing.T) {
	speedUpSettlePoll(t)
	sc := &stubClient{}
	err := awaitEventDelivery(context.Background(), sc, "org", "repo", 1, "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event delivery not confirmed")
}

func TestAwaitEventDelivery_IgnoresInProgressRuns(t *testing.T) {
	speedUpSettlePoll(t)
	sc := &stubClient{
		listWorkflowRunsResult: []forge.WorkflowRun{
			{ID: 42, Status: "in_progress", CreatedAt: time.Now().Format(time.RFC3339)},
		},
	}
	err := awaitEventDelivery(context.Background(), sc, "org", "repo", 1, "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event delivery not confirmed")
}

func TestAwaitEventDelivery_IgnoresOldRuns(t *testing.T) {
	speedUpSettlePoll(t)
	sc := &stubClient{
		listWorkflowRunsResult: []forge.WorkflowRun{
			{ID: 42, Status: "completed", CreatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339)},
		},
	}
	err := awaitEventDelivery(context.Background(), sc, "org", "repo", 1, "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event delivery not confirmed")
}

func TestAwaitEventDelivery_ContextCancelled(t *testing.T) {
	sc := &stubClient{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := awaitEventDelivery(ctx, sc, "org", "repo", 1, "fullsend.yaml", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

func TestCreateRepo_EventDeliveryError(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{getFileFn: installedGetFileFn}
	e := &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{},
		client: sc,
		runCLI: noopCLI,
		settle: noopSettle,
		eventDelivery: func(_ context.Context, _ forge.Client, _, _ string, _ int, _ string, _ func(string, ...any)) error {
			return fmt.Errorf("event delivery failed")
		},
		logf: t.Logf,
	}

	_, err := e.CreateRepo(context.Background(), "org", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verifying event delivery")
}

func TestCreateRepo_WarmupIssueError(t *testing.T) {
	speedUpGitReadyPoll(t)
	sc := &stubClient{createIssueErr: fmt.Errorf("issue creation failed")}
	e := &repoEnsurer{
		e2eCfg:        e2etest.EnvConfig{},
		client:        sc,
		runCLI:        noopCLI,
		settle:        noopSettle,
		eventDelivery: noopEventDelivery,
		logf:          t.Logf,
	}

	_, err := e.CreateRepo(context.Background(), "org", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating warmup issue")
}
