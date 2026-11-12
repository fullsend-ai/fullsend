package entitycache

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/normentity"
)

const (
	// DefaultMaxEntries is the number of live entities a [Memory] retains when
	// [MemoryOptions.MaxEntries] is zero.
	DefaultMaxEntries = 1024
	// DefaultTTL is how long a [Memory] retains an entity after its last
	// successful Put when [MemoryOptions.TTL] is zero.
	DefaultTTL = 5 * time.Minute
)

// defaultMaxTombstones bounds how many per-entity removal records a
// [Memory] retains before it collapses them into its shared absent revision.
const defaultMaxTombstones = 4096

// ErrInvalidOptions reports [MemoryOptions] with a negative value.
var ErrInvalidOptions = errors.New("entitycache: invalid memory options")

// MemoryOptions configures the retention of a [Memory]. The zero value
// selects the defaults.
type MemoryOptions struct {
	// MaxEntries is the most live entities retained. Inserting a new entity
	// beyond it evicts the least recently used one. Zero selects
	// [DefaultMaxEntries]; a negative value is invalid.
	MaxEntries int
	// TTL is how long an entity is retained after its last successful Put.
	// The expiry is absolute: Get does not extend it. Zero selects
	// [DefaultTTL]; a negative value is invalid.
	TTL time.Duration
}

// record is the cached state of one present entity. The document is held in
// its serialized form, which no caller can alias: Put encodes it and Get
// decodes a fresh copy, so every caller owns the document it gets back.
type record struct {
	id      normentity.ID
	doc     []byte
	rev     Revision
	expires time.Time
	lru     *list.Element // in Memory.recency
	ttl     *list.Element // in Memory.expiry
}

// Memory is a thread-safe, process-local [Cache] with bounded retention.
//
// An entity is retained until it is invalidated, expires TTL after its last
// successful Put, or is evicted as the least recently used entity to make
// room for a new one. Successful Gets and Puts mark an entity as recently
// used. Expired entities are treated as misses and are reclaimed by the next
// cache operation; no background goroutine runs.
//
// Revisions come from a single counter shared by every entity, so an entity's
// revision never repeats. An absent entity's revision is its tombstone
// revision when it was invalidated, expired, or evicted, and otherwise the
// shared absent revision. Tombstones keep a Put based on a pre-removal
// snapshot from restoring stale data. To bound memory, once the tombstone
// count reaches its limit every tombstone is dropped and the shared absent
// revision advances past all revisions issued so far; absent entities then
// report that new revision, so earlier snapshots of them still conflict. The
// cost is that a pending miss-time Put for an unrelated absent entity may
// also conflict and retry.
type Memory struct {
	mu         sync.Mutex
	records    map[normentity.ID]*record
	recency    *list.List // *record, most recently used first
	expiry     *list.List // *record, soonest expiry first (oldest Put first)
	tombstones map[normentity.ID]Revision
	absent     Revision // revision of absent entities without a tombstone
	last       Revision // last revision issued

	maxEntries    int
	ttl           time.Duration
	maxTombstones int
	now           func() time.Time // replaced in tests
}

var _ Cache = (*Memory)(nil)

// NewMemory returns an empty in-memory cache. It returns
// [ErrInvalidOptions] when opts has a negative value.
func NewMemory(opts MemoryOptions) (*Memory, error) {
	if opts.MaxEntries < 0 {
		return nil, fmt.Errorf("%w: MaxEntries %d is negative", ErrInvalidOptions, opts.MaxEntries)
	}
	if opts.TTL < 0 {
		return nil, fmt.Errorf("%w: TTL %v is negative", ErrInvalidOptions, opts.TTL)
	}
	if opts.MaxEntries == 0 {
		opts.MaxEntries = DefaultMaxEntries
	}
	if opts.TTL == 0 {
		opts.TTL = DefaultTTL
	}
	return &Memory{
		records:       make(map[normentity.ID]*record),
		recency:       list.New(),
		expiry:        list.New(),
		tombstones:    make(map[normentity.ID]Revision),
		maxEntries:    opts.MaxEntries,
		ttl:           opts.TTL,
		maxTombstones: defaultMaxTombstones,
		now:           time.Now,
	}, nil
}

// Get implements [Cache]. An expired entity is a miss. A stored record that
// cannot be decoded is reported as a miss at its current revision, so the
// caller can replace it with Put; Put only stores documents that encode, so
// this does not occur in practice.
func (m *Memory) Get(id normentity.ID) (*normentity.Document, Revision, bool) {
	if !id.Valid() {
		return nil, 0, false
	}

	m.mu.Lock()
	m.reclaimExpiredLocked()
	r, ok := m.records[id]
	rev := m.currentLocked(id)
	var stored []byte
	if ok {
		m.recency.MoveToFront(r.lru)
		// Put replaces r.doc rather than modifying the bytes, so this
		// slice stays valid to decode after the lock is released.
		stored = r.doc
	}
	m.mu.Unlock()
	if !ok {
		return nil, rev, false
	}
	var doc normentity.Document
	if err := json.Unmarshal(stored, &doc); err != nil {
		return nil, rev, false
	}
	return &doc, rev, true
}

// Put implements [Cache]. A successful Put restarts the entity's TTL and
// marks it as most recently used. Replacing an existing entity never evicts
// another. Inserting a new entity into a full cache reclaims expired entities
// first and then evicts the least recently used one. A failed Put changes
// neither recency nor expiry.
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
	now := m.reclaimExpiredLocked()
	if cur := m.currentLocked(id); cur != expected {
		return cur, ErrConflict
	}

	r, ok := m.records[id]
	if !ok {
		if len(m.records) >= m.maxEntries {
			m.removeLocked(m.recency.Back().Value.(*record))
		}
		r = &record{id: id}
		r.lru = m.recency.PushFront(r)
		r.ttl = m.expiry.PushBack(r)
		m.records[id] = r
		delete(m.tombstones, id)
	} else {
		m.recency.MoveToFront(r.lru)
		m.expiry.MoveToBack(r.ttl)
	}
	r.doc = stored
	r.rev = m.nextLocked()
	r.expires = now.Add(m.ttl)
	return r.rev, nil
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
	m.reclaimExpiredLocked()
	if r, ok := m.records[id]; ok {
		m.unlinkLocked(r)
	}
	return m.tombstoneLocked(id), nil
}

// reclaimExpiredLocked removes every expired entity, advancing each one's
// revision, and returns the time it used. Every entity has the same TTL, so
// the expiry list is ordered by expiry time and only its expired prefix is
// visited. m.mu must be held.
func (m *Memory) reclaimExpiredLocked() time.Time {
	now := m.now()
	for e := m.expiry.Front(); e != nil; e = m.expiry.Front() {
		r := e.Value.(*record)
		if now.Before(r.expires) {
			break
		}
		m.removeLocked(r)
	}
	return now
}

// removeLocked drops a live entity as expired or evicted and advances its
// revision, so a Put based on an earlier snapshot conflicts. m.mu must be
// held.
func (m *Memory) removeLocked(r *record) {
	m.unlinkLocked(r)
	m.tombstoneLocked(r.id)
}

// unlinkLocked removes r from the live set without issuing a revision.
// m.mu must be held.
func (m *Memory) unlinkLocked(r *record) {
	delete(m.records, r.id)
	m.recency.Remove(r.lru)
	m.expiry.Remove(r.ttl)
}

// tombstoneLocked advances the revision of the absent entity id and returns
// it. When the tombstone limit is reached it collapses all tombstones into
// the shared absent revision. m.mu must be held.
func (m *Memory) tombstoneLocked(id normentity.ID) Revision {
	rev := m.nextLocked()
	m.tombstones[id] = rev
	if len(m.tombstones) >= m.maxTombstones {
		// Every tombstone revision is at most rev, so advancing the shared
		// absent revision past it keeps all earlier snapshots stale.
		clear(m.tombstones)
		m.absent = m.nextLocked()
		rev = m.absent
	}
	return rev
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
