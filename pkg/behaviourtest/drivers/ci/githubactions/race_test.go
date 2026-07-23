package githubactions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// TestConcurrentAccess exercises the real githubactions.Driver under the
// race detector with a FakeClient backend. The Driver is an immutable
// wrapper around forge.Client and a string Token — it holds no
// unsynchronized mutable fields of its own — so sharing one instance
// across goroutines is safe by design. This test verifies that property:
// any unsynchronized field access (now or added in the future) would
// cause -race to fire.
//
// Only methods that return immediately with FakeClient are used here
// (no long-poll / wait loops) to avoid flaky multi-minute test runs.
// FakeClient is seeded so all calls return promptly.
func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	fc := forge.NewFakeClient()
	// Seed artifacts and workflow runs so methods return immediately.
	fc.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            1,
				Name:          "fullsend-triage",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 10,
			},
		},
	}

	d := New(fc, "test-token")
	ctx := context.Background()
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const goroutines = 12

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// CountHarnessDispatches — iterates artifacts, returns count.
			_, _ = d.CountHarnessDispatches(ctx, "org", "repo", "triage", after)

			// GetRunLogs — simple pass-through.
			_, _ = d.GetRunLogs(ctx, "org", "repo", i+1)

			// AssertNoHarnessAgentArtifact — checks artifact list once.
			// "review" agent has no seeded artifacts, so this returns nil.
			_ = d.AssertNoHarnessAgentArtifact(ctx, "org", "repo", "review", after)
		}()
	}
	wg.Wait()
}
