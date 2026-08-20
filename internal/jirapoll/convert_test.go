package jirapoll

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestToNormalizedEvent_MatchesFixture(t *testing.T) {
	data, err := os.ReadFile("../../docs/normative/normalized-event/v1/examples/jira-fs-triage-comment.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var want normevent.Event
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"557058:abc123def456": "Developers",
		},
	}

	event := JiraEvent{
		Type:        "comment_added",
		IssueID:     "10042",
		IssueKey:    "PROJ-123",
		IssueURL:    "https://acme.atlassian.net/browse/PROJ-123",
		UpdatedAt:   time.Now(),
		Labels:      []string{"needs-info", "bug"},
		CommentID:   "50001",
		CommentBody: "/fs-triage check acceptance criteria",
		CommentAuthor: jira.User{
			AccountID:   "557058:abc123def456",
			AccountType: "atlassian",
		},
		Reporter: jira.User{
			AccountID:   "other-user-id",
			AccountType: "atlassian",
		},
	}

	got := p.toNormalizedEvent(event)

	if got.Repo != want.Repo {
		t.Errorf("repo = %q, want %q", got.Repo, want.Repo)
	}
	if got.Source.System != want.Source.System {
		t.Errorf("source.system = %q, want %q", got.Source.System, want.Source.System)
	}
	if got.Source.RawType != want.Source.RawType {
		t.Errorf("source.raw_type = %q, want %q", got.Source.RawType, want.Source.RawType)
	}
	if got.Source.RawAction != want.Source.RawAction {
		t.Errorf("source.raw_action = %q, want %q", got.Source.RawAction, want.Source.RawAction)
	}
	if got.Entity.Kind != want.Entity.Kind {
		t.Errorf("entity.kind = %q, want %q", got.Entity.Kind, want.Entity.Kind)
	}
	if got.Entity.ID != want.Entity.ID {
		t.Errorf("entity.id = %d, want %d", got.Entity.ID, want.Entity.ID)
	}
	if got.Entity.Key != want.Entity.Key {
		t.Errorf("entity.key = %q, want %q", got.Entity.Key, want.Entity.Key)
	}
	if got.Entity.URL != want.Entity.URL {
		t.Errorf("entity.url = %q, want %q", got.Entity.URL, want.Entity.URL)
	}
	if got.Transition.Kind != want.Transition.Kind {
		t.Errorf("transition.kind = %q, want %q", got.Transition.Kind, want.Transition.Kind)
	}
	if got.Transition.Comment == nil {
		t.Fatal("transition.comment is nil")
	}
	if got.Transition.Comment.Command != want.Transition.Comment.Command {
		t.Errorf("transition.comment.command = %q, want %q", got.Transition.Comment.Command, want.Transition.Comment.Command)
	}
	if got.Transition.Comment.Body != want.Transition.Comment.Body {
		t.Errorf("transition.comment.body = %q, want %q", got.Transition.Comment.Body, want.Transition.Comment.Body)
	}
	if got.Transition.Comment.Instruction != want.Transition.Comment.Instruction {
		t.Errorf("transition.comment.instruction = %q, want %q", got.Transition.Comment.Instruction, want.Transition.Comment.Instruction)
	}
	if got.Actor.ID != want.Actor.ID {
		t.Errorf("actor.id = %q, want %q", got.Actor.ID, want.Actor.ID)
	}
	if got.Actor.Kind != want.Actor.Kind {
		t.Errorf("actor.kind = %q, want %q", got.Actor.Kind, want.Actor.Kind)
	}
	if got.Actor.Role != want.Actor.Role {
		t.Errorf("actor.role = %q, want %q", got.Actor.Role, want.Actor.Role)
	}
	if got.Actor.IsEntityAuthor != want.Actor.IsEntityAuthor {
		t.Errorf("actor.is_entity_author = %v, want %v", got.Actor.IsEntityAuthor, want.Actor.IsEntityAuthor)
	}
	if len(got.State.Labels) != len(want.State.Labels) {
		t.Errorf("state.labels len = %d, want %d", len(got.State.Labels), len(want.State.Labels))
	}
}

func TestToNormalizedEvent_CommentWithSlashCommand(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"user1": "Developers",
		},
	}

	event := JiraEvent{
		Type:        "comment_added",
		IssueID:     "10042",
		IssueKey:    "PROJ-123",
		IssueURL:    "https://acme.atlassian.net/browse/PROJ-123",
		CommentBody: "/fs-triage check acceptance criteria",
		CommentAuthor: jira.User{
			AccountID:   "user1",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)

	if ne.Transition.Comment == nil {
		t.Fatal("expected comment in transition")
	}
	if ne.Transition.Comment.Command != "/fs-triage" {
		t.Errorf("command = %q, want %q", ne.Transition.Comment.Command, "/fs-triage")
	}
	if ne.Transition.Comment.Instruction != "check acceptance criteria" {
		t.Errorf("instruction = %q, want %q", ne.Transition.Comment.Instruction, "check acceptance criteria")
	}
}

func TestToNormalizedEvent_EditedCommentRawAction(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
	}

	event := JiraEvent{
		Type:          "comment_added",
		IssueID:       "10042",
		IssueKey:      "PROJ-123",
		IssueURL:      "https://acme.atlassian.net/browse/PROJ-123",
		CommentBody:   "/fs-triage now with a command",
		CommentEdited: true,
		CommentAuthor: jira.User{
			AccountID:   "user1",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)
	if ne.Source.RawAction != "updated" {
		t.Errorf("source.raw_action = %q, want %q for an edit-detected comment", ne.Source.RawAction, "updated")
	}

	event.CommentEdited = false
	ne = p.toNormalizedEvent(event)
	if ne.Source.RawAction != "created" {
		t.Errorf("source.raw_action = %q, want %q for a new comment", ne.Source.RawAction, "created")
	}
}

func TestToNormalizedEvent_CommentWithoutSlashCommand(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"user1": "Developers",
		},
	}

	event := JiraEvent{
		Type:        "comment_added",
		IssueID:     "10042",
		IssueKey:    "PROJ-123",
		IssueURL:    "https://acme.atlassian.net/browse/PROJ-123",
		CommentBody: "This is a regular comment",
		CommentAuthor: jira.User{
			AccountID:   "user1",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)

	if ne.Transition.Comment == nil {
		t.Fatal("expected comment in transition")
	}
	if ne.Transition.Comment.Command != "" {
		t.Errorf("command = %q, want empty", ne.Transition.Comment.Command)
	}
	if ne.Transition.Comment.Instruction != "" {
		t.Errorf("instruction = %q, want empty", ne.Transition.Comment.Instruction)
	}
	if ne.Transition.Comment.Body != "This is a regular comment" {
		t.Errorf("body = %q, want %q", ne.Transition.Comment.Body, "This is a regular comment")
	}
}

func TestToNormalizedEvent_LabelAdded(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"user1": "Developers",
		},
	}

	event := JiraEvent{
		Type:         "label_changed",
		IssueID:      "10042",
		IssueKey:     "PROJ-123",
		IssueURL:     "https://acme.atlassian.net/browse/PROJ-123",
		Labels:       []string{"ready-to-code", "bug"},
		ChangedLabel: "ready-to-code",
		LabelAction:  "added",
		ChangeAuthor: jira.User{
			AccountID:   "user1",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)

	if ne.Transition.Kind != "label_changed" {
		t.Errorf("transition.kind = %q, want %q", ne.Transition.Kind, "label_changed")
	}
	if ne.Transition.Label == nil {
		t.Fatal("expected label in transition")
	}
	if ne.Transition.Label.Name != "ready-to-code" {
		t.Errorf("label.name = %q, want %q", ne.Transition.Label.Name, "ready-to-code")
	}
	if ne.Transition.Label.Action != "added" {
		t.Errorf("label.action = %q, want %q", ne.Transition.Label.Action, "added")
	}
	if ne.Source.RawType != "changelog" {
		t.Errorf("source.raw_type = %q, want %q", ne.Source.RawType, "changelog")
	}
	if ne.Source.RawAction != "updated" {
		t.Errorf("source.raw_action = %q, want %q", ne.Source.RawAction, "updated")
	}
}

func TestToNormalizedEvent_BotActor(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{},
	}

	event := JiraEvent{
		Type:     "comment_added",
		IssueID:  "10042",
		IssueKey: "PROJ-123",
		IssueURL: "https://acme.atlassian.net/browse/PROJ-123",
		CommentAuthor: jira.User{
			AccountID:   "bot-id",
			AccountType: "app",
		},
	}

	ne := p.toNormalizedEvent(event)

	if ne.Actor.Kind != "bot" {
		t.Errorf("actor.kind = %q, want %q", ne.Actor.Kind, "bot")
	}
}

func TestActorID_DataCenterNameFallback(t *testing.T) {
	cases := []struct {
		name string
		user jira.User
		want string
	}{
		{"cloud accountId preferred", jira.User{AccountID: "acc-1", Name: "jdoe"}, "acc-1"},
		{"data center name fallback", jira.User{Name: "jdoe"}, "jdoe"},
		{"neither set", jira.User{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := JiraEvent{Type: "comment_added", CommentAuthor: tc.user}
			if got := actorID(event); got != tc.want {
				t.Errorf("actorID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsEntityAuthor_DataCenterNameFallback(t *testing.T) {
	event := JiraEvent{
		Type:          "comment_added",
		CommentAuthor: jira.User{Name: "jdoe"},
		Reporter:      jira.User{Name: "jdoe"},
	}
	if !isEntityAuthor(event) {
		t.Error("isEntityAuthor() = false, want true when accountId is unavailable but names match")
	}
}

func TestActorKind_DisplayNameAutomationSuffix(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		want        string
	}{
		{"bot suffix with hyphen", "Jira-bot", "bot"},
		{"bot suffix bracketed", "renovate[bot]", "bot"},
		{"automation suffix with space", "ACME Automation", "bot"},
		{"automation suffix with hyphen", "release-automation", "bot"},
		{"Atlassian's built-in automation actor", "Automation for Jira", "bot"},
		{"bot substring mid-name, no delimiter", "Marbot Jenkins", "human"},
		{"bot as literal suffix, no delimiter", "Dependabot", "human"},
		{"plain human name", "Wayne Sun", "human"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := JiraEvent{
				Type: "comment_added",
				CommentAuthor: jira.User{
					AccountID:   "some-id",
					DisplayName: tc.displayName,
				},
			}
			if got := actorKind(event); got != tc.want {
				t.Errorf("actorKind(%q) = %q, want %q", tc.displayName, got, tc.want)
			}
		})
	}
}

func TestToNormalizedEvent_IsEntityAuthor(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"user1": "Developers",
		},
	}

	event := JiraEvent{
		Type:     "comment_added",
		IssueID:  "10042",
		IssueKey: "PROJ-123",
		IssueURL: "https://acme.atlassian.net/browse/PROJ-123",
		CommentAuthor: jira.User{
			AccountID:   "user1",
			AccountType: "atlassian",
		},
		Reporter: jira.User{
			AccountID: "user1",
		},
	}

	ne := p.toNormalizedEvent(event)

	if !ne.Actor.IsEntityAuthor {
		t.Error("expected actor.is_entity_author = true when comment author == reporter")
	}
}

func TestExtractPlainText_ADF(t *testing.T) {
	adf := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "/fs-triage check acceptance criteria",
					},
				},
			},
		},
	}

	got := extractPlainText(adf)
	want := "/fs-triage check acceptance criteria"
	if got != want {
		t.Errorf("extractPlainText(ADF) = %q, want %q", got, want)
	}
}

func TestExtractPlainText_HardBreak(t *testing.T) {
	// Shift+Enter in the Jira editor produces a hardBreak node inside a
	// paragraph; without emitting a newline for it, words the author
	// placed on separate visual lines fuse together — and text visually
	// on line 2 would be parsed as part of the slash command's first line.
	adf := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "/fs-code fix"},
					map[string]any{"type": "hardBreak"},
					map[string]any{"type": "text", "text": "this too"},
				},
			},
		},
	}

	got := extractPlainText(adf)
	want := "/fs-code fix\nthis too"
	if got != want {
		t.Errorf("extractPlainText(hardBreak ADF) = %q, want %q", got, want)
	}

	cmd, instruction := extractCommand(got)
	if cmd != "/fs-code" || instruction != "fix" {
		t.Errorf("extractCommand = (%q, %q), want (%q, %q) — soft-broken second line must not join the instruction", cmd, instruction, "/fs-code", "fix")
	}
}

func TestExtractPlainText_DeepNestingIsBounded(t *testing.T) {
	// Build an ADF document nested far deeper than any real Jira-UI-authored
	// comment would be, with a text node at every level. A malicious actor
	// with comment access could send something like this to try to exhaust
	// the poller's goroutine stack (see PR #5778 review).
	const depth = 10000
	root := map[string]any{
		"type": "paragraph",
		"text": "level-0",
	}
	leaf := root
	for i := 1; i < depth; i++ {
		child := map[string]any{
			"type": "paragraph",
			"text": fmt.Sprintf("level-%d", i),
		}
		leaf["content"] = []any{child}
		leaf = child
	}

	// This must return promptly instead of recursing 10000 levels deep.
	got := extractPlainText(root)
	if !strings.HasPrefix(got, "level-0") {
		t.Errorf("extractPlainText(deeply nested ADF) = %q, want it to start with %q", got, "level-0")
	}
	if strings.Contains(got, fmt.Sprintf("level-%d", depth-1)) {
		t.Errorf("extractPlainText(deeply nested ADF) walked all %d levels; want it capped well below that", depth)
	}
}

func TestExtractPlainText_String(t *testing.T) {
	got := extractPlainText("plain text body")
	if got != "plain text body" {
		t.Errorf("extractPlainText(string) = %q, want %q", got, "plain text body")
	}
}

func TestExtractPlainText_MultiParagraphADF(t *testing.T) {
	adf := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "First paragraph",
					},
				},
			},
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "Second paragraph",
					},
				},
			},
		},
	}

	got := extractPlainText(adf)
	want := "First paragraph\nSecond paragraph"
	if got != want {
		t.Errorf("extractPlainText(multi-paragraph ADF) = %q, want %q", got, want)
	}
}

func TestExtractCommand(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantCommand     string
		wantInstruction string
	}{
		{
			name:            "slash command with instruction",
			body:            "/fs-triage check acceptance criteria",
			wantCommand:     "/fs-triage",
			wantInstruction: "check acceptance criteria",
		},
		{
			name:            "slash command alone",
			body:            "/fs-triage",
			wantCommand:     "/fs-triage",
			wantInstruction: "",
		},
		{
			name:            "no slash command",
			body:            "this is a regular comment",
			wantCommand:     "",
			wantInstruction: "",
		},
		{
			name:            "empty body",
			body:            "",
			wantCommand:     "",
			wantInstruction: "",
		},
		{
			name:            "slash command with leading whitespace",
			body:            "  /fs-code fix the bug",
			wantCommand:     "/fs-code",
			wantInstruction: "fix the bug",
		},
		{
			// Per the adapter spec, the instruction is the remainder of the
			// first line only (same rules as the gha-event adapter).
			name:            "slash command with multi-line body",
			body:            "/fs-triage fix the bug\nMore context on the next line",
			wantCommand:     "/fs-triage",
			wantInstruction: "fix the bug",
		},
		{
			name:            "non-fullsend slash command",
			body:            "/other-tool do something",
			wantCommand:     "",
			wantInstruction: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, inst := extractCommand(tc.body)
			if cmd != tc.wantCommand {
				t.Errorf("command = %q, want %q", cmd, tc.wantCommand)
			}
			if inst != tc.wantInstruction {
				t.Errorf("instruction = %q, want %q", inst, tc.wantInstruction)
			}
		})
	}
}

func TestToNormalizedEvent_JSONRoundTrip(t *testing.T) {
	fixtureData, err := os.ReadFile("../../docs/normative/normalized-event/v1/examples/jira-fs-triage-comment.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"557058:abc123def456": "Developers",
		},
	}

	event := JiraEvent{
		Type:        "comment_added",
		IssueID:     "10042",
		IssueKey:    "PROJ-123",
		IssueURL:    "https://acme.atlassian.net/browse/PROJ-123",
		UpdatedAt:   time.Now(),
		Labels:      []string{"needs-info", "bug"},
		CommentID:   "50001",
		CommentBody: "/fs-triage check acceptance criteria",
		CommentAuthor: jira.User{
			AccountID:   "557058:abc123def456",
			AccountType: "atlassian",
		},
		Reporter: jira.User{
			AccountID:   "other-user-id",
			AccountType: "atlassian",
		},
	}

	got := p.toNormalizedEvent(event)

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal NormalizedEvent: %v", err)
	}

	var gotMap map[string]any
	if err := json.Unmarshal(gotJSON, &gotMap); err != nil {
		t.Fatalf("unmarshal got JSON into map: %v", err)
	}

	var wantMap map[string]any
	if err := json.Unmarshal(fixtureData, &wantMap); err != nil {
		t.Fatalf("unmarshal fixture JSON into map: %v", err)
	}

	if !reflect.DeepEqual(gotMap, wantMap) {
		gotPretty, _ := json.MarshalIndent(gotMap, "", "  ")
		wantPretty, _ := json.MarshalIndent(wantMap, "", "  ")
		t.Errorf("JSON round-trip mismatch.\ngot:\n%s\nwant:\n%s", gotPretty, wantPretty)
	}
}

func TestMapJiraRole(t *testing.T) {
	tests := []struct {
		roleName string
		want     string
	}{
		{"Administrators", "admin"},
		{"administrators", "admin"},
		{"Developers", "write"},
		{"developers", "write"},
		{"Reporters", "read"},
		{"Viewers", "read"},
		{"Custom Role", "read"},
		{"", "read"},
	}
	for _, tc := range tests {
		t.Run(tc.roleName, func(t *testing.T) {
			got := mapJiraRole(tc.roleName)
			if got != tc.want {
				t.Errorf("mapJiraRole(%q) = %q, want %q", tc.roleName, got, tc.want)
			}
		})
	}
}

func TestResolveRole_ExternalActor(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"known-user": "Developers",
		},
	}

	// Actor not in membership map should resolve to "external".
	event := JiraEvent{
		Type:     "comment_added",
		IssueID:  "10042",
		IssueKey: "PROJ-123",
		CommentAuthor: jira.User{
			AccountID:   "unknown-user",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)
	if ne.Actor.Role != "external" {
		t.Errorf("actor.role = %q, want %q", ne.Actor.Role, "external")
	}
}

func TestResolveRole_EmptyActorID(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{},
	}

	// Event with no actor ID should resolve to "external".
	event := JiraEvent{
		Type:     "comment_added",
		IssueID:  "10042",
		IssueKey: "PROJ-123",
		CommentAuthor: jira.User{
			AccountID:   "",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)
	if ne.Actor.Role != "external" {
		t.Errorf("actor.role = %q, want %q", ne.Actor.Role, "external")
	}
}

func TestResolveRole_CrossProjectFailsClosed(t *testing.T) {
	// roleMembership was loaded for PROJ1 only. An actor who is a Developer
	// there must not inherit that role on an issue from a different project
	// matched by a multi-project --jql (see PR #5778 review).
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
			JiraProject: "PROJ1",
		},
		roleMembership: map[string]string{
			"dev-user": "Developers",
		},
	}

	event := JiraEvent{
		Type:     "comment_added",
		IssueID:  "10042",
		IssueKey: "PROJ2-7",
		CommentAuthor: jira.User{
			AccountID:   "dev-user",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)
	if ne.Actor.Role != "external" {
		t.Errorf("actor.role = %q, want %q for an issue outside the configured Jira project", ne.Actor.Role, "external")
	}
}

func TestResolveRole_SameProjectSucceeds(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
			JiraProject: "PROJ1",
		},
		roleMembership: map[string]string{
			"dev-user": "Developers",
		},
	}

	event := JiraEvent{
		Type:     "comment_added",
		IssueID:  "10042",
		IssueKey: "PROJ1-7",
		CommentAuthor: jira.User{
			AccountID:   "dev-user",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)
	if ne.Actor.Role != "write" {
		t.Errorf("actor.role = %q, want %q for an issue in the configured Jira project", ne.Actor.Role, "write")
	}
}

func TestResolveRole_AdminActor(t *testing.T) {
	p := &Poller{
		opts: Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
		},
		roleMembership: map[string]string{
			"admin-user": "Administrators",
		},
	}

	event := JiraEvent{
		Type:     "comment_added",
		IssueID:  "10042",
		IssueKey: "PROJ-123",
		CommentAuthor: jira.User{
			AccountID:   "admin-user",
			AccountType: "atlassian",
		},
	}

	ne := p.toNormalizedEvent(event)
	if ne.Actor.Role != "admin" {
		t.Errorf("actor.role = %q, want %q", ne.Actor.Role, "admin")
	}
}
