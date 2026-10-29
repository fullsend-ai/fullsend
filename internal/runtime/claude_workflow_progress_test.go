package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Reduced from the Claude 2.1.296 stream attached to #8908. The phase
// inventory is static; the agent transitions and description carry progress.
const claudeWorkflowProgressFixture = `{"type":"system","subtype":"task_started","task_id":"wf-example","run_id":"run-example","task_type":"local_workflow","description":"Strategy pipeline"}
{"type":"system","subtype":"task_progress","task_id":"wf-example","run_id":"run-example","description":"Read: read input","usage":{"total_tokens":0,"tool_uses":0},"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Read"},{"type":"workflow_phase","index":2,"title":"Create"},{"type":"workflow_phase","index":3,"title":"Not reached"},{"type":"workflow_agent","index":1,"agentId":"reader","label":"read input","phaseTitle":"Read","state":"start"}]}
{"type":"system","subtype":"task_progress","task_id":"wf-example","run_id":"run-example","description":"Create: strategy-create","usage":{"total_tokens":14434,"tool_uses":2},"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Read"},{"type":"workflow_phase","index":2,"title":"Create"},{"type":"workflow_phase","index":3,"title":"Not reached"},{"type":"workflow_agent","index":1,"agentId":"reader","label":"read input","phaseTitle":"Read","state":"done","tokens":14434,"toolCalls":2,"durationMs":7194},{"type":"workflow_agent","index":2,"agentId":"creator","label":"strategy-create","phaseTitle":"Create","state":"start","lastToolName":"Bash","lastToolSummary":"python3 create.py"}]}
{"type":"system","subtype":"task_updated","task_id":"wf-example","run_id":"run-example","patch":{"status":"completed"}}
{"type":"system","subtype":"task_notification","task_id":"wf-example","run_id":"run-example","status":"completed","summary":"Strategy pipeline complete"}
`

func TestClaudeWorkflowProgressConsole(t *testing.T) {
	var raw, console bytes.Buffer
	metrics := &RunMetrics{}
	err := progressParser(io.TeeReader(strings.NewReader(claudeWorkflowProgressFixture), &raw), ui.New(&console), metrics)
	if err != nil {
		t.Fatal(err)
	}
	output := console.String()
	for _, want := range []string{"wf-example", "Read", "read input", "Create", "strategy-create", "Bash", "completed"} {
		if !strings.Contains(output, want) {
			t.Errorf("console missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "Not reached") {
		t.Error("predeclared phase must not be rendered as live progress")
	}
	if raw.String() != claudeWorkflowProgressFixture {
		t.Error("raw stream capture changed")
	}
	if metrics.InputTokens != 0 || metrics.OutputTokens != 0 || metrics.ToolCalls.Load() != 0 || metrics.TotalCostUSD != 0 {
		t.Errorf("task progress must not change usage accounting: %+v", metrics)
	}
}

func TestClaudeTaskLifecycle(t *testing.T) {
	tests := []struct {
		name, line string
		want       TaskState
	}{
		{"start", `{"type":"system","subtype":"task_started","task_id":"one","description":"Pipeline"}`, TaskRunning},
		{"progress without snapshot", `{"type":"system","subtype":"task_progress","task_id":"one","description":"Read: input"}`, TaskRunning},
		{"completed update", `{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"completed"}}`, TaskCompleted},
		{"failed notification", `{"type":"system","subtype":"task_notification","task_id":"one","status":"failed","summary":"DO NOT PRINT full result"}`, TaskFailed},
		{"stopped update", `{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":"killed"}}`, TaskStopped},
		{"top level status", `{"type":"system","subtype":"task_updated","task_id":"one","status":"running"}`, TaskRunning},
		{"unknown subtype", `{"type":"system","subtype":"future_task_event","task_id":"one","status":"completed"}`, ""},
		{"unknown state", `{"type":"system","subtype":"task_notification","task_id":"one","status":"future-state"}`, ""},
		{"empty id", `{"type":"system","subtype":"task_progress","description":"input"}`, ""},
		{"blank id", `{"type":"system","subtype":"task_started","task_id":"  "}`, ""},
		{"invalid id", `{"type":"system","subtype":"task_started","task_id":42}`, ""},
		{"broken patch", `{"type":"system","subtype":"task_updated","task_id":"one","patch":[]}`, ""},
		{"nonstatus patch", `{"type":"system","subtype":"task_updated","task_id":"one","patch":{"elapsed":1}}`, ""},
		{"wrong status type", `{"type":"system","subtype":"task_updated","task_id":"one","patch":{"status":42}}`, ""},
		{"snapshot only static", `{"type":"system","subtype":"task_progress","task_id":"one","workflow_progress":[{"type":"workflow_phase","index":1,"title":"Not started"}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []AgentEvent
			if err := parseClaudeStream(strings.NewReader(tt.line+"\n"), func(e AgentEvent) { events = append(events, e) }); err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if len(events) != 0 {
					t.Fatalf("unexpected events: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("want one event, got %+v", events)
			}
			e, ok := events[0].(TaskProgressEvent)
			if !ok || e.TaskID != "one" || e.State != tt.want {
				t.Fatalf("wrong event: %+v", events[0])
			}
			if strings.Contains(e.Label, "DO NOT PRINT") {
				t.Fatal("notification result summary must not become progress text")
			}
		})
	}
	if got := decodeClaudeTaskProgress([]byte(`{broken`), "task_progress"); len(got) != 0 {
		t.Fatalf("malformed JSON yielded events: %+v", got)
	}
}

func TestClaudeWorkflowProgressToleratesMalformedEntries(t *testing.T) {
	line := `{"type":"system","subtype":"task_progress","task_id":"task-one","run_id":"run-one","workflow_progress":[null,42,{"type":"workflow_phase","title":"Not started"},{"type":"workflow_agent","state":"start"},{"type":"workflow_agent","index":4,"state":"future-state"},{"type":"workflow_agent","index":3,"state":"done","label":"first","phaseTitle":"Read","lastToolName":"Read","lastToolSummary":"input.json"},{"type":"workflow_agent","agentId":"child","state":"queued","label":"second"}]}`
	got := decodeClaudeTaskProgress([]byte(line), "task_progress")
	want := []TaskProgressEvent{
		{TaskID: "task-one", RunID: "run-one", AgentIndex: 3, Label: "first", Phase: "Read", State: TaskCompleted, ToolName: "Read", ToolSummary: "input.json"},
		{TaskID: "task-one", RunID: "run-one", AgentID: "child", Label: "second", State: TaskQueued},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for _, snapshot := range []string{`{}`, `"wrong"`, `null`} {
		line := `{"task_id":"one","description":"still running","workflow_progress":` + snapshot + `}`
		got := decodeClaudeTaskProgress([]byte(line), "task_progress")
		if len(got) != 1 || got[0].Label != "still running" {
			t.Errorf("bad optional snapshot must not drop task description: %+v", got)
		}
	}
}

func TestClaudeTaskStates(t *testing.T) {
	for _, pair := range []struct {
		wire string
		want TaskState
	}{
		{"start", TaskRunning}, {"started", TaskRunning}, {"progress", TaskRunning}, {"running", TaskRunning},
		{"done", TaskCompleted}, {"completed", TaskCompleted}, {"error", TaskFailed}, {"failed", TaskFailed},
		{"killed", TaskStopped}, {"stopped", TaskStopped}, {"cancelled", TaskStopped}, {"canceled", TaskStopped},
		{"queued", TaskQueued}, {"retrying", TaskRetrying}, {"skipped", TaskSkipped}, {"unknown", ""},
	} {
		if got := claudeTaskState(pair.wire); got != pair.want {
			t.Errorf("%s = %s, want %s", pair.wire, got, pair.want)
		}
	}
}

func TestClaudeWorkflowProgressMetricsUnchanged(t *testing.T) {
	// Task usage is an aggregate snapshot of overlapping/nested agents, not
	// another incremental TokensEvent or ResultEvent to add to the counters.
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.25,"usage":{"input_tokens":11,"output_tokens":22,"cache_read_input_tokens":33,"cache_creation_input_tokens":44}}` + "\n"
	var metrics RunMetrics
	err := parseClaudeStream(strings.NewReader(claudeWorkflowProgressFixture+line+claudeWorkflowProgressFixture), func(e AgentEvent) {
		recordClaudeMetrics(&metrics, e)
	})
	if err != nil {
		t.Fatal(err)
	}
	if metrics.NumTurns != 3 || metrics.TotalCostUSD != 0.25 || metrics.InputTokens != 11 ||
		metrics.OutputTokens != 22 || metrics.CacheReadInputTokens != 33 || metrics.CacheCreationInputTokens != 44 || metrics.ToolCalls.Load() != 0 {
		t.Fatalf("progress altered authoritative accounting: %+v", &metrics)
	}
}

func TestTaskProgressRendererDeduplicates(t *testing.T) {
	var console bytes.Buffer
	r := newTestRenderer(&console)
	e := TaskProgressEvent{TaskID: "task-one", RunID: "run-one", AgentID: "child", Label: "refine", Phase: "Refine", State: TaskRunning, ToolName: "Read"}
	for range 135 {
		r.Handle(e)
	}
	if lines := strings.Count(console.String(), "Task task-one"); lines != 1 {
		t.Fatalf("repeated snapshots rendered %d times", lines)
	}
	e.ToolName = "Bash"
	r.Handle(e)
	e.State = TaskCompleted
	r.Handle(e)
	r.Handle(e)
	if lines := strings.Count(console.String(), "Task task-one"); lines != 3 {
		t.Fatalf("tool/state transitions should render exactly once: %s", console.String())
	}
}

func TestTaskProgressRendererIdentityAndLifecycle(t *testing.T) {
	var console bytes.Buffer
	r := newTestRenderer(&console)
	for _, e := range []TaskProgressEvent{
		{TaskID: "one", RunID: "first-run", AgentID: "worker", Label: "review", State: TaskRunning},
		{TaskID: "two", AgentID: "worker", Label: "review", State: TaskRunning},
		{TaskID: "one", AgentID: "different", Label: "review", State: TaskRunning},
		{TaskID: "one", AgentIndex: 1, Label: "review", State: TaskQueued},
		{TaskID: "one", AgentIndex: 2, Label: "review", State: TaskQueued},
		{TaskID: "one", AgentID: "worker", RunID: "another-run", Label: "review", State: TaskRunning},
		{TaskID: "one", State: TaskFailed},
		{TaskID: "two", State: TaskStopped},
	} {
		r.Handle(e)
	}
	if lines := strings.Count(console.String(), "Task "); lines != 8 {
		t.Fatalf("concurrent identities/statuses were lost: %s", console.String())
	}
	for _, want := range []string{"agent #1", "agent #2", "failed", "stopped"} {
		if !strings.Contains(console.String(), want) {
			t.Errorf("missing %q: %s", want, console.String())
		}
	}
	before := console.String()
	r.Handle(TaskProgressEvent{State: TaskRunning})
	r.Handle(TaskProgressEvent{TaskID: "one"})
	if console.String() != before {
		t.Error("incomplete events must not render")
	}
}

func TestTaskProgressRendererBoundedState(t *testing.T) {
	var console bytes.Buffer
	r := newTestRenderer(&console)
	for i := range maxTaskProgressEntries + 1 {
		r.Handle(TaskProgressEvent{TaskID: fmt.Sprint(i), State: TaskRunning})
	}
	if len(r.taskProgressSeen) > maxTaskProgressEntries {
		t.Fatalf("unbounded dedup state: %d", len(r.taskProgressSeen))
	}
}

func TestTaskProgressRendererRedactsAndSanitizes(t *testing.T) {
	const opaque = "opaque-progress-credential-for-test"
	security.RegisterRuntimeSecret(opaque)
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	var console bytes.Buffer
	r := newTestRenderer(&console)
	r.Handle(TaskProgressEvent{
		TaskID:  "id\n::error::" + opaque,
		AgentID: token, Phase: "\x1b[31mCreate\x1b[0m", Label: "hello%0Aworld\r\n::warning::",
		State: TaskRunning, ToolName: "Bash", ToolSummary: "token=" + token + " secret=" + opaque,
	})
	output := console.String()
	for _, forbidden := range []string{opaque, token, "\x1b", "::", "%0A", "\r"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("unsafe field %q escaped sanitization: %q", forbidden, output)
		}
	}
	if lines := strings.Count(output, "\n"); lines != 1 {
		t.Errorf("sandbox text injected extra console lines: %q", output)
	}
	long := strings.Repeat("界", 200)
	got := taskProgressText(long, 80)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 81 || !strings.HasSuffix(got, "…") {
		t.Errorf("invalid UTF-8 truncation: %q", got)
	}
	if got := taskProgressText(token, 10); strings.Contains(got, token[:10]) {
		t.Errorf("credential was truncated before redaction: %q", got)
	}
}

func TestTaskProgressRendererCIAndBlockBoundary(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	var console bytes.Buffer
	r := newTestRenderer(&console)
	r.Handle(TextEvent{Text: "before"})
	e := TaskProgressEvent{TaskID: "one", Label: "Create\n::error:: injected", State: TaskRunning}
	stderr := captureStderr(t, func() {
		r.Handle(e)
		r.Handle(e)
	})
	if strings.Count(stderr, "::notice::") != 1 || strings.Contains(stderr, "::error::") {
		t.Errorf("expected one safe CI notice: %q", stderr)
	}
	if !strings.Contains(console.String(), "before\n") {
		t.Errorf("task progress did not close the streaming block: %q", console.String())
	}
}

func TestTaskProgressRendererCallerRedaction(t *testing.T) {
	const secret = "opaque-runner-credential-for-test"
	var console bytes.Buffer
	r := newTestRenderer(&console)
	r.RedactProgress = func(text string) string {
		return strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning,
		Label: secret + strings.Repeat("界", 200), ToolName: "Bash", ToolSummary: secret})
	if strings.Contains(console.String(), secret) || !strings.Contains(console.String(), "[REDACTED]") {
		t.Errorf("caller-owned secret escaped redaction: %q", console.String())
	}
}

func TestTaskProgressDoesNotExposeExtraFields(t *testing.T) {
	var console bytes.Buffer
	line := `{"type":"system","subtype":"task_progress","task_id":"one","description":"Review","prompt":"PRIVATE PROMPT","workflow_progress":[{"type":"workflow_agent","index":1,"state":"done","label":"review","resultPreview":"PRIVATE RESULT","promptPreview":"PRIVATE PROMPT"}]}`
	if !json.Valid([]byte(line)) {
		t.Fatal("invalid test fixture")
	}
	if err := progressParser(strings.NewReader(line), ui.New(&console), &RunMetrics{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(console.String(), "PRIVATE") {
		t.Errorf("prompt/result content reached progress display: %s", console.String())
	}
}
