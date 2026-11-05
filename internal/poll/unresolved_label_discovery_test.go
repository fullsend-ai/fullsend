package poll

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestPoll_FailedAdditionRevalidationSurvivesCompetingWatermarkAdvance: the
// addition was dispatched, but revalidating it before persistence fails while
// a competing poll has already advanced the persisted watermark past the
// issue. Holding this writer's watermark cannot recover then, so the
// discovered occurrence must be handed off as pending: the recovery poll
// restores its label presence without creating another pipeline.
func TestPoll_FailedAdditionRevalidationSurvivesCompetingWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
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
		t.Fatal("poll Run: want the failed addition revalidation reported")
	}
	if !injected {
		t.Fatal("lookup failure never injected before persist")
	}
	state, _ := mc.getPollState()
	if state.LastPollAtFull != advanced {
		t.Fatalf("watermark = %s, want the competing poll's %s preserved", state.LastPollAtFull, advanced)
	}
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the dispatched addition e1 queued for reconciliation", state.PendingLabels)
	}

	delete(mc.issueErr, 5)
	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 0 {
		t.Fatalf("recovery poll dispatched %d pipelines, want none for the already dispatched addition", got)
	}
	state, _ = mc.getPollState()
	if got := state.LabelState[5]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Fatalf("LabelState[5] = %v, want the presence restored on the recovery poll", got)
	}
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the reconciled occurrence cleared", state.PendingLabels)
	}
}

// TestPoll_DiscoveryLookupFailureDropsConcurrentStalePresence: this poll's
// baseline lacked the label, and its occurrence lookup fails during
// discovery. A webhook meanwhile records presence for addition e1, and the
// label is then removed and re-added as e3. Restoring the previous snapshot
// is a no-op relative to the empty baseline, so the merge would keep e1's
// presence and the recovery poll would treat the label as already seen,
// never discovering e3. The unresolved presence must be removed.
func TestPoll_DiscoveryLookupFailureDropsConcurrentStalePresence(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEventsErr[5] = fmt.Errorf("label events unavailable")
	// The watermark is held just behind the issue whose lookup failed.
	startWatermark := mc.issues[0].UpdatedAt.Add(-30 * time.Second).Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || mc.labelEventCalls == 0 {
			return
		}
		injected = true
		// The webhook driver's concurrent write: e1's label presence.
		state, _ := mc.getPollState()
		state.LabelState = LabelState{5: {"ready-to-code"}}
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the label event lookup failure reported")
	}
	if !injected {
		t.Fatal("concurrent presence never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the unresolved occurrence's presence removed", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}

	delete(mc.labelEventsErr, 5)
	mc.onBranchRef = nil
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
		labelEvent(3, "add", "ready-to-code", alice, recent),
	}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("replacement addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
}

// TestPoll_DiscoveryLookupFailureSurvivesCompetingWatermarkAdvance: the
// occurrence lookup fails during discovery, and a competing poll then
// persists a watermark past the issue's updated_at. Holding this writer's
// watermark cannot recover the addition (later discovery excludes the issue),
// so the lookup failure must be queued as durable issue-level work: once the
// lookup recovers, the label is removed and re-added as e3, and the poller
// resolves and dispatches e3 without any watermark help.
func TestPoll_DiscoveryLookupFailureSurvivesCompetingWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEventsErr[5] = fmt.Errorf("label events unavailable")
	advanced := recent.Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || mc.labelEventCalls == 0 {
			return
		}
		injected = true
		// A competing poll's watermark has moved past issue 5's updated_at.
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the label event lookup failure reported")
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

	delete(mc.labelEventsErr, 5)
	mc.onBranchRef = nil
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
		labelEvent(3, "add", "ready-to-code", alice, time.Now().Add(-time.Minute)),
	}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("replacement addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the resolved marker cleared", state.PendingLabels)
	}
}

// TestPoll_SupersededFailedRevalidationQueuesReplacement: this poll
// dispatched e1; the label is then removed and re-added as e3 and a competing
// poll advances the watermark before the revalidation lookup fails. Queuing
// only the old occurrence e1 would clear it as no longer current and lose e3,
// so the issue-level marker must lead the recovery poll to dispatch e3 (and
// not dispatch e1 again).
func TestPoll_SupersededFailedRevalidationQueuesReplacement(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
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
		mc.labelEvents[5] = append(mc.labelEvents[5],
			labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
			labelEvent(3, "add", "ready-to-code", alice, time.Now().Add(-time.Minute)))
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
		mc.issueErr[5] = fmt.Errorf("issue lookup unavailable")
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed addition revalidation reported")
	}
	if !injected {
		t.Fatal("supersession never injected before persist")
	}

	delete(mc.issueErr, 5)
	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want exactly the replacement e3", got)
	}
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("replacement addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want everything resolved", state.PendingLabels)
	}
}

// TestPoll_MarkerResolvingToDiscoveredOccurrenceKeepsPendingOwnership: an
// unresolved marker resolves to the occurrence (e1) that normal discovery
// already queued in the same cycle, and its dispatch fails. Clearing the
// marker must not leave e1 held only by the retry count and the watermark
// holdback: a competing poll advances the persisted watermark past the issue
// before this cycle persists, so e1 must be handed off as a pending
// occurrence for the recovery poll to dispatch it.
func TestPoll_MarkerResolvingToDiscoveredOccurrenceKeepsPendingOwnership(t *testing.T) {
	mc := newMockClient()
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	markerKey, marker := unresolvedLabelMarker(5, "ready-to-code", recent.Add(-time.Minute))
	mc.setPollState(persistedPollState{
		LastPollAtFull: recent.Add(-time.Hour).Format(time.RFC3339),
		PendingLabels:  map[string]PendingLabel{markerKey: marker},
	})
	// Issue 5's e1 is dispatched first and fails; issue 6's addition then
	// succeeds, which lets the cycle reach persistence.
	calls := 0
	mc.onPipeline = func() {
		calls++
		if calls == 1 {
			mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
		} else {
			mc.pipelineErr = nil
		}
	}
	advanced := recent.Add(time.Minute).Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) < 2 {
			return
		}
		injected = true
		// A competing poll's watermark moves past issue 5's updated_at.
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
	if _, ok := state.PendingLabels[markerKey]; ok {
		t.Fatalf("PendingLabels = %+v, want the resolved marker cleared", state.PendingLabels)
	}
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the failed discovered occurrence e1 owned as pending", state.PendingLabels)
	}

	mc.pipelineErr = nil
	mc.onPipeline = nil
	mc.onBranchRef = nil
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("failed occurrence never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want everything resolved", state.PendingLabels)
	}
}

// TestPoll_NotesLookupFailureDropsConcurrentStalePresence: this poll's
// baseline lacks the label and the issue's notes lookup fails, so discovery
// restores an empty snapshot without resolving the addition. A webhook
// meanwhile records presence for addition e1, and the label is then removed
// and re-added as e3. Without an unresolved marker the merge would keep e1's
// presence and the recovery poll would treat the label as already seen,
// never discovering e3.
func TestPoll_NotesLookupFailureDropsConcurrentStalePresence(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.noteErr[5] = fmt.Errorf("notes unavailable")
	// The watermark is held just behind the issue whose lookup failed.
	startWatermark := mc.issues[0].UpdatedAt.Add(-30 * time.Second).Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected {
			return
		}
		injected = true
		// The webhook driver's concurrent write: e1's label presence.
		state, _ := mc.getPollState()
		state.LabelState = LabelState{5: {"ready-to-code"}}
		mc.setPollState(state)
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Logf("poll Run: %v", err)
	}
	if !injected {
		t.Fatal("concurrent presence never injected before persist")
	}
	state, _ := mc.getPollState()
	if got := state.LabelState[5]; len(got) != 0 {
		t.Fatalf("LabelState[5] = %v, want the unresolved occurrence's presence removed", got)
	}
	if state.LastPollAtFull != startWatermark {
		t.Fatalf("watermark = %s, want it held at %s", state.LastPollAtFull, startWatermark)
	}

	delete(mc.noteErr, 5)
	mc.onBranchRef = nil
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
		labelEvent(2, "remove", "ready-to-code", alice, recent.Add(-10*time.Minute)),
		labelEvent(3, "add", "ready-to-code", alice, recent),
	}
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["code:issue_label-5-ready-to-code-e3"]; !ok {
		t.Fatalf("replacement addition never dispatched on the recovery poll: %v", state.DispatchedKeysFull)
	}
}
