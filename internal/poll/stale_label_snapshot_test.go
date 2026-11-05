package poll

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestDiscoverAllEvents_LabelEventsEndingInRemovalHoldLabel: the issue-list
// snapshot still carries the label, but its latest label event is a removal,
// so the snapshot predates the removal. The addition must be neither emitted
// (a timestamp key could not be matched against a webhook's ID-keyed dispatch
// of the preceding add) nor recorded, and the watermark must be held.
func TestDiscoverAllEvents_LabelEventsEndingInRemovalHoldLabel(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"ready-to-code", "ready-for-review"}},
	}
	mc.notes[1] = []Note{}
	mc.labelEvents[1] = []ResourceLabelEvent{
		labelEvent(10, "add", "ready-to-code", alice, now.Add(-time.Hour)),
		labelEvent(11, "remove", "ready-to-code", alice, now.Add(-time.Minute)),
		labelEvent(12, "add", "ready-for-review", alice, now),
	}

	p := newEventsPoller(mc)
	events, labelState, minSkipped, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var labelEvs []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_label" {
			labelEvs = append(labelEvs, e)
		}
	}
	if len(labelEvs) != 1 || labelEvs[0].ChangedLabel != "ready-for-review" || labelEvs[0].LabelEventID != 12 {
		t.Fatalf("issue_label events = %+v, want only the consistent ready-for-review addition (event 12)", labelEvs)
	}
	if got := labelState[1]; len(got) != 1 || got[0] != "ready-for-review" {
		t.Errorf("labelState[1] = %v, want [ready-for-review] so the held label is rediscovered", got)
	}
	if !minSkipped.Equal(now) {
		t.Errorf("minSkippedAt = %v, want %v so the watermark is held back", minSkipped, now)
	}
}

// TestPoll_StaleSnapshotRemovalDoesNotDuplicateWebhookDispatch: the poll's
// issue snapshot predates a removal, and the preceding addition's webhook
// dispatch key is already persisted. The poll must not dispatch the label
// again under a timestamp key.
func TestPoll_StaleSnapshotRemovalDoesNotDuplicateWebhookDispatch(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc) // mc.issues snapshot still carries the label
	mc.issue[5].Labels = nil   // forge: the label was since removed
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent),
	}
	mc.setPollState(persistedPollState{
		LastPollAtFull: recent.Add(-time.Hour).Format(time.RFC3339),
		DispatchedKeysFull: map[string]int64{
			"code:issue_label-5-ready-to-code-e1": recent.Unix(),
		},
	})

	_ = eventsPoller(mc).Run(context.Background())
	if len(mc.pipelineCalls) != 0 {
		t.Fatalf("pipeline calls = %d, want 0: the stale snapshot's addition must not be dispatched", len(mc.pipelineCalls))
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Errorf("LabelState[5] = %v, want the stale addition not recorded", got)
	}
}

// staleRemovalFixture seeds issue 5 as: the poll's issue snapshot lacks
// ready-to-code (stored state records it), while the forge now carries a
// re-addition (event 3). Issue 6's dispatch triggers mc.onPipeline.
func staleRemovalFixture(mc *mockClient) {
	pollInDiscoveryWithTrigger(mc, nil, LabelState{5: {"ready-to-code"}})
	mc.issue[5].Labels = []string{"ready-to-code"}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
		labelEvent(3, "add", "ready-to-code", alice, recent),
	}
}

// TestPoll_StaleRemovalDoesNotEraseDispatchedReAdd: a webhook dispatches the
// re-addition after the poll's snapshot; the poll's stale removal must not
// erase the presence the webhook recorded.
func TestPoll_StaleRemovalDoesNotEraseDispatchedReAdd(t *testing.T) {
	mc := newMockClient()
	staleRemovalFixture(mc)
	mc.onPipeline = func() {
		cur, _ := mc.getPollState()
		cur.DispatchedKeysFull = map[string]int64{
			"code:issue_label-5-ready-to-code-e3": recent.Unix(),
		}
		mc.setPollState(cur)
		mc.onPipeline = nil
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want the dispatched re-addition's presence preserved", got)
	}
}

// TestPoll_StaleRemovalAppliedWhenReAddNotDispatched: with no dispatch
// evidence for the re-addition, the removal still applies so the next poll
// discovers and dispatches it.
func TestPoll_StaleRemovalAppliedWhenReAddNotDispatched(t *testing.T) {
	mc := newMockClient()
	staleRemovalFixture(mc)

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the removal applied", got)
	}
}

// TestPoll_StaleRemovalRevalidatedOnCASRetry: the first commit attempt loads
// state without dispatch evidence for the re-addition, so it applies the stale
// removal, then loses its CAS to a writer that dispatched the re-addition
// (conflictOnce publishes that competing state). The retry must revalidate
// the removal against the new state instead of reusing the first attempt's
// answer.
func TestPoll_StaleRemovalRevalidatedOnCASRetry(t *testing.T) {
	mc := newMockClient()
	staleRemovalFixture(mc)
	mc.onPipeline = nil
	mc.conflictOnce = &persistedPollState{
		LastPollAtFull: recent.Add(-time.Hour).Format(time.RFC3339),
		LabelState:     LabelState{5: {"ready-to-code"}},
		DispatchedKeysFull: map[string]int64{
			"code:issue_label-5-ready-to-code-e3": recent.Unix(),
		},
	}
	attempts := 0
	firstAttemptLabelEventCalls := -1
	mc.onBranchRef = func() {
		attempts++
		if firstAttemptLabelEventCalls < 0 {
			firstAttemptLabelEventCalls = mc.labelEventCalls
		}
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("persistence attempts = %d, want at least 2 (the first must lose its CAS)", attempts)
	}
	// Each attempt revalidates the removal against the forge: the first
	// attempt's lookups plus the retry's give at least two label-event reads.
	if got := mc.labelEventCalls - firstAttemptLabelEventCalls; got < 2 {
		t.Fatalf("label-event lookups across persistence attempts = %d, want at least 2 (removal revalidated again on the retry)", got)
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want the dispatched re-addition's presence preserved", got)
	}
}

// TestPoll_SupersededAdditionHoldsWatermarkAndRecovers: the poll discovers
// addition e1 and dispatches it, but before persisting, the label is removed
// and re-added as e3 (currently present, no dispatch evidence). Revalidation
// drops the stale e1 presence; a newer unrelated event would advance the
// watermark past the issue, so it must be held so that, with an
// updated_after-honoring issue list, the next cycle rediscovers and dispatches
// e3.
func TestPoll_SupersededAdditionHoldsWatermarkAndRecovers(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	// Issue 5 is older than the watermark an advancing cycle would store.
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	startWatermark := recent.Add(-time.Hour).Format(time.RFC3339)
	replaced := false
	mc.onBranchRef = func() {
		if replaced || len(mc.pipelineCalls) == 0 {
			return
		}
		replaced = true
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
			labelEvent(3, "add", "ready-to-code", alice, recent))
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if !replaced {
		t.Fatal("replacement addition never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the superseded addition not recorded", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("dispatch evidence for e1 missing: %v", state.DispatchedKeysFull)
	}

	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("replacement addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
}

// TestPoll_StaleRemovalLookupFailureKeepsPresence: when the forge cannot be
// consulted the presence is kept (not guessed away) and the failure reported.
func TestPoll_StaleRemovalLookupFailureKeepsPresence(t *testing.T) {
	mc := newMockClient()
	staleRemovalFixture(mc)
	mc.labelEventsErr[5] = fmt.Errorf("label events unavailable")

	err := eventsPoller(mc).Run(context.Background())
	if err == nil {
		t.Fatal("poll Run: want the failed removal revalidation reported")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want presence kept while revalidation fails", got)
	}
}

// TestPoll_FailedRemovalRevalidationHoldsWatermarkAndRecovers: the removal's
// revalidation fails (issue lookup error), so the label stays recorded. The
// watermark must not advance past the issue; with an updated_after-honoring
// issue list, the next cycle must rediscover it and apply the removal.
func TestPoll_FailedRemovalRevalidationHoldsWatermarkAndRecovers(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, nil, LabelState{5: {"ready-to-code"}})
	// Issue 5 is older than the watermark an advancing cycle would store.
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	startWatermark := recent.Add(-time.Hour).Format(time.RFC3339)
	mc.issueErr[5] = fmt.Errorf("issue lookup unavailable")

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed removal revalidation reported")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want presence kept while revalidation fails", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}
	if len(state.DispatchedKeysFull) == 0 {
		t.Fatal("dispatch evidence for the unrelated issue must still be persisted")
	}

	delete(mc.issueErr, 5)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the removal applied on the recovery poll", got)
	}
}

// TestPoll_StaleRemovalUndispatchedReAddHoldsWatermarkAndRecovers: the poll's
// snapshot predates a re-addition that has no dispatch evidence, so the stale
// removal is applied. A newer unrelated event would advance the watermark past
// the issue; it must be held so that, with an updated_after-honoring issue
// list, the next cycle rediscovers and dispatches the re-addition.
func TestPoll_StaleRemovalUndispatchedReAddHoldsWatermarkAndRecovers(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	staleRemovalFixture(mc)
	// Issue 5 is older than the watermark an advancing cycle would store.
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	startWatermark := recent.Add(-time.Hour).Format(time.RFC3339)

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the removal applied", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}
	if len(state.DispatchedKeysFull) == 0 {
		t.Fatal("dispatch evidence for the unrelated issue must still be persisted")
	}

	// The forge's issue list now reflects the re-addition on issue 5.
	mc.issues[0].Labels = []string{"ready-to-code"}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("re-addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want the re-addition recorded", got)
	}
}

// TestPoll_FailedAdditionRevalidationHoldsWatermarkAndRecovers: the poll
// dispatches an addition, but revalidating it before persistence fails, so the
// label presence is not recorded and no handoff is stored. A newer unrelated
// event would advance the watermark past the issue; it must be held so, with an
// updated_after-honoring issue list, the next cycle restores the presence
// without dispatching the same occurrence again.
func TestPoll_FailedAdditionRevalidationHoldsWatermarkAndRecovers(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	// Issue 5 is older than the watermark an advancing cycle would store.
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	startWatermark := recent.Add(-time.Hour).Format(time.RFC3339)
	failed := false
	mc.onBranchRef = func() {
		if failed || len(mc.pipelineCalls) == 0 {
			return
		}
		failed = true
		mc.issueErr[5] = fmt.Errorf("issue lookup unavailable")
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed addition revalidation reported")
	}
	if !failed {
		t.Fatal("lookup failure never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the unvalidated addition not recorded", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("dispatch evidence missing: %v", state.DispatchedKeysFull)
	}

	delete(mc.issueErr, 5)
	dispatched := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != dispatched {
		t.Fatalf("pipeline calls = %d, want %d: the recovery poll must not redispatch", len(mc.pipelineCalls), dispatched)
	}
	state, _ = mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want the presence restored on the recovery poll", got)
	}
}

// TestPoll_HeldWatermarkAlsoHoldsDispatchedKeyPrune: a catch-up poll dispatches
// an old addition (issue 5) and a recent unrelated one (issue 6), but
// revalidating issue 5's addition before persistence fails, so the watermark
// is held. The held watermark must hold the dispatched-key prune cutoff too:
// issue 5's key is older than the retention window behind the advanced
// watermark, and pruning it would let the recovery poll, which rediscovers
// issue 5 from the held watermark, dispatch the same occurrence again.
func TestPoll_HeldWatermarkAlsoHoldsDispatchedKeyPrune(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	old := recent.Add(-(dispatchedKeyRetention + time.Hour))
	mc.issues[0].UpdatedAt = old
	mc.issue[5].UpdatedAt = old
	mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(1, "add", "ready-to-code", alice, old)}
	startWatermark := old.Add(-time.Hour).Format(time.RFC3339)
	mc.setPollState(persistedPollState{LastPollAtFull: startWatermark})
	failed := false
	mc.onBranchRef = func() {
		if failed || len(mc.pipelineCalls) == 0 {
			return
		}
		failed = true
		mc.issueErr[5] = fmt.Errorf("issue lookup unavailable")
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed addition revalidation reported")
	}
	if !failed {
		t.Fatal("lookup failure never injected before persist")
	}
	state, _ := mc.getPollState()
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("dispatch evidence for the old addition was pruned: %v", state.DispatchedKeysFull)
	}

	delete(mc.issueErr, 5)
	dispatched := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != dispatched {
		t.Fatalf("pipeline calls = %d, want %d: the recovery poll must not redispatch", len(mc.pipelineCalls), dispatched)
	}
}

// TestPoll_DiscoveredAdditionRemovedBeforePersistNotRestored: the poll
// discovers an addition (its baseline lacks the label), then the label is
// removed on the forge — and the removal recorded — before the poll persists.
// The poll's raw label delta must not restore the stale presence.
func TestPoll_DiscoveredAdditionRemovedBeforePersistNotRestored(t *testing.T) {
	mc := newMockClient()
	webhookLabelAddFixture(mc)
	mc.setPollState(persistedPollState{
		LastPollAtFull: recent.Add(-time.Hour).Format(time.RFC3339),
	})
	removed := false
	mc.onBranchRef = func() {
		if removed || len(mc.pipelineCalls) == 0 {
			return
		}
		removed = true
		mc.issue[5].Labels = nil
		mc.labelEvents[5] = append(mc.labelEvents[5], labelEvent(2, "remove", "ready-to-code", alice, recent.Add(time.Minute)))
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if !removed {
		t.Fatal("removal never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the removed label's stale presence not restored", got)
	}
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("dispatch evidence missing: %v", state.DispatchedKeysFull)
	}
}

// TestPoll_SupersededAdditionDropsConcurrentStalePresence: the poll
// discovers e1 from an empty baseline, a webhook records e1's presence, and
// the label is then removed and re-added as an undispatched e3 before the
// poll persists. The poll drops its own addition, but the merge would keep
// the webhook's e1 presence and make the next poll treat e3 as already seen.
// The stale presence must be removed so e3 is rediscovered and dispatched
// even though no webhook arrives for it.
func TestPoll_SupersededAdditionDropsConcurrentStalePresence(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	startWatermark := recent.Add(-time.Hour).Format(time.RFC3339)
	replaced := false
	mc.onBranchRef = func() {
		if replaced || len(mc.pipelineCalls) == 0 {
			return
		}
		replaced = true
		// The webhook driver's concurrent write: e1's label presence.
		state, _ := mc.getPollState()
		state.LabelState = LabelState{5: {"ready-to-code"}}
		mc.setPollState(state)
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
			labelEvent(3, "add", "ready-to-code", alice, recent))
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if !replaced {
		t.Fatal("replacement addition never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the superseded occurrence's presence removed", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}

	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("replacement addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
}

// TestPoll_SupersededAdditionLookupFailureDropsConcurrentStalePresence: as
// TestPoll_SupersededAdditionDropsConcurrentStalePresence, but the issue
// lookup that revalidates the poll's addition fails transiently at persist
// time, so the poll cannot tell that e3 replaced e1. The webhook's e1
// presence must still not survive: holding the watermark alone would let the
// recovery poll see the label as already recorded and never discover e3.
func TestPoll_SupersededAdditionLookupFailureDropsConcurrentStalePresence(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	startWatermark := recent.Add(-time.Hour).Format(time.RFC3339)
	replaced := false
	mc.onBranchRef = func() {
		if replaced || len(mc.pipelineCalls) == 0 {
			return
		}
		replaced = true
		// The webhook driver's concurrent write: e1's label presence.
		state, _ := mc.getPollState()
		state.LabelState = LabelState{5: {"ready-to-code"}}
		mc.setPollState(state)
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
			labelEvent(3, "add", "ready-to-code", alice, recent))
		mc.issueErr[5] = fmt.Errorf("issue lookup unavailable")
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed addition revalidation reported")
	}
	if !replaced {
		t.Fatal("replacement addition never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the unresolved occurrence's presence removed", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}

	delete(mc.issueErr, 5)
	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("replacement addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
}

// TestPoll_FreshAdditionOnOldSnapshotKeepsDispatchKeyTimestamp: the issue
// snapshot is older than the retention window but its label was added just
// now. The dispatch key must carry the addition's own time, or an unrelated
// recent event advancing the watermark would prune it at once and a later
// webhook for the same occurrence would dispatch again.
func TestPoll_FreshAdditionOnOldSnapshotKeepsDispatchKeyTimestamp(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	old := recent.Add(-(dispatchedKeyRetention + time.Hour))
	mc.issues[0].UpdatedAt = old
	mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(1, "add", "ready-to-code", alice, recent)}
	mc.setPollState(persistedPollState{LastPollAtFull: old.Add(-time.Hour).Format(time.RFC3339)})

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) == 0 {
		t.Fatal("fresh addition was not dispatched")
	}
	state, _ := mc.getPollState()
	got, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]
	if !ok {
		t.Fatalf("dispatch key missing: %v", state.DispatchedKeysFull)
	}
	if got != recent.Unix() {
		t.Fatalf("dispatch key timestamp = %d, want the addition's time %d (snapshot time %d)", got, recent.Unix(), old.Unix())
	}
}

// TestPoll_FailedDiscoveredAdditionDropsConcurrentStalePresence: the poll
// starts with an empty LabelState baseline and discovers issue 5's
// replacement addition e3, but its dispatch fails. A webhook meanwhile
// committed presence for the superseded addition e1. Rolling the failed label
// out of the poll's snapshot is a no-op against the empty baseline, so the
// merge must still remove the webhook's presence; otherwise later polls see
// the label as already recorded and never retry e3.
func TestPoll_FailedDiscoveredAdditionDropsConcurrentStalePresence(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
		labelEvent(3, "add", "ready-to-code", alice, recent),
	}
	// Issue 5's e3 is dispatched first and fails; issue 6's addition then
	// succeeds, which lets the cycle reach label persistence.
	calls := 0
	mc.onPipeline = func() {
		calls++
		if calls == 1 {
			mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
		} else {
			mc.pipelineErr = nil
		}
	}
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) < 2 {
			return
		}
		injected = true
		// The webhook driver's concurrent write: e1's label presence.
		state, _ := mc.getPollState()
		state.LabelState = LabelState{5: {"ready-to-code"}}
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed e3 dispatch reported")
	}
	if !injected {
		t.Fatal("concurrent presence never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the stale e1 presence removed so e3 is retried", got)
	}

	mc.pipelineErr = nil
	mc.onPipeline = nil
	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("failed addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
}

// TestPoll_AllFailedDiscoveredAdditionDropsConcurrentStalePresence: same
// scenario as above, but the replacement addition e3 is the only discovered
// event, so every dispatch fails and the cycle takes the all-failed
// persistence path (watermark not advanced). That path must still remove the
// webhook's stale e1 presence; otherwise later polls treat the label as already
// recorded and never retry e3.
func TestPoll_AllFailedDiscoveredAdditionDropsConcurrentStalePresence(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	// Only issue 5 is discovered: its e3 dispatch is the cycle's sole event.
	mc.issues = mc.issues[:1]
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
		labelEvent(3, "add", "ready-to-code", alice, recent),
	}
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) < 1 {
			return
		}
		injected = true
		// The webhook driver's concurrent write: e1's label presence.
		state, _ := mc.getPollState()
		state.LabelState = LabelState{5: {"ready-to-code"}}
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed e3 dispatch reported")
	}
	if !injected {
		t.Fatal("concurrent presence never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the stale e1 presence removed so e3 is retried", got)
	}

	mc.pipelineErr = nil
	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("failed addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
}

// TestPoll_SupersededAdditionSurvivesCompetingWatermarkAdvance: as
// TestPoll_SupersededAdditionDropsConcurrentStalePresence, but a competing
// poll also advances the persisted watermark past issue 5 before this poll
// persists. Holding this writer's own watermark delta cannot restore the
// discovery range then, so the validated replacement e3 must be handed off
// durably as a pending label and dispatched by the recovery poll even though
// discovery no longer returns the issue.
func TestPoll_SupersededAdditionSurvivesCompetingWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	advanced := recent.Format(time.RFC3339)
	replaced := false
	mc.onBranchRef = func() {
		if replaced || len(mc.pipelineCalls) == 0 {
			return
		}
		replaced = true
		// The webhook driver's presence for e1 and a competing poll's
		// watermark, which has moved past issue 5's updated_at.
		state, _ := mc.getPollState()
		state.LabelState = LabelState{5: {"ready-to-code"}}
		state.LastPollAtFull = advanced
		mc.setPollState(state)
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
			labelEvent(3, "add", "ready-to-code", alice, recent))
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if !replaced {
		t.Fatal("replacement addition never injected before persist")
	}
	state, _ := mc.getPollState()
	if state.LastPollAtFull != advanced {
		t.Fatalf("watermark = %s, want the competing poll's %s preserved", state.LastPollAtFull, advanced)
	}
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the replacement e3 queued", state.PendingLabels)
	}

	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want 1 for the replacement e3", got)
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the dispatched occurrence cleared", state.PendingLabels)
	}
}

// TestPoll_StaleSnapshotHeldLabelSurvivesCompetingWatermarkAdvance: the poll's
// snapshot carries a label whose latest event is a removal, so the addition
// is held back. The label is then re-added (e3) and a competing poll persists
// a watermark past the issue. Holding this writer's watermark cannot recover
// the re-addition, so the held label must be queued as an unresolved marker
// that the next poll resolves and dispatches.
func TestPoll_StaleSnapshotHeldLabelSurvivesCompetingWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
	}
	advanced := recent.Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || mc.labelEventCalls == 0 {
			return
		}
		injected = true
		// The label is re-added after discovery, and a competing poll's
		// watermark moves past issue 5's updated_at.
		mc.issue[5].Labels = []string{"ready-to-code"}
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(3, "add", "ready-to-code", alice, time.Now().Add(-time.Minute)))
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if !injected {
		t.Fatal("competing watermark advance never injected before persist")
	}
	state, _ := mc.getPollState()
	if state.LastPollAtFull != advanced {
		t.Fatalf("watermark = %s, want the competing poll's %s preserved", state.LastPollAtFull, advanced)
	}
	markers := 0
	for _, pl := range state.PendingLabels {
		if pl.Unresolved && pl.IID == 5 && pl.Label == "ready-to-code" {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("PendingLabels = %+v, want one unresolved marker for issue 5", state.PendingLabels)
	}

	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("re-addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the resolved marker cleared", state.PendingLabels)
	}
}
