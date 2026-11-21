package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// TestCollectPostScanSecurityArtifactsRunsOnScanFailure pins that a failed host
// output scan still extracts the sandbox audit trail and still reports the scan
// failure.
func TestCollectPostScanSecurityArtifactsRunsOnScanFailure(t *testing.T) {
	runDir := t.TempDir()
	var downloaded bool
	original := downloadSandboxFindings
	downloadSandboxFindings = func(_, _, _ string) error {
		downloaded = true
		return nil
	}
	t.Cleanup(func() { downloadSandboxFindings = original })

	var buf bytes.Buffer
	scanErr := errors.New("could not write sanitized output file")
	err := collectPostScanSecurityArtifacts(runDir, "sandbox", ui.New(&buf), "", scanErr)

	require.Error(t, err)
	assert.True(t, downloaded, "sandbox findings must be extracted even when the scan failed")
	assert.Contains(t, err.Error(), "post-agent output scan failed")
	assert.Contains(t, buf.String(), "Security findings extracted")
}
