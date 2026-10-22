package poll

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// openedTriageRouter routes a work-item open to triage and nothing else, so
// these tests exercise issue-open recovery independent of HarnessRouter's
// opened routing.
type openedTriageRouter struct{}

func (openedTriageRouter) Route(ev *dispatch.NormalizedEvent) ([]string, error) {
	if ev.Transition.Kind == "opened" && ev.Entity.Kind == "work_item" {
		return []string{"triage"}, nil
	}
	return nil, nil
}

// beforeRecentTime is beforeRecent as a time: an installation cutoff that
// precedes the fixture issue's creation.
var beforeRecentTime = recent.Add(-10 * time.Minute).Truncate(time.Second)

func issueOpenPoller(mc *mockClient) *Poller {
	p := New(mc, openedTriageRouter{}, "group/project", withTestSecret(Options{
		Mode:        "events",
		BotUserID:   100,
		GitLabURL:   "https://gitlab.example.com",
		PipelineRef: "main",
	}))
	p.now = func() time.Time { return webhookTestNow }
	return p
}

// issueOpenFixture seeds issue #9, opened by alice at recent, as both the
// webhook re-fetch and events-mode discovery see it.
func issueOpenFixture(mc *mockClient, author UserRef) {
	mc.memberLevel[author.ID] = 30
	iss := Issue{IID: 9, State: "opened", Labels: []string{"bug"}, Author: author, CreatedAt: recent, UpdatedAt: recent}
	mc.issue[9] = &iss
	mc.issues = []Issue{iss}
}

func issueOpenEvents(events []RoutableEvent) []RoutableEvent {
	var out []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_event" && e.Action == "opened" {
			out = append(out, e)
		}
	}
	return out
}

func TestDiscoverAllEvents_IssueOpened(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{{
		IID: 7, State: "opened", Labels: []string{"bug"},
		Author: UserRef{ID: 42, Username: "alice"}, CreatedAt: now, UpdatedAt: now,
	}}

	p := newEventsPoller(mc)
	p.issueOpenCutoff = since
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opened := issueOpenEvents(events)
	if len(opened) != 1 {
		t.Fatalf("expected 1 opened issue_event, got %d (%+v)", len(opened), events)
	}
	got := opened[0]
	if got.IID != 7 || !got.UpdatedAt.Equal(now) || got.NoteAuthorID != 42 || got.NoteAuthorLogin != "alice" {
		t.Errorf("event = %+v", got)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "bug" {
		t.Errorf("Labels = %v, want [bug]", got.Labels)
	}
	// The webhook builder keys the same open on created_at (webhookIssueEvents).
	if want := fmt.Sprintf("issue_event-7-opened-%d", now.Unix()); got.Key() != want {
		t.Errorf("Key() = %q, want %q", got.Key(), want)
	}
}

func TestDiscoverAllEvents_IssueOpenedSkipped(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	alice := UserRef{ID: 42, Username: "alice"}
	tests := []struct {
		name   string
		issue  Issue
		cutoff time.Time
	}{
		{
			name:   "created before watermark, edited after",
			issue:  Issue{IID: 7, State: "opened", Author: alice, CreatedAt: since.Add(-24 * time.Hour), UpdatedAt: now},
			cutoff: since.Add(-48 * time.Hour),
		},
		{
			name:   "closed since it was opened",
			issue:  Issue{IID: 7, State: "closed", Author: alice, CreatedAt: now, UpdatedAt: now},
			cutoff: since,
		},
		{
			name:   "no created_at",
			issue:  Issue{IID: 7, State: "opened", Author: alice, UpdatedAt: now},
			cutoff: since,
		},
		{
			name:   "no cutoff",
			issue:  Issue{IID: 7, State: "opened", Author: alice, CreatedAt: now, UpdatedAt: now},
			cutoff: time.Time{},
		},
		{
			name:   "after the watermark but before the installation cutoff",
			issue:  Issue{IID: 7, State: "opened", Author: alice, CreatedAt: now.Add(-10 * time.Second), UpdatedAt: now},
			cutoff: now.Add(-5 * time.Second),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mc.issues = []Issue{tt.issue}
			p := newEventsPoller(mc)
			p.issueOpenCutoff = tt.cutoff
			events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opened := issueOpenEvents(events); len(opened) != 0 {
				t.Errorf("expected no opened issue_event, got %+v", opened)
			}
		})
	}
}

// A notes failure holds the watermark only to the issue's updated_at, which
// can be past created_at, so the open must still be emitted this cycle.
func TestDiscoverAllEvents_IssueOpenedSurvivesNotesFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{{
		IID: 7, State: "opened", Author: UserRef{ID: 42, Username: "alice"},
		CreatedAt: now.Add(-30 * time.Second), UpdatedAt: now,
	}}
	mc.noteErr[7] = errors.New("boom")

	p := newEventsPoller(mc)
	p.issueOpenCutoff = since
	events, _, minSkipped, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issueOpenEvents(events)) != 1 {
		t.Fatalf("expected 1 opened issue_event despite notes failure, got %+v", events)
	}
	if !minSkipped.Equal(now) {
		t.Errorf("minSkippedAt = %v, want %v", minSkipped, now)
	}
}

// TestRun_RecoversMissedIssueOpenOnce: with no webhook delivery, the poll
// dispatches triage for the new issue once, and the next cycle (whose
// watermark overlap still covers created_at) does not dispatch it again.
func TestRun_RecoversMissedIssueOpenOnce(t *testing.T) {
	mc := newMockClient()
	issueOpenFixture(mc, alice)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	for i := 0; i < 2; i++ {
		if err := issueOpenPoller(mc).Run(context.Background()); err != nil {
			t.Fatalf("Run #%d: %v", i, err)
		}
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	key := fmt.Sprintf("triage:issue_event-9-opened-%d", recent.Unix())
	if _, ok := state.DispatchedKeysFull[key]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want %s", state.DispatchedKeysFull, key)
	}
}

// TestRun_RecoversMissedIssueOpenWithHarnessRouter: the recovered open routes
// to triage through the production router, not just the test double above.
func TestRun_RecoversMissedIssueOpenWithHarnessRouter(t *testing.T) {
	mc := newMockClient()
	issueOpenFixture(mc, alice)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	router := dispatch.NewHarnessRouter([]string{"triage", "code", "review", "fix", "retro"})
	p := New(mc, router, "group/project", withTestSecret(Options{
		Mode:        "events",
		BotUserID:   100,
		GitLabURL:   "https://gitlab.example.com",
		PipelineRef: "main",
	}))
	p.now = func() time.Time { return webhookTestNow }
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	key := fmt.Sprintf("triage:issue_event-9-opened-%d", recent.Unix())
	if _, ok := state.DispatchedKeysFull[key]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want %s", state.DispatchedKeysFull, key)
	}
}

// TestRun_IssueOpenAfterWebhookNotDuplicated: the webhook dispatched the
// open, so the poll that later discovers it shares the key and skips it.
func TestRun_IssueOpenAfterWebhookNotDuplicated(t *testing.T) {
	mc := newMockClient()
	issueOpenFixture(mc, alice)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	if err := issueOpenPoller(mc).RunWebhook(context.Background(), issuePayload(t, "open", alice.ID, 9, nil)); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d after webhook, want 1", len(mc.pipelineCalls))
	}
	for i := 0; i < 2; i++ {
		if err := issueOpenPoller(mc).Run(context.Background()); err != nil {
			t.Fatalf("Run #%d: %v", i, err)
		}
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after polls, want 1", len(mc.pipelineCalls))
	}
}

// TestRun_FirstCycleDoesNotBackfillIssueOpens: with no persisted watermark the
// poll falls back to one hour ago, which must not reach issues opened before
// installation. The following cycle has a persisted watermark past them.
func TestRun_FirstCycleDoesNotBackfillIssueOpens(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	created := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	iss := Issue{IID: 9, State: "opened", Author: alice, CreatedAt: created, UpdatedAt: created}
	mc.issue[9] = &iss
	mc.issues = []Issue{iss}

	for i := 0; i < 2; i++ {
		p := issueOpenPoller(mc)
		p.now = nil // real clock: the issue's created_at is real time
		if err := p.Run(context.Background()); err != nil {
			t.Fatalf("Run #%d: %v", i, err)
		}
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

func TestRun_BotIssueOpenNotDispatched(t *testing.T) {
	mc := newMockClient()
	issueOpenFixture(mc, UserRef{ID: 100, Username: "fullsend-bot"})
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	if err := issueOpenPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

func TestRun_ServiceAccountIssueOpenNotDispatched(t *testing.T) {
	mc := newMockClient()
	// GitLab's nested issue author can omit bot, and a service account's
	// username can be customized. The full user record still reports bot.
	author := UserRef{ID: 205, Username: "release-automation"}
	issueOpenFixture(mc, author)
	mc.userBot[author.ID] = true
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	if err := issueOpenPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

func TestRun_IssueAuthorLookupFailureDoesNotAdvanceWatermark(t *testing.T) {
	mc := newMockClient()
	author := UserRef{ID: 205, Username: "release-automation"}
	issueOpenFixture(mc, author)
	mc.userBotErr[author.ID] = errors.New("temporary user API failure")
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	if err := issueOpenPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("expected author lookup failure")
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
	state, ok := mc.getPollState()
	if !ok {
		t.Fatal("poll state missing")
	}
	if state.LastPollAtFull != beforeRecent {
		t.Errorf("watermark = %q, want %q", state.LastPollAtFull, beforeRecent)
	}
}

// TestRun_IssueAuthorLookupFailureDoesNotBlockOtherEvents: a persistently
// failing author lookup fails closed for that issue open only. An unrelated
// issue open still dispatches once, the failing one is held back and counted
// against its retry budget, and the poll stops retrying it once spent.
func TestRun_IssueAuthorLookupFailureDoesNotBlockOtherEvents(t *testing.T) {
	mc := newMockClient()
	failing := UserRef{ID: 205, Username: "release-automation"}
	issueOpenFixture(mc, failing)
	mc.memberLevel[alice.ID] = 30
	other := Issue{IID: 10, State: "opened", Labels: []string{"bug"}, Author: alice, CreatedAt: recent, UpdatedAt: recent}
	mc.issue[10] = &other
	mc.issues = append(mc.issues, other)
	mc.userBotErr[failing.ID] = errors.New("temporary user API failure")
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	if err := issueOpenPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("expected author lookup failure to be reported")
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1 (unrelated issue open)", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	watermark, err := time.Parse(time.RFC3339, state.LastPollAtFull)
	if err != nil {
		t.Fatalf("parse watermark %q: %v", state.LastPollAtFull, err)
	}
	if watermark.After(recent) {
		t.Errorf("watermark = %s, want held back at or before the failing open's created_at %s", watermark, recent)
	}
	failedKey := fmt.Sprintf("issue_event-9-opened-%d", recent.Unix())
	if got := state.FailedKeysFull[failedKey]; got != 1 {
		t.Errorf("FailedKeysFull[%s] = %d, want 1", failedKey, got)
	}

	for i := 1; i < maxEventRetries; i++ {
		_ = issueOpenPoller(mc).Run(context.Background())
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after retries, want 1 (unrelated open not redispatched)", len(mc.pipelineCalls))
	}
	state, _ = mc.getPollState()
	if got := state.FailedKeysFull[failedKey]; got < maxEventRetries {
		t.Errorf("FailedKeysFull[%s] = %d, want >= %d", failedKey, got, maxEventRetries)
	}
	// Budget spent: the next cycle skips the lookup and succeeds.
	if err := issueOpenPoller(mc).Run(context.Background()); err != nil {
		t.Errorf("Run after retry budget spent: %v", err)
	}
}

// TestRun_IssueAuthorLookupSkippedWhenAlreadyDispatched: an issue open the
// webhook already dispatched needs no author lookup, so a failing lookup
// cannot fail the poll or re-dispatch it.
func TestRun_IssueAuthorLookupSkippedWhenAlreadyDispatched(t *testing.T) {
	mc := newMockClient()
	issueOpenFixture(mc, alice)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})

	if err := issueOpenPoller(mc).RunWebhook(context.Background(), issuePayload(t, "open", alice.ID, 9, nil)); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	mc.userBotErr[alice.ID] = errors.New("temporary user API failure")
	if err := issueOpenPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
}

func TestReadIssueOpenCutoff(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, "group/project", withTestSecret(Options{}))
	if _, persisted, err := p.readIssueOpenCutoff(context.Background(), "group", "project"); err != nil || persisted {
		t.Errorf("empty state: persisted = %v, err = %v; want false, nil", persisted, err)
	}
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent, IssueOpenCutoff: beforeRecentTime})
	got, persisted, err := p.readIssueOpenCutoff(context.Background(), "group", "project")
	if err != nil || !persisted || !got.Equal(beforeRecentTime) {
		t.Errorf("stored state: got %v, persisted = %v, err = %v; want %v, true, nil", got, persisted, err, beforeRecentTime)
	}
}

// The cutoff travels as a failed_keys_full entry so a writer from before it
// existed round-trips it; it must not surface as a retry-count key.
func TestIssueOpenCutoffWireRoundTrip(t *testing.T) {
	cutoff := time.UnixMilli(1_700_000_123_456)
	state := persistedPollState{
		IssueOpenCutoff: cutoff,
		FailedKeysFull:  map[string]int{"issue_event-1-opened-5": 2},
	}
	wire := encodeIssueOpenCutoff(state)
	if !hasKeyPrefix(wire.FailedKeysFull, issueOpenCutoffKeyPrefix) {
		t.Fatalf("encoded FailedKeysFull = %v, want a cutoff entry", wire.FailedKeysFull)
	}
	if len(state.FailedKeysFull) != 1 {
		t.Errorf("encode modified its input: %v", state.FailedKeysFull)
	}
	got := decodeIssueOpenCutoff(wire)
	if !got.IssueOpenCutoff.Equal(cutoff) {
		t.Errorf("IssueOpenCutoff = %v, want %v", got.IssueOpenCutoff, cutoff)
	}
	if len(got.FailedKeysFull) != 1 || got.FailedKeysFull["issue_event-1-opened-5"] != 2 {
		t.Errorf("FailedKeysFull = %v, want only the retry count", got.FailedKeysFull)
	}
	bad := decodeIssueOpenCutoff(persistedPollState{FailedKeysFull: map[string]int{issueOpenCutoffKeyPrefix + "nope": 1}})
	if !bad.IssueOpenCutoff.IsZero() || len(bad.FailedKeysFull) != 0 {
		t.Errorf("malformed entry: cutoff = %v, failed = %v; want dropped", bad.IssueOpenCutoff, bad.FailedKeysFull)
	}
}

// The cutoff is set once: a later cycle (or a concurrent writer) never moves
// an already-persisted one.
func TestApplyPersistDeltas_IssueOpenCutoffSetOnce(t *testing.T) {
	p := New(newMockClient(), nil, "group/project", withTestSecret(Options{Mode: "events"}))
	first := time.UnixMilli(1_700_000_000_000)
	later := first.Add(time.Hour)
	state := p.applyPersistDeltas(persistedPollState{}, persistDeltas{issueOpenCutoff: &first})
	if !state.IssueOpenCutoff.Equal(first) {
		t.Fatalf("IssueOpenCutoff = %v, want %v", state.IssueOpenCutoff, first)
	}
	state = p.applyPersistDeltas(state, persistDeltas{issueOpenCutoff: &later})
	if !state.IssueOpenCutoff.Equal(first) {
		t.Errorf("IssueOpenCutoff = %v after a later delta, want unchanged %v", state.IssueOpenCutoff, first)
	}
}

// firstCyclePredatingIssue seeds a quiet issue created before the poller's
// first cycle, listed the way the real updated_after filter lists it.
func firstCyclePredatingIssue(mc *mockClient, created time.Time) {
	mc.memberLevel[alice.ID] = 30
	mc.issuesHonorSince = true
	iss := Issue{IID: 9, State: "opened", Author: alice, CreatedAt: created, UpdatedAt: created}
	mc.issue[9] = &iss
	mc.issues = []Issue{iss}
}

func runCycles(t *testing.T, mc *mockClient, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		p := issueOpenPoller(mc)
		p.now = nil // real clock: the watermark and cutoff share one timeline
		// A cycle may report errors the scenario provokes; only dispatches matter.
		_ = p.Run(context.Background())
	}
}

// A first-cycle notes failure holds the watermark back to the issue's
// updated_at, which lets the next cycle's discovery reach created_at.
func TestRun_FirstCycleNotesFailureDoesNotBackfillOnSecondCycle(t *testing.T) {
	mc := newMockClient()
	firstCyclePredatingIssue(mc, time.Now().Add(-10*time.Minute).Truncate(time.Second))
	mc.noteErr[9] = errors.New("boom")
	runCycles(t, mc, 1)
	delete(mc.noteErr, 9)
	runCycles(t, mc, 1)
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

// An older note observed on the first cycle pulls the watermark up to an hour
// back; a quiet issue created after that note but before installation must
// still not be recovered.
func TestRun_FirstCycleOlderEventDoesNotBackfillOnSecondCycle(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issuesHonorSince = true
	noteAt := time.Now().Add(-50 * time.Minute).Truncate(time.Second)
	createdAt := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	older := Issue{IID: 8, State: "opened", Author: alice, CreatedAt: noteAt.Add(-time.Hour), UpdatedAt: noteAt}
	quiet := Issue{IID: 9, State: "opened", Author: alice, CreatedAt: createdAt, UpdatedAt: createdAt}
	mc.issue[8], mc.issue[9] = &older, &quiet
	mc.issues = []Issue{older, quiet}
	mc.notes[8] = []Note{{ID: 1, Body: "hello", Author: alice, CreatedAt: noteAt}}
	runCycles(t, mc, 2)
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

// An issue created in the 30s watermark overlap just before the first cycle
// predates installation too.
func TestRun_FirstCycleOverlapDoesNotBackfillOnSecondCycle(t *testing.T) {
	mc := newMockClient()
	firstCyclePredatingIssue(mc, time.Now().Add(-10*time.Second).Truncate(time.Second))
	runCycles(t, mc, 2)
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

// The first cycle persists the cutoff, and an issue opened after it is then
// recovered (once) by a later cycle.
func TestRun_IssueOpenedAfterFirstCycleIsRecovered(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issuesHonorSince = true
	runCycles(t, mc, 1)
	state, _ := mc.getPollState()
	if state.IssueOpenCutoff.IsZero() {
		t.Fatal("first cycle did not persist an issue-open cutoff")
	}
	created := time.Now().Add(time.Second).Truncate(time.Second)
	iss := Issue{IID: 9, State: "opened", Author: alice, CreatedAt: created, UpdatedAt: created}
	mc.issue[9] = &iss
	mc.issues = []Issue{iss}
	runCycles(t, mc, 2)
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
}

// A first cycle that fails during discovery must still have persisted the
// cutoff, so an issue opened between that cycle's start and the next cycle's
// start (with a missed webhook) is recovered rather than falling before a
// later cutoff.
func TestRun_FirstCycleDiscoveryFailurePersistsCutoff(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issuesHonorSince = true
	mc.issuesErr = errors.New("boom")
	firstStart := time.Now().Add(-time.Hour).Truncate(time.Second)
	p := issueOpenPoller(mc)
	p.now = func() time.Time { return firstStart }
	if err := p.Run(context.Background()); err == nil {
		t.Fatal("first cycle succeeded, want a discovery error")
	}
	state, _ := mc.getPollState()
	if !state.IssueOpenCutoff.Equal(firstStart) {
		t.Fatalf("IssueOpenCutoff = %v, want first cycle start %v", state.IssueOpenCutoff, firstStart)
	}

	mc.issuesErr = nil
	created := firstStart.Add(time.Second)
	iss := Issue{IID: 9, State: "opened", Author: alice, CreatedAt: created, UpdatedAt: created}
	mc.issue[9] = &iss
	mc.issues = []Issue{iss}
	runCycles(t, mc, 1)
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
}

// A transient failure of either initial state read (watermark, cutoff) on the
// first cycle must still persist the cutoff, so a missed-webhook issue opened
// before the next cycle starts is recovered.
func TestRun_FirstCycleStateReadFailurePersistsCutoff(t *testing.T) {
	for _, tc := range []struct {
		name string
		read int
	}{
		{"watermark read", 1},
		{"cutoff read", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mc := newMockClient()
			mc.memberLevel[alice.ID] = 30
			mc.issuesHonorSince = true
			mc.fileContentErrAt = map[int]error{tc.read: errors.New("network failure")}
			firstStart := time.Now().Add(-time.Hour).Truncate(time.Second)
			p := issueOpenPoller(mc)
			p.now = func() time.Time { return firstStart }
			if err := p.Run(context.Background()); err == nil {
				t.Fatal("first cycle succeeded, want a state read error")
			}
			state, _ := mc.getPollState()
			if !state.IssueOpenCutoff.Equal(firstStart) {
				t.Fatalf("IssueOpenCutoff = %v, want first cycle start %v", state.IssueOpenCutoff, firstStart)
			}

			created := firstStart.Add(time.Second)
			iss := Issue{IID: 9, State: "opened", Author: alice, CreatedAt: created, UpdatedAt: created}
			mc.issue[9] = &iss
			mc.issues = []Issue{iss}
			runCycles(t, mc, 1)
			if len(mc.pipelineCalls) != 1 {
				t.Errorf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
			}
		})
	}
}

// Tampered poll state is not transient: the cycle stays fail-closed and
// persists nothing.
func TestRun_TamperedStateReadDoesNotPersistCutoff(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErrAt = map[int]error{1: fmt.Errorf("%w on branch", errPollStateTampered)}
	p := issueOpenPoller(mc)
	if err := p.Run(context.Background()); !errors.Is(err, errPollStateTampered) {
		t.Fatalf("Run error = %v, want errPollStateTampered", err)
	}
	if state, ok := mc.getPollState(); ok && !state.IssueOpenCutoff.IsZero() {
		t.Errorf("IssueOpenCutoff = %v, want none persisted", state.IssueOpenCutoff)
	}
}

// Discovery re-reads poll state (detectNewLabels, the third read). A tamper
// detected there is not a transient discovery failure: the cycle stays
// fail-closed and writes no replacement commit.
func TestRun_DiscoveryTimeTamperDoesNotPersistCutoff(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issuesHonorSince = true
	mc.fileContentErrAt = map[int]error{3: fmt.Errorf("%w on branch", errPollStateTampered)}
	p := issueOpenPoller(mc)
	if err := p.Run(context.Background()); !errors.Is(err, errPollStateTampered) {
		t.Fatalf("Run error = %v, want errPollStateTampered", err)
	}
	if mc.forceCommits != 0 {
		t.Errorf("force commits = %d, want 0", mc.forceCommits)
	}
	if state, ok := mc.getPollState(); ok && !state.IssueOpenCutoff.IsZero() {
		t.Errorf("IssueOpenCutoff = %v, want none persisted", state.IssueOpenCutoff)
	}
}

// Malformed poll-state JSON is an integrity failure: the branch is discarded
// and the cycle writes no replacement commit.
func TestRun_MalformedStateDoesNotPersistCutoff(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issuesHonorSince = true
	mc.setPollStateRaw(`{invalid json`)
	p := issueOpenPoller(mc)
	if err := p.Run(context.Background()); !errors.Is(err, errPollStateTampered) {
		t.Fatalf("Run error = %v, want errPollStateTampered", err)
	}
	if len(mc.deletedRefs) == 0 {
		t.Error("malformed branch must be discarded")
	}
	if mc.forceCommits != 0 {
		t.Errorf("force commits = %d, want 0", mc.forceCommits)
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("no replacement poll state may be written in the failed cycle")
	}
}

// With no persisted watermark but a cutoff older than the one-hour fallback
// (a first cycle that failed hours ago, or a cutoff seeded at install), the
// first successful discovery must start at the cutoff, not an hour back, so an
// issue opened shortly after the cutoff with a missed webhook is recovered.
func TestRun_OldCutoffWithoutWatermarkRecoversIssueOpen(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issuesHonorSince = true
	cutoff := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	mc.setPollState(persistedPollState{IssueOpenCutoff: cutoff})
	created := cutoff.Add(5 * time.Minute)
	iss := Issue{IID: 9, State: "opened", Author: alice, CreatedAt: created, UpdatedAt: created}
	mc.issue[9] = &iss
	mc.issues = []Issue{iss}
	runCycles(t, mc, 1)
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
}

func TestReadWatermark_FallbackWithCutoff(t *testing.T) {
	cutoffAgo := func(d time.Duration) persistedPollState {
		return persistedPollState{IssueOpenCutoff: time.Now().Add(-d)}
	}
	tests := []struct {
		name    string
		mode    string
		state   persistedPollState
		wantAgo time.Duration
	}{
		{"no cutoff", "events", persistedPollState{}, time.Hour},
		{"cutoff older than an hour", "events", cutoffAgo(2 * time.Hour), 2 * time.Hour},
		{"cutoff newer than an hour", "events", cutoffAgo(10 * time.Minute), time.Hour},
		{"slash mode ignores the cutoff", "slash", cutoffAgo(2 * time.Hour), time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mc.setPollState(tt.state)
			p := issueOpenPoller(mc)
			p.opts.Mode = tt.mode
			p.slashCommandsOnly = tt.mode == "slash"
			got, err := p.readWatermark(context.Background(), "group", "project")
			if err != nil {
				t.Fatalf("readWatermark: %v", err)
			}
			if d := time.Since(got) - tt.wantAgo; d < -time.Minute || d > time.Minute {
				t.Errorf("watermark = %v ago, want about %v", time.Since(got), tt.wantAgo)
			}
		})
	}
}

// The pending-label and dedup-state reads follow the same policy as the
// initial reads: a transient failure persists the cutoff, tampered state does
// not.
func TestRun_LaterStateReadFailureCutoffPolicy(t *testing.T) {
	// Reads 1-3 are the watermark, the cutoff, and discovery's label state.
	for _, read := range []int{4, 5} {
		for _, tc := range []struct {
			name       string
			err        error
			wantCutoff bool
		}{
			{"transient", errors.New("network failure"), true},
			{"tampered", fmt.Errorf("%w on branch", errPollStateTampered), false},
		} {
			t.Run(fmt.Sprintf("read %d %s", read, tc.name), func(t *testing.T) {
				mc := newMockClient()
				mc.memberLevel[alice.ID] = 30
				mc.issuesHonorSince = true
				mc.fileContentErrAt = map[int]error{read: tc.err}
				p := issueOpenPoller(mc)
				if err := p.Run(context.Background()); err == nil {
					t.Fatal("cycle succeeded, want a state read error")
				}
				state, _ := mc.getPollState()
				if got := !state.IssueOpenCutoff.IsZero(); got != tc.wantCutoff {
					t.Errorf("cutoff persisted = %v, want %v", got, tc.wantCutoff)
				}
			})
		}
	}
}
