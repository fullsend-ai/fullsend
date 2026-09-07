package install

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// fakeMintDriver is a test double for mintDriver.
type fakeMintDriver struct {
	teardownCalled bool
	teardownErr    error
}

func (f *fakeMintDriver) Install(_ context.Context, _ string) (string, error) {
	return "https://mint.test", nil
}

func (f *fakeMintDriver) Teardown(_ context.Context) error {
	f.teardownCalled = true
	return f.teardownErr
}

// fakeComposedClient satisfies forge.Client and forge.AllRepoLister
// for composed driver tests.
type fakeComposedClient struct {
	forge.Client
	deleteRepoErr error
	deletedRepos  []string
	allRepos      []forge.Repository
}

func (f *fakeComposedClient) DeleteRepo(_ context.Context, _, repo string) error {
	if f.deleteRepoErr != nil {
		return f.deleteRepoErr
	}
	f.deletedRepos = append(f.deletedRepos, repo)
	return nil
}

func (f *fakeComposedClient) ListAllOrgRepos(_ context.Context, _ string) ([]forge.Repository, error) {
	return f.allRepos, nil
}

func TestNewComposedDriver_ReturnsDriver(t *testing.T) {
	e := &fakeEnsurer{}
	mint := &fakeMintDriver{}
	client := &fakeComposedClient{}

	d := newComposedDriver("org", mint, e, client, t.Logf)
	require.NotNil(t, d)
	assert.Equal(t, DefaultConcurrency, d.DefaultConcurrency())
}

func TestComposedDriver_CreateRepo(t *testing.T) {
	e := &fakeEnsurer{}
	mint := &fakeMintDriver{}
	client := &fakeComposedClient{}

	d := newComposedDriver("org", mint, e, client, t.Logf)

	name, err := d.CreateRepo(context.Background(), "triage")
	require.NoError(t, err)
	assert.Equal(t, "bt-fake-triage", name)
}

func TestComposedDriver_CreateRepo_TracksName(t *testing.T) {
	e := &fakeEnsurer{}
	client := &fakeComposedClient{}

	d := newComposedDriver("org", &fakeMintDriver{}, e, client, t.Logf)

	name, err := d.CreateRepo(context.Background(), "test")
	require.NoError(t, err)

	cd := d.(*composedDriver)
	assert.True(t, cd.created[name], "created repo should be tracked")
}

func TestComposedDriver_CreateRepo_Error(t *testing.T) {
	e := &failingEnsurer{err: fmt.Errorf("ensure failed")}
	client := &fakeComposedClient{}

	d := newComposedDriver("org", &fakeMintDriver{}, e, client, t.Logf)

	_, err := d.CreateRepo(context.Background(), "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure failed")
}

func TestComposedDriver_MarkDeleted(t *testing.T) {
	e := &fakeEnsurer{}
	client := &fakeComposedClient{}

	d := newComposedDriver("org", &fakeMintDriver{}, e, client, t.Logf)

	name, err := d.CreateRepo(context.Background(), "test")
	require.NoError(t, err)

	d.MarkDeleted(name)

	cd := d.(*composedDriver)
	assert.False(t, cd.created[name], "marked repo should be removed from tracking")
}

func TestComposedDriver_Finalize_PrunesOldRuns(t *testing.T) {
	if KeepRepos() {
		t.Skip("KeepRepos() is true")
	}

	// Simulate 14 runs × 16 repos = 224 repos, over the 200 threshold.
	// Oldest run should be pruned (224 - 16 = 208 ≥ 200, 208 - 16 = 192 < 200).
	var repos []forge.Repository
	for i := 1; i <= 14; i++ {
		ts := fmt.Sprintf("20260905T%02d0000", i)
		for j := 0; j < 16; j++ {
			repos = append(repos, forge.Repository{
				Name: fmt.Sprintf("bt-%s-%08x", ts, j),
			})
		}
	}

	client := &fakeComposedClient{allRepos: repos}
	mint := &fakeMintDriver{}

	d := newComposedDriver("org", mint, &fakeEnsurer{}, client, t.Logf)

	err := d.Finalize(context.Background())
	require.NoError(t, err)

	// Only run 1 (ts=01) should be deleted. Run 2 can't be deleted (208-16=192 < 200).
	assert.Len(t, client.deletedRepos, 16, "should delete exactly one run (16 repos)")
	for _, name := range client.deletedRepos {
		assert.Contains(t, name, "20260905T010000", "deleted repos should be from the oldest run")
	}
	assert.True(t, mint.teardownCalled)
}

func TestComposedDriver_Finalize_NoPruneUnderThreshold(t *testing.T) {
	if KeepRepos() {
		t.Skip("KeepRepos() is true")
	}

	// 10 runs × 16 repos = 160 repos, under threshold.
	var repos []forge.Repository
	for i := 1; i <= 10; i++ {
		ts := fmt.Sprintf("20260905T%02d0000", i)
		for j := 0; j < 16; j++ {
			repos = append(repos, forge.Repository{
				Name: fmt.Sprintf("bt-%s-%08x", ts, j),
			})
		}
	}

	client := &fakeComposedClient{allRepos: repos}

	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)

	err := d.Finalize(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.deletedRepos, "should not delete any repos when under threshold")
}

func TestComposedDriver_Finalize_KeepRepos(t *testing.T) {
	t.Setenv("E2E_KEEP_REPOS", "true")
	client := &fakeComposedClient{}
	e := &fakeEnsurer{}

	d := newComposedDriver("org", &fakeMintDriver{}, e, client, t.Logf)

	_, err := d.CreateRepo(context.Background(), "kept")
	require.NoError(t, err)

	err = d.Finalize(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.deletedRepos, "Finalize should not delete when E2E_KEEP_REPOS is set")
}

func TestComposedDriver_Finalize_TeardownError(t *testing.T) {
	mint := &fakeMintDriver{teardownErr: fmt.Errorf("teardown boom")}
	d := newComposedDriver("org", mint, &fakeEnsurer{}, &fakeComposedClient{}, t.Logf)

	err := d.Finalize(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "teardown boom")
}

func TestComposedDriver_Finalize_NilMint(t *testing.T) {
	d := newComposedDriver("org", nil, &fakeEnsurer{}, &fakeComposedClient{}, t.Logf)

	err := d.Finalize(context.Background())
	require.NoError(t, err)
}

func TestComposedDriver_Finalize_IgnoresNonBTRepos(t *testing.T) {
	if KeepRepos() {
		t.Skip("KeepRepos() is true")
	}

	repos := []forge.Repository{
		{Name: "some-other-repo"},
		{Name: "another-repo"},
	}
	// Add enough bt-* repos to exceed threshold, but non-bt repos shouldn't count.
	for i := 0; i < 16; i++ {
		repos = append(repos, forge.Repository{
			Name: fmt.Sprintf("bt-20260905T010000-%08x", i),
		})
	}

	client := &fakeComposedClient{allRepos: repos}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)

	err := d.Finalize(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.deletedRepos, "16 bt-* repos is under threshold, should not prune")
}

func TestComposedDriver_Finalize_PruneDeleteError(t *testing.T) {
	if KeepRepos() {
		t.Skip("KeepRepos() is true")
	}

	var repos []forge.Repository
	for i := 1; i <= 14; i++ {
		ts := fmt.Sprintf("20260905T%02d0000", i)
		for j := 0; j < 16; j++ {
			repos = append(repos, forge.Repository{
				Name: fmt.Sprintf("bt-%s-%08x", ts, j),
			})
		}
	}

	client := &fakeComposedClient{
		allRepos:      repos,
		deleteRepoErr: fmt.Errorf("permission denied"),
	}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)

	err := d.Finalize(context.Background())
	require.NoError(t, err, "delete errors are logged, not fatal")
}

func TestComposedDriver_Finalize_SkipsShortRepoNames(t *testing.T) {
	if KeepRepos() {
		t.Skip("KeepRepos() is true")
	}

	repos := []forge.Repository{
		{Name: "bt-short"},
		{Name: "bt-20260905T010000-aabbccdd"},
	}
	client := &fakeComposedClient{allRepos: repos}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)

	err := d.Finalize(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.deletedRepos, "under threshold, nothing pruned")
}

func TestComposedDriver_Finalize_ListError(t *testing.T) {
	if KeepRepos() {
		t.Skip("KeepRepos() is true")
	}

	client := &fakeComposedClientWithListError{
		fakeComposedClient: fakeComposedClient{},
		listErr:            fmt.Errorf("API error"),
	}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)

	err := d.Finalize(context.Background())
	require.NoError(t, err, "list errors are logged, not fatal")
}

type fakeComposedClientWithListError struct {
	fakeComposedClient
	listErr error
}

func (f *fakeComposedClientWithListError) ListAllOrgRepos(_ context.Context, _ string) ([]forge.Repository, error) {
	return nil, f.listErr
}

// fakeRateLimitClient implements forge.Client, AllRepoLister,
// RateLimitReporter, and RateLimitQuerier for testing rate limit logging.
type fakeRateLimitClient struct {
	fakeComposedClient
	rl       forge.RateLimit
	seen     bool
	queryErr error
}

func (f *fakeRateLimitClient) RateLimit() (forge.RateLimit, bool) {
	return f.rl, f.seen
}

func (f *fakeRateLimitClient) GetRateLimit(_ context.Context) (forge.RateLimit, error) {
	if f.queryErr != nil {
		return forge.RateLimit{}, f.queryErr
	}
	return f.rl, nil
}

func TestComposedDriver_LogRateLimit(t *testing.T) {
	client := &fakeRateLimitClient{
		rl:   forge.RateLimit{Limit: 5000, Remaining: 4500, Resource: "core"},
		seen: true,
	}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)
	cd := d.(*composedDriver)
	cd.logRateLimit("test")
}

func TestComposedDriver_LogRateLimit_NotSeen(t *testing.T) {
	client := &fakeRateLimitClient{seen: false}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)
	cd := d.(*composedDriver)
	cd.logRateLimit("test")
}

func TestComposedDriver_QueryRateLimit(t *testing.T) {
	client := &fakeRateLimitClient{
		rl: forge.RateLimit{Limit: 5000, Remaining: 4500, Resource: "core"},
	}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)
	cd := d.(*composedDriver)
	cd.queryRateLimit("test")
}

func TestComposedDriver_QueryRateLimit_Error(t *testing.T) {
	client := &fakeRateLimitClient{queryErr: fmt.Errorf("network error")}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)
	cd := d.(*composedDriver)
	cd.queryRateLimit("test")
}

func TestComposedDriver_QueryRateLimit_NotSupported(t *testing.T) {
	client := &fakeComposedClient{}
	d := newComposedDriver("org", &fakeMintDriver{}, &fakeEnsurer{}, client, t.Logf)
	cd := d.(*composedDriver)
	cd.queryRateLimit("test")
	cd.logRateLimit("test")
}

func TestComposedDriver_CreateRepo_TracksOnPartialFailure(t *testing.T) {
	client := &fakeComposedClient{}
	e := &failingEnsurer{err: fmt.Errorf("install failed"), name: "bt-partial-repo"}
	d := newComposedDriver("org", &fakeMintDriver{}, e, client, t.Logf)

	_, err := d.CreateRepo(context.Background(), "test")
	require.Error(t, err)

	cd := d.(*composedDriver)
	assert.True(t, cd.created["bt-partial-repo"], "should track repo name even on partial failure")
}

// failingEnsurer always returns an error.
type failingEnsurer struct {
	err  error
	name string
}

func (f *failingEnsurer) CreateRepo(_ context.Context, _, _ string) (string, error) {
	return f.name, f.err
}
