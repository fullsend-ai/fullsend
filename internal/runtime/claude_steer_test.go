package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestBuildRunCommand_Steerable(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/repo", AgentBaseName: "review", Steerable: true})

	for _, want := range []string{
		"{ unset -f tail echo wait; PATH=\"$FULLSEND_STEER_PATH\" tail -n +1 -f '/sandbox/claude-config/steer-inbox.ndjson' &",
		"echo $! > '/sandbox/claude-config/steer-feeder.pid'",
		`wait ; } | "$FULLSEND_CLAUDE_BIN"`,
		"--input-format stream-json",
		"--replay-user-messages",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("steerable command missing %q:\n%s", want, cmd)
		}
	}
	// The prompt must reach the agent through the mailbox rather than the
	// launch command, so it never lands on the long-lived agent process's
	// command line, which a `ps` during the run would show. (It still
	// transits the intermediate `sh -c` argv of the mailbox write; see
	// buildRunCommand.)
	if strings.Contains(cmd, DefaultAgentPrompt) {
		t.Errorf("steerable command still passes the prompt on argv:\n%s", cmd)
	}
}

// TestBuildRunCommand_SteerableClearsLoaderEnv pins the loader hygiene on
// the steerable launch: after the agent-writable .env and before the feeder
// and agent start, so a .env exporting NODE_OPTIONS=--require or LD_PRELOAD
// cannot run code inside the pinned claude and forge the delivery echo.
func TestBuildRunCommand_SteerableClearsLoaderEnv(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/repo", AgentBaseName: "review", Steerable: true})

	env := strings.Index(cmd, ". /sandbox/workspace/.env")
	unset := strings.Index(cmd, claudeSteerLoaderEnvUnset)
	feeder := strings.Index(cmd, "{ unset -f tail")
	if env < 0 || unset < 0 || feeder < 0 || !(env < unset && unset < feeder) {
		t.Fatalf("want .env (%d) < loader unset (%d) < feeder (%d):\n%s", env, unset, feeder, cmd)
	}
	for _, name := range []string{"NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT"} {
		if !strings.Contains(claudeSteerLoaderEnvUnset, name) {
			t.Errorf("loader unset misses %s", name)
		}
	}

	// The ordinary launch is unchanged.
	if plain := buildRunCommand(RunParams{RepoDir: "/repo", AgentBaseName: "review"}); strings.Contains(plain, claudeSteerLoaderEnvUnset) {
		t.Errorf("non-steerable launch gained the loader unset:\n%s", plain)
	}
}

// TestBuildRunCommand_SteerableKeepsFeedbackPromptOffArgv covers the
// validation loop's retry prompt specifically: it carries the previous
// iteration's failure text, which is the most attacker-influenced string
// the runner ever hands a runtime, and it must not sit on the agent
// process's own command line for the length of the run.
func TestBuildRunCommand_SteerableKeepsFeedbackPromptOffArgv(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/repo", AgentBaseName: "fix", Prompt: "previous iteration failed: SECRETMARKER", Steerable: true})
	if strings.Contains(cmd, "SECRETMARKER") {
		t.Errorf("retry prompt leaked onto argv:\n%s", cmd)
	}
}

// TestBuildRunCommand_NotSteerableUnchanged pins the ordinary path: no
// feeder, no input-format flags, prompt still on argv.
func TestBuildRunCommand_NotSteerableUnchanged(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/repo", AgentBaseName: "review"})
	for _, unwanted := range []string{"tail -n +1 -f", "--input-format", "--replay-user-messages", steerMailboxName} {
		if strings.Contains(cmd, unwanted) {
			t.Errorf("non-steerable command gained %q:\n%s", unwanted, cmd)
		}
	}
	if !strings.HasSuffix(cmd, "'"+DefaultAgentPrompt+"'") {
		t.Errorf("non-steerable command lost its argv prompt:\n%s", cmd)
	}
}

func TestClaudeInputLine_MultilineStaysOneLine(t *testing.T) {
	line, err := claudeInputLine("first\nsecond\nthird")
	if err != nil {
		t.Fatalf("claudeInputLine: %v", err)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("a literal newline would end the NDJSON record early: %q", line)
	}
	if !strings.Contains(line, `"type":"user"`) || !strings.Contains(line, `"role":"user"`) {
		t.Errorf("unexpected stream-json input shape: %s", line)
	}
}

func TestSteerFeed_SettleWhenIdleClosesOnce(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)

	// The opening prompt is echoed, its turn ends: the agent is idle.
	if ackPrompt(f, time.Now()) {
		t.Fatal("closed before Settle")
	}
	if f.noteTurnEnd() {
		t.Fatal("closed before Settle")
	}
	if !f.settle() {
		t.Fatal("Settle on an idle, fully-acked session should close")
	}
	// Latched: a second settle must not kill twice.
	if f.settle() {
		t.Error("close decision did not latch")
	}
}

func TestSteerFeed_SettleWaitsForTurnToEnd(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	ackPrompt(f, time.Now()) // turn in flight

	if f.settle() {
		t.Fatal("closed while a turn was still running")
	}
	if !f.noteTurnEnd() {
		t.Fatal("the result after Settle should close the session")
	}
}

// TestSteerFeed_SettleWaitsForUnechoedSteer is the race the ack exists
// for: a steer sitting in the mailbox that the agent has not read yet must
// not be thrown away by stopping the feeder.
func TestSteerFeed_SettleWaitsForUnechoedSteer(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	ackPrompt(f, time.Now())
	f.noteTurnEnd()

	if err := f.appendLine(context.Background(), SteerMessage{FollowUpRunID: 7}, `{"type":"user"}`, testSteerKey); err != nil {
		t.Fatalf("appendLine: %v", err)
	}
	if f.settle() {
		t.Fatal("closed with a steer still unread in the mailbox")
	}
	// The ack alone is not enough: the agent has now picked the steer up
	// and is about to work on it, so the run ends only after that turn.
	if ackSteer(f, time.Now()) {
		t.Fatal("closed on the ack, before the steered turn had run")
	}
	if !f.noteTurnEnd() {
		t.Fatal("the steered turn's result should close the settled session")
	}
}

func TestSteerFeed_AppendRefusedAfterClosing(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	ackPrompt(f, time.Now())
	f.noteTurnEnd()
	f.settle()

	err := f.appendLine(context.Background(), SteerMessage{}, `{"type":"user"}`, testSteerKey)
	if err == nil {
		t.Fatal("a steer racing the feeder kill must be refused, not silently dropped into a dead mailbox")
	}
}

// TestSteerFeed_FailedAppendIsNotCountedAsSent keeps a failed sandbox
// write from wedging the run: if it counted as pending, nothing would ever
// ack it and the session could never settle.
func TestSteerFeed_FailedAppendIsNotCountedAsSent(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/cfg", recordingCtxExec(&calls, "no space left on device", 1, nil))
	f.noteInitialPrompt(testPromptKey)
	ackPrompt(f, time.Now())
	f.noteTurnEnd()

	if err := f.appendLine(context.Background(), SteerMessage{}, "x", testSteerKey); err == nil {
		t.Fatal("expected an error on a non-zero exit")
	}
	if !f.settle() {
		t.Fatal("a failed append must not leave the session permanently unsettleable")
	}
	// And nothing may be reported as delivered: the runner marks a
	// follow-up run consumed from RunMetrics.Steers, so a SteerResult for a
	// write that never landed would lose the update entirely.
	if got := f.steerResults(); len(got) != 0 {
		t.Errorf("a failed mailbox write was recorded as a delivery: %+v", got)
	}
}

func TestSteerFeed_AppendPropagatesExecError(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/cfg", recordingCtxExec(&calls, "", 0, errors.New("gateway down")))
	f.noteInitialPrompt(testPromptKey)
	if err := f.appendLine(context.Background(), SteerMessage{}, "x", testSteerKey); err == nil ||
		!strings.Contains(err.Error(), "gateway down") {
		t.Fatalf("expected the gateway error to surface, got %v", err)
	}
}

// TestSteerFeed_EchoAttributionSurvivesAnEarlySteer covers the ordering
// trap: a steer written before the agent had read the opening prompt must
// still be credited with its OWN ack, not the prompt's.
func TestSteerFeed_EchoAttributionSurvivesAnEarlySteer(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)

	promptAck := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	steerAck := time.Date(2026, 9, 3, 12, 5, 0, 0, time.UTC)

	// Steer lands before the agent has consumed anything.
	if err := f.appendLine(context.Background(), SteerMessage{FollowUpRunID: 42}, "x", testSteerKey); err != nil {
		t.Fatalf("appendLine: %v", err)
	}
	ackPrompt(f, promptAck) // the opening prompt
	ackSteer(f, steerAck)   // the steer

	got := f.steerResults()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 recorded steer, got %d: %+v", len(got), got)
	}
	if got[0].FollowUpRunID != 42 {
		t.Errorf("wrong follow-up run recorded: %d", got[0].FollowUpRunID)
	}
	if !got[0].DeliveredAt.Equal(steerAck) {
		t.Errorf("DeliveredAt should be the steer's own ack %v, got %v", steerAck, got[0].DeliveredAt)
	}
	if got[0].Mode != SteerModeLive {
		t.Errorf("expected mode %q, got %q", SteerModeLive, got[0].Mode)
	}
}

func TestSteerFeed_InitCommandTruncates(t *testing.T) {
	f := newSteerFeed("sbx", "/cfg", nil)
	cmd := f.initCommand(`{"type":"user"}`)
	// A stale mailbox must be truncated, not appended to: `tail -n +1 -f`
	// re-reads from the start and would replay the previous iteration.
	if !strings.Contains(cmd, "> '/cfg/"+steerMailboxName+"'") || strings.Contains(cmd, ">> ") {
		t.Errorf("init command must truncate the mailbox: %s", cmd)
	}
}

func TestSteerFeed_StopFeederKillsRecordedPid(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	if err := f.stopFeeder(context.Background()); err != nil {
		t.Fatalf("stopFeeder: %v", err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "kill \"$(cat '/sandbox/claude-config/"+steerFeederPidName+"')\"") {
		t.Errorf("unexpected kill command: %v", calls)
	}
}

func TestSteerFeed_StopFeederReportsNonZeroExit(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/cfg", recordingCtxExec(&calls, "no such process", 1, nil))
	if err := f.stopFeeder(context.Background()); err == nil {
		t.Fatal("expected an error when the kill fails")
	}
}

func TestClaudeSteer_NoRegisteredSession(t *testing.T) {
	rt := ClaudeRuntime{}
	err := rt.Steer(context.Background(), "not-running", SteerMessage{Text: "hi"})
	if !errors.Is(err, ErrNoSteerSession) {
		t.Fatalf("expected ErrNoSteerSession, got %v", err)
	}
	// It must NOT be reported as "this runtime cannot steer": the runner
	// would stop trying instead of retrying a run that started late.
	if errors.Is(err, ErrSteerUnsupported) {
		t.Error("a missing session must not masquerade as an unsupported runtime")
	}
}

func TestClaudeSettle_NoRegisteredSessionIsNoOp(t *testing.T) {
	rt := ClaudeRuntime{}
	if err := rt.Settle(context.Background(), "not-running"); err != nil {
		t.Fatalf("Settle on a finished run must be a no-op, got %v", err)
	}
}

func TestClaudeSteer_AppendsEnvelopeToMailbox(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	registerSteerFeed("sbx-steer", f)
	defer unregisterSteerFeed("sbx-steer")

	rt := ClaudeRuntime{}
	err := rt.Steer(context.Background(), "sbx-steer", SteerMessage{
		FollowUpRunID: 99, Event: "pull_request_target", Actor: "dev", Text: "rebased onto main",
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one mailbox append, got %v", calls)
	}
	cmd := calls[0]
	if !strings.Contains(cmd, ">> '/sandbox/claude-config/"+steerMailboxName+"'") {
		t.Errorf("steer must append to the mailbox, not truncate it: %s", cmd)
	}
	for _, want := range []string{`{"type":"user"`, "rebased onto main", "follow-up run 99"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("append command missing %q: %s", want, cmd)
		}
	}
}

// TestClaudeSteerAggregator_CostIsTakenNotSummed is the regression guard
// for the measured asymmetry: Claude Code's total_cost_usd is already
// cumulative for the session while usage and num_turns are per-turn.
func TestClaudeSteerAggregator_CostIsTakenNotSummed(t *testing.T) {
	var m RunMetrics
	a := &claudeSteerAggregator{}

	// The two results below are the real values from a two-turn probe.
	a.onResult(ResultEvent{NumTurns: 1, TotalCostUSD: 0.0529384, InputTokens: 2, OutputTokens: 5,
		CacheReadInputTokens: 25322, CacheCreationInputTokens: 11955}, &m)
	a.onResult(ResultEvent{NumTurns: 1, TotalCostUSD: 0.0606798, InputTokens: 2, OutputTokens: 5,
		CacheReadInputTokens: 37277, CacheCreationInputTokens: 58}, &m)

	if m.TotalCostUSD != 0.0606798 {
		t.Errorf("cost must be the last cumulative value, got %v (summing would give ~0.1136)", m.TotalCostUSD)
	}
	if m.NumTurns != 2 {
		t.Errorf("num_turns is per-turn and must add up, got %d", m.NumTurns)
	}
	// The same result event's modelUsage block reported exactly these sums.
	if m.InputTokens != 4 || m.OutputTokens != 10 || m.CacheReadInputTokens != 62599 || m.CacheCreationInputTokens != 12013 {
		t.Errorf("token totals do not match the stream's own cumulative figures: in=%d out=%d cacheRead=%d cacheWrite=%d",
			m.InputTokens, m.OutputTokens, m.CacheReadInputTokens, m.CacheCreationInputTokens)
	}
}

// TestClaudeSteerAggregator_KeepsPartialTurnAfterAKill covers a run killed
// during turn 2: the parser's cumulative snapshot leads the completed-turn
// sum and must not be thrown away.
func TestClaudeSteerAggregator_KeepsPartialTurnAfterAKill(t *testing.T) {
	var m RunMetrics
	a := &claudeSteerAggregator{}
	a.onResult(ResultEvent{NumTurns: 1, TotalCostUSD: 0.05, InputTokens: 100, OutputTokens: 20}, &m)
	a.onTokens(TokensEvent{InputTokens: 180, OutputTokens: 35}, &m)

	if m.InputTokens != 180 || m.OutputTokens != 35 {
		t.Errorf("in-flight turn's tokens were dropped: in=%d out=%d", m.InputTokens, m.OutputTokens)
	}
}

// TestClaudeSteerAggregator_ThrottledSnapshotDoesNotUndoAResult is the
// other direction: TokensEvent is emitted only every tokenThreshold
// tokens, so a stale snapshot must not lower a finished turn's totals.
func TestClaudeSteerAggregator_ThrottledSnapshotDoesNotUndoAResult(t *testing.T) {
	var m RunMetrics
	a := &claudeSteerAggregator{}
	a.onResult(ResultEvent{NumTurns: 1, InputTokens: 9000, OutputTokens: 400}, &m)
	a.onTokens(TokensEvent{InputTokens: 5000, OutputTokens: 100}, &m)

	if m.InputTokens != 9000 || m.OutputTokens != 400 {
		t.Errorf("a throttled snapshot lowered completed-turn totals: in=%d out=%d", m.InputTokens, m.OutputTokens)
	}
}

// TestClaudeSettle_StopsTheFeederWhenIdle is the close path: the runner
// settles a run it has watched go idle, and that must stop the feeder so
// the agent sees EOF and exits instead of waiting out params.Timeout.
func TestClaudeSettle_StopsTheFeederWhenIdle(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	ackPrompt(f, time.Now())
	f.noteTurnEnd()
	registerSteerFeed("sbx-settle", f)
	defer unregisterSteerFeed("sbx-settle")

	rt := ClaudeRuntime{}
	if err := rt.Settle(context.Background(), "sbx-settle"); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "kill \"$(cat ") {
		t.Fatalf("Settle did not stop the feeder: %v", calls)
	}
}

// TestClaudeSettle_LeavesTheFeederRunningMidTurn is the other half: an
// agent still working keeps its input channel until the turn ends, so a
// steer that is already in the mailbox is not stranded.
func TestClaudeSettle_LeavesTheFeederRunningMidTurn(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	ackPrompt(f, time.Now()) // mid-turn
	registerSteerFeed("sbx-midturn", f)
	defer unregisterSteerFeed("sbx-midturn")

	rt := ClaudeRuntime{}
	if err := rt.Settle(context.Background(), "sbx-midturn"); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("Settle killed the feeder mid-turn: %v", calls)
	}
}

// TestSteerCloseFeedIf covers the shared close helper: the decision belongs
// to the settle state machine, and a failed kill must degrade to a warning
// rather than failing the run — the agent just keeps waiting on stdin and
// the run ends on params.Timeout instead.
func TestSteerCloseFeedIf(t *testing.T) {
	t.Run("does nothing when the machine says not to close", func(t *testing.T) {
		var calls []string
		f := newTestFeed(&calls)
		before := len(calls)
		steerCloseFeedIf(context.Background(), false, f, ui.New(io.Discard))
		if len(calls) != before {
			t.Errorf("stopped the feeder without being told to: %v", calls)
		}
	})

	t.Run("stops the feeder when told to", func(t *testing.T) {
		var calls []string
		f := newTestFeed(&calls)
		steerCloseFeedIf(context.Background(), true, f, ui.New(io.Discard))
		if len(calls) != 1 || !strings.Contains(calls[0], "kill \"$(cat ") {
			t.Errorf("expected one kill command, got %v", calls)
		}
	})

	t.Run("warns instead of failing when the kill fails", func(t *testing.T) {
		var calls []string
		f := newSteerFeed("sbx", "/cfg", recordingCtxExec(&calls, "no such process", 1, nil))
		var out bytes.Buffer
		steerCloseFeedIf(context.Background(), true, f, ui.New(&out))
		if out.Len() == 0 {
			t.Error("a failed kill should be reported to the operator")
		}
	})
}

// TestSteerFeed_InjectedEchoDoesNotReceiptARealSteer is the first half of
// the mailbox-injection defect. The mailbox is agent-writable, so the agent
// can append a line of its own; that line is echoed back exactly as the
// runner's are. When echoes were merely counted, an injected line arriving
// before a real steer's echo shifted the positional attribution and stamped
// the real steer's SteerResult on the wrong echo — receipting a steer the
// agent had not consumed, which lets the queued run skip work nobody did.
func TestSteerFeed_InjectedEchoDoesNotReceiptARealSteer(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	ackPrompt(f, time.Now())
	f.noteTurnEnd()

	if err := f.appendLine(context.Background(), SteerMessage{FollowUpRunID: 99}, "x", testSteerKey); err != nil {
		t.Fatalf("appendLine: %v", err)
	}

	// The agent appends its own line and it is echoed first.
	if f.noteEcho(time.Now(), "", "a line the agent wrote itself") {
		t.Fatal("an injected echo must not close the session")
	}
	if got := f.steerResults(); len(got) != 0 {
		t.Fatalf("an injected echo receipted a steer the agent never consumed: %+v", got)
	}

	// The real steer's own echo is what receipts it.
	ackSteer(f, time.Now())
	got := f.steerResults()
	if len(got) != 1 || got[0].FollowUpRunID != 99 {
		t.Fatalf("the real steer was not receipted by its own echo: %+v", got)
	}
}

// TestSteerFeed_InjectedEchoesStillSettle is the second half. The settle
// condition used to be an equality between lines sent and echoes seen, so a
// single injected line made it unsatisfiable for the rest of the run: the
// feeder was never stopped and the run burned its whole timeout. A stray
// echo must leave the condition exactly where it was.
func TestSteerFeed_InjectedEchoesStillSettle(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)

	for _, injected := range []string{"first stray", "second stray", "third stray"} {
		if f.noteEcho(time.Now(), "", injected) {
			t.Fatalf("an injected echo closed the session: %q", injected)
		}
	}
	if got := f.steerResults(); len(got) != 0 {
		t.Fatalf("injected echoes produced steer results: %+v", got)
	}

	// The run proceeds normally: the prompt is consumed, its turn ends,
	// and Settle closes despite the strays.
	ackPrompt(f, time.Now())
	f.noteTurnEnd()
	if !f.settle() {
		t.Fatal("injected echoes wedged the settle condition; the run would burn its timeout")
	}
}

// TestSteerFeed_CopiedKeyCannotOutrunTheOriginal covers the agent copying a
// key it can read out of the mailbox. It cannot get ahead of the message it
// copies — the copy must be appended after it, the feeder delivers in
// order, and each outstanding message is acked at most once — so the
// original claims its own echo and the copy matches nothing.
func TestSteerFeed_CopiedKeyCannotOutrunTheOriginal(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	ackPrompt(f, time.Now())
	f.noteTurnEnd()

	if err := f.appendLine(context.Background(), SteerMessage{FollowUpRunID: 7}, "x", testSteerKey); err != nil {
		t.Fatalf("appendLine: %v", err)
	}
	ackSteer(f, time.Now())      // the real line, delivered first
	if ackSteer(f, time.Now()) { // the agent's copy of the same key
		t.Fatal("a replayed key closed the session a second time")
	}
	if got := f.steerResults(); len(got) != 1 {
		t.Fatalf("a copied key produced a duplicate receipt: %+v", got)
	}
}

// TestClaudeSteerAggregator_ReasoningIsTakenNotSummed is the regression
// guard whose absence let a double-count through. ReasoningTokens looks
// like its neighbours on ResultEvent but is not read from `re.Usage`:
// parseClaudeStream accumulates thinking tokens into totalReasoning, never
// resets it, and emits that running total on every result. Summing it
// therefore counts every earlier turn again, and the error compounds with
// turn count.
func TestClaudeSteerAggregator_ReasoningIsTakenNotSummed(t *testing.T) {
	var m RunMetrics
	a := &claudeSteerAggregator{}

	// Turn 1 thought 100 tokens; turn 2 thought 50 more, and the parser
	// reports the running total of 150.
	a.onResult(ResultEvent{NumTurns: 1, InputTokens: 10, OutputTokens: 5, ReasoningTokens: 100}, &m)
	if m.ReasoningTokens != 100 {
		t.Fatalf("after one turn: got %d, want 100", m.ReasoningTokens)
	}
	a.onResult(ResultEvent{NumTurns: 1, InputTokens: 10, OutputTokens: 5, ReasoningTokens: 150}, &m)

	if m.ReasoningTokens != 150 {
		t.Errorf("reasoning must be the parser's running total, got %d (summing gives 250)", m.ReasoningTokens)
	}
	// The per-turn fields must still add up, so the fix is scoped to the
	// one field that is accumulated upstream.
	if m.InputTokens != 20 || m.OutputTokens != 10 || m.NumTurns != 2 {
		t.Errorf("per-turn fields stopped summing: in=%d out=%d turns=%d",
			m.InputTokens, m.OutputTokens, m.NumTurns)
	}
}

// TestClaudeSteerAggregator_PerMessageReasoningDoesNotRaiseTheTotal covers
// the other half: TokensEvent carries one message's thinking tokens, not a
// running total, so it must not be able to move the run-wide figure.
func TestClaudeSteerAggregator_PerMessageReasoningDoesNotRaiseTheTotal(t *testing.T) {
	var m RunMetrics
	a := &claudeSteerAggregator{}
	a.onResult(ResultEvent{NumTurns: 1, ReasoningTokens: 150}, &m)

	a.onTokens(TokensEvent{InputTokens: 9000, ReasoningTokens: 400}, &m)

	if m.ReasoningTokens != 150 {
		t.Errorf("a per-message reasoning value overwrote the run total: got %d, want 150", m.ReasoningTokens)
	}
}

// TestClaudeSteer_AfterSettleIsRefused: Steer used to report success when
// the session had already settled, so a caller could not tell a delivered
// steer from one dropped into a mailbox nothing would read again. The
// update is correctly left to the queued run — but the caller has to know
// that is what happened.
func TestClaudeSteer_AfterSettleIsRefused(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	registerSteerFeed("sbx-after-settle", f)
	defer unregisterSteerFeed("sbx-after-settle")

	f.mu.Lock()
	f.settled = true
	f.mu.Unlock()

	before := len(calls)
	err := ClaudeRuntime{}.Steer(context.Background(), "sbx-after-settle", SteerMessage{FollowUpRunID: 5, Text: "late"})
	if !errors.Is(err, ErrSteerAfterSettle) {
		t.Fatalf("expected ErrSteerAfterSettle, got %v", err)
	}
	if len(calls) != before {
		t.Errorf("a refused steer must not write to the mailbox, got %d new writes", len(calls)-before)
	}
}

// TestSteerFeeder_ResolvesTailThroughAPinnedPath is a security guard, so it
// asserts the ORDER of the launch string, not just its contents: the PATH
// must be captured before the agent-writable .env is sourced, and the
// feeder must use that captured value. The feeder owns the channel steers
// are delivered on — a `tail` planted by a previous iteration could drop
// real steers or feed the session a forged envelope.
func TestSteerFeeder_ResolvesTailThroughAPinnedPath(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/workspace/repo", Steerable: true})

	pin := strings.Index(cmd, "readonly "+steerPathVar+`="$PATH"`)
	env := strings.Index(cmd, ". /sandbox/workspace/.env")
	feeder := strings.Index(cmd, "tail -n +1 -f")
	if pin < 0 || env < 0 || feeder < 0 {
		t.Fatalf("launch is missing the pin, the .env sourcing or the feeder:\n%s", cmd)
	}
	if !(pin < env) {
		t.Errorf("PATH must be captured BEFORE .env is sourced, or .env can poison it:\n%s", cmd)
	}
	if !(env < feeder) {
		t.Fatalf("expected the feeder to run after .env; the ordering assumption changed:\n%s", cmd)
	}
	if !strings.Contains(cmd, `PATH="$`+steerPathVar+`" tail -n +1 -f`) {
		t.Errorf("the feeder must resolve tail through the pinned PATH:\n%s", cmd)
	}
	// Each of these is its own injection primitive, and a shell function
	// beats both a PATH lookup and a regular builtin — so every name the
	// fragment runs must be unset, not just the one that reads the mailbox.
	unset := strings.Index(cmd, "unset -f tail echo wait")
	if unset < 0 {
		t.Errorf("tail, echo and wait must all be unset: a planted wait writes straight into the agent's stdin, and a planted echo corrupts the feeder pid file:\n%s", cmd)
	}
	if unset > feeder {
		t.Errorf("the unset must come before the feeder starts:\n%s", cmd)
	}
	if bare := strings.Index(cmd, "; wait ; }"); bare >= 0 && unset < 0 {
		t.Errorf("the fragment ends in a bare wait with nothing unsetting it:\n%s", cmd)
	}
}

// TestSteerFeeder_NonSteerableRunIsUnchanged keeps the hardening scoped:
// a run with no feeder gets no pin and no fragment.
func TestSteerFeeder_NonSteerableRunIsUnchanged(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/workspace/repo"})
	if strings.Contains(cmd, steerPathVar) || strings.Contains(cmd, "tail -n +1 -f") {
		t.Errorf("a non-steerable run must not carry the feeder or its pin:\n%s", cmd)
	}
}

// TestSteerFeeder_PinsTheAgentBinaryBeforeEnv guards the other half of the
// channel. The PATH pin protects the feeder carrying steers IN; this pins
// the agent carrying the acknowledgement back OUT. --replay-user-messages
// echoes are the only proof a steer reached the real agent, so a `claude`
// shadowed after .env could swallow the steer and emit a forged echo of the
// runner's own text — a delivery the runner would record as real.
func TestSteerFeeder_PinsTheAgentBinaryBeforeEnv(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/workspace/repo", Steerable: true})

	pin := strings.Index(cmd, "readonly "+claudeBinaryVar+`="$(command -v claude)"`)
	env := strings.Index(cmd, ". /sandbox/workspace/.env")
	if pin < 0 {
		t.Fatalf("the agent binary must be pinned, as pi and codex pin theirs:\n%s", cmd)
	}
	if !(pin < env) {
		t.Errorf("the binary must be resolved BEFORE .env is sourced, or .env can shadow it:\n%s", cmd)
	}
	if !strings.Contains(cmd, `| "$`+claudeBinaryVar+`"`) {
		t.Errorf("the launch must invoke the pinned binary, not a bare name:\n%s", cmd)
	}
	if strings.Contains(cmd, "} | claude") {
		t.Errorf("a bare `claude` after .env is exactly what the pin exists to prevent:\n%s", cmd)
	}
}
