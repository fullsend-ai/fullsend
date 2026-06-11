package fetchsvc

import (
	"math"
	"sync/atomic"
)

const DefaultMaxFetches = 10

// RateLimiter enforces a maximum number of runtime fetches per agent run.
// It uses an atomic counter for thread safety without requiring a mutex.
type RateLimiter struct {
	max     int32
	current atomic.Int32
}

// NewRateLimiter creates a rate limiter with the given maximum.
// If max <= 0, DefaultMaxFetches is used.
func NewRateLimiter(max int) *RateLimiter {
	if max <= 0 {
		max = DefaultMaxFetches
	}
	if max > math.MaxInt32 {
		max = math.MaxInt32
	}
	return &RateLimiter{max: int32(max)}
}

// Allow checks if another fetch is permitted. Returns true and increments
// the counter atomically if under the limit. Thread-safe for concurrent use.
func (r *RateLimiter) Allow() bool {
	for {
		cur := r.current.Load()
		if cur >= r.max {
			return false
		}
		if r.current.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Release returns a previously consumed slot to the pool.
// It is safe to call even if no slot was consumed; the counter will not go below zero.
func (r *RateLimiter) Release() {
	for {
		cur := r.current.Load()
		if cur <= 0 {
			return
		}
		if r.current.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// Count returns the current number of fetches performed.
func (r *RateLimiter) Count() int32 {
	return r.current.Load()
}
