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
