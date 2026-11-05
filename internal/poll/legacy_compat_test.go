package poll

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Mixed-version tests for label keys (see legacy_mirror.go). The "old poller"
// below is modelled by what a pre-driver poller does with state.json: it keys
// a label addition on the Unix second, reads only that key, and re-signs the
// whole document (unknown failed-key entries included) when it writes.

const compatLabel = "ready-to-code"

func compatEvent(id int, at time.Time) RoutableEvent {
	return RoutableEvent{Type: "issue_label", IID: 5, ChangedLabel: compatLabel, UpdatedAt: at, LabelEventID: id}
}

// oldPollerSawDispatch is an older poller's dedup lookup: the exact
// Unix-second key, nothing else.
func oldPollerSawDispatch(dispatched map[string]int64, stage string, ev RoutableEvent) bool {
	_, ok := dispatched[stage+":"+ev.LegacyLabelKey()]
	return ok
}

// oldPollerRewrite emulates an older poller committing state.json: it parses
// the wire document without decoding any entry it does not know, applies
// mutate to the keys it understands, and re-signs it.
func oldPollerRewrite(t *testing.T, mc *mockClient, mutate func(*persistedPollState)) {
	t.Helper()
	data, err := mc.GetFileContentAtRef(context.Background(), "group", "project", PollStateFileName, PollStateBranchEvents)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var doc persistedPollState
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	mutate(&doc)
	doc.HMAC = ""
	sig, err := computeStateHMAC(testDispatchSecret, hmacDomainFor(PollStateBranchEvents, "group/project"), doc)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	doc.HMAC = sig
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mc.putBranchFile(PollStateBranchEvents, PollStateFileName, out)
}

func compatState(t *testing.T, mc *mockClient) persistedPollState {
	t.Helper()
	state, ok := mc.getPollState()
	if !ok {
		t.Fatal("no poll state")
	}
	return state
}

// Old reader after new writer: a dispatch the webhook records is visible to an
// older poller under the Unix-second key, and a failed dispatch's retry count
// is too, so a rollback neither redispatches nor resets the budget.
func TestCompat_OldReaderAfterNewWriter(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	payload := issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{compatLabel}))
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	state := compatState(t, mc)
	if !oldPollerSawDispatch(state.DispatchedKeysFull, "code", compatEvent(1, recent)) {
		t.Fatalf("DispatchedKeysFull = %v, want the Unix-second mirror an older poller reads", state.DispatchedKeysFull)
	}
	// The older poller therefore cannot treat the addition as undispatched.
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("DispatchedKeysFull = %v, want the occurrence key too", state.DispatchedKeysFull)
	}

	// Retry count: a failing dispatch leaves its count under the second key.
	mc2 := newMockClient()
	webhookLabelAddFixture(mc2)
	mc2.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	mc2.pipelineErr = errors.New("pipeline create failed")
	if err := eventsPoller(mc2).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want dispatch error")
	}
	st2 := compatState(t, mc2)
	legacy := compatEvent(1, recent).LegacyLabelKey()
	if got := st2.FailedKeysFull[legacy]; got != 1 {
		t.Errorf("FailedKeysFull = %v, want the older poller's count 1 under %s", st2.FailedKeysFull, legacy)
	}
}

// New reader after old writer: second-keyed evidence an older poller wrote
// (no ledger entry) still suppresses redispatch and its retry count carries
// onto the new key and is mirrored back afterwards.
func TestCompat_NewReaderAfterOldWriter(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	legacy := compatEvent(1, recent).LegacyLabelKey()
	mc.setPollState(persistedPollState{
		LastPollAtFull:     beforeRecent,
		DispatchedKeysFull: map[string]int64{"code:" + legacy: recent.Unix()},
	})
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	payload := issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{compatLabel}))
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Fatalf("pipeline calls = %d, want 0: the older poller's key is genuine evidence", len(mc.pipelineCalls))
	}
	if mirrors := compatState(t, mc).LegacyMirrors; len(mirrors) != 0 {
		t.Errorf("LegacyMirrors = %v, want the old poller's key left unregistered", mirrors)
	}
}

// Migration of an existing seconds-based retry count: it carries onto the
// occurrence key, survives as a mirror for rollback, and clears with success.
func TestCompat_MigratesSecondsRetryCountAndClearsOnSuccess(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	legacy := compatEvent(1, recent).LegacyLabelKey()
	mc.setPollState(persistedPollState{
		LastPollAtFull: beforeRecent,
		FailedKeysFull: map[string]int{legacy: 1},
	})
	mc.pipelineErr = errors.New("pipeline create failed")
	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want dispatch error")
	}
	state := compatState(t, mc)
	if state.FailedKeysFull["issue_label-5-ready-to-code-e1"] != 2 || state.FailedKeysFull[legacy] != 2 {
		t.Fatalf("FailedKeysFull = %v, want count 2 on the occurrence key and its second-key mirror", state.FailedKeysFull)
	}
	if owners := state.LegacyMirrors.owners(mirrorKindFailure, legacy); len(owners) != 1 {
		t.Fatalf("owners = %v, want the migrated occurrence", owners)
	}

	mc.pipelineErr = nil
	failedAttempts := len(mc.pipelineCalls) // the mock records failed creates too
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovering poll Run: %v", err)
	}
	state = compatState(t, mc)
	if len(state.FailedKeysFull) != 0 || len(state.LegacyMirrors.owners(mirrorKindFailure, legacy)) != 0 {
		t.Errorf("FailedKeysFull = %v, mirrors = %v, want everything cleared after success", state.FailedKeysFull, state.LegacyMirrors)
	}
	if got := len(mc.pipelineCalls) - failedAttempts; got != 1 {
		t.Errorf("successful pipeline creates = %d, want 1", got)
	}
}

// Same-second distinct occurrences: the second-resolution mirror of the first
// addition must not suppress a remove/re-add handled by the new code in the
// same second, while a replay of either occurrence is still deduplicated.
func TestCompat_SameSecondDistinctOccurrences(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	first := issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{compatLabel}))
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), first); err != nil {
		t.Fatalf("RunWebhook first: %v", err)
	}
	// Remove then re-add inside the same second as the first addition.
	again := recent.Add(500 * time.Millisecond)
	mc.labelEvents[5] = append(mc.labelEvents[5],
		labelEvent(2, "remove", compatLabel, alice, recent.Add(300*time.Millisecond)),
		labelEvent(3, "add", compatLabel, alice, again))
	iss := *mc.issue[5]
	iss.UpdatedAt = again
	mc.issue[5] = &iss
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), first); err != nil {
		t.Fatalf("RunWebhook second: %v", err)
	}
	if len(mc.pipelineCalls) != 2 {
		t.Fatalf("pipeline calls = %d, want 2: the re-add is a distinct occurrence", len(mc.pipelineCalls))
	}
	// Replays create nothing: each occurrence is matched by its own key.
	for i := 0; i < 2; i++ {
		if err := newWebhookPoller(mc).RunWebhook(context.Background(), first); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}
	if len(mc.pipelineCalls) != 2 {
		t.Errorf("pipeline calls = %d after replays, want 2", len(mc.pipelineCalls))
	}
	state := compatState(t, mc)
	legacy := "code:" + compatEvent(1, recent).LegacyLabelKey()
	owners := state.LegacyMirrors.owners(mirrorKindDispatch, legacy)
	if len(owners) != 2 {
		t.Errorf("mirror owners = %v, want both occurrences to own the shared second key", owners)
	}
}

// A mirror never proves a different occurrence dispatched; unit-level view of
// the same rule for the dispatch and failure lookups.
func TestCompat_MirrorOnlyCountsForItsOwner(t *testing.T) {
	first, second := compatEvent(1, recent), compatEvent(3, recent.Add(400*time.Millisecond))
	legacyD := "code:" + first.LegacyLabelKey()
	dispatched := map[string]int64{
		"code:" + first.Key(): recent.Unix(),
		legacyD:               recent.Unix(),
	}
	mirrors := mirrorSet{}
	mirrors.add(mirrorKindDispatch, legacyD, "code:"+first.Key())
	if !alreadyDispatched(dispatched, mirrors, "code", first) {
		t.Error("owner not recognised")
	}
	if alreadyDispatched(dispatched, mirrors, "code", second) {
		t.Error("a mirror of the first occurrence suppressed a same-second distinct occurrence")
	}
	if !alreadyDispatched(dispatched, nil, "code", second) {
		t.Error("an unregistered second key must stay genuine legacy evidence")
	}

	failed := map[string]int{first.Key(): 2, first.LegacyLabelKey(): 2}
	fm := mirrorSet{}
	fm.add(mirrorKindFailure, first.LegacyLabelKey(), first.Key())
	if got := failedCount(failed, fm, second); got != 0 {
		t.Errorf("distinct same-second occurrence inherited count %d", got)
	}
	if got := failedCount(failed, fm, first); got != 2 {
		t.Errorf("owner count = %d, want 2", got)
	}
	if got := failedCount(failed, nil, second); got != 2 {
		t.Errorf("unregistered legacy count = %d, want it carried over (2)", got)
	}
}

// Concurrent writers: an older poller re-signing the document keeps the
// mirror ledger (so the new reader still tells mirrors from legacy keys), and
// its own keys are honoured; a concurrent new writer's ledger merges with ours.
func TestCompat_OldWriterRoundTripKeepsLedger(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	first := issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{compatLabel}))
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), first); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	oldPollerRewrite(t, mc, func(doc *persistedPollState) {
		doc.DispatchedKeysFull["triage:issue_label-9-bug-1"] = recent.Unix()
		doc.LastPollAtFull = recent.Add(time.Minute).Format(time.RFC3339)
	})
	state := compatState(t, mc) // verifies the old writer's HMAC too
	legacy := "code:" + compatEvent(1, recent).LegacyLabelKey()
	if owners := state.LegacyMirrors.owners(mirrorKindDispatch, legacy); len(owners) != 1 {
		t.Fatalf("LegacyMirrors = %v, want the ledger to survive an older writer", state.LegacyMirrors)
	}
	// A later distinct same-second occurrence is still dispatched afterwards.
	again := recent.Add(500 * time.Millisecond)
	mc.labelEvents[5] = append(mc.labelEvents[5],
		labelEvent(2, "remove", compatLabel, alice, recent.Add(300*time.Millisecond)),
		labelEvent(3, "add", compatLabel, alice, again))
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), first); err != nil {
		t.Fatalf("RunWebhook after old writer: %v", err)
	}
	if len(mc.pipelineCalls) != 2 {
		t.Errorf("pipeline calls = %d, want 2", len(mc.pipelineCalls))
	}

	// Concurrent new writer: its ledger entries merge with ours under CAS.
	mc2 := newMockClient()
	webhookLabelAddFixture(mc2)
	other := compatEvent(9, recent)
	other.IID = 6
	otherLegacy := "code:" + other.LegacyLabelKey()
	conflict := persistedPollState{
		LastPollAtFull: beforeRecent,
		DispatchedKeysFull: map[string]int64{
			"code:" + other.Key(): recent.Unix(),
			otherLegacy:           recent.Unix(),
		},
		LegacyMirrors: mirrorSet{},
	}
	conflict.LegacyMirrors.add(mirrorKindDispatch, otherLegacy, "code:"+other.Key())
	mc2.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	mc2.conflictOnce = &conflict
	if err := newWebhookPoller(mc2).RunWebhook(context.Background(), first); err != nil {
		t.Fatalf("RunWebhook with concurrent writer: %v", err)
	}
	merged := compatState(t, mc2)
	if len(merged.LegacyMirrors) != 2 {
		t.Errorf("LegacyMirrors = %v, want both writers' mirrors", merged.LegacyMirrors)
	}
}

// A mirror registration that lands on a second key an older poller already
// wrote for a genuine dispatch is not registered: that key stays proof.
func TestCompat_MergeLeavesGenuineLegacyKeyUnregistered(t *testing.T) {
	ev := compatEvent(1, recent)
	legacy := "code:" + ev.LegacyLabelKey()
	state := persistedPollState{DispatchedKeysFull: map[string]int64{legacy: recent.Unix()}}
	add := mirrorSet{}
	add.add(mirrorKindDispatch, legacy, "code:"+ev.Key())
	if got := mergeLegacyMirrors(state, add); len(got) != 0 {
		t.Errorf("merged = %v, want the genuine key left unregistered", got)
	}
	state.DispatchedKeysFull = nil
	if got := mergeLegacyMirrors(state, add); len(got) != 1 {
		t.Errorf("merged = %v, want a new mirror registered", got)
	}
}

func TestCompat_LedgerWireRoundTripAndPrune(t *testing.T) {
	ev := compatEvent(1, recent)
	legacy := "code:" + ev.LegacyLabelKey()
	state := persistedPollState{
		DispatchedKeysFull: map[string]int64{legacy: recent.Unix()},
		FailedKeysFull:     map[string]int{"x": 1},
		LegacyMirrors:      mirrorSet{},
	}
	state.LegacyMirrors.add(mirrorKindDispatch, legacy, "code:"+ev.Key())
	wire := encodeLegacyMirrors(state)
	if len(wire.FailedKeysFull) != 2 || wire.FailedKeysFull["x"] != 1 {
		t.Fatalf("wire failed keys = %v", wire.FailedKeysFull)
	}
	back := decodeLegacyMirrors(wire)
	if len(back.LegacyMirrors) != 1 || len(back.FailedKeysFull) != 1 {
		t.Fatalf("decoded = %v / %v", back.LegacyMirrors, back.FailedKeysFull)
	}
	back.DispatchedKeysFull = nil
	if got := pruneLegacyMirrors(back); len(got) != 0 {
		t.Errorf("pruned = %v, want the ledger to follow its keys", got)
	}
}

// mirrorFailureCount carries the largest sibling count on a shared second key
// and deletes it only when no occurrence still owns a count.
func TestCompat_MirrorFailureCountSharedSecond(t *testing.T) {
	a, b := compatEvent(1, recent), compatEvent(3, recent.Add(400*time.Millisecond))
	legacy := a.LegacyLabelKey()
	mirrors := mirrorSet{}
	failed := map[string]int{a.Key(): 2, b.Key(): 1}
	mirrorFailureCount(failed, mirrors, a)
	mirrorFailureCount(failed, mirrors, b)
	if failed[legacy] != 2 {
		t.Fatalf("shared key = %d, want the larger owner count 2", failed[legacy])
	}
	failed[a.Key()] = 0
	mirrorFailureCount(failed, mirrors, a)
	if failed[legacy] != 1 {
		t.Fatalf("shared key = %d after a clears, want b's 1", failed[legacy])
	}
	failed[b.Key()] = 0
	mirrorFailureCount(failed, mirrors, b)
	if failed[legacy] != 0 {
		t.Fatalf("shared key = %d after both clear, want a tombstone", failed[legacy])
	}
}

// Rollback keeps the retry budget: after the new poller exhausts a label's
// retries, the count an older poller reads is spent too.
func TestCompat_RollbackSeesSpentBudget(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	mc.pipelineErr = errors.New("pipeline create failed")
	for i := 0; i < maxEventRetries; i++ {
		_ = eventsPoller(mc).Run(context.Background())
	}
	legacy := compatEvent(1, recent).LegacyLabelKey()
	state := compatState(t, mc)
	if got := state.FailedKeysFull[legacy]; got < maxEventRetries {
		t.Errorf("second-key count = %d (%v), want the spent budget %d visible to an older poller", got, state.FailedKeysFull, maxEventRetries)
	}
}

// Finding: a successful webhook dispatch whose occurrence was superseded
// during the dispatch must still reconcile, so the replacement re-add is
// queued rather than hidden by the earlier recorded presence.
func TestRunWebhook_SupersededDuringDispatchQueuesReplacement(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	mc.setPollState(reAddState(earlier, recent.Add(10*time.Minute)))
	mc.onPipeline = func() {
		mc.onPipeline = nil
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(4, "remove", compatLabel, alice, recent.Add(time.Minute)),
			labelEvent(5, "add", compatLabel, alice, recent.Add(2*time.Minute)))
	}
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state := compatState(t, mc)
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e5"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the replacement e5 queued", state.PendingLabels)
	}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 2 {
		t.Errorf("pipeline calls = %d, want the replacement dispatched by the poll", len(mc.pipelineCalls))
	}
}

// Finding: dispatch evidence for an occurrence still pending reconciliation
// survives the freshness prune, even when an unrelated event advances the
// watermark in the same commit; it is pruned once the occurrence resolves.
func TestApplyPersistDeltas_PendingOccurrenceKeepsDispatchEvidence(t *testing.T) {
	p := eventsPoller(newMockClient())
	old := recent.Add(-48 * time.Hour)
	ev := compatEvent(3, old)
	key := "code:" + ev.Key()
	pl := pendingLabelFor(ev)
	state := persistedPollState{
		DispatchedKeysFull: map[string]int64{key: old.Unix()},
		PendingLabels:      map[string]PendingLabel{ev.Key(): pl},
	}
	wm := recent.Add(time.Hour)
	deltas := persistDeltas{
		dispatched: map[string]int64{"code:note-1": recent.Unix()},
		watermark:  &wm,
		pruneCut:   wm,
	}
	got := p.applyPersistDeltas(state, deltas)
	if _, ok := got.DispatchedKeysFull[key]; !ok {
		t.Fatalf("DispatchedKeysFull = %v, want the pending occurrence's key kept", got.DispatchedKeysFull)
	}
	deltas.pendingClear = map[string]bool{ev.Key(): true}
	got = p.applyPersistDeltas(state, deltas)
	if _, ok := got.DispatchedKeysFull[key]; ok {
		t.Errorf("DispatchedKeysFull = %v, want the key pruned once the occurrence resolved", got.DispatchedKeysFull)
	}
}

// Finding (end to end): an old pending occurrence dispatches, its
// reconciliation lookup fails, and an unrelated recent dispatch advances the
// watermark; the retry must not create the pipeline again.
func TestPoll_PendingDispatchEvidenceSurvivesWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	old := recent.Add(-48 * time.Hour)
	state := reAddState(earlier, recent.Add(10*time.Minute))
	state.PendingLabels = map[string]PendingLabel{
		"issue_label-5-ready-to-code-e3": {
			IID: 5, Label: compatLabel, EventID: 3, At: old.UnixMilli(), OccurredAt: old.UnixMilli(),
			Labels: []string{compatLabel}, ActorID: alice.ID, ActorLogin: alice.Username,
		},
	}
	mc.setPollState(state)
	// Dispatch succeeds, then the post-dispatch lookup of the issue fails.
	calls := 0
	mc.onPipeline = func() {
		calls++
		mc.issueErr[5] = errors.New("gitlab 500")
	}
	_ = eventsPoller(mc).Run(context.Background())
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	st := compatState(t, mc)
	if _, ok := st.PendingLabels["issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("PendingLabels = %+v, want e3 kept pending after the failed reconcile", st.PendingLabels)
	}
	// An unrelated recent dispatch commits (advancing the watermark), then
	// the lookup recovers and the pending occurrence retries.
	delete(mc.issueErr, 5)
	if _, ok := st.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("DispatchedKeysFull = %v, want e3's key", st.DispatchedKeysFull)
	}
	oldPollerRewrite(t, mc, func(doc *persistedPollState) {
		doc.LastPollAtFull = recent.Add(24 * time.Hour).Format(time.RFC3339)
	})
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("retry poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want 1: the retry must not create the pipeline again (calls hook ran %d)", len(mc.pipelineCalls), calls)
	}
}

// A Unix-second failure mirror shared by several registered owners carries the
// largest of their counts, so it must not raise any one owner's count above its
// own key's: an owner with budget left keeps it, and an exhausted one stays
// exhausted on its own key.
func TestCompat_SharedMirrorDoesNotImportMaxIntoOwners(t *testing.T) {
	a, b := compatEvent(1, recent), compatEvent(3, recent.Add(400*time.Millisecond))
	legacy := a.LegacyLabelKey()
	failed := map[string]int{a.Key(): 3, b.Key(): 1, legacy: 3}
	mirrors := mirrorSet{}
	mirrors.add(mirrorKindFailure, legacy, a.Key())
	mirrors.add(mirrorKindFailure, legacy, b.Key())
	if got := failedCount(failed, mirrors, b); got != 1 {
		t.Errorf("failedCount(b) = %d, want its own 1 (not the shared mirror's 3)", got)
	}
	if got := failedCount(failed, mirrors, a); got != 3 {
		t.Errorf("failedCount(a) = %d, want its own exhausted 3", got)
	}
	// A single-owner mirror (an older poller may have advanced it) and genuine
	// unregistered legacy evidence still import.
	single := mirrorSet{}
	single.add(mirrorKindFailure, legacy, b.Key())
	if got := failedCount(map[string]int{b.Key(): 1, legacy: 2}, single, b); got != 2 {
		t.Errorf("single-owner mirror count = %d, want 2", got)
	}
	if got := failedCount(map[string]int{legacy: 2}, nil, b); got != 2 {
		t.Errorf("unregistered legacy count = %d, want 2", got)
	}
}
