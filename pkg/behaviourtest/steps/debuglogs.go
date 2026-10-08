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
	registerRedactionLiterals(w)

	if strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR")) == "" {
		logRedacted(w, "save workflow run logs: BEHAVIOUR_ARTIFACT_DIR unset, skipping log collection for %s run %d", label, run.ID)
		return
	}

	logPath, err := writeWorkflowRunLogs(ctx, w, label, run)
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

// partialLogMarkers are the in-band notes the forge clients embed in the
// text returned by GetRunLogs, without returning an error, when a single
// job's log (or the whole aggregate) could not be fetched in full.
var partialLogMarkers = []string{
	"[failed to fetch logs:",
	"[logs unavailable:",
	"[failed to read logs:",
	"error fetching trace:",
	"error reading trace:",
	"aggregate trace limit",
	// Truncation notes: the GitHub client reads one page of jobs and 1 MiB
	// of each job's log, the GitLab client 10 MiB of each trace.
	"[job list truncated:",
	"[log truncated:",
	"trace truncated at",
}

// runLogsComplete reports whether logs, fetched for run, are a complete
// snapshot: the run has reached a terminal status and no job's log was
// replaced by a fetch-failure note.
func runLogsComplete(run forge.WorkflowRun, logs string) bool {
	if run.Status != "completed" {
		return false
	}
	for _, marker := range partialLogMarkers {
		if strings.Contains(logs, marker) {
			return false
		}
	}
	return true
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

// registerRedactionLiterals registers the runner's known credential literals
// — w.Token, the values of sensitive-looking environment variables and the
// explicitly listed infrastructure identifiers — with the process-wide
// secret redactor. Prefix and structural patterns
// cannot recognise an opaque credential, which would otherwise survive in
// summaries, diagnostics and name-derived directory names. Values the shared
// redactor declines as too short are masked locally by redactText.
func registerRedactionLiterals(w *world.World) {
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
// worse, so redactText masks values of at least this length itself. Shorter
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

// redactText passes s through the secret redactor.
func redactText(s string) string {
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

// sanitizeConsole neutralizes text that GitHub Actions would interpret when
// printed to the console: line breaks and other control characters are
// replaced with spaces so interpolated text cannot start a new line, and the
// "::" workflow-command delimiter is broken up.
func sanitizeConsole(s string) string {
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
	w.Logf("%s", sanitizeConsole(redactText(fmt.Sprintf(format, args...))))
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
	registerRedactionLiterals(w)
	artifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if artifactDir == "" {
		logRedacted(w, "collect failure logs: BEHAVIOUR_ARTIFACT_DIR unset, skipping log collection for failed scenario %q", redactText(w.ScenarioName))
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
		logRedacted(w, "collect failure logs: %v", err)
		return
	}
	logRedacted(w, "collect failure logs: failure summary for scenario %q saved to %s", redactText(w.ScenarioName), summaryPath)
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
	s := fmt.Sprintf("run %d %q (status %q, conclusion %q)", run.ID, redactText(run.Name), redactText(run.Status), redactText(run.Conclusion))
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
	for _, r := range strings.ToLower(redactText(s)) {
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
	_, writeErr := tmp.WriteString(redactText(content))
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
