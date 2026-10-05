package agentnew

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
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

func TestGeneratedHarnessMakesGCPCredentialMountOptional(t *testing.T) {
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeTree(t, dir, files)
	h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
	if err != nil {
		t.Fatalf("generated harness does not load: %v", err)
	}

	for _, hostFile := range h.HostFiles {
		if hostFile.Src == "${GOOGLE_APPLICATION_CREDENTIALS}" {
			if !hostFile.Optional {
				t.Error("generated GCP credential host file must be optional")
			}
			return
		}
	}
	t.Error("generated harness is missing the GCP credential host file")
}

// TestGeneratedHarnessKeepsGHTokenOutOfSandbox pins #7883: the github-ro and
// github providers deliver GH_TOKEN to the sandbox as a placeholder, so the
// raw value belongs in env.runner (post-script) only. An env.sandbox entry
// would hand the sandbox the real token.
func TestGeneratedHarnessKeepsGHTokenOutOfSandbox(t *testing.T) {
	for _, role := range RoleNames() {
		t.Run(role, func(t *testing.T) {
			files, err := Render(testOptions("lint-docs", role))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			writeTree(t, dir, files)
			h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
			if err != nil {
				t.Fatalf("generated harness does not load: %v", err)
			}
			if _, ok := h.Env.Sandbox["GH_TOKEN"]; ok {
				t.Error("env.sandbox must not carry GH_TOKEN; the provider supplies a placeholder")
			}
			if got := h.Env.Runner["GH_TOKEN"]; got != "${GH_TOKEN}" {
				t.Errorf("env.runner GH_TOKEN = %q, want ${GH_TOKEN} for the post-script", got)
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

// TestSharedPolicyIsTheOnlyPolicy: the scaffold ships no policy and CI layers
// none, so the policies/base.yaml written here is the one a repo-local agent
// runs with (#6834). OpenShell 0.0.116+ refuses a policy without run_as_user.
func TestSharedPolicyIsTheOnlyPolicy(t *testing.T) {
	if _, err := scaffold.FullsendRepoFile("policies/base.yaml"); err == nil {
		t.Fatal("scaffold ships policies/base.yaml; agent new must not become a second copy (#7268)")
	}
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	got := fileByPath(t, files, "policies/base.yaml")
	if !got.Shared {
		t.Error("policies/base.yaml must be a shared asset")
	}
	policy := string(got.Data)
	for _, want := range []string{"version: 1", "filesystem_policy:", "landlock:", "process:", "run_as_user: sandbox", "run_as_group: sandbox"} {
		if !strings.Contains(policy, want) {
			t.Errorf("generated policies/base.yaml lacks %q", want)
		}
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
	// Absolute per JSON Schema 2020-12: a bare relative reference has no
	// base URI to resolve against in a file-local schema.
	if doc["$id"] != "https://fullsend.sh/schemas/lint-docs-result.schema.json" {
		t.Errorf("$id = %v", doc["$id"])
	}
	if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v", doc["$schema"])
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
		if f.Path == OpenAIProviderName {
			t.Error("the openai bare name must not be copied as a scaffold file")
		}
	}
	// 4 owned files, the policy and the validator. Providers and profiles
	// are bare names resolved from the binary, so none is written (#7268).
	if got := len(files); got != 4+1+1 {
		t.Errorf("retro with --validation-loop produced %d files, want 6", got)
	}
}

func testCodexOptions(name, role string) Options {
	o := testOptions(name, role)
	o.Runtime = "codex"
	o.Model = "openai/gpt-5.6-luna"
	return o
}

// TestCodexHarnessOmitsVertexCredentials is the #7264 pin: --runtime codex
// must not generate GCP host_files or Vertex env, must not default model to
// opus, must declare the openai provider, and must declare no Vertex
// provider or profile (#7971).
func TestCodexHarnessOmitsVertexCredentials(t *testing.T) {
	dir := t.TempDir()
	opts := testCodexOptions("lint-docs", "triage")
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
		t.Fatalf("generated codex harness does not load: %v", err)
	}
	if _, err := harness.CheckGenerated(h, dir); err != nil {
		t.Fatalf("generated codex tree does not validate: %v", err)
	}
	if !slices.Contains(h.Providers, OpenAIProviderName) {
		t.Errorf("providers = %v, want to include %q", h.Providers, OpenAIProviderName)
	}
	if slices.Contains(h.Providers, vertexProvider) {
		t.Errorf("codex harness must not declare Vertex: %v", h.Providers)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Path, "providers/") || strings.HasPrefix(f.Path, "profiles/") {
			t.Errorf("agent new must not write %s: built-in providers resolve from the binary", f.Path)
		}
	}
	for _, hf := range h.HostFiles {
		if strings.Contains(hf.Src, "GOOGLE_APPLICATION_CREDENTIALS") || strings.Contains(hf.Dest, "gcp") {
			t.Errorf("codex harness must not copy GCP credentials: %+v", hf)
		}
	}
	if h.Env != nil {
		for _, banned := range []string{
			"CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_VERTEX_PROJECT_ID",
			"CLOUD_ML_REGION", "GOOGLE_APPLICATION_CREDENTIALS",
		} {
			if _, ok := h.Env.Sandbox[banned]; ok {
				t.Errorf("codex sandbox env must not set %s", banned)
			}
		}
	}
	if h.Model != "openai/gpt-5.6-luna" {
		t.Errorf("model = %q, want openai/gpt-5.6-luna", h.Model)
	}
}

// TestDefaultHarnessDeclaresOpenAIAndVertex: the portable-harness pattern is
// to declare openai on every runtime, including the Vertex default, so a
// later `agent set --runtime codex` does not have to rewrite providers.
func TestDefaultHarnessDeclaresOpenAIAndVertex(t *testing.T) {
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}
	yaml := string(fileByPath(t, files, "harness/lint-docs.yaml").Data)
	for _, want := range []string{
		"- " + vertexProvider + "\n",
		OpenAIProviderName,
		"GOOGLE_APPLICATION_CREDENTIALS",
		"CLAUDE_CODE_USE_VERTEX",
		"model: opus",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("default harness should contain %q:\n%s", want, yaml)
		}
	}
}

func TestPiHarnessKeepsVertexCredentials(t *testing.T) {
	opts := testOptions("lint-docs", "triage")
	opts.Runtime = "pi"
	files, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	yaml := string(fileByPath(t, files, "harness/lint-docs.yaml").Data)
	for _, want := range []string{
		"GOOGLE_APPLICATION_CREDENTIALS",
		"CLAUDE_CODE_USE_VERTEX",
		OpenAIProviderName,
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("pi harness should contain %q:\n%s", want, yaml)
		}
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
		if h.OpenShell != nil && len(h.OpenShell.Profiles) != 0 {
			t.Errorf("role %q: openshell.profiles = %v, want none (built-in profiles come from the binary)", name, h.OpenShell.Profiles)
		}
		// readonly_repo must survive into the emitted YAML, not only sit on
		// the Role struct: a generated review harness that ships writable
		// lets the reviewer modify the code it reviews.
		if h.ReadonlyRepo != role.ReadonlyRepo || h.ReadonlyRepo != (name == "review") {
			t.Errorf("role %q: readonly_repo = %v, want %v", name, h.ReadonlyRepo, name == "review")
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

// TestGeneratedPromptFetchesByNumberAndRepo pins #7563: a github.com URL in
// a `gh` command is blocked by the SSRF PreToolUse hook (github.com does not
// resolve in the sandbox and github-ro allowlists api.github.com only), so
// the generated prompt must fetch by number and -R owner/repo instead.
func TestGeneratedPromptFetchesByNumberAndRepo(t *testing.T) {
	files, err := Render(testOptions("lint-docs", "triage"))
	if err != nil {
		t.Fatal(err)
	}

	md := string(fileByPath(t, files, "agents/lint-docs.md").Data)
	wantCmd := `gh issue view "$ISSUE_NUMBER" -R "$REPO_FULL_NAME" --json title,body,labels`
	if !strings.Contains(md, wantCmd) {
		t.Errorf("generated prompt must fetch by number and repo; missing %q\n%s", wantCmd, md)
	}
	for _, banned := range []string{
		`gh issue view "$ISSUE_URL"`,
		`gh issue view "$GITHUB_ISSUE_URL"`,
	} {
		if strings.Contains(md, banned) {
			t.Errorf("generated prompt still fetches by URL (%q), which the SSRF hook blocks", banned)
		}
	}

	dir := t.TempDir()
	writeTree(t, dir, files)
	h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
	if err != nil {
		t.Fatalf("generated harness does not load: %v", err)
	}
	if h.Env == nil || h.Env.Sandbox == nil {
		t.Fatal("generated harness has no env.sandbox")
	}
	if got := h.Env.Sandbox["ISSUE_NUMBER"]; got != "${ISSUE_NUMBER}" {
		t.Errorf("env.sandbox ISSUE_NUMBER = %q, want ${ISSUE_NUMBER}", got)
	}
	if got := h.Env.Sandbox["REPO_FULL_NAME"]; got != "${REPO_FULL_NAME}" {
		t.Errorf("env.sandbox REPO_FULL_NAME = %q, want ${REPO_FULL_NAME}", got)
	}
}

// TestPiOpenAIHarnessVertexBlock pins the pi + openai/ shape (#7971): no
// Vertex setting is active, so the agent runs with no GCP variables, and the
// commented-out block, once uncommented, is a harness the real loader
// accepts and that resolves to every Vertex setting with the credentials
// mount required.
func TestPiOpenAIHarnessVertexBlock(t *testing.T) {
	opts := testOptions("lint-docs", "triage")
	opts.Runtime, opts.Model = "pi", "openai/gpt-5.6-luna"
	files, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	generated := string(fileByPath(t, files, "harness/lint-docs.yaml").Data)

	dir := t.TempDir()
	writeTree(t, dir, files)
	h, err := harness.Load(filepath.Join(dir, "harness", "lint-docs.yaml"))
	if err != nil {
		t.Fatalf("generated harness does not load: %v", err)
	}
	if slices.Contains(h.Providers, vertexProvider) || len(h.HostFiles) != 0 || h.Env.Sandbox["ANTHROPIC_VERTEX_PROJECT_ID"] != "" {
		t.Fatalf("pi + openai/ harness must have no active Vertex setting:\n%s", generated)
	}
	if !strings.Contains(generated, vertexSubagentHeader) {
		t.Fatalf("missing the Vertex sub-agent header:\n%s", generated)
	}

	// Uncomment exactly what a user would: every line after the header.
	head, block, _ := strings.Cut(generated, vertexSubagentHeader)
	var uncommented strings.Builder
	for _, line := range strings.SplitAfter(block, "\n") {
		uncommented.WriteString(strings.TrimPrefix(line, "# "))
	}
	path := filepath.Join(dir, "harness", "lint-docs.yaml")
	if err := os.WriteFile(path, []byte(head+uncommented.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err = harness.Load(path)
	if err != nil {
		t.Fatalf("uncommented harness does not load: %v", err)
	}
	if _, err := harness.CheckGenerated(h, dir); err != nil {
		t.Fatalf("uncommented harness does not validate: %v", err)
	}
	if err := h.ResolveOverlays(nil, "github", nil); err != nil {
		t.Fatalf("resolving the uncommented overlay: %v", err)
	}
	if !slices.Contains(h.Providers, vertexProvider) {
		t.Errorf("uncommented block should add the Vertex provider: %v", h.Providers)
	}
	if h.OpenShell != nil && len(h.OpenShell.Profiles) != 0 {
		t.Errorf("uncommented block should list no profile; the built-in one is imported: %v", h.OpenShell.Profiles)
	}
	for k, v := range vertexSandboxEnv() {
		if h.Env.Sandbox[k] != v {
			t.Errorf("uncommented block: env.sandbox[%s] = %q, want %q", k, h.Env.Sandbox[k], v)
		}
	}
	var gac *harness.HostFile
	for i := range h.HostFiles {
		if h.HostFiles[i].Src == "${GOOGLE_APPLICATION_CREDENTIALS}" {
			gac = &h.HostFiles[i]
		}
	}
	if gac == nil || gac.Optional {
		t.Errorf("uncommented block must mount GCP credentials as required: %+v", h.HostFiles)
	}
}

// TestOnlyPiOpenAIGetsTheVertexBlock: codex sub-agents take OpenAI ids only,
// and a Vertex agent already has the settings active.
func TestOnlyPiOpenAIGetsTheVertexBlock(t *testing.T) {
	for name, opts := range map[string]Options{
		"codex":  testCodexOptions("lint-docs", "triage"),
		"claude": testOptions("lint-docs", "triage"),
		"pi-vertex": func() Options {
			o := testOptions("lint-docs", "triage")
			o.Runtime = "pi"
			return o
		}(),
	} {
		files, err := Render(opts)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(fileByPath(t, files, "harness/lint-docs.yaml").Data), vertexSubagentHeader) {
			t.Errorf("%s harness must not carry the Vertex sub-agent block", name)
		}
	}
}
