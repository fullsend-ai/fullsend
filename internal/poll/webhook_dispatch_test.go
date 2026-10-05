package poll

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// beforeRecent is a poll watermark early enough that a poll cycle
// discovers events at `recent`.
var beforeRecent = recent.Add(-10 * time.Minute).Format(time.RFC3339)

// webhookSlashNoteFixture seeds issue #4 with a fresh /fs-triage note (ID 12)
// authored by alice, as both the webhook re-fetch and the slash poll's
// project-events discovery see it.
func webhookSlashNoteFixture(mc *mockClient) {
	mc.memberLevel[alice.ID] = 30
	mc.issue[4] = &Issue{IID: 4, State: "opened", Labels: []string{"bug"}, Author: bob}
	mc.notes[4] = []Note{{ID: 12, Body: "/fs-triage please", Author: alice, CreatedAt: recent}}
	mc.events = []ProjectEvent{{
		ID:        1,
		Author:    alice,
		CreatedAt: recent,
		Note:      EventNote{ID: 12, NoteableType: "Issue", NoteableIID: 4, Body: "/fs-triage please"},
	}}
}

// webhookLabelAddFixture seeds issue #5 with a fresh ready-to-code addition by
// alice, as both the webhook re-fetch and events-mode discovery see it.
func webhookLabelAddFixture(mc *mockClient) {
	mc.memberLevel[alice.ID] = 30
	iss := Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, Author: bob, UpdatedAt: recent}
	mc.issue[5] = &iss
	mc.issues = []Issue{iss}
	mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(1, "add", "ready-to-code", alice, recent)}
}

// TestRunWebhook_SlashNoteExactlyOnce is the end-to-end dedup case for a
// slash command: the webhook dispatches it once, a replayed webhook does
// not dispatch it again, and the slash poll that later observes the same
// note skips it because both writers share DispatchedKeysFast.
func TestRunWebhook_SlashNoteExactlyOnce(t *testing.T) {
	mc := newMockClient()
	webhookSlashNoteFixture(mc)
	mc.setSlashState(persistedPollState{LastPollAtFast: beforeRecent})
	payload := notePayload(t, "Issue", alice.ID, 12, 4)

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	call := mc.pipelineCalls[0]
	if !call.ViaInputs || call.Variables["STAGE"] != "triage" {
		t.Errorf("pipeline call = %+v", call)
	}
	if call.Variables[forge.VarDispatchHMAC] != computeDispatchHMAC(testDispatchSecret, call.Variables) {
		t.Errorf("dispatch not signed with signedDispatchKeys: %v", call.Variables)
	}
	slashState, _ := mc.getSlashState()
	if _, ok := slashState.DispatchedKeysFast["triage:note-12"]; !ok {
		t.Errorf("slash DispatchedKeysFast = %v, want triage:note-12", slashState.DispatchedKeysFast)
	}
	if slashState.LastPollAtFast != beforeRecent {
		t.Errorf("webhook must not move the slash watermark, got %q", slashState.LastPollAtFast)
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("slash-command webhook must not write the events branch")
	}
	if len(mc.emojis) != 1 || mc.emojis[0].NoteID != 12 || mc.emojis[0].Emoji != "eyes" {
		t.Errorf("emojis = %+v", mc.emojis)
	}

	// Replayed webhook delivery.
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("replayed RunWebhook: %v", err)
	}
	// The slash poll observes the same note.
	poller := New(mc, newWebhookPoller(mc).router, "group/project", withTestSecret(Options{Mode: "slash", BotUserID: 100}))
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("slash poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after replay and poll, want exactly 1", len(mc.pipelineCalls))
	}
}

// TestRunWebhook_SkipsKeyPollerAlreadyDispatched covers the reverse
// order: the poller dispatched the note first.
func TestRunWebhook_SkipsKeyPollerAlreadyDispatched(t *testing.T) {
	mc := newMockClient()
	webhookSlashNoteFixture(mc)
	mc.setSlashState(persistedPollState{
		LastPollAtFast:     beforeRecent,
		DispatchedKeysFast: map[string]int64{"triage:note-12": recent.Unix()},
	})

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), notePayload(t, "Issue", alice.ID, 12, 4)); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
	if mc.forceCommits != 0 {
		t.Errorf("state commits = %d, want 0 when nothing dispatched", mc.forceCommits)
	}
}

// TestRunWebhook_LabelAdditionExactlyOnce checks the events-branch path
// and the label-state handoff: the webhook's label key can differ from the
// one poll discovery computes, so the poller must skip the addition
// because the webhook recorded the label in LabelState.
func TestRunWebhook_LabelAdditionExactlyOnce(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{
		LastPollAtFull: beforeRecent,
		LabelState:     LabelState{9: {"ready-for-review"}},
	})
	payload := issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"}))

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].Variables["STAGE"] != "code" {
		t.Fatalf("pipeline calls = %+v, want one code dispatch", mc.pipelineCalls)
	}
	state, _ := mc.getPollState()
	wantKey := "code:issue_label-5-ready-to-code-e1"
	if _, ok := state.DispatchedKeysFull[wantKey]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want %s", state.DispatchedKeysFull, wantKey)
	}
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Errorf("LabelState[5] = %v, want [ready-to-code]", got)
	}
	if got := state.LabelState[9]; len(got) != 1 || got[0] != "ready-for-review" {
		t.Errorf("LabelState[9] = %v, existing entries must survive", got)
	}
	if _, ok := mc.getSlashState(); ok {
		t.Error("label webhook must not write the slash branch")
	}

	poller := New(mc, newWebhookPoller(mc).router, "group/project", withTestSecret(Options{Mode: "events", BotUserID: 100}))
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("events poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after events poll, want exactly 1", len(mc.pipelineCalls))
	}
}

// TestRunWebhook_SkipsLabelPollerAlreadyRecorded covers a label addition
// the poller already observed (and dispatched) under its own key.
func TestRunWebhook_SkipsLabelPollerAlreadyRecorded(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{
		LabelState: LabelState{5: {"ready-to-code"}},
		// Poll discovery keys the addition on the issue's updated_at.
		DispatchedKeysFull: map[string]int64{
			fmt.Sprintf("code:issue_label-5-ready-to-code-%d", recent.Add(5*time.Second).Unix()): recent.Unix() + 5,
		},
	})

	err := newWebhookPoller(mc).RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"})))
	if err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

// TestRunWebhook_LabelReAddedBeforePoll covers remove-then-re-add with no
// intervening poll: the label is still in LabelState and an earlier
// occurrence's key is recorded, but the re-addition is a later occurrence
// and must dispatch.
func TestRunWebhook_LabelReAddedBeforePoll(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	earlier := recent.Add(-10 * time.Minute)
	mc.setPollState(persistedPollState{
		LabelState: LabelState{5: {"ready-to-code"}},
		DispatchedKeysFull: map[string]int64{
			fmt.Sprintf("code:issue_label-5-ready-to-code-%d", earlier.Unix()): earlier.Unix(),
		},
	})

	err := newWebhookPoller(mc).RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"})))
	if err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].Variables["STAGE"] != "code" {
		t.Fatalf("pipeline calls = %+v, want one code dispatch for the re-addition", mc.pipelineCalls)
	}
}

// reAddFixture seeds issue 5 as: ready-to-code added (event 1, earlier),
// removed (event 2), re-added (event 3, recent) — an occurrence a
// presence-based poll never observed because the label stays in LabelState.
func reAddFixture(mc *mockClient) (earlier time.Time) {
	mc.memberLevel[alice.ID] = 30
	earlier = recent.Add(-20 * time.Minute)
	iss := Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, Author: bob, UpdatedAt: recent}
	mc.issue[5] = &iss
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, earlier),
		labelEvent(2, "remove", "ready-to-code", alice, earlier.Add(time.Minute)),
		labelEvent(3, "add", "ready-to-code", alice, recent),
	}
	return earlier
}

func reAddState(earlier time.Time, watermark time.Time) persistedPollState {
	return persistedPollState{
		LastPollAtFull: watermark.Format(time.RFC3339),
		LabelState:     LabelState{5: {"ready-to-code"}},
		DispatchedKeysFull: map[string]int64{
			"code:issue_label-5-ready-to-code-e1": earlier.Unix(),
		},
	}
}

var reAddPayloadLabels = labelChange(nil, []string{"ready-to-code"})

func eventsPoller(mc *mockClient) *Poller {
	return New(mc, newWebhookPoller(mc).router, "group/project", withTestSecret(Options{Mode: "events", BotUserID: 100}))
}

// TestRunWebhook_DelayedReAddAfterWatermarkAdvance: a poll advanced the
// watermark past a remove/re-add it never observed (the label stayed in
// LabelState). The delayed, still-fresh webhook must dispatch exactly once,
// and neither a replay nor the poll may dispatch it again.
func TestRunWebhook_DelayedReAddAfterWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = []Issue{*mc.issue[5]}
	mc.setPollState(reAddState(earlier, recent.Add(10*time.Minute)))
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)

	for i := 0; i < 2; i++ { // delivery, then a replay
		if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
			t.Fatalf("RunWebhook #%d: %v", i, err)
		}
	}
	if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].Variables["STAGE"] != "code" {
		t.Fatalf("pipeline calls = %+v, want exactly one code dispatch", mc.pipelineCalls)
	}
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want the re-addition's key", state.DispatchedKeysFull)
	}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after poll, want 1", len(mc.pipelineCalls))
	}
}

// TestRunWebhook_PollFirstSharesOccurrenceKey: a poll that discovers an
// addition keys it on the same label event ID the webhook uses, so the
// webhook does not dispatch it again.
func TestRunWebhook_PollFirstSharesOccurrenceKey(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d after poll, want 1", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("DispatchedKeysFull = %v, want the label-event-ID key", state.DispatchedKeysFull)
	}
	err := newWebhookPoller(mc).RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"})))
	if err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after webhook, want 1", len(mc.pipelineCalls))
	}
}

// TestPoll_LegacySecondKeyStillDeduplicates: a key an earlier version
// persisted for a label (Unix seconds, issue updated_at) still suppresses
// the poller's rediscovery of that addition.
func TestPoll_LegacySecondKeyStillDeduplicates(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{
		LastPollAtFull: beforeRecent,
		DispatchedKeysFull: map[string]int64{
			fmt.Sprintf("code:issue_label-5-ready-to-code-%d", recent.Unix()): recent.Unix(),
		},
	})
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0 (legacy key recognised)", len(mc.pipelineCalls))
	}
}

// TestPoll_LegacyFailedCountMigrates: a retry count recorded under the
// legacy second key carries onto the new key and counts against the budget.
func TestPoll_LegacyFailedCountMigrates(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	legacy := fmt.Sprintf("issue_label-5-ready-to-code-%d", recent.Unix())
	mc.setPollState(persistedPollState{
		LastPollAtFull: beforeRecent,
		FailedKeysFull: map[string]int{legacy: maxEventRetries - 1},
	})
	mc.pipelineErr = errors.New("pipeline create failed")
	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want dispatch error")
	}
	state, _ := mc.getPollState()
	if got := state.FailedKeysFull["issue_label-5-ready-to-code-e1"]; got != maxEventRetries {
		t.Errorf("FailedKeysFull = %v, want the migrated count %d on the new key", state.FailedKeysFull, maxEventRetries)
	}
	// The second key stays in step, now as a registered mirror, so an older
	// poller after a rollback still sees the spent budget.
	if got := state.FailedKeysFull[legacy]; got != maxEventRetries {
		t.Errorf("FailedKeysFull = %v, want the legacy key mirroring count %d", state.FailedKeysFull, maxEventRetries)
	}
	if owners := state.LegacyMirrors.owners(mirrorKindFailure, legacy); len(owners) != 1 || owners[0] != "issue_label-5-ready-to-code-e1" {
		t.Errorf("legacy failure key owners = %v, want the ID-keyed occurrence", owners)
	}
}

// TestRunWebhook_FailedReAddHandedToPoller: the webhook dispatch for a
// re-add fails while the earlier occurrence is still in LabelState and the
// watermark is past the issue's updated_at. The occurrence is persisted as
// pending and the poll retries it with no help from discovery; success
// clears exactly that occurrence.
func TestRunWebhook_FailedReAddHandedToPoller(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil // updated_at filtering hides the issue from discovery
	mc.setPollState(reAddState(earlier, recent.Add(10*time.Minute)))
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)

	mc.pipelineErr = errors.New("pipeline create failed")
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook: want dispatch error")
	}
	state, _ := mc.getPollState()
	const key = "issue_label-5-ready-to-code-e3"
	pl, ok := state.PendingLabels[key]
	if !ok || pl.IID != 5 || pl.Label != "ready-to-code" || pl.EventID != 3 {
		t.Fatalf("PendingLabels = %+v, want the re-add occurrence", state.PendingLabels)
	}
	if _, ok := state.DispatchedKeysFull["code:"+key]; ok {
		t.Errorf("failed dispatch recorded as dispatched: %v", state.DispatchedKeysFull)
	}
	failedAttempts := len(mc.pipelineCalls)

	mc.pipelineErr = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != failedAttempts+1 || mc.pipelineCalls[failedAttempts].Variables["STAGE"] != "code" {
		t.Fatalf("pipeline calls = %+v, want the poll to retry the pending occurrence", mc.pipelineCalls)
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want the dispatched occurrence cleared", state.PendingLabels)
	}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("second poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != failedAttempts+1 {
		t.Errorf("pipeline calls = %d, want no further dispatch", len(mc.pipelineCalls))
	}
}

// TestRunWebhook_RetryAfterHandoffClearsOnWebhookSuccess: a webhook re-run
// that succeeds clears the pending occurrence it had handed off.
func TestRunWebhook_RetryAfterHandoffClearsOnWebhookSuccess(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.setPollState(reAddState(earlier, recent.Add(10*time.Minute)))
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)

	mc.pipelineErr = errors.New("pipeline create failed")
	_ = newWebhookPoller(mc).RunWebhook(context.Background(), payload)
	mc.pipelineErr = nil
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	state, _ := mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want cleared", state.PendingLabels)
	}
}

// TestPoll_InFlightPollKeepsNewerHandoff is the poll-then-webhook write
// order: a webhook failure hands an occurrence off after a poll cycle read
// state but before it persisted. The poll's stale snapshot and watermark
// must not erase the handoff.
func TestPoll_InFlightPollKeepsNewerHandoff(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	handoff := PendingLabel{IID: 9, Label: "ready-to-code", EventID: 50, At: recent.UnixMilli()}
	mc.conflictOnce = &persistedPollState{
		LastPollAtFull: beforeRecent,
		PendingLabels:  map[string]PendingLabel{"issue_label-9-ready-to-code-e50": handoff},
	}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	if got, ok := state.PendingLabels["issue_label-9-ready-to-code-e50"]; !ok || got.EventID != 50 {
		t.Errorf("PendingLabels = %+v, want the newer handoff preserved", state.PendingLabels)
	}
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want the poll's own key", state.DispatchedKeysFull)
	}
}

// TestRunWebhook_HandoffAfterConcurrentPoll is the webhook-after-poll write
// order: a poll commits between the webhook's read and its handoff write.
// The handoff lands on top of the poll's document and keeps its keys.
func TestRunWebhook_HandoffAfterConcurrentPoll(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.setPollState(reAddState(earlier, recent.Add(-time.Minute)))
	polled := reAddState(earlier, recent.Add(10*time.Minute))
	polled.DispatchedKeysFull["triage:note-77"] = recent.Unix()
	mc.conflictOnce = &polled

	mc.pipelineErr = errors.New("pipeline create failed")
	if err := newWebhookPoller(mc).RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)); err == nil {
		t.Fatal("RunWebhook: want dispatch error")
	}
	state, _ := mc.getPollState()
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e3"]; !ok {
		t.Errorf("PendingLabels = %+v, want the handoff", state.PendingLabels)
	}
	if _, ok := state.DispatchedKeysFull["triage:note-77"]; !ok {
		t.Errorf("concurrent poll's key dropped: %v", state.DispatchedKeysFull)
	}
	if !strings.HasPrefix(state.LastPollAtFull, recent.Add(10 * time.Minute).Format(time.RFC3339)[:16]) {
		t.Errorf("concurrent poll's watermark rolled back: %q", state.LastPollAtFull)
	}
}

func TestMergePendingLabels(t *testing.T) {
	x := PendingLabel{IID: 5, Label: "l", EventID: 3}
	y := PendingLabel{IID: 5, Label: "l", EventID: 4}
	got := mergePendingLabels(map[string]PendingLabel{"x": x, "y": y}, nil, map[string]bool{"x": true})
	if len(got) != 1 || got["y"].EventID != y.EventID {
		t.Errorf("clear of one occurrence = %+v, want only y left", got)
	}
	got = mergePendingLabels(map[string]PendingLabel{"x": x}, map[string]PendingLabel{"y": y}, map[string]bool{"x": true})
	if len(got) != 1 || got["y"].EventID != y.EventID {
		t.Errorf("add+clear = %+v, want y", got)
	}
	got = mergePendingLabels(map[string]PendingLabel{"x": x}, map[string]PendingLabel{"x": x}, map[string]bool{"x": true})
	if _, ok := got["x"]; !ok {
		t.Errorf("a handoff and a clear of the same key = %+v, the handoff must win", got)
	}
}

func TestAlreadyDispatched(t *testing.T) {
	ev := RoutableEvent{Type: "issue_label", IID: 5, ChangedLabel: "ready-to-code", UpdatedAt: recent.Add(500 * time.Millisecond)}
	idEv := ev
	idEv.LabelEventID = 3
	k := func(suffix string) map[string]int64 {
		return map[string]int64{"code:issue_label-5-ready-to-code-" + suffix: recent.Unix()}
	}
	cases := []struct {
		name string
		ev   RoutableEvent
		keys map[string]int64
		want bool
	}{
		{"exact id key", idEv, k("e3"), true},
		{"other id key", idEv, k("e2"), false},
		{"exact ms key", ev, k(fmt.Sprint(ev.UpdatedAt.UnixMilli())), true},
		{"later ms key", ev, k(fmt.Sprint(ev.UpdatedAt.UnixMilli() + 9000)), true},
		{"earlier ms key", ev, k(fmt.Sprint(ev.UpdatedAt.UnixMilli() - 1)), false},
		{"same-second earlier ms key is a distinct occurrence", ev, k(fmt.Sprint(recent.UnixMilli())), false},
		{"legacy second key same second", ev, k(fmt.Sprint(recent.Unix())), true},
		{"legacy second key later", idEv, k(fmt.Sprint(recent.Unix() + 9)), true},
		{"legacy second key earlier", idEv, k(fmt.Sprint(recent.Unix() - 9)), false},
		{"seconds are not read as milliseconds", idEv, k(fmt.Sprint(recent.Unix() - 1)), false},
		{"other stage", ev, map[string]int64{"review:issue_label-5-ready-to-code-e3": 1}, false},
		{"other issue", idEv, map[string]int64{"code:issue_label-6-ready-to-code-e3": 1}, false},
		{"longer label name", idEv, map[string]int64{"code:issue_label-5-ready-to-code-x-" + fmt.Sprint(recent.Unix()+9): 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := alreadyDispatched(tc.keys, nil, "code", tc.ev); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDispatchedKeysRetainedForFreshnessWindow: pruning behind the
// watermark keeps a key for the webhook freshness window plus skew, so a
// replay inside that window finds its evidence, and drops older keys.
func TestDispatchedKeysRetainedForFreshnessWindow(t *testing.T) {
	wm := recent.Add(10 * time.Minute)
	keys := map[string]int64{
		"keep-recent":   recent.Unix(),
		"keep-at-limit": wm.Add(-dispatchedKeyRetention).Unix(),
		"drop-old":      wm.Add(-dispatchedKeyRetention).Unix() - 1,
	}
	got := pruneDispatchedKeys(keys, wm, nil)
	if _, ok := got["keep-recent"]; !ok {
		t.Error("recent key pruned")
	}
	if _, ok := got["keep-at-limit"]; !ok {
		t.Error("key at the retention limit pruned")
	}
	if _, ok := got["drop-old"]; ok {
		t.Error("key past the retention window kept")
	}
	if dispatchedKeyRetention < webhookMaxEventAge+webhookMaxClockSkew {
		t.Errorf("retention %v shorter than the webhook window", dispatchedKeyRetention)
	}
}

// TestRunWebhook_ReplayAfterWatermarkAdvanceSkipped replays a dispatched
// note after a poll advanced the watermark 10 minutes: the key is retained
// within the freshness window, so the replay creates no second pipeline.
func TestRunWebhook_ReplayAfterWatermarkAdvanceSkipped(t *testing.T) {
	mc := newMockClient()
	webhookSlashNoteFixture(mc)
	mc.setSlashState(persistedPollState{LastPollAtFast: beforeRecent})
	payload := notePayload(t, "Issue", alice.ID, 12, 4)
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	p := New(mc, newWebhookPoller(mc).router, "group/project", withTestSecret(Options{Mode: "slash", BotUserID: 100}))
	p.slashCommandsOnly = true // set by Run in production
	wm := recent.Add(10 * time.Minute)
	if err := p.persistCycleState(context.Background(), "group", "project", map[string]int64{}, &wm, nil, nil); err != nil {
		t.Fatalf("persistCycleState: %v", err)
	}
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("replayed RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after watermark advance and replay, want 1", len(mc.pipelineCalls))
	}
}

// TestRunWebhook_EntityDedup checks that a label fan-out routing two
// labels to the same stage dispatches that stage once, as Run does.
func TestRunWebhook_EntityDedup(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code", "urgent"}, UpdatedAt: recent}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent),
		labelEvent(2, "add", "urgent", alice, recent),
	}
	p := newWebhookPoller(mc)
	p.router = &stubRouter{stages: []string{"triage"}}

	err := p.RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code", "urgent"})))
	if err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Errorf("LabelState[5] = %v, want only the routable label handed off", got)
	}
	// The coalesced label records its own key, so a replay of the payload
	// dispatches nothing.
	for _, k := range []string{"triage:issue_label-5-ready-to-code-e1", "triage:issue_label-5-urgent-e2"} {
		if _, ok := state.DispatchedKeysFull[k]; !ok {
			t.Errorf("DispatchedKeysFull = %v, want %s", state.DispatchedKeysFull, k)
		}
	}
	if err := p.RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code", "urgent"}))); err != nil {
		t.Fatalf("replayed RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after replay, want 1", len(mc.pipelineCalls))
	}
}

func TestRunWebhook_FailClosed(t *testing.T) {
	t.Run("invalid payload", func(t *testing.T) {
		mc := newMockClient()
		webhookSlashNoteFixture(mc)
		err := newWebhookPoller(mc).RunWebhook(context.Background(), notePayload(t, "Issue", bob.ID, 12, 4))
		if err == nil || !strings.Contains(err.Error(), "build webhook events") {
			t.Fatalf("err = %v, want build error", err)
		}
		if len(mc.pipelineCalls) != 0 || mc.forceCommits != 0 {
			t.Errorf("pipelines = %d, commits = %d, want none", len(mc.pipelineCalls), mc.forceCommits)
		}
	})
	t.Run("stale payload", func(t *testing.T) {
		mc := newMockClient()
		webhookSlashNoteFixture(mc)
		mc.notes[4][0].CreatedAt = webhookTestNow.Add(-2 * time.Hour)
		err := newWebhookPoller(mc).RunWebhook(context.Background(), notePayload(t, "Issue", alice.ID, 12, 4))
		if err == nil || !strings.Contains(err.Error(), "older than") {
			t.Fatalf("err = %v, want freshness error", err)
		}
		if len(mc.pipelineCalls) != 0 {
			t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
		}
	})
	t.Run("no dispatch secret", func(t *testing.T) {
		mc := newMockClient()
		webhookSlashNoteFixture(mc)
		p := newWebhookPoller(mc)
		p.opts.DispatchSecret = ""
		err := p.RunWebhook(context.Background(), notePayload(t, "Issue", alice.ID, 12, 4))
		if !errors.Is(err, errDispatchSecretUnset) {
			t.Fatalf("err = %v, want errDispatchSecretUnset", err)
		}
		if len(mc.pipelineCalls) != 0 {
			t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
		}
	})
	t.Run("dispatch failure records no key", func(t *testing.T) {
		mc := newMockClient()
		webhookSlashNoteFixture(mc)
		mc.pipelineErr = errors.New("boom")
		err := newWebhookPoller(mc).RunWebhook(context.Background(), notePayload(t, "Issue", alice.ID, 12, 4))
		if err == nil || !strings.Contains(err.Error(), "dispatch triage for note-12 failed") {
			t.Fatalf("err = %v, want dispatch failure", err)
		}
		if mc.forceCommits != 0 || len(mc.emojis) != 0 {
			t.Errorf("commits = %d, emojis = %d, want none after a failed dispatch", mc.forceCommits, len(mc.emojis))
		}
	})
	t.Run("persist failure", func(t *testing.T) {
		mc := newMockClient()
		webhookSlashNoteFixture(mc)
		mc.forceCommitErr = errors.New("write denied")
		err := newWebhookPoller(mc).RunWebhook(context.Background(), notePayload(t, "Issue", alice.ID, 12, 4))
		if err == nil || !strings.Contains(err.Error(), "persist poll state on "+PollStateBranchSlash) {
			t.Fatalf("err = %v, want persist error", err)
		}
	})
}

// TestRunWebhook_CASMergesConcurrentPollWrite checks that a poll cycle
// committing between the webhook's read and write keeps its keys and
// labels: the webhook reloads and unions rather than overwriting.
func TestRunWebhook_CASMergesConcurrentPollWrite(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	mc.conflictOnce = &persistedPollState{
		LastPollAtFull:     beforeRecent,
		DispatchedKeysFull: map[string]int64{"triage:note-77": recent.Unix()},
		LabelState:         LabelState{5: {"ready-for-review"}},
	}

	err := newWebhookPoller(mc).RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"})))
	if err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["triage:note-77"]; !ok {
		t.Errorf("concurrent writer's key dropped: %v", state.DispatchedKeysFull)
	}
	// The concurrent writer's key, the webhook's occurrence key, and the
	// Unix-second mirror of the latter that older pollers read.
	if len(state.DispatchedKeysFull) != 3 {
		t.Errorf("DispatchedKeysFull = %v, want both writers' keys plus the legacy mirror", state.DispatchedKeysFull)
	}
	got := state.LabelState[5]
	if len(got) != 2 || got[0] != "ready-for-review" || got[1] != "ready-to-code" {
		t.Errorf("LabelState[5] = %v, want union of both writers", got)
	}
}

func TestUnionLabelState(t *testing.T) {
	got := unionLabelState(
		LabelState{1: {"a"}, 2: {"b"}},
		LabelState{1: {"a", "c"}, 3: {"d"}},
	)
	want := LabelState{1: {"a", "c"}, 2: {"b"}, 3: {"d"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("unionLabelState = %v, want %v", got, want)
	}
}

func TestIsSlashCommandEvent(t *testing.T) {
	cases := []struct {
		ev   RoutableEvent
		want bool
	}{
		{RoutableEvent{NoteID: 1, NoteBody: "  /fs-code"}, true},
		{RoutableEvent{NoteID: 1, NoteBody: "looks good"}, false},
		{RoutableEvent{NoteBody: "/fs-code"}, false},
	}
	for _, c := range cases {
		if got := isSlashCommandEvent(c.ev); got != c.want {
			t.Errorf("isSlashCommandEvent(%+v) = %v, want %v", c.ev, got, c.want)
		}
	}
}

func TestReadWebhookPayloadFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(good, []byte(`{"object_kind":"note"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadWebhookPayloadFile(good)
	if err != nil || string(got) != `{"object_kind":"note"}` {
		t.Fatalf("ReadWebhookPayloadFile = %q, %v", got, err)
	}

	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, maxWebhookPayloadBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	failures := []struct {
		name, path, wantErr string
	}{
		{"empty path", "", "path is empty"},
		{"missing", filepath.Join(dir, "missing.json"), "stat webhook payload"},
		{"symlink", link, "not a regular file"},
		{"directory", dir, "not a regular file"},
		{"oversized", big, "exceeds"},
	}
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			_, err := ReadWebhookPayloadFile(f.path)
			if err == nil || !strings.Contains(err.Error(), f.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, f.wantErr)
			}
		})
	}
}
