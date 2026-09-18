package runtime

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// codexSteerQueues maps a sandbox name to the steerable codex run in it,
// for the same reason steerSessions exists: CodexRuntime is a value type,
// so a run's state cannot live on the receiver.
var codexSteerQueues sync.Map // sandboxName -> *codexSteerQueue

func registerCodexSteerQueue(sandboxName string, q *codexSteerQueue) {
	codexSteerQueues.Store(sandboxName, q)
}

func unregisterCodexSteerQueue(sandboxName string) { codexSteerQueues.Delete(sandboxName) }

func lookupCodexSteerQueue(sandboxName string) (*codexSteerQueue, bool) {
	v, ok := codexSteerQueues.Load(sandboxName)
	if !ok {
		return nil, false
	}
	q, ok := v.(*codexSteerQueue)
	return q, ok
}

// codexSteerQueue is the interrupt-and-resume state for a steerable codex
// run. codex exec has no live steer channel — steering exists only in
// app-server — so a mid-run update is delivered by stopping the current
// process and starting `codex exec ... resume <thread_id> -` with the
// update on stdin. The thread keeps its context, and each interrupt leaves
// one dangling tool call in the rollout, which codex tolerates ("Custom
// tool call output is missing").
type codexSteerQueue struct {
	sandboxName string
	// sweep interrupts the in-sandbox codex. Killing the openshell client
	// does not kill the process inside the sandbox, so the stray-process
	// sweep is the primitive; injected for tests. The default runs it with
	// interruptStrayGrace rather than the iteration-boundary default —
	// see interrupt().
	sweep func(execFn sandboxExecFunc, sandboxName string) (int, error)
	// exec runs the sweep in the sandbox (sandbox.Exec in production).
	exec sandboxExecFunc
	// warn receives a note when an interrupt could not be delivered.
	warn io.Writer

	mu sync.Mutex
	// threadID is captured from thread.started. Until it is known there is
	// nothing to resume, so an early steer is queued rather than acted on.
	threadID string
	// turnRunning is true only while a codex process is actually executing.
	// The interrupt is a sweep that kills every process of the sandbox
	// user, so firing it with nothing running is not merely wasted work: it
	// spends the whole TERM grace, kills anything the agent left running,
	// and — because the runner holds sandboxMu across Steer — blocks the
	// credential refreshers for the duration, all to interrupt nothing.
	// It is set before each process starts rather than after, so the error
	// is always a spurious sweep (harmless, self-correcting) rather than a
	// missed interrupt.
	turnRunning bool
	pending     []pendingSteer
	settled     bool
	results     []SteerResult
	// inFlight is the steer the current process was resumed for, staked by
	// nextCodexTurn and turned into a SteerResult only once that process
	// proves it opened the thread. It is deliberately not recorded at stake
	// time: RunMetrics.Steers is the run's record of what actually reached
	// the agent, so recording a resume that never started would claim an
	// update had been acted on when it never was.
	inFlight   *SteerMessage
	inFlightAt time.Time
	// inFlightFailures is how many resumes this message has already had
	// fail, carried with it across requeues so the retry is bounded.
	inFlightFailures int
	// wake is signalled whenever the Run loop may have work: a steer
	// arrived, or the run was settled. Buffered so a signal is never lost
	// against a loop that is not waiting yet.
	wake chan struct{}
}

func newCodexSteerQueue(sandboxName string, exec sandboxExecFunc, warn io.Writer) *codexSteerQueue {
	return &codexSteerQueue{
		sandboxName: sandboxName,
		sweep:       interruptSweep,
		exec:        exec,
		warn:        warn,
		wake:        make(chan struct{}, 1),
	}
}

// signal wakes the Run loop without blocking. The channel is a
// one-slot doorbell, not a queue: the loop re-reads the real state
// (pending, settled) after every wake.
func (q *codexSteerQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// noteThreadID records the rollout a resume will continue. codex reports
// the same thread_id on every resumed process, so the first one wins and
// RunMetrics.SessionID stays stable across the whole steered run.
func (q *codexSteerQueue) noteThreadID(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.threadID == "" {
		q.threadID = id
	}
	// thread.started on a resumed process is the moment the steer actually
	// reached the agent, which is what SteerResult.DeliveredAt promises. The
	// stake time is taken before the command is even built, so publishing it
	// would date the delivery by the whole process launch — earlier than it
	// happened, and out of order against anything else timestamped on the
	// run. Re-stamp here, where the evidence is.
	if q.inFlight != nil {
		q.inFlightAt = time.Now()
	}
}

func (q *codexSteerQueue) currentThreadID() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.threadID
}

// pendingSteer is a queued steer and how many resumes for it have already
// failed. The count travels with the message across a requeue, which is what
// makes the retry bounded.
type pendingSteer struct {
	msg      SteerMessage
	failures int
}

// maxCodexResumeAttempts is how many times one steer may be launched before
// it is abandoned to the queued run. Two: one ordinary attempt, one retry
// for a transient failure.
const maxCodexResumeAttempts = 2

// enqueue records a steer. accepted is false when the session has already
// settled, which is the caller's signal that the message was dropped rather
// than queued. Whether the running turn must be interrupted is NOT decided
// here: see interruptIfTurnRunning.
func (q *codexSteerQueue) enqueue(msg SteerMessage) (accepted bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.settled {
		return false
	}
	q.pending = append(q.pending, pendingSteer{msg: msg})
	return true
}

// interruptIfTurnRunning stops the codex process a turn is running in, so
// the loop can resume the thread with the steer as its next prompt. It does
// nothing between turns: the pending steer is picked up when the next turn
// starts, which is delivery, not a miss. Idle is the COMMON case on codex —
// its single ResultEvent arrives at stream EOF, so the runner's turn-end
// signal IS process exit and every steer after the first turn lands while
// nothing is running; sweeping then would spend the whole TERM grace
// interrupting nothing.
//
// The decision and the sweep are taken under ONE hold of q.mu. Deciding
// under the lock and sweeping after releasing it was a race: the turn could
// end and the next one begin in between, and the sweep then killed the
// process it had never inspected — a resumed turn dying before it answered.
// The sweep does not take q.mu, so holding it here cannot deadlock; it
// makes beginTurn wait until the sweep returns, which is exactly the
// serialization that keeps the decision true at the moment it acts.
func (q *codexSteerQueue) interruptIfTurnRunning() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.threadID == "" || !q.turnRunning {
		return
	}
	q.interruptLocked()
}

// beginTurn marks a codex process as about to run. It is called before the
// process starts, so a steer arriving early in a turn still interrupts it.
func (q *codexSteerQueue) beginTurn() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.turnRunning = true
}

// endTurn marks the process as finished, so a steer arriving while the loop
// waits for more work does not sweep a sandbox with nothing to kill.
func (q *codexSteerQueue) endTurn() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.turnRunning = false
}

// takePending removes the next steer to deliver.
func (q *codexSteerQueue) takePending() (SteerMessage, int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return SteerMessage{}, 0, false
	}
	next := q.pending[0]
	q.pending = q.pending[1:]
	return next.msg, next.failures, true
}

// settleIfSteerable is settle for a queue that may be nil, so a caller on a
// non-steerable run needs no branch of its own.
func (q *codexSteerQueue) settleIfSteerable() {
	if q != nil {
		q.settle()
	}
}

// settle records that no further steers will arrive. On codex this never
// kills anything: the current process is left to finish its turn, and the
// Run loop stops looping once it ends with nothing pending.
func (q *codexSteerQueue) settle() {
	q.mu.Lock()
	q.settled = true
	q.mu.Unlock()
	q.signal()
}

func (q *codexSteerQueue) isSettled() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.settled
}

// stakeDelivery records the steer the next process is being resumed for. at
// is a provisional timestamp: noteThreadID replaces it with the moment the
// resumed process reported its thread, which is when the steer actually
// reached the agent. It is kept as a fallback so a delivery confirmed by
// some other path is never recorded with a zero time.
func (q *codexSteerQueue) stakeDelivery(msg SteerMessage, failures int, at time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	staked := msg
	q.inFlight = &staked
	q.inFlightAt = at
	q.inFlightFailures = failures
}

// confirmDelivery resolves a staked delivery once the resumed process has
// been seen. delivered must be true only when that process reported a
// thread of its own: codex emits thread.started on a resume (verified
// live — the resumed process repeats the original thread_id), so an empty
// thread id means the resume never took and the steer did NOT reach the
// agent. Such a steer is dropped from the results rather than recorded, so
// the run never claims an update it did not deliver.
func (q *codexSteerQueue) confirmDelivery(delivered bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.inFlight == nil {
		return
	}
	if delivered {
		q.results = append(q.results, SteerResult{
			FollowUpRunID: q.inFlight.FollowUpRunID,
			DeliveredAt:   q.inFlightAt,
			Mode:          SteerModeResume,
		})
	} else {
		// The resume never opened a thread, so this steer did NOT reach the
		// agent — and dropping it here would silently discard a message
		// Steer already accepted. A nil return from Steer is the caller's
		// only signal that a codex steer was taken; ErrSteerAfterSettle
		// exists precisely so that nil never means "discarded". Put it back
		// at the front, so the next attempt retries it and FIFO order
		// survives the failure.
		//
		// Unconditional, including once settled. An earlier version skipped
		// the requeue when settled, on the reasoning that no further turn
		// would run — which is not what the loop does: nextCodexTurn drains
		// pending BEFORE it checks isSettled, so a settled session still
		// delivers what is queued. Skipping the requeue therefore dropped
		// the OLDER message while a newer one queued behind it was
		// delivered and recorded, out of order. Requeueing when nothing
		// drains is harmless — the message is discarded with the queue
		// either way — so unconditional is strictly the safer of the two.
		if failures := q.inFlightFailures + 1; failures < maxCodexResumeAttempts {
			q.requeueFrontLocked(pendingSteer{msg: *q.inFlight, failures: failures})
		} else if q.warn != nil {
			// Bounded, because a resume can fail for a reason retrying
			// cannot clear — a thread id the agent's session no longer
			// accepts fails identically every time. Requeueing without a
			// count relaunched the same failing resume until the run's
			// deadline, spending the whole budget on it. Dropping after the
			// cap leaves the steer undelivered, so no acknowledgement is
			// recorded, so no receipt claims it, and the queued run redoes
			// the work — the same fallback every other failure path takes.
			//
			// The invariant to preserve if this is ever touched again: a
			// steer is neither dropped without a record nor retried without
			// a bound. This code has held each half alone and been wrong
			// both times — first the silent drop, then the unbounded
			// requeue that replaced it.
			fmt.Fprintf(q.warn, "  Warning: giving up on follow-up run %d after %d failed resumes; the queued run will redo this work\n",
				q.inFlight.FollowUpRunID, maxCodexResumeAttempts)
		}
	}
	q.inFlight = nil
	q.inFlightAt = time.Time{}
	q.inFlightFailures = 0
}

// requeueFrontLocked returns a steer to the head of the queue after a failed
// delivery attempt. q.mu must be held.
func (q *codexSteerQueue) requeueFrontLocked(p pendingSteer) {
	q.pending = append([]pendingSteer{p}, q.pending...)
}

// requeueFront is requeueFrontLocked for a caller that holds no lock. It
// drops the message when the session has settled, for the same reason
// confirmDelivery does.
func (q *codexSteerQueue) requeueFront(msg SteerMessage, failures int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.settled {
		return
	}
	q.requeueFrontLocked(pendingSteer{msg: msg, failures: failures})
}

func (q *codexSteerQueue) steerResults() []SteerResult {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.results) == 0 {
		return nil
	}
	out := make([]SteerResult, len(q.results))
	copy(out, q.results)
	return out
}

// interruptSweep is the default codexSteerQueue.sweep: the stray-process
// sweep with the longer TERM→KILL grace. It keeps the injectable field's
// signature so tests can still replace the whole sweep.
func interruptSweep(execFn sandboxExecFunc, sandboxName string) (int, error) {
	return killStrayProcessesWithGrace(execFn, sandboxName, interruptStrayGrace)
}

// interruptLocked stops the in-sandbox codex so the Run loop can resume
// the thread with the steer. q.mu must be held; see interruptIfTurnRunning.
// A failed sweep is a warning, not an error: the
// steer stays queued and is delivered when the current process ends on its
// own, which is late rather than wrong.
//
// Every interrupt leaves a dangling tool call in the rollout — codex logs
// "Custom tool call output is missing for call id: ..." on the resume and
// tolerates it — so a steered run's transcript carries one per steer.
// That is also why this sweep uses interruptStrayGrace (10s) instead of the
// 2s the iteration boundary uses: unlike ClearIterationArtifacts, which
// sweeps leftovers from a run that is already over, this stops a process
// the runner intends to CONTINUE, and a fixed short grace leaves an agent
// no room to flush state on SIGTERM before the KILL lands (#6753). The
// exec timeout scales with the grace, so the longer wait is not truncated
// by the bound that exists to catch a hung gateway.
func (q *codexSteerQueue) interruptLocked() {
	if _, err := q.sweep(q.exec, q.sandboxName); err != nil && q.warn != nil {
		fmt.Fprintf(q.warn, "  Warning: could not interrupt the codex turn for a steer (it will be delivered when the current turn ends): %v\n",
			sanitizeOutput(err.Error()))
	}
}

// waitForWork blocks until a steer arrives, the run is settled, or ctx
// ends. It reports whether the loop should keep going. Without the
// ctx.Done arm a settled-but-never-steered run would sit here past its
// deadline with no way out.
func (q *codexSteerQueue) waitForWork(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-q.wake:
		return true
	}
}

// Steer implements Steerer for codex: it records the update and stops the
// current turn so the Run loop can resume the thread with it as the next
// prompt.
//
// The runner MUST hold its sandbox write lock across this call. That
// matters more here than on the live runtimes: the interrupt is the
// stray-process sweep, which kills every process of the sandbox user, so a
// credential refresher writing concurrently would be killed mid-write.
func (CodexRuntime) Steer(_ context.Context, sandboxName string, msg SteerMessage) error {
	q, ok := lookupCodexSteerQueue(sandboxName)
	if !ok {
		return ErrNoSteerSession
	}
	if !q.enqueue(msg) {
		return ErrSteerAfterSettle
	}
	q.interruptIfTurnRunning()
	q.signal()
	return nil
}

// Settle implements Steerer for codex: it stops the loop after the current
// process finishes. Nothing is killed — an interrupt here would discard
// the turn the agent is in the middle of, which is exactly what steering
// exists to avoid.
func (CodexRuntime) Settle(_ context.Context, sandboxName string) error {
	q, ok := lookupCodexSteerQueue(sandboxName)
	if !ok {
		return nil
	}
	q.settle()
	return nil
}

// codexSteerAggregator folds a steered codex run's several processes into
// one set of RunMetrics.
//
// Within one process, codex's usage on turn.completed is cumulative for
// the thread (the processor fills it from usage_from_last_total), so
// successive results REPLACE each other — that is why applyCodexMetrics
// assigns. Across an interrupt, the resumed process is a new `codex exec`
// whose counters start at zero and can only count the API calls it makes
// itself, so per-process totals must ADD. The resume probe shows this
// directly: the resumed process reported its own 16,068 input tokens, of
// which 15,903 were cached — the price of re-reading the thread, billed to
// that process alone.
type codexSteerAggregator struct {
	carried codexSteerTotals
	current codexSteerTotals
}

type codexSteerTotals struct {
	turns      int
	input      int
	output     int
	reasoning  int
	cacheRead  int
	cacheWrite int
}

func (t *codexSteerTotals) add(o codexSteerTotals) {
	t.turns += o.turns
	t.input += o.input
	t.output += o.output
	t.reasoning += o.reasoning
	t.cacheRead += o.cacheRead
	t.cacheWrite += o.cacheWrite
}

// onResult replaces the current process's totals and republishes the
// run-wide sum.
func (a *codexSteerAggregator) onResult(e ResultEvent, metrics *RunMetrics) {
	a.current = codexSteerTotals{
		turns:      e.NumTurns,
		input:      e.InputTokens,
		output:     e.OutputTokens,
		reasoning:  e.ReasoningTokens,
		cacheRead:  e.CacheReadInputTokens,
		cacheWrite: e.CacheCreationInputTokens,
	}
	a.publish(metrics)
}

// processEnded banks the finished process's totals so the next one adds to
// them instead of replacing them.
func (a *codexSteerAggregator) processEnded() {
	a.carried.add(a.current)
	a.current = codexSteerTotals{}
}

func (a *codexSteerAggregator) publish(metrics *RunMetrics) {
	total := a.carried
	total.add(a.current)
	metrics.NumTurns = total.turns
	metrics.InputTokens = total.input
	metrics.OutputTokens = total.output
	metrics.ReasoningTokens = total.reasoning
	metrics.CacheReadInputTokens = total.cacheRead
	metrics.CacheCreationInputTokens = total.cacheWrite
}

// Ensure CodexRuntime implements Steerer.
var _ Steerer = CodexRuntime{}
