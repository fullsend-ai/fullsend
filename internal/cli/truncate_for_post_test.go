package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/sticky"
)

// truncateForPost caps a body at cfg's limit before the create. The edges
// are exercised directly here — an unset MaxSize falls back to
// sticky.DefaultMaxSize and a small budget truncates with the note — so
// the cap arithmetic is cheap to diagnose when the budget logic changes.
func TestTruncateForPost(t *testing.T) {
	const note = "\n\n*Comment truncated due to comment size limits.*"
	body := strings.Repeat("x", 100)

	t.Run("unset MaxSize falls back to the default and keeps a default-sized body", func(t *testing.T) {
		got := truncateForPost(body, sticky.Config{})
		assert.Equal(t, body, got, "a body well under DefaultMaxSize passes unchanged")
	})

	t.Run("unset MaxSize falls back to the default and truncates an oversized body", func(t *testing.T) {
		oversized := strings.Repeat("x", sticky.DefaultMaxSize+1000)
		got := truncateForPost(oversized, sticky.Config{})
		assert.Len(t, got, sticky.DefaultMaxSize, "the body is capped at DefaultMaxSize")
		assert.True(t, strings.HasSuffix(got, note), "the truncation note is appended")
	})

	t.Run("a set MaxSize is honoured", func(t *testing.T) {
		// The cap must leave room for the truncation note itself.
		got := truncateForPost(body, sticky.Config{MaxSize: 80})
		assert.Len(t, got, 80, "the body is capped at the configured MaxSize")
		assert.True(t, strings.HasSuffix(got, note), "the truncation note is appended")
	})

	t.Run("a body within a small budget passes whole", func(t *testing.T) {
		got := truncateForPost("short", sticky.Config{MaxSize: 80})
		assert.Equal(t, "short", got, "a body within the budget is not truncated")
	})
}

// sticky.Config.EffectiveMaxSize owns the fallback the doors share: the
// table pins the unset, zero and set cases.
func TestEffectiveMaxSize(t *testing.T) {
	assert.Equal(t, sticky.DefaultMaxSize, sticky.Config{}.EffectiveMaxSize(), "unset MaxSize falls back to the default")
	assert.Equal(t, sticky.DefaultMaxSize, sticky.Config{MaxSize: 0}.EffectiveMaxSize(), "a zero MaxSize falls back to the default")
	assert.Equal(t, 1234, sticky.Config{MaxSize: 1234}.EffectiveMaxSize(), "a set MaxSize is returned as is")
}
