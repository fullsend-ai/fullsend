package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/google/uuid"

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
	client  forge.Client
	logf    func(string, ...any)

	// keepRepos, when true (E2E_KEEP_REPOS=true), preserves ephemeral
	// repos after deallocation for post-mortem debugging.
	keepRepos bool

	// rate, when set, samples the shared installation token's primary
	// rate-limit budget on every allocation and release, so a suite
	// that later goes blind on 403s shows in its own log how the
	// budget drained across the run (#6702).
	rate forge.RateLimitReporter

	names    chan string // buffered channel of available repo names
	capacity int

	mu          sync.Mutex
	outstanding map[string]struct{} // names currently leased
}

// newComposedDriver constructs a unified Driver from its constituent
// parts. It pre-fills the internal pool with unique repo names in the
// form "bt-{uuid4}-{slot}" so concurrent CI runs in the same org never
// collide. The caller (Factory) is responsible for deploying the mint
// and creating the ensurer before calling this.
func newComposedDriver(
	org string,
	mint mintDriver,
	ensurer ensurer,
	client forge.Client,
	prefix string,
	capacity int,
	logf func(string, ...any),
) (Driver, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("composed driver: capacity must be positive, got %d", capacity)
	}
	if prefix == "" {
		prefix = uuid.New().String()[:4]
	}
	names := make(chan string, capacity)
	for i := 1; i <= capacity; i++ {
		names <- fmt.Sprintf("bt-%s-%02d", prefix, i)
	}
	return &composedDriver{
		org:         org,
		mint:        mint,
		ensurer:     ensurer,
		client:      client,
		keepRepos:   os.Getenv("E2E_KEEP_REPOS") == "true",
		logf:        logf,
		names:       names,
		capacity:    capacity,
		outstanding: make(map[string]struct{}),
	}, nil
}

// AllocateRepo leases a slot from the internal pool and ensures the
// repo is created and installed. Blocks until a slot is free or ctx
// is cancelled.
func (d *composedDriver) AllocateRepo(ctx context.Context) (string, error) {
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

// DeallocateRepo returns a previously allocated repo to the pool.
// Unless E2E_KEEP_REPOS=true, the repo is deleted from the forge so
// concurrent CI runs never collide on stale repos. Errors on unknown
// name or double-release.
func (d *composedDriver) DeallocateRepo(ctx context.Context, repoName string) error {
	d.mu.Lock()

	if _, ok := d.outstanding[repoName]; !ok {
		d.mu.Unlock()
		return fmt.Errorf("DeallocateRepo: %q is not an outstanding lease (possible double-release)", repoName)
	}
	delete(d.outstanding, repoName)
	d.mu.Unlock()

	// Delete the ephemeral repo BEFORE returning the slot to the pool.
	// The channel send is what makes the slot available to other
	// goroutines — delaying it until after deletion completes prevents
	// a race where a new allocator begins EnsureRepo on a repo that is
	// still being deleted.
	if d.keepRepos {
		d.logf("[driver] keeping %s/%s (E2E_KEEP_REPOS=true)", d.org, repoName)
	} else if d.client != nil {
		forkName := repoName + "-fork"
		d.logf("[driver] deleting ephemeral fork %s/%s (if exists)", d.org, forkName)
		if err := d.client.DeleteRepo(ctx, d.org, forkName); err != nil && !forge.IsNotFound(err) {
			d.logf("[driver] warning: failed to delete fork %s/%s: %v", d.org, forkName, err)
		}
		d.logf("[driver] deleting ephemeral repo %s/%s", d.org, repoName)
		if err := d.client.DeleteRepo(ctx, d.org, repoName); err != nil {
			if !forge.IsNotFound(err) {
				d.logf("[driver] warning: failed to delete %s/%s: %v", d.org, repoName, err)
			}
		}
	}
	// Invalidate the ensurer's cache so re-allocation of this slot
	// triggers a fresh create+install cycle, even when keepRepos is
	// true (the repo exists but needs a clean install).
	d.ensurer.InvalidateCache(d.org, repoName)

	// Return the slot to the pool only after deletion is complete.
	d.names <- repoName
	d.logf("[driver] deallocated %s/%s", d.org, repoName)
	d.logRateLimit("after deallocating " + d.org + "/" + repoName)
	return nil
}

// Finalize tears down suite-scoped resources (mint). If leases are
// still outstanding, it reclaims them (logging the names) and returns
// an error alongside any mint teardown error via errors.Join.
func (d *composedDriver) Finalize(ctx context.Context) error {
	d.mu.Lock()
	var leakErr error
	if len(d.outstanding) > 0 {
		leaked := make([]string, 0, len(d.outstanding))
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
