package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
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
	q, _ := newTestCodexQueue()
	if q.enqueue(SteerMessage{FollowUpRunID: 1}) {
		t.Fatal("interrupted a process whose thread id was still unknown")
	}
	q.noteThreadID("01a066e2")
	if !q.enqueue(SteerMessage{FollowUpRunID: 2}) {
		t.Fatal("expected an interrupt once the thread id was known")
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

	first, _ := q.takePending()
	second, _ := q.takePending()
	if first.FollowUpRunID != 1 || second.FollowUpRunID != 2 {
		t.Errorf("steers delivered out of order: %d then %d", first.FollowUpRunID, second.FollowUpRunID)
	}
	if _, ok := q.takePending(); ok {
		t.Error("queue should be empty")
	}
}

func TestCodexSteerQueue_SettleRejectsLaterSteers(t *testing.T) {
	q, _ := newTestCodexQueue()
	q.noteThreadID("t")
	q.settle()
	if q.enqueue(SteerMessage{FollowUpRunID: 9}) {
		t.Error("a steer after Settle must not interrupt the final turn")
	}
	if _, ok := q.takePending(); ok {
		t.Error("a steer after Settle must not be queued")
	}
}

// TestCodexSteerQueue_InterruptUsesTheSweep pins the interrupt primitive:
// killing the openshell client does not kill the process inside the
// sandbox, so the stray-process sweep is what stops the turn.
func TestCodexSteerQueue_InterruptUsesTheSweep(t *testing.T) {
	q, sweeps := newTestCodexQueue()
	q.noteThreadID("t")

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

	registerCodexSteerQueue("sbx-badsweep", q)
	defer unregisterCodexSteerQueue("sbx-badsweep")

	rt := CodexRuntime{}
	if err := rt.Steer(context.Background(), "sbx-badsweep", SteerMessage{FollowUpRunID: 5}); err != nil {
		t.Fatalf("a failed interrupt must not fail the steer: %v", err)
	}
	if _, ok := q.takePending(); !ok {
		t.Error("the steer was dropped when the interrupt failed")
	}
}

func TestCodexSteer_NoRegisteredSession(t *testing.T) {
	rt := CodexRuntime{}
	if err := rt.Steer(context.Background(), "nope", SteerMessage{}); !errors.Is(err, errNoSteerSession) {
		t.Fatalf("expected errNoSteerSession, got %v", err)
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
	if got[0].Mode != steerModeResume {
		t.Errorf("expected mode %q, got %q", steerModeResume, got[0].Mode)
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

// TestSteerEnvelopeOpeningLineIsStable pins the first line of the
// envelope. The agent definitions in fullsend-ai/agents match on it to
// recognise a runner amendment, so it is a cross-repo interface: changing
// it silently turns every steer back into ignored text.
func TestSteerEnvelopeOpeningLineIsStable(t *testing.T) {
	const opening = "Runner update: your task inputs changed after this run started."
	for _, msg := range []SteerMessage{
		{Text: "x"},
		{FollowUpRunID: 1, Actor: "octocat", Event: "issue_comment", HeadSHA: "abc", Text: "x"},
	} {
		got := renderSteerEnvelope(msg)
		if !strings.HasPrefix(got, opening) {
			t.Errorf("envelope opening line changed; fullsend-ai/agents matches on it:\n%s", got)
		}
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
