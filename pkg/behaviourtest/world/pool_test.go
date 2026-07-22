package world

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRepoPool_RejectsZero(t *testing.T) {
	_, err := NewRepoPool(0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive")
}

func TestNewRepoPool_RejectsNegative(t *testing.T) {
	_, err := NewRepoPool(-1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive")
}

func TestRepoPool_AcquireReturnsDistinctNames(t *testing.T) {
	pool, err := NewRepoPool(12)
	require.NoError(t, err)

	ctx := context.Background()
	seen := make(map[string]bool)
	for range 12 {
		name, err := pool.Acquire(ctx)
		require.NoError(t, err)
		assert.False(t, seen[name], "duplicate name: %s", name)
		seen[name] = true
	}
	assert.Len(t, seen, 12)

	// Verify naming format.
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	assert.Equal(t, "test-repo-01", names[0])
	assert.Equal(t, "test-repo-12", names[11])
}

func TestRepoPool_AcquireBlocksWhenExhausted(t *testing.T) {
	pool, err := NewRepoPool(1)
	require.NoError(t, err)

	ctx := context.Background()
	name, err := pool.Acquire(ctx)
	require.NoError(t, err)

	// Pool is now empty. Acquire with a short timeout should fail.
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = pool.Acquire(shortCtx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Release and acquire should succeed.
	pool.Release(name)
	got, err := pool.Acquire(ctx)
	require.NoError(t, err)
	assert.Equal(t, name, got)
}

func TestRepoPool_ReleaseMakesNameAvailable(t *testing.T) {
	pool, err := NewRepoPool(2)
	require.NoError(t, err)

	ctx := context.Background()
	n1, err := pool.Acquire(ctx)
	require.NoError(t, err)
	n2, err := pool.Acquire(ctx)
	require.NoError(t, err)

	pool.Release(n1)
	pool.Release(n2)

	// Both names should be available again.
	got1, err := pool.Acquire(ctx)
	require.NoError(t, err)
	got2, err := pool.Acquire(ctx)
	require.NoError(t, err)

	returned := []string{got1, got2}
	sort.Strings(returned)
	expected := []string{n1, n2}
	sort.Strings(expected)
	assert.Equal(t, expected, returned)
}

func TestRepoPool_ConcurrentAcquireRelease(t *testing.T) {
	pool, err := NewRepoPool(3)
	require.NoError(t, err)

	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := make([]string, 0)

	for range 10 {
		wg.Go(func() {
			name, err := pool.Acquire(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			acquired = append(acquired, name)
			mu.Unlock()
			// Simulate work.
			time.Sleep(5 * time.Millisecond)
			pool.Release(name)
		})
	}
	wg.Wait()
	assert.Len(t, acquired, 10)
}

func TestRepoPool_Size(t *testing.T) {
	pool, err := NewRepoPool(5)
	require.NoError(t, err)
	assert.Equal(t, 5, pool.Size())
}
