package agentnew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSpec(t *testing.T) {
	spec, err := ParseSpec([]byte(`
version: "1"
name: lint-docs
role: triage
description: Check docs changes for broken links
on: command:/fs-lint-docs
model: opus
effort: high
runtime: claude
slug: my-org-lint-docs
timeout_minutes: 20
validation_loop: true
`))
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Name != "lint-docs" || spec.Role != "triage" {
		t.Errorf("unexpected spec: %+v", spec)
	}
	if spec.TimeoutMinutes == nil || *spec.TimeoutMinutes != 20 || !spec.ValidationLoop {
		t.Errorf("unexpected spec: %+v", spec)
	}
	if spec.Description != "Check docs changes for broken links" {
		t.Errorf("description: %q", spec.Description)
	}
}

func TestParseSpecRejects(t *testing.T) {
	tests := []struct {
		name, doc, wantErr string
	}{
		{"unknown key", "version: \"1\"\nname: a\nrol: triage\n", "field rol not found"},
		{"missing version", "name: a\n", "version"},
		{"wrong version", "version: \"2\"\nname: a\n", "version"},
		{"missing name", "version: \"1\"\n", "name"},
		{"on and trigger", "version: \"1\"\nname: a\non: label\ntrigger: 'true'\n", "mutually exclusive"},
		{"negative timeout", "version: \"1\"\nname: a\ntimeout_minutes: -1\n", "negative"},
		{"empty", "", "empty"},
		{"two documents", "version: \"1\"\nname: a\n---\nversion: \"1\"\nname: b\n", "exactly one"},
		{"not a mapping", "- a\n- b\n", "parsing spec file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSpec([]byte(tc.doc))
			if err == nil {
				t.Fatalf("ParseSpec(%q) succeeded; want error", tc.doc)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadSpecFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(path, []byte("version: \"1\"\nname: lint-docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadSpecFile(path)
	if err != nil {
		t.Fatalf("LoadSpecFile: %v", err)
	}
	if spec.Name != "lint-docs" {
		t.Errorf("name: %q", spec.Name)
	}

	if _, err := LoadSpecFile(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("LoadSpecFile on a missing path should fail")
	}
}

// TestParseSpecDistinguishesZeroFromOmitted: `timeout_minutes: 0` is a real
// choice, and reading the zero value as "omitted" would silently replace it
// with the 15-minute default.
func TestParseSpecDistinguishesZeroFromOmitted(t *testing.T) {
	omitted, err := ParseSpec([]byte("version: \"1\"\nname: a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if omitted.TimeoutMinutes != nil {
		t.Errorf("an omitted timeout should be nil, got %v", *omitted.TimeoutMinutes)
	}

	zero, err := ParseSpec([]byte("version: \"1\"\nname: a\ntimeout_minutes: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if zero.TimeoutMinutes == nil || *zero.TimeoutMinutes != 0 {
		t.Errorf("an explicit 0 should survive, got %v", zero.TimeoutMinutes)
	}
}
