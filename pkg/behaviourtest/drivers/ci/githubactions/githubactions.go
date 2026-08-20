package githubactions

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
)

const (
	pollInterval   = 15 * time.Second
	dispatchWait   = 12 * time.Minute
	dispatchPoll   = 5 * time.Second
	dispatchMaxTry = 48

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
}

func New(client forge.Client, token string) ci.Driver {
	return &Driver{Client: client, Token: token, afterFunc: time.After}
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

func (d *Driver) WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	var triageRun *forge.WorkflowRun
	for attempt := 0; attempt < dispatchMaxTry; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(dispatchPoll):
		}
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

	deadline := time.Now().Add(dispatchWait)
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

// listHarnessRunsAfter returns recent harness workflow runs created after the
// given time. It queries only the harness workflow (fullsend.yaml) to avoid
// false-positive fail-fast from unrelated workflow failures in the same repo.
func (d *Driver) listHarnessRunsAfter(ctx context.Context, owner, repo string, after time.Time) []forge.WorkflowRun {
	runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, harnessWorkflowFile)
	if err != nil {
		return nil
	}
	var matched []forge.WorkflowRun
	for _, run := range runs {
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		matched = append(matched, run)
	}
	return matched
}

// formatRunDiagnostics builds a human-readable summary of recent workflow
// runs for inclusion in error messages.
func formatRunDiagnostics(runs []forge.WorkflowRun) string {
	if len(runs) == 0 {
		return "no recent workflow runs found after trigger time"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "recent workflow runs (%d):", len(runs))
	for _, r := range runs {
		fmt.Fprintf(&b, "\n  run %d: status=%s conclusion=%s url=%s",
			r.ID, r.Status, r.Conclusion, r.HTMLURL)
	}
	return b.String()
}

// harnessJobSuffix returns the job name suffix used by the harness workflow
// matrix for a given agent. The full job name includes a caller prefix
// (e.g. "dispatch / Harness run (pr-ping)"), so callers use HasSuffix or
// Contains to match.
func harnessJobSuffix(agent string) string {
	return "Harness run (" + agent + ")"
}

// runHasAgentJob reports whether the given workflow run contains a job
// whose name matches the harness job for agent. It also returns the
// matched job when found, and any error from the API call.
func (d *Driver) runHasAgentJob(ctx context.Context, owner, repo string, runID int, agent string) (bool, forge.WorkflowJob, error) {
	jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, runID)
	if err != nil {
		return false, forge.WorkflowJob{}, fmt.Errorf("list jobs for run %d: %w", runID, err)
	}
	suffix := harnessJobSuffix(agent)
	for _, j := range jobs {
		if strings.HasSuffix(j.Name, suffix) {
			return true, j, nil
		}
	}
	return false, forge.WorkflowJob{}, nil
}

// WaitForHarnessAgent waits for a successful harness-run workflow job for
// the named agent. It fails fast only when a workflow run that contains the
// agent's "Harness run (<agent>)" job reaches a terminal failure conclusion.
// Sibling workflow runs that do not schedule the agent's job are ignored.
func (d *Driver) WaitForHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error) {
	artifactName := "fullsend-" + agent
	deadline := time.Now().Add(dispatchWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(dispatchPoll):
		}

		// Quick-success: check for the agent's artifact (a completed
		// harness job uploads fullsend-{agent}).
		arts, err := d.Client.ListRepositoryArtifacts(ctx, owner, repo, 100)
		if err == nil {
			if art := selectRepositoryArtifactAfter(arts, artifactName, after); art != nil {
				run, err := d.Client.GetWorkflowRun(ctx, owner, repo, art.WorkflowRunID)
				if err == nil {
					if run.Status == "completed" {
						if run.Conclusion == "success" {
							return run, nil
						}
						// Cancelled/skipped runs are concurrency-group
						// noise — the superseding run will produce
						// its own artifact. Keep polling.
						if isConcurrencySuperseded(run.Conclusion) {
							continue
						}
						return nil, fmt.Errorf("harness run for %q concluded with %q (run %d: %s)",
							agent, run.Conclusion, run.ID, run.HTMLURL)
					}
				}
			}
		}

		// Fail-fast: check recent harness runs for terminal failures,
		// but only attribute failure to runs that actually scheduled
		// this agent's harness job.
		recentRuns := d.listHarnessRunsAfter(ctx, owner, repo, after)
		for _, r := range recentRuns {
			if r.Status != "completed" || !isTerminalFailure(r.Conclusion) {
				continue
			}
			hasJob, _, _ := d.runHasAgentJob(ctx, owner, repo, r.ID, agent)
			if hasJob {
				return nil, fmt.Errorf("harness agent %q: workflow run %d concluded with %q before producing artifact (url=%s)",
					agent, r.ID, r.Conclusion, r.HTMLURL)
			}
		}
	}

	// Timeout: include diagnostics about recent workflow runs.
	recentRuns := d.listHarnessRunsAfter(ctx, owner, repo, after)
	return nil, fmt.Errorf("harness agent %q did not complete successfully; %s",
		agent, formatRunDiagnostics(recentRuns))
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
func (d *Driver) WaitForFailedHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error) {
	artifactName := "fullsend-" + agent
	deadline := time.Now().Add(dispatchWait)
	var lastJobErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(dispatchPoll):
		}

		// Artifact-first: resolve the agent's run from its artifact and
		// inspect the run conclusion.
		arts, err := d.Client.ListRepositoryArtifacts(ctx, owner, repo, 100)
		if err == nil {
			if art := selectRepositoryArtifactAfter(arts, artifactName, after); art != nil {
				run, err := d.Client.GetWorkflowRun(ctx, owner, repo, art.WorkflowRunID)
				if err == nil && run.Status == "completed" {
					if isTerminalFailure(run.Conclusion) {
						return run, nil
					}
					if run.Conclusion == "success" {
						return nil, fmt.Errorf("harness agent %q run %d concluded successfully; expected failure (url=%s)",
							agent, run.ID, run.HTMLURL)
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
		lastJobErr = nil
		recentRuns := d.listHarnessRunsAfter(ctx, owner, repo, after)
		for i := range recentRuns {
			run := recentRuns[i]
			if run.Status != "completed" {
				continue
			}
			hasJob, job, err := d.runHasAgentJob(ctx, owner, repo, run.ID, agent)
			if err != nil {
				lastJobErr = err
				continue
			}
			if !hasJob {
				continue
			}
			if isTerminalFailure(job.Conclusion) {
				return &run, nil
			}
			if job.Conclusion == "success" {
				return nil, fmt.Errorf("harness agent %q job in run %d concluded successfully; expected failure (url=%s)",
					agent, run.ID, run.HTMLURL)
			}
			// Skipped/cancelled jobs are concurrency noise — keep polling.
		}
	}

	recentRuns := d.listHarnessRunsAfter(ctx, owner, repo, after)
	diag := formatRunDiagnostics(recentRuns)
	if lastJobErr != nil {
		diag += fmt.Sprintf("; last job-listing error: %v", lastJobErr)
	}
	return nil, fmt.Errorf("harness agent %q did not complete with a failure; %s", agent, diag)
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
		case <-time.After(dispatchPoll):
		}
	}
}

// settleHarnessDispatchCount classifies harness runs created after the
// trigger time into counted dispatches and pending runs. A run is pending
// when its agent job exists but has not completed, or when the run itself
// is still executing and the agent's job has not appeared yet.
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
		hasJob, job, err := d.runHasAgentJob(ctx, owner, repo, r.ID, agent)
		if err != nil {
			return 0, 0, err
		}
		switch {
		case hasJob && job.Status != "completed":
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
		hasJob, _, err := d.runHasAgentJob(ctx, owner, repo, r.ID, agent)
		if err != nil {
			return err
		}
		if hasJob {
			return fmt.Errorf("expected harness %q not to run, but job %q found in workflow run %d",
				agent, harnessJobSuffix(agent), r.ID)
		}
	}
	return nil
}
