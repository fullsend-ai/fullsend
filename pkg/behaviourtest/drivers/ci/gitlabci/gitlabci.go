package gitlabci

import (
	"archive/zip"
	"context"
	"errors"
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
	pollInterval = 15 * time.Second
	dispatchWait = 12 * time.Minute

	// Dispatch detection uses exponential backoff: the poll interval
	// starts at dispatchPollInit, doubles each iteration up to
	// dispatchPollMax, and the total detection window is dispatchTimeout.
	dispatchPollInit = 2 * time.Second
	dispatchPollMax  = 30 * time.Second
	dispatchTimeout  = 5 * time.Minute

	// countSettlePoll is the fixed poll interval used by
	// CountHarnessDispatches while waiting for in-progress runs to
	// reach a terminal state.
	countSettlePoll = 5 * time.Second

	// pollMinBudget is the least wait budget a harness poll is started
	// with. A poll that would begin with less than this is skipped.
	pollMinBudget = 5 * time.Second

	artifactRunPoll = 5 * time.Second
	artifactRunWait = 5 * time.Minute

	assertNoWorkflowChecks = 3
	assertNoWorkflowDelay  = 10 * time.Second
)

// Driver implements ci.Driver against GitLab CI pipelines.
//
// Concurrency: the Driver is an immutable wrapper around forge.Client
// (which is itself safe for concurrent use) and holds no unsynchronized
// mutable fields (Client and Token are both set at construction and
// never modified). Sharing a single Driver across goroutines via
// World.Clone is safe by design. TestConcurrentAccess exercises the
// real driver under -race with a FakeClient.
type Driver struct {
	Client forge.Client
	Token  string

	// afterFunc is the timer function used by poll loops. It defaults to
	// time.After in New(). Tests inject an instant-return implementation
	// to avoid sleeping on real wall-clock intervals.
	afterFunc func(time.Duration) <-chan time.Time

	// nowFunc is the clock used by the harness-wait deadlines. It
	// defaults to time.Now in New(). Tests inject a stepping clock so
	// the timeout branch can be exercised without waiting on the wall
	// clock.
	nowFunc func() time.Time
}

// New creates a GitLab CI driver backed by the given forge client.
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
// still works.
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

// pollErrors tracks API call outcomes during a poll loop, distinguishing
// real failures from budget-induced cancellations. This mirrors the
// pollErrors pattern used by the GitHub Actions CI driver.
type pollErrors struct {
	calls    int   // total calls made
	failed   int   // real failures (error under live context)
	cutShort int   // budget cutoffs (error under expired context)
	last     error // most recent real error for reporting
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

// describe renders the failure tail for a timeout message. current is
// the error from the same lookup made while building the diagnostics;
// when it repeats the last recorded error, the text is not printed
// twice. what names the counted unit ("polls", "run lookups").
func (p pollErrors) describe(current error, what string) string {
	var b strings.Builder
	if p.failed > 0 {
		if current != nil && p.last != nil && current.Error() == p.last.Error() {
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

// WaitForWorkflow polls for a pipeline created after the trigger time
// and waits for it to complete successfully.
func (d *Driver) WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error) {
	_ = workflowFile // GitLab pipelines are not scoped by file
	var matchedRun *forge.WorkflowRun
	var listErrs pollErrors
	deadline := d.now().Add(dispatchTimeout)
	interval := dispatchPollInit
	for d.now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(interval):
		}
		interval = nextBackoff(interval, dispatchPollMax)
		remaining := deadline.Sub(d.now())
		pollCtx, cancel := context.WithTimeout(ctx, remaining)
		runs, err := d.Client.ListWorkflowRuns(pollCtx, owner, repo, "")
		listErrs.record(pollCtx, err)
		cancel()
		if err != nil {
			continue
		}
		if candidate := selectWorkflowRun(runs, after, event); candidate != nil {
			if candidate.Status == "completed" && candidate.Conclusion != "success" {
				return nil, fmt.Errorf("pipeline %d concluded with %q during dispatch",
					candidate.ID, candidate.Conclusion)
			}
			matchedRun = candidate
			break
		}
	}
	if matchedRun == nil {
		if event != "" {
			return nil, fmt.Errorf("pipeline (%s) was not dispatched%s", event, listErrs.describe(nil, "polls"))
		}
		return nil, fmt.Errorf("pipeline was not dispatched%s", listErrs.describe(nil, "polls"))
	}

	// Wait for the pipeline to complete.
	var getErrs pollErrors
	deadline = d.now().Add(dispatchWait)
	for d.now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(pollInterval):
		}
		remaining := deadline.Sub(d.now())
		pollCtx, cancel := context.WithTimeout(ctx, remaining)
		run, err := d.Client.GetWorkflowRun(pollCtx, owner, repo, matchedRun.ID)
		getErrs.record(pollCtx, err)
		cancel()
		if err != nil {
			continue
		}
		if run.Status == "completed" {
			if run.Conclusion == "success" {
				return run, nil
			}
			// Replacement-run scanning: if a concurrency group
			// (resource_group) cancelled the tracked pipeline while a
			// newer successful pipeline appeared, use the replacement.
			if replacement := selectSuccessfulRun(latestRuns(ctx, d, owner, repo), after, event); replacement != nil && replacement.ID > matchedRun.ID {
				matchedRun = replacement
				continue
			}
			return run, fmt.Errorf("pipeline %d concluded with %q", run.ID, run.Conclusion)
		}
	}
	return nil, fmt.Errorf("pipeline %d did not complete within deadline%s", matchedRun.ID, getErrs.describe(nil, "polls"))
}

// FindCompletedWorkflowRun finds a completed successful pipeline after the
// given time.
func (d *Driver) FindCompletedWorkflowRun(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error) {
	_ = workflowFile
	var listErrs pollErrors
	deadline := d.now().Add(artifactRunWait)
	for d.now().Before(deadline) {
		remaining := deadline.Sub(d.now())
		pollCtx, cancel := context.WithTimeout(ctx, remaining)
		runs, err := d.Client.ListWorkflowRuns(pollCtx, owner, repo, "")
		listErrs.record(pollCtx, err)
		cancel()
		if err == nil {
			if run := selectCompletedSuccessRun(runs, after); run != nil {
				return run, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.timerAfter(artifactRunPoll):
		}
	}
	return nil, fmt.Errorf("no completed pipeline after %s%s", after.Format(time.RFC3339), listErrs.describe(nil, "polls"))
}

// AssertNoWorkflow asserts that no pipeline was created after the trigger
// time within a settling window.
func (d *Driver) AssertNoWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) error {
	_ = workflowFile
	for attempt := range assertNoWorkflowChecks {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-d.timerAfter(assertNoWorkflowDelay):
			}
		}
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, "")
		if err != nil {
			return err
		}
		for _, run := range runs {
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil {
				continue
			}
			if !runTime.Before(after) {
				return fmt.Errorf("unexpected pipeline %d created after trigger time", run.ID)
			}
		}
	}
	return nil
}

// GetRunLogs retrieves the concatenated logs for all jobs in a pipeline.
func (d *Driver) GetRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	return d.Client.GetWorkflowRunLogs(ctx, owner, repo, runID)
}

// DownloadArtifacts downloads all artifacts from a pipeline's jobs.
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

// DownloadNamedArtifactFromRun downloads a specific named artifact from
// a pipeline. On GitLab, artifact names correspond to job names.
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
	return fmt.Errorf("artifact %q not found on pipeline %d", artifactName, runID)
}

// DownloadNamedArtifactAfter polls for a repository-level artifact matching
// the name created after the trigger time, then downloads it.
func (d *Driver) DownloadNamedArtifactAfter(ctx context.Context, owner, repo, artifactName string, after time.Time, destDir string) error {
	var listErrs pollErrors
	deadline := d.now().Add(artifactRunWait)
	var lastNewestCreatedAt string
	for d.now().Before(deadline) {
		remaining := deadline.Sub(d.now())
		pollCtx, cancel := context.WithTimeout(ctx, remaining)
		arts, err := d.Client.ListRepositoryArtifacts(pollCtx, owner, repo, 100)
		listErrs.record(pollCtx, err)
		cancel()
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
	return fmt.Errorf("artifact %q not found after %s%s", artifactName, after.Format(time.RFC3339), listErrs.describe(nil, "polls"))
}

// harnessJobSuffix returns the job name suffix used by the harness
// pipeline for a given agent on GitLab. The naming convention mirrors
// GitHub Actions' "Harness run (<agent>)" pattern.
func harnessJobSuffix(agent string) string {
	return "Harness run (" + agent + ")"
}

// runHasAgentJob reports whether the given pipeline contains a job whose
// name matches the harness job for agent.
func (d *Driver) runHasAgentJob(ctx context.Context, owner, repo string, runID int, agent string) (bool, forge.WorkflowJob, error) {
	jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, runID)
	if err != nil {
		return false, forge.WorkflowJob{}, fmt.Errorf("list jobs for pipeline %d: %w", runID, err)
	}
	suffix := harnessJobSuffix(agent)
	for _, j := range jobs {
		if strings.HasSuffix(j.Name, suffix) {
			return true, j, nil
		}
	}
	return false, forge.WorkflowJob{}, nil
}

// isTerminalFailure reports whether a pipeline conclusion represents a
// real failure that should trigger fail-fast.
func isTerminalFailure(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	default:
		return false
	}
}

// isConcurrencySuperseded reports whether a job conclusion indicates
// the run was superseded (cancelled or skipped).
func isConcurrencySuperseded(conclusion string) bool {
	switch conclusion {
	case "cancelled", "skipped":
		return true
	default:
		return false
	}
}

// WaitForHarnessAgent waits for a successful harness-run pipeline job for
// the named agent, using artifact-first detection with job-name fallback.
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

	return nil, fmt.Errorf("harness agent %q did not complete successfully within deadline%s%s%s",
		agent, artifactErrs.describe(nil, "artifact polls"), runsErrs.describe(nil, "run polls"), lookupErrs.describe(nil, "lookups"))
}

// harnessPollOnce performs one WaitForHarnessAgent poll with its API
// calls bounded by the remaining wait budget, so the client's rate-limit
// retries cannot stretch a poll past the deadline. done reports whether
// the wait should end with the returned run/error.
func (d *Driver) harnessPollOnce(ctx context.Context, remaining time.Duration, owner, repo, agent string, after time.Time, artifactErrs, runsErrs, lookupErrs *pollErrors) (run *forge.WorkflowRun, done bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	artifactName := "fullsend-" + agent

	// Quick-success: check for the agent's artifact.
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
				if isConcurrencySuperseded(candidate.Conclusion) {
					return nil, false, nil
				}
				return nil, true, fmt.Errorf("harness run for %q concluded with %q (pipeline %d: %s)",
					agent, candidate.Conclusion, candidate.ID, candidate.HTMLURL)
			}
		}
	}

	// Fail-fast: check recent pipelines for terminal failures.
	recentRuns, err := d.Client.ListWorkflowRuns(ctx, owner, repo, "")
	runsErrs.record(ctx, err)
	if err != nil {
		return nil, false, nil
	}
	for _, r := range recentRuns {
		runTime, parseErr := time.Parse(time.RFC3339, r.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		if r.Status != "completed" || !isTerminalFailure(r.Conclusion) {
			continue
		}
		hasJob, _, jobErr := d.runHasAgentJob(ctx, owner, repo, r.ID, agent)
		lookupErrs.record(ctx, jobErr)
		if hasJob {
			return nil, true, fmt.Errorf("harness agent %q: pipeline %d concluded with %q before producing artifact (url=%s)",
				agent, r.ID, r.Conclusion, r.HTMLURL)
		}
	}
	return nil, false, nil
}

// WaitForFailedHarnessAgent waits for the named agent's harness run to
// complete with a terminal failure conclusion. It errors out early when
// the run completes successfully instead.
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

	return nil, fmt.Errorf("harness agent %q did not complete with a failure within deadline%s%s%s",
		agent, artifactErrs.describe(nil, "artifact polls"), runsErrs.describe(nil, "run polls"), lookupErrs.describe(nil, "lookups"))
}

// failedHarnessPollOnce performs one WaitForFailedHarnessAgent poll with
// its API calls bounded by the remaining wait budget. done reports
// whether the wait should end with the returned run/error.
func (d *Driver) failedHarnessPollOnce(ctx context.Context, remaining time.Duration, owner, repo, agent string, after time.Time, artifactErrs, runsErrs, lookupErrs *pollErrors) (run *forge.WorkflowRun, done bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	artifactName := "fullsend-" + agent

	// Artifact-first: resolve the agent's run from its artifact.
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
					return nil, true, fmt.Errorf("harness agent %q pipeline %d concluded successfully; expected failure (url=%s)",
						agent, candidate.ID, candidate.HTMLURL)
				}
			}
		}
	}

	// Fallback: attribute the failure through its job name.
	recentRuns, err := d.Client.ListWorkflowRuns(ctx, owner, repo, "")
	runsErrs.record(ctx, err)
	if err != nil {
		return nil, false, nil
	}
	for i := range recentRuns {
		r := recentRuns[i]
		runTime, parseErr := time.Parse(time.RFC3339, r.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		if r.Status != "completed" {
			continue
		}
		hasJob, job, jobErr := d.runHasAgentJob(ctx, owner, repo, r.ID, agent)
		lookupErrs.record(ctx, jobErr)
		if jobErr != nil || !hasJob {
			continue
		}
		if isTerminalFailure(job.Conclusion) {
			return &r, true, nil
		}
		if job.Conclusion == "success" {
			return nil, true, fmt.Errorf("harness agent %q job in pipeline %d concluded successfully; expected failure (url=%s)",
				agent, r.ID, r.HTMLURL)
		}
	}
	return nil, false, nil
}

// AssertNoHarnessAgentArtifact asserts that the named agent's harness job
// did not run after the trigger time.
func (d *Driver) AssertNoHarnessAgentArtifact(ctx context.Context, owner, repo, agent string, after time.Time) error {
	allRuns, err := d.Client.ListWorkflowRuns(ctx, owner, repo, "")
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
			return fmt.Errorf("expected harness %q not to run, but job %q found in pipeline %d",
				agent, harnessJobSuffix(agent), r.ID)
		}
	}
	return nil
}

// CountHarnessDispatches returns the number of pipelines that scheduled
// the agent's harness job after the trigger time. Cancelled/skipped jobs
// are excluded. The count settles before returning.
func (d *Driver) CountHarnessDispatches(ctx context.Context, owner, repo, agent string, after time.Time) (int, error) {
	deadline := d.now().Add(dispatchWait)
	for {
		count, pending, err := d.settleHarnessDispatchCount(ctx, owner, repo, agent, after)
		if err != nil {
			return 0, err
		}
		if pending == 0 {
			return count, nil
		}
		if d.now().After(deadline) {
			return 0, fmt.Errorf("harness %q dispatch count did not settle: %d pipeline(s) still pending after %s",
				agent, pending, dispatchWait)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-d.timerAfter(countSettlePoll):
		}
	}
}

// settleHarnessDispatchCount classifies pipelines created after the
// trigger time into counted dispatches and pending runs.
func (d *Driver) settleHarnessDispatchCount(ctx context.Context, owner, repo, agent string, after time.Time) (count, pending int, err error) {
	allRuns, err := d.Client.ListWorkflowRuns(ctx, owner, repo, "")
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
			// Pipelines without the agent's job are not counted as pending.
			// On a busy multi-pipeline project, unrelated in-progress
			// pipelines would cause false pending counts and delay the
			// settle loop unnecessarily.
		}
	}
	return count, pending, nil
}

// --- helper functions ---

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

// latestRuns is a convenience wrapper that swallows errors so it can be
// used inline in replacement-run scanning.
func latestRuns(ctx context.Context, d *Driver, owner, repo string) []forge.WorkflowRun {
	runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, "")
	if err != nil {
		return nil
	}
	return runs
}

// selectSuccessfulRun returns the newest successful workflow run after
// triggerTime that matches the optional event filter. Used for
// replacement-run scanning when a concurrency group cancels the tracked run.
func selectSuccessfulRun(runs []forge.WorkflowRun, triggerTime time.Time, event string) *forge.WorkflowRun {
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
