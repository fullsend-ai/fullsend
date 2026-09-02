package runtime

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuf is a bytes.Buffer safe to read while the watchdog goroutine writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func countLines(s, substr string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// TestStallWatchdog_EventsKeepItQuiet: a stream that keeps producing events is
// never killed, however long the run lasts.
func TestStallWatchdog_EventsKeepItQuiet(t *testing.T) {
	var kills atomic.Int32
	w := startStallWatchdogTo(io.Discard, 150*time.Millisecond, ui.New(io.Discard), func() { kills.Add(1) })
	require.NotNil(t, w)
	defer w.stop()

	// Three times the timeout, with an event well inside every window.
	deadline := time.Now().Add(450 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.note()
		time.Sleep(15 * time.Millisecond)
	}

	assert.Zero(t, kills.Load(), "a stream that keeps emitting events must not be killed")
	assert.NoError(t, w.stalledErr())
}

// TestStallWatchdog_SilenceKills: past the threshold the sandbox command is
// killed and the run fails with the distinct sentinel, not a generic error.
func TestStallWatchdog_SilenceKills(t *testing.T) {
	var kills atomic.Int32
	w := startStallWatchdogTo(io.Discard, 100*time.Millisecond, ui.New(io.Discard), func() { kills.Add(1) })
	require.NotNil(t, w)
	defer w.stop()

	assert.Eventually(t, func() bool { return kills.Load() > 0 }, 2*time.Second, 10*time.Millisecond,
		"watchdog did not invoke the kill path after the stall timeout")

	err := w.stalledErr()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStalled)
	assert.Contains(t, err.Error(), "no runtime events for 100ms")

	// The kill fires once, and stop() after the fact keeps the verdict.
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(1), kills.Load(), "the watchdog must kill once, then stand down")
	w.stop()
	assert.ErrorIs(t, w.stalledErr(), ErrStalled)
}

// TestStallWatchdog_ZeroDisables: FULLSEND_STALL_TIMEOUT=0 means no watchdog,
// and every method stays safe on the nil that represents it.
func TestStallWatchdog_ZeroDisables(t *testing.T) {
	var kills atomic.Int32
	w := startStallWatchdogTo(io.Discard, 0, ui.New(io.Discard), func() { kills.Add(1) })
	require.Nil(t, w, "a zero timeout must disable the watchdog")

	w.note()
	w.stop()
	assert.NoError(t, w.stalledErr())

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, kills.Load())
}

// TestStallWatchdog_WarnsOncePerStallEpisode: half a timeout of silence warns
// exactly once — not once per poll — and an event rearms the warning.
func TestStallWatchdog_WarnsOncePerStallEpisode(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	var kills atomic.Int32
	annotations := &syncBuf{}
	w := startStallWatchdogTo(annotations, 500*time.Millisecond, ui.New(io.Discard), func() { kills.Add(1) })
	require.NotNil(t, w)
	defer w.stop()

	// Past the half-threshold (250ms), still short of the kill (500ms). The
	// poll runs every 25ms, so a per-poll warning would print several.
	time.Sleep(350 * time.Millisecond)
	assert.Equal(t, 1, countLines(annotations.String(), "::warning::"),
		"expected exactly one warning per stall episode, got: %q", annotations.String())
	assert.Contains(t, annotations.String(), "::warning::no agent events for 250ms")

	// An event ends the episode; the next silence warns again.
	w.note()
	time.Sleep(350 * time.Millisecond)
	assert.Equal(t, 2, countLines(annotations.String(), "::warning::"),
		"an event should rearm the warning for the next stall episode")
	assert.Zero(t, kills.Load(), "warning at half the threshold must not kill the run")
}

// TestStallWatchdog_NoWarningAfterStop: once stop() returns, a queued tick
// must not emit a stall warning — the select can still draw ticker.C after
// stopped closes, and a completed run warning about inactivity is misleading.
func TestStallWatchdog_NoWarningAfterStop(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	annotations := &syncBuf{}
	w := startStallWatchdogTo(annotations, 200*time.Millisecond, ui.New(io.Discard), func() {})
	require.NotNil(t, w)

	// Deep in the warn window (past 100ms, short of the 200ms kill), so ticks
	// queued around stop() would warn if the disarm didn't suppress them.
	time.Sleep(140 * time.Millisecond)
	w.stop()
	before := countLines(annotations.String(), "::warning::")

	// Several more poll intervals: any warning emitted now is the race.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, before, countLines(annotations.String(), "::warning::"),
		"a stopped watchdog must not emit new warnings, got: %q", annotations.String())
}

// TestStallWatchdog_NoAnnotationsOutsideCI keeps the workflow-command syntax
// out of local terminals, as the event renderer does.
func TestStallWatchdog_NoAnnotationsOutsideCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")

	annotations := &syncBuf{}
	console := &syncBuf{}
	w := startStallWatchdogTo(annotations, 100*time.Millisecond, ui.New(console), func() {})
	require.NotNil(t, w)
	defer w.stop()

	assert.Eventually(t, func() bool { return strings.Contains(console.String(), "no agent events for 50ms") },
		2*time.Second, 10*time.Millisecond, "the warning must still reach the printer outside CI")
	assert.Empty(t, annotations.String(), "no workflow annotations outside CI")
}

// TestRuntimeRun_DoesNotRebindTheCallerContext guards the regression that
// reported every successful run as "cancelled": a body-scope
// `ctx, cancel := context.WithCancel(ctx)` rebinds the ctx an earlier-
// registered defer closed over, and defers run LIFO, so the cancel fires
// first and the status notifier reads ctx.Err() != nil. The stall watchdog
// must therefore kill through the cancel ExecStreamReader already returns —
// a context derived inside the sandbox package, owned by nobody else —
// rather than deriving anything from the caller's ctx.
//
// It reads the source because the failure is invisible at the package
// boundary: the run still works, only the reported status is wrong.
func TestRuntimeRun_DoesNotRebindTheCallerContext(t *testing.T) {
	// Body-scope (one-tab) rebinding of the ctx parameter. A derived context
	// on a NEW variable name is fine; only rebinding `ctx` is the hazard.
	rebind := regexp.MustCompile(`(?m)^\tctx(, [A-Za-z_][A-Za-z0-9_]*)? :?= context\.`)

	for _, file := range []string{"claude.go", "pi_run.go"} {
		src, err := os.ReadFile(file)
		require.NoError(t, err)

		assert.NotRegexp(t, rebind, string(src),
			"%s rebinds the caller's ctx at function-body scope; the watchdog must reuse the ExecStreamReader cancel instead", file)
		assert.Contains(t, string(src), "startStallWatchdog(params.StallTimeout, printer, cancel)",
			"%s must arm the watchdog on the sandbox command's own cancel, not on a context it derives", file)
	}
}
