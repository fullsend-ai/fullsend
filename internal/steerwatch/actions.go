// Package steerwatch watches a repository's follow-up workflow runs while an
// agent run is in flight and turns the ones that pass provenance into steers
// delivered to the running session (ADR 0101).
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

// listPerPage bounds one follow-up run listing. A run that produces more
// than this many follow-ups in its lifetime is far past the steer cap.
const listPerPage = 50

// ActionsReader is the execution-platform read surface the provenance checks
// need. The GitHub client in internal/forge/github satisfies it; the watcher
// takes the interface so the forge API calls stay behind the adapter and the
// tests can point a real client at an httptest server.
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

// actorLogin returns the login that caused a run, preferring the triggering
// actor (the human who commented) over the run's attributed actor.
func actorLogin(r forge.WorkflowRun) string {
	if r.TriggeringActor != "" {
		return r.TriggeringActor
	}
	return r.Actor
}

// runCreatedAt parses a run's creation time. An unparseable timestamp yields
// the zero time, which fails the freshness check — the safe direction, since
// a run the watcher cannot date is a run it cannot prove is a follow-up.
func runCreatedAt(r forge.WorkflowRun) time.Time {
	t, err := time.Parse(time.RFC3339, r.CreatedAt)
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
