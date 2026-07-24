package poll

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

func TestSplitOwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
	}{
		{"org/project", "org", "project"},
		{"org/sub/project", "org/sub", "project"},
		{"org/sub1/sub2/project", "org/sub1/sub2", "project"},
		{"project", "", "project"},
	}
	for _, tc := range tests {
		owner, repo := splitOwnerRepo(tc.input)
		if owner != tc.wantOwner || repo != tc.wantRepo {
			t.Errorf("splitOwnerRepo(%q) = (%q, %q), want (%q, %q)",
				tc.input, owner, repo, tc.wantOwner, tc.wantRepo)
		}
	}
}

func TestNew(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, "org/sub/project", Options{
		SlashCommandsOnly: true,
		BotUserID:         42,
		GitLabURL:         "https://gitlab.example.com",
	})
	if p.owner != "org/sub" {
		t.Errorf("owner = %q, want %q", p.owner, "org/sub")
	}
	if p.repo != "project" {
		t.Errorf("repo = %q, want %q", p.repo, "project")
	}
	if p.botUserID != 42 {
		t.Errorf("botUserID = %d, want 42", p.botUserID)
	}
	if p.gitlabURL != "https://gitlab.example.com" {
		t.Errorf("gitlabURL = %q, want %q", p.gitlabURL, "https://gitlab.example.com")
	}
}

func TestNewDefaultGitLabURL(t *testing.T) {
	p := New(newMockClient(), nil, "org/project", Options{})
	if p.gitlabURL != "https://gitlab.com" {
		t.Errorf("gitlabURL = %q, want default %q", p.gitlabURL, "https://gitlab.com")
	}
}

type stubRouter struct {
	stages []string
	err    error
}

func (r *stubRouter) Route(_ *dispatch.NormalizedEvent) ([]string, error) {
	return r.stages, r.err
}

func TestRunEmptyPoll(t *testing.T) {
	now := time.Now()
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = now.Add(-10 * time.Minute).Format(time.RFC3339)

	p := New(mc, nil, "org/project", Options{})

	err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// No events discovered, no pipelines should be created.
	if mc.pipelineCounter != 0 {
		t.Errorf("expected 0 pipelines, got %d", mc.pipelineCounter)
	}

	if _, ok := mc.updatedVars["FULLSEND_LAST_POLL_AT_FULL"]; !ok {
		t.Error("watermark not updated")
	}
}

func TestRunSlashCommandsOnlyMode(t *testing.T) {
	now := time.Now()
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FAST"] = now.Add(-5 * time.Minute).Format(time.RFC3339)

	p := New(mc, nil, "org/project", Options{
		SlashCommandsOnly: true,
	})

	err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if _, ok := mc.updatedVars["FULLSEND_LAST_POLL_AT_FAST"]; !ok {
		t.Error("fast watermark not updated")
	}
}

func TestTrackFailure(t *testing.T) {
	var min time.Time
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)

	trackFailure(&min, t1)
	if !min.Equal(t1) {
		t.Errorf("first track: got %v, want %v", min, t1)
	}

	trackFailure(&min, t2)
	if !min.Equal(t2) {
		t.Errorf("second track (earlier): got %v, want %v", min, t2)
	}

	t3 := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	trackFailure(&min, t3)
	if !min.Equal(t2) {
		t.Errorf("third track (later): got %v, want %v", min, t2)
	}
}

func TestTrackLabelFailure(t *testing.T) {
	failed := make(map[int]map[string]bool)

	trackLabelFailure(failed, RoutableEvent{Type: "issue_note", IID: 1})
	if len(failed) != 0 {
		t.Error("non-label event should not be tracked")
	}

	trackLabelFailure(failed, RoutableEvent{
		Type:         "issue_label",
		IID:          5,
		Labels:       []string{"ready-to-code"},
		ChangedLabel: "ready-to-code",
	})
	if !failed[5]["ready-to-code"] {
		t.Error("label failure not tracked")
	}
}

func TestRunFullPollWithRouterAndDispatch(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "/fs-triage handle this", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.memberLevel[42] = 30
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}

	router := &stubRouter{stages: []string{"triage"}}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify pipeline was created via API.
	if mc.pipelineCounter != 1 {
		t.Fatalf("expected 1 pipeline, got %d", mc.pipelineCounter)
	}

	// Verify dispatch was recorded.
	if len(p.dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(p.dispatches))
	}
	if p.dispatches[0].Stage != "triage" {
		t.Errorf("stage = %q, want %q", p.dispatches[0].Stage, "triage")
	}

	if len(mc.emojis) != 1 {
		t.Fatalf("expected 1 emoji reaction, got %d", len(mc.emojis))
	}
	if mc.emojis[0].Emoji != "eyes" {
		t.Errorf("emoji = %q, want %q", mc.emojis[0].Emoji, "eyes")
	}
}

func TestRunMultipleStages(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"ready-to-code"}, UpdatedAt: now, Author: UserRef{ID: 5}},
	}
	mc.notes[1] = []Note{}
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 5}}
	mc.labelEvents[1] = []ResourceLabelEvent{
		{
			ID:     100,
			Action: "add",
			Label: struct {
				Name string `json:"name"`
			}{Name: "ready-to-code"},
			User: UserRef{ID: 5, Username: "dev"},
		},
	}
	mc.memberLevel[5] = 30

	router := &stubRouter{stages: []string{"triage", "code"}}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify 2 pipelines created via API.
	if mc.pipelineCounter != 2 {
		t.Fatalf("expected 2 pipelines, got %d", mc.pipelineCounter)
	}

	if len(p.dispatches) != 2 {
		t.Fatalf("expected 2 dispatches, got %d", len(p.dispatches))
	}

	if _, ok := mc.updatedVars["FULLSEND_LABEL_STATE"]; !ok {
		t.Error("expected label state to be persisted")
	}
}

func TestRunNoMatchingStages(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "just a comment", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.memberLevel[42] = 30
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}

	router := &stubRouter{stages: nil}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// No matching stages → no pipelines created.
	if mc.pipelineCounter != 0 {
		t.Errorf("expected 0 pipelines, got %d", mc.pipelineCounter)
	}
}

func TestRunRouterError(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "note", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.memberLevel[42] = 30
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}

	router := &stubRouter{err: fmt.Errorf("routing failed")}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() should not return error on router failure, got: %v", err)
	}
}

func TestRunConversionErrorSkipsEvent(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "note", CreatedAt: now, Author: UserRef{ID: 0}},
	}

	router := &stubRouter{stages: []string{"triage"}}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Conversion error → no pipelines created.
	if mc.pipelineCounter != 0 {
		t.Errorf("expected 0 pipelines for unresolvable actor, got %d", mc.pipelineCounter)
	}
}

func TestRunAllEventsFailWatermarkNotAdvanced(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now},
		{IID: 2, Labels: []string{"bug"}, UpdatedAt: now},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "note", CreatedAt: now, Author: UserRef{ID: 0}},
	}
	mc.notes[2] = []Note{
		{ID: 11, Body: "note", CreatedAt: now, Author: UserRef{ID: 0}},
	}

	router := &stubRouter{stages: []string{"triage"}}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if _, ok := mc.updatedVars["FULLSEND_LAST_POLL_AT_FULL"]; ok {
		t.Error("watermark should not be advanced when all events fail")
	}
}

func TestRunNilRouter(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "just a comment", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.memberLevel[42] = 30
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}

	p := New(mc, nil, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestRunIdempotentSecondPoll(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "/fs-triage handle this", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.memberLevel[42] = 30
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}

	router := &stubRouter{stages: []string{"triage"}}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	if mc.pipelineCounter != 1 {
		t.Fatalf("first run: expected 1 pipeline, got %d", mc.pipelineCounter)
	}

	// Simulate persisted state being readable on next cycle.
	mc.variables["FULLSEND_DISPATCHED_KEYS_FULL"] = mc.updatedVars["FULLSEND_DISPATCHED_KEYS_FULL"]
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = mc.updatedVars["FULLSEND_LAST_POLL_AT_FULL"]

	p2 := New(mc, router, "group/project", Options{})
	if err := p2.Run(context.Background()); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	// Second run should not create new pipelines (idempotent).
	if mc.pipelineCounter != 1 {
		t.Errorf("second run: expected no new pipelines (total 1), got %d", mc.pipelineCounter)
	}
}

func TestRunLabelFailureRollback(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"ready-to-code"}, UpdatedAt: now, Author: UserRef{ID: 5}},
		{IID: 2, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{}
	mc.notes[2] = []Note{
		{ID: 20, Body: "hello", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.labelEvents[1] = []ResourceLabelEvent{
		{
			ID:     100,
			Action: "add",
			Label: struct {
				Name string `json:"name"`
			}{Name: "ready-to-code"},
			User: UserRef{ID: 0}, // zero ID -> conversion will fail
		},
	}
	mc.memberLevel[42] = 30
	mc.issue[2] = &Issue{IID: 2, Author: UserRef{ID: 42}}

	router := &stubRouter{stages: []string{"triage"}}
	p := New(mc, router, "group/project", Options{})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	persisted, ok := mc.updatedVars["FULLSEND_LABEL_STATE"]
	if !ok {
		t.Fatal("expected label state to be persisted")
	}
	var ls LabelState
	if err := json.Unmarshal([]byte(persisted), &ls); err != nil {
		t.Fatalf("unmarshal label state: %v", err)
	}
	if labels, ok := ls[1]; ok && len(labels) > 0 {
		for _, l := range labels {
			if l == "ready-to-code" {
				t.Error("expected ready-to-code to be rolled back from label state after dispatch failure")
			}
		}
	}
}
