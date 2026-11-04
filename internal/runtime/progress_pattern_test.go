package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/security"
)

func TestProgressPatternRedactionBeforeUnicodeComposition(t *testing.T) {
	// Repetitive synthetic fixtures exercise patterns; never authenticate.
	tokens := []string{strings.Join([]string{"gh", "p_"}, "") + strings.Repeat("a", 35) + "e", strings.Join([]string{"AK", "IA"}, "") + strings.Repeat("A", 15) + "E"}
	for index, token := range tokens {
		for _, withCallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("pattern%d/callback=%t", index, withCallback), func(t *testing.T) {
				t.Setenv("GITHUB_ACTIONS", "true")
				var out bytes.Buffer
				r := newTestRenderer(&out)
				if withCallback {
					r.RedactProgress = func(s string) string { return strings.ReplaceAll(s, "caller-owned-placeholder", "[REDACTED]") }
				}
				stderr := captureStderr(t, func() {
					r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: " \t" + token + "\u0301 \n"})
					r.Handle(StatusProgressEvent{Kind: "notice", ID: "one", Label: "Notice", Detail: " \t" + token + "\u0301 \n"})
				})
				check := func(where, s string) {
					t.Helper()
					if strings.Contains(s, token[:len(token)-1]) {
						t.Fatalf("pattern credential prefix exposed in %s", where)
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
			})
		}
	}
}

func TestProgressRegisteredUnicodeLiteralWithoutCallback(t *testing.T) {
	const secret = "opaque-\u202eruntime-progress-for-test"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: secret})
	if strings.Contains(out.String(), "opaque-") || !strings.Contains(out.String(), "***") {
		t.Fatal("registered literal escaped normalization")
	}
}

func TestProgressPatternPaddingKeepsExactRegisteredMatches(t *testing.T) {
	const secret = "\t opaque-padded-runtime-progress-only \n"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic padded literal")
	}
	if got := scanProgressSecrets(secret); strings.Contains(got, "opaque-padded") {
		t.Fatal("padding optimization lost exact registered match")
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "padded", State: TaskRunning, Label: secret})
	if strings.Contains(out.String(), "opaque-padded") || !strings.Contains(out.String(), "***") {
		t.Fatal("nil-callback padded registered literal escaped")
	}
	const ordinary = " \tordinary status \n"
	if got := scanProgressSecrets(ordinary); got != ordinary {
		t.Fatal("nonsecret raw text changed before normalization")
	}
	if got := scanProgressSecrets(" \t \n"); got != " \t \n" {
		t.Fatal("empty scanner candidate changed raw text")
	}
}

func TestProgressFallbackRedactsNormalizedRuntimeSecret(t *testing.T) {
	// A registered credential containing whitespace variants (here a tab)
	// normalizes to different text than the registered literal. Without a
	// RedactProgress callback the fallback must still mask the normalized
	// literal, or the reconstructed credential is displayed and retained.
	const secret = "opaque-runtime-secret \twith-tab-for-test"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	cleaned := NormalizeProgressText(secret)
	if cleaned == secret || !strings.Contains(cleaned, "with-tab-for-test") {
		t.Fatalf("setup: secret did not normalize differently: %q", cleaned)
	}
	if got := scanProgressSecrets("progress " + cleaned + " done"); strings.Contains(got, "opaque-runtime-secret") || !strings.Contains(got, "***") {
		t.Fatalf("normalized registered literal escaped the fallback: %q", got)
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "progress " + cleaned + " done"})
	if strings.Contains(out.String(), "opaque-runtime-secret") || !strings.Contains(out.String(), "***") {
		t.Fatalf("normalized registered literal escaped rendering: %q", out.String())
	}
}

// A registered credential whose normalized form is shorter than the
// registration floor must still be masked: the original literal passed the
// floor at registration, so every non-empty normalized derivative stays an
// authenticated secret. No RedactProgress callback is set, exercising the
// renderer fallback.
func TestProgressFallbackRedactsShortNormalizedRuntimeSecret(t *testing.T) {
	const secret = "zz\x1b[32m\x1b[0mtok" // raw length 14 bytes; normalizes to "zztok" (5)
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	cleaned := NormalizeProgressText(secret)
	if cleaned != "zztok" {
		t.Fatalf("setup: unexpected normalized form %q", cleaned)
	}
	if got := scanProgressSecrets("status " + cleaned + " done"); strings.Contains(got, "zztok") || !strings.Contains(got, "***") {
		t.Fatalf("short normalized registered literal escaped the fallback: %q", got)
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "status " + cleaned + " done"})
	if strings.Contains(out.String(), "zztok") || !strings.Contains(out.String(), "***") {
		t.Fatalf("short normalized registered literal escaped rendering: %q", out.String())
	}
}

func TestProgressFallbackMaskingOrderKeepsOverlappingCredentials(t *testing.T) {
	// A secret with a longer raw value can normalize shorter than another
	// secret's normalized form. Masking in registry (raw-length) order
	// would replace the shorter literal first and leave the longer
	// overlapping credential's suffix visible. No callback is set.
	long := "containment-long\x1b[32m\x1b[0m" // raw 25 bytes, normalizes to "containment-long"
	short := "containment-long-suffix"        // raw 23 bytes, normalizes to itself
	if !security.RegisterRuntimeSecret(long) || !security.RegisterRuntimeSecret(short) {
		t.Fatal("register synthetic literals")
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "status containment-long-suffix done"})
	if strings.Contains(out.String(), "containment-long") || !strings.Contains(out.String(), "***") {
		t.Fatalf("overlapping normalized credential leaked: %q", out.String())
	}
}

func TestProgressFallbackMergesCrossingOverlaps(t *testing.T) {
	// One secret's end overlapping another's start defeats every
	// sequential replacement order; the merged span must be masked whole.
	a := "crossing-alpha-start"
	b := "start-crossing-beta"
	if !security.RegisterRuntimeSecret(a) || !security.RegisterRuntimeSecret(b) {
		t.Fatal("register synthetic literals")
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "prefix crossing-alpha-start-crossing-beta suffix"})
	if strings.Contains(out.String(), "crossing-alpha") || strings.Contains(out.String(), "crossing-beta") {
		t.Fatalf("crossing overlap leaked a fragment: %q", out.String())
	}
	if !strings.Contains(out.String(), "***") {
		t.Fatalf("crossing overlap was not masked: %q", out.String())
	}
}

func TestTaskProgressToolOnlyFramesDoNotPrint(t *testing.T) {
	// Subagent tool calls already arrive as ToolUseEvents; a task_progress
	// frame that only changes last_tool_name must not print a second line.
	lines := `{"type":"system","subtype":"task_progress","task_id":"sub1","description":"Subagent work","last_tool_name":"Read"}` + "\n" +
		`{"type":"system","subtype":"task_progress","task_id":"sub1","description":"Subagent work","last_tool_name":"Bash"}` + "\n"
	var out bytes.Buffer
	r := newTestRenderer(&out)
	if err := parseClaudeStream(strings.NewReader(lines), r.Handle); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Task sub1") != 1 {
		t.Fatalf("tool-only task_progress printed more than once: %q", out.String())
	}
	if strings.Contains(out.String(), "tool: Bash") {
		t.Fatalf("tool-only change printed a second row: %q", out.String())
	}
}

func TestTaskProgressToolLifecyclePrintsRealTransitions(t *testing.T) {
	// Tool swaps are attribution churn and do not print, but a tool
	// appearing or disappearing changes the row, and a later real
	// transition prints the latest retained tool name. Frames carry
	// usage counters like real SDK snapshots to prove they do not churn.
	parse := func(raw string, r *EventRenderer) {
		t.Helper()
		if err := parseClaudeStream(strings.NewReader(raw+"\n"), r.Handle); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	parse(`{"type":"system","subtype":"task_progress","task_id":"sub1","description":"Work","usage":{"total_tokens":10,"tool_uses":1}}`, r)
	parse(`{"type":"system","subtype":"task_progress","task_id":"sub1","description":"Work","last_tool_name":"Read","usage":{"total_tokens":20,"tool_uses":2}}`, r)
	if !strings.Contains(out.String(), "tool: Read") {
		t.Fatalf("tool appearance did not print: %q", out.String())
	}
	parse(`{"type":"system","subtype":"task_progress","task_id":"sub1","description":"Work","last_tool_name":"Bash","usage":{"total_tokens":30,"tool_uses":3}}`, r)
	if strings.Contains(out.String(), "tool: Bash") {
		t.Fatalf("tool swap printed a row: %q", out.String())
	}
	parse(`{"type":"system","subtype":"task_progress","task_id":"sub1","description":"Work","summary":"Halfway","last_tool_name":"Bash","usage":{"total_tokens":40,"tool_uses":4}}`, r)
	if !strings.Contains(out.String(), "tool: Bash") || !strings.Contains(out.String(), "Halfway") {
		t.Fatalf("real transition lost the retained tool name: %q", out.String())
	}
	parse(`{"type":"system","subtype":"task_progress","task_id":"sub1","description":"Work","last_tool_name":"","summary":""}`, r)
	if !strings.HasSuffix(strings.TrimRight(out.String(), "\n"), "— running") {
		t.Fatalf("explicit tool and summary clear did not print: %q", out.String())
	}
}

func TestTaskProgressJoinCannotReconstructCredential(t *testing.T) {
	// A credential split across TaskID and Label reconstructs in the
	// assembled row; the joined message must be masked before display.
	const secret = "taskrow-split: cred"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "taskrow-split", Label: "cred", State: TaskRunning})
	if strings.Contains(out.String(), "cred") || strings.Contains(out.String(), "taskrow-split: cred") {
		t.Fatalf("joined task-row credential leaked: %q", out.String())
	}
	if !strings.Contains(out.String(), "***") {
		t.Fatalf("joined task-row credential was not masked: %q", out.String())
	}
}
