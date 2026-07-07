package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// TestScanOutputFilesSkipsTelemetryArtifacts pins that the host-side output
// redaction scan does NOT rewrite the telemetry files. The NDJSON file is still
// held open for append by the recorder during the scan, so an in-place rewrite
// would truncate it out from under the open handle; and both files are
// metadata-only by construction, so they don't need redaction. A normal output
// file must still be sanitized.
func TestScanOutputFiles_SkipsTelemetryArtifacts(t *testing.T) {
	dir := t.TempDir()
	const secret = "Token: ghp_FAKEtesttoken000000000000000000000000\n"

	telem := filepath.Join(dir, telemetry.TelemetryFile)
	summary := filepath.Join(dir, telemetry.SummaryFile)
	normal := filepath.Join(dir, "output.txt")
	// A sandbox agent could write a file that merely shares the telemetry name
	// under its iteration output; that must still be sanitized (it is not the
	// recorder's own artifact).
	nested := filepath.Join(dir, "iteration-1", "output", telemetry.TelemetryFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o755))
	for _, p := range []string{telem, summary, normal, nested} {
		require.NoError(t, os.WriteFile(p, []byte(secret), 0o644))
	}

	err := scanOutputFiles(dir, "trace-id", ui.New(&bytes.Buffer{}))
	require.NoError(t, err)

	got, err := os.ReadFile(telem)
	require.NoError(t, err)
	assert.Equal(t, secret, string(got), "the recorder's own run-telemetry.jsonl must be left untouched")

	got, err = os.ReadFile(summary)
	require.NoError(t, err)
	assert.Equal(t, secret, string(got), "the recorder's own run-summary.json must be left untouched")

	got, err = os.ReadFile(normal)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "ghp_FAKEtest", "non-telemetry output must still be sanitized")

	got, err = os.ReadFile(nested)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "ghp_FAKEtest", "an agent file sharing the telemetry name must still be sanitized")
}
