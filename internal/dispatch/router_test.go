package dispatch

import "testing"

func TestHarnessRouter_SlashCommand(t *testing.T) {
	r := NewHarnessRouter([]string{"triage", "code", "review", "fix", "retro", "custom-agent"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-triage", Body: "/fs-triage please look"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "triage" {
		t.Fatalf("expected [triage], got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandCustomAgent(t *testing.T) {
	r := NewHarnessRouter([]string{"triage", "custom-agent"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-custom-agent", Body: "/fs-custom-agent do the thing"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "custom-agent" {
		t.Fatalf("expected [custom-agent], got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandUnknownAgent(t *testing.T) {
	r := NewHarnessRouter([]string{"triage", "code"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-unknown", Body: "/fs-unknown"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages, got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandInsufficientRole(t *testing.T) {
	r := NewHarnessRouter([]string{"triage", "code"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-triage", Body: "/fs-triage"}},
		Actor:      Actor{ID: "guest", Role: "read"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for Guest actor, got %v", stages)
	}
}

func TestHarnessRouter_LabelReadyToCode(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "label_changed", Label: &TransitionLabel{Name: "ready-to-code", Action: "added"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "code" {
		t.Fatalf("expected [code], got %v", stages)
	}
}

func TestHarnessRouter_LabelReadyForReview(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "label_changed", Label: &TransitionLabel{Name: "ready-for-review", Action: "added"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "review" {
		t.Fatalf("expected [review], got %v", stages)
	}
}

func TestHarnessRouter_LabelCodeAgentNotInValidSet(t *testing.T) {
	r := NewHarnessRouter([]string{"review", "triage"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "label_changed", Label: &TransitionLabel{Name: "ready-to-code", Action: "added"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages when code agent disabled, got %v", stages)
	}
}

func TestHarnessRouter_LabelRemovedIgnored(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "label_changed", Label: &TransitionLabel{Name: "ready-to-code", Action: "removed"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for label removal, got %v", stages)
	}
}

func TestHarnessRouter_LabelForkMRBlocked(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "label_changed", Label: &TransitionLabel{Name: "ready-to-code", Action: "added"}},
		Actor:      Actor{ID: "alice", Role: "write"},
		State:      State{ChangeProposal: &ChangeProposalState{IsFork: true}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for fork MR label, got %v", stages)
	}
}

func TestHarnessRouter_LabelSameProjectMRAllowed(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "label_changed", Label: &TransitionLabel{Name: "ready-to-code", Action: "added"}},
		Actor:      Actor{ID: "alice", Role: "write"},
		State:      State{ChangeProposal: &ChangeProposalState{IsFork: false}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "code" {
		t.Fatalf("expected [code] for same-project MR label, got %v", stages)
	}
}

func TestHarnessRouter_Merged(t *testing.T) {
	r := NewHarnessRouter([]string{"retro", "code"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 42},
		Transition: Transition{Kind: "merged"},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "retro" {
		t.Fatalf("expected [retro], got %v", stages)
	}
}

func TestHarnessRouter_ChangesRequestedMarker(t *testing.T) {
	r := NewHarnessRouter([]string{"fix", "review"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "Changes needed <!-- fullsend:changes-requested --> please fix",
		}},
		Actor: Actor{ID: "bot", Kind: "bot", Role: "write"},
		State: State{ChangeProposal: &ChangeProposalState{IsFork: false}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "fix" {
		t.Fatalf("expected [fix], got %v", stages)
	}
}

func TestHarnessRouter_ChangesRequestedForkBlocked(t *testing.T) {
	r := NewHarnessRouter([]string{"fix"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "<!-- fullsend:changes-requested -->",
		}},
		Actor: Actor{ID: "bot", Kind: "bot", Role: "write"},
		State: State{ChangeProposal: &ChangeProposalState{IsFork: true}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for fork MR changes-requested, got %v", stages)
	}
}

func TestHarnessRouter_ChangesRequestedNilChangeProposal(t *testing.T) {
	r := NewHarnessRouter([]string{"fix"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "<!-- fullsend:changes-requested -->",
		}},
		Actor: Actor{ID: "bot", Kind: "bot", Role: "write"},
		State: State{},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages when ChangeProposal is nil, got %v", stages)
	}
}

func TestHarnessRouter_NeedsInfoTriageByReporter(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "work_item", ID: 5},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "Here is the info you asked for",
		}},
		Actor: Actor{ID: "reporter", Role: "triage"},
		State: State{Labels: []string{"needs-info", "bug"}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "triage" {
		t.Fatalf("expected [triage], got %v", stages)
	}
}

func TestHarnessRouter_NeedsInfoTriageByGuestBlocked(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "work_item", ID: 5},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "Here is some info",
		}},
		Actor: Actor{ID: "guest", Role: "read"},
		State: State{Labels: []string{"needs-info"}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for Guest on needs-info, got %v", stages)
	}
}

func TestHarnessRouter_NeedsInfoTriageByEntityAuthor(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "work_item", ID: 5},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "I can clarify",
		}},
		Actor: Actor{ID: "author", Role: "read", IsEntityAuthor: true},
		State: State{Labels: []string{"needs-info"}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "triage" {
		t.Fatalf("expected [triage] for entity author, got %v", stages)
	}
}

func TestHarnessRouter_CommentWithoutNeedsInfoNoRoute(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "work_item", ID: 5},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "Just a comment",
		}},
		Actor: Actor{ID: "dev", Role: "write"},
		State: State{Labels: []string{"bug"}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages without needs-info label, got %v", stages)
	}
}

func TestHarnessRouter_NilEvent(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	stages, err := r.Route(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for nil event, got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandMixedCaseNormalized(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-TRIAGE", Body: "/fs-TRIAGE please"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "triage" {
		t.Fatalf("expected [triage] (lowercase), got %v", stages)
	}
}

func TestHarnessRouter_ChangesRequestedHumanIgnored(t *testing.T) {
	r := NewHarnessRouter([]string{"fix"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "<!-- fullsend:changes-requested -->",
		}},
		Actor: Actor{ID: "human", Kind: "human", Role: "write"},
		State: State{ChangeProposal: &ChangeProposalState{IsFork: false}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for human-authored marker, got %v", stages)
	}
}

func TestHarnessRouter_LabelInsufficientRole(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "label_changed", Label: &TransitionLabel{Name: "ready-to-code", Action: "added"}},
		Actor:      Actor{ID: "reporter", Role: "triage"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for triage-role label event, got %v", stages)
	}
}

func TestHarnessRouter_CaseInsensitiveAgentNames(t *testing.T) {
	r := NewHarnessRouter([]string{"Triage", "CODE"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-triage", Body: "/fs-triage"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "triage" {
		t.Fatalf("expected [triage], got %v", stages)
	}
}

func TestHarnessRouter_MergedRetroNotInValidSet(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 42},
		Transition: Transition{Kind: "merged"},
		Actor:      Actor{ID: "alice", Role: "maintain"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages when retro not in valid set, got %v", stages)
	}
}

func TestHarnessRouter_UnknownTransitionKind(t *testing.T) {
	r := NewHarnessRouter([]string{"triage", "code"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "status_changed"},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for unknown transition, got %v", stages)
	}
}

func TestHarnessRouter_CommentNilComment(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added"},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for nil comment, got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandEmptyAfterPrefix(t *testing.T) {
	r := NewHarnessRouter([]string{"triage"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "work_item", ID: 1},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-", Body: "/fs-"}},
		Actor:      Actor{ID: "alice", Role: "write"},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for empty command suffix, got %v", stages)
	}
}

func TestHarnessRouter_ChangesRequestedFixNotInValidSet(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "<!-- fullsend:changes-requested -->",
		}},
		Actor: Actor{ID: "bot", Kind: "bot", Role: "write"},
		State: State{ChangeProposal: &ChangeProposalState{IsFork: false}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages when fix not in valid set, got %v", stages)
	}
}

func TestHarnessRouter_NeedsInfoTriageNotInValidSet(t *testing.T) {
	r := NewHarnessRouter([]string{"code", "review"})

	event := &NormalizedEvent{
		Entity: Entity{Kind: "work_item", ID: 5},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{
			Body: "Here is the info",
		}},
		Actor: Actor{ID: "dev", Role: "write"},
		State: State{Labels: []string{"needs-info"}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages when triage not in valid set, got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandForkMRBlocked(t *testing.T) {
	r := NewHarnessRouter([]string{"triage", "code", "custom-agent"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-code", Body: "/fs-code"}},
		Actor:      Actor{ID: "alice", Role: "write"},
		State:      State{ChangeProposal: &ChangeProposalState{IsFork: true}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for slash command on fork MR, got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandCustomAgentForkMRBlocked(t *testing.T) {
	r := NewHarnessRouter([]string{"custom-agent"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-custom-agent", Body: "/fs-custom-agent"}},
		Actor:      Actor{ID: "alice", Role: "write"},
		State:      State{ChangeProposal: &ChangeProposalState{IsFork: true}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for custom agent slash command on fork MR, got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandNilChangeProposalBlocked(t *testing.T) {
	r := NewHarnessRouter([]string{"code"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-code", Body: "/fs-code"}},
		Actor:      Actor{ID: "alice", Role: "write"},
		State:      State{},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for slash command with nil ChangeProposal, got %v", stages)
	}
}

func TestHarnessRouter_SlashCommandSameProjectMRAllowed(t *testing.T) {
	r := NewHarnessRouter([]string{"code"})

	event := &NormalizedEvent{
		Entity:     Entity{Kind: "change_proposal", ID: 10},
		Transition: Transition{Kind: "comment_added", Comment: &TransitionComment{Command: "/fs-code", Body: "/fs-code"}},
		Actor:      Actor{ID: "alice", Role: "write"},
		State:      State{ChangeProposal: &ChangeProposalState{IsFork: false}},
	}

	stages, err := r.Route(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "code" {
		t.Fatalf("expected [code] for same-project MR slash command, got %v", stages)
	}
}

func TestRoleLevel(t *testing.T) {
	tests := []struct {
		role  string
		level int
	}{
		{"", 0},
		{"unknown", 0},
		{"read", 1},
		{"triage", 2},
		{"write", 3},
		{"maintain", 4},
		{"admin", 5},
	}
	for _, tt := range tests {
		if got := RoleLevel(tt.role); got != tt.level {
			t.Errorf("RoleLevel(%q) = %d, want %d", tt.role, got, tt.level)
		}
	}
}

func TestHasRole(t *testing.T) {
	tests := []struct {
		actor    string
		required string
		want     bool
	}{
		{"admin", "write", true},
		{"write", "write", true},
		{"triage", "write", false},
		{"read", "triage", false},
		{"", "read", false},
		{"admin", "admin", true},
		{"maintain", "admin", false},
		{"admin", "typo", false},
		{"write", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got := HasRole(tt.actor, tt.required); got != tt.want {
			t.Errorf("HasRole(%q, %q) = %v, want %v", tt.actor, tt.required, got, tt.want)
		}
	}
}
