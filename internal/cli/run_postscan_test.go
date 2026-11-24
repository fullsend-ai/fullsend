package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
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

// TestCollectPostScanSecurityArtifactsKeepsQuietStatusDetail pins that the wrap
// collectPostScanSecurityArtifacts adds around the scan error keeps the scan's
// fixed quiet wording reachable, so the completion comment can post a count of
// files instead of the agent-chosen names (ADR 0163).
func TestCollectPostScanSecurityArtifactsKeepsQuietStatusDetail(t *testing.T) {
	runDir := t.TempDir()
	original := downloadSandboxFindings
	downloadSandboxFindings = func(_, _, _ string) error { return errors.New("no findings") }
	t.Cleanup(func() { downloadSandboxFindings = original })

	const name = "@org/team-secret.json"
	scanErr := &outputScanError{err: fmt.Errorf("could not read %s: denied", name), files: 1}
	var buf bytes.Buffer
	err := collectPostScanSecurityArtifacts(runDir, "sandbox", ui.New(&buf), config.ProgressVerbosityQuiet, scanErr)

	require.Error(t, err)
	assert.Contains(t, err.Error(), name, "the run error keeps the path for telemetry")
	assert.Equal(t, "post-agent output scan failed: 1 file(s) could not be scanned",
		failureStatusDetail(err, config.ProgressVerbosityQuiet))
	assert.Contains(t, failureStatusDetail(err, ""), name, "full posts the error as before")
}
