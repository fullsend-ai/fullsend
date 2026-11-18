package install

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

// These tests drive slot GC (slotgc.go) against a shared fake forge with an
// injected clock. Seeded slot repos get explicit creation times; repos the
// fake creates during a test are stamped with the real time, which the test
// clocks start from, so they are young.

// seedIDs hands out IDs for seeded repos, below the fake's created IDs.
var seedIDs atomic.Int64

// seedSlotRepo adds a repository under slot's name with the given
// description and creation time, as left behind by an earlier run. It must
// run before the fake is used concurrently.
func seedSlotRepo(p *fakePool, s Slot, desc string, created time.Time) int64 {
	id := seedIDs.Add(1)
	p.fc.Repos = append(p.fc.Repos, forge.Repository{
		ID: id, Name: s.Repo, FullName: s.String(), Description: desc, DefaultBranch: "main", CreatedAt: created,
	})
	return id
}

// staleTime returns a creation time old enough to reclaim at clock's time.
func staleTime(clock *testClock) time.Time { return clock.Now().Add(-slotReclaimAge - time.Minute) }

// newGCAllocator returns an allocator with GC enabled and the shared
// e2e-lock bridge disabled, trying slots in index order on clock's time.
func newGCAllocator(t *testing.T, layout PoolLayout, clientFor OrgClientFunc, clock *testClock) *slotAllocator {
	t.Helper()
	a, err := newSlotAllocator(layout, clientFor, t.Logf)
	require.NoError(t, err)
	require.NotNil(t, a.gc, "GC is on by default")
	a.orgLock = nil
	a.shuffle = func([]Slot) {}
	a.now = clock.Now
	return a
}

func readGCLeaseFile(t *testing.T, fc *forge.FakeClient, org string) gcLeaseState {
	t.Helper()
	data, err := fc.GetFileContent(context.Background(), org, slotGCRepo, slotGCLeasePath)
	require.NoError(t, err)
	var state gcLeaseState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func writeGCLeaseFile(t *testing.T, fc *forge.FakeClient, org string, state gcLeaseState) {
	t.Helper()
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, fc.CreateOrUpdateFile(context.Background(), org, slotGCRepo, slotGCLeasePath, "seed", data))
}

func deleteCount(fc *forge.FakeClient, s Slot) int {
	n := 0
	for _, d := range fc.DeletedRepos {
		if d == s.String() {
			n++
		}
	}
	return n
}

// gcHookClient wraps an orgClient with hooks on the calls GC makes.
type gcHookClient struct {
	*orgClient

	mu        sync.Mutex
	repoCalls map[string]int
	fileCalls int
	// onGetRepo runs before each GetRepo with the per-name call count; a
	// non-nil error is returned instead of the lookup.
	onGetRepo func(repo string, call int) error
	// onGetLease runs before each read of the lease file with the call count.
	onGetLease func(call int)
	// onUpdate runs once, before the first UpdateFileIfMatch.
	onUpdate func()
	// deleteErr is returned by DeleteRepo without deleting anything.
	deleteErr error
}

func newGCHookClient(c *orgClient) *gcHookClient {
	return &gcHookClient{orgClient: c, repoCalls: map[string]int{}}
}

func (c *gcHookClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	c.mu.Lock()
	c.repoCalls[repo]++
	n, hook := c.repoCalls[repo], c.onGetRepo
	c.mu.Unlock()
	if hook != nil {
		if err := hook(repo, n); err != nil {
			return nil, err
		}
	}
	return c.orgClient.GetRepo(ctx, owner, repo)
}

func (c *gcHookClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	if repo == slotGCRepo && path == slotGCLeasePath {
		c.mu.Lock()
		c.fileCalls++
		n, hook := c.fileCalls, c.onGetLease
		c.mu.Unlock()
		if hook != nil {
			hook(n)
		}
	}
	return c.orgClient.GetFileContent(ctx, owner, repo, path)
}

func (c *gcHookClient) UpdateFileIfMatch(ctx context.Context, owner, repo, path, message string, content []byte, expectedSHA string) error {
	c.mu.Lock()
	hook := c.onUpdate
	c.onUpdate = nil
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return c.orgClient.UpdateFileIfMatch(ctx, owner, repo, path, message, content, expectedSHA)
}

func (c *gcHookClient) DeleteRepo(ctx context.Context, owner, repo string) error {
	c.mu.Lock()
	err := c.deleteErr
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.orgClient.DeleteRepo(ctx, owner, repo)
}

func (c *gcHookClient) getRepoCalls(repo string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.repoCalls[repo]
}

func singleClient(c forge.Client) OrgClientFunc {
	return func(context.Context, string) (forge.Client, error) { return c, nil }
}

// TestSlotGC_ReclaimsAbandonedSlotsInsideAllocator fills a small DEV pool
// with abandoned, unmarked and young repos, crosses the occupied-claim
// threshold, and checks that GC frees only the owned stale slots, which the
// next passes claim.
func TestSlotGC_ReclaimsAbandonedSlotsInsideAllocator(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 2}
	p := newFakePool(layout)
	a := newGCAllocator(t, layout, p.clientFor, clock)

	staleA := Slot{Org: "halfsend-01", Repo: "test-repo-01"}
	unmarked := Slot{Org: "halfsend-01", Repo: "test-repo-02"}
	young := Slot{Org: "halfsend-02", Repo: "test-repo-01"}
	staleB := Slot{Org: "halfsend-02", Repo: "test-repo-02"}
	seedSlotRepo(p, staleA, newLeaseDescription(), staleTime(clock))
	seedSlotRepo(p, unmarked, "some legacy repo", staleTime(clock))
	seedSlotRepo(p, young, newLeaseDescription(), clock.Now().Add(-slotReclaimAge+time.Minute))
	seedSlotRepo(p, staleB, newLeaseDescription(), staleTime(clock))

	// First pass: two occupied claims per org, below the threshold of
	// gcThresholdPasses × SlotsPerOrg = 4. No GC runs.
	_, _, err := a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, 4, capErr.Occupied)
	assert.Empty(t, capErr.GC, "GC must not run before the threshold")
	assert.Zero(t, capErr.Reclaimed)
	for _, org := range layout.Orgs() {
		_, getErr := p.fc.GetRepo(ctx, org, slotGCRepo)
		assert.True(t, forge.IsNotFound(getErr), "no coordination repo before GC runs")
	}

	// Second pass crosses the threshold in both orgs and collects them.
	_, _, err = a.Claim(ctx)
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, 2, capErr.Reclaimed)
	require.Contains(t, capErr.GC, "halfsend-01")
	require.Contains(t, capErr.GC, "halfsend-02")
	assert.Contains(t, capErr.GC["halfsend-01"], "reclaimed 1 (test-repo-01)")
	assert.Contains(t, capErr.GC["halfsend-01"], "test-repo-02 (no test-ownership marker)")
	assert.Contains(t, capErr.GC["halfsend-02"], "reclaimed 1 (test-repo-02)")
	assert.Contains(t, capErr.GC["halfsend-02"], "test-repo-01 (age 59m0s below reclaim age 1h0m0s)")
	assert.Contains(t, err.Error(), "2 slot(s) reclaimed by gc")
	assert.Contains(t, err.Error(), "gc: halfsend-01 (reclaimed 1")
	assert.Contains(t, a.GCDiagnostics(), "halfsend-02: reclaimed 1")
	assert.False(t, p.exists(t, staleA))
	assert.False(t, p.exists(t, staleB))

	// The collector released its leases and recorded the collection.
	for _, org := range layout.Orgs() {
		state := readGCLeaseFile(t, p.fc, org)
		assert.Empty(t, state.Holder, "lease released on %s", org)
		assert.Equal(t, clock.Now().UTC(), state.LastCollectedAt.UTC())
	}

	// The reclaimed slots are claimable; the unmarked and young ones stay.
	got := map[Slot]bool{}
	for range 2 {
		s, _, claimErr := a.Claim(ctx)
		require.NoError(t, claimErr)
		got[s] = true
	}
	assert.Equal(t, map[Slot]bool{staleA: true, staleB: true}, got)
	_, _, err = a.Claim(ctx)
	require.ErrorIs(t, err, ErrPoolCapacityExhausted)
	assert.True(t, p.exists(t, unmarked), "an unmarked repo is never reclaimed")
	assert.True(t, p.exists(t, young), "a repo younger than the reclaim age is never reclaimed")
}

// TestSlotGC_StageAcquireReclaims checks the single unsuffixed STAGE org
// takes the same path: Acquire waits through occupied passes, GC reclaims,
// and Acquire claims the freed slot without another poll wait.
func TestSlotGC_StageAcquireReclaims(t *testing.T) {
	old := slotClaimPoll
	slotClaimPoll = time.Millisecond
	t.Cleanup(func() { slotClaimPoll = old })

	clock := newTestClock(time.Now())
	layout := StagePoolLayout(3)
	p := newFakePool(layout)
	a := newGCAllocator(t, layout, p.clientFor, clock)
	young := Slot{Org: StageOrg, Repo: "test-repo-01"}
	stale := Slot{Org: StageOrg, Repo: "test-repo-02"}
	unmarked := Slot{Org: StageOrg, Repo: "test-repo-03"}
	seedSlotRepo(p, young, newLeaseDescription(), clock.Now())
	seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
	seedSlotRepo(p, unmarked, SlotRepoDescription, staleTime(clock))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, _, err := a.Acquire(ctx)
	require.NoError(t, err)
	assert.Equal(t, stale, s)
	assert.Contains(t, a.GCDiagnostics(), "test-repo-03 (no test-ownership marker)",
		"a description without a lease nonce is not the ownership marker")
	assert.True(t, p.exists(t, young))
	assert.True(t, p.exists(t, unmarked))
}

// TestSlotGC_PolicyConstants pins the documented relationships between the
// reclaim threshold, the age policy and the lease timings.
func TestSlotGC_PolicyConstants(t *testing.T) {
	a, err := newSlotAllocator(DevPoolLayout(12), func(context.Context, string) (forge.Client, error) { return nil, nil }, nil)
	require.NoError(t, err)
	assert.Equal(t, 24, a.gcThreshold(), "two full passes over a 12-slot org")
	assert.Equal(t, time.Hour, slotReclaimAge)
	assert.Equal(t, slotJobTimeLimit+slotReclaimMargin, slotReclaimAge)
	assert.Less(t, gcLeaseSafetyMargin, gcLeaseDuration)
	// The last action a collector starts (lease reread, identity recheck,
	// DELETE) is bounded per request and must finish inside the margin.
	assert.Less(t, 3*claimCreateTimeout, gcLeaseSafetyMargin)
	assert.Less(t, gcLeaseDuration, slotReclaimAge, "a crashed collector's lease expires long before a slot it skipped turns stale")
}

// TestSlotReclaimAgeCoversJobLimits ties slotJobTimeLimit to the actual
// execution limits of the suites that hold slot leases: the Makefile
// targets' go test -timeout (the per-attempt bound that panics every
// claiming process) and the CI jobs' timeout-minutes. The job budget may
// reach two attempts because scripts/run-with-infra-retry.sh re-runs an
// infrastructure-classified failed suite once, sequentially; each attempt
// keeps its own -timeout, so a slot repository's life stays bounded by
// slotJobTimeLimit regardless of the larger job budget. Raising the
// per-attempt -timeout past slotJobTimeLimit, or a job budget past the two
// attempts it may run, would let GC reclaim a live slot.
func TestSlotReclaimAgeCoversJobLimits(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	timeoutRe := regexp.MustCompile(`go test .*-timeout (\S+) \./e2e/behaviour/`)
	matches := timeoutRe.FindAllStringSubmatch(string(makefile), -1)
	require.Len(t, matches, 2, "behaviour-test and playback-test")
	for _, m := range matches {
		d, parseErr := time.ParseDuration(m[1])
		require.NoError(t, parseErr)
		assert.LessOrEqual(t, d, slotJobTimeLimit, "Makefile go test -timeout %s", m[1])
		assert.Positive(t, d)
	}

	f, err := os.Open(filepath.Join(root, ".github", "workflows", "e2e.yml"))
	require.NoError(t, err)
	defer f.Close()
	jobRe := regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	limitRe := regexp.MustCompile(`^    timeout-minutes:\s*(\d+)\s*$`)
	limits := map[string]time.Duration{}
	job := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := jobRe.FindStringSubmatch(line); m != nil {
			job = m[1]
			continue
		}
		if m := limitRe.FindStringSubmatch(line); m != nil && job != "" {
			n, convErr := strconv.Atoi(m[1])
			require.NoError(t, convErr)
			limits[job] = time.Duration(n) * time.Minute
		}
	}
	require.NoError(t, sc.Err())
	for _, j := range []string{"behaviour", "playback"} {
		limit, ok := limits[j]
		require.True(t, ok, "e2e.yml job %q must set timeout-minutes", j)
		// Two attempts, not one: the infra-retry wrapper runs a second
		// sequential go test process, each bounded by its own -timeout
		// (asserted above against the Makefile), so the job budget may
		// cover two attempts but no more.
		assert.LessOrEqual(t, limit, 2*slotJobTimeLimit, "e2e.yml job %q timeout-minutes", j)
	}
}

// TestSlotGC_Unreclaimable checks each reason a repo stays occupied, and
// the age boundary, with an injected clock.
func TestSlotGC_Unreclaimable(t *testing.T) {
	clock := newTestClock(time.Now())
	slot := Slot{Org: StageOrg, Repo: "test-repo-01"}
	marked := newLeaseDescription()
	a := newGCAllocator(t, StagePoolLayout(1), newFakePool(StagePoolLayout(1)).clientFor, clock)
	a.addPending(slot, "fullsend behaviour test slot lease=0123456789abcdef0123456789abcdef")

	old := clock.Now().Add(-slotReclaimAge)
	tests := []struct {
		name string
		repo forge.Repository
		want string
	}{
		{name: "exactly the reclaim age", repo: forge.Repository{ID: 1, Description: marked, CreatedAt: old}},
		{name: "one second young", repo: forge.Repository{ID: 1, Description: marked, CreatedAt: old.Add(time.Second)}, want: "age 59m59s below reclaim age 1h0m0s"},
		{name: "created in the future", repo: forge.Repository{ID: 1, Description: marked, CreatedAt: clock.Now().Add(time.Minute)}, want: "below reclaim age"},
		{name: "no marker", repo: forge.Repository{ID: 1, Description: "", CreatedAt: old}, want: "no test-ownership marker"},
		{name: "marker without nonce", repo: forge.Repository{ID: 1, Description: SlotRepoDescription, CreatedAt: old}, want: "no test-ownership marker"},
		{name: "malformed nonce", repo: forge.Repository{ID: 1, Description: SlotRepoDescription + leaseDescriptionSep + "another-holder", CreatedAt: old}, want: "no test-ownership marker"},
		{name: "upper-case nonce", repo: forge.Repository{ID: 1, Description: SlotRepoDescription + leaseDescriptionSep + "0123456789ABCDEF0123456789ABCDEF", CreatedAt: old}, want: "no test-ownership marker"},
		{name: "unknown ID", repo: forge.Repository{Description: marked, CreatedAt: old}, want: "repository ID unknown"},
		{name: "unknown age", repo: forge.Repository{ID: 1, Description: marked}, want: "creation time unknown"},
		{name: "archived", repo: forge.Repository{ID: 1, Description: marked, CreatedAt: old, Archived: true}, want: "archived"},
		{name: "own pending create", repo: forge.Repository{ID: 1, Description: "fullsend behaviour test slot lease=0123456789abcdef0123456789abcdef", CreatedAt: old}, want: "pending adoption"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.unreclaimable(slot, &tt.repo)
			if tt.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tt.want)
		})
	}
}

// TestSlotGC_HeldSlotIsNotProbed checks that GC skips slots this allocator
// holds, even when their repo would otherwise qualify.
func TestSlotGC_HeldSlotIsNotProbed(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	hooked := newGCHookClient(p.clients[StageOrg])
	a := newGCAllocator(t, layout, singleClient(hooked), clock)
	s, _, err := a.Claim(ctx)
	require.NoError(t, err)

	clock.Set(clock.Now().Add(2 * slotReclaimAge))
	before := hooked.getRepoCalls(s.Repo)
	n, report := a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Equal(t, "reclaimed 0", report)
	assert.Equal(t, before, hooked.getRepoCalls(s.Repo), "a held slot is not looked up")
	assert.True(t, p.exists(t, s))
}

// TestSlotGC_ExpiredLeaseTakeoverAfterCrash checks that an unexpired lease
// of another collector blocks collection, and that once it expires (its
// collector crashed) the next collector takes it over and reclaims.
func TestSlotGC_ExpiredLeaseTakeoverAfterCrash(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
	seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
	_, err := p.fc.CreateRepo(ctx, StageOrg, slotGCRepo, slotGCRepoDescription, false)
	require.NoError(t, err)
	crashed := gcLeaseState{Holder: "crashed-collector", AcquiredAt: clock.Now(), ExpiresAt: clock.Now().Add(gcLeaseDuration)}
	writeGCLeaseFile(t, p.fc, StageOrg, crashed)

	logs := &logRecorder{t: t}
	a := newGCAllocator(t, layout, p.clientFor, clock)
	a.logf = logs.logf

	n, report := a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "skipped: collector lease held by crashed-")
	assert.True(t, p.exists(t, stale), "only the current collector may act")

	clock.Set(crashed.ExpiresAt)
	n, report = a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
	assert.Equal(t, 1, n, report)
	assert.False(t, p.exists(t, stale))
	assert.True(t, logs.contains("took over expired collector lease of crashed-"))
	state := readGCLeaseFile(t, p.fc, StageOrg)
	assert.Empty(t, state.Holder)
	assert.Equal(t, clock.Now().UTC(), state.LastCollectedAt.UTC())
}

// TestSlotGC_Cooldown checks that a recent collection, by any suite, keeps
// other collectors from probing the org until gcCooldown has passed.
func TestSlotGC_Cooldown(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
	seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
	writeGCLeaseFile(t, p.fc, StageOrg, gcLeaseState{LastCollectedAt: clock.Now().Add(-time.Minute)})
	hooked := newGCHookClient(p.clients[StageOrg])
	a := newGCAllocator(t, layout, singleClient(hooked), clock)

	n, report := a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "cooldown 5m0s")
	assert.Zero(t, hooked.getRepoCalls(stale.Repo), "slots are not probed during the cooldown")

	clock.Set(clock.Now().Add(gcCooldown))
	n, _ = a.collectOrg(ctx, hooked, StageOrg)
	assert.Equal(t, 1, n)
}

// TestSlotGC_SimultaneousCollectorsActOnce runs many suites' collectors on
// one org at once: exactly one acts, and each abandoned repo is deleted
// exactly once.
func TestSlotGC_SimultaneousCollectorsActOnce(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(4)
	p := newFakePool(layout)
	slots := layout.Slots()
	for _, s := range slots {
		seedSlotRepo(p, s, newLeaseDescription(), staleTime(clock))
	}

	const collectors = 12
	results := make([]int, collectors)
	reports := make([]string, collectors)
	var wg sync.WaitGroup
	for i := range collectors {
		a := newGCAllocator(t, layout, p.clientFor, clock)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], reports[i] = a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
		}()
	}
	wg.Wait()

	total, acted := 0, 0
	for i, n := range results {
		total += n
		if n > 0 {
			acted++
			continue
		}
		// A collector that did not act was kept out by the lease or the
		// cooldown, or scanned after the winner freed every slot.
		assert.True(t, strings.Contains(reports[i], "skipped") || reports[i] == "reclaimed 0", reports[i])
	}
	assert.Equal(t, len(slots), total)
	assert.Equal(t, 1, acted, "only one collector acts")
	for _, s := range slots {
		assert.Equal(t, 1, deleteCount(p.fc, s), "%s deleted exactly once", s)
	}
}

// TestSlotGC_ConditionalWriteConflict checks that a collector whose lease
// write races another collector's loses and does nothing.
func TestSlotGC_ConditionalWriteConflict(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	for _, s := range layout.Slots() {
		seedSlotRepo(p, s, newLeaseDescription(), staleTime(clock))
	}
	hooked := newGCHookClient(p.clients[StageOrg])
	a := newGCAllocator(t, layout, singleClient(hooked), clock)
	b := newGCAllocator(t, layout, p.clientFor, clock)

	// b takes the lease after a read it but before a's write lands.
	bLease := make(chan *gcLease, 1)
	hooked.onUpdate = func() {
		l, reason, err := b.acquireGCLease(ctx, p.clients[StageOrg], StageOrg, gcLeaseState{}, "")
		require.NoError(t, err)
		require.NotNil(t, l, reason)
		bLease <- l
	}
	n, report := a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "skipped: lost the collector lease to another collector")
	assert.Empty(t, p.fc.DeletedRepos, "the losing collector deletes nothing")

	l := <-bLease
	assert.Equal(t, l.holder, readGCLeaseFile(t, p.fc, StageOrg).Holder, "the winner's lease is intact")
}

// TestSlotGC_StaleCollectorCannotReleaseOrUseSuccessorLease checks that a
// collector whose lease expired and was taken over can neither act under
// it nor release the successor's lease.
func TestSlotGC_StaleCollectorCannotReleaseOrUseSuccessorLease(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	client := p.clients[StageOrg]
	old := newGCAllocator(t, layout, p.clientFor, clock)
	next := newGCAllocator(t, layout, p.clientFor, clock)

	oldLease, _, err := old.acquireGCLease(ctx, client, StageOrg, gcLeaseState{}, "")
	require.NoError(t, err)
	require.NotNil(t, oldLease)
	require.NoError(t, old.checkGCLease(ctx, client, oldLease))

	// Inside the safety margin the old collector stops acting on its own.
	clock.Set(clock.Now().Add(gcLeaseDuration - gcLeaseSafetyMargin))
	require.ErrorIs(t, old.checkGCLease(ctx, client, oldLease), errGCLeaseLost)

	clock.Set(oldLease.state.ExpiresAt)
	state, sha, err := next.readGCLease(ctx, client, StageOrg)
	require.NoError(t, err)
	require.Empty(t, next.gcLeaseUnavailable(state), "an expired lease may be taken over")
	nextLease, _, err := next.acquireGCLease(ctx, client, StageOrg, state, sha)
	require.NoError(t, err)
	require.NotNil(t, nextLease)

	// Even with its clock wound back, the old collector sees the takeover.
	clock.Set(oldLease.state.AcquiredAt)
	err = old.checkGCLease(ctx, client, oldLease)
	require.ErrorIs(t, err, errGCLeaseLost)
	assert.Contains(t, err.Error(), "rewritten by another collector")

	old.releaseGCLease(ctx, client, oldLease)
	assert.Equal(t, nextLease.holder, readGCLeaseFile(t, p.fc, StageOrg).Holder, "a stale collector cannot release its successor's lease")
	require.NoError(t, next.checkGCLease(ctx, client, nextLease))
}

// TestSlotGC_PausedCollectorDeletesNothing pauses a collector between
// acquiring its lease and deleting. Meanwhile a successor reclaims the slot
// and a suite claims it again. The resumed collector must not delete the
// replacement repo.
func TestSlotGC_PausedCollectorDeletesNothing(t *testing.T) {
	tests := []struct {
		name string
		// pause runs while the old collector is paused, before the
		// successor collects.
		pause func(t *testing.T, p *fakePool, clock *testClock)
		want  string
	}{
		{
			name: "lease expired",
			pause: func(_ *testing.T, _ *fakePool, clock *testClock) {
				clock.Set(clock.Now().Add(gcLeaseDuration + gcCooldown))
			},
			want: "less than 4m0s of the lease remains",
		},
		{
			name: "coordination repo replaced",
			pause: func(t *testing.T, p *fakePool, _ *testClock) {
				require.NoError(t, p.fc.DeleteRepo(context.Background(), StageOrg, slotGCRepo))
			},
			want: "collector lease lost",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newTestClock(time.Now())
			layout := StagePoolLayout(1)
			p := newFakePool(layout)
			stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
			seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))

			hooked := newGCHookClient(p.clients[StageOrg])
			paused := newGCAllocator(t, layout, singleClient(hooked), clock)
			successor := newGCAllocator(t, layout, p.clientFor, clock)
			suite := newTestAllocator(t, layout, p)

			var replacement Slot
			// Read 1 is the initial lease read; read 2 is the check before
			// the delete, where the collector is paused.
			hooked.onGetLease = func(call int) {
				if call != 2 {
					return
				}
				tt.pause(t, p, clock)
				n, report := successor.collectOrg(ctx, p.clients[StageOrg], StageOrg)
				require.Equal(t, 1, n, report)
				var err error
				replacement, _, err = suite.Claim(ctx)
				require.NoError(t, err)
			}

			n, report := paused.collectOrg(ctx, hooked, StageOrg)
			assert.Zero(t, n)
			assert.Contains(t, report, tt.want)
			assert.Equal(t, stale, replacement)
			assert.True(t, p.exists(t, replacement), "the paused collector must not delete the replacement repo")
			assert.Equal(t, 1, deleteCount(p.fc, stale), "only the successor deleted")
		})
	}
}

// TestSlotGC_RepoRecreatedDuringCollectionIsKept deletes and recreates the
// candidate between the scan and the pre-delete identity recheck.
func TestSlotGC_RepoRecreatedDuringCollectionIsKept(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
	seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))

	hooked := newGCHookClient(p.clients[StageOrg])
	a := newGCAllocator(t, layout, singleClient(hooked), clock)
	suite := newTestAllocator(t, layout, p)
	hooked.onGetRepo = func(repo string, call int) error {
		if repo == stale.Repo && call == 2 {
			// Someone else freed the slot and a suite claimed it again.
			require.NoError(t, p.fc.DeleteRepo(ctx, stale.Org, stale.Repo))
			_, _, err := suite.Claim(ctx)
			require.NoError(t, err)
		}
		return nil
	}

	n, report := a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "test-repo-01 (replaced during collection")
	assert.True(t, p.exists(t, stale), "the replacement repo must not be deleted")
	assert.Equal(t, 1, deleteCount(p.fc, stale))
}

// TestSlotGC_RateLimitedLookupFailsClosed checks that a throttled lookup
// stops collection, backs the org off, and that the org is not probed again
// while its window is closed, neither directly nor from the claim path.
func TestSlotGC_RateLimitedLookupFailsClosed(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	for _, s := range layout.Slots() {
		seedSlotRepo(p, s, newLeaseDescription(), staleTime(clock))
	}
	hooked := newGCHookClient(p.clients[StageOrg])
	a := newGCAllocator(t, layout, singleClient(hooked), clock)
	hooked.onGetRepo = func(repo string, _ int) error {
		if repo == "test-repo-01" {
			return &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "API rate limit exceeded", RetryAfter: 10 * time.Minute}
		}
		return nil
	}

	n, report := a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "rate-limited until")
	assert.Contains(t, report, "test-repo-01 (lookup failed)")
	assert.Empty(t, p.fc.DeletedRepos)
	_, blocked := a.orgBlockedUntil(StageOrg)
	assert.True(t, blocked)

	calls := hooked.getRepoCalls("test-repo-01")
	n, report = a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "skipped: rate-limited until")
	assert.Equal(t, calls, hooked.getRepoCalls("test-repo-01"), "a closed rate window is not probed")

	// From the claim path the org is deferred and keeps its count.
	due, deferred := a.dueForGC(map[string]int{StageOrg: 2 * a.gcThreshold()})
	assert.Empty(t, due)
	assert.Contains(t, deferred, StageOrg)
	capErr := &CapacityError{occupiedByOrg: map[string]int{StageOrg: 1}}
	a.collectAfterPass(ctx, nil, capErr)
	assert.Contains(t, capErr.GC[StageOrg], "deferred: rate-limited until")
	assert.Equal(t, calls, hooked.getRepoCalls("test-repo-01"))

	// Once the window ends, the next pass that finds the org occupied
	// collects it: the deferred count was kept.
	clock.Set(clock.Now().Add(11 * time.Minute))
	hooked.onGetRepo = nil
	capErr = &CapacityError{occupiedByOrg: map[string]int{StageOrg: 1}}
	a.collectAfterPass(ctx, map[string]forge.Client{StageOrg: hooked}, capErr)
	assert.Equal(t, 2, capErr.Reclaimed, capErr.GC[StageOrg])
}

// TestSlotGC_AmbiguousFailuresFailClosed checks that failed lookups and
// deletes leave repos occupied with a diagnostic, while a delete that landed
// despite its error counts as reclaimed.
func TestSlotGC_AmbiguousFailuresFailClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("lookup error", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(2)
		p := newFakePool(layout)
		for _, s := range layout.Slots() {
			seedSlotRepo(p, s, newLeaseDescription(), staleTime(clock))
		}
		hooked := newGCHookClient(p.clients[StageOrg])
		hooked.onGetRepo = func(repo string, _ int) error {
			if repo == "test-repo-01" {
				return &gh.APIError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}
			}
			return nil
		}
		a := newGCAllocator(t, layout, singleClient(hooked), clock)
		n, report := a.collectOrg(ctx, hooked, StageOrg)
		assert.Equal(t, 1, n)
		assert.Contains(t, report, "test-repo-01 (lookup failed: github api: 502 bad gateway)")
		assert.True(t, p.exists(t, Slot{Org: StageOrg, Repo: "test-repo-01"}))
	})

	t.Run("recheck error", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(1)
		p := newFakePool(layout)
		stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
		seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
		hooked := newGCHookClient(p.clients[StageOrg])
		hooked.onGetRepo = func(repo string, call int) error {
			if repo == stale.Repo && call == 2 {
				return errors.New("connection reset")
			}
			return nil
		}
		a := newGCAllocator(t, layout, singleClient(hooked), clock)
		n, report := a.collectOrg(ctx, hooked, StageOrg)
		assert.Zero(t, n)
		assert.Contains(t, report, "not deleted: identity recheck failed")
		assert.True(t, p.exists(t, stale))
	})

	t.Run("delete error, repo kept", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(1)
		p := newFakePool(layout)
		stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
		seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
		hooked := newGCHookClient(p.clients[StageOrg])
		hooked.deleteErr = &gh.APIError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}
		a := newGCAllocator(t, layout, singleClient(hooked), clock)
		n, report := a.collectOrg(ctx, hooked, StageOrg)
		assert.Zero(t, n)
		assert.Contains(t, report, "test-repo-01 (delete failed: github api: 502 bad gateway)")
		assert.True(t, p.exists(t, stale))
		assert.Empty(t, readGCLeaseFile(t, p.fc, StageOrg).Holder, "the lease is released after a failed delete")
	})

	t.Run("delete rate-limited", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(2)
		p := newFakePool(layout)
		for _, s := range layout.Slots() {
			seedSlotRepo(p, s, newLeaseDescription(), staleTime(clock))
		}
		hooked := newGCHookClient(p.clients[StageOrg])
		hooked.deleteErr = &gh.APIError{StatusCode: http.StatusForbidden, Message: "You have exceeded a secondary rate limit"}
		a := newGCAllocator(t, layout, singleClient(hooked), clock)
		n, report := a.collectOrg(ctx, hooked, StageOrg)
		assert.Zero(t, n)
		assert.Contains(t, report, "stopped: deleting test-repo-01: rate-limited until")
		assert.Equal(t, 1, strings.Count(report, "delete failed"), "collection stops at the throttled delete")
		_, blocked := a.orgBlockedUntil(StageOrg)
		assert.True(t, blocked)
	})

	t.Run("delete error after landing", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(1)
		p := newFakePool(layout)
		stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
		seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
		p.clients[StageOrg].deleteLostErr = errors.New("connection reset")
		a := newGCAllocator(t, layout, p.clientFor, clock)
		n, report := a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
		assert.Equal(t, 1, n, report)
		assert.False(t, p.exists(t, stale))
	})

	t.Run("unreadable lease", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(1)
		p := newFakePool(layout)
		stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
		seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
		require.NoError(t, p.fc.CreateOrUpdateFile(ctx, StageOrg, slotGCRepo, slotGCLeasePath, "corrupt", []byte("{")))
		a := newGCAllocator(t, layout, p.clientFor, clock)
		n, report := a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
		assert.Zero(t, n)
		assert.Contains(t, report, "left for manual repair")
		assert.True(t, p.exists(t, stale))
	})

	t.Run("lease read rate-limited", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(1)
		p := newFakePool(layout)
		p.fc.GetFileContentErrors = map[string]error{
			StageOrg + "/" + slotGCRepo + "/" + slotGCLeasePath: &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "API rate limit exceeded"},
		}
		a := newGCAllocator(t, layout, p.clientFor, clock)
		n, report := a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
		assert.Zero(t, n)
		assert.Contains(t, report, "stopped: reading collector lease: rate-limited until")
	})

	t.Run("coordination repo create denied", func(t *testing.T) {
		clock := newTestClock(time.Now())
		layout := StagePoolLayout(1)
		p := newFakePool(layout)
		stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
		seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
		p.clients[StageOrg].createErr = &gh.APIError{StatusCode: http.StatusForbidden, Message: "Resource not accessible by integration"}
		a := newGCAllocator(t, layout, p.clientFor, clock)
		n, report := a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
		assert.Zero(t, n)
		assert.Contains(t, report, "stopped: acquiring collector lease failed")
		assert.True(t, p.exists(t, stale))
	})
}

// TestSlotGC_ThrottledLookupAfterFailedDeleteBacksOffOrg checks that a
// non-throttle delete failure followed by a throttled lookup still keeps the
// lookup's Retry-After window, so later claims skip the org.
func TestSlotGC_ThrottledLookupAfterFailedDeleteBacksOffOrg(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
	seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
	hooked := newGCHookClient(p.clients[StageOrg])
	hooked.deleteErr = &gh.APIError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}
	hooked.onGetRepo = func(repo string, call int) error {
		// Calls: scan, identity recheck, then the lookup after the delete.
		if repo == stale.Repo && call == 3 {
			return &gh.APIError{StatusCode: http.StatusForbidden, Message: "secondary rate limit", RetryAfter: 10 * time.Minute}
		}
		return nil
	}
	a := newGCAllocator(t, layout, singleClient(hooked), clock)

	n, report := a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "delete failed: github api: 502 bad gateway")
	assert.Contains(t, report, "rate-limited until")
	until, blocked := a.orgBlockedUntil(StageOrg)
	require.True(t, blocked, "the lookup's rate-limit window must be recorded")
	assert.GreaterOrEqual(t, until.Sub(clock.Now()), 10*time.Minute)

	calls := hooked.getRepoCalls(stale.Repo)
	_, report = a.collectOrg(ctx, hooked, StageOrg)
	assert.Contains(t, report, "skipped: rate-limited until")
	assert.Equal(t, calls, hooked.getRepoCalls(stale.Repo), "a closed rate window is not probed")
}

// TestSlotGC_RepoArchivedDuringCollectionIsKept archives the candidate
// between the scan and the pre-delete identity recheck.
func TestSlotGC_RepoArchivedDuringCollectionIsKept(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
	seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
	hooked := newGCHookClient(p.clients[StageOrg])
	hooked.onGetRepo = func(repo string, call int) error {
		if repo == stale.Repo && call == 2 {
			for i := range p.fc.Repos {
				if p.fc.Repos[i].Name == stale.Repo {
					p.fc.Repos[i].Archived = true
				}
			}
		}
		return nil
	}
	a := newGCAllocator(t, layout, singleClient(hooked), clock)

	n, report := a.collectOrg(ctx, hooked, StageOrg)
	assert.Zero(t, n)
	assert.Contains(t, report, "test-repo-01 (archived during collection; not deleted)")
	assert.True(t, p.exists(t, stale))
	assert.Zero(t, deleteCount(p.fc, stale), "an archived repo is never deleted")
}

// TestSlotGC_IncompleteLeaseRecordIsLeftForRepair checks that lease records
// that decode but are not ones a collector writes stop collection instead
// of counting as an available lease.
func TestSlotGC_IncompleteLeaseRecordIsLeftForRepair(t *testing.T) {
	ctx := context.Background()
	for _, content := range []string{`null`, `{}`, `{"holder":"x"}`} {
		t.Run(content, func(t *testing.T) {
			clock := newTestClock(time.Now())
			layout := StagePoolLayout(1)
			p := newFakePool(layout)
			stale := Slot{Org: StageOrg, Repo: "test-repo-01"}
			seedSlotRepo(p, stale, newLeaseDescription(), staleTime(clock))
			require.NoError(t, p.fc.CreateOrUpdateFile(ctx, StageOrg, slotGCRepo, slotGCLeasePath, "edited", []byte(content)))
			a := newGCAllocator(t, layout, p.clientFor, clock)
			n, report := a.collectOrg(ctx, p.clients[StageOrg], StageOrg)
			assert.Zero(t, n)
			assert.Contains(t, report, "left for manual repair")
			assert.True(t, p.exists(t, stale))
			assert.Zero(t, deleteCount(p.fc, stale))
		})
	}
}

// TestSlotGC_NoCredentials reports an org whose client cannot be resolved.
func TestSlotGC_NoCredentials(t *testing.T) {
	clock := newTestClock(time.Now())
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	p.noCreds[StageOrg] = errors.New("no installation")
	a := newGCAllocator(t, layout, p.clientFor, clock)
	capErr := &CapacityError{occupiedByOrg: map[string]int{StageOrg: a.gcThreshold()}}
	a.collectAfterPass(context.Background(), map[string]forge.Client{}, capErr)
	assert.Contains(t, capErr.GC[StageOrg], "skipped: no credentials: no installation")
	assert.Empty(t, a.gcRunning, "the running mark is cleared")
}

// gcDeleteServer serves one stale slot repo and the collector lease to the
// live client. Every DELETE is rejected with 429 unless okAfterFirst.
// During the client's rate-limit wait, onWait runs.
type gcDeleteServer struct {
	mu       sync.Mutex
	lease    []byte
	created  time.Time
	desc     string
	replaced bool
	archived bool
	deletes  atomic.Int32
	// okAfterFirst makes DELETEs after the first one succeed.
	okAfterFirst bool
	deleted      bool
}

func (s *gcDeleteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	leasePath := "/repos/halfsend/" + slotGCRepo + "/contents/" + slotGCLeasePath
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/"+slotGCRepo:
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 99, "name": slotGCRepo, "full_name": "halfsend/" + slotGCRepo})
	case r.Method == http.MethodGet && r.URL.Path == leasePath:
		if s.lease == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": base64.StdEncoding.EncodeToString(s.lease)})
	case r.Method == http.MethodPut && r.URL.Path == leasePath:
		var body struct {
			Content string `json:"content"`
			SHA     string `json:"sha"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		cur := ""
		if s.lease != nil {
			cur = forge.GitBlobSHA(s.lease)
		}
		if body.SHA != cur {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"does not match"}`))
			return
		}
		s.lease, _ = base64.StdEncoding.DecodeString(body.Content)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
		if s.deleted {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		id, created := 1, s.created
		if s.replaced {
			id, created = 2, time.Now()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": s.desc,
			"created_at": created.UTC().Format(time.RFC3339), "archived": s.archived,
		})
	case r.Method == http.MethodDelete && r.URL.Path == "/repos/halfsend/test-repo-01":
		if s.deletes.Add(1) > 1 && s.okAfterFirst {
			s.deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}
}

// TestSlotGC_LiveClientRateLimitedDeleteRechecks checks that when the live
// client waits out a rate-limited DELETE, the collector rechecks its lease
// and the repository identity before the resend.
func TestSlotGC_LiveClientRateLimitedDeleteRechecks(t *testing.T) {
	tests := []struct {
		name         string
		okAfterFirst bool
		onWait       func(s *gcDeleteServer)
		reclaimed    int
		deletes      int32
		want         string
	}{
		{name: "still the candidate", okAfterFirst: true, reclaimed: 1, deletes: 2, want: "reclaimed 1"},
		{
			name:    "replaced during wait",
			onWait:  func(s *gcDeleteServer) { s.replaced = true },
			deletes: 1, want: "gone, replaced or archived during retry wait; delete not resent",
		},
		{
			name:    "archived during wait",
			onWait:  func(s *gcDeleteServer) { s.archived = true },
			deletes: 1, want: "gone, replaced or archived during retry wait; delete not resent",
		},
		{
			name: "lease taken over during wait",
			onWait: func(s *gcDeleteServer) {
				s.lease = []byte(`{"holder":"successor","expires_at":"2999-01-01T00:00:00Z"}`)
			},
			deletes: 1, want: "collector lease lost during retry wait",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srvState := &gcDeleteServer{created: time.Now().Add(-2 * slotReclaimAge), desc: newLeaseDescription(), okAfterFirst: tt.okAfterFirst}
			srv := httptest.NewServer(srvState)
			defer srv.Close()
			client := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(time.Duration) <-chan time.Time {
				if tt.onWait != nil {
					srvState.mu.Lock()
					tt.onWait(srvState)
					srvState.mu.Unlock()
				}
				ch := make(chan time.Time, 1)
				ch <- time.Now()
				return ch
			})
			a, err := newSlotAllocator(StagePoolLayout(1), singleClient(client), t.Logf)
			require.NoError(t, err)
			a.orgLock = nil

			n, report := a.collectOrg(context.Background(), client, StageOrg)
			assert.Equal(t, tt.reclaimed, n, report)
			assert.Contains(t, report, tt.want)
			assert.Equal(t, tt.deletes, srvState.deletes.Load())
		})
	}
}
