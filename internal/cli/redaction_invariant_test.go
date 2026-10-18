package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// TestRedaction_NoSecretSurvivesAnyWrapping states what the Level 3
// record and the execute_tool spans guarantee, and checks it over every
// hostile wrapping a reviewer has constructed so far, enumerated — not
// one example per finding. The invariant: for every string the record or
// a span exports, no 12-byte run of a known secret's body appears, read
// three ways — as raw bytes, after the normalizer, and after decoding the
// record's JSON with every string walked. A known secret is one a
// SecretRedactor pattern matches, a runner env / provider-only env
// value the literal pass holds, or what a URL carries before its host or
// after its path — userinfo, a query, a fragment.
//
// Named exclusions, skipped below with the reason beside them and listed
// in docs/guides/dev/tracing.md ("Guarantee and limits"): an env value
// written in compatibility (fullwidth) forms, since the normalizer
// rewrites it and the literal pass matches only what the env has; a
// secret split across two parts or events; a secret matching no pattern
// and in no env — a token in a URL's path, in a URL holding a double
// quote that ends it (urlRun), what follows the quote and userinfo whose
// '@' follows it among them; a summary a pi or codex parser cut before the collector
// saw it, since that cut can split a value the parser's own scan did not
// see joined (a Claude summary is cut from a member the collector scans
// whole). Nothing else is excluded.
func TestRedaction_NoSecretSurvivesAnyWrapping(t *testing.T) {
	const window = 12

	// Deterministic bodies: lowercase letters and digits, so no 12-byte
	// window can occur in the noise (lowercase letters only) or in a mask.
	alnum := func(seed uint32, n int, alphabet string) string {
		var b strings.Builder
		x := seed
		for i := 0; i < n; i++ {
			x = x*1664525 + 1013904223
			b.WriteByte(alphabet[int(x>>24)%len(alphabet)])
		}
		return b.String()
	}
	const lowerDigits = "abcdefghijklmnopqrstuvwxyz0123456789"
	const upperDigits = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	noise := func(seed uint32, n int) string { return alnum(seed, n, "abcdefghijklmnopqrstuvwxyz") }

	type secret struct {
		name string
		text string // the whole secret as a stream would carry it
		body string // the part whose 12-byte windows must not survive
		env  bool   // held by the literal pass, not a pattern
	}
	envValue := alnum(11, 32, lowerDigits)
	providerValue := alnum(12, 32, lowerDigits)
	runnerEnv := map[string]string{"DEPLOY_PASSWORD": envValue}
	t.Setenv(workflowTokenEnv, providerValue)
	pemBody := alnum(13, 64, lowerDigits) + "\n" + alnum(14, 64, lowerDigits)
	pemHeader := "-----BEGIN " + "PRIVATE KEY-----" // split so the private-key lint stays quiet
	pemFooter := "-----END " + "PRIVATE KEY-----"
	mk := func(name string, seed uint32) [2]secret { // two instances, for the two-secrets wrapping
		var out [2]secret
		for i := range out {
			b := alnum(seed+uint32(i), 36, lowerDigits)
			switch name {
			case "github_pat":
				out[i] = secret{name, "ghp_" + b, b, false}
			case "aws_access_key":
				u := alnum(seed+uint32(i), 16, upperDigits)
				out[i] = secret{name, "AKIA" + u, u, false}
			case "runner_env":
				out[i] = secret{name, envValue, envValue, true}
			case "provider_env":
				out[i] = secret{name, providerValue, providerValue, true}
			case "private_key":
				out[i] = secret{name, pemHeader + "\n" + pemBody + "\n" + pemFooter, pemBody[:64], false}
			case "db_connection_password":
				p := b[:16]
				out[i] = secret{name, "postgres://user:" + p + "@db.example.com/app", p, false}
			case "json_field":
				p := b[:16]
				out[i] = secret{name, `"password":"` + p + `"`, p, false}
			case "env_assignment":
				out[i] = secret{name, "AWS_SECRET_ACCESS_KEY=" + b, b, false}
			case "auth_header":
				out[i] = secret{name, "Authorization: Bearer " + b[:32], b[:32], false}
			case "url_userinfo":
				out[i] = secret{name, "https://deploy:" + b[:16] + "@git.example.com/o/r", b[:16], false}
			case "url_userinfo_quoted": // shell quoting around the password
				out[i] = secret{name, "https://deploy:'" + b[:16] + "'@git.example.com/o/r", b[:16], false}
			case "url_userinfo_fullwidth": // a colon and an at sign the normalizer folds
				out[i] = secret{name, "https://deploy" + string(rune(0xff1a)) + b[:16] + string(rune(0xff20)) + "git.example.com/o/r", b[:16], false}
			case "url_query":
				out[i] = secret{name, "https://h.example.com/x?access_token=" + b[:16], b[:16], false}
			case "url_query_fullwidth": // a question mark the normalizer folds
				out[i] = secret{name, "https://h.example.com/x" + string(rune(0xff1f)) + "access_token=" + b[:16], b[:16], false}
			case "url_fragment":
				out[i] = secret{name, "https://app.example.com/cb#access_token=" + b[:16], b[:16], false}
			case "url_userinfo_escaped": // separators escaped for the shell or JSON
				out[i] = secret{name, `https:\/\/deploy:` + b[:16] + `@git.example.com\/o\/r`, b[:16], false}
			case "url_query_escaped":
				out[i] = secret{name, `https:\/\/h.example.com\/x?access_token=` + b[:16], b[:16], false}
			case "url_userinfo_slash": // a base64 password holds a '/'
				out[i] = secret{name, "https://deploy:ab+c/" + b[:16] + "=@git.example.com/o/r", b[:16], false}
			case "url_userinfo_double_quote": // a password holding a quote
				out[i] = secret{name, "https://deploy:" + b[:16] + `"x@git.example.com/o/r`, b[:16], false}
			case "url_userinfo_question": // a password holding a '?'
				out[i] = secret{name, "https://deploy:" + b[:16] + "?rest@git.example.com/o/r", b[:16], false}
			case "url_userinfo_hash": // a password holding a '#'
				out[i] = secret{name, "https://deploy:" + b[:16] + "#rest@git.example.com/o/r", b[:16], false}
			case "url_userinfo_escaped_colon": // the colon escaped for the shell too
				out[i] = secret{name, `https\:\/\/deploy:` + b[:16] + `@git.example.com/o/r`, b[:16], false}
			case "url_percent_encoded": // the whole URL percent-encoded, as a query value is
				out[i] = secret{name, "https%3A%2F%2Fdeploy%3A" + b[:16] + "%40git.example.com%2Fo%2Fr", b[:16], false}
			case "url_percent_encoded_nested": // percent-encoded, in a plain URL's path
				out[i] = secret{name, "https://proxy.example.com/fetch/https%3A%2F%2Fdeploy%3A" + b[:16] + "%40git.example.com", b[:16], false}
			case "url_percent_encoded_twice": // encoded in a query that was itself encoded
				out[i] = secret{name, "https%253A%252F%252Fdeploy%253A" + b[:16] + "%2540git.example.com", b[:16], false}
			case "url_mixed_colon_encoded": // the colon encoded, the slashes plain (Python's quote)
				out[i] = secret{name, "https%3A//deploy%3A" + b[:16] + "%40git.example.com/o/r", b[:16], false}
			case "url_mixed_colon_encoded_nested": // the same in a plain URL's path
				out[i] = secret{name, "https://proxy.example.com/fetch/https%3A//deploy%3A" + b[:16] + "%40git.example.com", b[:16], false}
			case "url_mixed_slashes_encoded": // the slashes encoded, the colon plain (Go's PathEscape)
				out[i] = secret{name, "https:%2F%2Fdeploy:" + b[:16] + "@git.example.com%2Fo%2Fr", b[:16], false}
			case "url_mixed_slashes_encoded_nested": // the same in a plain URL's path
				out[i] = secret{name, "https://proxy.example.com/fetch/https:%2F%2Fdeploy:" + b[:16] + "@git.example.com%2Fo%2Fr", b[:16], false}
			case "url_userinfo_percent_encoded": // userinfo encoded behind a plain separator
				out[i] = secret{name, "https://deploy%3A" + b[:16] + "%40git.example.com/o/r", b[:16], false}
			case "url_userinfo_percent_encoded_nested": // the same in a plain URL's path
				out[i] = secret{name, "https://proxy.example.com/fetch/https://deploy%3A" + b[:16] + "%40git.example.com", b[:16], false}
			case "url_query_percent_encoded": // the query's '?' encoded behind a plain separator
				out[i] = secret{name, "https://h.example.com/x%3Faccess_token=" + b[:16], b[:16], false}
			case "url_fragment_percent_encoded": // the fragment's '#' encoded behind a plain separator
				out[i] = secret{name, "https://app.example.com/cb%23access_token=" + b[:16], b[:16], false}
			case "url_nested": // a URL in another's path
				out[i] = secret{name, "https://proxy.example.com/https://u:" + b[:16] + "@h.example.com/x", b[:16], false}
			}
		}
		return out
	}
	secrets := [][2]secret{
		mk("github_pat", 100), mk("aws_access_key", 200), mk("runner_env", 300), mk("provider_env", 400),
		mk("private_key", 500), mk("db_connection_password", 600), mk("json_field", 700),
		mk("env_assignment", 800), mk("auth_header", 900),
		mk("url_userinfo", 1000), mk("url_userinfo_quoted", 1100), mk("url_userinfo_fullwidth", 1200),
		mk("url_query", 1300), mk("url_query_fullwidth", 1400), mk("url_fragment", 1500), mk("url_nested", 1600),
		mk("url_userinfo_escaped", 1700), mk("url_query_escaped", 1800),
		mk("url_userinfo_slash", 1900), mk("url_userinfo_double_quote", 2000), mk("url_userinfo_question", 2100), mk("url_userinfo_hash", 2200), mk("url_userinfo_escaped_colon", 2300), mk("url_percent_encoded", 2400), mk("url_percent_encoded_nested", 2500), mk("url_percent_encoded_twice", 2600),
		mk("url_mixed_colon_encoded", 2700), mk("url_mixed_colon_encoded_nested", 2800), mk("url_mixed_slashes_encoded", 2900), mk("url_mixed_slashes_encoded_nested", 3000), mk("url_userinfo_percent_encoded", 3100), mk("url_userinfo_percent_encoded_nested", 3200), mk("url_query_percent_encoded", 3300), mk("url_fragment_percent_encoded", 3400),
	}

	// Wrappings over the secret text; the ones that insert a character
	// do so at an offset into the body.
	insert := func(s secret, off int, ins string) string {
		i := strings.Index(s.text, s.body) + off
		return s.text[:i] + ins + s.text[i:]
	}
	zwSplit := func(s secret) string { return insert(s, len(s.body)/2, "\u200b") }
	fullwidth := func(s secret) string {
		var b strings.Builder
		for _, r := range s.body {
			if r >= 0x21 && r <= 0x7e {
				r += 0xfee0
			}
			b.WriteRune(r)
		}
		return strings.Replace(s.text, s.body, b.String(), 1)
	}
	tags := func(s secret) string {
		var b strings.Builder
		for i, r := range s.body {
			if i > 0 && i%4 == 0 {
				b.WriteRune(0xe0041) // TAG LATIN CAPITAL LETTER A
			}
			b.WriteRune(r)
		}
		return strings.Replace(s.text, s.body, b.String(), 1)
	}
	type wrapping struct {
		name      string
		wrap      func(a, b secret) string
		excluded  func(s secret) string // non-empty = the named exclusion's reason
		twoBodies bool
	}
	envExcluded := func(s secret) string {
		if s.env {
			return "an env value in compatibility forms: the normalizer rewrites it and the literal pass matches only what the env has"
		}
		return ""
	}
	none := func(secret) string { return "" }
	wrappings := []wrapping{
		{"plain", func(a, _ secret) string { return "see " + a.text + " here" }, none, false},
		{"csi eats the first letter", func(a, _ secret) string { return "\x1b[" + a.text }, none, false},
		{"csi eats the first letter, zero-width inside", func(a, _ secret) string { return "\x1b[" + zwSplit(a) }, none, false},
		{"csi colour around", func(a, _ secret) string { return "\x1b[31m" + a.text + "\x1b[0m" }, none, false},
		{"csi inside", func(a, _ secret) string { return insert(a, 3, "\x1b[0m") }, none, false},
		{"osc bel around", func(a, _ secret) string { return "\x1b]" + a.text + "\x07" }, none, false},
		{"osc st before", func(a, _ secret) string { return "\x1b]8;;x\x1b\\" + a.text }, none, false},
		{"repeated after csi", func(a, _ secret) string { return "\x1b[" + a.text + " " + a.text }, none, false},
		{"two secrets, second zero-width split", func(a, b secret) string { return a.text + " " + zwSplit(b) }, none, true},
		{"two secrets after csi, second zero-width split", func(a, b secret) string { return "\x1b[" + a.text + " " + zwSplit(b) }, none, true},
		{"U+200B at body+1", func(a, _ secret) string { return insert(a, 1, "\u200b") }, none, false},
		{"U+FEFF at body+4", func(a, _ secret) string { return insert(a, 4, "\ufeff") }, none, false},
		{"U+2060 at body middle", func(a, _ secret) string { return insert(a, len(a.body)/2, "\u2060") }, none, false},
		{"bidi override around", func(a, _ secret) string { return "\u202e" + a.text + "\u202c" }, none, false},
		{"tag characters every four bytes", func(a, _ secret) string { return tags(a) }, none, false},
		{"NUL inside", func(a, _ secret) string { return insert(a, 5, "\x00") }, none, false},
		{"invalid byte inside the body", func(a, _ secret) string { return insert(a, 1, "\xff") }, none, false},
		{"replacement character inside the body", func(a, _ secret) string { return insert(a, 1, "\ufffd") }, none, false},
		{"invalid byte after the body, secret straddling the summary cut", func(a, _ secret) string { return noise(4, 100) + " " + a.text + " \xff" }, none, false},
		{"variation selector inside", func(a, _ secret) string { return insert(a, 5, "\ufe0f") }, none, false},
		{"fullwidth body", func(a, _ secret) string { return fullwidth(a) }, envExcluded, false},
		{"inside 2 KiB of noise each side", func(a, _ secret) string { return noise(1, 2048) + " " + a.text + " " + noise(2, 2048) }, none, false},
		{"fullwidth body straddling the feedback cut", func(a, _ secret) string { return noise(3, maxFeedbackBytes-48) + " " + fullwidth(a) }, envExcluded, false},
		{"body straddling the feedback cut, 12 bytes before it", func(a, _ secret) string {
			return noise(5, maxFeedbackBytes-1-window-strings.Index(a.text, a.body)) + " " + a.text
		}, none, false},
	}

	// Views of one exported string.
	normalizer := security.NewUnicodeNormalizer()
	views := func(out string) []string {
		vs := []string{out}
		if n := normalizer.Scan(out); n.Sanitized != "" {
			vs = append(vs, n.Sanitized)
		}
		if json.Valid([]byte(out)) {
			var v any
			if json.Unmarshal([]byte(out), &v) == nil {
				var walk func(any)
				walk = func(x any) {
					switch t := x.(type) {
					case string:
						vs = append(vs, t)
						if n := normalizer.Scan(t); n.Sanitized != "" {
							vs = append(vs, n.Sanitized)
						}
					case []any:
						for _, e := range t {
							walk(e)
						}
					case map[string]any:
						for _, e := range t {
							walk(e)
						}
					}
				}
				walk(v)
			}
		}
		return vs
	}
	survives := func(out, body string) string {
		for _, v := range views(out) {
			for i := 0; i+window <= len(body); i++ {
				if strings.Contains(v, body[i:i+window]) {
					return body[i : i+window]
				}
			}
		}
		return ""
	}

	// Surfaces: each takes the wrapped text and returns what was exported.
	collector := func() *contentCollector {
		c := newContentCollector(maxContentBytes)
		c.runnerEnv = runnerEnv
		return c
	}
	// jsonStr quotes text as a JSON string while keeping its bytes as the
	// stream would carry them: json.Marshal would coerce an invalid byte to
	// U+FFFD before the collector could see it.
	jsonStr := func(text string) string {
		var b strings.Builder
		b.WriteByte('"')
		for i := 0; i < len(text); i++ {
			switch c := text[i]; {
			case c == '"' || c == '\\':
				b.WriteByte('\\')
				b.WriteByte(c)
			case c < 0x20:
				fmt.Fprintf(&b, "\\u%04x", c)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteByte('"')
		return b.String()
	}

	type surface struct {
		name string
		run  func(t *testing.T, text string) []string
	}
	viaCollector := func(f func(c *contentCollector, text string)) func(*testing.T, string) []string {
		return func(_ *testing.T, text string) []string {
			c := collector()
			f(c, text)
			return []string{c.Result("stop").OutputMessages}
		}
	}
	viaTracker := func(f func(tr *toolSpanTracker, text string)) func(*testing.T, string) []string {
		return func(t *testing.T, text string) []string {
			tr, rec, _ := toolSpanFixture(t)
			tr.runnerEnv = runnerEnv
			f(tr, text)
			var out []string
			for _, s := range endedToolSpans(rec) {
				out = append(out, s.Name)
				for _, kv := range s.Attributes {
					out = append(out, kv.Value.Emit())
				}
			}
			return out
		}
	}
	surfaces := []surface{
		{"redactText", func(_ *testing.T, text string) []string {
			var findings []security.Finding
			return []string{redactText(security.OutputPipeline(), runnerEnv, text, &findings)}
		}},
		{"text part", viaCollector(func(c *contentCollector, text string) { c.Handle(agentruntime.TextEvent{Text: text}) })},
		{"call name", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: text})
		})},
		{"call summary", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Summary: text})
		})},
		{"listed argument", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Arguments: `{"command":` + jsonStr(text) + `}`})
		})},
		{"unlisted argument", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Write", Arguments: `{"file_path":"/x","content":` + jsonStr(text) + `}`})
		})},
		{"array under a listed member", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Grep", Arguments: `{"path":[` + jsonStr(text) + `]}`})
		})},
		{"Claude call with its derived summary", viaCollector(func(c *contentCollector, text string) {
			// As the Claude parser derives a Bash summary: the member decoded
			// into a Go string (an invalid byte becomes U+FFFD) and cut to
			// maxCommandDisplay runes.
			var cmd string
			_ = json.Unmarshal([]byte(jsonStr(text)), &cmd)
			summary := cmd
			if r := []rune(cmd); len(r) > 120 {
				summary = string(r[:120]) + "…"
			}
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Summary: summary, Arguments: `{"command":` + jsonStr(text) + `}`})
		})},
		{"arguments that are not JSON", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Arguments: text})
		})},
		{"trailing input after the object", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash", Arguments: `{"command":"ls"} ` + text})
		})},
		{"tool result", viaCollector(func(c *contentCollector, text string) {
			c.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: "Bash"})
			c.Handle(agentruntime.ToolResultEvent{ID: "toolu_01", Result: text})
		})},
		{"input message", func(t *testing.T, text string) []string {
			// The real path: the feedback is redacted, then composed into the
			// retry prompt (sanitized, cut, framed), then recorded.
			fb := redactFeedback(text, runnerEnv)
			prompt, sanitized := buildFeedbackPrompt(fb)
			attrs := recordedInput(t, collector(), prompt, promptHidesAlterations(fb, sanitized))
			var out []string
			for _, v := range attrs {
				out = append(out, v.Emit())
			}
			return out
		}},
		{"span name", viaTracker(func(tr *toolSpanTracker, text string) {
			tr.Handle(agentruntime.ToolUseEvent{ID: "toolu_01", Name: text})
			tr.Handle(agentruntime.ToolResultEvent{ID: "toolu_01"})
		})},
		{"span id", viaTracker(func(tr *toolSpanTracker, text string) {
			tr.Handle(agentruntime.ToolUseEvent{ID: text, Name: "Bash"})
			tr.Handle(agentruntime.ToolResultEvent{ID: text})
		})},
	}

	for _, pair := range secrets {
		a, b := pair[0], pair[1]
		for _, w := range wrappings {
			for _, s := range surfaces {
				t.Run(fmt.Sprintf("%s/%s/%s", a.name, w.name, s.name), func(t *testing.T) {
					if reason := w.excluded(a); reason != "" {
						t.Skip("named exclusion: " + reason)
					}
					text := w.wrap(a, b)
					require.NotEmpty(t, survives(text, a.body), "control: the detector must see the secret in the input")
					for _, out := range s.run(t, text) {
						if run := survives(out, a.body); run != "" {
							t.Fatalf("%q of the first secret survives in %q", run, out)
						}
						if w.twoBodies {
							if run := survives(out, b.body); run != "" {
								t.Fatalf("%q of the second secret survives in %q", run, out)
							}
						}
					}
				})
			}
		}
	}
}
