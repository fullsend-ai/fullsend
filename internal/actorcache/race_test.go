package actorcache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// TestMemoryConcurrentAccess exercises Get, Put, and Invalidate on a shared
// Memory so the race detector can catch unsynchronized state.
func TestMemoryConcurrentAccess(t *testing.T) {
	m := NewMemory(HardMaxAge)
	base := humanKey(t)

	const goroutines = 12
	// Build the actor IDs on the test goroutine: mustActorID asserts with
	// require, which must not run in spawned goroutines.
	actors := make([]ActorID, 3)
	for i := range actors {
		actors[i] = mustActorID(t, fmt.Sprintf("actor-%d", i))
	}
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			key := base
			key.Actor = actors[g%len(actors)]
			for i := range 100 {
				key.Repository = fmt.Sprintf("R_%d", i%10)
				_ = m.Put(key, Entry{Role: normevent.RoleWrite, RoleVerified: true, LookupStart: time.Now()})
				m.Get(key)
				if i%25 == 0 {
					m.Invalidate(key.Actor)
				}
			}
		})
	}
	wg.Wait()
}
