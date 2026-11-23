package security

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// TestSanitizeJSONDocument_PrivateKeySplitAcrossMembersMasksCoveredMembers pins
// that a private key spread over several members is neutralized member by member
// instead of dropping the whole document. Scanning only the re-encoded form
// missed the block: encoding a map sorts keys, and with the END marker sorted
// before the BEGIN marker the pattern no longer matched the original key
// material. Dropping the document was the other extreme — a review result lost
// its verdict with it (#5705).
func TestSanitizeJSONDocument_PrivateKeySplitAcrossMembersMasksCoveredMembers(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	// Reverse the members' sort order so a map re-encode would emit END before
	// BEGIN and hide the block from a serialized scan.
	doc := []byte(`{"z_begin":"` + begin + `","m_body":"MIIEpAIBAAKCAQEA0123456789abcdef","a_end":"` + end + `","verdict":"approve"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "a framing-spanning private key must produce a finding")
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "BEGIN RSA")
	assert.NotContains(t, string(out), "MIIEpAIBAAKCAQEA")
	assert.NotContains(t, string(out), "output removed", "the document must be kept, not dropped")
	assert.Contains(t, string(out), `"verdict":"approve"`, "a member outside the block must survive")
	assert.Contains(t, string(out), `"z_begin":`, "key names are structure and must survive")
	assert.Contains(t, string(out), `"m_body":`)
	assert.Contains(t, string(out), `"a_end":`)
}

// TestSanitizeJSONDocument_HexPrefixBeforeBodyMaterialMasked pins the regression
// a reviewer found: only the first material run in a value was examined, so a
// value that put a hash before the body ("sha <hex> <body>") returned false at
// the hex run. The markers were masked, the body was written back, and the
// written-bytes rescan could not catch it because the markers were gone (#5705).
func TestSanitizeJSONDocument_HexPrefixBeforeBodyMaterialMasked(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	body := "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcw"
	doc := []byte(`{"a":"` + begin + `","b":"sha 0123456789abcdef0123 ` + body + `","c":"` + end + `"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), body, "a body after a hex prefix must not survive")
	assert.NotContains(t, string(out), "0123456789abcdef0123", "the member carrying the block is replaced whole")

	var result struct {
		B string `json:"b"`
	}
	require.NoError(t, json.Unmarshal(out, &result))
	assert.Equal(t, privateKeyMask, result.B, "the member carrying the block is masked")
}

// TestSanitizeJSONDocument_PathsAndProseBetweenMarkersStayReadable pins the other
// regression a reviewer found: a file path and a long word both reach the
// material floor, so a path such as "internal/security/jsonsanitize" or a word
// such as "misconfiguration" was masked as key material. A quoted marker pair
// carries no secret, and the review has to keep the values its schema requires
// (#5705).
func TestSanitizeJSONDocument_PathsAndProseBetweenMarkersStayReadable(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	path := "internal/security/jsonsanitize"
	prose := "a misconfiguration here"
	doc := []byte(`{"action":"comment","pr_number":12,"repo":"acme/widgets",` +
		`"head_sha":"abcdef0123456789abcdef0123456789abcdef01","findings":[` +
		`{"severity":"high","category":"logic-error","file":"a.go","description":"quotes the ` + begin + ` header"},` +
		`{"severity":"medium","category":"missing-test","file":"` + path + `","description":"` + prose + `"},` +
		`{"severity":"low","category":"stale-doc","file":"c.go","description":"quotes the ` + end + ` footer"}],"body":"summary"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "the cross-member marker pair is still reported")
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "PRIVATE KEY-----")
	assertReviewResultSchemaValid(t, out)

	var result struct {
		Findings []struct {
			Severity    string `json:"severity"`
			File        string `json:"file"`
			Description string `json:"description"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(out, &result), "the sanitized review must still decode: %s", out)
	require.Len(t, result.Findings, 3)
	assert.Equal(t, path, result.Findings[1].File, "a path is not PEM body material")
	assert.Equal(t, prose, result.Findings[1].Description, "a long word is not PEM body material")
	assert.Equal(t, "medium", result.Findings[1].Severity, "the structural enum keeps its value")
	assert.Equal(t, privateKeyMask, result.Findings[0].Description, "the value carrying the header is masked")
	assert.Equal(t, privateKeyMask, result.Findings[2].Description, "the value carrying the footer is masked")
}

// TestSanitizeJSONDocument_MarkersInSeparateFindingsKeepReview pins the
// regression a reviewer found: a review result that quotes the PEM header in one
// finding and the footer in another matches the lazy private-key pattern across
// members, but nothing is hidden and the review still has to be posted. Only the
// values that carry a marker or key material are masked, so the structural values
// the review schema constrains keep theirs — action, repo, severity and category
// included — and the sanitized document still validates against
// schemas/review-result.schema.json. Masking every covered value instead replaced
// those enums with the block marker, so validation failed and the review was not
// posted, which is the #5705 outcome (#5705).
func TestSanitizeJSONDocument_MarkersInSeparateFindingsKeepReview(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN" + " PRIVATE KEY-----"
	end := "-----END" + " PRIVATE KEY-----"
	doc := []byte(`{"action":"comment","pr_number":12,"repo":"acme/widgets",` +
		`"head_sha":"abcdef0123456789abcdef0123456789abcdef01",` +
		`"findings":[` +
		`{"severity":"high","category":"logic-error","file":"a.go","description":"quotes the ` + begin + ` header"},` +
		`{"severity":"medium","category":"missing-test","file":"b.go","description":"a finding between the markers"},` +
		`{"severity":"low","category":"stale-doc","file":"c.go","description":"quotes the ` + end + ` footer"},` +
		`{"severity":"info","category":"doc-style","file":"d.go","description":"a finding after the block"}],"body":"summary"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "the cross-member marker pair is still reported")
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "output removed", "the review must not be dropped whole")
	assert.NotContains(t, string(out), "PRIVATE KEY-----")

	// The blocking requirement: the sanitized review still satisfies the schema
	// the harness validates before post-review runs.
	assertReviewResultSchemaValid(t, out)

	var result struct {
		Action   string `json:"action"`
		PRNumber int    `json:"pr_number"`
		Repo     string `json:"repo"`
		Body     string `json:"body"`
		Findings []struct {
			Severity    string `json:"severity"`
			Category    string `json:"category"`
			File        string `json:"file"`
			Description string `json:"description"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(out, &result), "the sanitized review must still decode: %s", out)
	assert.Equal(t, "comment", result.Action, "the action must survive")
	assert.Equal(t, 12, result.PRNumber)
	assert.Equal(t, "acme/widgets", result.Repo)
	assert.Equal(t, "summary", result.Body, "a field outside the matched span keeps its content")
	require.Len(t, result.Findings, 4, "every finding must survive")

	// Only the values that carry a marker are masked; the free text between the
	// quoted markers is not key material and keeps its content.
	assert.Equal(t, "high", result.Findings[0].Severity, "a structural enum before the match keeps its value")
	assert.Equal(t, "a.go", result.Findings[0].File, "a value before the match keeps its content")
	assert.Equal(t, privateKeyMask, result.Findings[0].Description, "the value carrying the marker is masked")
	assert.Equal(t, "a finding between the markers", result.Findings[1].Description,
		"free text between a quoted marker pair is not key material")
	assert.Equal(t, privateKeyMask, result.Findings[2].Description, "the value carrying the footer is masked")
	assert.Equal(t, "d.go", result.Findings[3].File, "a finding after the footer is outside the span")
	assert.Equal(t, "a finding after the block", result.Findings[3].Description)
}

// TestSanitizeJSONDocument_KeyMaterialInSpanKeepsStructuralEnums pins the other
// half of the rule: when the block really is split across members, the values
// carrying key material are masked, while the structural values the review schema
// constrains keep theirs so the review can still be posted (#5705).
func TestSanitizeJSONDocument_KeyMaterialInSpanKeepsStructuralEnums(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"action":"request-changes","pr_number":12,"repo":"acme/widgets",` +
		`"head_sha":"abcdef0123456789abcdef0123456789abcdef01","findings":[` +
		`{"severity":"high","category":"secret-exposure","file":"a.go","description":"leaked ` + begin + `"},` +
		`{"severity":"high","category":"secret-exposure","file":"b.go","description":"MIIEpAIBAAKCAQEA0123456789abcdef"},` +
		`{"severity":"high","category":"secret-exposure","file":"c.go","description":"and ` + end + `"}],"body":"remove it"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "output removed", "the review must not be dropped whole")
	assert.NotContains(t, string(out), "PRIVATE KEY-----")
	assert.NotContains(t, string(out), "MIIEpAIBAAKCAQEA", "the key material must not survive")
	assertReviewResultSchemaValid(t, out)

	var result struct {
		Findings []struct {
			Severity    string `json:"severity"`
			Category    string `json:"category"`
			File        string `json:"file"`
			Description string `json:"description"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(out, &result))
	require.Len(t, result.Findings, 3)
	for i, finding := range result.Findings {
		assert.Equal(t, "high", finding.Severity, "finding %d must keep its severity enum", i)
		assert.Equal(t, "secret-exposure", finding.Category, "finding %d must keep its category enum", i)
		assert.NotEmpty(t, finding.File, "finding %d must keep its file", i)
		assert.NotEmpty(t, finding.Description, "finding %d must keep its description", i)
	}
}

// TestSanitizeJSONDocument_MarkerSplitAcrossMembersMasked pins that a marker
// written in pieces does not survive: the leaves the match starts and ends in are
// always masked, and a value carrying only a marker fragment is masked too, so no
// piece of the block header or footer is written back (#5705).
func TestSanitizeJSONDocument_MarkerSplitAcrossMembersMasked(t *testing.T) {
	p := OutputPipeline()
	doc := []byte(`["-----BEGIN"," RSA PRIVATE KEY-----","MIIEb2R5","-----END"," RSA PRIVATE KEY-----"]`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "-----BEGIN")
	assert.NotContains(t, string(out), "-----END")
	assert.NotContains(t, string(out), "PRIVATE KEY-----")
}

// TestSanitizeJSONDocument_ChunkedBodyBetweenMarkersMasked pins the regression a
// reviewer found: a body split across members into chunks that are each short,
// digit-free, or hex-looking was written back after the boundary markers were
// masked, where the written-bytes rescan can no longer see the block. The joined
// text of consecutive values reconstructs the body, so every chunk is masked
// (#5705).
func TestSanitizeJSONDocument_ChunkedBodyBetweenMarkersMasked(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	cases := map[string][]string{
		"short chunks":         {"MIIEowIB", "AAKCAQEA", "x9Kq2Lm"},
		"digit-free chunk":     {"MIIEowIBAAKCAQEA"},
		"hex chunk in the run": {"MIIEowIBAAKCAQEA", "abcdef0123456789", "short+chunk"},
	}
	for name, chunks := range cases {
		t.Run(name, func(t *testing.T) {
			quoted := make([]string, 0, len(chunks)+2)
			quoted = append(quoted, `"`+begin+`"`)
			for _, chunk := range chunks {
				quoted = append(quoted, `"`+chunk+`"`)
			}
			quoted = append(quoted, `"`+end+`"`)
			doc := []byte(`{"lines":[` + strings.Join(quoted, ",") + `]}`)

			out, findings, err := SanitizeJSONDocument(doc, p)
			require.NoError(t, err)
			require.NotEmpty(t, findings)
			assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
			assert.NotContains(t, string(out), "PRIVATE KEY-----")
			for _, chunk := range chunks {
				assert.NotContains(t, string(out), chunk, "a body chunk must not survive")
			}
		})
	}
}

// TestSanitizeJSONDocument_HexValuesInSpanKeepTheirContent pins that a hex run
// inside a quoted marker span is not treated as key material. A commit SHA or a
// finding id is schema-constrained (head_sha, id), and a PEM body is base64, so a
// hex-only run is far more likely a hash than a PEM line (#5705).
func TestSanitizeJSONDocument_HexValuesInSpanKeepTheirContent(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"action":"comment","pr_number":12,"repo":"acme/widgets","findings":[` +
		`{"severity":"high","category":"logic-error","file":"a.go","id":"f_0123456789abcdef0123456789abcdef","description":"quotes the ` + begin + ` header"},` +
		`{"severity":"low","category":"doc-style","file":"b.go","id":"f_0123456789abcdef0123456789abcdef","description":"and the ` + end + ` footer"}],` +
		`"head_sha":"abcdef0123456789abcdef0123456789abcdef01","body":"summary"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.Contains(t, string(out), `"head_sha":"abcdef0123456789abcdef0123456789abcdef01"`,
		"a commit SHA is not PEM body material")
	assert.Contains(t, string(out), `"id":"f_0123456789abcdef0123456789abcdef"`,
		"a finding id is not PEM body material")
	assert.NotContains(t, string(out), "PRIVATE KEY-----")
}

// TestSanitizeJSONDocument_KeyNameCarryingMarkerIsMasked pins the other half of
// the key rule: a key name is structure and keeps its name, unless the name itself
// carries a block marker. Then the name is block content and cannot be written
// back, while a sibling key between the markers still keeps its name (#5705).
func TestSanitizeJSONDocument_KeyNameCarryingMarkerIsMasked(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"` + begin + `":"x","severity":"high","` + end + `":"y"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "BEGIN RSA")
	assert.NotContains(t, string(out), "END RSA")
	assert.Contains(t, string(out), `"severity":`, "a key name between the markers keeps its name")
}

// TestJSONProvenance_KeysAreStructure pins that the decoded view records which
// leaves are object keys, that a spanning block only covers string values, and
// that a value between the markers is covered only when it carries key material,
// so the rewrite never renames a key unless its own text carries a marker.
func TestJSONProvenance_KeysAreStructure(t *testing.T) {
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"severity":"` + begin + `","category":"MIIEpAIBAAKCAQEA0123456789abcdef","body":"x","file":"` + end + `"}`)

	prov := collectJSONLeaves(doc)
	require.Equal(t, len(prov.ranges), len(prov.keys), "every recorded leaf must carry a kind")
	spans := prov.spanningPrivateKeys()
	require.Equal(t, 1, spans.localized)
	assert.Empty(t, spans.maskedKeys, "no key name carries a marker")
	require.Len(t, spans.covered, 3, "the marker and the key-material values are covered")
	for i := range spans.covered {
		assert.False(t, prov.keys[i], "only string values are covered")
	}
	assert.False(t, spans.covered[5], "free text between the markers is not key material")
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

// TestSanitizeJSONDocument_CrossMemberPrivateKeyWithLeafSecret pins that a leaf
// secret and a framing-spanning private key in the same document are both
// handled: the rewrite keeps member order, and the members carrying the block
// are masked while the rest of the document survives.
func TestSanitizeJSONDocument_CrossMemberPrivateKeyWithLeafSecret(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"z_begin":"` + begin + `","m_body":"MIIEpAIBAAKCAQEA0123456789abcdef","a_end":"` + end + `",` +
		`"token":"ghp_FAKEtesttoken000000000000000000000000","verdict":"approve"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
	assert.NotContains(t, string(out), "PRIVATE KEY-----")
	assert.Contains(t, string(out), `"verdict":"approve"`, "a member outside the block must survive")
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

// TestSanitizeJSONLines_RedactedAssignmentBeforeEscapedNewlineKeepsRecord pins
// that the whole-stream rescan cannot re-match a value the per-record pass
// already redacted. In encoded JSON a \n escape is two non-space bytes, so
// running env_assignment over the rebuilt stream matched the placeholder plus
// the next word, and the correctly redacted record was replaced with a
// cross-record marker — a false alarm that dropped real content from
// transcripts/*.jsonl and output.jsonl (#5705).
func TestSanitizeJSONLines_RedactedAssignmentBeforeEscapedNewlineKeepsRecord(t *testing.T) {
	p := OutputPipeline()
	content := []byte(`{"type":"assistant","text":"run: API_KEY=supersecretvalue123\nnext line"}` + "\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.Len(t, findings, 1, "one secret must be reported once, not recounted by the stream rescan: %+v", findings)
	assert.Equal(t, "env_assignment", findings[0].Name)

	line := strings.TrimSpace(string(out))
	assert.True(t, json.Valid([]byte(line)), "the record must stay valid JSON: %s", line)
	assert.NotContains(t, line, "output removed", "a redacted record must not be replaced")
	assert.NotContains(t, line, "supersecretvalue123")
	assert.Contains(t, line, "next line", "the text after the escaped newline must survive")
}

// TestSanitizeJSONLines_BenignEscapedAssignmentKeepsRecord pins the benign half
// of the same regression: a shell command with an escaped newline contains no
// secret at all, and the per-record pass leaves it alone (#5705).
func TestSanitizeJSONLines_BenignEscapedAssignmentKeepsRecord(t *testing.T) {
	p := OutputPipeline()
	content := []byte(`{"text":"export GITHUB_TOKEN=$TOKEN\nnext line"}` + "\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	assert.Empty(t, findings, "no secret is present, so nothing may be reported")
	assert.Equal(t, string(content), string(out), "a benign record must be written back unchanged")
}

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

// TestSanitizeJSONDocument_BackstopKeepsEarlierMask pins that the
// serialized-bytes backstop never masks a redaction placeholder a second time.
// The leaf pass masked part of an escaped sequence, so re-encoding left the
// placeholder followed by doubled backslashes. A value pattern captured that
// tail, found no complete mask, and masked again — dropping the informative
// prefix and a non-secret backslash, and logging a duplicate finding (#5705).
func TestSanitizeJSONDocument_BackstopKeepsEarlierMask(t *testing.T) {
	p := OutputPipeline()
	doc := []byte(`{"body":"x token_usage = metrics.get(\\\"token_usage\\\") or {}"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.Len(t, findings, 1, "only the leaf match may be reported: %+v", findings)
	assert.Equal(t, "env_assignment", findings[0].Name)

	var decoded struct {
		Body string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(out, &decoded))
	assert.Equal(t, `x token_usage = metr...\"token_usage\") or {}`, decoded.Body,
		"the earlier mask and the non-secret backslashes must survive")
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

// TestOutputRemoval pins the markers recognized as a whole-file replacement and
// the reason reported for the operator alert.
func TestOutputRemoval(t *testing.T) {
	for name, tc := range map[string]struct {
		input   string
		reason  string
		removed bool
	}{
		"invalid json":  {failClosedInvalidJSON + "\n", "redaction would invalidate JSON", true},
		"cross-record":  {failClosedCrossRecordJSON, "a secret spanned multiple records", true},
		"spans members": {failClosedSpansMembersJSON, "a private key block spans JSON framing", true},
		"ordinary doc":  {`{"error":"not a removal marker"}`, "", false},
		"not json":      {"plain prose", "", false},
		"empty":         {"", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			reason, removed := OutputRemoval([]byte(tc.input))
			assert.Equal(t, tc.removed, removed)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

// TestOutputRecordRemovals pins that a JSONL stream whose records were replaced
// reports each distinct reason once. A replaced record keeps the file's framing,
// so the file never equals a whole-file marker, and this is how its loss reaches
// an operator (#5705).
func TestOutputRecordRemovals(t *testing.T) {
	stream := []byte(
		`{"event":"clean"}` + "\n" +
			failClosedCrossRecordJSON + "\r\n" +
			failClosedCrossRecordJSON + "\n" +
			failClosedSpansMembersJSON + "\n")
	assert.Equal(t,
		[]string{"a secret spanned multiple records", "a private key block spans JSON framing"},
		OutputRecordRemovals(stream))
	assert.Empty(t, OutputRecordRemovals([]byte(`{"event":"clean"}`+"\n")))
}

// TestJSONProvenance_UnmappableBlock pins the defensive fallback: a block that no
// recorded leaf range covers is reported as unmappable so the caller fails
// closed instead of writing bytes that may still carry it.
func TestJSONProvenance_UnmappableBlock(t *testing.T) {
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	v := &jsonProvenance{}
	v.decoded.WriteString(begin + "\n" + "MIIEkeymaterial" + "\n" + end + "\n")
	// One empty string value, which can never overlap a match.
	v.ranges = []recordSpan{{start: 0, end: 0}}
	v.keys = []bool{false}

	spans := v.spanningPrivateKeys()
	assert.Equal(t, 1, spans.unmappable)
	assert.Zero(t, spans.localized)
	assert.Empty(t, spans.covered)
	assert.Empty(t, spans.maskedKeys)
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

// TestSanitizeJSONLines_RepeatedRecordsFailsClosed pins that a cross-record
// match is not localized by a substring search a repeated record can fool. A
// later untouched copy must never stand in for a covered record: keeping the
// original bytes would write the spanning secret back out. Duplicates outside
// the changed region are still preserved.
func TestSanitizeJSONLines_RepeatedRecordsFailsClosed(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const secret = `aaa"}` + "\n" + `{"v":"bbb`
	require.True(t, RegisterRuntimeSecret(secret))

	p := OutputPipeline()
	content := []byte(
		"{\"v\":\"aaa\"}\n" + // first half of the match
			"{\"v\":\"bbb\"}\n" + // second half of the match
			"{\"v\":\"aaa\"}\n" + // untouched duplicate of record 1
			"{\"v\":\"ccc\"}\n" +
			"{\"v\":\"bbb\"}\n") // untouched duplicate of record 2

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.NotContains(t, string(out), secret, "the spanning secret must not be reassembled")

	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 5, "record count must not change")
	for _, line := range lines[:2] {
		assert.True(t, json.Valid([]byte(line)), "each replaced record must stay valid JSON: %s", line)
		assert.Contains(t, line, "output removed")
	}
	assert.Equal(t, `{"v":"aaa"}`, lines[2], "an untouched duplicate outside the change must survive")
	assert.Equal(t, `{"v":"ccc"}`, lines[3])
	assert.Equal(t, `{"v":"bbb"}`, lines[4], "an untouched duplicate outside the change must survive")
}

// TestSanitizeJSONLines_RepeatedCleanRecordInsideCrossRecordMatch pins the
// inverse misattribution: a clean record that also appears later must not be
// kept in place of the covered copy, and the later clean copy must not be
// dropped for it.
func TestSanitizeJSONLines_RepeatedCleanRecordInsideCrossRecordMatch(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(
		"[\"" + begin + "\"]\n" +
			"{\"value\":\"middle\"}\n" +
			"[\"" + end + "\"]\n" +
			"{\"value\":\"middle\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.NotContains(t, string(out), begin)
	assert.NotContains(t, string(out), end)

	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 4, "record count must not change")
	for _, line := range lines[:3] {
		assert.True(t, json.Valid([]byte(line)), "each replaced record must stay valid JSON: %s", line)
		assert.Contains(t, line, "output removed")
	}
	assert.Equal(t, `{"value":"middle"}`, lines[3], "the repeated clean record outside the match must survive")
}

// TestSanitizeJSONDocument_SensitiveBeginMemberMasksBlock pins that masking a
// sensitive member value cannot hide a private-key block that spans members. A
// sensitive-named member holding only the BEGIN marker is masked as a
// json_field, which would leave the body and END marker behind; the block is
// detected in the original framing and its members are masked.
func TestSanitizeJSONDocument_SensitiveBeginMemberMasksBlock(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"key_start":"` + begin + `","payload":"MIIEpAIBAAKCAQEA0123456789abcdef","tail":"` + end + `","verdict":"approve"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "MIIEpAIBAAKCAQEA", "the key body must not survive")
	assert.NotContains(t, string(out), "END RSA")
	assert.Contains(t, string(out), `"verdict":"approve"`, "a member outside the block must survive")
}

// TestSanitizeJSONLines_CleanDuplicateKeyRecordPreserved pins that a clean
// duplicate-key record survives while another record is rewritten.
func TestSanitizeJSONLines_CleanDuplicateKeyRecordPreserved(t *testing.T) {
	p := OutputPipeline()
	content := []byte("{\"a\":1,\"a\":2}\n{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, `{"a":1,"a":2}`, lines[0], "clean duplicate-key record must remain unchanged")
	assert.True(t, json.Valid([]byte(lines[1])))
	assert.NotContains(t, lines[1], "ghp_FAKEtesttoken")
}

// TestSanitizeJSONLines_PrivateKeyAcrossRecordsFailsClosed pins that a PEM
// block split over three records replaces all three.
func TestSanitizeJSONLines_PrivateKeyAcrossRecordsFailsClosed(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(fmt.Sprintf("[%q]\n[%q]\n[%q]\n", begin, "MIIEpAIBAAKCAQEA0123456789abcdefghijklmnop", end))

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.Equal(t, "private_key", findings[0].Name)
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 3)
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)))
		assert.Contains(t, line, "output removed")
	}
}

// TestSanitizeJSONLines_RawMultilinePEMFailsClosed pins the same for a PEM
// block written as raw lines rather than JSON values.
func TestSanitizeJSONLines_RawMultilinePEMFailsClosed(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(fmt.Sprintf("%s\nMIIEpAIBAAKCAQEA0123456789abcdefghijklmnop\n%s\n", begin, end))

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.Equal(t, "private_key", findings[0].Name)
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 3)
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)))
		assert.Contains(t, line, "output removed")
		assert.NotContains(t, line, "MIIEpAIBAAKCAQEA")
	}
}

// TestSanitizeJSONLines_DuplicateKeyEscapedSecretRedacted pins that an escaped
// secret in a record with duplicate keys is found by the token walk.
func TestSanitizeJSONLines_DuplicateKeyEscapedSecretRedacted(t *testing.T) {
	p := OutputPipeline()
	content := []byte("{\"a\":\"first\",\"a\":\"second\",\"password\":\"\\u0073\\u0065\\u0063\\u0072\\u0065\\u0074_password_1234\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
	line := strings.TrimSpace(string(out))
	assert.True(t, json.Valid([]byte(line)))
	assert.NotContains(t, line, "password_1234")
	assert.Contains(t, line, `"a":"first"`, "an earlier duplicate value must survive redaction")
	assert.Contains(t, line, `"a":"second"`, "the second duplicate value must survive redaction")
}

// TestSanitizeJSONLines_FramingSecretPreservesLineCount pins that a secret
// spanning one record's own framing is redacted without adding or removing
// lines.
func TestSanitizeJSONLines_FramingSecretPreservesLineCount(t *testing.T) {
	ResetRuntimeSecretsForTest()
	t.Cleanup(ResetRuntimeSecretsForTest)
	const secret = `","field2":"`
	require.True(t, RegisterRuntimeSecret(secret))

	p := OutputPipeline()
	content := []byte("{\"field1\":\"foo\",\"field2\":\"bar\"}\n{\"field3\":\"second record\"}\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.Equal(t, 2, strings.Count(string(out), "\n"), "line count must remain exactly 2")
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.True(t, json.Valid([]byte(lines[0])))
	assert.True(t, json.Valid([]byte(lines[1])))
	assert.NotContains(t, lines[0], secret)
}

// TestSanitizeJSONLines_SingleRecordPrivateKeyRedactedInPlace pins that a key
// confined to one record is redacted rather than failed closed.
func TestSanitizeJSONLines_SingleRecordPrivateKeyRedactedInPlace(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(fmt.Sprintf("{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\",\"pem\":\"%s\\n%s\"}\n", begin, end))

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
	line := strings.TrimSpace(string(out))
	assert.True(t, json.Valid([]byte(line)))
	assert.Equal(t, 1, strings.Count(string(out), "\n"), "one record must stay one record")
	assert.NotContains(t, line, "ghp_FAKEtesttoken")
	assert.NotContains(t, line, "PRIVATE KEY-----")
	assert.Contains(t, line, "[REDACTED PRIVATE KEY]", "a key confined to one record is redacted in place")
}

// TestLineBounded pins the record-boundary check used to decide whether a
// surviving record copy is a whole record.
func TestLineBounded(t *testing.T) {
	data := []byte("a\nbb\r\nccc\nlast")
	tests := []struct {
		name       string
		start, end int
		want       bool
	}{
		{"whole first line", 0, 1, true},
		{"whole crlf line", 2, 4, true},
		{"whole middle line", 6, 9, true},
		{"last line without newline", 10, 14, true},
		{"starts mid-line", 1, 4, false},
		{"ends mid-line", 6, 8, false},
		{"lone carriage return", 9, 10, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lineBounded(data, tc.start, tc.end))
		})
	}
}

// TestSanitizeJSONDocument_LeafAndSplitPrivateKeysMasked pins that a handled
// leaf block does not exempt a second, framing-spanning block in the same
// document, and that neither leaves key material behind.
func TestSanitizeJSONDocument_LeafAndSplitPrivateKeysMasked(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"note":"` + begin + `\nMIIEleafkeymaterial\n` + end + `",` +
		`"key_start":"` + begin + `","payload":"MIIEspanningkeymaterial0","tail":"` + end + `"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "MIIEspanningkeymaterial0")
	assert.NotContains(t, string(out), "MIIEleafkeymaterial")
}

// TestSanitizeJSONLines_SensitiveBeginRecordFailsClosed pins that a
// cross-record private key is detected on the original stream, before masking
// a sensitive-named member removes its BEGIN marker.
func TestSanitizeJSONLines_SensitiveBeginRecordFailsClosed(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	records := []string{
		"{\"key_start\":\"" + begin + "\"}",
		"{\"payload\":\"MIIEspanningkeymaterial0\"}",
		"{\"tail\":\"" + end + "\"}",
	}
	for name, tc := range map[string]struct {
		sep     string
		trailer string
	}{
		"lf":                  {sep: "\n", trailer: "\n"},
		"crlf":                {sep: "\r\n", trailer: "\r\n"},
		"no trailing newline": {sep: "\n", trailer: ""},
	} {
		t.Run(name, func(t *testing.T) {
			content := []byte(strings.Join(records, tc.sep) + tc.trailer)

			out, findings, err := SanitizeJSONLines(content, p)
			require.NoError(t, err)
			require.NotEmpty(t, findings)
			assert.NotContains(t, string(out), "MIIEspanningkeymaterial0")
			assert.NotContains(t, string(out), "END RSA")

			lines := strings.Split(strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r"), tc.sep)
			require.Len(t, lines, 3, "record count must not change")
			for _, line := range lines {
				assert.True(t, json.Valid([]byte(line)), "each record must stay valid JSON: %s", line)
				assert.Contains(t, line, "output removed")
			}
		})
	}
}

// TestSanitizeJSONDocument_EscapedLeafBlockDoesNotExemptSplitBlock pins that an
// escaped-marker leaf block cannot stand in for a separate block that spans
// members: the escaped block is visible only after JSON decoding, so raw-byte
// and decoded finding counts are not comparable.
func TestSanitizeJSONDocument_EscapedLeafBlockDoesNotExemptSplitBlock(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	escapedBegin := `\u002d` + "----BEGIN RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"note":"` + escapedBegin + `\nMIIEleafkeymaterial\n` + end + `",` +
		`"key_start":"` + begin + `","payload":"MIIEspanningkeymaterial0","tail":"` + end + `"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "MIIEspanningkeymaterial0", "the spanning block must not survive")
	assert.NotContains(t, string(out), "MIIEleafkeymaterial", "the escaped leaf block must be redacted")
}

// TestSanitizeJSONLines_EscapedBeginRecordFailsClosed pins that a cross-record
// block is recognized even when its BEGIN marker is written with \u escapes in
// the original record bytes.
func TestSanitizeJSONLines_EscapedBeginRecordFailsClosed(t *testing.T) {
	p := OutputPipeline()
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(`{"key_start":"\u002d` + "----BEGIN RSA" + " PRIVATE KEY-----" + `"}` + "\n" +
		`{"payload":"MIIEspanningkeymaterial0"}` + "\n" +
		`{"tail":"` + end + `"}` + "\n")

	out, findings, err := SanitizeJSONLines(content, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.NotContains(t, string(out), "MIIEspanningkeymaterial0")
	assert.NotContains(t, string(out), "END RSA")

	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	require.Len(t, lines, 3, "record count must not change")
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)), "each record must stay valid JSON: %s", line)
		assert.Contains(t, line, "output removed")
	}
}

// TestSanitizeJSONDocument_CRLFNoStrayCarriageReturn pins that trailing CRLF
// framing is reproduced exactly, without a duplicated carriage return, on the
// unchanged and rewritten paths.
func TestSanitizeJSONDocument_CRLFNoStrayCarriageReturn(t *testing.T) {
	p := OutputPipeline()
	for name, doc := range map[string]string{
		"clean":  "{\"a\":\"x\"}\r\n",
		"secret": "{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, _, err := SanitizeJSONDocument([]byte(doc), p)
			require.NoError(t, err)
			assert.NotContains(t, string(out), "\r\r", "no stray carriage return")
			assert.True(t, strings.HasSuffix(string(out), "}\r\n"), "CRLF framing must be preserved: %q", out)
		})
	}
}

// TestSanitizeJSONDocument_EscapedMarkersAcrossMembersMasked pins that a block
// spanning members is detected even when no leaf produces a finding and the
// markers are \u-escaped in the raw bytes.
func TestSanitizeJSONDocument_EscapedMarkersAcrossMembersMasked(t *testing.T) {
	p := OutputPipeline()
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`["\u002d` + "----BEGIN RSA" + " PRIVATE KEY-----" +
		`","MIIEspanningkeymaterial0","` + end + `"]`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "MIIEspanningkeymaterial0")
}

// TestSanitizeJSONDocument_NormalizedMarkersAcrossMembersMasked pins that the
// decoded view applies the same Unicode normalization as the leaf scan, so a
// marker obfuscated with a zero-width character cannot hide a spanning block.
func TestSanitizeJSONDocument_NormalizedMarkersAcrossMembersMasked(t *testing.T) {
	p := OutputPipeline()
	end := "-----END RSA" + " PRIVATE KEY-----"
	// U+200B inside the BEGIN marker: not a PEM block in any single form.
	doc := []byte(`{"begin":"-----\u200bBEGIN RSA` + " PRIVATE KEY-----\"" +
		`,"payload":"MIIEspanningkeymaterial0","end":"` + end + `"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "MIIEspanningkeymaterial0")
}

// TestSanitizeJSONDocument_UnterminatedBeginBeforeCompleteBlockMasksBoth pins
// that a lazy match starting at one leaf's BEGIN and ending at another leaf's
// END cannot cancel out a complete leaf block: containment is judged per match,
// so both blocks are neutralized.
func TestSanitizeJSONDocument_UnterminatedBeginBeforeCompleteBlockMasksBoth(t *testing.T) {
	p := OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	doc := []byte(`{"a":"` + begin + `\nMIIEfirstkeymaterial","b":"` + begin + `\nZZZZZZZZ\n` + end + `"}`)

	out, findings, err := SanitizeJSONDocument(doc, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, json.Valid(out), "sanitized JSON must stay valid: %s", out)
	assert.NotContains(t, string(out), "MIIEfirstkeymaterial", "the unterminated block must not survive")
	assert.NotContains(t, string(out), "ZZZZZZZZ", "the complete block must not survive")
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

// review-result.schema.json is authored in fullsend-ai/agents and is not
// vendored here, so the checks below mirror the constraints sanitization can
// break in a review result: the enums and patterns of the structural fields,
// additionalProperties false, and the required members. Update them when the
// schema's enums change.
var (
	reviewResultActions    = []string{"approve", "request-changes", "comment", "reject", "failure"}
	reviewResultSeverities = []string{"critical", "high", "medium", "low", "info"}
	reviewResultCategories = []string{
		"logic-error", "nil-deref", "off-by-one", "edge-case", "api-contract", "missing-test",
		"test-inadequate", "pattern-violation", "test-weakened", "test-removed", "mock-loosened",
		"assertion-weakened", "coverage-reduced", "test-poisoning", "split-payload", "stale-reference",
		"auth-bypass", "rbac-violation", "data-exposure", "privilege-escalation", "injection-vuln",
		"sandbox-escape", "xss", "ssrf", "insecure-deserialization", "prompt-injection",
		"unicode-steganography", "bidi-override", "homoglyph-attack", "instruction-smuggling", "fail-open",
		"permission-expansion", "permission-reduction", "role-escalation", "workflow-permission",
		"secret-exposure", "scope-exceeded", "tier-mismatch", "unauthorized-change", "scope-creep",
		"missing-authorization", "misleading-label", "design-direction", "complexity-ratio",
		"misplaced-abstraction", "architectural-conflict", "design-smell", "over-engineering",
		"under-engineering", "scope-bundling", "naming-convention", "error-handling-idiom", "api-shape",
		"code-organization", "doc-style", "pattern-inconsistency", "stale-doc", "missing-doc",
		"incorrect-doc", "incomplete-doc", "breaking-api", "breaking-schema", "breaking-config",
		"breaking-cli", "missing-deprecation", "missing-version-bump", "backward-incompatible",
		"uxd-evaluate-code-heuristics", "protected-path", "provenance-warning",
		"scope-authorization-implicit", "sub-agent-failure",
	}
)

// assertReviewResultSchemaValid checks doc against the review-result schema
// constraints sanitization can break. A review the harness rejects is not posted,
// which is the #5705 outcome, so a fixture that survives sanitization has to still
// satisfy these.
func assertReviewResultSchemaValid(t *testing.T, doc []byte) {
	t.Helper()

	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(doc, &result), "sanitized review must decode: %s", doc)

	topLevel := map[string]bool{
		"action": true, "pr_number": true, "repo": true, "head_sha": true, "body": true,
		"findings": true, "reason": true, "dispositions": true, "label_actions": true,
		"risk_assessment": true, "risk_gated": true,
	}
	for key := range result {
		assert.True(t, topLevel[key], "unexpected top-level member %q", key)
	}

	var action string
	require.NoError(t, json.Unmarshal(result["action"], &action), "action is required")
	assert.Contains(t, reviewResultActions, action, "action must be one of the schema enums")

	var repo string
	require.NoError(t, json.Unmarshal(result["repo"], &repo), "repo is required")
	assert.Regexp(t, `^[a-zA-Z0-9._-]+(/[a-zA-Z0-9._-]+)+$`, repo, "repo must match the schema pattern")

	var prNumber int
	require.NoError(t, json.Unmarshal(result["pr_number"], &prNumber), "pr_number is required")
	assert.Positive(t, prNumber, "pr_number must be at least 1")

	if action == "comment" || action == "approve" || action == "request-changes" || action == "reject" {
		var headSHA, body string
		require.NoError(t, json.Unmarshal(result["head_sha"], &headSHA), "%s requires head_sha", action)
		require.NoError(t, json.Unmarshal(result["body"], &body), "%s requires body", action)
		assert.Regexp(t, `^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`, headSHA, "head_sha must match the schema pattern")
		assert.NotEmpty(t, body, "body must be at least one character")
	}

	rawFindings, hasFindings := result["findings"]
	if !hasFindings {
		assert.NotContains(t, []string{"request-changes", "reject"}, action, "%s requires findings", action)
		return
	}
	var findings []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawFindings, &findings), "findings must be an array of objects")
	require.NotEmpty(t, findings, "findings must not be empty")

	allowedFinding := map[string]bool{
		"severity": true, "category": true, "file": true, "line": true, "description": true,
		"remediation": true, "actionable": true, "id": true, "dimension": true,
	}
	for i, finding := range findings {
		for key := range finding {
			assert.True(t, allowedFinding[key], "finding %d has unexpected member %q", i, key)
		}
		var severity, category, file, description string
		require.NoError(t, json.Unmarshal(finding["severity"], &severity), "finding %d requires severity", i)
		require.NoError(t, json.Unmarshal(finding["category"], &category), "finding %d requires category", i)
		require.NoError(t, json.Unmarshal(finding["file"], &file), "finding %d requires file", i)
		require.NoError(t, json.Unmarshal(finding["description"], &description), "finding %d requires description", i)
		assert.Contains(t, reviewResultSeverities, severity, "finding %d severity must be one of the schema enums", i)
		assert.Contains(t, reviewResultCategories, category, "finding %d category must be one of the schema enums", i)
		assert.NotEmpty(t, file, "finding %d file must be at least one character", i)
		assert.NotEmpty(t, description, "finding %d description must be at least one character", i)
	}
}

// TestSanitizeJSONDocument_KeepsBOM pins that a document carrying a UTF-8 BOM is
// sanitized structurally and keeps its encoding mark. The BOM is not JSON
// whitespace, so without this the document falls through to the plain-text scan,
// which is the scan that cannot guarantee JSON syntax stays intact (#5705).
func TestSanitizeJSONDocument_KeepsBOM(t *testing.T) {
	p := OutputPipeline()
	bom := []byte("\ufeff")

	clean := append(append([]byte{}, bom...), []byte(`{"verdict":"approve"}`)...)
	out, findings, err := SanitizeJSONDocument(clean, p)
	require.NoError(t, err)
	assert.Empty(t, findings)
	assert.Equal(t, clean, out, "a clean BOM-prefixed document must be returned unchanged")

	dirty := append(append([]byte{}, bom...), []byte(`{"token":"ghp_FAKEtesttoken000000000000000000000000"}`)...)
	out, findings, err = SanitizeJSONDocument(dirty, p)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	assert.True(t, bytes.HasPrefix(out, bom), "the BOM must survive the rewrite: %q", out)
	assert.True(t, json.Valid(bytes.TrimPrefix(out, bom)), "sanitized document must stay valid JSON: %s", out)
	assert.NotContains(t, string(out), "ghp_FAKEtesttoken")
}

// TestOutputRemoval_NoPlainTextReason pins that the plain-text marker the CLI no
// longer writes is not a recognized reason. A document that parses as JSON takes
// the structural path, so every remaining fail-closed reason is structural.
func TestOutputRemoval_NoPlainTextReason(t *testing.T) {
	reason, removed := OutputRemoval([]byte(`{"error":"output removed because text redaction would invalidate JSON"}`))
	assert.False(t, removed, "the removed plain-text marker must not be recognized")
	assert.Empty(t, reason)
}
