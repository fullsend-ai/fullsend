package poll

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Producers other than the webhook builder (unresolved-marker resolution and
// replacement handoffs) build their event through labelAdditionEvent. It must
// keep the issue's updated_at as the snapshot time, because an older poller
// keys the addition on it.
func TestLabelAdditionEvent_PreservesIssueSnapshotTime(t *testing.T) {
	issueUpdated := recent.Add(time.Minute)
	issue := &Issue{IID: 5, State: "opened", Labels: []string{compatLabel}, UpdatedAt: issueUpdated}
	latest := labelEvent(1, "add", compatLabel, alice, recent)

	event := labelAdditionEvent(issue, compatLabel, latest)
	want := compatEvent(1, issueUpdated).LegacyLabelKey()
	if want == compatEvent(1, recent).LegacyLabelKey() {
		t.Fatal("fixture: issue and label event timestamps must differ in Unix seconds")
	}
	if got := event.LegacyLabelKey(); got != want {
		t.Fatalf("LegacyLabelKey = %s, want the issue-timestamp key %s", got, want)
	}
	if !event.UpdatedAt.Equal(recent) {
		t.Fatalf("UpdatedAt = %s, want the label event time %s", event.UpdatedAt, recent)
	}
}

// An unresolved marker resolves to an addition dispatched pending-only. If the
// dispatch succeeds but reconciliation then fails, the persisted evidence must
// be readable by an older poller under the issue-timestamp key.
func TestPoll_UnresolvedMarkerMirrorsIssueTimestampKey(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	issueUpdated := recent.Add(time.Minute)
	mc.issue[5].UpdatedAt = issueUpdated
	mc.issues = nil // discovery sees nothing: only the marker leads to the addition
	key, marker := unresolvedLabelMarker(5, compatLabel, time.Now())
	mc.setPollState(persistedPollState{
		LastPollAtFull: time.Now().Format(time.RFC3339),
		PendingLabels:  map[string]PendingLabel{key: marker},
	})
	mc.onPipeline = func() {
		mc.issueErr = map[int]error{5: fmt.Errorf("gitlab api: 503 unavailable")}
		mc.onPipeline = nil
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the reconcile failure reported")
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state := compatState(t, mc)
	oldKey := compatEvent(1, issueUpdated)
	if !oldPollerSawDispatch(state.DispatchedKeysFull, "code", oldKey) {
		t.Fatalf("DispatchedKeysFull = %v, want the mirror under the issue-timestamp key %s", state.DispatchedKeysFull, oldKey.LegacyLabelKey())
	}
}

// Deduplication depends on the dispatched keys, failure counts, and mirror
// ledger corresponding, so they are read from a single poll-state snapshot.
func TestReadDedupState_SingleSnapshot(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{
		DispatchedKeysFull: map[string]int64{"code:k": 1},
		FailedKeysFull:     map[string]int{"k": 2},
	})
	before := len(mc.fileContentRefs)
	dispatched, failed, _, err := eventsPoller(mc).readDedupState(context.Background(), "group", "project")
	if err != nil {
		t.Fatalf("readDedupState: %v", err)
	}
	if got := len(mc.fileContentRefs) - before; got != 1 {
		t.Fatalf("poll-state reads = %d, want 1", got)
	}
	if dispatched["code:k"] != 1 || failed["k"] != 2 {
		t.Fatalf("dispatched = %v, failed = %v", dispatched, failed)
	}
}
