package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// logFetchTimeout bounds the GetRunLogs API call so a hanging request
// does not block the scenario step indefinitely.
const logFetchTimeout = 30 * time.Second

const (
	// maxFailureLogRuns caps how many not-yet-saved workflow runs
	// CollectFailureLogs fetches logs for. A scenario dispatches a handful
	// of runs; the cap keeps a pathological repository from stretching the
	// After hook. It applies to fetches, not to the listing, so runs whose
	// logs were already saved do not use it up.
	maxFailureLogRuns = 10

	// maxListedFailureRuns bounds the listing of the repository's most
	// recent workflow runs. Listing is one cheap API call, so it is larger
	// than the fetch cap; a listing that fills it may be truncated and the
	// summary says so.
	maxListedFailureRuns = 50

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

// dispatchDiagnosticsGrace is how long CollectFailureLogs waits, after a
// scenario failed because a workflow run never appeared, before listing
// the repository's runs. A run that shows up during the grace period
// measures a late dispatch; no run at all points to a lost event (#9469).
// The diagnostics run only for a *ci.NotDispatchedError, which only the
// GitHub Actions driver returns, so this applies to GitHub Actions only.
// It runs outside failureLogBudget so it cannot starve the log collection
// that follows, and is bounded per process by dispatchGraceBudget. It is
// skipped when no verdict can be computed (the CI driver is not a
// ci.RunLister, or the error does not name the workflow). A variable so
// tests can shorten it.
var dispatchDiagnosticsGrace = 2 * time.Minute

// dispatchGraceBudgetTotal bounds the total grace wait time per test
// process: the length of the union of the wall-clock intervals the waits
// cover, so overlapping waits add no wall time. It is a union bound
// rather than a fixed cap on waits or a single shared window: a fixed cap
// starves a burst (12 scenarios timing out together, only the first few
// wait), and a single window starves a later isolated failure, which is
// the evidence #9469 needs. Once the budget is spent, a new wait ends
// where its uncovered time would exceed the budget, or is skipped if none
// is left; a skipped scenario still lists the repository. A wait cut
// short keeps its reservation; in practice a cancelled wait means the
// test process is shutting down.
const dispatchGraceBudgetTotal = 6 * time.Minute

// dispatchGraceBudget is the budget shared by every scenario in the
// process. A variable so tests can replace it.
var dispatchGraceBudget = newGraceBudget(dispatchGraceBudgetTotal)

// dispatchNow and dispatchAfter are the clock the grace wait uses;
// variables so tests can simulate the wait without sleeping.
var (
	dispatchNow   = time.Now
	dispatchAfter = time.After
)

// ResetDispatchGraceForTest gives the calling test a fresh grace budget,
// so grace waits from other tests in the process do not shorten or skip
// its wait, and restores the previous budget and dispatchDiagnosticsGrace
// when the test ends. It is the single reset entry point, also for tests
// in other packages (such as the suite's After hook tests); tests that
// call it must not run in parallel with other tests that use the grace
// wait.
func ResetDispatchGraceForTest(t testing.TB) {
	t.Helper()
	origBudget, origGrace := dispatchGraceBudget, dispatchDiagnosticsGrace
	dispatchGraceBudget = newGraceBudget(dispatchGraceBudgetTotal)
	t.Cleanup(func() { dispatchGraceBudget, dispatchDiagnosticsGrace = origBudget, origGrace })
}

// graceInterval is a half-open wall-clock interval [start, end).
type graceInterval struct {
	start, end time.Time
}

// graceBudget is a concurrency-safe bound on the wall-clock time covered
// by grace waits, shared across scenarios. covered is the union of the
// granted intervals, kept merged and sorted by start, so time two waits
// share counts once.
type graceBudget struct {
	total   time.Duration
	mu      sync.Mutex
	covered []graceInterval
}

func newGraceBudget(total time.Duration) *graceBudget {
	return &graceBudget{total: total}
}

// grant returns how long a wait starting at now may last, at most want,
// and adds the granted interval to the covered time. Time already covered
// (such as by a wait still in progress) is free; uncovered time is
// charged against the budget, and the wait ends where the budget runs
// out. It returns zero once the budget is used up.
func (g *graceBudget) grant(now time.Time, want time.Duration) time.Duration {
	if want <= 0 {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	var used time.Duration
	for _, iv := range g.covered {
		used += iv.end.Sub(iv.start)
	}
	left := g.total - used

	t, end := now, now.Add(want)
	for _, iv := range g.covered {
		if !t.Before(end) {
			break
		}
		if !iv.end.After(t) {
			continue
		}
		if iv.start.After(t) {
			gap := minTime(iv.start, end).Sub(t)
			if gap > left {
				t = t.Add(left)
				left = 0
				break
			}
			t = t.Add(gap)
			left -= gap
			if !t.Before(end) {
				break
			}
		}
		t = minTime(iv.end, end)
	}
	if t.Before(end) && left > 0 {
		t = minTime(t.Add(left), end)
	}
	granted := t.Sub(now)
	if granted <= 0 {
		return 0
	}
	g.covered = mergeIntervals(append(g.covered, graceInterval{start: now, end: t}))
	return granted
}

// mergeIntervals returns ivs as disjoint intervals sorted by start.
func mergeIntervals(ivs []graceInterval) []graceInterval {
	slices.SortFunc(ivs, func(a, b graceInterval) int { return a.start.Compare(b.start) })
	merged := make([]graceInterval, 0, len(ivs))
	for _, iv := range ivs {
		if n := len(merged); n > 0 && !iv.start.After(merged[n-1].end) {
			merged[n-1].end = maxTime(merged[n-1].end, iv.end)
			continue
		}
		merged = append(merged, iv)
	}
	return merged
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

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
	saveRunLogs(w, label, run, func() (string, error) {
		return writeWorkflowRunLogs(ctx, w, label, run)
	})
}

// saveFetchedWorkflowRunLogs is saveWorkflowRunLogs for logs, or a fetch
// error, that the caller already got from GetRunLogs, so a step that also
// reads the logs fetches them once.
func saveFetchedWorkflowRunLogs(w *world.World, label string, run *forge.WorkflowRun, logs string, fetchErr error) {
	saveRunLogs(w, label, run, func() (string, error) {
		return writeFetchedWorkflowRunLogs(w, label, run, logs, fetchErr)
	})
}

// saveRunLogs runs write for the two save functions above and logs the outcome.
func saveRunLogs(w *world.World, label string, run *forge.WorkflowRun, write func() (string, error)) {
	if run == nil {
		return
	}
	RegisterRedactionLiterals(w)

	if strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR")) == "" {
		logRedacted(w, "save workflow run logs: BEHAVIOUR_ARTIFACT_DIR unset, skipping log collection for %s run %d", label, run.ID)
		return
	}

	logPath, err := write()
	if err != nil {
		logRedacted(w, "save workflow run logs: %v", err)
		return
	}

	logRedacted(w, "save workflow run logs: %s run %d logs saved to %s", label, run.ID, logPath)
}

// writeWorkflowRunLogs fetches a run's logs and writes them, redacted,
// to debug-<label>-run-<id>/workflow-logs.txt, recording the run in
// w.SavedLogRunIDs. When the fetch fails it writes
// workflow-logs-unavailable.txt with the reason instead and returns the
// fetch error; an empty log response without an error is treated the same
// way. Callers must have checked BEHAVIOUR_ARTIFACT_DIR is set.
func writeWorkflowRunLogs(ctx context.Context, w *world.World, label string, run *forge.WorkflowRun) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
	defer cancel()

	logs, fetchErr := w.CI.GetRunLogs(fetchCtx, w.Org, w.RepoName, run.ID)
	return writeFetchedWorkflowRunLogs(w, label, run, logs, fetchErr)
}

// writeFetchedWorkflowRunLogs is writeWorkflowRunLogs for the result of a
// GetRunLogs call the caller already made.
func writeFetchedWorkflowRunLogs(w *world.World, label string, run *forge.WorkflowRun, logs string, fetchErr error) (string, error) {
	// Both forge clients return an empty string and no error when the run
	// has no jobs to read (for example a run that has not started any job).
	// Empty output is not a log snapshot: report it as unavailable, with an
	// explanation, and leave the run unsaved so the After hook fetches it again.
	// The fetch error is given its context here, before the debug directory
	// is prepared, so a directory failure cannot drop it.
	if fetchErr == nil && strings.TrimSpace(logs) == "" {
		fetchErr = fmt.Errorf("the forge returned no log output for %s run %d (the run may have no jobs yet, or its logs have expired)", label, run.ID)
	} else if fetchErr != nil {
		fetchErr = fmt.Errorf("fetch logs for %s run %d: %w", label, run.ID, fetchErr)
	}

	debugDir, err := prepareDebugDir(label, run.ID)
	if err != nil {
		return "", errors.Join(fetchErr, err)
	}

	if fetchErr != nil {
		notePath := filepath.Join(debugDir, runLogsUnavailableFile)
		note := fmt.Sprintf("Logs for workflow run %d (%s) could not be collected.\nRun: %s\nReason: %v\n",
			run.ID, label, describeRun(*run), fetchErr)
		if err := writeRedactedArtifact(notePath, note); err != nil {
			return "", errors.Join(fetchErr, err)
		}
		return "", fetchErr
	}

	logPath := filepath.Join(debugDir, "workflow-logs.txt")
	complete := runLogsComplete(*run, logs)
	// A complete snapshot already saved for this run is never replaced by a
	// partial one: SavedLogRunIDs is only ever set, so overwriting would
	// lose the complete logs while the run stays marked as saved.
	if !complete && w.SavedLogRunIDs[run.ID] {
		return logPath, nil
	}
	if err := writeRedactedArtifact(logPath, logs); err != nil {
		return "", err
	}
	// A retry that succeeds after a failed attempt in the same directory must
	// not leave the earlier "could not be collected" note beside the logs.
	if err := os.Remove(filepath.Join(debugDir, runLogsUnavailableFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove obsolete %s: %w", runLogsUnavailableFile, err)
	}
	// Only a complete snapshot is deduplicated: a run that had not reached a
	// terminal status, or whose log text embeds a per-job fetch failure, is
	// fetched again by CollectFailureLogs so the After hook can replace the
	// partial snapshot before the repository is deleted.
	if complete {
		if w.SavedLogRunIDs == nil {
			w.SavedLogRunIDs = make(map[int]bool)
		}
		w.SavedLogRunIDs[run.ID] = true
	}
	return logPath, nil
}

// partialLogNote is one in-band note the forge clients embed in the text
// returned by GetRunLogs, without returning an error, when a single job's
// log (or the whole aggregate) could not be fetched in full.
type partialLogNote struct {
	marker string
	// truncation marks a size or page cap, which a later fetch of the same
	// run returns again; any other note records a fetch that failed and may
	// succeed later.
	truncation bool
}

// partialLogNotes are the notes partialLogMarker looks for. The GitHub
// client reads one page of jobs and 1 MiB of each job's log, the GitLab
// client 10 MiB of each trace and 100 MiB in all. The markers are the
// forge package's log-note constants — the same literals the clients
// write — so a client-side rename is caught at compile time here rather
// than silently turning a fetch failure into "complete" logs.
var partialLogNotes = []partialLogNote{
	{marker: forge.LogNoteFailedToFetchLogs},
	{marker: forge.LogNoteLogsUnavailable},
	{marker: forge.LogNoteFailedToReadLogs},
	{marker: forge.LogNoteErrorFetchingTrace},
	{marker: forge.LogNoteErrorReadingTrace},
	{marker: forge.LogNoteAggregateTraceLimit, truncation: true},
	{marker: forge.LogNoteJobListTruncated, truncation: true},
	{marker: forge.LogNoteLogTruncated, truncation: true},
	{marker: forge.LogNoteTraceTruncated, truncation: true},
}

// runLogsComplete reports whether logs, fetched for run, are a complete
// snapshot: the run has reached a terminal status and no job's log was
// replaced by a fetch-failure note or cut at a cap.
func runLogsComplete(run forge.WorkflowRun, logs string) bool {
	return run.Status == "completed" && partialLogMarker(logs).marker == ""
}

// partialLogMarker returns the note logs holds, a fetch-failure note before
// a truncation note, or the zero note when the text carries none.
func partialLogMarker(logs string) partialLogNote {
	var found partialLogNote
	for _, note := range partialLogNotes {
		if !strings.Contains(logs, note.marker) {
			continue
		}
		if !note.truncation {
			return note
		}
		if found.marker == "" {
			found = note
		}
	}
	return found
}

// sensitiveEnvSuffixes mark runner environment variables whose values are
// credentials and are registered with the redactor as exact literals.
var sensitiveEnvSuffixes = []string{"_TOKEN", "_PAT", "_PEM", "_SECRET", "_PASSWORD", "_KEY", "_CREDENTIALS"}

// sensitiveEnvNames are runner environment variables whose values are
// sensitive infrastructure identifiers that no name suffix identifies.
var sensitiveEnvNames = []string{
	"E2E_GCP_PROJECT_ID",
	"E2E_GCP_MINT_PROJECT_ID",
	"E2E_GCP_WIF_PROVIDER",
	"E2E_GCP_SERVICE_ACCOUNT",
	"CLOUDFLARE_ACCOUNT_ID",
}

// RegisterRedactionLiterals registers the runner's known credential literals
// — w.Token, the values of sensitive-looking environment variables and the
// explicitly listed infrastructure identifiers — with the process-wide
// secret redactor. Prefix and structural patterns
// cannot recognise an opaque credential, which would otherwise survive in
// summaries, diagnostics and name-derived directory names. Values the shared
// redactor declines as too short are masked locally by RedactText.
//
// The failure-recording sites — the suite's After hook and the bootstrap
// recorder — also call this before redacting a message at record time, so
// the first failure a process records is redacted with the World's literals
// already registered, not only after debug-log collection has run.
func RegisterRedactionLiterals(w *world.World) {
	if w != nil && w.Token != "" {
		registerSecretForms(w.Token)
	}
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		name = strings.ToUpper(name)
		if slices.Contains(sensitiveEnvNames, name) {
			registerSecretForms(value)
			continue
		}
		for _, suffix := range sensitiveEnvSuffixes {
			if strings.HasSuffix(name, suffix) {
				registerSecretForms(value)
				break
			}
		}
	}
}

// registerSecretForms registers value and its escaped forms. An error or a
// message that quotes a credential (fmt %q, strconv.Quote, JSON encoding)
// changes how quotes, backslashes and control characters are written, so the
// exact literal no longer matches; the escaped spellings are registered too.
func registerSecretForms(value string) {
	registerSecretLiteral(value)
	quoted := strconv.Quote(value)
	registerSecretLiteral(quoted[1 : len(quoted)-1])
	if encoded, err := json.Marshal(value); err == nil && len(encoded) >= 2 {
		registerSecretLiteral(string(encoded[1 : len(encoded)-1]))
	}
}

// minLocalSecretLen is the shortest sensitive literal masked locally. The
// shared redactor refuses values under 8 bytes because masking them can
// mangle ordinary text; for a value known to be sensitive, failing open is
// worse, so RedactText masks values of at least this length itself. Shorter
// values would erase single characters or common fragments across every
// artifact, so they cannot be masked without destroying the diagnostics.
const minLocalSecretLen = 4

var (
	shortSecretsMu sync.RWMutex
	shortSecrets   []string
)

// registerSecretLiteral registers value with the shared redactor and, when the
// redactor declines it as too short, records it for local masking so a short
// sensitive value is not silently left unredacted.
func registerSecretLiteral(value string) {
	if security.RegisterRuntimeSecret(value) || len(value) < minLocalSecretLen {
		return
	}
	shortSecretsMu.Lock()
	defer shortSecretsMu.Unlock()
	if !slices.Contains(shortSecrets, value) {
		shortSecrets = append(shortSecrets, value)
	}
}

// RedactText masks secrets in s with the same redaction the debug-log
// collection applies to everything it writes: failure messages recorded at
// record time get the same treatment as the collected artifacts.
//
// The guarantee has two parts:
//
//   - Pattern-based redaction (security.NewSecretRedactor().Scan) runs
//     unconditionally and needs no initialization: secrets with a
//     recognizable shape — token prefixes such as ghp_ or sk_, credential
//     assignments, private key blocks — are masked whether or not anything
//     was registered.
//   - Exact-literal replacement depends on prior registration in the same
//     process. Literals of at least 8 bytes are registered process-wide
//     with the shared redactor, whose scan masks them; shorter sensitive
//     values (down to four bytes) are kept in package-level state guarded
//     by shortSecretsMu and masked here. RedactText registers nothing
//     itself: both sets are populated by this package's debug-log
//     collection — CollectFailureLogs, the workflow-log saving steps and
//     the mint-status steps register the World token and sensitive runner
//     environment values before writing artifacts — and by the
//     failure-recording sites, which call RegisterRedactionLiterals before
//     redacting a message at record time.
//
// Callers cannot assume a stronger guarantee than the tooling provides: in
// a process where no secrets were registered — a consumer binary that only
// calls RedactText, for example — both literal sets are empty and an
// opaque credential that no pattern recognizes passes through unredacted.
func RedactText(s string) string {
	if res := security.NewSecretRedactor().Scan(s); res.Sanitized != "" {
		s = res.Sanitized
	}
	shortSecretsMu.RLock()
	defer shortSecretsMu.RUnlock()
	for _, secret := range shortSecrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}

// SanitizeConsole neutralizes text that GitHub Actions would interpret when
// printed to the console: line breaks and other control characters are
// replaced with spaces so interpolated text cannot start a new line, and the
// "::" workflow-command delimiter is broken up.
func SanitizeConsole(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "::", ": :")
}

// logRedacted formats a diagnostic message, redacts it and sanitizes it for
// the console before handing it to the world logger: errors, scenario names
// and paths can carry credentials or forge-supplied text, and neither the CI
// artifact redaction nor the runner reaches console output.
func logRedacted(w *world.World, format string, args ...any) {
	if w.Logf == nil {
		return
	}
	w.Logf("%s", SanitizeConsole(RedactText(fmt.Sprintf(format, args...))))
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
	RegisterRedactionLiterals(w)
	artifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if artifactDir == "" {
		logRedacted(w, "collect failure logs: BEHAVIOUR_ARTIFACT_DIR unset, skipping log collection for failed scenario %q", RedactText(w.ScenarioName))
		return
	}

	// Dispatch diagnostics run first: the grace period gives a late run
	// time to appear, so the run-log collection below also saves it. They
	// need the details only *ci.NotDispatchedError (GitHub Actions)
	// carries; another driver's not-dispatched timeout, which wraps only
	// ci.ErrWorkflowNotDispatched, gets none.
	var dispatch string
	var waited *ci.NotDispatchedError
	if errors.As(scenarioErr, &waited) {
		dispatch = collectDispatchDiagnostics(ctx, w, waited)
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
	if dispatch != "" {
		b.WriteString("\nDispatch diagnostics:\n")
		b.WriteString(dispatch)
	}

	summaryPath, err := writeFailureSummary(artifactDir, w.ScenarioName, b.String())
	if err != nil {
		logRedacted(w, "collect failure logs: %v", err)
		return
	}
	logRedacted(w, "collect failure logs: failure summary for scenario %q saved to %s", RedactText(w.ScenarioName), summaryPath)
}

// collectScenarioRunLogs saves the logs of the scenario's runs that are
// not yet saved and returns one summary line per run, or lines stating
// why there are no runs to collect.
func collectScenarioRunLogs(ctx context.Context, w *world.World) string {
	if w.RepoName == "" {
		return "  none: the repository identity is unavailable (the scenario failed before one was recorded), so workflow runs could not be collected\n"
	}
	if w.CI == nil {
		return "  none: no CI driver is configured, so workflow runs cannot be collected\n"
	}

	var b strings.Builder
	var runs []forge.WorkflowRun
	var listed []forge.WorkflowRun

	if lister, ok := w.CI.(ci.RunLister); ok {
		listCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
		recent, err := lister.ListRecentRuns(listCtx, w.Org, w.RepoName, maxListedFailureRuns)
		cancel()
		if err != nil {
			fmt.Fprintf(&b, "  listing the repository's workflow runs failed: %v\n", err)
		}
		if len(recent) >= maxListedFailureRuns {
			fmt.Fprintf(&b, "  the listing returned %d runs, its limit; older runs may exist and were not considered\n", len(recent))
		}
		listed = recent
	} else {
		b.WriteString("  the CI driver cannot list workflow runs (it does not implement ci.RunLister); only the run resolved by a step is collected\n")
	}

	// Merge by run ID. The step-resolved run may be a stale snapshot (for
	// example in_progress when the enclosing workflow has since finished),
	// so the listing's metadata wins; the step-resolved run is the fallback
	// when the listing fails or omits it.
	if w.WorkflowRun != nil {
		resolved := *w.WorkflowRun
		for _, run := range listed {
			if run.ID == resolved.ID {
				resolved = run
				break
			}
		}
		runs = append(runs, resolved)
	}
	for _, run := range listed {
		if inScenarioWindow(run, w.ScenarioBegin) {
			runs = append(runs, run)
		}
	}

	seen := make(map[int]bool, len(runs))
	collected := 0
	fetches := 0
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
		if fetches >= maxFailureLogRuns {
			fmt.Fprintf(&b, "  - %s: logs not collected: the limit of %d log fetches was reached\n", describeRun(run), maxFailureLogRuns)
			continue
		}
		fetches++
		logPath, err := writeWorkflowRunLogs(ctx, w, runLabel(run), &run)
		if err != nil {
			logRedacted(w, "collect failure logs: %v", err)
			fmt.Fprintf(&b, "  - %s: logs could not be collected: %v\n", describeRun(run), err)
			continue
		}
		if !w.SavedLogRunIDs[run.ID] {
			fmt.Fprintf(&b, "  - %s: logs saved to %s, but the snapshot is incomplete: the run had not finished or the forge client omitted or truncated some job logs (see the notes inside the file)\n", describeRun(run), logPath)
			continue
		}
		fmt.Fprintf(&b, "  - %s: logs saved to %s\n", describeRun(run), logPath)
	}
	if collected == 0 {
		b.WriteString("  none: no workflow run was found in the repository for this scenario\n")
	}
	return b.String()
}

// collectDispatchDiagnostics records, for a scenario that failed because
// no workflow run appeared (GitHub Actions only), what the repository
// shows after a grace period: a note when the wait was skipped (no
// budget left, or no verdict possible) or shortened, the time of the
// listing and the real elapsed wait, the scenario's issue with its
// creation time and state, every workflow run (without the scenario time
// filter) with its event, creation time and workflow file, and whether a
// run of the waited-for workflow and event appeared after the wait. It
// runs before teardown deletes the repository, so the evidence survives
// in the artifact. Every lookup is best-effort and reports its own
// failure in place of its section.
func collectDispatchDiagnostics(ctx context.Context, w *world.World, waited *ci.NotDispatchedError) string {
	if w.RepoName == "" {
		return "  none: the repository identity is unavailable, so dispatch diagnostics could not be collected\n"
	}

	budget := dispatchGraceBudget
	var b strings.Builder
	graceStart := dispatchNow()
	// partialWait is empty when the configured wait ran in full, and
	// otherwise says how it fell short, so the verdict does not read a
	// missing run as a lost event.
	partialWait := ""
	var granted time.Duration
	_, canList := w.CI.(ci.RunLister)
	switch {
	case dispatchDiagnosticsGrace <= 0:
		partialWait = "no grace wait is configured"
	case !canList || waited == nil || waited.WorkflowFile == "":
		// No verdict can be computed, so waiting would prove nothing.
		partialWait = "the grace wait was skipped"
		b.WriteString("  grace wait: skipped: no verdict can be computed without a run listing and the waited-for workflow\n")
	case ctx.Err() != nil:
		// Already cancelled: do not use up the shared budget.
		partialWait = "the grace wait was cut short after 0s"
		fmt.Fprintf(&b, "  the grace period was cut short: %v\n", ctx.Err())
	default:
		granted = budget.grant(graceStart, dispatchDiagnosticsGrace)
	}
	switch {
	case partialWait != "":
	case granted <= 0:
		partialWait = "the grace wait was skipped"
		fmt.Fprintf(&b, "  grace wait: skipped: the total grace wait time (%s per test process) is used up\n",
			budget.total)
	default:
		if granted < dispatchDiagnosticsGrace {
			partialWait = "the grace wait was shortened to " + granted.Round(time.Second).String()
			fmt.Fprintf(&b, "  grace wait: shortened to %s: the total grace wait time (%s per test process) runs out then\n",
				granted.Round(time.Second), budget.total)
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(&b, "  the grace period was cut short: %v\n", ctx.Err())
		case <-dispatchAfter(granted):
		}
	}
	listedAt := dispatchNow()
	elapsed := listedAt.Sub(graceStart)
	if elapsed < 0 {
		elapsed = 0
	}
	if ctx.Err() != nil && granted > 0 && elapsed < granted {
		partialWait = "the grace wait was cut short after " + elapsed.Round(time.Second).String()
	}
	fmt.Fprintf(&b, "  repository: %s/%s\n", w.Org, w.RepoName)
	fmt.Fprintf(&b, "  listed at: %s (runner clock), after a %s grace period that began at %s\n",
		listedAt.UTC().Format(time.RFC3339), elapsed.Round(time.Second), graceStart.UTC().Format(time.RFC3339))
	issueLine, issueCreated := describeDispatchIssue(ctx, w)
	b.WriteString(issueLine)
	b.WriteString(describeDispatchRuns(ctx, w, waited, graceStart, issueCreated, partialWait))
	return b.String()
}

// describeDispatchIssue reports the scenario's issue with its creation
// time and state, and returns the parsed creation time (zero when
// unknown), so for an issue-triggered wait a late run's delay can be
// measured from the triggering event on GitHub's own clock.
func describeDispatchIssue(ctx context.Context, w *world.World) (string, time.Time) {
	if w.IssueNumber == 0 {
		return "  issue: none recorded for this scenario\n", time.Time{}
	}
	if w.SCM == nil {
		return fmt.Sprintf("  issue #%d: not looked up: no SCM driver is configured\n", w.IssueNumber), time.Time{}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
	defer cancel()
	issue, err := w.SCM.GetIssue(lookupCtx, w.Org, w.RepoName, w.IssueNumber)
	if err != nil {
		return fmt.Sprintf("  issue #%d: lookup failed: %v\n", w.IssueNumber, err), time.Time{}
	}
	created, _ := time.Parse(time.RFC3339, issue.CreatedAt)
	return fmt.Sprintf("  issue #%d: created_at %s, state %s\n", w.IssueNumber, orUnknown(issue.CreatedAt), orUnknown(issue.State)), created
}

// describeDispatchRuns lists every workflow run in the repository, then
// the runs of the workflow and event the scenario waited for, and states
// whether one appeared after the wait. For an issues-event wait each
// matching run shows its gap from the issue's creation (both GitHub
// timestamps); for other events the issue is not the triggering event,
// so no gap is measured. "After the wait" compares the run's GitHub
// created_at with the runner's grace start minus
// issueOpenDrainSkewBuffer, the same clock-skew allowance the issue-open
// drain uses. partialWait, when not empty, says how the grace wait fell
// short; the verdict is then inconclusive rather than a lost event or a
// clean "predates the wait". A missing run is also inconclusive when the
// listing was truncated, since the run may be among the unlisted ones.
func describeDispatchRuns(ctx context.Context, w *world.World, waited *ci.NotDispatchedError, graceStart, issueCreated time.Time, partialWait string) string {
	lister, ok := w.CI.(ci.RunLister)
	if !ok {
		return "  workflow runs: not listed: the CI driver cannot list workflow runs (it does not implement ci.RunLister)\n"
	}
	listCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
	defer cancel()
	runs, err := lister.ListRecentRuns(listCtx, w.Org, w.RepoName, maxListedFailureRuns)
	if err != nil {
		return fmt.Sprintf("  workflow runs: listing failed: %v\n", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  workflow runs (%d, no time filter):\n", len(runs))
	truncated := len(runs) >= maxListedFailureRuns
	if truncated {
		fmt.Fprintf(&b, "    the listing returned %d runs, its limit; older runs may exist\n", len(runs))
	}
	for _, run := range runs {
		fmt.Fprintf(&b, "    - run %d: workflow %q, event %q, created_at %s, status %q, conclusion %q\n",
			run.ID, RedactText(runWorkflowFile(run)), RedactText(run.Event), orUnknown(RedactText(run.CreatedAt)),
			RedactText(run.Status), RedactText(run.Conclusion))
	}
	if len(runs) == 0 {
		b.WriteString("    none\n")
	}

	if waited == nil || waited.WorkflowFile == "" {
		b.WriteString("  matching run after the wait: unknown: the error does not record which workflow and event were waited for\n")
		return b.String()
	}
	label := waited.WorkflowFile
	if waited.Event != "" {
		label += " (" + waited.Event + ")"
	}
	// Run timestamps have second precision; allow the runner clock to be
	// ahead of GitHub's by up to issueOpenDrainSkewBuffer.
	threshold := graceStart.Add(-issueOpenDrainSkewBuffer).Truncate(time.Second)
	issueTriggered := waited.Event == "issues"
	// lateRun is the earliest matching run created after the threshold, so
	// the verdict does not depend on the listing's order.
	var lateRun *forge.WorkflowRun
	var lateCreated time.Time
	var matching strings.Builder
	for i := range runs {
		run := runs[i]
		if filepath.Base(run.Path) != waited.WorkflowFile || (waited.Event != "" && run.Event != waited.Event) {
			continue
		}
		created, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if issueTriggered {
			gap := "unknown"
			if parseErr == nil && !issueCreated.IsZero() {
				gap = formatSignedDuration(created.Sub(issueCreated))
			}
			fmt.Fprintf(&matching, "    - run %d: created_at %s, %s after the issue's created_at\n",
				run.ID, orUnknown(RedactText(run.CreatedAt)), gap)
		} else {
			fmt.Fprintf(&matching, "    - run %d: created_at %s\n", run.ID, orUnknown(RedactText(run.CreatedAt)))
		}
		if parseErr == nil && !created.Before(threshold) && (lateRun == nil || created.Before(lateCreated)) {
			lateRun, lateCreated = &run, created
		}
	}
	if matching.Len() == 0 {
		fmt.Fprintf(&b, "  matching %s runs: none\n", label)
	} else if issueTriggered {
		fmt.Fprintf(&b, "  matching %s runs (gap measured on GitHub's clock):\n", label)
		b.WriteString(matching.String())
	} else {
		fmt.Fprintf(&b, "  matching %s runs (no gap: it is measured only from an issue's creation, and the triggering event here is not the issue):\n", label)
		b.WriteString(matching.String())
	}
	const skewNote = "compared with the runner clock, allowing 30s of clock skew"
	switch {
	case lateRun != nil:
		fmt.Fprintf(&b, "  matching %s run created after the wait ended: yes (run %d, created_at %s; %s): suggests a late dispatch\n",
			label, lateRun.ID, RedactText(lateRun.CreatedAt), skewNote)
	case matching.Len() == 0 && (partialWait != "" || truncated):
		reasons := make([]string, 0, 2)
		if partialWait != "" {
			reasons = append(reasons, partialWait)
		}
		if truncated {
			reasons = append(reasons, "the listing was truncated")
		}
		fmt.Fprintf(&b, "  matching %s run created after the wait ended: no (inconclusive: %s; %s)\n",
			label, strings.Join(reasons, "; "), skewNote)
	case matching.Len() == 0:
		fmt.Fprintf(&b, "  matching %s run created after the wait ended: no (%s): no matching run exists, which suggests the event produced no run\n",
			label, skewNote)
	case partialWait != "":
		fmt.Fprintf(&b, "  matching %s run created after the wait ended: no (inconclusive: %s; %s): the matching runs listed above predate the end of the wait\n",
			label, partialWait, skewNote)
	default:
		fmt.Fprintf(&b, "  matching %s run created after the wait ended: no (%s): the matching runs listed above predate the end of the wait\n",
			label, skewNote)
	}
	return b.String()
}

// formatSignedDuration renders d with an explicit sign, e.g. "+5m12s".
func formatSignedDuration(d time.Duration) string {
	if d < 0 {
		return "-" + (-d).String()
	}
	return "+" + d.String()
}

// runWorkflowFile names a run's workflow by its file, falling back to the
// workflow name when the forge did not report the file.
func runWorkflowFile(run forge.WorkflowRun) string {
	if run.Path != "" {
		return run.Path
	}
	return run.Name
}

// orUnknown returns s, or "(unknown)" when s is empty.
func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
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

// describeRun formats a run for a summary or note line. Each field is
// redacted before it is quoted: %q escapes quotes and backslashes, which
// would change the representation of a registered credential and let it
// evade exact-string matching in the final whole-message scan.
func describeRun(run forge.WorkflowRun) string {
	s := fmt.Sprintf("run %d %q (status %q, conclusion %q)", run.ID, RedactText(run.Name), RedactText(run.Status), RedactText(run.Conclusion))
	if run.HTMLURL != "" {
		s += " " + run.HTMLURL
	}
	return s
}

// runLabel derives a debug-directory label from a run's workflow name.
func runLabel(run forge.WorkflowRun) string {
	return pathSlug(run.Name, 40, "workflow")
}

// pathSlug redacts secrets from s, lowercases it and replaces every run of characters outside
// [a-z0-9] with a single '-', truncated to maxLen characters, so it is safe
// to embed in a directory name. Returns fallback when nothing remains.
func pathSlug(s string, maxLen int, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(RedactText(s)) {
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
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
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
//
// The content goes to a new 0o600 temp file in the same directory that is
// then renamed over path. os.WriteFile would keep the mode of a pre-existing
// more permissive file and would follow a pre-existing symlink at path; the
// rename replaces whatever is at path (a symlink is replaced, not followed)
// with a fresh private regular file.
func writeRedactedArtifact(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.WriteString(RedactText(content))
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

// prepareDebugDir creates a debug subdirectory for a workflow run's
// logs under BEHAVIOUR_ARTIFACT_DIR so CI's upload-artifact ships the
// logs automatically. Returns an error when BEHAVIOUR_ARTIFACT_DIR is
// not set — callers should skip log collection in that case to avoid
// leaking orphaned temp directories.
//
// label is redacted and reduced to a path-safe slug here, at the shared
// directory-creation boundary, so no caller can put a credential or a path
// separator into an uploaded directory name.
func prepareDebugDir(label string, runID int) (string, error) {
	dirName := fmt.Sprintf("debug-%s-run-%d", pathSlug(label, 40, "workflow"), runID)

	ciArtifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if ciArtifactDir == "" {
		return "", fmt.Errorf("BEHAVIOUR_ARTIFACT_DIR is not set")
	}

	debugDir := filepath.Join(ciArtifactDir, dirName)
	if err := os.MkdirAll(debugDir, 0o700); err != nil {
		return "", fmt.Errorf("creating debug dir: %w", err)
	}
	// MkdirAll accepts a symlink to a directory; refuse one so the writes
	// below cannot be redirected outside the artifact directory.
	info, err := os.Lstat(debugDir)
	if err != nil {
		return "", fmt.Errorf("creating debug dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("creating debug dir: %s is not a plain directory (symlinks are not followed)", debugDir)
	}
	return debugDir, nil
}
