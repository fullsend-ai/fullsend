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

// TestSanitizeJSONDocument_BackstopRedactsSecondRuntimeSecret pins that a
// runtime secret exposed only by serializing the parsed document is still
// redacted when another runtime secret was already found in a leaf.
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

// TestSanitizeJSONStructure_KeyRemovedEntirelyIsReplaced pins that a JSON key
// made only of removable characters never survives sanitization.
func TestSanitizeJSONStructure_KeyRemovedEntirelyIsReplaced(t *testing.T) {
	p := OutputPipeline()
	for name, key := range map[string]string{
		"null byte":     "\x00",
		"bidi override": "\u202e",
	} {
		t.Run(name, func(t *testing.T) {
			got, findings := SanitizeJSONStructure(map[string]any{key: "value"}, p)
			require.NotEmpty(t, findings)
			result := got.(map[string]any)
			require.Len(t, result, 1)
			for gotKey := range result {
				assert.NotContains(t, gotKey, key)
				assert.Equal(t, redactedJSONKey, gotKey)
			}
			assert.Equal(t, "value", result[redactedJSONKey])
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

// TestSanitizeJSONDocument_AlreadyMaskedValueNotRemasked pins that the
// serialized backstop leaves an existing mask placeholder intact.
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

// TestSanitizeJSONStructure_ScalarAndContainerCoverage exercises every scalar
// branch and nested collection of the structural walker.
func TestSanitizeJSONStructure_ScalarAndContainerCoverage(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const numericSecret = "123456789012"
	require.True(t, RegisterRuntimeSecret(numericSecret))

	p := OutputPipeline()
	input := map[string]any{
		"list":    []any{"plain", json.Number("42"), json.Number(numericSecret), true, nil},
		"nested":  map[string]any{"deep": []any{map[string]any{"value": "plain"}}},
		"counter": json.Number("7"),
	}
	got, findings := SanitizeJSONStructure(input, p)
	require.NotEmpty(t, findings)

	encoded, err := encodeJSONDocument(got, false)
	require.NoError(t, err)
	assert.True(t, json.Valid(encoded))
	assert.NotContains(t, string(encoded), numericSecret)
	assert.Contains(t, string(encoded), "42", "clean numbers must keep their precision")
	assert.Contains(t, string(encoded), `"counter":7`)
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

// TestSanitizeJSONDocument_DuplicateKeyRawTextFinding covers a duplicate-key
// document whose only match exists in the serialized form: the leaves are
// individually clean, so the raw-text scan finds and redacts it.
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
