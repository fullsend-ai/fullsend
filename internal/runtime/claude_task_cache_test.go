package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestClaudeTaskSparseToolAndSummary(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	for _, fields := range []string{
		`"description":"Build","last_tool_name":"Bash","summary":"Running tests"`,
		`"description":"Build","usage":{"total_tokens":20,"tool_uses":1}`,
		`"usage":{"total_tokens":30,"tool_uses":1}`,
		`"description":"Build","last_tool_name":null,"summary":null`,
	} {
		raw := `{"type":"system","subtype":"task_progress","task_id":"one",` + fields + "}\n"
		if err := parseClaudeStream(strings.NewReader(raw), r.Handle); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(out.String(), "Task one") != 1 || !strings.Contains(out.String(), "tool: Bash") || !strings.Contains(out.String(), "Running tests") {
		t.Fatal("sparse task erased progress: " + out.String())
	}
	for _, row := range r.taskProgressSeen {
		if row.event.ToolName != "Bash" || row.event.Summary != "Running tests" {
			t.Fatalf("sparse cache erased metadata: %+v", row.event)
		}
	}
}

func TestClaudeWorkflowLargeSnapshotsDoNotReprint(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	var out bytes.Buffer
	r := newTestRenderer(&out)
	parse := func(raw string) {
		t.Helper()
		if err := parseClaudeStream(strings.NewReader(raw+"\n"), r.Handle); err != nil {
			t.Fatal(err)
		}
	}
	frame := func(task string, count int) string {
		entries := make([]map[string]any, count)
		for i := range entries {
			entries[i] = map[string]any{"type": "workflow_agent", "index": i + 1, "state": "progress", "label": "Worker"}
		}
		raw, err := json.Marshal(map[string]any{"type": "system", "subtype": "task_progress", "task_id": task, "run_id": "r2", "workflow_progress": entries})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	a, b := frame("one", 1000), frame("two", 500)
	stderr := captureStderr(t, func() {
		parse(`{"type":"system","subtype":"task_started","task_id":"hidden","ambient":true,"description":"PRIVATE"}`)
		for _, task := range []string{"one", "two"} {
			parse(fmt.Sprintf(`{"type":"system","subtype":"task_started","task_id":%q,"run_id":"r2","description":%q}`, task, task))
		}
		for range 20 {
			parse(a)
			parse(b)
		}
		parse(`{"type":"system","subtype":"task_progress","task_id":"hidden","description":"PRIVATE"}`)
		parse(`{"type":"system","subtype":"task_notification","task_id":"one","run_id":"r1","status":"completed"}`)
	})
	if got := strings.Count(out.String(), " / agent #"); got != 1500 {
		t.Fatalf("unchanged snapshots printed %d agent lines, want1500", got)
	}
	if strings.Count(stderr, "::notice::") != 1502 || strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), "completed") {
		t.Fatal("cache pressure lost dedup/hidden/run guards")
	}
}

func TestClaudeTaskExplicitClearsAndPauseResume(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	parse := func(raw string) {
		t.Helper()
		if err := parseClaudeStream(strings.NewReader(raw+"\n"), r.Handle); err != nil {
			t.Fatal(err)
		}
	}
	parse(`{"type":"system","subtype":"task_progress","task_id":"one","description":"Build","last_tool_name":"Bash","summary":"Running tests"}`)
	parse(`{"type":"system","subtype":"task_progress","task_id":"one","description":"Renamed","last_tool_name":{},"summary":42}`)
	if !strings.HasSuffix(out.String(), "Renamed — running (tool: Bash) — Running tests\n") {
		t.Fatal("malformed optional fields erased context: " + out.String())
	}
	parse(`{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"paused"}}`)
	if !strings.HasSuffix(out.String(), "Renamed — paused (tool: Bash)\n") {
		t.Fatal("paused task displayed active summary")
	}
	parse(`{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"running"}}`)
	if !strings.HasSuffix(out.String(), "Renamed — running (tool: Bash) — Running tests\n") {
		t.Fatal("resume lost once-per-change status")
	}
	parse(`{"type":"system","subtype":"task_progress","task_id":"one","summary":""}`)
	parse(`{"type":"system","subtype":"task_progress","task_id":"one","last_tool_name":""}`)
	before := out.String()
	parse(`{"type":"system","subtype":"task_progress","task_id":"one","description":"Renamed","usage":{"total_tokens":30}}`)
	if out.String() != before || !strings.HasSuffix(before, "Renamed — running\n") {
		t.Fatal("empty fields not retained across sparse snapshot: " + out.String())
	}
	parse(`{"type":"system","subtype":"task_notification","task_id":"one","status":"completed","summary":"PRIVATE result"}`)
	parse(`{"type":"system","subtype":"task_started","task_id":"one","run_id":"r2","description":"Fresh"}`)
	if strings.Contains(out.String(), "PRIVATE") || !strings.HasSuffix(out.String(), "Fresh — running\n") {
		t.Fatal("new generation inherited old context/result: " + out.String())
	}
}
