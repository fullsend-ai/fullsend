package poll

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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
// repository's scaffold (the CLI installer this job's wrapper script
// builds fetches and rebuilds a non-pinned ref on every poll run), so
// dispatch() must read the committed wrapper and refuse a repository that
// does not declare the typed contract.
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
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.pipelineCalls) != 1 || !mc.pipelineCalls[0].ViaInputs {
			t.Fatalf("expected one CreatePipelineWithInputs call, got %+v", mc.pipelineCalls)
		}
	})

	// The former variable-based dispatch path has been removed: a
	// repository without the full typed contract is refused, and no
	// pipeline is ever created with user-defined variables.
	for name, setup := range map[string]func(*mockClient){
		"missing wrapper is refused": func(mc *mockClient) { mc.wrapperNotFound = true },
		"legacy wrapper is refused": func(mc *mockClient) {
			mc.wrapperContent = []byte("include:\n  - local: .gitlab/ci/fullsend-agent.yml\n")
		},
		"incomplete wrapper is refused": func(mc *mockClient) { mc.wrapperContent = []byte("spec:\n  inputs:\n    stage: {}\n---\n{}\n") },
	} {
		t.Run(name, func(t *testing.T) {
			mc := newMockClient()
			setup(mc)
			p := newTestPoller(mc, Options{})
			err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
			if err == nil || !strings.Contains(err.Error(), "legacy pipeline-variable dispatch path is no longer supported") {
				t.Fatalf("expected a typed-contract refusal, got %v", err)
			}
			if !strings.Contains(err.Error(), "fullsend_ref to "+firstTypedDispatchRelease) {
				t.Fatalf("expected the refusal to name fullsend_ref and the minimum release, got %v", err)
			}
			if len(mc.pipelineCalls) != 0 {
				t.Fatalf("expected no pipeline call, got %+v", mc.pipelineCalls)
			}
			if len(p.dispatches) != 0 {
				t.Fatalf("expected no recorded dispatch, got %+v", p.dispatches)
			}
		})
	}

	t.Run("wrapper read error propagates without dispatching", func(t *testing.T) {
		mc := newMockClient()
		mc.wrapperErr = fmt.Errorf("permission denied")
		p := newTestPoller(mc, Options{})
		err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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
	// dispatch while main has moved to typed inputs, or vice versa), the
	// poller must check the contract the *dispatch ref* actually
	// declares, not the default branch's.
	t.Run("reads the wrapper at the dispatch ref, not the default branch", func(t *testing.T) {
		mc := newMockClient()
		p := newTestPoller(mc, Options{PipelineRef: "legacy-release"})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.wrapperContentRefs) != 1 || mc.wrapperContentRefs[0] != "legacy-release" {
			t.Fatalf("expected wrapper lookup at ref %q, got %+v", "legacy-release", mc.wrapperContentRefs)
		}
	})

	t.Run("legacy wrapper at the dispatch ref is refused even when default branch is typed", func(t *testing.T) {
		mc := newMockClient() // default-branch wrapper fixture declares the full typed contract
		mc.wrapperNotFound = true
		p := newTestPoller(mc, Options{PipelineRef: "legacy-release"})
		err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
		if err == nil || !strings.Contains(err.Error(), `at ref "legacy-release"`) {
			t.Fatalf("expected a typed-contract refusal naming the dispatch ref, got %v", err)
		}
		if len(mc.pipelineCalls) != 0 {
			t.Fatalf("expected no pipeline call, got %+v", mc.pipelineCalls)
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

	err := p.dispatch(context.Background(), "owner", "repo", "review", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "review", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "review", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "code", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "code", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "review", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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

	data, err := buildEventPayload(event, nil)
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

	if got, ok := payloadCommentBody(t, data); !ok || got != "/fs-triage" {
		t.Errorf("canonical comment body = %q (present %v), want %q", got, ok, "/fs-triage")
	}
	if _, ok := m["note_body"]; ok {
		t.Errorf("payload still emits the legacy note_body field: %v", m["note_body"])
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

	data, err := buildEventPayload(event, nil)
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
	optionals := []string{"note_body", "_normalized_event", "note_id", "note_author_id", "labels", "mr_source_project_id", "mr_target_project_id"}
	for _, key := range optionals {
		if _, ok := m[key]; ok {
			t.Errorf("expected optional key %q to be omitted, but it was present", key)
		}
	}
}

// payloadCommentBody returns the canonical normalized comment body,
// _normalized_event.transition.comment.body, from a buildEventPayload
// result — the only field run-agent-job.sh reads it from.
func payloadCommentBody(t *testing.T, data []byte) (string, bool) {
	t.Helper()
	var p struct {
		Norm *struct {
			Transition struct {
				Kind    string `json:"kind"`
				Comment *struct {
					Body *string `json:"body"`
				} `json:"comment"`
			} `json:"transition"`
		} `json:"_normalized_event"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Norm == nil || p.Norm.Transition.Comment == nil || p.Norm.Transition.Comment.Body == nil {
		return "", false
	}
	if p.Norm.Transition.Kind != string(normevent.TransitionCommentAdded) {
		t.Errorf("transition.kind = %q, want %q", p.Norm.Transition.Kind, normevent.TransitionCommentAdded)
	}
	return *p.Norm.Transition.Comment.Body, true
}

// multilineFixBody is a /fs-fix command whose instruction spans lines;
// the instruction run-agent-job.sh derives from it must keep them.
const multilineFixBody = "/fs-fix only address the nil check\n\n- keep the public API\n- add a regression test"

func TestBuildEventPayload_CommandBodyAtCanonicalPath(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	for _, eventType := range []string{"issue_note", "mr_note"} {
		t.Run(eventType, func(t *testing.T) {
			event := RoutableEvent{Type: eventType, IID: 1, UpdatedAt: ts, NoteBody: multilineFixBody, NoteID: 9, NoteAuthorID: 3}

			data, err := buildEventPayload(event, nil)
			if err != nil {
				t.Fatalf("buildEventPayload: %v", err)
			}
			got, ok := payloadCommentBody(t, data)
			if !ok || got != multilineFixBody {
				t.Errorf("canonical comment body = %q (present %v), want %q", got, ok, multilineFixBody)
			}
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, legacy := range []string{"note_body", "comment", "instruction", "object_attributes"} {
				if _, ok := m[legacy]; ok {
					t.Errorf("payload emits non-canonical field %q: %v", legacy, m[legacy])
				}
			}
		})
	}
}

func TestBuildEventPayload_CommandBodyOverNoteLimitNotTruncated(t *testing.T) {
	// run-agent-job.sh derives the /fs-fix instruction from this body, so
	// a command must never be shortened in transport.
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	body := "/fs-fix " + strings.Repeat("x", 2*noteBodyTransportLimit)
	event := RoutableEvent{Type: "mr_note", IID: 1, UpdatedAt: ts, NoteBody: body}

	data, err := buildEventPayload(event, nil)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	if got, ok := payloadCommentBody(t, data); !ok || got != body {
		t.Errorf("canonical comment body has %d runes (present %v), want the intact %d-rune command", len([]rune(got)), ok, len([]rune(body)))
	}
}

func TestBuildEventPayload_NonCommandBodyOverLimitTruncatedWithNotice(t *testing.T) {
	// A non-command body (such as a bot review note) is bounded for
	// transport and carries an in-band signal that it was cut.
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	body := "Changes requested\n" + strings.Repeat("x", 900)
	event := RoutableEvent{Type: "mr_note", IID: 1, UpdatedAt: ts, NoteBody: body, IsBot: true}

	data, err := buildEventPayload(event, nil)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	got, ok := payloadCommentBody(t, data)
	if !ok {
		t.Fatal("canonical comment body missing")
	}
	if n := len([]rune(got)); n > noteBodyTransportLimit {
		t.Errorf("comment body has %d runes, want at most %d", n, noteBodyTransportLimit)
	}
	if !strings.HasSuffix(got, noteBodyTruncationNotice) {
		t.Errorf("comment body = %q, want it to end with the truncation notice %q", got, noteBodyTruncationNotice)
	}
	if !strings.HasPrefix(got, "Changes requested\n") {
		t.Errorf("comment body = %q, want truncation to cut from the end", got)
	}
}

func TestDispatch_RejectsCommandTooLargeToTransportIntact(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "mr_note",
		IID:          10,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     "/fs-fix " + strings.Repeat("y", maxEventPayloadChunks*dispatchPayloadChunkSize),
		NoteID:       5,
		NoteAuthorID: 3,
	}

	err := p.dispatch(context.Background(), "owner", "repo", "fix", event, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to dispatch a truncated note body") {
		t.Fatalf("dispatch error = %v, want a refusal to dispatch a truncated note body", err)
	}
	if len(mc.pipelineCalls) != 0 || len(p.dispatches) != 0 {
		t.Errorf("pipeline calls = %d, dispatches = %d, want none", len(mc.pipelineCalls), len(p.dispatches))
	}
}

func TestDispatch_MultilineCommandCarriedIntact(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	event := RoutableEvent{
		Type:         "mr_note",
		IID:          10,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     multilineFixBody,
		NoteID:       5,
		NoteAuthorID: 3,
	}

	if err := p.dispatch(context.Background(), "owner", "repo", "fix", event, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	payload, err := base64.StdEncoding.DecodeString(p.dispatches[0].EventPayloadB64)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got, ok := payloadCommentBody(t, payload); !ok || got != multilineFixBody {
		t.Errorf("dispatched canonical comment body = %q (present %v), want %q", got, ok, multilineFixBody)
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

	err := p.dispatch(context.Background(), "owner", "repo", "review", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "code", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "review", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "review", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "retro", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "retro", event, nil)
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

	err := p.dispatch(context.Background(), "owner", "repo", "retro", event, nil)
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

// TestEventPayloadChunks_ChunksUnderScaffoldInputPattern verifies a payload
// long enough to need multiple chunks (a 4096-byte note body
// base64-encodes to ~5.6 KB) is split into several pieces, each
// comfortably under the 1000-character chunk-input pattern the scaffold's
// own configuration enforces (dispatchPayloadChunkSize; GitLab's
// documented input limits are far larger), and that the chunks round-trip
// back to the original string by plain concatenation (#7850 injection-vuln
// fix: chunks are raw data, not shell statements, so no parsing step is
// needed to recover them).
func TestEventPayloadChunks_ChunksUnderScaffoldInputPattern(t *testing.T) {
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
			t.Errorf("chunk is %d bytes, want < 1024 (the scaffold's chunk-input pattern bound): %q", len(chunk), chunk)
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

// TestDispatch_ChildPipelineRoute covers the CI_JOB_TOKEN child-pipeline
// route (#8438): with ChildDispatchJobToken set, a typed repository's agent
// pipeline is started through the trigger API with that job token and the
// same signed typed inputs as the Poller API route; without it, the Poller
// API route is unchanged; and a legacy (variable-based) repository is
// refused rather than dispatched through variables.
func TestDispatch_ChildPipelineRoute(t *testing.T) {
	event := RoutableEvent{
		Type:      "issue_note",
		IID:       42,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:  "/fs-triage",
	}

	t.Run("typed wrapper dispatches a child pipeline with the job token", func(t *testing.T) {
		mc := newMockClient()
		p := newTestPoller(mc, Options{DispatchSecret: "s3cret", ChildDispatchJobToken: "job-token-value"})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.pipelineCalls) != 1 {
			t.Fatalf("expected one pipeline call, got %+v", mc.pipelineCalls)
		}
		call := mc.pipelineCalls[0]
		if !call.ViaInputs || call.JobToken != "job-token-value" {
			t.Fatalf("expected a typed child-pipeline call with the job token, got %+v", call)
		}
		if call.Variables["STAGE"] != "triage" {
			t.Errorf("STAGE input: got %q, want triage", call.Variables["STAGE"])
		}
		want := computeDispatchHMAC("s3cret", withoutKey(call.Variables, forge.VarDispatchHMAC))
		if call.Variables[forge.VarDispatchHMAC] != want {
			t.Errorf("dispatch HMAC: got %q, want %q", call.Variables[forge.VarDispatchHMAC], want)
		}
	})

	t.Run("no job token keeps the Poller API route", func(t *testing.T) {
		mc := newMockClient()
		p := newTestPoller(mc, Options{})
		if err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mc.pipelineCalls) != 1 || !mc.pipelineCalls[0].ViaInputs || mc.pipelineCalls[0].JobToken != "" {
			t.Fatalf("expected one Poller API typed-input call, got %+v", mc.pipelineCalls)
		}
	})

	t.Run("legacy wrapper is refused on the child route", func(t *testing.T) {
		mc := newMockClient()
		mc.wrapperNotFound = true
		p := newTestPoller(mc, Options{ChildDispatchJobToken: "job-token-value"})
		err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
		if err == nil || !strings.Contains(err.Error(), "requires the typed pipeline-input contract") {
			t.Fatalf("expected a typed-contract error, got %v", err)
		}
		if len(p.dispatches) != 0 {
			t.Fatalf("expected no recorded dispatch, got %+v", p.dispatches)
		}
		if len(mc.pipelineCalls) != 0 {
			t.Fatalf("expected no pipeline call, got %+v", mc.pipelineCalls)
		}
	})

	t.Run("child pipeline failure is reported", func(t *testing.T) {
		mc := newMockClient()
		mc.pipelineErr = fmt.Errorf("403 forbidden")
		p := newTestPoller(mc, Options{ChildDispatchJobToken: "job-token-value"})
		err := p.dispatch(context.Background(), "owner", "repo", "triage", event, nil)
		if err == nil || !strings.Contains(err.Error(), "403 forbidden") {
			t.Fatalf("expected the child-pipeline error, got %v", err)
		}
		if len(p.dispatches) != 0 {
			t.Fatalf("expected no recorded dispatch after a failed child pipeline, got %+v", p.dispatches)
		}
	})
}

// withoutKey returns a copy of vars without key.
func withoutKey(vars map[string]string, key string) map[string]string {
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// worstCaseAttackerEvent builds the worst attacker-shaped event the
// transport bounds have to contain: an escape-heavy note body at
// noteBodyTransportLimit, state.labels at the labels bound, maximal
// escape-heavy branch refs and logins, schema-maximal repo paths and entity
// URLs, the linked change proposal, a review — and the maximal
// transition.label.name a work item can carry beside its comment
// (TestBuildEventPayload_WorstCaseFitsTransportBudget, the frozen pin of
// this shape, predates the label name; this helper includes it so the
// non-frozen worst-case tests pay for it too).
func worstCaseAttackerEvent() (RoutableEvent, dispatch.NormalizedEvent) {
	event, ne := mrNote(strings.Repeat("<", noteBodyTransportLimit))
	labels := make([]string, 2000)
	for i := range labels {
		labels[i] = "label-number-" + strings.Repeat("z", 5)
	}
	ne.State.Labels = labels
	event.Labels = labels
	event.SourceBranch = strings.Repeat("<", 255)
	event.TargetBranch = strings.Repeat("<", 255)
	if cp := ne.State.ChangeProposal; cp != nil {
		cp.HeadRef = strings.Repeat("<", 255)
		cp.BaseRef = strings.Repeat("<", 255)
		cp.AuthorID = strings.Repeat("<", 255)
		cp.HeadRepo = strings.Repeat("<", 512)
		cp.BaseRepo = strings.Repeat("<", 512)
	}
	ne.Repo = strings.Repeat("<", 512)
	ne.Actor.ID = strings.Repeat("<", 255)
	ne.Entity.Kind = "work_item"
	ne.Entity.URL = strings.Repeat("<", 2048)
	ne.Entity.LinkedChangeProposal = &dispatch.LinkedChangeProposal{
		ID:  12,
		URL: strings.Repeat("<", 2048),
	}
	ne.Transition.Review = &dispatch.TransitionReview{
		State:      "approved",
		ReviewerID: strings.Repeat("<", 255),
	}
	ne.Transition.Label = &dispatch.TransitionLabel{Action: "added", Name: strings.Repeat("<", 255)}
	return event, ne
}

// TestBuildEventPayload_WorstCaseWithLabelNameFitsBudget pins the worst-case
// budget arithmetic the three transport bounds exist for, including the
// maximal transition.label.name the frozen worst-case test predates: with
// every bounded attacker-shaped field at its transported bound
// simultaneously — label name included — the complete form must still fit
// the chunk budget, so a bound raise, or a newly classified bounded field
// that consumes the remaining margin, reopens the comment-only fallback
// (the attacker-chosen "no event" routing channel) here instead of silently.
func TestBuildEventPayload_WorstCaseWithLabelNameFitsBudget(t *testing.T) {
	event, ne := worstCaseAttackerEvent()

	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	// The complete form, not the comment-only fallback: it validates as a
	// normalized event and carries the bounded label name.
	ev := payloadEvent(t, data)
	if label := ev.Transition.Label; label == nil || len(label.Name) != (transportStringRefLimit-2)/6 {
		t.Errorf("transported label = %+v, want the %d-rune bounded label name carried", ev.Transition.Label, (transportStringRefLimit-2)/6)
	}
	if n := base64.StdEncoding.EncodedLen(len(data)); n > maxEventPayloadChunks*dispatchPayloadChunkSize {
		t.Errorf("worst-case payload with label name is %d base64 characters, over the %d budget", n, maxEventPayloadChunks*dispatchPayloadChunkSize)
	}
}

// TestBuildEventPayload_OverBudgetNonCommandFallsBackBounded covers the
// comment-only fallback's over-budget branch: a valid complete normalized
// event that does not fit the transport budget with a non-command body must
// return the comment-only payload — never a partial _normalized_event — and
// the fallback's flat labels copy must be bounded like state.labels. The
// attacker-shaped bounds keep every attacker-shaped worst case inside the
// budget (TestBuildEventPayload_WorstCaseFitsTransportBudget and
// TestBuildEventPayload_WorstCaseWithLabelNameFitsBudget pin that), so
// the branch is forced here the way it can still be forced: a forge-assigned
// string no transport bound owns (entity.key, classified by the drift
// guards) padded past the budget. The branch is the payload path dispatch()'s
// outer budget check guards, so a future field or bound drift that reopens
// it fails here instead of shipping an untested fallback.
func TestBuildEventPayload_OverBudgetNonCommandFallsBackBounded(t *testing.T) {
	// The worst attacker-shaped shape, so only the padded forge-assigned
	// key decides whether the complete form fits.
	event, ne := worstCaseAttackerEvent()
	// entity.key is forge-assigned (the forge, not the event author, picks
	// it), so no transport bound owns it; padded past the budget it makes
	// the valid complete form untransportable.
	ne.Entity.Key = strings.Repeat("k", 2000)

	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	// The fallback is the comment-only payload, byte for byte: the same
	// event without a normalized event marshals the same map.
	want, err := buildEventPayload(event, nil)
	if err != nil {
		t.Fatalf("buildEventPayload without event: %v", err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("payload = %s\nwant the comment-only payload %s", data, want)
	}
	// The fallback's flat labels copy is bounded like state.labels: a
	// strict prefix of the event's labels whose JSON fits
	// transportLabelsLimit, so the fallback itself always fits the budget.
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, ok := m["labels"].([]any)
	if !ok || len(raw) == 0 || len(raw) == len(event.Labels) {
		t.Fatalf("flat labels = %v, want a strict non-empty prefix of %d labels", raw, len(event.Labels))
	}
	got := make([]string, len(raw))
	for i, v := range raw {
		got[i], ok = v.(string)
		if !ok {
			t.Fatalf("flat label %d = %T, want a string", i, v)
		}
	}
	if !slices.Equal(got, event.Labels[:len(got)]) {
		t.Errorf("flat labels are not a prefix of the event's labels")
	}
	if enc, err := json.Marshal(got); err != nil || len(enc) > transportLabelsLimit {
		t.Errorf("flat labels are %d JSON bytes (err %v), want at most %d", len(enc), err, transportLabelsLimit)
	}
	if n := base64.StdEncoding.EncodedLen(len(data)); n > maxEventPayloadChunks*dispatchPayloadChunkSize {
		t.Errorf("fallback payload is %d base64 characters, over the %d budget", n, maxEventPayloadChunks*dispatchPayloadChunkSize)
	}
}

// jiraBoundedFields are the string paths encodeJiraEventPayload bounds on
// the Jira door: state.labels through boundTransportLabels (exactly like the
// GitLab door), and the comment body and instruction through the
// jiraCommentLimits shortening ladder (a /fs-* command body is exempt and
// fails the dispatch closed when it does not fit).
var jiraBoundedFields = map[string]bool{
	"state.labels":                   true,
	"transition.comment.body":        true,
	"transition.comment.instruction": true,
}

// jiraCarriedAsIs are the attacker-shaped single strings the Jira door
// carries as-is — the recorded deliberate divergence from the GitLab door's
// boundTransportEventStrings: this door has no comment-only fallback whose
// no-event shape a truncated string could shape, so an oversized one fails
// the budget check and the dispatch errors before a pipeline is created
// (frozen TestEncodeJiraEventPayload_TooLargeWithoutComment pins that).
// Keep any change to transportStringRefLimit or this list mirrored on both
// doors, the way the GitLab door's drift guard
// (TestBoundTransportEventStrings_CoversEveryStringField) enforces its side.
var jiraCarriedAsIs = map[string]bool{
	"repo":                              true,
	"entity.url":                        true,
	"entity.linked_change_proposal.url": true,
	"transition.label.name":             true,
	"transition.review.reviewer_id":     true,
	"actor.id":                          true,
	"state.change_proposal.head_repo":   true,
	"state.change_proposal.base_repo":   true,
	"state.change_proposal.head_ref":    true,
	"state.change_proposal.base_ref":    true,
	"state.change_proposal.author_id":   true,
}

// jiraForgeOrEnum are the strings the forge, a closed schema enum or a
// derived token picks; the Jira event author cannot pad them.
var jiraForgeOrEnum = map[string]bool{
	"entity.kind":             true,
	"entity.key":              true,
	"transition.kind":         true,
	"transition.label.action": true,
	// extractCommand derives command from the comment body: empty or a
	// short /fs-* token.
	"transition.comment.command": true,
	// backend-native message ids (ADR 0086).
	"transition.comment.id":              true,
	"transition.comment.parent_id":       true,
	"transition.review.state":            true,
	"actor.kind":                         true,
	"actor.role":                         true,
	"actor.bot_role":                     true,
	"state.change_proposal.head_sha":     true,
	"state.conversation.category.id":     true,
	"state.conversation.category.name":   true,
	"state.conversation.category.slug":   true,
	"state.conversation.category.format": true,
	"source.system":                      true,
	"source.raw_type":                    true,
	"source.raw_action":                  true,
}

// TestEncodeJiraEventPayload_ClassifiesEveryStringField is the Jira door's
// drift guard, mirroring the GitLab door's
// TestBoundTransportEventStrings_CoversEveryStringField over normevent.Event
// (the struct the Jira door transports). A string field added to
// normevent.Event or one of its nested structs fails this test until it is
// classified: bounded in encodeJiraEventPayload, carried as-is under the
// recorded fail-closed divergence, or forge-assigned/enum — so a new
// attacker-shaped field cannot ship unbounded on one door unnoticed.
func TestEncodeJiraEventPayload_ClassifiesEveryStringField(t *testing.T) {
	seen := map[string]bool{}
	collectStringFieldPaths(reflect.TypeOf(normevent.Event{}), "", seen)
	for _, path := range slices.Sorted(maps.Keys(seen)) {
		if jiraBoundedFields[path] || jiraCarriedAsIs[path] || jiraForgeOrEnum[path] {
			continue
		}
		t.Errorf("unclassified string field %q: bound it in encodeJiraEventPayload, classify it as carried as-is under the recorded GitLab-door divergence, or as forge-assigned/enum", path)
	}
	for _, classified := range []map[string]bool{jiraBoundedFields, jiraCarriedAsIs, jiraForgeOrEnum} {
		for path := range classified {
			if !seen[path] {
				t.Errorf("classified string field %q no longer exists on normevent.Event", path)
			}
		}
	}
}

// TestBoundTransportStringQuoteAndControlRunesFitRefLimit covers
// jsonRuneLen's default arm — the quote, the backslash and ASCII control
// characters such as newline — which TestBoundTransportString's escape-heavy
// and invalid-UTF-8 cases do not reach. The arm is load-bearing for the
// "never estimate longer than the encoder writes" invariant: if it were
// simplified to utf8.RuneLen(r), a quote- or control-heavy branch name or
// label would silently exceed transportStringRefLimit and eat the worst-case
// budget margin.
func TestBoundTransportStringQuoteAndControlRunesFitRefLimit(t *testing.T) {
	s := strings.Repeat("\"\\\n", 128)
	got := boundTransportString(s)
	if want := (transportStringRefLimit - 2) / 6; len(got) != want {
		t.Errorf("boundTransportString(quote/control-heavy) = %d runes, want the %d-rune prefix", len(got), want)
	}
	if enc, err := json.Marshal(got); err != nil || len(enc) > transportStringRefLimit {
		t.Errorf("bounded string is %d JSON bytes (err %v), want at most %d", len(enc), err, transportStringRefLimit)
	}
}

// TestBoundTransportLabelsSingleOverBoundLabelDropsAll covers
// boundTransportLabels' kept==0 arm: when the first label alone exceeds
// transportLabelsLimit, nothing fits and the bound returns an empty,
// non-nil slice — the payload travels with state.labels: [] rather than a
// nil list or a cut single label. TestBoundTransportLabels (in
// dispatch_normalized_test.go) covers the fitting, nil and many-labels
// cases but never this one.
func TestBoundTransportLabelsSingleOverBoundLabelDropsAll(t *testing.T) {
	got := boundTransportLabels([]string{strings.Repeat("x", 1500)})
	if got == nil || len(got) != 0 {
		t.Errorf("boundTransportLabels(single over-bound label) = %v (len %d), want an empty non-nil slice", got, len(got))
	}
}

// TestEncodeJiraEventPayload_LastLadderStepCarriesNoCommentText pins the
// ladder's 0 step: at the largest comment ballast that still
// dispatches, the ladder has walked to its last step, and the dispatched
// comment body must be empty — not the 13-rune truncation notice. The
// ballast is the comment's id and parent_id (carried as-is, escape-heavy);
// one more ballast rune pair costs 12 JSON bytes, less than the notice's
// ~15, so a notice-carrying last step would fail this dispatch instead of
// fitting: a step-100 body costs far more than that, so the maximal
// fitting ballast always lands on step 0.
func TestEncodeJiraEventPayload_LastLadderStepCarriesNoCommentText(t *testing.T) {
	base := jiraTestEvent("group/sub/project")
	base.Transition.Comment.Body = "please review " + strings.Repeat("<", 900)
	base.Transition.Comment.Command = ""
	fits := func(n int) bool {
		ev := *base
		c := *ev.Transition.Comment
		c.ID = strings.Repeat("<", n)
		c.ParentID = strings.Repeat("<", n)
		ev.Transition.Comment = &c
		encoded, err := encodeJiraEventPayload("jira_comment", &ev)
		return err == nil && len(encoded) <= maxEventPayloadChunks*dispatchPayloadChunkSize
	}
	lo, hi := 0, 4096
	if !fits(lo) {
		t.Fatal("the unpadded event does not dispatch")
	}
	if fits(hi) {
		t.Fatal("the maximally padded event still dispatches; the search range is wrong")
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	ev := *base
	c := *ev.Transition.Comment
	c.ID = strings.Repeat("<", lo)
	c.ParentID = strings.Repeat("<", lo)
	ev.Transition.Comment = &c
	encoded, err := encodeJiraEventPayload("jira_comment", &ev)
	if err != nil {
		t.Fatalf("encodeJiraEventPayload at the largest fitting ballast %d: %v", lo, err)
	}
	_, got := decodeJiraPayload(t, encoded)
	if got.Transition.Comment.Body != "" || got.Transition.Comment.Instruction != "" {
		t.Errorf("last ladder step body = %q, instruction = %q, want both empty (no truncation notice)",
			got.Transition.Comment.Body, got.Transition.Comment.Instruction)
	}
}

// TestEncodeJiraEventPayload_OversizedCommandBodyFailsClosed pins the Jira
// door's command fail-closed for the body shape only the complete form
// refuses: a multiline ASCII /fs-* command past the adapter's 4096-code-point
// cap whose 4096-rune prefix fits the 9x1000-character budget. The jirapoll
// adapter now carries a command body untruncated (convert.go), so this door
// measures the complete body and must refuse it with ErrTransportBudget
// instead of dispatching a silently shortened instruction — the defect open
// threads 4532843765 and 4532950230 name (before that fix the adapter cut
// the body to 4096 runes first and the prefix dispatched).
func TestEncodeJiraEventPayload_OversizedCommandBodyFailsClosed(t *testing.T) {
	body := "/fs-triage review the acceptance criteria\n" +
		strings.Repeat("plain ascii review notes, one line at a time\n", 200)
	if got := len([]rune(body)); got <= 4096 {
		t.Fatalf("test body is %d runes, want more than the 4096-rune adapter cap", got)
	}
	ev := jiraTestEvent("group/sub/project")
	ev.Transition.Comment.Body = body
	ev.Transition.Comment.Command = "/fs-triage"
	ev.Transition.Comment.Instruction = "review the acceptance criteria"

	_, err := encodeJiraEventPayload("jira_comment", ev)
	if !errors.Is(err, ErrTransportBudget) {
		t.Fatalf("encodeJiraEventPayload(oversized command body) error = %v, want ErrTransportBudget", err)
	}

	// Control: the 4096-rune prefix the adapter used to cut command bodies
	// to fits the budget, so the refusal above is the complete command
	// body's, not an event too big on its own — exactly the silently
	// shortened dispatch the fail-closed exists to prevent.
	cut := *ev
	c := *ev.Transition.Comment
	c.Body = truncate(body, 4096)
	cut.Transition.Comment = &c
	if _, err := encodeJiraEventPayload("jira_comment", &cut); err != nil {
		t.Fatalf("encodeJiraEventPayload(4096-rune command prefix) error = %v, want none: the prefix fits the budget", err)
	}
}
