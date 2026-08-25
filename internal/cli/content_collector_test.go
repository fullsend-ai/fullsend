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
// an array of messages, each with a role, a parts array, and the
// REQUIRED finish_reason (the schema's OutputMessage.required is
// ["role","parts","finish_reason"]); every part carries a type.
func decodeOutputMessages(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &msgs), "OutputMessages must be valid JSON")
	for _, m := range msgs {
		require.Contains(t, m, "role", "schema requires role on every message")
		require.Contains(t, m, "finish_reason", "schema requires finish_reason on every message")
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

	res := c.Result("stop")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	require.Len(t, msgs, 1)
	assert.Equal(t, "assistant", msgs[0]["role"])
	assert.Equal(t, "stop", msgs[0]["finish_reason"])

	text := partAt(t, msgs, 0)
	assert.Equal(t, "text", text["type"])
	assert.Equal(t, "Hello world", text["content"])

	reasoning := partAt(t, msgs, 1)
	assert.Equal(t, "reasoning", reasoning["type"])
	assert.Equal(t, "pondering", reasoning["content"])

	assert.Zero(t, res.DroppedBytes)
	assert.False(t, res.Truncated)
}

func TestContentCollector_FinishReasonError(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: "partial answer before the crash"})

	msgs := decodeOutputMessages(t, c.Result("error").OutputMessages)
	assert.Equal(t, "error", msgs[0]["finish_reason"],
		"a failed iteration's message must carry finish_reason=error")
}

func TestContentCollector_ToolUseBecomesToolCallPart(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "ls -la"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
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

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret,
		"secret must not survive assembly")
	assert.NotEmpty(t, res.Findings,
		"a redaction hit must surface as a security finding")
}

func TestContentCollector_RedactsToolCallNameAndSummary(t *testing.T) {
	secret := "ghp_" + strings.Repeat("b", 36)
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "curl -H " + secret, Summary: "auth " + secret})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret,
		"tool_call name and summary are captured content and must be redacted")
}

func TestContentCollector_PreservesCleanText(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: "nothing secret here"})

	res := c.Result("stop")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	assert.Equal(t, "nothing secret here", partAt(t, msgs, 0)["content"],
		"clean text must not be blanked by the empty-Sanitized convention")
	assert.Empty(t, res.Findings)
}

func TestContentCollector_SanitizedToEmptyDoesNotLeakRaw(t *testing.T) {
	// All-null-byte text sanitizes to "", which collides with the
	// pipeline's empty-Sanitized-means-unchanged convention. The raw
	// bytes must NOT pass through; the finding must still be counted.
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: "\x00\x00\x00"})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, "\\u0000",
		"null bytes must not survive assembly")
	assert.NotEmpty(t, res.Findings, "the normalizer's finding must be counted")
}

func TestContentCollector_BudgetKeepsTheEnding(t *testing.T) {
	// The budget keeps an ordered SUFFIX: the iteration's ending — the
	// final answer — is what consumers judge, so overflow drops the
	// oldest content first.
	c := newContentCollector(40)
	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("early ", 40)})
	c.Handle(agentruntime.ThinkingEvent{Text: "last reasoning"})
	c.Handle(agentruntime.TextEvent{Text: "final answer"})

	res := c.Result("stop")
	assert.True(t, res.Truncated)
	assert.Positive(t, res.DroppedBytes)

	msgs := decodeOutputMessages(t, res.OutputMessages)
	parts := msgs[0]["parts"].([]any)
	last := parts[len(parts)-1].(map[string]any)
	assert.Equal(t, "final answer", last["content"],
		"the ending must survive truncation intact")
	for _, p := range parts {
		content, _ := p.(map[string]any)["content"].(string)
		assert.True(t, utf8.ValidString(content), "any cut must land on a rune boundary")
	}
}

func TestContentCollector_DroppedBytesAccountsExactly(t *testing.T) {
	// Suffix policy on a single oversized part keeps the TAIL. A cut at
	// 5 bytes from the end of "abcdéfgh" (9 bytes, é at [4:6]) lands at
	// byte 4 — a rune start — keeping "éfgh" (5 bytes), dropping 4.
	c := newContentCollector(5)
	c.Handle(agentruntime.TextEvent{Text: "abcdéfgh"})

	res := c.Result("stop")
	require.True(t, res.Truncated)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	kept := partAt(t, msgs, 0)["content"].(string)
	assert.Equal(t, "éfgh", kept)
	assert.Equal(t, 9-len(kept), res.DroppedBytes,
		"DroppedBytes must be exactly original bytes minus kept bytes")
}

func TestContentCollector_DroppedBytesCountToolCallNameBytes(t *testing.T) {
	// A dropped tool_call part's budget footprint includes its Name —
	// the docs define name+summary as captured content, so "exact
	// dropped-byte accounting" must count both.
	c := newContentCollector(10)
	c.Handle(agentruntime.ToolUseEvent{Name: "0123456789ABCDEF", Summary: "0123456789"})
	c.Handle(agentruntime.TextEvent{Text: "final"})

	res := c.Result("stop")
	require.True(t, res.Truncated)
	assert.Equal(t, 16+10, res.DroppedBytes,
		"the dropped tool_call must account for name and summary bytes")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	assert.Equal(t, "final", partAt(t, msgs, 0)["content"])
}

func TestContentCollector_NilIsInert(t *testing.T) {
	var c *contentCollector
	assert.NotPanics(t, func() {
		c.Handle(agentruntime.TextEvent{Text: "x"})
	})
	assert.Empty(t, c.Result("stop").OutputMessages)
}

func TestContentCollector_IgnoresNonContentEvents(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.InitEvent{Model: "m"})
	c.Handle(agentruntime.TokensEvent{InputTokens: 10})
	c.Handle(agentruntime.ResultEvent{NumTurns: 1})
	c.Handle(agentruntime.ErrorEvent{Message: "boom"})

	res := c.Result("stop")
	assert.Empty(t, res.OutputMessages, "no content events means no content")
	assert.Zero(t, res.DroppedBytes)
}

func TestContentCollector_EmptyDeltasProduceNoParts(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.TextEvent{Text: ""})

	assert.Empty(t, c.Result("stop").OutputMessages)
}

func TestContentCollector_MidRuneTailCutWalksForward(t *testing.T) {
	// A cut at 4 bytes from the end of "abcdéfgh" lands inside é (bytes
	// [4:6]); the boundary walk must move FORWARD, keeping "fgh" (3
	// bytes) and dropping 6 — never splitting the rune.
	c := newContentCollector(4)
	c.Handle(agentruntime.TextEvent{Text: "abcdéfgh"})

	res := c.Result("stop")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	kept := partAt(t, msgs, 0)["content"].(string)
	assert.Equal(t, "fgh", kept)
	assert.Equal(t, 9-len(kept), res.DroppedBytes)
}

func TestContentCollector_PreTrimRedactsBeforeCutting(t *testing.T) {
	// A secret straddling the eviction pre-trim boundary must be redacted
	// BEFORE the head cut: trimming raw bytes first would split the secret
	// so the redactor no longer recognizes the surviving fragment. With
	// maxBytes=100 the deltas below total 250 (>2*100), and a raw-first
	// trim would keep a 30-byte tail of the token verbatim.
	// The tail after the secret uses "! " — characters outside the token
	// alphabet — so the greedy token pattern cannot swallow it.
	secret := "ghp_" + strings.Repeat("a", 36)
	tail := strings.Repeat("! ", 35)
	c := newContentCollector(100)
	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("x", 140)})
	c.Handle(agentruntime.TextEvent{Text: secret})
	c.Handle(agentruntime.TextEvent{Text: tail})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, strings.Repeat("a", 10),
		"no fragment of a boundary-straddling secret may survive the pre-trim")
	assert.NotEmpty(t, res.Findings,
		"the pre-trim redaction hit must surface as a security finding")

	msgs := decodeOutputMessages(t, res.OutputMessages)
	kept := partAt(t, msgs, 0)["content"].(string)
	assert.True(t, strings.HasSuffix(kept, tail),
		"the ending must still survive the pre-trim")
}

func TestContentCollector_EvictedPartsAreStillScanned(t *testing.T) {
	// Whole parts evicted during accumulation never reach Result's redact
	// pass, but their findings must still be counted —
	// fullsend.content.redactions is documented to include findings from
	// parts the size budget later dropped.
	secret := "ghp_" + strings.Repeat("c", 36)
	c := newContentCollector(30)
	c.Handle(agentruntime.TextEvent{Text: "leak: " + secret})
	c.Handle(agentruntime.ThinkingEvent{Text: strings.Repeat("z", 30)})
	c.Handle(agentruntime.TextEvent{Text: "the end"})

	require.LessOrEqual(t, len(c.parts), 2,
		"the secret-bearing part must have been evicted during accumulation")

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret)
	assert.NotEmpty(t, res.Findings,
		"findings inside evicted parts must still be counted")
}

func TestContentCollector_EvictsWholeOldPartsExactly(t *testing.T) {
	// Long sessions must not accumulate unbounded content: parts older
	// than the suffix budget are evicted during Handle, and every
	// evicted byte still lands in DroppedBytes.
	c := newContentCollector(30)
	c.Handle(agentruntime.ToolUseEvent{Name: "OldTool", Summary: strings.Repeat("x", 33)})
	c.Handle(agentruntime.ThinkingEvent{Text: strings.Repeat("y", 30)})
	c.Handle(agentruntime.TextEvent{Text: "the very end"})

	require.LessOrEqual(t, len(c.parts), 2, "the old tool_call must have been evicted during accumulation")

	res := c.Result("stop")
	require.True(t, res.Truncated)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	parts := msgs[0]["parts"].([]any)
	last := parts[len(parts)-1].(map[string]any)
	assert.Equal(t, "the very end", last["content"])

	kept := 0
	for _, p := range parts {
		m := p.(map[string]any)
		for _, k := range []string{"content", "name", "summary"} {
			if v, ok := m[k].(string); ok {
				kept += len(v)
			}
		}
	}
	assert.Equal(t, (7+33)+30+12-kept, res.DroppedBytes,
		"evicted and budget-dropped bytes must sum exactly to original minus kept")
}

func TestTailToRuneBoundary(t *testing.T) {
	// The fits case is production-reachable: eviction pre-trims on
	// post-redaction content, which masking can shrink under the bound.
	assert.Equal(t, "fits", tailToRuneBoundary("fits", 10))
	assert.Equal(t, "fits", tailToRuneBoundary("fits", 4))
	assert.Equal(t, "", tailToRuneBoundary("anything", 0))
	assert.Equal(t, "défgh", tailToRuneBoundary("abcdéfgh", 6),
		"cut landing on a rune start keeps the full tail")
	assert.Equal(t, "fgh", tailToRuneBoundary("abcdéfgh", 4),
		"cut landing mid-rune walks forward, never splitting the rune")
}
