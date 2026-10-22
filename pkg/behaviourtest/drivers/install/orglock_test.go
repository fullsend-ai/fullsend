package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

// These tests drive the shared e2e-lock bridge through the slot allocator
// (Claim and Release) against one shared fake forge. A "legacy runner"
// takes and releases the lock exactly as e2etest's AcquireOrg and
// ReleaseLock do: CreateRepo of e2e-lock, then README.md set to its run ID.

const bridgeOrg = "halfsend"

// newBridgeAllocator returns an allocator with the bridge enabled, slots
// tried in index order, and the given clock (real time when nil).
func newBridgeAllocator(t *testing.T, layout PoolLayout, clientFor OrgClientFunc, clock *testClock) *slotAllocator {
	t.Helper()
	a, err := newSlotAllocator(layout, clientFor, t.Logf)
	require.NoError(t, err)
	require.NotNil(t, a.orgLock, "the bridge is on by default")
	a.shuffle = func([]Slot) {}
	if clock != nil {
		a.now = clock.Now
	}
	return a
}

// legacyAcquire takes org's lock the way e2etest.AcquireOrg does. It
// reports false when the lock repo already exists.
func legacyAcquire(t *testing.T, client forge.Client, org string) (string, bool) {
	t.Helper()
	ctx := context.Background()
	runID := uuid.NewString()
	_, err := client.CreateRepo(ctx, org, e2etest.LockRepo, "E2E test lock — do not delete manually", false)
	if forge.IsAlreadyExists(err) {
		return "", false
	}
	require.NoError(t, err)
	require.NoError(t, client.CreateOrUpdateFile(ctx, org, e2etest.LockRepo, "README.md", "acquire lock", []byte(runID)))
	return runID, true
}

func lockExists(t *testing.T, fc *forge.FakeClient, org string) bool {
	t.Helper()
	_, err := fc.GetRepo(context.Background(), org, e2etest.LockRepo)
	if forge.IsNotFound(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func lockReadme(t *testing.T, fc *forge.FakeClient, org string) string {
	t.Helper()
	data, err := fc.GetFileContent(context.Background(), org, e2etest.LockRepo, "README.md")
	require.NoError(t, err)
	return string(data)
}

func readLockState(t *testing.T, fc *forge.FakeClient, org string) sharedLockState {
	t.Helper()
	b := newSharedOrgLock(t.Logf, time.Now)
	state, _, err := b.readState(context.Background(), fc, org)
	require.NoError(t, err)
	return state
}

func lockCreatedAt(t *testing.T, fc *forge.FakeClient, org string) time.Time {
	t.Helper()
	repo, err := fc.GetRepo(context.Background(), org, e2etest.LockRepo)
	require.NoError(t, err)
	require.False(t, repo.CreatedAt.IsZero())
	return repo.CreatedAt
}

func TestSharedOrgLock_LegacyLockExcludesNewRunners(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	runID, ok := legacyAcquire(t, p.fc, bridgeOrg)
	require.True(t, ok)

	a := newBridgeAllocator(t, layout, p.clientFor, nil)
	_, _, err := a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.LockExcluded[bridgeOrg], "exclusive")
	assert.Contains(t, err.Error(), "e2e-lock: halfsend (held by an exclusive")
	assert.False(t, p.exists(t, Slot{Org: bridgeOrg, Repo: "test-repo-01"}), "no slot repo under a legacy lock")
	assert.Equal(t, runID, lockReadme(t, p.fc, bridgeOrg), "the legacy lock is untouched")

	e2etest.ReleaseLock(ctx, p.fc, bridgeOrg, runID, t)
	require.False(t, lockExists(t, p.fc, bridgeOrg))

	slot, _, err := a.Claim(ctx)
	require.NoError(t, err)
	assert.Equal(t, Slot{Org: bridgeOrg, Repo: "test-repo-01"}, slot)
	state := readLockState(t, p.fc, bridgeOrg)
	assert.Len(t, state.Holders, 1)
	assert.Equal(t, state.Generation, lockReadme(t, p.fc, bridgeOrg))
}

func TestSharedOrgLock_AdmitsNewHoldersAndExcludesLegacy(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(4)
	p := newFakePool(layout)
	a := newBridgeAllocator(t, layout, p.clientFor, nil)
	b := newBridgeAllocator(t, layout, p.clientFor, nil)

	s1, _, err := a.Claim(ctx)
	require.NoError(t, err)
	s2, _, err := b.Claim(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2)

	state := readLockState(t, p.fc, bridgeOrg)
	assert.Len(t, state.Holders, 2)
	assert.False(t, state.Closed)
	readme := lockReadme(t, p.fc, bridgeOrg)
	_, parseErr := uuid.Parse(readme)
	require.NoError(t, parseErr, "README.md must stay a UUID so legacy acquireLock does not delete it as invalid")
	assert.Equal(t, state.Generation, readme)
	marker, err := p.fc.GetFileContent(ctx, bridgeOrg, e2etest.LockRepo, sharedLockMarkerPath)
	require.NoError(t, err)
	assert.Equal(t, sharedLockMarker, strings.TrimSpace(string(marker)))

	// A legacy runner neither acquires nor releases the shared lock.
	_, ok := legacyAcquire(t, p.fc, bridgeOrg)
	assert.False(t, ok, "legacy runners see the shared lock as held")
	e2etest.ReleaseLock(ctx, p.fc, bridgeOrg, uuid.NewString(), t)
	require.True(t, lockExists(t, p.fc, bridgeOrg))

	require.NoError(t, a.Release(ctx, s1))
	require.True(t, lockExists(t, p.fc, bridgeOrg), "a holder remains")
	assert.Len(t, readLockState(t, p.fc, bridgeOrg).Holders, 1)

	require.NoError(t, b.Release(ctx, s2))
	assert.False(t, lockExists(t, p.fc, bridgeOrg), "the last holder deletes the lock")
	assert.False(t, p.exists(t, s1))
	assert.False(t, p.exists(t, s2))

	_, ok = legacyAcquire(t, p.fc, bridgeOrg)
	assert.True(t, ok, "legacy runners can take the org once the shared lock is gone")
}

func TestSharedOrgLock_ConcurrentHoldersAreNeverLost(t *testing.T) {
	old := slotClaimPoll
	slotClaimPoll = time.Millisecond
	t.Cleanup(func() { slotClaimPoll = old })

	ctx := context.Background()
	const runners = 6
	layout := StagePoolLayout(runners)
	p := newFakePool(layout)
	allocs := make([]*slotAllocator, runners)
	for i := range allocs {
		allocs[i] = newBridgeAllocator(t, layout, p.clientFor, nil)
	}

	slots := make([]Slot, runners)
	var wg sync.WaitGroup
	for i, a := range allocs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _, err := a.Acquire(ctx)
			assert.NoError(t, err)
			slots[i] = s
		}()
	}
	wg.Wait()
	require.Len(t, readLockState(t, p.fc, bridgeOrg).Holders, runners, "every admitted holder is recorded")

	for i, a := range allocs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, a.Release(ctx, slots[i]))
		}()
	}
	wg.Wait()
	assert.False(t, lockExists(t, p.fc, bridgeOrg), "the lock is deleted once every holder left")
}

// hookClient runs a hook once before the first UpdateFileIfMatch whose
// content matches.
type hookClient struct {
	*orgClient
	mu    sync.Mutex
	match func(content []byte) bool
	hook  func()
}

func (c *hookClient) UpdateFileIfMatch(ctx context.Context, owner, repo, path, message string, content []byte, expectedSHA string) error {
	c.mu.Lock()
	hook := c.hook
	if hook != nil && c.match(content) {
		c.hook = nil
	} else {
		hook = nil
	}
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return c.orgClient.UpdateFileIfMatch(ctx, owner, repo, path, message, content, expectedSHA)
}

func closingWrite(content []byte) bool { return strings.Contains(string(content), `"closed":true`) }

func TestSharedOrgLock_JoinDuringFinalReleaseKeepsLiveHolder(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	hooked := &hookClient{orgClient: p.clients[bridgeOrg]}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return hooked, nil }, nil)
	b := newBridgeAllocator(t, layout, p.clientFor, nil)

	s1, _, err := a.Claim(ctx)
	require.NoError(t, err)
	generation := readLockState(t, p.fc, bridgeOrg).Generation

	// b joins after a read the state but before a's closing write lands.
	var s2 Slot
	hooked.match = closingWrite
	hooked.hook = func() {
		var joinErr error
		s2, _, joinErr = b.Claim(ctx)
		require.NoError(t, joinErr)
	}
	require.NoError(t, a.Release(ctx, s1))

	require.True(t, lockExists(t, p.fc, bridgeOrg), "the final release must not delete a lock with a live holder")
	state := readLockState(t, p.fc, bridgeOrg)
	assert.Equal(t, generation, state.Generation)
	assert.Len(t, state.Holders, 1)
	assert.False(t, state.Closed)

	require.NoError(t, b.Release(ctx, s2))
	assert.False(t, lockExists(t, p.fc, bridgeOrg))
}

func TestSharedOrgLock_JoinAfterClosingWriteDoesNotJoinDeletedLock(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	a := newBridgeAllocator(t, layout, p.clientFor, nil)
	hooked := &hookClient{orgClient: p.clients[bridgeOrg]}
	b := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return hooked, nil }, nil)

	s1, _, err := a.Claim(ctx)
	require.NoError(t, err)
	first := readLockState(t, p.fc, bridgeOrg).Generation

	// a's final release closes and deletes the lock after b read the
	// state but before b's join write.
	hooked.match = func([]byte) bool { return true }
	hooked.hook = func() { require.NoError(t, a.Release(ctx, s1)) }
	s2, _, err := b.Claim(ctx)
	require.NoError(t, err)

	state := readLockState(t, p.fc, bridgeOrg)
	assert.NotEqual(t, first, state.Generation, "b must hold a new lock, not the deleted one")
	assert.Len(t, state.Holders, 1)

	require.NoError(t, b.Release(ctx, s2))
	assert.False(t, lockExists(t, p.fc, bridgeOrg))
}

func TestSharedOrgLock_LateReleaseKeepsReplacementLock(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)

	t.Run("legacy lock", func(t *testing.T) {
		p := newFakePool(layout)
		a := newBridgeAllocator(t, layout, p.clientFor, nil)
		s1, _, err := a.Claim(ctx)
		require.NoError(t, err)

		// A legacy runner reclaims the lock (as after 30 minutes).
		require.NoError(t, p.fc.DeleteRepo(ctx, bridgeOrg, e2etest.LockRepo))
		runID, ok := legacyAcquire(t, p.fc, bridgeOrg)
		require.True(t, ok)

		require.NoError(t, a.Release(ctx, s1))
		require.True(t, lockExists(t, p.fc, bridgeOrg), "a late release must not delete the legacy lock")
		assert.Equal(t, runID, lockReadme(t, p.fc, bridgeOrg))
		assert.False(t, p.exists(t, s1))
	})

	t.Run("stale shared lock reclaimed by a new runner", func(t *testing.T) {
		p := newFakePool(layout)
		start := time.Now()
		clockA := newTestClock(start)
		clockB := newTestClock(start)
		a := newBridgeAllocator(t, layout, p.clientFor, clockA)
		b := newBridgeAllocator(t, layout, p.clientFor, clockB)
		logs := &logRecorder{t: t}
		a.orgLock.logf = logs.logf

		s1, _, err := a.Claim(ctx)
		require.NoError(t, err)
		first := readLockState(t, p.fc, bridgeOrg).Generation

		// b finds the lock past the legacy stale age, reclaims it, and
		// holds a replacement generation.
		clockB.Set(lockCreatedAt(t, p.fc, bridgeOrg).Add(e2etest.StaleLockTimeout + time.Minute))
		s2, _, err := b.Claim(ctx)
		require.NoError(t, err)
		replacement := readLockState(t, p.fc, bridgeOrg)
		require.NotEqual(t, first, replacement.Generation)

		clockA.Set(start.Add(e2etest.StaleLockTimeout + 2*time.Minute))
		require.NoError(t, a.Release(ctx, s1))
		assert.True(t, logs.contains("beyond the 30m0s legacy reclaim age"))
		require.True(t, lockExists(t, p.fc, bridgeOrg), "a late release must not delete the replacement lock")
		assert.Equal(t, replacement, readLockState(t, p.fc, bridgeOrg))

		require.NoError(t, b.Release(ctx, s2))
		assert.False(t, lockExists(t, p.fc, bridgeOrg))
	})
}

func TestSharedOrgLock_AdmissionWindow(t *testing.T) {
	ctx := context.Background()
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 3}
	p := newFakePool(layout)
	a := newBridgeAllocator(t, layout, p.clientFor, nil)
	first, _, err := a.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, "halfsend-01", first.Org)
	created := lockCreatedAt(t, p.fc, "halfsend-01")

	// Just inside the window: joins the same org and logs the lock age.
	clockB := newTestClock(created.Add(sharedLockAdmissionWindow - time.Second))
	b := newBridgeAllocator(t, layout, p.clientFor, clockB)
	logs := &logRecorder{t: t}
	b.orgLock.logf = logs.logf
	s, _, err := b.Claim(ctx)
	require.NoError(t, err)
	assert.Equal(t, "halfsend-01", s.Org)
	assert.True(t, logs.contains("lock age at admission: 14m59s"))

	// At the window: seeks another org.
	clockC := newTestClock(created.Add(sharedLockAdmissionWindow))
	c := newBridgeAllocator(t, layout, p.clientFor, clockC)
	s, _, err = c.Claim(ctx)
	require.NoError(t, err)
	assert.Equal(t, "halfsend-02", s.Org)

	// With no other org, the claim reports why the org was skipped.
	single := StagePoolLayout(2)
	ps := newFakePool(single)
	d := newBridgeAllocator(t, single, ps.clientFor, nil)
	_, _, err = d.Claim(ctx)
	require.NoError(t, err)
	e := newBridgeAllocator(t, single, ps.clientFor, newTestClock(lockCreatedAt(t, ps.fc, bridgeOrg).Add(16*time.Minute)))
	_, _, err = e.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.LockExcluded[bridgeOrg], "past the 15m0s admission window")
}

func TestSharedOrgLock_LeaveLogsLongHolders(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	start := time.Now()
	clock := newTestClock(start)
	a := newBridgeAllocator(t, layout, p.clientFor, clock)
	logs := &logRecorder{t: t}
	a.orgLock.logf = logs.logf

	s, _, err := a.Claim(ctx)
	require.NoError(t, err)
	clock.Set(start.Add(26 * time.Minute))
	require.NoError(t, a.Release(ctx, s))
	assert.True(t, logs.contains("ran 26m0s, near the 30m0s legacy reclaim age"))

	s, _, err = a.Claim(ctx)
	require.NoError(t, err)
	clock.Set(clock.Now().Add(5 * time.Minute))
	require.NoError(t, a.Release(ctx, s))
	assert.True(t, logs.contains("leaving shared lock on halfsend after 5m0s (scenario plus cleanup)"))
}

func TestSharedOrgLock_UnusedAdmissionIsReturned(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	// The listing fails, so the claim probes by create and finds the slot
	// occupied only after joining the lock.
	p.fc.Errors = map[string]error{"ListOrgRepos": errors.New("listing down")}
	p.fc.Repos = append(p.fc.Repos, forge.Repository{Name: "test-repo-01", FullName: bridgeOrg + "/test-repo-01"})
	a := newBridgeAllocator(t, layout, p.clientFor, nil)

	_, _, err := a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, 1, capErr.Occupied)
	assert.False(t, lockExists(t, p.fc, bridgeOrg), "the holder that got no lease left and deleted the lock")
	assert.True(t, p.exists(t, Slot{Org: bridgeOrg, Repo: "test-repo-01"}))
}

func TestSharedOrgLock_CancelledClaimLeavesLock(t *testing.T) {
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hooked := &cancelOnSlotCreate{orgClient: p.clients[bridgeOrg], cancel: cancel}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return hooked, nil }, nil)

	_, _, err := a.Claim(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, lockExists(t, p.fc, bridgeOrg), "a cancelled claim still returns its holder")
}

// cancelOnSlotCreate cancels the claim while creating a slot repo.
type cancelOnSlotCreate struct {
	*orgClient
	cancel context.CancelFunc
}

func (c *cancelOnSlotCreate) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	if name == e2etest.LockRepo {
		return c.orgClient.CreateRepo(ctx, org, name, description, private)
	}
	c.cancel()
	return nil, ctx.Err()
}

func TestSharedOrgLock_JoinFailures(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(1)
	boom := errors.New("boom")

	t.Run("lock lookup failure fails the org", func(t *testing.T) {
		p := newFakePool(layout)
		p.fc.Errors = map[string]error{"GetRepo": boom}
		a := newBridgeAllocator(t, layout, p.clientFor, nil)
		_, _, err := a.Claim(ctx)
		var capErr *CapacityError
		require.ErrorAs(t, err, &capErr)
		require.ErrorIs(t, capErr.Failed[bridgeOrg], boom)
	})

	t.Run("failed initialisation leaves the half-made lock for stale reclaim", func(t *testing.T) {
		p := newFakePool(layout)
		p.fc.Errors = map[string]error{"CommitFiles": boom}
		a := newBridgeAllocator(t, layout, p.clientFor, nil)
		_, _, err := a.Claim(ctx)
		var capErr *CapacityError
		require.ErrorAs(t, err, &capErr)
		require.ErrorIs(t, capErr.Failed[bridgeOrg], boom)
		assert.True(t, lockExists(t, p.fc, bridgeOrg), "the commit may still land, so the repository is not deleted")
		assert.False(t, p.exists(t, Slot{Org: bridgeOrg, Repo: "test-repo-01"}))

		// Without the shared flag the lock reads as exclusive, so another
		// runner skips the org instead of joining a half-made lock.
		delete(p.fc.Errors, "CommitFiles")
		b := newBridgeAllocator(t, layout, p.clientFor, nil)
		_, _, err = b.Claim(ctx)
		require.ErrorAs(t, err, &capErr)
		assert.Contains(t, capErr.LockExcluded[bridgeOrg], "exclusive")
	})

	t.Run("shared lock of unknown age is not joined", func(t *testing.T) {
		p := newFakePool(layout)
		seed := newBridgeAllocator(t, StagePoolLayout(2), p.clientFor, nil)
		_, _, err := seed.Claim(ctx)
		require.NoError(t, err)
		a := newBridgeAllocator(t, StagePoolLayout(2), func(context.Context, string) (forge.Client, error) {
			return noCreatedAtClient{p.clients[bridgeOrg]}, nil
		}, nil)
		_, _, err = a.Claim(ctx)
		var capErr *CapacityError
		require.ErrorAs(t, err, &capErr)
		assert.Contains(t, capErr.LockExcluded[bridgeOrg], "age unknown")
	})

	t.Run("closing lock is not joined", func(t *testing.T) {
		p := newFakePool(layout)
		seed := newBridgeAllocator(t, StagePoolLayout(2), p.clientFor, nil)
		_, _, err := seed.Claim(ctx)
		require.NoError(t, err)
		state := readLockState(t, p.fc, bridgeOrg)
		state.Closed = true
		require.NoError(t, p.fc.CreateOrUpdateFile(ctx, bridgeOrg, e2etest.LockRepo, sharedLockHoldersPath, "close", mustJSON(t, state)))
		a := newBridgeAllocator(t, StagePoolLayout(2), p.clientFor, nil)
		_, _, err = a.Claim(ctx)
		var capErr *CapacityError
		require.ErrorAs(t, err, &capErr)
		assert.Contains(t, capErr.LockExcluded[bridgeOrg], "closing")
	})

	t.Run("flag without holder state is not joined", func(t *testing.T) {
		p := newFakePool(layout)
		seed := newBridgeAllocator(t, StagePoolLayout(2), p.clientFor, nil)
		_, _, err := seed.Claim(ctx)
		require.NoError(t, err)
		require.NoError(t, p.fc.DeleteFile(ctx, bridgeOrg, e2etest.LockRepo, sharedLockHoldersPath, "drop"))
		a := newBridgeAllocator(t, StagePoolLayout(2), p.clientFor, nil)
		_, _, err = a.Claim(ctx)
		var capErr *CapacityError
		require.ErrorAs(t, err, &capErr)
		assert.Contains(t, capErr.LockExcluded[bridgeOrg], "no holder state")
	})
}

func TestSharedOrgLock_ReleaseReportsLeaveFailure(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	a := newBridgeAllocator(t, layout, p.clientFor, nil)
	s, _, err := a.Claim(ctx)
	require.NoError(t, err)

	boom := errors.New("boom")
	p.fc.GetFileContentErrors = map[string]error{bridgeOrg + "/" + e2etest.LockRepo + "/" + sharedLockHoldersPath: boom}
	err = a.Release(ctx, s)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "leaving shared e2e-lock")
	assert.False(t, p.exists(t, s), "the slot repo is still deleted")
}

// noCreatedAtClient hides repository creation times, like a forge that
// does not report them.
type noCreatedAtClient struct{ *orgClient }

func (c noCreatedAtClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	r, err := c.orgClient.GetRepo(ctx, owner, repo)
	if r != nil {
		r.CreatedAt = time.Time{}
	}
	return r, err
}

func mustJSON(t *testing.T, state sharedLockState) []byte {
	t.Helper()
	data, err := json.Marshal(state)
	require.NoError(t, err)
	return data
}

func TestSharedOrgLock_NewRunnerReclaimsStaleLegacyLock(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	runID, ok := legacyAcquire(t, p.fc, bridgeOrg)
	require.True(t, ok)

	a := newBridgeAllocator(t, layout, p.clientFor, newTestClock(lockCreatedAt(t, p.fc, bridgeOrg).Add(e2etest.StaleLockTimeout+time.Second)))
	s, _, err := a.Claim(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, runID, lockReadme(t, p.fc, bridgeOrg), "the stale legacy lock was replaced")
	assert.Len(t, readLockState(t, p.fc, bridgeOrg).Holders, 1)
	require.NoError(t, a.Release(ctx, s))
	assert.False(t, lockExists(t, p.fc, bridgeOrg))
}

func TestSharedOrgLock_CorruptStateFailsOrg(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	seed := newBridgeAllocator(t, layout, p.clientFor, nil)
	_, _, err := seed.Claim(ctx)
	require.NoError(t, err)
	require.NoError(t, p.fc.CreateOrUpdateFile(ctx, bridgeOrg, e2etest.LockRepo, sharedLockHoldersPath, "corrupt", []byte("{")))

	a := newBridgeAllocator(t, layout, p.clientFor, nil)
	_, _, err = a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	require.Error(t, capErr.Failed[bridgeOrg])
	assert.Contains(t, capErr.Failed[bridgeOrg].Error(), "decoding")
}

func TestSharedOrgLock_UnknownFlagIsExclusive(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	seed := newBridgeAllocator(t, layout, p.clientFor, nil)
	_, _, err := seed.Claim(ctx)
	require.NoError(t, err)
	require.NoError(t, p.fc.CreateOrUpdateFile(ctx, bridgeOrg, e2etest.LockRepo, sharedLockMarkerPath, "flag", []byte("something else")))

	a := newBridgeAllocator(t, layout, p.clientFor, nil)
	_, _, err = a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.LockExcluded[bridgeOrg], "exclusive")
}

func TestSharedOrgLock_DeleteIfIDChecksIdentity(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	b := newSharedOrgLock(t.Logf, time.Now)

	deleted, err := b.deleteIfID(ctx, fc, bridgeOrg, 1)
	require.NoError(t, err)
	assert.False(t, deleted, "nothing to delete")

	repo, err := fc.CreateRepo(ctx, bridgeOrg, e2etest.LockRepo, "lock", false)
	require.NoError(t, err)
	deleted, err = b.deleteIfID(ctx, fc, bridgeOrg, repo.ID+1)
	require.NoError(t, err)
	assert.False(t, deleted, "a different repository is never deleted")
	require.True(t, lockExists(t, fc, bridgeOrg))

	deleted, err = b.deleteIfID(ctx, fc, bridgeOrg, repo.ID)
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.False(t, lockExists(t, fc, bridgeOrg))

	assert.Equal(t, "abc", truncateID("abc"))
}

// afterCallClient runs a hook once after a matching call has landed on the
// forge and before its result reaches the caller, and can then replace the
// result with an error, as a response lost in transit.
type afterCallClient struct {
	*orgClient
	mu sync.Mutex
	// afterCommit runs after the lock repository's initial CommitFiles
	// landed; the commit then reports commitErr.
	afterCommit func()
	commitErr   error
	// afterClose runs after a closing holders.json write landed.
	afterClose func()
}

func (c *afterCallClient) CommitFiles(ctx context.Context, owner, repo, message string, files []forge.TreeFile) (bool, error) {
	changed, err := c.orgClient.CommitFiles(ctx, owner, repo, message, files)
	c.mu.Lock()
	hook, hookErr := c.afterCommit, c.commitErr
	c.afterCommit = nil
	c.mu.Unlock()
	if err == nil && repo == e2etest.LockRepo && hook != nil {
		hook()
		return false, hookErr
	}
	return changed, err
}

func (c *afterCallClient) UpdateFileIfMatch(ctx context.Context, owner, repo, path, message string, content []byte, expectedSHA string) error {
	err := c.orgClient.UpdateFileIfMatch(ctx, owner, repo, path, message, content, expectedSHA)
	if err != nil || !closingWrite(content) {
		return err
	}
	c.mu.Lock()
	hook := c.afterClose
	c.afterClose = nil
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func TestSharedOrgLock_InitErrorAfterPublishKeepsJoinedHolder(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	hooked := &afterCallClient{orgClient: p.clients[bridgeOrg], commitErr: errors.New("response lost")}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return hooked, nil }, nil)
	b := newBridgeAllocator(t, layout, p.clientFor, nil)

	// The initial commit lands, another runner joins that generation, and
	// only then does a's CommitFiles report an error.
	var s2 Slot
	hooked.afterCommit = func() {
		var joinErr error
		s2, _, joinErr = b.Claim(ctx)
		require.NoError(t, joinErr)
	}
	_, _, err := a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	require.Error(t, capErr.Failed[bridgeOrg])

	require.True(t, lockExists(t, p.fc, bridgeOrg), "a live holder keeps the lock")
	state := readLockState(t, p.fc, bridgeOrg)
	assert.Len(t, state.Holders, 1, "only the creator's holder was removed")
	assert.False(t, state.Closed)
	assert.Equal(t, state.Generation, lockReadme(t, p.fc, bridgeOrg))
	assert.True(t, p.exists(t, s2))

	require.NoError(t, b.Release(ctx, s2))
	assert.False(t, lockExists(t, p.fc, bridgeOrg), "the last holder deletes the lock")
}

func TestSharedOrgLock_InitErrorAfterPublishDeletesUnusedLock(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	hooked := &afterCallClient{orgClient: p.clients[bridgeOrg], commitErr: errors.New("response lost")}
	hooked.afterCommit = func() {}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return hooked, nil }, nil)

	_, _, err := a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	require.Error(t, capErr.Failed[bridgeOrg])
	assert.False(t, lockExists(t, p.fc, bridgeOrg), "a published lock nobody joined is closed and deleted")
}

func TestSharedOrgLock_ReplacementAfterClosingWriteIsKept(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	hooked := &afterCallClient{orgClient: p.clients[bridgeOrg]}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return hooked, nil }, nil)
	b := newBridgeAllocator(t, layout, p.clientFor, nil)

	s1, _, err := a.Claim(ctx)
	require.NoError(t, err)
	first := readLockState(t, p.fc, bridgeOrg).Generation

	// After a's closing write lands and before a deletes the lock, a stale
	// reclaimer replaces it and b joins the replacement.
	var s2 Slot
	hooked.afterClose = func() {
		require.NoError(t, p.fc.DeleteRepo(ctx, bridgeOrg, e2etest.LockRepo))
		var joinErr error
		s2, _, joinErr = b.Claim(ctx)
		require.NoError(t, joinErr)
	}
	require.NoError(t, a.Release(ctx, s1))

	require.True(t, lockExists(t, p.fc, bridgeOrg), "a must not delete the replacement lock")
	state := readLockState(t, p.fc, bridgeOrg)
	assert.NotEqual(t, first, state.Generation)
	assert.Len(t, state.Holders, 1)
	assert.False(t, state.Closed)

	require.NoError(t, b.Release(ctx, s2))
	assert.False(t, lockExists(t, p.fc, bridgeOrg))
}

func TestSharedOrgLock_ClosedLockWithUnknownIDIsLeftForReclaim(t *testing.T) {
	ctx := context.Background()
	p := newFakePool(StagePoolLayout(1))
	logs := &logRecorder{t: t}
	b := newSharedOrgLock(logs.logf, time.Now)
	_, err := p.fc.CreateRepo(ctx, bridgeOrg, e2etest.LockRepo, "lock", false)
	require.NoError(t, err)

	require.NoError(t, b.deleteClosed(ctx, p.fc, &orgLockMembership{org: bridgeOrg, generation: "g"}))
	assert.True(t, lockExists(t, p.fc, bridgeOrg))
	assert.True(t, logs.contains("no recorded repository ID"))
}

// blockingLockLookupClient makes the e2e-lock repository lookup wait for
// its context and then fail the way the live client does when a deadline
// cuts a rate-limit backoff short: a rate-limit error carrying the
// Retry-After wait that also matches the context error.
type blockingLockLookupClient struct {
	*orgClient
	retryAfter time.Duration
}

func (c *blockingLockLookupClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	if repo != e2etest.LockRepo {
		return c.orgClient.GetRepo(ctx, owner, repo)
	}
	<-ctx.Done()
	return nil, fmt.Errorf("%w: %w", ctx.Err(), &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "rate limit", RetryAfter: c.retryAfter})
}

func TestSharedOrgLock_ThrottledLockOperationMovesClaimToAnotherOrg(t *testing.T) {
	shortCreateTimeout(t, 20*time.Millisecond)
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 1}
	p := newFakePool(layout)
	clock := newTestClock(time.Now())
	throttled := &blockingLockLookupClient{orgClient: p.clients["halfsend-01"], retryAfter: 5 * time.Minute}
	a := newBridgeAllocator(t, layout, func(_ context.Context, org string) (forge.Client, error) {
		if org == "halfsend-01" {
			return throttled, nil
		}
		return p.clients[org], nil
	}, clock)

	start := time.Now()
	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Equal(t, "halfsend-02", s.Org, "a throttled lock lookup moves the claim to another org")

	// The org stays closed for the Retry-After wait the interrupted call
	// was honouring, not the default window.
	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, clock.Now().Add(5*time.Minute), capErr.RateLimited["halfsend-01"])
}

// probeRetryClient issues a probe request through a live client against a
// server that always answers 502, using the context CommitFiles received,
// and records how many times the client sent it. A context that disables
// ambiguous retries yields one send.
type probeRetryClient struct {
	*orgClient
	probe    *gh.LiveClient
	attempts *atomic.Int32
}

func (c *probeRetryClient) CommitFiles(ctx context.Context, owner, repo, message string, files []forge.TreeFile) (bool, error) {
	if repo == e2etest.LockRepo {
		_ = c.probe.DeleteRepo(ctx, owner, "probe")
	}
	return c.orgClient.CommitFiles(ctx, owner, repo, message, files)
}

func TestSharedOrgLock_InitCommitIsNotResentAfterAmbiguousFailure(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	instant := func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	probe := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(instant)

	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	probed := &probeRetryClient{orgClient: p.clients[bridgeOrg], probe: probe, attempts: &attempts}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return probed, nil }, nil)

	_, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	// A resend after a 5xx could land after another runner joined and be
	// retried against the new head, overwriting that holder's entry.
	assert.Equal(t, int32(1), attempts.Load(), "the initialisation commit must not be resent after an ambiguous failure")
}

// lateInitClient makes the lock repository's initial CommitFiles fail
// without landing it, and lands it only after holders.json was read as
// missing: a commit that is applied after the creator gave up on it.
type lateInitClient struct {
	*orgClient
	mu      sync.Mutex
	pending []forge.TreeFile
	// afterMissingRead runs after the late commit landed.
	afterMissingRead func()
}

func (c *lateInitClient) CommitFiles(ctx context.Context, owner, repo, message string, files []forge.TreeFile) (bool, error) {
	if repo != e2etest.LockRepo {
		return c.orgClient.CommitFiles(ctx, owner, repo, message, files)
	}
	c.mu.Lock()
	c.pending = files
	c.mu.Unlock()
	return false, errors.New("response lost")
}

func (c *lateInitClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	data, err := c.orgClient.GetFileContent(ctx, owner, repo, path)
	if repo != e2etest.LockRepo || path != sharedLockHoldersPath || !forge.IsNotFound(err) {
		return data, err
	}
	c.mu.Lock()
	files, hook := c.pending, c.afterMissingRead
	c.pending, c.afterMissingRead = nil, nil
	c.mu.Unlock()
	if files != nil {
		if _, commitErr := c.orgClient.CommitFiles(ctx, owner, repo, "late init", files); commitErr != nil {
			return nil, commitErr
		}
		if hook != nil {
			hook()
		}
	}
	return data, err
}

func TestSharedOrgLock_LateInitCommitIsNotDeleted(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	late := &lateInitClient{orgClient: p.clients[bridgeOrg]}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return late, nil }, nil)
	b := newBridgeAllocator(t, layout, p.clientFor, nil)

	// The initial commit lands between a's missing-state read and whatever
	// a does next, and b joins the published lock at once.
	var s2 Slot
	late.afterMissingRead = func() {
		var joinErr error
		s2, _, joinErr = b.Claim(ctx)
		require.NoError(t, joinErr)
	}
	// a's claim moves on to the next slot and joins the published lock.
	s1, _, err := a.Claim(ctx)
	require.NoError(t, err)

	require.True(t, lockExists(t, p.fc, bridgeOrg), "a must not delete a lock another runner joined")
	state := readLockState(t, p.fc, bridgeOrg)
	assert.Len(t, state.Holders, 3, "b's and a's holders are intact; the creator's entry is left for stale reclaim")
	assert.False(t, state.Closed)
	assert.True(t, p.exists(t, s2))
	assert.Empty(t, p.fc.DeletedRepos, "nothing may be deleted")

	require.NoError(t, a.Release(ctx, s1))
	require.NoError(t, b.Release(ctx, s2))
	assert.True(t, lockExists(t, p.fc, bridgeOrg), "the creator's stale entry keeps the lock until reclaim")
}

// joinLostClient applies a join write to the forge and then reports an
// error, as a response lost in transit.
type joinLostClient struct {
	*orgClient
	mu   sync.Mutex
	lost bool
}

func (c *joinLostClient) UpdateFileIfMatch(ctx context.Context, owner, repo, path, message string, content []byte, expectedSHA string) error {
	err := c.orgClient.UpdateFileIfMatch(ctx, owner, repo, path, message, content, expectedSHA)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && message == "join shared lock" && !c.lost {
		c.lost = true
		return errors.New("response lost")
	}
	return err
}

func TestSharedOrgLock_AmbiguousJoinWriteIsLeft(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	seed := newBridgeAllocator(t, layout, p.clientFor, nil)
	s1, _, err := seed.Claim(ctx)
	require.NoError(t, err)

	lost := &joinLostClient{orgClient: p.clients[bridgeOrg]}
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return lost, nil }, nil)
	// The first join's write lands and its response is lost; the claim
	// moves on to the next slot with a fresh join.
	s2, _, err := a.Claim(ctx)
	require.NoError(t, err)

	state := readLockState(t, p.fc, bridgeOrg)
	assert.Len(t, state.Holders, 2, "the holder whose join response was lost must not stay in the lock")
	assert.False(t, state.Closed)

	require.NoError(t, seed.Release(ctx, s1))
	require.NoError(t, a.Release(ctx, s2))
	assert.False(t, lockExists(t, p.fc, bridgeOrg), "no leaked holder keeps the lock after the last release")
}

// replaceOnStateReadClient replaces the lock repository with a new shared
// lock right after the first holders.json read.
type replaceOnStateReadClient struct {
	*orgClient
	mu      sync.Mutex
	replace func()
}

func (c *replaceOnStateReadClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	data, err := c.orgClient.GetFileContent(ctx, owner, repo, path)
	if repo != e2etest.LockRepo || path != sharedLockHoldersPath || err != nil {
		return data, err
	}
	c.mu.Lock()
	hook := c.replace
	c.replace = nil
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return data, err
}

func TestSharedOrgLock_JoinBindsRepositoryIDToState(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	seed := newBridgeAllocator(t, layout, p.clientFor, nil)
	_, _, err := seed.Claim(ctx)
	require.NoError(t, err)
	oldRepo, err := p.fc.GetRepo(ctx, bridgeOrg, e2etest.LockRepo)
	require.NoError(t, err)

	hooked := &replaceOnStateReadClient{orgClient: p.clients[bridgeOrg]}
	other := newBridgeAllocator(t, layout, p.clientFor, nil)
	hooked.replace = func() {
		require.NoError(t, p.fc.DeleteRepo(ctx, bridgeOrg, e2etest.LockRepo))
		_, _, claimErr := other.Claim(ctx)
		require.NoError(t, claimErr)
	}

	b := newSharedOrgLock(t.Logf, time.Now)
	m, reason, err := b.join(ctx, hooked, bridgeOrg)
	require.NoError(t, err)
	require.NotNil(t, m, reason)
	cur, err := p.fc.GetRepo(ctx, bridgeOrg, e2etest.LockRepo)
	require.NoError(t, err)
	assert.NotEqual(t, oldRepo.ID, cur.ID, "the lock was replaced")
	assert.Equal(t, cur.ID, m.repoID, "the membership records the repository its generation came from")
	assert.Equal(t, readLockState(t, p.fc, bridgeOrg).Generation, m.generation)
}

// swapAfterLockCreateClient lets a legacy runner replace a freshly created
// lock repository right after CreateRepo returns: it sees the default
// non-UUID README, deletes the repository as invalid, and recreates the lock
// with its own run ID.
type swapAfterLockCreateClient struct {
	*orgClient
	fc        *forge.FakeClient
	legacyRun string
}

func (c *swapAfterLockCreateClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	repo, err := c.orgClient.CreateRepo(ctx, org, name, description, private)
	if err != nil || name != e2etest.LockRepo {
		return repo, err
	}
	if err := c.fc.DeleteRepo(ctx, org, name); err != nil {
		return nil, err
	}
	if _, err := c.fc.CreateRepo(ctx, org, name, description, false); err != nil {
		return nil, err
	}
	if err := c.fc.CreateOrUpdateFile(ctx, org, name, sharedLockReadmePath, "acquire lock", []byte(c.legacyRun)); err != nil {
		return nil, err
	}
	return repo, nil
}

func TestSharedOrgLock_InitDoesNotPublishIntoReplacedLock(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	swap := &swapAfterLockCreateClient{orgClient: p.clients[bridgeOrg], fc: p.fc, legacyRun: uuid.NewString()}

	b := newSharedOrgLock(t.Logf, time.Now)
	m, reason, err := b.join(ctx, swap, bridgeOrg)
	require.NoError(t, err)
	assert.Nil(t, m, "a runner must not join a lock a legacy runner now owns")
	assert.NotEmpty(t, reason)

	assert.Equal(t, swap.legacyRun, lockReadme(t, p.fc, bridgeOrg), "the legacy runner's lock is untouched")
	_, err = p.fc.GetFileContent(ctx, bridgeOrg, e2etest.LockRepo, sharedLockMarkerPath)
	assert.True(t, forge.IsNotFound(err), "no shared flag may be published into the legacy lock")
	_, err = p.fc.GetFileContent(ctx, bridgeOrg, e2etest.LockRepo, sharedLockHoldersPath)
	assert.True(t, forge.IsNotFound(err), "no holder state may be published into the legacy lock")
}

// liveCommitClient sends the lock repository's initial CommitFiles through
// a live client against a stand-in server, so the client's own waits run.
type liveCommitClient struct {
	*orgClient
	live *gh.LiveClient
}

func (c *liveCommitClient) CommitFiles(ctx context.Context, owner, repo, message string, files []forge.TreeFile) (bool, error) {
	if repo == e2etest.LockRepo {
		return c.live.CommitFiles(ctx, owner, repo, message, files)
	}
	return c.orgClient.CommitFiles(ctx, owner, repo, message, files)
}

func TestSharedOrgLock_InitCommitStopsWhenLockReplacedDuringRetryWait(t *testing.T) {
	ctx := context.Background()
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	legacyRun := uuid.NewString()

	// The lock repository's branch ref is not readable yet (auto-init
	// race), so the live client waits and would retry the read, now seeing
	// whatever repository holds the name.
	var otherRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+bridgeOrg+"/"+e2etest.LockRepo {
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
			return
		}
		otherRequests.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	swapped := false
	live := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(time.Duration) <-chan time.Time {
		if !swapped {
			swapped = true
			// A legacy runner deletes the fresh lock as invalid and
			// recreates it with its own run ID during the wait.
			require.NoError(t, p.fc.DeleteRepo(ctx, bridgeOrg, e2etest.LockRepo))
			_, err := p.fc.CreateRepo(ctx, bridgeOrg, e2etest.LockRepo, "", false)
			require.NoError(t, err)
			require.NoError(t, p.fc.CreateOrUpdateFile(ctx, bridgeOrg, e2etest.LockRepo, sharedLockReadmePath, "acquire lock", []byte(legacyRun)))
		}
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	})
	client := &liveCommitClient{orgClient: p.clients[bridgeOrg], live: live}

	b := newSharedOrgLock(t.Logf, time.Now)
	m, reason, err := b.join(ctx, client, bridgeOrg)
	require.NoError(t, err)
	assert.Nil(t, m, "a runner must not join a lock that was replaced during the commit's wait")
	assert.NotEmpty(t, reason)
	assert.Equal(t, int32(1), otherRequests.Load(), "the ref read is not retried against the replacement")

	assert.Equal(t, legacyRun, lockReadme(t, p.fc, bridgeOrg), "the legacy runner's lock is untouched")
	_, err = p.fc.GetFileContent(ctx, bridgeOrg, e2etest.LockRepo, sharedLockHoldersPath)
	assert.True(t, forge.IsNotFound(err), "no holder state may be published into the replacement")
}

// lockDeleteServer is a live-API stand-in for deleting the e2e-lock
// repository: DELETE is rate-limited; the repository ID changes once the
// client's rate-limit wait has elapsed.
type lockDeleteServer struct {
	deletes  atomic.Int32
	replaced atomic.Bool
}

func (s *lockDeleteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const path = "/repos/halfsend/" + e2etest.LockRepo
	switch {
	case r.Method == http.MethodGet && r.URL.Path == path:
		id := 1
		if s.replaced.Load() {
			id = 2
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": e2etest.LockRepo, "full_name": "halfsend/" + e2etest.LockRepo})
	case r.Method == http.MethodDelete && r.URL.Path == path:
		if s.deletes.Add(1) > 1 && !s.replaced.Load() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestSharedOrgLock_DeleteIfIDRechecksIDAfterRateLimitWait(t *testing.T) {
	for _, replaced := range []bool{true, false} {
		t.Run(fmt.Sprintf("replaced=%v", replaced), func(t *testing.T) {
			state := &lockDeleteServer{}
			srv := httptest.NewServer(state)
			defer srv.Close()
			client := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(time.Duration) <-chan time.Time {
				if replaced {
					state.replaced.Store(true)
				}
				ch := make(chan time.Time, 1)
				ch <- time.Now()
				return ch
			})

			b := newSharedOrgLock(t.Logf, time.Now)
			deleted, err := b.deleteIfID(context.Background(), client, "halfsend", 1)
			require.NoError(t, err)
			if replaced {
				assert.False(t, deleted)
				assert.Equal(t, int32(1), state.deletes.Load(), "the DELETE must not be resent against a replacement lock")
				return
			}
			assert.True(t, deleted)
			assert.Equal(t, int32(2), state.deletes.Load(), "the DELETE is resent while the lock is still ours")
		})
	}
}
