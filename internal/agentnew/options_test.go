package agentnew

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

func validOptions() Options {
	o := testOptions("lint-docs", "triage")
	return o
}

// TestOptionsValidateRejects covers every field that reaches a generated
// file. Name is the security-relevant one: it is interpolated into a shell
// script, so an invalid name must be refused before anything is written.
func TestOptionsValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string
	}{
		{"empty name", func(o *Options) { o.Name = "" }, "is not valid"},
		{"name with semicolon", func(o *Options) { o.Name = "a;rm -rf /" }, "is not valid"},
		{"name with slash", func(o *Options) { o.Name = "../escape" }, "is not valid"},
		{"name with space", func(o *Options) { o.Name = "lint docs" }, "is not valid"},
		{"name with dollar", func(o *Options) { o.Name = "a$(id)" }, "is not valid"},
		{"name with backtick", func(o *Options) { o.Name = "a`id`" }, "is not valid"},
		{"leading underscore", func(o *Options) { o.Name = "_lead" }, "is not valid"},
		{"leading hyphen", func(o *Options) { o.Name = "-lead" }, "is not valid"},
		{"unknown role", func(o *Options) { o.Role = "scribe" }, "unknown role"},
		{"empty trigger", func(o *Options) { o.Trigger = "" }, "trigger is required"},
		{"uncompilable trigger", func(o *Options) { o.Trigger = "this is not CEL" }, "does not compile"},
		{"non-boolean trigger", func(o *Options) { o.Trigger = `"a string"` }, "does not compile"},
		{"bad model", func(o *Options) { o.Model = "opus!!" }, "model"},
		{"codex without a model", func(o *Options) { o.Runtime = "codex"; o.Model = "" }, "no model was named"},
		{"codex with a Claude alias", func(o *Options) { o.Runtime = "codex"; o.Model = "opus" }, `"opus" is not one`},
		{"codex with another provider", func(o *Options) { o.Runtime = "codex"; o.Model = "anthropic/claude-x" }, `"anthropic/claude-x" is not one: use --model openai/gpt-5.6-luna`},
		{"bad effort", func(o *Options) { o.Effort = "extreme" }, "effort"},
		{"bad slug", func(o *Options) { o.Slug = "-leading-dash" }, "slug"},
		{"negative timeout", func(o *Options) { o.TimeoutMinutes = -1 }, "non-negative"},
		{"empty image", func(o *Options) { o.Image = "" }, "image"},
		{"workflow name under pi", func(o *Options) {
			o.Runtime = "pi"
			o.Workflow = &harness.WorkflowSpec{Source: "pipelines/sample-pipeline", Name: "run-all"}
		}, "on the pi runtime the definition is a pi extension, which takes no workflow name"},
		{"workflow under codex", func(o *Options) {
			o.Runtime = "codex"
			o.Model = "openai/gpt-5.6-luna"
			o.Workflow = &harness.WorkflowSpec{Source: "pipelines/sample-pipeline", Name: "run-all"}
		}, "a workflow definition runs on claude or pi, and this agent resolves to codex"},
		{"workflow source without a name on claude", func(o *Options) {
			o.Workflow = &harness.WorkflowSpec{Source: "pipelines/sample-pipeline"}
		}, "--workflow-source needs --workflow <name>"},
		{"workflow with a bad name", func(o *Options) {
			o.Workflow = &harness.WorkflowSpec{Source: "pipelines/sample-pipeline", Name: "run all"}
		}, "workflow.name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := validOptions()
			tc.mutate(&o)
			err := o.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestOptionsValidateAccepts(t *testing.T) {
	for _, role := range RoleNames() {
		o := testOptions("lint-docs", role)
		if err := o.Validate(); err != nil {
			t.Errorf("role %q: %v", role, err)
		}
	}
	// Optional fields may be empty.
	o := validOptions()
	o.Model, o.Effort, o.Slug, o.Description = "", "", "", ""
	o.TimeoutMinutes = 0
	if err := o.Validate(); err != nil {
		t.Errorf("optional fields should be allowed to be empty: %v", err)
	}

	codex := validOptions()
	codex.Runtime = "codex"
	codex.Model = "openai/gpt-5.6-luna"
	if err := codex.Validate(); err != nil {
		t.Errorf("codex with an OpenAI model should be accepted: %v", err)
	}
}

// TestUsesVertex covers every runtime value UsesVertex branches on,
// including the empty string: Options.Runtime is expected to already carry
// the resolved runtime, but UsesVertex still needs a defined answer if a
// caller leaves it empty, and that answer is the same as claude's.
func TestUsesVertex(t *testing.T) {
	for runtime, want := range map[string]bool{
		"": true, "claude": true, "pi": true, "dummy": true, "codex": false,
	} {
		if got := (Options{Runtime: runtime}).UsesVertex(); got != want {
			t.Errorf("UsesVertex(%q) = %v, want %v", runtime, got, want)
		}
	}
}

// TestUsesVertexPiKeysOnModelToo: --runtime pi with an OpenAI model calls
// OpenAI, not Vertex, so it must not carry the GOOGLE_APPLICATION_CREDENTIALS
// host_files and Vertex sandbox env either — the same failure shape as
// #7264, for pi instead of codex.
func TestUsesVertexPiKeysOnModelToo(t *testing.T) {
	if got := (Options{Runtime: "pi", Model: "openai/gpt-6-astra"}).UsesVertex(); got != false {
		t.Errorf("UsesVertex(pi, openai model) = %v, want false", got)
	}
	if got := (Options{Runtime: "pi", Model: "claude-opus-4-8"}).UsesVertex(); got != true {
		t.Errorf("UsesVertex(pi, vertex model) = %v, want true", got)
	}
}

// TestUsesVertexPiIgnoresAmbientProvider: UsesVertex must not reproduce the
// #7264 stranded-credentials shape by depending on the generator process's
// ambient FULLSEND_PI_PROVIDER — only an explicit "openai/" prefix on
// Options.Model may turn off Vertex for pi.
func TestUsesVertexPiIgnoresAmbientProvider(t *testing.T) {
	t.Setenv("FULLSEND_PI_PROVIDER", "openai")
	if got := (Options{Runtime: "pi", Model: "opus"}).UsesVertex(); got != true {
		t.Errorf("UsesVertex(pi, bare model) under FULLSEND_PI_PROVIDER=openai = %v, want true", got)
	}
}

// TestTriggerlessAgentIsRefusedLoudly: an agent with no trigger registers,
// validates and lists, then is silently skipped by dispatch with no
// annotation. The error has to explain that, or the user will not understand
// why the command refused.
func TestTriggerlessAgentIsRefusedLoudly(t *testing.T) {
	o := validOptions()
	o.Trigger = ""
	err := o.Validate()
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"--on", "--trigger", "never dispatched"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestParseWorkflow(t *testing.T) {
	remote := "https://github.com/example-org/sample-pipeline/tree/0123456789abcdef0123456789abcdef01234567"
	spec, err := ParseWorkflow("pipelines/sample-pipeline", "run-all")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Source != "pipelines/sample-pipeline" || spec.Name != "run-all" || spec.Args != "" {
		t.Errorf("unexpected spec: %+v", spec)
	}

	// A remote source without a pin gets the placeholder; one with a pin
	// is kept.
	spec, err = ParseWorkflow(remote, "run-all")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Source != remote+"#sha256="+PlaceholderTreeHash || !(Options{Workflow: spec}).HasPlaceholderPin() {
		t.Errorf("unpinned remote source: %+v", spec)
	}
	pinned := remote + "#sha256=" + strings.Repeat("a", 64)
	if spec, err = ParseWorkflow(pinned, "run-all"); err != nil || spec.Source != pinned || (Options{Workflow: spec}).HasPlaceholderPin() {
		t.Errorf("pinned remote source: %+v, %v", spec, err)
	}

	if spec, err := ParseWorkflow("", ""); spec != nil || err != nil {
		t.Errorf("no workflow: %+v, %v", spec, err)
	}
	if _, err := ParseWorkflow("", "run-all"); err == nil || !strings.Contains(err.Error(), "--workflow run-all needs --workflow-source") {
		t.Errorf("a name without a source must be refused: %v", err)
	}
	for bad, want := range map[string]string{
		"../other": `contains ".."`,
		"https://github.com/example-org/sample-pipeline/tree/main": "is not a commit sha",
	} {
		if _, err := ParseWorkflow(bad, "run-all"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseWorkflow(%q) = %v, want %q", bad, err, want)
		}
	}
}

func TestOptionsValidateAcceptsWorkflow(t *testing.T) {
	for _, runtimeName := range []string{"", "claude"} {
		o := validOptions()
		o.Runtime = runtimeName
		o.Workflow = &harness.WorkflowSpec{Source: "pipelines/sample-pipeline", Name: "run-all"}
		if err := o.Validate(); err != nil {
			t.Errorf("runtime %q: %v", runtimeName, err)
		}
	}
	o := validOptions()
	o.Runtime = "pi"
	o.Workflow = &harness.WorkflowSpec{Source: "pipelines/sample-pipeline"}
	if err := o.Validate(); err != nil {
		t.Errorf("a pi extension source without a name: %v", err)
	}
}
