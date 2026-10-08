package suite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// panickingSCM is a fake scm.Driver whose CloseIssue panics.
// Used to verify that afterScenario's deferred driver.DeallocateRepo runs
// even when steps.CleanupScenario panics during issue cleanup.
type panickingSCM struct{}

func (p *panickingSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}
func (p *panickingSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}
func (p *panickingSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}
func (p *panickingSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}
func (p *panickingSCM) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}
func (p *panickingSCM) GetFileContentAtRef(context.Context, string, string, string, string) ([]byte, error) {
	return nil, nil
}
func (p *panickingSCM) CommitFile(context.Context, string, string, string, string, []byte) error {
	return nil
}
func (p *panickingSCM) CreateBranch(context.Context, string, string, string) error { return nil }
func (p *panickingSCM) DeleteBranch(context.Context, string, string, string) error { return nil }
func (p *panickingSCM) DeleteRepo(context.Context, string, string) error           { return nil }
func (p *panickingSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (p *panickingSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
func (p *panickingSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}
func (p *panickingSCM) CloseIssue(context.Context, string, string, int) error {
	panic("simulated cleanup panic in CloseIssue")
}
func (p *panickingSCM) CreateRepo(context.Context, string, string, string) error { return nil }
func (p *panickingSCM) EnsureRepoPublic(context.Context, string, string) error   { return nil }
func (p *panickingSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, nil
}
func (p *panickingSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}
func (p *panickingSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}
func (p *panickingSCM) GetBranchRef(context.Context, string, string, string) (string, error) {
	return "abc123", nil
}
func (p *panickingSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (p *panickingSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (p *panickingSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
func (p *panickingSCM) ListPullRequestCommits(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}

func (p *panickingSCM) ListPullRequestReviews(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
	return nil, nil
}
func (p *panickingSCM) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, nil
}

// fakeDriver is a minimal install.Driver for unit testing suite hooks.
type fakeDriver struct {
	mu          sync.Mutex
	allocated   int
	deallocated int
	outstanding map[string]struct{}
	names       chan string

	// onDeallocate, when set, runs at the start of DeallocateRepo so
	// tests can observe state at the moment the lease ends.
	onDeallocate func()
}

func newFakeDriver(capacity int) *fakeDriver {
	names := make(chan string, capacity)
	for i := 1; i <= capacity; i++ {
		names <- fmt.Sprintf("test-repo-%02d", i)
	}
	return &fakeDriver{
		names:       names,
		outstanding: make(map[string]struct{}),
	}
}

func (f *fakeDriver) AllocateRepo(ctx context.Context) (string, error) {
	select {
	case name := <-f.names:
		f.mu.Lock()
		f.allocated++
		f.outstanding[name] = struct{}{}
		f.mu.Unlock()
		return name, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeDriver) DeallocateRepo(_ context.Context, name string) error {
	if f.onDeallocate != nil {
		f.onDeallocate()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.outstanding[name]; !ok {
		return fmt.Errorf("not an outstanding lease: %s", name)
	}
	delete(f.outstanding, name)
	f.deallocated++
	f.names <- name
	return nil
}

func (f *fakeDriver) Finalize(_ context.Context) error { return nil }
func (f *fakeDriver) Capacity() int                    { return cap(f.names) }

var _ install.Driver = (*fakeDriver)(nil)

func TestTagNames(t *testing.T) {
	names := tagNames([]*messages.PickleTag{{Name: "@foo"}, {Name: "@bar"}})
	assert.Equal(t, []string{"@foo", "@bar"}, names)
}

func TestResetScenarioWorld_ClearsSharedState(t *testing.T) {
	w := &world.World{
		PRNumber:                   99,
		DispatchAgent:              "dispatch",
		IssueNumber:                1,
		ArtifactDir:                "/tmp/x",
		ForkOwner:                  "org",
		ForkRepo:                   "repo-fork",
		ForkPRNumber:               42,
		ForkPRBranch:               "branch",
		URLHarnessRepoOwner:        "org",
		URLHarnessRepoName:         "harness-host",
		AllowedResourcesOverridden: true,
		AllowedResourcesOriginal:   []string{"https://example.com/"},
		AgentsOverridden:           true,
		AgentsOriginal:             []config.AgentEntry{{Name: "test", Source: "harness/test.yaml"}},
		PlaybackEntries:            []runtime.PlaybackEntry{{Result: "triage/bug"}},
		PlaybackCommitted:          true,
		ConsumedHarnessRunIDs:      map[string]map[int]bool{"review": {10: true}},
	}
	resetScenarioWorld(w)
	assert.Equal(t, 0, w.PRNumber)
	assert.Equal(t, "", w.DispatchAgent)
	assert.Equal(t, 0, w.IssueNumber)
	assert.Equal(t, "", w.ArtifactDir)
	assert.False(t, w.ScenarioStart.IsZero())
	assert.Equal(t, "", w.ForkOwner)
	assert.Equal(t, "", w.ForkRepo)
	assert.Equal(t, 0, w.ForkPRNumber)
	assert.Equal(t, "", w.ForkPRBranch)
	assert.Equal(t, "", w.URLHarnessRepoOwner)
	assert.Equal(t, "", w.URLHarnessRepoName)
	assert.False(t, w.AllowedResourcesOverridden)
	assert.Nil(t, w.AllowedResourcesOriginal)
	assert.False(t, w.AgentsOverridden)
	assert.Nil(t, w.AgentsOriginal)
	assert.Nil(t, w.PlaybackEntries)
	assert.False(t, w.PlaybackCommitted)
	assert.Nil(t, w.ConsumedHarnessRunIDs)
}

func TestSkipErrorForTagNames(t *testing.T) {
	w := &world.World{Config: env.RunnerConfig{InstallMode: "per-repo", SCM: "github"}}
	playbackWorld := &world.World{
		Config: env.RunnerConfig{InstallMode: "per-repo", SCM: "github"},
		Driver: install.NewPlaybackDriver(newFakeDriver(1)),
	}

	tests := []struct {
		name    string
		tags    []string
		wantErr error
		cfg     env.RunnerConfig
		world   *world.World
	}{
		{name: "no tags", tags: nil, wantErr: nil},
		{name: "skip per-repo on per-repo", tags: []string{"@skip:per-repo"}, wantErr: godog.ErrSkip},
		{name: "skip gitlab on github", tags: []string{"@skip:gitlab"}, wantErr: nil},
		{name: "skip gitlab on gitlab", tags: []string{"@skip:gitlab"}, wantErr: godog.ErrSkip, cfg: env.RunnerConfig{SCM: "gitlab"}},
		{name: "requires capability undeclared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: godog.ErrSkip},
		{name: "requires capability declared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: nil,
			cfg: env.RunnerConfig{InstallMode: "per-repo", SCM: "github", Capabilities: []string{"applier-branch-namespace"}}},
		{name: "requires capability other declared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: godog.ErrSkip,
			cfg: env.RunnerConfig{InstallMode: "per-repo", SCM: "github", Capabilities: []string{"something-else"}}},
		{name: "playback tag outside playback suite", tags: []string{"@playback"}, wantErr: godog.ErrSkip},
		{name: "playback tag inside playback suite", tags: []string{"@playback"}, wantErr: nil, world: playbackWorld},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ww := w
			switch {
			case tt.world != nil:
				ww = tt.world
			case tt.cfg.InstallMode != "" || tt.cfg.SCM != "":
				ww = &world.World{Config: tt.cfg}
			}
			err := SkipErrorForTagNames(tt.tags, ww)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSkipErrorForTagNames_MalformedCapabilityTag(t *testing.T) {
	w := &world.World{Config: env.RunnerConfig{InstallMode: "per-repo", SCM: "github"}}
	err := SkipErrorForTagNames([]string{"@requires:capability:"}, w)
	require.Error(t, err)
	assert.NotErrorIs(t, err, godog.ErrSkip, "an empty capability name is a tag-authoring mistake, not a normal skip")
	assert.Contains(t, err.Error(), "needs a name")
}

// --- Before/After hook tests ---

func TestBeforeScenario_ClonesAndResetsWorld(t *testing.T) {
	template := &world.World{
		Org:         "test-org",
		RepoName:    "test-repo",
		IssueNumber: 42, // scenario field — should be zeroed by reset
	}

	ctx, err := beforeScenario(context.Background(), nil, template, "a scenario")
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.NotSame(t, template, w)
	assert.Equal(t, "test-org", w.Org)
	assert.Equal(t, "test-repo", w.RepoName)
	assert.Equal(t, 0, w.IssueNumber, "scenario fields should be zeroed")
	assert.False(t, w.ScenarioStart.IsZero(), "ScenarioStart should be set")
	assert.Equal(t, w.ScenarioStart, w.ScenarioBegin, "ScenarioBegin should be set")
	assert.Equal(t, "a scenario", w.ScenarioName)
}

func TestBeforeScenario_NoPoolAcquire(t *testing.T) {
	driver := newFakeDriver(3)
	template := &world.World{Org: "test-org", Driver: driver}

	ctx, err := beforeScenario(context.Background(), nil, template, "a scenario")
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	// Before hook no longer acquires a pool lease — that is done by
	// AllocateRepo in the step.
	assert.Empty(t, w.LeasedRepoName, "Before hook should not acquire a lease")
	assert.Equal(t, 0, driver.allocated, "driver should not be called in Before")
}

func TestBeforeScenario_NilDriver(t *testing.T) {
	template := &world.World{Org: "test-org"}

	ctx, err := beforeScenario(context.Background(), nil, template, "a scenario")
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.Empty(t, w.LeasedRepoName, "no driver → no leased name")
}

func TestBeforeScenario_PlaybackDriverGetsRepoHint(t *testing.T) {
	base := newFakeDriver(1)
	pd := install.NewPlaybackDriver(base)
	template := &world.World{Org: "test-org", Driver: pd}

	// SetRepoHint on PlaybackDriver is a no-op (pool repos have stable
	// names), so this only verifies that beforeScenario's type assertion
	// finds the PlaybackDriver and calls it without error.
	ctx, err := beforeScenario(context.Background(), nil, template, "A Playback Scenario")
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.True(t, w.IsPlaybackMode())
}

func TestAfterScenario_NilWorld(t *testing.T) {
	// When Before fails (e.g. tag skip), the After hook receives a context
	// with no World. It should pass through the original error unchanged.
	origErr := godog.ErrSkip
	ctx := context.Background() // no World stored

	_, err := afterScenario(ctx, nil, origErr)
	assert.Equal(t, origErr, err, "original error should be preserved")
}

func TestAfterScenario_DeallocatesRepo(t *testing.T) {
	driver := newFakeDriver(1)

	name, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)

	w := &world.World{LeasedRepoName: name}
	ctx := world.WithWorld(context.Background(), w)

	_, err = afterScenario(ctx, driver, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, driver.deallocated)

	// The deallocated name should be available for re-allocation.
	got, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, name, got)
}

func TestAfterScenario_DoubleDeallocateSurfacesError(t *testing.T) {
	driver := newFakeDriver(2)

	name, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)

	// Deallocate before After — simulates a double-release.
	require.NoError(t, driver.DeallocateRepo(context.Background(), name))

	w := &world.World{LeasedRepoName: name}
	ctx := world.WithWorld(context.Background(), w)

	// After should surface the deallocation error, not panic.
	_, err = afterScenario(ctx, driver, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deallocating repo")
}

func TestAfterScenario_PreservesOriginalError(t *testing.T) {
	driver := newFakeDriver(2)

	name, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)

	w := &world.World{LeasedRepoName: name}
	ctx := world.WithWorld(context.Background(), w)

	origErr := assert.AnError
	_, err = afterScenario(ctx, driver, origErr)
	// Original error is preserved; deallocation error (if any) is logged
	// but not returned when there is already an error from the scenario.
	assert.Equal(t, origErr, err)
}

func TestAfterScenario_DeallocatesOnCleanupPanic(t *testing.T) {
	driver := newFakeDriver(1)

	name, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)

	// Build a World whose cleanup will panic: panickingSCM.CloseIssue
	// panics, and IssueNumber > 0 triggers that code path in
	// steps.CleanupScenario.
	w := &world.World{
		SCM:            &panickingSCM{},
		IssueNumber:    1,
		LeasedRepoName: name,
	}
	ctx := world.WithWorld(context.Background(), w)

	// afterScenario should panic (from CleanupScenario), but the
	// deferred DeallocateRepo must still run during stack unwinding.
	assert.Panics(t, func() {
		afterScenario(ctx, driver, nil) //nolint:errcheck // panic prevents return
	})

	// The deferred DeallocateRepo ran: the leased name is back in the
	// driver and can be re-allocated.
	got, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, name, got, "deferred DeallocateRepo should have returned the name")
}

func TestAfterScenario_NoLeaseNoDeallocation(t *testing.T) {
	driver := newFakeDriver(2)

	// Scenario that never allocated a repo.
	w := &world.World{}
	ctx := world.WithWorld(context.Background(), w)

	_, err := afterScenario(ctx, driver, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, driver.deallocated, "should not deallocate when no lease exists")
}

func TestAfterScenario_AllocateBlocksUntilDeallocate(t *testing.T) {
	driver := newFakeDriver(1)

	name, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)

	// Allocate with a short-timeout context should fail (pool exhausted).
	shortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = driver.AllocateRepo(shortCtx)
	require.Error(t, err)

	// After deallocates the first name.
	w := &world.World{LeasedRepoName: name}
	ctx := world.WithWorld(context.Background(), w)
	_, err = afterScenario(ctx, driver, nil)
	require.NoError(t, err)

	// Now a second allocation should succeed.
	name2, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, name2)
}

// TestAfterScenario_FailedScenarioWritesFailureSummaryBeforeDeallocating
// checks that a failed scenario leaves a failure summary under
// BEHAVIOUR_ARTIFACT_DIR, written before the leased repo — and with it
// the run logs — is deallocated, even when no step resolved a run.
func TestAfterScenario_FailedScenarioWritesFailureSummaryBeforeDeallocating(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	driver := newFakeDriver(1)
	name, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)

	summaryAtDealloc := 0
	driver.onDeallocate = func() {
		matches, _ := filepath.Glob(filepath.Join(artifactDir, "debug-scenario-*", "failure-summary.txt"))
		summaryAtDealloc = len(matches)
	}

	w := &world.World{LeasedRepoName: name, ScenarioName: "times out"}
	ctx := world.WithWorld(context.Background(), w)

	origErr := fmt.Errorf("harness agent did not complete")
	_, err = afterScenario(ctx, driver, origErr)
	assert.Equal(t, origErr, err)
	assert.Equal(t, 1, summaryAtDealloc, "the failure summary must exist before the repo is deallocated")
}

// TestAfterScenario_DeallocationFailureWritesFailureSummary checks that a
// passing scenario whose deferred deallocation fails still leaves a failure
// summary naming the deallocation error.
func TestAfterScenario_DeallocationFailureWritesFailureSummary(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	driver := newFakeDriver(2)
	name, err := driver.AllocateRepo(context.Background())
	require.NoError(t, err)
	// Deallocate before After — the hook's own deallocation then fails.
	require.NoError(t, driver.DeallocateRepo(context.Background(), name))

	w := &world.World{LeasedRepoName: name, ScenarioName: "passes but leaks"}
	ctx := world.WithWorld(context.Background(), w)

	_, err = afterScenario(ctx, driver, nil)
	require.Error(t, err)

	matches, globErr := filepath.Glob(filepath.Join(artifactDir, "debug-scenario-*", "failure-summary.txt"))
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	data, readErr := os.ReadFile(matches[0])
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "Scenario: passes but leaks")
	assert.Contains(t, string(data), "deallocating repo")
}

func TestAfterScenario_NoFailureSummaryForPassingOrSkippedScenario(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	for _, scenarioErr := range []error{nil, godog.ErrSkip} {
		w := &world.World{ScenarioName: "fine"}
		ctx := world.WithWorld(context.Background(), w)
		_, err := afterScenario(ctx, nil, scenarioErr)
		assert.Equal(t, scenarioErr, err)
	}

	entries, err := os.ReadDir(artifactDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "only failed scenarios write a failure summary")
}

// TestBeforeToAfter_MalformedCapabilityTagWritesFailureSummary checks that a
// scenario failed by Before-hook tag validation still leaves a failure
// summary: Before attaches a named World even though it returns an error, so
// the After hook does not return at its nil-World guard.
func TestBeforeToAfter_MalformedCapabilityTagWritesFailureSummary(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	template := &world.World{Config: env.RunnerConfig{InstallMode: "per-repo", SCM: "github"}}
	ctx, beforeErr := beforeScenario(context.Background(), []string{"@requires:capability:"}, template, "malformed tag scenario")
	require.Error(t, beforeErr)
	require.NotErrorIs(t, beforeErr, godog.ErrSkip)
	require.NotNil(t, world.FromContext(ctx), "the failed Before hook must still attach a World")

	_, err := afterScenario(ctx, nil, fmt.Errorf("before scenario hook failed: %w", beforeErr))
	require.Error(t, err)

	matches, globErr := filepath.Glob(filepath.Join(artifactDir, "debug-scenario-*", "failure-summary.txt"))
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	data, readErr := os.ReadFile(matches[0])
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "Scenario: malformed tag scenario")
	assert.Contains(t, string(data), "needs a name")
}

// TestBeforeScenario_SkipDoesNotAttachWorld checks a normal tag skip keeps
// returning ErrSkip without a World, so the After hook does nothing for it.
func TestBeforeScenario_SkipDoesNotAttachWorld(t *testing.T) {
	template := &world.World{Config: env.RunnerConfig{InstallMode: "per-repo", SCM: "github"}}
	ctx, err := beforeScenario(context.Background(), []string{"@skip:per-repo"}, template, "skipped")
	require.ErrorIs(t, err, godog.ErrSkip)
	assert.Nil(t, world.FromContext(ctx))
}
