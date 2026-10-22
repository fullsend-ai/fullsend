package poll

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// legacyPersistedPollState is the state.json document as read by a poller
// from before pending_labels existed: unknown fields are dropped on decode
// and the HMAC is recomputed over exactly these fields.
type legacyPersistedPollState struct {
	LastPollAtFast     string           `json:"last_poll_at_fast,omitempty"`
	LastPollAtFull     string           `json:"last_poll_at_full,omitempty"`
	LabelState         LabelState       `json:"label_state,omitempty"`
	DispatchedKeysFast map[string]int64 `json:"dispatched_keys_fast,omitempty"`
	DispatchedKeysFull map[string]int64 `json:"dispatched_keys_full,omitempty"`
	FailedKeysFast     map[string]int   `json:"failed_keys_fast,omitempty"`
	FailedKeysFull     map[string]int   `json:"failed_keys_full,omitempty"`
	HMAC               string           `json:"hmac,omitempty"`
}

// TestPollState_OlderReaderVerifiesPendingLabelDocument: a document that
// carries a pending handoff must still verify for a reader that does not know
// about it, or that reader would delete the whole state branch as tampered.
func TestPollState_OlderReaderVerifiesPendingLabelDocument(t *testing.T) {
	domain := hmacDomainFor(PollStateBranchEvents, "group/project")
	state := persistedPollState{
		LastPollAtFull:     "2026-01-01T00:00:00Z",
		LabelState:         LabelState{5: {"ready-to-code"}},
		DispatchedKeysFull: map[string]int64{"code:issue_label-5-ready-to-code-e1": 100},
		PendingLabels: map[string]PendingLabel{
			"issue_label-5-ready-to-code-e3": {IID: 5, Label: "ready-to-code", EventID: 3, At: 1, ActorID: 42, ActorLogin: "alice"},
		},
	}
	signed, err := signPollState(testDispatchSecret, domain, state)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}

	var old legacyPersistedPollState
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	got := old.HMAC
	old.HMAC = ""
	canonical, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(testDispatchSecret))
	mac.Write([]byte(domain))
	mac.Write(canonical)
	if want := hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Errorf("older reader would compute HMAC %s, document carries %s", want, got)
	}

	// And this version still accepts its own document.
	if ok, err := verifyPollState(testDispatchSecret, domain, signed); err != nil || !ok {
		t.Errorf("verifyPollState = %v, %v; want valid", ok, err)
	}
}

func TestVerifyPollState_PendingLabelsAreSigned(t *testing.T) {
	domain := hmacDomainFor(PollStateBranchEvents, "group/project")
	base := persistedPollState{
		LastPollAtFull: "2026-01-01T00:00:00Z",
		PendingLabels:  map[string]PendingLabel{"k": {IID: 5, Label: "l", EventID: 3, ActorID: 42, ActorLogin: "alice"}},
	}
	signed, err := signPollState(testDispatchSecret, domain, base)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(t *testing.T) persistedPollState {
		t.Helper()
		var s persistedPollState
		if err := json.Unmarshal(wire, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if ok, err := verifyPollState(testDispatchSecret, domain, decode(t)); err != nil || !ok {
		t.Fatalf("unmodified document: verifyPollState = %v, %v; want valid", ok, err)
	}

	// Editing, adding, or removing the handoff on the wire invalidates the
	// document signature; removal must not pass as an empty handoff.
	tests := map[string]func(s *persistedPollState){
		"edited actor": func(s *persistedPollState) {
			enc, err := encodePendingLabels(persistedPollState{PendingLabels: map[string]PendingLabel{
				"k": {IID: 5, Label: "l", EventID: 3, ActorID: 43, ActorLogin: "bob"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			s.FailedKeysFull = enc.FailedKeysFull
		},
		"added occurrence": func(s *persistedPollState) {
			enc, err := encodePendingLabels(persistedPollState{PendingLabels: map[string]PendingLabel{"x": {IID: 6, Label: "l"}}})
			if err != nil {
				t.Fatal(err)
			}
			for k, c := range enc.FailedKeysFull {
				s.FailedKeysFull[k] = c
			}
		},
		"removed handoff": func(s *persistedPollState) {
			s.FailedKeysFull = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := decode(t)
			mutate(&s)
			if ok, err := verifyPollState(testDispatchSecret, domain, s); err != nil || ok {
				t.Errorf("verifyPollState = %v, %v; want rejected", ok, err)
			}
		})
	}
}

// TestPollState_OlderWriterPreservesPendingLabels: a poller from before the
// pending handoff existed reads the document, drops fields it does not know,
// and re-signs what it saved. The handoff must still be retryable by a
// current poller afterwards (a remove/re-add already behind LabelState and
// the watermark cannot be rediscovered, so losing it loses the occurrence).
func TestPollState_OlderWriterPreservesPendingLabels(t *testing.T) {
	mc := newMockClient()
	domain := hmacDomainFor(PollStateBranchEvents, "group/project")
	signed, err := signPollState(testDispatchSecret, domain, persistedPollState{
		LastPollAtFull: "2026-01-01T00:00:00Z",
		LabelState:     LabelState{5: {"ready-to-code"}},
		PendingLabels: map[string]PendingLabel{
			"issue_label-5-ready-to-code-e3": {IID: 5, Label: "ready-to-code", EventID: 3, At: 1, ActorID: 42, ActorLogin: "alice"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}

	var old legacyPersistedPollState
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	// The older poller verifies, prunes failed keys by its own rule, and
	// re-signs the fields it knows.
	got := old.HMAC
	old.HMAC = ""
	canonical, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(testDispatchSecret))
	mac.Write([]byte(domain))
	mac.Write(canonical)
	if want := hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("older reader would compute HMAC %s, document carries %s", want, got)
	}
	old.FailedKeysFull = pruneFailedKeys(old.FailedKeysFull)
	canonical, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	mac = hmac.New(sha256.New, []byte(testDispatchSecret))
	mac.Write([]byte(domain))
	mac.Write(canonical)
	old.HMAC = hex.EncodeToString(mac.Sum(nil))
	rewritten, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	mc.putBranchFile(PollStateBranchEvents, PollStateFileName, rewritten)

	pending, err := eventsPoller(mc).readPendingLabels(context.Background(), "group", "project")
	if err != nil {
		t.Fatalf("readPendingLabels after older writer: %v", err)
	}
	if pl, ok := pending["issue_label-5-ready-to-code-e3"]; !ok || pl.ActorID != 42 {
		t.Errorf("pending = %+v, want the handoff to survive an older writer", pending)
	}
}

// TestPoll_PendingRetryKeepsOriginalActor: after another user removes and
// re-adds the label, the older pending occurrence is still routed and
// dispatched under the actor bound to it, not under the newest adder.
func TestPoll_PendingRetryKeepsOriginalActor(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.memberLevel[bob.ID] = 10 // bob lacks the access alice has
	mc.labelEvents[5] = append(mc.labelEvents[5],
		labelEvent(4, "remove", "ready-to-code", bob, recent.Add(time.Minute)),
		labelEvent(5, "add", "ready-to-code", bob, recent.Add(2*time.Minute)))
	mc.issues = nil
	state := reAddState(earlier, recent.Add(10*time.Minute))
	state.PendingLabels = map[string]PendingLabel{
		"issue_label-5-ready-to-code-e3": {
			IID: 5, Label: "ready-to-code", EventID: 3, At: recent.UnixMilli(),
			Labels: []string{"ready-to-code"}, ActorID: alice.ID, ActorLogin: alice.Username,
		},
	}
	mc.setPollState(state)

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	if got := mc.pipelineCalls[0].Variables["ACTOR_ID"]; got != strconv.Itoa(alice.ID) {
		t.Errorf("ACTOR_ID = %q, want the original actor %d", got, alice.ID)
	}
}

// TestPoll_PendingRetryRechecksOriginalActorAccess: the retry checks the
// original actor's current access, so revoking it is honoured even though a
// later user (with access) re-added the label.
func TestPoll_PendingRetryRechecksOriginalActorAccess(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.memberLevel[alice.ID] = 0 // revoked
	mc.memberLevel[bob.ID] = 30
	mc.labelEvents[5] = append(mc.labelEvents[5],
		labelEvent(4, "remove", "ready-to-code", bob, recent.Add(time.Minute)),
		labelEvent(5, "add", "ready-to-code", bob, recent.Add(2*time.Minute)))
	mc.issues = nil
	state := reAddState(earlier, recent.Add(10*time.Minute))
	state.PendingLabels = map[string]PendingLabel{
		"issue_label-5-ready-to-code-e3": {
			IID: 5, Label: "ready-to-code", EventID: 3, At: recent.UnixMilli(),
			Labels: []string{"ready-to-code"}, ActorID: alice.ID, ActorLogin: alice.Username,
		},
	}
	mc.setPollState(state)

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0: the revoked actor must not be authorized by a later user's access", len(mc.pipelineCalls))
	}
	// Clearing the revoked occurrence must not lose the authorized
	// replacement: reconciliation queued it under its own actor, and a later
	// cycle dispatches it although LabelState still hides the label.
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("second poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d after the later cycle, want the replacement dispatched", len(mc.pipelineCalls))
	}
	if got := mc.pipelineCalls[0].Variables["ACTOR_ID"]; got != strconv.Itoa(bob.ID) {
		t.Errorf("ACTOR_ID = %q, want the replacement's own actor %d", got, bob.ID)
	}
}

// TestPoll_PendingRetrySurvivesTransientMembershipFailure: a failed (not
// "not a member") membership lookup must not resolve the pending handoff. The
// occurrence stays queued and dispatches once the lookup recovers.
func TestPoll_PendingRetrySurvivesTransientMembershipFailure(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	state := reAddState(earlier, recent.Add(10*time.Minute))
	const key = "issue_label-5-ready-to-code-e3"
	state.PendingLabels = map[string]PendingLabel{
		key: {
			IID: 5, Label: "ready-to-code", EventID: 3, At: recent.UnixMilli(),
			Labels: []string{"ready-to-code"}, ActorID: alice.ID, ActorLogin: alice.Username,
		},
	}
	mc.setPollState(state)
	mc.memberErr[alice.ID] = fmt.Errorf("gitlab api: 503 unavailable")

	// The failing cycle may report an error; it must not dispatch or clear.
	_ = eventsPoller(mc).Run(context.Background())
	if len(mc.pipelineCalls) != 0 {
		t.Fatalf("pipeline calls = %d, want 0 while the lookup fails", len(mc.pipelineCalls))
	}
	after, _ := mc.getPollState()
	if _, kept := after.PendingLabels[key]; !kept {
		t.Fatalf("PendingLabels = %+v, want the occurrence retained after a failed lookup", after.PendingLabels)
	}

	delete(mc.memberErr, alice.ID)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run after recovery: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1 after the lookup recovers", len(mc.pipelineCalls))
	}
	after, _ = mc.getPollState()
	if len(after.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want cleared after dispatch", after.PendingLabels)
	}
}

// TestPoll_CoalescedPendingOccurrencesRecordKeys: several pending additions
// for one issue coalesce into one pipeline, every occurrence records its own
// dispatch key, and a webhook replay of a coalesced occurrence creates no
// further pipeline.
func TestPoll_CoalescedPendingOccurrencesRecordKeys(t *testing.T) {
	mc := newMockClient()
	// The poll prunes dispatched keys behind its (wall-clock) watermark, so
	// the occurrences must be recent in real time.
	fresh := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	mc.memberLevel[alice.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code", "urgent"}, UpdatedAt: fresh}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, fresh),
		labelEvent(2, "add", "urgent", alice, fresh),
	}
	pending := func(id int, label string) PendingLabel {
		return PendingLabel{
			IID: 5, Label: label, EventID: id, At: fresh.UnixMilli(),
			Labels: []string{"ready-to-code", "urgent"}, ActorID: alice.ID, ActorLogin: alice.Username,
		}
	}
	mc.setPollState(persistedPollState{
		LastPollAtFull: fresh.Add(-10 * time.Minute).Format(time.RFC3339),
		PendingLabels: map[string]PendingLabel{
			"issue_label-5-ready-to-code-e1": pending(1, "ready-to-code"),
			"issue_label-5-urgent-e2":        pending(2, "urgent"),
		},
	})
	router := &stubRouter{stages: []string{"triage"}}
	poller := New(mc, router, "group/project", withTestSecret(Options{Mode: "events", BotUserID: 100}))
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1 coalesced dispatch", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	for _, k := range []string{"triage:issue_label-5-ready-to-code-e1", "triage:issue_label-5-urgent-e2"} {
		if _, ok := state.DispatchedKeysFull[k]; !ok {
			t.Errorf("DispatchedKeysFull = %v, want %s", state.DispatchedKeysFull, k)
		}
	}
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want both resolved occurrences cleared", state.PendingLabels)
	}

	wh := newWebhookPoller(mc)
	wh.router = router
	wh.now = func() time.Time { return fresh.Add(time.Minute) }
	if err := wh.RunWebhook(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code", "urgent"}))); err != nil {
		t.Fatalf("webhook replay: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d after webhook replay, want 1", len(mc.pipelineCalls))
	}
}

// TestFailedCount_FindsMillisecondFallbackKey: a retry count recorded under
// the millisecond key (label event ID lookup failed) is still found once the
// ID resolves, and is retired onto the ID key.
func TestFailedCount_FindsMillisecondFallbackKey(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC)
	noID := RoutableEvent{Type: "issue_label", IID: 5, ChangedLabel: "ready-to-code", UpdatedAt: at}
	withID := noID
	withID.LabelEventID = 9

	failed := map[string]int{noID.Key(): 2}
	if got := failedCount(failed, nil, withID); got != 2 {
		t.Fatalf("failedCount with resolved ID = %d, want the fallback key's 2", got)
	}
	failed[withID.Key()] = failedCount(failed, nil, withID) + 1
	retireFallbackFailureKey(failed, withID)
	if failed[withID.Key()] != 3 || failed[noID.Key()] != 0 {
		t.Errorf("failed = %v, want count 3 on the ID key and the fallback key tombstoned", failed)
	}
	if got := failedCount(failed, nil, withID); got != 3 {
		t.Errorf("failedCount = %d, want 3", got)
	}
	// A different occurrence time is a distinct addition and does not share the count.
	other := withID
	other.UpdatedAt = at.Add(time.Second)
	other.LabelEventID = 10
	if got := failedCount(map[string]int{noID.Key(): 2}, nil, other); got != 0 {
		t.Errorf("failedCount for a distinct addition = %d, want 0", got)
	}
}

// TestPoll_PendingOnlyPartialFailureRecordsDispatchedKeys: a cycle that
// retries only pending occurrences never advances the watermark. When one of
// them fails, the other's success must still be recorded before it leaves the
// retry queue, or a fresh webhook replay would dispatch it again.
func TestPoll_PendingOnlyPartialFailureRecordsDispatchedKeys(t *testing.T) {
	mc := newMockClient()
	fresh := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	mc.memberLevel[alice.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, UpdatedAt: fresh}
	mc.issue[6] = &Issue{IID: 6, State: "opened", Labels: []string{"ready-to-code"}, UpdatedAt: fresh}
	pending := func(iid, id int) PendingLabel {
		return PendingLabel{
			IID: iid, Label: "ready-to-code", EventID: id, At: fresh.UnixMilli(),
			Labels: []string{"ready-to-code"}, ActorID: alice.ID, ActorLogin: alice.Username,
		}
	}
	ok, failing := "issue_label-5-ready-to-code-e1", "issue_label-6-ready-to-code-e2"
	mc.setPollState(persistedPollState{
		LastPollAtFull: fresh.Add(-10 * time.Minute).Format(time.RFC3339),
		PendingLabels:  map[string]PendingLabel{ok: pending(5, 1), failing: pending(6, 2)},
	})
	mc.pipelineErr = errors.New("pipeline create failed")
	mc.pipelineErrAfter = 1 // the first dispatch succeeds, the second fails

	router := &stubRouter{stages: []string{"triage"}}
	poller := New(mc, router, "group/project", withTestSecret(Options{Mode: "events", BotUserID: 100}))
	if err := poller.Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed retry reported")
	}

	state, _ := mc.getPollState()
	if _, recorded := state.DispatchedKeysFull["triage:"+ok]; !recorded {
		t.Errorf("DispatchedKeysFull = %v, want the successful occurrence's key", state.DispatchedKeysFull)
	}
	if _, kept := state.PendingLabels[ok]; kept {
		t.Errorf("PendingLabels = %+v, want the dispatched occurrence cleared", state.PendingLabels)
	}
	if _, kept := state.PendingLabels[failing]; !kept {
		t.Errorf("PendingLabels = %+v, want the failed occurrence kept for retry", state.PendingLabels)
	}
}

// TestPoll_DiscoveredLabelBindsActorOfSelectedOccurrence: discovery selects
// an add event and its actor from one snapshot. If the label is removed and
// re-added by someone else before normalization fetches label events again,
// the discovered occurrence must still be attributed to its own actor.
func TestPoll_DiscoveredLabelBindsActorOfSelectedOccurrence(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.memberLevel[bob.ID] = 10
	at := recent.Add(-time.Minute)
	iss := Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, Author: bob, UpdatedAt: at}
	mc.issues = []Issue{iss}
	mc.issue[5] = &iss
	mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(3, "add", "ready-to-code", alice, at)}

	p := eventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].LabelEventID != 3 || events[0].NoteAuthorID != alice.ID {
		t.Fatalf("events = %+v, want one label event bound to occurrence 3 and its actor", events)
	}

	// The label changes hands between discovery and normalization.
	mc.labelEvents[5] = append(mc.labelEvents[5],
		labelEvent(4, "remove", "ready-to-code", alice, at.Add(time.Second)),
		labelEvent(5, "add", "ready-to-code", bob, at.Add(2*time.Second)))

	ne, actorID, err := p.toNormalizedEvent(context.Background(), events[0])
	if err != nil {
		t.Fatal(err)
	}
	if actorID != alice.ID || ne.Actor.ID != alice.Username {
		t.Errorf("actor = %d/%q, want the original occurrence's actor %d/%q", actorID, ne.Actor.ID, alice.ID, alice.Username)
	}

	// With no actor bound (the snapshot lookup failed), resolution is pinned
	// to the exact event ID rather than the latest addition.
	unbound := events[0]
	unbound.NoteAuthorID, unbound.NoteAuthorLogin, unbound.IsBot = 0, "", false
	if _, id, err := p.toNormalizedEvent(context.Background(), unbound); err != nil || id != alice.ID {
		t.Errorf("unbound actor resolution = %d, %v; want exact-ID actor %d", id, err, alice.ID)
	}
}

// TestPollState_OlderWriterPrunedDispatchKeysRestored: a poller from before
// the webhook freshness retention prunes dispatched_keys_* at its watermark
// and re-signs the document. The replay evidence must survive that rewrite on
// both state branches, so a current reader still sees every dispatched key
// the webhook driver needs to suppress a still-fresh replay.
func TestPollState_OlderWriterPrunedDispatchKeysRestored(t *testing.T) {
	for _, branch := range []string{PollStateBranchEvents, PollStateBranchSlash} {
		t.Run(branch, func(t *testing.T) {
			mc := newMockClient()
			domain := hmacDomainFor(branch, "group/project")
			const key = "triage:issue_label-5-ready-to-code-e3"
			doc := persistedPollState{FailedKeysFull: map[string]int{"issue_note-9": 1}}
			mode := "events"
			if branch == PollStateBranchSlash {
				mode = "slash"
				doc = persistedPollState{
					LastPollAtFast:     "2026-01-01T00:00:00Z",
					DispatchedKeysFast: map[string]int64{key: 1700000000},
				}
			} else {
				doc.LastPollAtFull = "2026-01-01T00:00:00Z"
				doc.DispatchedKeysFull = map[string]int64{key: 1700000000}
			}
			signed, err := signPollState(testDispatchSecret, domain, doc)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(signed)
			if err != nil {
				t.Fatal(err)
			}

			var old legacyPersistedPollState
			if err := json.Unmarshal(data, &old); err != nil {
				t.Fatal(err)
			}
			// The older writer prunes dispatched keys at its watermark
			// (everything here is behind it) and failed keys by retry
			// budget, then re-signs what it knows.
			old.DispatchedKeysFast, old.DispatchedKeysFull = nil, nil
			old.FailedKeysFast = pruneFailedKeys(old.FailedKeysFast)
			old.FailedKeysFull = pruneFailedKeys(old.FailedKeysFull)
			old.HMAC = ""
			canonical, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			mac := hmac.New(sha256.New, []byte(testDispatchSecret))
			mac.Write([]byte(domain))
			mac.Write(canonical)
			old.HMAC = hex.EncodeToString(mac.Sum(nil))
			rewritten, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			mc.putBranchFile(branch, PollStateFileName, rewritten)

			p := New(mc, nil, "group/project", withTestSecret(Options{Mode: mode, BotUserID: 100}))
			p.slashCommandsOnly = mode == "slash" // normally set by Run
			got, err := p.loadPollState(context.Background(), "group", "project")
			if err != nil {
				t.Fatalf("loadPollState after older writer: %v", err)
			}
			keys := got.DispatchedKeysFull
			if branch == PollStateBranchSlash {
				keys = got.DispatchedKeysFast
			}
			if keys[key] != 1700000000 {
				t.Errorf("dispatched keys = %v, want %s restored", keys, key)
			}
			for k := range got.FailedKeysFast {
				if strings.HasPrefix(k, replayKeyPrefix) {
					t.Errorf("replay mirror %q leaked into FailedKeysFast", k)
				}
			}
			for k := range got.FailedKeysFull {
				if strings.HasPrefix(k, replayKeyPrefix) {
					t.Errorf("replay mirror %q leaked into FailedKeysFull", k)
				}
			}
			if branch == PollStateBranchEvents && got.FailedKeysFull["issue_note-9"] != 1 {
				t.Errorf("FailedKeysFull = %v, want the real failure count kept", got.FailedKeysFull)
			}
		})
	}
}
