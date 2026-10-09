//go:build !unix

package fetch

import "context"

// lockCacheEntry is a no-op where flock is unavailable; fullsend's
// release builds are unix-only.
func lockCacheEntry(context.Context, string) (func(), error) { return func() {}, nil }
