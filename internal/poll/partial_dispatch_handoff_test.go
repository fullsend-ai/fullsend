package poll

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// twoStageRouter routes every label addition to two stages.
type twoStageRouter struct{}

func (twoStageRouter) Route(event *dispatch.NormalizedEvent) ([]string, error) {
	if event.Transition.Label == nil || event.Transition.Label.Action != "added" {
		return nil, nil
	}
	return []string{"code", "review"}, nil
}

// TestPoll_PartialMultiStageDispatchFailureSurvivesCompetingWatermarkAdvance:
// a discovered label addition routes to two stages; the first dispatches and
// the second fails, while a competing poll persists a watermark past the
// issue. The successful stage must not clear the failure count: the failed
// stage has no dispatch key, so the occurrence must be handed off as pending,
// and the recovery poll must dispatch only the failed stage.
func TestPoll_PartialMultiStageDispatchFailureSurvivesCompetingWatermarkAdvance(t *testing.T) {
	mc := newMockClient()
	mc.issuesHonorSince = true
	pollInDiscoveryWithTrigger(mc, []string{"ready-to-code"}, nil)
	mc.issues = mc.issues[:1]
	mc.issues[0].UpdatedAt = recent.Add(-10 * time.Minute)
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", alice, recent.Add(-20*time.Minute)),
	}
	mc.memberLevel[alice.ID] = 30
	// The first stage dispatches, the second fails.
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
	mc.pipelineErrAfter = 1
	advanced := recent.Format(time.RFC3339)
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) < 2 {
			return
		}
		injected = true
		state, _ := mc.getPollState()
		state.LastPollAtFull = advanced
		mc.setPollState(state)
	}
	newPoller := func() *Poller {
		return New(mc, twoStageRouter{}, "group/project", withTestSecret(Options{Mode: "events", BotUserID: 100}))
	}

	if err := newPoller().Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed second-stage dispatch reported")
	}
	if !injected {
		t.Fatal("competing watermark advance never injected before persist")
	}
	if got := len(mc.pipelineCalls); got != 2 {
		t.Fatalf("pipeline calls = %d, want 2 (one success, one failure)", got)
	}
	state, _ := mc.getPollState()
	if _, ok := state.PendingLabels["issue_label-5-ready-to-code-e1"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the partially dispatched occurrence queued", state.PendingLabels)
	}

	mc.pipelineErr = nil
	mc.pipelineErrAfter = 0
	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := newPoller().Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want 1 for the failed stage only", got)
	}
	state, _ = mc.getPollState()
	if len(state.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the occurrence cleared", state.PendingLabels)
	}
}

// TestPoll_SupersededPendingOccurrenceQueuesReplacement: a pending-only
// occurrence (e3) dispatches successfully but a newer remove/re-add (e5) has
// replaced it. With the label still recorded and the watermark past the
// issue, neither discovery nor a retry would see e5, so reconciliation must
// hand it off as pending, and the next poll must dispatch it.
func TestPoll_SupersededPendingOccurrenceQueuesReplacement(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.labelEvents[5] = append(mc.labelEvents[5],
		labelEvent(4, "remove", "ready-to-code", alice, recent.Add(time.Minute)),
		labelEvent(5, "add", "ready-to-code", alice, recent.Add(2*time.Minute)))
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
	if got := len(mc.pipelineCalls); got != 1 {
		t.Fatalf("pipeline calls = %d, want 1 for the pending e3", got)
	}
	after, _ := mc.getPollState()
	if _, ok := after.PendingLabels["issue_label-5-ready-to-code-e5"]; !ok {
		t.Fatalf("PendingLabels = %+v, want the undispatched replacement e5 queued", after.PendingLabels)
	}
	if _, ok := after.PendingLabels["issue_label-5-ready-to-code-e3"]; ok {
		t.Fatalf("PendingLabels = %+v, want the dispatched e3 cleared", after.PendingLabels)
	}

	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("second poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls); got != 2 {
		t.Fatalf("pipeline calls = %d, want 2 after the replacement e5 is retried", got)
	}
	after, _ = mc.getPollState()
	if len(after.PendingLabels) != 0 {
		t.Fatalf("PendingLabels = %+v, want the replacement cleared", after.PendingLabels)
	}
}

// TestPoll_SupersededPendingOccurrenceLookupFailureQueuesMarker: as above,
// but the replacement lookup fails at persist time. The failure must be
// reported and an unresolved marker queued so recovery does not depend on
// the watermark.
func TestPoll_SupersededPendingOccurrenceLookupFailureQueuesMarker(t *testing.T) {
	mc := newMockClient()
	earlier := reAddFixture(mc)
	mc.labelEvents[5] = append(mc.labelEvents[5],
		labelEvent(4, "remove", "ready-to-code", alice, recent.Add(time.Minute)),
		labelEvent(5, "add", "ready-to-code", alice, recent.Add(2*time.Minute)))
	mc.issues = nil
	state := reAddState(earlier, recent.Add(10*time.Minute))
	state.PendingLabels = map[string]PendingLabel{
		"issue_label-5-ready-to-code-e3": {
			IID: 5, Label: "ready-to-code", EventID: 3, At: recent.UnixMilli(),
			Labels: []string{"ready-to-code"}, ActorID: alice.ID, ActorLogin: alice.Username,
		},
	}
	mc.setPollState(state)
	injected := false
	mc.onBranchRef = func() {
		if injected || len(mc.pipelineCalls) == 0 {
			return
		}
		injected = true
		// The currency check (first lookup) succeeds and finds e3
		// superseded; the replacement lookup (second) fails.
		mc.labelEventsFailAfter = mc.labelEventCalls + 1
	}

	if err := eventsPoller(mc).Run(context.Background()); err == nil {
		t.Fatal("poll Run: want the failed replacement lookup reported")
	}
	if !injected {
		t.Fatal("lookup failure never injected before persist")
	}
	after, _ := mc.getPollState()
	markers := 0
	for _, pl := range after.PendingLabels {
		if pl.Unresolved && pl.IID == 5 && pl.Label == "ready-to-code" {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("PendingLabels = %+v, want one unresolved marker for issue 5", after.PendingLabels)
	}

	mc.labelEventsFailAfter = 0
	mc.onBranchRef = nil
	before := len(mc.pipelineCalls)
	if err := eventsPoller(mc).Run(context.Background()); err != nil {
		t.Fatalf("recovery poll Run: %v", err)
	}
	if got := len(mc.pipelineCalls) - before; got != 1 {
		t.Fatalf("recovery poll dispatched %d pipelines, want 1 for the replacement e5", got)
	}
}
