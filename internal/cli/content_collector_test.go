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
			if part["type"] == "tool_call_response" {
				require.Contains(t, part, "response",
					"the schema's ToolCallResponsePart.required is [\"type\",\"response\"] — the key must be present even for an empty result")
			}
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

func TestContentCollector_ToolCallPartCarriesID(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_09qrs", Name: "Read", Summary: "/src/main.go"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, "toolu_09qrs", part["id"],
		"tool_call parts carry the id that correlates them with tool_call_response parts")
}

func TestContentCollector_IDlessToolCallOmitsIDKey(t *testing.T) {
	// Runtimes without wire-format call ids (and the assistant fallback
	// path before ids existed) emit ID-less events; the schema's id is
	// optional, so the key is omitted rather than serialized empty.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "ls"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	assert.NotContains(t, partAt(t, msgs, 0), "id")
}

func TestContentCollector_ToolResultBecomesToolCallResponsePart(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_01abc", Result: "main.go\nutil.go\n"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, "tool_call_response", part["type"])
	assert.Equal(t, "toolu_01abc", part["id"])
	assert.Equal(t, "main.go\nutil.go\n", part["response"],
		"the schema's result field is named response, not result")
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

func TestContentCollector_RedactsToolResultResponse(t *testing.T) {
	// Tool results are the highest-secret-density field in the stream —
	// they carry file contents and command output verbatim.
	secret := "ghp_" + strings.Repeat("d", 36)
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_06sec", Result: "config dump: " + secret})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret,
		"a secret inside a tool result must not survive assembly")
	assert.NotEmpty(t, res.Findings)
}

func TestContentCollector_EmptyToolResultProducesNoPart(t *testing.T) {
	// An empty result carries no content-bearing bytes; like text
	// sanitized to empty, it produces no part — which also means no
	// tool_call_response part ever omits its schema-required response key.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_07empty", Result: ""})

	assert.Empty(t, c.Result("stop").OutputMessages)
}

func TestContentCollector_ToolResultsStayDiscrete(t *testing.T) {
	// Unlike text/reasoning deltas, tool results never coalesce.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_a", Result: "one"})
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_b", Result: "two"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	parts := msgs[0]["parts"].([]any)
	require.Len(t, parts, 2)
	assert.Equal(t, "toolu_a", parts[0].(map[string]any)["id"])
	assert.Equal(t, "toolu_b", parts[1].(map[string]any)["id"])
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

func TestContentCollector_EvictedToolResultsAreStillScanned(t *testing.T) {
	// Response bytes in parts evicted during accumulation must be scanned
	// exactly like Content/Name/Summary — findings count even when the
	// budget drops the part.
	secret := "ghp_" + strings.Repeat("e", 36)
	c := newContentCollector(30)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_ev", Result: "leak: " + secret})
	c.Handle(agentruntime.ThinkingEvent{Text: strings.Repeat("z", 30)})
	c.Handle(agentruntime.TextEvent{Text: "the end"})

	require.LessOrEqual(t, len(c.parts), 2,
		"the secret-bearing tool result must have been evicted during accumulation")

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret)
	assert.NotEmpty(t, res.Findings,
		"findings inside evicted tool results must still be counted")
}

func TestContentCollector_BoundaryToolResultKeepsResponseTail(t *testing.T) {
	// A tool_call_response at the suffix boundary is tail-cut on its
	// response — like text, and unlike tool_call (whose name+summary
	// would be misrepresented by a cut). The id survives the trim and
	// its 9 bytes occupy budget first: "final" (5) leaves 15, the id
	// takes 9, so 6 response bytes fit.
	c := newContentCollector(20)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_cut", Result: "0123456789ABCDEFGHIJ"})
	c.Handle(agentruntime.TextEvent{Text: "final"})

	res := c.Result("stop")
	require.True(t, res.Truncated)
	assert.Equal(t, 14, res.DroppedBytes,
		"exactly the response bytes that did not fit next to the id are dropped")

	msgs := decodeOutputMessages(t, res.OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, "tool_call_response", part["type"])
	assert.Equal(t, "toolu_cut", part["id"])
	assert.Equal(t, "EFGHIJ", part["response"])
	assert.Equal(t, "final", partAt(t, msgs, 1)["content"])
}

func TestContentCollector_DroppedToolResultCountsResponseBytes(t *testing.T) {
	// When no budget remains at the boundary, the whole response part
	// drops and its response and id bytes land in DroppedBytes exactly.
	c := newContentCollector(5)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_drop", Result: "0123456789"})
	c.Handle(agentruntime.TextEvent{Text: "final"})

	res := c.Result("stop")
	require.True(t, res.Truncated)
	assert.Equal(t, 20, res.DroppedBytes)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	require.Len(t, msgs[0]["parts"].([]any), 1)
	assert.Equal(t, "final", partAt(t, msgs, 0)["content"])
}

func TestContentCollector_PreTrimRedactsGiantToolResult(t *testing.T) {
	// The over-double-budget pre-trim must operate on a lone
	// tool_call_response's response field with the same
	// redact-before-cut invariant as text content.
	secret := "ghp_" + strings.Repeat("f", 36)
	tail := strings.Repeat("! ", 35)
	c := newContentCollector(100)
	c.Handle(agentruntime.ToolResultEvent{
		ID:     "toolu_giant",
		Result: strings.Repeat("x", 140) + secret + tail,
	})

	require.LessOrEqual(t, c.total, 2*c.maxBytes,
		"a lone giant tool result must be pre-trimmed to bound memory")

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, strings.Repeat("f", 10),
		"no fragment of a boundary-straddling secret may survive the pre-trim")
	assert.NotEmpty(t, res.Findings)

	msgs := decodeOutputMessages(t, res.OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, "toolu_giant", part["id"])
	assert.True(t, strings.HasSuffix(part["response"].(string), tail),
		"the response ending must survive the pre-trim")
}

func TestContentCollector_ErrorToolResultCarriesErrorKey(t *testing.T) {
	// A failed call's part carries is_error; successful parts omit the
	// key. additionalProperties permits the sibling, same latitude as
	// tool_call's summary.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_err", Result: "exit 1", IsError: true})
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_ok", Result: "done"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	failed := partAt(t, msgs, 0)
	assert.Equal(t, true, failed["is_error"])
	assert.NotContains(t, partAt(t, msgs, 1), "is_error")
}

func TestContentCollector_CutPartsCarryTruncatedMarker(t *testing.T) {
	// Every cut part says so: a scorer must not read a fragment as a
	// whole result. The marker is structural (outside the accounting)
	// and absent on untouched parts.
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_big", Result: strings.Repeat("a", maxToolResultBytes+100)})
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_ok", Result: "small"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	assert.Equal(t, true, partAt(t, msgs, 0)["fullsend.truncated"],
		"a cap-cut response must be marked")
	assert.NotContains(t, partAt(t, msgs, 1), "fullsend.truncated")
}

func TestContentCollector_BoundaryTrimMarksPart(t *testing.T) {
	c := newContentCollector(20)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_cut", Result: "0123456789ABCDEFGHIJ"})
	c.Handle(agentruntime.TextEvent{Text: "final"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	assert.Equal(t, true, partAt(t, msgs, 0)["fullsend.truncated"],
		"a boundary-trimmed part must be marked")
	assert.NotContains(t, partAt(t, msgs, 1), "fullsend.truncated",
		"the intact ending stays unmarked")
}

func TestContentCollector_BareOAuthTokenInResultRedacted(t *testing.T) {
	// Tool results carry raw command stdout; a bare GCP bearer token —
	// the credential class WIF-provisioned runs actually handle — must
	// not survive to the span.
	token := "ya29.a0AfB_byDEMOtoken1234567890abcdefghij"
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_gcp", Result: "access token: " + token})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, "a0AfB_byDEMO")
	assert.NotEmpty(t, res.Findings)
}

func TestContentCollector_RedactionUnderCapDoesNotMarkTruncated(t *testing.T) {
	// Redaction can shrink an over-cap response below the cap; nothing
	// is cut then, so neither the part marker nor the span truncation
	// state may fire. The 40-byte secret masks to 7, shrinking an
	// 8,212-byte response to 8,179 — under the 8,192 cap.
	secret := "ghp_" + strings.Repeat("j", 36)
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolResultEvent{
		ID:     "toolu_shrink",
		Result: strings.Repeat("x", maxToolResultBytes+20-len(secret)) + secret,
	})

	res := c.Result("stop")
	assert.False(t, res.Truncated, "nothing was cut — redaction shrink is not truncation")
	assert.Zero(t, res.DroppedBytes)
	assert.NotEmpty(t, res.Findings)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	assert.NotContains(t, partAt(t, msgs, 0), "fullsend.truncated")
}

func TestContentCollector_CappedResultScannedOnce(t *testing.T) {
	// The cap path already scans the response; Result must not scan the
	// same bytes again — a re-scan re-matches masked db-URL passwords
	// (supe... still fits the 4+-char capture) and double-counts the
	// finding.
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolResultEvent{
		ID:     "toolu_db",
		Result: strings.Repeat("x", maxToolResultBytes) + " postgres://user:supersecret@host",
	})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, "supersecret")
	assert.Len(t, res.Findings, 1,
		"one secret must yield exactly one finding, not one per scan")
}

func TestContentCollector_CoalescedAfterPreTrimStillScanned(t *testing.T) {
	// The pre-trim marks its part's bulk as scanned; deltas that coalesce
	// into that part afterwards are NOT scanned yet, so the flag must
	// clear on coalesce or the appended bytes reach the span raw — the
	// tail-kept suffix keeps exactly the newest bytes.
	secret := "ghp_" + strings.Repeat("k", 36)
	c := newContentCollector(1024)
	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("x", 3000)})
	c.Handle(agentruntime.TextEvent{Text: " leak: " + secret})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret,
		"bytes coalesced after a pre-trim must still be redacted")
	assert.NotEmpty(t, res.Findings)
}

func TestContentCollector_ErroredEmptyResultKept(t *testing.T) {
	// A failed call with empty output is signal, not absence: the part
	// survives with is_error and its schema-required response key, even
	// empty. Successful empty results still produce no part.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_errempty", Result: "", IsError: true})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, "tool_call_response", part["type"])
	assert.Equal(t, true, part["is_error"])
	assert.Equal(t, "", part["response"],
		"the schema-required response key is present even when empty")
	assert.Equal(t, "toolu_errempty", part["id"])
}

func TestContentCollector_EmptyTailDropChargesWholePart(t *testing.T) {
	// When the boundary window lands inside a trailing multi-byte rune,
	// the tail is empty and the part drops whole — id included — so the
	// whole part must be charged, not just its response bytes. Budget 16:
	// "final" (5) leaves 11; the id (9) leaves a 2-byte window inside €.
	c := newContentCollector(16)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_cut", Result: "0123456789ABCDEFG€"})
	c.Handle(agentruntime.TextEvent{Text: "final"})

	res := c.Result("stop")
	require.True(t, res.Truncated)
	assert.Equal(t, 29, res.DroppedBytes,
		"a whole-dropped part is charged in full: 20 response + 9 id bytes")

	msgs := decodeOutputMessages(t, res.OutputMessages)
	parts := msgs[0]["parts"].([]any)
	require.Len(t, parts, 1)
	assert.Equal(t, "final", parts[0].(map[string]any)["content"])
}

func TestContentCollector_IDBytesCountTowardBudget(t *testing.T) {
	// Ids are serialized into the attribute, so they count toward the
	// budget like every other part byte — uncounted ids would let the
	// attribute grow past the budget in aggregate.
	c := newContentCollector(30)
	c.Handle(agentruntime.ToolResultEvent{ID: "12345678901234567890", Result: "0123456789"})
	c.Handle(agentruntime.TextEvent{Text: "end"})

	res := c.Result("stop")
	require.True(t, res.Truncated)
	assert.Equal(t, 3, res.DroppedBytes,
		"the id's 20 bytes count: only 7 response bytes fit next to it")

	msgs := decodeOutputMessages(t, res.OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, "3456789", part["response"])
	assert.Equal(t, "12345678901234567890", part["id"],
		"a kept boundary part keeps its id intact")
}

func TestContentCollector_SecretBearingIDDropped(t *testing.T) {
	// Ids pass the same redaction scan as every other stream-derived
	// string; a finding drops the id entirely — never substitutes, since
	// a rewritten id could falsely collide.
	secret := "ghp_" + strings.Repeat("h", 36)
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: secret, Result: "clean output"})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret)
	assert.NotEmpty(t, res.Findings)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	part := partAt(t, msgs, 0)
	assert.NotContains(t, part, "id", "a secret-bearing id is dropped, not rewritten")
	assert.Equal(t, "clean output", part["response"])
}

func TestContentCollector_ZeroContentPartsDoNotAccumulate(t *testing.T) {
	// Parts with no content-bearing bytes are refused at Handle: they
	// would contribute nothing to the output (the empty-result rule) yet
	// accumulate unboundedly, invisible to the size-based eviction.
	c := newContentCollector(4096)
	for i := 0; i < 100; i++ {
		c.Handle(agentruntime.ToolResultEvent{ID: "toolu_x", Result: ""})
		c.Handle(agentruntime.ToolUseEvent{ID: "toolu_y"})
	}
	assert.Empty(t, c.parts, "zero-content parts must not accumulate")
}

func TestContentCollector_OversizedIDDropped(t *testing.T) {
	// The stream decodes ids unbounded and Level 3 lifts the SDK
	// attribute cap, so an id beyond any legitimate format is treated as
	// malformed and dropped — never truncated, since a truncated id
	// could falsely collide. The part itself survives, uncorrelated.
	huge := strings.Repeat("x", maxToolIDBytes+1)
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{ID: huge, Name: "Bash", Summary: "ls"})
	c.Handle(agentruntime.ToolResultEvent{ID: huge, Result: "output"})

	msgs := decodeOutputMessages(t, c.Result("stop").OutputMessages)
	parts := msgs[0]["parts"].([]any)
	require.Len(t, parts, 2)
	for _, p := range parts {
		assert.NotContains(t, p.(map[string]any), "id",
			"an oversized id must be dropped, not serialized")
	}
}

func TestContentCollector_CapsOversizedToolResult(t *testing.T) {
	// A single tool result larger than maxToolResultBytes keeps only its
	// tail — measured on real review runs, uncapped results overflow the
	// total budget and evict whole older parts; the cap lowers that
	// pressure. Capped bytes land in DroppedBytes exactly.
	c := newContentCollector(maxContentBytes)
	oversized := strings.Repeat("a", maxToolResultBytes) + strings.Repeat("b", 100)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_cap", Result: oversized})

	res := c.Result("stop")
	require.True(t, res.Truncated)
	assert.Equal(t, 100, res.DroppedBytes,
		"exactly the bytes beyond the per-result cap are dropped")

	msgs := decodeOutputMessages(t, res.OutputMessages)
	part := partAt(t, msgs, 0)
	response := part["response"].(string)
	assert.Len(t, response, maxToolResultBytes)
	assert.True(t, strings.HasSuffix(response, strings.Repeat("b", 100)),
		"the cap keeps the tail — the budget's suffix policy extended per-result")
	assert.Equal(t, "toolu_cap", part["id"])
}

func TestContentCollector_CapRedactsBeforeCutting(t *testing.T) {
	// The redaction-before-truncation invariant applies to the per-result
	// cap exactly as to every other cut: a secret straddling the cap
	// boundary must be redacted before the head is discarded.
	// The cap keeps the tail, so the head-cut point for this 8262-byte
	// result lands at byte 70 — inside the secret spanning [50:90]. A
	// raw-first cut would keep an unrecognizable 20-byte fragment.
	secret := "ghp_" + strings.Repeat("g", 36)
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolResultEvent{
		ID:     "toolu_capsec",
		Result: strings.Repeat("x", 50) + secret + strings.Repeat("! ", 4086),
	})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, strings.Repeat("g", 10),
		"no fragment of a cap-straddling secret may survive")
	assert.NotEmpty(t, res.Findings)
}

func TestContentCollector_SubCapToolResultUntouched(t *testing.T) {
	c := newContentCollector(maxContentBytes)
	within := strings.Repeat("c", maxToolResultBytes)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_fit", Result: within})

	res := c.Result("stop")
	assert.False(t, res.Truncated)
	assert.Zero(t, res.DroppedBytes)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	assert.Equal(t, within, partAt(t, msgs, 0)["response"])
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
