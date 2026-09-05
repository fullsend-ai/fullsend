package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGeneratedTree lays down a minimal but complete agent tree of the shape
// `fullsend agent new` produces, so CheckGenerated can be exercised directly
// rather than through the generator.
func writeGeneratedTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"harness/demo.yaml": `agent: agents/demo.md
role: triage
policy: policies/base.yaml
post_script: scripts/post-demo.sh
providers:
  - providers/vertex-ai.yaml
openshell:
  profiles:
    - profiles/fullsend-vertex-ai.yaml
trigger: |
  event.entity.kind == "work_item"
`,
		"agents/demo.md":                   "---\nname: demo\n---\n\nbody\n",
		"policies/base.yaml":               "version: 1\n",
		"scripts/post-demo.sh":             "#!/usr/bin/env bash\ntrue\n",
		"providers/vertex-ai.yaml":         "name: vertex-ai\ntype: fullsend-vertex-ai\n",
		"profiles/fullsend-vertex-ai.yaml": "id: fullsend-vertex-ai\n",
	}
	for path, body := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func loadDemo(t *testing.T, dir string) *Harness {
	t.Helper()
	h, err := Load(filepath.Join(dir, "harness", "demo.yaml"))
	if err != nil {
		t.Fatalf("loading the test harness: %v", err)
	}
	return h
}

func TestCheckGeneratedHappyPath(t *testing.T) {
	dir := writeGeneratedTree(t)
	diags, err := CheckGenerated(loadDemo(t, dir), dir)
	if err != nil {
		t.Fatalf("a complete tree should validate: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
}

// TestCheckGeneratedMissingFiles covers both halves of the file checking:
// ValidateFilesExist for the agent/policy/script, and the extra provider and
// profile stat that ValidateFilesExist deliberately skips.
func TestCheckGeneratedMissingFiles(t *testing.T) {
	for _, victim := range []string{
		"agents/demo.md",
		"policies/base.yaml",
		"scripts/post-demo.sh",
		"providers/vertex-ai.yaml",
		"profiles/fullsend-vertex-ai.yaml",
	} {
		t.Run(victim, func(t *testing.T) {
			dir := writeGeneratedTree(t)
			if err := os.Remove(filepath.Join(dir, victim)); err != nil {
				t.Fatal(err)
			}
			_, err := CheckGenerated(loadDemo(t, dir), dir)
			if err == nil {
				t.Fatalf("removing %s was not detected", victim)
			}
			if !strings.Contains(err.Error(), filepath.Base(victim)) {
				t.Errorf("error should name %q, got: %v", victim, err)
			}
		})
	}
}

// TestCheckGeneratedReturnsLintDiagnostics: a deprecated shape is reported as
// a non-fatal diagnostic, not an error, so the caller can warn and continue.
func TestCheckGeneratedReturnsLintDiagnostics(t *testing.T) {
	dir := writeGeneratedTree(t)
	h := loadDemo(t, dir)
	h.RunnerEnv = map[string]string{"LEGACY": "value"} // deprecated by ADR 0055

	diags, err := CheckGenerated(h, dir)
	if err != nil {
		t.Fatalf("a deprecated shape must not be fatal: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for runner_env")
	}
	var found bool
	for _, d := range diags {
		if d.Field == "runner_env" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a runner_env diagnostic, got %v", diags)
	}
}

// TestCheckGeneratedRejectsEscapingPaths: ResolveRelativeTo must refuse a
// path that climbs out of the fullsend directory.
func TestCheckGeneratedRejectsEscapingPaths(t *testing.T) {
	dir := writeGeneratedTree(t)
	h := loadDemo(t, dir)
	h.Policy = "../../etc/shadow"

	if _, err := CheckGenerated(h, dir); err == nil {
		t.Fatal("a path escaping the fullsend directory should be refused")
	} else if !strings.Contains(err.Error(), "resolves outside") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCheckGeneratedSkipsBareProviderNames: a bare name is resolved from the
// binary's embedded definitions, not from disk, so it must not be stat-ed.
func TestCheckGeneratedSkipsBareProviderNames(t *testing.T) {
	dir := writeGeneratedTree(t)
	h := loadDemo(t, dir)
	h.Providers = []string{"vertex-ai"}
	h.OpenShell = nil

	if _, err := CheckGenerated(h, dir); err != nil {
		t.Errorf("a bare provider name should not be stat-ed: %v", err)
	}
}

func TestValidAgentBasename(t *testing.T) {
	for _, name := range []string{"a", "lint-docs", "Lint_Docs9", "A-B_c-1"} {
		if !ValidAgentBasename(name) {
			t.Errorf("ValidAgentBasename(%q) = false, want true", name)
		}
	}
	// The rejected set is the security-relevant half: the agent name reaches
	// shell interpolation.
	for _, name := range []string{
		"", "a b", "a;rm -rf /", "a$(id)", "a`id`", "../escape", "a/b", "a|b", "a&b", "a\nb", "ä",
	} {
		if ValidAgentBasename(name) {
			t.Errorf("ValidAgentBasename(%q) = true, want false", name)
		}
	}
}

func TestValidSlug(t *testing.T) {
	for _, slug := range []string{"a", "0", "my-org-agent", "Org_1-x"} {
		if !ValidSlug(slug) {
			t.Errorf("ValidSlug(%q) = false, want true", slug)
		}
	}
	for _, slug := range []string{"", "-leading", "_leading", "has space", "has/slash", "a.b"} {
		if ValidSlug(slug) {
			t.Errorf("ValidSlug(%q) = true, want false", slug)
		}
	}
}
