// Package forgejoactions is the Forgejo Actions ci.Driver for behaviour
// tests.
//
// The driver observes workflow runs; it never dispatches, cancels, or
// reruns one. Agent handoffs stay the poller's responsibility. Runs are
// matched by workflow file, event, and creation time, the same contract
// as the GitHub Actions driver: a WaitForWorkflow timeout with no
// matching run is a *ci.NotDispatchedError, so steps retry it once with
// the clock-skew buffer and the failure diagnostics list the
// repository's runs through ci.RunLister.
//
// Forgejo has no cross-run artifact lookup that this driver relies on:
// every artifact is read from the run that uploaded it, by the existing
// Fullsend artifact names ("fullsend-<agent>", with an optional
// ".matrix-<n>" suffix). Agent jobs are recognised by the same job names
// as on GitHub Actions ("Harness run (<agent>)" for a custom harness and
// the stage name, e.g. "Triage", for a built-in stage), matched by
// suffix so a reusable-workflow caller prefix does not matter.
//
// The driver implements ci.RunLister, ci.EmptyMatrixWaiter, and
// ci.AgentArtifactDownloader. It never delegates to the GitHub Actions or
// GitLab CI drivers.
package forgejoactions

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
)

// pollInterval is how often WaitForWorkflow polls a matched run.
// BEHAVIOUR_RUN_POLL_INTERVAL overrides it (a Go duration), for a local
// Forgejo where runs finish in seconds.
var pollInterval = env.MustDuration("BEHAVIOUR_RUN_POLL_INTERVAL", 15*time.Second)

const (
	// dispatchWait bounds how long a harness wait or a matched run's
	// completion is waited for.
	dispatchWait = 20 * time.Minute

	// Run detection backs off from dispatchPollInit, doubling up to
	// dispatchPollMax, within a dispatchTimeout window.
	dispatchPollInit = 2 * time.Second
	dispatchPollMax  = 30 * time.Second
	dispatchTimeout  = 5 * time.Minute

	// countSettlePoll is CountHarnessDispatches' poll interval while
	// runs settle.
	countSettlePoll = 5 * time.Second

	// pollMinBudget is the least wait budget a harness poll starts with.
	pollMinBudget = 5 * time.Second

	artifactRunPoll = 5 * time.Second
	artifactRunWait = 5 * time.Minute

	assertNoWorkflowChecks = 3
	assertNoWorkflowDelay  = 10 * time.Second

	// harnessWorkflowFile is the harness workflow's file name. Forgejo
	// identifies a workflow by its file name alone.
	harnessWorkflowFile = "fullsend.yaml"

	// harnessJobNameMarker matches every harness matrix job name.
	harnessJobNameMarker = "Harness run ("
	// harnessJobPlaceholder is the matrix job's name before its matrix
	// is known.
	harnessJobPlaceholder = "Harness run (${{ matrix.agent }})"
	// harnessDispatchJobSuffix is the job that computes the harness
	// matrix.
	harnessDispatchJobSuffix = "Harness dispatch"
)

// RunClient is the subset of forge.Client the driver uses: read-only
// workflow-run, job, artifact, and log operations. forge.Client and the
// Forgejo adapter (internal/forge/forgejo.LiveClient) both satisfy it.
type RunClient interface {
	ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error)
	ListWorkflowRunsSince(ctx context.Context, owner, repo, workflowFile string, since time.Time) ([]forge.WorkflowRun, error)
	ListRecentWorkflowRuns(ctx context.Context, owner, repo string, perPage int) ([]forge.WorkflowRun, error)
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error)
	ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error)
	ListWorkflowRunArtifacts(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowArtifact, error)
	DownloadWorkflowRunArtifact(ctx context.Context, owner, repo string, artifactID int) ([]byte, error)
	GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error)
}

var _ RunClient = forge.Client(nil)

// Driver implements ci.Driver for Forgejo Actions. Client and Token are
// set at construction and never modified, and the timer hooks are only
// read, so it is safe for concurrent use.
type Driver struct {
	// Client reads the Forgejo Actions API.
	Client RunClient
	// Token is the credential the driver was built with.
	Token string

	// afterFunc is the poll-loop timer; tests inject an instant one.
	afterFunc func(time.Duration) <-chan time.Time
	// nowFunc is the clock of the wait deadlines; tests inject a
	// stepping clock.
	nowFunc func() time.Time
}

var (
	_ ci.Driver                  = (*Driver)(nil)
	_ ci.RunLister               = (*Driver)(nil)
	_ ci.EmptyMatrixWaiter       = (*Driver)(nil)
	_ ci.AgentArtifactDownloader = (*Driver)(nil)
)

// New returns the Forgejo Actions CI driver.
func New(client RunClient, token string) ci.Driver {
	return &Driver{Client: client, Token: token, afterFunc: time.After, nowFunc: time.Now}
}

func (d *Driver) now() time.Time {
	if d.nowFunc != nil {
		return d.nowFunc()
	}
	return time.Now()
}

func (d *Driver) timerAfter(dur time.Duration) <-chan time.Time {
	if d.afterFunc != nil {
		return d.afterFunc(dur)
	}
	return time.After(dur)
}

// sleep waits dur or until ctx ends.
func (d *Driver) sleep(ctx context.Context, dur time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.timerAfter(dur):
		return nil
	}
}

func nextBackoff(current, limit time.Duration) time.Duration {
	return min(current*2, limit)
}

// pollErrors counts a poll loop's API failures so a timeout can tell a
// wait spent failing apart from one that found nothing.
type pollErrors struct {
	calls    int
	failed   int
	cutShort int
	last     error
}

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

func (p pollErrors) describe(what string) string {
	var b strings.Builder
	if p.failed > 0 {
		fmt.Fprintf(&b, " (failed on %d of %d %s during the wait; last: %v)", p.failed, p.calls, what, p.last)
	}
	if p.cutShort > 0 {
		fmt.Fprintf(&b, " (%d of %d %s cut short by the wait budget)", p.cutShort, p.calls, what)
	}
	return b.String()
}

// createdBefore reports whether created is earlier than the trigger.
// Forgejo timestamps have second precision, so the trigger is truncated
// to whole seconds first, as in the GitHub Actions driver (#9495).
func createdBefore(created, trigger time.Time) bool {
	return created.Before(trigger.Truncate(time.Second))
}

// createdAtOrAfter reports whether run was created at or after the
// trigger's second. A run whose creation time does not parse never
// matches.
func createdAtOrAfter(run forge.WorkflowRun, trigger time.Time) bool {
	created, err := time.Parse(time.RFC3339, run.CreatedAt)
	return err == nil && !createdBefore(created, trigger)
}

func runMatches(run forge.WorkflowRun, after time.Time, event string) bool {
	return createdAtOrAfter(run, after) && (event == "" || run.Event == event)
}

// newestMatch returns the newest run matching after and event, accepting
// only runs ok approves.
func newestMatch(runs []forge.WorkflowRun, after time.Time, event string, ok func(forge.WorkflowRun) bool) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for i := range runs {
		r := runs[i]
		if !runMatches(r, after, event) || !ok(r) {
			continue
		}
		if best == nil || r.ID > best.ID {
			best = &r
		}
	}
	return best
}

func anyRun(forge.WorkflowRun) bool { return true }

func completedSuccess(r forge.WorkflowRun) bool {
	return r.Status == "completed" && r.Conclusion == "success"
}

// WaitForWorkflow waits for the newest run of workflowFile triggered by
// event (any event when empty) created at or after the trigger, then for
// that run to complete. When no such run appears within the dispatch
// window, it returns a *ci.NotDispatchedError. When the run concludes
// other than success, a newer successful run of the same workflow and
// event (one that superseded it in a concurrency group) is used instead;
// the driver never reruns a run itself.
func (d *Driver) WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	var matched *forge.WorkflowRun
	var listErrs pollErrors
	deadline := d.now().Add(dispatchTimeout)
	interval := dispatchPollInit
	for matched == nil && d.now().Before(deadline) {
		if err := d.sleep(ctx, interval); err != nil {
			return nil, err
		}
		interval = nextBackoff(interval, dispatchPollMax)
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
		listErrs.record(ctx, err)
		if err != nil {
			continue
		}
		matched = newestMatch(runs, after, event, anyRun)
	}
	if matched == nil {
		return nil, &ci.NotDispatchedError{WorkflowFile: workflowFile, Event: event, Detail: listErrs.describe("polls")}
	}
	if matched.Status == "completed" && matched.Conclusion != "success" {
		return nil, fmt.Errorf("workflow %s run %d concluded with %q during dispatch", workflowFile, matched.ID, matched.Conclusion)
	}

	var getErrs pollErrors
	deadline = d.now().Add(dispatchWait)
	for d.now().Before(deadline) {
		if err := d.sleep(ctx, pollInterval); err != nil {
			return nil, err
		}
		run, err := d.Client.GetWorkflowRun(ctx, owner, repo, matched.ID)
		getErrs.record(ctx, err)
		if err != nil || run.Status != "completed" {
			continue
		}
		if run.Conclusion == "success" {
			return run, nil
		}
		if runs, listErr := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile); listErr == nil {
			if replacement := newestMatch(runs, after, event, completedSuccess); replacement != nil && replacement.ID > matched.ID {
				return replacement, nil
			}
		}
		return run, fmt.Errorf("workflow %s run %d concluded with %q (%s)", workflowFile, run.ID, run.Conclusion, run.HTMLURL)
	}
	return nil, fmt.Errorf("workflow %s run %d did not complete within deadline%s", workflowFile, matched.ID, getErrs.describe("polls"))
}

// FindCompletedWorkflowRun waits for the newest successful run of
// workflowFile created at or after the given time.
func (d *Driver) FindCompletedWorkflowRun(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	var listErrs pollErrors
	deadline := d.now().Add(artifactRunWait)
	for d.now().Before(deadline) {
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
		listErrs.record(ctx, err)
		if err == nil {
			if run := newestMatch(runs, after, "", completedSuccess); run != nil {
				return run, nil
			}
		}
		if err := d.sleep(ctx, artifactRunPoll); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no completed workflow run for %s after %s%s", workflowFile, after.Format(time.RFC3339), listErrs.describe("polls"))
}

// AssertNoWorkflow checks, over a settling window, that no run of
// workflowFile was created at or after the exact trigger time.
func (d *Driver) AssertNoWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) error {
	workflowFile = filepath.Base(workflowFile)
	for attempt := range assertNoWorkflowChecks {
		if attempt > 0 {
			if err := d.sleep(ctx, assertNoWorkflowDelay); err != nil {
				return err
			}
		}
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
		if err != nil {
			return err
		}
		for _, run := range runs {
			created, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			// The exact trigger time is kept rather than truncating it to
			// Forgejo's second precision: truncating would fail on an
			// unrelated run created earlier in the trigger's own second.
			// The cost is that a run created in that same second after the
			// trigger is not flagged either, as in the GitHub driver.
			if parseErr == nil && !created.Before(after) {
				return fmt.Errorf("unexpected workflow %s run %d (event %q) created at %s, after the trigger time", workflowFile, run.ID, run.Event, run.CreatedAt)
			}
		}
	}
	return nil
}

// GetRunLogs returns the plain-text logs of a run's jobs.
func (d *Driver) GetRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	return d.Client.GetWorkflowRunLogs(ctx, owner, repo, runID)
}

// ListRecentRuns implements ci.RunLister.
func (d *Driver) ListRecentRuns(ctx context.Context, owner, repo string, limit int) ([]forge.WorkflowRun, error) {
	return d.Client.ListRecentWorkflowRuns(ctx, owner, repo, limit)
}

// DownloadArtifacts downloads every artifact of a run.
func (d *Driver) DownloadArtifacts(ctx context.Context, owner, repo string, runID int, destDir string) error {
	_, err := d.downloadFromRun(ctx, owner, repo, runID, destDir, func(string) bool { return true })
	return err
}

// DownloadNamedArtifactFromRun downloads the run's artifact named exactly
// artifactName.
func (d *Driver) DownloadNamedArtifactFromRun(ctx context.Context, owner, repo string, runID int, artifactName string, destDir string) error {
	n, err := d.downloadFromRun(ctx, owner, repo, runID, destDir, func(name string) bool { return name == artifactName })
	if err == nil && n == 0 {
		return fmt.Errorf("artifact %q not found in workflow run %d", artifactName, runID)
	}
	return err
}

// DownloadAgentArtifactsFromRun implements ci.AgentArtifactDownloader: it
// downloads every artifact of the run that belongs to agent.
func (d *Driver) DownloadAgentArtifactsFromRun(ctx context.Context, owner, repo string, runID int, agent string, destDir string) error {
	requested := "fullsend-" + agent
	n, err := d.downloadFromRun(ctx, owner, repo, runID, destDir, func(name string) bool { return agentArtifactNameMatches(name, requested) })
	if err == nil && n == 0 {
		return fmt.Errorf("no artifact for agent %q (%q) in workflow run %d", agent, requested, runID)
	}
	return err
}

// downloadFromRun downloads the run's artifacts whose names match and
// returns how many it downloaded.
func (d *Driver) downloadFromRun(ctx context.Context, owner, repo string, runID int, destDir string, matches func(string) bool) (int, error) {
	arts, err := d.Client.ListWorkflowRunArtifacts(ctx, owner, repo, runID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, art := range arts {
		if !matches(art.Name) {
			continue
		}
		data, err := d.Client.DownloadWorkflowRunArtifact(ctx, owner, repo, art.ID)
		if err != nil {
			return n, fmt.Errorf("downloading artifact %q of workflow run %d: %w", art.Name, runID, err)
		}
		if err := extractArtifactZip(art.Name, data, destDir); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// DownloadNamedArtifactAfter waits for a run created at or after the
// given time that uploaded an artifact named exactly artifactName, and
// downloads it from that run.
func (d *Driver) DownloadNamedArtifactAfter(ctx context.Context, owner, repo, artifactName string, after time.Time, destDir string) error {
	return d.downloadAfter(ctx, owner, repo, artifactName, after, destDir, false, func(name string) bool { return name == artifactName })
}

// DownloadAgentArtifactsAfter implements ci.AgentArtifactDownloader: it
// waits for a completed run created at or after the given time that
// uploaded agent's artifacts, and downloads all of them from that run.
func (d *Driver) DownloadAgentArtifactsAfter(ctx context.Context, owner, repo, agent string, after time.Time, destDir string) error {
	requested := "fullsend-" + agent
	return d.downloadAfter(ctx, owner, repo, requested, after, destDir, true, func(name string) bool { return agentArtifactNameMatches(name, requested) })
}

// downloadAfter scans the runs created at or after the given time, newest
// first, for one whose own artifact listing has a match, and downloads
// the matches from that run only. When completedOnly is set, a run that
// has not completed is skipped, so a matrix whose jobs upload as they
// finish is never downloaded partially.
func (d *Driver) downloadAfter(ctx context.Context, owner, repo, label string, after time.Time, destDir string, completedOnly bool, matches func(string) bool) error {
	var listErrs pollErrors
	deadline := d.now().Add(artifactRunWait)
	for d.now().Before(deadline) {
		runs, err := d.Client.ListWorkflowRunsSince(ctx, owner, repo, "", after.Truncate(time.Second))
		listErrs.record(ctx, err)
		if err == nil {
			found, err := d.downloadFromNewestRun(ctx, owner, repo, runs, after, destDir, completedOnly, matches, &listErrs)
			if found || err != nil {
				return err
			}
		}
		if err := d.sleep(ctx, artifactRunPoll); err != nil {
			return err
		}
	}
	return fmt.Errorf("artifact %q not found in any workflow run after %s%s", label, after.Format(time.RFC3339), listErrs.describe("lookups"))
}

func (d *Driver) downloadFromNewestRun(ctx context.Context, owner, repo string, runs []forge.WorkflowRun, after time.Time, destDir string, completedOnly bool, matches func(string) bool, listErrs *pollErrors) (bool, error) {
	for _, r := range newestFirst(runs, after) {
		if completedOnly && r.Status != "completed" {
			continue
		}
		arts, err := d.Client.ListWorkflowRunArtifacts(ctx, owner, repo, r.ID)
		listErrs.record(ctx, err)
		if err != nil || !slices.ContainsFunc(arts, func(a forge.WorkflowArtifact) bool { return matches(a.Name) }) {
			continue
		}
		_, err = d.downloadFromRun(ctx, owner, repo, r.ID, destDir, matches)
		if errors.Is(err, forge.ErrNotFound) {
			// The artifact is listed but its upload is not confirmed
			// yet; poll again.
			return false, nil
		}
		return true, err
	}
	return false, nil
}

// newestFirst returns the runs created at or after the trigger, highest
// ID first.
func newestFirst(runs []forge.WorkflowRun, after time.Time) []forge.WorkflowRun {
	var out []forge.WorkflowRun
	for _, r := range runs {
		if createdAtOrAfter(r, after) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b forge.WorkflowRun) int { return b.ID - a.ID })
	return out
}

// agentArtifactNameMatches accepts the exact "fullsend-<agent>" name and
// its readable matrix suffix "fullsend-<agent>.matrix-<n>".
func agentArtifactNameMatches(actual, requested string) bool {
	const logicalPrefix = "fullsend-"
	agent, ok := strings.CutPrefix(requested, logicalPrefix)
	if !ok || !artifactAgentNamePattern.MatchString(agent) {
		return false
	}
	if actual == requested {
		return true
	}
	index, ok := strings.CutPrefix(actual, requested+".matrix-")
	return ok && artifactMatrixIndexPattern.MatchString(index)
}

var (
	artifactAgentNamePattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	artifactMatrixIndexPattern = regexp.MustCompile(`^[0-9]+$`)
)

// --- harness agent jobs ---

func harnessJobSuffix(agent string) string {
	return "Harness run (" + agent + ")"
}

// builtinRoleJobName returns the job name of a built-in stage ("Triage"
// for "triage"), or "" when agent is not one.
func builtinRoleJobName(agent string) string {
	if agent != "" && slices.Contains(config.ValidAgentNames(), agent) {
		return strings.ToUpper(agent[:1]) + agent[1:]
	}
	return ""
}

// matchingAgentJobs returns every job of agent: its custom-harness matrix
// job or its built-in stage job.
func matchingAgentJobs(jobs []forge.WorkflowJob, agent string) []forge.WorkflowJob {
	suffix := harnessJobSuffix(agent)
	builtin := builtinRoleJobName(agent)
	var out []forge.WorkflowJob
	for _, j := range jobs {
		if strings.HasSuffix(j.Name, suffix) || (builtin != "" && strings.HasSuffix(j.Name, builtin)) {
			out = append(out, j)
		}
	}
	return out
}

// matchAgentJob returns the most decisive of agent's jobs: a running job,
// then a failure, then a success, then a skipped or cancelled one.
func matchAgentJob(jobs []forge.WorkflowJob, agent string) (bool, forge.WorkflowJob) {
	found := false
	var best forge.WorkflowJob
	for _, j := range matchingAgentJobs(jobs, agent) {
		if !found || jobMatchTier(j) > jobMatchTier(best) {
			found, best = true, j
		}
	}
	return found, best
}

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

func isWeakJobMatch(j forge.WorkflowJob) bool {
	return j.Status == "completed" && isConcurrencySuperseded(j.Conclusion)
}

func isConcurrencySuperseded(conclusion string) bool {
	return conclusion == "cancelled" || conclusion == "skipped"
}

func isTerminalFailure(conclusion string) bool {
	return conclusion == "failure" || conclusion == "timed_out" || conclusion == "startup_failure"
}

// harnessMatrixUnresolved reports whether the harness matrix of a run is
// still being computed: no harness matrix job is listed yet, or the Route
// job has not completed.
func harnessMatrixUnresolved(jobs []forge.WorkflowJob) bool {
	expanded := false
	for _, j := range jobs {
		if strings.Contains(j.Name, harnessJobNameMarker) {
			expanded = true
		}
		if strings.HasSuffix(j.Name, "Route") && j.Status != "completed" {
			return true
		}
	}
	return !expanded
}

// runRanNoJob reports whether a completed run was skipped as a whole.
func runRanNoJob(r forge.WorkflowRun) bool {
	return r.Status == "completed" && r.Conclusion == "skipped"
}

// listHarnessRuns returns the harness runs created at or after the
// trigger, paginating back to it.
func (d *Driver) listHarnessRuns(ctx context.Context, owner, repo string, after time.Time) ([]forge.WorkflowRun, error) {
	runs, err := d.Client.ListWorkflowRunsSince(ctx, owner, repo, harnessWorkflowFile, after.Truncate(time.Second))
	if err != nil {
		return nil, err
	}
	var out []forge.WorkflowRun
	for _, r := range runs {
		if createdAtOrAfter(r, after) {
			out = append(out, r)
		}
	}
	return out, nil
}

// agentState is what one harness run shows about an agent.
type agentState int

const (
	agentAbsent     agentState = iota // the run does not involve the agent
	agentPending                      // the run may still schedule or is running the agent
	agentSucceeded                    // the agent's run completed successfully
	agentFailed                       // the agent's run genuinely failed
	agentSuperseded                   // the agent's run was cancelled or skipped
)

// agentRunState classifies run for agent from its jobs and, once it has
// completed, its own artifacts: an agent artifact proves the agent ran in
// this run even when its job is not recognised by name.
func (d *Driver) agentRunState(ctx context.Context, owner, repo string, r forge.WorkflowRun, agent string) (agentState, error) {
	jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, r.ID)
	if err != nil {
		return agentPending, fmt.Errorf("list jobs for workflow run %d: %w", r.ID, err)
	}
	hasJob, job := matchAgentJob(jobs, agent)
	if r.Status != "completed" {
		// The run's outcome decides, so an unfinished run that has the
		// agent's job, or may still schedule it, is not settled yet.
		if hasJob || harnessMatrixUnresolved(jobs) {
			return agentPending, nil
		}
		return agentAbsent, nil
	}
	hasArtifact := false
	if !hasJob || isWeakJobMatch(job) {
		arts, err := d.Client.ListWorkflowRunArtifacts(ctx, owner, repo, r.ID)
		if err != nil {
			return agentPending, fmt.Errorf("list artifacts of workflow run %d: %w", r.ID, err)
		}
		requested := "fullsend-" + agent
		hasArtifact = slices.ContainsFunc(arts, func(a forge.WorkflowArtifact) bool { return agentArtifactNameMatches(a.Name, requested) })
	}
	switch {
	case !hasJob && !hasArtifact:
		return agentAbsent, nil
	case !hasArtifact && isWeakJobMatch(job):
		return agentSuperseded, nil
	case r.Conclusion == "success":
		return agentSucceeded, nil
	case isConcurrencySuperseded(r.Conclusion):
		return agentSuperseded, nil
	case hasArtifact || isTerminalFailure(job.Conclusion):
		return agentFailed, nil
	default:
		// The run failed in a sibling job; the agent's own job did not.
		return agentAbsent, nil
	}
}

// harnessWait runs poll with a backoff until it reports done or the
// dispatch wait ends.
func (d *Driver) harnessWait(ctx context.Context, poll func(context.Context) (*forge.WorkflowRun, bool, error)) (*forge.WorkflowRun, bool, error) {
	deadline := d.now().Add(dispatchWait)
	interval := dispatchPollInit
	for {
		if err := d.sleep(ctx, interval); err != nil {
			return nil, true, err
		}
		interval = nextBackoff(interval, dispatchPollMax)
		remaining := deadline.Sub(d.now())
		if remaining < pollMinBudget {
			return nil, false, nil
		}
		pollCtx, cancel := context.WithTimeout(ctx, remaining)
		run, done, err := poll(pollCtx)
		cancel()
		if done {
			return run, true, err
		}
	}
}

// WaitForHarnessAgent waits for the newest harness run that involves
// agent to succeed. A failed run is reported, with the run, only when no
// newer run of the agent is still pending or has succeeded.
func (d *Driver) WaitForHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error) {
	var runsErrs, lookupErrs pollErrors
	run, done, err := d.harnessWait(ctx, func(ctx context.Context) (*forge.WorkflowRun, bool, error) {
		runs, err := d.listHarnessRuns(ctx, owner, repo, after)
		runsErrs.record(ctx, err)
		if err != nil {
			return nil, false, nil
		}
		for _, r := range newestFirst(runs, after) {
			state, err := d.agentRunState(ctx, owner, repo, r, agent)
			lookupErrs.record(ctx, err)
			switch {
			case err != nil, state == agentPending:
				// A newer run of the agent is not settled, so an older
				// failure is not decisive yet.
				return nil, false, nil
			case state == agentSucceeded:
				found := r
				return &found, true, nil
			case state == agentFailed:
				failed := r
				return &failed, true, fmt.Errorf("harness agent %q: workflow run %d concluded with %q (url=%s)", agent, r.ID, r.Conclusion, r.HTMLURL)
			}
		}
		return nil, false, nil
	})
	if done {
		return run, err
	}
	return nil, fmt.Errorf("harness agent %q did not complete successfully within %s%s%s", agent, dispatchWait, runsErrs.describe("run polls"), lookupErrs.describe("lookups"))
}

// WaitForHarnessAgentRound waits for the earliest harness run, not in
// consumed, in which agent's job settled, and returns it on success. An
// earlier candidate that has not settled holds back every later run.
func (d *Driver) WaitForHarnessAgentRound(ctx context.Context, owner, repo, agent string, after time.Time, consumed map[int]bool) (*forge.WorkflowRun, error) {
	var runsErrs, lookupErrs pollErrors
	run, done, err := d.harnessWait(ctx, func(ctx context.Context) (*forge.WorkflowRun, bool, error) {
		runs, err := d.listHarnessRuns(ctx, owner, repo, after)
		runsErrs.record(ctx, err)
		if err != nil {
			return nil, false, nil
		}
		ordered := newestFirst(runs, after)
		slices.Reverse(ordered)
		for _, r := range ordered {
			if consumed[r.ID] {
				continue
			}
			state, err := d.agentRunState(ctx, owner, repo, r, agent)
			lookupErrs.record(ctx, err)
			switch {
			case err != nil, state == agentPending:
				return nil, false, nil
			case state == agentSucceeded:
				found := r
				return &found, true, nil
			case state == agentFailed:
				failed := r
				return &failed, true, fmt.Errorf("harness agent %q: workflow run %d concluded with %q (url=%s)", agent, r.ID, r.Conclusion, r.HTMLURL)
			}
		}
		return nil, false, nil
	})
	if done {
		return run, err
	}
	return nil, fmt.Errorf("harness agent %q did not complete a new round successfully within %s%s%s", agent, dispatchWait, runsErrs.describe("run polls"), lookupErrs.describe("lookups"))
}

// WaitForFailedHarnessAgent waits for the newest harness run involving
// agent to fail, and errors out early when it succeeds instead.
func (d *Driver) WaitForFailedHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error) {
	var runsErrs, lookupErrs pollErrors
	run, done, err := d.harnessWait(ctx, func(ctx context.Context) (*forge.WorkflowRun, bool, error) {
		runs, err := d.listHarnessRuns(ctx, owner, repo, after)
		runsErrs.record(ctx, err)
		if err != nil {
			return nil, false, nil
		}
		for _, r := range newestFirst(runs, after) {
			state, err := d.agentRunState(ctx, owner, repo, r, agent)
			lookupErrs.record(ctx, err)
			switch {
			case err != nil, state == agentPending:
				return nil, false, nil
			case state == agentFailed:
				failed := r
				return &failed, true, nil
			case state == agentSucceeded:
				return nil, true, fmt.Errorf("harness agent %q workflow run %d concluded successfully; expected failure (url=%s)", agent, r.ID, r.HTMLURL)
			}
		}
		return nil, false, nil
	})
	if done {
		return run, err
	}
	return nil, fmt.Errorf("harness agent %q did not complete with a failure within %s%s%s", agent, dispatchWait, runsErrs.describe("run polls"), lookupErrs.describe("lookups"))
}

// AssertNoHarnessAgentArtifact checks that no harness run created at or
// after the trigger ran agent's job: every matching job must be skipped
// and no run may hold an artifact of agent. An unreadable job or artifact
// listing fails the assertion rather than passing it.
func (d *Driver) AssertNoHarnessAgentArtifact(ctx context.Context, owner, repo, agent string, after time.Time) error {
	requested := "fullsend-" + agent
	runs, err := d.listHarnessRuns(ctx, owner, repo, after)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if runRanNoJob(r) {
			continue
		}
		jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, r.ID)
		if err != nil {
			return fmt.Errorf("list jobs for workflow run %d: %w", r.ID, err)
		}
		for _, job := range matchingAgentJobs(jobs, agent) {
			if job.Conclusion != "skipped" {
				return fmt.Errorf("expected harness %q not to run, but job %q found in workflow run %d", agent, job.Name, r.ID)
			}
		}
		// An agent artifact proves the agent ran even when its job is not
		// recognised by name, as in agentRunState.
		arts, err := d.Client.ListWorkflowRunArtifacts(ctx, owner, repo, r.ID)
		if err != nil {
			return fmt.Errorf("list artifacts of workflow run %d: %w", r.ID, err)
		}
		for _, art := range arts {
			if agentArtifactNameMatches(art.Name, requested) {
				return fmt.Errorf("expected harness %q not to run, but artifact %q found in workflow run %d", agent, art.Name, r.ID)
			}
		}
	}
	return nil
}

// CountHarnessDispatches returns how many harness runs created at or
// after the trigger ran agent's job, excluding cancelled and skipped
// jobs. It waits until no candidate run is still settling.
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
			return 0, fmt.Errorf("harness %q dispatch count did not settle: %d run(s) still pending after %s", agent, pending, dispatchWait)
		}
		if err := d.sleep(ctx, countSettlePoll); err != nil {
			return 0, err
		}
	}
}

func (d *Driver) settleHarnessDispatchCount(ctx context.Context, owner, repo, agent string, after time.Time) (count, pending int, err error) {
	runs, err := d.listHarnessRuns(ctx, owner, repo, after)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range runs {
		if runRanNoJob(r) {
			continue
		}
		if r.Status == "queued" {
			pending++
			continue
		}
		jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, r.ID)
		if err != nil {
			return 0, 0, fmt.Errorf("list jobs for workflow run %d: %w", r.ID, err)
		}
		hasJob, job := matchAgentJob(jobs, agent)
		running := r.Status != "completed"
		switch {
		case hasJob && job.Status != "completed":
			pending++
		case hasJob && running && harnessMatrixUnresolved(jobs):
			pending++
		case hasJob && !isConcurrencySuperseded(job.Conclusion):
			count++
		case !hasJob && running:
			pending++
		}
	}
	return count, pending, nil
}

// WaitForHarnessEmptyMatrix implements ci.EmptyMatrixWaiter. Every
// harness run created at or after the trigger that was not cancelled or
// skipped must complete with its Harness dispatch job succeeding and no
// "Harness run (<agent>)" job scheduled. The unexpanded matrix job may
// be listed as skipped, or be absent: Forgejo replaces a matrix job with
// its expanded jobs once the matrix is known, and a completed run's job
// listing is final. A run whose dispatch job failed, or that scheduled
// an agent, fails the wait; absent runs are waited for.
func (d *Driver) WaitForHarnessEmptyMatrix(ctx context.Context, owner, repo string, after time.Time) (*forge.WorkflowRun, error) {
	state := "no poll completed"
	var lastErr error
	run, done, err := d.harnessWait(ctx, func(ctx context.Context) (*forge.WorkflowRun, bool, error) {
		run, done, err := d.emptyMatrixPollOnce(ctx, owner, repo, after, &state)
		if !done && err != nil {
			lastErr = err
		}
		return run, done, err
	})
	if done {
		return run, err
	}
	msg := fmt.Sprintf("harness dispatch did not settle with an empty matrix within %s: %s", dispatchWait, state)
	if lastErr != nil {
		msg += fmt.Sprintf(" (last lookup error: %v)", lastErr)
	}
	return nil, errors.New(msg)
}

func (d *Driver) emptyMatrixPollOnce(ctx context.Context, owner, repo string, after time.Time, state *string) (*forge.WorkflowRun, bool, error) {
	runs, err := d.listHarnessRuns(ctx, owner, repo, after)
	if err != nil {
		return nil, false, err
	}
	var settled *forge.WorkflowRun
	pending := 0
	for _, r := range newestFirst(runs, after) {
		if r.Status == "completed" && isConcurrencySuperseded(r.Conclusion) {
			continue
		}
		if r.Status != "completed" {
			pending++
			continue
		}
		jobs, err := d.Client.ListWorkflowRunJobs(ctx, owner, repo, r.ID)
		if err != nil {
			return nil, false, fmt.Errorf("list jobs for workflow run %d: %w", r.ID, err)
		}
		var dispatch *forge.WorkflowJob
		for i, job := range jobs {
			switch {
			case strings.HasSuffix(job.Name, harnessDispatchJobSuffix):
				dispatch = &jobs[i]
			case strings.HasSuffix(job.Name, harnessJobPlaceholder):
				if job.Conclusion != "skipped" {
					return nil, true, fmt.Errorf("expected the %q job to be skipped with an empty matrix, but it ended %s %s without expanding in workflow run %d", job.Name, job.Status, job.Conclusion, r.ID)
				}
			case strings.Contains(job.Name, harnessJobNameMarker):
				return nil, true, fmt.Errorf("expected an empty harness matrix, but job %q was scheduled in workflow run %d", job.Name, r.ID)
			}
		}
		switch {
		case dispatch == nil:
			return nil, true, fmt.Errorf("expected the %q job in workflow run %d, but the run has no such job", harnessDispatchJobSuffix, r.ID)
		case dispatch.Conclusion != "success":
			return nil, true, fmt.Errorf("expected the %q job to succeed with an empty matrix, but it concluded %s in workflow run %d; see the run logs", dispatch.Name, dispatch.Conclusion, r.ID)
		}
		run := r
		settled = &run
	}
	switch {
	case pending > 0:
		*state = fmt.Sprintf("%d harness run(s) still in progress", pending)
		return nil, false, nil
	case settled == nil:
		*state = "no harness run created after the trigger time"
		return nil, false, nil
	}
	return settled, true, nil
}

// --- artifact extraction ---

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
	if safeName == "" || safeName == "." || safeName == ".." || safeName == string(os.PathSeparator) {
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
	var total int64
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
		total += int64(len(data))
		if total > totalExtractLimit {
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
