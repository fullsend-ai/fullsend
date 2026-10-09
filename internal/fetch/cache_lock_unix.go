//go:build unix

package fetch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// cacheLockWait bounds how long a writer waits for another writer's
// cache-entry lock. The holder only writes one definition tree and
// renames it (bounded by the fetch limits), so a lock held longer than
// this belongs to a stuck process; a var so tests can shorten it.
var cacheLockWait = 2 * time.Minute

// lockCacheEntry takes an exclusive lock on the cache entry directory dir,
// shared across processes, and returns the function that releases it.
// Writers replacing a materialized tree hold it, so one never removes a
// tree another has just installed. It polls a non-blocking flock and
// gives up after cacheLockWait, so a stuck holder cannot hang the run, or
// as soon as ctx is done.
func lockCacheEntry(ctx context.Context, dir string) (func(), error) {
	path := filepath.Join(dir, ".tree.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening cache lock: %w", err)
	}
	deadline := time.Now().Add(cacheLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf("another fullsend process has held the cache lock %q for over %s; stop it, or delete the cache entry and run again", path, cacheLockWait)
			}
			return nil, fmt.Errorf("acquiring cache lock: %w", err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("waiting for cache lock %q: %w", path, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()
	}, nil
}
