package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// useSettleClock fixes the clock at now and makes the wait fire at once,
// recording each requested duration.
func useSettleClock(t *testing.T, now time.Time) *[]time.Duration {
	t.Helper()
	var waited []time.Duration
	origNow, origAfter := settleNowFn, settleAfterFn
	settleNowFn = func() time.Time { return now }
	settleAfterFn = func(d time.Duration) <-chan time.Time {
		waited = append(waited, d)
		c := make(chan time.Time, 1)
		c <- now.Add(d)
		return c
	}
	t.Cleanup(func() { settleNowFn, settleAfterFn = origNow, origAfter })
	return &waited
}

func TestWaitForOpenShellFirstPoll_WaitsForTheRemainder(t *testing.T) {
	readyAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	waited := useSettleClock(t, readyAt.Add(7*time.Second))
	var buf bytes.Buffer

	got, err := waitForOpenShellFirstPoll(context.Background(), readyAt, ui.New(&buf))

	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, got)
	assert.Equal(t, []time.Duration{5 * time.Second}, *waited)
	assert.Contains(t, buf.String(), "first policy poll")
	assert.Contains(t, buf.String(), "#3809")
}

func TestWaitForOpenShellFirstPoll_NoWaitOncePast(t *testing.T) {
	readyAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, elapsed := range []time.Duration{openShellFirstPollSettle, 30 * time.Second} {
		waited := useSettleClock(t, readyAt.Add(elapsed))
		var buf bytes.Buffer

		got, err := waitForOpenShellFirstPoll(context.Background(), readyAt, ui.New(&buf))

		require.NoError(t, err)
		assert.Zero(t, got, "elapsed %s", elapsed)
		assert.Empty(t, *waited, "elapsed %s", elapsed)
		assert.Empty(t, buf.String(), "no message when nothing is waited (elapsed %s)", elapsed)
	}
}

func TestWaitForOpenShellFirstPoll_NilPrinter(t *testing.T) {
	readyAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	waited := useSettleClock(t, readyAt)

	got, err := waitForOpenShellFirstPoll(context.Background(), readyAt, nil)

	require.NoError(t, err)
	assert.Equal(t, openShellFirstPollSettle, got)
	assert.Equal(t, []time.Duration{openShellFirstPollSettle}, *waited)
}

func TestWaitForOpenShellFirstPoll_AlreadyCancelled(t *testing.T) {
	readyAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	waited := useSettleClock(t, readyAt)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := waitForOpenShellFirstPoll(ctx, readyAt, nil)

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, got)
	assert.Empty(t, *waited, "a cancelled run must not start the wait")
}

func TestWaitForOpenShellFirstPoll_CancelledAfterTheWindow(t *testing.T) {
	// Bootstrap already took longer than the window: there is nothing to
	// wait for, but a cancelled run must still stop here.
	readyAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	waited := useSettleClock(t, readyAt.Add(30*time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := waitForOpenShellFirstPoll(ctx, readyAt, nil)

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, got)
	assert.Empty(t, *waited)
}

func TestWaitForOpenShellFirstPoll_CancelledDuringWait(t *testing.T) {
	readyAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	origNow, origAfter := settleNowFn, settleAfterFn
	t.Cleanup(func() { settleNowFn, settleAfterFn = origNow, origAfter })
	settleNowFn = func() time.Time { return readyAt }
	ctx, cancel := context.WithCancel(context.Background())
	settleAfterFn = func(time.Duration) <-chan time.Time {
		cancel() // cancelled while waiting; this timer never fires
		return make(chan time.Time)
	}

	got, err := waitForOpenShellFirstPoll(ctx, readyAt, nil)

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, got)
}
