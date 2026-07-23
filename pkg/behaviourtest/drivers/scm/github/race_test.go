package github

import (
	"context"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// TestConcurrentAccess exercises the real github.Driver under the race
// detector with a FakeClient backend. The Driver is an immutable wrapper
// around forge.Client — it holds no mutable fields of its own — so
// sharing one instance across goroutines is safe by design. This test
// verifies that property: any unsynchronized field access (now or added
// in the future) would cause -race to fire.
//
// All calls go through forge.FakeClient, which is mutex-protected and
// returns immediately — no network, no polling loops.
func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	fc := forge.NewFakeClient()
	// Seed the FakeClient so methods return without errors.
	fc.FileContents = map[string][]byte{
		"org/repo/dummy.yaml": []byte("content"),
	}

	d := New(fc)
	ctx := context.Background()

	const goroutines = 12

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// CreateIssue
			_, _ = d.CreateIssue(ctx, "org", "repo", "title", "body", "label")

			// GetIssue — FakeClient returns a default issue.
			_, _ = d.GetIssue(ctx, "org", "repo", i+1)

			// GetFileContent
			_, _ = d.GetFileContent(ctx, "org", "repo", "dummy.yaml")

			// CommitFile
			_ = d.CommitFile(ctx, "org", "repo", "path.txt", "msg", []byte("data"))

			// CreateBranch
			_ = d.CreateBranch(ctx, "org", "repo", "branch")

			// AddComment
			_, _ = d.AddComment(ctx, "org", "repo", 1, "comment")
		}()
	}
	wg.Wait()
}
