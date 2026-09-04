package agentnew

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// writeTree writes a rendered file set into dir, as the command does.
func writeTree(t *testing.T, dir string, files []File) {
	t.Helper()
	for _, f := range files {
		path := filepath.Join(dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Data, os.FileMode(f.Mode)); err != nil {
			t.Fatal(err)
		}
	}
}

func testOptions(name, role string) Options {
	trigger, err := ExpandTrigger(DefaultOn, name)
	if err != nil {
		panic(err)
	}
	r, err := LookupRole(role)
	if err != nil {
		panic(err)
	}
	return Options{
		Name:           name,
		Role:           role,
		Description:    "Check docs changes for broken links",
		Trigger:        trigger,
		Model:          DefaultModel,
		Effort:         DefaultEffort,
		Slug:           "my-org-" + name,
		Image:          r.Image,
		TimeoutMinutes: DefaultTimeoutMinutes,
	}
}

// TestGeneratedHarnessLoads is the test that matters most: for every role,
// the generated tree must load through the same loader dispatch uses. It is
// the check that would have caught #6834 (a policy: with no policies/
// directory) and the missing profiles/ layering.
func TestGeneratedHarnessLoads(t *testing.T) {
	for _, role := range RoleNames() {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			opts := testOptions("lint-docs", role)
			if err := opts.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			files, err := Render(opts)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			writeTree(t, dir, files)

			h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
			if err != nil {
				t.Fatalf("generated harness does not load: %v", err)
			}
			if err := h.ResolveRelativeTo(dir); err != nil {
				t.Fatalf("ResolveRelativeTo: %v", err)
			}
			if err := h.ValidateFilesExist(); err != nil {
				t.Fatalf("ValidateFilesExist: %v", err)
			}
			if diags := h.Lint(); len(diags) != 0 {
				t.Errorf("generated harness produces lint diagnostics: %v", diags)
			}
			if h.Role != role {
				t.Errorf("role = %q, want %q", h.Role, role)
			}
		})
	}
}

// TestGeneratedHarnessHasNoDeprecatedShapes pins decision 6: no forge: block
// (deprecated by ADR 0088) and no runner_env (deprecated by ADR 0055). Lint
// would warn, and a generator must never emit a shape the repo has deprecated.
func TestGeneratedHarnessHasNoDeprecatedShapes(t *testing.T) {
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	yaml := string(fileByPath(t, files, "harness/lint-docs.yaml").Data)
	for _, banned := range []string{"\nforge:", "runner_env:", "overlays:"} {
		if strings.Contains(yaml, banned) {
			t.Errorf("generated harness contains %q:\n%s", banned, yaml)
		}
	}
	if !strings.Contains(yaml, "policy: policies/base.yaml") {
		t.Error("generated harness must always set policy:")
	}
}

// TestValidationLoopIsOptional: the block is off by default and the
// validator script is only written when it is on. Once the block exists,
// script: is mandatory, so the two must move together.
func TestValidationLoopIsOptional(t *testing.T) {
	off, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fileByPath(t, off, "harness/lint-docs.yaml").Data), "validation_loop") {
		t.Error("validation_loop must be absent by default")
	}
	if hasPath(off, "scripts/validate-output-schema.sh") {
		t.Error("validator script must not be written without --validation-loop")
	}

	opts := testOptions("lint-docs", "triage")
	opts.ValidationLoop = true
	on, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	yaml := string(fileByPath(t, on, "harness/lint-docs.yaml").Data)
	if !strings.Contains(yaml, "validation_loop:") || !strings.Contains(yaml, "script: scripts/validate-output-schema.sh") {
		t.Errorf("validation_loop block missing or incomplete:\n%s", yaml)
	}
	if !hasPath(on, "scripts/validate-output-schema.sh") {
		t.Error("--validation-loop must write the validator script")
	}
}

// TestDescriptionIsMarshalledNotInterpolated: a description containing YAML
// metacharacters must not break either document.
func TestDescriptionIsMarshalledNotInterpolated(t *testing.T) {
	nasty := `weird: "value" #comment` + "\n" + `>folded: yes`
	opts := testOptions("lint-docs", "triage")
	opts.Description = nasty
	files, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeTree(t, dir, files)
	h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
	if err != nil {
		t.Fatalf("harness with an awkward description does not load: %v", err)
	}
	if h.Description != nasty {
		t.Errorf("description round-trip failed:\n got: %q\nwant: %q", h.Description, nasty)
	}
	md := string(fileByPath(t, files, "agents/lint-docs.md").Data)
	if !strings.HasPrefix(md, "---\n") || strings.Count(md, "\n---\n") < 1 {
		t.Errorf("agent definition frontmatter is malformed:\n%s", md[:200])
	}
}

// TestSchemaIsValidJSON: the post-script and the validation loop both parse
// it, so a malformed schema is a run-time failure.
func TestSchemaIsValidJSON(t *testing.T) {
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(fileByPath(t, files, "schemas/lint-docs-result.schema.json").Data, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if doc["additionalProperties"] != false {
		t.Error("schema must set additionalProperties: false")
	}
	if doc["$id"] != "lint-docs-result.schema.json" {
		t.Errorf("$id = %v", doc["$id"])
	}
}

// TestPostScriptSubstitution: the agent name is the only user value that
// reaches the shell script, and every placeholder must be consumed.
func TestPostScriptSubstitution(t *testing.T) {
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	f := fileByPath(t, files, "scripts/post-lint-docs.sh")
	script := string(f.Data)
	if strings.Contains(script, "__") {
		t.Errorf("unsubstituted placeholder remains in the post-script:\n%s", script)
	}
	if !strings.Contains(script, "POST_LINT_DOCS_DRY_RUN") {
		t.Error("dry-run variable not derived from the agent name")
	}
	if !strings.Contains(script, "<!-- fullsend:lint-docs-agent -->") {
		t.Error("sticky marker not derived from the agent name")
	}
	if f.Mode != 0o755 {
		t.Errorf("post-script mode = %o, want 755", f.Mode)
	}
}

func TestDryRunEnvVar(t *testing.T) {
	for in, want := range map[string]string{
		"lint-docs": "POST_LINT_DOCS_DRY_RUN",
		"a":         "POST_A_DRY_RUN",
		"a_b-c":     "POST_A_B_C_DRY_RUN",
	} {
		if got := DryRunEnvVar(in); got != want {
			t.Errorf("DryRunEnvVar(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSharedAssetsAreMarked: everything a generated agent does not own must
// be flagged Shared so --force never overwrites it.
func TestSharedAssetsAreMarked(t *testing.T) {
	opts := testOptions("lint-docs", "retro")
	opts.ValidationLoop = true
	files, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	owned := map[string]bool{
		"harness/lint-docs.yaml":               true,
		"agents/lint-docs.md":                  true,
		"schemas/lint-docs-result.schema.json": true,
		"scripts/post-lint-docs.sh":            true,
	}
	for _, f := range files {
		if owned[f.Path] == f.Shared {
			t.Errorf("%s: Shared = %v, want %v", f.Path, f.Shared, !owned[f.Path])
		}
	}
	// retro is the two-forge-provider role: 3 providers + 3 profiles.
	if got := len(files); got != 4+1+6+1 {
		t.Errorf("retro with --validation-loop produced %d files, want 12", got)
	}
}

func TestRenderRejectsUnknownRole(t *testing.T) {
	opts := testOptions("lint-docs", "triage")
	opts.Role = "scribe"
	if _, err := Render(opts); err == nil {
		t.Fatal("Render with an unknown role should fail")
	}
}

func fileByPath(t *testing.T, files []File, path string) File {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no generated file at %q", path)
	return File{}
}

func hasPath(files []File, path string) bool {
	for _, f := range files {
		if f.Path == path {
			return true
		}
	}
	return false
}

// TestPostScriptTruncatesWithinTheCap: the generated script advertises a
// 16384-character limit and the result schema enforces it, so appending the
// truncation marker after cutting at the cap would overshoot it.
func TestPostScriptTruncatesWithinTheCap(t *testing.T) {
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(fileByPath(t, files, "scripts/post-lint-docs.sh").Data)

	if strings.Contains(script, `comment="${comment:0:${MAX_COMMENT_CHARS}}"`) {
		t.Error("comment is cut at the cap and then has the marker appended, which overshoots it")
	}
	for _, want := range []string{
		"TRUNCATION_MARKER=",
		"keep=$(( MAX_COMMENT_CHARS - ${#TRUNCATION_MARKER} ))",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("truncation should reserve room for the marker; missing %q", want)
		}
	}
}

// TestRoleImageReachesTheHarness covers what the per-role golden trees used
// to: that each role's image constant actually lands in the generated
// harness. The role table test asserts the table is right; this asserts the
// value survives into the output, which is the half a table test cannot see.
func TestRoleImageReachesTheHarness(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range RoleNames() {
		role, err := LookupRole(name)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		files, err := Render(testOptions("lint-docs", name))
		if err != nil {
			t.Fatal(err)
		}
		writeTree(t, dir, files)

		h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
		if err != nil {
			t.Fatalf("role %q: %v", name, err)
		}
		if h.Image != role.Image {
			t.Errorf("role %q: harness image = %q, want %q", name, h.Image, role.Image)
		}
		if !reflect.DeepEqual(h.Providers, role.Providers) {
			t.Errorf("role %q: providers = %v, want %v", name, h.Providers, role.Providers)
		}
		if h.OpenShell == nil || !reflect.DeepEqual(h.OpenShell.Profiles, role.Profiles) {
			t.Errorf("role %q: profiles = %v, want %v", name, h.OpenShell, role.Profiles)
		}
		seen[role.Image] = true
	}
	// Both image constants must be reachable, or one is dead configuration.
	if len(seen) != 2 {
		t.Errorf("expected the role table to use both image constants, saw %d: %v", len(seen), seen)
	}
}

// TestTriggerReachesTheHarness covers what the label-trigger golden used to.
// The preset text itself is pinned by TestTriggerPresetsArePinned; this
// asserts the chosen preset survives marshalling into the harness.
func TestTriggerReachesTheHarness(t *testing.T) {
	for _, on := range []string{"command", "label:needs-docs", "issue-opened", "pr-opened"} {
		trigger, err := ExpandTrigger(on, "lint-docs")
		if err != nil {
			t.Fatal(err)
		}
		opts := testOptions("lint-docs", "triage")
		opts.Trigger = trigger

		dir := t.TempDir()
		files, err := Render(opts)
		if err != nil {
			t.Fatal(err)
		}
		writeTree(t, dir, files)

		h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
		if err != nil {
			t.Fatalf("--on %s: %v", on, err)
		}
		if strings.TrimSpace(h.Trigger) != strings.TrimSpace(trigger) {
			t.Errorf("--on %s: trigger did not survive marshalling\n got: %q\nwant: %q", on, h.Trigger, trigger)
		}
	}
}
