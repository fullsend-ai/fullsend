package fetchsvc

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestRateLimiter_AllowsUpToMax(t *testing.T) {
	r := NewRateLimiter(3)

	for i := 0; i < 3; i++ {
		if !r.Allow() {
			t.Fatalf("Allow() returned false on call %d, want true", i+1)
		}
	}
	if r.Allow() {
		t.Fatal("Allow() returned true after max reached, want false")
	}
}

func TestRateLimiter_RejectsAboveMax(t *testing.T) {
	r := NewRateLimiter(1)

	if !r.Allow() {
		t.Fatal("first Allow() returned false")
	}
	for i := 0; i < 5; i++ {
		if r.Allow() {
			t.Fatalf("Allow() returned true on extra call %d", i+1)
		}
	}
}

func TestRateLimiter_DefaultMax(t *testing.T) {
	r := NewRateLimiter(0)
	if r.max != DefaultMaxFetches {
		t.Fatalf("max = %d, want %d", r.max, DefaultMaxFetches)
	}

	r2 := NewRateLimiter(-5)
	if r2.max != DefaultMaxFetches {
		t.Fatalf("max = %d, want %d", r2.max, DefaultMaxFetches)
	}
}

func TestRateLimiter_Count(t *testing.T) {
	r := NewRateLimiter(5)

	if got := r.Count(); got != 0 {
		t.Fatalf("Count() = %d before any calls, want 0", got)
	}

	r.Allow()
	r.Allow()
	if got := r.Count(); got != 2 {
		t.Fatalf("Count() = %d after 2 Allow(), want 2", got)
	}
}

func TestRateLimiter_ConcurrentSafety(t *testing.T) {
	const max = 50
	const goroutines = 200

	r := NewRateLimiter(max)

	var allowed atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if r.Allow() {
				allowed.Add(1)
			}
		}()
	}

	wg.Wait()

	if got := allowed.Load(); got != max {
		t.Fatalf("total allowed = %d across %d goroutines, want exactly %d", got, goroutines, max)
	}
	if got := r.Count(); got != int32(max) {
		t.Fatalf("Count() = %d, want %d", got, max)
	}
}
