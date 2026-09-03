package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingCtxExec returns a sandboxExecCtxFunc that appends every command
// it is given to calls and answers with the supplied result.
func recordingCtxExec(calls *[]string, stderr string, exitCode int, err error) sandboxExecCtxFunc {
	return func(_ context.Context, _, cmd string, _ time.Duration) (string, string, int, error) {
		*calls = append(*calls, cmd)
		return "", stderr, exitCode, err
	}
}

func newTestFeed(calls *[]string) *steerFeed {
	f := newSteerFeed("sbx", "/sandbox/claude-config", recordingCtxExec(calls, "", 0, nil))
	f.noteInitialPrompt()
	return f
}

func TestBuildRunCommand_Steerable(t *testing.T) {
	cmd := buildRunCommand(RunParams{RepoDir: "/repo", AgentBaseName: "review", Steerable: true})

	for _, want := range []string{
		"{ tail -n +1 -f '/sandbox/claude-config/steer-inbox.ndjson' &",
		"echo $! > '/sandbox/claude-config/steer-feeder.pid'",
		"wait ; } | claude",
		"--input-format stream-json",
		"--replay-user-messages",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("steerable command missing %q:\n%s", want, cmd)
		}
	}
	// The prompt must reach the agent through the mailbox, never argv:
	// argv is world-readable in the sandbox and the prompt is
	// attacker-influenced on a retry iteration.
	if strings.Contains(cmd, DefaultAgentPrompt) {
		t.Errorf("steerable command still passes the prompt on argv:\n%s", cmd)
	}
}

// TestBuildRunCommand_SteerableKeepsFeedbackPromptOffArgv covers the
// validation loop's retry prompt specifically: it carries the previous
// iteration's failure text, which is the most attacker-influenced string
// the runner ever hands a runtime.
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

func TestRenderSteerEnvelope_FullProvenance(t *testing.T) {
	got := renderSteerEnvelope(SteerMessage{
		FollowUpRunID: 33740015232,
		Event:         "issue_comment",
		Actor:         "octocat",
		CreatedAt:     time.Date(2026, 9, 3, 10, 48, 29, 0, time.UTC),
		HeadSHA:       "abc1234",
		Text:          "Also cover the error path.",
	})
	for _, want := range []string{
		"follow-up run 33740015232",
		"issue_comment by octocat",
		"2026-09-03T10:48:29Z",
		"head is now abc1234",
		"authorized to direct this run",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("envelope missing %q:\n%s", want, got)
		}
	}
	// The runner already sanitized Text; the envelope must not reshape it.
	if !strings.HasSuffix(got, "Also cover the error path.") {
		t.Errorf("steer text was not emitted verbatim at the end:\n%s", got)
	}
}

// TestRenderSteerEnvelope_LocalRun covers `fullsend run`, where no
// follow-up run, actor or head exists: the header must degrade to
// something readable rather than printing zero values or an empty Source.
func TestRenderSteerEnvelope_LocalRun(t *testing.T) {
	got := renderSteerEnvelope(SteerMessage{Text: "reviewer asked for the null case"})
	if strings.Contains(got, "run 0") || strings.Contains(got, "0001-01-01") {
		t.Errorf("envelope printed empty provenance as zero values:\n%s", got)
	}
	if strings.Contains(got, "Source: \n") {
		t.Errorf("envelope left an empty Source line:\n%s", got)
	}
	if !strings.HasSuffix(got, "reviewer asked for the null case") {
		t.Errorf("steer text missing:\n%s", got)
	}
}

// TestRenderSteerEnvelope_ProhibitionStaysNarrow is a regression guard on
// wording that was measured, not guessed: an envelope telling the agent
// not to let the update change its "scope" was quoted back by Claude Code
// 2.1.259 as its reason for refusing the steer. Updating scope is the
// whole point of a steer, so only tools, permissions and security
// instructions may be placed off limits.
func TestRenderSteerEnvelope_ProhibitionStaysNarrow(t *testing.T) {
	got := renderSteerEnvelope(SteerMessage{Actor: "octocat", Event: "issue_comment", Text: "x"})
	if strings.Contains(got, "scope") {
		t.Errorf("envelope forbids changing scope, which is what a steer is for:\n%s", got)
	}
	for _, want := range []string{"no new tools or permissions", "relaxes no security instruction"} {
		if !strings.Contains(got, want) {
			t.Errorf("envelope lost the prohibition that must stay (%q):\n%s", want, got)
		}
	}
}

// TestRenderSteerEnvelope_DoesNotContradictItsOwnProvenance guards the
// other measured failure: an envelope claiming the update is not from the
// comment stream, above a Source line naming an issue_comment, was
// reported by the agent as "a hallmark of a prompt-injection attempt".
func TestRenderSteerEnvelope_DoesNotContradictItsOwnProvenance(t *testing.T) {
	got := renderSteerEnvelope(SteerMessage{Actor: "octocat", Event: "issue_comment", Text: "x"})
	if strings.Contains(got, "not a message from the work item") ||
		strings.Contains(got, "not from the work item") {
		t.Errorf("envelope denies a provenance its own Source line states:\n%s", got)
	}
	if !strings.Contains(got, "The content came from the work item") {
		t.Errorf("envelope should state the content's real origin:\n%s", got)
	}
}

func TestSteerFeed_SettleWhenIdleClosesOnce(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)

	// The opening prompt is echoed, its turn ends: the agent is idle.
	if f.noteEcho(time.Now()) {
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
	f.noteEcho(time.Now()) // turn in flight

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
	f.noteEcho(time.Now())
	f.noteTurnEnd()

	if err := f.appendLine(context.Background(), SteerMessage{FollowUpRunID: 7}, `{"type":"user"}`); err != nil {
		t.Fatalf("appendLine: %v", err)
	}
	if f.settle() {
		t.Fatal("closed with a steer still unread in the mailbox")
	}
	// The ack alone is not enough: the agent has now picked the steer up
	// and is about to work on it, so the run ends only after that turn.
	if f.noteEcho(time.Now()) {
		t.Fatal("closed on the ack, before the steered turn had run")
	}
	if !f.noteTurnEnd() {
		t.Fatal("the steered turn's result should close the settled session")
	}
}

func TestSteerFeed_AppendRefusedAfterClosing(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	f.noteEcho(time.Now())
	f.noteTurnEnd()
	f.settle()

	err := f.appendLine(context.Background(), SteerMessage{}, `{"type":"user"}`)
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
	f.noteInitialPrompt()
	f.noteEcho(time.Now())
	f.noteTurnEnd()

	if err := f.appendLine(context.Background(), SteerMessage{}, "x"); err == nil {
		t.Fatal("expected an error on a non-zero exit")
	}
	if !f.settle() {
		t.Fatal("a failed append must not leave the session permanently unsettleable")
	}
}

func TestSteerFeed_AppendPropagatesExecError(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/cfg", recordingCtxExec(&calls, "", 0, errors.New("gateway down")))
	f.noteInitialPrompt()
	if err := f.appendLine(context.Background(), SteerMessage{}, "x"); err == nil ||
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
	if err := f.appendLine(context.Background(), SteerMessage{FollowUpRunID: 42}, "x"); err != nil {
		t.Fatalf("appendLine: %v", err)
	}
	f.noteEcho(promptAck) // the opening prompt
	f.noteEcho(steerAck)  // the steer

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
	if got[0].Mode != steerModeLive {
		t.Errorf("expected mode %q, got %q", steerModeLive, got[0].Mode)
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
	if !errors.Is(err, errNoSteerSession) {
		t.Fatalf("expected errNoSteerSession, got %v", err)
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

func TestSteerEchoTime(t *testing.T) {
	ts := "2026-09-03T10:48:29Z"
	if got := steerEchoTime(ts); !got.Equal(time.Date(2026, 9, 3, 10, 48, 29, 0, time.UTC)) {
		t.Errorf("unexpected parse of %q: %v", ts, got)
	}
	// An unusable timestamp must not become the zero time: the runner
	// compares DeliveredAt against its own start to decide what an update
	// covered.
	for _, raw := range []string{"", "not-a-time"} {
		if got := steerEchoTime(raw); got.IsZero() {
			t.Errorf("steerEchoTime(%q) returned the zero time", raw)
		}
	}
}

func TestSteerFeed_SeedTruncatesAndCountsThePrompt(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/cfg", recordingCtxExec(&calls, "", 0, nil))
	if err := f.seed(context.Background(), `{"type":"user"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "> '/cfg/"+steerMailboxName+"'") {
		t.Fatalf("unexpected seed command: %v", calls)
	}
	// The opening prompt counts as pending: the session must not settle
	// before the agent has actually consumed it.
	if f.settle() {
		t.Error("settled before the opening prompt was ever read")
	}
}

func TestSteerFeed_SeedFailureIsReported(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/cfg", recordingCtxExec(&calls, "permission denied", 1, nil))
	err := f.seed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "seeding the steer mailbox") {
		t.Fatalf("expected a seed failure, got %v", err)
	}
}

// TestClaudeSettle_StopsTheFeederWhenIdle is the close path: the runner
// settles a run it has watched go idle, and that must stop the feeder so
// the agent sees EOF and exits instead of waiting out params.Timeout.
func TestClaudeSettle_StopsTheFeederWhenIdle(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	f.noteEcho(time.Now())
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
	f.noteEcho(time.Now()) // mid-turn
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
