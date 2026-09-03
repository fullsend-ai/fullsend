package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/agentnew"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// newFullsendDir creates a minimal per-repo .fullsend directory.
func newFullsendDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".fullsend")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("version: \"1\"\nroles: [triage]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// defaultFlags mirrors what cobra would supply, including the flag defaults.
func defaultFlags(dir string, changed ...string) agentNewFlags {
	set := map[string]bool{}
	for _, c := range changed {
		set[c] = true
	}
	return agentNewFlags{
		fullsendDir:    dir,
		role:           agentnew.DefaultRole,
		model:          agentnew.DefaultModel,
		effort:         agentnew.DefaultEffort,
		timeoutMinutes: agentnew.DefaultTimeoutMinutes,
		changed:        func(name string) bool { return set[name] },
	}
}

func runNew(t *testing.T, name string, f agentNewFlags) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := runAgentNew(context.Background(), name, f, ui.New(&buf))
	return buf.String(), err
}

// TestAgentNewEndToEnd is the whole command against a real directory: files
// land, the harness loads through the real loader, and the agent is
// registered so dispatch can find it.
func TestAgentNewEndToEnd(t *testing.T) {
	dir := newFullsendDir(t)
	out, err := runNew(t, "lint-docs", defaultFlags(dir))
	if err != nil {
		t.Fatalf("runAgentNew: %v\n%s", err, out)
	}

	for _, want := range []string{
		"harness/lint-docs.yaml",
		"agents/lint-docs.md",
		"schemas/lint-docs-result.schema.json",
		"scripts/post-lint-docs.sh",
		"policies/base.yaml",
		"providers/vertex-ai.yaml",
		"profiles/fullsend-vertex-ai.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected %s to be written: %v", want, err)
		}
	}

	h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
	if err != nil {
		t.Fatalf("generated harness does not load: %v", err)
	}
	if _, err := harness.CheckGenerated(h, dir); err != nil {
		t.Fatalf("generated tree does not validate: %v", err)
	}

	cfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := harness.RegisteredAgents(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range agents {
		if a.Name == "lint-docs" {
			found = true
		}
	}
	if !found {
		t.Errorf("agent was not registered; got %v", agents)
	}

	// The printed CI instruction must carry the same command the trigger
	// encodes, or the user is told to type something that will not fire.
	if !strings.Contains(out, "/fs-lint-docs") {
		t.Errorf("next steps should name the slash command:\n%s", out)
	}
	if !strings.Contains(h.Trigger, `"/fs-lint-docs"`) {
		t.Errorf("trigger should carry the same command: %q", h.Trigger)
	}
}

// TestAgentNewPostScriptIsExecutable: the post-script is invoked directly by
// the runner, so the execute bit is load-bearing.
func TestAgentNewPostScriptIsExecutable(t *testing.T) {
	dir := newFullsendDir(t)
	if _, err := runNew(t, "lint-docs", defaultFlags(dir)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "scripts", "post-lint-docs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("post-script is not executable: %v", info.Mode())
	}
}

func TestAgentNewDryRunWritesNothing(t *testing.T) {
	dir := newFullsendDir(t)
	before := treeSnapshot(t, dir)

	f := defaultFlags(dir)
	f.dryRun = true
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if !strings.Contains(out, "Nothing was written") {
		t.Errorf("dry run should say nothing was written:\n%s", out)
	}
	if after := treeSnapshot(t, dir); after != before {
		t.Errorf("dry run modified the directory\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestAgentNewRejectsCollisions and --force semantics: --force may overwrite
// the files this agent owns, but never a shared scaffold asset.
func TestAgentNewForceNeverOverwritesSharedAssets(t *testing.T) {
	dir := newFullsendDir(t)
	if _, err := runNew(t, "lint-docs", defaultFlags(dir)); err != nil {
		t.Fatal(err)
	}

	sentinel := []byte("# hand-edited, must survive\n")
	policyPath := filepath.Join(dir, "policies", "base.yaml")
	if err := os.WriteFile(policyPath, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	harnessPath := filepath.Join(dir, "harness", "lint-docs.yaml")
	if err := os.WriteFile(harnessPath, []byte("clobbered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without --force the collision is refused and nothing changes.
	if _, err := runNew(t, "lint-docs", defaultFlags(dir)); err == nil {
		t.Fatal("a second run without --force should fail")
	}
	if got, _ := os.ReadFile(harnessPath); string(got) != "clobbered\n" {
		t.Error("a refused run must not modify anything")
	}

	f := defaultFlags(dir)
	f.force = true
	f.noRegister = true // already registered; --force must not paper over that
	if _, err := runNew(t, "lint-docs", f); err != nil {
		t.Fatalf("--force run failed: %v", err)
	}
	if got, _ := os.ReadFile(harnessPath); string(got) == "clobbered\n" {
		t.Error("--force should have rewritten the harness")
	}
	if got, _ := os.ReadFile(policyPath); string(got) != string(sentinel) {
		t.Errorf("--force must never overwrite a shared asset; policy is now %q", got)
	}
}

func TestAgentNewNoRegister(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir)
	f.noRegister = true
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "harness", "lint-docs.yaml")); err != nil {
		t.Error("--no-register should still write the files")
	}
	cfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AgentEntries()) != 0 {
		t.Errorf("--no-register should not touch config.yaml, got %v", cfg.AgentEntries())
	}
	if !strings.Contains(out, "fullsend agent add") {
		t.Errorf("--no-register should say how to register later:\n%s", out)
	}
}

// TestAgentNewMissingConfig: runAgentAdd loads with MissingOK false, so a
// directory with no config.yaml must fail with something actionable rather
// than a bare stat error after the files have landed.
func TestAgentNewMissingConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".fullsend")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runNew(t, "lint-docs", defaultFlags(dir))
	if err == nil {
		t.Fatal("a fullsend dir with no config.yaml should fail")
	}
}

func TestAgentNewMissingDir(t *testing.T) {
	_, err := runNew(t, "lint-docs", defaultFlags(filepath.Join(t.TempDir(), "nope")))
	if err == nil {
		t.Fatal("a missing fullsend dir should fail")
	}
	if !strings.Contains(err.Error(), "github setup") {
		t.Errorf("error should point at github setup, got: %v", err)
	}
}

func TestAgentNewWithRuntime(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir, "runtime")
	f.runtime = "pi"
	if _, err := runNew(t, "lint-docs", f); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := config.AgentSettingsFor(cfg.AgentEntries(), "lint-docs")
	if !ok {
		t.Fatal("agent not registered")
	}
	if entry.Runtime != "pi" {
		t.Errorf("runtime = %q, want pi", entry.Runtime)
	}
}

func TestResolveAgentNewOptions(t *testing.T) {
	dir := newFullsendDir(t)

	t.Run("defaults", func(t *testing.T) {
		opts, _, _, err := resolveAgentNewOptions("lint-docs", defaultFlags(dir))
		if err != nil {
			t.Fatal(err)
		}
		if opts.Role != "triage" || opts.Model != "opus" || opts.Effort != "high" {
			t.Errorf("unexpected defaults: %+v", opts)
		}
		if opts.Description != "Custom lint-docs agent." {
			t.Errorf("description = %q", opts.Description)
		}
		if !strings.Contains(opts.Trigger, "/fs-lint-docs") {
			t.Errorf("default trigger = %q", opts.Trigger)
		}
	})

	t.Run("name and -f together are rejected", func(t *testing.T) {
		f := defaultFlags(dir)
		f.specFile = writeSpec(t, "version: \"1\"\nname: other\n")
		if _, _, _, err := resolveAgentNewOptions("lint-docs", f); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("no name at all is rejected", func(t *testing.T) {
		if _, _, _, err := resolveAgentNewOptions("", defaultFlags(dir)); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("--on and --trigger are mutually exclusive", func(t *testing.T) {
		f := defaultFlags(dir, "on", "trigger")
		f.on, f.trigger = "label:x", "true"
		if _, _, _, err := resolveAgentNewOptions("lint-docs", f); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("spec file supplies values", func(t *testing.T) {
		f := defaultFlags(dir)
		f.specFile = writeSpec(t, `version: "1"
name: from-spec
role: review
description: From the spec
on: label:needs-review
model: sonnet
timeout_minutes: 30
`)
		opts, _, _, err := resolveAgentNewOptions("", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Name != "from-spec" || opts.Role != "review" || opts.Model != "sonnet" {
			t.Errorf("spec not applied: %+v", opts)
		}
		if opts.TimeoutMinutes != 30 || opts.Description != "From the spec" {
			t.Errorf("spec not applied: %+v", opts)
		}
		if !strings.Contains(opts.Trigger, "needs-review") {
			t.Errorf("spec trigger not applied: %q", opts.Trigger)
		}
	})

	t.Run("flags override spec keys", func(t *testing.T) {
		f := defaultFlags(dir, "role", "model")
		f.role, f.model = "coder", "haiku"
		f.specFile = writeSpec(t, "version: \"1\"\nname: from-spec\nrole: review\nmodel: sonnet\n")
		opts, _, _, err := resolveAgentNewOptions("", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Role != "coder" || opts.Model != "haiku" {
			t.Errorf("flags should win over spec: %+v", opts)
		}
	})

	t.Run("explicit slug suppresses the fallback warning", func(t *testing.T) {
		f := defaultFlags(dir, "slug")
		f.slug = "my-org-lint-docs"
		opts, _, warning, err := resolveAgentNewOptions("lint-docs", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Slug != "my-org-lint-docs" || warning != "" {
			t.Errorf("slug = %q, warning = %q", opts.Slug, warning)
		}
	})
}

func TestSlashCommandFromTrigger(t *testing.T) {
	expr, err := agentnew.ExpandTrigger("command:/fs-lint-docs", "lint-docs")
	if err != nil {
		t.Fatal(err)
	}
	if got := slashCommandFromTrigger(expr); got != "/fs-lint-docs" {
		t.Errorf("slashCommandFromTrigger = %q", got)
	}
	label, err := agentnew.ExpandTrigger("label:x", "lint-docs")
	if err != nil {
		t.Fatal(err)
	}
	if got := slashCommandFromTrigger(label); got != "" {
		t.Errorf("a label trigger has no slash command, got %q", got)
	}
	if got := slashCommandFromTrigger(""); got != "" {
		t.Errorf("empty trigger, got %q", got)
	}
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// treeSnapshot renders a directory's file list and contents for comparison.
func treeSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		b.WriteString(path + ":" + string(data) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
