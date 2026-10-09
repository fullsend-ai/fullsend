package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveModel(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "openai/gpt-5.6-luna", EffectiveModel("openai/gpt-5.6-luna", "opus"),
		"the runner's resolved value wins")
	assert.Equal(t, "opus", EffectiveModel("", "opus"),
		"the agent definition is the fallback, as it is in buildPiRunCommand")
	assert.Empty(t, EffectiveModel("", ""), "neither: the runtime default applies")
}

func TestAgentDefinitionModel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}

	assert.Equal(t, "openai/gpt-5.6-luna",
		AgentDefinitionModel(write("with.md", "---\nname: a\nmodel: openai/gpt-5.6-luna\n---\nbody")))
	assert.Empty(t, AgentDefinitionModel(write("no-model.md", "---\nname: a\n---\nbody")))
	assert.Empty(t, AgentDefinitionModel(write("no-frontmatter.md", "just a body")))
	// Unreadable, unterminated or absent: "" and the runtime default; the
	// run fails on the same file later, in Bootstrap, with a real message.
	assert.Empty(t, AgentDefinitionModel(write("unterminated.md", "---\nname: a\nmodel: x\n")))
	assert.Empty(t, AgentDefinitionModel(filepath.Join(dir, "missing.md")))
	assert.Empty(t, AgentDefinitionModel(""))
}

func TestAgentDefinitionTools(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}

	read := func(p string) AgentToolAccess {
		t.Helper()
		access, err := AgentDefinitionTools(p)
		require.NoError(t, err, p)
		return access
	}

	a := read(write("string.md", "---\nname: a\ntools: Bash(gh,jq), Read, Workflow\n---\nbody"))
	assert.True(t, a.Listed)
	assert.Equal(t, []string{"Bash", "Read", "Workflow"}, a.Tools, "argument restrictions are stripped")

	a = read(write("list.md", "---\nname: a\ntools:\n  - Read\n  - Write\n---\nbody"))
	assert.True(t, a.Listed)
	assert.Equal(t, []string{"Read", "Write"}, a.Tools)

	a = read(write("empty.md", "---\nname: a\ntools: []\n---\nbody"))
	assert.True(t, a.Listed, "an empty list is a list: the agent gets no tools")
	assert.Empty(t, a.Tools)

	a = read(write("disallowed.md", "---\nname: a\ndisallowedTools: Workflow, Bash(rm)\n---\nbody"))
	assert.False(t, a.Listed)
	assert.Equal(t, []string{"Workflow", "Bash"}, a.Disallowed)

	for _, p := range []string{
		write("no-tools.md", "---\nname: a\n---\nbody"),
		write("no-frontmatter.md", "just a body"),
		"",
	} {
		assert.False(t, read(p).Listed, p)
	}

	// A definition that cannot be read or parsed is an error, never a
	// definition without a tools: entry.
	for _, p := range []string{
		write("unterminated.md", "---\nname: a\ntools: Read\n"),
		write("bad-entry.md", "---\nname: a\ntools: [Read, 42]\n---\nbody"),
		write("bad-disallowed.md", "---\nname: a\ndisallowedTools: {a: b}\n---\nbody"),
		filepath.Join(dir, "missing.md"),
	} {
		_, err := AgentDefinitionTools(p)
		assert.Error(t, err, p)
	}
}

// A malformed disallowedTools: fails only the tool-access read: the shared
// parser still yields the definition, so runtimes that never read the key
// (pi and codex bootstrap, the claude name check, model resolution) are
// unaffected.
func TestMalformedDisallowedToolsOnlyFailsToolAccess(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "a.md")
	require.NoError(t, os.WriteFile(p, []byte("---\nname: a\nmodel: opus\ntools: Read\ndisallowedTools: [Read, 42]\n---\nbody"), 0o644))
	data, err := os.ReadFile(p)
	require.NoError(t, err)

	def, err := parsePiAgent(data)
	require.NoError(t, err)
	assert.Equal(t, "a", def.Name)
	assert.Equal(t, []string{"Read"}, def.Tools)
	assert.Equal(t, "opus", AgentDefinitionModel(p))

	_, err = AgentDefinitionTools(p)
	require.Error(t, err)
	assert.Equal(t, "agent definition: disallowedTools: entries must be strings, got int", err.Error())
}
