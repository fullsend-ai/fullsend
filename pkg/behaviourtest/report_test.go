//go:build behaviour || playback

package behaviourtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Every test clears all three destination variables: the two report
// destinations under assertion, and GITHUB_STEP_SUMMARY because GitHub
// Actions exports it for every step, so a test running in CI would
// otherwise append to the real job summary. The t.Setenv calls also make
// these tests serial (never t.Parallel).

// clearReportDestinations unsets the artifact dir, the failure-class
// file, and the step summary, so a call writes nothing unless a test
// sets a destination explicitly.
func clearReportDestinations(t *testing.T) {
	t.Helper()
	t.Setenv(artifactDirEnv, "")
	t.Setenv(failureClassFileEnv, "")
	t.Setenv(stepSummaryEnv, "")
}

// reportWorld builds a World whose recorder holds one recorded failure
// per class argument, the way the After hook and failRun record: source
// and class explicit, message already redacted by the caller.
func reportWorld(classes ...behaviourfail.Class) *world.World {
	rec := &behaviourfail.Recorder{}
	for i, class := range classes {
		rec.RecordFailure("scenario", fmt.Sprintf("scenario %d", i), class, fmt.Sprintf("message %d", i))
	}
	return &world.World{Failures: rec}
}

func TestReportRunClassAllInfrastructure(t *testing.T) {
	clearReportDestinations(t)
	w := reportWorld(behaviourfail.Infrastructure, behaviourfail.Infrastructure)

	report, path := writeCompletionReport(w, "behaviour", nil, 1)
	assert.Equal(t, behaviourfail.Infrastructure, report.Class, "all-infrastructure runs classify infrastructure")
	assert.Equal(t, 2, report.InfrastructureFailures)
	assert.Equal(t, 0, report.ScenarioFailures)
	assert.Len(t, report.Failures, 2)
	assert.Empty(t, path, "no destination configured, so no report file path is returned")
}

func TestReportRunClassMixedIsScenario(t *testing.T) {
	clearReportDestinations(t)
	w := reportWorld(behaviourfail.Infrastructure, behaviourfail.Scenario)

	report, _ := writeCompletionReport(w, "behaviour", nil, 1)
	assert.Equal(t, behaviourfail.Scenario, report.Class, "mixed runs stay scenario-classified so they are never retried")
	assert.Equal(t, 1, report.InfrastructureFailures)
	assert.Equal(t, 1, report.ScenarioFailures)
}

func TestReportRunClassAllScenario(t *testing.T) {
	clearReportDestinations(t)
	w := reportWorld(behaviourfail.Scenario)

	report, _ := writeCompletionReport(w, "behaviour", nil, 1)
	assert.Equal(t, behaviourfail.Scenario, report.Class)
	assert.Equal(t, 0, report.InfrastructureFailures)
	assert.Equal(t, 1, report.ScenarioFailures)
}

func TestReportRunClassCleanRunIsSuccess(t *testing.T) {
	clearReportDestinations(t)
	w := reportWorld()

	report, _ := writeCompletionReport(w, "playback", nil, 0)
	assert.Equal(t, behaviourfail.Success, report.Class, "a clean run reports success so the wrapper sees a fresh, non-retried marker")
	assert.Equal(t, behaviourfail.Report{
		Class:    behaviourfail.Success,
		Failures: []behaviourfail.FailureRecord{},
	}, report)
}

func TestReportRunClassFailedRunWithoutRecordsIsScenario(t *testing.T) {
	clearReportDestinations(t)
	w := reportWorld()

	report, _ := writeCompletionReport(w, "behaviour", nil, 3)
	assert.Equal(t, behaviourfail.Scenario, report.Class, "a failed run with no recorded failures never retries")

	report, _ = writeCompletionReport(w, "behaviour", errors.New("suite runner exploded"), 0)
	assert.Equal(t, behaviourfail.Scenario, report.Class, "a run error with no recorded failures never retries")
}

func TestReportNilWorldReportsCleanCounts(t *testing.T) {
	clearReportDestinations(t)

	report, path := writeCompletionReport(nil, "behaviour", nil, 0)
	assert.Equal(t, behaviourfail.Success, report.Class)
	assert.Empty(t, report.Failures)
	assert.Empty(t, path)
}

func TestReportWritesFailureClassJSONToArtifactDir(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv(artifactDirEnv, artifactDir)
	t.Setenv(failureClassFileEnv, "")
	t.Setenv(stepSummaryEnv, "")

	w := reportWorld(behaviourfail.Infrastructure)

	report, path := writeCompletionReport(w, "behaviour", nil, 1)
	require.Equal(t, behaviourfail.Infrastructure, report.Class)
	require.Equal(t, filepath.Join(artifactDir, failureClassFile), path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	// The documented shape (design D5):
	// {class, counts, failures:[{source, scenario, class, message}]}.
	// Pin the wire field names through a raw map so a JSON-tag rename
	// cannot drift silently.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "infrastructure", raw["class"])
	assert.Equal(t, float64(1), raw["infrastructure_failures"])
	assert.Equal(t, float64(0), raw["scenario_failures"])
	failures, ok := raw["failures"].([]any)
	require.True(t, ok, "failures must be a JSON array, not null")
	require.Len(t, failures, 1)
	first, ok := failures[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "scenario", first["source"])
	assert.Equal(t, "scenario 0", first["scenario"])
	assert.Equal(t, "infrastructure", first["class"])
	assert.Equal(t, "message 0", first["message"])

	var roundTrip behaviourfail.Report
	require.NoError(t, json.Unmarshal(data, &roundTrip))
	assert.Equal(t, report, roundTrip)

	// Mode 0o600, like the other artifact files, and no leftover temp
	// files in the uploaded directory.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	entries, err := os.ReadDir(artifactDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only failure-class.json may remain in the artifact dir")
	assert.Equal(t, failureClassFile, entries[0].Name())
}

func TestReportWritesFailureClassFile(t *testing.T) {
	classFile := filepath.Join(t.TempDir(), "failure-class.json")
	t.Setenv(artifactDirEnv, "")
	t.Setenv(failureClassFileEnv, classFile)
	t.Setenv(stepSummaryEnv, "")

	w := reportWorld()

	report, path := writeCompletionReport(w, "playback", nil, 0)
	require.Equal(t, behaviourfail.Success, report.Class)
	require.Equal(t, classFile, path, "the failure-class file is the report path when no artifact dir is set")

	data, err := os.ReadFile(classFile)
	require.NoError(t, err)
	var roundTrip behaviourfail.Report
	require.NoError(t, json.Unmarshal(data, &roundTrip))
	assert.Equal(t, behaviourfail.Success, roundTrip.Class)
	assert.Empty(t, roundTrip.Failures, "a clean run still writes a fresh success marker")

	info, err := os.Stat(classFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestReportWritesBothDestinationsAndPrefersArtifactDir(t *testing.T) {
	artifactDir := t.TempDir()
	classFile := filepath.Join(t.TempDir(), "failure-class.json")
	t.Setenv(artifactDirEnv, artifactDir)
	t.Setenv(failureClassFileEnv, classFile)
	t.Setenv(stepSummaryEnv, "")

	w := reportWorld(behaviourfail.Scenario)

	report, path := writeCompletionReport(w, "behaviour", nil, 1)
	require.Equal(t, behaviourfail.Scenario, report.Class)
	require.Equal(t, filepath.Join(artifactDir, failureClassFile), path, "the artifact-dir report is the primary path")

	for _, p := range []string{path, classFile} {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		var roundTrip behaviourfail.Report
		require.NoError(t, json.Unmarshal(data, &roundTrip))
		assert.Equal(t, report, roundTrip, "both destinations carry the same report")
	}
}

func TestReportWritesNothingWithoutDestinations(t *testing.T) {
	clearReportDestinations(t)
	w := reportWorld(behaviourfail.Infrastructure)

	report, path := writeCompletionReport(w, "behaviour", nil, 1)
	assert.Equal(t, behaviourfail.Infrastructure, report.Class)
	assert.Empty(t, path, "neither report destination set: nothing is written")
}

func TestReportStepSummaryCarriesOnlyLabelsCountsAndSuite(t *testing.T) {
	summaryFile := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv(artifactDirEnv, "")
	t.Setenv(failureClassFileEnv, "")
	t.Setenv(stepSummaryEnv, summaryFile)

	// Hostile record content: forge-supplied scenario names and
	// error-string messages must never reach the summary (design D5's
	// boundary — markdown injection from external text).
	rec := &behaviourfail.Recorder{}
	rec.RecordFailure("scenario", "INJECTED-SCENARIO-NAME <img src=x>", behaviourfail.Infrastructure,
		"INJECTED-MESSAGE ghp_0123456789abcdefghijklmnopqrstuvwxyz")
	rec.RecordFailure("scenario", "another scenario", behaviourfail.Scenario,
		"INJECTED-MESSAGE **bold** [link](https://example.com)")
	w := &world.World{Failures: rec}

	report, _ := writeCompletionReport(w, "behaviour", nil, 1)
	require.Equal(t, behaviourfail.Scenario, report.Class)

	data, err := os.ReadFile(summaryFile)
	require.NoError(t, err)
	summary := string(data)
	assert.Contains(t, summary, "## behaviour suite result")
	assert.Contains(t, summary, "- Failure class: scenario")
	assert.Contains(t, summary, "- Infrastructure failures: 1")
	assert.Contains(t, summary, "- Scenario failures: 1")
	assert.NotContains(t, summary, "INJECTED-SCENARIO-NAME", "scenario names never reach the summary")
	assert.NotContains(t, summary, "INJECTED-MESSAGE", "failure messages never reach the summary")
	assert.NotContains(t, summary, "<img", "markdown from error text never reaches the summary")
	assert.NotContains(t, summary, "ghp_", "credentials never reach the summary")
	assert.NotContains(t, summary, "https://example.com", "links from error text never reach the summary")
}

func TestReportStepSummaryWriteFailureIsBestEffort(t *testing.T) {
	// The summary parent directory does not exist, so the append fails;
	// the run's report must still be returned and nothing may panic.
	t.Setenv(artifactDirEnv, "")
	t.Setenv(failureClassFileEnv, "")
	t.Setenv(stepSummaryEnv, filepath.Join(t.TempDir(), "missing-dir", "summary.md"))

	w := reportWorld(behaviourfail.Infrastructure)
	var logged []string
	w.Logf = func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	report, path := writeCompletionReport(w, "behaviour", nil, 1)
	assert.Equal(t, behaviourfail.Infrastructure, report.Class, "a summary write failure never changes the run's class")
	assert.Empty(t, path)
	require.Len(t, logged, 1, "the summary write failure is logged once")
	assert.Contains(t, logged[0], "step summary")
}

func TestReportArtifactWriteFailureIsBestEffort(t *testing.T) {
	// A file where the artifact directory should be makes every write
	// under it fail; the report is still returned and the failure logged.
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("occupied"), 0o600))
	t.Setenv(artifactDirEnv, filepath.Join(blocker, "nested"))
	t.Setenv(failureClassFileEnv, "")
	t.Setenv(stepSummaryEnv, "")

	w := reportWorld(behaviourfail.Infrastructure)
	var logged []string
	w.Logf = func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	report, path := writeCompletionReport(w, "behaviour", nil, 1)
	assert.Equal(t, behaviourfail.Infrastructure, report.Class, "a report write failure never changes the run's class")
	assert.Empty(t, path)
	require.Len(t, logged, 1, "the artifact write failure is logged once")
	assert.Contains(t, logged[0], "write")
}

func TestReportWriteFailuresTolerateNilLogger(t *testing.T) {
	// A World without a logger must not panic when both the artifact-dir
	// write and the summary append fail — the log line is diagnostics
	// only, so a nil Logf suppresses it and nothing else.
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("occupied"), 0o600))
	t.Setenv(artifactDirEnv, filepath.Join(blocker, "nested"))
	t.Setenv(failureClassFileEnv, "")
	t.Setenv(stepSummaryEnv, filepath.Join(t.TempDir(), "missing-dir", "summary.md"))

	w := reportWorld(behaviourfail.Infrastructure)
	assert.NotPanics(t, func() {
		report, path := writeCompletionReport(w, "behaviour", nil, 1)
		assert.Equal(t, behaviourfail.Infrastructure, report.Class)
		assert.Empty(t, path)
	})
}

func TestReportWriteReportFileFailsOnUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks do not bind for root")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // let t.TempDir's cleanup remove it

	err := writeReportFile(filepath.Join(dir, failureClassFile), "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write ")
}

func TestReportWriteReportFileCleansUpAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, failureClassFile)
	// A directory where the report file should land: the temp-file write
	// succeeds but the rename over the target fails.
	require.NoError(t, os.Mkdir(target, 0o700))

	err := writeReportFile(target, "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write ")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the failed rename removed its temp file; only the blocking directory remains")
	assert.Equal(t, failureClassFile, entries[0].Name())
}

func TestReportSanitizeSummaryText(t *testing.T) {
	assert.Equal(t, "behaviour", sanitizeSummaryText("behaviour"))
	assert.Equal(t, "play back", sanitizeSummaryText("play\nback"), "newlines cannot start a new summary line")
	assert.Equal(t, "a b", sanitizeSummaryText("a\tb"), "tabs cannot shift the markdown structure")
}

func TestReportRecordBootstrapFailure(t *testing.T) {
	t.Run("classifies and records with source bootstrap", func(t *testing.T) {
		w := &world.World{Failures: &behaviourfail.Recorder{}}
		err := behaviourfail.Mark(errors.New("repo acme/widget not visible after 30 attempts"), "setup: repo never visible")

		returned := recordBootstrapFailure(w, err)
		assert.Same(t, w, returned, "a non-nil World is returned as-is")

		snapshot := w.Failures.Snapshot()
		require.Len(t, snapshot, 1)
		assert.Equal(t, bootstrapSource, snapshot[0].Source)
		assert.Empty(t, snapshot[0].Scenario, "bootstrap failures happen before any scenario runs")
		assert.Equal(t, behaviourfail.Infrastructure, snapshot[0].Class, "a marked error classifies infrastructure")
		assert.Equal(t, err.Error(), snapshot[0].Message, "the message is recorded verbatim when nothing needs redaction")
	})

	t.Run("plain errors classify scenario", func(t *testing.T) {
		w := recordBootstrapFailure(nil, errors.New("invalid behaviour runner config: ENVIRONMENT"))

		snapshot := w.Failures.Snapshot()
		require.Len(t, snapshot, 1)
		assert.Equal(t, behaviourfail.Scenario, snapshot[0].Class, "config-validation errors stay scenario — the conservative default")
	})

	t.Run("redacts the message at record time", func(t *testing.T) {
		w := recordBootstrapFailure(nil, errors.New("minting token: ghp_0123456789abcdefghijklmnopqrstuvwxyz"))

		snapshot := w.Failures.Snapshot()
		require.Len(t, snapshot, 1)
		assert.NotContains(t, snapshot[0].Message, "ghp_0123456789abcdefghijklmnopqrstuvwxyz", "pattern-shaped credentials are masked before recording")
		assert.Contains(t, snapshot[0].Message, "ghp_...", "the redactor's masked form is recorded")
	})

	t.Run("nil world gets a recorder so the failure reaches the report", func(t *testing.T) {
		w := recordBootstrapFailure(nil, errors.New("creating install driver"))
		require.NotNil(t, w)
		require.NotNil(t, w.Failures)
		assert.Len(t, w.Failures.Snapshot(), 1)
	})

	t.Run("nil recorder is installed", func(t *testing.T) {
		w := &world.World{}

		returned := recordBootstrapFailure(w, errors.New("creating install driver"))
		assert.Same(t, w, returned)
		require.NotNil(t, w.Failures)
		assert.Len(t, w.Failures.Snapshot(), 1)
	})
}

func TestReportBootstrapFailureMessage(t *testing.T) {
	msg := bootstrapFailureMessage("behaviour", behaviourfail.Infrastructure,
		errors.New("minting a token for any pool org"), "/tmp/artifacts/failure-class.json")
	assert.Contains(t, msg, "behaviour suite failed during bootstrap")
	assert.Contains(t, msg, "infrastructure failure", "the class must appear in the final test output")
	assert.Contains(t, msg, "minting a token for any pool org")
	assert.Contains(t, msg, "report: /tmp/artifacts/failure-class.json")

	msg = bootstrapFailureMessage("playback", behaviourfail.Scenario, errors.New("invalid config"), "")
	assert.Contains(t, msg, "playback suite failed during bootstrap")
	assert.Contains(t, msg, "scenario failure")
	assert.NotContains(t, msg, "report:", "no report-path suffix when nothing was written")
}
