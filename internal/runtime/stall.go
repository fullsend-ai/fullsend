package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// ErrStalled reports that a run was terminated because its event stream went
// silent, not because it finished or hit the global timeout. Callers match it
// with errors.Is to separate "the agent was wedged" from "the agent failed",
// which otherwise look the same from the exit code.
var ErrStalled = errors.New("agent stalled")

// stallPollDivisor sets the watchdog poll interval as a fraction of the stall
// timeout, so detection lands within ~5% of the threshold at any setting.
// stallMaxPoll caps it for long timeouts (the default 10m polls every 30s).
const (
	stallPollDivisor = 20
	stallMaxPoll     = 30 * time.Second
	stallMinPoll     = time.Millisecond
)

// stallWatchdog terminates a run whose runtime event stream has gone quiet.
//
// The runner's heartbeat reports elapsed wall-clock time whether or not the
// agent is alive, so a wedged process is indistinguishable from a thinking
// one until the global timeout expires — and gets billed for the difference.
// Every event the stream parser emits is proof of life: note() records it,
// half a timeout of silence logs a warning, and a full timeout of silence
// calls kill, which is the same context cancel the global timeout uses to
// terminate the sandbox command (sandbox.ExecStreamReader). The watchdog
// owns no context of its own and never touches the caller's.
//
// A nil *stallWatchdog is a disabled watchdog: every method is a no-op, so
// call sites need no branch for FULLSEND_STALL_TIMEOUT=0.
type stallWatchdog struct {
	timeout time.Duration
	kill    func()
	printer *ui.Printer
	warnW   io.Writer
	isCI    bool

	lastEvent atomic.Int64 // UnixNano of the most recent event
	fired     atomic.Bool
	stopped   chan struct{}
	stopOnce  sync.Once
}

// startStallWatchdog arms a watchdog that calls kill after timeout of event
// silence, counting from now. A non-positive timeout returns nil (disabled).
// The caller must call stop() when the event stream ends.
func startStallWatchdog(timeout time.Duration, printer *ui.Printer, kill func()) *stallWatchdog {
	return startStallWatchdogTo(os.Stderr, timeout, printer, kill)
}

// startStallWatchdogTo is startStallWatchdog with the CI-annotation writer
// injected, so tests can read the annotations back.
func startStallWatchdogTo(annotations io.Writer, timeout time.Duration, printer *ui.Printer, kill func()) *stallWatchdog {
	if timeout <= 0 || kill == nil {
		return nil
	}
	w := &stallWatchdog{
		timeout: timeout,
		kill:    kill,
		printer: printer,
		warnW:   annotations,
		isCI:    os.Getenv("GITHUB_ACTIONS") == "true",
		stopped: make(chan struct{}),
	}
	w.lastEvent.Store(time.Now().UnixNano())
	go w.watch()
	return w
}

// note records that an event arrived, ending any stall episode in progress.
func (w *stallWatchdog) note() {
	if w == nil {
		return
	}
	w.lastEvent.Store(time.Now().UnixNano())
}

// stop disarms the watchdog. Safe to call more than once, and safe after the
// watchdog has already fired — stalledErr keeps reporting the kill.
func (w *stallWatchdog) stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopped) })
}

// stalledErr returns a non-nil error wrapping ErrStalled when the watchdog
// killed the run, and nil otherwise.
func (w *stallWatchdog) stalledErr() error {
	if w == nil || !w.fired.Load() {
		return nil
	}
	return fmt.Errorf("%w: no runtime events for %s (FULLSEND_STALL_TIMEOUT)", ErrStalled, w.timeout)
}

func (w *stallWatchdog) watch() {
	interval := w.timeout / stallPollDivisor
	if interval > stallMaxPoll {
		interval = stallMaxPoll
	}
	if interval < stallMinPoll {
		interval = stallMinPoll
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// The lastEvent value a warning was already emitted for. Comparing
	// against it (rather than a bool) rearms the warning as soon as an event
	// arrives, so each stall episode warns exactly once.
	var warnedFor int64

	for {
		select {
		case <-w.stopped:
			return
		case now := <-ticker.C:
			last := w.lastEvent.Load()
			silence := now.Sub(time.Unix(0, last))
			switch {
			case silence >= w.timeout:
				w.fired.Store(true)
				w.kill()
				return
			case silence >= w.timeout/2 && warnedFor != last:
				warnedFor = last
				w.warn(fmt.Sprintf("no agent events for %s", w.timeout/2))
			}
		}
	}
}

// warn surfaces a stall warning the way the event renderer surfaces tool
// progress: a GitHub workflow annotation in CI, plus the printer everywhere.
func (w *stallWatchdog) warn(msg string) {
	if w.isCI {
		fmt.Fprintf(w.warnW, "::warning::%s\n", msg)
	}
	if w.printer != nil {
		w.printer.StepWarn(msg)
	}
}
