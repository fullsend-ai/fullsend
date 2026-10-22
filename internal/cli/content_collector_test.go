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
	"github.com/fullsend-ai/fullsend/internal/security"
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
	// A kept string goes through the text pipeline on its own, decoded:
	// the patterns are written for plain text, and over serialised JSON
	// they miss an assignment that opens a string or follows an escaped
	// newline, and a value behind escaped quotes.
	const secret = "s3cr3tvalue99xyz"
	pat := "ghp_" + strings.Repeat("m", 36)
	for name, args := range map[string]string{
		"leading assignment":       `{"command":"DEPLOY_TOKEN=` + secret + ` make"}`,
		"assignment after newline": `{"command":"cd x\nAPI_KEY=` + secret + ` run"}`,
		"assignment in quotes":     `{"command":"export GH_TOKEN=\"` + secret + `\""}`,
		"token prefix":             `{"command":"curl -u x:` + pat + ` h"}`,
		"two secrets":              `{"command":"TOKEN=` + secret + ` ` + pat + `"}`,
		// The normalizer joins what a zero-width character split and
		// folds what was spelled in compatibility forms before the
		// patterns run.
		"zero width": `{"command":"TOKEN=s3cr3tva​lue99xyz"}`,
		"folded":     `{"command":"TOKEN=` + secret + `！"}`,
		// An escape sequence the normalizer strips can carry the value
		// the patterns never see: such a string is masked whole.
		"inside an escape sequence": `{"command":"\u001b]\u001b]x\u0007` + secret + `\u0007"}`,
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

func TestContentCollector_RecordsOnlyListedArguments(t *testing.T) {
	// The record keeps the members recordedArguments names for the tool —
	// paths, patterns, commands, modes and bounds — and nothing else of a
	// call's input: a file body, an edit, a prompt, a notebook source or
	// a todo list is dropped with no finding and the summary kept, since
	// nothing found a secret, and neither charged nor marked, since the
	// list is the documented policy, not a loss. A tool the list does not
	// name — an MCP tool, a tool of another name — records nothing.
	const opaque = "opaque-credential-123"
	for name, tc := range map[string]struct {
		tool, args, want string
	}{
		"Write keeps the path":                   {"Write", `{"file_path":"/x/a.json","content":"{\"credentials\":{\"value\":\"` + opaque + `\"}}"}`, `{"file_path":"/x/a.json"}`},
		"Edit keeps the path":                    {"Edit", `{"file_path":"/x/a.go","old_string":"` + opaque + `","new_string":"x","replace_all":true}`, `{"file_path":"/x/a.go","replace_all":true}`},
		"NotebookEdit keeps the path":            {"NotebookEdit", `{"notebook_path":"n.ipynb","cell_id":"c1","new_source":"print('` + opaque + `')"}`, `{"cell_id":"c1","notebook_path":"n.ipynb"}`},
		"Agent keeps its description":            {"Agent", `{"description":"scan","prompt":"the token is ` + opaque + `","model":"sonnet","subagent_type":"Explore"}`, `{"description":"scan","model":"sonnet","subagent_type":"Explore"}`},
		"WebFetch keeps the url":                 {"WebFetch", `{"url":"https://example.com/doc","prompt":"find ` + opaque + `"}`, `{"url":"https://example.com/doc"}`},
		"Bash keeps the command":                 {"Bash", `{"command":"ls","description":"list","timeout":5000,"run_in_background":true}`, `{"command":"ls","description":"list","timeout":5000}`},
		"Grep keeps everything":                  {"Grep", `{"pattern":"TODO","path":"src","output_mode":"content","-n":true,"context":2}`, `{"-n":true,"context":2,"output_mode":"content","path":"src","pattern":"TODO"}`},
		"Read keeps everything":                  {"Read", `{"file_path":"/x","offset":10,"limit":50}`, `{"file_path":"/x","limit":50,"offset":10}`},
		"a listed member holding an object":      {"Grep", `{"path":{"dir":"` + opaque + `"},"pattern":"x"}`, `{"pattern":"x"}`},
		"a listed member holding an array":       {"Grep", `{"pattern":["` + opaque + `"],"path":"src"}`, `{"path":"src"}`},
		"no members":                             {"Bash", `{}`, `{}`},
		"an MCP tool records nothing":            {"mcp__gh__create_issue", `{"title":"bug","description":"the token is ` + opaque + `"}`, ""},
		"a tool of another name records nothing": {"MultiEdit", `{"file_path":"/x/a.go","edits":[{"old_string":"` + opaque + `","new_string":"x"}]}`, ""},
		"TodoWrite records nothing":              {"TodoWrite", `{"todos":[{"content":"` + opaque + `","status":"pending"}]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{Name: tc.tool, Summary: "s", Arguments: tc.args})

			res := c.Result("stop")
			part := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
			if tc.want == "" {
				assert.NotContains(t, part, "arguments")
			} else {
				assert.Contains(t, res.OutputMessages, `"arguments":`+tc.want)
			}
			assert.NotContains(t, res.OutputMessages, opaque)
			assert.Equal(t, "s", part["summary"], "nothing found a secret")
			assert.Empty(t, res.Findings)
			assert.False(t, res.Truncated, "a member the list leaves out is policy, not loss")
			assert.NotContains(t, part, "fullsend.truncated")
			assert.Zero(t, res.DroppedBytes)
		})
	}
}

func TestContentCollector_DroppedMembersAreScannedAndNotCharged(t *testing.T) {
	// Discarded content is redacted first, so a secret in a member that
	// is not recorded still counts — and costs the summary, as a secret
	// found anywhere in the arguments does — but the member is neither
	// charged nor marked: leaving it out is the list's policy, and the
	// markers mean loss.
	token := "ghp_" + strings.Repeat("r", 36)
	for name, args := range map[string]string{
		"a file body":                       `{"file_path":"/x","content":"echo ` + token + `"}`,
		"a listed member holding an object": `{"file_path":"/x","path":{"t":"` + token + `"}}`,
		"a number outside the list":         `{"file_path":"/x","pin":12345678}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{Name: "Write", Summary: "/x", Arguments: args})

			res := c.Result("stop")
			assert.NotContains(t, res.OutputMessages, token[4:])
			assert.Contains(t, res.OutputMessages, `"arguments":{"file_path":"/x"}`)
			assert.Zero(t, res.DroppedBytes)
			assert.False(t, res.Truncated)
			assert.NotContains(t, res.OutputMessages, `"fullsend.truncated"`)
			if strings.Contains(name, "number") {
				assert.Empty(t, res.Findings)
				assert.Contains(t, res.OutputMessages, `"summary":"/x"`)
			} else {
				assert.Len(t, res.Findings, 1)
				assert.NotContains(t, res.OutputMessages, `"summary"`)
			}
		})
	}
}

func TestContentCollector_AURLsUserinfoQueryAndFragmentAreMasked(t *testing.T) {
	// No pattern is written for a password before a host, a presigned
	// signature or an access token in a query string, or a token behind a
	// fragment, and the semantic conventions say url.full must not carry
	// userinfo and should redact signature query values. redactText masks
	// them in every recorded string, last — after the normalizer folds a
	// compatibility '？' or '＠' and the patterns mask a token in the path —
	// counts one finding, which costs a call its summary, and re-encodes
	// nothing.
	const pw = "hunter2hunter2"
	fw := func(r rune) string { return string(r) }
	ghp := "ghp_" + strings.Repeat("a", 36)
	var ghpFull strings.Builder
	for _, r := range ghp[4:] {
		ghpFull.WriteRune(r + 0xfee0)
	}
	for name, tc := range map[string]struct {
		tool, args, want string
	}{
		"a password before the host":              {"WebFetch", `{"url":"https://alice:` + pw + `@example.com/repo"}`, `{"url":"https://***@example.com/repo"}`},
		"a presigned query":                       {"WebFetch", `{"url":"https://b.s3.amazonaws.com/k?X-Amz-Credential=AKIA` + pw + `&X-Amz-Signature=` + pw + `"}`, `{"url":"https://b.s3.amazonaws.com/k?***"}`},
		"an access token in the query":            {"Bash", `{"command":"curl 'https://api.example.com/v1/me?access_token=` + pw + `' -s"}`, `{"command":"curl 'https://api.example.com/v1/me?*** -s"}`},
		"a token in a clone URL":                  {"Bash", `{"command":"git clone https://x-access-token:` + pw + `@github.com/o/r.git /tmp/r"}`, `{"command":"git clone https://***@github.com/o/r.git /tmp/r"}`},
		"a token behind the fragment":             {"Bash", `{"command":"open https://app.example.com/cb#access_token=` + pw + `"}`, `{"command":"open https://app.example.com/cb#***"}`},
		"a password quoted for the shell":         {"Bash", `{"command":"git clone https://deploy:'` + pw + `'@git.example.com/o/r"}`, `{"command":"git clone https://***@git.example.com/o/r"}`},
		"a query quoted half-way":                 {"Bash", `{"command":"curl 'https://h.example.com/x'?token=` + pw + `"}`, `{"command":"curl 'https://h.example.com/x'?***"}`},
		"a URL nested in another's path":          {"Bash", `{"command":"curl https://proxy.example.com/https://u:` + pw + `@h.example.com/x"}`, `{"command":"curl https://***@h.example.com/x"}`},
		"a password holding a '/'":                {"Bash", `{"command":"git clone https://x-access-token:ab+c/` + pw + `=@github.com/o/r"}`, `{"command":"git clone https://***@github.com/o/r"}`},
		"a password holding a '?'":                {"Bash", `{"command":"git clone https://deploy:` + pw + `?x@github.com/o/r.git"}`, `{"command":"git clone https://***"}`},
		"a password holding a '#'":                {"Bash", `{"command":"git clone https://deploy:` + pw + `#x@github.com/o/r.git"}`, `{"command":"git clone https://***"}`},
		"an '@' in the query":                     {"Bash", `{"command":"curl https://h.example.com/x?user=dev@example.com&token=` + pw + `"}`, `{"command":"curl https://***"}`},
		"a query holding a quote":                 {"Bash", `{"command":"curl 'https://h.example.com/?q=\"a\"&token=` + pw + `'"}`, `{"command":"curl 'https://h.example.com/?***"}`},
		"a query after a closing quote":           {"Bash", `{"command":"curl \"https://h.example.com/x\"?token=` + pw + `"}`, `{"command":"curl \"https://h.example.com/x\"?***"}`},
		"a fullwidth question mark":               {"Bash", `{"command":"curl https://api.example.com/v1/me` + fw(0xff1f) + `access_token=` + pw + `"}`, `{"command":"curl https://api.example.com/v1/me?***"}`},
		"a fullwidth at sign":                     {"Bash", `{"command":"git clone https://x-access-token` + fw(0xff1a) + pw + fw(0xff20) + `github.com/o/r"}`, `{"command":"git clone https://***@github.com/o/r"}`},
		"a zero-width split scheme":               {"Bash", `{"command":"curl https:` + fw(0x200b) + `//h.example.com/p?access_token=` + pw + `"}`, `{"command":"curl https://h.example.com/p?***"}`},
		"a fullwidth token in the path":           {"Bash", `{"command":"curl https://h.example.com/ghp_` + ghpFull.String() + `?x=1"}`, `{"command":"curl https://h.example.com/ghp_...?***"}`},
		"the colon escaped for the shell too":     {"Bash", `{"command":"git clone https\\\\:\\\\/\\\\/deploy:` + pw + `@git.example.com/o/r"}`, `{"command":"git clone https\\\\:\\\\/\\\\/***@git.example.com/o/r"}`},
		"a URL percent-encoded whole":             {"Bash", `{"command":"curl https%3A%2F%2Fdeploy%3A` + pw + `%40git.example.com%2Fo%2Fr"}`, `{"command":"curl https%3A%2F%2F***"}`},
		"a URL percent-encoded in a query":        {"Bash", `{"command":"curl https://h.example.com/x?next=https%3A%2F%2Fdeploy%3A` + pw + `%40git.example.com"}`, `{"command":"curl https://***"}`},
		"a URL percent-encoded in a path":         {"Bash", `{"command":"curl https://proxy.example.com/fetch/https%3A%2F%2Fdeploy%3A` + pw + `%40git.example.com"}`, `{"command":"curl https://***"}`},
		"a URL percent-encoded twice":             {"Bash", `{"command":"curl https%253A%252F%252Fdeploy%253A` + pw + `%2540git.example.com"}`, `{"command":"curl https%253A%252F%252F***"}`},
		"separators escaped for the shell":        {"Bash", `{"command":"curl https:\\\\/\\\\/h.example.com/p?access_token=` + pw + `"}`, `{"command":"curl https:\\\\/\\\\/h.example.com/p?***"}`},
		"a password behind escaped separators":    {"Bash", `{"command":"git clone https:\\\\/\\\\/deploy:` + pw + `@git.example.com/o/r"}`, `{"command":"git clone https:\\\\/\\\\/***@git.example.com/o/r"}`},
		"the colon encoded, the slashes plain":    {"Bash", `{"command":"curl https%3A//deploy%3A` + pw + `%40git.example.com/o/r"}`, `{"command":"curl https%3A//***"}`},
		"that, in a plain URL's path":             {"Bash", `{"command":"curl https://proxy.example.com/fetch/https%3A//deploy%3A` + pw + `%40git.example.com"}`, `{"command":"curl https://***"}`},
		"the slashes encoded, the colon plain":    {"Bash", `{"command":"curl https:%2F%2Fdeploy:` + pw + `@git.example.com%2Fo%2Fr"}`, `{"command":"curl https:%2F%2F***"}`},
		"that, in a plain URL's path too":         {"Bash", `{"command":"curl https://proxy.example.com/fetch/https:%2F%2Fdeploy:` + pw + `@git.example.com%2Fo%2Fr"}`, `{"command":"curl https://***"}`},
		"userinfo encoded behind a plain '://'":   {"Bash", `{"command":"git clone https://deploy%3A` + pw + `%40git.example.com/o/r"}`, `{"command":"git clone https://***%40git.example.com/o/r"}`},
		"that, in a plain URL's path as well":     {"Bash", `{"command":"curl https://proxy.example.com/fetch/https://deploy%3A` + pw + `%40git.example.com"}`, `{"command":"curl https://***%40git.example.com"}`},
		"a fragment encoded behind a plain '://'": {"Bash", `{"command":"open https://app.example.com/cb%23access_token=` + pw + `"}`, `{"command":"open https://app.example.com/cb%23***"}`},
		"a query encoded behind a plain '://'":    {"Bash", `{"command":"curl https://h.example.com/x%3Faccess_token=` + pw + `"}`, `{"command":"curl https://h.example.com/x%3F***"}`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{Name: tc.tool, Summary: "s", Arguments: tc.args})

			res := c.Result("stop")
			assert.Contains(t, res.OutputMessages, `"arguments":`+tc.want)
			assert.NotContains(t, res.OutputMessages, pw)
			assert.NotContains(t, res.OutputMessages, ghp[4:])
			assert.NotContains(t, res.OutputMessages, "%EF", "nothing is re-encoded")
			assert.NotContains(t, res.OutputMessages, `"summary"`, "the summary was cut from the same member")
			urls := 0
			for _, f := range res.Findings {
				if f.Scanner == "url" {
					urls++
				}
			}
			assert.Equal(t, 1, urls, "one finding for the string")
		})
	}
	t.Run("an Agent prompt's URL costs the summary cut from it", func(t *testing.T) {
		c := newContentCollector(4096)
		c.Handle(agentruntime.ToolUseEvent{Name: "Agent", Summary: "Clone https://deploy:" + pw + "@git.example.com/o/r and",
			Arguments: `{"description":"clone","prompt":"Clone https://deploy:` + pw + `@git.example.com/o/r and read it"}`})

		res := c.Result("stop")
		assert.Contains(t, res.OutputMessages, `"arguments":{"description":"clone"}`)
		assert.NotContains(t, res.OutputMessages, pw)
		assert.NotContains(t, res.OutputMessages, `"summary"`)
	})
	t.Run("a URL in a tool result or in text", func(t *testing.T) {
		c := newContentCollector(4096)
		c.Handle(agentruntime.TextEvent{Text: "pushing to https://deploy:" + pw + "@git.example.com/o/r now"})
		c.Handle(agentruntime.ToolResultEvent{ID: "toolu_01", Result: "origin\thttps://deploy:" + pw + "@git.example.com/o/r (fetch)"})
		c.Handle(agentruntime.ToolResultEvent{ID: "toolu_02", Result: `{"next":"https:\/\/api.example.com\/v1?access_token=` + pw + `"}`})

		res := c.Result("stop")
		assert.Contains(t, res.OutputMessages, `"content":"pushing to https://***@git.example.com/o/r now"`)
		assert.Contains(t, res.OutputMessages, `"response":"origin\thttps://***@git.example.com/o/r (fetch)"`)
		assert.Contains(t, res.OutputMessages, `/v1?***\"}`, "a JSON encoder's escaped slashes")
		assert.NotContains(t, res.OutputMessages, pw)
	})
	t.Run("a URL in minified JSON masks nothing past its closing quote", func(t *testing.T) {
		c := newContentCollector(4096)
		c.Handle(agentruntime.ToolResultEvent{ID: "toolu_01", Result: `{"html_url":"https://github.com/o/r/pull/1?x=1","title":"Fix the parser"}`})
		c.Handle(agentruntime.ToolResultEvent{ID: "toolu_02", Result: `{"url":"https://github.com/o/r","author":{"email":"dev@example.com"}}`})
		c.Handle(agentruntime.ToolResultEvent{ID: "toolu_03", Result: `{"url":"https://github.com","email":"dev@example.com"}`})

		res := c.Result("stop")
		parts := decodeOutputMessages(t, res.OutputMessages)[0]["parts"].([]any)
		require.Len(t, parts, 3)
		assert.Equal(t, `{"html_url":"https://github.com/o/r/pull/1?***","title":"Fix the parser"}`, parts[0].(map[string]any)["response"])
		assert.Equal(t, `{"url":"https://github.com/o/r","author":{"email":"dev@example.com"}}`, parts[1].(map[string]any)["response"], "the quote ends the URL before the email")
		assert.Equal(t, `{"url":"https://github.com","email":"dev@example.com"}`, parts[2].(map[string]any)["response"], "the quote ends the URL")
		assert.Len(t, res.Findings, 1, "only the query was masked")
	})
	t.Run("a URL with none of them is kept as written", func(t *testing.T) {
		c := newContentCollector(4096)
		c.Handle(agentruntime.ToolUseEvent{Name: "WebFetch", Summary: "s", Arguments: `{"url":"https://example.com/docs/a.html"}`})

		res := c.Result("stop")
		assert.Contains(t, res.OutputMessages, `"arguments":{"url":"https://example.com/docs/a.html"}`)
		assert.Contains(t, res.OutputMessages, `"summary":"s"`)
		assert.Empty(t, res.Findings)
	})
}

func TestContentCollector_AToolTheListDoesNotNameRecordsNoEmptyObject(t *testing.T) {
	// `{}` under a name the list does not name is still nothing of that
	// tool's input, and a name that redacts away is never one the list
	// names: neither part carries arguments.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "mcp__x__y", Summary: "s", Arguments: `{}`})
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: string(rune(0x200B)), Arguments: `{}`})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, "arguments")
	assert.NotContains(t, res.OutputMessages, "toolu_01", "a nameless part with no summary is not kept")
}

func TestContentCollector_AStringTheNormalizerStrippedIsMaskedWhole(t *testing.T) {
	// The normalizer strips a terminal escape sequence or tag characters
	// before the patterns run, and the stripping can take the first
	// letter of a token (a CSI sequence ends at the next letter) or a
	// whole payload the patterns never see: such a string is masked
	// whole, and the summary goes with it.
	token := "ghp_" + strings.Repeat("r", 36)
	for name, tc := range map[string]struct {
		args, want, gone string
	}{
		"a colour code that eats a token's first letter": {`{"command":"\u001b[` + token + `"}`, `{"command":"***"}`, token[1:]},
		"a token inside a title code":                    {`{"command":"echo \u001b]` + token + `\u0007"}`, `{"command":"***"}`, token[4:]},
		"tag characters":                                 {`{"command":"x󠁧y"}`, `{"command":"***"}`, "xy"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "s", Arguments: tc.args})

			res := c.Result("stop")
			assert.Contains(t, res.OutputMessages, `"arguments":`+tc.want)
			assert.NotContains(t, res.OutputMessages, tc.gone)
			assert.NotContains(t, res.OutputMessages, `"summary"`)
		})
	}
}

func TestContentCollector_ANumberUnderAListedMemberIsScanned(t *testing.T) {
	// A number under a listed member is kept, and scanned as its digits
	// first like every kept string: a runner env value sent as a number
	// is replaced, and the mask is exported as a string, so a reader that
	// gets an integer back got the real one. A clean number stays a
	// number.
	digits := strings.Repeat("7", minRedactableSecretLen)
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"DEPLOY_PASSWORD": digits})
	c.Handle(agentruntime.ToolUseEvent{Name: "Read", Arguments: `{"file_path":"/x","limit":50,"offset":` + digits + `}`})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"arguments":{"file_path":"/x","limit":50,"offset":"[REDACTED:DEPLOY_PASSWORD]"}`)
	assert.NotContains(t, res.OutputMessages, digits)
	assert.Equal(t, 1, runnerEnvFindings(res))
	assert.False(t, res.Truncated)
	assert.Zero(t, res.DroppedBytes)
}

func TestContentCollector_ArgumentsWithTrailingInputAreDropped(t *testing.T) {
	// One JSON value, then white space at most. Anything after it is not
	// the object the record would describe, so the text is scanned — a
	// secret in the tail counts, and costs the summary — then dropped
	// whole and charged as the redacted text.
	token := "ghp_" + strings.Repeat("r", 36)
	for name, tc := range map[string]struct {
		args    string
		charged int
		found   int
	}{
		"text after the object": {`{"command":"ls"} garbage`, len(`{"command":"ls"} garbage`), 0},
		"a second value":        {`{"command":"ls"}{"command":"rm"}`, len(`{"command":"ls"}{"command":"rm"}`), 0},
		"text after null":       {`null x`, len(`null x`), 0},
		"a token in the tail":   {`{"command":"ls"} ` + token, len(`{"command":"ls"} ghp_...`), 1},
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "ls", Arguments: tc.args})

			res := c.Result("stop")
			assert.NotContains(t, res.OutputMessages, "arguments")
			assert.NotContains(t, res.OutputMessages, token[4:])
			assert.True(t, res.Truncated)
			assert.Equal(t, tc.charged, res.DroppedBytes)
			assert.Len(t, res.Findings, tc.found)
			if tc.found == 0 {
				assert.Contains(t, res.OutputMessages, `"summary":"ls"`)
			} else {
				assert.NotContains(t, res.OutputMessages, `"summary"`)
			}
		})
	}

	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: "{\"command\":\"ls\"} \n"})
	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"arguments":{"command":"ls"}`, "white space after the value is not trailing input")
	assert.False(t, res.Truncated)
}

func TestContentCollector_AResultTheNormalizerStrippedIsMaskedWhole(t *testing.T) {
	// The normalizer strips a terminal escape sequence before the patterns
	// run, and a colour code ends at the next letter: ESC [ g eats the g
	// of ghp_, and a title code's payload is text no pattern sees. Every
	// Level 3 string goes through redactText, so the rule lives there and
	// is coarse on purpose: a string the normalizer stripped anything from
	// is masked whole. No scan of the text as written can stand in — the
	// redactor reports one finding for repeated matches, and a second
	// value the normalizer joins can hide a first one it cut.
	token := "ghp_" + strings.Repeat("r", 36)
	other := "ghp_" + strings.Repeat("s", 36)
	for name, result := range map[string]string{
		"a colour code that eats a token's first letter": "\x1b[" + token,
		"a token inside a title code":                    "ok \x1b]" + token + "\x07",
		"the eaten token repeated":                       "\x1b[" + token + " " + token,
		"a second token the normalizer joins":            "\x1b[" + token + " " + other[:6] + "\u200b" + other[6:],
		"a coloured line with no secret":                 "\x1b[31mok\x1b[0m",
		"a coloured line with a token":                   "\x1b[31m" + token + "\x1b[0m",
	} {
		t.Run(name, func(t *testing.T) {
			c := newContentCollector(4096)
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash"})
			c.Handle(agentruntime.ToolResultEvent{ID: "toolu_01", Result: result})

			res := c.Result("stop")
			assert.Contains(t, res.OutputMessages, `"response":"***"`)
			assert.NotContains(t, res.OutputMessages, token[4:])
			assert.NotContains(t, res.OutputMessages, other[10:])
			assert.NotEmpty(t, res.Findings)
		})
	}

	// The same rule covers a call's name on the record.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_02", Name: "\x1b[" + token + " " + token})
	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"name":"***"`)
	assert.NotContains(t, res.OutputMessages, token[4:])
}

func TestAttachInput_APromptAlteredUpstreamIsMaskedWhole(t *testing.T) {
	// The retry prompt is sanitized and cut before it is recorded
	// (buildFeedbackPrompt), so the recorder's own pass cannot see what
	// the sanitizer stripped: a colour code that ate a token's first
	// letter plus a zero-width character that kept the token from the
	// pattern scan leave "hp_" and the body; and a token in compatibility
	// forms straddling the cut is split before anything can fold it; and a
	// URL password straddling the cut loses the '@' that marks it. All are
	// recorded masked whole (promptHidesAlterations). Big plain feedback,
	// a URL with nothing to mask in it included, is not.
	token := "ghp_" + strings.Repeat("r", 36)
	var fw strings.Builder
	for _, r := range token {
		fw.WriteRune(r + 0xfee0)
	}
	plain := strings.Repeat("the validator said no. ", 600) // over maxFeedbackBytes
	for name, tc := range map[string]struct {
		feedback string
		masked   bool
	}{
		"a colour code ate the first letter of a zero-width-split token": {"\x1b[" + token[:20] + "\u200b" + token[20:], true},
		"a fullwidth token straddling the cut":                           {strings.Repeat("x", maxFeedbackBytes-48) + " " + fw.String(), true},
		"a URL password straddling the cut, 12 bytes before it":          {strings.Repeat("x", maxFeedbackBytes-28) + " https://deploy:" + token[4:20] + "@git.example.com/o/r", true},
		"plain feedback over the cut":                                    {plain + " see https://example.com/docs", false},
	} {
		t.Run(name, func(t *testing.T) {
			fb := redactFeedback(tc.feedback, nil)
			prompt, sanitized := buildFeedbackPrompt(fb)
			c := newContentCollector(maxContentBytes)
			in := recordedInput(t, c, prompt, promptHidesAlterations(fb, sanitized))["gen_ai.input.messages"].AsString()
			assert.NotContains(t, in, token[4:16])
			if tc.masked {
				assert.Contains(t, in, `"content":"***"`)
			} else {
				assert.Contains(t, in, "the validator said no.")
			}
		})
	}
}

func TestContentCollector_WhiteSpaceArgumentsAreNone(t *testing.T) {
	// A call whose arguments are white space only carried nothing: no
	// arguments, nothing charged, nothing marked — as for "".
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "ls", Arguments: " \n\t"})
	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"summary":"ls"}`)
	assert.NotContains(t, res.OutputMessages, "arguments")
	assert.False(t, res.Truncated)
	assert.Zero(t, res.DroppedBytes)
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
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Arguments: `{"command":"deploy --token ` + secret + ` origin"}`})
	c.Handle(agentruntime.ToolResultEvent{ID: "toolu_" + secret, Result: "remote: " + secret})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret)
	assert.Contains(t, res.OutputMessages, `"content":"pushing [REDACTED:PUSH_TOKEN] to feature-branch-name, short"`)
	assert.Contains(t, res.OutputMessages, `"arguments":{"command":"deploy --token [REDACTED:PUSH_TOKEN] origin"}`)
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

	input := recordedInput(t, newContentCollectorIfEnabled(env), "validator said: "+fullwidth(secret), false)["gen_ai.input.messages"].AsString()
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
		Summary:   "deploy --token " + secret[:20] + "…",
		Arguments: `{"command":"deploy --token ` + secret + ` origin"}`,
	})
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Summary: "ls", Arguments: `{"command":"ls"}`})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret[:5])
	assert.Contains(t, res.OutputMessages, `{"type":"tool_call","name":"Bash","arguments":{"command":"deploy --token [REDACTED:PUSH_TOKEN] origin"}}`)
	assert.Contains(t, res.OutputMessages, `"summary":"ls"`, "a call whose arguments held no value keeps its summary")
}

func TestContentCollector_DropsTheSummaryWhenAnEscapeSequenceHidAToken(t *testing.T) {
	// The normalizer strips the whole sequence from the arguments, token
	// and all; the parser's cut summary lost the terminator, so nothing
	// strips the beginning of the token there.
	token := "ghp_" + strings.Repeat("r", 36)
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{
		Name:      "Bash",
		Summary:   "echo \x1b]" + token[:36],
		Arguments: `{"command":"echo \u001b]` + token + `\u0007"}`,
	})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, token[:36])
	assert.NotContains(t, res.OutputMessages, `"summary"`)
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

func TestContentCollector_ReplacesProviderOnlyValuesFromTheProcessEnv(t *testing.T) {
	// GH_WORKFLOW_TOKEN is kept out of RunnerEnv (#6649); the collector
	// reads it where redactFeedback does.
	const secret = "workflow-only-opaque-value"
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	t.Setenv(workflowTokenEnv, secret)
	c := newContentCollectorIfEnabled(nil)
	c.Handle(agentruntime.TextEvent{Text: "token " + secret})
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"command":"echo ` + secret + `"}`})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, secret)
	assert.Equal(t, 2, strings.Count(res.OutputMessages, "[REDACTED:"+workflowTokenEnv+"]"))
	assert.Equal(t, 2, runnerEnvFindings(res), "counted like a runner env value")
}

func TestContentCollector_ReplacesBothValuesWhenRunnerEnvReusesAProviderOnlyName(t *testing.T) {
	// Nothing refuses GH_WORKFLOW_TOKEN as a runner env key; the process
	// value is still the one redactFeedback replaces.
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	t.Setenv(workflowTokenEnv, "from-the-process-env")
	c := newContentCollectorIfEnabled(map[string]string{workflowTokenEnv: "from-the-runner-env"})
	c.Handle(agentruntime.TextEvent{Text: "a from-the-process-env b from-the-runner-env"})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"a [REDACTED:`+workflowTokenEnv+`] b [REDACTED:`+workflowTokenEnv+`]"`)
}

func TestContentCollector_ReplacesARunnerEnvValueSentAsANumber(t *testing.T) {
	// A number outside the list is scanned as its digits before it is
	// dropped, so the literal pass still counts it.
	digits := strings.Repeat("7", minRedactableSecretLen)
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")
	c := newContentCollectorIfEnabled(map[string]string{"DEPLOY_PASSWORD": digits})
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"pin":` + digits + `,"timeout":42}`})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `"arguments":{"timeout":42}`)
	assert.NotContains(t, res.OutputMessages, digits)
	assert.Equal(t, 1, runnerEnvFindings(res))
	assert.Zero(t, res.DroppedBytes, "a member the list leaves out is not charged")
}

func TestContentCollector_ANameRedactedAwayNeverHadArguments(t *testing.T) {
	// Arguments are kept only under a tool's exact name, and those names
	// are plain words no scan touches: a name that redacts to nothing was
	// never one of them, so nothing is lost with it — the part survives
	// with its summary, unmarked and uncharged.
	name := string(rune(0x200B))
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: name, Summary: "ls", Arguments: `{"command":"ls"}`})
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: name, Arguments: `{"command":"ls"}`})

	res := c.Result("stop")
	assert.Contains(t, res.OutputMessages, `{"type":"tool_call","summary":"ls"}`)
	assert.NotContains(t, res.OutputMessages, "arguments")
	assert.NotContains(t, res.OutputMessages, "toolu_01", "nothing is left of the part without a summary")
	assert.Len(t, res.Findings, 2)
	assert.False(t, res.Truncated)
	assert.Zero(t, res.DroppedBytes)
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
	// Arguments are scanned when the event is handled, and neither
	// eviction nor Result scans them again.
	c := newContentCollector(64)
	scans := countScans(c, "psql") // survives redaction, so a second scan would see it
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"command":"psql postgres://u:hunter2hunter2@db/x"}`})
	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("x", 200)}) // evicts the call

	assert.Equal(t, []string{"db_connection_password", "url_masked"}, findingNames(c.Result("stop").Findings))
	assert.Equal(t, 1, *scans)
}

func TestContentCollector_FullwidthQuotesCannotAddAMember(t *testing.T) {
	// NFKC folds a fullwidth quotation mark to '"'. Folded inside the
	// serialised text it would close the string and add a member.
	c := newContentCollector(4096)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: `{"command":"a\uFF02,\uFF02injected\uFF02:\uFF02b"}`})

	part := partAt(t, decodeOutputMessages(t, c.Result("stop").OutputMessages), 0)
	assert.Equal(t, map[string]any{"command": `a","injected":"b`}, part["arguments"])
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
	c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Summary: "/x", Arguments: `{"command": "` + body + `"}`})

	res := c.Result("stop")
	part := partAt(t, decodeOutputMessages(t, res.OutputMessages), 0)
	assert.NotContains(t, part, "arguments", "a cut object is not JSON, so nothing partial is kept")
	assert.Equal(t, "/x", part["summary"])
	assert.Equal(t, "toolu_01", part["id"])
	assert.Equal(t, true, part["fullsend.truncated"])
	assert.True(t, res.Truncated)
	assert.Equal(t, len(`{"command":"`+body+`"}`), res.DroppedBytes, "charged as re-encoded")
}

func TestContentCollector_ArgumentsAtTheBoundAreKept(t *testing.T) {
	args := `{"command":"` + strings.Repeat("x", maxToolArgumentsBytes-len(`{"command":""}`)) + `"}`
	require.Len(t, args, maxToolArgumentsBytes)
	c := newContentCollector(maxContentBytes)
	c.Handle(agentruntime.ToolUseEvent{Name: "Bash", Arguments: args})

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
	// same bytes again (bulkScanned).
	c := newContentCollector(maxContentBytes)
	scans := countScans(c, "scanned-once") // survives redaction and the cut
	c.Handle(agentruntime.ToolResultEvent{
		ID:     "toolu_db",
		Result: strings.Repeat("x", maxToolResultBytes) + " scanned-once postgres://user:supersecret@host",
	})

	res := c.Result("stop")
	assert.NotContains(t, res.OutputMessages, "supersecret")
	assert.Equal(t, []string{"db_connection_password", "url_masked"}, findingNames(res.Findings))
	assert.Equal(t, 1, *scans)
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
	// '<' and control byte. The exported attribute must still fit the
	// encoded ceiling, keeping as much of the tail as fits. (An invalid
	// byte never reaches the encoder: a string holding one is masked whole
	// by redactText.)
	for _, unit := range []string{"<", "\x01"} {
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
func recordedInput(t *testing.T, c *contentCollector, prompt string, alteredUpstream bool) map[attribute.Key]attribute.Value {
	t.Helper()
	_, rec, span := toolSpanFixture(t)
	c.attachInput(span, prompt, alteredUpstream)
	span.End()
	ended := rec.Ended()
	require.Len(t, ended, 1)
	return toolSpanAttrs(tracetest.SpanStubFromReadOnlySpan(ended[0]))
}

func TestAttachInput_RecordsTheRetryPromptAsOneUserMessage(t *testing.T) {
	prompt, _ := buildFeedbackPrompt("lint: main.go:3 unused variable <x>")
	attrs := recordedInput(t, newContentCollector(maxContentBytes), prompt, false)

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
	assert.Empty(t, recordedInput(t, newContentCollector(maxContentBytes), "", false),
		"no composed prompt (the first iteration, or feedback_mode off)")
	assert.Empty(t, recordedInput(t, nil, "would-be prompt", false), "gate off")
}

func TestAttachInput_RedactsAndCountsFindings(t *testing.T) {
	secret := "ghp_" + strings.Repeat("k", 36)
	c := newContentCollector(maxContentBytes)
	in := recordedInput(t, c, "token "+secret+" leaked", false)["gen_ai.input.messages"].AsString()

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
	in := recordedInput(t, c, prompt, false)["gen_ai.input.messages"].AsString()
	require.Greater(t, len(in), 10*maxFeedbackBytes, "fixture must expand for this test to prove anything")

	c.Handle(agentruntime.TextEvent{Text: strings.Repeat("<", maxContentBytes)}) // encodes sixfold
	out := c.Result("stop").OutputMessages
	require.NotEmpty(t, out)
	assert.LessOrEqual(t, len(in)+len(out), maxEncodedContentBytes,
		"both attributes ride one span; together they stay within the proven size")
	assert.Greater(t, len(in)+len(out), maxEncodedContentBytes-100,
		"the output keeps everything the input left")
}

// scanCounter counts the strings holding marker that reach a pipeline.
type scanCounter struct {
	marker string
	n      *int
}

func (s scanCounter) Name() string { return "scan_counter" }

func (s scanCounter) Scan(text string) security.ScanResult {
	if strings.Contains(text, s.marker) {
		*s.n++
	}
	return security.ScanResult{Safe: true}
}

// countScans puts a scanCounter ahead of c's pipeline and returns its count.
func countScans(c *contentCollector, marker string) *int {
	n := new(int)
	c.pipeline = security.NewPipeline(scanCounter{marker, n}, security.NewUnicodeNormalizer(), security.NewSecretRedactor())
	return n
}

func findingNames(fs []security.Finding) []string {
	names := make([]string, 0, len(fs))
	for _, f := range fs {
		names = append(names, f.Name)
	}
	return names
}
