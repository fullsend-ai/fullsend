package agentnew

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeRunDir lays out a run directory the way internal/cli/run.go does:
// postCmd.Dir is the RUN directory, and each iteration's output lives in
// iteration-<N>/output/agent-result.json.
func writeRunDir(t *testing.T, results map[string]any) string {
	t.Helper()
	runDir := t.TempDir()
	for iter, result := range results {
		dir := filepath.Join(runDir, iter, "output")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agent-result.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return runDir
}

// runPostScript executes the generated post-script from runDir, in dry-run
// mode so it never posts anything.
func runPostScript(t *testing.T, script, runDir string, extraEnv ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(),
		"ISSUE_URL=https://github.com/fullsend-ai/demo/pull/99",
		"GH_TOKEN=test-token",
		DryRunEnvVar("lint-docs")+"=1",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// renderPostScriptTo writes the generated post-script to disk and returns its
// path.
func renderPostScriptTo(t *testing.T, dir string) string {
	t.Helper()
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "post-lint-docs.sh")
	if err := os.WriteFile(path, fileByPath(t, files, "scripts/post-lint-docs.sh").Data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGeneratedPostScriptRunsFromTheRunDirectory executes the generated
// script against the directory layout run.go actually provides.
//
// Every other test of this template matches substrings, and none of them
// could have caught the script looking in the wrong directory: `output/` is
// present in both the correct and incorrect forms. Running it is the only
// check that distinguishes them.
func TestGeneratedPostScriptRunsFromTheRunDirectory(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	script := renderPostScriptTo(t, t.TempDir())

	t.Run("finds the result under iteration-N/output", func(t *testing.T) {
		runDir := writeRunDir(t, map[string]any{
			"iteration-1": map[string]any{"status": "findings", "summary": "two broken links", "comment": "- a.md"},
		})
		stdout, stderr, err := runPostScript(t, script, runDir)
		if err != nil {
			t.Fatalf("post-script failed against a real run directory: %v\nstderr: %s", err, stderr)
		}
		if !strings.Contains(stdout, "two broken links") {
			t.Errorf("summary missing from output: %q", stdout)
		}
	})

	t.Run("takes the last iteration", func(t *testing.T) {
		runDir := writeRunDir(t, map[string]any{
			"iteration-1": map[string]any{"status": "findings", "summary": "first", "comment": "c"},
			"iteration-2": map[string]any{"status": "findings", "summary": "second", "comment": "c"},
		})
		stdout, stderr, err := runPostScript(t, script, runDir)
		if err != nil {
			t.Fatalf("post-script failed: %v\nstderr: %s", err, stderr)
		}
		if !strings.Contains(stdout, "second") {
			t.Errorf("expected the last iteration, got: %q", stdout)
		}
	})

	t.Run("a validated iteration wins over the scan", func(t *testing.T) {
		runDir := writeRunDir(t, map[string]any{
			"iteration-1": map[string]any{"status": "findings", "summary": "validated", "comment": "c"},
			"iteration-2": map[string]any{"status": "findings", "summary": "later", "comment": "c"},
		})
		stdout, stderr, err := runPostScript(t, script, runDir,
			"FULLSEND_VALIDATED_ITERATION_DIR="+filepath.Join(runDir, "iteration-1", "output"))
		if err != nil {
			t.Fatalf("post-script failed: %v\nstderr: %s", err, stderr)
		}
		if !strings.Contains(stdout, "validated") {
			t.Errorf("FULLSEND_VALIDATED_ITERATION_DIR should win, got: %q", stdout)
		}
	})

	t.Run("no output anywhere is an error", func(t *testing.T) {
		_, stderr, err := runPostScript(t, script, t.TempDir())
		if err == nil {
			t.Fatal("expected a failure when no iteration produced output")
		}
		if !strings.Contains(stderr, "no agent-result.json found") {
			t.Errorf("unexpected error text: %q", stderr)
		}
	})
}

// TestGeneratedPostScriptFlattensAnInvalidStatusBeforeLogging feeds a status
// that carries a newline and a workflow-command prefix. The rejection message
// must stay on one line and must not reproduce the injected line, because
// the script's stderr lands in the runner log where `::` at line start is
// interpreted as a command.
func TestGeneratedPostScriptFlattensAnInvalidStatusBeforeLogging(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	script := renderPostScriptTo(t, t.TempDir())
	runDir := writeRunDir(t, map[string]any{
		"iteration-1": map[string]any{
			"status":  "bogus\n::error::injected",
			"summary": "s",
			"comment": "c",
		},
	})
	_, stderr, err := runPostScript(t, script, runDir)
	if err == nil {
		t.Fatal("expected the script to reject an invalid status")
	}
	if !strings.Contains(stderr, "status must be ok, findings or error") {
		t.Fatalf("expected the status rejection, got stderr:\n%s", stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if strings.HasPrefix(line, "::") {
			t.Fatalf("model-supplied status reached the log as its own line: %q", line)
		}
	}
	if strings.Count(strings.TrimSpace(stderr), "\n") != 0 {
		t.Fatalf("rejection must be a single log line, got:\n%s", stderr)
	}
}

// TestGeneratedPostScriptDoesNotExpandEscapesUnderXpgEcho runs the script
// with bash's xpg_echo option on, where `echo` interprets backslash escapes.
// A status carrying a literal backslash-n must still be logged on one line.
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
		t.Fatalf("backslash escapes were expanded into a new log line:\n%s", out)
	}
	if !strings.Contains(out, `bogus\n::error::injected`) {
		t.Fatalf("expected the literal value on the single line, got: %q", out)
	}
}

// TestGeneratedPostScriptOkPostsNothingWithoutAnEarlierComment pins the ok
// path under dry run: the script exits 0 and says nothing was posted. The
// live path additionally looks for an earlier marker comment and replaces it
// with the all-clear; that needs the forge, so it is exercised by the
// example's script test in fullsend-ai/agents, not here.
func TestGeneratedPostScriptOkPostsNothingWithoutAnEarlierComment(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	script := renderPostScriptTo(t, t.TempDir())
	runDir := writeRunDir(t, map[string]any{
		"iteration-1": map[string]any{"status": "ok", "summary": "All clear", "comment": "All added documentation links resolve."},
	})
	stdout, stderr, err := runPostScript(t, script, runDir)
	if err != nil {
		t.Fatalf("ok status must exit 0, got %v; stderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "nothing to post") {
		t.Fatalf("expected the nothing-to-post notice, got stderr:\n%s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("ok under dry run must print no comment body, got:\n%s", stdout)
	}
}

// TestGeneratedPostScriptOkReplacesEarlierFindingsViaOnlyIfExists runs the
// live ok path (no dry run) against a stub `fullsend` on PATH that records
// its arguments. The script must delegate the "is there an earlier findings
// comment" decision to `fullsend issues post-comment --only-if-exists`
// rather than reimplementing it with raw API calls in shell.
func TestGeneratedPostScriptOkReplacesEarlierFindingsViaOnlyIfExists(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	binDir := t.TempDir()
	argsFile := filepath.Join(binDir, "args")
	stub := "#!/usr/bin/env bash\nif [[ \"$*\" == *--help* ]]; then echo '  --only-if-exists   update an existing comment but never create one'; exit 0; fi\nprintf '%s\\n' \"$@\" > " + argsFile + "\ncat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(binDir, "fullsend"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script := renderPostScriptTo(t, t.TempDir())
	runDir := writeRunDir(t, map[string]any{
		"iteration-1": map[string]any{"status": "ok", "summary": "All clear", "comment": "All added documentation links resolve."},
	})
	cmd := exec.Command("bash", script)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ISSUE_URL=https://github.com/fullsend-ai/demo/pull/99",
		"GH_TOKEN=test-token",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ok live path must exit 0, got %v; stderr:\n%s", err, stderr.String())
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the script never invoked fullsend on the ok path: %v; stderr:\n%s", err, stderr.String())
	}
	args := string(got)
	for _, want := range []string{"issues\npost-comment\n", "--only-if-exists\n", "--marker\n", "fullsend-ai/demo\n", "99\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("fullsend was called without %q; args:\n%s", strings.TrimSpace(want), args)
		}
	}
}

// TestGeneratedPostScriptOkOnAnOlderFullsendPostsNothing pins the
// degradation path: the runner's fullsend is pinned by the repository, not
// by the CLI that generated the script, so when its post-comment has no
// --only-if-exists the ok path must post nothing and exit 0 rather than fail
// on an unknown flag.
func TestGeneratedPostScriptOkOnAnOlderFullsendPostsNothing(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the generated post-script needs it")
	}
	binDir := t.TempDir()
	argsFile := filepath.Join(binDir, "args")
	// An older CLI: --help lists no --only-if-exists, and any real call
	// with that flag would fail — so the stub fails loudly if invoked to post.
	stub := "#!/usr/bin/env bash\nif [[ \"$*\" == *--help* ]]; then echo '  --dry-run   print what would be posted'; exit 0; fi\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho 'Error: unknown flag: --only-if-exists' >&2; exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "fullsend"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script := renderPostScriptTo(t, t.TempDir())
	runDir := writeRunDir(t, map[string]any{
		"iteration-1": map[string]any{"status": "ok", "summary": "All clear", "comment": "All added documentation links resolve."},
	})
	cmd := exec.Command("bash", script)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ISSUE_URL=https://github.com/fullsend-ai/demo/pull/99",
		"GH_TOKEN=test-token",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ok on an older fullsend must exit 0, got %v; stderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "nothing to post") {
		t.Fatalf("expected the nothing-to-post notice, got:\n%s", stderr.String())
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatalf("an older fullsend must not be asked to post on the ok path")
	}
}
