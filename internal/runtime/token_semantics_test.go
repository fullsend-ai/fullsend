package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// TestOutputExcludesReasoning_ClassifiesEveryRuntime lists every name in
// config.ValidRuntimes, so a new one fails here until someone has read how
// its parser fills OutputTokens and ReasoningTokens.
func TestOutputExcludesReasoning_ClassifiesEveryRuntime(t *testing.T) {
	want := map[string]bool{
		"claude":         false,
		"pi":             false,
		"codex":          true,
		"opencode":       true,
		"dummy":          false,
		"dummy-playback": false,
	}
	for _, name := range config.ValidRuntimes() {
		excludes, classified := want[name]
		require.True(t, classified,
			"runtime %q is not classified: add it here and to OutputExcludesReasoning", name)
		assert.Equal(t, excludes, OutputExcludesReasoning(name), "runtime %q", name)
	}
	assert.False(t, OutputExcludesReasoning("no-such-runtime"),
		"an unknown runtime name is treated as counting reasoning inside output")
}

// TestOutputExcludesReasoning_MatchesCodexCounters ties the codex entry to
// what the parser does: counters() takes reasoning out of the wire output, so
// output plus reasoning is the wire total again.
func TestOutputExcludesReasoning_MatchesCodexCounters(t *testing.T) {
	require.True(t, OutputExcludesReasoning(CodexRuntime{}.Name()))

	wire := codexUsage{OutputTokens: 700, ReasoningOutputTokens: 200}
	c := wire.counters()
	assert.Equal(t, 500, c.Output)
	assert.Equal(t, 200, c.Reasoning)
	assert.Equal(t, wire.OutputTokens, c.Output+c.Reasoning)
}
