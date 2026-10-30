package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestProgressCallerRedactionAfterCleanup(t *testing.T) {
	for _, tc := range []struct{ name, secret, input string }{
		{"ANSI split", "opaque-runner-credential-for-test", "opaque-runner-\x1b[31mcredential-for-test"},
		{"OSC split", "opaque-runner-credential-for-test", "opaque-runner-\x1b]0;hidden\x07credential-for-test"},
		{"whitespace", "opaque runner credential for test", "opaque\t runner\n credential   for test"},
		{"encoded whitespace", "opaque runner credential for test", "opaque%0Arunner%0Dcredential for test"},
		{"raw whitespace literal", "opaque\trunner credential for test", "opaque\trunner credential for test"},
		{"split whitespace literal", "opaque\trunner credential for test", "opaque\trun\x1b[31mner credential for test"},
		{"split rewritten markers", "opaque::runner%0Acredential", "opaque:\x1b[31m:runner%0Acredential"},
		{"before truncation", "opaque-" + strings.Repeat("long-credential", 30), "opaque-\x1b[31m" + strings.Repeat("long-credential", 30)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", "true")
			var out bytes.Buffer
			r := newTestRenderer(&out)
			r.RedactProgress = func(s string) string {
				return strings.ReplaceAll(strings.ReplaceAll(s, tc.secret, "[REDACTED]"), NormalizeProgressText(tc.secret), "[REDACTED]")
			}
			stderr := captureStderr(t, func() {
				r.Handle(TaskProgressEvent{TaskID: "task", AgentIndex: 1, AgentID: tc.input, Label: tc.input, Phase: tc.input, State: TaskRunning, ToolName: tc.input, ToolSummary: tc.input, Summary: tc.input, Detail: tc.input, LastAttemptReason: tc.input})
				r.Handle(StatusProgressEvent{Kind: "hook", ID: "id", Label: tc.input, State: tc.input, Detail: tc.input, Subject: tc.input, Context: tc.input})
			})
			check := func(where, s string) {
				t.Helper()
				if strings.Contains(s, "opaque") || strings.Contains(s, "long-credential") {
					t.Fatalf("secret exposed in %s: %q", where, s)
				}
			}
			check("console", out.String())
			check("CI", stderr)
			if !strings.Contains(out.String(), "[REDACTED]") || !strings.Contains(stderr, "[REDACTED]") {
				t.Fatal("missing redaction marker")
			}
			for _, row := range r.taskProgressSeen {
				check("task cache", fmt.Sprintf("%+v", row.event))
			}
			for _, row := range r.statusProgressSeen {
				check("status cache", fmt.Sprintf("%+v", row))
			}
		})
	}
}

func TestProgressCallerRedactionDifferentSpans(t *testing.T) {
	const longer = "opaque-prefix-shortsecret-tail"
	const shorter = "shortsecret"
	redact := func(s string) string { return strings.NewReplacer(longer, "[LONG]", shorter, "[SHORT]").Replace(s) }
	r := newTestRenderer(&bytes.Buffer{})
	r.RedactProgress = redact
	// The raw pass masks the inner secret, the independent normalized pass
	// sees the outer secret. Do not print the remnants of either candidate.
	if got := r.progressText("context opaque-prefix-\x1b[31mshortsecret-tail", 160); got != "[REDACTED]" {
		t.Fatalf("conflicting redaction left residual text: %q", got)
	}
	// A raw-only match must still protect a literal that cleanup would alter.
	r.RedactProgress = func(s string) string { return strings.ReplaceAll(s, "opaque\tcredential", "[REDACTED]") }
	if got := r.progressText("opaque\tcredential", 160); got != "[REDACTED]" {
		t.Fatalf("lost raw-only masking: %q", got)
	}
}
