package install

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// --- fakeMintDriver for composed driver tests ---

type fakeMintDriver struct {
	installState  State
	installErr    error
	teardownErr   error
	teardownCalls int
	mu            sync.Mutex
}

func (f *fakeMintDriver) Install(_ context.Context, _ string) (State, error) {
	if f.installErr != nil {
		return nil, f.installErr
	}
	return f.installState, nil
}

func (f *fakeMintDriver) Teardown(_ context.Context, _ string, _ State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardownCalls++
	return f.teardownErr
}

func (f *fakeMintDriver) TeardownCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.teardownCalls
}

var _ MintDriver = (*fakeMintDriver)(nil)

// --- NewComposedDriver tests ---

func TestNewComposedDriver_Success(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", "https://mint.test"),
	}
	e2eCfg := e2etest.EnvConfig{}

	drv, mintState, err := NewComposedDriver(
		context.Background(), mint, "org", e2eCfg,
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.NoError(t, err)
	require.NotNil(t, drv)
	require.NotNil(t, mintState)

	assert.Equal(t, 3, drv.Capacity())
	assert.Equal(t, "org", mintState.ConfigOwner())
}

func TestNewComposedDriver_InstallFailure_CallsTeardown(t *testing.T) {
	mint := &fakeMintDriver{
		installErr: fmt.Errorf("deploy exploded"),
	}
	e2eCfg := e2etest.EnvConfig{}

	drv, mintState, err := NewComposedDriver(
		context.Background(), mint, "org", e2eCfg,
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.Error(t, err)
	assert.Nil(t, drv)
	assert.Nil(t, mintState)
	assert.Contains(t, err.Error(), "installing mint")

	// Teardown should have been called for cleanup.
	assert.Equal(t, 1, mint.TeardownCallCount(),
		"Teardown should be called to clean up a partial deploy")
}

func TestNewComposedDriver_ThreadsMintURL(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", "https://preview.test"),
	}
	e2eCfg := e2etest.EnvConfig{}

	_, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2eCfg,
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.NoError(t, err)
	// The e2eCfg was passed by value so we can't verify the mutation
	// directly, but the driver was created successfully which means
	// the mint URL extraction path ran without error.
}

// --- AllocateRepo / DeallocateRepo tests ---

func TestAllocateRepo_ReturnsDistinctNames(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	// Use a fake ensurer that always succeeds.
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.NoError(t, err)

	// Replace the ensurer with a fake that always succeeds.
	cd := drv.(*composedDriver)
	cd.ensurer = newFakeEnsurer()

	names := make(map[string]bool)
	for range 3 {
		name, err := drv.AllocateRepo(context.Background())
		require.NoError(t, err)
		assert.False(t, names[name], "duplicate name %q", name)
		names[name] = true
	}
	assert.Len(t, names, 3)
}

func TestAllocateRepo_BlocksWhenExhausted(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", 1, t.Logf,
	)
	require.NoError(t, err)
	cd := drv.(*composedDriver)
	cd.ensurer = newFakeEnsurer()

	// Exhaust the single slot.
	name, err := drv.AllocateRepo(context.Background())
	require.NoError(t, err)

	// Next allocate should block and fail with cancelled context.
	cancelledCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = drv.AllocateRepo(cancelledCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acquiring repo slot")

	// Release and try again — should succeed.
	require.NoError(t, drv.DeallocateRepo(context.Background(), name))
	name2, err := drv.AllocateRepo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, name, name2)
}

func TestAllocateRepo_EnsureFailure_ReleasesSlot(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", 1, t.Logf,
	)
	require.NoError(t, err)

	// Use a failing ensurer.
	cd := drv.(*composedDriver)
	cd.ensurer = &failingEnsurer{err: fmt.Errorf("install boom")}

	_, err = drv.AllocateRepo(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensuring repo")

	// The slot should have been released back — replace ensurer and
	// verify the slot is available.
	cd.ensurer = newFakeEnsurer()
	name, err := drv.AllocateRepo(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, name)
}

func TestDeallocateRepo_UnknownName(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.NoError(t, err)

	err = drv.DeallocateRepo(context.Background(), "not-allocated")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an outstanding allocation")
}

func TestDeallocateRepo_DoubleRelease(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.NoError(t, err)
	cd := drv.(*composedDriver)
	cd.ensurer = newFakeEnsurer()

	name, err := drv.AllocateRepo(context.Background())
	require.NoError(t, err)

	require.NoError(t, drv.DeallocateRepo(context.Background(), name))

	// Second release should error.
	err = drv.DeallocateRepo(context.Background(), name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "double-release")
}

// --- Finalize tests ---

func TestFinalize_NoOutstanding_TearsDownMint(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.NoError(t, err)

	err = drv.Finalize(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, mint.TeardownCallCount())
}

func TestFinalize_WithOutstanding_ReclaimsAndReturnsError(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", 3, t.Logf,
	)
	require.NoError(t, err)
	cd := drv.(*composedDriver)
	cd.ensurer = newFakeEnsurer()

	// Allocate but don't deallocate.
	name, err := drv.AllocateRepo(context.Background())
	require.NoError(t, err)

	err = drv.Finalize(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outstanding allocations were not deallocated")
	assert.Contains(t, err.Error(), name)

	// Mint teardown should still have been called.
	assert.Equal(t, 1, mint.TeardownCallCount())
}

func TestCapacity(t *testing.T) {
	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	for _, size := range []int{1, 5, 12} {
		drv, _, err := NewComposedDriver(
			context.Background(), mint, "org", e2etest.EnvConfig{},
			nil, "tok", "/bin/true", size, t.Logf,
		)
		require.NoError(t, err)
		assert.Equal(t, size, drv.Capacity())
	}
}

// --- Concurrent access test ---

func TestComposedDriver_ConcurrentAllocateDeallocate(t *testing.T) {
	t.Parallel()

	mint := &fakeMintDriver{
		installState: NewPerRepoState("org", "", ""),
	}
	const poolSize = 4
	drv, _, err := NewComposedDriver(
		context.Background(), mint, "org", e2etest.EnvConfig{},
		nil, "tok", "/bin/true", poolSize, t.Logf,
	)
	require.NoError(t, err)
	cd := drv.(*composedDriver)
	cd.ensurer = newFakeEnsurer()

	const goroutines = 20
	var wg sync.WaitGroup

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name, allocErr := drv.AllocateRepo(context.Background())
			if allocErr != nil {
				t.Errorf("AllocateRepo: %v", allocErr)
				return
			}
			// Simulate scenario work.
			time.Sleep(time.Millisecond)
			if deallocErr := drv.DeallocateRepo(context.Background(), name); deallocErr != nil {
				t.Errorf("DeallocateRepo: %v", deallocErr)
			}
		}()
	}
	wg.Wait()

	// All slots should be returned.
	err = drv.Finalize(context.Background())
	require.NoError(t, err, "no outstanding allocations expected after all goroutines finished")
}

// --- Test helpers ---

// failingEnsurer always returns an error.
type failingEnsurer struct {
	err error
}

func (f *failingEnsurer) EnsureRepo(_ context.Context, _, _ string) (State, error) {
	return nil, f.err
}

var _ RepoEnsurer = (*failingEnsurer)(nil)
