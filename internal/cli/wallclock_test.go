package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// simulateHostSleep shrinks the wall-clock watchdog interval and replaces
// the refreshers' wall clock with one that runs ahead of the real
// (monotonic) clock by the returned offset, the way it does after the host
// slept. The offset starts at zero.
func simulateHostSleep(t *testing.T, watchdog time.Duration) *atomic.Int64 {
	t.Helper()
	var slept atomic.Int64
	prevWatchdog, prevNow := wallClockWatchdog, wallNowFn
	wallClockWatchdog = watchdog
	wallNowFn = func() time.Time { return time.Now().Round(0).Add(time.Duration(slept.Load())) }
	t.Cleanup(func() { wallClockWatchdog, wallNowFn = prevWatchdog, prevNow })
	return &slept
}

func TestWallNowFn_HasNoMonotonicReading(t *testing.T) {
	now := wallNowFn()
	assert.Equal(t, now.Round(0).String(), now.String(), "the wall clock carries no monotonic reading")
}

func TestWaitWallClock_WallClockPastDueReleasesTheWait(t *testing.T) {
	slept := simulateHostSleep(t, 5*time.Millisecond)
	dueAt := wallNowFn().Add(time.Hour)
	// The monotonic timer still has an hour to go; the wall clock says the
	// host slept past the deadline.
	slept.Store(int64(2 * time.Hour))

	done := make(chan bool, 1)
	go func() { done <- waitWallClock(context.Background(), time.Hour, dueAt) }()
	select {
	case ok := <-done:
		assert.True(t, ok, "a wall-clock deadline that has passed releases the wait")
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog did not release a wait whose wall-clock deadline had passed")
	}
}

func TestWaitWallClock_WatchdogDoesNotFireAheadOfTheTimer(t *testing.T) {
	simulateHostSleep(t, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// An awake host: the watchdog ticks many times before ctx ends, but the
	// wall-clock deadline is still an hour out.
	assert.False(t, waitWallClock(ctx, time.Hour, wallNowFn().Add(time.Hour)), "only ctx ends the wait")
}

func TestWaitWallClock_TimerReleasesTheWait(t *testing.T) {
	simulateHostSleep(t, time.Hour)
	assert.True(t, waitWallClock(context.Background(), 10*time.Millisecond, wallNowFn().Add(10*time.Millisecond)))
}

func TestWaitWallClock_StopsOnCancel(t *testing.T) {
	simulateHostSleep(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.False(t, waitWallClock(ctx, time.Hour, wallNowFn().Add(time.Hour)))
}
