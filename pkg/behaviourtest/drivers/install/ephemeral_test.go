package install

import (
	"context"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEphemeralRepoName(t *testing.T) {
	runID := newTestRunID(t)
	a := ephemeralRepoName(runID)
	b := ephemeralRepoName(runID)

	assert.True(t, strings.HasPrefix(a, "bt-"+runID.String()+"-"), "name must carry the shared run-id prefix: %s", a)
	assert.Len(t, a, ephemeralRepoPrefixLen+8, "name = bt-{runID}-{8hex}")
	assert.NotEqual(t, a, b, "per-repo suffixes must differ within a run")
	// The run-id slice used by prune must recover the run id exactly.
	assert.Equal(t, runID.String(), a[3:3+RunIDLen])
}

// ephemeralTestDriver builds an ephemeralDriver over a FakeClient with a
// single-org lock pool, for prune/release unit tests.
func ephemeralTestDriver(t *testing.T, org string, slots int) (*ephemeralDriver, *forge.FakeClient) {
	t.Helper()
	fc := forge.NewFakeClient()
	runID := newTestRunID(t)
	lp, err := NewLockPool(singleOrgCfg(org, slots), runID, func(context.Context, string) (forge.Client, error) { return fc, nil }, nil)
	require.NoError(t, err)
	return &ephemeralDriver{
		org:      org,
		client:   fc,
		lockPool: lp,
		runID:    runID,
		logf:     func(string, ...any) {},
		created:  make(map[string]bool),
	}, fc
}

// seedRun adds n bt-{runID}-* repos for a run to the fake's pre-populated set.
func seedRun(fc *forge.FakeClient, org string, runID RunID, n int) {
	for i := 0; i < n; i++ {
		name := ephemeralRepoName(runID)
		fc.Repos = append(fc.Repos, forge.Repository{
			Name: name, FullName: org + "/" + name,
		})
	}
}

func TestEphemeralPrune_UnderThresholdKeepsAll(t *testing.T) {
	prev := repoKeepThreshold
	repoKeepThreshold = 100
	t.Cleanup(func() { repoKeepThreshold = prev })

	d, fc := ephemeralTestDriver(t, "org", 1)
	seedRun(fc, "org", newTestRunID(t), 5)

	d.pruneOldRuns(context.Background())
	assert.Empty(t, fc.DeletedRepos, "nothing is pruned at or under the threshold")
}

func TestEphemeralPrune_DeletesOldestFirst(t *testing.T) {
	prev := repoKeepThreshold
	repoKeepThreshold = 2
	t.Cleanup(func() { repoKeepThreshold = prev })

	d, fc := ephemeralTestDriver(t, "org", 1)
	// Three runs of 2 repos each (6 total). Run ids are time-ordered, so
	// NewRunID() yields ascending ids; create them in order.
	oldest := newTestRunID(t)
	mid := newTestRunID(t)
	newest := newTestRunID(t)
	seedRun(fc, "org", oldest, 2)
	seedRun(fc, "org", mid, 2)
	seedRun(fc, "org", newest, 2)

	d.pruneOldRuns(context.Background())

	// Must delete oldest runs until total would drop below 2: deletes
	// oldest (→4) and mid (→2); stops before newest.
	assert.Len(t, fc.DeletedRepos, 4)
	for _, del := range fc.DeletedRepos {
		assert.False(t, strings.Contains(del, newest.String()), "newest run must be retained: %s", del)
	}
}

func TestEphemeralPrune_SkipsLockedRun(t *testing.T) {
	prev := repoKeepThreshold
	repoKeepThreshold = 2
	t.Cleanup(func() { repoKeepThreshold = prev })

	d, fc := ephemeralTestDriver(t, "org", 1)
	oldest := newTestRunID(t)
	newest := newTestRunID(t)
	seedRun(fc, "org", oldest, 3)
	seedRun(fc, "org", newest, 3)

	// The oldest run still holds lock-1 — it must not be pruned even though
	// it is the oldest and the count is over threshold.
	fc.Repos = append(fc.Repos, forge.Repository{
		Name: "lock-1", FullName: "org/lock-1",
		Description: lockRepoDescriptionPrefix + oldest.String(),
	})

	d.pruneOldRuns(context.Background())

	for _, del := range fc.DeletedRepos {
		assert.False(t, strings.Contains(del, oldest.String()),
			"a run that still holds a lock must be skipped by prune: %s", del)
	}
}

func TestEphemeralLockedRunIDs(t *testing.T) {
	d, fc := ephemeralTestDriver(t, "org", 2)
	r1, r2 := newTestRunID(t), newTestRunID(t)
	fc.Repos = []forge.Repository{
		{Name: "lock-1", FullName: "org/lock-1", Description: lockRepoDescriptionPrefix + r1.String()},
		{Name: "lock-2", FullName: "org/lock-2", Description: lockRepoDescriptionPrefix + r2.String()},
	}
	locked := d.lockedRunIDs(context.Background())
	assert.True(t, locked[r1.String()])
	assert.True(t, locked[r2.String()])
	assert.Len(t, locked, 2)
}

func TestEphemeralDeallocateLease_RetainsAndSanitizes(t *testing.T) {
	d, fc := ephemeralTestDriver(t, "org", 1)
	d.created["bt-x"] = true
	ctx := context.Background()
	require.NoError(t, fc.CreateOrUpdateRepoVariable(ctx, "org", "bt-x", "FULLSEND_MINT_URL", "https://mint"))
	require.NoError(t, fc.CreateOrUpdateRepoVariable(ctx, "org", "bt-x", "FULLSEND_GCP_WIF_PROVIDER", "wif"))

	require.NoError(t, d.DeallocateLease(ctx, "org", "bt-x"))

	assert.Empty(t, fc.DeletedRepos, "deallocate retains the repo for later pruning")
	assert.NotContains(t, d.created, "bt-x", "deallocate stops tracking the repo")
	vars, err := fc.ListRepoVariables(ctx, "org", "bt-x")
	require.NoError(t, err)
	assert.Empty(t, vars, "retained repo's variables must be stripped so it cannot invoke the shared WIF")
}

func TestEphemeralStillOwnsLease(t *testing.T) {
	d, _ := ephemeralTestDriver(t, "org", 1)
	ok, err := d.StillOwnsLease(context.Background(), "org", "bt-x")
	require.NoError(t, err)
	assert.True(t, ok, "ephemeral repos are unique to the run and always owned")
}
