package cli

import (
	"bytes"
	"encoding/json"
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
	} {
		content, err := os.ReadFile(p.path)
		require.NoError(t, err)
		assert.True(t, json.Valid(content), "sanitized %s must remain valid JSON: %s", filepath.Base(p.path), string(content))
		assert.NotContains(t, string(content), p.secret, "secret must be redacted from %s", filepath.Base(p.path))
	}
}
