package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBuildPiTurnCommand_Steerable(t *testing.T) {
	params := RunParams{RepoDir: "/repo", Steerable: true}
	cmd := buildPiTurnCommand(params, &piManifest{}, nil, "", "01a06800-0000-7000-8000-0000000000ab")

	for _, want := range []string{
		"{ tail -n +1 -f '/sandbox/pi-config/steer-inbox.ndjson' &",
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
	if err := rt.Steer(context.Background(), "nope", SteerMessage{}); !errors.Is(err, errNoSteerSession) {
		t.Fatalf("expected errNoSteerSession, got %v", err)
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
	_, err := parsePiStreamMode(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		switch e := evt.(type) {
		case ResultEvent:
			results = append(results, e)
		case UserReplayEvent:
			acks++
		}
	}, true)
	if err != nil {
		t.Fatalf("parsePiStreamMode: %v", err)
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
	_, err := parsePiStreamMode(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}, true)
	if err != nil {
		t.Fatalf("parsePiStreamMode: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("EOF after the last settled turn duplicated the result: got %d", len(results))
	}
	if results[0].IsError {
		t.Error("a clean end-of-steered-run was reported as an error")
	}
}

// TestParsePiStreamMode_FailedAckIsNotADelivery: a rejected command never
// reached the agent, so counting it would let the run settle with a steer
// still unread.
func TestParsePiStreamMode_FailedAckIsNotADelivery(t *testing.T) {
	input := `{"id":"p1","type":"response","command":"prompt","success":false}` + "\n" +
		`{"id":"p2","type":"response","command":"interrupt","success":true}` + "\n"
	acks := 0
	_, err := parsePiStreamMode(strings.NewReader(input), func(evt AgentEvent) {
		if _, ok := evt.(UserReplayEvent); ok {
			acks++
		}
	}, true)
	if err != nil {
		t.Fatalf("parsePiStreamMode: %v", err)
	}
	if acks != 0 {
		t.Errorf("a failed prompt or a non-prompt command was counted as a delivery: %d", acks)
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
	sid, err := parsePiStreamMode(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			results = append(results, e)
		}
	}, false)
	if err != nil {
		t.Fatalf("parsePiStreamMode: %v", err)
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
	if got[0].Mode != steerModeLive {
		t.Errorf("expected mode %q, got %q", steerModeLive, got[0].Mode)
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
