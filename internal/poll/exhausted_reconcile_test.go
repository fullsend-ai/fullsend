package poll

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A discovered label occurrence whose legacy retry count leaves one attempt,
// with no pending handoff, spends its budget on the final failed attempt. A
// distinct remove/re-add that lands during that attempt, together with a
// competing poll advancing the watermark past the issue, must still be queued
// as its own pending occurrence rather than hidden by the retired one.
func TestPoll_ExhaustedDiscoveredOccurrenceQueuesReplacement(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues = mc.issues[:1]
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	// A discovered label event is keyed on the issue's updated_at second.
	e1 := compatEvent(1, mc.issues[0].UpdatedAt)
	state, _ := mc.getPollState()
	state.FailedKeysFull = map[string]int{e1.LegacyLabelKey(): maxEventRetries - 1}
	mc.setPollState(state)

	advanced := recent.Format(time.RFC3339)
	replaced := false
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
	mc.onBranchRef = func() {
		if replaced || len(mc.pipelineCalls) == 0 {
			return
		}
		replaced = true
		// A competing poll's watermark has moved past issue 5, and the label
		// was removed and re-added while the final attempt was running.
		cur, _ := mc.getPollState()
		cur.LastPollAtFull = advanced
		mc.setPollState(cur)
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
			labelEvent(3, "add", "ready-to-code", alice, recent))
	}

	_ = eventsPoller(mc).Run(context.Background())
	if !replaced {
		t.Fatal("replacement addition never injected before persist")
	}
	got, _ := mc.getPollState()
	if _, ok := got.PendingLabels["issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the replacement e3 queued", got.PendingLabels)
	}
	if _, ok := got.PendingLabels["issue_label-5-ready-to-code-e1"]; ok {
		t.Fatalf("PendingLabels = %+v, exhausted e1 must not be handed off again", got.PendingLabels)
	}

	mc.pipelineErr = nil
	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if n := len(mc.pipelineCalls) - before; n != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want 1 for the replacement e3", n)
	}
}

// A writer that cleared one occurrence tombstones the shared Unix-second
// failure mirror, but a concurrent writer's failure for another occurrence of
// that second survives under its own key. The merge must keep the mirror (and
// its ledger registration) at the surviving owner's count.
func TestApplyPersistDeltas_SharedFailureMirrorSurvivesStaleTombstone(t *testing.T) {
	a, b := compatEvent(1, recent), compatEvent(3, recent.Add(400*time.Millisecond))
	legacy := a.LegacyLabelKey()
	mirrors := mirrorSet{}
	mirrors.add(mirrorKindFailure, legacy, a.Key())
	mirrors.add(mirrorKindFailure, legacy, b.Key())
	// The reloaded document: a's pre-cycle count, plus b's concurrent failure.
	state := persistedPollState{
		FailedKeysFull: map[string]int{a.Key(): 1, b.Key(): 2, legacy: 2},
		LegacyMirrors:  mirrors.clone(),
	}
	// This writer cleared a only: a's key and the shared mirror are tombstones.
	writerMirrors := mirrorSet{}
	writerMirrors.add(mirrorKindFailure, legacy, a.Key())
	failed := map[string]int{a.Key(): 0, legacy: 0}

	p := &Poller{}
	got := p.applyPersistDeltas(state, persistDeltas{failed: failed, mirrors: writerMirrors})
	if n := got.FailedKeysFull[legacy]; n != 2 {
		t.Fatalf("shared mirror = %d (%v), want b's surviving count 2", n, got.FailedKeysFull)
	}
	if !got.LegacyMirrors.ownedBy(mirrorKindFailure, legacy, b.Key()) || len(got.LegacyMirrors.owners(mirrorKindFailure, legacy)) == 0 {
		t.Fatalf("ledger = %v, want the mirror registration kept", got.LegacyMirrors)
	}
	if _, ok := got.FailedKeysFull[a.Key()]; ok {
		t.Fatalf("a's own key should still be cleared: %v", got.FailedKeysFull)
	}

	// With no surviving owner the tombstone still deletes the mirror.
	only := persistedPollState{
		FailedKeysFull: map[string]int{a.Key(): 1, legacy: 1},
		LegacyMirrors:  writerMirrors.clone(),
	}
	got = p.applyPersistDeltas(only, persistDeltas{failed: failed, mirrors: writerMirrors})
	if _, ok := got.FailedKeysFull[legacy]; ok {
		t.Fatalf("mirror should be deleted when no owner remains: %v", got.FailedKeysFull)
	}
}

// Owners a (count 2) and b (count 1) share a Unix-second failure mirror. A
// writer that clears a computes the mirror as b's count 1, but the max-count
// union keeps the persisted 2. The merge must recompute the mirror from the
// surviving owners so an older poller does not see b's occurrence with a
// count it never reached.
func TestApplyPersistDeltas_SharedFailureMirrorLowersWhenLargerOwnerCleared(t *testing.T) {
	a, b := compatEvent(1, recent), compatEvent(3, recent.Add(400*time.Millisecond))
	legacy := a.LegacyLabelKey()
	mirrors := mirrorSet{}
	mirrors.add(mirrorKindFailure, legacy, a.Key())
	mirrors.add(mirrorKindFailure, legacy, b.Key())
	state := persistedPollState{
		FailedKeysFull: map[string]int{a.Key(): 2, b.Key(): 1, legacy: 2},
		LegacyMirrors:  mirrors.clone(),
	}
	// This writer cleared a; the mirror it computes is b's count.
	failed := map[string]int{a.Key(): 0, legacy: 1}

	p := &Poller{}
	got := p.applyPersistDeltas(state, persistDeltas{failed: failed, mirrors: mirrors})
	if n := got.FailedKeysFull[legacy]; n != 1 {
		t.Fatalf("shared mirror = %d (%v), want the surviving owner's count 1", n, got.FailedKeysFull)
	}
	if n := got.FailedKeysFull[b.Key()]; n != 1 {
		t.Fatalf("b's own count = %d, want 1", n)
	}

	// A mirror with no registered owner is genuine legacy evidence: the
	// max-count union still wins.
	genuine := persistedPollState{FailedKeysFull: map[string]int{legacy: 2}}
	got = p.applyPersistDeltas(genuine, persistDeltas{failed: map[string]int{legacy: 1}})
	if n := got.FailedKeysFull[legacy]; n != 2 {
		t.Fatalf("genuine legacy count = %d, want 2 kept", n)
	}
}
