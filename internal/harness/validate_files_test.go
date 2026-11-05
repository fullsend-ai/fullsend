package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestPolicy writes a one-field OpenShell policy and returns its path.
func writeTestPolicy(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestAgent(t *testing.T, dir string) string {
	t.Helper()
	agent := filepath.Join(dir, "agent.md")
	if err := os.WriteFile(agent, []byte("# agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return agent
}

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

func TestValidateFilesExist_MissingPolicyField(t *testing.T) {
	dir := t.TempDir()
	h := &Harness{Agent: writeTestAgent(t, dir)}
	err := h.ValidateFilesExist()
	if err == nil {
		t.Fatal("expected an error when policy: is unset")
	}
	msg := err.Error()
	for _, want := range []string{
		"policy field is required",
		"commit a copy of the fleet policy from fullsend-ai/agents",
		"set policy: to its URL with a #sha256= hash",
		"`fullsend agent new` writes one when generating a new agent",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}

func TestValidateFilesExist_CommentOnlyPolicy(t *testing.T) {
	dir := t.TempDir()
	agent := writeTestAgent(t, dir)
	policy := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(policy, []byte("# Minimal policy for probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Harness{Agent: agent, Policy: policy}
	err := h.ValidateFilesExist()
	if err == nil {
		t.Fatal("expected an error for a comment-only policy")
	}
	msg := err.Error()
	for _, want := range []string{
		"declares no fields",
		policy,
		"OpenShell 0.1 cannot activate an empty policy",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}

func TestValidateFilesExist_EmptyYAMLPolicy(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(policy, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := (&Harness{Agent: writeTestAgent(t, dir), Policy: policy}).ValidateFilesExist()
	if err == nil {
		t.Fatal("expected an error for an empty mapping")
	}
	if !strings.Contains(err.Error(), "declares no fields") {
		t.Errorf("error %q lacks %q", err.Error(), "declares no fields")
	}
}

func TestValidateFilesExist_UnreadablePolicy(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read chmod 000 files")
	}
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policy, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(policy, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(policy, 0o644) }()
	err := (&Harness{Agent: writeTestAgent(t, dir), Policy: policy}).ValidateFilesExist()
	if err == nil {
		t.Fatal("expected an error for an unreadable policy")
	}
	if !strings.Contains(err.Error(), "policy:") {
		t.Errorf("error %q lacks %q", err.Error(), "policy:")
	}
}

func TestValidateFilesExist_MalformedPolicyLeftToOpenShell(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(policy, []byte(": not valid yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Decode errors are OpenShell's; this check only rejects empty documents.
	if err := (&Harness{Agent: writeTestAgent(t, dir), Policy: policy}).ValidateFilesExist(); err != nil {
		t.Fatalf("malformed YAML should not be rejected here: %v", err)
	}
}

func TestValidateFilesExist_PolicyURLSkippedForContent(t *testing.T) {
	dir := t.TempDir()
	h := &Harness{
		Agent:  writeTestAgent(t, dir),
		Policy: "https://github.com/fullsend-ai/agents/blob/main/policies/base.yaml#sha256=abc",
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("URL policy should skip the local-file content check: %v", err)
	}
}

func TestValidateFilesExist_InheritedPolicyFromBase(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "policies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "child.md"), []byte("# child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policies", "base.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
policy: policies/base.yaml
`)
	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
agent: agents/child.md
role: test
`)
	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	if err != nil {
		t.Fatalf("LoadWithBase: %v", err)
	}
	if h.Policy != "policies/base.yaml" {
		t.Fatalf("inherited policy = %q, want policies/base.yaml", h.Policy)
	}
	if err := h.ResolveRelativeTo(dir); err != nil {
		t.Fatalf("ResolveRelativeTo: %v", err)
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("child that inherits base policy should pass: %v", err)
	}
}
