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
