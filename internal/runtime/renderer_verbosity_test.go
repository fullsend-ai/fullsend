package runtime

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// verbosityEvents is one of each printed event kind. Every value the agent
// or the source could have written carries a SECRET- marker.
func verbosityEvents() []AgentEvent {
	return []AgentEvent{
		InitEvent{Model: "claude-opus-4-6", Version: "1.2.3"},
		ThinkingEvent{Text: "SECRET-THINK"},
		TextEvent{Text: "SECRET-REPLY"},
		ToolUseEvent{Name: "Bash", Summary: "SECRET-TOOL"},
		TaskProgressEvent{TaskID: "task-1", Started: true, State: TaskRunning, Label: "SECRET-TASK"},
		StatusProgressEvent{Kind: "hook", ID: "h1", Label: "Hook", Subject: "SECRET-SUBJECT", SubjectSet: true, State: "running", Detail: "SECRET-DETAIL"},
		TokensEvent{InputTokens: 10, OutputTokens: 5},
		ErrorEvent{ErrorType: "api_error", Message: "SECRET-ERROR"},
		RetryEvent{Attempt: 1, MaxRetries: 3, Error: "SECRET-RETRY", DelayMs: 500},
		PluginErrorEvent{Plugin: "SECRET-PLUGIN", Type: "load_error", Message: "SECRET-PLUGIN-MSG"},
		ResultEvent{IsError: true, Subtype: "error_during_execution", NumTurns: 3, TotalCostUSD: 0.5, ErrorMessage: "SECRET-RESULT"},
	}
}

// renderAt feeds verbosityEvents to a renderer at level in CI mode and
// returns the console output and the stderr annotations.
func renderAt(t *testing.T, level string) (console, annotations string) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
	oldStderr := os.Stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = stderrW
	defer func() { os.Stderr = oldStderr }()

	var buf bytes.Buffer
	r := newEventRendererAt(ui.New(&buf), level)
	for _, evt := range verbosityEvents() {
		r.Handle(evt)
	}
	stderrW.Close()
	stderr, err := io.ReadAll(stderrR)
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), string(stderr)
}

func assertContainsAll(t *testing.T, level, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s: expected %q in output:\n%s", level, w, out)
		}
	}
}

func assertContainsNone(t *testing.T, level, out string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(out, u) {
			t.Errorf("%s: unexpected %q in output:\n%s", level, u, out)
		}
	}
}

// An unset level is today's output, and full is the same.
func TestRendererVerbosityFullMatchesUnset(t *testing.T) {
	unsetConsole, unsetNotes := renderAt(t, "")
	fullConsole, fullNotes := renderAt(t, config.ProgressVerbosityFull)
	if unsetConsole != fullConsole || unsetNotes != fullNotes {
		t.Errorf("full differs from unset:\nunset:\n%s%s\nfull:\n%s%s", unsetConsole, unsetNotes, fullConsole, fullNotes)
	}
	assertContainsAll(t, "full", fullConsole,
		"SECRET-THINK", "SECRET-REPLY", "SECRET-TOOL", "SECRET-TASK", "SECRET-DETAIL",
		"SECRET-ERROR", "SECRET-RETRY", "SECRET-PLUGIN-MSG", "SECRET-RESULT")
	assertContainsAll(t, "full", fullNotes, "::notice::Bash: SECRET-TOOL", "SECRET-TASK")
}

func TestRendererVerbosityTools(t *testing.T) {
	console, notes := renderAt(t, config.ProgressVerbosityTools)
	assertContainsNone(t, "tools", console, "SECRET-THINK", "SECRET-REPLY")
	assertContainsAll(t, "tools", console,
		"claude-opus-4-6", "Bash: SECRET-TOOL", "SECRET-TASK", "SECRET-DETAIL",
		"TOKENS in=10 out=5", "Result: ERROR (error_during_execution)", "$0.5000",
		"SECRET-ERROR", "SECRET-RESULT")
	assertContainsAll(t, "tools", notes, "::notice::Bash: SECRET-TOOL", "SECRET-TASK")
}

func TestRendererVerbosityQuiet(t *testing.T) {
	for _, level := range []string{config.ProgressVerbosityQuiet, "loud"} {
		console, notes := renderAt(t, level)
		if notes != "" {
			t.Errorf("%s: expected no annotations, got:\n%s", level, notes)
		}
		assertContainsNone(t, level, console, "SECRET-", "Bash")
		assertContainsAll(t, level, console,
			"claude-opus-4-6",
			"Task — running",
			"Status: hook — running",
			"TOKENS in=10 out=5",
			"api_error",
			"Retry 1/3 (delay 500ms)",
			"Plugin failed to load (load_error)",
			"Result: ERROR (error_during_execution)",
			"Turns", "$0.5000")
	}
}

func TestQuietTaskLine(t *testing.T) {
	tests := []struct {
		e    TaskProgressEvent
		want string
	}{
		{TaskProgressEvent{State: TaskCompleted, Label: "x"}, "Task — completed"},
		{TaskProgressEvent{AgentID: "a1", State: TaskFailed}, "Task agent — failed"},
		{TaskProgressEvent{AgentIndex: 2, State: TaskState("SECRET-STATE")}, "Task agent update"},
		{TaskProgressEvent{}, "Task update"},
	}
	for _, tc := range tests {
		if got := quietTaskLine(tc.e); got != tc.want {
			t.Errorf("quietTaskLine(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestQuietStatusLineAndField(t *testing.T) {
	if got := quietStatusLine(StatusProgressEvent{Kind: "rate_limit", State: "rejected", Detail: "SECRET"}); got != "Status: rate_limit — rejected" {
		t.Errorf("got %q", got)
	}
	if got := quietStatusLine(StatusProgressEvent{}); got != "Status: status" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("x", maxQuietFieldLen+10)
	if got := quietField(long, ""); got != strings.Repeat("x", maxQuietFieldLen)+"…" {
		t.Errorf("quietField did not bound a long value: %q", got)
	}
	if got := quietField("\x1b[31m", "fallback"); got != "fallback" {
		t.Errorf("quietField fallback = %q", got)
	}
}

func TestEmitQuietTranscriptErrors(t *testing.T) {
	var buf bytes.Buffer
	EmitQuietTranscriptErrors(&buf, []TranscriptError{
		{Source: "SECRET-SOURCE.jsonl", IsError: true, ErrorMessage: "SECRET-MSG", Subtype: "error_max_turns"},
		{Source: "b.jsonl", IsError: true, ErrorMessage: "SECRET-MSG"},
	})
	want := "::error title=Agent Error::agent terminated with error (subtype: error_max_turns)\n" +
		"::error title=Agent Error::agent terminated with error (subtype: unknown)\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}
