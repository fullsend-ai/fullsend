package poll

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestToNormalizedEvent_IssueNote(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[42] = 30                               // Developer -> "write"
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}} // actor is the author
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:            "issue_note",
		IID:             1,
		NoteBody:        "looks good to me",
		NoteID:          10,
		NoteAuthorID:    42,
		NoteAuthorLogin: "alice",
		IsBot:           false,
		Labels:          []string{"bug"},
	}

	ne, err := p.toNormalizedEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ne.Repo != "group/project" {
		t.Errorf("Repo = %q, want %q", ne.Repo, "group/project")
	}
	if ne.Source.System != "gitlab" {
		t.Errorf("Source.System = %q, want %q", ne.Source.System, "gitlab")
	}
	if ne.Source.RawType != "issues" {
		t.Errorf("Source.RawType = %q, want %q", ne.Source.RawType, "issues")
	}
	if ne.Entity.Kind != "work_item" {
		t.Errorf("Entity.Kind = %q, want %q", ne.Entity.Kind, "work_item")
	}
	if ne.Entity.ID != 1 {
		t.Errorf("Entity.ID = %d, want 1", ne.Entity.ID)
	}
	if ne.Entity.URL != "https://gitlab.com/group/project/-/issues/1" {
		t.Errorf("Entity.URL = %q, want %q", ne.Entity.URL, "https://gitlab.com/group/project/-/issues/1")
	}
	if ne.Transition.Kind != "comment_added" {
		t.Errorf("Transition.Kind = %q, want %q", ne.Transition.Kind, "comment_added")
	}
	if ne.Transition.Comment == nil {
		t.Fatal("expected Transition.Comment to be set")
	}
	if ne.Transition.Comment.Body != "looks good to me" {
		t.Errorf("Comment.Body = %q, want %q", ne.Transition.Comment.Body, "looks good to me")
	}
	if ne.Actor.ID != "alice" {
		t.Errorf("Actor.ID = %q, want %q", ne.Actor.ID, "alice")
	}
	if ne.Actor.Kind != "human" {
		t.Errorf("Actor.Kind = %q, want %q", ne.Actor.Kind, "human")
	}
	if ne.Actor.Role != "write" {
		t.Errorf("Actor.Role = %q, want %q", ne.Actor.Role, "write")
	}
	if !ne.Actor.IsEntityAuthor {
		t.Error("expected Actor.IsEntityAuthor to be true")
	}
	if len(ne.State.Labels) != 1 || ne.State.Labels[0] != "bug" {
		t.Errorf("State.Labels = %v, want [bug]", ne.State.Labels)
	}
}

func TestToNormalizedEvent_IssueLabel(t *testing.T) {
	mc := newMockClient()
	mc.labelEvents[1] = []ResourceLabelEvent{
		{
			ID:     100,
			Action: "add",
			Label: struct {
				Name string `json:"name"`
			}{Name: "ready-to-code"},
			User: UserRef{ID: 55, Username: "carol", Bot: false},
		},
	}
	mc.memberLevel[55] = 40                               // Maintainer -> "maintain"
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 99}} // actor 55 is NOT the author
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:         "issue_label",
		IID:          1,
		Labels:       []string{"ready-to-code"},
		ChangedLabel: "ready-to-code",
	}

	ne, err := p.toNormalizedEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ne.Transition.Kind != "label_changed" {
		t.Errorf("Transition.Kind = %q, want %q", ne.Transition.Kind, "label_changed")
	}
	if ne.Transition.Label == nil {
		t.Fatal("expected Transition.Label to be set")
	}
	if ne.Transition.Label.Name != "ready-to-code" {
		t.Errorf("Label.Name = %q, want %q", ne.Transition.Label.Name, "ready-to-code")
	}
	if ne.Transition.Label.Action != "added" {
		t.Errorf("Label.Action = %q, want %q", ne.Transition.Label.Action, "added")
	}
	if ne.Actor.ID != "carol" {
		t.Errorf("Actor.ID = %q, want %q (resolved from label event)", ne.Actor.ID, "carol")
	}
	if ne.Actor.Kind != "human" {
		t.Errorf("Actor.Kind = %q, want %q", ne.Actor.Kind, "human")
	}
	if ne.Actor.Role != "maintain" {
		t.Errorf("Actor.Role = %q, want %q", ne.Actor.Role, "maintain")
	}
	if ne.Actor.IsEntityAuthor {
		t.Error("expected Actor.IsEntityAuthor to be false")
	}
}

func TestToNormalizedEvent_MRNote(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[42] = 30
	mc.projectPaths[1] = "group/project"
	mc.projectPaths[2] = "fork-owner/project"
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:            "mr_note",
		IID:             5,
		NoteBody:        "/fs-review check this",
		NoteID:          20,
		NoteAuthorID:    42,
		NoteAuthorLogin: "alice",
		IsBot:           false,
		MRSource:        2,
		MRTarget:        1,
		MRAuthorID:      42,
		MRAuthorLogin:   "alice",
		SourceBranch:    "feature",
		TargetBranch:    "main",
	}

	ne, err := p.toNormalizedEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ne.Entity.Kind != "change_proposal" {
		t.Errorf("Entity.Kind = %q, want %q", ne.Entity.Kind, "change_proposal")
	}
	if ne.Entity.URL != "https://gitlab.com/group/project/-/merge_requests/5" {
		t.Errorf("Entity.URL = %q, want %q", ne.Entity.URL, "https://gitlab.com/group/project/-/merge_requests/5")
	}
	if ne.Source.RawType != "merge_request" {
		t.Errorf("Source.RawType = %q, want %q", ne.Source.RawType, "merge_request")
	}
	if ne.Transition.Kind != "comment_added" {
		t.Errorf("Transition.Kind = %q, want %q", ne.Transition.Kind, "comment_added")
	}
	if ne.Transition.Comment == nil {
		t.Fatal("expected Transition.Comment to be set")
	}
	if ne.Transition.Comment.Command != "/fs-review" {
		t.Errorf("Comment.Command = %q, want %q", ne.Transition.Comment.Command, "/fs-review")
	}
	if ne.Transition.Comment.Instruction != "check this" {
		t.Errorf("Comment.Instruction = %q, want %q", ne.Transition.Comment.Instruction, "check this")
	}

	// MR note should have change proposal state populated.
	if ne.State.ChangeProposal == nil {
		t.Fatal("expected State.ChangeProposal to be set for mr_note")
	}
	if ne.State.ChangeProposal.HeadRepo != "fork-owner/project" {
		t.Errorf("ChangeProposal.HeadRepo = %q, want %q", ne.State.ChangeProposal.HeadRepo, "fork-owner/project")
	}
	if ne.State.ChangeProposal.BaseRepo != "group/project" {
		t.Errorf("ChangeProposal.BaseRepo = %q, want %q", ne.State.ChangeProposal.BaseRepo, "group/project")
	}
	if !ne.State.ChangeProposal.IsFork {
		t.Error("expected ChangeProposal.IsFork to be true")
	}
}

func TestToNormalizedEvent_MREventMerge(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[10] = 50 // Owner -> "admin"
	mc.projectPaths[1] = "group/project"
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:          "mr_event",
		IID:           5,
		NoteAuthorID:  10,
		MergedByLogin: "maintainer-user",
		IsBot:         false,
		MRSource:      1,
		MRTarget:      1,
		MRAuthorID:    10,
		MRAuthorLogin: "maintainer-user",
		SourceBranch:  "feature",
		TargetBranch:  "main",
	}

	ne, err := p.toNormalizedEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ne.Transition.Kind != "merged" {
		t.Errorf("Transition.Kind = %q, want %q", ne.Transition.Kind, "merged")
	}
	if ne.Actor.ID != "maintainer-user" {
		t.Errorf("Actor.ID = %q, want %q", ne.Actor.ID, "maintainer-user")
	}
	if ne.Actor.Role != "admin" {
		t.Errorf("Actor.Role = %q, want %q", ne.Actor.Role, "admin")
	}
	if ne.State.ChangeProposal == nil {
		t.Fatal("expected State.ChangeProposal to be set for mr_event")
	}
	if ne.State.ChangeProposal.IsFork {
		t.Error("expected ChangeProposal.IsFork to be false for same project")
	}
}

func TestToNormalizedEvent_UnresolvableActorError(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc)

	// issue_note with NoteAuthorID=0 -> authorID stays 0 -> error.
	event := RoutableEvent{
		Type:         "issue_note",
		IID:          1,
		NoteAuthorID: 0,
		NoteBody:     "orphaned",
	}

	_, err := p.toNormalizedEvent(context.Background(), event)
	if err == nil {
		t.Fatal("expected error for unresolvable actor")
	}
	if got := err.Error(); got != "unresolvable actor" {
		t.Errorf("error = %q, want %q", got, "unresolvable actor")
	}
}

func TestTranslateEventType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"issue_label", "label_changed"},
		{"issue_note", "comment_added"},
		{"mr_note", "comment_added"},
		{"mr_event", "merged"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := translateEventType(tt.input)
			if got != tt.want {
				t.Errorf("translateEventType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveLabelAuthor_FindsMatch(t *testing.T) {
	mc := newMockClient()
	mc.labelEvents[1] = []ResourceLabelEvent{
		{
			ID:     1,
			Action: "add",
			Label: struct {
				Name string `json:"name"`
			}{Name: "bug"},
			User: UserRef{ID: 10, Username: "alice", Bot: false},
		},
		{
			ID:     2,
			Action: "add",
			Label: struct {
				Name string `json:"name"`
			}{Name: "ready-to-code"},
			User: UserRef{ID: 20, Username: "bob", Bot: false},
		},
		{
			ID:     3,
			Action: "add",
			Label: struct {
				Name string `json:"name"`
			}{Name: "ready-to-code"},
			User: UserRef{ID: 30, Username: "carol", Bot: true},
		},
	}
	p := newEventsPoller(mc)

	// Should find the most recent "add" for "ready-to-code" (ID 3, user 30).
	la, err := p.resolveLabelAuthor(context.Background(), 1, "ready-to-code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if la.ID != 30 {
		t.Errorf("LabelAuthor.ID = %d, want 30 (most recent add)", la.ID)
	}
	if !la.IsBot {
		t.Error("expected LabelAuthor.IsBot to be true")
	}
}

func TestResolveLabelAuthor_NoMatch(t *testing.T) {
	mc := newMockClient()
	mc.labelEvents[1] = []ResourceLabelEvent{
		{
			ID:     1,
			Action: "remove",
			Label: struct {
				Name string `json:"name"`
			}{Name: "ready-to-code"},
			User: UserRef{ID: 10, Username: "alice"},
		},
	}
	p := newEventsPoller(mc)

	_, err := p.resolveLabelAuthor(context.Background(), 1, "ready-to-code")
	if err == nil {
		t.Fatal("expected error when no matching add event found")
	}
}

func TestResolveActorRole_AllLevels(t *testing.T) {
	tests := []struct {
		level int
		want  string
	}{
		{10, "read"},
		{20, "triage"},
		{30, "write"},
		{40, "maintain"},
		{50, "admin"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("level_%d", tt.level), func(t *testing.T) {
			mc := newMockClient()
			mc.memberLevel[1] = tt.level
			p := newEventsPoller(mc)

			got := p.resolveActorRole(context.Background(), 1)
			if got != tt.want {
				t.Errorf("resolveActorRole(level=%d) = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

func TestResolveActorRole_UnknownLevel(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[1] = 99 // unknown level
	p := newEventsPoller(mc)

	got := p.resolveActorRole(context.Background(), 1)
	if got != "none" {
		t.Errorf("resolveActorRole(level=99) = %q, want %q", got, "none")
	}
}

func TestResolveActorRole_MemberNotFound(t *testing.T) {
	mc := newMockClient()
	// No memberLevel set for userID 1 -> GetMemberAccessLevel returns error.
	p := newEventsPoller(mc)

	got := p.resolveActorRole(context.Background(), 1)
	if got != "none" {
		t.Errorf("resolveActorRole(not found) = %q, want %q", got, "none")
	}
}

func TestExtractCommand_Triage(t *testing.T) {
	cmd, instruction := extractCommand("/fs-triage")
	if cmd != "/fs-triage" {
		t.Errorf("command = %q, want %q", cmd, "/fs-triage")
	}
	if instruction != "" {
		t.Errorf("instruction = %q, want empty", instruction)
	}
}

func TestExtractCommand_WithInstruction(t *testing.T) {
	cmd, instruction := extractCommand("/fs-code please implement the fix\nwith tests")
	if cmd != "/fs-code" {
		t.Errorf("command = %q, want %q", cmd, "/fs-code")
	}
	want := "please implement the fix\nwith tests"
	if instruction != want {
		t.Errorf("instruction = %q, want %q", instruction, want)
	}
}

func TestExtractCommand_NoCommand(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"regular comment", "just a regular comment"},
		{"non-fs slash", "/label ~bug"},
		{"empty body", ""},
		{"whitespace only", "   \n   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, instruction := extractCommand(tt.body)
			if cmd != "" {
				t.Errorf("command = %q, want empty", cmd)
			}
			if instruction != "" {
				t.Errorf("instruction = %q, want empty", instruction)
			}
		})
	}
}

func TestEntityURL_Issue(t *testing.T) {
	got := entityURL("https://gitlab.com", "group/project", "issue_note", 42)
	want := "https://gitlab.com/group/project/-/issues/42"
	if got != want {
		t.Errorf("entityURL(issue_note) = %q, want %q", got, want)
	}

	// Also test issue_label.
	got = entityURL("https://gitlab.com", "group/project", "issue_label", 7)
	want = "https://gitlab.com/group/project/-/issues/7"
	if got != want {
		t.Errorf("entityURL(issue_label) = %q, want %q", got, want)
	}
}

func TestEntityURL_MR(t *testing.T) {
	got := entityURL("https://gitlab.com", "group/project", "mr_note", 15)
	want := "https://gitlab.com/group/project/-/merge_requests/15"
	if got != want {
		t.Errorf("entityURL(mr_note) = %q, want %q", got, want)
	}

	// Also test mr_event.
	got = entityURL("https://gitlab.com", "group/project", "mr_event", 3)
	want = "https://gitlab.com/group/project/-/merge_requests/3"
	if got != want {
		t.Errorf("entityURL(mr_event) = %q, want %q", got, want)
	}
}

func TestEntityURL_TrailingSlash(t *testing.T) {
	got := entityURL("https://gitlab.com/", "group/project", "issue_note", 1)
	want := "https://gitlab.com/group/project/-/issues/1"
	if got != want {
		t.Errorf("entityURL with trailing slash = %q, want %q", got, want)
	}
}

func TestMapRawType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"issue_label", "issues"},
		{"issue_note", "issues"},
		{"mr_note", "merge_request"},
		{"mr_event", "merge_request"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapRawType(tt.input)
			if got != tt.want {
				t.Errorf("mapRawType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMapRawAction(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"issue_label", "labeled"},
		{"issue_note", "commented"},
		{"mr_note", "commented"},
		{"mr_event", "merged"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapRawAction(tt.input)
			if got != tt.want {
				t.Errorf("mapRawAction(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEntityKind(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"issue_label", "work_item"},
		{"issue_note", "work_item"},
		{"mr_note", "change_proposal"},
		{"mr_event", "change_proposal"},
		{"unknown", "work_item"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := entityKind(tt.input)
			if got != tt.want {
				t.Errorf("entityKind(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildChangeProposalState(t *testing.T) {
	mc := newMockClient()
	mc.projectPaths[10] = "fork/repo"
	mc.projectPaths[20] = "upstream/repo"
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:          "mr_note",
		IID:           5,
		MRSource:      10,
		MRTarget:      20,
		SourceBranch:  "feature-branch",
		TargetBranch:  "main",
		MRAuthorID:    77,
		MRAuthorLogin: "dev-user",
	}

	cps, err := p.buildChangeProposalState(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cps.ID != 5 {
		t.Errorf("ID = %d, want 5", cps.ID)
	}
	if cps.HeadRepo != "fork/repo" {
		t.Errorf("HeadRepo = %q, want %q", cps.HeadRepo, "fork/repo")
	}
	if cps.BaseRepo != "upstream/repo" {
		t.Errorf("BaseRepo = %q, want %q", cps.BaseRepo, "upstream/repo")
	}
	if cps.HeadRef != "feature-branch" {
		t.Errorf("HeadRef = %q, want %q", cps.HeadRef, "feature-branch")
	}
	if cps.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want %q", cps.BaseRef, "main")
	}
	if cps.AuthorID != "dev-user" {
		t.Errorf("AuthorID = %q, want %q", cps.AuthorID, "dev-user")
	}
	if !cps.IsFork {
		t.Error("expected IsFork to be true for different source/target projects")
	}
}

func TestBuildChangeProposalState_SameProject(t *testing.T) {
	mc := newMockClient()
	mc.projectPaths[1] = "group/project"
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:     "mr_event",
		IID:      3,
		MRSource: 1,
		MRTarget: 1,
	}

	cps, err := p.buildChangeProposalState(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cps.IsFork {
		t.Error("expected IsFork to be false for same source/target project")
	}
}

func TestBuildChangeProposalState_ZeroProjectIDs(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:     "mr_note",
		IID:      5,
		MRSource: 0,
		MRTarget: 0,
	}

	_, err := p.buildChangeProposalState(context.Background(), event)
	if err == nil {
		t.Fatal("expected error for zero MRSource project ID")
	}
}

func TestIsEntityAuthor_IssueNote(t *testing.T) {
	mc := newMockClient()
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}
	p := newEventsPoller(mc)

	event := RoutableEvent{Type: "issue_note", IID: 1}

	if !p.isEntityAuthor(context.Background(), event, 42) {
		t.Error("expected isEntityAuthor to return true when actor is issue author")
	}
	if p.isEntityAuthor(context.Background(), event, 99) {
		t.Error("expected isEntityAuthor to return false when actor is not issue author")
	}
}

func TestIsEntityAuthor_MREvent(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc)

	event := RoutableEvent{Type: "mr_event", IID: 5, MRAuthorID: 42}
	if !p.isEntityAuthor(context.Background(), event, 42) {
		t.Error("expected isEntityAuthor to return true when actor is MR author")
	}
	if p.isEntityAuthor(context.Background(), event, 99) {
		t.Error("expected isEntityAuthor to return false when actor is not MR author")
	}

	noAuthor := RoutableEvent{Type: "mr_event", IID: 5, MRAuthorID: 0}
	if p.isEntityAuthor(context.Background(), noAuthor, 42) {
		t.Error("expected isEntityAuthor to return false when MRAuthorID is 0")
	}
}

func TestToNormalizedEvent_BotActor(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[200] = 30
	p := newEventsPoller(mc)

	event := RoutableEvent{
		Type:            "issue_note",
		IID:             1,
		NoteBody:        "automated comment",
		NoteID:          10,
		NoteAuthorID:    200,
		NoteAuthorLogin: "project_1_bot_abc",
		IsBot:           true,
		Labels:          []string{},
	}

	ne, err := p.toNormalizedEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ne.Actor.Kind != "bot" {
		t.Errorf("Actor.Kind = %q, want %q for bot actor", ne.Actor.Kind, "bot")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string: got %q, want %q", got, "hello")
	}
	if got := truncate("hello", 3); got != "hel" {
		t.Errorf("ASCII truncate: got %q, want %q", got, "hel")
	}
	multi := strings.Repeat("\U0001F600", 5)
	got := truncate(multi, 3)
	if []rune(got)[0] != '\U0001F600' || len([]rune(got)) != 3 {
		t.Errorf("multi-byte truncate: got %q (len %d runes), want 3 runes", got, len([]rune(got)))
	}
}
