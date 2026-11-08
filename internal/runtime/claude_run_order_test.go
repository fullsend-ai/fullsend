package runtime

import (
	"bytes"
	"strings"
	"testing"
)

func TestClaudeTaskLateUnseenRunCannotBecomeCurrent(t *testing.T) {
	for _, late := range []string{
		`{"type":"system","subtype":"task_notification","task_id":"t1","run_id":"r1","status":"completed"}`,
		`{"type":"system","subtype":"task_progress","task_id":"t1","run_id":"r1","description":"STALE"}`,
		`{"type":"system","subtype":"task_updated","task_id":"t1","run_id":"r1","patch":{"description":"STALE","status":"failed"}}`,
		`{"type":"system","subtype":"task_progress","task_id":"t1","run_id":"r1","workflow_progress":[{"type":"workflow_agent","index":1,"state":"done","label":"STALE"}]}`,
	} {
		t.Run(late, func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			stream := `{"type":"system","subtype":"task_started","task_id":"t1","run_id":"r2","description":"Current"}` + "\n" + late + "\n" + `{"type":"system","subtype":"task_updated","task_id":"t1","patch":{"status":"failed"}}` + "\n"
			if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "completed") || strings.Contains(out.String(), "STALE") || !strings.HasSuffix(out.String(), "Task t1: Current — failed\n") || strings.Count(out.String(), "Task t1") != 2 {
				t.Fatal("late run hijacked display: " + out.String())
			}
		})
	}
}

func TestTaskRunOrderForwardAndAnonymousWatermark(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", RunID: "r2", Started: true, State: TaskRunning, Label: "Prior"})
	r.Handle(TaskProgressEvent{TaskID: "one", Started: true, State: TaskRunning, Label: "Anonymous"})
	before := out.String()
	for _, e := range []TaskProgressEvent{
		{TaskID: "one", RunID: "r1", State: TaskCompleted},
		{TaskID: "one", RunID: "r2", State: TaskCompleted},
		{TaskID: "one", RunID: "r1", Started: true, State: TaskRunning, Label: "STALE"},
		{TaskID: "one", RunID: "r2", Started: true, State: TaskRunning, Label: "STALE"},
	} {
		r.Handle(e)
	}
	if out.String() != before {
		t.Fatal("older run took anonymous generation: " + out.String())
	}
	r.Handle(TaskProgressEvent{TaskID: "one", RunID: "r3", State: TaskRunning, ToolName: "Bash"})
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskCompleted, Update: true})
	if !strings.HasSuffix(out.String(), "Anonymous — completed (tool: Bash)\n") {
		t.Fatal("newer name lost current context: " + out.String())
	}
	r.Handle(TaskProgressEvent{TaskID: "one", RunID: "r4", State: TaskRunning, Label: "Forward"})
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskFailed, Update: true})
	if !strings.HasSuffix(out.String(), "Forward — failed\n") {
		t.Fatal("newer unseen name not adopted: " + out.String())
	}
}

func TestTaskRunOrderPlainStringAndKnownOldRuns(t *testing.T) {
	for _, tc := range []struct{ old, current string }{
		{"r1", "r2"}, {"r10", "r2"}, {"r", "r1"}, {"\U00010000", "\ue000"},
	} {
		t.Run(tc.current, func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: tc.old, Started: true, State: TaskRunning, Label: "Old"})
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: tc.current, Started: true, State: TaskRunning, Label: "Current"})
			before := out.String()
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: tc.old, State: TaskCompleted})
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: tc.old, State: TaskRunning, Label: "STALE"})
			if out.String() != before {
				t.Fatal("known superseded run rendered: " + out.String())
			}
			r.Handle(TaskProgressEvent{TaskID: "one", State: TaskFailed, Update: true})
			if !strings.HasSuffix(out.String(), "Current — failed\n") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestTaskRunOrderUnsafeIDsNeedExplicitStart(t *testing.T) {
	for _, id := range []string{"opaque-run-order-secret-for-test", strings.Repeat("z", maxTaskRunOrderBytes+1), "bad\nrun", string([]byte{0xff})} {
		t.Run(id[:min(len(id), 24)], func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			r.RedactProgress = func(s string) string { return strings.ReplaceAll(s, "opaque-run-order-secret-for-test", "[REDACTED]") }
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: "r2", Started: true, State: TaskRunning, Label: "Current"})
			before := out.String()
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: id, State: TaskCompleted})
			if out.String() != before {
				t.Fatal("unorderable unseen frame replaced current: " + out.String())
			}
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: id, Started: true, State: TaskRunning, Label: "Explicit"})
			for _, run := range r.progress.taskProgressRuns {
				if run.order != "" {
					t.Fatalf("retained unsafe order text: %q", run.order)
				}
			}
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: "r3", State: TaskCompleted})
			r.Handle(TaskProgressEvent{TaskID: "one", State: TaskFailed, Update: true})
			if !strings.HasSuffix(out.String(), "Explicit — failed\n") {
				t.Fatal("unorderable state stole implicit updates: " + out.String())
			}
			r.Handle(TaskProgressEvent{TaskID: "one", Started: true, Hidden: true, State: TaskRunning, Label: "PRIVATE"})
			before = out.String()
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: id, Started: true, State: TaskRunning, Label: "STALE"})
			r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "PRIVATE"})
			if out.String() != before {
				t.Fatal("unorderable prior name exposed anonymous generation: " + out.String())
			}

		})
	}
}

func TestClaudeTaskLateStartCannotExposeAnonymousRestart(t *testing.T) {
	stream := `{"type":"system","subtype":"task_started","task_id":"t1","run_id":"r2","description":"Visible prior"}
{"type":"system","subtype":"task_started","task_id":"t1","ambient":true,"description":"PRIVATE"}
{"type":"system","subtype":"task_started","task_id":"t1","run_id":"r2","description":"STALE"}
{"type":"system","subtype":"task_progress","task_id":"t1","description":"PRIVATE"}
{"type":"system","subtype":"task_progress","task_id":"t1","run_id":"r3","description":"PRIVATE"}
{"type":"system","subtype":"task_notification","task_id":"t1","status":"completed"}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "STALE") || strings.Contains(out.String(), "PRIVATE") || strings.Count(out.String(), "Task t1") != 1 {
		t.Fatal("old start exposed anonymous run: " + out.String())
	}
}
