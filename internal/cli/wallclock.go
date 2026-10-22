package cli

import (
	"context"
	"time"
)

// Wall-clock waits for credential refreshers (#7748, #8584). Go timers
// count monotonic time, which does not advance while the host sleeps
// (mach_absolute_time on darwin, CLOCK_MONOTONIC on Linux, a suspended
// VM), but credential expiries are wall-clock times checked by the
// gateway or proxy. The refreshers compare deadlines on the wall clock and
// wait with waitWallClock, so a host that sleeps past a refresh point
// refreshes on the first check after it wakes.
var (
	// wallClockWatchdog is how often a refresher waiting in waitWallClock
	// compares the wall clock with its deadline. Short enough that a host
	// that wakes only briefly (a macOS maintenance wake) still gets a
	// check in each wake; Apple does not document how long those wakes
	// last.
	wallClockWatchdog = 30 * time.Second
	// wallNowFn is the refreshers' wall clock: time.Now with the monotonic
	// reading stripped, so comparisons count time the host spent asleep,
	// as the credential's expiry does. Override in tests to return a clock
	// that runs ahead of the real one, simulating a host that slept.
	wallNowFn = func() time.Time { return time.Now().Round(0) }
)

// waitWallClock blocks until a deadline is due — after delay, which is
// dueAt on the wall clock — and reports false if ctx ends first. The timer
// counts monotonic time, so on its own it can fire after a wall-clock
// expiry has already passed. A periodic wall-clock check releases the wait
// as soon as dueAt has passed on the wall clock. On an awake host both
// clocks advance together, so the check never fires ahead of the timer.
func waitWallClock(ctx context.Context, delay time.Duration, dueAt time.Time) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	watchdog := time.NewTicker(wallClockWatchdog)
	defer watchdog.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-watchdog.C:
			if !wallNowFn().Before(dueAt) {
				return true
			}
		}
	}
}
