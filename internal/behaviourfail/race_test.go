package behaviourfail

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecorderConcurrentRecordFailure(t *testing.T) {
	t.Parallel()

	const goroutines = 12
	const perGoroutine = 25

	r := &Recorder{}
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			class := Scenario
			if i%2 == 0 {
				class = Infrastructure
			}
			for range perGoroutine {
				r.RecordFailure("scenario", fmt.Sprintf("scenario-%d", i), class, "boom")
			}
			r.Counts()
			r.Snapshot()
		}()
	}
	wg.Wait()

	infrastructure, scenario := r.Counts()
	require.Equal(t, 6*perGoroutine, infrastructure)
	require.Equal(t, 6*perGoroutine, scenario)
	snap := r.Snapshot()
	require.Len(t, snap, goroutines*perGoroutine)
	require.Equal(t, len(snap), infrastructure+scenario)
}
