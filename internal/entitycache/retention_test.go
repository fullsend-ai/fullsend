package entitycache

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a manually advanced clock, so retention tests are
// deterministic.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

const testTTL = time.Minute

// newRetained returns a Memory with the given capacity, a one-minute TTL, and
// a fake clock.
func newRetained(t *testing.T, maxEntries int) (*Memory, *fakeClock) {
	t.Helper()
	m := newMemory(t, MemoryOptions{MaxEntries: maxEntries, TTL: testTTL})
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m.now = c.now
	return m, c
}

// put stores a document for sourceID at the entity's current revision.
func put(t *testing.T, m *Memory, sourceID string) Revision {
	t.Helper()
	id := mustID(t, sourceID)
	_, rev, _ := m.Get(id)
	rev, err := m.Put(id, doc(sourceID, sourceID), rev)
	require.NoError(t, err)
	return rev
}

// live reports whether sourceID is cached, without a lookup that would mark
// it as recently used.
func live(m *Memory, t *testing.T, sourceID string) bool {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.records[mustID(t, sourceID)]
	return ok
}

// assertConsistent checks that the lookup map and both orderings track the
// same set of records.
func assertConsistent(t *testing.T, m *Memory) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, len(m.records), m.recency.Len())
	assert.Equal(t, len(m.records), m.expiry.Len())
	assert.LessOrEqual(t, len(m.records), m.maxEntries)
	assert.Less(t, len(m.tombstones), m.maxTombstones)
}

func TestNewMemory_Options(t *testing.T) {
	m := newMemory(t, MemoryOptions{})
	assert.Equal(t, 1024, m.maxEntries)
	assert.Equal(t, 5*time.Minute, m.ttl)
	assert.Equal(t, DefaultMaxEntries, m.maxEntries)
	assert.Equal(t, DefaultTTL, m.ttl)
	assert.Equal(t, DefaultTTL, m.ttl)

	m = newMemory(t, MemoryOptions{MaxEntries: 7, TTL: time.Second})
	assert.Equal(t, 7, m.maxEntries)
	assert.Equal(t, time.Second, m.ttl)

	m = newMemory(t, MemoryOptions{MaxEntries: 7})
	assert.Equal(t, DefaultTTL, m.ttl)
	m = newMemory(t, MemoryOptions{TTL: time.Second})
	assert.Equal(t, DefaultMaxEntries, m.maxEntries)

	for name, opts := range map[string]MemoryOptions{
		"negative entries": {MaxEntries: -1},
		"negative TTL":     {TTL: -time.Nanosecond},
	} {
		m, err := NewMemory(opts)
		require.ErrorIs(t, err, ErrInvalidOptions, name)
		assert.Nil(t, m, name)
	}
}

func TestExpiry_IsAMissAtAnAdvancedRevision(t *testing.T) {
	m, c := newRetained(t, 4)
	id := mustID(t, "I_1")
	_, r0, _ := m.Get(id)
	r1 := put(t, m, "I_1")

	// Just before the TTL elapses the entity is still live.
	c.advance(testTTL - time.Nanosecond)
	got, rev, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, r1, rev)
	assert.Equal(t, "I_1", titleOf(t, got))

	// Expiry is absolute: the Get above did not extend it.
	c.advance(time.Nanosecond)
	got, r2, ok := m.Get(id)
	assert.False(t, ok)
	assert.Nil(t, got)
	assert.Greater(t, r2, r1)
	assertConsistent(t, m)

	// A Put based on any pre-expiry snapshot conflicts and restores nothing.
	for _, stale := range []Revision{r0, r1} {
		cur, err := m.Put(id, doc("I_1", "stale"), stale)
		require.ErrorIs(t, err, ErrConflict, "revision %d", stale)
		assert.Equal(t, r2, cur)
	}
	_, _, ok = m.Get(id)
	assert.False(t, ok)

	// Retrying from the miss revision succeeds.
	r3, err := m.Put(id, doc("I_1", "fresh"), r2)
	require.NoError(t, err)
	assert.Greater(t, r3, r2)
}

func TestExpiry_PutConflictsWithoutAPriorGet(t *testing.T) {
	m, c := newRetained(t, 4)
	id := mustID(t, "I_1")
	r1 := put(t, m, "I_1")

	c.advance(testTTL)
	// The expired entry is reclaimed by the Put itself.
	cur, err := m.Put(id, doc("I_1", "stale"), r1)
	require.ErrorIs(t, err, ErrConflict)
	assert.Greater(t, cur, r1)
	assert.False(t, live(m, t, "I_1"))
}

func TestExpiry_PutRestartsTheTTL(t *testing.T) {
	m, c := newRetained(t, 4)
	id := mustID(t, "I_1")
	r1 := put(t, m, "I_1")

	c.advance(testTTL - time.Second)
	r2, err := m.Put(id, doc("I_1", "again"), r1)
	require.NoError(t, err)

	// Past the first Put's expiry, but within the second's.
	c.advance(testTTL - time.Second)
	got, rev, ok := m.Get(id)
	require.True(t, ok)
	assert.Equal(t, r2, rev)
	assert.Equal(t, "again", titleOf(t, got))

	c.advance(time.Second)
	_, _, ok = m.Get(id)
	assert.False(t, ok)
}

func TestExpiry_FailedPutDoesNotRestartTheTTL(t *testing.T) {
	m, c := newRetained(t, 4)
	id := mustID(t, "I_1")
	r1 := put(t, m, "I_1")

	c.advance(testTTL - time.Second)
	_, err := m.Put(id, doc("I_1", "conflict"), r1+100)
	require.ErrorIs(t, err, ErrConflict)
	_, err = m.Put(id, doc("I_2", "invalid"), r1)
	require.ErrorIs(t, err, ErrInvalidEntity)

	c.advance(time.Second)
	_, _, ok := m.Get(id)
	assert.False(t, ok, "only a successful Put restarts the TTL")
}

func TestExpiry_ReclaimedDuringOtherOperations(t *testing.T) {
	for name, op := range map[string]func(m *Memory, t *testing.T){
		"Get of another key": func(m *Memory, t *testing.T) { m.Get(mustID(t, "I_other")) },
		"Put of another key": func(m *Memory, t *testing.T) { put(t, m, "I_other") },
		"Invalidate of another key": func(m *Memory, t *testing.T) {
			_, err := m.Invalidate(mustID(t, "I_other"))
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, c := newRetained(t, 8)
			put(t, m, "I_1")
			c.advance(testTTL / 2)
			put(t, m, "I_2")
			c.advance(testTTL / 2) // I_1 expired, I_2 not yet

			op(m, t)
			assert.False(t, live(m, t, "I_1"), "the expired entity is reclaimed")
			assert.True(t, live(m, t, "I_2"))
			assertConsistent(t, m)
		})
	}
}

func TestExpiry_ReclaimsEveryExpiredEntityInOrder(t *testing.T) {
	m, c := newRetained(t, 8)
	put(t, m, "I_1")
	c.advance(10 * time.Second)
	put(t, m, "I_2")
	c.advance(10 * time.Second)
	put(t, m, "I_3")

	// Recency is the reverse of expiry order for I_1, which expires first
	// even though a Get makes it the most recently used.
	_, _, ok := m.Get(mustID(t, "I_1"))
	require.True(t, ok)

	c.advance(testTTL - 20*time.Second) // I_1 expires now
	_, _, ok = m.Get(mustID(t, "I_3"))
	assert.True(t, ok)
	assert.False(t, live(m, t, "I_1"))
	assert.True(t, live(m, t, "I_2"))

	c.advance(20 * time.Second) // all expired
	_, _, ok = m.Get(mustID(t, "I_none"))
	assert.False(t, ok)
	for _, s := range []string{"I_1", "I_2", "I_3"} {
		assert.False(t, live(m, t, s), s)
	}
	assertConsistent(t, m)
}

func TestInvalidate_OfAnExpiredEntityStillAdvances(t *testing.T) {
	m, c := newRetained(t, 4)
	id := mustID(t, "I_1")
	r1 := put(t, m, "I_1")
	c.advance(testTTL)

	r2, err := m.Invalidate(id)
	require.NoError(t, err)
	assert.Greater(t, r2, r1)
	_, rev, ok := m.Get(id)
	assert.False(t, ok)
	assert.Equal(t, r2, rev)
}

func TestLRU_EvictsTheLeastRecentlyUsed(t *testing.T) {
	m, _ := newRetained(t, 3)
	put(t, m, "I_1")
	put(t, m, "I_2")
	put(t, m, "I_3")

	// A Get makes I_1 the most recently used, leaving I_2 the oldest.
	_, _, ok := m.Get(mustID(t, "I_1"))
	require.True(t, ok)
	put(t, m, "I_4")
	assert.True(t, live(m, t, "I_1"))
	assert.False(t, live(m, t, "I_2"))
	assert.True(t, live(m, t, "I_3"))
	assert.True(t, live(m, t, "I_4"))

	// A successful Put to an existing entity also refreshes it, leaving I_3
	// the oldest.
	id1 := mustID(t, "I_1")
	_, r, _ := m.Get(id1)
	_, err := m.Put(id1, doc("I_1", "updated"), r)
	require.NoError(t, err)
	_, _, ok = m.Get(mustID(t, "I_4"))
	require.True(t, ok)
	put(t, m, "I_5")
	assert.False(t, live(m, t, "I_3"))
	for _, s := range []string{"I_1", "I_4", "I_5"} {
		assert.True(t, live(m, t, s), s)
	}
	assertConsistent(t, m)
}

func TestLRU_MissesAndFailedPutsDoNotRefresh(t *testing.T) {
	m, _ := newRetained(t, 2)
	put(t, m, "I_1")
	put(t, m, "I_2")
	id1 := mustID(t, "I_1")
	_, r1, _ := m.Get(id1)            // I_1 most recent
	_, _, _ = m.Get(mustID(t, "I_2")) // I_2 most recent, I_1 oldest

	// Conflicting and invalid Puts to I_1 must not refresh it.
	_, err := m.Put(id1, doc("I_1", "x"), r1-1)
	require.ErrorIs(t, err, ErrConflict)
	_, err = m.Put(id1, doc("I_2", "x"), r1)
	require.ErrorIs(t, err, ErrInvalidEntity)
	_, err = m.Put(id1, nil, r1)
	require.ErrorIs(t, err, ErrInvalidEntity)
	// Nor does a rejected Put of a new key evict anything.
	_, err = m.Put(mustID(t, "I_3"), doc("I_3", "x"), 99)
	require.ErrorIs(t, err, ErrConflict)
	assert.True(t, live(m, t, "I_1"))
	assert.True(t, live(m, t, "I_2"))

	put(t, m, "I_4")
	assert.False(t, live(m, t, "I_1"), "the failed Puts left I_1 least recently used")
	assert.True(t, live(m, t, "I_2"))
}

func TestCapacity_Boundaries(t *testing.T) {
	t.Run("filling to capacity evicts nothing", func(t *testing.T) {
		m, _ := newRetained(t, 3)
		for i := range 3 {
			put(t, m, fmt.Sprintf("I_%d", i))
		}
		for i := range 3 {
			assert.True(t, live(m, t, fmt.Sprintf("I_%d", i)))
		}
		assertConsistent(t, m)
	})

	t.Run("one past capacity evicts exactly one", func(t *testing.T) {
		m, _ := newRetained(t, 3)
		for i := range 4 {
			put(t, m, fmt.Sprintf("I_%d", i))
		}
		assert.False(t, live(m, t, "I_0"))
		for i := 1; i < 4; i++ {
			assert.True(t, live(m, t, fmt.Sprintf("I_%d", i)))
		}
		assertConsistent(t, m)
	})

	t.Run("updating an existing entity at capacity evicts nothing", func(t *testing.T) {
		m, _ := newRetained(t, 2)
		put(t, m, "I_1")
		put(t, m, "I_2")
		id := mustID(t, "I_1")
		_, r, _ := m.Get(id)
		_, err := m.Put(id, doc("I_1", "updated"), r)
		require.NoError(t, err)
		assert.True(t, live(m, t, "I_1"))
		assert.True(t, live(m, t, "I_2"))
	})

	t.Run("capacity one", func(t *testing.T) {
		m, _ := newRetained(t, 1)
		put(t, m, "I_1")
		put(t, m, "I_2")
		assert.False(t, live(m, t, "I_1"))
		assert.True(t, live(m, t, "I_2"))
		assertConsistent(t, m)
	})

	t.Run("invalidation frees capacity", func(t *testing.T) {
		m, _ := newRetained(t, 2)
		put(t, m, "I_1")
		put(t, m, "I_2")
		_, err := m.Invalidate(mustID(t, "I_1"))
		require.NoError(t, err)
		put(t, m, "I_3")
		assert.True(t, live(m, t, "I_2"), "no live entity was evicted")
		assert.True(t, live(m, t, "I_3"))
	})
}

func TestCapacity_ReclaimsExpiredBeforeEvictingLive(t *testing.T) {
	m, c := newRetained(t, 3)
	put(t, m, "I_1") // oldest, expires first
	c.advance(testTTL / 2)
	put(t, m, "I_2")
	put(t, m, "I_3")
	// I_1 is also the least recently used. Make it the most recent so an
	// LRU-only policy would evict I_2 instead.
	_, _, ok := m.Get(mustID(t, "I_1"))
	require.True(t, ok)
	c.advance(testTTL / 2) // I_1 expired; I_2 and I_3 live

	// Put directly, with no earlier Get, so the Put itself must reclaim.
	_, err := m.Put(mustID(t, "I_4"), doc("I_4", "I_4"), 0)
	require.NoError(t, err)
	assert.False(t, live(m, t, "I_1"))
	for _, s := range []string{"I_2", "I_3", "I_4"} {
		assert.True(t, live(m, t, s), "live entity %s must not be evicted", s)
	}
	assertConsistent(t, m)
}

func TestEviction_StaleCASWriteConflicts(t *testing.T) {
	m, _ := newRetained(t, 1)
	id := mustID(t, "I_1")
	_, r0, _ := m.Get(id)
	r1 := put(t, m, "I_1")

	put(t, m, "I_2") // evicts I_1
	_, r2, ok := m.Get(id)
	assert.False(t, ok)
	assert.Greater(t, r2, r1)

	for _, stale := range []Revision{r0, r1} {
		cur, err := m.Put(id, doc("I_1", "stale"), stale)
		require.ErrorIs(t, err, ErrConflict, "revision %d", stale)
		assert.Equal(t, r2, cur)
	}
	assert.False(t, live(m, t, "I_1"), "stale data must not be restored")
	assert.True(t, live(m, t, "I_2"), "a conflicting Put must not evict")

	// A caller that re-reads the miss revision can recreate the entity.
	_, err := m.Put(id, doc("I_1", "fresh"), r2)
	require.NoError(t, err)
	assert.True(t, live(m, t, "I_1"))
}

func TestRemoval_TombstonesStayBounded(t *testing.T) {
	m, c := newRetained(t, 2)
	m.maxTombstones = 5

	// Evictions and expiries each leave a tombstone, and compaction keeps
	// their count below the limit.
	first := mustID(t, "I_0")
	_, rFirst, _ := m.Get(first)
	rFirst1 := put(t, m, "I_0")
	for i := 1; i < 40; i++ {
		put(t, m, fmt.Sprintf("I_%d", i))
		if i%7 == 0 {
			c.advance(testTTL)
		}
		assertConsistent(t, m)
	}

	// Snapshots taken before the compaction are still stale.
	for _, stale := range []Revision{rFirst, rFirst1} {
		_, err := m.Put(first, doc("I_0", "stale"), stale)
		require.ErrorIs(t, err, ErrConflict, "revision %d", stale)
	}
}

func TestRemoval_DoesNotAffectOtherEntities(t *testing.T) {
	m, c := newRetained(t, 2)
	put(t, m, "I_1")
	c.advance(testTTL / 2)
	idB := mustID(t, "I_2")
	rB := put(t, m, "I_2")
	c.advance(testTTL / 2) // I_1 expires

	put(t, m, "I_3")
	got, rev, ok := m.Get(idB)
	require.True(t, ok)
	assert.Equal(t, rB, rev, "removing another entity leaves this revision unchanged")
	assert.Equal(t, "I_2", titleOf(t, got))
}
