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
// stallMaxPoll caps it for long timeouts (the default 15m polls every 30s).
const (
	stallPollDivisor = 20
	stallMaxPoll     = 30 * time.Second
	stallMinPoll     = time.Millisecond
)

// StallDetectionLatency is how long after the silence threshold the watchdog
// can take to notice it: it polls, so a stall crossing the threshold right
// after a tick waits one full interval to be seen. Exported because the
// runner decides whether arming the watchdog is worth it at all, and that
// decision is about when the kill actually lands, not when the threshold is
// nominally crossed (internal/cli/run_overrides.go).
func StallDetectionLatency(timeout time.Duration) time.Duration {
	interval := timeout / stallPollDivisor
	if interval > stallMaxPoll {
		interval = stallMaxPoll
	}
	if interval < stallMinPoll {
		interval = stallMinPoll
	}
	return interval
}

// Watchdog lifecycle. stop() and the fire branch in watch() both attempt
// watchdogArmed -> {watchdogStopped,watchdogFired} via CompareAndSwap: exactly
// one wins, so a tick that loses the race to a concurrent stop() can never
// fire, and a stop() that loses the race to a firing tick can never suppress it.
const (
	watchdogArmed int32 = iota
	watchdogStopped
	watchdogFired
)

// stallWatchdog terminates a run whose runtime event stream has gone quiet.
//
// The runner's heartbeat reports elapsed wall-clock time whether or not the
// agent is alive, so a wedged process is indistinguishable from a thinking
// one until the global timeout expires — and gets billed for the difference.
// Every well-formed line the stream parser reads is proof of life: note()
// records it, half a timeout of silence logs a warning, and a full timeout of
// silence calls kill — stallKill at every runtime call site, which terminates
// the agent inside the sandbox and only then releases the local exec client.
// The watchdog owns no context of its own and never touches the caller's.
//
// A nil *stallWatchdog is a disabled watchdog: every method is a no-op, so
// call sites need no branch for FULLSEND_STALL_TIMEOUT=0.
type stallWatchdog struct {
	timeout time.Duration
	kill    func()
	printer *ui.Printer
	warnW   io.Writer
	isCI    bool

	// start anchors lastEvent and every silence calculation to time.Since,
	// which is monotonic — unlike time.Now().UnixNano() reconstructed via
	// time.Unix, a wall-clock step (NTP, DST, manual) can't perturb it.
	start     time.Time
	lastEvent atomic.Int64 // time.Since(start) as of the most recent event
	state     atomic.Int32 // watchdogArmed | watchdogStopped | watchdogFired
	stopped   chan struct{}

	// mu serializes stop()'s disarm with warning emission. The select in
	// watch() can draw an already-queued tick even when stopped is closed,
	// and the CAS only protects the kill path — a bare state check before
	// warning would leave a check-then-warn window, so both the disarm and
	// the check+emit happen under this lock: no warning once stop() returns.
	// note() stays lock-free; warnIfStillSilent re-reads lastEvent under mu
	// instead, so an event that lands after the tick computed its silence
	// still suppresses the warning.
	mu sync.Mutex
}

// stallKill is the kill every streaming runtime hands the watchdog.
//
// cancel — the context cancel from sandbox.ExecStreamReader, and the whole of
// what the global timeout does — SIGKILLs the local `openshell sandbox exec`
// client; it does not signal the agent, which keeps writing the workspace and
// spending tokens until the run's deferred sandbox.Delete tears the sandbox
// down (and forever, under --keep-sandbox). OpenShell exposes no signal API,
// so the in-sandbox half is the stray-process sweep run over a second exec
// channel: it TERM->KILLs the sandbox user's processes, sparing only its own
// shell's ancestry and the keep-alive, which is exactly the wedged agent.
// The sweep runs first so the stream reaches EOF on its own; cancel then
// releases the client whatever the sweep did.
//
// Unlike ClearIterationArtifacts' sweep this one is not serialized against
// the credential refreshers' writes (internal/cli/run.go's sandboxMu is not
// reachable from here): a refresher upload killed mid-write leaves a
// truncated credential file in a sandbox the stalled run is already tearing
// down, so it has nothing left to break.
func stallKill(execFn sandboxExecFunc, sandboxName string, w io.Writer, cancel func()) func() {
	return func() {
		defer cancel()
		n, err := killStrayProcesses(execFn, sandboxName)
		switch {
		case err != nil:
			fmt.Fprintf(w, "  Warning: could not terminate the stalled agent inside the sandbox: %v\n", sanitizeOutput(err.Error()))
		case n > 0:
			fmt.Fprintf(w, "  Terminated %d stalled sandbox process(es)\n", n)
		}
	}
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
		start:   time.Now(),
		stopped: make(chan struct{}),
	}
	go w.watch()
	return w
}

// note records that an event arrived, ending any stall episode in progress.
func (w *stallWatchdog) note() {
	if w == nil {
		return
	}
	w.lastEvent.Store(int64(time.Since(w.start)))
}

// stop disarms the watchdog. Safe to call more than once, and safe after the
// watchdog has already fired — stalledErr keeps reporting the kill.
func (w *stallWatchdog) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.state.CompareAndSwap(watchdogArmed, watchdogStopped) {
		close(w.stopped)
	}
	w.mu.Unlock()
}

// stalledErr returns a non-nil error wrapping ErrStalled when the watchdog
// killed the run, and nil otherwise.
func (w *stallWatchdog) stalledErr() error {
	if w == nil || w.state.Load() != watchdogFired {
		return nil
	}
	return fmt.Errorf("%w: no runtime events for %s (FULLSEND_STALL_TIMEOUT)", ErrStalled, w.timeout)
}

func (w *stallWatchdog) watch() {
	ticker := time.NewTicker(StallDetectionLatency(w.timeout))
	defer ticker.Stop()

	// The lastEvent value a warning was already emitted for. Comparing
	// against it (rather than a bool) rearms the warning as soon as an event
	// arrives, so each stall episode warns exactly once. -1 is a sentinel:
	// lastEvent is always >= 0 (elapsed since start), so it can never collide
	// with "no warning issued yet" the way a zero-initialized value would.
	var warnedFor int64 = -1

	for {
		select {
		case <-w.stopped:
			return
		case <-ticker.C:
			last := w.lastEvent.Load()
			silence := time.Since(w.start) - time.Duration(last)
			switch {
			case silence >= w.timeout:
				if w.state.CompareAndSwap(watchdogArmed, watchdogFired) {
					w.kill()
				}
				return
			case silence >= w.timeout/2 && warnedFor != last:
				warnedFor = last
				w.warnIfStillSilent(last)
			}
		}
	}
}

// warnIfStillSilent emits the half-timeout warning for the stall episode that
// began at last, unless the watchdog was stopped or an event arrived after
// the tick computed its silence — note() is not serialized with the tick, so
// last can be stale by the time the lock is held, and a warning right after
// activity resumed would be misleading.
func (w *stallWatchdog) warnIfStillSilent(last int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Load() != watchdogArmed || w.lastEvent.Load() != last {
		return
	}
	w.warn(fmt.Sprintf("no agent events for %s", w.timeout/2))
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
