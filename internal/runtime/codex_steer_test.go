package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCodexQueue() (*codexSteerQueue, *int) {
	sweeps := 0
	q := newCodexSteerQueue("sbx", nil, io.Discard)
	q.sweep = func(sandboxExecFunc, string) (int, error) {
		sweeps++
		return 1, nil
	}
	return q, &sweeps
}

// TestCodexSteerQueue_EarlySteerIsNotInterrupted covers the window before
// thread.started: there is no rollout to resume onto yet, so killing the
// process would throw the run away instead of steering it. The steer is
// queued and delivered when the current process ends.
func TestCodexSteerQueue_EarlySteerIsNotInterrupted(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	q.beginTurn()
	q.enqueue(SteerMessage{FollowUpRunID: 1})
	q.interruptIfTurnRunning()
	if *sweeps != 0 {
		t.Fatal("interrupted a process whose thread id was still unknown")
	}
	q.noteThreadID("01a066e2")
	q.enqueue(SteerMessage{FollowUpRunID: 2})
	q.interruptIfTurnRunning()
	if *sweeps != 1 {
		t.Fatal("expected an interrupt once the thread id was known and a turn was running")
	}
}

// TestCodexSteerQueue_IdleSteerDoesNotSweep is the defect this gate exists
// for. codex emits its single ResultEvent at stream EOF, so the runner's
// turn-end signal IS process exit: every steer after the first turn arrives
// while the Run loop is parked in waitForWork with nothing running.
// Interrupting there would spend the full TERM grace killing every process
// of the sandbox user — including anything the agent left behind — to
// interrupt nothing, while holding the runner's sandbox lock throughout.
func TestCodexSteerQueue_IdleSteerDoesNotSweep(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	q.noteThreadID("01a066e2")
	q.beginTurn()
	q.endTurn() // the first turn finished; the loop is now in waitForWork

	registerCodexSteerQueue("sbx-idle", q)
	defer unregisterCodexSteerQueue("sbx-idle")

	rt := CodexRuntime{}
	if err := rt.Steer(context.Background(), "sbx-idle", SteerMessage{FollowUpRunID: 8, Text: "late update"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if *sweeps != 0 {
		t.Errorf("swept an idle sandbox with no codex process running (%d sweeps)", *sweeps)
	}

	// The steer must still be delivered — by the resume, not the sweep.
	turn, ok := nextCodexTurn(context.Background(), q)
	if !ok {
		t.Fatal("an idle steer was not picked up by the resume loop")
	}
	if !strings.Contains(turn.Prompt, "late update") {
		t.Errorf("resume lost the steer text: %q", turn.Prompt)
	}
}

// TestCodexSteerQueue_SteerDuringALiveTurnInterrupts is the other side: a
// turn really is running, so the sweep is what stops it.
func TestCodexSteerQueue_SteerDuringALiveTurnInterrupts(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	q.noteThreadID("01a066e2")
	q.beginTurn()

	registerCodexSteerQueue("sbx-live", q)
	defer unregisterCodexSteerQueue("sbx-live")

	rt := CodexRuntime{}
	if err := rt.Steer(context.Background(), "sbx-live", SteerMessage{FollowUpRunID: 9, Text: "x"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if *sweeps != 1 {
		t.Errorf("a steer during a live turn did not interrupt it (%d sweeps)", *sweeps)
	}
}

func TestCodexSteerQueue_ThreadIDIsStable(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("first")
	q.noteThreadID("second")
	// codex reports the same thread_id on every resumed process; taking a
	// later one would let a bad read move RunMetrics.SessionID mid-run.
	if got := q.currentThreadID(); got != "first" {
		t.Errorf("thread id changed mid-run: %q", got)
	}
}

func TestCodexSteerQueue_PendingIsFIFO(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")
	q.enqueue(SteerMessage{FollowUpRunID: 1})
	q.enqueue(SteerMessage{FollowUpRunID: 2})

	first, _, _ := q.takePending()
	second, _, _ := q.takePending()
	if first.FollowUpRunID != 1 || second.FollowUpRunID != 2 {
		t.Errorf("steers delivered out of order: %d then %d", first.FollowUpRunID, second.FollowUpRunID)
	}
	if _, _, ok := q.takePending(); ok {
		t.Error("queue should be empty")
	}
}

func TestCodexSteerQueue_SettleRejectsLaterSteers(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	q.noteThreadID("t")
	q.settle()
	accepted := q.enqueue(SteerMessage{FollowUpRunID: 9})
	q.interruptIfTurnRunning()
	if *sweeps != 0 {
		t.Error("a steer after Settle must not interrupt the final turn")
	}
	if accepted {
		t.Error("a steer after Settle must be reported as dropped, not accepted")
	}
	if _, _, ok := q.takePending(); ok {
		t.Error("a steer after Settle must not be queued")
	}
}

// TestCodexSteerQueue_InterruptUsesTheSweep pins the interrupt primitive:
// killing the openshell client does not kill the process inside the
// sandbox, so the stray-process sweep is what stops the turn.
func TestCodexSteerQueue_InterruptUsesTheSweep(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	q.noteThreadID("t")
	q.beginTurn()

	rt := CodexRuntime{}
	registerCodexSteerQueue("sbx-codex", q)
	defer unregisterCodexSteerQueue("sbx-codex")

	if err := rt.Steer(context.Background(), "sbx-codex", SteerMessage{FollowUpRunID: 5}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if *sweeps != 1 {
		t.Errorf("expected exactly one interrupt sweep, got %d", *sweeps)
	}
}

// TestCodexSteerQueue_FailedInterruptStillQueues keeps a broken sweep from
// losing the update: it is delivered late (when the turn ends) rather than
// dropped.
func TestCodexSteerQueue_FailedInterruptStillQueues(t *testing.T) {
	q := newCodexSteerQueue("sbx", nil, io.Discard)
	q.sweep = func(sandboxExecFunc, string) (int, error) { return 0, errors.New("gateway down") }
	q.noteThreadID("t")
	q.beginTurn()

	registerCodexSteerQueue("sbx-badsweep", q)
	defer unregisterCodexSteerQueue("sbx-badsweep")

	rt := CodexRuntime{}
	if err := rt.Steer(context.Background(), "sbx-badsweep", SteerMessage{FollowUpRunID: 5}); err != nil {
		t.Fatalf("a failed interrupt must not fail the steer: %v", err)
	}
	if _, _, ok := q.takePending(); !ok {
		t.Error("the steer was dropped when the interrupt failed")
	}
}

func TestCodexSteer_NoRegisteredSession(t *testing.T) {
	rt := CodexRuntime{}
	if err := rt.Steer(context.Background(), "nope", SteerMessage{}); !errors.Is(err, ErrNoSteerSession) {
		t.Fatalf("expected ErrNoSteerSession, got %v", err)
	}
	if err := rt.Settle(context.Background(), "nope"); err != nil {
		t.Fatalf("Settle on a finished run must be a no-op, got %v", err)
	}
}

func TestNextCodexTurn_ResumesWithTheEnvelope(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("01a066e2-a54e-7222-84ae-53549f3d2316")
	q.enqueue(SteerMessage{FollowUpRunID: 7, Actor: "octocat", Event: "issue_comment", Text: "cover the error path"})

	turn, ok := nextCodexTurn(context.Background(), q)
	if !ok {
		t.Fatal("expected another turn for the queued steer")
	}
	if turn.ResumeThreadID != "01a066e2-a54e-7222-84ae-53549f3d2316" {
		t.Errorf("resume targeted the wrong thread: %q", turn.ResumeThreadID)
	}
	if !strings.Contains(turn.Prompt, "cover the error path") {
		t.Errorf("resume prompt lost the steer text: %q", turn.Prompt)
	}
	// Staked, not yet recorded: the resumed process has not run, so as far
	// as the runner is concerned nothing has been delivered.
	if got := q.steerResults(); len(got) != 0 {
		t.Errorf("delivery recorded before the resumed process ran: %+v", got)
	}
}

// TestCodexSteerQueue_ConfirmedResumeIsRecorded is the delivery half: once
// the resumed process reports a thread of its own, the steer really did
// reach the agent and the runner may mark its follow-up run consumed.
func TestCodexSteerQueue_ConfirmedResumeIsRecorded(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("01a066e2")
	q.enqueue(SteerMessage{FollowUpRunID: 7, Text: "cover the error path"})

	before := time.Now()
	if _, ok := nextCodexTurn(context.Background(), q); !ok {
		t.Fatal("expected a resume turn")
	}
	q.confirmDelivery(true)

	got := q.steerResults()
	if len(got) != 1 {
		t.Fatalf("expected exactly one recorded delivery, got %d", len(got))
	}
	if got[0].FollowUpRunID != 7 {
		t.Errorf("FollowUpRunID not carried through from the SteerMessage: %d", got[0].FollowUpRunID)
	}
	if got[0].Mode != SteerModeResume {
		t.Errorf("expected mode %q, got %q", SteerModeResume, got[0].Mode)
	}
	// DeliveredAt is the resume's start, staked before the process ran.
	if got[0].DeliveredAt.Before(before) || got[0].DeliveredAt.After(time.Now()) {
		t.Errorf("DeliveredAt is not the resume start time: %v", got[0].DeliveredAt)
	}
}

// TestCodexSteerQueue_UnconfirmedResumeIsNotRecorded is the finding this
// two-phase record exists for. codex emits thread.started on a resume, so a
// resumed process that reported no thread never opened one and the steer
// did NOT reach the agent. Recording it anyway would let the runner mark
// the follow-up run consumed from RunMetrics.Steers, and the queued run
// would then skip an update nobody acted on — losing it outright rather
// than merely delaying it.
func TestCodexSteerQueue_UnconfirmedResumeIsNotRecorded(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("01a066e2")
	q.enqueue(SteerMessage{FollowUpRunID: 7, Text: "cover the error path"})

	if _, ok := nextCodexTurn(context.Background(), q); !ok {
		t.Fatal("expected a resume turn")
	}
	// The resume failed to start, or codex never opened the thread.
	q.confirmDelivery(false)

	if got := q.steerResults(); len(got) != 0 {
		t.Fatalf("a resume that never opened a thread was recorded as delivered: %+v", got)
	}
	// And it must not linger: a second confirm cannot resurrect it.
	q.confirmDelivery(true)
	if got := q.steerResults(); len(got) != 0 {
		t.Errorf("a discarded delivery was recorded by a later confirm: %+v", got)
	}
}

// TestCodexSteerQueue_ConfirmWithNothingStakedIsANoOp covers the first turn
// of every run, which is not a resume and has nothing to confirm.
func TestCodexSteerQueue_ConfirmWithNothingStakedIsANoOp(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.confirmDelivery(true)
	if got := q.steerResults(); len(got) != 0 {
		t.Errorf("confirming with nothing staked invented a delivery: %+v", got)
	}
}

func TestNextCodexTurn_StopsWhenSettled(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")
	q.settle()
	if _, ok := nextCodexTurn(context.Background(), q); ok {
		t.Error("a settled run with nothing pending must stop looping")
	}
}

// TestNextCodexTurn_StopsWhenContextEnds is the deadline arm: without it a
// steerable run that is never settled would block here past its budget.
func TestNextCodexTurn_StopsWhenContextEnds(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := nextCodexTurn(ctx, q); ok {
		t.Error("a cancelled context must end the resume loop")
	}
}

// TestNextCodexTurn_StopsWhenNoThreadEverStarted covers a run that died
// before thread.started: the steer cannot be delivered because there is no
// rollout to resume, and looping would spin on a resume that cannot be
// built.
func TestNextCodexTurn_StopsWhenNoThreadEverStarted(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.enqueue(SteerMessage{FollowUpRunID: 3}) // no thread id noted
	if _, ok := nextCodexTurn(context.Background(), q); ok {
		t.Error("expected the loop to stop with no thread to resume onto")
	}
}

// TestNextCodexTurn_WakesOnALateSteer covers the blocking path: the run is
// not settled and nothing is pending, so the loop parks until Steer rings
// the doorbell.
func TestNextCodexTurn_WakesOnALateSteer(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")

	done := make(chan codexTurn, 1)
	go func() {
		turn, ok := nextCodexTurn(context.Background(), q)
		if ok {
			done <- turn
		}
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	q.enqueue(SteerMessage{FollowUpRunID: 11, Text: "late update"})
	q.signal()

	select {
	case turn, ok := <-done:
		if !ok {
			t.Fatal("loop stopped instead of taking the late steer")
		}
		if !strings.Contains(turn.Prompt, "late update") {
			t.Errorf("wrong prompt: %q", turn.Prompt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nextCodexTurn did not wake on a late steer")
	}
}

// TestBuildCodexTurnCommand_ResumeShape pins the composed resume command
// against codex 0.152.1, where `resume` is a subcommand of `codex exec`:
// `codex exec resume --help` offers -c, -m, -o, --json,
// --skip-git-repo-check and both --dangerously-bypass-* flags but NOT
// -C/--cd, which exists only on `codex exec`. So every flag must precede
// `resume`, and the stdin sentinel must stay last.
func TestBuildCodexTurnCommand_ResumeShape(t *testing.T) {
	params := RunParams{RepoDir: "/sandbox/workspace/repo", SandboxName: "sbx"}
	cmd := buildCodexTurnCommand(params, "gpt-5.6", "", false, codexRunnerHeldDigestSet{},
		codexTurn{ResumeThreadID: "01a066e2", Prompt: "the steer envelope"})

	resumeAt := strings.Index(cmd, " resume '01a066e2'")
	if resumeAt < 0 {
		t.Fatalf("resume not composed into the command:\n%s", cmd)
	}
	if !strings.HasSuffix(cmd, " -") {
		t.Errorf("the stdin sentinel must stay last:\n%s", cmd)
	}
	// -C is not a flag of `resume`; it must appear before it.
	cdAt := strings.Index(cmd, "-C ")
	if cdAt < 0 || cdAt > resumeAt {
		t.Errorf("-C must precede resume (it is not a resume flag):\n%s", cmd)
	}
	// The prompt still goes in on stdin, never argv.
	if strings.Contains(cmd[resumeAt:], "the steer envelope") {
		t.Errorf("steer text must not appear after resume on argv:\n%s", cmd)
	}
	if !strings.Contains(cmd, "printf '%s' 'the steer envelope'") {
		t.Errorf("steer text should be piped in on stdin:\n%s", cmd)
	}
}

func TestBuildCodexTurnCommand_ZeroTurnIsTodaysCommand(t *testing.T) {
	params := RunParams{RepoDir: "/repo", SandboxName: "sbx"}
	base := buildCodexRunCommand(params, "gpt-5.6", "", false, codexRunnerHeldDigestSet{})
	zero := buildCodexTurnCommand(params, "gpt-5.6", "", false, codexRunnerHeldDigestSet{}, codexTurn{})
	if base != zero {
		t.Errorf("the zero codexTurn must render today's command exactly:\n%s\n---\n%s", base, zero)
	}
	if strings.Contains(base, "resume") {
		t.Errorf("a first turn must not carry resume:\n%s", base)
	}
}

// TestCodexSteerAggregator_SumsAcrossProcesses is the counterpart of the
// Claude rule and goes the other way. Within one process codex's usage is
// cumulative for the thread, so results replace; across an interrupt the
// resumed process is a new `codex exec` whose counters start at zero, so
// per-process totals must add.
func TestCodexSteerAggregator_SumsAcrossProcesses(t *testing.T) {
	var m RunMetrics
	a := &codexSteerAggregator{}

	// Process 1: two turns, the second cumulative over the first.
	a.onResult(ResultEvent{NumTurns: 1, InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 50}, &m)
	a.onResult(ResultEvent{NumTurns: 2, InputTokens: 300, OutputTokens: 25, CacheReadInputTokens: 120}, &m)
	if m.InputTokens != 300 || m.NumTurns != 2 {
		t.Fatalf("within a process, results must replace: in=%d turns=%d", m.InputTokens, m.NumTurns)
	}
	a.processEnded()

	// Process 2 after a steer: its own fresh totals.
	a.onResult(ResultEvent{NumTurns: 1, InputTokens: 16068, OutputTokens: 27, CacheReadInputTokens: 15903}, &m)
	if m.InputTokens != 300+16068 || m.OutputTokens != 25+27 || m.CacheReadInputTokens != 120+15903 {
		t.Errorf("across processes, totals must add: in=%d out=%d cacheRead=%d",
			m.InputTokens, m.OutputTokens, m.CacheReadInputTokens)
	}
	if m.NumTurns != 3 {
		t.Errorf("turns must add across processes, got %d", m.NumTurns)
	}
}

// TestCodexSettle_DoesNotKillTheCurrentTurn is the codex-specific settle
// rule: unlike an interrupt, Settle must leave the running process alone
// and merely stop the loop after it finishes. Killing here would discard
// the turn the agent is in the middle of, which is what steering exists to
// avoid.
func TestCodexSettle_DoesNotKillTheCurrentTurn(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	q.noteThreadID("t")
	registerCodexSteerQueue("sbx-settle-codex", q)
	defer unregisterCodexSteerQueue("sbx-settle-codex")

	rt := CodexRuntime{}
	if err := rt.Settle(context.Background(), "sbx-settle-codex"); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if *sweeps != 0 {
		t.Errorf("Settle interrupted the in-flight turn (%d sweeps)", *sweeps)
	}
	if !q.isSettled() {
		t.Error("Settle did not mark the run settled")
	}
}

// TestParseCodexStreamWith_PublishesThreadIDMidStream is the regression
// guard for the defect that made steering a no-op on codex: the thread id
// was only published when the stream ended, but a steer can interrupt only
// once there is a thread to resume onto — and for codex the first process
// is normally the whole run. The reader below blocks after thread.started,
// so the assertion can only pass if the id was published while the stream
// was still open.
func TestParseCodexStreamWith_PublishesThreadIDMidStream(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	got := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		_, _ = parseCodexStreamWith(pr, func(AgentEvent) {}, func(id string) { got <- id })
		close(done)
	}()

	if _, err := io.WriteString(pw, `{"type":"thread.started","thread_id":"01a066e2-a54e"}`+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case id := <-got:
		if id != "01a066e2-a54e" {
			t.Errorf("wrong thread id published: %q", id)
		}
	case <-done:
		t.Fatal("the parser finished before publishing the thread id")
	case <-time.After(5 * time.Second):
		t.Fatal("thread id was not published until the stream ended: a steer during the first turn could never interrupt")
	}
}

// TestCodexSteerQueue_InterruptsDuringTheFirstTurn wires the parser hook to
// the queue exactly as runCodexTurn does, and checks the end-to-end
// consequence: a steer arriving after thread.started but before the process
// ends takes the interrupt path.
func TestCodexSteerQueue_InterruptsDuringTheFirstTurn(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	registerCodexSteerQueue("sbx-firstturn", q)
	defer unregisterCodexSteerQueue("sbx-firstturn")

	pr, pw := io.Pipe()
	defer pw.Close()
	parsed := make(chan struct{})
	go func() {
		_, _ = parseCodexStreamWith(pr, func(AgentEvent) {}, q.noteThreadID)
		close(parsed)
	}()

	if _, err := io.WriteString(pw, `{"type":"thread.started","thread_id":"01a066e2"}`+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Wait for the hook to land rather than racing it.
	deadline := time.Now().Add(5 * time.Second)
	for q.currentThreadID() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if q.currentThreadID() == "" {
		t.Fatal("thread id never reached the queue")
	}

	q.beginTurn() // the first process is running while its stream is parsed

	rt := CodexRuntime{}
	if err := rt.Steer(context.Background(), "sbx-firstturn", SteerMessage{FollowUpRunID: 3, Text: "x"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if *sweeps != 1 {
		t.Errorf("a steer during the first turn did not interrupt it (%d sweeps): it would have waited for the turn to end on its own", *sweeps)
	}
}

// TestCodexSteer_AfterSettleIsRefused: codex drops a steer that arrives
// after Settle, and Steer must say so rather than returning nil. enqueue
// reports the drop separately from "queued, no interrupt needed", which is
// the ordinary between-turns case and is NOT an error.
func TestCodexSteer_AfterSettleIsRefused(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	registerCodexSteerQueue("sbx-codex-after-settle", q)
	defer unregisterCodexSteerQueue("sbx-codex-after-settle")
	q.noteThreadID("t")
	q.settle()

	err := CodexRuntime{}.Steer(context.Background(), "sbx-codex-after-settle", SteerMessage{FollowUpRunID: 5, Text: "late"})
	if !errors.Is(err, ErrSteerAfterSettle) {
		t.Fatalf("expected ErrSteerAfterSettle, got %v", err)
	}
	if *sweeps != 0 {
		t.Errorf("a refused steer must not interrupt the final turn, got %d sweeps", *sweeps)
	}
}

// TestCodexSteer_BetweenTurnsIsNotAnError guards the distinction the error
// depends on: a steer accepted while no process is running needs no
// interrupt, and that is success, not a drop.
func TestCodexSteer_BetweenTurnsIsNotAnError(t *testing.T) {
	q, _ := newTestCodexQueue()
	registerCodexSteerQueue("sbx-codex-idle", q)
	defer unregisterCodexSteerQueue("sbx-codex-idle")
	q.noteThreadID("t")

	if err := (CodexRuntime{}).Steer(context.Background(), "sbx-codex-idle", SteerMessage{FollowUpRunID: 6}); err != nil {
		t.Fatalf("a steer between turns is delivered, got %v", err)
	}
}

// TestCodexTurnTimeout_SteeredTurnsShareOneBudget is the defect this
// guards: every turn used to be given the full params.Timeout, so a run
// that absorbed N steers could run for N times its budget and outlast the
// credential that has to post its result.
func TestCodexTurnTimeout_SteeredTurnsShareOneBudget(t *testing.T) {
	budget := 20 * time.Minute

	t.Run("no deadline leaves the budget alone", func(t *testing.T) {
		got, err := codexTurnTimeout(context.Background(), budget)
		if err != nil || got != budget {
			t.Fatalf("got (%v, %v), want (%v, nil)", got, err, budget)
		}
	})

	t.Run("a later turn gets only what is left", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(3*time.Minute))
		defer cancel()
		got, err := codexTurnTimeout(ctx, budget)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got > 3*time.Minute || got < 2*time.Minute {
			t.Errorf("expected roughly the 3m remaining, got %v", got)
		}
	})

	t.Run("an exhausted budget refuses rather than running unbounded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		if _, err := codexTurnTimeout(ctx, budget); err == nil {
			t.Fatal("a spent budget must not start another turn; a zero timeout reads as no limit")
		}
	})
}

// TestCodexSteerQueue_SweepCannotKillTheNextTurn constructs the interleaving
// the lock hold exists for. A steer arrives while a turn is running, so it
// must interrupt; the sweep is held open while that turn ends and the loop
// starts the NEXT one. If the decision and the sweep were not taken under one
// hold of q.mu, the sweep would kill a process it never inspected — a resumed
// turn dying before it answered.
func TestCodexSteerQueue_SweepCannotKillTheNextTurn(t *testing.T) {
	inSweep := make(chan struct{})
	releaseSweep := make(chan struct{})
	var sweeps int32

	q := newCodexSteerQueue("sbx", nil, io.Discard)
	q.sweep = func(sandboxExecFunc, string) (int, error) {
		atomic.AddInt32(&sweeps, 1)
		close(inSweep)
		<-releaseSweep
		return 1, nil
	}
	q.noteThreadID("t")
	q.beginTurn()

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.enqueue(SteerMessage{FollowUpRunID: 1})
		q.interruptIfTurnRunning()
	}()

	<-inSweep // the sweep is now in flight, holding q.mu

	// The run loop tries to end this turn and begin the next one. Both take
	// q.mu, so both must block until the sweep finishes.
	turned := make(chan struct{})
	go func() {
		defer close(turned)
		q.endTurn()
		q.beginTurn()
	}()

	select {
	case <-turned:
		t.Fatal("the turn advanced while a sweep was in flight: the sweep can kill the next turn's process")
	case <-time.After(100 * time.Millisecond):
		// Correct: the turn transition is serialized behind the sweep.
	}

	close(releaseSweep)
	<-done
	<-turned

	if got := atomic.LoadInt32(&sweeps); got != 1 {
		t.Errorf("expected exactly one sweep, got %d", got)
	}
}

// TestCodexSteerQueue_DeliveredAtIsWhenTheThreadStarted pins the field's
// contract. The stake happens before the command is even built, so
// publishing that timestamp would date a delivery by the whole process
// launch — earlier than it happened. thread.started on the resumed process
// is the moment the steer reached the agent.
func TestCodexSteerQueue_DeliveredAtIsWhenTheThreadStarted(t *testing.T) {
	q, _ := newTestCodexQueue()

	staked := time.Now().Add(-5 * time.Second) // as if the launch took 5s
	q.stakeDelivery(SteerMessage{FollowUpRunID: 42}, 0, staked)
	q.noteThreadID("t-resumed")
	q.confirmDelivery(true)

	results := q.steerResults()
	require.Len(t, results, 1)
	if !results[0].DeliveredAt.After(staked) {
		t.Errorf("DeliveredAt %v is the stake time, not the moment the thread started", results[0].DeliveredAt)
	}
	assert.Equal(t, SteerModeResume, results[0].Mode)
}

// TestCodexSteerQueue_DeliveredAtFallsBackToTheStake keeps the fallback
// honest: a delivery confirmed without a thread report must still carry a
// real time rather than the zero value.
func TestCodexSteerQueue_DeliveredAtFallsBackToTheStake(t *testing.T) {
	q, _ := newTestCodexQueue()
	staked := time.Now().Add(-time.Second)
	q.stakeDelivery(SteerMessage{FollowUpRunID: 43}, 0, staked)
	q.confirmDelivery(true)

	results := q.steerResults()
	require.Len(t, results, 1)
	assert.False(t, results[0].DeliveredAt.IsZero(), "a recorded delivery must carry a time")
}

// TestCodexSteerQueue_FailedResumeKeepsTheSteerDeliverable is the property
// the old code broke: Steer returning nil is the caller's only signal that a
// codex steer was accepted, so a resume that never opened a thread must put
// the message back rather than drop it. Dropping it lost an update the
// caller had already been told was taken.
func TestCodexSteerQueue_FailedResumeKeepsTheSteerDeliverable(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")

	require.True(t, q.enqueue(SteerMessage{FollowUpRunID: 1, Text: "first"}))
	require.True(t, q.enqueue(SteerMessage{FollowUpRunID: 2, Text: "second"}))

	staked, _, ok := q.takePending()
	require.True(t, ok)
	require.Equal(t, int64(1), staked.FollowUpRunID)
	q.stakeDelivery(staked, 0, time.Now())

	// The resumed process never reported a thread.
	q.confirmDelivery(false)

	assert.Empty(t, q.steerResults(), "an unconfirmed resume must not be recorded as delivered")

	next, _, ok := q.takePending()
	require.True(t, ok, "the undelivered steer must still be queued")
	assert.Equal(t, int64(1), next.FollowUpRunID,
		"it must go back to the FRONT: a failed attempt must not reorder the queue")
}

// TestCodexSteerQueue_SettleDoesNotDropTheOlderSteer is the FIFO property a
// conditional requeue broke. The loop drains pending BEFORE it checks
// isSettled, so a settled session still delivers what is queued — which
// means skipping the requeue on settle dropped the OLDER message while a
// newer one queued behind it went out and was recorded, out of order.
func TestCodexSteerQueue_SettleDoesNotDropTheOlderSteer(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")

	require.True(t, q.enqueue(SteerMessage{FollowUpRunID: 1, Text: "older"}))
	require.True(t, q.enqueue(SteerMessage{FollowUpRunID: 2, Text: "newer"}))

	older, _, ok := q.takePending()
	require.True(t, ok)
	require.Equal(t, int64(1), older.FollowUpRunID)
	q.stakeDelivery(older, 0, time.Now())

	// Settle wins the race, and only then does the failed resume resolve.
	q.settle()
	q.confirmDelivery(false)

	// The loop drains pending even when settled, so the older message must
	// still be there and must still come first.
	next, _, ok := q.takePending()
	require.True(t, ok, "the older steer must not be dropped just because settle won the race")
	assert.Equal(t, int64(1), next.FollowUpRunID,
		"the older steer must still precede the one queued behind it")

	after, _, ok := q.takePending()
	require.True(t, ok)
	assert.Equal(t, int64(2), after.FollowUpRunID)
	assert.Empty(t, q.steerResults(), "neither was delivered, so neither is recorded")
}

// TestCodexSteerQueue_PersistentResumeFailureIsBounded is the regression for
// a spin this queue briefly had. Requeueing a failed resume unconditionally
// fixed a silent drop but created the opposite defect: a resume that fails
// for a reason retrying cannot clear — a thread id the session no longer
// accepts — was retaken and relaunched every time, so the run spent its
// whole deadline relaunching the same failing process. Measured at 50 of 50
// iterations with no cap before the count was added.
func TestCodexSteerQueue_PersistentResumeFailureIsBounded(t *testing.T) {
	var warn bytes.Buffer
	q := newCodexSteerQueue("sbx", nil, &warn)
	q.sweep = func(sandboxExecFunc, string) (int, error) { return 0, nil }
	q.noteThreadID("t-established")
	require.True(t, q.enqueue(SteerMessage{FollowUpRunID: 4242, Text: "one steer"}))

	// A bounded context: with the queue drained and not settled,
	// nextCodexTurn waits for work, and the test's question is how many
	// launches happened before that point.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	launches := 0
	for i := 0; i < 50; i++ {
		_, more := nextCodexTurn(ctx, q)
		if !more {
			break
		}
		launches++
		// The resumed process never reports a thread, every time.
		q.confirmDelivery(false)
	}

	assert.Equal(t, maxCodexResumeAttempts, launches,
		"a resume that always fails must be abandoned after the cap, not relaunched until the deadline")

	_, _, ok := q.takePending()
	assert.False(t, ok, "the abandoned steer must not remain queued")
	assert.Empty(t, q.steerResults(), "it was never delivered, so nothing may be recorded")
	assert.Contains(t, warn.String(), "4242",
		"the warning must name the follow-up run that was given up on")
	assert.Contains(t, warn.String(), "queued run will redo")
}

// TestCodexSteerQueue_OneFailureStillRetries keeps the silent-drop fix
// standing: the bound must not turn the first, possibly transient, failure
// into an immediate drop.
func TestCodexSteerQueue_OneFailureStillRetries(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")
	require.True(t, q.enqueue(SteerMessage{FollowUpRunID: 7}))

	_, more := nextCodexTurn(context.Background(), q)
	require.True(t, more)
	q.confirmDelivery(false) // first failure

	msg, failures, ok := q.takePending()
	require.True(t, ok, "one failure must not abandon the steer")
	assert.Equal(t, int64(7), msg.FollowUpRunID)
	assert.Equal(t, 1, failures, "the failure count must travel with the message")
}

// TestCodexSteerQueue_ConfirmedResumeDrainsFirstTime is the control: a
// delivery that succeeds ends the loop immediately.
func TestCodexSteerQueue_ConfirmedResumeDrainsFirstTime(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")
	require.True(t, q.enqueue(SteerMessage{FollowUpRunID: 9}))

	_, more := nextCodexTurn(context.Background(), q)
	require.True(t, more)
	q.noteThreadID("t")
	q.confirmDelivery(true)

	_, _, ok := q.takePending()
	assert.False(t, ok, "a confirmed delivery must drain on the first attempt")
	assert.Len(t, q.steerResults(), 1)
}
