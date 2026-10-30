package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestClaudeWorkflowSparseLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, first, sparse string
		want                TaskState
	}{
		{"queued", `"state":"start","queuedAt":100`, `"state":"start","tokens":20`, TaskQueued},
		{"retry", `"state":"start","startedAt":200,"attempt":2,"lastAttemptReason":"stalled"`, `"state":"start","tokens":20`, TaskRetrying},
		{"skipped", `"state":"error","skipped":true`, `"state":"error","tokens":20`, TaskSkipped},
		{"legacy fallback", `"state":"start"`, `"state":"start","tokens":20`, TaskRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			for _, fields := range []string{tc.first, tc.sparse, tc.sparse} {
				raw := fmt.Sprintf(`{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,%s}]}`+"\n", fields)
				if err := parseClaudeStream(strings.NewReader(raw), r.Handle); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Count(out.String(), "Task wf") != 1 {
				t.Fatalf("sparse snapshot fabricated transition: %s", out.String())
			}
			for _, row := range r.taskProgressSeen {
				if row.event.State != tc.want {
					t.Fatalf("state %s, want %s", row.event.State, tc.want)
				}
			}
		})
	}
}

func TestClaudeWorkflowSparseLifecycleTransitions(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	for _, step := range []struct {
		fields string
		want   TaskState
	}{
		{`"state":"start","queuedAt":100`, TaskQueued},
		{`"state":"start","queuedAt":100,"tokens":20`, TaskQueued},
		{`"state":"start","startedAt":200,"attempt":1`, TaskRunning},
		{`"state":"progress","tokens":30`, TaskRunning},
		{`"state":"error","skipped":true`, TaskSkipped},
		{`"state":"error","tokens":40`, TaskSkipped},
		{`"state":"error","skipped":false`, TaskFailed},
		{`"state":"start","startedAt":300,"attempt":2,"lastAttemptReason":"throttled"`, TaskRetrying},
		{`"state":"start","tokens":50`, TaskRetrying},
		{`"state":"progress","tokens":60`, TaskRunning},
		{`"state":"error"`, TaskFailed},                // previous skipped flag must not leak
		{`"state":"start","queuedAt":400`, TaskQueued}, // fresh quota queue drops old startedAt
		{`"state":"start","tokens":70`, TaskQueued},
		{`"state":"start","startedAt":500,"attempt":2,"lastAttemptReason":""`, TaskRetrying},
		{`"state":"done"`, TaskCompleted},
	} {
		raw := fmt.Sprintf(`{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,%s}]}`+"\n", step.fields)
		if err := parseClaudeStream(strings.NewReader(raw), r.Handle); err != nil {
			t.Fatal(err)
		}
		for _, row := range r.taskProgressSeen {
			if row.event.State != step.want {
				t.Fatalf("%s: state %s, want %s; output: %s", step.fields, row.event.State, step.want, out.String())
			}
		}
	}
}

func TestClaudeWorkflowSparseLifecycleClosedGuards(t *testing.T) {
	for _, tc := range []struct{ name, terminal string }{
		{"parent", `{"type":"system","subtype":"task_notification","task_id":"wf","status":"completed"}`},
		{"completed agent", `{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"state":"done"}]}`},
		{"skipped agent", `{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"state":"error","skipped":true}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			parse := func(raw string) {
				t.Helper()
				if err := parseClaudeStream(strings.NewReader(raw+"\n"), r.Handle); err != nil {
					t.Fatal(err)
				}
			}
			parse(`{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","startedAt":100,"attempt":1}]}`)
			parse(tc.terminal)
			before := out.String()
			parse(`{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","startedAt":200,"attempt":2}]}`)
			if out.String() != before {
				t.Fatalf("reopened closed work: %s", out.String())
			}
		})
	}
}
