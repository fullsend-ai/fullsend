package suite

import (
	"context"
	"testing"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// panickingSCM is a fake scm.Driver whose CloseIssue panics.
// Used to verify that afterScenario's deferred pool.Release runs
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
func (p *panickingSCM) CommitFile(context.Context, string, string, string, string, []byte) error {
	return nil
}
func (p *panickingSCM) CreateBranch(context.Context, string, string, string) error { return nil }
func (p *panickingSCM) DeleteBranch(context.Context, string, string, string) error { return nil }
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
func (p *panickingSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (p *panickingSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (p *panickingSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

func TestTagNames(t *testing.T) {
	names := tagNames([]*messages.PickleTag{{Name: "@foo"}, {Name: "@bar"}})
	assert.Equal(t, []string{"@foo", "@bar"}, names)
}

func TestResetScenarioWorld_ClearsSharedState(t *testing.T) {
	w := &world.World{
		PRNumber:      99,
		DispatchAgent: "dispatch",
		IssueNumber:   1,
		ArtifactDir:   "/tmp/x",
		ForkOwner:     "org",
		ForkRepo:      "repo-fork",
		ForkPRNumber:  42,
		ForkPRBranch:  "branch",
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
}

func TestSkipErrorForTagNames(t *testing.T) {
	w := &world.World{Config: env.RunnerConfig{InstallMode: "per-repo", SCM: "github"}}

	tests := []struct {
		name    string
		tags    []string
		wantErr error
		cfg     env.RunnerConfig
	}{
		{name: "no tags", tags: nil, wantErr: nil},
		{name: "skip per-repo on per-repo", tags: []string{"@skip:per-repo"}, wantErr: godog.ErrSkip},
		{name: "skip per-org on per-repo", tags: []string{"@skip:per-org"}, wantErr: nil},
		{name: "requires per-repo on per-repo", tags: []string{"@requires:per-repo"}, wantErr: nil},
		{name: "requires per-repo on per-org", tags: []string{"@requires:per-repo"}, wantErr: godog.ErrSkip, cfg: env.RunnerConfig{InstallMode: "per-org"}},
		{name: "skip gitlab on github", tags: []string{"@skip:gitlab"}, wantErr: nil},
		{name: "skip gitlab on gitlab", tags: []string{"@skip:gitlab"}, wantErr: godog.ErrSkip, cfg: env.RunnerConfig{SCM: "gitlab"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ww := w
			if tt.cfg.InstallMode != "" || tt.cfg.SCM != "" {
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

// --- Before/After hook tests ---

func TestBeforeScenario_ClonesAndResetsWorld(t *testing.T) {
	template := &world.World{
		Org:         "test-org",
		RepoName:    "test-repo",
		IssueNumber: 42, // scenario field — should be zeroed by reset
	}

	ctx, err := beforeScenario(context.Background(), nil, template, nil)
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.NotSame(t, template, w)
	assert.Equal(t, "test-org", w.Org)
	assert.Equal(t, "test-repo", w.RepoName)
	assert.Equal(t, 0, w.IssueNumber, "scenario fields should be zeroed")
	assert.False(t, w.ScenarioStart.IsZero(), "ScenarioStart should be set")
}

func TestBeforeScenario_AcquiresPoolLease(t *testing.T) {
	template := &world.World{Org: "test-org"}
	pool, err := world.NewRepoPool(3)
	require.NoError(t, err)

	ctx, err := beforeScenario(context.Background(), nil, template, pool)
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.NotEmpty(t, w.LeasedRepoName)
	assert.Contains(t, w.LeasedRepoName, "test-repo-")
}

func TestBeforeScenario_PoolAcquireFailure(t *testing.T) {
	template := &world.World{Org: "test-org"}
	pool, err := world.NewRepoPool(1)
	require.NoError(t, err)

	// Exhaust the pool.
	_, err = pool.Acquire(context.Background())
	require.NoError(t, err)

	// Acquire with a cancelled context should fail.
	cancelledCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = beforeScenario(cancelledCtx, nil, template, pool)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acquiring pool repo name")
}

func TestBeforeScenario_NilPool(t *testing.T) {
	template := &world.World{Org: "test-org"}

	ctx, err := beforeScenario(context.Background(), nil, template, nil)
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.Empty(t, w.LeasedRepoName, "no pool → no leased name")
}

func TestAfterScenario_NilWorld(t *testing.T) {
	// When Before fails (e.g. tag skip), the After hook receives a context
	// with no World. It should pass through the original error unchanged.
	origErr := godog.ErrSkip
	ctx := context.Background() // no World stored

	_, err := afterScenario(ctx, nil, origErr)
	assert.Equal(t, origErr, err, "original error should be preserved")
}

func TestAfterScenario_ReleasesPoolLease(t *testing.T) {
	pool, err := world.NewRepoPool(1)
	require.NoError(t, err)

	name, err := pool.Acquire(context.Background())
	require.NoError(t, err)

	w := &world.World{LeasedRepoName: name}
	ctx := world.WithWorld(context.Background(), w)

	_, err = afterScenario(ctx, pool, nil)
	require.NoError(t, err)

	// The released name should be available for re-acquisition.
	got, err := pool.Acquire(context.Background())
	require.NoError(t, err)
	assert.Equal(t, name, got)
}

func TestAfterScenario_DoubleReleaseSurfacesError(t *testing.T) {
	pool, err := world.NewRepoPool(2)
	require.NoError(t, err)

	name, err := pool.Acquire(context.Background())
	require.NoError(t, err)

	// Release the name before After — simulates a double-release.
	require.NoError(t, pool.Release(name))

	w := &world.World{LeasedRepoName: name}
	ctx := world.WithWorld(context.Background(), w)

	// After should surface the release error, not panic.
	_, err = afterScenario(ctx, pool, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "releasing pool repo name")
}

func TestAfterScenario_PreservesOriginalError(t *testing.T) {
	pool, err := world.NewRepoPool(2)
	require.NoError(t, err)

	name, err := pool.Acquire(context.Background())
	require.NoError(t, err)

	w := &world.World{LeasedRepoName: name}
	ctx := world.WithWorld(context.Background(), w)

	origErr := assert.AnError
	_, err = afterScenario(ctx, pool, origErr)
	// Original error is preserved; release error is logged but not
	// returned when there is already an error from the scenario.
	assert.Equal(t, origErr, err)
}

func TestAfterScenario_ReleasesLeaseOnCleanupPanic(t *testing.T) {
	pool, err := world.NewRepoPool(1)
	require.NoError(t, err)

	name, err := pool.Acquire(context.Background())
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
	// deferred pool.Release must still run during stack unwinding.
	assert.Panics(t, func() {
		afterScenario(ctx, pool, nil) //nolint:errcheck // panic prevents return
	})

	// The deferred Release ran: the leased name is back in the pool
	// and can be re-acquired.
	got, err := pool.Acquire(context.Background())
	require.NoError(t, err)
	assert.Equal(t, name, got, "deferred Release should have returned the name to the pool")
}
