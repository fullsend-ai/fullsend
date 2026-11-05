package install

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

// testClock is a settable clock for allocators.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(at time.Time) *testClock { return &testClock{now: at} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

// logRecorder collects log lines.
type logRecorder struct {
	t     *testing.T
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.t.Helper()
	line := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.lines = append(r.lines, line)
	r.mu.Unlock()
	r.t.Log(line)
}

func (r *logRecorder) contains(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// orgClient is one org's credentials over a shared fake forge. It can
// inject a CreateRepo error and report rate-limit headers.
type orgClient struct {
	*forge.FakeClient

	mu        sync.Mutex
	createErr error
	// createLostErr makes CreateRepo succeed server-side and then fail
	// with this error, as a retried create whose first attempt landed.
	createLostErr error
	// createBlocks makes CreateRepo wait for its context, as a client
	// sitting in rate-limit backoff.
	createBlocks bool
	// createLandsThenBlocks is createBlocks after the repo was created
	// server-side, as a create whose response never arrives.
	createLandsThenBlocks bool
	// createLandsLate is createBlocks for a create that lands only after
	// the claim gave up on it: CreateRepo records the description in
	// lateDesc instead of creating, and the test lands it later.
	createLandsLate bool
	lateDesc        string
	// deleteLostErr makes DeleteRepo succeed server-side and then fail
	// with this error.
	deleteLostErr error
	rl            forge.RateLimit
	rlSeen        bool
	creates       int
}

func (c *orgClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	c.mu.Lock()
	c.creates++
	err := c.createErr
	lost := c.createLostErr
	blocks := c.createBlocks || c.createLandsThenBlocks || c.createLandsLate
	lands := c.createLandsThenBlocks
	if c.createLandsLate {
		c.lateDesc = description
	}
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if lands {
		_, _ = c.FakeClient.CreateRepo(ctx, org, name, description, private)
	}
	if blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	repo, createErr := c.FakeClient.CreateRepo(ctx, org, name, description, private)
	if createErr == nil && lost != nil {
		return nil, lost
	}
	return repo, createErr
}

func (c *orgClient) DeleteRepo(ctx context.Context, owner, repo string) error {
	c.mu.Lock()
	lost := c.deleteLostErr
	c.mu.Unlock()
	err := c.FakeClient.DeleteRepo(ctx, owner, repo)
	if err == nil && lost != nil {
		return lost
	}
	return err
}

func (c *orgClient) RateLimit() (forge.RateLimit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rl, c.rlSeen
}

func (c *orgClient) setRateLimit(rl forge.RateLimit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rl, c.rlSeen = rl, true
}

func (c *orgClient) createCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates
}

// fakePool is a shared fake forge with one orgClient per org.
type fakePool struct {
	fc      *forge.FakeClient
	clients map[string]*orgClient
	noCreds map[string]error
}

func newFakePool(layout PoolLayout) *fakePool {
	fc := forge.NewFakeClient()
	p := &fakePool{fc: fc, clients: map[string]*orgClient{}, noCreds: map[string]error{}}
	for _, org := range layout.Orgs() {
		p.clients[org] = &orgClient{FakeClient: fc}
	}
	return p
}

func (p *fakePool) clientFor(_ context.Context, org string) (forge.Client, error) {
	if err, ok := p.noCreds[org]; ok {
		return nil, err
	}
	c, ok := p.clients[org]
	if !ok {
		return nil, fmt.Errorf("unknown org %s", org)
	}
	return c, nil
}

func (p *fakePool) exists(t *testing.T, s Slot) bool {
	t.Helper()
	_, err := p.fc.GetRepo(context.Background(), s.Org, s.Repo)
	if err == nil {
		return true
	}
	require.True(t, forge.IsNotFound(err), "unexpected GetRepo error: %v", err)
	return false
}

func testLayout() PoolLayout { return PoolLayout{FirstOrgIndex: 1, OrgCount: 3, SlotsPerOrg: 4} }

// newTestAllocator returns an allocator without the shared e2e-lock
// bridge or slot GC, so slot-leasing tests see only slot repo calls.
// orglock_test.go covers the bridge and slotgc_test.go covers GC.
func newTestAllocator(t *testing.T, layout PoolLayout, p *fakePool) *slotAllocator {
	t.Helper()
	a, err := newSlotAllocator(layout, p.clientFor, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.gc = nil
	return a
}

func TestPoolNames(t *testing.T) {
	assert.Equal(t, StageOrg, PoolOrgName(0))
	assert.Equal(t, "halfsend-01", PoolOrgName(1))
	assert.Equal(t, "halfsend-12", PoolOrgName(12))
	assert.Equal(t, "test-repo-01", PoolSlotName(1))
	assert.Equal(t, "test-repo-12", PoolSlotName(12))
	assert.Equal(t, "halfsend-01/test-repo-03", Slot{Org: "halfsend-01", Repo: "test-repo-03"}.String())
}

func TestDevPoolLayout(t *testing.T) {
	t.Setenv("BEHAVIOUR_POOL_SIZE", "")
	l := PoolLayoutForEnvironment("dev", t.Logf)
	assert.Equal(t, DevPoolLayout(DefaultPoolSize), l)
	orgs := l.Orgs()
	require.Len(t, orgs, 12)
	assert.Equal(t, "halfsend-01", orgs[0])
	assert.Equal(t, "halfsend-12", orgs[11])
	slots := l.Slots()
	require.Len(t, slots, 144)
	assert.Equal(t, Slot{Org: "halfsend-01", Repo: "test-repo-01"}, slots[0])
	assert.Equal(t, Slot{Org: "halfsend-12", Repo: "test-repo-12"}, slots[143])
}

func TestStagePoolLayout_OnlyUnsuffixedOrg(t *testing.T) {
	t.Setenv("BEHAVIOUR_POOL_SIZE", "")
	l := PoolLayoutForEnvironment("stage", t.Logf)
	assert.Equal(t, StagePoolLayout(DefaultPoolSize), l)
	assert.Equal(t, []string{"halfsend"}, l.Orgs())
	slots := l.Slots()
	require.Len(t, slots, 12)
	for _, s := range slots {
		assert.Equal(t, StageOrg, s.Org)
	}
}

func TestNewSlotAllocator_Validation(t *testing.T) {
	cf := func(context.Context, string) (forge.Client, error) { return nil, nil }
	for name, l := range map[string]PoolLayout{
		"negative first": {FirstOrgIndex: -1, OrgCount: 1, SlotsPerOrg: 1},
		"zero orgs":      {OrgCount: 0, SlotsPerOrg: 1},
		"zero slots":     {OrgCount: 1, SlotsPerOrg: 0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newSlotAllocator(l, cf, nil)
			require.Error(t, err)
		})
	}
	_, err := newSlotAllocator(StagePoolLayout(DefaultPoolSize), nil, nil)
	require.Error(t, err)

	a, err := newSlotAllocator(DevPoolLayout(DefaultPoolSize), cf, nil)
	require.NoError(t, err)
	assert.Equal(t, 144, a.Capacity())
}

func TestSlotAllocator_ConcurrentClaimsAreDistinct(t *testing.T) {
	layout := testLayout()
	p := newFakePool(layout)

	// Two independent allocators model two suites sharing the pool.
	allocs := []*slotAllocator{newTestAllocator(t, layout, p), newTestAllocator(t, layout, p)}

	const workers = 20
	type result struct {
		slot  Slot
		alloc *slotAllocator
		err   error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(a *slotAllocator) {
			defer wg.Done()
			s, _, err := a.Claim(context.Background())
			results <- result{slot: s, alloc: a, err: err}
		}(allocs[i%2])
	}
	wg.Wait()
	close(results)

	seen := map[Slot]*slotAllocator{}
	orgs := map[string]bool{}
	var exhausted int
	for r := range results {
		if r.err != nil {
			require.ErrorIs(t, r.err, ErrPoolCapacityExhausted)
			exhausted++
			continue
		}
		_, dup := seen[r.slot]
		require.False(t, dup, "slot %s leased twice", r.slot)
		seen[r.slot] = r.alloc
		orgs[r.slot.Org] = true
	}
	assert.Len(t, seen, 12, "every slot is leased exactly once")
	assert.Equal(t, workers-12, exhausted)
	assert.Len(t, orgs, 3, "leases span all orgs")

	// Releasing one lease deletes only that repo.
	var released Slot
	for s, a := range seen {
		released = s
		require.NoError(t, a.Release(context.Background(), s))
		break
	}
	assert.Equal(t, []string{released.String()}, p.fc.DeletedRepos)
	for s := range seen {
		assert.Equal(t, s != released, p.exists(t, s), "slot %s", s)
	}
}

func TestSlotAllocator_ExistingRepoIsOccupiedAndNeverDeleted(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 0, OrgCount: 1, SlotsPerOrg: 3}
	p := newFakePool(layout)
	for _, s := range layout.Slots()[:2] {
		p.fc.Repos = append(p.fc.Repos, forge.Repository{Name: s.Repo, FullName: s.String()})
	}
	a := newTestAllocator(t, layout, p)

	s, client, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Slot{Org: "halfsend", Repo: "test-repo-03"}, s)
	assert.Same(t, p.clients["halfsend"], client)

	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, 3, capErr.Occupied)
	assert.Contains(t, err.Error(), "3 occupied slot(s)")
	assert.Empty(t, p.fc.DeletedRepos, "a competing allocator's repos must never be deleted")
}

func TestSlotAllocator_ReleaseErrors(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 0, OrgCount: 1, SlotsPerOrg: 1}
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)

	err := a.Release(context.Background(), Slot{Org: "halfsend", Repo: "test-repo-01"})
	require.ErrorContains(t, err, "not held")
	assert.Empty(t, p.fc.DeletedRepos)

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []Slot{s}, a.Held())

	p.fc.Errors["DeleteRepo"] = errors.New("boom")
	err = a.Release(context.Background(), s)
	require.ErrorContains(t, err, "boom")
	assert.Empty(t, a.Held(), "the lease is dropped even when deletion fails")
	assert.True(t, p.exists(t, s), "the undeleted repo stays and keeps the slot occupied")

	// The failed-deletion repo is occupied for every allocator, including this one.
	_, _, err = a.Claim(context.Background())
	require.ErrorIs(t, err, ErrPoolCapacityExhausted)

	// Double release after the failure is rejected.
	require.ErrorContains(t, a.Release(context.Background(), s), "not held")

	// A repo already gone at release time is not an error.
	delete(p.fc.Errors, "DeleteRepo")
	require.NoError(t, p.fc.DeleteRepo(context.Background(), s.Org, s.Repo))
	s2, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	p.fc.Errors["DeleteRepo"] = fmt.Errorf("%w: gone", forge.ErrNotFound)
	require.NoError(t, a.Release(context.Background(), s2))
}

func TestSlotAllocator_HeldSlotCountsAsOccupied(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 0, OrgCount: 1, SlotsPerOrg: 1}
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	_, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	calls := p.clients["halfsend"].createCalls()

	_, _, err = a.Claim(context.Background())
	require.ErrorIs(t, err, ErrPoolCapacityExhausted)
	assert.Equal(t, calls, p.clients["halfsend"].createCalls(), "held slots are skipped without an API call")
}

func TestSlotAllocator_RateLimitHeadersSwitchOrgs(t *testing.T) {
	layout := testLayout()
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	limited := p.clients["halfsend-01"]
	reset := now.Add(10 * time.Minute)
	// Make halfsend-01 slots come first so its headers are observed.
	a.shuffle = func([]Slot) {}

	// The client has seen no headers yet, so the first claim lands on it.
	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-01", s.Org)
	// The scenario then spends the budget; the next claim observes it
	// before creating, so the org is closed without another create call.
	limited.setRateLimit(forge.RateLimit{Limit: 5000, Remaining: 0, Reset: reset, Resource: "core"})
	for range 8 {
		s, _, err = a.Claim(context.Background())
		require.NoError(t, err)
		assert.NotEqual(t, "halfsend-01", s.Org)
	}
	assert.Equal(t, 1, limited.createCalls(), "no further claims on the limited org")

	// Everything left is either occupied or quota-gated.
	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, reset, capErr.RateLimited["halfsend-01"])
	assert.Contains(t, err.Error(), "rate-limited: halfsend-01 until 2026-10-19T12:10:00Z")
	assert.Contains(t, a.QuotaDiagnostics(), "halfsend-01: remaining=0/5000")

	// After the reset the org is eligible again.
	now = reset
	s, _, err = a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-01", s.Org)
}

func TestSlotAllocator_QuotaBelowReserveBeforeFirstClaim(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 2}
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.shuffle = func([]Slot) {}

	// Low but non-zero remaining, below the reserve, before any claim.
	reset := now.Add(20 * time.Minute)
	p.clients["halfsend-01"].setRateLimit(forge.RateLimit{Limit: 5000, Remaining: rateLimitReserve, Reset: reset, Resource: "core"})

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-02", s.Org)
	assert.Zero(t, p.clients["halfsend-01"].createCalls(), "an org already below the reserve gets no create call")

	// Once the reset passes the org is eligible again.
	now = reset
	s, _, err = a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-01", s.Org)
}

func TestSlotAllocator_RetryAfterBeyondDefaultWindow(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 1}
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.shuffle = func([]Slot) {}

	// Secondary limit with healthy primary quota and a 5 minute Retry-After.
	p.clients["halfsend-01"].setRateLimit(forge.RateLimit{Limit: 5000, Remaining: 4000, Reset: now.Add(time.Hour)})
	p.clients["halfsend-01"].createErr = &gh.APIError{
		StatusCode: http.StatusForbidden,
		Message:    "rate limit: secondary",
		RetryAfter: 5 * time.Minute,
	}
	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-02", s.Org)

	// Past the default window but inside Retry-After the org stays closed.
	now = now.Add(defaultRateLimitWindow + time.Second)
	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, time.Date(2026, 10, 19, 12, 5, 0, 0, time.UTC), capErr.RateLimited["halfsend-01"])
	assert.Equal(t, 1, p.clients["halfsend-01"].createCalls())
}

func TestSlotAllocator_RateLimitErrorRetryWindow(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 1}
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.shuffle = func([]Slot) {}

	// Secondary limit: 403 rate limit, no exhausted primary quota.
	p.clients["halfsend-01"].createErr = &gh.APIError{StatusCode: http.StatusForbidden, Message: "You have exceeded a secondary rate limit"}
	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-02", s.Org)

	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, now.Add(defaultRateLimitWindow), capErr.RateLimited["halfsend-01"])
	assert.Equal(t, 1, p.clients["halfsend-01"].createCalls(), "gated org is not retried inside its window")

	// A 429 with an exhausted primary quota waits for the reported reset.
	p.clients["halfsend-01"].createErr = &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many"}
	reset := now.Add(30 * time.Minute)
	p.clients["halfsend-01"].setRateLimit(forge.RateLimit{Remaining: 0, Reset: reset})
	now = now.Add(defaultRateLimitWindow)
	_, _, err = a.Claim(context.Background())
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, reset, capErr.RateLimited["halfsend-01"])
}

func TestSlotAllocator_DeniedAndFailedOrgs(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 3, SlotsPerOrg: 2}
	p := newFakePool(layout)
	p.noCreds["halfsend-01"] = errors.New("mint refused")
	p.clients["halfsend-02"].createErr = &gh.APIError{StatusCode: http.StatusForbidden, Message: "Resource not accessible by integration"}
	p.clients["halfsend-03"].createErr = &gh.APIError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}
	a := newTestAllocator(t, layout, p)

	_, _, err := a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	require.ErrorIs(t, err, ErrPoolCapacityExhausted)
	assert.Contains(t, capErr.Denied, "halfsend-01")
	assert.Contains(t, capErr.Denied, "halfsend-02")
	assert.Contains(t, capErr.Failed, "halfsend-03")
	assert.Equal(t, 1, p.clients["halfsend-02"].createCalls(), "a denied org is skipped for the rest of the pass")
	assert.Equal(t, 2, p.clients["halfsend-03"].createCalls(), "a transient failure does not skip the org")
	msg := err.Error()
	assert.Contains(t, msg, "denied: halfsend-01 (mint refused), halfsend-02")
	assert.Contains(t, msg, "failed: halfsend-03")
	assert.Empty(t, a.Held())
}

func TestSlotAllocator_Cancellation(t *testing.T) {
	layout := testLayout()
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := a.Claim(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, errors.Is(err, ErrPoolCapacityExhausted))

	// Cancelled while resolving credentials.
	ctx2, cancel2 := context.WithCancel(context.Background())
	a2, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) {
		cancel2()
		return nil, errors.New("cancelled mint call")
	}, t.Logf)
	require.NoError(t, err)
	_, _, err = a2.Claim(ctx2)
	require.ErrorIs(t, err, context.Canceled)

	// Cancelled during the create call.
	ctx3, cancel3 := context.WithCancel(context.Background())
	p.clients["halfsend-01"].createErr = errors.New("request aborted")
	p.clients["halfsend-02"].createErr = errors.New("request aborted")
	p.clients["halfsend-03"].createErr = errors.New("request aborted")
	a3, err := newSlotAllocator(layout, func(ctx context.Context, org string) (forge.Client, error) {
		cancel3()
		return p.clientFor(ctx, org)
	}, t.Logf)
	require.NoError(t, err)
	a3.orgLock = nil
	_, _, err = a3.Claim(ctx3)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, a3.Held())
}

func TestSlotAllocator_AcquireWaitsForRelease(t *testing.T) {
	old := slotClaimPoll
	slotClaimPoll = 5 * time.Millisecond
	t.Cleanup(func() { slotClaimPoll = old })

	layout := PoolLayout{FirstOrgIndex: 0, OrgCount: 1, SlotsPerOrg: 1}
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	other := newTestAllocator(t, layout, p)

	held, _, err := other.Claim(context.Background())
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, _, err := a.Acquire(context.Background())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, other.Release(context.Background(), held))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire did not return after the slot was released")
	}
	assert.Equal(t, []Slot{held}, a.Held())
}

func TestSlotAllocator_AcquireCancelAndHardError(t *testing.T) {
	old := slotClaimPoll
	slotClaimPoll = 5 * time.Millisecond
	t.Cleanup(func() { slotClaimPoll = old })

	layout := PoolLayout{FirstOrgIndex: 0, OrgCount: 1, SlotsPerOrg: 1}
	p := newFakePool(layout)
	p.fc.Repos = append(p.fc.Repos, forge.Repository{Name: "test-repo-01", FullName: "halfsend/test-repo-01"})
	a := newTestAllocator(t, layout, p)

	// Cancel once a pass has reported no capacity, so the wait is what
	// observes the cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.logf = func(format string, args ...any) {
		if strings.HasPrefix(format, "[slots] no capacity") {
			cancel()
		}
	}
	_, _, err := a.Acquire(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrPoolCapacityExhausted)
	assert.Contains(t, err.Error(), "occupied")

	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	_, _, err = a.Acquire(cctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, errors.Is(err, ErrPoolCapacityExhausted))
}

func TestSlotAllocator_QuotaIgnoresNonReporters(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 0, OrgCount: 1, SlotsPerOrg: 1}
	fc := forge.NewFakeClient()
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return fc, nil }, nil)
	require.NoError(t, err)
	_, _, err = a.Claim(context.Background())
	require.NoError(t, err)
	assert.Empty(t, a.QuotaDiagnostics())

	// A reporter that has not seen headers yet records nothing.
	p := newFakePool(layout)
	b := newTestAllocator(t, layout, p)
	_, _, err = b.Claim(context.Background())
	require.NoError(t, err)
	assert.Empty(t, b.QuotaDiagnostics())
	assert.Equal(t, "quota unknown", b.quotaSummary("halfsend"))
}
