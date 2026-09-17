package dispatch

import (
	"sync"
	"testing"
)

func TestIsAutomaticReviewHandoff(t *testing.T) {
	t.Parallel()

	ready := &TransitionLabel{Name: "ready-for-review", Action: "added"}
	handoffLabels := []string{"ready-for-review", AutoReviewHandoffLabel}

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
			if got := IsAutomaticReviewHandoff(tt.event); got != tt.want {
				t.Fatalf("IsAutomaticReviewHandoff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAutomaticHandoffSkipIgnoresActorIdentity(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})

	actors := []Actor{
		{ID: "alice", Kind: "human", Role: "write"},
		{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"},
		{ID: "unrelated[bot]", Kind: "bot", Role: "write"},
		{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "none"},
	}

	for _, actor := range actors {
		event := labeledReviewEvent(actor, []string{"ready-for-review", AutoReviewHandoffLabel})
		stages, err := r.Route(event)
		if err != nil {
			t.Fatalf("actor %s: unexpected error: %v", actor.ID, err)
		}
		if len(stages) != 0 {
			t.Fatalf("actor %s: automatic handoff must skip regardless of actor identity, got %v", actor.ID, stages)
		}
	}
}

func TestExplicitReadyForReviewDoesNotUseBotName(t *testing.T) {
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

func TestExplicitReadyForReviewStillRequiresWrite(t *testing.T) {
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

func TestOpenedDispatchesEvenWhenHandoffLabelPresent(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 8},
		Transition: Transition{Kind: "opened"},
		Actor:      Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"},
		State:      State{Labels: []string{AutoReviewHandoffLabel, "ready-for-review"}},
	}
	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "review" {
		t.Fatalf("expected opened to dispatch review, got %v", stages)
	}
}

func TestReviewDispatchScenarios(t *testing.T) {
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
				opened(bot, headA, []string{AutoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(bot, []string{AutoReviewHandoffLabel, "ready-for-review"}),
			},
			wantReview: 1,
		},
		{
			name: "automatic labeled then opened: one review",
			events: []*NormalizedEvent{
				labeledReviewEvent(bot, []string{AutoReviewHandoffLabel, "ready-for-review"}),
				opened(bot, headA, []string{AutoReviewHandoffLabel, "ready-for-review"}),
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
				opened(bot, headA, []string{AutoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(bot, []string{AutoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(alice, []string{"ready-for-review"}),
			},
			wantReview: 2,
		},
		{
			name: "/fs-review requests another review of the same SHA",
			events: []*NormalizedEvent{
				opened(bot, headA, []string{AutoReviewHandoffLabel, "ready-for-review"}),
				labeledReviewEvent(bot, []string{AutoReviewHandoffLabel, "ready-for-review"}),
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

func TestConcurrentOpenedAndAutomaticLabel(t *testing.T) {
	t.Parallel()
	r := NewHarnessRouter([]string{"review"})
	bot := Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"}

	opened := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 7},
		Transition: Transition{Kind: "opened"},
		Actor:      bot,
		State: State{
			Labels:         []string{AutoReviewHandoffLabel, "ready-for-review"},
			ChangeProposal: &ChangeProposalState{ID: 7, IsFork: false},
		},
	}
	labeled := labeledReviewEvent(bot, []string{AutoReviewHandoffLabel, "ready-for-review"})

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

func TestConcurrentOpenedAndExplicitLabel(t *testing.T) {
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

func TestGitHubLikeExplicitLabelIgnoresBotUsername(t *testing.T) {
	t.Parallel()
	bot := Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"}
	if !githubLikeReviewDecision("labeled", bot, []string{"ready-for-review"}, "aaa") {
		t.Fatal("bot-applied ready-for-review without provenance must dispatch")
	}
	if githubLikeReviewDecision("labeled", bot, []string{AutoReviewHandoffLabel, "ready-for-review"}, "aaa") {
		t.Fatal("automatic handoff must skip regardless of bot username")
	}
	human := Actor{ID: "alice", Kind: "human", Role: "write"}
	if githubLikeReviewDecision("labeled", human, []string{AutoReviewHandoffLabel, "ready-for-review"}, "aaa") {
		t.Fatal("automatic handoff must skip even when the actor is human")
	}
}

func TestGitHubLikeSynchronizeStillReviews(t *testing.T) {
	t.Parallel()
	// GitHub maps opened|synchronize|ready_for_review onto review in
	// the workflow router. The Go HarnessRouter has no synchronize
	// kind; this helper mirrors the GitHub decision so the scenario
	// coverage includes subsequent and fix-agent pushes.
	decide := githubLikeReviewDecision
	bot := Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"}
	alice := Actor{ID: "alice", Kind: "human", Role: "write"}

	type ev struct {
		action string
		actor  Actor
		labels []string
		sha    string
	}

	run := func(t *testing.T, events []ev, want int) {
		t.Helper()
		var got int
		for _, e := range events {
			if decide(e.action, e.actor, e.labels, e.sha) {
				got++
			}
		}
		if got != want {
			t.Fatalf("github-like review dispatches = %d, want %d", got, want)
		}
	}

	t.Run("creation either order", func(t *testing.T) {
		t.Parallel()
		pair := []ev{
			{action: "opened", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"},
			{action: "labeled", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"},
		}
		run(t, pair, 1)
		run(t, []ev{pair[1], pair[0]}, 1)
	})

	t.Run("subsequent push and fix-agent push", func(t *testing.T) {
		t.Parallel()
		run(t, []ev{
			{action: "opened", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"},
			{action: "labeled", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"},
			{action: "synchronize", actor: bot, labels: nil, sha: "bbb"},
		}, 2)
	})

	t.Run("explicit same-revision label and slash", func(t *testing.T) {
		t.Parallel()
		run(t, []ev{
			{action: "opened", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"},
			{action: "labeled", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"},
			{action: "labeled", actor: alice, labels: []string{"ready-for-review"}, sha: "aaa"},
			{action: "slash-review", actor: alice, labels: nil, sha: "aaa"},
		}, 3)
	})

	t.Run("ready_for_review event still reviews", func(t *testing.T) {
		t.Parallel()
		run(t, []ev{
			{action: "ready_for_review", actor: alice, labels: nil, sha: "aaa"},
		}, 1)
	})
}

func TestConcurrentGitHubLikeCreation(t *testing.T) {
	t.Parallel()
	bot := Actor{ID: "fullsend-ai-coder[bot]", Kind: "bot", Role: "write"}
	opened := githubLikeEvent{action: "opened", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"}
	labeled := githubLikeEvent{action: "labeled", actor: bot, labels: []string{AutoReviewHandoffLabel, "ready-for-review"}, sha: "aaa"}

	const iterations = 32
	for i := 0; i < iterations; i++ {
		events := []githubLikeEvent{opened, labeled}
		if i%2 == 1 {
			events[0], events[1] = events[1], events[0]
		}
		got := countGitHubLikeConcurrent(t, events)
		if got != 1 {
			t.Fatalf("iteration %d: concurrent github-like pair dispatched %d reviews, want 1", i, got)
		}
	}
}

type githubLikeEvent struct {
	action string
	actor  Actor
	labels []string
	sha    string
}

// githubLikeReviewDecision mirrors the per-repo GitHub Route job after
// the maintainer patch: opened|synchronize|ready_for_review dispatch
// review (authorization is out of scope here), labeled ready-for-review
// dispatches unless the automatic-handoff provenance label is on the
// event snapshot, and /fs-review dispatches.
func githubLikeReviewDecision(action string, actor Actor, labels []string, sha string) bool {
	_ = actor
	_ = sha
	switch action {
	case "opened", "synchronize", "ready_for_review", "slash-review":
		return true
	case "labeled":
		return !hasLabel(labels, AutoReviewHandoffLabel)
	default:
		return false
	}
}

func countGitHubLikeConcurrent(t *testing.T, events []githubLikeEvent) int {
	t.Helper()
	var (
		mu    sync.Mutex
		count int
		wg    sync.WaitGroup
	)
	wg.Add(len(events))
	for _, ev := range events {
		go func(e githubLikeEvent) {
			defer wg.Done()
			if githubLikeReviewDecision(e.action, e.actor, e.labels, e.sha) {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}(ev)
	}
	wg.Wait()
	return count
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
