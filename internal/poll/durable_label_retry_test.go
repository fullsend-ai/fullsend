package poll

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestPoll_FailedDiscoveredAdditionSurvivesCompetingWatermarkAdvance: a
// poll-discovered label addition fails to dispatch while no marker or pending
// entry backs it, and a competing poll persists a watermark past the issue
// before this poll persists. The failure count alone cannot reconstruct the
// occurrence once updated_after discovery excludes the issue, so the failed
// occurrence must be handed off as pending, bound to its own actor, and the
// recovery poll must dispatch it without discovery's help.
func TestPoll_FailedDiscoveredAdditionSurvivesCompetingWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	// Only issue 5 is discovered: its e1 dispatch is the cycle's sole event.
	mc.issues = mc.issues[:1]
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
	advanced := recent.Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) < 1 {
			return
		}
		injected = true
		// A competing poll's watermark has moved past issue 5's updated_at.
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed e1 dispatch reported")
	}
	if !injected {
		t.Fatal("competing watermark advance never injected before persist")
	}
	state, _ := mc.getPollState()
	if state.LastPollAtFull != advanced {
		t.Fatalf("watermark = %s, want the competing poll's %s preserved", state.LastPollAtFull, advanced)
	}
	pl, ok := state.PendingLabels["issue_label-5-ready-to-code-e1"]
	if !ok {
		t.Fatalf("PendingLabels = %+v, want the failed occurrence e1 queued", state.PendingLabels)
	}
	if pl.Unresolved || pl.ActorID != alice.ID || pl.ActorLogin != alice.Username {
		t.Fatalf("pending e1 = %+v, want a known occurrence bound to its actor", pl)
	}

	mc.pipelineErr = nil
	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want 1 for the failed e1", got)
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the dispatched occurrence cleared", state.PendingLabels)
	}
}

// TestPoll_FailedRemovalRevalidationSurvivesCompetingWatermarkAdvance: the
// poll's issue snapshot lacks a label that state still records, and the
// forge now carries an undispatched re-addition. Revalidating the removal
// fails while a competing poll has advanced the persisted watermark past the
// issue. Holding this writer's watermark cannot recover then, and the
// retained presence would suppress the re-addition on later updates, so the
// failure must queue an unresolved marker that the recovery poll resolves to
// the re-addition independent of the watermark.
func TestPoll_FailedRemovalRevalidationSurvivesCompetingWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	staleRemovalFixture(mc)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	advanced := recent.Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) == 0 {
			return
		}
		injected = true
		// A competing poll's watermark has moved past issue 5's updated_at.
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
		mc.issueErr[5] = fmt.Errorf("issue lookup unavailable")
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed removal revalidation reported")
	}
	if !injected {
		t.Fatal("lookup failure never injected before persist")
	}
	state, _ := mc.getPollState()
	if state.LastPollAtFull != advanced {
		t.Fatalf("watermark = %s, want the competing poll's %s preserved", state.LastPollAtFull, advanced)
	}
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want presence kept while revalidation fails", got)
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

	delete(mc.issueErr, 5)
	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want 1 for the re-addition e3", got)
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the resolved marker cleared", state.PendingLabels)
	}
}

// TestPoll_PendingRetryKeepsOccurrenceTime: a fresh label addition is seen
// through an issue snapshot older than the dispatch-key retention window and
// its dispatch fails while a competing poll advances the watermark past the
// issue. The pending-only retry that later succeeds must record the dispatch
// key under the addition's own time, not the old snapshot time, or an
// advancing watermark would prune it at once and a still-fresh webhook for
// the same occurrence would dispatch again.
func TestPoll_PendingRetryKeepsOccurrenceTime(t *testing.T) {
	// The pending-only retry advances the watermark to the wall clock, so the
	// times here are relative to now rather than the fixed `recent`.
	now := time.Now().Truncate(time.Second)
	occurred := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues = mc.issues[:1]
	old := now.Add(-(dispatchedKeyRetention + time.Hour))
	mc.issues[0].UpdatedAt = old
	mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(1, "add", "ready-to-code", alice, occurred)}
	mc.setPollState(persistedPollState{LastPollAtFull: old.Add(-time.Hour).Format(time.RFC3339)})
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
	advanced := now.Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) < 1 {
			return
		}
		injected = true
		// A competing poll's watermark has moved past issue 5's updated_at.
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed dispatch reported")
	}
	if !injected {
		t.Fatal("competing watermark advance never injected before persist")
	}
	state, _ := mc.getPollState()
	pl, ok := state.PendingLabels["issue_label-5-ready-to-code-e1"]
	if !ok {
		t.Fatalf("PendingLabels = %+v, want the failed occurrence queued", state.PendingLabels)
	}
	if pl.OccurredAt != occurred.UnixMilli() {
		t.Fatalf("pending OccurredAt = %d, want the addition's time %d", pl.OccurredAt, occurred.UnixMilli())
	}

	mc.pipelineErr = nil
	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	got, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]
	if !ok {
		t.Fatalf("dispatch key missing after pending-only retry: %v", state.DispatchedKeysFull)
	}
	if got != occurred.Unix() {
		t.Fatalf("dispatch key timestamp = %d, want the addition's time %d (snapshot time %d)", got, occurred.Unix(), old.Unix())
	}
}

// TestPendingLabel_OccurredAtRoundTrip: the occurrence time survives the
// pending handoff independently of the snapshot time, and a handoff without
// it (written before the field existed) rebuilds an event with no
// occurrence time.
func TestPendingLabel_OccurredAtRoundTrip(t *testing.T) {
	snapshot := recent.Add(-2 * time.Hour)
	event := RoutableEvent{Type: "issue_label", IID: 5, ChangedLabel: "ready-to-code", LabelEventID: 1, UpdatedAt: snapshot, OccurredAt: recent}
	got := pendingLabelFor(event).routableEvent()
	if !got.UpdatedAt.Equal(snapshot.Truncate(time.Millisecond)) || !got.OccurredAt.Equal(recent.Truncate(time.Millisecond)) {
		t.Fatalf("round trip UpdatedAt=%v OccurredAt=%v, want %v and %v", got.UpdatedAt, got.OccurredAt, snapshot, recent)
	}
	legacy := PendingLabel{IID: 5, Label: "ready-to-code", EventID: 1, At: snapshot.UnixMilli()}.routableEvent()
	if !legacy.OccurredAt.IsZero() {
		t.Fatalf("legacy handoff OccurredAt = %v, want zero", legacy.OccurredAt)
	}
}

// TestPoll_SupersededAdditionReplacementLookupFailureQueuesMarker: the
// discovered addition e1 is superseded by an undispatched e3, and the
// follow-up replacement lookup fails while a competing poll has advanced the
// persisted watermark past the issue. The failure must be reported and an
// unresolved marker queued, so recovery does not depend on the watermark and
// dispatches e3 once the lookup recovers.
func TestPoll_SupersededAdditionReplacementLookupFailureQueuesMarker(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues = mc.issues[:1]
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	advanced := recent.Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) == 0 {
			return
		}
		injected = true
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
			labelEvent(3, "add", "ready-to-code", alice, recent))
		// The occurrence check (first lookup) succeeds and finds e1
		// superseded; the replacement lookup (second) fails.
		mc.labelEventsFailAfter = mc.labelEventCalls + 1
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed replacement lookup reported")
	}
	if !injected {
		t.Fatal("replacement addition never injected before persist")
	}
	state, _ := mc.getPollState()
	if state.LastPollAtFull != advanced {
		t.Fatalf("watermark = %s, want the competing poll's %s preserved", state.LastPollAtFull, advanced)
	}
	unresolved := 0
	for _, pl := range state.PendingLabels {
		if pl.Unresolved && pl.IID == 5 && pl.Label == "ready-to-code" {
			unresolved++
		}
	}
	if unresolved == 0 {
		t.Fatalf("PendingLabels = %+v, want an unresolved marker for issue 5", state.PendingLabels)
	}

	mc.labelEventsFailAfter = 0
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
		t.Fatalf("PendingLabels = %+v, want the marker cleared", state.PendingLabels)
	}
}

// exhaustedPendingFixture sets up a pending occurrence e1 for issue 5 whose
// label was removed and re-added (e3, undispatched, by the same actor) while
// LabelState still records the label and the watermark excludes the issue, so
// only reconciliation can find e3. failed is e1's recorded retry count.
func exhaustedPendingFixture(mc *mockClient, failed int) {
	earlier := reAddFixture(mc)
	mc.issues = nil
	state := reAddState(earlier, recent.Add(10*time.Minute))
	state.DispatchedKeysFull = nil
	state.FailedKeysFull = map[string]int{"issue_label-5-ready-to-code-e1": failed}
	state.PendingLabels = map[string]PendingLabel{
		"issue_label-5-ready-to-code-e1": {
			IID: 5, Label: "ready-to-code", EventID: 1, At: earlier.UnixMilli(),
			Labels: []string{"ready-to-code"}, ActorID: alice.ID, ActorLogin: alice.Username,
		},
	}
	mc.setPollState(state)
}

func requireReplacementQueuedAndDispatched(t *testing.T, mc *mockClient) {
	t.Helper()
	state, _ := mc.getPollState()
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e1"]; ok {
		t.Fatalf("PendingLabels = %+v, want the exhausted e1 retired", state.PendingLabels)
	}
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the superseding e3 queued", state.PendingLabels)
	}
	mc.pipelineErr = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want 1 for e3 only", got)
	}
}

// TestPoll_AlreadyExhaustedPendingQueuesSupersedingAddition: retiring a pending
// occurrence whose budget is already spent must not lose a later remove/re-add
// that LabelState and the watermark hide from discovery.
func TestPoll_AlreadyExhaustedPendingQueuesSupersedingAddition(t *testing.T) {
	mc := newMockClient()
	exhaustedPendingFixture(mc, maxEventRetries)

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Fatalf("pipeline calls = %d, want the exhausted e1 not dispatched", len(mc.pipelineCalls))
	}
	requireReplacementQueuedAndDispatched(t, mc)
}

// TestPoll_FinalAttemptFailureQueuesSupersedingAddition: the same hand-off when
// the pending occurrence exhausts its budget on this cycle's failed dispatch.
func TestPoll_FinalAttemptFailureQueuesSupersedingAddition(t *testing.T) {
	mc := newMockClient()
	exhaustedPendingFixture(mc, maxEventRetries-1)
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed e1 dispatch reported")
	}
	requireReplacementQueuedAndDispatched(t, mc)
}

// TestPoll_ExhaustedPendingStillCurrentIsNotRequeued: an exhausted occurrence
// that is still the issue's current addition is retired for good, not handed
// off again with a restarted budget.
func TestPoll_ExhaustedPendingStillCurrentIsNotRequeued(t *testing.T) {
	mc := newMockClient()
	exhaustedPendingFixture(mc, maxEventRetries)
	mc.labelEvents[5] = mc.labelEvents[5][:1] // only e1: it is still current

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("poll Run: %v", err)
	}
	state, _ := mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the exhausted current occurrence retired", state.PendingLabels)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Fatalf("pipeline calls = %d, want none", len(mc.pipelineCalls))
	}
}
