package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// TestScanOutputFiles_SkipsTelemetryArtifacts pins that the host-side output
// redaction scan does NOT rewrite the telemetry JSONL file. It is still held
// open for append during the scan, so an in-place rewrite would truncate it
// under the open handle; and any Level 3 conversation content it carries was
// already redacted at assembly (contentCollector) before reaching a
// span, so the file needs no post-hoc sweep. A normal output file must still
// be sanitized.
func TestScanOutputFiles_SkipsTelemetryArtifacts(t *testing.T) {
	dir := t.TempDir()
	const secret = "Token: ghp_FAKEtesttoken000000000000000000000000\n"

	telem := filepath.Join(dir, telemetry.TelemetryFile)
	normal := filepath.Join(dir, "output.txt")
	// A sandbox agent could write a file that merely shares the telemetry name
	// under its iteration output; that must still be sanitized (it is not the
	// recorder's own artifact).
	nested := filepath.Join(dir, "iteration-1", "output", telemetry.TelemetryFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o755))
	for _, p := range []string{telem, normal, nested} {
		require.NoError(t, os.WriteFile(p, []byte(secret), 0o644))
	}

	err := scanOutputFiles(dir, "trace-id", "", ui.New(&bytes.Buffer{}))
	require.NoError(t, err)

	got, err := os.ReadFile(telem)
	require.NoError(t, err)
	assert.Equal(t, secret, string(got), "the recorder's own run-telemetry.jsonl must be left untouched")

	got, err = os.ReadFile(normal)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "ghp_FAKEtest", "non-telemetry output must still be sanitized")

	got, err = os.ReadFile(nested)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "ghp_FAKEtest", "an agent file sharing the telemetry name must still be sanitized")
}

// The staged skill trees and personas.json are runner input written before
// the agent runs; the output sweep must not rewrite them (#6158). An agent
// file that only shares a name, below the run dir, is still swept.
func TestScanOutputFiles_SkipsStagedSkillsAndPersonaManifest(t *testing.T) {
	dir := t.TempDir()
	secret := "Token: ghp_FAKEtesttoken" + strings.Repeat("0", 24) + "\n"

	staged := filepath.Join(dir, "skills", "0", "pr-review", "sub-agents", "security.md")
	manifest := filepath.Join(dir, personasFileName)
	nestedManifest := filepath.Join(dir, "iteration-1", "output", personasFileName)
	nestedSkills := filepath.Join(dir, "iteration-1", "output", "skills", "x.md")
	for _, p := range []string{staged, manifest, nestedManifest, nestedSkills} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(secret), 0o644))
	}

	require.NoError(t, scanOutputFiles(dir, "trace-id", "", ui.New(&bytes.Buffer{})))

	for _, p := range []string{staged, manifest} {
		got, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Equal(t, secret, string(got), p)
	}
	for _, p := range []string{nestedManifest, nestedSkills} {
		got, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.NotEqual(t, secret, string(got), p)
	}
}

// TestScanOutputFiles_PreservesJSONStructuralValidity pins that if an output
// file was valid JSON before sanitization, scanOutputFiles structurally sanitizes
// string leaves so that the file on disk remains structurally valid JSON and
// secrets are completely redacted (fail-closed, #5705).
func TestScanOutputFiles_PreservesJSONStructuralValidity(t *testing.T) {
	dir := t.TempDir()

	// Case 1: Standard secret token in string field.
	jsonFile1 := filepath.Join(dir, "agent-result.json")
	validJSON1 := `{"action":"comment","body":"Token: ghp_FAKEtesttoken000000000000000000000000\n"}`
	require.NoError(t, os.WriteFile(jsonFile1, []byte(validJSON1), 0o644))

	// Case 2: json_field with an embedded escaped quote and secret.
	jsonFile2 := filepath.Join(dir, "escaped.json")
	validJSON2 := `{"findings":[{"password":"secret_password_1234\"rest"}]}`
	require.NoError(t, os.WriteFile(jsonFile2, []byte(validJSON2), 0o644))

	// Case 3: A secret containing a backslash / newline split.
	jsonFile3 := filepath.Join(dir, "split.json")
	validJSON3 := `{"auth":"Authorization: Bearer abc\ndefghijkl1234"}`
	require.NoError(t, os.WriteFile(jsonFile3, []byte(validJSON3), 0o644))

	// Case 4: Secret token in object key, large integers (> 2^53), float precision, and HTML characters.
	jsonFile4 := filepath.Join(dir, "key_and_numbers.json")
	validJSON4 := `{"ghp_FAKEtesttoken000000000000000000000000": 12345678901234567891, "ratio": 1.0, "html": "<tag>&val</tag>"}`
	require.NoError(t, os.WriteFile(jsonFile4, []byte(validJSON4), 0o644))

	// Case 5: Slices containing string secrets and colliding keys.
	jsonFile5 := filepath.Join(dir, "slice_and_collision.json")
	validJSON5 := `{"tokens":["ghp_FAKEtesttoken000000000000000000000001"],"ghp_FAKEtesttoken000000000000000000000002":1,"ghp_FAKEtesttoken000000000000000000000003":2}`
	require.NoError(t, os.WriteFile(jsonFile5, []byte(validJSON5), 0o644))

	// Structural findings plus a private-key marker spanning separate values
	// must still be written as valid JSON after the serialized-text backstop.
	jsonFile8 := filepath.Join(dir, "private_key_and_password.json")
	validJSON8 := `["see -----BEGIN RSA` + ` PRIVATE KEY----- marker", {"note": "and -----END RSA` + ` PRIVATE KEY----- marker", "password": "a-long-sensitive-value"}]`
	require.NoError(t, os.WriteFile(jsonFile8, []byte(validJSON8), 0o644))

	// Regression for #5705: an env assignment mentioned in ordinary prose is
	// redacted structurally without corrupting JSON syntax.
	jsonFile6 := filepath.Join(dir, "scribe_secret_backend.json")
	validJSON6 := `{"body":"Changed SCRIBE_SECRET_BACKEND=environment_backend"}`
	require.NoError(t, os.WriteFile(jsonFile6, []byte(validJSON6), 0o644))

	// The private-key pattern can match across separate JSON string values in
	// raw text, even though neither value contains a complete key block.
	// Structural scanning must not let such a cross-value match corrupt JSON.
	jsonFile7 := filepath.Join(dir, "private_key_markers.json")
	validJSON7 := `["see -----BEGIN RSA` + ` PRIVATE KEY----- marker", {"note": "and -----END RSA` + ` PRIVATE KEY----- marker"}]`
	require.NoError(t, os.WriteFile(jsonFile7, []byte(validJSON7), 0o644))

	jsonFile9 := filepath.Join(dir, "split_pem_lines.json")
	validJSON9 := `["-----BEGIN RSA` + ` PRIVATE KEY-----","BASE64_KEY_MATERIAL_REDACTED","-----END RSA` + ` PRIVATE KEY-----"]`
	require.NoError(t, os.WriteFile(jsonFile9, []byte(validJSON9), 0o644))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", printer))

	for _, p := range []struct {
		path   string
		secret string
	}{
		{jsonFile1, "ghp_FAKEtest"},
		{jsonFile2, "secret_password_1234"},
		{jsonFile3, "abcdefghijkl1234"},
		{jsonFile4, "ghp_FAKEtest"},
		{jsonFile5, "ghp_FAKEtest"},
	} {
		content, err := os.ReadFile(p.path)
		require.NoError(t, err)
		assert.True(t, json.Valid(content), "sanitized %s must remain valid JSON: %s", filepath.Base(p.path), string(content))
		assert.NotContains(t, string(content), p.secret, "secret must be redacted from %s", filepath.Base(p.path))
	}

	// Decode the escaped-newline case and check the decoded credential. The
	// unescaped spelling does not occur in the raw JSON, so a raw substring
	// assertion alone would pass even if sanitization missed it.
	content3, err := os.ReadFile(jsonFile3)
	require.NoError(t, err)
	var split struct {
		Auth string `json:"auth"`
	}
	require.NoError(t, json.Unmarshal(content3, &split))
	assert.NotContains(t, split.Auth, "abc\ndefghijkl1234", "decoded credential must be redacted")
	var escapedResult struct {
		Findings []struct {
			Password string `json:"password"`
		} `json:"findings"`
	}
	escapedBytes, err := os.ReadFile(jsonFile2)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(escapedBytes, &escapedResult))
	require.Len(t, escapedResult.Findings, 1)
	assert.NotContains(t, escapedResult.Findings[0].Password, "secret_password_1234", "embedded secret must be redacted")
	assert.Equal(t, "secr...", escapedResult.Findings[0].Password, "the value is wholly masked without changing surrounding JSON")

	content4, err := os.ReadFile(jsonFile4)
	require.NoError(t, err)
	assert.Contains(t, string(content4), "12345678901234567891", "large integer precision must be preserved")
	assert.Contains(t, string(content4), "1.0", "float formatting must be preserved")
	assert.Contains(t, string(content4), "<tag>&val</tag>", "HTML characters must not be escaped")
	var keySanitized map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(content4, &keySanitized))
	for key := range keySanitized {
		assert.NotContains(t, key, "ghp_FAKEtest", "secret must be redacted from JSON object keys")
	}

	content5, err := os.ReadFile(jsonFile5)
	require.NoError(t, err)
	var collisionResult struct {
		Tokens []string `json:"tokens"`
	}
	var collisionDocument map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(content5, &collisionDocument))
	require.NoError(t, json.Unmarshal(content5, &collisionResult))
	assert.Len(t, collisionResult.Tokens, 1)
	assert.NotContains(t, collisionResult.Tokens[0], "ghp_FAKEtest", "array token must be redacted and retained")

	var preservedValues []int
	var sanitizedKeys []string
	for key, rawValue := range collisionDocument {
		if key == "tokens" {
			continue
		}
		sanitizedKeys = append(sanitizedKeys, key)
		var value int
		require.NoError(t, json.Unmarshal(rawValue, &value))
		preservedValues = append(preservedValues, value)
	}
	assert.ElementsMatch(t, []int{1, 2}, preservedValues, "both colliding-key values must survive under distinct sanitized keys")
	assert.Len(t, sanitizedKeys, 2, "both colliding keys must be retained")
	sort.Strings(sanitizedKeys)
	assert.NotEqual(t, sanitizedKeys[0], sanitizedKeys[1], "colliding keys must be made distinct")

	content6, err := os.ReadFile(jsonFile6)
	require.NoError(t, err)
	assert.True(t, json.Valid(content6), "sanitized #5705 fixture must remain valid JSON")
	var scribeResult map[string]string
	require.NoError(t, json.Unmarshal(content6, &scribeResult))
	assert.NotContains(t, scribeResult["body"], "SCRIBE_SECRET_BACKEND=environment_backend", "incident-shaped assignment should be redacted")
	assert.Contains(t, scribeResult["body"], "SCRIBE_SECRET_BACKEND=", "assignment context should remain visible")

	content7, err := os.ReadFile(jsonFile7)
	require.NoError(t, err)
	assert.True(t, json.Valid(content7), "cross-value private-key match must not corrupt JSON")
	assert.Contains(t, string(content7), "output removed", "cross-value private-key marker must fail closed")
	content8, err := os.ReadFile(jsonFile8)
	require.NoError(t, err)
	assert.True(t, json.Valid(content8), "backstop handling must preserve valid JSON")
	assert.Contains(t, string(content8), "output removed", "cross-value backstop match must fail closed")
	content9, err := os.ReadFile(jsonFile9)
	require.NoError(t, err)
	assert.True(t, json.Valid(content9))
	assert.Contains(t, string(content9), "output removed", "split PEM-like material must fail closed")

	// jq is the parser implicated by the original incident; exercise it when
	// available, while keeping the Go validity assertions portable.
	if _, err := exec.LookPath("jq"); err == nil {
		cmd := exec.Command("jq", ".", jsonFile6)
		assert.NoError(t, cmd.Run(), "jq must parse the sanitized incident-shaped fixture")
	}
}

// TestScanOutputFiles_DuplicateJSONKeys verifies that JSON files containing
// duplicate keys (which Go's map[string]any decoding drops) still have secrets
// redacted via the fail-closed fallback scan (#5705).
func TestScanOutputFiles_DuplicateJSONKeys(t *testing.T) {
	dir := t.TempDir()
	dupFile := filepath.Join(dir, "duplicate_keys.json")
	secret := "ghp_FAKEtesttoken000000000000000000000000"
	content := fmt.Sprintf(`{"a":"x %s","a":"second"}`, secret)
	require.NoError(t, os.WriteFile(dupFile, []byte(content), 0o644))

	dupWithLargeNumFile := filepath.Join(dir, "duplicate_with_large_num.json")
	contentWithLargeNum := fmt.Sprintf(`{"large":1e1000,"a":"x %s","a":"second"}`, secret)
	require.NoError(t, os.WriteFile(dupWithLargeNumFile, []byte(contentWithLargeNum), 0o644))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", printer))

	got, err := os.ReadFile(dupFile)
	require.NoError(t, err)
	assert.NotContains(t, string(got), secret)
	assert.Contains(t, string(got), "second")

	gotLarge, err := os.ReadFile(dupWithLargeNumFile)
	require.NoError(t, err)
	assert.NotContains(t, string(gotLarge), secret)
	assert.Contains(t, string(gotLarge), "second")
}

// TestScanOutputFiles_SingleRecordJSONL_PreservesLineCount verifies that
// single-record .jsonl files are processed through the line-preserving text scan
// rather than being reformatted into multi-line JSON (#5705).
func TestScanOutputFiles_SingleRecordJSONL_PreservesLineCount(t *testing.T) {
	dir := t.TempDir()
	jsonlFile := filepath.Join(dir, "events.jsonl")
	secret := "ghp_FAKEtesttoken000000000000000000000000"
	content := fmt.Sprintf(`{"action":"comment","body":"%s"}`+"\n", secret)
	require.NoError(t, os.WriteFile(jsonlFile, []byte(content), 0o644))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", printer))

	got, err := os.ReadFile(jsonlFile)
	require.NoError(t, err)
	assert.NotContains(t, string(got), secret)
	assert.Equal(t, 1, strings.Count(string(got), "\n"), "single-record jsonl must remain exactly 1 line")
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	assert.Len(t, lines, 1)
}

func TestScanOutputFiles_JSONLCrossValuePrivateKeyRemainsValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := ` ["-----BEGIN RSA` + ` PRIVATE KEY-----","KEY_MATERIAL_BASE64_VALUE_1234567890","-----END RSA` + ` PRIVATE KEY-----"]` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", ui.New(&bytes.Buffer{})))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(got), "\n"))
	line := strings.TrimSpace(string(got))
	assert.True(t, json.Valid([]byte(line)), "each JSONL record must remain valid JSON: %s", line)
	assert.Contains(t, line, "output removed", "cross-value private-key content should fail closed")
}

func TestSanitizeJSONLinesKeepsCleanDuplicateRecordWhenAnotherRecordChanges(t *testing.T) {
	pipeline := security.OutputPipeline()
	content := []byte("{\"a\":1,\"a\":2}\n{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\"}\n")
	got, findings, _, err := sanitizeJSONLines(content, pipeline)
	require.NoError(t, err)
	assert.Positive(t, findings)
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, `{"a":1,"a":2}`, lines[0], "clean duplicate-key record must remain unchanged")
	assert.True(t, json.Valid([]byte(lines[1])))
	assert.NotContains(t, lines[1], "ghp_FAKEtesttoken")
}

func TestSanitizeJSONLinesFailsClosedForPrivateKeyAcrossRecords(t *testing.T) {
	pipeline := security.OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(fmt.Sprintf("[%q]\n[%q]\n[%q]\n", begin, "MIIEpAIBAAKCAQEA0123456789abcdefghijklmnop", end))
	got, findings, details, err := sanitizeJSONLines(content, pipeline)
	require.NoError(t, err)
	assert.Positive(t, findings)
	require.NotEmpty(t, details)
	assert.Equal(t, "private_key", details[0].Name)
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	require.Len(t, lines, 3)
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)))
		assert.Contains(t, line, "output removed")
	}
}

func TestSanitizeJSONLinesFailsClosedForRawMultilinePEM(t *testing.T) {
	pipeline := security.OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(fmt.Sprintf("%s\nMIIEpAIBAAKCAQEA0123456789abcdefghijklmnop\n%s\n", begin, end))
	got, findings, details, err := sanitizeJSONLines(content, pipeline)
	require.NoError(t, err)
	assert.Positive(t, findings)
	require.NotEmpty(t, details)
	assert.Equal(t, "private_key", details[0].Name)
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	require.Len(t, lines, 3)
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)))
		assert.Contains(t, line, "output removed")
		assert.NotContains(t, line, "MIIEpAIBAAKCAQEA")
	}
}

func TestScanOutputFiles_JSONLRawMultilinePEMRemainsValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw_pem.jsonl")
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := fmt.Sprintf("%s\nMIIEpAIBAAKCAQEA0123456789abcdefghijklmnop\n%s\n", begin, end)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", ui.New(&bytes.Buffer{})))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	require.Len(t, lines, 3)
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)), "each line must be valid JSON: %s", line)
		assert.Contains(t, line, "output removed")
		assert.NotContains(t, line, "MIIEpAIBAAKCAQEA")
	}
}

func TestSanitizeJSONLines_DuplicateKeyRecordWithStructuralFindingsFailsClosed(t *testing.T) {
	pipeline := security.OutputPipeline()
	// A secret encoded with JSON Unicode escapes in a record that also has duplicate keys.
	// Raw text scanner doesn't match the unicode escapes, but structural scan decodes and finds it.
	content := []byte("{\"a\":\"first\",\"a\":\"second\",\"password\":\"\\u0073\\u0065\\u0063\\u0072\\u0065\\u0074_password_1234\"}\n")
	got, findings, details, err := sanitizeJSONLines(content, pipeline)
	require.NoError(t, err)
	assert.Positive(t, findings)
	require.NotEmpty(t, details)
	line := strings.TrimSpace(string(got))
	assert.True(t, json.Valid([]byte(line)))
	assert.Contains(t, line, "output removed")
	assert.NotContains(t, line, "password_1234")
}

func TestSanitizeJSONLines_BackstopNoStructuralFindingsPreservesLineCount(t *testing.T) {
	const secretVal = `","field2":"`
	security.ResetRuntimeSecretsForTest()
	t.Cleanup(security.ResetRuntimeSecretsForTest)
	require.True(t, security.RegisterRuntimeSecret(secretVal))

	pipeline := security.OutputPipeline()
	content := []byte("{\"field1\":\"foo\",\"field2\":\"bar\"}\n{\"field3\":\"second record\"}\n")
	got, findings, details, err := sanitizeJSONLines(content, pipeline)
	require.NoError(t, err)
	assert.Positive(t, findings)
	require.NotEmpty(t, details)
	assert.Equal(t, 2, strings.Count(string(got), "\n"), "line count must remain exactly 2, no extra blank lines")
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.True(t, json.Valid([]byte(lines[0])))
	assert.True(t, json.Valid([]byte(lines[1])))
	assert.NotContains(t, lines[0], secretVal)
}

func TestSanitizeJSONLines_NeedsRewriteWithBackstopPrivateKeyFailsClosed(t *testing.T) {
	pipeline := security.OutputPipeline()
	begin := "-----BEGIN RSA" + " PRIVATE KEY-----"
	end := "-----END RSA" + " PRIVATE KEY-----"
	content := []byte(fmt.Sprintf("{\"token\":\"ghp_FAKEtesttoken000000000000000000000000\",\"pem\":\"%s\\n%s\"}\n", begin, end))
	got, findings, details, err := sanitizeJSONLines(content, pipeline)
	require.NoError(t, err)
	assert.Positive(t, findings)
	require.NotEmpty(t, details)
	line := strings.TrimSpace(string(got))
	assert.True(t, json.Valid([]byte(line)))
	assert.Contains(t, line, "output removed")
}

func TestScanOutputFiles_QuietJSONFallbackDoesNotPrintPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-chosen-secret-name.json")
	content := `{"token":"ghp_FAKEtesttoken000000000000000000000000","token":"second"}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	var output bytes.Buffer
	require.NoError(t, scanOutputFiles(dir, "trace-id", "quiet", ui.New(&output)))
	assert.NotContains(t, output.String(), "agent-chosen-secret-name.json")
}

func TestScanOutputFiles_ReturnsErrorWhenSanitizedWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(path, []byte("Token: ghp_FAKEtesttoken000000000000000000000000"), 0o644))

	err := scanOutputFilesWithWriter(dir, "trace-id", "", ui.New(&bytes.Buffer{}), func(string, []byte, os.FileMode) error {
		return os.ErrPermission
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not write sanitized secret.txt")
}

func TestScanOutputFiles_QuietJSONWriteFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"token":"ghp_FAKEtesttoken000000000000000000000000"}`), 0o644))
	err := scanOutputFilesWithWriter(dir, "trace-id", "quiet", ui.New(&bytes.Buffer{}), func(string, []byte, os.FileMode) error {
		return os.ErrPermission
	})
	require.Error(t, err)
	assert.EqualError(t, err, "could not write sanitized output file")
}

func TestScanOutputFiles_BackstopDoesNotRemaskExistingMask(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-result.json")
	content := "{\n  \"body\": \"Guard against null values with token_usage = metrics.get(\\\"token_usage\\\") or {}\"\n}"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	var output bytes.Buffer
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", ui.New(&output)))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(first), "metr...", "the first redaction should retain its informative prefix")
	assert.NotContains(t, output.String(), "(backstop)", "the structural mask should not be redacted again")

	assert.Equal(t, 1, strings.Count(output.String(), "Sanitized [env_assignment]"), "one secret should create one finding")

	// Rescanning the already-sanitized document must be idempotent.
	var secondOutput bytes.Buffer
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", ui.New(&secondOutput)))
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestSanitizeJSONStructure_CleanKeyWinsSanitizedCollision(t *testing.T) {
	const sanitized = "ghp_..."
	pipeline := security.OutputPipeline()
	input := map[string]any{
		"ghp_FAKEtesttoken000000000000000000000000": "secretkeyval",
		sanitized: "plainval",
	}
	input["ghp_FAKEtesttoken000000000000000000000000"] = "secretkeyval"
	for range 20 {
		got, findings := sanitizeJSONStructure(input, pipeline)
		require.NotEmpty(t, findings)
		result := got.(map[string]any)
		assert.Equal(t, "plainval", result[sanitized], "clean key must retain its name and value")
		assert.NotContains(t, result, "ghp_FAKEtesttoken000000000000000000000000")
		assert.Len(t, result, 2)
	}
}

// TestScanOutputFiles_CleanJSON verifies that clean JSON files with no findings
// remain completely untouched byte-for-byte.
func TestScanOutputFiles_CleanJSON(t *testing.T) {
	dir := t.TempDir()
	cleanFile := filepath.Join(dir, "clean.json")
	content := `{"count": 42, "status": "ok"}`
	require.NoError(t, os.WriteFile(cleanFile, []byte(content), 0o644))

	var buf bytes.Buffer
	printer := ui.New(&buf)
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", printer))

	got, err := os.ReadFile(cleanFile)
	require.NoError(t, err)
	assert.Equal(t, content, string(got), "clean JSON should remain byte-for-byte identical")
}

func TestScanOutputFiles_NumericRuntimeSecretRedacted(t *testing.T) {
	const secretNum = "123456789012"
	require.True(t, security.RegisterRuntimeSecret(secretNum))

	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "numeric.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(fmt.Sprintf(`{"id":%s}`, secretNum)), 0o644))

	jsonlPath := filepath.Join(dir, "numeric.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(fmt.Sprintf(`{"id":%s}`+"\n", secretNum)), 0o644))

	var output bytes.Buffer
	require.NoError(t, scanOutputFiles(dir, "trace-id", "", ui.New(&output)))

	gotJSON, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	assert.NotContains(t, string(gotJSON), secretNum)
	assert.True(t, json.Valid(gotJSON))

	gotJSONL, err := os.ReadFile(jsonlPath)
	require.NoError(t, err)
	assert.NotContains(t, string(gotJSONL), secretNum)
	lines := strings.Split(strings.TrimSpace(string(gotJSONL)), "\n")
	require.Len(t, lines, 1)
	assert.True(t, json.Valid([]byte(lines[0])))
}

func TestHasDuplicateJSONKeys(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{"no duplicates", `{"a": 1, "b": 2}`, false},
		{"duplicate top-level", `{"a": 1, "a": 2}`, true},
		{"nested duplicate", `{"parent": {"x": 1, "x": 2}}`, true},
		{"nested no duplicate", `{"p1": {"x": 1}, "p2": {"x": 1}}`, false},
		{"array with duplicate in object", `[{"k": 1, "k": 2}]`, true},
		{"array with separate objects", `[{"k": 1}, {"k": 2}]`, false},
		{"value matches key name", `{"a": "a", "b": "a"}`, false},
		{"array values inside object", `{"a": [1, 2], "b": 3}`, false},
		{"out-of-range number before duplicate keys", `{"large": 1e1000, "dup": 1, "dup": 2}`, true},
		{"out-of-range number without duplicates", `{"large": 1e1000, "no_dup": 1}`, false},
		{"malformed json", `{"a": `, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, hasDuplicateJSONKeys([]byte(tc.json)))
		})
	}
}
