package install

import (
	"context"
	"errors"
	"fmt"
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

	assert.True(t, strings.HasPrefix(a, "bt-"+runID.String()+"-allow-"),
		"name must carry the shared run-id prefix and allow marker: %s", a)
	assert.Len(t, a, ephemeralRepoPrefixLen+len(ephemeralAllowInfix)+1+8, "name = bt-{runID}-allow-{8hex}")
	assert.NotEqual(t, a, b, "per-repo suffixes must differ within a run")
	// The run-id slice used by prune must recover the run id exactly.
	assert.Equal(t, runID.String(), a[3:3+RunIDLen])
}

func TestUnapprovedProbeHost(t *testing.T) {
	runID := newTestRunID(t)

	// Ephemeral: swap the allow marker for deny; keeps the run-id (so prune
	// and cleanup still find it) but falls outside bt-{run-id}-allow-*.
	leased := ephemeralRepoName(runID)
	deny := UnapprovedProbeHost(leased)
	assert.True(t, strings.HasPrefix(deny, "bt-"+runID.String()+"-deny-"),
		"ephemeral unapproved host carries the run-id with the deny marker: %s", deny)
	assert.NotContains(t, deny, "-allow-", "the allow marker must be gone")
	assert.Equal(t, runID.String(), deny[3:3+RunIDLen], "run-id is preserved for prune/cleanup")

	// Slot pool (no marker): append -mint-unapproved, on no list.
	assert.Equal(t, "test-repo-07-mint-unapproved", UnapprovedProbeHost("test-repo-07"))
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

// rateReportingClient is a forge.Client stub that implements the optional
// rate-limit reporter/fetcher interfaces, so the API-consumption logging can
// be exercised with populated readings. Only the reporter methods are called;
// the embedded nil interface covers the rest at compile time.
type rateReportingClient struct {
	forge.Client
	fetchRL  forge.RateLimit
	fetchErr error
	quota    []forge.APIQuotaCounter
	rate     forge.RateLimit
	rateSeen bool
	calls    map[string]int64
}

func (c *rateReportingClient) FetchRateLimit(context.Context) (forge.RateLimit, error) {
	return c.fetchRL, c.fetchErr
}
func (c *rateReportingClient) APIQuotaCounters() []forge.APIQuotaCounter { return c.quota }
func (c *rateReportingClient) RateLimit() (forge.RateLimit, bool)        { return c.rate, c.rateSeen }
func (c *rateReportingClient) APICalls() map[string]int64                { return c.calls }

func TestLogStartRateLimit(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	t.Run("fetcher success logs the baseline", func(t *testing.T) {
		lines = nil
		c := &rateReportingClient{fetchRL: forge.RateLimit{Limit: 9000, Remaining: 8765, Resource: "core"}}
		logStartRateLimit(context.Background(), c, "org", logf)
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "rate-limit at start (org org)")
		assert.Contains(t, lines[0], "remaining=8765/9000")
	})

	t.Run("fetcher error is reported, not fatal", func(t *testing.T) {
		lines = nil
		c := &rateReportingClient{fetchErr: errors.New("boom")}
		logStartRateLimit(context.Background(), c, "org", logf)
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "baseline read failed: boom")
	})

	t.Run("non-fetcher client is a no-op", func(t *testing.T) {
		lines = nil
		logStartRateLimit(context.Background(), forge.NewFakeClient(), "org", logf)
		assert.Empty(t, lines)
	})
}

func TestLogAPIConsumption(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	c := &rateReportingClient{
		quota:    []forge.APIQuotaCounter{{Limit: 9000, Highest: 8900, Lowest: 7000, Calls: map[string]int64{"ensure/GET": 50}}},
		rate:     forge.RateLimit{Limit: 9000, Remaining: 7000, Resource: "core"},
		rateSeen: true,
		calls:    map[string]int64{"ensure": 50},
	}
	logAPIConsumption(c, "org", "at finalize", logf)

	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "rate-limit quota at finalize (org org)")
	assert.Contains(t, joined, "rate-limit at finalize (org org)")
	assert.Contains(t, joined, "suite-side API calls at finalize (org org)")

	// A plain fake (no reporter interfaces) emits nothing.
	lines = nil
	logAPIConsumption(forge.NewFakeClient(), "org", "at finalize", logf)
	assert.Empty(t, lines)
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

func TestLockedRunIDs(t *testing.T) {
	r1, r2, r3 := newTestRunID(t), newTestRunID(t), newTestRunID(t)
	repos := []forge.Repository{
		{Name: "lock-1", Description: lockRepoDescriptionPrefix + r1.String()},
		{Name: "lock-2", Description: lockRepoDescriptionPrefix + r2.String()},
		// A slot beyond the current run's configured range must still be seen,
		// so an older run holding a higher-numbered lock is protected.
		{Name: "lock-9", Description: lockRepoDescriptionPrefix + r3.String()},
		// Non-lock repos and malformed descriptions are ignored.
		{Name: "bt-abc-allow-1", Description: "fullsend behaviour test"},
		{Name: "lock-2-extra", Description: lockRepoDescriptionPrefix + r1.String()},
		{Name: "lock-bad", Description: lockRepoDescriptionPrefix + r1.String()},
	}
	locked := lockedRunIDs(repos)
	assert.True(t, locked[r1.String()])
	assert.True(t, locked[r2.String()])
	assert.True(t, locked[r3.String()], "a lock outside the configured slot range must still protect its run")
	assert.Len(t, locked, 3)
}

func TestEphemeralPrune_AbortsWhenListFails(t *testing.T) {
	prev := repoKeepThreshold
	repoKeepThreshold = 1
	t.Cleanup(func() { repoKeepThreshold = prev })

	d, fc := ephemeralTestDriver(t, "org", 1)
	seedRun(fc, "org", newTestRunID(t), 5)
	fc.Errors = map[string]error{"ListAllOrgRepos": assert.AnError}

	d.pruneOldRuns(context.Background())
	assert.Empty(t, fc.DeletedRepos, "a failed repo listing must abort prune so protection can't fail open")
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
	assert.True(t, fc.ActionsDisabled["org/bt-x"],
		"retiring a repo must disable Actions so no further workflow run can mint")
	vars, err := fc.ListRepoVariables(ctx, "org", "bt-x")
	require.NoError(t, err)
	assert.Empty(t, vars, "retained repo's variables must be stripped so it cannot invoke the shared WIF")
}

func TestEphemeralDeallocateLease_PropagatesRetirementFailure(t *testing.T) {
	d, fc := ephemeralTestDriver(t, "org", 1)
	d.created["bt-x"] = true
	fc.Errors = map[string]error{"DisableActions": assert.AnError}

	err := d.DeallocateLease(context.Background(), "org", "bt-x")
	require.Error(t, err, "a repo that could not be retired must not report clean deallocation")
	assert.Contains(t, err.Error(), "disabling Actions")
	assert.NotContains(t, d.created, "bt-x", "the repo is still untracked even when retirement fails")
}

func TestEphemeralStillOwnsLease(t *testing.T) {
	d, _ := ephemeralTestDriver(t, "org", 1)
	ok, err := d.StillOwnsLease(context.Background(), "org", "bt-x")
	require.NoError(t, err)
	assert.True(t, ok, "ephemeral repos are unique to the run and always owned")
}
