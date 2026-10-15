//go:build unix

package fetch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockCacheEntry_GivesUpOnAStuckHolder(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockCacheEntry(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	saved := cacheLockWait
	cacheLockWait = 200 * time.Millisecond
	defer func() { cacheLockWait = saved }()

	start := time.Now()
	_, err = lockCacheEntry(context.Background(), dir)
	if err == nil {
		t.Fatal("second lock succeeded while the first was held")
	}
	if !strings.Contains(err.Error(), "has held the cache lock") {
		t.Errorf("unexpected error: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waited %s, want about %s", time.Since(start), cacheLockWait)
	}
}

func TestLockCacheEntry_WaitsForARelease(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockCacheEntry(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		unlock()
	}()
	unlock2, err := lockCacheEntry(context.Background(), dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
}

func TestLockCacheEntry_StopsWaitingWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockCacheEntry(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err = lockCacheEntry(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want an error wrapping context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "waiting for cache lock") {
		t.Errorf("unexpected error: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waited %s after cancellation, want about 50ms", time.Since(start))
	}
}

// A materialized-tree write whose entry lock another writer holds stops
// when its context is cancelled, rather than waiting out cacheLockWait.
func TestCachePutMaterializedDir_CancelledWhileLockHeld(t *testing.T) {
	ws := t.TempDir()
	files := map[string][]byte{"a.txt": []byte("a\n")}
	dir, err := MaterializedCachePath(ws, ComputeTreeHash(files))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockCacheEntry(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = CachePutMaterializedDir(ctx, ws, "https://example.com/x", files)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want an error wrapping context.Canceled", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "tree")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("tree installed despite cancellation: %v", statErr)
	}
}
