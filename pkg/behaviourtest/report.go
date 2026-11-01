//go:build behaviour || playback

package behaviourtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/steps"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Both suite runners (RunSuite, build tag `behaviour`, and
// RunPlaybackSuite, build tag `playback`) compile this file; with neither
// tag set it is excluded from the public build, like run.go and
// run_playback.go themselves.

// failureClassFile is the completion report's file name under
// BEHAVIOUR_ARTIFACT_DIR. The infra-retry wrapper script resolves this
// same path — or BEHAVIOUR_FAILURE_CLASS_FILE for local runs — to decide
// whether a failed run may be retried once (design D4).
const failureClassFile = "failure-class.json"

// bootstrapSource labels failures recorded before godog runs (design
// D2): config validation, driver creation, actor resolution, pool token
// minting. It pairs with the "scenario" source the suite's After hook
// records.
const bootstrapSource = "bootstrap"

// artifactDirEnv names the directory CI uploads after redaction; the
// completion report lands there as failure-class.json when it is set.
const artifactDirEnv = "BEHAVIOUR_ARTIFACT_DIR"

// failureClassFileEnv names a direct report path for local runs. The
// infra-retry wrapper exports it (mktemp) when no artifact dir is set, so
// the retry decision works without flipping the suite's "artifact dir
// unset ⇒ skip debug artifacts" behavior (design D4).
const failureClassFileEnv = "BEHAVIOUR_FAILURE_CLASS_FILE"

// stepSummaryEnv names GitHub Actions' job-summary file; the runner
// appends its labelled section when Actions makes the variable available
// to the `make behaviour-test` step's child processes (design D5).
const stepSummaryEnv = "GITHUB_STEP_SUMMARY"

// failRun handles a bootstrap-phase suite failure for both runners: it
// classifies err, records it on the run's shared failure recorder
// (source "bootstrap"), writes the completion report, and fails the test
// with the class in the message, so the final test output carries the
// label (design D2/D5).
//
// w is the template World, or nil when bootstrap failed before one
// existed (config validation, driver creation, pool token minting) — the
// failure is recorded on a fresh recorder either way so it reaches the
// completion report. suite names the runner ("behaviour" or "playback")
// for the report and the CI summary. err must be non-nil.
func failRun(t *testing.T, w *world.World, suite string, err error) {
	t.Helper()
	w = recordBootstrapFailure(w, err)
	// Status 1: godog never ran, so there is no suite status to report —
	// only that the run failed before it could start.
	report, reportPath := writeCompletionReport(w, suite, err, 1)
	t.Fatalf("%s", bootstrapFailureMessage(suite, report.Class, err, reportPath))
}

// recordBootstrapFailure classifies err and records it on the run's
// failure recorder with source "bootstrap" and an empty scenario name
// (bootstrap failures happen before any scenario runs), mirroring
// suite.recordScenarioFailure for the After hook. The message is redacted
// at record time — the completion report renders recorded messages
// verbatim, so this is the only redaction they get — and the World's
// credential literals are registered first, so a bootstrap failure
// recorded before any debug-log collection still masks an opaque token
// that no pattern recognizes.
//
// w may be nil, or carry a nil recorder, when bootstrap failed before the
// template World existed; a fresh recorder is installed in that case so
// the failure always reaches the completion report. Dropping it instead
// (relying on the recorder's nil-safety) would silently re-classify an
// infrastructure bootstrap failure as scenario and cost the run its one
// retry. The returned World is never nil; when w was non-nil it is
// returned as-is.
func recordBootstrapFailure(w *world.World, err error) *world.World {
	class := failclass.Classify(err)
	if w == nil {
		w = &world.World{}
	}
	if w.Failures == nil {
		// Safe only because failRun runs before godog: no scenario clone
		// shares this World yet, so installing the recorder cannot split
		// records across two recorders mid-run.
		w.Failures = &behaviourfail.Recorder{}
	}
	steps.RegisterRedactionLiterals(w)
	w.Failures.RecordFailure(bootstrapSource, "", class, steps.RedactText(err.Error()))
	return w
}

// bootstrapFailureMessage renders failRun's terminal message: the suite
// name, the run class, the redacted error, and the report path when one
// was written. The class must appear in the final test output (design
// D5) so a red check names the failure mode. Redaction is idempotent, so
// re-running the source error through RedactText here (it was already
// redacted at record time) cannot double-mask.
func bootstrapFailureMessage(suite string, class behaviourfail.Class, err error, reportPath string) string {
	msg := fmt.Sprintf("%s suite failed during bootstrap: %s failure: %s", suite, class, steps.RedactText(err.Error()))
	if reportPath != "" {
		msg += "; report: " + reportPath
	}
	return msg
}

// writeCompletionReport builds the run's completion report and writes it
// to every configured destination (design D5):
//
//   - failure-class.json under BEHAVIOUR_ARTIFACT_DIR, when set — the
//     artifact CI uploads, written mode 0o600 through a fresh temp file
//     renamed over the target (writeRedactedArtifact-style, so the mode
//     of a pre-existing file cannot leak through and a pre-existing
//     symlink at the path is replaced, not followed). The report's
//     messages were redacted at record time and are written verbatim —
//     no re-redaction here.
//   - the path in BEHAVIOUR_FAILURE_CLASS_FILE, when set — the local-run
//     fallback the infra-retry wrapper reads when no artifact dir is
//     configured.
//   - a short markdown section appended to $GITHUB_STEP_SUMMARY, when
//     set. The section carries ONLY the class label, the per-class
//     counts and the suite name — never failure messages or scenario
//     names, which are forge- and error-supplied text and a
//     markdown-injection surface. The report artifact carries the
//     detail; the summary carries the label.
//
// The run class is derived from the recorded failures and the run
// outcome: scenario when any recorded failure is scenario-classified
// (mixed runs stay scenario, so they never retry); infrastructure when at
// least one failure was recorded and none are scenario; success only for
// a clean run. A failed run (runErr non-nil, or status — the suite
// runner's exit status — nonzero) with no recorded failures classifies
// scenario: unexplained failures are never retried, the conservative
// default of design D1.
//
// It returns the report and the path of the report file written — the
// artifact-dir path when that destination is configured, otherwise the
// BEHAVIOUR_FAILURE_CLASS_FILE path — or "" when neither is set. All
// writes are best-effort: a failure is logged through w.Logf (when set)
// and never fails the run, because a missing report reads as
// "unclassified, do not retry" to the wrapper — the safe direction.
func writeCompletionReport(w *world.World, suite string, runErr error, status int) (behaviourfail.Report, string) {
	var rec *behaviourfail.Recorder
	var logf func(string, ...any)
	if w != nil {
		rec, logf = w.Failures, w.Logf
	}
	report := rec.Report(runClass(rec, runErr, status))

	// MarshalIndent cannot fail for this shape (only strings, ints and
	// slices of structs), but a defensive branch beats dropping the
	// report silently if that ever changes.
	content := ""
	if data, err := json.MarshalIndent(report, "", "  "); err != nil {
		logReportIssue(logf, "marshalling failure-class report: %v", err)
	} else {
		content = string(data) + "\n"
	}

	path := ""
	if content != "" {
		if dir := strings.TrimSpace(os.Getenv(artifactDirEnv)); dir != "" {
			p := filepath.Join(dir, failureClassFile)
			if err := writeReportFile(p, content); err != nil {
				logReportIssue(logf, "%v", err)
			} else {
				path = p
			}
		}
		if file := strings.TrimSpace(os.Getenv(failureClassFileEnv)); file != "" {
			if err := writeReportFile(file, content); err != nil {
				logReportIssue(logf, "%v", err)
			} else if path == "" {
				path = file
			}
		}
	}
	if summary := strings.TrimSpace(os.Getenv(stepSummaryEnv)); summary != "" {
		appendStepSummary(summary, suite, report, logf)
	}
	return report, path
}

// runClass derives the run classification (design D2) from the recorded
// failures and the run outcome: scenario when any recorded failure is
// scenario-classified — mixed runs stay scenario so they are never
// retried; infrastructure when at least one failure was recorded and
// none are scenario; success only for a clean run. A failed run with no
// recorded failures (a godog-level failure the After hook never saw)
// classifies scenario — the conservative default that keeps unexplained
// failures from triggering the single automatic retry.
func runClass(rec *behaviourfail.Recorder, runErr error, status int) behaviourfail.Class {
	infrastructure, scenario := rec.Counts()
	switch {
	case scenario > 0:
		return behaviourfail.Scenario
	case infrastructure > 0:
		return behaviourfail.Infrastructure
	case runErr != nil || status != 0:
		return behaviourfail.Scenario
	}
	return behaviourfail.Success
}

// appendStepSummary appends the run's markdown section to GitHub's
// step-summary file. Design D5's boundary: the section carries ONLY the
// class label, the per-class counts and the suite name — never failure
// messages or scenario names, which come from external error text and
// are a markdown-injection surface. The write is best-effort: a failure
// is logged through logf and never fails the run.
func appendStepSummary(path, suite string, report behaviourfail.Report, logf func(string, ...any)) {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s suite result\n\n", sanitizeSummaryText(suite))
	fmt.Fprintf(&b, "- Failure class: %s\n", sanitizeSummaryText(string(report.Class)))
	fmt.Fprintf(&b, "- Infrastructure failures: %d\n", report.InfrastructureFailures)
	fmt.Fprintf(&b, "- Scenario failures: %d\n\n", report.ScenarioFailures)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		logReportIssue(logf, "appending failure-class summary to step summary: %v", err)
		return
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		logReportIssue(logf, "appending failure-class summary to step summary: %v", err)
		return
	}
	if err := f.Close(); err != nil {
		logReportIssue(logf, "appending failure-class summary to step summary: %v", err)
	}
}

// sanitizeSummaryText collapses control characters — newlines above all
// — to spaces so the suite name cannot break out of the summary
// section's markdown structure. The suite names are package constants
// today ("behaviour", "playback"); the guard keeps the summary's content
// boundary independent of what future callers pass.
func sanitizeSummaryText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// writeReportFile writes content to path as a fresh private (0o600)
// regular file, the way steps.writeRedactedArtifact writes debug
// artifacts: content goes to a new temp file in the same directory that
// is renamed over path, so the mode of a pre-existing file cannot leak
// through and a pre-existing symlink at path is replaced rather than
// followed. Unlike that helper it does not run the secret redactor — the
// report's messages were redacted at record time and must not be
// re-redacted.
func writeReportFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, ".report-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.WriteString(content)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// logReportIssue reports a best-effort report-writing failure through
// the world logger when one is available. Report writing never fails the
// run — a missing report reads as "unclassified" to the retry wrapper,
// the safe direction — so the log line is diagnostics, not a failure. A
// nil logf (no World, or a World built without one) suppresses only the
// log line.
func logReportIssue(logf func(string, ...any), format string, args ...any) {
	if logf == nil {
		return
	}
	logf(format, args...)
}
