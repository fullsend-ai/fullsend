package runtime

import (
	"bytes"
	"strings"
	"testing"
)

func TestClaudeReviewHookCompletionRetainsIdentity(t *testing.T) {
	stream := `{"type":"system","subtype":"hook_started","hook_id":"guard-1","hook_name":"guard","hook_event":"PreToolUse"}
{"type":"system","subtype":"hook_progress","hook_id":"guard-1","stdout":"PRIVATE"}
{"type":"system","subtype":"hook_response","hook_id":"guard-1","outcome":"success","exit_code":0,"output":"PRIVATE"}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Hook guard [PreToolUse]") != 2 || strings.Count(out.String(), "running") != 1 || !strings.Contains(out.String(), "Hook guard [PreToolUse] — completed: exit 0") || strings.Contains(out.String(), "PRIVATE") {
		t.Fatal(out.String())
	}
}

func TestClaudeReviewAgentCounterSnapshotRetainsContext(t *testing.T) {
	detailed := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"error","label":"Create","phaseTitle":"Build","lastToolName":"Bash","lastToolSummary":"run checks","error":"access refused","blocked":true,"cached":true}]}` + "\n"
	sparse := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"error","tokens":20,"toolCalls":3}]}` + "\n"
	clear := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"error","label":"","phaseTitle":"","lastToolName":"","lastToolSummary":"","error":"","blocked":false,"cached":false}]}` + "\n"
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(detailed+sparse), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Task one") != 1 {
		t.Fatal("counter update erased context: " + out.String())
	}
	for _, want := range []string{"[Build]", "Create", "Bash: run checks", "access refused", "(blocked)", "(cached)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
	if err := parseClaudeStream(strings.NewReader(clear+sparse), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Task one") != 2 || !strings.HasSuffix(out.String(), "Task one / agent #1 — failed\n") {
		t.Fatal("explicit clears not retained: " + out.String())
	}
}

func TestClaudeReviewDistinctRunDoesNotInheritHiddenParent(t *testing.T) {
	stream := `{"type":"system","subtype":"task_started","task_id":"one","run_id":"r1","ambient":true,"description":"PRIVATE"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"r2","workflow_progress":[{"type":"workflow_agent","index":1,"state":"done","label":"New worker"}]}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"r2","description":"Visible new run"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"r1","description":"PRIVATE"}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "New worker — completed") || !strings.Contains(out.String(), "Visible new run — running") || strings.Contains(out.String(), "PRIVATE") {
		t.Fatal(out.String())
	}
}

func TestClaudeReviewAgentToolChangeDoesNotKeepOldSummary(t *testing.T) {
	stream := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","lastToolName":"Bash","lastToolSummary":"OLD COMMAND"}]}
{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"progress","lastToolName":"Read"}]}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(tool: Read)") || strings.Contains(out.String(), "Read: OLD COMMAND") {
		t.Fatal(out.String())
	}
}

func TestClaudeReviewHookIdentityExplicitClear(t *testing.T) {
	stream := `{"type":"system","subtype":"hook_started","hook_id":"one","hook_name":"guard","hook_event":"PreToolUse"}
{"type":"system","subtype":"hook_progress","hook_id":"one","hook_name":"","hook_event":""}
{"type":"system","subtype":"hook_response","hook_id":"one","hook_name":null,"hook_event":null,"outcome":"success"}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "guard") != 1 || !strings.HasSuffix(out.String(), "Hook — completed\n") {
		t.Fatal(out.String())
	}
}

func TestClaudeReviewAgentRecoveryClearsTransientMetadata(t *testing.T) {
	stream := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","startedAt":100,"attempt":2,"lastAttemptReason":"throttled","label":"Create","error":"rate limited","blocked":true,"cached":true}]}
{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"progress","startedAt":100,"attempt":2,"label":"Create","lastToolName":"Bash"}]}
{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"done","startedAt":100,"attempt":2,"label":"Create"}]}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	for _, transient := range []string{"(blocked)", "(cached)", "rate limited"} {
		if strings.Count(out.String(), transient) != 1 {
			t.Fatalf("%s survived recovery: %s", transient, out.String())
		}
	}
	if !strings.Contains(out.String(), "Create — running (tool: Bash)") || !strings.Contains(out.String(), "Create — completed (tool: Bash)") {
		t.Fatal(out.String())
	}
}

// The internal 2.1.312 producer keys Workflow entries by type:index. This
// reduced sequence models its queued/start/done shapes; it is not a newly
// recorded live 2.1.312 run.
func TestClaudeReviewLateAgentIDUsesOneIndexedRow(t *testing.T) {
	stream := `{"type":"system","subtype":"task_progress","task_id":"wf","run_id":"run","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","queuedAt":100,"label":"read","phaseTitle":"Read"}]}
{"type":"system","subtype":"task_progress","task_id":"wf","run_id":"run","workflow_progress":[{"type":"workflow_agent","index":1,"agentId":"a1","state":"start","queuedAt":100,"startedAt":101,"attempt":1,"label":"read","phaseTitle":"Read"}]}
{"type":"system","subtype":"task_progress","task_id":"wf","run_id":"run","workflow_progress":[{"type":"workflow_agent","index":1,"state":"done","queuedAt":100,"startedAt":101,"attempt":1,"label":"read","phaseTitle":"Read"}]}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "— running") != 1 || strings.Count(out.String(), "— completed") != 1 || len(r.progress.taskProgressSeen) != 1 {
		t.Fatal("late ID split agent row: " + out.String())
	}
	for _, row := range r.progress.taskProgressSeen {
		if row.event.AgentID != "a1" || row.event.State != TaskCompleted || row.event.AgentIndex != 1 {
			t.Fatalf("lost agent identity or completion: %+v", row.event)
		}
	}
	if !strings.Contains(out.String(), "a1") {
		t.Fatal("known agent ID missing from completion display")
	}
}
