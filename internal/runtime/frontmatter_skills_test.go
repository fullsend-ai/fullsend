package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectFrontmatterSkills_ExistingSkillsDedup(t *testing.T) {
	t.Parallel()
	src := `---
name: triage
skills:
  - skill-b
  - skill-c
tools: Bash(gh),Skill
---

You are the triage agent.
`
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/skill-a",
		"/path/to/skill-b", // already present — should be deduplicated
	})
	require.NoError(t, err)

	got := string(result)
	// skill-b must appear exactly once (not duplicated).
	assert.Equal(t, 1, strings.Count(got, "skill-b"), "skill-b should appear exactly once")
	// skill-a must be added.
	assert.Contains(t, got, "  - skill-a")
	// skill-c must be preserved.
	assert.Contains(t, got, "  - skill-c")
	// The body must be preserved.
	assert.Contains(t, got, "You are the triage agent.")
	// Frontmatter must be valid.
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_NoExistingSkills(t *testing.T) {
	t.Parallel()
	src := `---
name: code
model: opus
---

You are the code agent.
`
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/skill-a",
	})
	require.NoError(t, err)

	got := string(result)
	assert.Contains(t, got, "skills:\n  - skill-a\n")
	assert.Contains(t, got, "name: code")
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "You are the code agent.")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_NoFrontmatter(t *testing.T) {
	t.Parallel()
	src := `# Just a prompt

Do things.
`
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/my-skill",
	})
	require.NoError(t, err)

	got := string(result)
	assert.Contains(t, got, "---\nskills:\n  - my-skill\n---\n")
	assert.Contains(t, got, "# Just a prompt")
	assert.Contains(t, got, "Do things.")
}

func TestInjectFrontmatterSkills_EmptySkillDirs(t *testing.T) {
	t.Parallel()
	src := `---
name: test
---
Body
`
	result, err := injectFrontmatterSkills([]byte(src), nil)
	require.NoError(t, err)
	assert.Equal(t, src, string(result))

	result, err = injectFrontmatterSkills([]byte(src), []string{})
	require.NoError(t, err)
	assert.Equal(t, src, string(result))
}

func TestInjectFrontmatterSkills_AllAlreadyPresent(t *testing.T) {
	t.Parallel()
	src := `---
name: test
skills:
  - skill-a
  - skill-b
---
Body
`
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/cache/skill-a",
		"/cache/skill-b",
	})
	require.NoError(t, err)
	assert.Equal(t, src, string(result), "no change when all skills already present")
}

func TestInjectFrontmatterSkills_PreservesOtherFrontmatter(t *testing.T) {
	t.Parallel()
	src := `---
name: review
description: >-
  Code review agent.
model: opus
tools: Bash(gh,jq),Skill
skills:
  - code-review
disallowedTools: >-
  Bash(git push *)
---

Review the PR.
`
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/security-review",
	})
	require.NoError(t, err)

	got := string(result)
	assert.Contains(t, got, "name: review")
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "tools: Bash(gh,jq),Skill")
	assert.Contains(t, got, "  - code-review")
	assert.Contains(t, got, "  - security-review")
	assert.Contains(t, got, "disallowedTools:")
	assert.Contains(t, got, "Review the PR.")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_DeterministicOrder(t *testing.T) {
	t.Parallel()
	src := `---
name: test
---
Body
`
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/zebra",
		"/path/to/alpha",
		"/path/to/middle",
	})
	require.NoError(t, err)

	got := string(result)
	alphaIdx := strings.Index(got, "alpha")
	middleIdx := strings.Index(got, "middle")
	zebraIdx := strings.Index(got, "zebra")
	assert.True(t, alphaIdx < middleIdx && middleIdx < zebraIdx,
		"added skills should be sorted alphabetically")
}

func TestInjectFrontmatterSkills_EmptyStringDirs(t *testing.T) {
	t.Parallel()
	src := `---
name: test
---
Body
`
	// Empty strings in skillDirs should be ignored.
	result, err := injectFrontmatterSkills([]byte(src), []string{"", ""})
	require.NoError(t, err)
	assert.Equal(t, src, string(result))
}

func TestInjectFrontmatterSkills_SkillsAfterBody(t *testing.T) {
	t.Parallel()
	// The skills: section comes before other fields.
	src := `---
name: test
skills:
  - existing
model: opus
---
Body text here.
`
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/new-skill",
	})
	require.NoError(t, err)

	got := string(result)
	assert.Contains(t, got, "  - existing")
	assert.Contains(t, got, "  - new-skill")
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body text here.")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_FlowStyleSkills(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: [skill-a, skill-b]\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/skill-c",
	})
	require.NoError(t, err)

	got := string(result)
	// Existing flow-style skills must be preserved in block form.
	assert.Contains(t, got, "  - skill-a")
	assert.Contains(t, got, "  - skill-b")
	// New skill must be added.
	assert.Contains(t, got, "  - skill-c")
	// Flow-style line must NOT remain.
	assert.NotContains(t, got, "[skill-a")
	// Other frontmatter must be preserved.
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_FlowStyleEmpty(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: []\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/skill-a",
	})
	require.NoError(t, err)

	got := string(result)
	assert.Contains(t, got, "  - skill-a")
	assert.NotContains(t, got, "[]")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_CommentsInSkillsBlock(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills:\n  - skill-a\n  # A comment in the middle\n  - skill-b\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/path/to/skill-c",
	})
	require.NoError(t, err)

	got := string(result)
	// skill-c must be injected after skill-b (the last list item),
	// not after skill-a (before the comment).
	assert.Contains(t, got, "  - skill-a")
	assert.Contains(t, got, "  - skill-b")
	assert.Contains(t, got, "  - skill-c")
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	// Comment must be preserved.
	assert.Contains(t, got, "# A comment")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_BOM(t *testing.T) {
	t.Parallel()
	src := "\xEF\xBB\xBF---\nname: test\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/skill-a"})
	require.NoError(t, err)
	assert.Contains(t, string(result), "  - skill-a")
	assert.Contains(t, string(result), "Body")
}

func TestInjectFrontmatterSkills_NoFrontmatterDedup(t *testing.T) {
	t.Parallel()
	src := "# Just a prompt\n"
	// Two skill dirs with the same basename — should produce one entry.
	result, err := injectFrontmatterSkills([]byte(src), []string{
		"/a/skill-x",
		"/b/skill-x",
	})
	require.NoError(t, err)

	got := string(result)
	assert.Equal(t, 1, strings.Count(got, "skill-x"), "duplicate basenames should be deduplicated")
	assert.Contains(t, got, "# Just a prompt")
}

func TestInjectFrontmatterSkills_BlockScalarNoFalseMatch(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\ndescription: >-\n  skills: are critical for automation\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/my-skill"})
	require.NoError(t, err)

	got := string(result)
	// The description continuation line must be preserved, not matched as a skills: key.
	assert.Contains(t, got, "skills: are critical for automation")
	assert.Contains(t, got, "  - my-skill")
	assert.Contains(t, got, "name: test")
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_MultilineFlowStyle(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: [\n  skill-a,\n  skill-b\n]\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/skill-c"})
	require.NoError(t, err)

	got := string(result)
	// All existing skills must be preserved in block form.
	assert.Contains(t, got, "  - skill-a")
	assert.Contains(t, got, "  - skill-b")
	// New skill must be added.
	assert.Contains(t, got, "  - skill-c")
	// Flow array remnants must not leak into output.
	assert.NotContains(t, got, "skill-a,")
	assert.NotContains(t, got, "]")
	// Other frontmatter must be preserved.
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_InvalidSkillName(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\n---\nBody\n"

	tests := []struct {
		name string
		dir  string
	}{
		{"colon", "/path/to/my: skill"},
		{"hash", "/path/to/skill #beta"},
		{"bracket", "/path/to/skill[0]"},
		{"space", "/path/to/my skill"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := injectFrontmatterSkills([]byte(src), []string{tc.dir})
			require.Error(t, err, "should reject skill name with %s", tc.name)
			assert.Contains(t, err.Error(), "invalid skill name")
		})
	}
}

func TestInjectFrontmatterSkills_ScalarSkillsValue(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: my-single-skill\n---\nBody\n"
	_, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/new-skill"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skills field must be a YAML list")
}

func TestInjectFrontmatterSkills_BOMStrippedOnNoOp(t *testing.T) {
	t.Parallel()
	src := "\xEF\xBB\xBF---\nname: test\nskills:\n  - skill-a\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/skill-a"})
	require.NoError(t, err)
	// BOM should be stripped even when no skills are added (no-op path).
	assert.False(t, strings.HasPrefix(string(result), "\xEF\xBB\xBF"), "BOM should be stripped on no-op path")
	assert.Contains(t, string(result), "---\nname: test")
}

func TestInjectFrontmatterSkills_SkillsWithComment(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: # no skills yet\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/skill-a"})
	require.NoError(t, err)

	got := string(result)
	// New skill must be injected.
	assert.Contains(t, got, "  - skill-a")
	// Other frontmatter keys must be preserved (not silently skipped).
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_SkillsNull(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: null\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/skill-a"})
	require.NoError(t, err)

	got := string(result)
	// skills: null must be replaced with block-style list.
	assert.Contains(t, got, "  - skill-a")
	assert.NotContains(t, got, "null")
	// Other frontmatter keys must be preserved.
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_CRLFLineEndings(t *testing.T) {
	t.Parallel()
	src := "---\r\nname: test\r\nskills:\r\n  - existing\r\nmodel: opus\r\n---\r\nBody\r\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/new-skill"})
	require.NoError(t, err)

	got := string(result)
	// Injected lines must use CRLF to match existing content.
	assert.Contains(t, got, "  - new-skill\r\n")
	// Existing content must be preserved with CRLF.
	assert.Contains(t, got, "  - existing\r\n")
	assert.Contains(t, got, "model: opus\r\n")
	assert.Contains(t, got, "Body")
	// Must not contain bare LF (without preceding CR) in the frontmatter.
	frontmatter := got[:strings.Index(got, "Body")]
	for i, c := range frontmatter {
		if c == '\n' && (i == 0 || frontmatter[i-1] != '\r') {
			t.Errorf("found bare LF at position %d in frontmatter, expected CRLF", i)
		}
	}
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_CRLFNoFrontmatter(t *testing.T) {
	t.Parallel()
	src := "# Just a prompt\r\n\r\nDo things.\r\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/my-skill"})
	require.NoError(t, err)

	got := string(result)
	// Injected frontmatter must use CRLF to match the body.
	assert.Contains(t, got, "---\r\n")
	assert.Contains(t, got, "  - my-skill\r\n")
	assert.Contains(t, got, "# Just a prompt")
}

func TestInjectFrontmatterSkills_FlowCommentWithBracket(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: [\n  code-review, # see ] for details\n  security-review\n]\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/new-skill"})
	require.NoError(t, err)

	got := string(result)
	assert.Contains(t, got, "  - code-review")
	assert.Contains(t, got, "  - security-review")
	assert.Contains(t, got, "  - new-skill")
	// Flow array remnants must not leak into output.
	assert.NotContains(t, got, "code-review,")
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

func TestInjectFrontmatterSkills_AnchorNoDedup(t *testing.T) {
	t.Parallel()
	src := "---\nname: test\nskills: &defaults\n  - skill-a\nmodel: opus\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/skill-b"})
	require.NoError(t, err)

	got := string(result)
	assert.Equal(t, 1, strings.Count(got, "skill-a"), "skill-a should appear exactly once")
	assert.Contains(t, got, "  - skill-b")
	assert.Contains(t, got, "model: opus")
	assert.Contains(t, got, "Body")
	assertValidFrontmatter(t, result)
}

// assertValidFrontmatter checks that the result has valid YAML frontmatter
// delimiters and the YAML inside parses without error.
func assertValidFrontmatter(t *testing.T, data []byte) {
	t.Helper()
	def, err := parsePiAgent(data)
	require.NoError(t, err, "result should be a valid agent definition with frontmatter")
	require.NotNil(t, def)
}
