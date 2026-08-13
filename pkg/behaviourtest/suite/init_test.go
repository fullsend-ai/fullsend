package suite

import (
	"context"
	"fmt"
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// panickingSCM is a fake scm.Driver whose CloseIssue panics.
// Used to verify that afterScenario's deferred DeallocateRepo runs
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

// fakeDriver is a minimal install.Driver for unit testing the suite
// lifecycle hooks. It tracks allocations and deallocations.
type fakeDriver struct {
	capacity     int
	allocated    []string
	deallocated  []string
	allocateErr  error
	deallocateRv error
	nextName     string
}

func (f *fakeDriver) AllocateRepo(_ context.Context) (string, error) {
	if f.allocateErr != nil {
		return "", f.allocateErr
	}
	name := f.nextName
	f.allocated = append(f.allocated, name)
	return name, nil
}

func (f *fakeDriver) DeallocateRepo(_ context.Context, name string) error {
	f.deallocated = append(f.deallocated, name)
	return f.deallocateRv
}

func (f *fakeDriver) Finalize(_ context.Context) error { return nil }
func (f *fakeDriver) Capacity() int                    { return f.capacity }

var _ install.Driver = (*fakeDriver)(nil)

func TestTagNames(t *testing.T) {
	names := tagNames([]*messages.PickleTag{{Name: "@foo"}, {Name: "@bar"}})
	assert.Equal(t, []string{"@foo", "@bar"}, names)
}

func TestResetScenarioWorld_ClearsSharedState(t *testing.T) {
	w := &world.World{
		PRNumber:            99,
		DispatchAgent:       "dispatch",
		IssueNumber:         1,
		ArtifactDir:         "/tmp/x",
		ForkOwner:           "org",
		ForkRepo:            "repo-fork",
		ForkPRNumber:        42,
		ForkPRBranch:        "branch",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "harness-host",
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
		{name: "requires capability undeclared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: godog.ErrSkip},
		{name: "requires capability declared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: nil,
			cfg: env.RunnerConfig{InstallMode: "per-repo", SCM: "github", Capabilities: []string{"applier-branch-namespace"}}},
		{name: "requires capability other declared", tags: []string{"@requires:capability:applier-branch-namespace"}, wantErr: godog.ErrSkip,
			cfg: env.RunnerConfig{InstallMode: "per-repo", SCM: "github", Capabilities: []string{"something-else"}}},
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

	ctx, err := beforeScenario(context.Background(), nil, template)
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.NotSame(t, template, w)
	assert.Equal(t, "test-org", w.Org)
	assert.Equal(t, "test-repo", w.RepoName)
	assert.Equal(t, 0, w.IssueNumber, "scenario fields should be zeroed")
	assert.False(t, w.ScenarioStart.IsZero(), "ScenarioStart should be set")
}

func TestBeforeScenario_NoPoolAcquire(t *testing.T) {
	// With the unified driver, beforeScenario no longer acquires a
	// pool lease. Allocation is deferred to the step.
	template := &world.World{Org: "test-org"}

	ctx, err := beforeScenario(context.Background(), nil, template)
	require.NoError(t, err)

	w := world.FromContext(ctx)
	require.NotNil(t, w)
	assert.Empty(t, w.LeasedRepoName, "Before hook should not allocate a repo")
}

func TestAfterScenario_NilWorld(t *testing.T) {
	// When Before fails (e.g. tag skip), the After hook receives a context
	// with no World. It should pass through the original error unchanged.
	origErr := godog.ErrSkip
	ctx := context.Background() // no World stored

	_, err := afterScenario(ctx, origErr)
	assert.Equal(t, origErr, err, "original error should be preserved")
}

func TestAfterScenario_DeallocatesRepo(t *testing.T) {
	drv := &fakeDriver{capacity: 3, nextName: "test-repo-01"}
	w := &world.World{
		LeasedRepoName: "test-repo-01",
		RepoDriver:     drv,
	}
	ctx := world.WithWorld(context.Background(), w)

	_, err := afterScenario(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"test-repo-01"}, drv.deallocated)
}

func TestAfterScenario_DeallocateError_SurfacedWhenNoScenarioError(t *testing.T) {
	drv := &fakeDriver{
		capacity:     3,
		deallocateRv: fmt.Errorf("double-release"),
	}
	w := &world.World{
		LeasedRepoName: "test-repo-01",
		RepoDriver:     drv,
		Logf:           func(string, ...any) {},
	}
	ctx := world.WithWorld(context.Background(), w)

	_, err := afterScenario(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deallocating repo")
}

func TestAfterScenario_PreservesOriginalError(t *testing.T) {
	drv := &fakeDriver{capacity: 3}
	w := &world.World{
		LeasedRepoName: "test-repo-01",
		RepoDriver:     drv,
	}
	ctx := world.WithWorld(context.Background(), w)

	origErr := assert.AnError
	_, err := afterScenario(ctx, origErr)
	// Original error is preserved; dealloc error is logged but not
	// returned when there is already an error from the scenario.
	assert.Equal(t, origErr, err)
}

func TestAfterScenario_NoDriverOrNoLease(t *testing.T) {
	// No driver → no deallocation attempt.
	w := &world.World{LeasedRepoName: "test-repo-01"}
	ctx := world.WithWorld(context.Background(), w)
	_, err := afterScenario(ctx, nil)
	require.NoError(t, err)

	// No leased name → no deallocation attempt.
	drv := &fakeDriver{capacity: 3}
	w2 := &world.World{RepoDriver: drv}
	ctx2 := world.WithWorld(context.Background(), w2)
	_, err = afterScenario(ctx2, nil)
	require.NoError(t, err)
	assert.Empty(t, drv.deallocated, "no deallocation when LeasedRepoName is empty")
}

func TestAfterScenario_DeallocatesOnCleanupPanic(t *testing.T) {
	drv := &fakeDriver{capacity: 1, nextName: "test-repo-01"}

	// Build a World whose cleanup will panic: panickingSCM.CloseIssue
	// panics, and IssueNumber > 0 triggers that code path in
	// steps.CleanupScenario.
	w := &world.World{
		SCM:            &panickingSCM{},
		IssueNumber:    1,
		LeasedRepoName: "test-repo-01",
		RepoDriver:     drv,
	}
	ctx := world.WithWorld(context.Background(), w)

	// afterScenario should panic (from CleanupScenario), but the
	// deferred DeallocateRepo must still run during stack unwinding.
	assert.Panics(t, func() {
		afterScenario(ctx, nil) //nolint:errcheck // panic prevents return
	})

	// The deferred DeallocateRepo ran.
	assert.Equal(t, []string{"test-repo-01"}, drv.deallocated,
		"deferred DeallocateRepo should have run despite cleanup panic")
}
