package runtime

import (
	"bytes"
	"strings"
	"testing"
)

func TestClaudeTaskPartialUpdatesAndResume(t *testing.T) {
	stream := `{"type":"system","subtype":"task_started","task_id":"one","run_id":"run1","description":"Initial"}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"pending","description":"Waiting for gate"}}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"paused"}}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"description":"Resume authorized"}}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"running"}}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"run1","description":"Working","last_tool_name":"Bash","summary":"Testing the patch"}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"failed","error":"provider refused"}}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"run1","description":"STALE progress"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"run1","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","label":"STALE child"}]}
{"type":"system","subtype":"task_notification","task_id":"one","status":"failed","summary":"PRIVATE full result"}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Waiting for gate — pending", "Waiting for gate — paused", "Resume authorized — paused", "Resume authorized — running", "tool: Bash", "Testing the patch", "failed", "provider refused"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out.String())
		}
	}
	for _, bad := range []string{"PRIVATE", "STALE"} {
		if strings.Contains(out.String(), bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
	// A real new start may reuse a task ID. Missing run IDs inherit only the
	// current run, and a second explicit run does not merge the old label.
	if err := parseClaudeStream(strings.NewReader(`{"type":"system","subtype":"task_started","task_id":"one","run_id":"run2","description":"Fresh run"}`+"\n"), r.Handle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Fresh run — running") {
		t.Fatal("new start was suppressed")
	}
}

func TestClaudeWorkflowAgentDetails(t *testing.T) {
	line := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"error","blocked":true,"error":"schema refused","label":"Create"},{"type":"workflow_agent","index":2,"state":"done","cached":true,"label":"Read","resultPreview":"PRIVATE"},{"type":"workflow_agent","index":3,"state":"start","queuedAt":1,"label":"Review"}]}`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(line+"\n"), r.Handle); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"failed (blocked)", "schema refused", "completed (cached)", "Review — queued"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("result leaked")
	}
}

func TestClaudeAmbientTasksStayHidden(t *testing.T) {
	stream := `{"type":"system","subtype":"task_started","task_id":"ambient","ambient":true,"description":"PRIVATE"}
{"type":"system","subtype":"task_progress","task_id":"ambient","description":"PRIVATE","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","label":"PRIVATE"}]}
{"type":"system","subtype":"task_updated","task_id":"ambient","patch":{"status":"completed"}}
{"type":"system","subtype":"task_notification","task_id":"ambient","status":"completed"}
{"type":"system","subtype":"task_started","task_id":"skip","skip_transcript":true,"description":"PRIVATE"}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("ambient tasks leaked: %s", out.String())
	}
}

func TestClaudeTaskErrorOnlyAndExplicitEmptyLabel(t *testing.T) {
	stream := `{"type":"system","subtype":"task_started","task_id":"one","description":"Old title"}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"description":""}}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"error":"Details changed"}}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"failed"}}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"error":""}}
{"type":"system","subtype":"task_updated","task_id":"unknown","patch":{"description":"Orphan patch"}}
{"type":"system","subtype":"task_notification","task_id":"orphan","status":"stopped","reason":"worker_restart","summary":"PRIVATE"}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Old title") != 1 || !strings.Contains(out.String(), "Task one — running — Details changed") || !strings.Contains(out.String(), "Task one — failed — Details changed") || !strings.Contains(out.String(), "Task one — failed\n") || !strings.Contains(out.String(), "Orphan patch — updated") || !strings.Contains(out.String(), "worker restarted") {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("terminal summary leaked")
	}
	for _, state := range []string{"pending", "paused"} {
		if string(claudeTaskState(state)) != state {
			t.Fatalf("state %s missing", state)
		}
	}
}
