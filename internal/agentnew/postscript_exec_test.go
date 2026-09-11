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
