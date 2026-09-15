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

func TestDiscoverAllEvents_MROpenedEvents(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             8,
			CreatedAt:       now,
			UpdatedAt:       now,
			Author:          UserRef{ID: 42, Username: "alice", Bot: false},
			SourceProjectID: 1,
			TargetProjectID: 1,
			SourceBranch:    "feature",
			TargetBranch:    "main",
			Labels:          []string{"enhancement"},
		},
	}
	mc.mrNotes[8] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var opened []RoutableEvent
	for _, e := range events {
		if e.Type == "mr_event" && e.Action == "opened" {
			opened = append(opened, e)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("expected 1 opened mr_event, got %d", len(opened))
	}
	got := opened[0]
	if got.IID != 8 {
		t.Errorf("IID = %d, want 8", got.IID)
	}
	if got.NoteAuthorID != 42 {
		t.Errorf("NoteAuthorID = %d, want 42", got.NoteAuthorID)
	}
	if got.NoteAuthorLogin != "alice" {
		t.Errorf("NoteAuthorLogin = %q, want alice", got.NoteAuthorLogin)
	}
	if got.MRAuthorID != 42 {
		t.Errorf("MRAuthorID = %d, want 42", got.MRAuthorID)
	}
	if got.UpdatedAt != now {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
}

func TestDiscoverAllEvents_MROpenedIgnoresOldCreatedAt(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             9,
			CreatedAt:       since.Add(-time.Hour),
			UpdatedAt:       now, // listed because of a later update (e.g. a comment)
			Author:          UserRef{ID: 42, Username: "alice"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[9] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range events {
		if e.Type == "mr_event" {
			t.Fatalf("unexpected mr_event for old CreatedAt: %+v", e)
		}
	}
}

func TestDiscoverAllEvents_MROpenedAndMergedSameWindow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             11,
			CreatedAt:       now.Add(-30 * time.Second),
			MergedAt:        now,
			UpdatedAt:       now,
			Author:          UserRef{ID: 42, Username: "alice"},
			MergedBy:        UserRef{ID: 10, Username: "bob"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[11] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var opened, merged int
	for _, e := range events {
		if e.Type != "mr_event" {
			continue
		}
		switch e.Action {
		case "opened":
			opened++
		case "":
			merged++
		default:
			t.Errorf("unexpected Action %q", e.Action)
		}
	}
	if opened != 1 {
		t.Errorf("opened events = %d, want 1", opened)
	}
	if merged != 1 {
		t.Errorf("merged events = %d, want 1", merged)
	}
}

func TestDiscoverAllEvents_MRClosedEvents(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             12,
			State:           "closed",
			ClosedAt:        now,
			UpdatedAt:       now,
			Author:          UserRef{ID: 42, Username: "alice"},
			ClosedBy:        UserRef{ID: 10, Username: "bob", Bot: false},
			SourceProjectID: 1,
			TargetProjectID: 1,
			SourceBranch:    "feature",
			TargetBranch:    "main",
		},
	}
	mc.mrNotes[12] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var closed []RoutableEvent
	for _, e := range events {
		if e.Type == "mr_event" && e.Action == "closed" {
			closed = append(closed, e)
		}
	}
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed mr_event, got %d", len(closed))
	}
	got := closed[0]
	if got.IID != 12 {
		t.Errorf("IID = %d, want 12", got.IID)
	}
	if got.NoteAuthorID != 10 {
		t.Errorf("NoteAuthorID (closer) = %d, want 10", got.NoteAuthorID)
	}
	if got.NoteAuthorLogin != "bob" {
		t.Errorf("NoteAuthorLogin = %q, want bob", got.NoteAuthorLogin)
	}
	if got.UpdatedAt != now {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
}

func TestDiscoverAllEvents_MRClosedIgnoresMerged(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             13,
			State:           "merged",
			MergedAt:        now,
			ClosedAt:        now, // GitLab may also set closed_at on merge
			UpdatedAt:       now,
			Author:          UserRef{ID: 42, Username: "alice"},
			MergedBy:        UserRef{ID: 10, Username: "bob"},
			ClosedBy:        UserRef{ID: 10, Username: "bob"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[13] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var closed, merged int
	for _, e := range events {
		if e.Type != "mr_event" {
			continue
		}
		switch e.Action {
		case "closed":
			closed++
		case "":
			merged++
		}
	}
	if closed != 0 {
		t.Errorf("closed events = %d, want 0 (merged MRs must not emit closed)", closed)
	}
	if merged != 1 {
		t.Errorf("merged events = %d, want 1", merged)
	}
}

func TestDiscoverAllEvents_MRClosedIgnoresOldClosedAt(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             14,
			State:           "closed",
			ClosedAt:        since.Add(-time.Hour),
			UpdatedAt:       now, // listed because of a later comment
			Author:          UserRef{ID: 42, Username: "alice"},
			ClosedBy:        UserRef{ID: 10, Username: "bob"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[14] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range events {
		if e.Type == "mr_event" {
			t.Fatalf("unexpected mr_event for old ClosedAt: %+v", e)
		}
	}
}

func TestDiscoverAllEvents_CommentOnOldClosedMRDoesNotReemitClosed(t *testing.T) {
	// An MR closed before the watermark that receives a new comment after
	// it: the comment bumps updated_at (so the MR is listed) but not
	// closed_at, so the watermark comparison must emit only the comment
	// event, never re-dispatch the already-processed closed event.
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             16,
			State:           "closed",
			ClosedAt:        since.Add(-time.Hour),
			UpdatedAt:       now, // listed because of the later comment
			Author:          UserRef{ID: 42, Username: "alice"},
			ClosedBy:        UserRef{ID: 10, Username: "bob"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[16] = []Note{
		{ID: 30, Body: "a late comment", Author: UserRef{ID: 42, Username: "alice"}, CreatedAt: now},
	}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var noteEvents, closedEvents int
	for _, e := range events {
		if e.Type == "mr_event" && e.Action == "closed" {
			closedEvents++
		}
		if e.Type == "mr_note" {
			noteEvents++
		}
	}
	if closedEvents != 0 {
		t.Errorf("closed mr_event re-emitted for comment on old closed MR: got %d, want 0", closedEvents)
	}
	if noteEvents != 1 {
		t.Errorf("mr_note events = %d, want 1", noteEvents)
	}
}

func TestDiscoverAllEvents_MRReopenedStaleClosedAtIgnored(t *testing.T) {
	// A reopened MR is back in the "opened" state but may still carry a
	// closed_at within the watermark window on some GitLab versions.
	// The state guard must prevent it from re-dispatching retro.
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             15,
			State:           "opened",
			ClosedAt:        now, // stale close timestamp, inside window
			UpdatedAt:       now,
			Author:          UserRef{ID: 42, Username: "alice"},
			ClosedBy:        UserRef{ID: 10, Username: "bob"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[15] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range events {
		if e.Type == "mr_event" && e.Action == "closed" {
			t.Fatalf("unexpected closed mr_event for reopened MR: %+v", e)
		}
	}
}

func TestDiscoverAllEvents_MRClosedAbsentClosedBy(t *testing.T) {
	// When GitLab omits closed_by, the close is attributed to no actor
	// (empty NoteAuthor*, IsBot false) rather than falling back to the MR
	// author. This keeps a human's close of a bot-authored MR from being
	// misread as a bot event downstream. The MR author is still carried in
	// MRAuthor* so toNormalizedEvent can resolve the actor.
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             15,
			State:           "closed",
			ClosedAt:        now,
			UpdatedAt:       now,
			Author:          UserRef{ID: 42, Username: "alice"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[15] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var closed []RoutableEvent
	for _, e := range events {
		if e.Type == "mr_event" && e.Action == "closed" {
			closed = append(closed, e)
		}
	}
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed mr_event, got %d", len(closed))
	}
	if closed[0].NoteAuthorID != 0 {
		t.Errorf("NoteAuthorID = %d, want 0 (no closed_by, no author fallback)", closed[0].NoteAuthorID)
	}
	if closed[0].NoteAuthorLogin != "" {
		t.Errorf("NoteAuthorLogin = %q, want empty (no closed_by, no author fallback)", closed[0].NoteAuthorLogin)
	}
	if closed[0].IsBot {
		t.Error("IsBot = true, want false when closed_by is absent")
	}
	if closed[0].MRAuthorID != 42 || closed[0].MRAuthorLogin != "alice" {
		t.Errorf("MRAuthor = (%d, %q), want (42, alice)", closed[0].MRAuthorID, closed[0].MRAuthorLogin)
	}
}

func TestDiscoverAllEvents_MROpenedAndClosedSameWindow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.mrs = []MergeRequest{
		{
			IID:             16,
			State:           "closed",
			CreatedAt:       now.Add(-30 * time.Second),
			ClosedAt:        now,
			UpdatedAt:       now,
			Author:          UserRef{ID: 42, Username: "alice"},
			ClosedBy:        UserRef{ID: 10, Username: "bob"},
			SourceProjectID: 1,
			TargetProjectID: 1,
		},
	}
	mc.mrNotes[16] = []Note{}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var opened, closed int
	for _, e := range events {
		if e.Type != "mr_event" {
			continue
		}
		switch e.Action {
		case "opened":
			opened++
		case "closed":
			closed++
		default:
			t.Errorf("unexpected Action %q", e.Action)
		}
	}
	if opened != 1 {
		t.Errorf("opened events = %d, want 1", opened)
	}
	if closed != 1 {
		t.Errorf("closed events = %d, want 1", closed)
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

func TestFilterBotEvents_RetainsBotOpenedMR(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc) // botUserID = 100

	events := []RoutableEvent{
		{
			Type:         "mr_event",
			Action:       "opened",
			IID:          8,
			IsBot:        true,
			NoteAuthorID: 100,
			MRAuthorID:   100,
		},
	}
	filtered := p.filterBotEvents(events)
	if len(filtered) != 1 {
		t.Fatalf("expected bot-authored MR open to be retained, got %d events", len(filtered))
	}
}

func TestFilterBotEvents_RemovesBotClosedMR(t *testing.T) {
	// A close performed by the enrolled bot (e.g. closeStaleScaffoldPRs
	// cleaning up the bot's own stale scaffold MRs) must not dispatch
	// retro — mirroring bot-merged filtering. Human-performed closes are
	// not bot events, so genuine closed-unmerged MRs still reach retro.
	mc := newMockClient()
	p := newEventsPoller(mc) // botUserID = 100

	events := []RoutableEvent{
		{
			Type:         "mr_event",
			Action:       "closed",
			IID:          8,
			IsBot:        true,
			NoteAuthorID: 100,
			MRAuthorID:   42,
		},
	}
	filtered := p.filterBotEvents(events)
	if len(filtered) != 0 {
		t.Errorf("expected bot-closed MR to be removed, got %d events", len(filtered))
	}
}

func TestFilterBotEvents_RetainsClosedMRWithAbsentCloser(t *testing.T) {
	// A human closing a bot-authored MR when GitLab omits closed_by: the
	// closed event carries no actor identity (NoteAuthor* empty), so it must
	// not be treated as a bot event even though the bot authored the MR.
	// Dropping it here would skip retro on a human's close of the agent's
	// own work — the case #7322 makes reliable. MRAuthorID is the bot but
	// must not drive bot filtering.
	mc := newMockClient()
	p := newEventsPoller(mc) // botUserID = 100

	events := []RoutableEvent{
		{
			Type:         "mr_event",
			Action:       "closed",
			IID:          8,
			IsBot:        false,
			NoteAuthorID: 0,
			MRAuthorID:   100, // bot authored the MR
		},
	}
	filtered := p.filterBotEvents(events)
	if len(filtered) != 1 {
		t.Errorf("expected human-closed bot-authored MR to be retained, got %d events", len(filtered))
	}
}

func TestFilterBotEvents_RemovesBotMergedMR(t *testing.T) {
	mc := newMockClient()
	p := newEventsPoller(mc)

	events := []RoutableEvent{
		{
			Type:         "mr_event",
			IID:          8,
			IsBot:        true,
			NoteAuthorID: 100,
		},
	}
	filtered := p.filterBotEvents(events)
	if len(filtered) != 0 {
		t.Errorf("expected bot-authored MR merge to be removed, got %d events", len(filtered))
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

func TestDiscoverAllEvents_EventsModeSkipsSlashCommands(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"bug"}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "/fs-triage handle this", Author: UserRef{ID: 42, Username: "alice"}, CreatedAt: now},
		{ID: 11, Body: "regular comment", Author: UserRef{ID: 43, Username: "bob"}, CreatedAt: now},
	}
	mc.mrs = []MergeRequest{
		{IID: 5, SourceProjectID: 1, TargetProjectID: 1, UpdatedAt: now},
	}
	mc.mrNotes[5] = []Note{
		{ID: 20, Body: "/fs-review", Author: UserRef{ID: 44, Username: "charlie"}, CreatedAt: now},
		{ID: 21, Body: "MR feedback", Author: UserRef{ID: 45, Username: "dave"}, CreatedAt: now},
	}

	// Use events mode — slash commands should be skipped.
	p := New(mc, nil, "group/project", Options{
		BotUserID: 100,
		GitLabURL: "https://gitlab.com",
		Mode:      "events",
	})
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only non-slash notes should be discovered.
	var issueNotes, mrNotes []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_note" {
			issueNotes = append(issueNotes, e)
		}
		if e.Type == "mr_note" {
			mrNotes = append(mrNotes, e)
		}
	}
	if len(issueNotes) != 1 {
		t.Fatalf("expected 1 issue_note (non-slash only), got %d", len(issueNotes))
	}
	if issueNotes[0].NoteBody != "regular comment" {
		t.Errorf("expected non-slash note, got %q", issueNotes[0].NoteBody)
	}
	if len(mrNotes) != 1 {
		t.Fatalf("expected 1 mr_note (non-slash only), got %d", len(mrNotes))
	}
	if mrNotes[0].NoteBody != "MR feedback" {
		t.Errorf("expected non-slash MR note, got %q", mrNotes[0].NoteBody)
	}
}

func TestDiscoverAllEvents_EventsModeSkipsWhitespacePrefixedSlash(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"bug"}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "  /fs-triage handle this", Author: UserRef{ID: 42, Username: "alice"}, CreatedAt: now},
		{ID: 11, Body: "\n/fs-code", Author: UserRef{ID: 43, Username: "bob"}, CreatedAt: now},
		{ID: 12, Body: "regular comment", Author: UserRef{ID: 44, Username: "carol"}, CreatedAt: now},
	}

	p := New(mc, nil, "group/project", Options{
		BotUserID: 100,
		GitLabURL: "https://gitlab.com",
		Mode:      "events",
	})
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var issueNotes []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_note" {
			issueNotes = append(issueNotes, e)
		}
	}
	if len(issueNotes) != 1 {
		t.Fatalf("expected 1 issue_note (whitespace-prefixed /fs- filtered), got %d", len(issueNotes))
	}
	if issueNotes[0].NoteBody != "regular comment" {
		t.Errorf("expected non-slash note, got %q", issueNotes[0].NoteBody)
	}
}

func TestDiscoverAllEvents_DefaultModeKeepsSlashCommands(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.issues = []Issue{
		{IID: 1, UpdatedAt: now, Labels: []string{"bug"}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "/fs-triage handle this", Author: UserRef{ID: 42, Username: "alice"}, CreatedAt: now},
		{ID: 11, Body: "regular comment", Author: UserRef{ID: 43, Username: "bob"}, CreatedAt: now},
	}

	// Default mode (empty) — slash commands should NOT be filtered.
	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var noteEvents []RoutableEvent
	for _, e := range events {
		if e.Type == "issue_note" {
			noteEvents = append(noteEvents, e)
		}
	}
	if len(noteEvents) != 2 {
		t.Fatalf("expected 2 issue_note events (no filtering in default mode), got %d", len(noteEvents))
	}
}

func TestRoutableEventKey_OpenedDistinctFromMerged(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0)
	opened := RoutableEvent{Type: "mr_event", Action: "opened", IID: 5, UpdatedAt: ts}
	closed := RoutableEvent{Type: "mr_event", Action: "closed", IID: 5, UpdatedAt: ts}
	merged := RoutableEvent{Type: "mr_event", IID: 5, UpdatedAt: ts}
	if opened.Key() == merged.Key() {
		t.Fatalf("opened and merged keys collided: %s", opened.Key())
	}
	if opened.Key() == closed.Key() {
		t.Fatalf("opened and closed keys collided: %s", opened.Key())
	}
	if closed.Key() == merged.Key() {
		t.Fatalf("closed and merged keys collided: %s", closed.Key())
	}
	if want := "mr_event-5-opened-1700000000"; opened.Key() != want {
		t.Errorf("opened key = %q, want %q", opened.Key(), want)
	}
	if want := "mr_event-5-closed-1700000000"; closed.Key() != want {
		t.Errorf("closed key = %q, want %q", closed.Key(), want)
	}
	if want := "mr_event-5-1700000000"; merged.Key() != want {
		t.Errorf("merged key = %q, want %q", merged.Key(), want)
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
