package security

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizeJSONDocument_DuplicateKeyEarlierOccurrenceRedacted pins that a
// secret in an earlier, overwritten duplicate-key occurrence is still found.
// Map decoding drops that occurrence, so only a token-preserving walk sees it.
func TestSanitizeJSONDocument_DuplicateKeyEarlierOccurrenceRedacted(t *testing.T) {
	p := OutputPipeline()
	// \u0067 decodes to "g"; the raw text never contains "ghp_".
	escapedSecret := `\u0067hp_FAKEtesttoken000000000000000000000000`
	doc := []byte(`{"note":"` + escapedSecret + `","note":"clean"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized duplicate-key JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
	assert.NotContains(t, string(out), `\u0067hp_`)
	assert.Contains(t, string(out), `"note":"clean"`, "the surviving duplicate value must be preserved")
}

// TestSanitizeJSONDocument_DuplicateKeysSurviveRewrite pins that the
// token-preserving walk keeps duplicate keys and their order when another
// member forces a rewrite.
func TestSanitizeJSONDocument_DuplicateKeysSurviveRewrite(t *testing.T) {
	p := OutputPipeline()
	doc := []byte(`{"a":1,"a":2,"token":"ghp_FAKEtesttoken000000000000000000000000"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.Contains(t, string(out), `"a":1,"a":2`, "duplicate members must keep their order")
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
}

// TestSanitizeJSONDocument_PrivateKeySplitAcrossMembersFailsClosed pins that a
// private key spread over several members is detected in the bytes that are
// actually written. Scanning only the re-encoded form missed it: encoding a map
// sorts keys, and with the END marker sorted before the BEGIN marker the
// pattern no longer matched the original key material.
func TestSanitizeJSONDocument_PrivateKeySplitAcrossMembersFailsClosed(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	// Reverse the members' sort order so a map re-encode would emit END before
	// BEGIN and hide the block from a serialized scan.
	doc := []byte(`{"z_begin":"` + begin + `","m_body":"MIIEpAIBAAKCAQEA0123456789abcdef","a_end":"` + end + `"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "a framing-spanning private key must produce a finding")
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "BEGIN RSA")
	assert.NotContains(t, string(out), "MIIEpAIBAAKCAQEA")
	assert.Contains(t, string(out), "output removed", "an unmappable match must fail closed")
}

// TestSanitizeJSONDocument_FramingSecretInCleanDocumentFailsClosed pins that a
// document with no leaf findings is still scanned in its written form. A
// runtime secret spanning two member boundaries is visible only in the raw
// bytes, and the previous no-findings path returned those bytes untouched.
func TestSanitizeJSONDocument_FramingSecretInCleanDocumentFailsClosed(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const secret = `","field2":"`
	require.True(t, RegisterRuntimeSecret(secret))

	p := OutputPipeline()
	doc := []byte(`{"field1":"foo","field2":"bar"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "a framing-spanning secret must produce a finding")
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), secret)
}

// TestSanitizeJSONDocument_CrossMemberPrivateKeyWithLeafSecretFailsClosed pins
// that a leaf secret and a framing-spanning private key in the same document
// are both handled: the rewrite keeps member order, so the framing-spanning
// block is still visible in the rewritten bytes.
func TestSanitizeJSONDocument_CrossMemberPrivateKeyWithLeafSecretFailsClosed(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"z_begin":"` + begin + `","m_body":"MIIEpAIBAAKCAQEA0123456789abcdef","a_end":"` + end + `",` +
		`"token":"ghp_FAKEtesttoken000000000000000000000000"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
	assert.NotContains(t, string(out), "PRIVATE KEY-----")
}

// TestSanitizeJSONDocument_DuplicateKeySanitizedKeyCollision pins that a
// sanitized key never steals the name of an existing clean member, and that
// intentional duplicate keys still survive.
func TestSanitizeJSONDocument_DuplicateKeySanitizedKeyCollision(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const secret = "abcdefghijk"
	require.True(t, RegisterRuntimeSecret(secret))

	p := OutputPipeline()
	doc := []byte(`{"` + secret + `":1,"***":2,"d":0,"d":1}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), secret)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(out, &decoded))
	assert.Equal(t, float64(2), decoded["***"], "the clean key must keep its own value")
	assert.Equal(t, float64(1), decoded["***_1"], "the sanitized key must be disambiguated")
	assert.Equal(t, 2, strings.Count(string(out), `"d":`), "intentional duplicate keys must survive")
}

// TestSanitizeJSONLines_CrossRecordRuntimeSecretFailsClosed pins that a
// runtime secret spanning two individually valid records is not discarded.
func TestSanitizeJSONLines_CrossRecordRuntimeSecretFailsClosed(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const secret = `one"}` + "\n" + `{"key`
	require.True(t, RegisterRuntimeSecret(secret))

	p := OutputPipeline()
	content := []byte("{\"key_one\":\"one\"}\n{\"key_two\":\"two\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.NotContains(t, string(out), secret, "the cross-record secret must not survive")

	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 2, "record count must not change")
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)), "each record must stay valid JSON: %s", line)
		assert.Contains(t, line, "output removed")
	}
}

// TestSanitizeJSONLines_DisjointCrossRecordMatchesKeepCleanRecord pins that
// only the records a cross-record match touches are replaced. A single
// first-to-last changed region used to swallow every record between two
// disjoint matches.
func TestSanitizeJSONLines_DisjointCrossRecordMatchesKeepCleanRecord(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const first = `one"}` + "\n" + `{"b`
	const second = `three"}` + "\n" + `{"d`
	require.True(t, RegisterRuntimeSecret(first))
	require.True(t, RegisterRuntimeSecret(second))

	p := OutputPipeline()
	content := []byte(
		"{\"a\":\"one\"}\n" +
			"{\"b\":\"two\"}\n" +
			"{\"clean\":\"middle\"}\n" +
			"{\"c\":\"three\"}\n" +
			"{\"d\":\"four\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.NotContains(t, string(out), first)
	assert.NotContains(t, string(out), second)

	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 5, "record count must not change")
	assert.Equal(t, `{"clean":"middle"}`, lines[2], "the clean record between two matches must survive")
	for _, i := range []int{0, 1, 3, 4} {
		assert.True(t, json.Valid([]byte(lines[i])), "each replaced record must stay valid JSON: %s", lines[i])
		assert.Contains(t, lines[i], "output removed")
	}
}

// TestSanitizeJSONDocument_BackstopRedactsSecondRuntimeSecret pins that a
// runtime secret exposed only by serializing the parsed document is still
// handled when another runtime secret was already found in a leaf.
func TestSanitizeJSONDocument_BackstopRedactsSecondRuntimeSecret(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const leafSecret = "leafonlysecretvalue"
	const serializedSecret = `","field2":"`
	require.True(t, RegisterRuntimeSecret(leafSecret))
	require.True(t, RegisterRuntimeSecret(serializedSecret))

	p := OutputPipeline()
	doc := []byte(`{"field1":"leafonlysecretvalue","field2":"bar"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), leafSecret)
	assert.NotContains(t, string(out), serializedSecret)
}

// TestSanitizeJSONDocument_KeyRemovedEntirelyIsReplaced pins that a JSON key
// made only of removable characters never survives sanitization.
func TestSanitizeJSONDocument_KeyRemovedEntirelyIsReplaced(t *testing.T) {
	p := OutputPipeline()
	for name, key := range map[string]string{
		"null byte":     "\x00",
		"bidi override": "\u202e",
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := json.Marshal(map[string]string{key: "value"})
			require.NoError(t, err)

			out, findings, err := SanitizeJSONDocument(doc, p)
			require.NoError(t, err)
			require.NotEmpty(t, findings)
			assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
			assert.NotContains(t, string(out), `\u0000`)
			assert.NotContains(t, string(out), `\u202e`)

			var decoded map[string]string
			require.NoError(t, json.Unmarshal(out, &decoded))
			assert.Equal(t, "value", decoded[redactedJSONKey])
		})
	}
}

// TestSanitizeJSONLines_SingleRecordPEMKeepsCleanRecords pins that a private
// key confined to one record is redacted in place instead of replacing every
// record in the file.
func TestSanitizeJSONLines_SingleRecordPEMKeepsCleanRecords(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(
		"{\"event\":\"clean-before\"}\n" +
			"{\"tool\":\"read\",\"text\":\"" + begin + "\\nMIIEpAIBAAKCAQEA0123456789abcdef\\n" + end + "\"}\n" +
			"{\"event\":\"clean-after\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)

	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 3, "record count must not change")
	assert.Equal(t, `{"event":"clean-before"}`, lines[0], "clean records must be untouched")
	assert.Equal(t, `{"event":"clean-after"}`, lines[2], "clean records must be untouched")
	assert.True(t, json.Valid([]byte(lines[1])))
	assert.NotContains(t, lines[1], "BEGIN RSA")
	assert.NotContains(t, lines[1], "MIIEpAIBAAKCAQEA")
}

// TestSanitizeJSONDocument_AlreadyMaskedValueNotRemasked pins that rescanning a
// document leaves an existing mask placeholder intact.
func TestSanitizeJSONDocument_AlreadyMaskedValueNotRemasked(t *testing.T) {
	p := OutputPipeline()
	doc := []byte(`{"body":"Guard against null values with token_usage = metrics.get(\"token_usage\") or {}"}`)

	first, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.Len(t, findings, 1, "one secret must produce one finding")
	assert.Contains(t, string(first), "metr...", "the informative prefix must be retained")

	second, findings, err := SanitizeJSONDocument(first, p)
	require.NoError(t, err)
	assert.Empty(t, findings, "rescanning a sanitized document must be idempotent")
	assert.Equal(t, first, second)
}

// TestSanitizeJSONDocument_DuplicateKeyNestedContainers covers the
// token-preserving walk across nested objects, arrays, every scalar type, and
// an escaped duplicate key whose value is a container.
func TestSanitizeJSONDocument_DuplicateKeyNestedContainers(t *testing.T) {
	p := OutputPipeline()
	escapedSecret := `\u0067hp_FAKEtesttoken000000000000000000000000`
	doc := []byte(`{"outer":[{"id":1,"id":2,"opts":{"flag":true,"none":null,"ratio":1.5}},` +
		`{"token":"` + escapedSecret + `","token":["clean"]}],"tail":false}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")

	var decoded any
	require.NoError(t, json.Unmarshal(out, &decoded))
	outer := decoded.(map[string]any)["outer"].([]any)
	second := outer[1].(map[string]any)
	// The duplicate-key walk keeps the later occurrence, which is the array.
	assert.Equal(t, []any{"clean"}, second["token"])
	assert.Equal(t, false, decoded.(map[string]any)["tail"])
}

// TestSanitizeJSONDocument_ScalarAndContainerCoverage exercises every scalar
// branch and nested collection of the walker.
func TestSanitizeJSONDocument_ScalarAndContainerCoverage(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const numericSecret = "123456789012"
	require.True(t, RegisterRuntimeSecret(numericSecret))

	p := OutputPipeline()
	doc := []byte(`{"list":["plain",42,` + numericSecret + `,true,null],` +
		`"nested":{"deep":[{"value":"plain"}]},"counter":7}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), numericSecret)
	assert.Contains(t, string(out), "42", "clean numbers must keep their precision")
	assert.Contains(t, string(out), `"counter":7`)
}

// TestSanitizeJSONDocument_CleanKeyWinsSanitizedCollision pins that a secret key
// whose mask collides with an existing clean member never steals that member's
// name or value.
func TestSanitizeJSONDocument_CleanKeyWinsSanitizedCollision(t *testing.T) {
	const secret = "ghp_FAKEtesttoken000000000000000000000000"
	const masked = "ghp_..."
	p := OutputPipeline()
	doc := []byte(`{"` + secret + `":"secretkeyval","` + masked + `":"plainval"}`)

	for range 20 {
		out, findings, err := SanitizeJSONDocument(doc, p)
		require.NoError(t, err)
		require.NotEmpty(t, findings)
		assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)

		var decoded map[string]string
		require.NoError(t, json.Unmarshal(out, &decoded))
		assert.Equal(t, "plainval", decoded[masked], "clean key must retain its name and value")
		assert.Len(t, decoded, 2)
		assert.NotContains(t, string(out), secret)
	}
}

// TestSanitizeJSONDocument_LeavesUnsupportedInputAlone pins the fallback
// contract for documents the structural path cannot handle.
func TestSanitizeJSONDocument_LeavesUnsupportedInputAlone(t *testing.T) {
	p := OutputPipeline()
	for name, doc := range map[string]string{
		"not json":       `{"unterminated": `,
		"plain text":     "just prose",
		"empty":          "",
		"trailing value": `{"a":1}{"b":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, findings, err := SanitizeJSONDocument([]byte(doc), p)
			require.NoError(t, err)
			assert.Empty(t, findings)
			assert.Equal(t, doc, string(out), "an unsupported document must be returned unchanged")
		})
	}
}

// TestSanitizeJSONLines_NonJSONRecordsRedacted pins the text fallback for
// records that are not valid JSON.
func TestSanitizeJSONLines_NonJSONRecordsRedacted(t *testing.T) {
	p := OutputPipeline()
	content := []byte("plain Token: ghp_FAKEtesttoken000000000000000000000000\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
	assert.Equal(t, 1, strings.Count(string(out), "\n"), "line framing must be preserved")
}

// TestSanitizeJSONLines_PreservesBlankAndCleanRecords pins that blank lines and
// clean non-JSON records survive a stream that also contains a secret.
func TestSanitizeJSONLines_PreservesBlankAndCleanRecords(t *testing.T) {
	p := OutputPipeline()
	content := []byte("plain prose without secrets\n" +
		"\n" +
		"{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)

	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 3, "blank lines and record count must be preserved")
	assert.Equal(t, "plain prose without secrets", lines[0])
	assert.Empty(t, lines[1])
	assert.NotContains(t, lines[2], "ghp_FAKEtesttoken")
}

// TestSanitizeJSONDocument_TextRedactionThatBreaksJSONFailsClosed pins that a
// framing-spanning secret whose in-place redaction would leave invalid JSON is
// replaced with a valid marker instead of corrupted syntax.
func TestSanitizeJSONDocument_TextRedactionThatBreaksJSONFailsClosed(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	// Spans the closing quote, comma and next key, so masking it breaks the
	// document.
	const secret = `"v1","k2"`
	require.True(t, RegisterRuntimeSecret(secret))

	p := OutputPipeline()
	doc := []byte(`{"k1":"v1","k2":"v2"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), secret)
	assert.Contains(t, string(out), "output removed")
}

// TestSanitizeJSONDocument_TopLevelScalars pins that scalar documents are
// handled without inventing structure.
func TestSanitizeJSONDocument_TopLevelScalars(t *testing.T) {
	p := OutputPipeline()
	for name, doc := range map[string]string{
		"number": "42",
		"true":   "true",
		"null":   "null",
	} {
		t.Run(name, func(t *testing.T) {
			out, findings, err := SanitizeJSONDocument([]byte(doc), p)
			require.NoError(t, err)
			assert.Empty(t, findings)
			assert.Equal(t, doc, string(out))
		})
	}
}

// TestSanitizeJSONLines_LastRecordWithoutNewline pins that a final record
// without a trailing newline is still sanitized and keeps its framing.
func TestSanitizeJSONLines_LastRecordWithoutNewline(t *testing.T) {
	p := OutputPipeline()
	content := []byte(`{"token":"ghp_FAKEtesttoken000000000000000000000000"}`)

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
	assert.False(t, strings.HasSuffix(string(out), "\n"), "no newline must be invented")
	assert.True(t, json.Valid(out), "the record must stay valid JSON: %s", out)
}

// TestSanitizeJSONLines_PreservesCRLFFraming pins that CRLF records keep their
// carriage returns across a rewrite.
func TestSanitizeJSONLines_PreservesCRLFFraming(t *testing.T) {
	p := OutputPipeline()
	content := []byte("{\"event\":\"clean\"}\r\n{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}\r\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.Equal(t, 2, strings.Count(string(out), "\r\n"), "CRLF framing must be preserved")
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
}

// TestSanitizeJSONDocument_TrailingNewlinePreserved pins that a rewritten
// document keeps the file's own line framing.
func TestSanitizeJSONDocument_TrailingNewlinePreserved(t *testing.T) {
	p := OutputPipeline()
	for name, doc := range map[string]string{
		"lf":   "{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}\n",
		"crlf": "{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}\r\n",
		"none": "{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}",
	} {
		t.Run(name, func(t *testing.T) {
			out, findings, err := SanitizeJSONDocument([]byte(doc), p)
			require.NoError(t, err)
			require.NotEmpty(t, findings)
			assert.NotContains(t, string(out), "ghp_FAKEtesttoken")

			want := map[string]string{"lf": "\n", "crlf": "\r\n", "none": ""}[name]
			assert.True(t, strings.HasSuffix(string(out), "}"+want), "trailing framing must be preserved: %q", out)
			assert.Equal(t, strings.Count(doc, "\n"), strings.Count(string(out), "\n"))
		})
	}
}

// TestSanitizeJSONDocument_CleanDuplicateKeyDocumentUnchanged pins that a
// duplicate-key document with nothing to redact is left byte-for-byte intact.
func TestSanitizeJSONDocument_CleanDuplicateKeyDocumentUnchanged(t *testing.T) {
	p := OutputPipeline()
	doc := []byte(`{"a": 1, "a": 2}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	assert.Empty(t, findings)
	assert.Equal(t, string(doc), string(out), "a clean duplicate-key document must not be reformatted")
}

// TestSanitizeJSONDocument_DuplicateKeyRawTextFinding covers a document whose
// match exists in the serialized form: the leaves are individually clean, so
// the raw-text scan finds and redacts it.
func TestSanitizeJSONDocument_DuplicateKeyRawTextFinding(t *testing.T) {
	p := OutputPipeline()
	doc := []byte(`{"password": "secretvalue123", "dup": 1, "dup": 2}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "secretvalue123")
}

// TestSanitizeJSONDocument_DuplicateKeyWithSecretKey covers a duplicate-key
// document whose keys themselves carry a secret.
func TestSanitizeJSONDocument_DuplicateKeyWithSecretKey(t *testing.T) {
	p := OutputPipeline()
	const secret = "ghp_FAKEtesttoken000000000000000000000000"
	doc := []byte(`{"` + secret + `": 1, "` + secret + `": 2}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), secret)
}
