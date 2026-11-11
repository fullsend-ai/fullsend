package entitycache

import (
	"bytes"
	"sync"
)

// defaultMaxTombstones bounds how many per-entity invalidation records a
// [Memory] retains before it collapses them into its shared absent revision.
const defaultMaxTombstones = 4096

// record is the cached state of one present entity.
type record struct {
	entity Entity
	rev    Revision
}

// Memory is a thread-safe, process-local [Cache].
//
// Revisions come from a single counter shared by every entity, so an entity's
// revision never repeats. An absent entity's revision is its tombstone
// revision when it was invalidated, and otherwise the shared absent revision.
// Tombstones keep a Put based on a pre-invalidation snapshot from restoring
// stale data. To bound memory, once the tombstone count reaches its limit
// every tombstone is dropped and the shared absent revision advances past all
// revisions issued so far; absent entities then report that new revision, so
// earlier snapshots of them still conflict. The cost is that a pending
// miss-time Put for an unrelated absent entity may also conflict and retry.
type Memory struct {
	mu            sync.Mutex
	records       map[EntityID]record
	tombstones    map[EntityID]Revision
	absent        Revision // revision of absent entities without a tombstone
	last          Revision // last revision issued
	maxTombstones int
}

var _ Cache = (*Memory)(nil)

// NewMemory returns an empty in-memory cache.
func NewMemory() *Memory {
	return &Memory{
		records:       make(map[EntityID]record),
		tombstones:    make(map[EntityID]Revision),
		maxTombstones: defaultMaxTombstones,
	}
}

// Get implements [Cache].
func (m *Memory) Get(id EntityID) (Entity, Revision, bool) {
	if !id.Valid() {
		return nil, 0, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.records[id]; ok {
		return bytes.Clone(r.entity), r.rev, true
	}
	return nil, m.currentLocked(id), false
}

// Put implements [Cache].
func (m *Memory) Put(id EntityID, entity Entity, expected Revision) (Revision, error) {
	if !id.Valid() {
		return 0, ErrInvalidID
	}
	if err := validateEntity(id, entity); err != nil {
		return 0, err
	}
	stored := bytes.Clone(entity)

	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.currentLocked(id); cur != expected {
		return cur, ErrConflict
	}
	rev := m.nextLocked()
	m.records[id] = record{entity: stored, rev: rev}
	delete(m.tombstones, id)
	return rev, nil
}

// Invalidate implements [Cache].
func (m *Memory) Invalidate(id EntityID) (Revision, error) {
	if !id.Valid() {
		return 0, ErrInvalidID
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, id)
	rev := m.nextLocked()
	m.tombstones[id] = rev
	if len(m.tombstones) >= m.maxTombstones {
		// Every tombstone revision is at most rev, so advancing the shared
		// absent revision past it keeps all earlier snapshots stale.
		clear(m.tombstones)
		m.absent = m.nextLocked()
		rev = m.absent
	}
	return rev, nil
}

// currentLocked returns id's current revision. m.mu must be held.
func (m *Memory) currentLocked(id EntityID) Revision {
	if r, ok := m.records[id]; ok {
		return r.rev
	}
	if rev, ok := m.tombstones[id]; ok {
		return rev
	}
	return m.absent
}

// nextLocked issues a new revision. m.mu must be held.
func (m *Memory) nextLocked() Revision {
	m.last++
	return m.last
}
