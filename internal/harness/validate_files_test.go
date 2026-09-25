package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateFilesExist_MissingPolicyNamesTheFix: a missing relative policy
// fails with a fix that works for an existing harness (#6834), and never
// suggests re-running agent new, which refuses on an existing agent.
func TestValidateFilesExist_MissingPolicyNamesTheFix(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agents", "lint-docs.md")
	if err := os.MkdirAll(filepath.Dir(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte("# agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Harness{Agent: agent, Policy: filepath.Join(dir, "policies", "base.yaml")}
	err := h.ValidateFilesExist()
	if err == nil {
		t.Fatal("expected an error for a missing policy")
	}
	msg := err.Error()
	for _, want := range []string{
		"policy: stat",
		"no such file or directory",
		"commit a copy of the fleet policy",
		"set policy: to its URL with a #sha256= hash",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "writes policies/base.yaml when it is absent") ||
		strings.Contains(msg, "Re-run `agent new`") ||
		strings.Contains(msg, "Re-running `agent new`") {
		t.Errorf("error %q suggests re-running agent new, which refuses on an existing agent", msg)
	}

	// Present policy: no hint, no error.
	if err := os.MkdirAll(filepath.Dir(h.Policy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Policy, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("unexpected error with the policy present: %v", err)
	}
}
