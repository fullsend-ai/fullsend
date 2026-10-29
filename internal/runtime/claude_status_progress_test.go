package runtime

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Shapes below are reduced from the embedded SDK schemas/emitter in Claude
// 2.1.312. Raw output/prompt/result fields are included to prove exclusion.
func TestClaudeStatusProgressShapes(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"hook start", `{"type":"system","subtype":"hook_started","hook_id":"h1","hook_name":"guard","hook_event":"PreToolUse"}`, "Hook guard [PreToolUse] — running"},
		{"hook progress", `{"type":"system","subtype":"hook_progress","hook_id":"h1","hook_name":"guard","stdout":"PRIVATE","stderr":"PRIVATE","output":"PRIVATE"}`, "Hook guard — running"},
		{"hook success", `{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"guard","outcome":"success","exit_code":0,"output":"PRIVATE"}`, "completed: exit 0"},
		{"hook failed", `{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"guard","outcome":"error","exit_code":2}`, "failed: exit 2"},
		{"hook cancelled", `{"type":"system","subtype":"hook_response","hook_id":"h1","outcome":"cancelled"}`, "stopped"},
		{"summary", `{"type":"system","subtype":"task_summary","detail":"Refining strategy"}`, "Task summary: Refining strategy"},
		{"older summary", `{"type":"system","subtype":"task_summary","summary":"Reviewing change"}`, "Task summary: Reviewing change"},
		{"notice", `{"type":"system","subtype":"informational","level":"notice","content":"Retrying a phase"}`, "Claude notice: Retrying a phase"},
		{"suggestion", `{"type":"system","subtype":"informational","level":"suggestion","content":"Check access"}`, "Claude suggestion: Check access"},
		{"stopped continuation", `{"type":"system","subtype":"informational","level":"warning","content":"Hook blocked continuation","prevent_continuation":true}`, "continuation stopped: Hook blocked continuation"},
		{"notification", `{"type":"system","subtype":"notification","key":"budget","text":"Budget nearly used","priority":"high"}`, "Claude notification — high: Budget nearly used"},
		{"compacting", `{"type":"system","subtype":"status","status":"compacting","compact_output_chars":27}`, "Claude status — compacting"},
		{"compaction failure", `{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"provider refused"}`, "compaction failed: provider refused"},
		{"session running", `{"type":"system","subtype":"session_state_changed","state":"running"}`, "Session — running"},
		{"session idle", `{"type":"system","subtype":"session_state_changed","state":"idle"}`, "Session — idle"},
		{"requires action", `{"type":"system","subtype":"session_state_changed","state":"requires_action"}`, "Session — requires action"},
		{"permission", `{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"t1","agent_id":"a1","decision_reason":"policy denied","message":"PRIVATE tool result"}`, "Permission denied: Bash / agent a1: policy denied"},
		{"background membership", `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"one"},{"task_id":"ambient","ambient":true},null,42,{"task_id":""}]}`, "Background tasks: 1 reported (membership only)"},
		{"empty membership", `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`, "Background tasks: 0 reported (membership only)"},
		{"rate warning", `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","resetsAt":1700000000}}`, "Rate limit — approaching limit: five_hour; resets 2023-11-14T22:13:20Z"},
		{"rate rejected", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`, "Rate limit — rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := newTestRenderer(&out)
			if err := parseClaudeStream(strings.NewReader(tc.line+"\n"), r.Handle); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q in %q", tc.want, out.String())
			}
			if strings.Contains(out.String(), "PRIVATE") {
				t.Fatal("private output exposed")
			}
		})
	}
}

func TestClaudeStatusIgnoresNonDisplayAndMalformed(t *testing.T) {
	lines := []string{
		`{broken`,
		`{"type":"system","subtype":"hook_started"}`,
		`{"type":"system","subtype":"hook_response","hook_id":"one","outcome":"future"}`,
		`{"type":"system","subtype":"informational","level":"info","content":"transcript only"}`,
		`{"type":"system","subtype":"informational","level":"warning","content":" "}`,
		`{"type":"system","subtype":"notification","text":" "}`,
		`{"type":"system","subtype":"task_summary","detail":null}`,
		`{"type":"system","subtype":"status","status":null}`,
		`{"type":"system","subtype":"status","status":"future"}`,
		`{"type":"system","subtype":"status","status":"requesting"}`,
		`{"type":"system","subtype":"session_state_changed","state":"future"}`,
		`{"type":"system","subtype":"permission_denied"}`,
		`{"type":"system","subtype":"background_tasks_changed"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":null}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":42}`,
		`{"type":"system","subtype":"future_display","content":"PRIVATE"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"future"}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"rate_limit_event","rate_limit_info":42}`,
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(strings.Join(lines, "\n")+"\n"), r.Handle); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output: %q", out.String())
	}
	if _, ok := decodeClaudeStatusProgress([]byte(`{"hook_id":42}`), "hook_started"); ok {
		t.Fatal("invalid fields accepted")
	}
	if _, ok := decodeClaudeStatusProgress([]byte(`{}`), "future"); ok {
		t.Fatal("unknown subtype accepted")
	}
	if _, ok := decodeClaudeRateLimit([]byte(`{broken`)); ok {
		t.Fatal("invalid rate event accepted")
	}
}

func TestClaudeStatusTransitionsAndDedup(t *testing.T) {
	stream := `{"type":"system","subtype":"status","status":"compacting"}
{"type":"system","subtype":"status","status":"compacting","compact_output_chars":500}
{"type":"system","subtype":"status","status":null}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"system","subtype":"task_summary","detail":"Reviewing"}
{"type":"system","subtype":"task_summary","detail":"Reviewing"}
{"type":"system","subtype":"task_summary","detail":null}
{"type":"system","subtype":"task_summary","detail":"Reviewing"}
`
	var raw, out bytes.Buffer
	metrics := &RunMetrics{}
	r := newTestRenderer(&out)
	err := parseClaudeStream(io.TeeReader(strings.NewReader(stream), &raw), func(e AgentEvent) { recordClaudeMetrics(metrics, e); r.Handle(e) })
	if err != nil {
		t.Fatal(err)
	}
	for text, count := range map[string]int{"compacting": 1, "ready": 1, "rejected": 1, "allowed": 1, "Reviewing": 2} {
		if got := strings.Count(out.String(), text); got != count {
			t.Errorf("%s count=%d want=%d: %s", text, got, count, out.String())
		}
	}
	if raw.String() != stream {
		t.Fatal("raw events changed")
	}
	if metrics.InputTokens != 0 || metrics.OutputTokens != 0 || metrics.ToolCalls.Load() != 0 || metrics.TotalCostUSD != 0 {
		t.Fatal("advisories altered metrics")
	}
}
