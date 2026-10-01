package agentnew

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// stopCommandsTokenRe matches the stop-commands opening line the dry-run
// preview emits: "::stop-commands::<32 hex chars>".
var stopCommandsTokenRe = regexp.MustCompile(`^::stop-commands::([0-9a-f]{32})$`)

// TestGeneratedPostScriptNeverEchoesAnInvalidStatus feeds a variety of
// malicious status values — a line break paired with a `::`-style workflow
// command, and the legacy `##[...]` Actions logging-command form — and
// checks that none of them, or any other part of the raw value, ever reaches
// the rejection message. Capping and flattening (an earlier version of this
// script's approach) only defeats line-splitting: GitHub's Actions log
// viewer recognizes `##[...]` anywhere in a line, not only at its start, so
// a fixed prefix in front of the value does not neutralize it. The only
// reliable fix is to never print the value at all.
func TestGeneratedPostScriptNeverEchoesAnInvalidStatus(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	script := renderPostScriptTo(t, t.TempDir())
	cases := map[string]string{
		"LF":                  "bogus\n::error::injected",
		"CR":                  "bogus\r::error::injected",
		"CRLF":                "bogus\r\n::error::injected",
		"legacy-warning":      "##[warning]forged",
		"legacy-add-mask":     "##[add-mask]s3cr3t",
		"long":                strings.Repeat("x", 200),
		"legacy-mid-sentence": "status is totally fine ##[error]nope, trust me",
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			runDir := writeRunDir(t, map[string]any{
				"iteration-1": map[string]any{
					"status":  status,
					"summary": "s",
					"comment": "c",
				},
			})
			_, stderr, err := runPostScript(t, script, runDir)
			if err == nil {
				t.Fatal("expected the script to reject an invalid status")
			}
			const wantMsg = "post-lint-docs: status must be ok, findings or error"
			if strings.TrimSpace(stderr) != wantMsg {
				t.Fatalf("expected the fixed rejection message %q and nothing else, got stderr:\n%s", wantMsg, stderr)
			}
			for _, line := range strings.FieldsFunc(stderr, func(r rune) bool { return r == '\n' || r == '\r' }) {
				if strings.HasPrefix(line, "::") {
					t.Fatalf("model-supplied status reached the log as its own line: %q", line)
				}
			}
		})
	}
}

// TestGeneratedPostScriptDoesNotExpandEscapesUnderXpgEcho runs the script
// with bash's xpg_echo option on, where `echo` interprets backslash escapes.
// Because the invalid-status message never includes the value, a status
// carrying a literal backslash-n must still produce the single fixed
// rejection line regardless of xpg_echo.
func TestGeneratedPostScriptDoesNotExpandEscapesUnderXpgEcho(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	script := renderPostScriptTo(t, t.TempDir())
	runDir := writeRunDir(t, map[string]any{
		"iteration-1": map[string]any{
			"status":  `bogus\n::error::injected`,
			"summary": "s",
			"comment": "c",
		},
	})
	cmd := exec.Command("bash", "-O", "xpg_echo", script)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(),
		"ISSUE_URL=https://github.com/fullsend-ai/demo/pull/99",
		"GH_TOKEN=test-token",
		DryRunEnvVar("lint-docs")+"=1",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected the script to reject an invalid status")
	}
	out := strings.TrimSpace(stderr.String())
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("unexpected extra log line under xpg_echo:\n%s", out)
	}
	if strings.Contains(out, `bogus`) {
		t.Fatalf("the invalid status value must never appear in the log, got: %q", out)
	}
}

// TestGeneratedPostScriptDryRunPreviewCannotIssueWorkflowCommands feeds a
// findings comment whose lines start with "::" (after LF, after CR, and
// after leading spaces, which the Actions runner trims) and checks that the
// dry-run preview keeps every one of them inside a stop-commands block with
// a random 32-hex token, opened before the preview and closed after it.
func TestGeneratedPostScriptDryRunPreviewCannotIssueWorkflowCommands(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	script := renderPostScriptTo(t, t.TempDir())
	runDir := writeRunDir(t, map[string]any{
		"iteration-1": map[string]any{
			"status":  "findings",
			"summary": "s",
			"comment": "intro\n::error::injected\r::add-mask::x\n   ::warning::indented",
		},
	})
	token := stopCommandsTokenRe
	var tokens []string
	for i := 0; i < 2; i++ {
		stdout, stderr, err := runPostScript(t, script, runDir)
		if err != nil {
			t.Fatalf("dry run must exit 0, got %v; stderr:\n%s", err, stderr)
		}
		lines := strings.FieldsFunc(stdout, func(r rune) bool { return r == '\n' || r == '\r' })
		m := token.FindStringSubmatch(lines[0])
		if m == nil {
			t.Fatalf("preview must open with ::stop-commands::<32 hex>, got first line %q", lines[0])
		}
		if last := lines[len(lines)-1]; last != "::"+m[1]+"::" {
			t.Fatalf("preview must close with ::%s::, got last line %q", m[1], last)
		}
		inside := strings.Join(lines[1:len(lines)-1], "\n")
		for _, injected := range []string{"::error::injected", "::add-mask::x", "::warning::indented"} {
			if !strings.Contains(inside, injected) {
				t.Fatalf("%q must appear inside the stop-commands block, got:\n%s", injected, stdout)
			}
		}
		tokens = append(tokens, m[1])
	}
	if tokens[0] == tokens[1] {
		t.Fatalf("the stop-commands token must be random per run, got %s twice", tokens[0])
	}
}

// TestGeneratedPostScriptDryRunFailsClosedWithoutAToken pins the token
// guard: if od yields no token, an empty ::stop-commands:: would be
// rejected by the runner and the preview read as commands, so the script
// must exit 1 before printing anything.
func TestGeneratedPostScriptDryRunFailsClosedWithoutAToken(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(binDir+"/od", []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := renderPostScriptTo(t, t.TempDir())
	runDir := writeRunDir(t, map[string]any{
		"iteration-1": map[string]any{"status": "findings", "summary": "s", "comment": "::error::injected"},
	})
	stdout, stderr, err := runPostScript(t, script, runDir, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil {
		t.Fatalf("a dry run without a token must fail; stdout:\n%s", stdout)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("nothing may be printed without a token, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "could not generate a stop-commands token") {
		t.Fatalf("expected the token guard's message, got:\n%s", stderr)
	}
}
