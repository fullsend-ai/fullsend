package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// Reduced from the inspected Claude Code 2.1.312 bundled schema/emitter,
// not a newly recorded live run. The wire state enum has only four values.
func TestClaudeWorkflowAgentProducerStates(t *testing.T) {
	cases := []struct {
		name, agent string
		want        TaskState
	}{
		{"queued", `"state":"start","queuedAt":0`, TaskQueued},
		{"started", `"state":"start","queuedAt":0,"startedAt":0,"attempt":1`, TaskRunning},
		{"retry start", `"state":"start","startedAt":20,"attempt":2,"lastAttemptReason":"stalled"`, TaskRetrying},
		{"retry progress", `"state":"progress","startedAt":20,"attempt":2,"lastAttemptReason":"stalled"`, TaskRunning},
		{"completed", `"state":"done","attempt":2`, TaskCompleted},
		{"skipped", `"state":"error","skipped":true,"error":"skipped by user"`, TaskSkipped},
		{"failed", `"state":"error","skipped":false`, TaskFailed},
		{"blocked", `"state":"error","blocked":true`, TaskFailed},
		{"older start without times", `"state":"start"`, TaskRunning},
		{"not an agent queued enum", `"state":"queued"`, ""},
		{"not an agent retry enum", `"state":"retrying"`, ""},
		{"not an agent skip enum", `"state":"skipped"`, ""},
		{"not an agent pause enum", `"state":"paused"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"label":"read",%s}]}`, tc.agent)
			events := decodeClaudeTaskProgress([]byte(raw), "task_progress")
			if tc.want == "" {
				if len(events) != 0 {
					t.Fatalf("unsupported agent enum accepted: %+v", events)
				}
				return
			}
			if len(events) != 1 || events[0].State != tc.want {
				t.Fatalf("got %+v, want state %s", events, tc.want)
			}
		})
	}
}

func TestClaudeWorkflowAgentRetriesAndSkipsRender(t *testing.T) {
	stream := `{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"label":"Read","state":"start","queuedAt":100}]}
{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a1","label":"Read","state":"start","queuedAt":100,"startedAt":101,"attempt":1}]}
{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a1","label":"Read","state":"error","queuedAt":100,"startedAt":101,"attempt":1,"error":"stalled"}]}
{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a2","label":"Read","state":"start","queuedAt":100,"startedAt":201,"attempt":2,"lastAttemptReason":"stalled"}]}
{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a2","label":"Read","state":"start","queuedAt":100,"startedAt":201,"attempt":2,"lastAttemptReason":"stalled","tokens":200}]}
{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a2","label":"Read","state":"error","queuedAt":100,"startedAt":201,"attempt":2,"lastAttemptReason":"stalled","error":"retry requested"}]}
{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a3","label":"Read","state":"start","queuedAt":100,"startedAt":301,"attempt":3,"lastAttemptReason":"user-retry"}]}
{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a3","label":"Read","state":"done","queuedAt":100,"startedAt":301,"attempt":3,"lastAttemptReason":"user-retry"},{"type":"workflow_agent","index":2,"label":"Other","state":"error","skipped":true,"error":"skipped by user"}]}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "— queued") != 1 || strings.Count(out.String(), "— retrying") != 2 {
		t.Fatal("missing queue/retry transitions: " + out.String())
	}
	for _, want := range []string{"attempt 2; retry: stalled", "attempt 3; retry: user-retry", "Other — skipped"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Other — failed") {
		t.Fatal("skip reported as failure")
	}
}

func TestClaudeWorkflowAgentQuotaWaitAndStaleSnapshots(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	for _, fields := range []string{
		`"state":"start","queuedAt":10,"startedAt":100,"attempt":1`,
		`"state":"start","queuedAt":10`, // stale initial queue
		`"state":"error","queuedAt":10,"startedAt":100,"attempt":1,"error":"quota"`,
		`"state":"start","queuedAt":200`,                            // quota wait has no attempt/startedAt
		`"state":"start","queuedAt":10,"startedAt":100,"attempt":1`, // stale running during wait
		`"state":"start","queuedAt":10`,                             // stale queue during wait
		`"state":"error","queuedAt":200,"error":"quota wait failed"`,
		`"state":"start","queuedAt":10,"startedAt":100,"attempt":1`, // stale after wait failure
		`"state":"error","queuedAt":10,"startedAt":100,"attempt":1`, // stale failure
		`"state":"start","queuedAt":200,"startedAt":300,"attempt":1`,
		`"state":"error","queuedAt":200,"startedAt":300,"attempt":1`,
		`"state":"start","queuedAt":200,"attempt":2,"lastAttemptReason":"throttled"`,
		`"state":"start","queuedAt":200,"startedAt":400,"attempt":2,"lastAttemptReason":"throttled"`,
		`"state":"done","queuedAt":200,"startedAt":300,"attempt":1`, // stale completion
		`"state":"done","queuedAt":200,"startedAt":400,"attempt":2`,
	} {
		raw := fmt.Sprintf(`{"type":"system","subtype":"task_progress","task_id":"wf","workflow_progress":[{"type":"workflow_agent","index":1,"label":"Read",%s}]}`+"\n", fields)
		if err := parseClaudeStream(strings.NewReader(raw), r.Handle); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(out.String(), "— queued") != 2 || strings.Count(out.String(), "— completed") != 1 || !strings.Contains(out.String(), "attempt 2; retry: throttled") {
		t.Fatal(out.String())
	}
	for _, row := range r.taskProgressSeen {
		if row.event.State != TaskCompleted || row.event.Attempt != 2 {
			t.Fatalf("stale attempt changed final state: %+v", row.event)
		}
	}
}

func TestClaudeWorkflowAgentRetryDoesNotReopenClosedWork(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(fmt.Sprint("parent=", parent), func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			r.Handle(TaskProgressEvent{TaskID: "wf", AgentIndex: 1, State: TaskFailed, Attempt: 1, AttemptSet: true})
			if parent {
				r.Handle(TaskProgressEvent{TaskID: "wf", State: TaskCompleted})
			} else {
				r.Handle(TaskProgressEvent{TaskID: "wf", AgentIndex: 1, State: TaskCompleted, Attempt: 1, AttemptSet: true})
			}
			before := out.String()
			r.Handle(TaskProgressEvent{TaskID: "wf", AgentIndex: 1, State: TaskRetrying, Attempt: 2, AttemptSet: true})
			if out.String() != before {
				t.Fatal("retry reopened closed work: " + out.String())
			}
		})
	}
}
