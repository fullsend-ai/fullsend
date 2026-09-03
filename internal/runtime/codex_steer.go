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
	// sweep is the primitive; injected for tests.
	sweep func(execFn sandboxExecFunc, sandboxName string) (int, error)
	// exec runs the sweep in the sandbox (sandbox.Exec in production).
	exec sandboxExecFunc
	// warn receives a note when an interrupt could not be delivered.
	warn io.Writer

	mu sync.Mutex
	// threadID is captured from thread.started. Until it is known there is
	// nothing to resume, so an early steer is queued rather than acted on.
	threadID string
	pending  []SteerMessage
	settled  bool
	results  []SteerResult
	// wake is signalled whenever the Run loop may have work: a steer
	// arrived, or the run was settled. Buffered so a signal is never lost
	// against a loop that is not waiting yet.
	wake chan struct{}
}

func newCodexSteerQueue(sandboxName string, exec sandboxExecFunc, warn io.Writer) *codexSteerQueue {
	return &codexSteerQueue{
		sandboxName: sandboxName,
		sweep:       killStrayProcesses,
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
}

func (q *codexSteerQueue) currentThreadID() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.threadID
}

// enqueue records a steer and reports whether the running process should
// be interrupted for it. It should not be when the thread id is still
// unknown: the process is in its first moments, there is nothing to resume
// onto, and killing it would throw the run away rather than steer it. Such
// a steer is delivered when the current process ends on its own instead.
func (q *codexSteerQueue) enqueue(msg SteerMessage) (interrupt bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.settled {
		return false
	}
	q.pending = append(q.pending, msg)
	return q.threadID != ""
}

// takePending removes the next steer to deliver.
func (q *codexSteerQueue) takePending() (SteerMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return SteerMessage{}, false
	}
	msg := q.pending[0]
	q.pending = q.pending[1:]
	return msg, true
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

// recordDelivery notes that a resumed process is starting for msg. On
// codex the delivery time is when the resume begins, because that is when
// the message actually enters the thread.
func (q *codexSteerQueue) recordDelivery(msg SteerMessage, at time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.results = append(q.results, SteerResult{
		FollowUpRunID: msg.FollowUpRunID,
		DeliveredAt:   at,
		Mode:          steerModeResume,
	})
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

// interrupt stops the in-sandbox codex so the Run loop can resume the
// thread with the steer. A failed sweep is a warning, not an error: the
// steer stays queued and is delivered when the current process ends on its
// own, which is late rather than wrong.
func (q *codexSteerQueue) interrupt() {
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
		return errNoSteerSession
	}
	if q.enqueue(msg) {
		q.interrupt()
	}
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
