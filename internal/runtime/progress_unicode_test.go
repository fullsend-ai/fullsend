package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestProgressUnicodeControlsRemovedFromDisplayAndCaches(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	controls := "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\u200b\ufeff\U000e0061"
	label := "Visible" + controls + " café 漢字 العربية"
	var out bytes.Buffer
	r := newTestRenderer(&out)
	stderr := captureStderr(t, func() {
		r.Handle(TaskProgressEvent{TaskID: "task", RunID: "r1", AgentIndex: 1, AgentID: "worker" + controls, Label: label, Phase: label, State: TaskRunning, ToolName: label, ToolSummary: label, Summary: label, Detail: label, LastAttemptReason: label})
		r.Handle(StatusProgressEvent{Kind: "hook", ID: "one", Label: label, Subject: label, Context: label, State: label, Detail: label})
	})
	check := func(where, text string) {
		t.Helper()
		if strings.ContainsAny(text, controls) {
			t.Fatalf("unsafe Unicode in %s", where)
		}
	}
	check("console", out.String())
	check("CI", stderr)
	for _, row := range r.progress.taskProgressSeen {
		check("task cache", fmt.Sprintf("%+v", row.event))
	}
	for _, row := range r.progress.statusProgressSeen {
		check("status cache", fmt.Sprintf("%+v", row))
	}
	if !strings.Contains(out.String(), "café 漢字 العربية") {
		t.Fatal("ordinary Unicode content changed")
	}
	if got := NormalizeProgressText("：：error：： injected"); strings.Contains(got, "::") {
		t.Fatal("compatibility normalization recreated CI command")
	}
}

func TestClaudeProgressCallbacksStayRawWhileDisplayReconciles(t *testing.T) {
	label := "Working\u202e"
	raw, err := json.Marshal(map[string]any{"type": "system", "subtype": "task_progress", "task_id": "task", "description": label})
	if err != nil {
		t.Fatal(err)
	}
	stream := strings.Repeat(string(raw)+"\n", 3) + `{"type":"result","num_turns":3,"total_cost_usd":0.25}` + "\n"
	var console, capture bytes.Buffer
	r := newTestRenderer(&console)
	var labels []string
	var metrics RunMetrics
	if err := parseClaudeStream(io.TeeReader(strings.NewReader(stream), &capture), func(e AgentEvent) {
		recordClaudeMetrics(&metrics, e)
		r.Handle(e)
		if task, ok := e.(TaskProgressEvent); ok {
			labels = append(labels, task.Label)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if capture.String() != stream || len(labels) != 3 {
		t.Fatal("raw stream/event contract changed")
	}
	for _, got := range labels {
		if got != label {
			t.Fatal("renderer changed caller event")
		}
	}
	if strings.Count(console.String(), "Task task") != 1 || strings.Contains(console.String(), "\u202e") {
		t.Fatal("display did not reconcile/sanitize independently")
	}
	if metrics.NumTurns != 3 || metrics.TotalCostUSD != 0.25 {
		t.Fatal("progress changed accounting")
	}
}
