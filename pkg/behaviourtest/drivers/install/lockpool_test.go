package install

import (
	"context"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRunID returns a valid fixed-width run id for tests.
func newTestRunID(t *testing.T) RunID {
	t.Helper()
	id, err := NewRunID()
	require.NoError(t, err)
	return id
}

// fakeLockPool builds a lock pool backed by a single shared FakeClient for
// every org, with a disabled logger.
func fakeLockPool(t *testing.T, cfg LockPoolConfig, runID RunID) (*LockPool, *forge.FakeClient) {
	t.Helper()
	fc := forge.NewFakeClient()
	clientFor := func(context.Context, string) (forge.Client, error) { return fc, nil }
	p, err := NewLockPool(cfg, runID, clientFor, nil)
	require.NoError(t, err)
	return p, fc
}

func singleOrgCfg(org string, n int) LockPoolConfig {
	return LockPoolConfig{Orgs: []OrgSlots{{Org: org, Slots: n}}}
}

// slowHeartbeat sets the heartbeat interval long enough that it never fires
// during a fast test, isolating behaviour under test from heartbeat writes.
func slowHeartbeat(t *testing.T) {
	t.Helper()
	prev := lockHeartbeatInterval
	lockHeartbeatInterval = time.Hour
	t.Cleanup(func() { lockHeartbeatInterval = prev })
}

func TestLockPool_AcquireAndRelease(t *testing.T) {
	slowHeartbeat(t)
	p, fc := fakeLockPool(t, singleOrgCfg("org", 2), newTestRunID(t))

	held, err := p.Acquire(context.Background())
	require.NoError(t, err)
	require.NotNil(t, held)
	assert.Equal(t, "org", held.Org())

	// The claimed lock carries this run's owner token.
	repo, err := fc.GetRepo(context.Background(), held.slot.Org, held.slot.Name)
	require.NoError(t, err)
	token, ok := parseOwnerToken(repo.Description)
	require.True(t, ok)
	assert.Equal(t, p.runID, token)

	require.NoError(t, held.Release(context.Background()))
	_, err = fc.GetRepo(context.Background(), held.slot.Org, held.slot.Name)
	assert.True(t, forge.IsNotFound(err), "released lock repo should be deleted")

	// Release is idempotent.
	require.NoError(t, held.Release(context.Background()))
}

func TestLockPool_SkipsHeldLiveSlot(t *testing.T) {
	slowHeartbeat(t)
	runID := newTestRunID(t)
	p, fc := fakeLockPool(t, singleOrgCfg("org", 2), runID)

	// lock-1 is held by a live foreign run (fresh timestamps).
	foreign := newTestRunID(t)
	now := time.Now()
	fc.Repos = []forge.Repository{{
		ID: 1, Name: "lock-1", FullName: "org/lock-1",
		Description: lockRepoDescriptionPrefix + foreign.String(),
		CreatedAt:   now, UpdatedAt: now,
	}}

	held, err := p.Acquire(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lock-2", held.slot.Name, "must take the free slot, not the live one")
	require.NoError(t, held.Release(context.Background()))
}

func TestLockPool_ReclaimsStaleSlot(t *testing.T) {
	slowHeartbeat(t)
	runID := newTestRunID(t)
	p, fc := fakeLockPool(t, singleOrgCfg("org", 1), runID)

	// The only slot is held by a dead run: old creation and no heartbeat
	// within the progress window.
	foreign := newTestRunID(t)
	old := time.Now().Add(-30 * time.Minute)
	fc.Repos = []forge.Repository{{
		ID: 1, Name: "lock-1", FullName: "org/lock-1",
		Description: lockRepoDescriptionPrefix + foreign.String(),
		CreatedAt:   old, UpdatedAt: old,
	}}

	held, err := p.Acquire(context.Background())
	require.NoError(t, err)
	require.NotNil(t, held)

	repo, err := fc.GetRepo(context.Background(), "org", "lock-1")
	require.NoError(t, err)
	token, ok := parseOwnerToken(repo.Description)
	require.True(t, ok)
	assert.Equal(t, runID, token, "reclaimed lock must now carry our token")
	require.NoError(t, held.Release(context.Background()))
}

func TestLockPool_ReclaimsMalformedToken(t *testing.T) {
	slowHeartbeat(t)
	p, fc := fakeLockPool(t, singleOrgCfg("org", 1), newTestRunID(t))

	old := time.Now().Add(-30 * time.Minute)
	fc.Repos = []forge.Repository{{
		ID: 1, Name: "lock-1", FullName: "org/lock-1",
		Description: "not a valid owner token",
		CreatedAt:   old, UpdatedAt: time.Now(), // recent, but token is malformed
	}}

	held, err := p.Acquire(context.Background())
	require.NoError(t, err, "a lock with a missing/malformed token is reclaimable regardless of recency")
	require.NoError(t, held.Release(context.Background()))
}

func TestLockPool_LeavesYoungForeignSlotAlone(t *testing.T) {
	slowHeartbeat(t)
	prevPoll, prevTimeout := lockAcquirePoll, lockAcquireTimeout
	lockAcquirePoll, lockAcquireTimeout = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { lockAcquirePoll, lockAcquireTimeout = prevPoll, prevTimeout })

	p, fc := fakeLockPool(t, singleOrgCfg("org", 1), newTestRunID(t))

	// A just-created foreign lock (within the age gate) whose heartbeat has
	// not landed yet must not be reclaimed.
	foreign := newTestRunID(t)
	now := time.Now()
	fc.Repos = []forge.Repository{{
		ID: 1, Name: "lock-1", FullName: "org/lock-1",
		Description: lockRepoDescriptionPrefix + foreign.String(),
		CreatedAt:   now, UpdatedAt: time.Time{}, // no heartbeat yet
	}}

	_, err := p.Acquire(context.Background())
	assert.ErrorIs(t, err, ErrLockPoolExhausted, "young foreign lock must be left alone, so acquire times out")
}

func TestLockPool_ReleaseDoesNotDeleteReclaimedSlot(t *testing.T) {
	slowHeartbeat(t)
	runID := newTestRunID(t)
	p, fc := fakeLockPool(t, singleOrgCfg("org", 1), runID)

	held, err := p.Acquire(context.Background())
	require.NoError(t, err)

	// Simulate another run reclaiming the slot (ownership token changes).
	other := newTestRunID(t)
	require.NoError(t, fc.UpdateRepoDescription(context.Background(), "org", "lock-1",
		lockRepoDescriptionPrefix+other.String()))

	require.NoError(t, held.Release(context.Background()))
	repo, err := fc.GetRepo(context.Background(), "org", "lock-1")
	require.NoError(t, err, "release must not delete a slot a second run now holds")
	assert.Contains(t, repo.Description, other.String())
}

func TestLockPool_Heartbeat(t *testing.T) {
	prev := lockHeartbeatInterval
	lockHeartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() { lockHeartbeatInterval = prev })

	p, fc := fakeLockPool(t, singleOrgCfg("org", 1), newTestRunID(t))
	held, err := p.Acquire(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Release(context.Background()) })

	before, err := fc.GetRepo(context.Background(), "org", "lock-1")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		after, err := fc.GetRepo(context.Background(), "org", "lock-1")
		return err == nil && after.UpdatedAt.After(before.UpdatedAt)
	}, time.Second, 5*time.Millisecond, "heartbeat should bump UpdatedAt")
}

func TestParseLockPoolSpec(t *testing.T) {
	cfg, err := parseLockPoolSpec("org-a:4, org-b:2 ,org-c:1")
	require.NoError(t, err)
	assert.Equal(t, []OrgSlots{{"org-a", 4}, {"org-b", 2}, {"org-c", 1}}, cfg.Orgs)

	_, err = parseLockPoolSpec("org-a")
	assert.Error(t, err, "missing :N is rejected")
	_, err = parseLockPoolSpec("org-a:zero")
	assert.Error(t, err, "non-numeric N is rejected")
	_, err = parseLockPoolSpec("org-a:0")
	assert.Error(t, err, "non-positive N is rejected")
}

func TestLockPoolConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	// Default: single org, default N.
	cfg, err := LockPoolConfigFromEnv("fallback-org", getenv, nil)
	require.NoError(t, err)
	assert.Equal(t, []OrgSlots{{"fallback-org", DefaultLockSlotsPerOrg}}, cfg.Orgs)

	// Per-org override.
	env["BEHAVIOUR_LOCK_SLOTS_PER_ORG"] = "7"
	cfg, err = LockPoolConfigFromEnv("fallback-org", getenv, nil)
	require.NoError(t, err)
	assert.Equal(t, []OrgSlots{{"fallback-org", 7}}, cfg.Orgs)

	// Explicit pool spec wins.
	env["BEHAVIOUR_LOCK_POOL"] = "a:2,b:3"
	cfg, err = LockPoolConfigFromEnv("fallback-org", getenv, nil)
	require.NoError(t, err)
	assert.Equal(t, []OrgSlots{{"a", 2}, {"b", 3}}, cfg.Orgs)
}
