package dispatch

import (
	"sync"
	"testing"
)

func TestIsAutomaticReviewHandoff(t *testing.T) {
	t.Parallel()

	ready := &TransitionLabel{Name: "ready-for-review", Action: "added"}
	handoffLabels := []string{"ready-for-review", autoReviewHandoffLabel}

	tests := []struct {
		name  string
		event *NormalizedEvent
		want  bool
	}{
		{name: "nil event", event: nil, want: false},
		{
			name:  "nil label",
			event: &NormalizedEvent{Transition: Transition{Kind: "label_changed"}},
			want:  false,
		},
		{
			name: "wrong transition kind",
			event: &NormalizedEvent{
				Transition: Transition{Kind: "opened", Label: ready},
				State:      State{Labels: handoffLabels},
			},
			want: false,
		},
		{
			name: "label removed",
			event: &NormalizedEvent{
				Transition: Transition{
					Kind:  "label_changed",
					Label: &TransitionLabel{Name: "ready-for-review", Action: "removed"},
				},
				State: State{Labels: handoffLabels},
			},
			want: false,
		},
		{
			name: "different label with provenance present",
			event: &NormalizedEvent{
				Transition: Transition{
					Kind:  "label_changed",
					Label: &TransitionLabel{Name: "ready-to-code", Action: "added"},
				},
				State: State{Labels: append(handoffLabels, "ready-to-code")},
			},
			want: false,
		},
		{
			name: "ready-for-review without provenance",
			event: &NormalizedEvent{
				Transition: Transition{Kind: "label_changed", Label: ready},
				State:      State{Labels: []string{"ready-for-review"}},
			},
			want: false,
		},
		{
			name: "ready-for-review with provenance",
			event: &NormalizedEvent{
				Transition: Transition{Kind: "label_changed", Label: ready},
				State:      State{Labels: handoffLabels},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isAutomaticReviewHandoff(tt.event); got != tt.want {
				t.Fatalf("isAutomaticReviewHandoff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHarnessRouter_AutomaticHandoffSkipIgnoresActorIdentity(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})

	actors := []Actor{
		{ID: "alice", Kind: "human", Role: "write"},
		{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"},
		{ID: "unrelated[bot]", Kind: "bot", Role: "write"},
		{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "none"},
	}

	for _, actor := range actors {
		event := labeledReviewEvent(actor, []string{"ready-for-review", autoReviewHandoffLabel})
		stages, err := r.Route(event)
		if err != nil {
			t.Fatalf("actor %s: unexpected error: %v", actor.ID, err)
		}
		if len(stages) != 0 {
			t.Fatalf("actor %s: automatic handoff must skip regardless of actor identity, got %v", actor.ID, stages)
		}
	}
}

func TestHarnessRouter_ExplicitReadyForReviewDoesNotUseBotName(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})

	// A bot-applied ready-for-review without provenance is an explicit
	// request, not a suppressed handoff. Authorization still applies.
	event := labeledReviewEvent(
		Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"},
		[]string{"ready-for-review"},
	)
	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "review" {
		t.Fatalf("expected [review] for bot-applied explicit label, got %v", stages)
	}
}

func TestHarnessRouter_ExplicitReadyForReviewStillRequiresWrite(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})

	event := labeledReviewEvent(
		Actor{ID: "guest", Kind: "human", Role: "read"},
		[]string{"ready-for-review"},
	)
	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for read-only explicit label, got %v", stages)
	}
}

func TestHarnessRouter_OpenedDispatchesEvenWhenHandoffLabelPresent(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 8},
		Transition: Transition{Kind: "opened"},
		Actor:      Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"},
		State:      State{Labels: []string{autoReviewHandoffLabel, "ready-for-review"}},
	}
	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "review" {
		t.Fatalf("expected opened to dispatch review, got %v", stages)
	}
}

func TestHarnessRouter_ReviewDispatchScenarios(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review", "fix", "code"})

	alice := Actor{ID: "alice", Kind: "human", Role: "write"}
	bot := Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"}
	headA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	opened := func(actor Actor, sha string, labels []string) *NormalizedEvent {
		return &NormalizedEvent{
			Entity:     Entity{Kind: "change_proposal", ID: 42},
			Transition: Transition{Kind: "opened"},
			Actor:      actor,
			State: State{
				Labels: labels,
				ChangeProposal: &ChangeProposalState{
					ID: 42, HeadSHA: sha, IsFork: false,
				},
			},
		}
	}
	slashReview := func(actor Actor, sha string) *NormalizedEvent {
		return &NormalizedEvent{
			Entity: Entity{Kind: "change_proposal", ID: 42},
			Transition: Transition{
				Kind:    "comment_added",
				Comment: &TransitionComment{Command: "/fs-review", Body: "/fs-review"},
			},
			Actor: actor,
			State: State{
				ChangeProposal: &ChangeProposalState{ID: 42, HeadSHA: sha, IsFork: false},
			},
		}
	}

	tests := []struct {
		name       string
		events     []*NormalizedEvent
		wantReview int
	}{
		{
			name: "opened then automatic labeled: one review",
			events: []*NormalizedEvent{
				opened(bot, headA, []string{autoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(bot, []string{autoReviewHandoffLabel, "ready-for-review"}),
			},
			wantReview: 1,
		},
		{
			name: "automatic labeled then opened: one review",
			events: []*NormalizedEvent{
				labeledReviewEvent(bot, []string{autoReviewHandoffLabel, "ready-for-review"}),
				opened(bot, headA, []string{autoReviewHandoffLabel, "ready-for-review"}),
			},
			wantReview: 1,
		},
		{
			name: "opened then explicit labeled: two reviews (same SHA re-request)",
			events: []*NormalizedEvent{
				opened(bot, headA, nil),
				labeledReviewEvent(alice, []string{"ready-for-review"}),
			},
			wantReview: 2,
		},
		{
			name: "explicit labeled after provenance consumed: review",
			events: []*NormalizedEvent{
				opened(bot, headA, []string{autoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(bot, []string{autoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(alice, []string{"ready-for-review"}),
			},
			wantReview: 2,
		},
		{
			name: "/fs-review requests another review of the same SHA",
			events: []*NormalizedEvent{
				opened(bot, headA, []string{autoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(bot, []string{autoReviewHandoffLabel, "ready-for-review"}),
				slashReview(alice, headA),
			},
			wantReview: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := countReviewDispatches(t, r, tt.events)
			if got != tt.wantReview {
				t.Fatalf("review dispatches = %d, want %d", got, tt.wantReview)
			}
		})
	}

}

func TestHarnessRouter_ConcurrentOpenedAndAutomaticLabel(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})
	bot := Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"}

	opened := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 7},
		Transition: Transition{Kind: "opened"},
		Actor:      bot,
		State: State{
			Labels:         []string{autoReviewHandoffLabel, "ready-for-review"},
			ChangeProposal: &ChangeProposalState{ID: 7, IsFork: false},
		},
	}
	labeled := labeledReviewEvent(bot, []string{autoReviewHandoffLabel, "ready-for-review"})

	const iterations = 32
	for i := 0; i < iterations; i++ {
		events := []*NormalizedEvent{opened, labeled}
		if i%2 == 1 {
			events[0], events[1] = events[1], events[0]
		}
		got := countReviewDispatchesConcurrent(t, r, events)
		if got != 1 {
			t.Fatalf("iteration %d: concurrent automatic pair dispatched %d reviews, want 1", i, got)
		}
	}
}

func TestHarnessRouter_ConcurrentOpenedAndExplicitLabel(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})
	bot := Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"}
	alice := Actor{ID: "alice", Kind: "human", Role: "write"}

	opened := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 7},
		Transition: Transition{Kind: "opened"},
		Actor:      bot,
		State:      State{ChangeProposal: &ChangeProposalState{ID: 7, IsFork: false}},
	}
	labeled := labeledReviewEvent(alice, []string{"ready-for-review"})

	const iterations = 32
	for i := 0; i < iterations; i++ {
		got := countReviewDispatchesConcurrent(t, r, []*NormalizedEvent{opened, labeled})
		if got != 2 {
			t.Fatalf("iteration %d: explicit same-revision pair dispatched %d reviews, want 2", i, got)
		}
	}
}

func labeledReviewEvent(actor Actor, labels []string) *NormalizedEvent {
	return &NormalizedEvent{
		Entity: Entity{Kind: "change_proposal", ID: 42},
		Transition: Transition{
			Kind:  "label_changed",
			Label: &TransitionLabel{Name: "ready-for-review", Action: "added"},
		},
		Actor: actor,
		State: State{
			Labels:         labels,
			ChangeProposal: &ChangeProposalState{ID: 42, IsFork: false},
		},
	}
}

func countReviewDispatches(t *testing.T, r *HarnessRouter, events []*NormalizedEvent) int {
	t.Helper()
	count := 0
	for _, ev := range events {
		stages, err := r.Route(ev)
		if err != nil {
			t.Fatalf("route error: %v", err)
		}
		for _, s := range stages {
			if s == "review" {
				count++
			}
		}
	}
	return count
}

func countReviewDispatchesConcurrent(t *testing.T, r *HarnessRouter, events []*NormalizedEvent) int {
	t.Helper()
	var (
		mu    sync.Mutex
		count int
		wg    sync.WaitGroup
	)
	wg.Add(len(events))
	for _, ev := range events {
		go func(e *NormalizedEvent) {
			defer wg.Done()
			stages, err := r.Route(e)
			if err != nil {
				t.Errorf("route error: %v", err)
				return
			}
			n := 0
			for _, s := range stages {
				if s == "review" {
					n++
				}
			}
			mu.Lock()
			count += n
			mu.Unlock()
		}(ev)
	}
	wg.Wait()
	return count
}
