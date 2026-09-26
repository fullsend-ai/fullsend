package cli

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
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

func TestContentCollector_ToolCallPartCarriesArguments(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Summary: "ls -la",
		Arguments: `{"timeout": 9007199254740993, "command":"ls -la"}`})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"arguments":{"command":"ls -la","timeout":9007199254740993}`,
		"arguments are an object, re-encoded; integers keep their digits")
	part := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
	assert.Equal(t, "ls -la", part["summary"], "the summary stays beside the arguments")
	assert.Zero(t, res.DroppedBytes)
	assert.False(t, res.Truncated)
}

func TestContentCollector_RedactsSecretsInArguments(t *testing.T) {
	const secret = "s3cr3tvalue99xyz"
	pat := "ghp_" + strings.Repeat("m", 36)
	for name, args := range map[string]string{
		"leading assignment":       `{"command":"DEPLOY_TOKEN=` + secret + ` make"}`,
		"assignment after newline": `{"command":"cd x\nAPI_KEY=` + secret + ` run"}`,
		"assignment in quotes":     `{"command":"export GH_TOKEN=\"` + secret + `\""}`,
		"json inside a string":     `{"content":"{\"password\": \"` + secret + `\"}"}`,
		"secret-named member":      `{"password":"` + secret + `"}`,
		"nested member":            `{"edits":[{"headers":{"api_key":"` + secret + `"}}]}`,
		"token prefix":             `{"command":"curl -u x:` + pat + ` h"}`,
		"token as a key":           `{"` + pat + `":"v"}`,
		// A secret-named member whose value raises a finding of its own
		// (Unicode or another secret) is still scanned beside its key.
		"secret-named, zero width":    `{"password":"s3cr3tva\u200Blue99xyz"}`,
		"secret-named, folded":        `{"password":"` + secret + `\uFF01"}`,
		"secret-named, two secrets":   `{"password":"` + secret + ` ` + pat + `"}`,
		"secret-named, early quote":   `{"password":"ab\"` + secret + `"}`,
		"quote in a secret-named key": `{"api\"key":"` + secret + `"}`,
		// Joined by the probe's stand-in for the quote, the two runs must
		// not become one token a prefix pattern masks ahead of the
		// member-name pattern.
		"quote inside a token run": `{"api_key":"sk-abc\"` + secret + `"}`,
		// The normalizer is not idempotent over escape sequences: run a
		// second time over the pair, it strips one the first pass left
		// open and takes the value, or the key's keyword, with it.
		"escape sequence closed by the pair, value": `{"password":"\u001b]\u001b]x\u0007` + secret + `\u0007"}`,
		"escape sequence closed by the pair, key":   `{"password\u001b]":"ab\u0007` + secret + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: args})

			res := c.Result("stop")
			assert.NotContains(t, res.OutputMessages, secret)
			assert.NotContains(t, res.OutputMessages, pat)
			assert.NotEmpty(t, res.Findings)
			part := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
			assert.IsType(t, map[string]any{}, part["arguments"], "redaction keeps the arguments an object")
		})
	}
}

func TestContentCollector_AKeyFindingLeavesTheValueAndCountsOnce(t *testing.T) {
	for name, tc := range map[string]struct{ args, want string }{
		"token":     {`{"ghp_` + strings.Repeat("r", 36) + `":"hello"}`, `{"ghp_...":"hello"}`},
		"fullwidth": {`{"\uFF50ath":"/tmp/x"}`, `{"path":"/tmp/x"}`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{Name: "Read", Arguments: tc.args})

			res := c.Result("stop")
			assert.Contains(t, res.OutputMessages, `"arguments":`+tc.want)
			assert.Len(t, res.Findings, 1)
		})
	}
}

func TestContentCollector_KeysThatRedactAlikeDropTheArguments(t *testing.T) {
	// Folding makes the two keys one; keeping either member would show a
	// call the agent did not make, and which one would follow map order.
	const args = `{"command":"rm -rf /","\uFF43ommand":"ls"}`
	var first contentResult
	for i := 0; i < 100; i++ {
		c := newContentCollector(4096)
		c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "rm -rf /", Arguments: args})
		res := c.Result("stop")
		if i == 0 {
			first = res
		}
		require.Equal(t, first.OutputMessages, res.OutputMessages)
		require.Equal(t, first.DroppedBytes, res.DroppedBytes)
	}
	part := partAt(t, decodeOutputMessages(t, first.OutputMessages), 0)
	assert.NotContains(t, part, "arguments")
	assert.Equal(t, true, part["fullsend.truncated"])
	assert.True(t, first.Truncated)
	assert.Positive(t, first.DroppedBytes)
}

func TestContentCollector_NamelessCallCarriesNoArgumentsAndNoCharge(t *testing.T) {
	// The schema requires a name on a tool_call part. A nameless call is
	// refused; its arguments must not survive alone, nor charge a part
	// that was never kept.
	for name, args := range map[string]string{
		"kept":       `{"a":1}`,
		"over bound": `{"a":"` + strings.Repeat("x", maxToolArgumentsBytes) + `"}`,
		"not json":   `{"a":"unterminated`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(maxContentBytes)
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Arguments: args})
			assert.Equal(t, contentResult{}, c.Result("stop"))
		})
	}
}

// runnerEnvFindings counts the findings the runner env literal pass raised.
func runnerEnvFindings(res contentResult) int {
	n := 0
	for _, f := range res.Findings {
		if f.Scanner == "runner_env" {
			n++
		}
	}
	return n
}

func TestContentCollector_ReplacesRunnerEnvValues(t *testing.T) {
	// No prefix and no shape a pattern knows: only the literal pass sees it.
	const secret = "runner-only-opaque-value"
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{
		"PUSH_TOKEN": secret,
		"BRANCH":     "feature-branch-name", // not a sensitive key
		"API_KEY":    "short",               // under minRedactableSecretLen
	})
	c.Handle(agentruntime.TextEvent{Text: "pushing " + secret + " to feature-branch-name, short"})
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Arguments: `{"command":"git push https://x:` + secret + `@host/repo"}`})
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_" + secret, Result: "remote: " + secret})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret)
	assert.Contains(t, res.OutputMessages, `"content":"pushing [REDACTED:PUSH_TOKEN] to feature-branch-name, short"`)
	assert.Contains(t, res.OutputMessages, `"arguments":{"command":"git push https://x:[REDACTED:PUSH_TOKEN]@host/repo"}`)
	assert.Contains(t, res.OutputMessages, `{"type":"tool_call_response","response":"remote: [REDACTED:PUSH_TOKEN]"}`, "an id that holds a value is dropped")
	assert.Equal(t, 4, runnerEnvFindings(res), "one per replaced key per pass per scanned string")
	assert.Len(t, res.Findings, 4)
}

func TestContentCollector_ReplacesARunnerEnvValueBeforeAPatternMasksIt(t *testing.T) {
	// The assignment pattern would mask this value and keep its first
	// four bytes; the literal pass has to reach it first.
	const secret = "zzqx-opaque-runner-value"
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"PUSH_TOKEN": secret})
	c.Handle(agentruntime.TextEvent{Text: "export PUSH_TOKEN=" + secret})

	res := c.Result("stop")
	require.NotEmpty(t, res.OutputMessages)
	assert.NotContains(t, res.OutputMessages, secret[:4])
}

func TestContentCollector_ARunnerEnvValueSplitInsideAnAssignmentKeepsFourBytes(t *testing.T) {
	// The documented limit: the pass ahead of the pipeline cannot see a
	// value an invisible character splits, the normalizer joins it, and the
	// assignment pattern masks it its own way before the second pass runs.
	const secret = "zzqx-opaque-runner-value"
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"PUSH_TOKEN": secret})
	c.Handle(agentruntime.TextEvent{Text: "export PUSH_TOKEN=" + secret[:11] + "\u200B" + secret[11:]})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"content":"export PUSH_TOKEN=zzqx..."`)
}

// fullwidth writes ASCII in its compatibility forms, which NFKC folds back.
func fullwidth(s string) string {
	return strings.Map(func(r rune) rune {
		if r > ' ' && r <= '~' {
			return r + 0xFEE0
		}
		return r
	}, s)
}

func TestContentCollector_ReplacesRunnerEnvValuesTheNormalizerSpellsOut(t *testing.T) {
	// A value in fullwidth forms, or split by a zero-width space, is the
	// value only once the pipeline's normalizer ran.
	const secret = "runner-only-opaque-value"
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	env := map[string]string{"PUSH_TOKEN": secret}

	c := newContentCollectorIfEnabled(env)
	c.Handle(agentruntime.TextEvent{Text: "pushing " + fullwidth(secret)})
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"command":"echo ` + secret[:11] + `\u200B` + secret[11:] + `"}`})
	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret)
	assert.Equal(t, 2, strings.Count(res.OutputMessages, "[REDACTED:PUSH_TOKEN]"))
	assert.Equal(t, 2, runnerEnvFindings(res), "a value found after the pipeline counts like any other")

	input := recordedInput(t, newContentCollectorIfEnabled(env), "validator said: "+fullwidth(secret))["gen_ai.input.messages"].AsString()
	assert.Contains(t, input, "validator said: [REDACTED:PUSH_TOKEN]")
}

func TestContentCollector_ReplacesARunnerEnvValueTheNormalizerWouldRewrite(t *testing.T) {
	// ² folds to 2 and the ligature to "fi", and a combining mark after the
	// value composes with its last letter: the pass ahead of the pipeline
	// sees the value as the env has it.
	secret := "opaque²-runner-" + "\uFB01" + "nal-value"
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"DEPLOY_PASSWORD": secret})
	c.Handle(agentruntime.TextEvent{Text: "deploying with " + secret + "\u0301 now"})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, "deploying with [REDACTED:DEPLOY_PASSWORD]")
	assert.NotContains(t, res.OutputMessages, "runner-final-value")
	assert.Equal(t, 1, runnerEnvFindings(res))
}

func TestContentCollector_TextThatOnlyBeginsARunnerEnvValueStays(t *testing.T) {
	// Only a whole value is replaced: ordinary words that begin one stay,
	// and so does a call id.
	token := "github_pat_" + strings.Repeat("A", 30)
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"DEPLOY_TOKEN": token})
	c.Handle(agentruntime.TextEvent{Text: "opened a pull request on github"})
	c.Handle(agentruntime.ToolUseEvent{ID: "call_on_github", Name: "search_github", Summary: "querying github"})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"content":"opened a pull request on github"`)
	assert.Contains(t, res.OutputMessages, `{"type":"tool_call","id":"call_on_github","name":"search_github","summary":"querying github"}`)
	assert.Empty(t, res.Findings)
}

func TestContentCollector_DropsTheSummaryWhenTheArgumentsHeldARunnerEnvValue(t *testing.T) {
	// The parser cuts the summary out of the arguments before anything
	// scans it, so the value can be there as a beginning no literal matches.
	const secret = "runner-only-opaque-value"
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"PUSH_TOKEN": secret})
	c.Handle(agentruntime.ToolUseEvent{
		Name:      "Bash",
		Summary:   "git push https://x:" + secret[:20] + "…",
		Arguments: `{"command":"git push https://x:` + secret + `@host/repo"}`,
	})
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "ls", Arguments: `{"command":"ls"}`})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret[:5])
	assert.Contains(t, res.OutputMessages, `{"type":"tool_call","name":"Bash","arguments":{"command":"git push https://x:[REDACTED:PUSH_TOKEN]@host/repo"}}`)
	assert.Contains(t, res.OutputMessages, `"summary":"ls"`, "a call whose arguments held no value keeps its summary")
}

func TestContentCollector_DropsTheSummaryWhenAPatternFoundTheSecret(t *testing.T) {
	// A zero-width space hides the token from the parser's scan of the
	// summary; the collector's pattern finds it in the arguments once the
	// normalizer joined it, and the summary holds its cut beginning.
	token := "ghp_" + strings.Repeat("k", 36)
	split := token[:20] + "\u200B" + token[20:]
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{
		Name:      "Bash",
		Summary:   "curl -H x:" + token[:20] + "…",
		Arguments: `{"command":"curl -H x:` + split + ` https://host"}`,
	})
	c.Handle(agentruntime.ToolUseEvent{Name: "Write", Summary: "/docs/a.md", Arguments: `{"file_path":"/docs/a.md","content":"to be continued…"}`})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, token[:20])
	assert.Contains(t, res.OutputMessages, `"summary":"/docs/a.md"`, "a normalizer finding alone does not cost the summary")
}

func TestContentCollector_ReplacesARunnerEnvValueSentAsANumber(t *testing.T) {
	digits := strings.Repeat("7", minRedactableSecretLen)
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"DEPLOY_PASSWORD": digits})
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"pin":` + digits + `,"n":42}`})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"arguments":{"n":42,"pin":"[REDACTED:DEPLOY_PASSWORD]"}`)
}

func TestContentCollector_ANameRedactedAwayTakesItsArgumentsAlong(t *testing.T) {
	const args = `{"a":1}`
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "\u200B", Arguments: args})

	res := c.Result("stop")
	assert.Empty(t, res.OutputMessages, "nothing is left of the part")
	assert.Len(t, res.Findings, 1)
	assert.True(t, res.Truncated)
	assert.Equal(t, len(args), res.DroppedBytes)
}

func TestContentCollector_ANameRedactedAwayMarksTheCallItsSummaryKeeps(t *testing.T) {
	const args = `{"command":"ls"}`
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "\u200B", Summary: "ls", Arguments: args})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"summary":"ls","fullsend.truncated":true`)
	assert.NotContains(t, res.OutputMessages, "arguments")
	assert.True(t, res.Truncated)
	assert.Equal(t, len(args), res.DroppedBytes)
}

func TestContentCollector_DroppedBytesCountArgumentsOnEveryDropPath(t *testing.T) {
	const args = `{"command":"ls -la"}`
	for name, tc := range map[string]struct {
		text    string
		evicted bool
	}{
		"evicted during Handle":     {strings.Repeat("x", 25), true},
		"dropped by the raw budget": {"final", false},
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(25)
			c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: args})
			c.Handle(agentruntime.TextEvent{Text: tc.text})
			require.Equal(t, tc.evicted, len(c.parts) == 1, "which path drops the call")

			res := c.Result("stop")
			assert.True(t, res.Truncated)
			assert.Equal(t, len("Bash")+len(args), res.DroppedBytes)
			assert.NotContains(t, res.OutputMessages, "arguments")
		})
	}
}

func TestContentCollector_ADroppedResultIsReportedWithNoPartsLeft(t *testing.T) {
	// An over-cap result that redacts to nothing leaves no part; what was
	// cut and found still has to reach the span.
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_01", Result: strings.Repeat("\u200B", maxToolResultBytes)})

	res := c.Result("stop")
	assert.Empty(t, res.OutputMessages)
	assert.NotEmpty(t, res.Findings)
}

func TestContentCollector_ArgumentsAreScannedOnce(t *testing.T) {
	// A masked connection string still matches its own pattern, so a second
	// pass over redacted arguments would count the same secret twice.
	c := newContentCollector(64)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"command":"psql postgres://u:hunter2hunter2@db/x"}`})
	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("x", 200)}) // evicts the call

	assert.Len(t, c.Result("stop").Findings, 1)
}

func TestContentCollector_FullwidthQuotesCannotAddAMember(t *testing.T) {
	// NFKC folds a fullwidth quotation mark to '"'. Folded inside the
	// serialised text it would close the string and add a member.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Write", Arguments: `{"content":"a\uFF02,\uFF02injected\uFF02:\uFF02b"}`})

	part := partAt(t, decodeOutputMessages(t, c.Result("stop").OutputMessages), 0)
	assert.Equal(t, map[string]any{"content": `a","injected":"b`}, part["arguments"])
}

func TestContentCollector_IncompleteArgumentsAreDroppedAndMarked(t *testing.T) {
	// Input assembled from stream deltas stops growing at the parser's
	// cap, so it can be a fragment of a JSON value. A fragment cannot be
	// redacted string by string, and as text this assignment hides behind
	// the quote that opens it.
	fragment := `{"content":"TOKEN=s3cr3tvalue99xyz ghp_` + strings.Repeat("n", 36) + ` and then the inp`
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Write", Summary: "/x", Arguments: fragment})

	res := c.Result("stop")
	part := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
	assert.NotContains(t, part, "arguments")
	assert.Equal(t, "Write", part["name"], "the call itself is kept")
	assert.NotContains(t, part, "summary", "a secret in the arguments costs the summary")
	assert.Equal(t, true, part["fullsend.truncated"])
	assert.True(t, res.Truncated)
	assert.Len(t, res.Findings, 1, "dropped content is scanned first")
	assert.Equal(t, len(fragment)-len("ghp_"+strings.Repeat("n", 36))+len("ghp_..."), res.DroppedBytes,
		"charged as redacted, like every other discarded byte")
	assert.NotContains(t, res.OutputMessages, "s3cr3tvalue99xyz")
}

func TestContentCollector_OverBoundArgumentsAreDroppedWholeAndMarked(t *testing.T) {
	body := strings.Repeat("x", maxToolArgumentsBytes)
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Write", Summary: "/x", Arguments: `{"content": "` + body + `"}`})

	res := c.Result("stop")
	part := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
	assert.NotContains(t, part, "arguments", "a cut object is not JSON, so nothing partial is kept")
	assert.Equal(t, "/x", part["summary"])
	assert.Equal(t, "toolu_01", part["id"])
	assert.Equal(t, true, part["fullsend.truncated"])
	assert.True(t, res.Truncated)
	assert.Equal(t, len(`{"content":"`+body+`"}`), res.DroppedBytes, "charged as re-encoded")
}

func TestContentCollector_ArgumentsAtTheBoundAreKept(t *testing.T) {
	args := `{"content":"` + strings.Repeat("x", maxToolArgumentsBytes-len(`{"content":""}`)) + `"}`
	require.Len(t, args, maxToolArgumentsBytes)
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolUseEvent{Name: "Write", Arguments: args})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, args)
	assert.False(t, res.Truncated)
}

func TestContentCollector_ArgumentsBoundAppliesAfterRedaction(t *testing.T) {
	// Over the bound only while unredacted: the token masks to seven
	// bytes, and the secret inside over-bound arguments still counts.
	token := "ghp_" + strings.Repeat("q", 2*maxToolArgumentsBytes)
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"command":"echo ` + token + `"}`})
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"command":"echo ` + token + ` ` + strings.Repeat("x", maxToolArgumentsBytes) + `"}`})

	res := c.Result("stop")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	assert.Equal(t, map[string]any{"command": "echo ghp_..."}, partAt(t, msgs, 0)["arguments"])
	assert.NotContains(t, partAt(t, msgs, 1), "arguments")
	assert.Len(t, res.Findings, 2)
	assert.NotContains(t, res.OutputMessages, "qqqq")
}

func TestContentCollector_NullArgumentsAreOmitted(t *testing.T) {
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `null`})

	part := partAt(t, decodeOutputMessages(t, c.Result("stop").OutputMessages), 0)
	assert.NotContains(t, part, "arguments")
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

func TestContentCollector_PartialResultCarriesTruncatedMarker(t *testing.T) {
	// A result whose non-text blocks were skipped at the parser is a
	// fragment; the part reuses the same marker every other cut sets.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_mix", Result: "the text half", Partial: true})

	res := c.Result("stop")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	part := partAt(t, msgs, 0)
	assert.Equal(t, true, part["fullsend.truncated"],
		"a partial result must not read as a whole one")
	assert.Equal(t, "the text half", part["response"])
	assert.True(t, res.Truncated,
		"the span-level marker must fire too — it is the only cheap filter for affected spans")
	assert.Zero(t, res.DroppedBytes,
		"the parser never measured the skipped blocks; no byte count is fabricated")
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

func TestContentCollector_OversizedResultKeepsMarkedEmptyPart(t *testing.T) {
	// The parser skipped the result's line: the record keeps the call's
	// answer as an empty part marked cut, so a judge sees a lost result
	// rather than a call with none. No text and no byte count is invented.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_big", Name: "Read"})
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_big", Oversized: true})

	res := c.Result("stop")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	require.Len(t, msgs[0]["parts"].([]any), 2)
	part := partAt(t, msgs, 1)
	assert.Equal(t, "tool_call_response", part["type"])
	assert.Equal(t, "toolu_big", part["id"])
	assert.Equal(t, "", part["response"])
	assert.Equal(t, true, part["fullsend.truncated"])
	assert.NotContains(t, part, "is_error", "is_error was never decoded; it is unknown, not false")
	assert.True(t, res.Truncated, "the span-level marker must fire for the lost result")
	assert.Zero(t, res.DroppedBytes, "the lost line was never measured; no byte count is fabricated")
}

func TestContentCollector_PartialEmptyResultStillProducesNoPart(t *testing.T) {
	// A result that was entirely non-text (an image) flattens to nothing.
	// It stays absent, as documented: only a skipped line leaves a stand-in.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_img", Partial: true})
	assert.Empty(t, c.parts)
	assert.Equal(t, contentResult{}, c.Result("stop"))
}

func TestContentCollector_OversizedResultPartIsEvictable(t *testing.T) {
	// The marked-empty part costs its marker plus its id, so a run of
	// them cannot accumulate outside the budget. Budget 16: the part
	// (26 marker + 9 id) is evicted once 16 text bytes follow it.
	c := newContentCollector(16)
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_big", Oversized: true})
	require.Equal(t, 35, c.total)
	c.Handle(agentruntime.TextEvent{Text: "0123456789ABCDEF"})

	res := c.Result("stop")
	assert.Equal(t, 35, res.DroppedBytes)
	msgs := decodeOutputMessages(t, res.OutputMessages)
	parts := msgs[0]["parts"].([]any)
	require.Len(t, parts, 1)
	assert.Equal(t, "0123456789ABCDEF", parts[0].(map[string]any)["content"])
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

func TestContentCollector_EncodedCeilingBoundsEscapeDenseContent(t *testing.T) {
	// The size budget counts raw bytes; JSON encoding spends six on every
	// '<', control byte and invalid byte. The exported attribute must
	// still fit the encoded ceiling, keeping as much of the tail as fits.
	for _, unit := range []string{"<", "\x01", "\xff", "€\xe2\x82"} {
		t.Run(fmt.Sprintf("%q", unit), func(t *testing.T) {
			raw := strings.Repeat(unit, 100*1024/len(unit))
			c := newContentCollector(200_000)
			c.maxEncoded = 50_000
			c.Handle(agentruntime.TextEvent{Text: raw})

			res := c.Result("stop")
			assert.LessOrEqual(t, len(res.OutputMessages), 50_000)
			assert.Greater(t, len(res.OutputMessages), 50_000-12,
				"the cut removes what the ceiling needs and no more than one unit beyond it")
			msgs := decodeOutputMessages(t, res.OutputMessages)
			part := partAt(t, msgs, 0)
			assert.Equal(t, true, part["fullsend.truncated"])
			assert.True(t, res.Truncated)
			if content := part["content"].(string); utf8.ValidString(raw) {
				assert.True(t, strings.HasSuffix(raw, content), "the cut keeps the tail")
				assert.Equal(t, len(raw)-len(content), res.DroppedBytes, "dropped bytes stay raw bytes")
			}
			assert.Greater(t, res.DroppedBytes, len(raw)/2)
			assert.Less(t, res.DroppedBytes, len(raw))
		})
	}
}

func TestContentCollector_EncodedCeilingDropsOldestWholeAndNeverCutsAToolCall(t *testing.T) {
	events := []agentruntime.AgentEvent{
		agentruntime.ToolUseEvent{ID: "toolu_a", Name: "Bash", Summary: "ls"},
		agentruntime.ToolResultEvent{ID: "toolu_a", Result: strings.Repeat("r", 1000)},
		agentruntime.TextEvent{Text: "final answer"},
	}
	whole := newContentCollector(4096)
	for _, e := range events {
		whole.Handle(e)
	}
	full := whole.Result("stop").OutputMessages

	// Over by the whole tool_call (its encoding and comma) plus 50: the
	// oldest part is dropped whole, and the next part's head pays the 50
	// and the 26-byte marker the cut adds. 'r' encodes one to one.
	call, err := json.Marshal(contentPart{Type: "tool_call", ID: "toolu_a", Name: "Bash", Summary: "ls"})
	require.NoError(t, err)
	c := newContentCollector(4096)
	c.maxEncoded = len(full) - (len(call) + 1) - 50
	for _, e := range events {
		c.Handle(e)
	}
	res := c.Result("stop")

	assert.Len(t, res.OutputMessages, c.maxEncoded, "the cut removes exactly what is owed")
	msgs := decodeOutputMessages(t, res.OutputMessages)
	parts := msgs[0]["parts"].([]any)
	require.Len(t, parts, 2, "the tool_call is dropped whole, never cut")
	result := partAt(t, msgs, 0)
	assert.Equal(t, "tool_call_response", result["type"])
	assert.Equal(t, strings.Repeat("r", 1000-76), result["response"])
	assert.Equal(t, true, result["fullsend.truncated"])
	assert.Equal(t, "final answer", partAt(t, msgs, 1)["content"], "the ending is untouched")
	assert.NotContains(t, partAt(t, msgs, 1), "fullsend.truncated")
	assert.True(t, res.Truncated)
	assert.Equal(t, 13+76, res.DroppedBytes, "the call's 13 raw bytes and the 76 cut from the response")
}

func TestContentCollector_EncodedCeilingStopsOnceAWholeDropPaysTheDebt(t *testing.T) {
	events := []agentruntime.AgentEvent{
		agentruntime.ToolUseEvent{ID: "toolu_a", Name: "Bash", Summary: "ls"},
		agentruntime.ToolResultEvent{ID: "toolu_a", Result: strings.Repeat("r", 200)},
		agentruntime.TextEvent{Text: "final answer"},
	}
	whole := newContentCollector(4096)
	for _, e := range events {
		whole.Handle(e)
	}
	full := whole.Result("stop").OutputMessages

	// Over by exactly the tool_call and its comma: dropping it pays the
	// debt to zero, so the next part is neither cut nor marked.
	call, err := json.Marshal(contentPart{Type: "tool_call", ID: "toolu_a", Name: "Bash", Summary: "ls"})
	require.NoError(t, err)
	c := newContentCollector(4096)
	c.maxEncoded = len(full) - (len(call) + 1)
	for _, e := range events {
		c.Handle(e)
	}
	res := c.Result("stop")

	assert.Len(t, res.OutputMessages, c.maxEncoded)
	result := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
	assert.Equal(t, strings.Repeat("r", 200), result["response"])
	assert.NotContains(t, result, "fullsend.truncated")
	assert.Equal(t, 13, res.DroppedBytes)
}

func TestContentCollector_EncodedCeilingCutsAnAlreadyMarkedPartExactly(t *testing.T) {
	// A result the per-result cap already cut carries its marker, so a
	// ceiling cut adds none and owes no reserve for one. On live streams
	// this is the usual head part: every capped result is marked.
	events := []agentruntime.AgentEvent{
		agentruntime.ToolResultEvent{ID: "toolu_a", Result: strings.Repeat("r", maxToolResultBytes+100)},
		agentruntime.TextEvent{Text: "final answer"},
	}
	whole := newContentCollector(maxContentBytes)
	for _, e := range events {
		whole.Handle(e)
	}
	before := whole.Result("stop")

	c := newContentCollector(maxContentBytes)
	c.maxEncoded = len(before.OutputMessages) - 50
	for _, e := range events {
		c.Handle(e)
	}
	res := c.Result("stop")

	assert.Len(t, res.OutputMessages, c.maxEncoded, "exactly the 50 bytes owed are cut")
	part := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
	assert.Equal(t, strings.Repeat("r", maxToolResultBytes-50), part["response"])
	assert.Equal(t, true, part["fullsend.truncated"])
	assert.Equal(t, before.DroppedBytes+50, res.DroppedBytes)
}

func TestContentCollector_EncodedCeilingChargesFlagFootprintsOnWholeDrop(t *testing.T) {
	events := []agentruntime.AgentEvent{
		agentruntime.ToolResultEvent{ID: "toolu_big", Oversized: true},
		agentruntime.ToolResultEvent{ID: "toolu_err", IsError: true},
		agentruntime.TextEvent{Text: "final answer"},
	}
	whole := newContentCollector(4096)
	for _, e := range events {
		whole.Handle(e)
	}
	full := whole.Result("stop").OutputMessages

	c := newContentCollector(4096)
	c.maxEncoded = len(full) - 150
	for _, e := range events {
		c.Handle(e)
	}
	res := c.Result("stop")

	msgs := decodeOutputMessages(t, res.OutputMessages)
	require.Len(t, msgs[0]["parts"].([]any), 1)
	assert.Equal(t, "final answer", partAt(t, msgs, 0)["content"])
	assert.Equal(t, (truncatedFootprint+9)+(isErrorFootprint+9), res.DroppedBytes,
		"flagged empty parts charge their fixed footprint plus id on the ceiling path too")
}

func TestContentCollector_EncodedCeilingLeavesAFittingRecordUntouched(t *testing.T) {
	events := []agentruntime.AgentEvent{
		agentruntime.ToolUseEvent{ID: "toolu_a", Name: "Bash", Summary: "ls"},
		agentruntime.ToolResultEvent{ID: "toolu_a", Result: "<ok>"},
		agentruntime.TextEvent{Text: "final answer"},
	}
	whole := newContentCollector(4096)
	for _, e := range events {
		whole.Handle(e)
	}
	want := whole.Result("stop")

	c := newContentCollector(4096)
	c.maxEncoded = len(want.OutputMessages) // exactly at the ceiling is within it
	for _, e := range events {
		c.Handle(e)
	}
	got := c.Result("stop")
	assert.Equal(t, want.OutputMessages, got.OutputMessages)
	assert.False(t, got.Truncated)
	assert.Zero(t, got.DroppedBytes)

	c = newContentCollector(4096)
	c.maxEncoded = len(want.OutputMessages) - 1 // a single byte over is over
	for _, e := range events {
		c.Handle(e)
	}
	got = c.Result("stop")
	assert.Less(t, len(got.OutputMessages), len(want.OutputMessages))
	assert.True(t, got.Truncated)
}

func TestMaxEncodedContentBytes_StaysWithinTheProvenSize(t *testing.T) {
	// The one attribute size the pilot backend is proven to accept
	// (2026-08-20). Raising the ceiling means proving a larger size first.
	const provenAttributeBytes = 255_082
	assert.LessOrEqual(t, maxEncodedContentBytes, provenAttributeBytes)
	assert.Equal(t, maxEncodedContentBytes, newContentCollector(1).maxEncoded)
}

func TestContentCollector_EncodedCeilingBelowEveryPartYieldsNoContent(t *testing.T) {
	c := newContentCollector(4096)
	c.maxEncoded = 10
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_a", Name: "Bash", Summary: "ls"})
	c.Handle(agentruntime.TextEvent{Text: "final"})

	res := c.Result("stop")
	assert.Empty(t, res.OutputMessages)
	assert.True(t, res.Truncated)
	assert.Equal(t, 13+5, res.DroppedBytes)
}

func TestContentCollector_EncodedCeilingHoldsForEscapeDenseRecords(t *testing.T) {
	// Property: whatever the parts hold, the exported record fits the
	// ceiling, stays valid JSON, keeps a suffix, and charges exactly the
	// raw bytes it removed. The alphabet is what JSON inflates — HTML
	// escapes, quotes, control bytes — plus multi-byte runes.
	alphabet := []string{"<", ">", "&", "\"", "\\", "\n", "\t", "\x01", "\b", "€", "𝄞", "a", "z"}
	rng := rand.New(rand.NewSource(6603))
	rawBytes := func(parts []any) int {
		n := 0
		for _, p := range parts {
			m := p.(map[string]any)
			for _, k := range []string{"content", "id", "name", "summary", "response"} {
				if v, ok := m[k].(string); ok {
					n += len(v)
				}
			}
			if args, ok := m["arguments"]; ok {
				enc, err := json.Marshal(args) // arguments are charged as encoded
				require.NoError(t, err)
				n += len(enc)
			}
		}
		return n
	}
	for round := 0; round < 300; round++ {
		var events []agentruntime.AgentEvent
		for i, n := 0, 1+rng.Intn(6); i < n; i++ {
			var b strings.Builder
			for j, m := 0, rng.Intn(400); j < m; j++ {
				b.WriteString(alphabet[rng.Intn(len(alphabet))])
			}
			id := fmt.Sprintf("toolu_%d_%d", round, i)
			switch rng.Intn(3) {
			case 0:
				events = append(events, agentruntime.TextEvent{Text: b.String() + "."})
			case 1:
				args, err := json.Marshal(map[string]string{"command": b.String()})
				require.NoError(t, err)
				events = append(events, agentruntime.ToolUseEvent{ID: id, Name: "Bash", Summary: b.String(), Arguments: string(args)})
			default:
				events = append(events, agentruntime.ToolResultEvent{ID: id, Result: b.String() + "."})
			}
		}
		whole := newContentCollector(1 << 20)
		for _, e := range events {
			whole.Handle(e)
		}
		before := whole.Result("stop")
		beforeParts := decodeOutputMessages(t, before.OutputMessages)[0]["parts"].([]any)

		c := newContentCollector(1 << 20)
		c.maxEncoded = 60 + rng.Intn(len(before.OutputMessages))
		for _, e := range events {
			c.Handle(e)
		}
		res := c.Result("stop")

		require.LessOrEqual(t, len(res.OutputMessages), c.maxEncoded, "round %d", round)
		kept := []any{}
		if res.OutputMessages != "" {
			kept = decodeOutputMessages(t, res.OutputMessages)[0]["parts"].([]any)
		}
		require.Equal(t, rawBytes(beforeParts)-rawBytes(kept), res.DroppedBytes-before.DroppedBytes, "round %d", round)
		require.LessOrEqual(t, len(kept), len(beforeParts))
		for i := range kept {
			// A suffix: every kept part but the first is whole and in
			// place; the first may be tail-cut, in its bulk field only —
			// so a tool_call, which has none, is never cut.
			want := beforeParts[len(beforeParts)-len(kept)+i]
			if i > 0 {
				require.Equal(t, want, kept[i], "round %d part %d", round, i)
				continue
			}
			got := kept[0].(map[string]any)
			for k, v := range want.(map[string]any) {
				if k == "content" || k == "response" {
					require.True(t, strings.HasSuffix(v.(string), got[k].(string)), "round %d: the cut keeps the tail of %s", round, k)
					continue
				}
				require.Equal(t, v, got[k], "round %d %s", round, k)
			}
		}
		if len(res.OutputMessages) < len(before.OutputMessages) {
			require.True(t, res.Truncated, "round %d", round)
		}
	}
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

// recordedInput runs attachInput on a recorded agent span and returns the
// attributes the span ended with.
func recordedInput(t *testing.T, c *contentCollector, prompt string) map[attribute.Key]attribute.Value {
	t.Helper()
	_, rec, span := toolSpanFixture(t)
	c.attachInput(span, prompt)
	span.End()
	ended := rec.Ended()
	require.Len(t, ended, 1)
	return toolSpanAttrs(tracetest.SpanStubFromReadOnlySpan(ended[0]))
}

func TestAttachInput_RecordsTheRetryPromptAsOneUserMessage(t *testing.T) {
	prompt, _ := buildFeedbackPrompt("lint: main.go:3 unused variable <x>")
	attrs := recordedInput(t, newContentCollector(maxContentBytes), prompt)

	var msgs []map[string]any
	require.NoError(t, json.Unmarshal([]byte(attrs["gen_ai.input.messages"].AsString()), &msgs))
	require.Len(t, msgs, 1)
	assert.Equal(t, map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"type": "text", "content": prompt}},
	}, msgs[0], "an input message carries role and parts; finish_reason belongs to output messages")
	assert.Len(t, attrs, 1)
}

func TestAttachInput_NoPromptOrNoCollectorAddsNothing(t *testing.T) {
	assert.Empty(t, recordedInput(t, newContentCollector(maxContentBytes), ""),
		"no composed prompt (the first iteration, or feedback_mode off)")
	assert.Empty(t, recordedInput(t, nil, "would-be prompt"), "gate off")
}

func TestAttachInput_RedactsAndCountsFindings(t *testing.T) {
	secret := "ghp_" + strings.Repeat("k", 36)
	c := newContentCollector(maxContentBytes)
	in := recordedInput(t, c, "token "+secret+" leaked")["gen_ai.input.messages"].AsString()

	assert.NotContains(t, in, secret)
	assert.Contains(t, in, "leaked")
	// A retry can fail before the agent emits anything; the input's
	// findings still have to reach the span.
	assert.NotEmpty(t, c.Result("error").Findings)
}

func TestAttachInput_WorstCaseFeedbackSharesTheEncodedCeiling(t *testing.T) {
	// U+FDFA has the largest NFKC expansion in Unicode (3 bytes to 33) and
	// redaction folds it, so the recorded copy of a 10 KiB feedback is an
	// order of magnitude larger than the prompt the agent was sent.
	prompt, _ := buildFeedbackPrompt(strings.Repeat("\uFDFA", maxFeedbackBytes))
	c := newContentCollector(maxContentBytes)
	in := recordedInput(t, c, prompt)["gen_ai.input.messages"].AsString()
	require.Greater(t, len(in), 10*maxFeedbackBytes, "fixture must expand for this test to prove anything")

	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("<", maxContentBytes)}) // encodes sixfold
	out := c.Result("stop").OutputMessages
	require.NotEmpty(t, out)
	assert.LessOrEqual(t, len(in)+len(out), maxEncodedContentBytes,
		"both attributes ride one span; together they stay within the proven size")
	assert.Greater(t, len(in)+len(out), maxEncodedContentBytes-100,
		"the output keeps everything the input left")
}
