package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/security"
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
	if len(r.progress.statusProgressSeen) > maxTaskProgressEntries {
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
	for _, display := range r.progress.statusProgressSeen {
		if strings.Contains(display.subject+display.context+display.message, secret) || len([]rune(display.subject)) > 121 || len([]rune(display.context)) > 81 {
			t.Fatal("unsafe or unbounded cached attribution")
		}
	}
	if strings.Count(out.String(), "[REDACTED]") != 4 {
		t.Fatal("sparse completion lost redacted attribution: " + out.String())
	}
}

func TestStatusProgressCapacityUpdateKeepsExistingEntries(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	// Fill the status cache to capacity with distinct keys carrying subjects.
	for i := range maxStatusProgressEntries {
		r.Handle(StatusProgressEvent{Kind: "row", ID: fmt.Sprint(i), Label: "row " + fmt.Sprint(i), Subject: "subject " + fmt.Sprint(i), SubjectSet: true})
	}
	if len(r.progress.statusProgressSeen) != maxStatusProgressEntries {
		t.Fatalf("setup: want %d entries, got %d", maxStatusProgressEntries, len(r.progress.statusProgressSeen))
	}
	out.Reset()
	// Updating an existing key at capacity must not wipe the cache.
	r.Handle(StatusProgressEvent{Kind: "row", ID: "0", Label: "row 0 updated"})
	if len(r.progress.statusProgressSeen) != maxStatusProgressEntries {
		t.Fatalf("existing-key update at capacity wiped the cache: %d entries remain", len(r.progress.statusProgressSeen))
	}
	// Retained state of other keys must survive: a changed label on another
	// key still merges its retained subject, printing the new row exactly
	// once. (An unchanged resend is deduplicated and proves nothing: it
	// prints nothing whether or not the cache was wiped.)
	r.Handle(StatusProgressEvent{Kind: "row", ID: "1", Label: "row 1 changed"})
	if msg := out.String(); !strings.Contains(msg, "row 1 changed subject 1") {
		t.Fatalf("retained subject lost after capacity update: %q", msg)
	}
	if strings.Count(out.String(), "row 1 changed subject 1") != 1 {
		t.Fatalf("capacity update row printed more than once: %q", out.String())
	}
}

func TestStatusProgressJoinCannotReconstructCredential(t *testing.T) {
	// A tab-containing credential normalizes to the exact text a
	// label/subject join can produce when the credential is split across
	// fields. The joined row must be masked before display and retention.
	const secret = "opaque-join-runtime-secret \tfor-test"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	if NormalizeProgressText(secret) != "opaque-join-runtime-secret for-test" {
		t.Fatalf("setup: unexpected normalized form %q", NormalizeProgressText(secret))
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(StatusProgressEvent{Kind: "hook", ID: "one", Label: "opaque-join-runtime-secret", Subject: "for-test", SubjectSet: true})
	if strings.Contains(out.String(), "for-test") || strings.Contains(out.String(), "opaque-join-runtime-secret") {
		t.Fatalf("joined credential leaked: %q", out.String())
	}
}
