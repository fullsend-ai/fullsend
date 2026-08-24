package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type namedDebugLog struct {
	OpenCodeRuntime
	name string
}

func (n namedDebugLog) DebugLogName() string { return n.name }

func TestDebugLogNameFor(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "claude-debug.log", DebugLogNameFor(ClaudeRuntime{}))
	assert.Equal(t, DefaultDebugLogName, DebugLogNameFor(OpenCodeRuntime{}))
	assert.Equal(t, piDebugLogFile, DebugLogNameFor(PiRuntime{}))
	assert.Equal(t, DefaultDebugLogName, DebugLogNameFor(DummyRuntime{}))
	assert.Equal(t, "other.log", DebugLogNameFor(namedDebugLog{name: "other.log"}))
	// An empty name falls back to the default rather than producing "".
	assert.Equal(t, DefaultDebugLogName, DebugLogNameFor(namedDebugLog{}))
	// Backends resolved from the registry expose the transcript handler.
	b, err := Resolve("claude")
	if assert.NoError(t, err) {
		assert.Equal(t, "claude-debug.log", DebugLogNameFor(b.Runtime, b.Transcripts))
	}
	// A backend whose Runtime alone implements DebugLogNamer is honoured when
	// the runner passes both components (Runtime first, then Transcripts).
	assert.Equal(t, "rt.log", DebugLogNameFor(namedDebugLog{name: "rt.log"}, OpenCodeRuntime{}))
	assert.Equal(t, "tx.log", DebugLogNameFor(OpenCodeRuntime{}, namedDebugLog{name: "tx.log"}))
	assert.Equal(t, DefaultDebugLogName, DebugLogNameFor())
}

func TestWantsClaudeMDBridge(t *testing.T) {
	t.Parallel()
	assert.True(t, WantsClaudeMDBridge(ClaudeRuntime{}))
	assert.False(t, WantsClaudeMDBridge(OpenCodeRuntime{}))
	assert.False(t, WantsClaudeMDBridge(PiRuntime{}))
	assert.False(t, WantsClaudeMDBridge(DummyRuntime{}))
}
