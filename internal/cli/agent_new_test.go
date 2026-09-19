package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
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

func TestAgentNewDryRunReportsWhatItWouldWrite(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir)
	f.dryRun = true
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	// That a dry run writes nothing is asserted in package agentnew
	// (TestGenerateDryRunWritesNothing). What only this layer can check is
	// that the command reports the plan rather than staying silent.
	for _, want := range []string{
		"Nothing was written",
		"harness/lint-docs.yaml",
		"agents/lint-docs.md",
		"--- harness/lint-docs.yaml ---",
		"role: triage",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output should contain %q:\n%s", want, out)
		}
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

// TestAgentNewNoRegisterWithRuntimeHintsAgentSet: --no-register combined
// with a non-empty --runtime leaves the runtime recorded nowhere (it only
// shapes the generated harness), and `agent add` — the command the plain
// --no-register hint names — has no --runtime flag to restore it. The hint
// must instead (or additionally) name `agent set --runtime`, which does.
func TestAgentNewNoRegisterWithRuntimeHintsAgentSet(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir, "runtime")
	f.runtime = "pi"
	f.noRegister = true
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fullsend agent set lint-docs --runtime pi") {
		t.Errorf("--no-register with --runtime should hint `agent set --runtime`, so the runtime can still be recorded:\n%s", out)
	}
}

func TestAgentNewCodexRequiresOpenAIModel(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir, "runtime")
	f.runtime = "codex"
	_, err := runNew(t, "lint-docs", f)
	if err == nil {
		t.Fatal("--runtime codex without --model should be refused")
	}
	for _, want := range []string{"codex takes OpenAI model ids only", "FULLSEND_CODEX_MODEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestAgentNewCodexOmitsVertexHostFiles(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir, "runtime", "model")
	f.runtime = "codex"
	f.model = "openai/gpt-5.6-luna"
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatalf("runAgentNew: %v\n%s", err, out)
	}

	h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
	if err != nil {
		t.Fatalf("generated harness does not load: %v", err)
	}
	if !slices.Contains(h.Providers, agentnew.OpenAIProviderName) {
		t.Errorf("providers = %v, want to include openai", h.Providers)
	}
	for _, hf := range h.HostFiles {
		if strings.Contains(hf.Src, "GOOGLE_APPLICATION_CREDENTIALS") {
			t.Errorf("codex harness must not require GCP credentials: %+v", hf)
		}
	}
	if strings.Contains(out, "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Errorf("codex next steps should not mention GCP credentials:\n%s", out)
	}
	if !strings.Contains(out, "OPENAI_API_KEY") {
		t.Errorf("codex next steps should mention OPENAI_API_KEY:\n%s", out)
	}

	cfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := config.AgentSettingsFor(cfg.AgentEntries(), "lint-docs")
	if !ok {
		t.Fatal("agent not registered")
	}
	if entry.Runtime != "codex" {
		t.Errorf("runtime = %q, want codex", entry.Runtime)
	}
}

// TestAgentNewPiOpenAIModelOmitsVertexHostFiles: --runtime pi with an
// OpenAI model calls OpenAI, not Vertex (the same distinction
// TestAgentNewCodexOmitsVertexHostFiles checks for codex), so both the
// generated harness and the printed next steps must match — mentioning
// OPENAI_API_KEY and not GOOGLE_APPLICATION_CREDENTIALS.
func TestAgentNewPiOpenAIModelOmitsVertexHostFiles(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir, "runtime", "model")
	f.runtime = "pi"
	f.model = "openai/gpt-6-astra"
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatalf("runAgentNew: %v\n%s", err, out)
	}

	h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
	if err != nil {
		t.Fatalf("generated harness does not load: %v", err)
	}
	for _, hf := range h.HostFiles {
		if strings.Contains(hf.Src, "GOOGLE_APPLICATION_CREDENTIALS") {
			t.Errorf("pi with an OpenAI model must not require GCP credentials: %+v", hf)
		}
	}
	if strings.Contains(out, "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Errorf("pi with an OpenAI model: next steps should not mention GCP credentials:\n%s", out)
	}
	if !strings.Contains(out, "OPENAI_API_KEY") {
		t.Errorf("pi with an OpenAI model: next steps should mention OPENAI_API_KEY:\n%s", out)
	}
}

func TestResolveAgentNewOptions(t *testing.T) {
	dir := newFullsendDir(t)

	t.Run("defaults", func(t *testing.T) {
		opts, runtimeName, _, err := resolveAgentNewOptions("lint-docs", defaultFlags(dir))
		if err != nil {
			t.Fatal(err)
		}
		if opts.Role != "triage" || opts.Model != "opus" || opts.Effort != "high" {
			t.Errorf("unexpected defaults: %+v", opts)
		}
		// dir's config.yaml sets no runtime:, so the repo default (claude)
		// resolves into opts.Runtime even though no --runtime flag was
		// given. runtimeName, the flag/spec-only value that agent set
		// --runtime would write, stays empty.
		if opts.Runtime != "claude" {
			t.Errorf("runtime = %q, want the resolved repo default claude", opts.Runtime)
		}
		if runtimeName != "" {
			t.Errorf("runtimeName = %q, want empty (no explicit --runtime given)", runtimeName)
		}
		if opts.Description != "Custom lint-docs agent." {
			t.Errorf("description = %q", opts.Description)
		}
		if !strings.Contains(opts.Trigger, "/fs-lint-docs") {
			t.Errorf("default trigger = %q", opts.Trigger)
		}
	})

	t.Run("no runtime given resolves the repo's configured default", func(t *testing.T) {
		// A repo whose config.yaml already sets runtime: codex must shape
		// the generated harness (and the opus-default-clearing / codex
		// model check) for codex, even though `agent new` was not given
		// --runtime: that is what runtime.ResolveForAgent will dispatch
		// this agent under (#7264 reached via the repo-wide default).
		codexDir := filepath.Join(t.TempDir(), ".fullsend")
		if err := os.MkdirAll(codexDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexDir, "config.yaml"),
			[]byte("version: \"1\"\nroles: [triage]\nruntime: codex\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := resolveAgentNewOptions("lint-docs", defaultFlags(codexDir))
		if err == nil {
			t.Fatal("want error: the repo default is codex, so the default opus model must be refused")
		}
		if !strings.Contains(err.Error(), "no model was named") {
			t.Errorf("error %q should mention no model was named", err)
		}

		f := defaultFlags(codexDir, "model")
		f.model = "openai/gpt-5.6-luna"
		opts, runtimeName, _, err := resolveAgentNewOptions("lint-docs", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Runtime != "codex" {
			t.Errorf("runtime = %q, want the resolved repo default codex", opts.Runtime)
		}
		if runtimeName != "" {
			t.Errorf("runtimeName = %q, want empty: no explicit --runtime was given, so `agent set --runtime` must not fire", runtimeName)
		}
	})

	t.Run("runtime is threaded into Options", func(t *testing.T) {
		f := defaultFlags(dir, "runtime")
		f.runtime = "pi"
		opts, runtimeName, _, err := resolveAgentNewOptions("lint-docs", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Runtime != "pi" || runtimeName != "pi" {
			t.Errorf("runtime = %q / %q, want pi", opts.Runtime, runtimeName)
		}
	})

	t.Run("codex without an explicit model is refused", func(t *testing.T) {
		f := defaultFlags(dir, "runtime")
		f.runtime = "codex"
		_, _, _, err := resolveAgentNewOptions("lint-docs", f)
		if err == nil {
			t.Fatal("want error")
		}
		if !strings.Contains(err.Error(), "no model was named") {
			t.Errorf("error %q should mention no model was named", err)
		}
	})

	t.Run("codex with an OpenAI model is accepted", func(t *testing.T) {
		f := defaultFlags(dir, "runtime", "model")
		f.runtime = "codex"
		f.model = "openai/gpt-5.6-luna"
		opts, _, _, err := resolveAgentNewOptions("lint-docs", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Runtime != "codex" || opts.Model != "openai/gpt-5.6-luna" {
			t.Errorf("unexpected options: %+v", opts)
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
runtime: pi
`)
		opts, runtimeName, _, err := resolveAgentNewOptions("", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Name != "from-spec" || opts.Role != "review" || opts.Model != "sonnet" {
			t.Errorf("spec not applied: %+v", opts)
		}
		if opts.TimeoutMinutes != 30 || opts.Description != "From the spec" {
			t.Errorf("spec not applied: %+v", opts)
		}
		if opts.Runtime != "pi" || runtimeName != "pi" {
			t.Errorf("spec runtime not applied: %q / %q", opts.Runtime, runtimeName)
		}
		if !strings.Contains(opts.Trigger, "needs-review") {
			t.Errorf("spec trigger not applied: %q", opts.Trigger)
		}
	})

	t.Run("spec runtime codex without a model is refused", func(t *testing.T) {
		f := defaultFlags(dir)
		f.specFile = writeSpec(t, "version: \"1\"\nname: from-spec\nruntime: codex\n")
		_, _, _, err := resolveAgentNewOptions("", f)
		if err == nil {
			t.Fatal("want error")
		}
		if !strings.Contains(err.Error(), "no model was named") {
			t.Errorf("error %q should mention no model was named", err)
		}
	})

	t.Run("spec runtime codex with an OpenAI model is accepted", func(t *testing.T) {
		f := defaultFlags(dir)
		f.specFile = writeSpec(t, "version: \"1\"\nname: from-spec\nruntime: codex\nmodel: openai/gpt-5.6-luna\n")
		opts, _, _, err := resolveAgentNewOptions("", f)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Runtime != "codex" || opts.Model != "openai/gpt-5.6-luna" {
			t.Errorf("unexpected options: %+v", opts)
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
