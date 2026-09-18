// Package steerwatch watches a repository's follow-up workflow runs while an
// agent run is in flight and turns the ones that pass provenance into steers
// delivered to the running session (ADR 0113).
//
// The runner in a CI job has no inbound path: GitHub Actions cannot deliver
// input to a running job. But every legitimate update to the work item
// already fires the shim, and its Route job already applied ADR 0054's
// authorization. That run record is a server-side, unforgeable statement of
// "who asked for what", so this package re-implements no routing predicate
// and calls no permission API — it verifies provenance of follow-up runs and
// nothing else.
package steerwatch

import (
	"context"
	"sort"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// listPerPage is the page size of one follow-up run listing. The client
// paginates, so this is not a ceiling on what a poll can see; it is GitHub's
// maximum, chosen to reach a given depth in as few requests as possible
// because the poll re-lists on every tick against a job token's hourly
// budget. The depth ceiling is the client's own page cap.
const listPerPage = 100

// ActionsReader is the execution-platform read surface the provenance checks
// need: a subset of forge.Client, so any forge client satisfies it. The
// narrow interface is a test seam, letting tests point a real client at an
// httptest server or pass a small stub. Only the GitHub client implements
// these reads; GitLab returns forge.ErrNotSupported, and the runner gates
// steering to GitHub (ADR 0113).
type ActionsReader interface {
	// GetWorkflowRun returns one run record, including its provenance
	// fields (path, referenced workflows, actors, item association).
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error)
	// ListWorkflowRunJobs returns the jobs of one run.
	ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error)
	// ListWorkflowRunsSince returns runs of one workflow file created at or
	// after since.
	ListWorkflowRunsSince(ctx context.Context, owner, repo, workflowFile string, since time.Time, perPage int) ([]forge.WorkflowRun, error)
}

// actorLogin returns the login whose event created the run — `actor` on the
// run record, which for an issue_comment run is the comment's author.
//
// Not `triggering_actor`: GitHub sets that to whoever caused the *latest
// attempt*, so on a re-run it names the person who pressed the button while
// the Route job still authorized the comment author. Preferring it would
// hand the re-runner's login the comment author's authority (or vice
// versa). The run record's own identity is the event's initiator.
func actorLogin(r forge.WorkflowRun) string {
	return r.Actor
}

// runCreatedAt parses a run's creation time, with or without fractional
// seconds. An unparseable timestamp yields the zero time, which fails the
// freshness check — the safe direction, since a run the watcher cannot date
// is a run it cannot prove is a follow-up.
func runCreatedAt(r forge.WorkflowRun) time.Time {
	t, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// runsSince lists follow-up runs of the shim, oldest first.
func (w *Watcher) runsSince(ctx context.Context, workflowFile string, since time.Time) ([]forge.WorkflowRun, error) {
	owner, repo, err := splitRepo(w.cfg.Repo)
	if err != nil {
		return nil, err
	}
	runs, err := w.actions.ListWorkflowRunsSince(ctx, owner, repo, workflowFile, since, listPerPage)
	if err != nil {
		return nil, err
	}
	sort.Slice(runs, func(i, j int) bool {
		return runCreatedAt(runs[i]).Before(runCreatedAt(runs[j]))
	})
	return runs, nil
}
