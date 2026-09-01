package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_injectFrontmatterSkills_ExistingSkillsDedup(t *testing.T) {
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

func Test_injectFrontmatterSkills_NoExistingSkills(t *testing.T) {
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

func Test_injectFrontmatterSkills_NoFrontmatter(t *testing.T) {
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

func Test_injectFrontmatterSkills_EmptySkillDirs(t *testing.T) {
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

func Test_injectFrontmatterSkills_AllAlreadyPresent(t *testing.T) {
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

func Test_injectFrontmatterSkills_PreservesOtherFrontmatter(t *testing.T) {
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

func Test_injectFrontmatterSkills_DeterministicOrder(t *testing.T) {
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

func Test_injectFrontmatterSkills_EmptyStringDirs(t *testing.T) {
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

func Test_injectFrontmatterSkills_SkillsAfterBody(t *testing.T) {
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

func Test_injectFrontmatterSkills_FlowStyleSkills(t *testing.T) {
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

func Test_injectFrontmatterSkills_FlowStyleEmpty(t *testing.T) {
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

func Test_injectFrontmatterSkills_CommentsInSkillsBlock(t *testing.T) {
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

func Test_injectFrontmatterSkills_BOM(t *testing.T) {
	t.Parallel()
	src := "\xef\xbb\xbf---\nname: test\n---\nBody\n"
	result, err := injectFrontmatterSkills([]byte(src), []string{"/path/to/skill-a"})
	require.NoError(t, err)
	assert.Contains(t, string(result), "  - skill-a")
	assert.Contains(t, string(result), "Body")
}

// assertValidFrontmatter checks that the result has valid YAML frontmatter
// delimiters and the YAML inside parses without error.
func assertValidFrontmatter(t *testing.T, data []byte) {
	t.Helper()
	def, err := parsePiAgent(data)
	require.NoError(t, err, "result should be a valid agent definition with frontmatter")
	require.NotNil(t, def)
}
