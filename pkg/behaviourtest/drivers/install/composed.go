package install

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// composedDriver wraps a MintDriver, an internal slot pool, and a
// RepoEnsurer behind the unified Driver interface. Suite and scenario
// lifecycle code speaks only to this surface.
//
// Thread safety: all exported methods are safe for concurrent callers.
type composedDriver struct {
	mint      MintDriver
	mintState State
	ensurer   RepoEnsurer
	org       string
	logf      func(string, ...any)

	// Internal slot pool.
	poolSize int
	names    chan string // available slot names

	mu          sync.Mutex
	outstanding map[string]struct{} // currently allocated repo names
}

// NewComposedDriver creates a unified Driver that wraps a MintDriver
// with internal pool and RepoEnsurer management. It calls
// mint.Install to perform suite-level setup before returning so that
// setup failures fail the suite before scenarios run.
//
// If Install fails after a partial deploy, the constructor calls
// mint.Teardown to clean up before returning the error.
//
// The returned State is the mint-level install state (org-scoped, empty
// repo). Callers use it to seed the template World's Install field.
// Per-scenario repo-scoped state is constructed after AllocateRepo.
func NewComposedDriver(
	ctx context.Context,
	mint MintDriver,
	org string,
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	poolSize int,
	logf func(string, ...any),
) (Driver, State, error) {
	// Step 1: install the mint (deploy preview, etc.).
	mintState, err := mint.Install(ctx, org)
	if err != nil {
		// Attempt teardown in case the deploy partially succeeded
		// (e.g. cfmint set previewAlias before deployCFMint failed).
		_ = mint.Teardown(ctx, org, nil)
		return nil, nil, fmt.Errorf("installing mint: %w", err)
	}

	// Step 2: thread mint URL to the ensurer config so additional
	// pool repos use the same mint endpoint.
	if m, ok := mintState.(MintURLProvider); ok && m.MintURL() != "" {
		logf("using mint URL for ensurer: %s", m.MintURL())
		e2eCfg.MintURL = m.MintURL()
	}

	// Step 3: create the internal slot pool.
	ch := make(chan string, poolSize)
	for i := 1; i <= poolSize; i++ {
		ch <- fmt.Sprintf("test-repo-%02d", i)
	}

	// Step 4: create the internal ensurer.
	ensurer := NewRepoEnsurer(e2eCfg, client, token, binary, logf)

	d := &composedDriver{
		mint:        mint,
		mintState:   mintState,
		ensurer:     ensurer,
		org:         org,
		logf:        logf,
		poolSize:    poolSize,
		names:       ch,
		outstanding: make(map[string]struct{}),
	}
	return d, mintState, nil
}

// AllocateRepo leases a slot from the internal pool and ensures the
// repo is created and has fullsend installed. Blocks until a slot is
// free or ctx is cancelled.
func (d *composedDriver) AllocateRepo(ctx context.Context) (string, error) {
	// Acquire a slot name from the pool.
	var name string
	select {
	case name = <-d.names:
	case <-ctx.Done():
		return "", fmt.Errorf("acquiring repo slot: %w", ctx.Err())
	}

	// Ensure the repo is created and installed.
	if _, err := d.ensurer.EnsureRepo(ctx, d.org, name); err != nil {
		// Release the slot back on failure.
		d.names <- name
		return "", fmt.Errorf("ensuring repo %s/%s: %w", d.org, name, err)
	}

	d.mu.Lock()
	d.outstanding[name] = struct{}{}
	d.mu.Unlock()

	return name, nil
}

// DeallocateRepo returns a previously allocated repo slot. Errors on
// unknown name or double-release.
func (d *composedDriver) DeallocateRepo(_ context.Context, repoName string) error {
	d.mu.Lock()
	if _, ok := d.outstanding[repoName]; !ok {
		d.mu.Unlock()
		return fmt.Errorf("DeallocateRepo: %q is not an outstanding allocation (possible double-release)", repoName)
	}
	delete(d.outstanding, repoName)
	d.mu.Unlock()

	// Return the name to the pool. The channel buffer equals poolSize
	// and this name was removed during AllocateRepo, so the send is
	// guaranteed non-blocking.
	d.names <- repoName
	return nil
}

// Finalize tears down suite-scoped resources. If leases are still
// outstanding it reclaims them, logs the names, completes teardown,
// and returns an error so leaked After-hooks fail CI without stranding
// resources (e.g. CF preview mints).
func (d *composedDriver) Finalize(ctx context.Context) error {
	// Collect outstanding leases.
	d.mu.Lock()
	outstanding := make([]string, 0, len(d.outstanding))
	for name := range d.outstanding {
		outstanding = append(outstanding, name)
	}
	d.mu.Unlock()

	// Deterministic order for logging.
	sort.Strings(outstanding)

	var leakErr error
	if len(outstanding) > 0 {
		d.logf("Finalize: reclaiming %d outstanding allocations: %v", len(outstanding), outstanding)
		for _, name := range outstanding {
			d.names <- name
		}
		d.mu.Lock()
		for _, name := range outstanding {
			delete(d.outstanding, name)
		}
		d.mu.Unlock()
		leakErr = fmt.Errorf("Finalize: %d outstanding allocations were not deallocated: %v", len(outstanding), outstanding)
	}

	// Tear down mint infrastructure (e.g. CF preview worker).
	var teardownErr error
	if d.mint != nil && d.mintState != nil {
		if err := d.mint.Teardown(ctx, d.org, d.mintState); err != nil {
			d.logf("Finalize: mint teardown: %v", err)
			teardownErr = fmt.Errorf("mint teardown: %w", err)
		}
	}

	return errors.Join(leakErr, teardownErr)
}

// Capacity returns the max concurrent outstanding allocations (the
// driver's real parallelism ceiling).
func (d *composedDriver) Capacity() int {
	return d.poolSize
}

// InstallState returns the suite-level install state obtained from the
// MintDriver during construction. The suite uses this via the
// StateProvider interface to seed World.Install without threading State
// through the Factory return value.
func (d *composedDriver) InstallState() State {
	return d.mintState
}

// Compile-time checks.
var (
	_ Driver        = (*composedDriver)(nil)
	_ StateProvider = (*composedDriver)(nil)
)
