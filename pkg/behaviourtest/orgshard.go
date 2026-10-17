package behaviourtest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const (
	// maxProvisioningPerOrg caps the provisioning scenario instances
	// (fresh repo + install) a DEV run assigns to one pool org. GitHub's
	// primary REST budget is per app installation, i.e. per org, so the
	// suite spreads provisioning work over as many orgs as this cap
	// requires instead of draining one org's budget.
	maxProvisioningPerOrg = 20

	// defaultScenarioAPICost is the estimated primary REST budget one
	// provisioning scenario instance consumes (fresh install, workflow
	// polling, validation, cleanup). Override with
	// BEHAVIOUR_SCENARIO_API_COST.
	defaultScenarioAPICost = 200

	// defaultOrgQuotaReserve is the primary REST budget an org must keep
	// on top of its scenarios' cost for suite-scoped work (stale-resource
	// cleanup, mint deploy, finalize, non-provisioning scenarios).
	// Override with BEHAVIOUR_ORG_QUOTA_RESERVE.
	defaultOrgQuotaReserve = 500

	// reserveRetryBackoff is the mean pause before a run that could not
	// complete its multi-org reservation retries it from scratch.
	reserveRetryBackoff = 15 * time.Second
)

// quotaPolicy turns an org's observed primary REST budget into the
// number of provisioning scenario instances the org may take.
type quotaPolicy struct {
	ScenarioCost int
	Reserve      int
	Now          func() time.Time
}

// quotaPolicyFromEnv returns the default policy, with the cost and
// reserve overridable by BEHAVIOUR_SCENARIO_API_COST and
// BEHAVIOUR_ORG_QUOTA_RESERVE.
func quotaPolicyFromEnv() (quotaPolicy, error) {
	p := quotaPolicy{
		ScenarioCost: defaultScenarioAPICost,
		Reserve:      defaultOrgQuotaReserve,
		Now:          time.Now,
	}
	if v := os.Getenv("BEHAVIOUR_SCENARIO_API_COST"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return p, fmt.Errorf("BEHAVIOUR_SCENARIO_API_COST must be a positive integer, got %q", v)
		}
		p.ScenarioCost = n
	}
	if v := os.Getenv("BEHAVIOUR_ORG_QUOTA_RESERVE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, fmt.Errorf("BEHAVIOUR_ORG_QUOTA_RESERVE must be a non-negative integer, got %q", v)
		}
		p.Reserve = n
	}
	return p, nil
}

func (p quotaPolicy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// budget is the primary REST budget the policy counts on: the observed
// remaining requests, or the full limit once the budget's reset time
// has passed. A reset still in the future is not counted, since
// quota-consuming setup starts before it lands.
func (p quotaPolicy) budget(rl forge.RateLimit) int {
	b := rl.Remaining
	if !rl.Reset.IsZero() && rl.Limit > b && !rl.Reset.After(p.now()) {
		b = rl.Limit
	}
	return b
}

// capacity returns how many provisioning scenario instances fit in the
// observed budget after the reserve, capped at perOrgCap. An unknown
// budget (the credential reports no rate-limit headers) is not limited.
func (p quotaPolicy) capacity(rl forge.RateLimit, known bool, perOrgCap int) int {
	if !known {
		return perOrgCap
	}
	cost := max(p.ScenarioCost, 1)
	c := (p.budget(rl) - p.Reserve) / cost
	return min(max(c, 0), perOrgCap)
}

// coversReserve reports whether the budget covers the reserve alone,
// for an org that only hosts non-provisioning scenarios.
func (p quotaPolicy) coversReserve(rl forge.RateLimit, known bool) bool {
	return !known || p.budget(rl) >= p.Reserve
}

// orgLease is one acquired, locked pool org.
type orgLease struct {
	Org        string
	Token      string
	Quota      forge.RateLimit
	QuotaKnown bool
	// Capacity is the provisioning scenario instances the org may take.
	Capacity int
}

func (l orgLease) quotaString() string {
	if !l.QuotaKnown {
		return "quota unknown"
	}
	return l.Quota.String()
}

// orgPoolDeps are the side effects org acquisition needs; RunSuite wires
// them to e2etest, tests to fakes.
type orgPoolDeps struct {
	// Acquire locks one org from pool and returns it with its token.
	// With wait it blocks until an org frees up or the lock timeout
	// expires; without, it makes one pass over the pool and fails when
	// every org is locked.
	Acquire func(ctx context.Context, pool []string, wait bool) (org, token string, err error)
	// Release unlocks an org Acquire returned.
	Release func(org, token string)
	// SampleQuota reports the org token's primary REST budget.
	SampleQuota func(ctx context.Context, org, token string) (forge.RateLimit, bool)
	Logf        func(string, ...any)
}

// orgPlanConfig configures acquireOrgsForDemand.
type orgPlanConfig struct {
	Pool []string
	// SingleOrg runs every scenario in one org with no per-org cap
	// (STAGE, whose pool is one org). Its budget is checked and a
	// shortfall reported, but scenarios are never dropped.
	SingleOrg bool
	// PerOrgCap is the provisioning cap per org in multi-org mode.
	PerOrgCap int
	Policy    quotaPolicy
	// ReserveTimeout bounds the whole of acquireOrgsForDemand: every
	// Acquire and quota sample runs under it, and a reservation that
	// could not be completed is released and retried only while time
	// remains. Zero means a single attempt with no deadline of its own.
	ReserveTimeout time.Duration
	// RetryBackoff is the mean pause, jittered, before such a retry.
	RetryBackoff time.Duration
}

// acquireOrgsForDemand locks pool orgs until their capacity covers the
// run's provisioning scenario instances (at least one org when any
// scenario will run). An org whose budget leaves room for no
// provisioning scenario is released and passed over. Only the first org
// is waited for. While holding an incomplete reservation it never
// blocks on another org: if none is free it releases everything it
// holds and retries after a jittered pause, so concurrent runs cannot
// each hold part of the pool and wait on each other. When the pool
// cannot cover the run it releases every org it locked and returns an
// error naming the shortfall, each org's budget and the acquire error.
func acquireOrgsForDemand(ctx context.Context, deps orgPoolDeps, cfg orgPlanConfig, demand suiteDemand) ([]orgLease, error) {
	if demand.Eligible == 0 {
		deps.Logf("[org-shard] no scenario will run (%d selected, %d skipped by tags); not acquiring a pool org", demand.Selected, demand.Skipped)
		return nil, nil
	}
	var passedOver []string
	lowQuota := make(map[string]bool)
	// ReserveTimeout bounds the whole acquisition, including a blocking
	// Acquire inside an attempt, not only the retry decision. A zero
	// timeout means one attempt with no deadline of its own.
	deadline := time.Now().Add(cfg.ReserveTimeout)
	if cfg.ReserveTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	for {
		leases, retryable, err := reserveOrgs(ctx, deps, cfg, demand, lowQuota, &passedOver)
		if err == nil {
			return leases, nil
		}
		remaining := time.Until(deadline)
		if !retryable || remaining <= 0 {
			return nil, err
		}
		pause := cfg.RetryBackoff
		if pause > 0 {
			pause = pause/2 + rand.N(pause)
		}
		pause = min(pause, remaining)
		deps.Logf("[org-shard] could not complete the reservation (%v); released the orgs held, retrying in %s", err, pause)
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return nil, capacityError(cfg, demand, nil, passedOver, ctx.Err())
		}
	}
}

// reserveOrgs makes one attempt at a complete reservation. It releases
// every org it locked when it fails. retryable reports a failure to
// acquire a further org while already holding some, which a later
// attempt may avoid. lowQuota carries the orgs passed over for low
// budget between attempts.
func reserveOrgs(ctx context.Context, deps orgPoolDeps, cfg orgPlanConfig, demand suiteDemand, lowQuota map[string]bool, passedOver *[]string) (_ []orgLease, retryable bool, retErr error) {
	var leases []orgLease
	defer func() {
		if retErr != nil {
			for _, l := range leases {
				deps.Release(l.Org, l.Token)
			}
		}
	}()

	excluded := maps.Clone(lowQuota)
	covered := 0
	need := demand.Provisioning
	for len(leases) == 0 || covered < need {
		if err := ctx.Err(); err != nil {
			return nil, false, capacityError(cfg, demand, leases, *passedOver, err)
		}
		var candidates []string
		for _, org := range cfg.Pool {
			if !excluded[org] {
				candidates = append(candidates, org)
			}
		}
		if len(candidates) == 0 {
			return nil, false, capacityError(cfg, demand, leases, *passedOver, errors.New("no pool org left to try"))
		}
		org, token, err := deps.Acquire(ctx, candidates, len(leases) == 0)
		if err != nil {
			return nil, len(leases) > 0 && ctx.Err() == nil, capacityError(cfg, demand, leases, *passedOver, err)
		}
		excluded[org] = true
		rl, known := deps.SampleQuota(ctx, org, token)
		lease := orgLease{Org: org, Token: token, Quota: rl, QuotaKnown: known}
		remaining := need - covered

		if cfg.SingleOrg {
			lease.Capacity = need
			if known {
				if est := cfg.Policy.Reserve + need*cfg.Policy.ScenarioCost; cfg.Policy.budget(rl) < est {
					deps.Logf("[org-shard] WARNING: single-org environment: %s budget (%s) is below the estimated %d requests for %d provisioning scenario instance(s); all scenarios still run in %s and may hit primary-quota 403s", org, lease.quotaString(), est, need, org)
				}
			}
			deps.Logf("[org-shard] single-org environment: %s takes all %d provisioning scenario instance(s), no per-org cap (%s)", org, need, lease.quotaString())
			leases = append(leases, lease)
			covered = need
			continue
		}

		lease.Capacity = cfg.Policy.capacity(rl, known, cfg.PerOrgCap)
		usable := lease.Capacity >= 1
		if remaining == 0 {
			usable = cfg.Policy.coversReserve(rl, known)
		}
		if !usable {
			deps.Logf("[org-shard] passing over %s: budget %s leaves room for no provisioning scenario (estimated %d requests each, reserve %d)", org, lease.quotaString(), cfg.Policy.ScenarioCost, cfg.Policy.Reserve)
			*passedOver = append(*passedOver, fmt.Sprintf("%s (%s)", org, lease.quotaString()))
			lowQuota[org] = true
			deps.Release(org, token)
			continue
		}
		deps.Logf("[org-shard] acquired %s for up to %d provisioning scenario instance(s) (%s)", org, lease.Capacity, lease.quotaString())
		leases = append(leases, lease)
		covered += lease.Capacity
	}
	return leases, false, nil
}

func capacityError(cfg orgPlanConfig, demand suiteDemand, leases []orgLease, passedOver []string, cause error) error {
	var held []string
	covered := 0
	for _, l := range leases {
		held = append(held, fmt.Sprintf("%s (capacity %d, %s)", l.Org, l.Capacity, l.quotaString()))
		covered += l.Capacity
	}
	perOrg := "no per-org cap"
	if !cfg.SingleOrg {
		perOrg = fmt.Sprintf("max %d per org", cfg.PerOrgCap)
	}
	return fmt.Errorf("insufficient pool-org capacity: %d scenario instance(s) will run, %d of them provisioning (%s); "+
		"acquired orgs cover %d: [%s]; passed over for low quota: [%s]: %w",
		demand.Eligible, demand.Provisioning, perOrg, covered,
		strings.Join(held, ", "), strings.Join(passedOver, ", "), cause)
}

// orgShard is one acquired org's slice of the run.
type orgShard struct {
	Org      string
	Capacity int
	Template *world.World

	provisioning int
	other        int
	scenarios    []string
}

// shardAssigner routes each scenario instance to an org as godog starts
// it. It is safe for concurrent use.
type shardAssigner struct {
	mu     sync.Mutex
	shards []*orgShard
	logf   func(string, ...any)
}

func newShardAssigner(shards []*orgShard, logf func(string, ...any)) *shardAssigner {
	return &shardAssigner{shards: shards, logf: logf}
}

// Assign picks the org for one scenario instance. A provisioning
// instance goes to the org with the lowest share of its capacity in use
// and never past that capacity; any other instance goes to the org with
// the fewest instances. Ties go to the org acquired first.
func (a *shardAssigner) Assign(scenario string, provisions bool) (*orgShard, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.shards) == 0 {
		return nil, fmt.Errorf("no pool org was acquired for scenario %q", scenario)
	}
	var best *orgShard
	for _, s := range a.shards {
		if provisions {
			if s.provisioning >= s.Capacity {
				continue
			}
			// Compare provisioning/Capacity ratios without division.
			if best == nil || s.provisioning*best.Capacity < best.provisioning*s.Capacity ||
				(s.provisioning*best.Capacity == best.provisioning*s.Capacity && s.total() < best.total()) {
				best = s
			}
			continue
		}
		if best == nil || s.total() < best.total() {
			best = s
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no org capacity left for provisioning scenario %q: %s", scenario, a.summaryLocked())
	}
	kind := "non-provisioning"
	if provisions {
		best.provisioning++
		kind = "provisioning"
	} else {
		best.other++
	}
	best.scenarios = append(best.scenarios, scenario)
	a.logf("[org-shard] scenario %q (%s) -> org %s (provisioning %d/%d, other %d)", scenario, kind, best.Org, best.provisioning, best.Capacity, best.other)
	return best, nil
}

func (s *orgShard) total() int { return s.provisioning + s.other }

// Router returns the suite's TemplateRouter: each scenario instance is
// assigned an org (as provisioning when one of its steps leases a fresh
// repository) and runs against that org's template World.
func (a *shardAssigner) Router() suite.TemplateRouter {
	return func(sc *godog.Scenario) (*world.World, error) {
		shard, err := a.Assign(sc.Uri+": "+sc.Name, pickleProvisions(sc.Steps))
		if err != nil {
			return nil, err
		}
		if shard.Template == nil {
			return nil, fmt.Errorf("org %q has no template World", shard.Org)
		}
		return shard.Template, nil
	}
}

// driverCapacity is the sum of the orgs' install-driver capacities, 0
// when there is none. Each org's driver limits that org to its own repo
// slots, so the sum is the most scenarios the orgs can run at once. The
// suite defaults its aggregate concurrency to it: with three orgs of 12
// repo slots each, 36.
func (a *shardAssigner) driverCapacity() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	capacity := 0
	for _, s := range a.shards {
		if s.Template == nil || s.Template.Driver == nil {
			continue
		}
		capacity += s.Template.Driver.Capacity()
	}
	return capacity
}

// orgLogf prefixes every line logged through logf with the org, so the
// interleaved output of concurrent orgs stays attributable.
func orgLogf(logf func(string, ...any), org string) func(string, ...any) {
	return func(format string, args ...any) {
		logf("[%s] "+format, append([]any{org}, args...)...)
	}
}

// Summary renders each org's assignment for the run log.
func (a *shardAssigner) Summary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.summaryLocked()
}

func (a *shardAssigner) summaryLocked() string {
	parts := make([]string, 0, len(a.shards))
	for _, s := range a.shards {
		parts = append(parts, fmt.Sprintf("%s: provisioning %d/%d, other %d", s.Org, s.provisioning, s.Capacity, s.other))
	}
	return strings.Join(parts, "; ")
}

// Scenarios returns the scenario names assigned to each org, sorted.
func (a *shardAssigner) Scenarios() map[string][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string][]string, len(a.shards))
	for _, s := range a.shards {
		names := append([]string(nil), s.scenarios...)
		sort.Strings(names)
		out[s.Org] = names
	}
	return out
}

// shardSetup is what setting up one org produced. Run owns it and passes
// it to Setup, which fills in each field as soon as it exists, so
// whatever was created is torn down even when Setup fails or panics
// part-way.
type shardSetup struct {
	Driver   install.Driver
	Template *world.World
	// Quota, when set, reports the org token's current budget for the
	// end-of-run log.
	Quota func() (forge.RateLimit, bool)
}

// shardedSuite acquires the orgs a run needs, sets them up concurrently,
// runs the suite body, and tears every org down.
type shardedSuite struct {
	Deps   orgPoolDeps
	Config orgPlanConfig
	// Setup prepares one acquired org (stale-resource cleanup, install
	// driver, SCM/CI drivers, template World) into st. It must set
	// st.Driver as soon as the driver is created.
	Setup func(ctx context.Context, lease orgLease, st *shardSetup) error
}

// Run executes body with an assigner over the acquired orgs. Every org
// that was locked is released, and every driver that was created is
// finalized first, whether acquisition, setup or body fails, panics or
// is cancelled.
func (s *shardedSuite) Run(ctx context.Context, demand suiteDemand, body func(*shardAssigner) error) error {
	logf := s.Deps.Logf
	leases, err := acquireOrgsForDemand(ctx, s.Deps, s.Config, demand)
	if err != nil {
		return err
	}
	setups := make([]*shardSetup, len(leases))
	for i := range setups {
		setups[i] = &shardSetup{}
	}
	defer func() {
		for i, l := range leases {
			if st := setups[i]; st.Driver != nil {
				if st.Quota != nil {
					if rl, ok := st.Quota(); ok {
						logf("[org-shard] %s ending quota: %s", l.Org, rl)
					}
				}
				if ferr := st.Driver.Finalize(context.Background()); ferr != nil {
					logf("[org-shard] %s driver finalize: %v", l.Org, ferr)
				}
			}
			s.Deps.Release(l.Org, l.Token)
		}
	}()

	errs := make([]error, len(leases))
	var wg sync.WaitGroup
	for i := range leases {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("panic: %v", r)
				}
			}()
			errs[i] = s.Setup(ctx, leases[i], setups[i])
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			errs[i] = fmt.Errorf("setting up org %q: %w", leases[i].Org, errs[i])
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	shards := make([]*orgShard, len(leases))
	for i, l := range leases {
		shards[i] = &orgShard{Org: l.Org, Capacity: l.Capacity, Template: setups[i].Template}
		logf("[org-shard] org %d/%d: %s, provisioning capacity %d, starting quota: %s", i+1, len(leases), l.Org, l.Capacity, l.quotaString())
	}
	assigner := newShardAssigner(shards, logf)
	start := time.Now()
	defer func() {
		logf("[org-shard] suite body finished in %s across %d org(s): %s", time.Since(start).Round(time.Second), len(shards), assigner.Summary())
		scenarios := assigner.Scenarios()
		for _, sh := range shards {
			logf("[org-shard] %s scenarios: %s", sh.Org, strings.Join(scenarios[sh.Org], " | "))
		}
	}()
	return body(assigner)
}
