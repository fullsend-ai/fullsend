package poll

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A webhook addition carries the label event's own time as its identity, but an
// older poller keys the same addition on the issue's updated_at. After an
// unrelated issue update the two differ. When the dispatch succeeds but label
// reconciliation fails, the persisted dispatch evidence must still be readable
// under the issue-timestamp key, or an older poller dispatches the addition
// again.
func TestCompat_OldReaderAfterNewWriterWithDifferingTimestamps(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	issueUpdated := recent.Add(time.Minute) // an unrelated update after the label add
	mc.issue[5].UpdatedAt = issueUpdated
	mc.setPollState(persistedPollState{LastPollAtFull: beforeRecent})
	mc.onPipeline = func() {
		// Dispatch succeeds; the post-dispatch reconciliation lookup fails.
		mc.issueErr = map[int]error{5: fmt.Errorf("gitlab api: 503 unavailable")}
		mc.onPipeline = nil
	}

	payload := issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{compatLabel}))
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook: want the reconcile failure reported")
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state := compatState(t, mc)
	if len(state.LabelState[5]) != 0 {
		t.Fatalf("LabelState = %v, want the label unrecorded after the failed reconcile", state.LabelState)
	}

	// The older poller discovers the label on the issue and keys the addition
	// on the issue's updated_at; it must find the dispatch.
	oldKey := compatEvent(1, issueUpdated)
	if oldKey.LegacyLabelKey() == compatEvent(1, recent).LegacyLabelKey() {
		t.Fatal("fixture: issue and label event timestamps must differ in Unix seconds")
	}
	if !oldPollerSawDispatch(state.DispatchedKeysFull, "code", oldKey) {
		t.Fatalf("DispatchedKeysFull = %v, want the mirror under the issue-timestamp key %s", state.DispatchedKeysFull, oldKey.LegacyLabelKey())
	}
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("DispatchedKeysFull = %v, want the occurrence key too", state.DispatchedKeysFull)
	}

	// The pending handoff keeps the snapshot time, so the retry that finally
	// reconciles still derives the same legacy key.
	pl, ok := state.PendingLabels["issue_label-5-ready-to-code-e1"]
	if !ok {
		t.Fatalf("PendingLabels = %+v, want the reconciliation handoff", state.PendingLabels)
	}
	if got := pl.routableEvent().LegacyLabelKey(); got != oldKey.LegacyLabelKey() {
		t.Fatalf("pending legacy key = %s, want %s", got, oldKey.LegacyLabelKey())
	}
}

// An older writer advances only the Unix-second failure mirror between a
// current writer's initial read and its persistence. The recomputation must not
// restore the retry budget: it keeps the advance, and for an unambiguous single
// owner migrates the count onto the occurrence key.
func TestApplyPersistDeltas_PreservesLegacyOnlyFailureAdvance(t *testing.T) {
	a, b := compatEvent(1, recent), compatEvent(3, recent.Add(400*time.Millisecond))
	legacy := a.LegacyLabelKey()
	p := &Poller{}

	single := mirrorSet{}
	single.add(mirrorKindFailure, legacy, a.Key())
	// The reloaded document: the older writer advanced the mirror to 2.
	state := persistedPollState{
		FailedKeysFull: map[string]int{a.Key(): 1, legacy: 2},
		LegacyMirrors:  single.clone(),
	}
	// The current writer prepared occurrence=1 and mirror=1 from its earlier read.
	got := p.applyPersistDeltas(state, persistDeltas{failed: map[string]int{a.Key(): 1, legacy: 1}, mirrors: single})
	if n := got.FailedKeysFull[legacy]; n != 2 {
		t.Fatalf("mirror = %d (%v), want the older writer's advance 2 kept", n, got.FailedKeysFull)
	}
	if n := got.FailedKeysFull[a.Key()]; n != 2 {
		t.Fatalf("owner count = %d (%v), want the advance migrated onto the occurrence key", n, got.FailedKeysFull)
	}

	// Several owners: the advance cannot be attributed, but it is not lost.
	shared := mirrorSet{}
	shared.add(mirrorKindFailure, legacy, a.Key())
	shared.add(mirrorKindFailure, legacy, b.Key())
	state = persistedPollState{
		FailedKeysFull: map[string]int{a.Key(): 1, b.Key(): 1, legacy: 2},
		LegacyMirrors:  shared.clone(),
	}
	got = p.applyPersistDeltas(state, persistDeltas{failed: map[string]int{a.Key(): 1, legacy: 1}, mirrors: shared})
	if n := got.FailedKeysFull[legacy]; n != 2 {
		t.Fatalf("shared mirror = %d (%v), want the advance 2 kept", n, got.FailedKeysFull)
	}
	if got.FailedKeysFull[a.Key()] != 1 || got.FailedKeysFull[b.Key()] != 1 {
		t.Fatalf("owner counts = %v, want unattributed advance left off the owners", got.FailedKeysFull)
	}

	// An explicit resolution by this writer still deletes the mirror.
	state = persistedPollState{
		FailedKeysFull: map[string]int{a.Key(): 1, legacy: 2},
		LegacyMirrors:  single.clone(),
	}
	got = p.applyPersistDeltas(state, persistDeltas{failed: map[string]int{a.Key(): 0, legacy: 0}, mirrors: single})
	if _, ok := got.FailedKeysFull[legacy]; ok {
		t.Fatalf("mirror should be deleted by an explicit resolution: %v", got.FailedKeysFull)
	}
}
