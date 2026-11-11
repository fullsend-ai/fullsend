package entitycache

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/normentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeLabel returns snapshot (or a new document when snapshot is nil) with
// label added to the known labels. It stands in for a caller merging newly
// acquired data into the latest snapshot.
func mergeLabel(snapshot *normentity.Document, label string) *normentity.Document {
	if snapshot == nil {
		snapshot = doc("I_shared", "shared")
	}
	labels := &snapshot.Entity.Labels
	labels.Known = append(labels.Known, label)
	slices.Sort(labels.Known)
	labels.Coverage = normentity.Coverage{Status: normentity.CoveragePartial}
	return snapshot
}

// TestMemoryConcurrentMerges has concurrent clients progressively add their
// own label to the same entity with a Get, merge, Put, retry-on-conflict
// loop. No update may be lost, and the race detector checks shared state.
func TestMemoryConcurrentMerges(t *testing.T) {
	m := newMemory(t, MemoryOptions{})
	id := mustID(t, "I_shared")
	other := mustID(t, "I_other")

	const goroutines = 12
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			label := fmt.Sprintf("label_%02d", g)
			for {
				snapshot, rev, _ := m.Get(id)
				_, err := m.Put(id, mergeLabel(snapshot, label), rev)
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
	want := make([]string, goroutines)
	for g := range goroutines {
		want[g] = fmt.Sprintf("label_%02d", g)
	}
	assert.Equal(t, want, final.Entity.Labels.Known, "an update was lost")
}
