package poll

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// --- dispatch method tests ---

func TestDispatch_CreatesAPIpipelineAndAppendsRecord(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	event := RoutableEvent{
		Type:      "issue_comment",
		IID:       42,
		UpdatedAt: ts,
		NoteBody:  "/fs-triage",
		NoteID:    100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the mock client was called.
	if mc.pipelineCounter != 1 {
		t.Fatalf("expected 1 pipeline created, got %d", mc.pipelineCounter)
	}

	// Verify the API call arguments.
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("expected 1 pipeline call, got %d", len(mc.pipelineCalls))
	}
	call := mc.pipelineCalls[0]
	if call.Owner != "owner" {
		t.Errorf("owner: got %q, want owner", call.Owner)
	}
	if call.Repo != "repo" {
		t.Errorf("repo: got %q, want repo", call.Repo)
	}
	vars := call.Variables
	if vars["STAGE"] != "triage" {
		t.Errorf("STAGE variable: got %q, want triage", vars["STAGE"])
	}
	if vars["EVENT_TYPE"] != "issue_comment" {
		t.Errorf("EVENT_TYPE variable: got %q, want issue_comment", vars["EVENT_TYPE"])
	}
	if vars["RESOURCE_KEY"] != "issue-42" {
		t.Errorf("RESOURCE_KEY variable: got %q, want issue-42", vars["RESOURCE_KEY"])
	}
	if vars["EVENT_PAYLOAD_B64"] == "" {
		t.Error("EVENT_PAYLOAD_B64 variable should be set")
	}
	if vars["IS_FORK"] != "false" {
		t.Errorf("IS_FORK variable: got %q, want false (issue event)", vars["IS_FORK"])
	}
	if _, ok := vars["MR_AUTHOR_ID"]; ok {
		t.Error("MR_AUTHOR_ID should not be set for issue events")
	}

	// Verify a dispatch record was appended.
	if len(p.dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(p.dispatches))
	}
	d := p.dispatches[0]
	if d.Stage != "triage" {
		t.Errorf("stage: got %q, want triage", d.Stage)
	}
	if d.EventType != "issue_comment" {
		t.Errorf("event_type: got %q, want issue_comment", d.EventType)
	}
	if d.ResourceKey != "issue-42" {
		t.Errorf("resource_key: got %q, want issue-42", d.ResourceKey)
	}
	if d.IID != 42 {
		t.Errorf("IID: got %d, want 42", d.IID)
	}

	// Verify the payload is valid base64-encoded JSON.
	decoded, err := base64.StdEncoding.DecodeString(d.EventPayloadB64)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["type"] != "issue_comment" {
		t.Errorf("payload type: got %v, want issue_comment", payload["type"])
	}
	if int(payload["iid"].(float64)) != 42 {
		t.Errorf("payload iid: got %v, want 42", payload["iid"])
	}
}

func TestDispatch_PropagatesMRAuthorAndFork(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:       "mr_event",
		IID:        10,
		UpdatedAt:  time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRAuthorID: 99,
		MRSource:   100,
		MRTarget:   200,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "review", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify variable contract for MR events.
	vars := mc.pipelineCalls[0].Variables
	if vars["MR_AUTHOR_ID"] != "99" {
		t.Errorf("MR_AUTHOR_ID variable: got %q, want 99", vars["MR_AUTHOR_ID"])
	}
	if vars["IS_FORK"] != "true" {
		t.Errorf("IS_FORK variable: got %q, want true (source != target)", vars["IS_FORK"])
	}
	if vars["STATUS_IID"] != "10" {
		t.Errorf("STATUS_IID variable: got %q, want 10", vars["STATUS_IID"])
	}

	d := p.dispatches[0]
	if d.MRAuthorID != 99 {
		t.Errorf("MRAuthorID: got %d, want 99", d.MRAuthorID)
	}
	if !d.IsFork {
		t.Error("IsFork: got false, want true (source != target)")
	}
}

func TestDispatch_ActorID_MREvent(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "mr_event",
		IID:          10,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRAuthorID:   42,
		NoteAuthorID: 55,
		MRSource:     100,
		MRTarget:     100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "review", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d := p.dispatches[0]
	if d.ActorID != 55 {
		t.Errorf("ActorID: got %d, want 55 (from NoteAuthorID, the merger)", d.ActorID)
	}
}

func TestDispatch_ActorID_IssueNoteEvent(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "issue_note",
		IID:          7,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     "/fs-triage",
		NoteID:       300,
		NoteAuthorID: 99,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d := p.dispatches[0]
	if d.ActorID != 99 {
		t.Errorf("ActorID: got %d, want 99 (from NoteAuthorID)", d.ActorID)
	}
	if d.MRAuthorID != 0 {
		t.Errorf("MRAuthorID: got %d, want 0 (issue events have no MR author)", d.MRAuthorID)
	}
}

func TestDispatch_ActorID_MRNoteUsesCommenter(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "mr_note",
		IID:          10,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRAuthorID:   42,
		NoteAuthorID: 99,
		NoteBody:     "/fs-triage",
		NoteID:       500,
		MRSource:     100,
		MRTarget:     100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d := p.dispatches[0]
	if d.ActorID != 99 {
		t.Errorf("ActorID: got %d, want 99 (NoteAuthorID, not MRAuthorID)", d.ActorID)
	}
	if d.MRAuthorID != 42 {
		t.Errorf("MRAuthorID: got %d, want 42 (preserved for backward compat)", d.MRAuthorID)
	}
}

func TestDispatch_ActorID_ZeroWhenNeitherSet(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:      "issue_label",
		IID:       5,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
	}

	err := p.dispatch(context.Background(), "owner", "repo", "code", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d := p.dispatches[0]
	if d.ActorID != 0 {
		t.Errorf("ActorID: got %d, want 0 (no actor available for label events)", d.ActorID)
	}
}

func TestDispatch_SameProjectIsNotFork(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:      "mr_event",
		IID:       10,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRSource:  100,
		MRTarget:  100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "review", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.dispatches[0].IsFork {
		t.Error("IsFork: got true, want false (same project)")
	}
}

func TestDispatch_PropagatesPollJobURL(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{PollJobURL: "https://gitlab.example.com/-/jobs/12345"})

	event := RoutableEvent{
		Type:      "issue_label",
		IID:       5,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := mc.pipelineCalls[0].Variables
	if vars["FULLSEND_POLL_JOB_URL"] != "https://gitlab.example.com/-/jobs/12345" {
		t.Errorf("FULLSEND_POLL_JOB_URL: got %q, want poll job URL", vars["FULLSEND_POLL_JOB_URL"])
	}
}

func TestDispatch_UsesPipelineRefOption(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{PipelineRef: "release/v2"})

	event := RoutableEvent{
		Type:      "issue_label",
		IID:       5,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mc.pipelineCalls[0].Ref != "release/v2" {
		t.Errorf("ref: got %q, want release/v2", mc.pipelineCalls[0].Ref)
	}
}

func TestDispatch_CreatePipelineErrorPropagates(t *testing.T) {
	mc := newMockClient()
	mc.pipelineErr = fmt.Errorf("API error: 403 forbidden")
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:      "issue_comment",
		IID:       42,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:  "/fs-triage",
		NoteID:    100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err == nil {
		t.Fatal("expected error from dispatch when CreatePipeline fails")
	}
	if len(p.dispatches) != 0 {
		t.Errorf("expected 0 dispatches on error, got %d", len(p.dispatches))
	}
}

func TestRunCreatePipelineFailureDoesNotAdvanceWatermark(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "/fs-triage handle this", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.memberLevel[42] = 30
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}

	router := &stubRouter{stages: []string{"triage"}}
	p := New(mc, router, "group/project", Options{PipelineRef: "main"})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if mc.pipelineCounter != 0 {
		t.Errorf("expected 0 pipelines created, got %d", mc.pipelineCounter)
	}

	if _, ok := mc.updatedVars["FULLSEND_LAST_POLL_AT_FULL"]; ok {
		t.Error("watermark should not be advanced when pipeline creation fails")
	}
}

func TestRunPartialDispatchFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = since.Format(time.RFC3339)
	// Fail after 1 successful CreatePipeline call.
	mc.pipelineErr = fmt.Errorf("API error: 500 internal server error")
	mc.pipelineErrAfter = 1
	mc.issues = []Issue{
		{IID: 1, Labels: []string{"bug"}, UpdatedAt: now.Add(-2 * time.Minute), Author: UserRef{ID: 42}},
		{IID: 2, Labels: []string{"bug"}, UpdatedAt: now, Author: UserRef{ID: 42}},
	}
	mc.notes[1] = []Note{
		{ID: 10, Body: "/fs-triage first", CreatedAt: now.Add(-2 * time.Minute), Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.notes[2] = []Note{
		{ID: 20, Body: "/fs-triage second", CreatedAt: now, Author: UserRef{ID: 42, Username: "alice"}},
	}
	mc.memberLevel[42] = 30
	mc.issue[1] = &Issue{IID: 1, Author: UserRef{ID: 42}}
	mc.issue[2] = &Issue{IID: 2, Author: UserRef{ID: 42}}

	router := &stubRouter{stages: []string{"triage"}}
	p := New(mc, router, "group/project", Options{PipelineRef: "main"})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// First event should have succeeded, second should have failed.
	if mc.pipelineCounter != 1 {
		t.Errorf("expected 1 successful pipeline, got %d", mc.pipelineCounter)
	}
	// Dispatch record only for the successful event.
	if len(p.dispatches) != 1 {
		t.Errorf("expected 1 dispatch record, got %d", len(p.dispatches))
	}
}

// --- buildEventPayload tests ---

func TestBuildEventPayload_IncludesAllFields(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	event := RoutableEvent{
		Type:         "issue_comment",
		IID:          7,
		UpdatedAt:    ts,
		NoteBody:     "/fs-triage",
		NoteID:       200,
		NoteAuthorID: 55,
		Labels:       []string{"ready-to-code"},
		MRSource:     100,
		MRTarget:     200,
	}

	data, err := buildEventPayload(event)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	checks := map[string]interface{}{
		"type":                 "issue_comment",
		"iid":                  float64(7),
		"note_body":            "/fs-triage",
		"note_id":              float64(200),
		"note_author_id":       float64(55),
		"mr_source_project_id": float64(100),
		"mr_target_project_id": float64(200),
	}
	for key, want := range checks {
		got, ok := m[key]
		if !ok {
			t.Errorf("missing key %q", key)
			continue
		}
		if got != want {
			t.Errorf("%s: got %v, want %v", key, got, want)
		}
	}

	// Check updated_at.
	if m["updated_at"] != ts.Format(time.RFC3339) {
		t.Errorf("updated_at: got %v, want %v", m["updated_at"], ts.Format(time.RFC3339))
	}

	// Check labels array.
	labels, ok := m["labels"].([]interface{})
	if !ok {
		t.Fatal("labels should be an array")
	}
	if len(labels) != 1 || labels[0] != "ready-to-code" {
		t.Errorf("labels: got %v, want [ready-to-code]", labels)
	}
}

func TestBuildEventPayload_OmitsZeroOptionalFields(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	event := RoutableEvent{
		Type:      "issue_label",
		IID:       10,
		UpdatedAt: ts,
		// All optional fields are zero values.
	}

	data, err := buildEventPayload(event)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Must have required fields.
	for _, required := range []string{"type", "iid", "updated_at"} {
		if _, ok := m[required]; !ok {
			t.Errorf("missing required key %q", required)
		}
	}

	// Must NOT have optional fields when zero.
	optionals := []string{"note_body", "note_id", "note_author_id", "labels", "mr_source_project_id", "mr_target_project_id"}
	for _, key := range optionals {
		if _, ok := m[key]; ok {
			t.Errorf("expected optional key %q to be omitted, but it was present", key)
		}
	}
}

func TestDispatch_UnknownProjectIDsAreForkFailClosed(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:      "mr_event",
		IID:       10,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		// MRSource and MRTarget both zero (unknown)
	}

	err := p.dispatch(context.Background(), "owner", "repo", "review", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !p.dispatches[0].IsFork {
		t.Error("IsFork: got false, want true (unknown project IDs should fail-closed)")
	}
}

func TestDispatch_IssueEventIsNotFork(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:      "issue_label",
		IID:       42,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
	}

	err := p.dispatch(context.Background(), "owner", "repo", "code", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.dispatches[0].IsFork {
		t.Error("IsFork: got true, want false (issue events have no fork context)")
	}
}

func TestDispatch_MREventOneZeroProjectIDIsFork(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:      "mr_event",
		IID:       10,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRSource:  100,
		MRTarget:  0,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "review", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !p.dispatches[0].IsFork {
		t.Error("IsFork: got false, want true (one zero project ID should fail-closed)")
	}
}

func TestResourceKey_EntityBased(t *testing.T) {
	tests := []struct {
		event RoutableEvent
		want  string
	}{
		{RoutableEvent{Type: "issue_label", IID: 42}, "issue-42"},
		{RoutableEvent{Type: "issue_note", IID: 7}, "issue-7"},
		{RoutableEvent{Type: "mr_event", IID: 10}, "mr-10"},
		{RoutableEvent{Type: "mr_note", IID: 3}, "mr-3"},
	}
	for _, tt := range tests {
		got := resourceKey(tt.event)
		if got != tt.want {
			t.Errorf("resourceKey(%s, IID=%d) = %q, want %q", tt.event.Type, tt.event.IID, got, tt.want)
		}
	}
}
