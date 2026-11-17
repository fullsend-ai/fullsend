package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	err := scanOutputFiles(dir, "trace-id", ui.New(&bytes.Buffer{}))
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

	require.NoError(t, scanOutputFiles(dir, "trace-id", ui.New(&bytes.Buffer{})))

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

	var buf bytes.Buffer
	printer := ui.New(&buf)
	require.NoError(t, scanOutputFiles(dir, "trace-id", printer))

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
	require.NoError(t, scanOutputFiles(dir, "trace-id", printer))

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
	require.NoError(t, scanOutputFiles(dir, "trace-id", printer))

	got, err := os.ReadFile(jsonlFile)
	require.NoError(t, err)
	assert.NotContains(t, string(got), secret)
	assert.Equal(t, 1, strings.Count(string(got), "\n"), "single-record jsonl must remain exactly 1 line")
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	assert.Len(t, lines, 1)
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
	require.NoError(t, scanOutputFiles(dir, "trace-id", printer))

	got, err := os.ReadFile(cleanFile)
	require.NoError(t, err)
	assert.Equal(t, content, string(got), "clean JSON should remain byte-for-byte identical")
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
