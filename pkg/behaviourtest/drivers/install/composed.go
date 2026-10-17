package install

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// composedDriver wraps a mintDriver, ensurer, and an internal
// channel-based pool into a unified Driver. The suite constructs one
// via newComposedDriver (typically called from a Factory) and threads
// it through World. Scenarios call AllocateRepo / DeallocateRepo;
// Finalize tears down suite-scoped resources.
type composedDriver struct {
	org     string
	mint    mintDriver
	ensurer ensurer
	logf    func(string, ...any)

	// rate, when set, samples the shared installation token's primary
	// rate-limit budget on every allocation and release, so a suite
	// that later goes blind on 403s shows in its own log how the
	// budget drained across the run (#6702).
	rate forge.RateLimitReporter

	// mintURL is the suite's mint URL, reported by MintURL. Set by the
	// factories through withMintURL once the mint is deployed.
	mintURL string

	names    chan string // buffered channel of available repo names
	capacity int

	mu          sync.Mutex
	outstanding map[string]struct{} // names currently leased
}

// newComposedDriver constructs a unified Driver from its constituent
// parts. It pre-fills the internal pool with repo names in the form
// "test-repo-01" … "test-repo-NN". The caller (Factory) is responsible
// for deploying the mint and creating the ensurer before calling this.
func newComposedDriver(
	org string,
	mint mintDriver,
	ensurer ensurer,
	capacity int,
	logf func(string, ...any),
) (Driver, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("composed driver: capacity must be positive, got %d", capacity)
	}
	if ensurer == nil {
		return nil, errors.New("composed driver: ensurer must not be nil")
	}
	names := make(chan string, capacity)
	for i := 1; i <= capacity; i++ {
		names <- fmt.Sprintf("test-repo-%02d", i)
	}
	return &composedDriver{
		org:         org,
		mint:        mint,
		ensurer:     ensurer,
		logf:        logf,
		names:       names,
		capacity:    capacity,
		outstanding: make(map[string]struct{}),
	}, nil
}

// AllocateRepo leases a slot from the internal pool and ensures the
// repo is created and installed from a clean base (delete+recreate if
// it already exists). Blocks until a slot is free or ctx is cancelled.
func (d *composedDriver) AllocateRepo(ctx context.Context) (string, error) {
	// Pace setup against the shared installation token's primary quota.
	if err := d.awaitQuota(ctx); err != nil {
		return "", fmt.Errorf("allocating repo: %w", err)
	}

	// Acquire a name from the pool (blocks if all slots are in use).
	var name string
	select {
	case name = <-d.names:
	case <-ctx.Done():
		return "", fmt.Errorf("allocating repo: %w", ctx.Err())
	}

	d.mu.Lock()
	d.outstanding[name] = struct{}{}
	d.mu.Unlock()

	// Ensure the repo exists and has fullsend installed.
	if err := d.ensurer.EnsureRepo(ctx, d.org, name); err != nil {
		// Return the name to the pool on failure so it can be retried.
		d.mu.Lock()
		delete(d.outstanding, name)
		d.mu.Unlock()
		d.names <- name
		return "", fmt.Errorf("allocating repo %s/%s: %w", d.org, name, err)
	}

	d.logf("[driver] allocated %s/%s", d.org, name)
	d.logRateLimit("after allocating " + d.org + "/" + name)
	return name, nil
}

// DeallocateRepo deletes the leased pool base (best-effort) and returns
// the name to the pool. Called from the After hook after CleanupScenario
// and after in-scenario debug collection (workflow logs, agent
// artifacts) so CI still has those files under BEHAVIOUR_ARTIFACT_DIR
// once the forge repo is gone. Errors on unknown name or double-release.
// A delete failure is logged; the name is still returned so the slot is
// not leaked. The next AllocateRepo reset+recreates leftovers.
func (d *composedDriver) DeallocateRepo(ctx context.Context, repoName string) error {
	d.mu.Lock()
	if _, ok := d.outstanding[repoName]; !ok {
		d.mu.Unlock()
		return fmt.Errorf("DeallocateRepo: %q is not an outstanding lease (possible double-release)", repoName)
	}
	delete(d.outstanding, repoName)
	d.mu.Unlock()

	if d.ensurer != nil {
		if err := d.ensurer.DeleteRepo(ctx, d.org, repoName); err != nil {
			d.logf("[driver] deleting leased repo %s/%s: %v", d.org, repoName, err)
		}
	}

	d.mu.Lock()
	// Send inside the lock: the channel buffer equals capacity and this
	// name was removed during AllocateRepo, so the send is guaranteed
	// non-blocking.
	d.names <- repoName
	d.logf("[driver] deallocated %s/%s", d.org, repoName)
	d.mu.Unlock()
	d.logRateLimit("after deallocating " + d.org + "/" + repoName)
	return nil
}

// Finalize tears down suite-scoped resources (mint). If leases are
// still outstanding, it reclaims them (logging the names), best-effort
// deletes each leaked repo (same log-and-continue policy as
// DeallocateRepo, so a leak doesn't leave the forge repo behind until
// the next acquire), and returns an error alongside any mint teardown
// error via errors.Join.
func (d *composedDriver) Finalize(ctx context.Context) error {
	d.mu.Lock()
	var leakErr error
	leaked := make([]string, 0, len(d.outstanding))
	if len(d.outstanding) > 0 {
		for name := range d.outstanding {
			leaked = append(leaked, name)
		}
		d.logf("[driver] Finalize: reclaiming %d outstanding lease(s): %v", len(leaked), leaked)
		for _, name := range leaked {
			delete(d.outstanding, name)
			d.names <- name
		}
		leakErr = fmt.Errorf("Finalize: %d outstanding lease(s) not deallocated: %v", len(leaked), leaked)
	}
	d.mu.Unlock()

	for _, name := range leaked {
		if err := d.ensurer.DeleteRepo(ctx, d.org, name); err != nil {
			d.logf("[driver] Finalize: deleting leaked repo %s/%s: %v", d.org, name, err)
		}
	}

	var teardownErr error
	if d.mint != nil {
		if err := d.mint.Teardown(ctx); err != nil {
			teardownErr = fmt.Errorf("Finalize: mint teardown: %w", err)
		}
	}

	return errors.Join(leakErr, teardownErr)
}

// Capacity returns the max concurrent outstanding allocations.
func (d *composedDriver) Capacity() int {
	return d.capacity
}

// Compile-time check.
var _ Driver = (*composedDriver)(nil)

// withRateLimitReporter attaches client as the driver's rate-limit
// sampler when it reports one; other clients leave sampling off.
func withRateLimitReporter(d Driver, client forge.Client) Driver {
	if cd, ok := d.(*composedDriver); ok {
		if r, ok := client.(forge.RateLimitReporter); ok {
			cd.rate = r
		}
	}
	return d
}

// withMintURL records mintURL as the URL a composed driver reports
// through MintURLReporter; other drivers are returned unchanged.
func withMintURL(d Driver, mintURL string) Driver {
	if cd, ok := d.(*composedDriver); ok {
		cd.mintURL = mintURL
	}
	return d
}

// MintURL implements MintURLReporter.
func (d *composedDriver) MintURL() string { return d.mintURL }

// Compile-time check: composedDriver reports its mint URL.
var _ MintURLReporter = (*composedDriver)(nil)

// quotaLowWater is the primary-quota budget below which AllocateRepo waits
// for the reset before starting another repo setup. One setup (create
// repo, push scaffold, set variables and secrets) costs well over a
// hundred calls, so starting one near the bottom of the window only
// exhausts the quota partway through it.
const quotaLowWater = 300

// quotaSleep waits d or until ctx is done. Replaced in tests.
var quotaSleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// awaitQuota delays an allocation while the last observed primary quota is
// below quotaLowWater and its reset is still ahead. A reset the context
// deadline cannot cover is logged and not waited for (the setup's own
// rate-limit handling then reports the cause); a stale observation whose
// reset has passed is ignored.
func (d *composedDriver) awaitQuota(ctx context.Context) error {
	if d.rate == nil {
		return nil
	}
	rl, seen := d.rate.RateLimit()
	if !seen || rl.Remaining >= quotaLowWater || rl.Reset.IsZero() {
		return nil
	}
	wait := time.Until(rl.Reset) + time.Second
	if wait <= time.Second {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && wait > time.Until(deadline) {
		d.logf("[driver] primary quota low (%s) and its reset in %s is beyond the suite deadline; allocating anyway", rl, wait.Round(time.Second))
		return nil
	}
	d.logf("[driver] primary quota low (%s); waiting %s for the reset before allocating", rl, wait.Round(time.Second))
	return quotaSleep(ctx, wait)
}

// logRateLimit writes one primary-quota sample, if a reporter is set
// and has observed a response.
func (d *composedDriver) logRateLimit(when string) {
	if d.rate == nil {
		return
	}
	if rl, seen := d.rate.RateLimit(); seen {
		d.logf("[driver] rate limit %s: %s", when, rl)
	}
}
