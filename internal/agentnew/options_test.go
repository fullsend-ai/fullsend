package agentnew

import (
	"strings"
	"testing"
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
		{"codex with a Claude alias", func(o *Options) { o.Runtime = "codex"; o.Model = "opus" }, "Claude model aliases"},
		{"bad effort", func(o *Options) { o.Effort = "extreme" }, "effort"},
		{"bad slug", func(o *Options) { o.Slug = "-leading-dash" }, "slug"},
		{"negative timeout", func(o *Options) { o.TimeoutMinutes = -1 }, "non-negative"},
		{"empty image", func(o *Options) { o.Image = "" }, "image"},
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
