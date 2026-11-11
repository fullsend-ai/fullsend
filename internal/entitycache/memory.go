package entitycache

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// defaultMaxTombstones bounds how many per-entity invalidation records a
// [Memory] retains before it collapses them into its shared absent revision.
const defaultMaxTombstones = 4096

// record is the cached state of one present entity. The document is held in
// its serialized form, which no caller can alias: Put encodes it and Get
// decodes a fresh copy, so every caller owns the document it gets back.
type record struct {
	doc []byte
	rev Revision
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
	records       map[normentity.ID]record
	tombstones    map[normentity.ID]Revision
	absent        Revision // revision of absent entities without a tombstone
	last          Revision // last revision issued
	maxTombstones int
}

var _ Cache = (*Memory)(nil)

// NewMemory returns an empty in-memory cache.
func NewMemory() *Memory {
	return &Memory{
		records:       make(map[normentity.ID]record),
		tombstones:    make(map[normentity.ID]Revision),
		maxTombstones: defaultMaxTombstones,
	}
}

// Get implements [Cache]. A stored record that cannot be decoded is reported
// as a miss at its current revision, so the caller can replace it with Put;
// Put only stores documents that encode, so this does not occur in practice.
func (m *Memory) Get(id normentity.ID) (*normentity.Document, Revision, bool) {
	if !id.Valid() {
		return nil, 0, false
	}

	m.mu.Lock()
	r, ok := m.records[id]
	rev := m.currentLocked(id)
	m.mu.Unlock()
	if !ok {
		return nil, rev, false
	}
	var doc normentity.Document
	if err := json.Unmarshal(r.doc, &doc); err != nil {
		return nil, rev, false
	}
	return &doc, rev, true
}

// Put implements [Cache].
func (m *Memory) Put(id normentity.ID, doc *normentity.Document, expected Revision) (Revision, error) {
	if !id.Valid() {
		return 0, ErrInvalidID
	}
	stored, err := encode(id, doc)
	if err != nil {
		return 0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.currentLocked(id); cur != expected {
		return cur, ErrConflict
	}
	rev := m.nextLocked()
	m.records[id] = record{doc: stored, rev: rev}
	delete(m.tombstones, id)
	return rev, nil
}

// encode validates doc against id and serializes it.
func encode(id normentity.ID, doc *normentity.Document) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("%w: no document", ErrInvalidEntity)
	}
	if doc.Entity.ID != id {
		return nil, fmt.Errorf("%w: entity.id %q does not match key %q", ErrInvalidEntity, doc.Entity.ID, id)
	}
	if err := doc.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEntity, err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEntity, err)
	}
	return b, nil
}

// Invalidate implements [Cache].
func (m *Memory) Invalidate(id normentity.ID) (Revision, error) {
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
func (m *Memory) currentLocked(id normentity.ID) Revision {
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
