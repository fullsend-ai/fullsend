package install

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// fakeMintDriver is a test double for mintDriver.
type fakeMintDriver struct {
	teardownCalled    bool
	teardownErr       error
	collectLogsCalled bool
	collectLogsErr    error
}

func (f *fakeMintDriver) Install(_ context.Context, _ string) (string, error) {
	return "https://mint.test", nil
}

func (f *fakeMintDriver) Teardown(_ context.Context) error {
	f.teardownCalled = true
	return f.teardownErr
}

func (f *fakeMintDriver) CollectLogs(_ context.Context, _ time.Time, _ string) error {
	f.collectLogsCalled = true
	return f.collectLogsErr
}

func TestNewComposedDriver_OK(t *testing.T) {
	e := newFakeEnsurer()
	mint := &fakeMintDriver{}

	d, err := newComposedDriver("org", mint, e, 3, t.Logf)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, 3, d.Capacity())
}

func TestNewComposedDriver_InvalidCapacity(t *testing.T) {
	_, err := newComposedDriver("org", nil, nil, 0, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capacity must be positive")
}

func TestComposedDriver_AllocateAndDeallocate(t *testing.T) {
	e := newFakeEnsurer()
	mint := &fakeMintDriver{}

	d, err := newComposedDriver("org", mint, e, 3, t.Logf)
	require.NoError(t, err)

	ctx := context.Background()

	// Allocate a repo.
	name, err := d.AllocateRepo(ctx)
	require.NoError(t, err)
	assert.Contains(t, name, "test-repo-")

	// Deallocate the repo.
	err = d.DeallocateRepo(ctx, name)
	require.NoError(t, err)

	// Re-allocate should succeed.
	name2, err := d.AllocateRepo(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, name2)
}

func TestComposedDriver_DeallocateUnknownName(t *testing.T) {
	e := newFakeEnsurer()
	mint := &fakeMintDriver{}

	d, err := newComposedDriver("org", mint, e, 2, t.Logf)
	require.NoError(t, err)

	err = d.DeallocateRepo(context.Background(), "unknown-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an outstanding lease")
}

func TestComposedDriver_DoubleDeallocate(t *testing.T) {
	e := newFakeEnsurer()
	mint := &fakeMintDriver{}

	d, err := newComposedDriver("org", mint, e, 2, t.Logf)
	require.NoError(t, err)

	ctx := context.Background()
	name, err := d.AllocateRepo(ctx)
	require.NoError(t, err)

	// First deallocate succeeds.
	err = d.DeallocateRepo(ctx, name)
	require.NoError(t, err)

	// Second deallocate fails.
	err = d.DeallocateRepo(ctx, name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "double-release")
}

func TestComposedDriver_AllocateBlocksUntilDeallocate(t *testing.T) {
	e := newFakeEnsurer()
	mint := &fakeMintDriver{}

	d, err := newComposedDriver("org", mint, e, 1, t.Logf)
	require.NoError(t, err)

	ctx := context.Background()

	// Exhaust the pool.
	name, err := d.AllocateRepo(ctx)
	require.NoError(t, err)

	// Allocate with a short-timeout context should fail.
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = d.AllocateRepo(shortCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allocating repo")

	// Deallocate the first name.
	err = d.DeallocateRepo(ctx, name)
	require.NoError(t, err)

	// Now allocate should succeed.
	name2, err := d.AllocateRepo(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, name2)
}

func TestComposedDriver_AllocateEnsureError_ReturnsNameToPool(t *testing.T) {
	// Use a failing ensurer to verify the name is returned to the pool
	// on failure.
	failEnsurer := &failingEnsurer{err: fmt.Errorf("ensure failed")}
	mint := &fakeMintDriver{}

	d, err := newComposedDriver("org", mint, failEnsurer, 1, t.Logf)
	require.NoError(t, err)

	ctx := context.Background()

	// First allocate fails due to ensurer error.
	_, err = d.AllocateRepo(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure failed")

	// The name should be back in the pool — next allocate with a
	// working ensurer would succeed. But since we can't swap the
	// ensurer, verify the pool still has the slot by checking capacity
	// or attempting another allocate (which will also fail but proves
	// the pool isn't exhausted).
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = d.AllocateRepo(shortCtx)
	// Should get an ensure error, not a context deadline (proving the
	// slot was returned).
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure failed")
}

func TestComposedDriver_FinalizeNoOutstanding(t *testing.T) {
	mint := &fakeMintDriver{}
	e := newFakeEnsurer()

	d, err := newComposedDriver("org", mint, e, 2, t.Logf)
	require.NoError(t, err)

	err = d.Finalize(context.Background())
	require.NoError(t, err)
	assert.True(t, mint.teardownCalled, "mint teardown should be called")
}

func TestComposedDriver_FinalizeWithOutstanding(t *testing.T) {
	mint := &fakeMintDriver{}
	e := newFakeEnsurer()

	d, err := newComposedDriver("org", mint, e, 2, t.Logf)
	require.NoError(t, err)

	ctx := context.Background()
	name, err := d.AllocateRepo(ctx)
	require.NoError(t, err)
	_ = name // outstanding

	err = d.Finalize(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outstanding lease")
	assert.True(t, mint.teardownCalled, "mint teardown should still be called")
}

func TestComposedDriver_FinalizeJoinsErrors(t *testing.T) {
	mint := &fakeMintDriver{teardownErr: fmt.Errorf("teardown boom")}
	e := newFakeEnsurer()

	d, err := newComposedDriver("org", mint, e, 2, t.Logf)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = d.AllocateRepo(ctx)
	require.NoError(t, err)

	err = d.Finalize(ctx)
	require.Error(t, err)
	// Both errors should be present via errors.Join.
	assert.Contains(t, err.Error(), "outstanding lease")
	assert.Contains(t, err.Error(), "teardown boom")
}

func TestComposedDriver_ConcurrentAllocateDeallocate(t *testing.T) {
	e := newFakeEnsurer()
	mint := &fakeMintDriver{}

	const poolSize = 4
	d, err := newComposedDriver("org", mint, e, poolSize, t.Logf)
	require.NoError(t, err)

	const goroutines = 8
	ctx := context.Background()
	errs := make([]error, goroutines)
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			name, allocErr := d.AllocateRepo(ctx)
			if allocErr != nil {
				errs[idx] = allocErr
				return
			}
			// Simulate some work.
			time.Sleep(5 * time.Millisecond)
			errs[idx] = d.DeallocateRepo(ctx, name)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d failed", i)
	}

	// All names should be back in the pool.
	err = d.Finalize(ctx)
	require.NoError(t, err, "no outstanding leases after all deallocations")
}

func TestComposedDriver_FinalizeCollectsLogs(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	mint := &fakeMintDriver{}
	e := newFakeEnsurer()

	d, err := newComposedDriver("org", mint, e, 2, t.Logf)
	require.NoError(t, err)

	err = d.Finalize(context.Background())
	require.NoError(t, err)
	assert.True(t, mint.collectLogsCalled, "CollectLogs should be called during Finalize")
	assert.True(t, mint.teardownCalled, "Teardown should still be called")
}

func TestComposedDriver_FinalizeSkipsLogsWhenArtifactDirUnset(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", "")

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	mint := &fakeMintDriver{}
	e := newFakeEnsurer()

	d, err := newComposedDriver("org", mint, e, 2, logf)
	require.NoError(t, err)

	err = d.Finalize(context.Background())
	require.NoError(t, err)
	assert.False(t, mint.collectLogsCalled, "CollectLogs should not be called when BEHAVIOUR_ARTIFACT_DIR is unset")
	assert.True(t, mint.teardownCalled, "Teardown should still be called")

	// Should log the skip reason.
	found := false
	for _, l := range logged {
		if strings.Contains(l, "BEHAVIOUR_ARTIFACT_DIR unset") || strings.Contains(l, "skipping mint log collection") {
			found = true
			break
		}
	}
	assert.True(t, found, "should log that artifact dir is unset")
}

func TestComposedDriver_FinalizeLogCollectionErrorDoesNotFail(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	mint := &fakeMintDriver{collectLogsErr: fmt.Errorf("log collection exploded")}
	e := newFakeEnsurer()

	d, err := newComposedDriver("org", mint, e, 2, logf)
	require.NoError(t, err)

	// Finalize should NOT return the log collection error.
	err = d.Finalize(context.Background())
	require.NoError(t, err)
	assert.True(t, mint.collectLogsCalled, "CollectLogs should be called")
	assert.True(t, mint.teardownCalled, "Teardown should still be called after log error")

	// The error should be logged.
	found := false
	for _, l := range logged {
		if strings.Contains(l, "log collection exploded") {
			found = true
			break
		}
	}
	assert.True(t, found, "log collection error should be logged")
}

func TestComposedDriver_FinalizeCollectsLogsBeforeTeardown(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	// Track the order of operations.
	var ops []string
	mint := &orderTrackingMintDriver{ops: &ops}
	e := newFakeEnsurer()

	d, err := newComposedDriver("org", mint, e, 2, t.Logf)
	require.NoError(t, err)

	err = d.Finalize(context.Background())
	require.NoError(t, err)

	require.Len(t, ops, 2, "both CollectLogs and Teardown should be called")
	assert.Equal(t, "CollectLogs", ops[0], "CollectLogs should be called first")
	assert.Equal(t, "Teardown", ops[1], "Teardown should be called second")
}

// orderTrackingMintDriver records the order of method calls.
type orderTrackingMintDriver struct {
	ops *[]string
}

func (m *orderTrackingMintDriver) Install(_ context.Context, _ string) (string, error) {
	return "https://mint.test", nil
}

func (m *orderTrackingMintDriver) Teardown(_ context.Context) error {
	*m.ops = append(*m.ops, "Teardown")
	return nil
}

func (m *orderTrackingMintDriver) CollectLogs(_ context.Context, _ time.Time, _ string) error {
	*m.ops = append(*m.ops, "CollectLogs")
	return nil
}

func TestComposedDriver_SuiteStartIsRecorded(t *testing.T) {
	before := time.Now()
	e := newFakeEnsurer()
	d, err := newComposedDriver("org", &fakeMintDriver{}, e, 2, t.Logf)
	after := time.Now()
	require.NoError(t, err)

	cd := d.(*composedDriver)
	assert.False(t, cd.suiteStart.IsZero(), "suiteStart should be set")
	assert.False(t, cd.suiteStart.Before(before), "suiteStart should be >= before")
	assert.False(t, cd.suiteStart.After(after), "suiteStart should be <= after")
}

// failingEnsurer always returns an error.
type failingEnsurer struct {
	err error
}

func (f *failingEnsurer) EnsureRepo(_ context.Context, _, _ string) error {
	return f.err
}

// rateReportingClient is a forge.Client that also reports a fixed
// rate-limit observation.
type rateReportingClient struct {
	forge.Client
	rl   forge.RateLimit
	seen bool
}

func (c *rateReportingClient) RateLimit() (forge.RateLimit, bool) { return c.rl, c.seen }

func TestComposedDriver_SamplesRateLimitOnAllocateAndDeallocate(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	e := newFakeEnsurer()
	d, err := newComposedDriver("org", &fakeMintDriver{}, e, 2, logf)
	require.NoError(t, err)

	// A client without a reporter, or one that has observed nothing yet, samples nothing.
	withRateLimitReporter(d, &rateReportingClient{})
	name, err := d.AllocateRepo(context.Background())
	require.NoError(t, err)
	require.NoError(t, d.DeallocateRepo(context.Background(), name))
	for _, l := range lines {
		assert.NotContains(t, l, "rate limit")
	}

	withRateLimitReporter(d, &rateReportingClient{seen: true, rl: forge.RateLimit{Limit: 5000, Remaining: 42, Reset: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Resource: "core"}})
	lines = nil
	name, err = d.AllocateRepo(context.Background())
	require.NoError(t, err)
	require.NoError(t, d.DeallocateRepo(context.Background(), name))
	assert.Contains(t, lines, "[driver] rate limit after allocating org/"+name+": remaining=42/5000 reset=2026-01-01T00:00:00Z resource=core")
	assert.Contains(t, lines, "[driver] rate limit after deallocating org/"+name+": remaining=42/5000 reset=2026-01-01T00:00:00Z resource=core")
}
