package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	run := &forge.WorkflowRun{ID: 42, Status: "completed"}
	saveWorkflowRunLogs(context.Background(), w, "triage", run)

	// Verify the log file was written.
	logPath := filepath.Join(artifactDir, "debug-triage-run-42", "workflow-logs.txt")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, "=== triage run logs ===", string(data))

	// Verify success was logged.
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "logs saved to")
	assert.True(t, w.SavedLogRunIDs[42], "a saved run must be recorded so failure collection skips it")
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

	// An explicit note states why the logs are missing.
	notePath := filepath.Join(artifactDir, "debug-agent-run-99", runLogsUnavailableFile)
	note, err := os.ReadFile(notePath)
	require.NoError(t, err, "a failed log fetch must leave an explanatory artifact")
	assert.Contains(t, string(note), "could not be collected")
	assert.Contains(t, string(note), "API error")
	info, err := os.Stat(notePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.False(t, w.SavedLogRunIDs[99], "a run whose logs were not saved must not be recorded as saved")
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

// fakeHarnessWaitCI answers WaitForHarnessAgent with a fixed run and
// error, and GetRunLogs with fixed logs.
type fakeHarnessWaitCI struct {
	fakeDebugCI
	run *forge.WorkflowRun
	err error
}

func (f *fakeHarnessWaitCI) WaitForHarnessAgent(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return f.run, f.err
}

// TestThenHarnessWorkflowCompletes_SavesFailedRunLogs checks that a
// harness run that concluded with a failure leaves its logs in the
// artifact dir: the pool repo, and with it the run's logs, is deleted
// when the lease ends.
func TestThenHarnessWorkflowCompletes_SavesFailedRunLogs(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	w := &world.World{
		Org:           "org",
		RepoName:      "repo",
		ScenarioStart: time.Now(),
		CI: &fakeHarnessWaitCI{
			fakeDebugCI: fakeDebugCI{logs: "=== pi-smoke failed run ==="},
			run:         &forge.WorkflowRun{ID: 77, Conclusion: "failure"},
			err:         fmt.Errorf(`harness agent "pi-smoke": workflow run 77 concluded with "failure" before producing artifact`),
		},
		Logf: func(string, ...any) {},
	}

	err := thenHarnessWorkflowCompletes(w, "pi-smoke")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before producing artifact")

	data, readErr := os.ReadFile(filepath.Join(artifactDir, "debug-pi-smoke-run-77", "workflow-logs.txt"))
	require.NoError(t, readErr, "the failed run's logs must be saved")
	assert.Equal(t, "=== pi-smoke failed run ===", string(data))
}

// TestThenHarnessWorkflowCompletes_TimeoutWithoutRun keeps the timeout
// path (no run) an error with nothing saved by the step itself; the
// After hook's CollectFailureLogs covers it (see
// TestCollectFailureLogs_TimeoutWithoutRunSavesListedRun).
func TestThenHarnessWorkflowCompletes_TimeoutWithoutRun(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	w := &world.World{
		Org:           "org",
		RepoName:      "repo",
		ScenarioStart: time.Now(),
		CI:            &fakeHarnessWaitCI{err: fmt.Errorf(`harness agent "pi-smoke" did not complete successfully`)},
		Logf:          func(string, ...any) {},
	}

	require.Error(t, thenHarnessWorkflowCompletes(w, "pi-smoke"))
	entries, err := os.ReadDir(artifactDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestSaveWorkflowRunLogs_RedactsAndRestrictsMode checks that saved logs
// are redacted and written 0o600: they are external content uploaded as
// a CI artifact.
func TestSaveWorkflowRunLogs_RedactsAndRestrictsMode(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	token := "ghp_" + strings.Repeat("A", 36)
	w := &world.World{
		Org:      "org",
		RepoName: "repo",
		CI:       &fakeDebugCI{logs: "step output\nGH_TOKEN=" + token + "\ndone"},
		Logf:     func(string, ...any) {},
	}

	saveWorkflowRunLogs(context.Background(), w, "pi-smoke", &forge.WorkflowRun{ID: 5})

	logPath := filepath.Join(artifactDir, "debug-pi-smoke-run-5", "workflow-logs.txt")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), token, "a token in the logs must be redacted")
	assert.Contains(t, string(data), "step output")

	info, err := os.Stat(logPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// fakeListerCI implements ci.Driver and ci.RunLister for failure log
// collection tests: ListRecentRuns answers with runs (or listErr), and
// GetRunLogs answers per run ID from logs, or with logsErr[runID].
type fakeListerCI struct {
	ci.Driver

	runs    []forge.WorkflowRun
	listErr error
	logs    map[int]string
	logsErr map[int]error
	fetched []int
}

func (f *fakeListerCI) ListRecentRuns(context.Context, string, string, int) ([]forge.WorkflowRun, error) {
	return f.runs, f.listErr
}

func (f *fakeListerCI) GetRunLogs(_ context.Context, _, _ string, runID int) (string, error) {
	f.fetched = append(f.fetched, runID)
	if err := f.logsErr[runID]; err != nil {
		return "", err
	}
	return f.logs[runID], nil
}

// readFailureSummary returns the single failure summary written under
// artifactDir, checking it is written 0o600.
func readFailureSummary(t *testing.T, artifactDir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(artifactDir, "debug-scenario-*", failureSummaryFile))
	require.NoError(t, err)
	require.Len(t, matches, 1, "a failed scenario must leave exactly one failure summary")
	info, err := os.Stat(matches[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	return string(data)
}

func TestCollectFailureLogs_NilWorld(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
	CollectFailureLogs(context.Background(), nil, fmt.Errorf("boom"))
}

func TestCollectFailureLogs_SkipsWhenArtifactDirUnset(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", "")

	var logged []string
	fake := &fakeListerCI{runs: []forge.WorkflowRun{{ID: 1}}}
	w := &world.World{
		Org: "org", RepoName: "repo", CI: fake,
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	assert.Empty(t, fake.fetched, "nothing is fetched when there is nowhere to save it")
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "BEHAVIOUR_ARTIFACT_DIR unset")
}

// TestCollectFailureLogs_TimeoutWithoutRunSavesListedRun covers the path
// #8037 reports: a harness wait timed out, so no step got a run and no
// logs were saved; the After hook finds the run in the repository and
// saves its logs before the lease ends.
func TestCollectFailureLogs_TimeoutWithoutRunSavesListedRun(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fake := &fakeListerCI{
		runs: []forge.WorkflowRun{{ID: 31, Name: "Fullsend", Status: "completed", Conclusion: "failure", HTMLURL: "https://github.com/org/test-repo-03/actions/runs/31"}},
		logs: map[int]string{31: "=== timed-out harness run ==="},
	}
	w := &world.World{
		Org: "org", RepoName: "test-repo-03", CI: fake,
		ScenarioName: "pi runtime smoke",
		Logf:         func(string, ...any) {},
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf(`harness agent "pi-smoke" did not complete successfully`))

	data, err := os.ReadFile(filepath.Join(artifactDir, "debug-fullsend-run-31", "workflow-logs.txt"))
	require.NoError(t, err, "the run no step resolved must have its logs saved")
	assert.Equal(t, "=== timed-out harness run ===", string(data))

	summary := readFailureSummary(t, artifactDir)
	assert.Contains(t, summary, "Scenario: pi runtime smoke")
	assert.Contains(t, summary, `did not complete successfully`)
	assert.Contains(t, summary, "Repository: org/test-repo-03")
	assert.Contains(t, summary, "run 31")
	assert.Contains(t, summary, "actions/runs/31")
	assert.Contains(t, summary, "logs saved to")
}

func TestCollectFailureLogs_SkipsAlreadySavedAndReportsFetchErrors(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fake := &fakeListerCI{
		runs: []forge.WorkflowRun{
			{ID: 3, Name: "Fullsend", Status: "in_progress"},
			{ID: 2, Name: "Fullsend", Status: "completed", Conclusion: "failure"},
			{ID: 1, Name: "Fullsend", Status: "completed", Conclusion: "success"},
		},
		logs:    map[int]string{2: "run 2 logs"},
		logsErr: map[int]error{3: fmt.Errorf("logs not available for an in-progress run")},
	}
	w := &world.World{
		Org: "org", RepoName: "repo", CI: fake,
		ScenarioName: "triage",
		// The step-resolved run duplicates a listed run; it is fetched once.
		WorkflowRun:    &forge.WorkflowRun{ID: 2, Name: "Fullsend"},
		SavedLogRunIDs: map[int]bool{1: true},
		Logf:           func(string, ...any) {},
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	assert.ElementsMatch(t, []int{2, 3}, fake.fetched, "already-saved runs are not fetched again, duplicates once")
	_, err := os.ReadFile(filepath.Join(artifactDir, "debug-fullsend-run-2", "workflow-logs.txt"))
	require.NoError(t, err)
	note, err := os.ReadFile(filepath.Join(artifactDir, "debug-fullsend-run-3", runLogsUnavailableFile))
	require.NoError(t, err, "a run whose logs cannot be fetched gets an explanatory note")
	assert.Contains(t, string(note), "in-progress run")

	summary := readFailureSummary(t, artifactDir)
	assert.Contains(t, summary, "run 1")
	assert.Contains(t, summary, "logs already saved during the scenario")
	assert.Contains(t, summary, "logs could not be collected")
	assert.Contains(t, summary, "in-progress run")
	assert.Equal(t, 1, strings.Count(summary, "run 2 "), "a run is listed once")
}

func TestCollectFailureLogs_ListErrorStillCollectsResolvedRun(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fake := &fakeListerCI{
		listErr: fmt.Errorf("API rate limit exceeded"),
		logs:    map[int]string{8: "resolved run logs"},
	}
	w := &world.World{
		Org: "org", RepoName: "repo", CI: fake,
		WorkflowRun: &forge.WorkflowRun{ID: 8, Name: "Fullsend"},
		Logf:        func(string, ...any) {},
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	_, err := os.ReadFile(filepath.Join(artifactDir, "debug-fullsend-run-8", "workflow-logs.txt"))
	require.NoError(t, err)
	summary := readFailureSummary(t, artifactDir)
	assert.Contains(t, summary, "listing the repository's workflow runs failed")
	assert.Contains(t, summary, "API rate limit exceeded")
}

func TestCollectFailureLogs_DriverWithoutRunLister(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	w := &world.World{
		Org: "org", RepoName: "repo",
		CI:          &fakeDebugCI{logs: "resolved run logs"},
		WorkflowRun: &forge.WorkflowRun{ID: 4, Name: "Fullsend"},
		Logf:        func(string, ...any) {},
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	_, err := os.ReadFile(filepath.Join(artifactDir, "debug-fullsend-run-4", "workflow-logs.txt"))
	require.NoError(t, err)
	assert.Contains(t, readFailureSummary(t, artifactDir), "does not implement ci.RunLister")
}

func TestCollectFailureLogs_NoRunsFound(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	w := &world.World{Org: "org", RepoName: "repo", CI: &fakeListerCI{}, Logf: func(string, ...any) {}}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("creating issue: 502"))

	summary := readFailureSummary(t, artifactDir)
	assert.Contains(t, summary, "creating issue: 502")
	assert.Contains(t, summary, "no workflow run was found")
}

func TestCollectFailureLogs_NoRepository(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	w := &world.World{ScenarioName: "setup fails", Logf: func(string, ...any) {}}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("allocating repo: pool exhausted"))

	summary := readFailureSummary(t, artifactDir)
	assert.Contains(t, summary, "allocating repo: pool exhausted")
	assert.Contains(t, summary, "Repository: (none)")
	assert.Contains(t, summary, "repository identity is unavailable")
	assert.NotContains(t, summary, "there are no workflow runs")
}

func TestCollectFailureLogs_NoCIDriver(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	w := &world.World{Org: "org", RepoName: "repo", Logf: func(string, ...any) {}}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	assert.Contains(t, readFailureSummary(t, artifactDir), "no CI driver is configured")
}

func TestCollectFailureLogs_SkipsRunsBeforeScenario(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	begin := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeListerCI{
		runs: []forge.WorkflowRun{
			{ID: 20, Name: "Fullsend", CreatedAt: begin.Add(time.Minute).Format(time.RFC3339)},
			// Within the clock-skew buffer: kept.
			{ID: 19, Name: "Fullsend", CreatedAt: begin.Add(-10 * time.Second).Format(time.RFC3339)},
			// Unparsable creation time: kept rather than dropped.
			{ID: 18, Name: "Fullsend", CreatedAt: "not-a-time"},
			// A previous scenario's run on a long-lived repository: skipped.
			{ID: 17, Name: "Fullsend", CreatedAt: begin.Add(-time.Hour).Format(time.RFC3339)},
		},
		logs: map[int]string{20: "a", 19: "b", 18: "c", 17: "d"},
	}
	w := &world.World{
		Org: "org", RepoName: "repo", CI: fake,
		ScenarioBegin: begin,
		Logf:          func(string, ...any) {},
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	assert.ElementsMatch(t, []int{20, 19, 18}, fake.fetched)
	assert.NotContains(t, readFailureSummary(t, artifactDir), "run 17")
}

// TestCollectFailureLogs_RedactsSummaryAndLogs checks that the summary
// (which embeds the scenario error) and collected logs are redacted.
func TestCollectFailureLogs_RedactsSummaryAndLogs(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	token := "ghp_" + strings.Repeat("B", 36)
	fake := &fakeListerCI{
		runs: []forge.WorkflowRun{{ID: 6, Name: "Fullsend"}},
		logs: map[int]string{6: "GH_TOKEN=" + token},
	}
	w := &world.World{Org: "org", RepoName: "repo", CI: fake, Logf: func(string, ...any) {}}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("request failed with token %s", token))

	assert.NotContains(t, readFailureSummary(t, artifactDir), token)
	logPath := filepath.Join(artifactDir, "debug-fullsend-run-6", "workflow-logs.txt")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), token)
	info, err := os.Stat(logPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestCollectFailureLogs_SameScenarioNameDoesNotOverwrite(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	w := &world.World{ScenarioName: "outline row", Logf: func(string, ...any) {}}
	CollectFailureLogs(context.Background(), w, fmt.Errorf("first"))
	CollectFailureLogs(context.Background(), w, fmt.Errorf("second"))

	matches, err := filepath.Glob(filepath.Join(artifactDir, "debug-scenario-outline-row-*", failureSummaryFile))
	require.NoError(t, err)
	assert.Len(t, matches, 2)
}

func TestPathSlug(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"Fullsend", "fullsend"},
		{"Triage Agent / run #3", "triage-agent-run-3"},
		{"../../etc", "etc"},
		{"", "fallback"},
		{"!!!", "fallback"},
		{strings.Repeat("a", 50), strings.Repeat("a", 40)},
		// Truncation that ends on a separator does not leave a trailing '-'.
		{strings.Repeat("abc ", 12), strings.TrimSuffix(strings.Repeat("abc-", 10), "-")},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, pathSlug(tt.in, 40, "fallback"), "pathSlug(%q)", tt.in)
	}
}

// TestSaveWorkflowRunLogs_PartialSnapshotNotDeduplicated checks that a log
// fetch that succeeds but is incomplete (run still in progress, or a job's
// log replaced by a fetch-failure note) is not recorded as saved, so the
// After hook collects the run again.
func TestSaveWorkflowRunLogs_PartialSnapshotNotDeduplicated(t *testing.T) {
	tests := []struct {
		name string
		run  forge.WorkflowRun
		logs string
	}{
		{"run not terminal", forge.WorkflowRun{ID: 7, Status: "in_progress"}, "=== job ==="},
		{"queued run", forge.WorkflowRun{ID: 7, Status: "queued"}, "=== job ==="},
		{"job fetch failed", forge.WorkflowRun{ID: 7, Status: "completed"}, "=== job ===\n[failed to fetch logs: boom]\n"},
		{"job unavailable", forge.WorkflowRun{ID: 7, Status: "completed"}, "[logs unavailable: HTTP 404]"},
		{"job read failed", forge.WorkflowRun{ID: 7, Status: "completed"}, "[failed to read logs: EOF]"},
		{"gitlab trace fetch failed", forge.WorkflowRun{ID: 7, Status: "completed"}, "=== Job 1 (x): error fetching trace: boom ==="},
		{"gitlab trace read failed", forge.WorkflowRun{ID: 7, Status: "completed"}, "=== Job 1 (x): error reading trace: boom ==="},
		{"gitlab aggregate limit", forge.WorkflowRun{ID: 7, Status: "completed"}, "=== aggregate trace limit (1 bytes) reached ==="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			artifactDir := t.TempDir()
			t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)
			w := &world.World{Org: "org", RepoName: "repo", CI: &fakeDebugCI{logs: tt.logs}, Logf: func(string, ...any) {}}

			run := tt.run
			saveWorkflowRunLogs(context.Background(), w, "triage", &run)

			_, err := os.Stat(filepath.Join(artifactDir, "debug-triage-run-7", "workflow-logs.txt"))
			require.NoError(t, err, "the partial snapshot is still preserved")
			assert.False(t, w.SavedLogRunIDs[7], "a partial snapshot must not be recorded as saved")
		})
	}
}

// TestCollectFailureLogs_RefetchesPartialSnapshot checks the After hook
// replaces an early, partial snapshot with a complete one.
func TestCollectFailureLogs_RefetchesPartialSnapshot(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fake := &fakeListerCI{
		runs: []forge.WorkflowRun{{ID: 9, Name: "Fullsend", Status: "completed", Conclusion: "failure"}},
		logs: map[int]string{9: "=== complete logs ==="},
	}
	w := &world.World{
		Org: "org", RepoName: "repo", CI: fake,
		ScenarioName: "partial",
		Logf:         func(string, ...any) {},
	}

	// Early fetch while the run is still in progress, with a stale run value.
	early := forge.WorkflowRun{ID: 9, Name: "Fullsend", Status: "in_progress"}
	fake.logs[9] = "=== partial logs ===\n[failed to fetch logs: boom]\n"
	saveWorkflowRunLogs(context.Background(), w, "fullsend", &early)
	require.False(t, w.SavedLogRunIDs[9])

	fake.logs[9] = "=== complete logs ==="
	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	data, err := os.ReadFile(filepath.Join(artifactDir, "debug-fullsend-run-9", "workflow-logs.txt"))
	require.NoError(t, err)
	assert.Equal(t, "=== complete logs ===", string(data), "the After hook must replace the partial snapshot")
	assert.Equal(t, []int{9, 9}, fake.fetched)
	assert.Contains(t, readFailureSummary(t, artifactDir), "logs saved to")
}

// TestLogRedacted checks diagnostic console messages are redacted after
// formatting, and a nil logger is tolerated.
func TestLogRedacted(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("C", 36)
	var logged []string
	w := &world.World{Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }}

	logRedacted(w, "collect failure logs: %v for %q", fmt.Errorf("request failed with token %s", token), "scenario "+token)

	require.Len(t, logged, 1)
	assert.NotContains(t, logged[0], token)
	assert.Contains(t, logged[0], "collect failure logs")

	assert.NotPanics(t, func() { logRedacted(&world.World{}, "no logger %s", token) })
}

// TestCollectFailureLogs_RedactsConsoleLogs checks that errors, scenario
// names and summary paths reach World.Logf redacted.
func TestCollectFailureLogs_RedactsConsoleLogs(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	key := "sk-" + strings.Repeat("a", 24)
	token := "ghp_" + strings.Repeat("D", 36)
	fake := &fakeListerCI{
		runs:    []forge.WorkflowRun{{ID: 12, Name: "Fullsend"}},
		logsErr: map[int]error{12: fmt.Errorf("fetch failed with token %s", token)},
	}
	var logged []string
	w := &world.World{
		Org: "org", RepoName: "repo", CI: fake,
		ScenarioName: "scenario " + key,
		Logf:         func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	require.NotEmpty(t, logged)
	for _, line := range logged {
		assert.NotContains(t, line, token)
		assert.NotContains(t, line, key)
	}
}

// TestCollectFailureLogs_RedactsDirectoryNames checks scenario and workflow
// names are redacted before they become artifact directory names.
func TestCollectFailureLogs_RedactsDirectoryNames(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	key := "sk-" + strings.Repeat("a", 24)
	fake := &fakeListerCI{
		runs: []forge.WorkflowRun{{ID: 13, Name: "run " + key}},
		logs: map[int]string{13: "logs"},
	}
	w := &world.World{
		Org: "org", RepoName: "repo", CI: fake,
		ScenarioName: "scenario " + key,
		Logf:         func(string, ...any) {},
	}

	CollectFailureLogs(context.Background(), w, fmt.Errorf("boom"))

	entries, err := os.ReadDir(artifactDir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), strings.Repeat("a", 24), "directory name must not carry the secret")
	}
}
