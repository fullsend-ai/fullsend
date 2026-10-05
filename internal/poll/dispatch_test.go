package poll

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// --- dispatch method tests ---

func TestDispatch_CreatesAPIpipelineAndAppendsRecord(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	event := RoutableEvent{
		Type:         "issue_note",
		IID:          42,
		UpdatedAt:    ts,
		NoteBody:     "/fs-triage",
		NoteID:       100,
		NoteAuthorID: 88,
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
	if vars["EVENT_TYPE"] != "issue_note" {
		t.Errorf("EVENT_TYPE variable: got %q, want issue_note", vars["EVENT_TYPE"])
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
	if vars["ACTOR_ID"] != "88" {
		t.Errorf("ACTOR_ID variable: got %q, want 88", vars["ACTOR_ID"])
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
	if d.EventType != "issue_note" {
		t.Errorf("event_type: got %q, want issue_note", d.EventType)
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
	if payload["type"] != "issue_note" {
		t.Errorf("payload type: got %v, want issue_note", payload["type"])
	}
	if int(payload["iid"].(float64)) != 42 {
		t.Errorf("payload iid: got %v, want 42", payload["iid"])
	}
}

// TestDispatch_SelectsTransportFromTargetRepositoryContract guards the
// breaking-config bug where dispatch() called CreatePipelineWithInputs
// unconditionally regardless of the target repository's own installed
// contract. A poller binary is not upgraded in lockstep with every
// repository's scaffold migration (the CLI installer this job's wrapper
// script builds fetches and rebuilds a non-pinned ref on every poll run),
// so dispatch() must read the committed wrapper and select the transport
// that repository actually declares.
func TestDispatch_SelectsTransportFromTargetRepositoryContract(t *testing.T) {
	event := RoutableEvent{
		Type:      "issue_note",
		IID:       42,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:  "/fs-triage",
	}

	t.Run("typed wrapper uses pipeline inputs", func(t *testing.T) {
		mc := newMockClient() // default wrapper fixture declares the full typed contract
		p := newTestPoller(mc, Options{})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.pipelineCalls) != 1 || !mc.pipelineCalls[0].ViaInputs {
			t.Fatalf("expected one CreatePipelineWithInputs call, got %+v", mc.pipelineCalls)
		}
	})

	t.Run("missing wrapper falls back to variables-based pipeline", func(t *testing.T) {
		mc := newMockClient()
		mc.wrapperNotFound = true // simulates a legacy, not-yet-migrated installation
		p := newTestPoller(mc, Options{})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].ViaInputs {
			t.Fatalf("expected one CreatePipeline (variables) call, got %+v", mc.pipelineCalls)
		}
		if mc.pipelineCalls[0].Variables["STAGE"] != "triage" {
			t.Errorf("STAGE variable: got %q, want triage", mc.pipelineCalls[0].Variables["STAGE"])
		}
	})

	t.Run("incomplete wrapper falls back to variables-based pipeline", func(t *testing.T) {
		mc := newMockClient()
		mc.wrapperContent = []byte("spec:\n  inputs:\n    stage: {}\n---\n{}\n") // declares some, not all, inputs
		p := newTestPoller(mc, Options{})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].ViaInputs {
			t.Fatalf("expected one CreatePipeline (variables) call, got %+v", mc.pipelineCalls)
		}
	})

	t.Run("wrapper read error propagates without dispatching", func(t *testing.T) {
		mc := newMockClient()
		mc.wrapperErr = fmt.Errorf("permission denied")
		p := newTestPoller(mc, Options{})
		err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("expected a wrapped permission-denied error, got %v", err)
		}
		if len(mc.pipelineCalls) != 0 {
			t.Fatalf("expected no pipeline call when the transport check fails, got %+v", mc.pipelineCalls)
		}
	})

	// TestDispatch_SelectsTransportFromTargetRepositoryContract guards a
	// second bug: usesTypedDispatch previously read the wrapper from the
	// default branch (GetFileContent) while CreatePipeline/
	// CreatePipelineWithInputs targeted p.opts.PipelineRef. When the
	// dispatch ref's wrapper contract differs from the default branch's
	// (e.g. a legacy release branch still pinned to variable-based
	// dispatch while main has migrated to typed inputs, or vice versa),
	// the poller must select the transport the *dispatch ref* actually
	// declares, not the default branch's.
	t.Run("reads the wrapper at the dispatch ref, not the default branch", func(t *testing.T) {
		mc := newMockClient()
		p := newTestPoller(mc, Options{PipelineRef: "legacy-release"})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.wrapperContentRefs) != 1 || mc.wrapperContentRefs[0] != "legacy-release" {
			t.Fatalf("expected wrapper lookup at ref %q, got %+v", "legacy-release", mc.wrapperContentRefs)
		}
	})

	t.Run("legacy wrapper at the dispatch ref falls back to variables even when default branch is typed", func(t *testing.T) {
		mc := newMockClient() // default-branch wrapper fixture declares the full typed contract
		mc.wrapperNotFound = true
		p := newTestPoller(mc, Options{PipelineRef: "legacy-release"})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].ViaInputs {
			t.Fatalf("expected one CreatePipeline (variables) call reflecting the dispatch ref's legacy contract, got %+v", mc.pipelineCalls)
		}
		if mc.pipelineCalls[0].Ref != "legacy-release" {
			t.Errorf("pipeline ref: got %q, want legacy-release", mc.pipelineCalls[0].Ref)
		}
	})
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

func TestDispatch_OpenedMRIncludesActionInPayload(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "mr_event",
		Action:       "opened",
		IID:          8,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRAuthorID:   42,
		NoteAuthorID: 42,
		MRSource:     100,
		MRTarget:     100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "review", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(p.dispatches[0].EventPayloadB64)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["action"] != "opened" {
		t.Errorf("payload action: got %v, want opened", payload["action"])
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
		t.Errorf("ActorID: got %d, want 0 (NoteAuthorID not threaded)", d.ActorID)
	}
}

func TestDispatch_ActorID_IssueLabelEvent(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "issue_label",
		IID:          5,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteAuthorID: 77,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "code", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d := p.dispatches[0]
	if d.ActorID != 77 {
		t.Errorf("ActorID: got %d, want 77 (from NoteAuthorID, threaded by poll loop)", d.ActorID)
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
		Type:      "issue_note",
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
	since := now.Add(-20 * time.Minute)
	mc := newMockClient()
	mc.setPollState(persistedPollState{LastPollAtFull: since.Format(time.RFC3339)})
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
	p := New(mc, router, "group/project", withTestSecret(Options{PipelineRef: "main"}))

	err := p.Run(context.Background())
	if err == nil {
		t.Fatal("Run() should return error when pipeline creation fails")
	}
	if !strings.Contains(err.Error(), "dispatch") {
		t.Errorf("error = %q, want dispatch failure", err)
	}

	if mc.pipelineCounter != 0 {
		t.Errorf("expected 0 pipelines created, got %d", mc.pipelineCounter)
	}

	got, ok := mc.getPollState()
	if ok && got.LastPollAtFull != "" && got.LastPollAtFull != since.Format(time.RFC3339) {
		t.Error("watermark should not be advanced when pipeline creation fails")
	}
	if !ok || got.FailedKeysFull["note-10"] != 1 {
		t.Errorf("failed keys = %v, want note-10:1", got.FailedKeysFull)
	}
}

func TestRunPartialDispatchFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-20 * time.Minute)
	mc := newMockClient()
	mc.setPollState(persistedPollState{LastPollAtFull: since.Format(time.RFC3339)})
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
	p := New(mc, router, "group/project", withTestSecret(Options{PipelineRef: "main"}))

	err := p.Run(context.Background())
	if err == nil {
		t.Fatal("Run() should return error when one dispatch fails")
	}

	// First event should have succeeded, second should have failed.
	if mc.pipelineCounter != 1 {
		t.Errorf("expected 1 successful pipeline, got %d", mc.pipelineCounter)
	}
	// Dispatch record only for the successful event.
	if len(p.dispatches) != 1 {
		t.Errorf("expected 1 dispatch record, got %d", len(p.dispatches))
	}
	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state to be persisted after partial failure")
	}
	if got.FailedKeysFull["note-20"] != 1 {
		t.Errorf("failed keys = %v, want note-20:1", got.FailedKeysFull)
	}
}

// --- buildEventPayload tests ---

func TestBuildEventPayload_IncludesAllFields(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	event := RoutableEvent{
		Type:         "issue_note",
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
		"type":                 "issue_note",
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

func TestBuildEventPayload_NoteBodyUnderLimitUntouched(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	body := "/fs-fix make it faster"
	event := RoutableEvent{Type: "issue_note", IID: 1, UpdatedAt: ts, NoteBody: body}

	data, err := buildEventPayload(event)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m["note_body"]; got != body {
		t.Errorf("note_body = %q, want unchanged %q", got, body)
	}
}

func TestBuildEventPayload_NoteBodyOverLimitTruncatedWithNotice(t *testing.T) {
	// Consumer-contract test: run-agent-job.sh derives /fs-fix instructions
	// and retro comments straight from this dispatched note_body. A note
	// over the 800-character dispatch limit must still carry an in-band
	// signal that it was cut, not just silently end mid-sentence.
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	body := "/fs-fix " + strings.Repeat("x", 900)
	event := RoutableEvent{Type: "issue_note", IID: 1, UpdatedAt: ts, NoteBody: body}

	data, err := buildEventPayload(event)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := m["note_body"].(string)
	if !ok {
		t.Fatalf("note_body missing or not a string: %v", m["note_body"])
	}
	if len([]rune(got)) > 800 {
		t.Errorf("note_body has %d runes, want at most 800", len([]rune(got)))
	}
	if !strings.HasSuffix(got, noteBodyTruncationNotice) {
		t.Errorf("note_body = %q, want it to end with the truncation notice %q", got, noteBodyTruncationNotice)
	}
	if strings.HasPrefix(got, "/fs-fix") {
		// Sanity: the instruction prefix a human typed must survive
		// truncation (truncation cuts from the end), so the shell's
		// `/fs-fix*` match in run-agent-job.sh still fires.
	} else {
		t.Errorf("note_body = %q, want it to still start with the original /fs-fix prefix", got)
	}
}

func TestTruncateNoteBody_ExactLimitUntouched(t *testing.T) {
	s := strings.Repeat("a", 800)
	if got := truncateNoteBody(s, 800); got != s {
		t.Errorf("truncateNoteBody at exact limit changed the string: got %d runes, want 800 unchanged", len([]rune(got)))
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

// --- HMAC signing tests ---

func TestComputeDispatchHMAC_Deterministic(t *testing.T) {
	vars := map[string]string{
		"STAGE":             "triage",
		"EVENT_TYPE":        "issue_note",
		"EVENT_PAYLOAD_B64": "eyJ0eXBlIjoiaXNzdWVfbm90ZSJ9",
		"RESOURCE_KEY":      "issue-42",
		"IS_FORK":           "false",
		"ACTOR_ID":          "88",
	}

	h1 := computeDispatchHMAC("test-secret", vars)
	h2 := computeDispatchHMAC("test-secret", vars)
	if h1 != h2 {
		t.Errorf("HMAC not deterministic: %q != %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("HMAC hex length: got %d, want 64", len(h1))
	}
}

func TestComputeDispatchHMAC_DifferentSecretProducesDifferentMAC(t *testing.T) {
	vars := map[string]string{
		"STAGE":      "triage",
		"EVENT_TYPE": "issue_note",
	}

	h1 := computeDispatchHMAC("secret-a", vars)
	h2 := computeDispatchHMAC("secret-b", vars)
	if h1 == h2 {
		t.Error("different secrets should produce different HMACs")
	}
}

func TestComputeDispatchHMAC_MissingKeysUseEmptyValue(t *testing.T) {
	// Only set STAGE — all other signed keys should use empty string.
	vars := map[string]string{
		"STAGE": "triage",
	}

	// Manually compute expected HMAC with the same canonical format.
	parts := make([]string, len(signedDispatchKeys))
	for i, k := range signedDispatchKeys {
		parts[i] = k + "=" + vars[k]
	}
	message := strings.Join(parts, "\n")
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(message))
	expected := hex.EncodeToString(mac.Sum(nil))

	got := computeDispatchHMAC("test-secret", vars)
	if got != expected {
		t.Errorf("HMAC mismatch: got %q, want %q", got, expected)
	}
}

func TestComputeDispatchHMAC_TamperedVariableChangesMAC(t *testing.T) {
	vars := map[string]string{
		"STAGE":             "triage",
		"EVENT_TYPE":        "issue_note",
		"EVENT_PAYLOAD_B64": "eyJ0eXBlIjoiaXNzdWVfbm90ZSJ9",
		"RESOURCE_KEY":      "issue-42",
		"IS_FORK":           "false",
		"ACTOR_ID":          "88",
	}

	original := computeDispatchHMAC("test-secret", vars)

	// Tamper with IS_FORK.
	tampered := maps.Clone(vars)
	tampered["IS_FORK"] = "true"

	forged := computeDispatchHMAC("test-secret", tampered)
	if original == forged {
		t.Error("tampering IS_FORK should change the HMAC")
	}
}

func TestDispatch_IncludesHMACWhenSecretSet(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{DispatchSecret: "test-secret"})

	event := RoutableEvent{
		Type:         "issue_note",
		IID:          42,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     "/fs-triage",
		NoteID:       100,
		NoteAuthorID: 88,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := mc.pipelineCalls[0].Variables
	hmacVal, ok := vars["FULLSEND_DISPATCH_HMAC"]
	if !ok {
		t.Fatal("FULLSEND_DISPATCH_HMAC should be set when DispatchSecret is configured")
	}
	if len(hmacVal) != 64 {
		t.Errorf("HMAC hex length: got %d, want 64", len(hmacVal))
	}

	// Verify the HMAC is correct by recomputing.
	expected := computeDispatchHMAC("test-secret", vars)
	if hmacVal != expected {
		t.Errorf("HMAC mismatch: got %q, want %q", hmacVal, expected)
	}
}

func TestDispatch_HMACCoversAllSignedKeysForMREvent(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{
		DispatchSecret: "test-secret",
		PollJobURL:     "https://gitlab.example.com/-/jobs/99999",
	})

	event := RoutableEvent{
		Type:         "mr_note",
		IID:          10,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRAuthorID:   42,
		NoteAuthorID: 99,
		NoteBody:     "/fs-review",
		NoteID:       500,
		MRSource:     100,
		MRTarget:     100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "review", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := mc.pipelineCalls[0].Variables

	// All signed keys should be present and non-empty.
	for _, key := range signedDispatchKeys {
		val, ok := vars[key]
		if !ok {
			t.Errorf("signed key %q missing from pipeline variables", key)
		} else if val == "" {
			t.Errorf("signed key %q is empty", key)
		}
	}

	hmacVal := vars["FULLSEND_DISPATCH_HMAC"]
	expected := computeDispatchHMAC("test-secret", vars)
	if hmacVal != expected {
		t.Errorf("HMAC mismatch: got %q, want %q", hmacVal, expected)
	}
}

func TestDispatch_NoHMACWhenSecretEmpty(t *testing.T) {
	mc := newMockClient()
	p := newUnsignedTestPoller(mc)

	event := RoutableEvent{
		Type:         "issue_note",
		IID:          42,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     "/fs-triage",
		NoteID:       100,
		NoteAuthorID: 88,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := mc.pipelineCalls[0].Variables
	if _, ok := vars["FULLSEND_DISPATCH_HMAC"]; ok {
		t.Error("FULLSEND_DISPATCH_HMAC should not be set when DispatchSecret is empty")
	}
}

func TestSignedDispatchKeys_MatchShellTemplate(t *testing.T) {
	content, err := scaffold.GitLabPerRepoFile(".gitlab/ci/scripts/run-agent-job.sh")
	if err != nil {
		t.Fatalf("read agent job script: %v", err)
	}
	s := string(content)

	// Extract the printf format string from the HMAC_MESSAGE line.
	// Format: printf 'ACTOR_ID=%s\nEVENT_PAYLOAD_B64=%s\n...'
	const marker = "HMAC_MESSAGE=$(printf '"
	idx := strings.Index(s, marker)
	if idx < 0 {
		t.Fatal("HMAC_MESSAGE printf not found in template")
	}
	fmtStart := idx + len(marker)
	fmtEnd := strings.Index(s[fmtStart:], "'")
	if fmtEnd < 0 {
		t.Fatal("closing quote for printf format not found")
	}
	fmtStr := s[fmtStart : fmtStart+fmtEnd]

	// Parse KEY=%s pairs separated by \n.
	pairs := strings.Split(fmtStr, `\n`)
	var shellKeys []string
	for _, pair := range pairs {
		eqIdx := strings.Index(pair, "=")
		if eqIdx < 0 {
			t.Fatalf("malformed pair in printf format: %q", pair)
		}
		shellKeys = append(shellKeys, pair[:eqIdx])
	}

	// Verify the shell keys match signedDispatchKeys exactly.
	if len(shellKeys) != len(signedDispatchKeys) {
		t.Fatalf("key count mismatch: shell has %d, Go has %d\nshell: %v\nGo:    %v",
			len(shellKeys), len(signedDispatchKeys), shellKeys, signedDispatchKeys)
	}
	for i, key := range signedDispatchKeys {
		if shellKeys[i] != key {
			t.Errorf("key %d: shell has %q, Go has %q", i, shellKeys[i], key)
		}
	}
}

func TestComputeDispatchHMAC_MatchesPython3(t *testing.T) {
	vars := map[string]string{
		"ACTOR_ID":              "99",
		"EVENT_PAYLOAD_B64":     "eyJ0eXBlIjoibXJfbm90ZSJ9",
		"EVENT_TYPE":            "mr_note",
		"FULLSEND_POLL_JOB_URL": "https://gitlab.example.com/-/jobs/12345",
		"IS_FORK":               "false",
		"MR_AUTHOR_ID":          "42",
		"ORIGINATING_URL":       "https://gitlab.example.com/testgroup/testrepo/-/merge_requests/10",
		"REPO_FULL_NAME":        "testgroup/testrepo",
		"RESOURCE_KEY":          "mr-10",
		"STAGE":                 "review",
		"STATUS_IID":            "10",
	}
	secret := "test-secret-for-cross-lang"

	goHMAC := computeDispatchHMAC(secret, vars)

	// Build the same canonical message the shell printf produces.
	parts := make([]string, len(signedDispatchKeys))
	for i, k := range signedDispatchKeys {
		parts[i] = k + "=" + vars[k]
	}
	message := strings.Join(parts, "\n")

	// Compute HMAC using python3 (same as the shell verifier).
	cmd := exec.Command("python3", "-c",
		"import hmac,hashlib,os,sys; print(hmac.new(os.environ['HMAC_SECRET'].encode(),sys.stdin.read().encode(),hashlib.sha256).hexdigest())")
	cmd.Stdin = strings.NewReader(message)
	cmd.Env = append(os.Environ(), "HMAC_SECRET="+secret)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python3 hmac: %v", err)
	}
	pythonHMAC := strings.TrimSpace(string(out))

	if goHMAC != pythonHMAC {
		t.Errorf("HMAC mismatch:\n  Go:     %s\n  python: %s", goHMAC, pythonHMAC)
	}
}

func TestDispatch_IncludesOriginatingURLForIssueEvent(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "issue_note",
		IID:          42,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     "/fs-retro",
		NoteID:       100,
		NoteAuthorID: 88,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "retro", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := mc.pipelineCalls[0].Variables
	wantURL := "https://gitlab.example.com/testgroup/testrepo/-/issues/42"
	if vars["ORIGINATING_URL"] != wantURL {
		t.Errorf("ORIGINATING_URL: got %q, want %q", vars["ORIGINATING_URL"], wantURL)
	}
	if vars["REPO_FULL_NAME"] != "testgroup/testrepo" {
		t.Errorf("REPO_FULL_NAME: got %q, want %q", vars["REPO_FULL_NAME"], "testgroup/testrepo")
	}
}

func TestDispatch_IncludesOriginatingURLForMREvent(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:      "mr_event",
		IID:       10,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		MRSource:  100,
		MRTarget:  100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "retro", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := mc.pipelineCalls[0].Variables
	wantURL := "https://gitlab.example.com/testgroup/testrepo/-/merge_requests/10"
	if vars["ORIGINATING_URL"] != wantURL {
		t.Errorf("ORIGINATING_URL: got %q, want %q", vars["ORIGINATING_URL"], wantURL)
	}
	if vars["REPO_FULL_NAME"] != "testgroup/testrepo" {
		t.Errorf("REPO_FULL_NAME: got %q, want %q", vars["REPO_FULL_NAME"], "testgroup/testrepo")
	}
}

func TestDispatch_OriginatingURLWithSubgroup(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	p.projectPath = "group/sub/project"
	p.gitlabURL = "https://gitlab.cee.redhat.com"

	event := RoutableEvent{
		Type:         "mr_note",
		IID:          7,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     "/fs-retro",
		NoteID:       200,
		NoteAuthorID: 55,
		MRAuthorID:   42,
		MRSource:     100,
		MRTarget:     100,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "retro", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := mc.pipelineCalls[0].Variables
	wantURL := "https://gitlab.cee.redhat.com/group/sub/project/-/merge_requests/7"
	if vars["ORIGINATING_URL"] != wantURL {
		t.Errorf("ORIGINATING_URL: got %q, want %q", vars["ORIGINATING_URL"], wantURL)
	}
	if vars["REPO_FULL_NAME"] != "group/sub/project" {
		t.Errorf("REPO_FULL_NAME: got %q, want %q", vars["REPO_FULL_NAME"], "group/sub/project")
	}
}

// --- pipeline-inputs transport tests (#7850) ---

// TestEventPayloadChunks_ChunksUnderGitLabInputLimit verifies a payload
// long enough to need multiple chunks (a 4096-byte note body
// base64-encodes to ~5.6 KB) is split into several pieces, each
// comfortably under GitLab's ~1 KB per-pipeline-input-string-value limit
// (https://docs.gitlab.com/ci/inputs/), and that the chunks round-trip
// back to the original string by plain concatenation (#7850 injection-vuln
// fix: chunks are raw data, not shell statements, so no parsing step is
// needed to recover them).
func TestEventPayloadChunks_ChunksUnderGitLabInputLimit(t *testing.T) {
	payload := strings.Repeat("A", 5600)
	chunks, err := eventPayloadChunks(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for a %d-byte payload, got %d", len(payload), len(chunks))
	}

	var rebuilt strings.Builder
	for _, chunk := range chunks {
		if len(chunk) >= 1024 {
			t.Errorf("chunk is %d bytes, want < 1024 (GitLab's per-input-string limit): %q", len(chunk), chunk)
		}
		rebuilt.WriteString(chunk)
	}
	if rebuilt.String() != payload {
		t.Errorf("reconstructed payload does not match original:\ngot:  %q\nwant: %q", rebuilt.String(), payload)
	}
}

// TestEventPayloadChunks_ShortPayloadSingleChunk verifies a short payload
// (the common case) produces exactly one chunk, unchanged.
func TestEventPayloadChunks_ShortPayloadSingleChunk(t *testing.T) {
	chunks, err := eventPayloadChunks("eyJ0eXBlIjoiaXNzdWVfbGFiZWwifQ==")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for a short payload, got %d", len(chunks))
	}
	if chunks[0] != "eyJ0eXBlIjoiaXNzdWVfbGFiZWwifQ==" {
		t.Errorf("chunk = %q, want original payload unchanged", chunks[0])
	}
}

// TestEventPayloadChunks_EmptyPayloadProducesNoChunks documents the
// defensive empty-input case: buildEventPayload always emits at least
// {"type":...}, so this should not occur with a real dispatch.
func TestEventPayloadChunks_EmptyPayloadProducesNoChunks(t *testing.T) {
	chunks, err := eventPayloadChunks("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for an empty payload, got %d", len(chunks))
	}
}

// TestEventPayloadChunks_TooLargeReturnsError verifies dispatch() fails
// closed instead of silently truncating when a payload would need more
// than maxEventPayloadChunks pieces.
func TestEventPayloadChunks_TooLargeReturnsError(t *testing.T) {
	payload := strings.Repeat("A", (maxEventPayloadChunks+1)*dispatchPayloadChunkSize)
	_, err := eventPayloadChunks(payload)
	if err == nil {
		t.Fatal("expected error for an oversized payload")
	}
}

// gitlabChunkInputNames returns every "event_payload_chunk_NN:" input
// name declared in a GitLab per-repo scaffold YAML file's spec:inputs
// block, in file order.
func gitlabChunkInputNames(t *testing.T, path string) []string {
	t.Helper()
	content, err := scaffold.GitLabPerRepoFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var names []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if name, ok := strings.CutSuffix(trimmed, ":"); ok && strings.HasPrefix(name, "event_payload_chunk_") {
			names = append(names, name)
		}
	}
	return names
}

// TestGitLabScaffoldChunkInputsMatchGoContract is a contract test (#7945
// missing-test finding): the event_payload_chunk_NN pipeline inputs
// declared in the three GitLab scaffold YAML files that carry dispatch
// data (the root .gitlab-ci.yml fixture, fullsend-pipeline.yml, and
// fullsend-agent.yml) must declare exactly maxEventPayloadChunks chunks,
// named event_payload_chunk_00..NN-1 contiguously — the Go constant that
// bounds eventPayloadChunks/dispatchInputs is the authority these three
// independently-maintained YAML files have to agree with, and with each
// other, for dispatch() to actually be able to transport a maximal
// payload through every include hop. Nothing short of parsing the YAML
// previously compared the two; a change to maxEventPayloadChunks (or a
// hand-edit to one YAML file but not the others) could silently drift
// without this test.
func TestGitLabScaffoldChunkInputsMatchGoContract(t *testing.T) {
	wantNames := make([]string, maxEventPayloadChunks)
	for i := range wantNames {
		wantNames[i] = dispatchEventPayloadChunkInputName(i)
	}

	for _, path := range []string{
		".gitlab-ci.yml",
		".gitlab/ci/fullsend-pipeline.yml",
		".gitlab/ci/fullsend-agent.yml",
	} {
		got := gitlabChunkInputNames(t, path)
		if len(got) != len(wantNames) {
			t.Errorf("%s: declares %d event_payload_chunk_NN inputs, want %d (maxEventPayloadChunks)", path, len(got), len(wantNames))
			continue
		}
		for i, name := range got {
			if name != wantNames[i] {
				t.Errorf("%s: chunk input %d = %q, want %q", path, i, name, wantNames[i])
			}
		}
	}
}

// inputString decodes a json.RawMessage holding a marshaled
// forge.PipelineInputValue (e.g. one produced by marshalInputs from a
// forge.StringInput) back to a Go string via json.Unmarshal.
func inputString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode input as string: %v", err)
	}
	return s
}

func marshalInputs(t *testing.T, inputs map[string]forge.PipelineInputValue) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(inputs)
	if err != nil {
		t.Fatalf("marshal inputs: %v", err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode marshaled inputs: %v", err)
	}
	return out
}

// TestDispatchInputs_OmitsUnsetOptionalFields verifies optional dispatch
// fields absent from the variables map (the conditional-set pattern in
// dispatch() for MR_AUTHOR_ID/ACTOR_ID/STATUS_IID/poll-job-URL/HMAC) stay
// absent from the resulting pipeline inputs too, rather than appearing as
// an empty-string input — fullsend-agent.yml's declared defaults fill the
// gap the same way an unset CI variable used to.
func TestDispatchInputs_OmitsUnsetOptionalFields(t *testing.T) {
	variables := map[string]string{
		"STAGE":             "triage",
		"EVENT_TYPE":        "issue_label",
		"EVENT_PAYLOAD_B64": "eyJ0eXBlIjoiaXNzdWVfbGFiZWwifQ==",
		"RESOURCE_KEY":      "issue-1",
		"IS_FORK":           "false",
		"ORIGINATING_URL":   "https://gitlab.example.com/org/repo/-/issues/1",
		"REPO_FULL_NAME":    "org/repo",
	}
	inputs, err := dispatchInputs(variables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw := marshalInputs(t, inputs)

	for _, name := range []string{"mr_author_id", "actor_id", "status_iid", "poll_job_url", "dispatch_hmac"} {
		if _, ok := raw[name]; ok {
			t.Errorf("input %q should be absent when its dispatch field is unset, got %s", name, raw[name])
		}
	}
	if got, want := inputString(t, raw["stage"]), "triage"; got != want {
		t.Errorf("stage input = %q, want %q", got, want)
	}
	if _, ok := raw["event_payload_chunk_00"]; !ok {
		t.Error("event_payload_chunk_00 input should be present for a non-empty payload")
	}
}

// TestDispatchInputs_IncludesAllPresentFields verifies every optional
// field is translated to its declared pipeline-input name when present.
func TestDispatchInputs_IncludesAllPresentFields(t *testing.T) {
	variables := map[string]string{
		"STAGE":               "code",
		"EVENT_TYPE":          "mr_event",
		"EVENT_PAYLOAD_B64":   "eyJ0eXBlIjoibXJfZXZlbnQifQ==",
		"RESOURCE_KEY":        "mr-5",
		"IS_FORK":             "true",
		"ORIGINATING_URL":     "https://gitlab.example.com/org/repo/-/merge_requests/5",
		"REPO_FULL_NAME":      "org/repo",
		"MR_AUTHOR_ID":        "7",
		"ACTOR_ID":            "9",
		"STATUS_IID":          "5",
		forge.VarPollJobURL:   "https://gitlab.example.com/org/repo/-/jobs/1",
		forge.VarDispatchHMAC: "deadbeef",
	}
	inputs, err := dispatchInputs(variables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw := marshalInputs(t, inputs)

	wantByInput := map[string]string{
		"stage":           "code",
		"event_type":      "mr_event",
		"resource_key":    "mr-5",
		"is_fork":         "true",
		"originating_url": "https://gitlab.example.com/org/repo/-/merge_requests/5",
		"repo_full_name":  "org/repo",
		"mr_author_id":    "7",
		"actor_id":        "9",
		"status_iid":      "5",
		"poll_job_url":    "https://gitlab.example.com/org/repo/-/jobs/1",
		"dispatch_hmac":   "deadbeef",
	}
	for name, want := range wantByInput {
		got, ok := raw[name]
		if !ok {
			t.Errorf("expected input %q to be present", name)
			continue
		}
		if s := inputString(t, got); s != want {
			t.Errorf("input %q = %q, want %q", name, s, want)
		}
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
