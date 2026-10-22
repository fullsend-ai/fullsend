package install

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// orgCredential is one org's installation token and the forge client
// built from it.
type orgCredential struct {
	token  string
	client forge.Client
}

// orgCredentials resolves and caches each org's credential. A token is
// minted once per org and kept for the suite (see PoolConfig.TokenFor).
// Failures are not cached, so a later lease retries the org. One client
// per org is shared by the allocator, the org's ensurer and its
// scenarios, so the allocator's rate-limit gate sees all of the org's
// traffic.
type orgCredentials struct {
	tokenFor  OrgTokenFunc
	newClient func(token string) forge.Client

	mu     sync.Mutex
	byOrg  map[string]orgCredential
	flight singleflight.Group
}

func newOrgCredentials(tokenFor OrgTokenFunc, newClient func(string) forge.Client) *orgCredentials {
	return &orgCredentials{tokenFor: tokenFor, newClient: newClient, byOrg: map[string]orgCredential{}}
}

func (c *orgCredentials) get(ctx context.Context, org string) (orgCredential, error) {
	c.mu.Lock()
	cred, ok := c.byOrg[org]
	c.mu.Unlock()
	if ok {
		return cred, nil
	}
	v, err := doContext(ctx, &c.flight, org, func() (any, error) {
		c.mu.Lock()
		cached, ok := c.byOrg[org]
		c.mu.Unlock()
		if ok {
			return cached, nil
		}
		// The mint is shared by every caller waiting on this org, so it
		// does not stop when the caller that started it is cancelled.
		token, err := c.tokenFor(context.WithoutCancel(ctx), org)
		if err != nil {
			return nil, fmt.Errorf("minting token for %s: %w", org, err)
		}
		cred := orgCredential{token: token, client: c.newClient(token)}
		c.mu.Lock()
		c.byOrg[org] = cred
		c.mu.Unlock()
		return cred, nil
	})
	if err != nil {
		return orgCredential{}, err
	}
	return v.(orgCredential), nil
}

// doContext runs fn through g like singleflight.Group.Do, but a caller
// waiting on a shared in-flight call returns ctx.Err() as soon as its own
// context is done instead of blocking until the call completes. fn keeps
// running for the remaining callers, so it must not depend on any one
// caller's cancellation.
func doContext(ctx context.Context, g *singleflight.Group, key string, fn func() (any, error)) (any, error) {
	ch := g.DoChan(key, fn)
	select {
	case res := <-ch:
		return res.Val, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// clientFor implements OrgClientFunc for the slot allocator.
func (c *orgCredentials) clientFor(ctx context.Context, org string) (forge.Client, error) {
	cred, err := c.get(ctx, org)
	if err != nil {
		return nil, err
	}
	return cred.client, nil
}

// orgEnv is the per-org half of the shared driver: the org's mint and
// the ensurer that installs into the org's slot repos with the org's
// credential.
type orgEnv struct {
	mint    mintDriver
	mintURL string
	ensurer ensurer
	// statusGroup and workflowHostRepos are what the org's mint was
	// deployed with (--status-github-group, --workflow-host-repos);
	// empty when the driver does not configure them. Leases carry them
	// so the mint status steps check the mint actually serving the
	// scenario.
	statusGroup       string
	workflowHostRepos []string
}

// orgSetupFunc deploys org's mint and returns its orgEnv. The shared
// driver calls it once per org, the first time a slot in the org is
// leased. On error it must leave nothing deployed.
type orgSetupFunc func(ctx context.Context, org string, cred orgCredential) (*orgEnv, error)

// composedDriver is the shared Driver of the behaviour and playback
// suites. It leases (org, repo-name) slots across the pool orgs through
// the slot allocator (slotpool.go): a claim creates the slot repo, so an
// existing repo always belongs to another holder and is never reset or
// deleted. Each org gets its own mint and ensurer on its first lease
// (orgSetupFunc), using that org's installation credential, and every
// lease carries the org, credential and mint URL so the scenario's
// World, SCM/CI drivers and cleanup all target the same org.
type composedDriver struct {
	alloc *slotAllocator
	creds *orgCredentials
	setup orgSetupFunc
	logf  func(string, ...any)

	// traces, when set, is the job-scoped trace collector: each
	// allocated repo is pointed at it, and Finalize stops it and
	// saves its trace files. Set by traceStart.attach.
	traces traceSink

	// pendingTraces is a collector that failed to start and whose own
	// cleanup also failed. It is never used to configure repos; Finalize
	// only retries its cleanup.
	pendingTraces interface{ Stop(context.Context) error }

	// envMu guards envs, closed and abandoned. setups counts org setups
	// in flight: a setup runs detached from its callers' contexts, so
	// Finalize waits for it (until its own context is done) rather than
	// snapshotting envs while a mint is still being deployed. closed is
	// set by Finalize; no setup starts after it. abandoned is set when
	// Finalize snapshots envs for teardown: a setup that finishes after
	// that tears down its own mint instead of caching it.
	envMu     sync.Mutex
	envs      map[string]*orgEnv
	closed    bool
	abandoned bool
	setups    sync.WaitGroup
	envFlight singleflight.Group

	// allocs counts AllocateLease calls admitted (under envMu, while the
	// driver is not closed) and not yet returned, so Finalize can wait
	// for allocations still provisioning before it snapshots leases.
	allocs sync.WaitGroup
	// acquireCtx is the parent of every admitted allocation's wait for
	// pool capacity; Finalize cancels it (cancelAcquire) so an
	// allocation blocked on a full pool cannot keep Finalize from
	// reclaiming the leases that would unblock it. Both are created on
	// the first admission, under envMu.
	acquireCtx    context.Context
	cancelAcquire context.CancelFunc

	mu          sync.Mutex
	outstanding map[Slot]*orgEnv // slots currently leased
	// finalized is set by Finalize when it snapshots outstanding: an
	// allocation finishing after that (Finalize gave up waiting) releases
	// its slot instead of registering a lease nobody will reclaim.
	finalized bool
}

// newComposedDriver builds the shared driver over pool. setup deploys a
// single org's mint and ensurer on demand.
func newComposedDriver(pool PoolConfig, setup orgSetupFunc) (*composedDriver, error) {
	if err := pool.validate(); err != nil {
		return nil, fmt.Errorf("composed driver: %w", err)
	}
	if setup == nil {
		return nil, errors.New("composed driver: org setup must not be nil")
	}
	logf := poolLogf(pool)
	creds := newOrgCredentials(pool.TokenFor, pool.NewClient)
	alloc, err := newSlotAllocator(pool.Layout, creds.clientFor, logf)
	if err != nil {
		return nil, fmt.Errorf("composed driver: %w", err)
	}
	return &composedDriver{
		alloc:       alloc,
		creds:       creds,
		setup:       setup,
		logf:        logf,
		envs:        map[string]*orgEnv{},
		outstanding: map[Slot]*orgEnv{},
	}, nil
}

// groupMintFunc builds the mint driver for one mint group from the
// credential of the org whose lease triggered the deploy. cleanup
// releases deploy-only material (PEM files, trace headers) and is
// called once Install has returned.
type groupMintFunc func(group mintGroup, cred orgCredential) (md mintDriver, cleanup func(), err error)

// buildPoolDriver constructs the shared slot-leasing driver. plan maps
// each org to its mint group (nil serves every org from one mint); a
// group's mint is newMint's, deployed on the first lease in any of the
// group's orgs and shared by them (sharedMint), and torn down once at
// Finalize. Each org gets its own ensurer bound to the org's credential
// and its group's mint URL, installing with setupOpts (and the playback
// tracking-issue hook when setupOpts selects the playback runtime).
// traces is the already started trace collector (if any): it is
// attached to the returned driver, or stopped when construction fails.
func buildPoolDriver(pool PoolConfig, plan *mintPlan, newMint groupMintFunc, setupOpts common.GitHubSetupOpts, traces traceStart) (Driver, error) {
	logf := poolLogf(pool)
	if plan == nil {
		plan = singleMintPlan(pool.Layout)
	}
	shared := make([]*sharedMint, len(plan.groups))
	for i, group := range plan.groups {
		shared[i] = &sharedMint{
			deploy: func(ctx context.Context, cred orgCredential) (mintDriver, string, error) {
				md, cleanup, err := newMint(group, cred)
				if err != nil {
					return nil, "", err
				}
				logf("[driver] deploying the mint of %s", group)
				mintURL, err := md.Install(ctx)
				if cleanup != nil {
					cleanup()
				}
				if err != nil {
					// A deploy that failed partway may still have created the
					// mint; tear it down so a failed setup leaves nothing behind.
					if tdErr := md.Teardown(context.WithoutCancel(ctx)); tdErr != nil {
						logf("[driver] tearing down the failed mint deploy of %s: %v", group, tdErr)
					}
					return nil, "", fmt.Errorf("deploying mint: %w", err)
				}
				return md, mintURL, nil
			},
		}
	}
	var hostsOf [][]string
	for _, group := range plan.groups {
		var hosts []string
		if group.workflowHostRepos != "" {
			hosts = strings.Split(group.workflowHostRepos, ",")
		}
		hostsOf = append(hostsOf, hosts)
	}
	setup := func(ctx context.Context, org string, cred orgCredential) (*orgEnv, error) {
		gi, ok := plan.groupOf[org]
		if !ok {
			return nil, fmt.Errorf("org %s is not in any mint group", org)
		}
		md := &sharedMintRef{shared: shared[gi], cred: cred}
		mintURL, err := md.Install(ctx)
		if err != nil {
			return nil, err
		}

		e2eCfg := e2etest.EnvConfig{
			MintURL:      mintURL,
			GCPProjectID: pool.GCPProjectID,
		}
		ens, err := newRepoEnsurerWithOpts(e2eCfg, cred.client, cred.token, pool.Binary, setupOpts, logf,
			playbackInstallHooks(setupOpts, logf))
		if err != nil {
			if tdErr := md.Teardown(context.WithoutCancel(ctx)); tdErr != nil {
				logf("[driver] tearing down the mint for %s after ensurer setup failed: %v", org, tdErr)
			}
			return nil, err
		}
		logf("[driver] %s uses the mint of %s at %s", org, plan.groups[gi], mintURL)
		return &orgEnv{
			mint:              md,
			mintURL:           mintURL,
			ensurer:           ens,
			statusGroup:       plan.statusGroup,
			workflowHostRepos: hostsOf[gi],
		}, nil
	}

	d, err := newComposedDriver(pool, setup)
	if err != nil {
		traces.abort()
		return nil, err
	}
	return traces.attach(d), nil
}

// poolLogf returns pool.Logf, or a no-op logger when it is nil.
func poolLogf(pool PoolConfig) func(string, ...any) {
	if pool.Logf == nil {
		return func(string, ...any) {}
	}
	return pool.Logf
}

// errLeaseRequired is returned by the name-only Driver methods.
var errLeaseRequired = errors.New("the shared pool driver leases slots across orgs; use AllocateLease and DeallocateLease (install.LeaseDriver)")

// AllocateRepo fails: a repo name alone does not identify a slot across
// orgs. Use AllocateLease.
func (d *composedDriver) AllocateRepo(context.Context) (string, error) {
	return "", errLeaseRequired
}

// DeallocateRepo fails: a repo name alone does not identify a slot
// across orgs. Use DeallocateLease.
func (d *composedDriver) DeallocateRepo(context.Context, string) error {
	return errLeaseRequired
}

// AllocateLease implements LeaseDriver.
func (d *composedDriver) AllocateLease(ctx context.Context) (RepoLease, error) {
	// Admission is gated on the driver's lifecycle, including for orgs
	// whose environment is already cached.
	d.envMu.Lock()
	if d.closed {
		d.envMu.Unlock()
		return RepoLease{}, errors.New("allocating repo: driver is finalized")
	}
	d.allocs.Add(1)
	if d.acquireCtx == nil {
		d.acquireCtx, d.cancelAcquire = context.WithCancel(context.Background())
	}
	acquireStop := d.acquireCtx
	d.envMu.Unlock()
	defer d.allocs.Done()

	// Only the wait for capacity is cancelled when Finalize begins;
	// an allocation already provisioning keeps ctx alone.
	acquireCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopWatch := context.AfterFunc(acquireStop, cancel)
	slot, _, err := d.alloc.Acquire(acquireCtx)
	stopWatch()
	if err != nil {
		return RepoLease{}, fmt.Errorf("allocating repo: %w", err)
	}

	lease, env, err := d.provision(ctx, slot)
	if err != nil {
		// The claim created the slot repo, so it is ours to delete:
		// release the slot rather than leaving it occupied until GC. The
		// release runs even if ctx was cancelled.
		if relErr := d.alloc.Release(context.WithoutCancel(ctx), slot); relErr != nil {
			d.logf("[driver] releasing %s after failed provisioning: %v", slot, relErr)
		}
		return RepoLease{}, fmt.Errorf("allocating repo %s: %w", slot, err)
	}

	d.mu.Lock()
	if d.finalized {
		d.mu.Unlock()
		// Finalize gave up waiting for this allocation and has already
		// torn the driver down; undo it instead of leaking the slot.
		if relErr := d.alloc.Release(context.WithoutCancel(ctx), slot); relErr != nil {
			d.logf("[driver] releasing %s allocated after Finalize: %v", slot, relErr)
		}
		return RepoLease{}, fmt.Errorf("allocating repo %s: driver is finalized", slot)
	}
	d.outstanding[slot] = env
	d.mu.Unlock()

	d.logf("[driver] allocated %s (mint %s; %s)", slot, env.mintURL, d.alloc.quotaSummary(slot.Org))
	return lease, nil
}

// provision makes a freshly claimed slot ready: the org's credential and
// orgEnv (deploying its mint on first use), then the install.
func (d *composedDriver) provision(ctx context.Context, slot Slot) (_ RepoLease, _ *orgEnv, err error) {
	cred, err := d.creds.get(ctx, slot.Org)
	if err != nil {
		return RepoLease{}, nil, err
	}
	env, err := d.orgEnv(ctx, slot.Org, cred)
	if err != nil {
		return RepoLease{}, nil, err
	}
	// Provisioning can be delayed (retry waits, mint or inference setup),
	// so the ensurer re-verifies the claim is still this lease's before
	// each write to the slot.
	stillOwns := func(ctx context.Context) (bool, error) {
		return d.alloc.StillOwns(ctx, slot)
	}
	if err := env.ensurer.EnsureRepo(ctx, slot.Org, slot.Repo, stillOwns); err != nil {
		return RepoLease{}, nil, err
	}
	// EnsureRepo succeeded, so the ensurer remembers this slot as
	// installed. If provisioning fails from here on, the caller releases
	// the slot's claim and the next lease creates a fresh repo under the
	// same name; forget the install (and clean up the fork, while the
	// claim is still ours) so that lease installs from scratch instead of
	// being handed an uninstalled repo. The cleanup runs even if ctx was
	// cancelled.
	defer func() {
		if err == nil {
			return
		}
		if relErr := env.ensurer.ReleaseRepo(context.WithoutCancel(ctx), slot.Org, slot.Repo, stillOwns); relErr != nil {
			d.logf("[driver] releasing leftover state of %s after failed provisioning: %v", slot, relErr)
		}
	}()
	if d.traces != nil {
		// EnsureRepo's validation and Actions-readiness waits can outlast
		// the lease, and trace configuration writes variables and secrets
		// by repo name, so recheck ownership and guard those writes.
		target := slot.Org + "/" + slot.Repo
		if err := requireOwnership(ctx, stillOwns, target, "trace configuration"); err != nil {
			return RepoLease{}, nil, err
		}
		traceCtx := ownershipGuardedCtx(ctx, stillOwns, target, "trace configuration")
		if err := d.traces.ConfigureRepo(traceCtx, cred.client, slot.Org, slot.Repo); err != nil {
			if errors.Is(err, errLeaseLost) {
				// The slot is no longer this lease's: fail the allocation
				// rather than record a lease on a repo it does not own.
				return RepoLease{}, nil, err
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				// The caller cancelled (or its deadline passed) while the
				// traces were being configured: that is not a best-effort
				// trace failure, and the caller no longer wants the lease.
				return RepoLease{}, nil, fmt.Errorf("configuring trace collection: %w", ctxErr)
			}
			d.logf("[driver] trace collector: configuring %s (continuing without traces from it): %v", slot, err)
		}
	}
	return RepoLease{
		Org:     slot.Org,
		Repo:    slot.Repo,
		Token:   cred.token,
		Client:  cred.client,
		MintURL: env.mintURL,

		MintStatusGroup:       env.statusGroup,
		MintWorkflowHostRepos: env.workflowHostRepos,
	}, env, nil
}

// orgEnv returns org's environment, setting it up on first use.
// Concurrent first leases in one org share a single setup; a failed
// setup is not cached, so the org's next lease tries again.
func (d *composedDriver) orgEnv(ctx context.Context, org string, cred orgCredential) (*orgEnv, error) {
	d.envMu.Lock()
	env, ok := d.envs[org]
	d.envMu.Unlock()
	if ok {
		return env, nil
	}
	v, err := doContext(ctx, &d.envFlight, org, func() (any, error) {
		d.envMu.Lock()
		cached, ok := d.envs[org]
		if !ok && d.closed {
			d.envMu.Unlock()
			return nil, fmt.Errorf("setting up %s: driver is finalized", org)
		}
		if !ok {
			d.setups.Add(1)
		}
		d.envMu.Unlock()
		if ok {
			return cached, nil
		}
		defer d.setups.Done()
		d.logf("[driver] first lease in %s: deploying its mint", org)
		// Shared by every lease waiting on this org, so it is independent
		// of the cancellation of the lease that happened to start it.
		env, err := d.setup(context.WithoutCancel(ctx), org, cred)
		if err != nil {
			return nil, fmt.Errorf("setting up %s: %w", org, err)
		}
		d.envMu.Lock()
		if d.abandoned {
			// Finalize gave up waiting and has already torn down the
			// mints it knew of; this one is ours to remove.
			d.envMu.Unlock()
			if env.mint != nil {
				if err := env.mint.Teardown(context.WithoutCancel(ctx)); err != nil {
					d.logf("[driver] tearing down %s mint deployed after Finalize: %v", org, err)
				}
			}
			return nil, fmt.Errorf("setting up %s: driver is finalized", org)
		}
		d.envs[org] = env
		d.envMu.Unlock()
		return env, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*orgEnv), nil
}

// StillOwnsLease implements LeaseVerifier through the allocator, which
// compares the repository under the slot's name with this lease's claim.
func (d *composedDriver) StillOwnsLease(ctx context.Context, org, repo string) (bool, error) {
	return d.alloc.StillOwns(ctx, Slot{Org: org, Repo: repo})
}

// DeallocateLease implements LeaseDriver. The ensurer removes leftover
// per-slot state, then the allocator deletes the slot repo after
// checking it is still this lease's and frees the slot. Release failures
// are logged, not returned: a repo the release could not delete stays
// occupied for every allocator and is reclaimed by GC (slotgc.go), so
// it cannot leak into another lease.
func (d *composedDriver) DeallocateLease(ctx context.Context, org, repo string) error {
	slot := Slot{Org: org, Repo: repo}
	d.mu.Lock()
	env, ok := d.outstanding[slot]
	if ok {
		delete(d.outstanding, slot)
	}
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("DeallocateLease: %s is not an outstanding lease (possible double-release)", slot)
	}
	d.release(ctx, slot, env)
	d.logf("[driver] deallocated %s (%s)", slot, d.alloc.quotaSummary(slot.Org))
	return nil
}

// release runs the ensurer's and the allocator's release steps for one
// slot, logging failures. The ensurer deletes the slot's fork only while
// the allocator confirms the slot repo is still this lease's, since a
// replaced repo means the fork may belong to the replacement's holder.
// The ensurer re-asks before the delete and every resend of it. A repo
// that cannot be verified is treated as not ours.
func (d *composedDriver) release(ctx context.Context, slot Slot, env *orgEnv) {
	stillOwns := func(ctx context.Context) (bool, error) {
		return d.alloc.StillOwns(ctx, slot)
	}
	if err := env.ensurer.ReleaseRepo(ctx, slot.Org, slot.Repo, stillOwns); err != nil {
		d.logf("[driver] releasing leftover state of %s: %v", slot, err)
	}
	if err := d.alloc.Release(ctx, slot); err != nil {
		d.logf("[driver] releasing slot %s (left for GC): %v", slot, err)
	}
}

// Finalize releases any outstanding leases (logging them and returning
// an error), releases every org's mint (each shared mint is torn down
// once, by its last org), logs the per-org quota
// diagnostics, and stops the trace collector last.
func (d *composedDriver) Finalize(ctx context.Context) error {
	// Refuse new allocations, then wait for those still provisioning so
	// their leases are in outstanding before it is snapshotted. The wait
	// honors ctx; an allocation still running then releases its own slot
	// (see finalized).
	d.envMu.Lock()
	d.closed = true
	if d.cancelAcquire != nil {
		// Abandon waits for pool capacity: with a full pool they only
		// end once the leases Finalize reclaims below are released.
		d.cancelAcquire()
	}
	d.envMu.Unlock()
	allocsDone := make(chan struct{})
	go func() {
		d.allocs.Wait()
		close(allocsDone)
	}()
	var allocWaitErr error
	select {
	case <-allocsDone:
	case <-ctx.Done():
		allocWaitErr = fmt.Errorf("Finalize: allocation still in flight: %w", ctx.Err())
	}

	d.mu.Lock()
	d.finalized = true
	leaked := make([]Slot, 0, len(d.outstanding))
	envs := make(map[Slot]*orgEnv, len(d.outstanding))
	for slot, env := range d.outstanding {
		leaked = append(leaked, slot)
		envs[slot] = env
	}
	d.outstanding = map[Slot]*orgEnv{}
	d.mu.Unlock()

	var leakErr error
	if len(leaked) > 0 {
		sort.Slice(leaked, func(i, j int) bool { return leaked[i].String() < leaked[j].String() })
		d.logf("[driver] Finalize: reclaiming %d outstanding lease(s): %v", len(leaked), leaked)
		for _, slot := range leaked {
			d.release(ctx, slot, envs[slot])
		}
		leakErr = fmt.Errorf("Finalize: %d outstanding lease(s) not deallocated: %v", len(leaked), leaked)
	}

	// Refuse new org setups, then wait for those still in flight (they
	// outlive the leases that started them) so their mints are cached
	// and torn down below instead of being deployed after teardown. The
	// wait honors ctx: a hung deploy must not block teardown of the other
	// orgs' mints. A setup still running then tears down its own mint.
	d.envMu.Lock()
	d.closed = true
	d.envMu.Unlock()
	setupsDone := make(chan struct{})
	go func() {
		d.setups.Wait()
		close(setupsDone)
	}()
	var waitErr error
	select {
	case <-setupsDone:
	case <-ctx.Done():
		waitErr = fmt.Errorf("Finalize: org setup still in flight: %w", ctx.Err())
	}

	d.envMu.Lock()
	d.abandoned = true
	orgs := sortedKeys(d.envs)
	mints := make([]mintDriver, 0, len(orgs))
	for _, org := range orgs {
		mints = append(mints, d.envs[org].mint)
	}
	d.envMu.Unlock()

	var teardownErr error
	for i, md := range mints {
		if md == nil {
			continue
		}
		if err := md.Teardown(ctx); err != nil {
			teardownErr = errors.Join(teardownErr, fmt.Errorf("Finalize: mint teardown for %s: %w", orgs[i], err))
		}
	}

	if diag := d.alloc.QuotaDiagnostics(); diag != "" {
		d.logf("[driver] per-org rate limits at finalize:\n%s", diag)
	}

	// Stop the trace collector last, after the repos (and their
	// workflows) are gone, so it flushes everything it received.
	var traceErr error
	if d.traces != nil {
		if err := d.traces.Stop(ctx); err != nil {
			traceErr = fmt.Errorf("Finalize: trace collector: %w", err)
		}
	}
	if d.pendingTraces != nil {
		if err := d.pendingTraces.Stop(ctx); err != nil {
			traceErr = errors.Join(traceErr, fmt.Errorf("Finalize: failed-start trace collector cleanup: %w", err))
		}
	}

	return errors.Join(allocWaitErr, leakErr, waitErr, teardownErr, traceErr)
}

// Capacity returns the number of slots in the pool.
func (d *composedDriver) Capacity() int {
	return d.alloc.Capacity()
}

// Compile-time check.
var _ LeaseDriver = (*composedDriver)(nil)
var _ LeaseVerifier = (*composedDriver)(nil)
