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

func TestBuildPiTurnCommand_Steerable(t *testing.T) {
	params := RunParams{RepoDir: "/repo", Steerable: true}
	cmd := buildPiTurnCommand(params, &piManifest{}, nil, "", "01a06800-0000-7000-8000-0000000000ab")

	for _, want := range []string{
		"{ unset -f tail echo wait; PATH=\"$FULLSEND_STEER_PATH\" tail -n +1 -f '/sandbox/pi-config/steer-inbox.ndjson' &",
		"echo $! > '/sandbox/pi-config/steer-feeder.pid'",
		"--mode rpc",
		"--session-id '01a06800-0000-7000-8000-0000000000ab'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("steerable pi command missing %q:\n%s", want, cmd)
		}
	}
	// rpc takes prompts as commands on stdin, so the single-prompt mode
	// and its stdin guard must both be gone, and no prompt on argv.
	for _, unwanted := range []string{"--print", "--mode json", "</dev/null", DefaultAgentPrompt} {
		if strings.Contains(cmd, unwanted) {
			t.Errorf("steerable pi command still carries %q:\n%s", unwanted, cmd)
		}
	}
}

// TestBuildPiTurnCommand_SteerableKeepsGuards makes sure the rpc branch did
// not drop the launch hardening: those guards are what stop an
// agent-written .env from moving pi's config dir or shadowing its binary.
func TestBuildPiTurnCommand_SteerableKeepsGuards(t *testing.T) {
	params := RunParams{RepoDir: "/repo", Steerable: true, HooksSettingsPath: "/x"}
	m := &piManifest{Hooks: &piHooksManifest{Groups: []piHookGroup{}}}
	cmd := buildPiTurnCommand(params, m, nil, "", "sid")

	for _, want := range []string{
		piBinaryVar,          // binary pinned before .env is sourced
		"--session-dir ",     // runner-owned session store
		"--no-approve",       // no interactive approval
		"--no-extensions",    // only the extensions the runner passes
		piManifestEnv,        // manifest location re-pinned after .env
		"/sandbox/pi-config", // config dir re-pinned after .env
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("steerable pi command lost guard %q:\n%s", want, cmd)
		}
	}
}

// TestBuildPiTurnCommand_SteerableClearsDynamicLoaderEnv pins the order on
// the steerable launch: .env, then the LD_* unset, then the feeder and pi,
// so a .env exporting LD_PRELOAD cannot load a shim that forges pi's rpc
// ack. The ordinary launch is unchanged (tracked on #7580).
func TestBuildPiTurnCommand_SteerableClearsDynamicLoaderEnv(t *testing.T) {
	m := &piManifest{}
	cmd := buildPiTurnCommand(RunParams{RepoDir: "/repo", Steerable: true}, m, nil, "", "sid")

	env := strings.Index(cmd, "&& . '/sandbox/workspace/.env'")
	unset := strings.Index(cmd, piSteerLoaderEnvUnset)
	feeder := strings.Index(cmd, "{ unset -f tail")
	if env < 0 || unset < 0 || feeder < 0 || !(env < unset && unset < feeder) {
		t.Fatalf("want .env (%d) < LD_* unset (%d) < feeder (%d):\n%s", env, unset, feeder, cmd)
	}
	for _, name := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT"} {
		if !strings.Contains(piSteerLoaderEnvUnset, name) {
			t.Errorf("steer loader unset misses %s", name)
		}
	}

	if plain := buildPiTurnCommand(RunParams{RepoDir: "/repo"}, m, nil, "", ""); strings.Contains(plain, piSteerLoaderEnvUnset) {
		t.Errorf("non-steerable launch gained the LD_* unset:\n%s", plain)
	}
}

func TestBuildPiTurnCommand_NotSteerableUnchanged(t *testing.T) {
	params := RunParams{RepoDir: "/repo"}
	base := buildPiRunCommand(params, &piManifest{}, nil, "")
	// An empty session id means "not steerable" even when the flag is set,
	// which is the state a failed seed would leave behind.
	same := buildPiTurnCommand(RunParams{RepoDir: "/repo", Steerable: true}, &piManifest{}, nil, "", "")
	if base != same {
		t.Errorf("a steerable run with no session id must fall back to today's command:\n%s\n---\n%s", base, same)
	}
	for _, unwanted := range []string{"--mode rpc", "tail -n +1 -f", "--session-id"} {
		if strings.Contains(base, unwanted) {
			t.Errorf("non-steerable pi command gained %q:\n%s", unwanted, base)
		}
	}
	if !strings.Contains(base, "</dev/null") {
		t.Errorf("non-steerable pi command lost its stdin guard:\n%s", base)
	}
}

func TestPiInputLine_SteerCarriesBehavior(t *testing.T) {
	line, err := piInputLine("id-1", "first\nsecond", piSteerBehavior)
	if err != nil {
		t.Fatalf("piInputLine: %v", err)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("a literal newline would end the NDJSON command early: %q", line)
	}
	for _, want := range []string{`"type":"prompt"`, `"id":"id-1"`, `"streamingBehavior":"steer"`} {
		if !strings.Contains(line, want) {
			t.Errorf("rpc prompt missing %q: %s", want, line)
		}
	}
}

// TestPiInputLine_OpeningPromptHasNoBehavior: there is no turn to steer
// into for the first message, and streamingBehavior is omitempty so the
// field is absent rather than empty.
func TestPiInputLine_OpeningPromptHasNoBehavior(t *testing.T) {
	line, err := piInputLine("id-0", "start", "")
	if err != nil {
		t.Fatalf("piInputLine: %v", err)
	}
	if strings.Contains(line, "streamingBehavior") {
		t.Errorf("opening prompt should carry no streamingBehavior: %s", line)
	}
}

func TestPiSteer_NoRegisteredSession(t *testing.T) {
	rt := PiRuntime{}
	if err := rt.Steer(context.Background(), "nope", SteerMessage{}); !errors.Is(err, ErrNoSteerSession) {
		t.Fatalf("expected ErrNoSteerSession, got %v", err)
	}
	if err := rt.Settle(context.Background(), "nope"); err != nil {
		t.Fatalf("Settle on a finished run must be a no-op, got %v", err)
	}
}

func TestPiSteer_AppendsSteerCommand(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/sandbox/pi-config", recordingCtxExec(&calls, "", 0, nil))
	f.noteInitialPrompt(testPromptKey)
	registerSteerFeed("sbx-pi", f)
	defer unregisterSteerFeed("sbx-pi")

	rt := PiRuntime{}
	err := rt.Steer(context.Background(), "sbx-pi", SteerMessage{FollowUpRunID: 4, Actor: "octocat", Text: "cover the error path"})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one mailbox append, got %v", calls)
	}
	for _, want := range []string{`"streamingBehavior":"steer"`, "cover the error path", ">> '/sandbox/pi-config/steer-inbox.ndjson'"} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("append command missing %q: %s", want, calls[0])
		}
	}
}

func TestNewPiSessionID_IsUniqueAndNonEmpty(t *testing.T) {
	a, b := newPiSessionID(), newPiSessionID()
	if a == "" || a == b {
		t.Errorf("session ids must be unique and non-empty: %q, %q", a, b)
	}
}

// TestParsePiStreamMode_PerPromptEmitsEachTurn is the blocker this mode
// exists for. In --mode json pi runs one prompt per process, so the parser
// holds its single result until EOF; a steered rpc run ends only when the
// feeder is killed, so holding would emit nothing until the run was over
// and the settle rule (close on a turn ending) would never fire.
func TestParsePiStreamMode_PerPromptEmitsEachTurn(t *testing.T) {
	lines := []string{
		`{"id":"p1","type":"response","command":"prompt","success":true}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ONE"}],"stopReason":"stop","usage":{"input":10,"output":2,"cost":{"total":0.01}}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
		`{"id":"p2","type":"response","command":"prompt","success":true}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"TWO"}],"stopReason":"stop","usage":{"input":25,"output":5,"cost":{"total":0.03}}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
	}
	var results []ResultEvent
	var acks int
	_, err := parsePiStreamWith(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		switch e := evt.(type) {
		case ResultEvent:
			results = append(results, e)
		case UserReplayEvent:
			acks++
		}
	}, true)
	if err != nil {
		t.Fatalf("parsePiStreamWith: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected one result per prompt, got %d", len(results))
	}
	if acks != 2 {
		t.Errorf("expected one delivery ack per prompt, got %d", acks)
	}
	// pi's counters accumulate across the whole stream and are never
	// reset, so each per-prompt result already carries run-wide totals —
	// which is why PiRuntime.Run assigns them instead of folding.
	if results[1].InputTokens != 35 || results[1].OutputTokens != 7 {
		t.Errorf("per-prompt results should carry cumulative totals: in=%d out=%d",
			results[1].InputTokens, results[1].OutputTokens)
	}
}

// TestParsePiStreamMode_PerPromptNoDuplicateAtEOF: the feeder kill closes
// the stream after the last agent_settled, and that EOF must not re-emit

// TestParsePiStreamMode_PerPromptErrorDoesNotStick covers a steered run
// whose first prompt fails and whose steer then succeeds: each per-prompt
// result is that prompt's own verdict, so the second must not inherit the
// first's IsError or its error text, and PiRuntime.Run (which reads the
// last result) must not fail a session that recovered.
func TestParsePiStreamMode_PerPromptErrorDoesNotStick(t *testing.T) {
	lines := []string{
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"FIRST PROMPT FAILED","usage":{"input":10,"output":0,"cost":{"total":0.01}}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"TWO"}],"stopReason":"stop","usage":{"input":25,"output":5,"cost":{"total":0.03}}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
	}
	var results []ResultEvent
	if _, err := parsePiStreamWith(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}, true); err != nil {
		t.Fatalf("parsePiStreamWith: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected one result per prompt, got %d", len(results))
	}
	if !results[0].IsError || results[0].ErrorMessage != "FIRST PROMPT FAILED" {
		t.Errorf("first prompt: want its error, got %+v", results[0])
	}
	if results[1].IsError || results[1].ErrorMessage != "" || results[1].Subtype != "stop" {
		t.Errorf("second prompt inherited the first's failure: %+v", results[1])
	}
}

// TestParsePiStreamMode_PerPromptCountsTurnsPerPrompt keeps the
// no-assistant-message check per prompt: a later prompt that ends without
// one is an error even though the stream as a whole has seen turns.
func TestParsePiStreamMode_PerPromptCountsTurnsPerPrompt(t *testing.T) {
	lines := []string{
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ONE"}],"stopReason":"stop","usage":{"input":10,"output":2,"cost":{"total":0.01}}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
		`{"type":"agent_start"}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
	}
	var results []ResultEvent
	if _, err := parsePiStreamWith(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}, true); err != nil {
		t.Fatalf("parsePiStreamWith: %v", err)
	}
	if len(results) != 2 || results[0].IsError || !results[1].IsError {
		t.Fatalf("want [ok, error] for a prompt with no assistant message, got %+v", results)
	}
}

// TestParsePiStreamMode_PerPromptEOFMidPromptIsIncomplete covers a steered
// session that dies after the next prompt started: an earlier prompt having
// reported must not make that EOF read as a finished run, or Run would take
// the earlier success as the run's verdict.
func TestParsePiStreamMode_PerPromptEOFMidPromptIsIncomplete(t *testing.T) {
	lines := []string{
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ONE"}],"stopReason":"stop","usage":{"input":10,"output":2,"cost":{"total":0.01}}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
		`{"type":"agent_start"}`,
	}
	var results []ResultEvent
	if _, err := parsePiStreamWith(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}, true); err != nil {
		t.Fatalf("parsePiStreamWith: %v", err)
	}
	if len(results) != 2 || results[0].IsError {
		t.Fatalf("want the first prompt's success then an incomplete result, got %+v", results)
	}
	if !results[1].IsError || results[1].Subtype != "incomplete" {
		t.Errorf("EOF mid-prompt must report incomplete, got %+v", results[1])
	}
}

// the result already reported.
func TestParsePiStreamMode_PerPromptNoDuplicateAtEOF(t *testing.T) {
	lines := []string{
		`{"id":"p1","type":"response","command":"prompt","success":true}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ONE"}],"stopReason":"stop","usage":{"input":10,"output":2}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
	}
	var results []ResultEvent
	_, err := parsePiStreamWith(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}, true)
	if err != nil {
		t.Fatalf("parsePiStreamWith: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("EOF after the last settled turn duplicated the result: got %d", len(results))
	}
	if results[0].IsError {
		t.Error("a clean end-of-steered-run was reported as an error")
	}
}

// TestParsePiStreamMode_FailedAckIsNotADelivery: a rejected prompt never
// reached the agent, so counting it would let the run settle with a steer
// still unread. It IS surfaced, marked Rejected, because the line is
// already off the mailbox and will never be echoed — the runner has to
// retire it or it waits for an echo that cannot come. A non-prompt command
// is not reported at all.
func TestParsePiStreamMode_FailedAckIsNotADelivery(t *testing.T) {
	input := `{"id":"p1","type":"response","command":"prompt","success":false}` + "\n" +
		`{"id":"p2","type":"response","command":"interrupt","success":true}` + "\n"
	var events []UserReplayEvent
	_, err := parsePiStreamWith(strings.NewReader(input), func(evt AgentEvent) {
		if e, ok := evt.(UserReplayEvent); ok {
			events = append(events, e)
		}
	}, true)
	if err != nil {
		t.Fatalf("parsePiStreamWith: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected only the refused prompt to be reported, got %d: %+v", len(events), events)
	}
	if events[0].ID != "p1" || !events[0].Rejected {
		t.Errorf("the refused prompt must be reported as Rejected, not as a delivery: %+v", events[0])
	}
}

// TestParsePiStreamMode_DefaultModeStillEmitsOnce pins the ordinary
// --mode json path: one result, at EOF, exactly as before.
func TestParsePiStreamMode_DefaultModeStillEmitsOnce(t *testing.T) {
	lines := []string{
		`{"type":"session","version":3,"id":"ses_x"}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ONE"}],"stopReason":"stop","usage":{"input":10,"output":2}}}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
	}
	var results []ResultEvent
	sid, err := parsePiStreamWith(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}, false)
	if err != nil {
		t.Fatalf("parsePiStreamWith: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly one result in the default mode, got %d", len(results))
	}
	if sid != "ses_x" {
		t.Errorf("session id lost: %q", sid)
	}
}

// TestPiSettle_StopsTheFeederWhenSettled mirrors the Claude case: pi's
// print-mode session exits only when its stdin closes, so a settled, idle
// run must stop the feeder rather than wait out params.Timeout.
func TestPiSettle_StopsTheFeederWhenSettled(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/sandbox/pi-config", recordingCtxExec(&calls, "", 0, nil))
	f.noteInitialPrompt(testPromptKey)
	ackPrompt(f, steerEchoTime(""))
	f.noteTurnEnd()
	registerSteerFeed("sbx-pi-settle", f)
	defer unregisterSteerFeed("sbx-pi-settle")

	rt := PiRuntime{}
	if err := rt.Settle(context.Background(), "sbx-pi-settle"); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "kill \"$(cat ") {
		t.Fatalf("Settle did not stop the feeder: %v", calls)
	}
}

// TestPiSteer_DeliveryIsRecordedOnlyOnTheAck ties pi's runtime to the
// shared delivery accounting: the SteerResult must appear only once pi has
// acked the mailbox line (rpc `response` with command=prompt and
// success=true, surfaced as UserReplayEvent), never at append time. The
// runner marks a follow-up run consumed from RunMetrics.Steers, so a
// result recorded before the ack would let the queued run skip an update
// pi had not taken yet.
func TestPiSteer_DeliveryIsRecordedOnlyOnTheAck(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/sandbox/pi-config", recordingCtxExec(&calls, "", 0, nil))
	f.noteInitialPrompt(testPromptKey)
	registerSteerFeed("sbx-pi-ack", f)
	defer unregisterSteerFeed("sbx-pi-ack")

	rt := PiRuntime{}
	if err := rt.Steer(context.Background(), "sbx-pi-ack", SteerMessage{FollowUpRunID: 21, Text: "new commits on the branch"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if got := f.steerResults(); len(got) != 0 {
		t.Fatalf("delivery recorded at append time, before pi acked: %+v", got)
	}

	// pi consumes the opening prompt, then the steer.
	ackNextOutstanding(f, steerEchoTime(""))
	if got := f.steerResults(); len(got) != 0 {
		t.Fatalf("the opening prompt's ack was credited to the steer: %+v", got)
	}
	ackNextOutstanding(f, steerEchoTime(""))

	got := f.steerResults()
	if len(got) != 1 {
		t.Fatalf("expected exactly one recorded delivery after the steer's ack, got %d", len(got))
	}
	if got[0].FollowUpRunID != 21 {
		t.Errorf("FollowUpRunID not carried through from the SteerMessage: %d", got[0].FollowUpRunID)
	}
	if got[0].Mode != SteerModeLive {
		t.Errorf("expected mode %q, got %q", SteerModeLive, got[0].Mode)
	}
	if got[0].DeliveredAt.IsZero() {
		t.Error("DeliveredAt was not recorded")
	}
}

// TestPiSteer_FailedAppendRecordsNoDelivery: pi's mailbox write is the same
// shared path as Claude's, and a write that never landed must not appear as
// a delivery.
func TestPiSteer_FailedAppendRecordsNoDelivery(t *testing.T) {
	var calls []string
	f := newSteerFeed("sbx", "/sandbox/pi-config", recordingCtxExec(&calls, "disk full", 1, nil))
	f.noteInitialPrompt(testPromptKey)
	registerSteerFeed("sbx-pi-fail", f)
	defer unregisterSteerFeed("sbx-pi-fail")

	rt := PiRuntime{}
	if err := rt.Steer(context.Background(), "sbx-pi-fail", SteerMessage{FollowUpRunID: 22, Text: "x"}); err == nil {
		t.Fatal("expected the failed mailbox write to surface as an error")
	}
	if got := f.steerResults(); len(got) != 0 {
		t.Errorf("a failed mailbox write was recorded as a delivery: %+v", got)
	}
}

// TestPiSteer_AfterSettleIsRefused mirrors the Claude case: pi shares the
// same feed, so the refusal must reach its caller as an error rather than
// as a silent success.
func TestPiSteer_AfterSettleIsRefused(t *testing.T) {
	var calls []string
	f := newTestFeed(&calls)
	registerSteerFeed("sbx-pi-after-settle", f)
	defer unregisterSteerFeed("sbx-pi-after-settle")

	f.mu.Lock()
	f.settled = true
	f.mu.Unlock()

	err := PiRuntime{}.Steer(context.Background(), "sbx-pi-after-settle", SteerMessage{FollowUpRunID: 5, Text: "late"})
	if !errors.Is(err, ErrSteerAfterSettle) {
		t.Fatalf("expected ErrSteerAfterSettle, got %v", err)
	}
}

// TestPiAttemptEventHandler_SteeredResultsGoOutPerTurn pins the seam
// between the steer path and the #7026 fallback loop: an attempt holds its
// ResultEvent back for the loop to replay, but a steered run must forward
// each turn's result live and take the settle decision in stream order.
// Held back, the first turn's result would never reach noteTurnEnd, the
// feeder would never stop, and the run would sit out params.Timeout.
func TestPiAttemptEventHandler_SteeredResultsGoOutPerTurn(t *testing.T) {
	run := func(settleMidTurn bool) (log []string, last *ResultEvent) {
		f := newSteerFeed("sbx", "/sandbox/pi-config", func(_ context.Context, _ string, cmd string, _ time.Duration) (string, string, int, error) {
			log = append(log, "exec: "+cmd)
			return "", "", 0, nil
		})
		f.noteInitialPrompt(testPromptKey)
		gate := &piAttemptGate{next: func(evt AgentEvent) {
			if r, ok := evt.(ResultEvent); ok {
				log = append(log, "render: "+r.Subtype)
			}
		}}
		h := piAttemptEventHandler(context.Background(), f, gate, ui.New(io.Discard), &last)
		h(UserReplayEvent{ID: testPromptKey, At: steerEchoTime("")})
		if settleMidTurn && f.settle() {
			t.Fatal("settle stopped the feeder while a turn was in flight")
		}
		h(ResultEvent{Subtype: "turn-1"})
		return log, last
	}

	// Unsettled: the turn's result goes out live and the feeder stays up
	// for the next steer.
	log, last := run(false)
	if len(log) != 1 || log[0] != "render: turn-1" {
		t.Fatalf("unsettled: want only the result rendered, got %v", log)
	}
	if last == nil || last.Subtype != "turn-1" {
		t.Fatalf("lastResult = %+v, want turn-1", last)
	}

	// Settled mid-turn: the turn end renders the result, then stops the
	// feeder.
	log, _ = run(true)
	if len(log) != 2 || log[0] != "render: turn-1" || !strings.HasPrefix(log[1], "exec: kill ") {
		t.Fatalf("settled: want render then feeder stop, got %v", log)
	}
}

// TestPiAttemptEventHandler_UnsteeredResultIsHeld keeps the fallback
// loop's contract: without a feed the attempt never forwards its result, so
// an attempt abandoned for a fallback renders no result block.
func TestPiAttemptEventHandler_UnsteeredResultIsHeld(t *testing.T) {
	var rendered int
	gate := &piAttemptGate{next: func(evt AgentEvent) {
		if _, ok := evt.(ResultEvent); ok {
			rendered++
		}
	}}
	var last *ResultEvent
	h := piAttemptEventHandler(context.Background(), nil, gate, ui.New(io.Discard), &last)

	h(ResultEvent{Subtype: "error", IsError: true})
	if rendered != 0 {
		t.Fatalf("an unsteered attempt forwarded its result %d time(s)", rendered)
	}
	if last == nil || last.Subtype != "error" {
		t.Fatalf("lastResult = %+v, want the captured result", last)
	}
}

// TestPiSteerGate_FallbackWins pins the choice between the two features on
// pi: a run whose chain can fall back is not steerable, is announced once,
// and has Steer decline it as unsupported so the runner leaves the update
// to the queued run rather than retrying a session that never registers.
// The chain is not touched, so the #7026 fallback path runs as it would
// without steering.
func TestPiSteerGate_FallbackWins(t *testing.T) {
	const sbx = "sbx-pi-gate-fallback"
	var out bytes.Buffer
	chain := []string{"google-vertex-anthropic/claude-opus-4-6", "google-vertex-anthropic/claude-sonnet-4-6"}
	before := append([]string(nil), chain...)

	if piSteerGate(true, sbx, chain, ui.New(&out)) {
		t.Fatal("a run that can fall back must not be steerable")
	}
	defer clearPiSteerDecline(sbx)
	if strings.Count(out.String(), "Steering disabled: ") != 1 || !strings.Contains(out.String(), "falls back across models") {
		t.Fatalf("want one line naming the reason, got %q", out.String())
	}
	if strings.Join(chain, ",") != strings.Join(before, ",") {
		t.Fatalf("the gate changed the fallback chain: %v", chain)
	}

	err := PiRuntime{}.Steer(context.Background(), sbx, SteerMessage{Text: "hi"})
	if !errors.Is(err, ErrSteerUnsupported) || errors.Is(err, ErrNoSteerSession) {
		t.Fatalf("Steer on a declined run: want ErrSteerUnsupported, got %v", err)
	}

	// Once the run has returned, the sandbox is an ordinary one again.
	clearPiSteerDecline(sbx)
	if err := (PiRuntime{}).Steer(context.Background(), sbx, SteerMessage{Text: "hi"}); !errors.Is(err, ErrNoSteerSession) {
		t.Fatalf("after the run: want ErrNoSteerSession, got %v", err)
	}
}

// TestPiSteerGate_NoFallbackSteers keeps a single-model pi run steerable,
// silently, and leaves an unsteered request alone whatever the chain.
func TestPiSteerGate_NoFallbackSteers(t *testing.T) {
	var out bytes.Buffer
	if !piSteerGate(true, "sbx-pi-gate-single", []string{"google-vertex-anthropic/claude-opus-4-6"}, ui.New(&out)) {
		t.Fatal("a single-model run must stay steerable")
	}
	if piSteerGate(false, "sbx-pi-gate-off", []string{"a", "b"}, ui.New(&out)) {
		t.Fatal("the gate must never turn steering on")
	}
	if out.Len() != 0 {
		t.Fatalf("no decline to announce, got %q", out.String())
	}
	if _, declined := piSteerDeclines.Load("sbx-pi-gate-off"); declined {
		t.Fatal("an unsteered request must not be recorded as a decline")
	}
}
