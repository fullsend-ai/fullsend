package runtime

import (
	"testing"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePiAgent_FleetTriageShape(t *testing.T) {
	t.Parallel()
	src := `---
name: triage
description: Inspect an issue, assess information sufficiency, and produce a structured triage decision.
skills:
  - issue-labels
# comment lines are allowed in the frontmatter
tools: Bash(gh,curl,jq),Skill
model: opus
---

You are the triage agent.

## Steps
1. Read the issue.
`
	def, err := parsePiAgent([]byte(src))
	require.NoError(t, err)
	assert.Equal(t, "triage", def.Name)
	assert.Equal(t, "opus", def.Model)
	assert.Contains(t, def.Description, "structured triage decision")
	assert.Equal(t, []string{"Bash", "Skill"}, def.Tools)
	assert.Equal(t, []string{"gh", "curl", "jq"}, def.BashAllowlist)
	assert.True(t, len(def.Body) > 0 && def.Body[0] == 'Y', "body starts after the frontmatter: %q", def.Body[:20])
	assert.Contains(t, def.Body, "## Steps")
	assert.NotContains(t, def.Body, "---")
}

func TestParsePiAgent_NoToolsMeansDefault(t *testing.T) {
	t.Parallel()
	src := "---\nname: code\ndescription: >-\n  Implementation specialist.\n  Second line.\nmodel: opus\nskills:\n  - code-implementation\n---\nBody here\n"
	def, err := parsePiAgent([]byte(src))
	require.NoError(t, err)
	assert.Nil(t, def.Tools, "absent tools entry keeps the runtime default")
	assert.Nil(t, def.BashAllowlist)
	assert.Equal(t, "Implementation specialist. Second line.", def.Description)
	assert.Equal(t, "Body here", def.Body)
}

func TestParsePiAgent_ToolsListForm(t *testing.T) {
	t.Parallel()
	src := "---\nname: x\ntools:\n  - Read\n  - Bash(go, make)\n  - Grep,Glob\n---\n"
	def, err := parsePiAgent([]byte(src))
	require.NoError(t, err)
	assert.Equal(t, []string{"Read", "Bash", "Grep", "Glob"}, def.Tools)
	assert.Equal(t, []string{"go", "make"}, def.BashAllowlist)
}

func TestParsePiAgent_UnrestrictedBashAndEmptyTools(t *testing.T) {
	t.Parallel()
	def, err := parsePiAgent([]byte("---\nname: x\ntools: Bash,Read\n---\nb"))
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash", "Read"}, def.Tools)
	assert.Nil(t, def.BashAllowlist)

	def, err = parsePiAgent([]byte("---\nname: x\ntools: \"\"\n---\nb"))
	require.NoError(t, err)
	assert.Equal(t, []string{}, def.Tools, "explicitly empty tools is a restriction, not the default")
}

func TestParsePiAgent_FenceIsExactLine(t *testing.T) {
	t.Parallel()
	// CRLF, a "---" that only starts a line inside the YAML, and a "---"
	// horizontal rule in the body.
	src := "---\r\nname: x\r\ndescription: >-\r\n  --- not a fence\r\n  still yaml\r\n---\r\nBody line\r\n\r\n---\r\n\r\nAfter rule\r\n"
	def, err := parsePiAgent([]byte(src))
	require.NoError(t, err)
	assert.Equal(t, "x", def.Name)
	assert.Equal(t, "--- not a fence still yaml", def.Description)
	assert.Contains(t, def.Body, "Body line")
	assert.Contains(t, def.Body, "After rule")

	// An indented "---" inside a block scalar (a markdown rule in the
	// description) is YAML content, not the closing fence; tools: after it
	// must survive.
	def, err = parsePiAgent([]byte("---\nname: z\ndescription: |\n  Intro\n  ---\n  More\ntools: Bash(gh)\n---\nbody"))
	require.NoError(t, err)
	assert.Equal(t, "Intro\n---\nMore", def.Description)
	assert.Equal(t, []string{"Bash"}, def.Tools)
	assert.Equal(t, "body", def.Body)

	// Trailing whitespace on a fence is tolerated; the restriction must not
	// be dropped silently.
	def, err = parsePiAgent([]byte("--- \nname: y\ntools: Bash(gh)\n---\t\nbody"))
	require.NoError(t, err)
	assert.Equal(t, "y", def.Name)
	assert.Equal(t, []string{"Bash"}, def.Tools)

	// A first line that starts with --- but is not a fence is an error, not
	// an all-body file with the tools: restriction lost.
	_, err = parsePiAgent([]byte("----\nname: y\n---\nbody"))
	require.ErrorContains(t, err, "not a frontmatter fence")
	_, err = parsePiAgent([]byte("---x: y\n---\nbody"))
	require.ErrorContains(t, err, "not a frontmatter fence")
}

func TestParsePiAgent_NoFrontmatter(t *testing.T) {
	t.Parallel()
	def, err := parsePiAgent([]byte("# Just a prompt\n\nDo things.\n"))
	require.NoError(t, err)
	assert.Empty(t, def.Name)
	assert.Nil(t, def.Tools)
	assert.Equal(t, "# Just a prompt\n\nDo things.", def.Body)
}

func TestParsePiAgent_Errors(t *testing.T) {
	t.Parallel()
	_, err := parsePiAgent([]byte("---\nname: x\nno end"))
	require.ErrorContains(t, err, "unterminated frontmatter")

	_, err = parsePiAgent([]byte("---\ntools:\n  - 42\n---\n"))
	require.ErrorContains(t, err, "must be strings")

	_, err = parsePiAgent([]byte("---\ntools:\n  a: b\n---\n"))
	require.ErrorContains(t, err, "string or list")

	_, err = parsePiAgent([]byte("---\nname: [\n---\n"))
	require.ErrorContains(t, err, "parsing frontmatter")
}

func TestSplitTopLevelCommas(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"Bash(gh,curl,jq)", "Skill"}, splitTopLevelCommas("Bash(gh,curl,jq),Skill"))
	assert.Equal(t, []string{"Read", "Write"}, splitTopLevelCommas(" Read , Write ,"))
	assert.Equal(t, []string{}, splitTopLevelCommas(""))
}

func TestPiToolsFor(t *testing.T) {
	t.Parallel()
	tools, unsupported := piToolsFor(nil)
	assert.Nil(t, tools)
	assert.Nil(t, unsupported)

	tools, unsupported = piToolsFor([]string{"Bash", "Skill"})
	assert.Equal(t, []string{"bash"}, tools, "Skill is native in pi, not a tool")
	assert.Nil(t, unsupported)

	tools, unsupported = piToolsFor([]string{"Read", "Edit", "MultiEdit", "Glob", "WebFetch", "Task", "LS"})
	assert.Equal(t, []string{"read", "edit", "find", "ls"}, tools)
	assert.Equal(t, []string{"WebFetch", "Task"}, unsupported)

	tools, unsupported = piToolsFor([]string{"Skill"})
	assert.Equal(t, []string{}, tools, "restriction with no pi tools stays non-nil")
	assert.Nil(t, unsupported)
}

func TestPiToolNameMapsAreInverse(t *testing.T) {
	t.Parallel()
	for claude, pi := range piToolForClaude {
		if claude == "MultiEdit" {
			// MultiEdit and Edit both map onto pi's single edit tool, so the
			// adapter reports every pi edit as "Edit". An agent allowlisted
			// only for MultiEdit is therefore blocked under pi as a plain
			// tool_blocked — a renaming gap the hook's case-variant diagnostic
			// (#608) cannot see. Documented in docs/runtimes.md; not a bug in
			// the maps.
			continue
		}
		assert.Equal(t, claude, claudeToolForPi[pi], "pi tool %q must map back to %q", pi, claude)
	}
	for pi, claude := range claudeToolForPi {
		assert.Equal(t, pi, piToolForClaude[claude], "Claude tool %q must map back to %q", claude, pi)
	}
}

// The hook scripts only recognise canonical (or legacy) Claude names, so the
// adapter's translation table must stay inside that vocabulary (#608).
func TestPiToolNameMapsUseClaudeVocabulary(t *testing.T) {
	t.Parallel()
	for pi, claude := range claudeToolForPi {
		assert.True(t, security.KnownClaudeTool(claude),
			"claudeToolForPi[%q] = %q is not a canonical or legacy Claude tool name", pi, claude)
	}
	for claude := range piToolForClaude {
		assert.True(t, security.KnownClaudeTool(claude),
			"piToolForClaude key %q is not a canonical or legacy Claude tool name", claude)
	}
}
