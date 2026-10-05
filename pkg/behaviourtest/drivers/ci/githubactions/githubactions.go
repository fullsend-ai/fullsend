package githubactions

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
)

const (
	pollInterval = 15 * time.Second
	// dispatchWait bounds how long WaitForHarnessAgent and friends wait
	// for a harness run to settle. Raised from 12 to 20 minutes to give
	// longer-running playback scenarios (which replay a full dummy-agent
	// pipeline rather than a single dispatch) room to complete without
	// spuriously timing out.
	dispatchWait = 20 * time.Minute

	// Dispatch detection uses exponential backoff: the poll interval
	// starts at dispatchPollInit, doubles each iteration up to
	// dispatchPollMax, and the total detection window is dispatchTimeout.
	// This replaces the previous fixed dispatchPoll × dispatchMaxTry
	// window to tolerate transient GitHub Actions dispatch latency.
	dispatchPollInit = 2 * time.Second
	dispatchPollMax  = 30 * time.Second
	dispatchTimeout  = 5 * time.Minute

	// countSettlePoll is the fixed poll interval used by
	// CountHarnessDispatches while waiting for in-progress runs to
	// reach a terminal state.
	countSettlePoll = 5 * time.Second

	// pollMinBudget is the least wait budget a harness poll is started
	// with. A poll that would begin with less than this is skipped, so
	// the driver never cuts its own last poll short and then reports the
	// cutoff as a listing failure.
	pollMinBudget = 5 * time.Second

	// timeoutDiagnosticsBudget bounds the whole diagnostics pass made
	// while building a harness-wait timeout message, and lookupBudget
	// bounds each API call within it. The token may still be rate
	// limited at that point; the wait already recorded the informative
	// error, so one stalled lookup must not consume the budget of the
	// others.
	timeoutDiagnosticsBudget = 2 * time.Minute
	lookupBudget             = 30 * time.Second

	artifactRunPoll = 5 * time.Second
	artifactRunWait = 5 * time.Minute

	agentWorkflowName = "Triage Agent"

	assertNoWorkflowChecks = 3
	assertNoWorkflowDelay  = 10 * time.Second

	// harnessWorkflowFile is the workflow filename used by the harness.
	// Used to scope fail-fast and diagnostic queries to harness runs only,
	// avoiding false positives from unrelated workflow failures.
	harnessWorkflowFile = "fullsend.yaml"
)

// Driver implements ci.Driver against GitHub Actions.
type Driver struct {
	Client forge.Client
	Token  string

	// afterFunc is the timer function used by poll loops. It defaults to
	// time.After in New(). Tests inject an instant-return implementation
	// to avoid sleeping on real wall-clock intervals.
	afterFunc func(time.Duration) <-chan time.Time

	// nowFunc is the clock used by the harness-wait deadlines. It
	// defaults to time.Now in New(). Tests inject a stepping clock so
	// the timeout branch — and the diagnostics it builds — can be
	// exercised without waiting out dispatchWait on the wall clock.
	nowFunc func() time.Time
}

func New(client forge.Client, token string) ci.Driver {
	return &Driver{Client: client, Token: token, afterFunc: time.After, nowFunc: time.Now}
}

// now returns the current time from nowFunc, falling back to time.Now
// so that a zero-value Driver still works.
func (d *Driver) now() time.Time {
	if d.nowFunc != nil {
		return d.nowFunc()
	}
	return time.Now()
}

// timerAfter returns a channel that fires after dur. It uses afterFunc
// when set, falling back to time.After so that a zero-value Driver
// (used in some tests) still works.
func (d *Driver) timerAfter(dur time.Duration) <-chan time.Time {
	if d.afterFunc != nil {
		return d.afterFunc(dur)
	}
	return time.After(dur)
}

// nextBackoff doubles current, capping at max.
func nextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}

func (d *Driver) WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	var triageRun *forge.WorkflowRun
	deadline := time.Now().Add(dispatchTimeout)
	interval := dispatchPollInit
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(interval):
		}
		interval = nextBackoff(interval, dispatchPollMax)
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
		if err != nil {
			continue
		}
		if candidate := selectWorkflowRun(runs, after, event); candidate != nil {
			if candidate.Status == "completed" && candidate.Conclusion != "success" {
				return nil, fmt.Errorf("workflow %s run %d concluded with %q during dispatch", workflowFile, candidate.ID, candidate.Conclusion)
			}
			triageRun = candidate
			break
		}
	}
	if triageRun == nil {
		if event != "" {
			return nil, fmt.Errorf("workflow %s (%s) was not dispatched", workflowFile, event)
		}
		return nil, fmt.Errorf("workflow %s was not dispatched", workflowFile)
	}

	deadline = time.Now().Add(dispatchWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(pollInterval):
		}
		run, err := d.Client.GetWorkflowRun(ctx, owner, repo, triageRun.ID)
		if err != nil {
			continue
		}
		if run.Status == "completed" {
			if run.Conclusion == "success" {
				return run, nil
			}
			if replacement := selectSuccessfulWorkflowRun(latestRuns(ctx, d, owner, repo, workflowFile), after, event); replacement != nil && replacement.ID > triageRun.ID {
				triageRun = replacement
				continue
			}
			return run, fmt.Errorf("workflow %s run %d concluded with %q", workflowFile, run.ID, run.Conclusion)
		}
	}
	return nil, fmt.Errorf("workflow %s run %d did not complete within deadline", workflowFile, triageRun.ID)
}

func latestRuns(ctx context.Context, d *Driver, owner, repo, workflowFile string) []forge.WorkflowRun {
	runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
	if err != nil {
		return nil
	}
	return runs
}

// selectWorkflowRun returns the newest workflow run after triggerTime that matches
// the optional event filter. Callers decide how to handle non-success conclusions.
func selectWorkflowRun(runs []forge.WorkflowRun, triggerTime time.Time, event string) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		if !workflowRunMatches(run, triggerTime, event) {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func selectSuccessfulWorkflowRun(runs []forge.WorkflowRun, triggerTime time.Time, event string) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		if !workflowRunMatches(run, triggerTime, event) {
			continue
		}
		if run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func workflowRunMatches(run forge.WorkflowRun, triggerTime time.Time, event string) bool {
	runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
	if parseErr != nil || runTime.Before(triggerTime) {
		return false
	}
	if event != "" && run.Event != event {
		return false
	}
	return true
}

func (d *Driver) FindCompletedWorkflowRun(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	deadline := time.Now().Add(artifactRunWait)
	for time.Now().Before(deadline) {
		run, err := d.findCompletedWorkflowRunOnce(ctx, owner, repo, workflowFile, after)
		if err != nil {
			return nil, err
		}
		if run != nil {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(artifactRunPoll):
		}
	}
	return nil, fmt.Errorf("no completed workflow run for %s after %s", workflowFile, after.Format(time.RFC3339))
}

func (d *Driver) findCompletedWorkflowRunOnce(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	for _, wf := range []string{workflowFile, ".github/workflows/" + workflowFile} {
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, wf)
		if err != nil {
			continue
		}
		if run := selectCompletedSuccessRun(runs, after); run != nil {
			return run, nil
		}
	}
	runs, err := d.Client.ListRecentWorkflowRuns(ctx, owner, repo, 30)
	if err != nil {
		return nil, err
	}
	return selectCompletedSuccessRunByName(runs, after, agentWorkflowName), nil
}

func selectCompletedSuccessRunByName(runs []forge.WorkflowRun, after time.Time, name string) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		if run.Name != name {
			continue
		}
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		if run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func selectCompletedSuccessRun(runs []forge.WorkflowRun, after time.Time) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		if run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func (d *Driver) AssertNoWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) error {
	for attempt := 0; attempt < assertNoWorkflowChecks; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-d.timerAfter(assertNoWorkflowDelay):
			}
		}
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
		if err != nil {
			return err
		}
		for _, run := range runs {
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil {
				continue
			}
			if !runTime.Before(after) {
				return fmt.Errorf("unexpected workflow run %d for %s", run.ID, workflowFile)
			}
		}
	}
	return nil
}

func (d *Driver) GetRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	return d.Client.GetWorkflowRunLogs(ctx, owner, repo, runID)
}

func (d *Driver) DownloadNamedArtifactFromRun(ctx context.Context, owner, repo string, runID int, artifactName string, destDir string) error {
	artifacts, err := d.Client.ListWorkflowRunArtifacts(ctx, owner, repo, runID)
	if err != nil {
		return err
	}
	for _, art := range artifacts {
		if art.Name != artifactName {
			continue
		}
		zipData, err := d.Client.DownloadWorkflowRunArtifact(ctx, owner, repo, art.ID)
		if err != nil {
			return err
		}
		return extractArtifactZip(art.Name, zipData, destDir)
	}
	return fmt.Errorf("artifact %q not found on workflow run %d", artifactName, runID)
}

func (d *Driver) DownloadArtifacts(ctx context.Context, owner, repo string, runID int, destDir string) error {
	artifacts, err := d.Client.ListWorkflowRunArtifacts(ctx, owner, repo, runID)
	if err != nil {
		return err
	}
	for _, art := range artifacts {
		zipData, err := d.Client.DownloadWorkflowRunArtifact(ctx, owner, repo, art.ID)
		if err != nil {
			return err
		}
		if err := extractArtifactZip(art.Name, zipData, destDir); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) DownloadNamedArtifactAfter(ctx context.Context, owner, repo, artifactName string, after time.Time, destDir string) error {
	deadline := time.Now().Add(artifactRunWait)
	var lastNewestCreatedAt string
	for time.Now().Before(deadline) {
		arts, err := d.Client.ListRepositoryArtifacts(ctx, owner, repo, 100)
		if err != nil {
			return err
		}
		newestCreatedAt := newestRepositoryArtifactCreatedAt(arts)
		if newestCreatedAt != "" && newestCreatedAt == lastNewestCreatedAt {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-d.timerAfter(artifactRunPoll):
			}
			continue
		}
		lastNewestCreatedAt = newestCreatedAt

		if art := selectRepositoryArtifactAfter(arts, artifactName, after); art != nil {
			zipData, err := d.Client.DownloadWorkflowRunArtifact(ctx, owner, repo, art.ID)
			if err != nil {
				return err
			}
			return extractArtifactZip(art.Name, zipData, destDir)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.timerAfter(artifactRunPoll):
		}
	}
	return fmt.Errorf("artifact %q not found after %s", artifactName, after.Format(time.RFC3339))
}

func newestRepositoryArtifactCreatedAt(arts []forge.RepositoryArtifact) string {
	var newest string
	for _, art := range arts {
		if art.CreatedAt > newest {
			newest = art.CreatedAt
		}
	}
	return newest
}

func selectRepositoryArtifactAfter(arts []forge.RepositoryArtifact, name string, after time.Time) *forge.RepositoryArtifact {
	var best *forge.RepositoryArtifact
	for _, art := range arts {
		if art.Name != name {
			continue
		}
		artTime, parseErr := time.Parse(time.RFC3339, art.CreatedAt)
		if parseErr != nil || artTime.Before(after) {
			continue
		}
		if best == nil || art.ID > best.ID {
			a := art
			best = &a
		}
	}
	return best
}

func extractArtifactZip(name string, zipData []byte, destDir string) error {
	tmp, err := os.CreateTemp("", "behaviour-artifact-*.zip")
	if err != nil {
		return fmt.Errorf("create temp artifact zip: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(zipData); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp artifact zip: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp artifact zip: %w", err)
	}

	safeName := filepath.Base(name)
	if safeName == "" || safeName == "." {
		safeName = "artifact"
	}
	artDir := filepath.Join(destDir, safeName)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		return err
	}

	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return fmt.Errorf("parse artifact zip %q: %w", safeName, err)
	}
	defer zr.Close()

	const perFileLimit = 10 << 20
	const totalExtractLimit = 100 << 20
	var totalExtracted int64
	for _, f := range zr.File {
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact zip %q contains symlink entry %q", safeName, f.Name)
		}
		outPath := filepath.Join(artDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(outPath), filepath.Clean(artDir)+string(os.PathSeparator)) {
			return fmt.Errorf("artifact zip %q contains path traversal entry %q", safeName, f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(outPath, 0o755); err != nil {
				return fmt.Errorf("create artifact dir %q: %w", f.Name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return fmt.Errorf("create artifact parent dir for %q: %w", f.Name, err)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := readLimited(rc, perFileLimit)
		rc.Close()
		if err != nil {
			return fmt.Errorf("read artifact entry %q: %w", f.Name, err)
		}
		totalExtracted += int64(len(data))
		if totalExtracted > totalExtractLimit {
			return fmt.Errorf("artifact zip %q exceeds %d byte aggregate extraction limit", safeName, totalExtractLimit)
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("entry exceeds %d byte limit", limit)
	}
	return data, nil
}

// isTerminalFailure reports whether a workflow run conclusion represents a
// real failure that should trigger fail-fast. Skipped and cancelled runs are
// excluded because they are typically concurrency-group noise, not harness
// failures.
func isTerminalFailure(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	default:
		return false
	}
}

// listHarnessRunsAfter returns harness workflow runs created at or after
// the trigger time. The listing error is returned rather than swallowed:
// during the #6647 flake the pool-org installation token was rate
// limited for the whole wait, every poll's listing failed, and the wait
// reported "no recent workflow runs found" for runs that existed.
// Callers record the error and surface it in their timeout diagnostics.
//
// This is capped at the live client's single-page ListWorkflowRuns listing
// (10 newest runs), which is fine for callers that only care about the
// newest eligible run (e.g. WaitForHarnessAgent's latest-eligible-run
// selection and the timeout diagnostics): the newest runs are always on
// that page. harnessRoundPollOnce's earliest-round selection cannot rely
// on that — see listHarnessRunsAfterPaginated.
func (d *Driver) listHarnessRunsAfter(ctx context.Context, owner, repo string, after time.Time) ([]forge.WorkflowRun, error) {
	runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, harnessWorkflowFile)
	if err != nil {
		return nil, err
	}
	var matched []forge.WorkflowRun
	for _, run := range runs {
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		matched = append(matched, run)
	}
	return matched, nil
}

// listHarnessRunsAfterPaginated returns harness workflow runs created at or
// after the trigger time, like listHarnessRunsAfter, but through
// ListWorkflowRunsSince, which paginates back to that time boundary
// instead of returning only the live client's newest 10 runs.
// harnessRoundPollOnce's earliest-round selection needs every eligible,
// not-yet-consumed run: ten newer harness runs — for this agent or others
// — can otherwise push an earlier eligible run off ListWorkflowRuns's
// single page, so sorting and checking consumed can no longer recover it
// and a later successful round is returned instead of the correct one
// (#7996 review).
func (d *Driver) listHarnessRunsAfterPaginated(ctx context.Context, owner, repo string, after time.Time) ([]forge.WorkflowRun, error) {
	runs, err := d.Client.ListWorkflowRunsSince(ctx, owner, repo, harnessWorkflowFile, after)
	if err != nil {
		return nil, err
	}
	var matched []forge.WorkflowRun
	for _, run := range runs {
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		matched = append(matched, run)
	}
	return matched, nil
}

// formatArtifactDiagnostics summarises, for a timeout error, why the
// repo-level artifact lookup did not select the target artifact. Each
// name match is classified against the trigger time the poll loop used,
// so the message states which filter actually rejected it instead of
// guessing: absent from the (100-item) listing, created before the
// trigger time, unparseable created_at, or eligible — in which case the
// listing did not cause the miss and the run diagnostics are the place
// to look.
func formatArtifactDiagnostics(arts []forge.RepositoryArtifact, targetName string, after time.Time) string {
	if len(arts) == 0 {
		return "no repository artifacts listed"
	}
	var matched int
	var b strings.Builder
	for _, art := range arts {
		if art.Name != targetName {
			continue
		}
		matched++
		fmt.Fprintf(&b, "\n  artifact %d: run=%d created_at=%s ", art.ID, art.WorkflowRunID, art.CreatedAt)
		artTime, err := time.Parse(time.RFC3339, art.CreatedAt)
		switch {
		case err != nil:
			b.WriteString("(rejected: unparseable created_at)")
		case artTime.Before(after):
			fmt.Fprintf(&b, "(rejected: created before trigger time %s)", after.UTC().Format(time.RFC3339))
		default:
			b.WriteString("(eligible by trigger time; not explained by the listing — run lookup failed, run not completed or cancelled/skipped, or artifact appeared after the last poll; see the run diagnostics)")
		}
	}
	if matched == 0 {
		return fmt.Sprintf("artifact %q not among the %d listed artifacts (listing is capped at 100 newest)", targetName, len(arts))
	}
	return fmt.Sprintf("artifact %q listed %d time(s):%s", targetName, matched, b.String())
}

// harnessJobPlaceholder is the job name GitHub renders when the harness
// matrix is empty, so the matrix job never expanded into a
// "Harness run (<agent>)" job. Seeing it skipped means the harness was
// never dispatched — no amount of waiting will produce an artifact.
// describeAgentJob attributes the empty matrix through the Harness
// dispatch job's own conclusion.
const harnessJobPlaceholder = "Harness run (${{ matrix.agent }})"

// harnessDispatchJobSuffix is the job that computes the harness matrix.
// Its conclusion says whether an unexpanded matrix was a decision (the
// job succeeded with an empty matrix) or a failure of the job itself.
const harnessDispatchJobSuffix = "Harness dispatch"

// describeAgentJob reports the state of the agent's harness job in a
// run, for timeout diagnostics. It distinguishes the job having run
// (with its conclusion), the matrix never having expanded (attributed
// through the Harness dispatch job's own conclusion), and the job simply
// not being present.
func (d *Driver) describeAgentJob(ctx context.Context, owner, repo string, runID int, agent string) string {
	jobs, err, cut := withLookupBudget(ctx, func(ctx context.Context) ([]forge.WorkflowJob, error) {
		return d.Client.ListWorkflowRunJobs(ctx, owner, repo, runID)
	})
	if cut {
		return "job lookup cut short by the diagnostics budget"
	}
	if err != nil {
		return fmt.Sprintf("job lookup failed: %v", err)
	}
	if len(jobs) == 0 {
		return "no jobs listed for run (not populated yet?)"
	}
	suffix := harnessJobSuffix(agent)
	var placeholder, dispatch *forge.WorkflowJob
	for i := range jobs {
		j := &jobs[i]
		switch {
		case strings.HasSuffix(j.Name, suffix):
			return fmt.Sprintf("agent job %q status=%s conclusion=%s", j.Name, j.Status, j.Conclusion)
		case strings.HasSuffix(j.Name, harnessJobPlaceholder):
			placeholder = j
		case strings.HasSuffix(j.Name, harnessDispatchJobSuffix):
			dispatch = j
		}
	}
	if placeholder == nil {
		return fmt.Sprintf("no %q job in run", suffix)
	}
	head := fmt.Sprintf("harness matrix not expanded (job %q conclusion=%s)", placeholder.Name, placeholder.Conclusion)
	switch {
	case placeholder.Conclusion == "cancelled":
		// An empty matrix renders as a skipped placeholder; a cancelled
		// one means the run ended before the job was evaluated, which
		// says nothing about the matrix.
		return head + ": the run was cancelled before the matrix was evaluated, not dispatched with an empty matrix; see the run"
	case placeholder.Conclusion != "skipped":
		// Neither shape we can attribute (e.g. failure, timed_out):
		// report the observation only.
		return head + fmt.Sprintf(": the matrix job concluded %s without expanding; see the run", placeholder.Conclusion)
	case dispatch == nil:
		return head + fmt.Sprintf(": no %q job in run to attribute it to", harnessDispatchJobSuffix)
	case dispatch.Conclusion == "success":
		return head + fmt.Sprintf(": the %q job succeeded with an empty matrix, so no harness was dispatched for this event (possible causes include no registered harness triggers, no trigger matching the event, the actor's role not resolving or not being authorized, or a kill switch); see that job's log", dispatch.Name)
	default:
		return head + fmt.Sprintf(": the %q job concluded %s; see its log", dispatch.Name, dispatch.Conclusion)
	}
}

// withLookupBudget runs one diagnostics API call under its own
// lookupBudget slice of the diagnostics envelope. cut reports that the
// call's own context had expired when it returned a deadline error —
// the same test pollErrors.record applies — so a deadline error raised
// while the context was still live (an HTTP client timeout), or under a
// context that was cancelled rather than expired, is not mistaken for
// the driver's own cutoff.
func withLookupBudget[T any](ctx context.Context, call func(context.Context) (T, error)) (v T, err error, cut bool) {
	ctx, cancel := context.WithTimeout(ctx, lookupBudget)
	defer cancel()
	v, err = call(ctx)
	cut = err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded)
	return v, err, cut
}

// formatRunDiagnosticsWithJobs lists the harness runs after the trigger
// time with the agent's job state for every completed run, so a timeout
// caused by the harness never being dispatched reads as such instead of
// as a mysteriously successful run with no artifact.
func (d *Driver) formatRunDiagnosticsWithJobs(ctx context.Context, owner, repo, agent string, runs []forge.WorkflowRun) string {
	if len(runs) == 0 {
		return "no recent workflow runs found after trigger time"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "recent workflow runs (%d):", len(runs))
	for _, r := range runs {
		fmt.Fprintf(&b, "\n  run %d: status=%s conclusion=%s url=%s",
			r.ID, r.Status, r.Conclusion, r.HTMLURL)
		if r.Status == "completed" {
			b.WriteString("; " + d.describeAgentJob(ctx, owner, repo, r.ID, agent))
		}
	}
	return b.String()
}

// pollErrors tracks how often a call failed during a wait loop. The
// loops keep polling through failures — a transient error must not end
// a scenario — but the timeout message must say the wait was blind, not
// that nothing existed. Calls cut short by the poll's own wait budget
// are counted separately: they are the driver's doing, not an API
// failure, and must never be presented as one.
type pollErrors struct {
	calls    int
	failed   int
	cutShort int
	last     error
}

// record classifies the outcome of one call made under ctx.
func (p *pollErrors) record(ctx context.Context, err error) {
	p.calls++
	if err == nil {
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
		p.cutShort++
		return
	}
	p.failed++
	p.last = err
}

// lastDelayRe strips the jittered retry delay the GitHub client embeds
// in its exhausted-retry errors, so consecutive rate-limit errors
// compare equal.
var lastDelayRe = regexp.MustCompile(`\s*\(last delay: [^)]*\)`)

func sameError(a, b error) bool {
	return a != nil && b != nil && lastDelayRe.ReplaceAllString(a.Error(), "") == lastDelayRe.ReplaceAllString(b.Error(), "")
}

// describe renders the failure tail for a timeout message. current is
// the error from the same lookup made while building the diagnostics;
// when it repeats the last recorded error, the text is not printed
// twice. what names the counted unit ("polls", "run lookups").
func (p pollErrors) describe(current error, what string) string {
	var b strings.Builder
	if p.failed > 0 {
		if sameError(current, p.last) {
			fmt.Fprintf(&b, " (same error on %d of %d %s during the wait)", p.failed, p.calls, what)
		} else {
			fmt.Fprintf(&b, " (failed on %d of %d %s during the wait; last: %v)", p.failed, p.calls, what, p.last)
		}
	}
	if p.cutShort > 0 {
		fmt.Fprintf(&b, " (%d of %d %s cut short by the wait budget)", p.cutShort, p.calls, what)
	}
	return b.String()
}

// harnessTimeoutDiagnostics builds the diagnostic tail of a harness-wait
// timeout error: the runs after the trigger time with the agent's job
// state, the artifact-listing classification, and the errors recorded
// during the wait. Lookups made here can fail the same way the polls
// did (the token may still be rate limited), so each gets its own
// lookupBudget inside the timeoutDiagnosticsBudget envelope, and a
// lookup that is itself cut short reports the last poll error as the
// headline rather than its own cutoff.
func (d *Driver) harnessTimeoutDiagnostics(ctx context.Context, owner, repo, agent string, after time.Time, runsErrs, artifactErrs, lookupErrs pollErrors) string {
	ctx, cancel := context.WithTimeout(ctx, timeoutDiagnosticsBudget)
	defer cancel()

	// Both single-call listings go first: the per-run job lookups that
	// follow can each spend a lookupBudget, and the artifact
	// classification must not be the section that gets starved.
	runs, runsErr, runsCut := withLookupBudget(ctx, func(ctx context.Context) ([]forge.WorkflowRun, error) {
		return d.listHarnessRunsAfter(ctx, owner, repo, after)
	})
	arts, artsErr, artsCut := withLookupBudget(ctx, func(ctx context.Context) ([]forge.RepositoryArtifact, error) {
		return d.Client.ListRepositoryArtifacts(ctx, owner, repo, 100)
	})

	var b strings.Builder
	switch {
	case runsCut:
		b.WriteString("run listing cut short by the diagnostics budget" + lastPollError(runsErrs))
	case runsErr != nil:
		fmt.Fprintf(&b, "run listing failed: %v%s", runsErr, runsErrs.describe(runsErr, "polls"))
	default:
		b.WriteString(d.formatRunDiagnosticsWithJobs(ctx, owner, repo, agent, runs))
		b.WriteString(runsErrs.describe(nil, "polls"))
	}
	switch {
	case artsCut:
		b.WriteString("; artifact listing cut short by the diagnostics budget" + lastPollError(artifactErrs))
	case artsErr != nil:
		fmt.Fprintf(&b, "; artifact listing failed: %v%s", artsErr, artifactErrs.describe(artsErr, "polls"))
	default:
		b.WriteString("; " + formatArtifactDiagnostics(arts, "fullsend-"+agent, after))
		b.WriteString(artifactErrs.describe(nil, "polls"))
	}
	if tail := lookupErrs.describe(nil, "lookups"); tail != "" {
		b.WriteString("; run lookups" + tail)
	}
	return b.String()
}

// lastPollError renders the informative error recorded during the wait
// for a lookup that the diagnostics budget cut short, if there is one.
func lastPollError(p pollErrors) string {
	if p.last == nil {
		return ""
	}
	return fmt.Sprintf("; last poll error (%d of %d polls failed): %v", p.failed, p.calls, p.last)
}

// harnessJobSuffix returns the job name suffix used by the harness workflow
// matrix for a given agent. The full job name includes a caller prefix
// (e.g. "dispatch / Harness run (pr-ping)"), so callers use HasSuffix or
// Contains to match.
func harnessJobSuffix(agent string) string {
	return "Harness run (" + agent + ")"
}

// harnessJobNameMarker matches any harness matrix job name, per-agent
// (e.g. "Harness run (triage)") or the unexpanded-matrix placeholder
// (harnessJobPlaceholder). Its presence in a run's job list means the
// harness dispatch job has produced matrix output — either concrete
// per-agent jobs or a deliberate empty matrix — as opposed to a run whose
// dispatch job is still computing the matrix, where no such job exists yet.
const harnessJobNameMarker = "Harness run ("

// builtinRoleJobName returns the static job name reusable-dispatch.yml
// assigns to one of fullsend's built-in stages (e.g. "Triage", "Code"),
// or "" if agent is not one of config.ValidAgentNames(). Built-in stages
// run as fixed per-role jobs, unlike a user-registered custom harness,
// which runs as a single matrix job named "Harness run (<agent>)"
// (harnessJobSuffix). Both forms are prefixed by the caller job id when
// surfaced through the Jobs API (e.g. "dispatch / Triage", "dispatch /
// Harness run (pr-ping)"), so matching uses HasSuffix either way.
func builtinRoleJobName(agent string) string {
	for _, name := range config.ValidAgentNames() {
		if name == agent && agent != "" {
			return strings.ToUpper(agent[:1]) + agent[1:]
		}
	}
	return ""
}

// matchAgentJob reports whether jobs contains the harness job for agent,
// returning the matched job when found. agent may name either a
// user-registered custom harness (job "Harness run (<agent>)") or one of
// the built-in stages (job named after the stage, e.g. "Triage"); both
// forms are checked (#7957).
//
// A single job list can contain both forms for the same agent name: a
// built-in stage's static job exists on every run but is skipped (or, on
// a concurrency-group cancellation unrelated to whether a same-named
// custom harness matrix job actually ran, cancelled) when that trigger
// did not schedule the stage, while a same-named user-registered custom
// harness runs as a separate matrix job that can actually execute.
// Scanning stops at the first match regardless of its state would let
// job-list order alone decide the outcome: a built-in job that already
// succeeded could shadow a same-named custom job that is still running or
// has genuinely failed, letting round polling report success prematurely
// and dispatch counting settle a count while the custom job is still in
// flight (#7996 review). To avoid that, every matching job is ranked by
// jobMatchTier and the highest-tier match wins regardless of list order:
// a still-running match outranks any completed one (the round is not
// settled yet), a genuine failure outranks a success (it must not be
// hidden), and a success outranks a weak (skipped or cancelled) match.
// Only when every match is weak does the function report one of those,
// picked arbitrarily by job-list order between a skipped and a cancelled
// match. That arbitrary pick is fine for the fail-fast/superseding
// callers, which only care whether the match is weak at all, but it is
// not a reliable evidence source for a negative assertion that must tell
// skipped and cancelled apart (#7996 review) — such callers use
// matchingAgentJobs directly instead (e.g. AssertNoHarnessAgentArtifact,
// which must still treat a cancelled match as possible execution even
// when a skipped match for the same agent is also present).
func matchAgentJob(jobs []forge.WorkflowJob, agent string) (bool, forge.WorkflowJob) {
	matches := matchingAgentJobs(jobs, agent)
	found := false
	var best forge.WorkflowJob
	bestTier := -1
	for _, j := range matches {
		tier := jobMatchTier(j)
		if !found || tier > bestTier {
			found = true
			best = j
			bestTier = tier
		}
	}
	return found, best
}

// jobMatchTier ranks a matched job by how decisive its state is for
// matchAgentJob's selection among multiple same-named matches (#7996
// review). From most to least decisive: a still-running job (the
// candidate is not settled yet, regardless of what any completed sibling
// shows), a genuine terminal failure (must not be hidden behind a
// sibling's success), a success, and finally a weak (skipped or
// cancelled) match, which carries no information about whether the agent
// actually ran.
func jobMatchTier(j forge.WorkflowJob) int {
	switch {
	case j.Status != "completed":
		return 3
	case isWeakJobMatch(j):
		return 0
	case j.Conclusion == "success":
		return 1
	default:
		return 2
	}
}

// matchingAgentJobs returns every job in jobs whose name matches the
// harness job for agent — either a user-registered custom harness job
// ("Harness run (<agent>)") or a built-in stage's static job (e.g.
// "Triage") — per matchAgentJob's doc. Unlike matchAgentJob, which
// collapses the list down to a single representative job for callers
// that need one "best" match, this returns every match so a caller that
// must not let one weak match (skipped or cancelled) shadow another can
// inspect them independently (AssertNoHarnessAgentArtifact, #7996 review).
func matchingAgentJobs(jobs []forge.WorkflowJob, agent string) []forge.WorkflowJob {
	suffix := harnessJobSuffix(agent)
	builtinSuffix := builtinRoleJobName(agent)
	var matches []forge.WorkflowJob
	for _, j := range jobs {
		if strings.HasSuffix(j.Name, suffix) || (builtinSuffix != "" && strings.HasSuffix(j.Name, builtinSuffix)) {
			matches = append(matches, j)
		}
	}
	return matches
}

// isWeakJobMatch reports whether j is a completed job that was either
// skipped (the built-in stage wasn't scheduled for this trigger) or
// cancelled. A built-in static job and a same-named custom harness
// matrix job run in distinct concurrency groups, so the static job's
// cancellation says nothing about whether the matrix job executed
// (#7957 review, fifth pass): matchAgentJob must let a stronger
// (pending, successful, or genuinely failed) match found elsewhere in
// the job list supersede either kind of weak match, the same way it
// already did for skipped alone.
func isWeakJobMatch(j forge.WorkflowJob) bool {
	return j.Status == "completed" && (j.Conclusion == "skipped" || j.Conclusion == "cancelled")
}

// isBuiltinRoleMatch reports whether job matched agent through its
// built-in stage name (e.g. "Triage", builtinRoleJobName) rather than
// through the user-registered custom-harness matrix job name
// ("Harness run (<agent>)", harnessJobSuffix). The built-in stage and a
// same-named custom harness run in distinct jobs — one static, one a
// matrix entry computed by the independent Harness dispatch job — so the
// built-in job's own conclusion, whatever it is (success, a genuine
// failure, or a weak skip/cancel), says nothing about whether a custom
// harness job will also appear for this run until the matrix itself
// resolves. Callers combine this with harnessMatrixUnresolved to decide
// whether a built-in match is decisive yet (#7996 review).
func isBuiltinRoleMatch(j forge.WorkflowJob, agent string) bool {
	return !strings.HasSuffix(j.Name, harnessJobSuffix(agent))
}

// harnessMatrixExpanded reports whether jobs shows that the harness
// workflow's matrix has resolved — either into concrete per-agent
// "Harness run (<agent>)" jobs, or into the unexpanded-matrix placeholder
// for a deliberately empty matrix. Until one of these appears, the
// dispatch job that computes the matrix is still running, so the absence
// of any particular agent's job in jobs says nothing about whether that
// agent will be scheduled.
func harnessMatrixExpanded(jobs []forge.WorkflowJob) bool {
	for _, j := range jobs {
		if strings.Contains(j.Name, harnessJobNameMarker) {
			return true
		}
	}
	return false
}

// harnessMatrixUnresolved reports whether the harness dispatch decision is
// still in progress. An unrelated expanded matrix is not sufficient evidence
// that the requested built-in stage has been scheduled: the Route job can
// still be pending while another matrix is already visible. Keep the
// candidate inconclusive until Route completes or the requested matrix has
// resolved (#7996 review).
//
// Route and the Harness dispatch job (harnessDispatchJobSuffix) are
// independent: Route decides whether built-in stages are scheduled, while
// Harness dispatch computes and expands the custom-harness matrix. Neither
// job's completion proves the matrix has resolved: downstream job
// visibility can lag the producer jobs' completion. Until expanded matrix
// jobs or the empty-matrix placeholder are visible
// (harnessMatrixExpanded), the matrix is therefore unresolved regardless of
// the Route and Harness dispatch jobs' states (#7996 review).
func harnessMatrixUnresolved(jobs []forge.WorkflowJob) bool {
	if !harnessMatrixExpanded(jobs) {
		return true
	}
	for _, j := range jobs {
		if strings.HasSuffix(j.Name, "Route") && j.Status != "completed" {
			return true
		}
	}
	return false
}

// runHasAgentJob reports whether the given workflow run contains a job
// whose name matches the harness job for agent. It also returns the
// matched job when found, and any error from the API call.
func (d *Driver) runHasAgentJob(ctx context.Context, owner, repo string, runID int, agent string) (bool, forge.WorkflowJob, error) {
	jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, runID)
	if err != nil {
		return false, forge.WorkflowJob{}, fmt.Errorf("list jobs for run %d: %w", runID, err)
	}
	hasJob, job := matchAgentJob(jobs, agent)
	return hasJob, job, nil
}

// WaitForHarnessAgent waits for a successful harness-run workflow job for
// the named agent. It fails fast only when a workflow run that contains the
// agent's "Harness run (<agent>)" job reaches a terminal failure conclusion
// and no newer run for the same agent is still pending or has already
// succeeded. A dual-dispatch race can leave an earlier sibling concluding
// failure while a later run of the same agent goes on to succeed (#7574).
// Sibling workflow runs that do not schedule the agent's job are ignored.
// On a fail-fast the failed run is returned together with the error, so
// the caller can save its logs before the scenario's repository is deleted.
//
// Listing failures never end the wait — the loop keeps polling — but they
// are counted and reported on timeout so that a wait spent rate limited
// is not mistaken for a harness that never ran (#6697).
func (d *Driver) WaitForHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error) {
	deadline := d.now().Add(dispatchWait)
	interval := dispatchPollInit
	var artifactErrs, runsErrs, lookupErrs pollErrors
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(interval):
		}
		interval = nextBackoff(interval, dispatchPollMax)
		// One clock reading decides both whether to poll and how much
		// budget the poll gets, so the two cannot disagree.
		remaining := deadline.Sub(d.now())
		if remaining < pollMinBudget {
			break
		}
		run, done, err := d.harnessPollOnce(ctx, remaining, owner, repo, agent, after, &artifactErrs, &runsErrs, &lookupErrs)
		if done {
			return run, err
		}
	}

	return nil, fmt.Errorf("harness agent %q did not complete successfully; %s",
		agent, d.harnessTimeoutDiagnostics(ctx, owner, repo, agent, after, runsErrs, artifactErrs, lookupErrs))
}

// harnessPollOnce performs one WaitForHarnessAgent poll with its API
// calls bounded by the remaining wait budget, so the client's rate-limit
// retries (up to five, each waiting on Retry-After) cannot stretch a
// poll past the deadline — attempt 1 of the #6647 flake ran 26 minutes
// against a 12-minute dispatchWait. done reports whether the wait should
// end with the returned run/error.
func (d *Driver) harnessPollOnce(ctx context.Context, remaining time.Duration, owner, repo, agent string, after time.Time, artifactErrs, runsErrs, lookupErrs *pollErrors) (run *forge.WorkflowRun, done bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	artifactName := "fullsend-" + agent

	// Quick-success: check for the agent's artifact (a completed
	// harness job uploads fullsend-{agent}).
	arts, err := d.Client.ListRepositoryArtifacts(ctx, owner, repo, 100)
	artifactErrs.record(ctx, err)
	if err == nil {
		if art := selectRepositoryArtifactAfter(arts, artifactName, after); art != nil {
			candidate, err := d.Client.GetWorkflowRun(ctx, owner, repo, art.WorkflowRunID)
			lookupErrs.record(ctx, err)
			if err == nil && candidate.Status == "completed" {
				if candidate.Conclusion == "success" {
					return candidate, true, nil
				}
				// The harness workflow uploads fullsend-{agent} with
				// if: always(), so a dual-dispatch sibling — cancelled,
				// skipped, or a genuine failure — can produce this
				// artifact while a later run for the same agent is
				// still going or has already succeeded.
				// selectRepositoryArtifactAfter only returns the single
				// highest-ID matching artifact, and artifact IDs are
				// assigned at upload time — if the non-success run
				// finishes uploading after the successful run, its
				// artifact keeps winning that selection on every poll,
				// hiding the success artifact from this branch
				// entirely. Scan the other matching artifacts for one
				// whose run already completed successfully before
				// deciding how to treat this artifact's conclusion —
				// otherwise a cancelled/skipped artifact that keeps
				// winning the max-ID selection would hide a
				// lower-ID success forever, since the early return
				// below never reaches this scan.
				if success := d.harnessArtifactRunSuccess(ctx, owner, repo, arts, artifactName, after, art.ID, lookupErrs); success != nil {
					return success, true, nil
				}
				// Cancelled/skipped runs are concurrency-group noise —
				// the superseding run will produce its own artifact.
				// Keep polling without a fail-fast scan this round.
				if isConcurrencySuperseded(candidate.Conclusion) {
					return nil, false, nil
				}
				// Apply the same supersede check as the recentRuns
				// job-scan branch below (#7574) before treating this
				// artifact's run as authoritative.
				recentRuns, runsErr := d.listHarnessRunsAfter(ctx, owner, repo, after)
				runsErrs.record(ctx, runsErr)
				if runsErr != nil {
					// Listing failures never end the wait (doc comment
					// above): an empty recentRuns from the error would
					// make hasSupersedingAgentRun report false and fall
					// through to the fail-fast return below on
					// incomplete information. Keep polling instead.
					return nil, false, nil
				}
				if d.hasSupersedingAgentRun(ctx, owner, repo, agent, *candidate, recentRuns, lookupErrs) {
					return nil, false, nil
				}
				return candidate, true, fmt.Errorf("harness run for %q concluded with %q (run %d: %s)",
					agent, candidate.Conclusion, candidate.ID, candidate.HTMLURL)
			}
		}
	}

	// Fail-fast: check recent harness runs for terminal failures,
	// but only attribute failure to runs that actually scheduled
	// this agent's harness job. A newer pending or successful run
	// for the same agent supersedes an earlier failure (#7574).
	recentRuns, err := d.listHarnessRunsAfter(ctx, owner, repo, after)
	runsErrs.record(ctx, err)
	for _, r := range recentRuns {
		if r.Status != "completed" || !isTerminalFailure(r.Conclusion) {
			continue
		}
		hasJob, job, err := d.runHasAgentJob(ctx, owner, repo, r.ID, agent)
		lookupErrs.record(ctx, err)
		// builtinRoleJobName (#7957) means hasJob is also true for a
		// built-in stage's job that exists on every run but was skipped
		// for this particular trigger (e.g. the workflow schedules a
		// "Fix" job that only runs conditionally). The run's overall
		// failure can come entirely from a sibling job in that case, so
		// only attribute it to agent when agent's own job actually ran
		// and concluded a real failure.
		if !hasJob || !isTerminalFailure(job.Conclusion) {
			continue
		}
		if d.hasSupersedingAgentRun(ctx, owner, repo, agent, r, recentRuns, lookupErrs) {
			continue
		}
		failed := r
		return &failed, true, fmt.Errorf("harness agent %q: workflow run %d concluded with %q before producing artifact (url=%s)",
			agent, r.ID, r.Conclusion, r.HTMLURL)
	}
	return nil, false, nil
}

// WaitForHarnessAgentRound waits for the named agent's harness run to
// succeed, selecting the earliest eligible run not in consumed instead of
// WaitForHarnessAgent's latest-eligible-run selection (see interface doc,
// #7957). Unlike WaitForHarnessAgent, it does not attempt dual-dispatch
// fail-fast reconciliation (#7574): a playback scenario's harness runs are
// driven by the dummy runtime rather than racing real webhook redelivery,
// so a terminal failure on an eligible, not-yet-consumed run is reported
// directly.
func (d *Driver) WaitForHarnessAgentRound(ctx context.Context, owner, repo, agent string, after time.Time, consumed map[int]bool) (*forge.WorkflowRun, error) {
	deadline := d.now().Add(dispatchWait)
	interval := dispatchPollInit
	var runsErrs, lookupErrs pollErrors
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(interval):
		}
		interval = nextBackoff(interval, dispatchPollMax)
		remaining := deadline.Sub(d.now())
		if remaining < pollMinBudget {
			break
		}
		run, done, err := d.harnessRoundPollOnce(ctx, remaining, owner, repo, agent, after, consumed, &runsErrs, &lookupErrs)
		if done {
			return run, err
		}
	}
	return nil, fmt.Errorf("harness agent %q did not complete a new round successfully; %s",
		agent, d.harnessTimeoutDiagnostics(ctx, owner, repo, agent, after, runsErrs, pollErrors{}, lookupErrs))
}

// harnessRoundPollOnce performs one WaitForHarnessAgentRound poll. It scans
// runs created at or after `after`, oldest first, skipping any run already
// in consumed, and settles on the first one that schedules agent's job:
// success returns it; a genuine terminal failure (not skipped/cancelled)
// returns it with an error; a still-in-flight job for that earliest
// candidate holds off considering any later run until it settles, so an
// earlier round cannot be skipped over while still pending. An earliest
// candidate whose matrix has not resolved yet (harnessMatrixExpanded) and
// has not reached a terminal run state is treated the same way: agent's
// absence from its current job list does not yet rule out scheduling, so a
// later, already-resolved run must not be allowed to win selection instead
// (#7957 review). The same unresolved-matrix interval also applies when
// matchAgentJob reports a match on a built-in stage job (e.g. "Triage"):
// that static job exists on every run regardless of whether a same-named
// custom harness matrix job will later run on this candidate, so its
// conclusion — skipped, cancelled, a success, or a genuine failure — does
// not rule out scheduling until either the run terminates or its matrix
// resolves (#7957 review, second pass; cancelled static jobs added #7996
// review; success/failure built-in matches added #7996 review, second
// pass).
func (d *Driver) harnessRoundPollOnce(ctx context.Context, remaining time.Duration, owner, repo, agent string, after time.Time, consumed map[int]bool, runsErrs, lookupErrs *pollErrors) (run *forge.WorkflowRun, done bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()

	recentRuns, err := d.listHarnessRunsAfterPaginated(ctx, owner, repo, after)
	runsErrs.record(ctx, err)
	if err != nil {
		return nil, false, nil
	}

	ordered := make([]forge.WorkflowRun, len(recentRuns))
	copy(ordered, recentRuns)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && workflowRunNewer(ordered[j-1], ordered[j]); j-- {
			ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
		}
	}

	for i := range ordered {
		r := ordered[i]
		if consumed[r.ID] {
			continue
		}
		jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, r.ID)
		lookupErrs.record(ctx, err)
		if err != nil {
			// The earliest not-yet-consumed candidate's job state is
			// unknown — do not let the scan fall through to a later,
			// already-resolved run while that is still uncertain
			// (#7957 review): a transient lookup failure here must not
			// let a later round win selection out of order.
			return nil, false, nil
		}
		hasJob, job := matchAgentJob(jobs, agent)
		matrixUnresolved := r.Status != "completed" && harnessMatrixUnresolved(jobs)
		if !hasJob {
			if matrixUnresolved {
				// The dispatch job that computes this run's matrix has
				// not resolved yet, and the run itself has not reached
				// a terminal state — agent's absence from jobs says
				// nothing about whether it will still be scheduled on
				// this earliest not-yet-consumed run (harnessMatrixExpanded
				// doc, #7957 review). Wait rather than letting a later,
				// already-resolved run win selection out of order.
				return nil, false, nil
			}
			continue
		}
		if matrixUnresolved && (isWeakJobMatch(job) || isBuiltinRoleMatch(job, agent)) {
			// matchAgentJob matched a built-in stage's static job — e.g.
			// "Triage" — which exists on every run independent of whether
			// a same-named custom harness matrix job will run on this
			// candidate. Until the matrix resolves or the run terminates,
			// that job's conclusion does not rule out scheduling here,
			// regardless of whether it is weak (skipped/cancelled) or
			// already settled (success or a genuine failure): a success
			// must not return this round prematurely while the
			// independent Harness dispatch job is still computing the
			// matrix, and a failure must not be reported before the real
			// custom-harness job is known either. Treat it the same as
			// the !hasJob case above rather than letting a completed
			// built-in conclusion settle this candidate and hand
			// selection to a later run (#7957 review, second pass;
			// cancelled static jobs added #7996 review; success/failure
			// built-in matches added #7996 review, second pass).
			return nil, false, nil
		}
		if job.Status != "completed" {
			// The earliest not-yet-consumed candidate that schedules
			// agent is still running; wait for it rather than
			// considering a later run out of order.
			return nil, false, nil
		}
		if job.Conclusion == "success" {
			found := r
			return &found, true, nil
		}
		if isConcurrencySuperseded(job.Conclusion) {
			continue
		}
		failed := r
		return &failed, true, fmt.Errorf("harness agent %q: workflow run %d's job concluded with %q (url=%s)",
			agent, r.ID, job.Conclusion, r.HTMLURL)
	}
	return nil, false, nil
}

// harnessArtifactRunSuccess scans arts for a matching artifact — other than
// skipID, the one already selected as the highest-ID match — that was
// created at or after after and belongs to a completed, successful
// workflow run. selectRepositoryArtifactAfter only ever returns the single
// highest-ID artifact for a given name, but the harness workflow uploads
// fullsend-{agent} with if: always(), so a dual-dispatch sibling that
// concludes failure, cancelled, or skipped can win that selection over an
// already-succeeded run's own artifact whenever the non-success run
// finishes uploading later (#7574). This scan finds that hidden success
// without waiting for its artifact to eventually outrank the other run's
// by ID, which it may never do — including when the higher-ID artifact
// belongs to a cancelled/skipped run, which would otherwise keep winning
// the max-ID selection on every poll and hide the success forever.
func (d *Driver) harnessArtifactRunSuccess(ctx context.Context, owner, repo string, arts []forge.RepositoryArtifact, name string, after time.Time, skipID int, lookupErrs *pollErrors) *forge.WorkflowRun {
	for _, art := range arts {
		if art.Name != name || art.ID == skipID {
			continue
		}
		artTime, parseErr := time.Parse(time.RFC3339, art.CreatedAt)
		if parseErr != nil || artTime.Before(after) {
			continue
		}
		run, err := d.Client.GetWorkflowRun(ctx, owner, repo, art.WorkflowRunID)
		lookupErrs.record(ctx, err)
		if err == nil && run.Status == "completed" && run.Conclusion == "success" {
			return run
		}
	}
	return nil
}

// workflowRunNewer reports whether a was created after b. CreatedAt is
// compared when both parse as RFC3339; otherwise run ID is the tiebreak
// (GitHub run IDs are monotonic).
func workflowRunNewer(a, b forge.WorkflowRun) bool {
	at, aErr := time.Parse(time.RFC3339, a.CreatedAt)
	bt, bErr := time.Parse(time.RFC3339, b.CreatedAt)
	if aErr == nil && bErr == nil && !at.Equal(bt) {
		return at.After(bt)
	}
	return a.ID > b.ID
}

// hasSupersedingAgentRun reports whether recentRuns contains a run newer
// than failed that either has scheduled agent's harness job and that job
// is still pending or concluded success, or cannot yet be ruled out as
// such. Dual-dispatch can leave an earlier sibling concluding failure
// while a later run of the same agent is still going, still expanding its
// job matrix, or has already succeeded; fail-fast must not treat the
// earlier failure as authoritative in any of these cases (#7574). A
// candidate is only confirmed not to supersede once either its own
// matching job has itself completed without succeeding (cancelled,
// skipped, or failure — the overall run's conclusion does not decide
// this, only the agent's own job does), or its matrix has resolved
// (harnessMatrixExpanded) without scheduling the agent at all.
//
// A job-listing error on a candidate, or a candidate whose matrix has not
// resolved yet, leaves that candidate's outcome unknown; both are treated
// as inconclusive (return true) so the caller keeps polling instead of
// fail-fasting on incomplete information. The same inconclusive treatment
// applies when the only match is a skipped or cancelled built-in stage job
// (e.g. "Triage") on a still-executing candidate whose matrix has not
// resolved: a same-named custom harness matrix job can still appear on it
// (matchAgentJob doc), so that conclusion alone does not yet rule the
// candidate out as superseding (#7957 review, third pass — mirrors
// harnessRoundPollOnce's guard; cancelled static jobs added #7996 review).
func (d *Driver) hasSupersedingAgentRun(ctx context.Context, owner, repo, agent string, failed forge.WorkflowRun, recentRuns []forge.WorkflowRun, lookupErrs *pollErrors) bool {
	for _, other := range recentRuns {
		if other.ID == failed.ID || !workflowRunNewer(other, failed) {
			continue
		}
		if other.Status == "completed" && other.Conclusion != "success" {
			continue
		}
		jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, other.ID)
		lookupErrs.record(ctx, err)
		if err != nil {
			return true
		}
		if hasJob, job := matchAgentJob(jobs, agent); hasJob {
			// The overall run can conclude "success" while this agent's
			// own job was skipped or cancelled (other matrix jobs
			// succeeded) — that must not suppress fail-fast on a genuine
			// earlier failure. Only a still-pending or successfully
			// concluded agent job supersedes.
			if job.Status != "completed" || job.Conclusion == "success" {
				return true
			}
			if (isWeakJobMatch(job) || isBuiltinRoleMatch(job, agent)) && other.Status != "completed" && harnessMatrixUnresolved(jobs) {
				return true
			}
			continue
		}
		if other.Status != "completed" && harnessMatrixUnresolved(jobs) {
			return true
		}
	}
	return false
}

// WaitForFailedHarnessAgent waits for the named agent's harness run to
// complete with a terminal failure conclusion. It errors out early when
// the run completes successfully instead — callers use this to assert a
// refuse-to-push (or similar fail-closed) path.
//
// Detection is artifact-first: the fullsend action uploads the
// fullsend-<agent> artifact with `if: always()`, so it exists for failed
// runs too, and it is the only signal that works for both standard stage
// jobs (named e.g. "Code"/"Fix") and custom-harness matrix jobs (named
// "Harness run (<agent>)"). The job-name scan is a fallback for
// custom-harness runs that failed before uploading the artifact.
//
// Listing failures are recorded and reported on timeout, as in
// WaitForHarnessAgent.
func (d *Driver) WaitForFailedHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error) {
	deadline := d.now().Add(dispatchWait)
	interval := dispatchPollInit
	var artifactErrs, runsErrs, lookupErrs pollErrors
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(interval):
		}
		interval = nextBackoff(interval, dispatchPollMax)
		remaining := deadline.Sub(d.now())
		if remaining < pollMinBudget {
			break
		}
		run, done, err := d.failedHarnessPollOnce(ctx, remaining, owner, repo, agent, after, &artifactErrs, &runsErrs, &lookupErrs)
		if done {
			return run, err
		}
	}

	return nil, fmt.Errorf("harness agent %q did not complete with a failure; %s",
		agent, d.harnessTimeoutDiagnostics(ctx, owner, repo, agent, after, runsErrs, artifactErrs, lookupErrs))
}

// failedHarnessPollOnce performs one WaitForFailedHarnessAgent poll with
// its API calls bounded by the remaining wait budget. done reports
// whether the wait should end with the returned run/error.
func (d *Driver) failedHarnessPollOnce(ctx context.Context, remaining time.Duration, owner, repo, agent string, after time.Time, artifactErrs, runsErrs, lookupErrs *pollErrors) (run *forge.WorkflowRun, done bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	artifactName := "fullsend-" + agent

	// Artifact-first: resolve the agent's run from its artifact and
	// inspect the run conclusion.
	arts, err := d.Client.ListRepositoryArtifacts(ctx, owner, repo, 100)
	artifactErrs.record(ctx, err)
	if err == nil {
		if art := selectRepositoryArtifactAfter(arts, artifactName, after); art != nil {
			candidate, err := d.Client.GetWorkflowRun(ctx, owner, repo, art.WorkflowRunID)
			lookupErrs.record(ctx, err)
			if err == nil && candidate.Status == "completed" {
				if isTerminalFailure(candidate.Conclusion) {
					return candidate, true, nil
				}
				if candidate.Conclusion == "success" {
					return nil, true, fmt.Errorf("harness agent %q run %d concluded successfully; expected failure (url=%s)",
						agent, candidate.ID, candidate.HTMLURL)
				}
			}
		}
	}

	// Fallback: a custom-harness run can fail before uploading the
	// artifact; attribute the failure through its matrix job name.
	// Scans every completed run regardless of overall conclusion —
	// not just runs already known to have failed — so a run whose
	// agent job succeeded despite the artifact lookup missing it
	// still fails fast instead of running out the full timeout.
	recentRuns, err := d.listHarnessRunsAfter(ctx, owner, repo, after)
	runsErrs.record(ctx, err)
	for i := range recentRuns {
		r := recentRuns[i]
		if r.Status != "completed" {
			continue
		}
		hasJob, job, err := d.runHasAgentJob(ctx, owner, repo, r.ID, agent)
		lookupErrs.record(ctx, err)
		if err != nil {
			continue
		}
		if !hasJob {
			continue
		}
		if isTerminalFailure(job.Conclusion) {
			return &r, true, nil
		}
		if job.Conclusion == "success" {
			return nil, true, fmt.Errorf("harness agent %q job in run %d concluded successfully; expected failure (url=%s)",
				agent, r.ID, r.HTMLURL)
		}
		// Skipped/cancelled jobs are concurrency noise — keep polling.
	}
	return nil, false, nil
}

// CountHarnessDispatches returns the number of harness workflow runs that
// scheduled the "Harness run (<agent>)" job after the trigger time.
//
// Runs where the agent's job was cancelled or skipped are excluded.
// GitHub's concurrency groups cancel duplicate runs when two events
// (e.g. synchronize + labeled) race, and webhook at-least-once delivery
// can trigger duplicate workflow runs for the same event. In both cases
// the concurrency group cancels the superseded run, and counting it
// would produce a false-positive exact-count assertion failure (#6053).
//
// The count settles before it is returned: a run whose agent job has not
// reached a terminal conclusion — or whose job matrix has not expanded
// yet — is polled until it completes, so an in-flight duplicate is
// classified by its final conclusion instead of being counted while its
// cancellation is still in progress.
func (d *Driver) CountHarnessDispatches(ctx context.Context, owner, repo, agent string, after time.Time) (int, error) {
	deadline := time.Now().Add(dispatchWait)
	for {
		count, pending, err := d.settleHarnessDispatchCount(ctx, owner, repo, agent, after)
		if err != nil {
			return 0, err
		}
		if pending == 0 {
			return count, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("harness %q dispatch count did not settle: %d run(s) still pending after %s",
				agent, pending, dispatchWait)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(countSettlePoll):
		}
	}
}

// settleHarnessDispatchCount classifies harness runs created after the
// trigger time into counted dispatches and pending runs. A run is pending
// when its agent job exists but has not completed, or when the run itself
// is still executing and the agent's job has not appeared yet. It is also
// pending when the only match is a built-in stage job (e.g. "Triage") on a
// still-executing run whose matrix has not resolved yet, regardless of
// that job's conclusion (skipped, cancelled, a success, or a genuine
// failure): a same-named custom harness matrix job can still appear on
// this candidate (matchAgentJob doc), so that conclusion alone does not
// yet rule out — or confirm — a dispatch here. Without this, such a run
// was either silently classified as neither counted nor pending (a weak
// match, settling and undercounting before a same-named matrix job it was
// still waiting on even appeared), or counted immediately on a successful
// built-in match before the independent Harness dispatch job finished
// computing the matrix (#7957 review, third pass — mirrors
// harnessRoundPollOnce's guard; cancelled static jobs added #7996 review;
// success/failure built-in matches added #7996 review, second pass).
func (d *Driver) settleHarnessDispatchCount(ctx context.Context, owner, repo, agent string, after time.Time) (count, pending int, err error) {
	allRuns, err := d.Client.ListWorkflowRuns(ctx, owner, repo, harnessWorkflowFile)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range allRuns {
		runTime, parseErr := time.Parse(time.RFC3339, r.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, r.ID)
		if err != nil {
			return 0, 0, err
		}
		hasJob, job := matchAgentJob(jobs, agent)
		switch {
		case hasJob && job.Status != "completed":
			pending++
		case hasJob && r.Status != "completed" && harnessMatrixUnresolved(jobs) && (isWeakJobMatch(job) || isBuiltinRoleMatch(job, agent)):
			pending++
		case hasJob && !isConcurrencySuperseded(job.Conclusion):
			count++
		case !hasJob && r.Status != "completed":
			// The agent's matrix job does not exist until the Route
			// job finishes expanding the matrix, so an executing run
			// cannot be classified yet.
			pending++
		}
	}
	return count, pending, nil
}

// isConcurrencySuperseded reports whether a job conclusion indicates
// the run was superseded by a concurrency group (cancelled or skipped).
// These runs should not count toward dispatch totals because the
// platform handled deduplication correctly.
func isConcurrencySuperseded(conclusion string) bool {
	switch conclusion {
	case "cancelled", "skipped":
		return true
	default:
		return false
	}
}

// AssertNoHarnessAgentArtifact asserts that the named agent's harness job
// did not run after the trigger time. It checks workflow run jobs rather
// than artifacts so that sibling runs for other agents are not mistaken
// for evidence.
func (d *Driver) AssertNoHarnessAgentArtifact(ctx context.Context, owner, repo, agent string, after time.Time) error {
	allRuns, err := d.Client.ListWorkflowRuns(ctx, owner, repo, harnessWorkflowFile)
	if err != nil {
		return err
	}
	for _, r := range allRuns {
		runTime, parseErr := time.Parse(time.RFC3339, r.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, r.ID)
		if err != nil {
			return err
		}
		// Every matching job is inspected independently here, rather than
		// through matchAgentJob's single "best" match: a job list can
		// contain both a skipped built-in stage job (e.g. "Triage") and a
		// cancelled same-named custom harness matrix job for the same
		// agent, and matchAgentJob's tie-break — which treats skipped and
		// cancelled as equally "weak" for the fail-fast/superseding paths
		// that need one representative job — would arbitrarily keep
		// whichever came first in job-list order. A negative assertion
		// cannot depend on that order: a skipped job (builtinRoleJobName,
		// #7957) is present on every run but never evidence the agent
		// ran, while a cancelled job can mean it ran before being
		// cancelled — forge.WorkflowJob exposes no started-at metadata
		// that would let us rule that out — so a cancelled match must
		// still fail the assertion even when a skipped match for the
		// same agent is also present (#7996 review).
		for _, job := range matchingAgentJobs(jobs, agent) {
			if job.Conclusion != "skipped" {
				return fmt.Errorf("expected harness %q not to run, but job %q found in workflow run %d",
					agent, job.Name, r.ID)
			}
		}
	}
	return nil
}
