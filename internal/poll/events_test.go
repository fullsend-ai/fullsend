package poll

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// newEventsPoller creates a Poller with test defaults suitable for events
// and convert tests (sets projectPath, gitlabURL, and botUserID).
func newEventsPoller(client GitLabClient) *Poller {
	return New(client, nil, "group/project", Options{
		BotUserID: 100,
		GitLabURL: "https://gitlab.com",
	})
}

func TestDiscoverAllEvents_IssueNotes(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"bug"}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "hello world", Author: UserRef{ID: 42, Username: "alice"}, CreatedAt: now},
	}

	p := newEventsPoller(mc)
	events, _, minSkipped, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !minSkipped.IsZero() {
		t.Errorf("expected zero minSkippedAt, got %v", minSkipped)
	}

	var noteEvents []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_note" {
			noteEvents = append(noteEvents, e)
		}
	}
	if len(noteEvents) != 1 {
		t.Fatalf("expected 1 issue_note event, got %d", len(noteEvents))
	}
	got := noteEvents[0]
	if got.IID != 1 {
		t.Errorf("IID = %d, want 1", got.IID)
	}
	if got.NoteID != 10 {
		t.Errorf("NoteID = %d, want 10", got.NoteID)
	}
	if got.NoteBody != "hello world" {
		t.Errorf("NoteBody = %q, want %q", got.NoteBody, "hello world")
	}
	if got.NoteAuthorID != 42 {
		t.Errorf("NoteAuthorID = %d, want 42", got.NoteAuthorID)
	}
}

func TestDiscoverAllEvents_LabelEvents(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"ready-to-code"}},
	}
	// No previous label state -> ErrNotFound -> empty state -> "ready-to-code" is new.
	mc.notes[1] = []Note{} // no notes

	p := newEventsPoller(mc)
	events, labelState, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var labelEvents []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_label" {
			labelEvents = append(labelEvents, e)
		}
	}
	if len(labelEvents) != 1 {
		t.Fatalf("expected 1 issue_label event, got %d", len(labelEvents))
	}
	got := labelEvents[0]
	if got.IID != 1 {
		t.Errorf("IID = %d, want 1", got.IID)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "ready-to-code" {
		t.Errorf("Labels = %v, want [ready-to-code]", got.Labels)
	}

	// Label state should track "ready-to-code" for IID 1.
	if ls, ok := labelState[1]; !ok {
		t.Error("labelState missing IID 1")
	} else if len(ls) != 1 || ls[0] != "ready-to-code" {
		t.Errorf("labelState[1] = %v, want [ready-to-code]", ls)
	}
}

func TestDiscoverAllEvents_MRMergeEvents(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             5,
			MergedAt:        now,
			MergedBy:        UserRef{ID: 10, Username: "bob", Bot: false},
			SourceProjectID: 1,
			TargetProjectID: 1,
			UpdatedAt:       now,
		},
	}
	mc.mrNotes[5] = []Note{} // no MR notes

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var mrEvents []RoutableEvent
	for _, e := range events {
		if e.Type == "mr_event" {
			mrEvents = append(mrEvents, e)
		}
	}
	if len(mrEvents) != 1 {
		t.Fatalf("expected 1 mr_event, got %d", len(mrEvents))
	}
	got := mrEvents[0]
	if got.IID != 5 {
		t.Errorf("IID = %d, want 5", got.IID)
	}
	if got.NoteAuthorID != 10 {
		t.Errorf("NoteAuthorID (MergedByID) = %d, want 10", got.NoteAuthorID)
	}
	if got.MRSource != 1 || got.MRTarget != 1 {
		t.Errorf("MRSource=%d, MRTarget=%d, want 1, 1", got.MRSource, got.MRTarget)
	}
}

func TestDiscoverAllEvents_MRNotes(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             5,
			SourceProjectID: 1,
			TargetProjectID: 1,
			UpdatedAt:       now,
		},
	}
	mc.mrNotes[5] = []Note{
		{ID: 20, Body: "looks good", Author: UserRef{ID: 42, Username: "alice"}, CreatedAt: now},
	}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var mrNotes []RoutableEvent
	for _, e := range events {
		if e.Type == "mr_note" {
			mrNotes = append(mrNotes, e)
		}
	}
	if len(mrNotes) != 1 {
		t.Fatalf("expected 1 mr_note event, got %d", len(mrNotes))
	}
	got := mrNotes[0]
	if got.IID != 5 {
		t.Errorf("IID = %d, want 5", got.IID)
	}
	if got.NoteID != 20 {
		t.Errorf("NoteID = %d, want 20", got.NoteID)
	}
	if got.NoteBody != "looks good" {
		t.Errorf("NoteBody = %q, want %q", got.NoteBody, "looks good")
	}
	if got.MRSource != 1 || got.MRTarget != 1 {
		t.Errorf("MRSource=%d, MRTarget=%d, want 1, 1", got.MRSource, got.MRTarget)
	}
}

func TestDiscoverAllEvents_SkipsOldNotes(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"bug"}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "old comment", Author: UserRef{ID: 42}, CreatedAt: since.Add(-time.Hour)},
	}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range events {
		if e.Type == "issue_note" {
			t.Errorf("expected no issue_note events for old notes, got event with NoteID=%d", e.NoteID)
		}
	}
}

func TestDiscoverAllEvents_NoteFetchFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()

	// Set up existing label state so we can verify restoration.
	mc.variables["FULLSEND_LABEL_STATE"] = `{"1":["ready-to-code"]}`

	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"ready-to-code", "ready-for-review"}},
	}
	// ListIssueNotes fails for IID 1.
	mc.noteErr[1] = fmt.Errorf("API timeout")

	p := newEventsPoller(mc)
	events, labelState, minSkipped, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No events should be returned for the failed issue.
	for _, e := range events {
		if e.IID == 1 {
			t.Errorf("expected no events for IID 1 after note failure, got %s", e.Type)
		}
	}

	// minSkippedAt should be set to the issue's UpdatedAt.
	if minSkipped.IsZero() {
		t.Fatal("expected minSkippedAt to be set")
	}
	if !minSkipped.Equal(now) {
		t.Errorf("minSkippedAt = %v, want %v", minSkipped, now)
	}

	// Label state should be restored to previous state ["ready-to-code"],
	// not the updated state ["ready-to-code", "ready-for-review"].
	ls, ok := labelState[1]
	if !ok {
		t.Fatal("expected labelState to contain IID 1")
	}
	if len(ls) != 1 || ls[0] != "ready-to-code" {
		t.Errorf("labelState[1] = %v, want [ready-to-code] (restored)", ls)
	}
}

func TestDiscoverAllEvents_MRListFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"bug"}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "a comment", Author: UserRef{ID: 42}, CreatedAt: now},
	}
	mc.mrsErr = fmt.Errorf("MR API down")

	p := newEventsPoller(mc)
	events, _, minSkipped, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v (should be nil, MR failure is non-fatal)", err)
	}

	// Issue events should still be returned.
	var noteEvents []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_note" {
			noteEvents = append(noteEvents, e)
		}
	}
	if len(noteEvents) != 1 {
		t.Fatalf("expected 1 issue_note event despite MR failure, got %d", len(noteEvents))
	}

	// minSkippedAt should be set.
	if minSkipped.IsZero() {
		t.Error("expected minSkippedAt to be set after MR list failure")
	}
}

func TestDiscoverSlashCommands_IssueCommands(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.events = []ProjectEvent{
		{
			ID:        1,
			Author:    UserRef{ID: 42, Username: "alice"},
			CreatedAt: now,
			Note: EventNote{
				ID:           100,
				NoteableType: "Issue",
				NoteableIID:  1,
				Body:         "/fs-triage please handle this",
			},
		},
	}

	p := newEventsPoller(mc)
	events, minSkipped, err := p.discoverSlashCommands(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !minSkipped.IsZero() {
		t.Errorf("expected zero minSkippedAt for successful discovery, got %v", minSkipped)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.Type != "issue_note" {
		t.Errorf("Type = %q, want %q", got.Type, "issue_note")
	}
	if got.NoteID != 100 {
		t.Errorf("NoteID = %d, want 100", got.NoteID)
	}
	if got.NoteBody != "/fs-triage please handle this" {
		t.Errorf("NoteBody = %q, want %q", got.NoteBody, "/fs-triage please handle this")
	}
	if got.NoteAuthorID != 42 {
		t.Errorf("NoteAuthorID = %d, want 42", got.NoteAuthorID)
	}
}

func TestDiscoverSlashCommands_MRCommands(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.events = []ProjectEvent{
		{
			ID:        2,
			Author:    UserRef{ID: 55, Username: "bob"},
			CreatedAt: now,
			Note: EventNote{
				ID:           200,
				NoteableType: "MergeRequest",
				NoteableIID:  10,
				Body:         "/fs-review",
			},
		},
	}
	mc.mr[10] = &MergeRequest{
		IID:             10,
		SourceProjectID: 100,
		TargetProjectID: 100,
		SourceBranch:    "feature",
		TargetBranch:    "main",
		Author:          UserRef{ID: 55, Username: "bob"},
		Labels:          []string{"review"},
	}

	p := newEventsPoller(mc)
	events, minSkipped, err := p.discoverSlashCommands(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !minSkipped.IsZero() {
		t.Errorf("expected zero minSkippedAt for successful discovery, got %v", minSkipped)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.Type != "mr_note" {
		t.Errorf("Type = %q, want %q", got.Type, "mr_note")
	}
	if got.IID != 10 {
		t.Errorf("IID = %d, want 10", got.IID)
	}
	if got.MRSource != 100 || got.MRTarget != 100 {
		t.Errorf("MRSource=%d, MRTarget=%d, want 100, 100", got.MRSource, got.MRTarget)
	}
	if got.SourceBranch != "feature" {
		t.Errorf("SourceBranch = %q, want %q", got.SourceBranch, "feature")
	}
}

func TestDiscoverSlashCommands_MRFetchError(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.events = []ProjectEvent{
		{
			ID:        5,
			Author:    UserRef{ID: 55, Username: "bob"},
			CreatedAt: now,
			Note: EventNote{
				ID:           500,
				NoteableType: "MergeRequest",
				NoteableIID:  99,
				Body:         "/fs-code",
			},
		},
	}
	mc.mrErr[99] = fmt.Errorf("API error")

	p := newEventsPoller(mc)
	events, minSkipped, err := p.discoverSlashCommands(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events when MR fetch fails, got %d", len(events))
	}
	if minSkipped.IsZero() {
		t.Fatal("expected minSkippedAt to be set when MR fetch fails")
	}
	if !minSkipped.Equal(now) {
		t.Errorf("minSkippedAt = %v, want %v", minSkipped, now)
	}
}

func TestDiscoverSlashCommands_SkipsNonSlashCommands(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.events = []ProjectEvent{
		{
			ID:        3,
			Author:    UserRef{ID: 42, Username: "alice"},
			CreatedAt: now,
			Note: EventNote{
				ID:           300,
				NoteableType: "Issue",
				NoteableIID:  1,
				Body:         "just a regular comment",
			},
		},
		{
			ID:        4,
			Author:    UserRef{ID: 42, Username: "alice"},
			CreatedAt: now,
			Note: EventNote{
				ID:           301,
				NoteableType: "Issue",
				NoteableIID:  2,
				Body:         "/label ~bug",
			},
		},
	}

	p := newEventsPoller(mc)
	events, _, err := p.discoverSlashCommands(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for non-slash-command notes, got %d", len(events))
	}
}

func TestIsProjectAccessTokenBot(t *testing.T) {
	tests := []struct {
		username string
		want     bool
	}{
		{"project_123_bot_456", true},
		{"project_1_bot_2", true},
		{"project_abc_bot_xyz", true},
		{"alice", false},
		{"project_123", false},
		{"bot_user", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.username, func(t *testing.T) {
			got := isProjectAccessTokenBot(tt.username)
			if got != tt.want {
				t.Errorf("isProjectAccessTokenBot(%q) = %v, want %v", tt.username, got, tt.want)
			}
		})
	}
}

func TestFilterBotEvents_RemovesBotEvents(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc)

	events := []RoutableEvent{
		{Type: "issue_note", IID: 1, NoteBody: "bot comment", IsBot: true, NoteAuthorID: 999},
	}
	filtered := p.filterBotEvents(events)
	if len(filtered) != 0 {
		t.Errorf("expected bot event to be removed, got %d events", len(filtered))
	}
}

func TestFilterBotEvents_RetainsBotChangesRequested(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc) // botUserID = 100

	events := []RoutableEvent{
		{
			Type:         "mr_note",
			IID:          5,
			NoteBody:     "Changes needed <!-- fullsend:changes-requested --> here",
			IsBot:        true,
			NoteAuthorID: 100, // matches botUserID
		},
	}
	filtered := p.filterBotEvents(events)
	if len(filtered) != 1 {
		t.Fatalf("expected bot changes-requested marker to be retained, got %d events", len(filtered))
	}
}

func TestFilterBotEvents_RetainsNonBotEvents(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc)

	events := []RoutableEvent{
		{Type: "issue_note", IID: 1, NoteBody: "human comment", IsBot: false, NoteAuthorID: 42},
	}
	filtered := p.filterBotEvents(events)
	if len(filtered) != 1 {
		t.Errorf("expected non-bot event to be retained, got %d events", len(filtered))
	}
}

func TestDeduplicate(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc)

	events := []RoutableEvent{
		{Type: "issue_note", IID: 1, NoteID: 10, NoteBody: "hello"},
		{Type: "issue_note", IID: 1, NoteID: 10, NoteBody: "hello"}, // duplicate
		{Type: "issue_note", IID: 1, NoteID: 20, NoteBody: "world"}, // different note
		{Type: "issue_label", IID: 1, Labels: []string{"ready-to-code"}},
		{Type: "issue_label", IID: 1, Labels: []string{"ready-to-code"}}, // duplicate
	}
	unique := p.deduplicate(events)
	if len(unique) != 3 {
		t.Errorf("expected 3 unique events, got %d", len(unique))
		for _, e := range unique {
			t.Logf("  %s (key=%s)", e.Type, e.Key())
		}
	}
}

func TestFilterRoutableLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   []string
	}{
		{
			name:   "filters to known labels",
			labels: []string{"bug", "ready-to-code", "enhancement", "ready-for-review"},
			want:   []string{"ready-to-code", "ready-for-review"},
		},
		{
			name:   "no routable labels",
			labels: []string{"bug", "enhancement"},
			want:   nil,
		},
		{
			name:   "empty input",
			labels: nil,
			want:   nil,
		},
		{
			name:   "all routable",
			labels: []string{"ready-to-code", "ready-for-review"},
			want:   []string{"ready-to-code", "ready-for-review"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterRoutableLabels(tt.labels)
			if len(got) != len(tt.want) {
				t.Fatalf("filterRoutableLabels(%v) returned %v, want %v", tt.labels, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("filterRoutableLabels(%v)[%d] = %q, want %q", tt.labels, i, got[i], tt.want[i])
				}
			}
		})
	}
}
