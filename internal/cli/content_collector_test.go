package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// decodeOutputMessages unmarshals a collector's OutputMessages JSON and
// asserts the structural requirements of the GenAI output-messages schema:
// an array of messages, each with a role and a parts array, every part
// carrying a type.
func decodeOutputMessages(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &msgs), "OutputMessages must be valid JSON")
	for _, m := range msgs {
		require.Contains(t, m, "role", "schema requires role on every message")
		parts, ok := m["parts"].([]any)
		require.True(t, ok, "schema requires a parts array on every message")
		for _, p := range parts {
			part, ok := p.(map[string]any)
			require.True(t, ok)
			require.Contains(t, part, "type", "schema requires type on every part")
		}
	}
	return msgs
}

func partAt(t *testing.T, msgs []map[string]any, i int) map[string]any {
	t.Helper()
	require.NotEmpty(t, msgs)
	parts := msgs[0]["parts"].([]any)
	require.Greater(t, len(parts), i)
	return parts[i].(map[string]any)
}

func TestContentCollector_CoalescesContiguousDeltas(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: "Hello "})
	c.Handle(agentruntime.TextEvent{Text: "world"})
	c.Handle(agentruntime.ThinkingEvent{Text: "pondering"})

	res := c.Result()
	msgs := decodeOutputMessages(t, res.OutputMessages)
	require.Len(t, msgs, 1)
	assert.Equal(t, "assistant", msgs[0]["role"])

	text := partAt(t, msgs, 0)
	assert.Equal(t, "text", text["type"])
	assert.Equal(t, "Hello world", text["content"])

	reasoning := partAt(t, msgs, 1)
	assert.Equal(t, "reasoning", reasoning["type"])
	assert.Equal(t, "pondering", reasoning["content"])

	assert.Zero(t, res.DroppedBytes)
	assert.False(t, res.Truncated)
}

func TestContentCollector_ToolUseBecomesToolCallPart(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "ls -la"})

	msgs := decodeOutputMessages(t, c.Result().OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, "tool_call", part["type"])
	assert.Equal(t, "Bash", part["name"])
	assert.Equal(t, "ls -la", part["summary"])
	assert.NotContains(t, part, "arguments",
		"a summary is not the tool's arguments; do not fabricate them")
}

func TestContentCollector_RedactsSecretsAndSurfacesFindings(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: "the token is " + secret})

	res := c.Result()
	assert.NotContains(t, res.OutputMessages, secret,
		"secret must not survive assembly")
	assert.NotEmpty(t, res.Findings,
		"a redaction hit must surface as a security finding")
}

func TestContentCollector_PreservesCleanText(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: "nothing secret here"})

	res := c.Result()
	msgs := decodeOutputMessages(t, res.OutputMessages)
	assert.Equal(t, "nothing secret here", partAt(t, msgs, 0)["content"],
		"clean text must not be blanked by the empty-Sanitized convention")
	assert.Empty(t, res.Findings)
}

func TestContentCollector_BoundsTotalSizeOnRuneBoundary(t *testing.T) {
	c := newContentCollector(40)
	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("héllo ", 40)})
	c.Handle(agentruntime.ThinkingEvent{Text: "this reasoning does not fit at all"})

	res := c.Result()
	assert.True(t, res.Truncated)
	assert.Positive(t, res.DroppedBytes)

	msgs := decodeOutputMessages(t, res.OutputMessages)
	for _, p := range msgs[0]["parts"].([]any) {
		content, _ := p.(map[string]any)["content"].(string)
		assert.True(t, utf8.ValidString(content), "truncation must cut on a rune boundary")
	}
}

func TestContentCollector_DroppedBytesAccountsExactly(t *testing.T) {
	// The budget cut lands mid-rune ("é" is 2 bytes), so the rune-boundary
	// walk-back keeps fewer bytes than the budget allows. DroppedBytes must
	// equal original-minus-kept exactly, including that walk-back slack.
	c := newContentCollector(5)
	c.Handle(agentruntime.TextEvent{Text: "abcdéfgh"}) // 9 bytes; é occupies [4:6]

	res := c.Result()
	require.True(t, res.Truncated)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	kept := partAt(t, msgs, 0)["content"].(string)
	assert.Equal(t, "abcd", kept, "a cut at byte 5 lands inside é and walks back to 4")
	assert.Equal(t, 9-len(kept), res.DroppedBytes,
		"DroppedBytes must be exactly original bytes minus kept bytes")
}

func TestContentCollector_NilIsInert(t *testing.T) {
	var c *contentCollector
	assert.NotPanics(t, func() {
		c.Handle(agentruntime.TextEvent{Text: "x"})
	})
	assert.Empty(t, c.Result().OutputMessages)
}

func TestContentCollector_IgnoresNonContentEvents(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.InitEvent{Model: "m"})
	c.Handle(agentruntime.TokensEvent{InputTokens: 10})
	c.Handle(agentruntime.ResultEvent{NumTurns: 1})
	c.Handle(agentruntime.ErrorEvent{Message: "boom"})

	res := c.Result()
	assert.Empty(t, res.OutputMessages, "no content events means no content")
	assert.Zero(t, res.DroppedBytes)
}

func TestContentCollector_EmptyDeltasProduceNoParts(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: ""})

	assert.Empty(t, c.Result().OutputMessages)
}
