package behaviourtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// --- fakes ---------------------------------------------------------------

// eventLog records teardown events across orgs in order.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (e *eventLog) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, s)
}

func (e *eventLog) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

// fakeOrgPool stands in for the e2etest org locks and quota probe.
type fakeOrgPool struct {
	mu          sync.Mutex
	quotas      map[string]forge.RateLimit // absent: quota unknown
	unavailable map[string]bool            // locked by another run
	held        map[string]bool
	acquired    []string
	released    []string
	double      []string
	acquireErr  error
	onAcquire   func(org string)
	onRelease   func(org string) // called with mu held, after the release
	waits       []bool           // the wait argument of every Acquire call
	log         *eventLog
}

func newFakeOrgPool(log *eventLog) *fakeOrgPool {
	return &fakeOrgPool{
		quotas:      map[string]forge.RateLimit{},
		unavailable: map[string]bool{},
		held:        map[string]bool{},
		log:         log,
	}
}

func fullQuota() forge.RateLimit {
	return forge.RateLimit{Limit: 5000, Remaining: 5000, Reset: time.Now().Add(time.Hour), Resource: "core"}
}

func poolOrgs(n int) []string {
	orgs := make([]string, n)
	for i := range orgs {
		orgs[i] = fmt.Sprintf("halfsend-%02d", i+1)
	}
	return orgs
}

func (p *fakeOrgPool) deps(t *testing.T) orgPoolDeps {
	return orgPoolDeps{
		Acquire: func(ctx context.Context, pool []string, wait bool) (string, string, error) {
			// Like e2etest.AcquireOrg: one pass over the pool, then
			// (when waiting) polling until an org frees up.
			deadline := time.Now().Add(2 * time.Second)
			for {
				p.mu.Lock()
				p.waits = append(p.waits, wait)
				if p.acquireErr != nil {
					p.mu.Unlock()
					return "", "", p.acquireErr
				}
				for _, org := range pool {
					if p.unavailable[org] || p.held[org] {
						continue
					}
					p.held[org] = true
					p.acquired = append(p.acquired, org)
					if p.onAcquire != nil {
						p.onAcquire(org)
					}
					p.mu.Unlock()
					return org, "token-" + org, nil
				}
				p.mu.Unlock()
				if !wait || !time.Now().Before(deadline) {
					return "", "", fmt.Errorf("could not acquire any org from pool after 10m0s (tried %d orgs)", len(pool))
				}
				select {
				case <-time.After(time.Millisecond):
				case <-ctx.Done():
					return "", "", ctx.Err()
				}
			}
		},
		Release: func(org, token string) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.onRelease != nil {
				defer p.onRelease(org)
			}
			if !p.held[org] || token != "token-"+org {
				p.double = append(p.double, "release:"+org)
			}
			delete(p.held, org)
			p.released = append(p.released, org)
			if p.log != nil {
				p.log.add("release:" + org)
			}
		},
		SampleQuota: func(_ context.Context, org, token string) (forge.RateLimit, bool) {
			p.mu.Lock()
			defer p.mu.Unlock()
			rl, ok := p.quotas[org]
			return rl, ok
		},
		Logf: t.Logf,
	}
}

// shardDriver is an install.Driver bound to one org.
type shardDriver struct {
	org      string
	capacity int
	log      *eventLog
	active   *activeCounter

	mu          sync.Mutex
	allocations int
	outstanding map[string]bool
	finalized   int
	next        int
}

func newShardDriver(org string, log *eventLog, active *activeCounter) *shardDriver {
	return &shardDriver{org: org, capacity: install.DefaultPoolSize, log: log, active: active, outstanding: map[string]bool{}}
}

func (d *shardDriver) AllocateRepo(context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	name := fmt.Sprintf("test-repo-%02d", d.next)
	d.outstanding[name] = true
	d.allocations++
	return name, nil
}

func (d *shardDriver) DeallocateRepo(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.outstanding[name] {
		return fmt.Errorf("%s/%s is not leased", d.org, name)
	}
	delete(d.outstanding, name)
	return nil
}

func (d *shardDriver) Finalize(context.Context) error {
	d.mu.Lock()
	d.finalized++
	leaked := len(d.outstanding)
	d.mu.Unlock()
	if d.log != nil {
		d.log.add("finalize:" + d.org)
	}
	if leaked > 0 {
		return fmt.Errorf("%d lease(s) leaked", leaked)
	}
	return nil
}

func (d *shardDriver) Capacity() int { return d.capacity }

// activeCounter tracks the peak number of concurrently active scenarios.
type activeCounter struct {
	mu     sync.Mutex
	active int
	peak   int
}

func (a *activeCounter) inc() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active++
	a.peak = max(a.peak, a.active)
}

func (a *activeCounter) dec() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
}

// shardHarness wires a shardedSuite to fakes and records what ran.
type shardHarness struct {
	pool    *fakeOrgPool
	log     *eventLog
	active  *activeCounter
	mu      sync.Mutex
	drivers map[string]*shardDriver
	setupFn func(ctx context.Context, lease orgLease, st *shardSetup) error
}

func newShardHarness(_ *testing.T, orgs int) *shardHarness {
	log := &eventLog{}
	h := &shardHarness{pool: newFakeOrgPool(log), log: log, active: &activeCounter{}, drivers: map[string]*shardDriver{}}
	for _, org := range poolOrgs(orgs) {
		h.pool.quotas[org] = fullQuota()
	}
	return h
}

func (h *shardHarness) suite(t *testing.T, cfg orgPlanConfig) *shardedSuite {
	setup := h.setupFn
	if setup == nil {
		setup = h.defaultSetup
	}
	return &shardedSuite{Deps: h.pool.deps(t), Config: cfg, Setup: setup}
}

// createDriver registers a driver in st, as the real Setup does as soon
// as the install driver exists.
func (h *shardHarness) createDriver(lease orgLease, st *shardSetup) *shardDriver {
	d := newShardDriver(lease.Org, h.log, h.active)
	h.mu.Lock()
	h.drivers[lease.Org] = d
	h.mu.Unlock()
	st.Driver = d
	st.Quota = func() (forge.RateLimit, bool) { return fullQuota(), true }
	return d
}

func (h *shardHarness) defaultSetup(_ context.Context, lease orgLease, st *shardSetup) error {
	d := h.createDriver(lease, st)
	st.Template = &world.World{
		Config:    env.RunnerConfig{SCM: "gitlab", InstallMode: "per-repo"},
		Driver:    d,
		Org:       lease.Org,
		RepoOwner: lease.Org,
		Token:     lease.Token,
	}
	return nil
}

func defaultPlan(orgs int) orgPlanConfig {
	return orgPlanConfig{
		Pool:      poolOrgs(orgs),
		PerOrgCap: maxProvisioningPerOrg,
		Policy:    quotaPolicy{ScenarioCost: defaultScenarioAPICost, Reserve: defaultOrgQuotaReserve},
	}
}

// assertTornDown checks every acquired org was released exactly once,
// after its driver (when one was created) was finalized exactly once.
func (h *shardHarness) assertTornDown(t *testing.T) {
	t.Helper()
	assert.Empty(t, h.pool.double, "double acquisition or release")
	assert.Empty(t, h.pool.held, "every lock must be released")
	assert.ElementsMatch(t, h.pool.acquired, h.pool.released)
	events := h.log.list()
	for org, d := range h.drivers {
		assert.Equal(t, 1, d.finalized, "%s finalized", org)
		fin, rel := indexOf(events, "finalize:"+org), indexOf(events, "release:"+org)
		assert.True(t, fin >= 0 && rel > fin, "%s must be finalized before its lock is released: %v", org, events)
	}
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// --- matrix --------------------------------------------------------------

// matrixFeatures returns features with n provisioning outline rows, one
// scenario that needs the org but no repo, one scenario compatibility
// tags skip, and one the tag filter drops.
func matrixFeatures(n int, withPlain bool) map[string]string {
	var b strings.Builder
	b.WriteString("Feature: matrix\n")
	if withPlain {
		b.WriteString("  Scenario: org only\n    Given a plain step\n\n")
	}
	b.WriteString("  @skip:gitlab\n  Scenario: skipped by compatibility tag\n    Given the enrolled test repository\n\n")
	b.WriteString("  @wip\n  Scenario: filtered by tag expression\n    Given the enrolled test repository\n\n")
	if n > 0 {
		b.WriteString("  Scenario Outline: provision <row>\n    Given the enrolled test repository\n    Then a plain step\n\n    Examples:\n      | row |\n")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "      | %d |\n", i)
		}
	}
	return map[string]string{"matrix.feature": b.String()}
}

type execution struct {
	label  string
	org    string
	token  string
	driver *shardDriver
}

// runMatrix sizes and runs a generated suite through shardedSuite and
// a real godog run whose Before hook uses the assigner's router.
func runMatrix(t *testing.T, h *shardHarness, cfg orgPlanConfig, n int, withPlain bool, concurrency int) (suiteDemand, []execution, *shardAssigner, error) {
	t.Helper()
	dir := writeFeatures(t, matrixFeatures(n, withPlain))
	const tagFilter = "~@wip"
	base := &world.World{Config: env.RunnerConfig{SCM: "gitlab", InstallMode: "per-repo"}}
	instances, err := loadScenarioInstances([]string{dir}, tagFilter)
	require.NoError(t, err)
	demand := summarizeDemand(instances, func(tags []string) error { return suite.SkipErrorForTagNames(tags, base) })

	var mu sync.Mutex
	var runs []execution
	var gotAssigner *shardAssigner
	err = h.suite(t, cfg).Run(context.Background(), demand, func(a *shardAssigner) error {
		gotAssigner = a
		router := a.Router()
		st := godog.TestSuite{
			Name: "matrix",
			ScenarioInitializer: func(sc *godog.ScenarioContext) {
				sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
					if err := suite.SkipErrorForTagNames(tagNamesOf(scenario), base); err != nil {
						return ctx, err
					}
					template, err := router(scenario)
					if err != nil {
						return ctx, err
					}
					h.active.inc()
					w := template.Clone()
					w.ScenarioName = scenario.Uri + ": " + scenario.Name
					mu.Lock()
					runs = append(runs, execution{label: w.ScenarioName, org: w.Org, token: w.Token, driver: w.Driver.(*shardDriver)})
					mu.Unlock()
					return world.WithWorld(ctx, w), nil
				})
				sc.Step(`^the enrolled test repository$`, func(ctx context.Context) error {
					w := world.FromContext(ctx)
					name, err := w.Driver.AllocateRepo(ctx)
					if err != nil {
						return err
					}
					w.LeasedRepoName = name
					w.RepoName = name
					w.RepoFull = w.Org + "/" + name
					time.Sleep(time.Millisecond) // let scenarios overlap
					return nil
				})
				sc.Step(`^a plain step$`, func() error { return nil })
				sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
					w := world.FromContext(ctx)
					if w == nil {
						return ctx, err
					}
					defer h.active.dec()
					if w.LeasedRepoName != "" {
						return ctx, errors.Join(err, w.Driver.DeallocateRepo(ctx, w.LeasedRepoName))
					}
					return ctx, err
				})
			},
			Options: &godog.Options{
				Format:      "progress",
				Output:      io.Discard,
				Paths:       []string{dir},
				Tags:        tagFilter,
				Concurrency: concurrency,
				Strict:      true,
			},
		}
		if status := st.Run(); status != 0 {
			return fmt.Errorf("godog status %d", status)
		}
		return nil
	})
	return demand, runs, gotAssigner, err
}

func tagNamesOf(sc *godog.Scenario) []string {
	names := make([]string, 0, len(sc.Tags))
	for _, tag := range sc.Tags {
		names = append(names, tag.Name)
	}
	return names
}

func TestShardedSuite_ProvisioningMatrix(t *testing.T) {
	for _, tc := range []struct {
		provisioning int
		wantOrgs     int
	}{
		{0, 1}, // the org-only scenario still needs an org
		{1, 1},
		{20, 1},
		{21, 2},
		{49, 3},
		{60, 3},
		{61, 4},
	} {
		t.Run(fmt.Sprintf("%d_provisioning", tc.provisioning), func(t *testing.T) {
			h := newShardHarness(t, 12)
			demand, runs, assigner, err := runMatrix(t, h, defaultPlan(12), tc.provisioning, true, 12)
			require.NoError(t, err)

			assert.Equal(t, suiteDemand{Selected: tc.provisioning + 2, Skipped: 1, Eligible: tc.provisioning + 1, Provisioning: tc.provisioning}, demand)
			assert.Len(t, h.pool.acquired, tc.wantOrgs, "distinct orgs")

			// Exact-once execution of every eligible scenario.
			seen := map[string]int{}
			for _, r := range runs {
				seen[r.label]++
			}
			assert.Len(t, runs, demand.Eligible)
			for label, count := range seen {
				assert.Equal(t, 1, count, "scenario %q ran %d times", label, count)
			}

			// Per-org cap and isolation: each scenario ran with its own org's
			// token and driver.
			total := 0
			for org, d := range h.drivers {
				assert.LessOrEqual(t, d.allocations, maxProvisioningPerOrg, "%s provisioning", org)
				total += d.allocations
			}
			assert.Equal(t, tc.provisioning, total, "every provisioning scenario leased exactly one repo")
			for _, r := range runs {
				assert.Equal(t, r.org, r.driver.org, "scenario %q driver org", r.label)
				assert.Equal(t, "token-"+r.org, r.token, "scenario %q token", r.label)
			}
			for _, s := range assigner.shards {
				assert.LessOrEqual(t, s.provisioning, maxProvisioningPerOrg)
			}
			assert.LessOrEqual(t, h.active.peak, 12, "aggregate concurrency")
			h.assertTornDown(t)
		})
	}
}

func TestShardedSuite_AggregateConcurrencyIsNotMultipliedByOrgs(t *testing.T) {
	h := newShardHarness(t, 12)
	_, _, _, err := runMatrix(t, h, defaultPlan(12), 49, true, 4)
	require.NoError(t, err)
	assert.Len(t, h.pool.acquired, 3)
	assert.LessOrEqual(t, h.active.peak, 4, "three orgs must still share the suite's concurrency")
}

func TestShardedSuite_NothingEligibleAcquiresNoOrg(t *testing.T) {
	h := newShardHarness(t, 12)
	demand, runs, assigner, err := runMatrix(t, h, defaultPlan(12), 0, false, 12)
	require.NoError(t, err)
	assert.Equal(t, 0, demand.Eligible)
	assert.Empty(t, h.pool.acquired)
	assert.Empty(t, runs)
	_, err = assigner.Assign("late", false)
	assert.Error(t, err)
}

func TestShardedSuite_STAGESingleOrgKeepsEveryScenario(t *testing.T) {
	h := newShardHarness(t, 1)
	h.pool.quotas["halfsend-01"] = forge.RateLimit{Limit: 5000, Remaining: 3000, Reset: time.Now().Add(time.Hour)}
	cfg := defaultPlan(1)
	cfg.SingleOrg = true
	var logs []string
	var mu sync.Mutex
	h.pool.onAcquire = func(string) {}
	s := h.suite(t, cfg)
	s.Deps.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	demand := suiteDemand{Selected: 49, Eligible: 49, Provisioning: 49}
	var shards int
	err := s.Run(context.Background(), demand, func(a *shardAssigner) error {
		shards = len(a.shards)
		for i := range 49 {
			if _, err := a.Assign(fmt.Sprintf("s%d", i), true); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, shards)
	assert.Contains(t, strings.Join(logs, "\n"), "WARNING: single-org environment")
	h.assertTornDown(t)
}

// --- quota-aware acquisition ---------------------------------------------

func TestAcquireOrgsForDemand_PassesOverLowQuotaOrg(t *testing.T) {
	h := newShardHarness(t, 3)
	h.pool.quotas["halfsend-01"] = forge.RateLimit{Limit: 5000, Remaining: 600, Reset: time.Now().Add(time.Hour)}

	leases, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), defaultPlan(3), suiteDemand{Eligible: 21, Provisioning: 21})
	require.NoError(t, err)
	require.Len(t, leases, 2)
	assert.Equal(t, "halfsend-02", leases[0].Org)
	assert.Equal(t, "halfsend-03", leases[1].Org)
	assert.Equal(t, []string{"halfsend-01"}, h.pool.released, "the passed-over org is released at once")
	assert.Empty(t, h.pool.double)
}

func TestAcquireOrgsForDemand_PartlyDepletedOrgTakesLessWork(t *testing.T) {
	h := newShardHarness(t, 3)
	// (2500 - 500) / 200 = 10 provisioning scenarios.
	h.pool.quotas["halfsend-01"] = forge.RateLimit{Limit: 5000, Remaining: 2500, Reset: time.Now().Add(time.Hour)}

	err := h.suite(t, defaultPlan(3)).Run(context.Background(), suiteDemand{Eligible: 21, Provisioning: 21}, func(a *shardAssigner) error {
		require.Len(t, a.shards, 2)
		assert.Equal(t, 10, a.shards[0].Capacity)
		assert.Equal(t, 20, a.shards[1].Capacity)
		for i := range 21 {
			if _, err := a.Assign(fmt.Sprintf("s%d", i), true); err != nil {
				return err
			}
		}
		assert.LessOrEqual(t, a.shards[0].provisioning, 10)
		assert.Equal(t, 21, a.shards[0].provisioning+a.shards[1].provisioning)
		// Fill the remaining planned capacity (10 + 20 = 30).
		for i := 21; i < 30; i++ {
			if _, err := a.Assign(fmt.Sprintf("s%d", i), true); err != nil {
				return err
			}
		}
		assert.Equal(t, 10, a.shards[0].provisioning)
		assert.Equal(t, 20, a.shards[1].provisioning)
		_, err := a.Assign("one too many", true)
		assert.Error(t, err, "past the planned capacity nothing is assigned silently")
		return nil
	})
	require.NoError(t, err)
	h.assertTornDown(t)
}

func TestAcquireOrgsForDemand_SkipsOrgsLockedByOtherRuns(t *testing.T) {
	h := newShardHarness(t, 4)
	h.pool.unavailable["halfsend-01"] = true
	h.pool.unavailable["halfsend-03"] = true
	leases, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), defaultPlan(4), suiteDemand{Eligible: 40, Provisioning: 40})
	require.NoError(t, err)
	assert.Equal(t, []string{"halfsend-02", "halfsend-04"}, []string{leases[0].Org, leases[1].Org})
	assert.Empty(t, h.pool.double)
}

func TestAcquireOrgsForDemand_InsufficientCapacityFailsEarly(t *testing.T) {
	h := newShardHarness(t, 3)
	h.pool.quotas["halfsend-03"] = forge.RateLimit{Limit: 5000, Remaining: 100, Reset: time.Now().Add(time.Hour)}

	_, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), defaultPlan(3), suiteDemand{Eligible: 61, Provisioning: 61})
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "insufficient pool-org capacity")
	assert.Contains(t, msg, "61 of them provisioning")
	assert.Contains(t, msg, "max 20 per org")
	assert.Contains(t, msg, "halfsend-03 (remaining=100/5000")
	assert.Contains(t, msg, "no pool org left to try")
	assert.Empty(t, h.pool.held, "every lock taken is released")
	assert.ElementsMatch(t, h.pool.acquired, h.pool.released)
}

func TestAcquireOrgsForDemand_AcquireErrorReleasesHeldOrgs(t *testing.T) {
	h := newShardHarness(t, 12)
	h.pool.onAcquire = func(org string) {
		if org == "halfsend-02" {
			h.pool.acquireErr = errors.New("could not acquire any org from pool after 10m0s")
		}
	}
	_, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), defaultPlan(12), suiteDemand{Eligible: 50, Provisioning: 50})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 10m0s")
	assert.Contains(t, err.Error(), "acquired orgs cover 40")
	assert.Empty(t, h.pool.held)
}

func TestAcquireOrgsForDemand_NeverBlocksWhileHoldingPartialReservation(t *testing.T) {
	h := newShardHarness(t, 2)
	// Another run holds halfsend-02 until this run releases halfsend-01.
	h.pool.unavailable["halfsend-02"] = true
	h.pool.onRelease = func(org string) {
		if org == "halfsend-01" {
			delete(h.pool.unavailable, "halfsend-02")
		}
	}
	plan := defaultPlan(2)
	plan.ReserveTimeout = time.Minute
	plan.RetryBackoff = time.Millisecond

	leases, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), plan, suiteDemand{Eligible: 40, Provisioning: 40})
	require.NoError(t, err)
	require.Len(t, leases, 2)
	assert.Equal(t, []string{"halfsend-01", "halfsend-01", "halfsend-02"}, h.pool.acquired,
		"the incomplete first reservation is released before retrying")
	assert.Equal(t, []string{"halfsend-01"}, h.pool.released)
	// Only an acquisition made while holding nothing may wait.
	assert.Equal(t, []bool{true, false, true, false}, h.pool.waits)
}

func TestAcquireOrgsForDemand_GivesUpAfterReserveTimeout(t *testing.T) {
	h := newShardHarness(t, 2)
	h.pool.unavailable["halfsend-02"] = true
	plan := defaultPlan(2)
	plan.ReserveTimeout = 30 * time.Millisecond
	plan.RetryBackoff = time.Millisecond

	_, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), plan, suiteDemand{Eligible: 40, Provisioning: 40})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient pool-org capacity")
	assert.Empty(t, h.pool.held, "nothing is left locked")
	assert.ElementsMatch(t, h.pool.acquired, h.pool.released)
}

func TestAcquireOrgsForDemand_ReserveTimeoutBoundsBlockingAcquire(t *testing.T) {
	h := newShardHarness(t, 2)
	// halfsend-02 is held by another run. When the incomplete first
	// reservation is released, halfsend-01 is taken too, so the retry's
	// blocking Acquire would wait (the fake polls for up to 2s).
	h.pool.unavailable["halfsend-02"] = true
	h.pool.onRelease = func(org string) {
		if org == "halfsend-01" {
			h.pool.unavailable["halfsend-01"] = true
		}
	}
	plan := defaultPlan(2)
	plan.ReserveTimeout = 50 * time.Millisecond
	plan.RetryBackoff = time.Millisecond

	start := time.Now()
	_, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), plan, suiteDemand{Eligible: 40, Provisioning: 40})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second, "the blocking Acquire must stop at ReserveTimeout")
	assert.Empty(t, h.pool.held, "nothing is left locked")
	assert.ElementsMatch(t, h.pool.acquired, h.pool.released)
}

func TestAcquireOrgsForDemand_CompetingPartialReservationsDoNotDeadlock(t *testing.T) {
	h := newShardHarness(t, 2)
	plan := defaultPlan(2)
	plan.ReserveTimeout = 10 * time.Second
	plan.RetryBackoff = 2 * time.Millisecond
	deps := h.pool.deps(t)

	// Two runs each need both orgs of a two-org pool, so each could
	// grab one and wait forever on the other.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leases, err := acquireOrgsForDemand(context.Background(), deps, plan, suiteDemand{Eligible: 40, Provisioning: 40})
			errs[i] = err
			for _, l := range leases {
				deps.Release(l.Org, l.Token)
			}
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Empty(t, h.pool.held)
}

func TestAcquireOrgsForDemand_Cancelled(t *testing.T) {
	h := newShardHarness(t, 12)
	ctx, cancel := context.WithCancel(context.Background())
	h.pool.onAcquire = func(string) { cancel() }
	_, err := acquireOrgsForDemand(ctx, h.pool.deps(t), defaultPlan(12), suiteDemand{Eligible: 30, Provisioning: 30})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"halfsend-01"}, h.pool.acquired)
	assert.Empty(t, h.pool.held)
}

func TestAcquireOrgsForDemand_NonProvisioningOnlyNeedsReserve(t *testing.T) {
	h := newShardHarness(t, 2)
	h.pool.quotas["halfsend-01"] = forge.RateLimit{Limit: 5000, Remaining: 100, Reset: time.Now().Add(time.Hour)}
	leases, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), defaultPlan(2), suiteDemand{Eligible: 3})
	require.NoError(t, err)
	require.Len(t, leases, 1)
	assert.Equal(t, "halfsend-02", leases[0].Org)
}

func TestAcquireOrgsForDemand_UnknownQuotaUsesCap(t *testing.T) {
	h := newShardHarness(t, 2)
	delete(h.pool.quotas, "halfsend-01")
	leases, err := acquireOrgsForDemand(context.Background(), h.pool.deps(t), defaultPlan(2), suiteDemand{Eligible: 20, Provisioning: 20})
	require.NoError(t, err)
	require.Len(t, leases, 1)
	assert.Equal(t, 20, leases[0].Capacity)
	assert.Equal(t, "quota unknown", leases[0].quotaString())
}

func TestQuotaPolicy(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p := quotaPolicy{ScenarioCost: 200, Reserve: 500, Now: func() time.Time { return now }}

	assert.Equal(t, 20, p.capacity(forge.RateLimit{Limit: 5000, Remaining: 5000, Reset: now.Add(time.Hour)}, true, 20))
	assert.Equal(t, 20, p.capacity(forge.RateLimit{Limit: 12500, Remaining: 12500}, true, 20), "capped")
	assert.Equal(t, 10, p.capacity(forge.RateLimit{Limit: 5000, Remaining: 2500, Reset: now.Add(time.Hour)}, true, 20))
	assert.Equal(t, 0, p.capacity(forge.RateLimit{Limit: 5000, Remaining: 300, Reset: now.Add(time.Hour)}, true, 20))
	assert.Equal(t, 0, p.capacity(forge.RateLimit{Limit: 5000, Remaining: 10, Reset: now.Add(2 * time.Minute)}, true, 20),
		"a reset that has not happened yet is not counted: setup would run into 403s first")
	assert.Equal(t, 20, p.capacity(forge.RateLimit{Limit: 5000, Remaining: 10, Reset: now.Add(-time.Second)}, true, 20),
		"a budget whose reset time has passed counts at its full limit")
	assert.Equal(t, 20, p.capacity(forge.RateLimit{}, false, 20))
	assert.True(t, p.coversReserve(forge.RateLimit{Remaining: 500}, true))
	assert.False(t, p.coversReserve(forge.RateLimit{Remaining: 499, Reset: now.Add(time.Hour)}, true))
	assert.True(t, p.coversReserve(forge.RateLimit{}, false))

	zero := quotaPolicy{}
	assert.Equal(t, 3, zero.capacity(forge.RateLimit{Remaining: 3}, true, 20), "zero cost counts as 1 per scenario")
}

func TestQuotaPolicyFromEnv(t *testing.T) {
	t.Setenv("BEHAVIOUR_SCENARIO_API_COST", "")
	t.Setenv("BEHAVIOUR_ORG_QUOTA_RESERVE", "")
	p, err := quotaPolicyFromEnv()
	require.NoError(t, err)
	assert.Equal(t, defaultScenarioAPICost, p.ScenarioCost)
	assert.Equal(t, defaultOrgQuotaReserve, p.Reserve)

	t.Setenv("BEHAVIOUR_SCENARIO_API_COST", "250")
	t.Setenv("BEHAVIOUR_ORG_QUOTA_RESERVE", "0")
	p, err = quotaPolicyFromEnv()
	require.NoError(t, err)
	assert.Equal(t, 250, p.ScenarioCost)
	assert.Equal(t, 0, p.Reserve)

	t.Setenv("BEHAVIOUR_SCENARIO_API_COST", "0")
	_, err = quotaPolicyFromEnv()
	assert.ErrorContains(t, err, "BEHAVIOUR_SCENARIO_API_COST")

	t.Setenv("BEHAVIOUR_SCENARIO_API_COST", "200")
	t.Setenv("BEHAVIOUR_ORG_QUOTA_RESERVE", "-1")
	_, err = quotaPolicyFromEnv()
	assert.ErrorContains(t, err, "BEHAVIOUR_ORG_QUOTA_RESERVE")
}

// --- teardown --------------------------------------------------------------

func TestShardedSuite_TeardownOnScenarioFailure(t *testing.T) {
	h := newShardHarness(t, 12)
	err := h.suite(t, defaultPlan(12)).Run(context.Background(), suiteDemand{Eligible: 41, Provisioning: 41}, func(*shardAssigner) error {
		return errors.New("behaviour suite failed with status 1")
	})
	require.ErrorContains(t, err, "status 1")
	assert.Len(t, h.pool.acquired, 3)
	h.assertTornDown(t)
}

func TestShardedSuite_TeardownOnPanic(t *testing.T) {
	h := newShardHarness(t, 12)
	assert.Panics(t, func() {
		_ = h.suite(t, defaultPlan(12)).Run(context.Background(), suiteDemand{Eligible: 21, Provisioning: 21}, func(*shardAssigner) error {
			panic("boom")
		})
	})
	h.assertTornDown(t)
}

func TestShardedSuite_TeardownOnSetupFailure(t *testing.T) {
	h := newShardHarness(t, 12)
	h.setupFn = func(ctx context.Context, lease orgLease, st *shardSetup) error {
		switch lease.Org {
		case "halfsend-02":
			// Driver created, then a later setup step failed.
			_ = h.defaultSetup(ctx, lease, st)
			return errors.New("unsupported BEHAVIOUR_CI")
		case "halfsend-03":
			return errors.New("deploying mint")
		case "halfsend-04":
			panic("setup panic")
		case "halfsend-05":
			// Driver created, then a later constructor panicked.
			h.createDriver(lease, st)
			panic("constructor panic")
		}
		return h.defaultSetup(ctx, lease, st)
	}
	called := false
	err := h.suite(t, defaultPlan(12)).Run(context.Background(), suiteDemand{Eligible: 90, Provisioning: 90}, func(*shardAssigner) error {
		called = true
		return nil
	})
	require.Error(t, err)
	assert.False(t, called, "no scenario runs when an org fails to set up")
	assert.Contains(t, err.Error(), `setting up org "halfsend-02": unsupported BEHAVIOUR_CI`)
	assert.Contains(t, err.Error(), `setting up org "halfsend-03": deploying mint`)
	assert.Contains(t, err.Error(), `setting up org "halfsend-04": panic: setup panic`)
	assert.Contains(t, err.Error(), `setting up org "halfsend-05": panic: constructor panic`)
	assert.Len(t, h.pool.acquired, 5)
	assert.Contains(t, h.drivers, "halfsend-02", "the partly set up org's driver is finalized")
	assert.Contains(t, h.drivers, "halfsend-05", "a driver created before a setup panic is finalized")
	h.assertTornDown(t)
}

func TestShardedSuite_TeardownOnCancelledSetup(t *testing.T) {
	h := newShardHarness(t, 12)
	ctx, cancel := context.WithCancel(context.Background())
	h.setupFn = func(ctx context.Context, lease orgLease, st *shardSetup) error {
		_ = h.defaultSetup(ctx, lease, st)
		cancel()
		return ctx.Err()
	}
	err := h.suite(t, defaultPlan(12)).Run(ctx, suiteDemand{Eligible: 25, Provisioning: 25}, func(*shardAssigner) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	h.assertTornDown(t)
}

func TestShardedSuite_FinalizeErrorStillReleases(t *testing.T) {
	h := newShardHarness(t, 12)
	err := h.suite(t, defaultPlan(12)).Run(context.Background(), suiteDemand{Eligible: 1, Provisioning: 1}, func(a *shardAssigner) error {
		// Leak a lease so Finalize reports an error.
		_, err := a.shards[0].Template.Driver.AllocateRepo(context.Background())
		return err
	})
	require.NoError(t, err)
	h.assertTornDown(t)
}

// --- assigner --------------------------------------------------------------

func TestShardAssigner_BalancesByCapacityShare(t *testing.T) {
	a := newShardAssigner([]*orgShard{{Org: "a", Capacity: 10}, {Org: "b", Capacity: 20}}, t.Logf)
	for i := range 15 {
		_, err := a.Assign(fmt.Sprintf("p%d", i), true)
		require.NoError(t, err)
	}
	assert.Equal(t, 5, a.shards[0].provisioning)
	assert.Equal(t, 10, a.shards[1].provisioning)

	// Non-provisioning work goes to the org with the fewest scenarios:
	// a (5) until it catches up with b (10).
	for i := range 7 {
		_, err := a.Assign(fmt.Sprintf("o%d", i), false)
		require.NoError(t, err)
	}
	assert.Equal(t, 6, a.shards[0].other)
	assert.Equal(t, 1, a.shards[1].other)
	assert.Equal(t, "a: provisioning 5/10, other 6; b: provisioning 10/20, other 1", a.Summary())
	assert.Equal(t, []string{"o0", "o1", "o2", "o3", "o4", "o5", "p0", "p12", "p3", "p6", "p9"}, a.Scenarios()["a"])
}

func TestShardAssigner_RouterAndCapacity(t *testing.T) {
	d := newShardDriver("a", nil, nil)
	d.capacity = 8
	d2 := newShardDriver("c", nil, nil)
	d2.capacity = 12
	a := newShardAssigner([]*orgShard{
		{Org: "a", Capacity: 1, Template: &world.World{Org: "a", Driver: d}},
		{Org: "b", Capacity: 1},
		{Org: "c", Capacity: 1, Template: &world.World{Org: "c", Driver: d2}},
	}, t.Logf)
	// Orgs without a template do not count; the rest add up.
	assert.Equal(t, 20, a.driverCapacity())

	router := a.Router()
	w, err := router(&godog.Scenario{Uri: "x.feature", Name: "one"})
	require.NoError(t, err)
	assert.Equal(t, "a", w.Org)
	_, err = router(&godog.Scenario{Uri: "x.feature", Name: "two"})
	assert.ErrorContains(t, err, `org "b" has no template World`)
	assert.Equal(t, 0, newShardAssigner(nil, t.Logf).driverCapacity())
}

func TestOrgLogf(t *testing.T) {
	var got string
	logf := orgLogf(func(format string, args ...any) { got = fmt.Sprintf(format, args...) }, "halfsend-07")
	logf("[driver] allocated %s/%s (%d%%)", "halfsend-07", "test-repo-01", 50)
	assert.Equal(t, "[halfsend-07] [driver] allocated halfsend-07/test-repo-01 (50%)", got)
}
