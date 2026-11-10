package actorcache

import (
	"sync"
	"time"
)

// minSweepThreshold is the smallest entry count that triggers pruning of
// expired entries and invalidation records during Put.
const minSweepThreshold = 64

// Memory is a thread-safe, process-local [Cache].
type Memory struct {
	maxAge time.Duration
	now    func() time.Time // trusted clock; tests override it

	mu          sync.Mutex
	entries     map[ActorID]map[Key]Entry
	invalidated map[ActorID]time.Time // most recent invalidation per actor
	size        int                   // total entries across all actors
	sweepAt     int                   // size at which Put prunes expired state
}

var _ Cache = (*Memory)(nil)

// NewMemory returns an in-memory cache whose entries expire maxAge after
// their lookup start. maxAge is the platform policy's maximum age: a value
// above [HardMaxAge] is clamped to it, and a non-positive value disables
// cross-check reuse so every Get misses and Put stores nothing.
func NewMemory(maxAge time.Duration) *Memory {
	if maxAge > HardMaxAge {
		maxAge = HardMaxAge
	}
	if maxAge < 0 {
		maxAge = 0
	}
	return &Memory{
		maxAge:      maxAge,
		now:         time.Now,
		entries:     make(map[ActorID]map[Key]Entry),
		invalidated: make(map[ActorID]time.Time),
		sweepAt:     minSweepThreshold,
	}
}

func (m *Memory) enabled() bool { return m.maxAge > 0 }

// usable reports whether e can be served at now: its age must be known
// (lookup start not in the future) and below the maximum age.
func (m *Memory) usable(e Entry, now time.Time) bool {
	return !now.Before(e.LookupStart) && now.Before(e.LookupStart.Add(m.maxAge))
}

// Get implements [Cache].
func (m *Memory) Get(key Key) (Entry, bool) {
	if !m.enabled() || key.validate() != nil {
		return Entry{}, false
	}
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	scopes := m.entries[key.Actor]
	e, ok := scopes[key]
	if !ok {
		return Entry{}, false
	}
	if !m.usable(e, now) {
		m.deleteLocked(key)
		return Entry{}, false
	}
	return e, true
}

// Put implements [Cache].
func (m *Memory) Put(key Key, entry Entry) error {
	if err := key.validate(); err != nil {
		return err
	}
	if err := entry.validate(key.Kind); err != nil {
		return err
	}
	if !m.enabled() {
		return nil
	}
	now := m.now()
	if now.Before(entry.LookupStart) {
		return ErrUncertainAge
	}
	if !m.usable(entry, now) {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.invalidated[key.Actor]; ok && !entry.LookupStart.After(t) {
		return ErrInvalidated
	}
	scopes := m.entries[key.Actor]
	if scopes == nil {
		scopes = make(map[Key]Entry)
		m.entries[key.Actor] = scopes
	}
	if prev, ok := scopes[key]; ok {
		if prev.LookupStart.After(entry.LookupStart) {
			// Keep the fresher verdict; an older lookup that finished late
			// must not replace it.
			return nil
		}
	} else {
		m.size++
	}
	scopes[key] = entry
	if m.size >= m.sweepAt {
		m.sweepLocked(now)
	}
	return nil
}

// Invalidate implements [Cache].
func (m *Memory) Invalidate(actor ActorID) {
	if !m.enabled() {
		return
	}
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.size -= len(m.entries[actor])
	delete(m.entries, actor)
	if t, ok := m.invalidated[actor]; !ok || now.After(t) {
		m.invalidated[actor] = now
	}
}

// deleteLocked removes the existing entry for key. m.mu must be held.
func (m *Memory) deleteLocked(key Key) {
	scopes := m.entries[key.Actor]
	delete(scopes, key)
	m.size--
	if len(scopes) == 0 {
		delete(m.entries, key.Actor)
	}
}

// sweepLocked prunes expired entries and invalidation records that can no
// longer reject a storable verdict, then raises the next sweep threshold so
// pruning stays amortized. m.mu must be held.
func (m *Memory) sweepLocked(now time.Time) {
	for actor, scopes := range m.entries {
		for key, e := range scopes {
			if !m.usable(e, now) {
				delete(scopes, key)
				m.size--
			}
		}
		if len(scopes) == 0 {
			delete(m.entries, actor)
		}
	}
	// A verdict whose lookup started at or before t expires by t+maxAge,
	// so Put rejects it as expired before consulting this record.
	for actor, t := range m.invalidated {
		if !now.Before(t.Add(m.maxAge)) {
			delete(m.invalidated, actor)
		}
	}
	m.sweepAt = max(2*m.size, minSweepThreshold)
}
