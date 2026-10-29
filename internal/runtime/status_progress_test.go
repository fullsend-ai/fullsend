package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestStatusProgressRendererSafetyAndBounds(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	var out bytes.Buffer
	r := newTestRenderer(&out)
	const secret = "opaque-status-secret-for-test"
	r.RedactProgress = func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") }
	e := StatusProgressEvent{Kind: "notification", ID: "one", Label: secret + "\n::error::", State: "\x1b[31mwarning\x1b[0m", Detail: secret + strings.Repeat("界", 500)}
	stderr := captureStderr(t, func() { r.Handle(TextEvent{Text: "before"}); r.Handle(e); r.Handle(e) })
	if strings.Contains(out.String(), secret) || strings.Contains(stderr, secret) || strings.Contains(stderr, "::error::") || strings.Contains(out.String(), "\x1b") {
		t.Fatal("unsafe status display")
	}
	if strings.Count(stderr, "::notice::") != 1 || !strings.Contains(out.String(), "before\n") {
		t.Fatal("bad CI/block boundary")
	}
	if len([]rune(out.String())) > 500 {
		t.Fatal("unbounded status")
	}
	r.Handle(StatusProgressEvent{})
	r.Handle(StatusProgressEvent{Kind: "empty"})
	r.isCI = false
	for i := range maxTaskProgressEntries + 1 {
		r.Handle(StatusProgressEvent{Kind: "row", ID: fmt.Sprint(i), Label: "row"})
	}
	if len(r.statusProgressSeen) > maxTaskProgressEntries {
		t.Fatal("unbounded status cache")
	}
}

func TestWorkflowReviewJoiningFieldsCannotRecreateCommands(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "task:", Label: "label", State: TaskRunning})
	r.Handle(StatusProgressEvent{Kind: "warning", Label: "warning:", Detail: "detail"})
	if strings.Contains(out.String(), "::") {
		t.Fatal("field joins recreated command delimiter: " + out.String())
	}
}

func TestStatusProgressAttributionRedactedBeforeRetention(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	const secret = "opaque-hook-attribution-secret-for-test"
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.RedactProgress = func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") }
	stderr := captureStderr(t, func() {
		r.Handle(StatusProgressEvent{Kind: "hook", ID: "one", Label: "Hook", State: "running", Subject: secret + strings.Repeat("界", 200), Context: secret + "\n::error::", SubjectSet: true, ContextSet: true})
		r.Handle(StatusProgressEvent{Kind: "hook", ID: "one", Label: "Hook", State: "completed"})
	})
	if strings.Contains(out.String(), secret) || strings.Contains(stderr, secret) || strings.Contains(stderr, "::error::") || strings.Count(stderr, "::notice::") != 2 {
		t.Fatal("unsafe hook attribution")
	}
	for _, display := range r.statusProgressSeen {
		if strings.Contains(display.subject+display.context+display.message, secret) || len([]rune(display.subject)) > 121 || len([]rune(display.context)) > 81 {
			t.Fatal("unsafe or unbounded cached attribution")
		}
	}
	if strings.Count(out.String(), "[REDACTED]") != 4 {
		t.Fatal("sparse completion lost redacted attribution: " + out.String())
	}
}
