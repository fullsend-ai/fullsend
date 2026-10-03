package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// logFetchTimeout bounds the GetRunLogs API call so a hanging request
// does not block the scenario step indefinitely.
const logFetchTimeout = 30 * time.Second

const (
	// maxFailureLogRuns caps how many of the repository's most recent
	// workflow runs CollectFailureLogs considers. A scenario dispatches a
	// handful of runs; the cap keeps a pathological repository from
	// stretching the After hook.
	maxFailureLogRuns = 10

	// failureLogBudget bounds the whole of CollectFailureLogs so log
	// collection for one failed scenario cannot eat into the suite's
	// timeout budget (docs/guides/dev/behaviour-testing.md).
	failureLogBudget = 2 * time.Minute

	// runLogsUnavailableFile is written in place of workflow-logs.txt when
	// a run's logs could not be fetched, so the artifact states why.
	runLogsUnavailableFile = "workflow-logs-unavailable.txt"

	// failureSummaryFile is written for every failed scenario by
	// CollectFailureLogs.
	failureSummaryFile = "failure-summary.txt"
)

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
// When the logs cannot be fetched, the debug directory holds a
// workflow-logs-unavailable.txt stating why instead.
//
// The logs are external content that leaves the runner as a CI artifact,
// so they are passed through the secret redactor and written 0o600
// (docs/contributing/go-code.md, "Credential redaction for external
// content").
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

	logPath, err := writeWorkflowRunLogs(ctx, w, label, run)
	if err != nil {
		worldLogf(w, "save workflow run logs: %v", err)
		return
	}

	worldLogf(w, "save workflow run logs: %s run %d logs saved to %s", label, run.ID, logPath)
}

// writeWorkflowRunLogs fetches a run's logs and writes them, redacted,
// to debug-<label>-run-<id>/workflow-logs.txt, recording the run in
// w.SavedLogRunIDs. When the fetch fails it writes
// workflow-logs-unavailable.txt with the reason instead and returns the
// fetch error. Callers must have checked BEHAVIOUR_ARTIFACT_DIR is set.
func writeWorkflowRunLogs(ctx context.Context, w *world.World, label string, run *forge.WorkflowRun) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
	defer cancel()

	logs, fetchErr := w.CI.GetRunLogs(fetchCtx, w.Org, w.RepoName, run.ID)

	debugDir, err := prepareDebugDir(label, run.ID)
	if err != nil {
		return "", errors.Join(fetchErr, fmt.Errorf("create debug dir: %w", err))
	}

	if fetchErr != nil {
		fetchErr = fmt.Errorf("fetch logs for %s run %d: %w", label, run.ID, fetchErr)
		notePath := filepath.Join(debugDir, runLogsUnavailableFile)
		note := fmt.Sprintf("Logs for workflow run %d (%s) could not be collected.\nRun: %s\nReason: %v\n",
			run.ID, label, describeRun(*run), fetchErr)
		if err := writeRedactedArtifact(notePath, note); err != nil {
			return "", errors.Join(fetchErr, err)
		}
		return "", fetchErr
	}

	logPath := filepath.Join(debugDir, "workflow-logs.txt")
	if err := writeRedactedArtifact(logPath, logs); err != nil {
		return "", err
	}
	if w.SavedLogRunIDs == nil {
		w.SavedLogRunIDs = make(map[int]bool)
	}
	w.SavedLogRunIDs[run.ID] = true
	return logPath, nil
}

// CollectFailureLogs is called by the suite's After hook for a failed
// scenario, before CleanupScenario and before the leased repository —
// and with it every run's logs — is deleted. It saves the logs of the
// repository's recent workflow runs (and the scenario's resolved run)
// that were not already saved during the scenario, which covers the
// failure paths where no step got hold of a run: a wait that timed out,
// a setup step that failed before any run was resolved, or a step that
// waits for a run without saving its logs.
//
// It always writes a failure-summary.txt under
// debug-scenario-<name>-<suffix>/ listing the scenario error and, for
// each run, where its logs were saved or why they could not be
// collected — so every failed scenario leaves an artifact, even when
// there are no logs to collect.
//
// Like saveWorkflowRunLogs it is best-effort, a no-op when
// BEHAVIOUR_ARTIFACT_DIR is unset, redacts everything it writes, and
// writes files 0o600.
func CollectFailureLogs(ctx context.Context, w *world.World, scenarioErr error) {
	if w == nil {
		return
	}
	artifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if artifactDir == "" {
		worldLogf(w, "collect failure logs: BEHAVIOUR_ARTIFACT_DIR unset, skipping log collection for failed scenario %q", w.ScenarioName)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, failureLogBudget)
	defer cancel()

	repo := "(none)"
	if w.RepoName != "" {
		repo = w.Org + "/" + w.RepoName
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Scenario: %s\n", w.ScenarioName)
	fmt.Fprintf(&b, "Error: %v\n", scenarioErr)
	fmt.Fprintf(&b, "Repository: %s\n", repo)
	b.WriteString("\nWorkflow runs:\n")
	b.WriteString(collectScenarioRunLogs(ctx, w))

	summaryPath, err := writeFailureSummary(artifactDir, w.ScenarioName, b.String())
	if err != nil {
		worldLogf(w, "collect failure logs: %v", err)
		return
	}
	worldLogf(w, "collect failure logs: failure summary for scenario %q saved to %s", w.ScenarioName, summaryPath)
}

// collectScenarioRunLogs saves the logs of the scenario's runs that are
// not yet saved and returns one summary line per run, or lines stating
// why there are no runs to collect.
func collectScenarioRunLogs(ctx context.Context, w *world.World) string {
	if w.RepoName == "" {
		return "  none: the scenario failed before a repository was configured, so there are no workflow runs to collect\n"
	}
	if w.CI == nil {
		return "  none: no CI driver is configured, so workflow runs cannot be collected\n"
	}

	var b strings.Builder
	var runs []forge.WorkflowRun
	if w.WorkflowRun != nil {
		runs = append(runs, *w.WorkflowRun)
	}

	if lister, ok := w.CI.(ci.RunLister); ok {
		listCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
		recent, err := lister.ListRecentRuns(listCtx, w.Org, w.RepoName, maxFailureLogRuns)
		cancel()
		if err != nil {
			fmt.Fprintf(&b, "  listing the repository's workflow runs failed: %v\n", err)
		}
		for _, run := range recent {
			if inScenarioWindow(run, w.ScenarioBegin) {
				runs = append(runs, run)
			}
		}
	} else {
		b.WriteString("  the CI driver cannot list workflow runs (it does not implement ci.RunLister); only the run resolved by a step is collected\n")
	}

	seen := make(map[int]bool, len(runs))
	collected := 0
	for i := range runs {
		run := runs[i]
		if seen[run.ID] {
			continue
		}
		seen[run.ID] = true
		collected++
		if w.SavedLogRunIDs[run.ID] {
			fmt.Fprintf(&b, "  - %s: logs already saved during the scenario\n", describeRun(run))
			continue
		}
		logPath, err := writeWorkflowRunLogs(ctx, w, runLabel(run), &run)
		if err != nil {
			worldLogf(w, "collect failure logs: %v", err)
			fmt.Fprintf(&b, "  - %s: logs could not be collected: %v\n", describeRun(run), err)
			continue
		}
		fmt.Fprintf(&b, "  - %s: logs saved to %s\n", describeRun(run), logPath)
	}
	if collected == 0 {
		b.WriteString("  none: no workflow run was found in the repository for this scenario\n")
	}
	return b.String()
}

// inScenarioWindow reports whether run was created during the scenario.
// Leased repositories are recreated per lease, so this only matters for
// a scenario that named a long-lived repository. A run whose creation
// time is unknown or unparsable is kept rather than silently dropped,
// and the window opens issueOpenDrainSkewBuffer early to tolerate clock
// skew between the runner and the forge.
func inScenarioWindow(run forge.WorkflowRun, begin time.Time) bool {
	if begin.IsZero() {
		return true
	}
	created, err := time.Parse(time.RFC3339, run.CreatedAt)
	if err != nil {
		return true
	}
	return !created.Before(begin.Add(-issueOpenDrainSkewBuffer))
}

// describeRun formats a run for a summary or note line.
func describeRun(run forge.WorkflowRun) string {
	s := fmt.Sprintf("run %d %q (status %q, conclusion %q)", run.ID, run.Name, run.Status, run.Conclusion)
	if run.HTMLURL != "" {
		s += " " + run.HTMLURL
	}
	return s
}

// runLabel derives a debug-directory label from a run's workflow name.
func runLabel(run forge.WorkflowRun) string {
	return pathSlug(run.Name, 40, "workflow")
}

// pathSlug lowercases s and replaces every run of characters outside
// [a-z0-9] with a single '-', truncated to maxLen characters, so it is safe
// to embed in a directory name. Returns fallback when nothing remains.
func pathSlug(s string, maxLen int, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > maxLen {
		slug = slug[:maxLen]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return fallback
	}
	return slug
}

// writeFailureSummary writes content to a fresh
// debug-scenario-<name>-<suffix>/failure-summary.txt under artifactDir.
// The random suffix keeps scenarios that share a name (scenario
// outlines, reruns) from overwriting each other.
func writeFailureSummary(artifactDir, scenarioName, content string) (string, error) {
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", fmt.Errorf("creating artifact dir: %w", err)
	}
	dir, err := os.MkdirTemp(artifactDir, "debug-scenario-"+pathSlug(scenarioName, 60, "unnamed")+"-")
	if err != nil {
		return "", fmt.Errorf("creating failure summary dir: %w", err)
	}
	path := filepath.Join(dir, failureSummaryFile)
	if err := writeRedactedArtifact(path, content); err != nil {
		return "", err
	}
	return path, nil
}

// writeRedactedArtifact writes content to path through the secret
// redactor with mode 0o600: everything under BEHAVIOUR_ARTIFACT_DIR
// leaves the runner as a CI artifact.
func writeRedactedArtifact(path, content string) error {
	redacted := content
	if res := security.NewSecretRedactor().Scan(content); res.Sanitized != "" {
		redacted = res.Sanitized
	}
	if err := os.WriteFile(path, []byte(redacted), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
