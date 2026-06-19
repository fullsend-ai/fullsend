package skill

import (
	"strings"
	"testing"
)

func TestParseFrontmatter_WithDependencies(t *testing.T) {
	content := []byte(`---
name: rust-conventions
dependencies:
  - ../common/cargo-integration/SKILL.md
  - https://github.com/fullsend-ai/skills/security-baseline/SKILL.md#sha256=abc123
---
# Rust Conventions
Some content here.
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "rust-conventions" {
		t.Errorf("name = %q, want %q", meta.Name, "rust-conventions")
	}
	if len(meta.Dependencies) != 2 {
		t.Fatalf("dependencies len = %d, want 2", len(meta.Dependencies))
	}
	if meta.Dependencies[0] != "../common/cargo-integration/SKILL.md" {
		t.Errorf("dependencies[0] = %q, want %q", meta.Dependencies[0], "../common/cargo-integration/SKILL.md")
	}
	if meta.Dependencies[1] != "https://github.com/fullsend-ai/skills/security-baseline/SKILL.md#sha256=abc123" {
		t.Errorf("dependencies[1] = %q", meta.Dependencies[1])
	}
}

func TestParseFrontmatter_NoDependencies(t *testing.T) {
	content := []byte(`---
name: simple-skill
description: A skill with no dependencies
---
# Simple Skill
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "simple-skill" {
		t.Errorf("name = %q, want %q", meta.Name, "simple-skill")
	}
	if meta.Description != "A skill with no dependencies" {
		t.Errorf("description = %q", meta.Description)
	}
	if len(meta.Dependencies) != 0 {
		t.Errorf("dependencies should be empty, got %v", meta.Dependencies)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	content := []byte(`# Plain Markdown
No frontmatter here. Just a regular Markdown file.
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Errorf("expected nil for no frontmatter, got %+v", meta)
	}
}

func TestParseFrontmatter_MalformedYAML(t *testing.T) {
	content := []byte(`---
name: [invalid yaml
  this is broken: {
---
# Content
`)
	meta, err := ParseFrontmatter(content)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
	if meta != nil {
		t.Errorf("expected nil on error, got %+v", meta)
	}
	if !strings.Contains(err.Error(), "parsing skill frontmatter") {
		t.Errorf("error should be wrapped with context, got: %v", err)
	}
}

func TestParseFrontmatter_EmptyFrontmatter(t *testing.T) {
	content := []byte("---\n---\n# Content after empty frontmatter\n")
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta for empty frontmatter")
	}
	if meta.Name != "" || len(meta.Dependencies) != 0 {
		t.Errorf("expected zero-value SkillMeta, got %+v", meta)
	}
}

func TestParseFrontmatter_ContentAfterFrontmatter(t *testing.T) {
	content := []byte(`---
name: test-skill
---
# This is the body
It should be completely ignored by the parser.
Even if it contains --- markers.
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "test-skill" {
		t.Errorf("name = %q, want %q", meta.Name, "test-skill")
	}
}

func TestParseFrontmatter_MixedDependencyTypes(t *testing.T) {
	content := []byte(`---
name: mixed-deps
dependencies:
  - ../sibling/SKILL.md#sha256=aaa111
  - https://example.com/skills/remote/SKILL.md#sha256=bbb222
  - ./local-child/SKILL.md#sha256=ccc333
---
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if len(meta.Dependencies) != 3 {
		t.Fatalf("dependencies len = %d, want 3", len(meta.Dependencies))
	}
	want := []string{
		"../sibling/SKILL.md#sha256=aaa111",
		"https://example.com/skills/remote/SKILL.md#sha256=bbb222",
		"./local-child/SKILL.md#sha256=ccc333",
	}
	for i, w := range want {
		if meta.Dependencies[i] != w {
			t.Errorf("dependencies[%d] = %q, want %q", i, meta.Dependencies[i], w)
		}
	}
}

func TestParseFrontmatter_EmptyContent(t *testing.T) {
	meta, err := ParseFrontmatter([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Errorf("expected nil for empty content, got %+v", meta)
	}
}

func TestParseFrontmatter_ExistingSkillFormat(t *testing.T) {
	content := []byte(`---
name: pr-review
description: >-
  PR review orchestrator. Triages the change, dispatches specialized
  sub-agents in parallel.
model: claude-sonnet-4-6
allowed-tools:
  - Read
  - Bash
disable-model-invocation: false
---
# PR Review
Content here.
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "pr-review" {
		t.Errorf("name = %q, want %q", meta.Name, "pr-review")
	}
	if len(meta.Dependencies) != 0 {
		t.Errorf("expected no dependencies, got %v", meta.Dependencies)
	}
}

func TestParseFrontmatter_MultilineDescription(t *testing.T) {
	content := []byte(`---
name: verbose-skill
description: >-
  A skill with a very long description
  that spans multiple lines using YAML
  folded style.
dependencies:
  - ../dep/SKILL.md
---
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "verbose-skill" {
		t.Errorf("name = %q", meta.Name)
	}
	wantDesc := "A skill with a very long description that spans multiple lines using YAML folded style."
	if meta.Description != wantDesc {
		t.Errorf("description = %q, want %q", meta.Description, wantDesc)
	}
	if len(meta.Dependencies) != 1 {
		t.Fatalf("dependencies len = %d, want 1", len(meta.Dependencies))
	}
}

func TestParseFrontmatter_UTF8BOM(t *testing.T) {
	bom := []byte("\xef\xbb\xbf")
	content := append(bom, []byte("---\nname: bom-skill\n---\n# Content\n")...)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "bom-skill" {
		t.Errorf("name = %q, want %q", meta.Name, "bom-skill")
	}
}

func TestParseFrontmatter_UnclosedFrontmatter(t *testing.T) {
	content := []byte("---\nname: unclosed\nThis has no closing delimiter.\n")
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Errorf("expected nil for unclosed frontmatter, got %+v", meta)
	}
}

func TestParseFrontmatter_FalsePositiveClosingDelimiter(t *testing.T) {
	content := []byte(`---
name: tricky-skill
description: ----some-text-starting-with-dashes
---
# Content
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "tricky-skill" {
		t.Errorf("name = %q, want %q", meta.Name, "tricky-skill")
	}
	if meta.Description != "----some-text-starting-with-dashes" {
		t.Errorf("description = %q, want %q", meta.Description, "----some-text-starting-with-dashes")
	}
}

func TestParseFrontmatter_WhitespacePaddedClosingDelimiter(t *testing.T) {
	content := []byte("---\nname: padded-close\n  ---  \n# Content\n")
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil SkillMeta")
	}
	if meta.Name != "padded-close" {
		t.Errorf("name = %q, want %q", meta.Name, "padded-close")
	}
}

func TestParseFrontmatter_WithPolicy(t *testing.T) {
	content := []byte(`---
name: rust-conventions
dependencies:
  - ../common/cargo-integration/SKILL.md
policy: policies/rust-sandbox.yaml#sha256=bbb222
---
# Rust Conventions
`)
	meta, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.Policy != "policies/rust-sandbox.yaml#sha256=bbb222" {
		t.Errorf("policy = %q, want %q", meta.Policy, "policies/rust-sandbox.yaml#sha256=bbb222")
	}
	if len(meta.Dependencies) != 1 {
		t.Fatalf("dependencies length = %d, want 1", len(meta.Dependencies))
	}
}
