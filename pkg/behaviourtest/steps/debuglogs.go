package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// logFetchTimeout bounds the GetRunLogs API call so a hanging request
// does not block the scenario step indefinitely.
const logFetchTimeout = 30 * time.Second

// saveWorkflowRunLogs fetches the logs for a workflow run and writes
// them into a debug subdirectory under BEHAVIOUR_ARTIFACT_DIR. Called
// unconditionally after a scenario resolves a workflow run so that even
// successful runs leave a log trail for diagnosing dispatch, harness,
// or skip behaviour.
//
// When BEHAVIOUR_ARTIFACT_DIR is not set (local development), log
// collection is skipped entirely to avoid leaking orphaned temp
// directories that no one will inspect.
//
// Logs are fetched before the debug directory is created so that a
// failed API call does not leave an empty directory behind.
//
// Errors are logged but not returned — log collection is best-effort
// and must not fail the scenario.
func saveWorkflowRunLogs(ctx context.Context, w *world.World, label string, run *forge.WorkflowRun) {
	if run == nil {
		return
	}

	if strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR")) == "" {
		worldLogf(w, "save workflow run logs: BEHAVIOUR_ARTIFACT_DIR unset, skipping log collection for %s run %d", label, run.ID)
		return
	}

	fetchCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
	defer cancel()

	logs, err := w.CI.GetRunLogs(fetchCtx, w.Org, w.RepoName, run.ID)
	if err != nil {
		worldLogf(w, "save workflow run logs: fetch logs for %s run %d: %v", label, run.ID, err)
		return
	}

	debugDir, err := prepareDebugDir(label, run.ID)
	if err != nil {
		worldLogf(w, "save workflow run logs: create debug dir: %v", err)
		return
	}

	logPath := filepath.Join(debugDir, "workflow-logs.txt")
	if err := os.WriteFile(logPath, []byte(logs), 0o644); err != nil {
		worldLogf(w, "save workflow run logs: write %s: %v", logPath, err)
		return
	}

	worldLogf(w, "save workflow run logs: %s run %d logs saved to %s", label, run.ID, logPath)
}

// prepareDebugDir creates a debug subdirectory for a workflow run's
// logs under BEHAVIOUR_ARTIFACT_DIR so CI's upload-artifact ships the
// logs automatically. Returns an error when BEHAVIOUR_ARTIFACT_DIR is
// not set — callers should skip log collection in that case to avoid
// leaking orphaned temp directories.
func prepareDebugDir(label string, runID int) (string, error) {
	dirName := fmt.Sprintf("debug-%s-run-%d", label, runID)

	ciArtifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if ciArtifactDir == "" {
		return "", fmt.Errorf("BEHAVIOUR_ARTIFACT_DIR is not set")
	}

	debugDir := filepath.Join(ciArtifactDir, dirName)
	if err := os.MkdirAll(debugDir, 0o755); err != nil {
		return "", fmt.Errorf("creating debug dir: %w", err)
	}
	return debugDir, nil
}
