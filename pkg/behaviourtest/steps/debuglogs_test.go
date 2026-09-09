package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestSaveWorkflowRunLogs_NilRun(t *testing.T) {
	t.Parallel()
	var logged []string
	w := &world.World{
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	// Should be a no-op — no panic, no log.
	saveWorkflowRunLogs(context.Background(), w, "triage", nil)
	assert.Empty(t, logged)
}

func TestSaveWorkflowRunLogs_SkipsWhenArtifactDirUnset(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", "")

	var logged []string
	w := &world.World{
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	run := &forge.WorkflowRun{ID: 10}
	saveWorkflowRunLogs(context.Background(), w, "triage", run)

	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "BEHAVIOUR_ARTIFACT_DIR unset")
	assert.Contains(t, logged[0], "skipping log collection")
}

func TestSaveWorkflowRunLogs_WritesLogs(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	var logged []string
	fakeCI := &fakeDebugCI{logs: "=== triage run logs ==="}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		CI:       fakeCI,
		Logf:     func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	run := &forge.WorkflowRun{ID: 42}
	saveWorkflowRunLogs(context.Background(), w, "triage", run)

	// Verify the log file was written.
	logPath := filepath.Join(artifactDir, "debug-triage-run-42", "workflow-logs.txt")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, "=== triage run logs ===", string(data))

	// Verify success was logged.
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "logs saved to")
}

func TestSaveWorkflowRunLogs_GetRunLogsError(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	var logged []string
	fakeCI := &fakeDebugCI{logsErr: fmt.Errorf("API error")}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		CI:       fakeCI,
		Logf:     func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	run := &forge.WorkflowRun{ID: 99}
	saveWorkflowRunLogs(context.Background(), w, "agent", run)

	// Should log the error, not panic or fail.
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "fetch logs")
	assert.Contains(t, logged[0], "API error")

	// No log file should exist.
	logPath := filepath.Join(artifactDir, "debug-agent-run-99", "workflow-logs.txt")
	_, err := os.Stat(logPath)
	assert.True(t, os.IsNotExist(err))
}

func TestSaveWorkflowRunLogs_NilLogf(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fakeCI := &fakeDebugCI{logs: "log content"}
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		CI:       fakeCI,
		// Logf deliberately nil — worldLogf guards it.
	}

	run := &forge.WorkflowRun{ID: 1}
	// Should not panic even with nil Logf.
	saveWorkflowRunLogs(context.Background(), w, "triage", run)

	logPath := filepath.Join(artifactDir, "debug-triage-run-1", "workflow-logs.txt")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, "log content", string(data))
}

func TestPrepareDebugDir_WithArtifactDir(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	dir, err := prepareDebugDir("triage", 123)
	require.NoError(t, err)

	expected := filepath.Join(artifactDir, "debug-triage-run-123")
	assert.Equal(t, expected, dir)
	assert.DirExists(t, dir)
}

func TestPrepareDebugDir_WithoutArtifactDir(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", "")

	_, err := prepareDebugDir("agent", 456)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BEHAVIOUR_ARTIFACT_DIR is not set")
}

// fakeDebugCI implements ci.Driver for debug log tests. Unused methods
// come from the embedded interface and panic when called.
type fakeDebugCI struct {
	ci.Driver

	logs    string
	logsErr error
}

func (f *fakeDebugCI) GetRunLogs(context.Context, string, string, int) (string, error) {
	return f.logs, f.logsErr
}
