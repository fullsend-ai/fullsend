package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestWorkflowReviewTerminalNotificationKeepsContext(t *testing.T) {
	for _, state := range []string{"completed", "failed", "stopped"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", "true")
			var out bytes.Buffer
			r := newTestRenderer(&out)
			stream := fmt.Sprintf(`{"type":"system","subtype":"task_started","task_id":"one","description":"Work"}
{"type":"system","subtype":"task_progress","task_id":"one","description":"Work","last_tool_name":"Bash"}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":%q,"error":"context retained"}}
{"type":"system","subtype":"task_notification","task_id":"one","status":%q,"summary":"PRIVATE"}
`, state, state)
			stderr := captureStderr(t, func() {
				if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
					t.Fatal(err)
				}
			})
			if strings.Count(out.String(), "— "+state) != 1 || strings.Count(stderr, "— "+state) != 1 {
				t.Fatalf("terminal message duplicated: %s", out.String())
			}
			if !strings.Contains(out.String(), "context retained") || strings.Contains(out.String(), "PRIVATE") {
				t.Fatal(out.String())
			}
			// A supplied worker-restart cause is new information and must replace
			// the prior detail, rather than being swallowed by the partial merge.
			if state == "stopped" {
				r.isCI = false
				if err := parseClaudeStream(strings.NewReader(`{"type":"system","subtype":"task_notification","task_id":"one","status":"stopped","reason":"worker_restart"}`+"\n"), r.Handle); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "worker restarted") {
					t.Fatal(out.String())
				}
			}
		})
	}
}

func TestWorkflowReviewUnkeyedWarningsAreOccurrences(t *testing.T) {
	for _, subtype := range []string{"informational", "permission_denied", "notification"} {
		t.Run(subtype, func(t *testing.T) {
			line := fmt.Sprintf(`{"type":"system","subtype":%q,"level":"warning","content":"Retry required","tool_name":"Bash","decision_reason":"Retry required","text":"Retry required"}`, subtype) + "\n"
			var out bytes.Buffer
			r := newTestRenderer(&out)
			if err := parseClaudeStream(strings.NewReader(line+line), r.Handle); err != nil {
				t.Fatal(err)
			}
			if strings.Count(out.String(), "Retry required") != 2 {
				t.Fatalf("lost repeated %s: %s", subtype, out.String())
			}
		})
	}
}

func TestWorkflowReviewRequestCyclesStayQuiet(t *testing.T) {
	cycle := `{"type":"system","subtype":"status","status":"requesting"}
{"type":"system","subtype":"status","status":null}
`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(strings.Repeat(cycle, 20)), r.Handle); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("per-request status chatter: " + out.String())
	}
	if err := parseClaudeStream(strings.NewReader(`{"type":"system","subtype":"status","status":"compacting"}`+"\n"+cycle+cycle), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "compacting") != 1 || strings.Count(out.String(), "ready") != 1 {
		t.Fatal(out.String())
	}
}

func TestWorkflowReviewRunIDPromotionPreservesHiddenAndTerminal(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprint(hidden), func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			stream := fmt.Sprintf(`{"type":"system","subtype":"task_started","task_id":"one","description":"Initial","ambient":%t}
{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"completed"}}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"run1","description":"STALE"}
{"type":"system","subtype":"task_notification","task_id":"one","run_id":"run1","status":"completed"}
`, hidden)
			if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "STALE") {
				t.Fatal("promotion lost terminal guard: " + out.String())
			}
			if hidden && out.Len() != 0 {
				t.Fatal("promotion exposed ambient task: " + out.String())
			}
			if !hidden && strings.Count(out.String(), "completed") != 1 {
				t.Fatal("promotion lost terminal identity: " + out.String())
			}
		})
	}
}

func TestWorkflowReviewNewRunWithoutStartReceivesOptionalCompletion(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	stream := `{"type":"system","subtype":"task_started","task_id":"one","run_id":"old","description":"Old"}
{"type":"system","subtype":"task_notification","task_id":"one","run_id":"old","status":"completed"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"new","description":"New"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"old","description":"LATE OLD"}
{"type":"system","subtype":"task_notification","task_id":"one","status":"completed"}
`
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "New — completed") || strings.Contains(out.String(), "LATE OLD") {
		t.Fatal(out.String())
	}
}

func TestWorkflowReviewSubtypeFieldsDecodeIndependently(t *testing.T) {
	line := `{"type":"system","subtype":"informational","level":"warning","content":"Still useful","hook_id":42,"status":{},"tasks":false}`
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(line+"\n"), r.Handle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Still useful") {
		t.Fatal("unrelated optional fields discarded notice")
	}
}

func TestWorkflowReviewNewStartResetsAgents(t *testing.T) {
	for _, run := range []string{"", ` ,"run_id":"run1"`} {
		t.Run(run, func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			started := `{"type":"system","subtype":"task_started","task_id":"one"` + run + `}` + "\n"
			finished := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"done","label":"Read"}]}` + "\n"
			restart := `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_agent","index":1,"state":"start","label":"Fresh agent"}]}` + "\n"
			if err := parseClaudeStream(strings.NewReader(started+finished+started+restart), r.Handle); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Fresh agent — running") {
				t.Fatal("new start did not reset agent guard: " + out.String())
			}
		})
	}
}

func TestWorkflowReviewRestartWithoutRunIDKeepsOldRunHistory(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	stream := `{"type":"system","subtype":"task_started","task_id":"one","description":"Old"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"A","description":"Old"}
{"type":"system","subtype":"task_notification","task_id":"one","run_id":"A","status":"completed"}
{"type":"system","subtype":"task_started","task_id":"one","description":"New"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"A","description":"STALE"}
{"type":"system","subtype":"task_notification","task_id":"one","run_id":"A","status":"completed"}
{"type":"system","subtype":"task_progress","task_id":"one","description":"Current work"}
{"type":"system","subtype":"task_progress","task_id":"one","run_id":"B","description":"Current work"}
{"type":"system","subtype":"task_notification","task_id":"one","status":"completed"}
`
	if err := parseClaudeStream(strings.NewReader(stream), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "STALE") || strings.Count(out.String(), "completed") != 2 || strings.Count(out.String(), "Current work — running") != 1 || !strings.Contains(out.String(), "Current work — completed") {
		t.Fatal("old run stole restarted task: " + out.String())
	}
}
