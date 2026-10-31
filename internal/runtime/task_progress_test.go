package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/security"
)

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
		{TaskID: "one", AgentID: "worker", RunID: "second-run", Label: "review", State: TaskRunning},
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
	if len(r.progress.taskProgressSeen) > maxTaskProgressEntries || len(r.progress.taskProgressRuns) > maxTaskProgressEntries {
		t.Fatalf("unbounded task/parent state: %d rows, %d controls", len(r.progress.taskProgressSeen), len(r.progress.taskProgressRuns))
	}
}

func TestTaskProgressRendererRedactsAndSanitizes(t *testing.T) {
	const opaque = "opaque-progress-credential-for-test"
	security.RegisterRuntimeSecret(opaque)
	// Generate a scanner-shaped dummy; no stored credential fixture.
	token := "ghp_" + strings.Repeat("0", 36)
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

func TestTaskProgressHiddenAgentCannotReplaceParent(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", AgentIndex: 1, State: TaskCompleted, Hidden: true, Label: "PRIVATE"})
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "Visible parent"})
	r.Handle(TaskProgressEvent{TaskID: "one", AgentIndex: 1, State: TaskRunning, Label: "PRIVATE"})
	if strings.Contains(out.String(), "PRIVATE") || !strings.Contains(out.String(), "Visible parent — running") {
		t.Fatal(out.String())
	}
}

func TestTaskProgressIndexIdentityScopesAndIDFallback(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	for _, e := range []TaskProgressEvent{
		{TaskID: "one", RunID: "first", AgentIndex: 1, AgentID: "shared", Label: "Work", State: TaskRunning},
		{TaskID: "one", RunID: "first", AgentIndex: 2, AgentID: "shared", Label: "Work", State: TaskRunning},
		{TaskID: "two", RunID: "first", AgentIndex: 1, AgentID: "shared", Label: "Work", State: TaskRunning},
		{TaskID: "one", RunID: "second", AgentIndex: 1, AgentID: "shared", Label: "Work", State: TaskRunning},
		{TaskID: "one", RunID: "second", AgentID: "shared", Label: "Work", State: TaskRunning},
		{TaskID: "one", RunID: "second", AgentID: "fallback-a", Label: "Work", State: TaskRunning},
		{TaskID: "one", RunID: "second", AgentID: "fallback-b", Label: "Work", State: TaskRunning},
	} {
		r.Handle(e)
	}
	if len(r.progress.taskProgressSeen) != 5 || strings.Count(out.String(), "— running") != 7 {
		t.Fatal("distinct active slots/tasks/fallback IDs collapsed: " + out.String())
	}
	r.Handle(TaskProgressEvent{TaskID: "one", RunID: "second", AgentIndex: 1, AgentID: "learned", Label: "Work", State: TaskRunning})
	if len(r.progress.taskProgressSeen) != 5 || strings.Count(out.String(), "— running") != 7 {
		t.Fatal("ID-only indexed change created another row")
	}
	r.Handle(TaskProgressEvent{TaskID: "one", RunID: "second", AgentIndex: 1, Label: "Work", State: TaskCompleted})
	if !strings.Contains(out.String(), "agent #1 (learned): Work — completed") {
		t.Fatal("known display ID was lost: " + out.String())
	}
}
