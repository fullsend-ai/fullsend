package forgejoactions

import (
	"sync"
	"testing"
	"time"
)

// TestConcurrentAccess shares one Driver across goroutines under the race
// detector, as World.Clone does with GODOG_CONCURRENCY>1. The Driver holds
// no mutable state of its own, so any unsynchronized field access added
// later makes -race fire. Only calls that return promptly against the
// seeded fake are used.
func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{harnessRun(1, "success", trigger)}
	f.jobs[1] = []fakeJob{{Name: "Harness run (triage)", Status: "success"}}

	const goroutines = 12
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			_, _ = d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "issues")
			_, _ = d.CountHarnessDispatches(ctx, "o", "r", "triage", trigger)
			_, _ = d.GetRunLogs(ctx, "o", "r", i+1)
			_ = d.AssertNoHarnessAgentArtifact(ctx, "o", "r", "review", trigger.Add(-time.Second))
		})
	}
	wg.Wait()
}
