package poll

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- appendDispatch tests ---

func TestAppendDispatch_AddsToList(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	d := Dispatch{
		Stage:           "triage",
		EventType:       "issue_comment",
		EventPayloadB64: "dGVzdA==",
		ResourceKey:     "issue_comment-1",
	}
	if err := p.appendDispatch(d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(p.dispatches))
	}
	if p.dispatches[0].Stage != "triage" {
		t.Errorf("got stage %q, want triage", p.dispatches[0].Stage)
	}
}

func TestAppendDispatch_AccumulatesMultiple(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	for i := 0; i < 3; i++ {
		_ = p.appendDispatch(Dispatch{Stage: "triage", ResourceKey: "k"})
	}
	if len(p.dispatches) != 3 {
		t.Errorf("expected 3 dispatches, got %d", len(p.dispatches))
	}
}

// --- writeDispatches tests ---

func TestWriteDispatches_EmptyWritesEmptyArray(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	path := filepath.Join(t.TempDir(), "dispatches.json")
	if err := p.writeDispatches(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "[]\n" {
		t.Errorf("got %q, want %q", string(data), "[]\n")
	}
}

func TestWriteDispatches_WritesValidJSONArray(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	_ = p.appendDispatch(Dispatch{
		Stage:           "triage",
		EventType:       "issue_comment",
		EventPayloadB64: "cGF5bG9hZA==",
		ResourceKey:     "issue_comment-5",
	})
	_ = p.appendDispatch(Dispatch{
		Stage:           "code",
		EventType:       "issue_label",
		EventPayloadB64: "bGFiZWw=",
		ResourceKey:     "issue_label-10",
	})

	path := filepath.Join(t.TempDir(), "dispatches.json")
	if err := p.writeDispatches(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var parsed []Dispatch
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 dispatches, got %d", len(parsed))
	}
	if parsed[0].Stage != "triage" {
		t.Errorf("dispatch 0: got stage %q, want triage", parsed[0].Stage)
	}
	if parsed[1].Stage != "code" {
		t.Errorf("dispatch 1: got stage %q, want code", parsed[1].Stage)
	}
}

func TestWriteDispatches_TrailingNewline(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	_ = p.appendDispatch(Dispatch{Stage: "triage", ResourceKey: "k-1"})

	path := filepath.Join(t.TempDir(), "dispatches.json")
	if err := p.writeDispatches(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("expected trailing newline")
	}
}

// --- dispatch method tests ---

func TestDispatch_BuildsPayloadAndAppends(t *testing.T) {
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

// --- generateChildPipelineYAML tests ---

func TestGenerateChildPipelineYAML_SingleDispatch(t *testing.T) {
	dispatches := []Dispatch{
		{
			Stage:           "triage",
			EventType:       "issue_comment",
			EventPayloadB64: "cGF5bG9hZA==",
			ResourceKey:     "issue_comment-1",
		},
	}

	yaml, err := generateChildPipelineYAML(dispatches)
	if err != nil {
		t.Fatalf("generateChildPipelineYAML: %v", err)
	}

	expected := []string{
		"agent-0:",
		"trigger:",
		".gitlab/ci/fullsend-triage.yml",
		"strategy: depend",
		`STAGE: "triage"`,
		`EVENT_TYPE: "issue_comment"`,
		`EVENT_PAYLOAD_B64: "cGF5bG9hZA=="`,
		`RESOURCE_KEY: "issue_comment-1"`,
		"rules:",
		"- when: always",
	}
	for _, substr := range expected {
		if !strings.Contains(yaml, substr) {
			t.Errorf("missing %q in output:\n%s", substr, yaml)
		}
	}
}

func TestGenerateChildPipelineYAML_MultipleDispatches(t *testing.T) {
	dispatches := []Dispatch{
		{Stage: "triage", EventType: "issue_comment", EventPayloadB64: "a", ResourceKey: "k1"},
		{Stage: "code", EventType: "issue_label", EventPayloadB64: "b", ResourceKey: "k2"},
		{Stage: "review", EventType: "mr_comment", EventPayloadB64: "c", ResourceKey: "k3"},
	}

	yaml, err := generateChildPipelineYAML(dispatches)
	if err != nil {
		t.Fatalf("generateChildPipelineYAML: %v", err)
	}

	for i := 0; i < 3; i++ {
		marker := "agent-" + strings.Repeat("", 0) + string(rune('0'+i)) + ":"
		if !strings.Contains(yaml, marker) {
			t.Errorf("missing %q in output:\n%s", marker, yaml)
		}
	}

	if !strings.Contains(yaml, "fullsend-triage.yml") {
		t.Error("missing triage stage include")
	}
	if !strings.Contains(yaml, "fullsend-code.yml") {
		t.Error("missing code stage include")
	}
	if !strings.Contains(yaml, "fullsend-review.yml") {
		t.Error("missing review stage include")
	}
}

func TestGenerateChildPipelineYAML_EmptyDispatches(t *testing.T) {
	yaml, err := generateChildPipelineYAML(nil)
	if err != nil {
		t.Fatalf("generateChildPipelineYAML(nil): %v", err)
	}
	if yaml != "" {
		t.Errorf("expected empty string, got %q", yaml)
	}

	yaml, err = generateChildPipelineYAML([]Dispatch{})
	if err != nil {
		t.Fatalf("generateChildPipelineYAML([]): %v", err)
	}
	if yaml != "" {
		t.Errorf("expected empty string for empty slice, got %q", yaml)
	}
}

func TestGenerateChildPipelineYAML_InvalidStageReturnsError(t *testing.T) {
	dispatches := []Dispatch{
		{Stage: "INVALID STAGE!", EventType: "issue_comment", EventPayloadB64: "a", ResourceKey: "k"},
	}
	_, err := generateChildPipelineYAML(dispatches)
	if err == nil {
		t.Fatal("expected error for invalid stage name")
	}
	if !strings.Contains(err.Error(), "invalid stage name") {
		t.Errorf("unexpected error: %v", err)
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

// --- GenerateChildPipelineFromFile tests ---

func TestGenerateChildPipelineFromFile_ReadsAndWrites(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "dispatches.json")
	outputPath := filepath.Join(tmpDir, "child-pipeline.yml")

	dispatches := []Dispatch{
		{
			Stage:           "triage",
			EventType:       "issue_comment",
			EventPayloadB64: "cGF5bG9hZA==",
			ResourceKey:     "issue_comment-5",
		},
		{
			Stage:           "code",
			EventType:       "issue_label",
			EventPayloadB64: "bGFiZWw=",
			ResourceKey:     "issue_label-10",
		},
	}
	data, err := json.Marshal(dispatches)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(inputPath, data, 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	if err := GenerateChildPipelineFromFile(inputPath, outputPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	yamlStr := string(output)

	if !strings.Contains(yamlStr, "agent-0:") {
		t.Error("missing agent-0 in output")
	}
	if !strings.Contains(yamlStr, "agent-1:") {
		t.Error("missing agent-1 in output")
	}
	if !strings.Contains(yamlStr, "fullsend-triage.yml") {
		t.Error("missing triage include")
	}
	if !strings.Contains(yamlStr, "fullsend-code.yml") {
		t.Error("missing code include")
	}
}

func TestGenerateChildPipelineFromFile_MissingInputFile(t *testing.T) {
	tmpDir := t.TempDir()
	err := GenerateChildPipelineFromFile(
		filepath.Join(tmpDir, "nonexistent.json"),
		filepath.Join(tmpDir, "output.yml"),
	)
	if err == nil {
		t.Fatal("expected error for missing input file")
	}
	if !strings.Contains(err.Error(), "read dispatches file") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGenerateChildPipelineFromFile_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(inputPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := GenerateChildPipelineFromFile(inputPath, filepath.Join(tmpDir, "output.yml"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "unmarshal dispatches") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGenerateChildPipelineFromFile_EmptyDispatches(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "empty.json")
	outputPath := filepath.Join(tmpDir, "output.yml")

	if err := os.WriteFile(inputPath, []byte("[]"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := GenerateChildPipelineFromFile(inputPath, outputPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(output) != "" {
		t.Errorf("expected empty output for empty dispatches, got %q", string(output))
	}
}
