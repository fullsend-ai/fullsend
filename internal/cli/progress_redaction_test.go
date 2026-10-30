package cli

import (
	"bytes"
	"strings"
	"testing"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestRedactProgressSecretLiterals(t *testing.T) {
	for _, tc := range []struct{ name, secret, input string }{
		{"split tab", "opaque\trunner credential", "opaque\trun\x1b[31mner credential"},
		{"split markers", "opaque::runner%0Acredential", "opaque:\x1b[31m:runner%0Acredential"},
		{"short normalized value", "opaque\x1b[31m", "opaque"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := agentruntime.NewEventRenderer(ui.New(&out))
			r.RedactProgress = func(s string) string { return redactProgressSecretLiterals(s, map[string]string{"X_TOKEN": tc.secret}) }
			r.Handle(agentruntime.TaskProgressEvent{TaskID: "wf", State: agentruntime.TaskRunning, Label: tc.input})
			if strings.Contains(out.String(), "opaque") || !strings.Contains(out.String(), "[REDACTED") {
				t.Fatalf("normalized credential escaped: %q", out.String())
			}
		})
	}
	const longer = "opaque-prefix-shortsecret-tail"
	env := map[string]string{"LONG_TOKEN": longer, "SHORT_TOKEN": "shortsecret", "EMPTY_TOKEN": "\x1b[31m\x1b[0m"}
	var out bytes.Buffer
	r := agentruntime.NewEventRenderer(ui.New(&out))
	r.RedactProgress = func(s string) string { return redactProgressSecretLiterals(s, env) }
	r.Handle(agentruntime.TaskProgressEvent{TaskID: "wf", State: agentruntime.TaskRunning, Label: "opaque-prefix-\x1b[31mshortsecret-tail"})
	if strings.Contains(out.String(), "opaque") || strings.Contains(out.String(), "-tail") {
		t.Fatalf("nested literal leaked: %q", out.String())
	}
	// Empty normalized literals must not inject replacements everywhere.
	if got := redactProgressSecretLiterals("ordinary", env); got != "ordinary" {
		t.Fatalf("empty normalization matched text: %q", got)
	}
	t.Setenv("GH_WORKFLOW_TOKEN", "provider\tcredential-for-test")
	if got := redactProgressSecretLiterals("provider credential-for-test", nil); strings.Contains(got, "provider") {
		t.Fatalf("provider alias unredacted: %q", got)
	}
	const registered = "registered\tcredential-for-progress-test"
	if !security.RegisterRuntimeSecret(registered) {
		t.Fatal("register fake runtime secret")
	}
	if got := redactProgressSecretLiterals("registered credential-for-progress-test", nil); got != "***" {
		t.Fatalf("registered alias unredacted: %q", got)
	}
}
