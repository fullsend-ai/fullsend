package poll

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

var webhookTestNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// recent is an event time inside the webhook freshness window.
var recent = webhookTestNow.Add(-2 * time.Minute)

var (
	alice = UserRef{ID: 42, Username: "alice"}
	bob   = UserRef{ID: 43, Username: "bob"}
)

func newWebhookPoller(mc *mockClient) *Poller {
	router := dispatch.NewHarnessRouter([]string{"triage", "code", "review", "fix", "retro"})
	p := New(mc, router, "group/project", Options{
		BotUserID:      100,
		GitLabURL:      "https://gitlab.example.com",
		PipelineRef:    "main",
		DispatchSecret: testDispatchSecret,
	})
	p.now = func() time.Time { return webhookTestNow }
	return p
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// hostileURLs are payload fields the builder must never follow.
var hostileURLs = map[string]any{
	"path_with_namespace": "evil/other",
	"web_url":             "https://attacker.example/evil/other",
	"git_http_url":        "https://attacker.example/evil/other.git",
}

func issuePayload(t *testing.T, action string, userID, iid int, labels map[string]any) []byte {
	t.Helper()
	m := map[string]any{
		"object_kind": "issue",
		"user":        map[string]any{"id": userID, "username": "whoever"},
		"project":     hostileURLs,
		"repository":  map[string]any{"git_http_url": "https://attacker.example/x.git"},
		"object_attributes": map[string]any{
			"iid":    iid,
			"action": action,
			"url":    "https://attacker.example/evil/other/-/issues/1",
		},
	}
	if labels != nil {
		m["changes"] = map[string]any{"labels": labels}
	}
	return mustJSON(t, m)
}

func mrPayload(t *testing.T, action string, userID, iid int, changes map[string]any) []byte {
	t.Helper()
	m := map[string]any{
		"object_kind": "merge_request",
		"user":        map[string]any{"id": userID},
		"project":     hostileURLs,
		"object_attributes": map[string]any{
			"iid":    iid,
			"action": action,
			"url":    "https://attacker.example/evil/other/-/merge_requests/1",
		},
	}
	if changes != nil {
		m["changes"] = changes
	}
	return mustJSON(t, m)
}

func notePayload(t *testing.T, noteableType string, userID, noteID, iid int) []byte {
	t.Helper()
	m := map[string]any{
		"object_kind": "note",
		"user":        map[string]any{"id": userID},
		"project":     hostileURLs,
		"object_attributes": map[string]any{
			"id":            noteID,
			"action":        "create",
			"noteable_type": noteableType,
			"note":          "payload body must not be used",
			"url":           "https://attacker.example/note",
		},
	}
	switch noteableType {
	case "Issue":
		m["issue"] = map[string]any{"iid": iid}
	case "MergeRequest":
		m["merge_request"] = map[string]any{"iid": iid}
	}
	return mustJSON(t, m)
}

func labelChange(previous, current []string) map[string]any {
	conv := func(ts []string) []map[string]any {
		out := make([]map[string]any, 0, len(ts))
		for _, title := range ts {
			out = append(out, map[string]any{"title": title, "id": 1})
		}
		return out
	}
	return map[string]any{"previous": conv(previous), "current": conv(current)}
}

func labelEvent(id int, action, name string, user UserRef, at time.Time) ResourceLabelEvent {
	e := ResourceLabelEvent{ID: id, Action: action, User: user, CreatedAt: at}
	e.Label.Name = name
	return e
}

func sameProjectMR(iid int, state string) *MergeRequest {
	return &MergeRequest{
		IID: iid, State: state, Labels: []string{"bug"},
		SourceProjectID: 7, TargetProjectID: 7,
		SourceBranch: "feat", TargetBranch: "main",
		Author:    alice,
		CreatedAt: recent,
		UpdatedAt: recent,
	}
}

func TestBuildWebhookEvents_IssueLabelAdded(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"bug", "ready-to-code"}, Author: bob, UpdatedAt: recent}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "bug", bob, recent.Add(-time.Hour)),
		labelEvent(2, "add", "ready-to-code", alice, recent),
	}
	p := newWebhookPoller(mc)

	got, err := p.BuildWebhookEvents(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange([]string{"bug"}, []string{"bug", "ready-to-code"})))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	we := got[0]
	if we.Event.Type != "issue_label" || we.Event.ChangedLabel != "ready-to-code" {
		t.Errorf("event = %+v", we.Event)
	}
	if we.ActorID != alice.ID || we.Event.NoteAuthorID != alice.ID {
		t.Errorf("actor = %d / %d, want %d", we.ActorID, we.Event.NoteAuthorID, alice.ID)
	}
	ne := we.Normalized
	if ne.Transition.Kind != "label_changed" || ne.Transition.Label == nil || ne.Transition.Label.Action != "added" {
		t.Errorf("transition = %+v", ne.Transition)
	}
	if ne.Actor.ID != "alice" || ne.Actor.Role != "write" {
		t.Errorf("actor = %+v", ne.Actor)
	}
	if ne.Entity.URL != "https://gitlab.example.com/group/project/-/issues/5" {
		t.Errorf("entity URL = %q (payload URLs must not be used)", ne.Entity.URL)
	}
	if len(we.Stages) != 1 || we.Stages[0] != "code" {
		t.Errorf("stages = %v, want [code]", we.Stages)
	}
	// Same dedup key the poller computes for the same label add: both key
	// on the resource label event ID.
	want := RoutableEvent{Type: "issue_label", IID: 5, ChangedLabel: "ready-to-code", UpdatedAt: recent, LabelEventID: 2}.Key()
	if we.Event.Key() != want {
		t.Errorf("Key() = %q, want %q", we.Event.Key(), want)
	}
}

func TestBuildWebhookEvents_IssueMultiLabelDiff(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-for-review", "ready-to-code"}, Author: bob, UpdatedAt: recent}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "triaged", bob, recent.Add(-time.Hour)),
		labelEvent(2, "remove", "triaged", alice, recent),
		labelEvent(3, "add", "ready-to-code", alice, recent),
		labelEvent(4, "add", "ready-for-review", alice, recent),
	}
	p := newWebhookPoller(mc)

	got, err := p.BuildWebhookEvents(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange([]string{"triaged"}, []string{"ready-to-code", "ready-for-review"})))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Two additions fan out; the validated removal is not emitted.
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Event.ChangedLabel != "ready-for-review" || got[1].Event.ChangedLabel != "ready-to-code" {
		t.Errorf("labels = %q, %q", got[0].Event.ChangedLabel, got[1].Event.ChangedLabel)
	}
	if got[0].Stages[0] != "review" || got[1].Stages[0] != "code" {
		t.Errorf("stages = %v, %v", got[0].Stages, got[1].Stages)
	}
}

func TestBuildWebhookEvents_BotAppliedLabelAdditions(t *testing.T) {
	botUser := UserRef{ID: 100, Username: "project_1_bot_abc", Bot: true}
	mc := newMockClient()
	mc.memberLevel[botUser.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-for-review", "ready-to-code"}, Author: bob, UpdatedAt: recent}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", botUser, recent),
		labelEvent(2, "add", "ready-for-review", botUser, recent),
	}
	p := newWebhookPoller(mc)

	got, err := p.BuildWebhookEvents(context.Background(),
		issuePayload(t, "update", botUser.ID, 5, labelChange(nil, []string{"ready-to-code", "ready-for-review"})))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (bot-applied label additions must not be filtered)", len(got))
	}
	if got[0].Event.ChangedLabel != "ready-for-review" || got[1].Event.ChangedLabel != "ready-to-code" {
		t.Errorf("labels = %q, %q", got[0].Event.ChangedLabel, got[1].Event.ChangedLabel)
	}
	if len(got[0].Stages) != 1 || got[0].Stages[0] != "review" || len(got[1].Stages) != 1 || got[1].Stages[0] != "code" {
		t.Errorf("stages = %v, %v", got[0].Stages, got[1].Stages)
	}
	for i, we := range got {
		if we.ActorID != botUser.ID || we.Event.NoteAuthorID != botUser.ID {
			t.Errorf("event %d actor = %d / %d, want %d", i, we.ActorID, we.Event.NoteAuthorID, botUser.ID)
		}
	}
}

func TestBuildWebhookEvents_IssueLabelRemovedOnly(t *testing.T) {
	mc := newMockClient()
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{}, UpdatedAt: recent}
	mc.labelEvents[5] = []ResourceLabelEvent{
		labelEvent(1, "add", "ready-to-code", bob, recent.Add(-time.Hour)),
		labelEvent(2, "remove", "ready-to-code", alice, recent),
	}
	p := newWebhookPoller(mc)

	got, err := p.BuildWebhookEvents(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange([]string{"ready-to-code"}, nil)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d events, want 0 (removals are not routable)", len(got))
	}
}

func TestBuildWebhookEvents_IssueLabelFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		events  []ResourceLabelEvent
		eventsE error
		change  map[string]any
		wantErr string
	}{
		{
			// The payload names alice for a label bob already applied:
			// alice has write access, the label is present, but nothing
			// links alice to this change.
			name:   "named actor for already-present label",
			labels: []string{"ready-to-code"},
			events: []ResourceLabelEvent{
				labelEvent(1, "add", "ready-to-code", bob, recent),
			},
			change:  labelChange(nil, []string{"ready-to-code"}),
			wantErr: "does not match the payload user",
		},
		{
			name:   "stale label event replay",
			labels: []string{"ready-to-code"},
			events: []ResourceLabelEvent{
				labelEvent(1, "add", "ready-to-code", alice, webhookTestNow.Add(-3*time.Hour)),
			},
			change:  labelChange(nil, []string{"ready-to-code"}),
			wantErr: "older than",
		},
		{
			name:    "added label absent from re-fetch",
			labels:  []string{"bug"},
			change:  labelChange(nil, []string{"ready-to-code"}),
			wantErr: "contradicts current labels",
		},
		{
			name:    "removed label still present",
			labels:  []string{"ready-to-code"},
			change:  labelChange([]string{"ready-to-code"}, nil),
			wantErr: "contradicts current labels",
		},
		{
			name:    "no label event",
			labels:  []string{"ready-to-code"},
			change:  labelChange(nil, []string{"ready-to-code"}),
			wantErr: "no resource label event",
		},
		{
			name:   "latest event is a removal",
			labels: []string{"ready-to-code"},
			events: []ResourceLabelEvent{
				labelEvent(1, "add", "ready-to-code", alice, recent),
				labelEvent(2, "remove", "ready-to-code", alice, recent),
			},
			change:  labelChange(nil, []string{"ready-to-code"}),
			wantErr: `latest label event is "remove"`,
		},
		{
			name:    "label events unavailable",
			labels:  []string{"ready-to-code"},
			eventsE: fmt.Errorf("boom"),
			change:  labelChange(nil, []string{"ready-to-code"}),
			wantErr: "re-fetch label events",
		},
		{
			name:    "empty diff",
			labels:  []string{"bug"},
			change:  labelChange([]string{"bug"}, []string{"bug"}),
			wantErr: "does not resolve",
		},
		{
			name:    "label without title",
			labels:  []string{"bug"},
			change:  map[string]any{"previous": []any{}, "current": []any{map[string]any{"name": "bug"}}},
			wantErr: "no title",
		},
		{
			name:    "label without title in previous",
			labels:  []string{"bug"},
			change:  map[string]any{"previous": []any{map[string]any{"name": "bug"}}, "current": []any{}},
			wantErr: "no title",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mc.memberLevel[alice.ID] = 30
			mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: tt.labels, UpdatedAt: recent}
			mc.labelEvents[5] = tt.events
			if tt.eventsE != nil {
				mc.labelEventsErr[5] = tt.eventsE
			}
			p := newWebhookPoller(mc)
			got, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, "update", alice.ID, 5, tt.change))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q (events %v)", err, tt.wantErr, got)
			}
			if got != nil {
				t.Errorf("events must be nil on failure, got %v", got)
			}
		})
	}
}

func TestBuildWebhookEvents_IssueLifecycle(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		mc := newMockClient()
		mc.issue[9] = &Issue{IID: 9, State: "opened", Author: alice, CreatedAt: recent}
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, "open", alice.ID, 9, nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ne := got[0].Normalized
		if ne.Transition.Kind != "opened" || ne.Entity.Kind != "work_item" || ne.Source.RawType != "issues" || ne.Source.RawAction != "opened" {
			t.Errorf("normalized = %+v", ne)
		}
		if !ne.Actor.IsEntityAuthor {
			t.Error("expected issue author to be entity author")
		}
		if len(got[0].Stages) != 0 {
			t.Errorf("issue open must not route, got %v", got[0].Stages)
		}
	})
	t.Run("close", func(t *testing.T) {
		mc := newMockClient()
		mc.issue[9] = &Issue{IID: 9, State: "closed", Author: bob, ClosedBy: alice, ClosedAt: recent}
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, "close", alice.ID, 9, nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		we := got[0]
		if we.Normalized.Transition.Kind != "closed" || we.Normalized.Actor.ID != "alice" {
			t.Errorf("normalized = %+v", we.Normalized)
		}
		if we.Event.Key() != fmt.Sprintf("issue_event-9-closed-%d", recent.Unix()) {
			t.Errorf("Key() = %q", we.Event.Key())
		}
		// Retro is for change proposals only.
		if len(we.Stages) != 0 {
			t.Errorf("issue close must not route, got %v", we.Stages)
		}
	})

	failures := []struct {
		name    string
		action  string
		issue   *Issue
		wantErr string
	}{
		{"open but closed", "open", &Issue{IID: 9, State: "closed", Author: alice, CreatedAt: recent}, "not opened"},
		{"open by someone else", "open", &Issue{IID: 9, State: "opened", Author: bob, CreatedAt: recent}, "does not match"},
		{"open long ago", "open", &Issue{IID: 9, State: "opened", Author: alice, CreatedAt: webhookTestNow.Add(-48 * time.Hour)}, "older than"},
		{"close but open", "close", &Issue{IID: 9, State: "opened", ClosedBy: alice, ClosedAt: recent}, "close: state"},
		{"close without closed_by", "close", &Issue{IID: 9, State: "closed", ClosedAt: recent}, "not recorded"},
		{"close without closed_at", "close", &Issue{IID: 9, State: "closed", ClosedBy: alice}, "close: state"},
		{"reopen", "reopen", &Issue{IID: 9, State: "opened"}, "no event-time actor evidence"},
		{"bare update", "update", &Issue{IID: 9, State: "opened"}, "uncheckable"},
		{"unknown action", "frobnicate", &Issue{IID: 9, State: "opened"}, "not a checkable transition"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mc.issue[9] = tt.issue
			p := newWebhookPoller(mc)
			_, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, tt.action, alice.ID, 9, nil))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}

	t.Run("issue not found", func(t *testing.T) {
		p := newWebhookPoller(newMockClient())
		_, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, "open", alice.ID, 9, nil))
		if err == nil || !strings.Contains(err.Error(), "re-fetch issue #9") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBuildWebhookEvents_MergeRequest(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		mc := newMockClient()
		mc.mr[3] = sameProjectMR(3, "opened")
		mc.projectPaths[7] = "group/project"
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), mrPayload(t, "open", alice.ID, 3, nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		we := got[0]
		if we.Normalized.Transition.Kind != "opened" || we.Normalized.Entity.Kind != "change_proposal" {
			t.Errorf("normalized = %+v", we.Normalized)
		}
		if we.Normalized.State.ChangeProposal == nil || we.Normalized.State.ChangeProposal.IsFork {
			t.Errorf("change proposal = %+v", we.Normalized.State.ChangeProposal)
		}
		if len(we.Stages) != 1 || we.Stages[0] != "review" {
			t.Errorf("stages = %v, want [review]", we.Stages)
		}
		if we.Event.Key() != fmt.Sprintf("mr_event-3-opened-%d", recent.Unix()) {
			t.Errorf("Key() = %q", we.Event.Key())
		}
	})
	t.Run("merge", func(t *testing.T) {
		mc := newMockClient()
		mr := sameProjectMR(3, "merged")
		mr.MergedAt = recent
		mr.ClosedAt = recent
		mr.MergeUser = bob
		mc.mr[3] = mr
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), mrPayload(t, "merge", bob.ID, 3, nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		we := got[0]
		if we.Normalized.Transition.Kind != "merged" || we.Normalized.Actor.ID != "bob" {
			t.Errorf("normalized = %+v", we.Normalized)
		}
		if len(we.Stages) != 1 || we.Stages[0] != "retro" {
			t.Errorf("stages = %v, want [retro]", we.Stages)
		}
	})
	t.Run("close", func(t *testing.T) {
		mc := newMockClient()
		mr := sameProjectMR(3, "closed")
		mr.ClosedAt = recent
		mr.ClosedBy = bob
		mc.mr[3] = mr
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), mrPayload(t, "close", bob.ID, 3, nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got[0].Normalized.Transition.Kind != "closed" || got[0].Stages[0] != "retro" {
			t.Errorf("got %+v", got[0])
		}
	})
	t.Run("bot close is filtered like the poller", func(t *testing.T) {
		mc := newMockClient()
		mr := sameProjectMR(3, "closed")
		mr.ClosedAt = recent
		mr.ClosedBy = UserRef{ID: 100, Username: "fullsend-bot"}
		mc.mr[3] = mr
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), mrPayload(t, "close", 100, 3, nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("bot close must be filtered, got %v", got)
		}
	})

	reopenedStale := sameProjectMR(3, "opened")
	reopenedStale.ClosedAt = recent
	reopenedStale.ClosedBy = alice
	mergedButClaimedClose := sameProjectMR(3, "closed")
	mergedButClaimedClose.ClosedAt = recent
	mergedButClaimedClose.MergedAt = recent
	mergedButClaimedClose.ClosedBy = alice
	notMerged := sameProjectMR(3, "opened")
	oldOpen := sameProjectMR(3, "opened")
	oldOpen.CreatedAt = webhookTestNow.Add(-48 * time.Hour)
	failures := []struct {
		name    string
		action  string
		mr      *MergeRequest
		changes map[string]any
		wantErr string
	}{
		// Reopened MR keeps a stale closed_at/closed_by: a replayed or
		// forged close must not pass on timestamps alone.
		{"close against reopened MR with stale timestamps", "close", reopenedStale, nil, "close: state"},
		{"close of a merged MR", "close", mergedButClaimedClose, nil, "timestamps inconsistent"},
		{"merge of an open MR", "merge", notMerged, nil, "merged_at unset"},
		{"open of a closed MR", "open", mergedButClaimedClose, nil, "not opened"},
		{"open long ago", "open", oldOpen, nil, "older than"},
		{"open by someone else", "open", sameProjectMR(3, "opened"), nil, ""},
		{"reopen", "reopen", sameProjectMR(3, "opened"), nil, "no event-time actor evidence"},
		{"label update", "update", sameProjectMR(3, "opened"), map[string]any{"labels": labelChange(nil, []string{"ready-for-review"})}, "not supported"},
		{"bare update", "update", sameProjectMR(3, "opened"), nil, "uncheckable"},
		{"unknown action", "approved", sameProjectMR(3, "opened"), nil, "not a checkable transition"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mc.mr[3] = tt.mr
			p := newWebhookPoller(mc)
			userID := alice.ID
			wantErr := tt.wantErr
			if wantErr == "" {
				userID = bob.ID
				wantErr = "does not match the payload user"
			}
			_, err := p.BuildWebhookEvents(context.Background(), mrPayload(t, tt.action, userID, 3, tt.changes))
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("err = %v, want containing %q", err, wantErr)
			}
		})
	}

	t.Run("MR not found", func(t *testing.T) {
		p := newWebhookPoller(newMockClient())
		_, err := p.BuildWebhookEvents(context.Background(), mrPayload(t, "open", alice.ID, 3, nil))
		if err == nil || !strings.Contains(err.Error(), "re-fetch merge request !3") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBuildWebhookEvents_Notes(t *testing.T) {
	t.Run("issue slash command", func(t *testing.T) {
		mc := newMockClient()
		mc.memberLevel[alice.ID] = 30
		mc.issue[4] = &Issue{IID: 4, State: "opened", Labels: []string{"bug"}, Author: bob}
		mc.notes[4] = []Note{
			{ID: 11, Body: "earlier", Author: bob, CreatedAt: recent},
			{ID: 12, Body: "/fs-triage please", Author: alice, CreatedAt: recent},
		}
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), notePayload(t, "Issue", alice.ID, 12, 4))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		we := got[0]
		if we.Event.Key() != "note-12" {
			t.Errorf("Key() = %q", we.Event.Key())
		}
		c := we.Normalized.Transition.Comment
		if c == nil || c.Command != "/fs-triage" || c.Body != "/fs-triage please" {
			t.Errorf("comment = %+v (body must come from the re-fetch)", c)
		}
		if len(we.Stages) != 1 || we.Stages[0] != "triage" {
			t.Errorf("stages = %v", we.Stages)
		}
		if len(we.Event.Labels) != 1 || we.Event.Labels[0] != "bug" {
			t.Errorf("labels = %v", we.Event.Labels)
		}
	})
	t.Run("MR note", func(t *testing.T) {
		mc := newMockClient()
		mc.memberLevel[alice.ID] = 30
		mc.mr[3] = sameProjectMR(3, "opened")
		mc.projectPaths[7] = "group/project"
		mc.mrNotes[3] = []Note{{ID: 21, Body: "/fs-code", Author: alice, CreatedAt: recent}}
		p := newWebhookPoller(mc)
		got, err := p.BuildWebhookEvents(context.Background(), notePayload(t, "MergeRequest", alice.ID, 21, 3))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		we := got[0]
		if we.Event.Type != "mr_note" || we.Normalized.Entity.Kind != "change_proposal" {
			t.Errorf("event = %+v", we.Event)
		}
		if len(we.Stages) != 1 || we.Stages[0] != "code" {
			t.Errorf("stages = %v", we.Stages)
		}
	})

	failures := []struct {
		name    string
		payload func(t *testing.T) []byte
		setup   func(mc *mockClient)
		wantErr string
	}{
		{
			name:    "note missing",
			payload: func(t *testing.T) []byte { return notePayload(t, "Issue", alice.ID, 99, 4) },
			wantErr: "not found",
		},
		{
			name:    "note by another user",
			payload: func(t *testing.T) []byte { return notePayload(t, "Issue", bob.ID, 12, 4) },
			wantErr: "does not match",
		},
		{
			name:    "old note replay",
			payload: func(t *testing.T) []byte { return notePayload(t, "Issue", alice.ID, 13, 4) },
			wantErr: "older than",
		},
		{
			name:    "notes unavailable",
			payload: func(t *testing.T) []byte { return notePayload(t, "Issue", alice.ID, 12, 4) },
			setup:   func(mc *mockClient) { mc.noteErr[4] = fmt.Errorf("boom") },
			wantErr: "re-fetch notes",
		},
		{
			name:    "issue unavailable",
			payload: func(t *testing.T) []byte { return notePayload(t, "Issue", alice.ID, 12, 4) },
			setup:   func(mc *mockClient) { delete(mc.issue, 4) },
			wantErr: "re-fetch issue",
		},
		{
			name:    "MR unavailable",
			payload: func(t *testing.T) []byte { return notePayload(t, "MergeRequest", alice.ID, 21, 3) },
			setup: func(mc *mockClient) {
				mc.mrNotes[3] = []Note{{ID: 21, Body: "x", Author: alice, CreatedAt: recent}}
			},
			wantErr: "re-fetch merge request",
		},
		{
			name:    "snippet note",
			payload: func(t *testing.T) []byte { return notePayload(t, "Snippet", alice.ID, 12, 4) },
			wantErr: "unsupported noteable_type",
		},
		{
			name: "missing issue iid",
			payload: func(t *testing.T) []byte {
				return mustJSON(t, map[string]any{"object_kind": "note", "user": map[string]any{"id": 42},
					"object_attributes": map[string]any{"id": 12, "action": "create", "noteable_type": "Issue"}})
			},
			wantErr: "no valid issue iid",
		},
		{
			name: "missing MR iid",
			payload: func(t *testing.T) []byte {
				return mustJSON(t, map[string]any{"object_kind": "note", "user": map[string]any{"id": 42},
					"object_attributes": map[string]any{"id": 12, "action": "create", "noteable_type": "MergeRequest"}})
			},
			wantErr: "no valid merge request iid",
		},
		{
			name: "missing note id",
			payload: func(t *testing.T) []byte {
				return mustJSON(t, map[string]any{"object_kind": "note", "user": map[string]any{"id": 42},
					"object_attributes": map[string]any{"action": "create", "noteable_type": "Issue"}})
			},
			wantErr: "no valid note id",
		},
		{
			name: "note edit",
			payload: func(t *testing.T) []byte {
				return mustJSON(t, map[string]any{"object_kind": "note", "user": map[string]any{"id": 42},
					"object_attributes": map[string]any{"id": 12, "action": "update", "noteable_type": "Issue"}})
			},
			wantErr: "not a checkable transition",
		},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mc.issue[4] = &Issue{IID: 4, State: "opened"}
			mc.notes[4] = []Note{
				{ID: 12, Body: "/fs-code", Author: alice, CreatedAt: recent},
				{ID: 13, Body: "/fs-code", Author: alice, CreatedAt: webhookTestNow.Add(-24 * time.Hour)},
			}
			if tt.setup != nil {
				tt.setup(mc)
			}
			p := newWebhookPoller(mc)
			_, err := p.BuildWebhookEvents(context.Background(), tt.payload(t))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildWebhookEvents_InvalidPayloads(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		wantErr string
	}{
		{"empty", nil, "empty"},
		{"oversized", make([]byte, maxWebhookPayloadBytes+1), "exceeding"},
		{"not JSON", []byte("{secret-token-value"), "not a valid GitLab webhook body"},
		{"string iid", []byte(`{"object_kind":"issue","user":{"id":42},"object_attributes":{"iid":"5/../../evil","action":"open"}}`), "not a valid"},
		{"no user", []byte(`{"object_kind":"issue","object_attributes":{"iid":5,"action":"open"}}`), "no valid user id"},
		{"negative iid", []byte(`{"object_kind":"issue","user":{"id":42},"object_attributes":{"iid":-1,"action":"open"}}`), "no valid iid"},
		{"MR zero iid", []byte(`{"object_kind":"merge_request","user":{"id":42},"object_attributes":{"iid":0,"action":"open"}}`), "no valid iid"},
		{"unsupported kind", []byte(`{"object_kind":"pipeline","user":{"id":42}}`), "unsupported webhook object_kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newWebhookPoller(newMockClient())
			_, err := p.BuildWebhookEvents(context.Background(), tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "secret-token-value") {
				t.Errorf("error leaks payload contents: %v", err)
			}
		})
	}

	t.Run("nil client", func(t *testing.T) {
		p := New(nil, nil, "group/project", Options{})
		if _, err := p.BuildWebhookEvents(context.Background(), []byte("{}")); err == nil {
			t.Fatal("expected error")
		}
	})
}

// routerFunc adapts a function to dispatch.EventRouter.
type routerFunc func(*dispatch.NormalizedEvent) ([]string, error)

func (f routerFunc) Route(e *dispatch.NormalizedEvent) ([]string, error) { return f(e) }

func TestBuildWebhookEvents_RouterAndNormalizeErrors(t *testing.T) {
	t.Run("router error", func(t *testing.T) {
		mc := newMockClient()
		mc.issue[9] = &Issue{IID: 9, State: "opened", Author: alice, CreatedAt: recent}
		p := newWebhookPoller(mc)
		p.router = routerFunc(func(*dispatch.NormalizedEvent) ([]string, error) { return nil, fmt.Errorf("boom") })
		_, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, "open", alice.ID, 9, nil))
		if err == nil || !strings.Contains(err.Error(), "route webhook") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no router", func(t *testing.T) {
		mc := newMockClient()
		mc.issue[9] = &Issue{IID: 9, State: "opened", Author: alice, CreatedAt: recent}
		p := newWebhookPoller(mc)
		p.router = nil
		got, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, "open", alice.ID, 9, nil))
		if err != nil || len(got) != 1 || got[0].Stages != nil {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("normalize error", func(t *testing.T) {
		// A non-label event whose actor cannot be resolved at normalization.
		mc := newMockClient()
		mc.issue[9] = &Issue{IID: 9, State: "opened", Author: UserRef{ID: alice.ID}, CreatedAt: recent}
		p := newWebhookPoller(mc)
		_, err := p.BuildWebhookEvents(context.Background(), issuePayload(t, "open", alice.ID, 9, nil))
		if err == nil {
			t.Fatal("expected an error for an actor without a username")
		}
	})
}

// labelEventsChanging serves the real label events on the first call and
// a different snapshot on every later call.
type labelEventsChanging struct {
	*mockClient
	calls  *int
	second []ResourceLabelEvent
}

func (l *labelEventsChanging) ListResourceLabelEvents(ctx context.Context, owner, repo string, iid int) ([]ResourceLabelEvent, error) {
	*l.calls++
	if *l.calls > 1 {
		return l.second, nil
	}
	return l.mockClient.ListResourceLabelEvents(ctx, owner, repo, iid)
}

// TestBuildWebhookEvents_LabelActorNotReResolved checks that the validated
// label actor is carried through normalization: a later snapshot naming a
// different actor, or showing an intervening removal, must not change the
// actor or role used for routing.
func TestBuildWebhookEvents_LabelActorNotReResolved(t *testing.T) {
	for name, second := range map[string][]ResourceLabelEvent{
		"different actor add": {
			labelEvent(1, "add", "ready-to-code", alice, recent),
			labelEvent(2, "add", "ready-to-code", bob, recent),
		},
		"intervening removal": {
			labelEvent(1, "add", "ready-to-code", alice, recent),
			labelEvent(2, "remove", "ready-to-code", bob, recent),
		},
		"empty": nil,
	} {
		t.Run(name, func(t *testing.T) {
			mc := newMockClient()
			mc.memberLevel[alice.ID] = 30
			mc.memberLevel[bob.ID] = 50
			mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, UpdatedAt: recent}
			mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(1, "add", "ready-to-code", alice, recent)}
			p := newWebhookPoller(mc)
			calls := 0
			p.client = &labelEventsChanging{mockClient: mc, calls: &calls, second: second}
			got, err := p.BuildWebhookEvents(context.Background(),
				issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"})))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			we := got[0]
			if we.ActorID != alice.ID || we.Normalized.Actor.ID != "alice" || we.Normalized.Actor.Role != "write" {
				t.Errorf("actor = %d %q %q, want the validated actor alice with write role",
					we.ActorID, we.Normalized.Actor.ID, we.Normalized.Actor.Role)
			}
			if calls != 1 {
				t.Errorf("label events fetched %d times, want 1", calls)
			}
		})
	}
}

// TestBuildWebhookEvents_LabelKeyStableAcrossIssueUpdates checks that an
// unrelated issue update between two constructions of the same validated
// label addition does not change the dedup key.
func TestBuildWebhookEvents_LabelKeyStableAcrossIssueUpdates(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, UpdatedAt: recent}
	mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(1, "add", "ready-to-code", alice, recent)}
	p := newWebhookPoller(mc)
	payload := issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"}))

	first, err := p.BuildWebhookEvents(context.Background(), payload)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	mc.issue[5].UpdatedAt = recent.Add(5 * time.Minute)
	second, err := p.BuildWebhookEvents(context.Background(), payload)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first[0].Event.Key() != second[0].Event.Key() {
		t.Errorf("key changed after unrelated issue update: %q vs %q", first[0].Event.Key(), second[0].Event.Key())
	}
}

func TestCheckFresh_FutureAndBoundary(t *testing.T) {
	p := newWebhookPoller(newMockClient())
	for _, tt := range []struct {
		name    string
		at      time.Time
		wantErr string
	}{
		{"within skew", webhookTestNow.Add(webhookMaxClockSkew), ""},
		{"beyond skew", webhookTestNow.Add(webhookMaxClockSkew + time.Second), "in the future"},
		{"far future", webhookTestNow.Add(24 * time.Hour), "in the future"},
		{"max age boundary", webhookTestNow.Add(-webhookMaxEventAge), ""},
		{"beyond max age", webhookTestNow.Add(-webhookMaxEventAge - time.Second), "old"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := p.checkFresh(tt.at, "x")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestBuildWebhookEvents_DispatchHandoff checks that builder output feeds
// the existing signed, chunked pipeline-input transport (ADR 0131)
// unchanged.
func TestBuildWebhookEvents_DispatchHandoff(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.issue[5] = &Issue{IID: 5, State: "opened", Labels: []string{"ready-to-code"}, UpdatedAt: recent}
	mc.labelEvents[5] = []ResourceLabelEvent{labelEvent(1, "add", "ready-to-code", alice, recent)}
	p := newWebhookPoller(mc)

	got, err := p.BuildWebhookEvents(context.Background(),
		issuePayload(t, "update", alice.ID, 5, labelChange(nil, []string{"ready-to-code"})))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	we := got[0]
	if err := p.dispatch(context.Background(), p.owner, p.repo, we.Stages[0], we.Event); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d", len(mc.pipelineCalls))
	}
	call := mc.pipelineCalls[0]
	if !call.ViaInputs {
		t.Error("expected typed pipeline-input transport")
	}
	v := call.Variables
	if v["STAGE"] != "code" || v["ACTOR_ID"] != "42" || v["RESOURCE_KEY"] != "issue-5" ||
		v["ORIGINATING_URL"] != "https://gitlab.example.com/group/project/-/issues/5" {
		t.Errorf("variables = %v", v)
	}
	if v[forge.VarDispatchHMAC] != computeDispatchHMAC(testDispatchSecret, v) {
		t.Errorf("expected HMAC-signed dispatch, variables = %v", v)
	}
	raw, err := base64.StdEncoding.DecodeString(v["EVENT_PAYLOAD_B64"])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if strings.Contains(string(raw), "attacker.example") {
		t.Errorf("payload URLs leaked into dispatch payload: %s", raw)
	}
}

func TestTranslateEventType_IssueEvent(t *testing.T) {
	for _, action := range []string{"opened", "closed"} {
		ev := RoutableEvent{Type: "issue_event", Action: action}
		if got := translateEventType(ev); got != action {
			t.Errorf("translateEventType(%s) = %q", action, got)
		}
		if got := mapRawAction(ev); got != action {
			t.Errorf("mapRawAction(%s) = %q", action, got)
		}
	}
	if mapRawType("issue_event") != "issues" || entityKind("issue_event") != "work_item" {
		t.Error("issue_event must map to issues/work_item")
	}
}
