package poll

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestRunWebhook_TransientLookupFailureHandsOffValidatedLabel: a membership
// lookup that fails while normalizing a validated re-add must not drop the
// occurrence. It is persisted as pending (nothing is dispatched), and a later
// poll dispatches it once the lookup recovers, with no help from discovery.
func TestRunWebhook_TransientLookupFailureHandsOffValidatedLabel(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil // the earlier occurrence stays in LabelState, behind the watermark
	mc.setPollState(reAddState(earlier, recent.Add(10*time.Minute)))
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	mc.memberErr[alice.ID] = fmt.Errorf("gitlab api: 503 unavailable")

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook: want the lookup failure reported")
	}
	if len(mc.pipelineCalls) != 0 {
		t.Fatalf("pipeline calls = %d, want 0 while the lookup fails", len(mc.pipelineCalls))
	}
	const key = "issue_label-5-ready-to-code-e3"
	state, _ := mc.getPollState()
	pl, ok := state.PendingLabels[key]
	if !ok || pl.IID != 5 || pl.Label != "ready-to-code" || pl.EventID != 3 || pl.ActorID != alice.ID {
		t.Fatalf("PendingLabels = %+v, want the validated re-add occurrence with its actor", state.PendingLabels)
	}

	delete(mc.memberErr, alice.ID)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run after recovery: %v", err)
	}
	if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].Variables["STAGE"] != "code" {
		t.Fatalf("pipeline calls = %+v, want the poll to dispatch the pending occurrence", mc.pipelineCalls)
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want cleared after dispatch", state.PendingLabels)
	}
}

// TestRunWebhook_UnvalidatedLabelPayloadPersistsNothing: a payload that fails provenance validation is still rejected without any
// persisted handoff.
func TestRunWebhook_UnvalidatedLabelPayloadPersistsNothing(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.setPollState(reAddState(earlier, recent.Add(10*time.Minute)))
	// bob did not apply the label: actor binding fails.
	payload := issuePayload(t, "update", bob.ID, 5, reAddPayloadLabels)

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook: want validation error")
	}
	state, _ := mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want none for an unvalidated payload", state.PendingLabels)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Errorf("pipeline calls = %d, want 0", len(mc.pipelineCalls))
	}
}

// pendingInitialAddState seeds a pending initial addition (event 1) that has
// no LabelState entry and whose issue is behind the discovery watermark.
func pendingInitialAddState(key string) persistedPollState {
	return persistedPollState{
		LastPollAtFull: recent.Add(10 * time.Minute).Format(time.RFC3339),
		PendingLabels: map[string]PendingLabel{
			key: {
				IID: 5, Label: "ready-to-code", EventID: 1, At: recent.UnixMilli(),
				Labels: []string{"ready-to-code"}, ActorID: alice.ID, ActorLogin: alice.Username,
			},
		},
	}
}

// replacePersistedPollState overwrites the already-materialized events-branch
// state.json with a correctly signed copy of s. mockClient.setPollState only
// seeds a not-yet-written branch; GetFileContentAtRef prefers a written file.
func replacePersistedPollState(t *testing.T, mc *mockClient, s persistedPollState) {
	t.Helper()
	signed, err := signPollState(testDispatchSecret, hmacDomainFor(PollStateBranchEvents, "group/project"), s)
	if err != nil {
		t.Fatalf("sign poll state: %v", err)
	}
	data, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal poll state: %v", err)
	}
	mc.putBranchFile(PollStateBranchEvents, PollStateFileName, data)
}

// TestPoll_PendingRetryRecordsLabelState: a successful pending-only retry
// records the label in LabelState, so after the dispatch key is pruned an
// unrelated issue update does not rediscover and re-dispatch the addition.
func TestPoll_PendingRetryRecordsLabelState(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.issues = nil // behind the watermark: discovery cannot see the issue
	const key = "issue_label-5-ready-to-code-e1"
	mc.setPollState(pendingInitialAddState(key))

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want cleared", state.PendingLabels)
	}
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState = %+v, want the retried label recorded", state.LabelState)
	}

	// The dispatch key and its replay mirrors expire. The first run already
	// materialized state.json on the mock branch, which setPollState's seed
	// would not override, so replace the persisted document itself.
	state.DispatchedKeysFull = nil
	state.LegacyMirrors = nil
	replacePersistedPollState(t, mc, state)
	keys, err := eventsPoller(mc).readDispatchedKeys(context.Background(), "group", "project")
	if err != nil {
		t.Fatalf("readDispatchedKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("dispatched keys = %v, want none after expiry", keys)
	}

	// An unrelated update then surfaces the issue.
	iss := *mc.issue[5]
	iss.UpdatedAt = time.Now()
	mc.issues = []Issue{iss}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("second poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want no re-dispatch of the same addition", len(mc.pipelineCalls))
	}
}

// TestPoll_PendingRetryDoesNotRestoreRemovedLabel: a label removed since the
// handoff is not written back into LabelState.
func TestPoll_PendingRetryDoesNotRestoreRemovedLabel(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.issues = nil
	mc.issue[5].Labels = nil
	const key = "issue_label-5-ready-to-code-e1"
	mc.setPollState(pendingInitialAddState(key))

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Errorf("LabelState = %+v, want the removed label not restored", state.LabelState)
	}
}

// TestPoll_OlderPendingRetryDoesNotSuppressFailedReAdd: pending addition e1
// (alice) is retried while the label has since been removed and re-added as
// e3 (bob), whose discovery fails on a transient membership lookup. The older
// retry succeeds but must not record the label in LabelState: e3 was never
// dispatched and has to be rediscovered once bob's lookup recovers.
func TestPoll_OlderPendingRetryDoesNotSuppressFailedReAdd(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.memberLevel[bob.ID] = 30
	iss := Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, Author: alice, UpdatedAt: recent}
	mc.issue[5] = &iss
	mc.issues = []Issue{iss}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-19*time.Minute)),
		labelEvent(3, "add", "ready-to-code", bob, recent),
	}
	const key = "issue_label-5-ready-to-code-e1"
	mc.setPollState(pendingInitialAddState(key))
	mc.memberErr[bob.ID] = fmt.Errorf("gitlab api: 503 unavailable")

	// e3 fails normalization; the older e1 retry dispatches.
	_ = eventsPoller(mc).Run(context.Background())
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1 (the older pending retry only)", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	// The resolved e1 is cleared; the failed discovered e3 is handed off as
	// pending, bound to bob, so its retry does not depend on the watermark.
	const failedKey = "issue_label-5-ready-to-code-e3"
	if len(state.PendingLabels) != 1 || state.PendingLabels[failedKey].ActorID != bob.ID {
		t.Errorf("PendingLabels = %+v, want only the failed e3 handed off with bob as actor", state.PendingLabels)
	}
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState = %+v, want the failed e3 occurrence left unrecorded", state.LabelState)
	}

	delete(mc.memberErr, bob.ID)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run after recovery: %v", err)
	}
	if len(mc.pipelineCalls) != 2 {
		t.Fatalf("pipeline calls = %d, want e3 dispatched once bob's lookup recovers", len(mc.pipelineCalls))
	}
}

// TestPoll_PendingRetryKeptWhenReconcileFails: a failed issue lookup after a
// successful dispatch keeps the occurrence pending without re-dispatching;
// the next cycle records the label state and clears it.
func TestPoll_PendingRetryKeptWhenReconcileFails(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.issues = nil
	const key = "issue_label-5-ready-to-code-e1"
	mc.setPollState(pendingInitialAddState(key))
	mc.issueErr = map[int]error{5: fmt.Errorf("gitlab api: 503 unavailable")}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the reconcile failure reported")
	}
	state, _ := mc.getPollState()
	if _, kept := state.PendingLabels[key]; !kept {
		t.Fatalf("PendingLabels = %+v, want the occurrence kept", state.PendingLabels)
	}

	mc.issueErr = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run after recovery: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want exactly 1 across both cycles", len(mc.pipelineCalls))
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want cleared", state.PendingLabels)
	}
	if got := state.LabelState[5]; len(got) != 1 {
		t.Errorf("LabelState = %+v, want the label recorded", state.LabelState)
	}
}

// TestPoll_DoesNotEraseConcurrentWebhookLabelOutsideDiscovery: a poll loaded
// LabelState with an entry for an open issue it does not discover (behind the
// watermark); a webhook records a further label for that issue while the poll
// dispatches. The poll's persist must not write its stale entry back over it.
func TestPoll_DoesNotEraseConcurrentWebhookLabelOutsideDiscovery(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.issue[9] = &Issue{IID: 9, State: "opened", Labels: []string{"ready-to-code", "ready-for-review"}}
	st := persistedPollState{
		LastPollAtFull: recent.Add(-time.Hour).Format(time.RFC3339),
		LabelState:     LabelState{9: {"ready-to-code"}},
	}
	mc.setPollState(st)
	mc.onPipeline = func() {
		cur, _ := mc.getPollState()
		cur.LabelState = LabelState{9: {"ready-to-code", "ready-for-review"}}
		mc.setPollState(cur)
		mc.onPipeline = nil
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[9]; len(got) != 2 {
		t.Fatalf("LabelState[9] = %v, want the webhook's concurrently recorded label preserved", got)
	}
}

// TestRunWebhook_RemovalDuringDispatchDoesNotRestoreLabelPresence: the label
// is removed (and the poll persisted its absence) after the webhook validated
// the addition. The webhook must not union the stale presence back, so a
// later re-add whose webhook is never delivered is still discovered by the
// poll.
func TestRunWebhook_RemovalDuringDispatchDoesNotRestoreLabelPresence(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	st := reAddState(earlier, recent.Add(10*time.Minute))
	st.LabelState = nil // the poll already persisted the label's absence
	mc.setPollState(st)
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	mc.onPipeline = func() {
		mc.issue[5].Labels = nil
		mc.labelEvents[5] = append(mc.labelEvents[5], labelEvent(4, "remove", "ready-to-code", alice, recent.Add(time.Minute)))
		mc.onPipeline = nil
	}

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want the successful dispatch key preserved", state.DispatchedKeysFull)
	}
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState = %+v, want the removed label's presence not restored", state.LabelState)
	}

	// Re-add (event 5) without any webhook delivery: discovery must see it.
	mc.issue[5].Labels = []string{"ready-to-code"}
	mc.labelEvents[5] = append(mc.labelEvents[5], labelEvent(5, "add", "ready-to-code", alice, recent.Add(2*time.Minute)))
	iss := *mc.issue[5]
	iss.UpdatedAt = recent.Add(2 * time.Minute)
	mc.issues = []Issue{iss}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 2 {
		t.Fatalf("pipeline calls = %d, want the undelivered re-add dispatched by the poll", len(mc.pipelineCalls))
	}
}

// removeReadyToCode simulates a label removal (event 4) landing on the forge.
func removeReadyToCode(mc *mockClient) {
	mc.issue[5].Labels = nil
	mc.labelEvents[5] = append(mc.labelEvents[5], labelEvent(4, "remove", "ready-to-code", alice, recent.Add(time.Minute)))
}

// assertRemovalNotRestoredThenReAddDispatched checks the persisted state kept
// the dispatch key without restoring the removed label's presence, and that a
// later re-add whose webhook is never delivered is still found by the poll.
func assertRemovalNotRestoredThenReAddDispatched(t *testing.T, mc *mockClient) {
	t.Helper()
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want the successful dispatch key preserved", state.DispatchedKeysFull)
	}
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState = %+v, want the removed label's presence not restored", state.LabelState)
	}
	mc.issue[5].Labels = []string{"ready-to-code"}
	mc.labelEvents[5] = append(mc.labelEvents[5], labelEvent(5, "add", "ready-to-code", alice, recent.Add(2*time.Minute)))
	iss := *mc.issue[5]
	iss.UpdatedAt = recent.Add(2 * time.Minute)
	mc.issues = []Issue{iss}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 2 {
		t.Fatalf("pipeline calls = %d, want the undelivered re-add dispatched by the poll", len(mc.pipelineCalls))
	}
}

// TestRunWebhook_RemovalBeforeCommitDoesNotRestoreLabelPresence: the label is
// removed after the webhook's reconciliation check but before its poll-state
// commit (the removal's own poll-state commit already recorded the absence).
// Persistence revalidates the addition after loading state, so the stale
// presence is not unioned back and the dispatch key is still kept.
func TestRunWebhook_RemovalBeforeCommitDoesNotRestoreLabelPresence(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	st := reAddState(earlier, recent.Add(10*time.Minute))
	st.LabelState = nil
	mc.setPollState(st)
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	mc.onBranchRef = func() {
		removeReadyToCode(mc)
		mc.onBranchRef = nil
	}

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	assertRemovalNotRestoredThenReAddDispatched(t, mc)
}

// TestRunWebhook_RemovalDuringCASRetryDoesNotRestoreLabelPresence: the first
// commit attempt loses its CAS to a concurrent writer; the label is removed
// before the retry. The retry must revalidate, not reuse the first attempt's
// answer.
func TestRunWebhook_RemovalDuringCASRetryDoesNotRestoreLabelPresence(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	st := reAddState(earlier, recent.Add(10*time.Minute))
	st.LabelState = nil
	mc.setPollState(st)
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	conflict := st
	mc.conflictOnce = &conflict
	calls := 0
	mc.onBranchRef = func() {
		calls++
		if calls == 2 {
			removeReadyToCode(mc)
		}
	}

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook: %v", err)
	}
	if calls < 2 {
		t.Fatalf("GetBranchRef calls = %d, want a CAS retry", calls)
	}
	mc.onBranchRef = nil
	assertRemovalNotRestoredThenReAddDispatched(t, mc)
}

// TestPoll_PendingRetryRemovalBeforeCommitDoesNotRestoreLabel: same window for
// a pending-only retry reconciled by the poller.
func TestPoll_PendingRetryRemovalBeforeCommitDoesNotRestoreLabel(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.issues = nil
	const key = "issue_label-5-ready-to-code-e1"
	mc.setPollState(pendingInitialAddState(key))
	mc.onBranchRef = func() {
		mc.issue[5].Labels = nil
		mc.labelEvents[5] = append(mc.labelEvents[5], labelEvent(2, "remove", "ready-to-code", alice, recent.Add(time.Minute)))
		mc.onBranchRef = nil
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState = %+v, want the removed label not restored", state.LabelState)
	}
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want the resolved occurrence cleared", state.PendingLabels)
	}
}

// TestRunWebhook_PersistTimeLookupFailureKeepsHandoff: the revalidation
// lookup at persistence time fails. The dispatch key is still committed and
// the occurrence is handed off for the poller to reconcile, not recorded.
func TestRunWebhook_PersistTimeLookupFailureKeepsHandoff(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	st := reAddState(earlier, recent.Add(10*time.Minute))
	st.LabelState = nil
	mc.setPollState(st)
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	mc.onBranchRef = func() {
		mc.issueErr = map[int]error{5: fmt.Errorf("gitlab api: 503 unavailable")}
		mc.onBranchRef = nil
	}

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook: want the reconcile failure reported")
	}
	const key = "issue_label-5-ready-to-code-e3"
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:"+key]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want the dispatch key committed", state.DispatchedKeysFull)
	}
	if _, ok := state.PendingLabels[key]; !ok {
		t.Fatalf("PendingLabels = %+v, want a reconciliation handoff", state.PendingLabels)
	}
	if got := state.LabelState[5]; len(got) != 0 {
		t.Errorf("LabelState = %+v, want nothing recorded on a failed lookup", state.LabelState)
	}
}

// TestRunWebhook_ReconcileLookupFailureKeepsHandoff: the issue lookup that
// confirms the dispatched addition is still current fails after a successful
// dispatch. The dispatch key is still recorded and a pending handoff is kept
// so the poller records LabelState once the lookup recovers, without a
// second pipeline.
func TestRunWebhook_ReconcileLookupFailureKeepsHandoff(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	st := reAddState(earlier, recent.Add(10*time.Minute))
	st.LabelState = nil
	mc.setPollState(st)
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	mc.onPipeline = func() {
		mc.issueErr = map[int]error{5: fmt.Errorf("gitlab api: 503 unavailable")}
		mc.onPipeline = nil
	}

	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook: want the reconcile failure reported")
	}
	const key = "issue_label-5-ready-to-code-e3"
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:"+key]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want the dispatch key recorded", state.DispatchedKeysFull)
	}
	if _, ok := state.PendingLabels[key]; !ok {
		t.Fatalf("PendingLabels = %+v, want a reconciliation handoff", state.PendingLabels)
	}

	mc.issueErr = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run after recovery: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want no second pipeline", len(mc.pipelineCalls))
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want cleared", state.PendingLabels)
	}
	if got := state.LabelState[5]; len(got) != 1 {
		t.Errorf("LabelState = %+v, want the label recorded", state.LabelState)
	}
}

// TestRunWebhook_RetryReconcilesHandoffWhenAllStagesDispatched: the first
// delivery created the pipeline but its reconciliation lookup failed, so the
// dispatch key and a pending handoff were persisted. A webhook replay finds
// every stage already dispatched; it must still reconcile LabelState before
// clearing the handoff, and keep the handoff while reconciliation keeps
// failing.
func TestRunWebhook_RetryReconcilesHandoffWhenAllStagesDispatched(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.issues = nil
	st := reAddState(earlier, recent.Add(10*time.Minute))
	st.LabelState = nil
	mc.setPollState(st)
	payload := issuePayload(t, "update", alice.ID, 5, reAddPayloadLabels)
	mc.onPipeline = func() {
		mc.issueErr = map[int]error{5: fmt.Errorf("gitlab api: 503 unavailable")}
		mc.onPipeline = nil
	}
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook: want the reconcile failure reported")
	}
	const key = "issue_label-5-ready-to-code-e3"

	// Replay while the lookup still fails: the handoff must survive.
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err == nil {
		t.Fatal("RunWebhook replay: want the reconcile failure reported")
	}
	state, _ := mc.getPollState()
	if _, ok := state.PendingLabels[key]; !ok {
		t.Fatalf("PendingLabels = %+v, want the handoff kept while reconciliation fails", state.PendingLabels)
	}

	// Replay after recovery: reconcile, then clear.
	mc.issueErr = nil
	if err := newWebhookPoller(mc).RunWebhook(context.Background(), payload); err != nil {
		t.Fatalf("RunWebhook replay after recovery: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Errorf("pipeline calls = %d, want no second pipeline", len(mc.pipelineCalls))
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Errorf("PendingLabels = %+v, want cleared after reconciliation", state.PendingLabels)
	}
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState = %+v, want the label recorded before the handoff was cleared", state.LabelState)
	}
}

// pollInDiscoveryWithTrigger seeds issue 5 (already inside discovery) with
// the given labels and an unrelated newly labelled issue 6 whose dispatch
// triggers mc.onPipeline.
func pollInDiscoveryWithTrigger(mc *mockClient, issue5Labels []string, stored LabelState) {
	webhookLabelAddFixture(mc)
	mc.issue[5].Labels = issue5Labels
	iss5 := *mc.issue[5]
	iss6 := Issue{IID: 6, State: "opened", Labels: []string{"ready-to-code"}, Author: bob, UpdatedAt: recent}
	mc.issue[6] = &iss6
	mc.labelEvents[6] = []ResourceLabelEvent{labelEvent(2, "add", "ready-to-code", alice, recent)}
	mc.issues = []Issue{iss5, iss6}
	mc.setPollState(persistedPollState{
		LastPollAtFull: recent.Add(-time.Hour).Format(time.RFC3339),
		LabelState:     stored,
	})
}

// TestPoll_ConcurrentAdditionInsideDiscoveryPreserved: a webhook records
// ready-for-review for issue 5 after the poll loaded state. The poll also
// discovers issue 5 (newly carrying ready-to-code); persisting its snapshot
// must add its own change, not replace the webhook's label.
func TestPoll_ConcurrentAdditionInsideDiscoveryPreserved(t *testing.T) {
	mc := newMockClient()
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.onPipeline = func() {
		cur, _ := mc.getPollState()
		cur.LabelState = LabelState{5: {"ready-for-review"}}
		mc.setPollState(cur)
		mc.onPipeline = nil
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	got := toSet(state.LabelState[5])
	if len(got) != 2 || !got["ready-to-code"] || !got["ready-for-review"] {
		t.Fatalf("LabelState[5] = %v, want both the poll's and the concurrent label", state.LabelState[5])
	}
}

// TestPoll_ConcurrentRemovalInsideDiscoveryNotRestored: a concurrent writer
// removes ready-to-code from issue 5's recorded state after the poll loaded
// it. The poll's unchanged, stale snapshot of issue 5 must not restore it,
// or a later re-add whose webhook is missed would be invisible to discovery.
func TestPoll_ConcurrentRemovalInsideDiscoveryNotRestored(t *testing.T) {
	mc := newMockClient()
	both := []string{"ready-to-code", "ready-for-review"}
	pollInDiscoveryWithTrigger(mc, both, LabelState{5: both})
	mc.onPipeline = func() {
		cur, _ := mc.getPollState()
		cur.LabelState = LabelState{5: {"ready-for-review"}}
		mc.setPollState(cur)
		mc.onPipeline = nil
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1 (issue 6 only)", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-for-review" {
		t.Fatalf("LabelState[5] = %v, want the concurrent removal preserved", got)
	}
}

func TestMergeLabelStateAgainst(t *testing.T) {
	existing := LabelState{1: {"a", "c"}, 2: {"a"}, 3: {"a"}}
	base := LabelState{1: {"a", "b"}, 2: {"a"}, 3: {"a"}}
	incoming := LabelState{1: {"a", "d"}, 2: {"a"}, 3: {}, 4: {"x"}}
	merged := mergeLabelStateAgainst(existing, base, incoming)
	if got := merged[1]; len(got) != 3 || got[0] != "a" || got[1] != "c" || got[2] != "d" {
		t.Errorf("merged[1] = %v, want [a c d] (b removed, d added, concurrent c kept)", got)
	}
	if got := merged[2]; len(got) != 1 || got[0] != "a" {
		t.Errorf("merged[2] = %v, want unchanged", got)
	}
	if _, ok := merged[3]; ok {
		t.Errorf("merged[3] = %v, want deleted once its labels are removed", merged[3])
	}
	if got := merged[4]; len(got) != 1 || got[0] != "x" {
		t.Errorf("merged[4] = %v, want [x]", got)
	}
}
