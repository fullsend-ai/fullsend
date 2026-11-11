package entitycache

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeField decodes snapshot (or starts a new document for id when snapshot
// is nil), sets entity.<field> to true, and re-encodes it. It stands in for a
// caller merging newly acquired data into the latest snapshot.
func mergeField(id EntityID, snapshot Entity, field string) (Entity, error) {
	var d struct {
		Entity map[string]any `json:"entity"`
	}
	if snapshot != nil {
		if err := json.Unmarshal(snapshot, &d); err != nil {
			return nil, err
		}
	}
	if d.Entity == nil {
		d.Entity = map[string]any{"id": string(id)}
	}
	d.Entity[field] = true
	return json.Marshal(d)
}

// TestMemoryConcurrentMerges has concurrent clients progressively add their
// own field to the same entity with a Get, merge, Put, retry-on-conflict loop.
// No update may be lost, and the race detector checks shared state.
func TestMemoryConcurrentMerges(t *testing.T) {
	m := NewMemory()
	id := mustEntityID(t, "I_shared")
	other := mustEntityID(t, "I_other")

	const goroutines = 12
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			field := fmt.Sprintf("field_%d", g)
			for {
				snapshot, rev, _ := m.Get(id)
				merged, err := mergeField(id, snapshot, field)
				if !assert.NoError(t, err) {
					return
				}
				_, err = m.Put(id, merged, rev)
				if err == nil {
					break
				}
				if !assert.True(t, errors.Is(err, ErrConflict), "unexpected error: %v", err) {
					return
				}
			}
			// Unrelated churn on another key exercises Invalidate concurrently.
			_, _ = m.Invalidate(other)
		})
	}
	wg.Wait()

	final, _, ok := m.Get(id)
	require.True(t, ok)
	var d struct {
		Entity map[string]any `json:"entity"`
	}
	require.NoError(t, json.Unmarshal(final, &d))
	for g := range goroutines {
		assert.Equal(t, true, d.Entity[fmt.Sprintf("field_%d", g)], "field_%d lost", g)
	}
}
