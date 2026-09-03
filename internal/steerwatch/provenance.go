package steerwatch

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// allowedEvents are the forge events whose runs execute the shim from the
// base branch. `push`, `pull_request` and `workflow_dispatch` are absent on
// purpose: a follow-up must be an update to the work item, routed by a Route
// job that ran the base-branch workflow file.
var allowedEvents = map[string]bool{
	"issue_comment":               true,
	"issues":                      true,
	"pull_request_target":         true,
	"pull_request_review":         true,
	"pull_request_review_comment": true,
}

// rejection explains why a candidate run was not accepted as a steer. It is
// logged, never surfaced to the agent.
type rejection struct {
	check  string
	detail string
}

func (r rejection) String() string { return r.check + ": " + r.detail }

// routeJobName reports whether name is the dispatch Route job. Reusable
// workflows prefix the called job's name with the caller's job id
// ("dispatch / Route"), and a repo that calls the workflow from a job with a
// different id gets a different prefix — so match on the suffix, not the
// whole string.
func routeJobName(name string) bool {
	return name == "Route" || strings.HasSuffix(name, " / Route")
}

// sameDispatchChain reports whether two runs called the same reusable
// workflows at the same ref. This is the check that makes version knowledge
// unnecessary: a foreign or renamed reusable workflow, or one pinned to a
// different ref, fails by inequality.
//
// The sha is deliberately not compared. A shim pinned to a branch
// (`@main`, which the fullsend repo's own shim uses) resolves to a new sha
// every time that branch advances, which on an active repo is between most
// pairs of events; comparing it would silently drop every steer there. Path
// plus ref already names the trusted workflow: a newer sha at the same
// `refs/heads/main` of the same path is the same trusted workflow, newer.
func sameDispatchChain(mine, theirs []forge.ReferencedWorkflow) bool {
	if len(mine) != len(theirs) {
		return false
	}
	key := func(w forge.ReferencedWorkflow) string { return w.Path + "\x00" + w.Ref }
	a := make([]string, 0, len(mine))
	for _, w := range mine {
		a = append(a, key(w))
	}
	b := make([]string, 0, len(theirs))
	for _, w := range theirs {
		b = append(b, key(w))
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// jobSelected reports whether the stage job named stageJob was selected by
// the candidate's Route job.
//
// A stage the route job did not pick is `completed`/`skipped`. A null
// conclusion means the job is queued or running — under `queue: single` that
// is exactly the run waiting behind me, which is the common case and must
// count as selected. `cancelled` also counts: a later event replaced this
// run's pending stage job, but the authorization the Route job established
// still stands.
func jobSelected(jobs []forge.WorkflowJob, stageJob string) (bool, bool) {
	for _, j := range jobs {
		if j.Name != stageJob {
			continue
		}
		return j.Conclusion != "skipped", true
	}
	return false, false
}

// routeSucceeded reports whether the candidate's Route job concluded
// success. The run's own conclusion is deliberately ignored: under
// `queue: single` a later event cancels the earlier pending stage job and
// the run concludes `cancelled` while its authorization stands.
func routeSucceeded(jobs []forge.WorkflowJob) bool {
	for _, j := range jobs {
		if routeJobName(j.Name) {
			return j.Conclusion == "success"
		}
	}
	return false
}

// boundToItem reports whether the candidate run concerns the work item this
// run is serving.
//
// PR events carry pull_requests[]. issue_comment and issues carry nothing
// usable, so the shim sets a run-name of "<owner/repo>#<number>", which the
// API returns as display_title. A candidate that matches neither is skipped
// rather than guessed at — a wrong binding steers one work item's agent with
// another's content.
func boundToItem(run forge.WorkflowRun, runName string, number int) bool {
	for _, n := range run.PullRequestNumbers {
		if n == number {
			return true
		}
	}
	return runName != "" && run.DisplayTitle == runName
}

// candidateChecks applies checks 2, 3, 6 of ADR 0101 §"provenance" — the
// ones answerable from the run record alone, without a second API call.
// Check 1 (same repository) is implicit in the API path.
func (w *Watcher) candidateChecks(run forge.WorkflowRun) *rejection {
	if int64(run.ID) == w.cfg.RunID {
		return &rejection{"self", "my own run"}
	}
	if w.seen[int64(run.ID)] {
		return &rejection{"once", "already judged"}
	}
	if run.Path != w.myRun.Path {
		return &rejection{"shim", fmt.Sprintf("path %q is not the shim %q", run.Path, w.myRun.Path)}
	}
	if !allowedEvents[run.Event] {
		return &rejection{"event", fmt.Sprintf("event %q is not a work-item update", run.Event)}
	}
	if !runCreatedAt(run).After(w.cfg.StartedAt) {
		return &rejection{"fresh", "created before my run started"}
	}
	if !sameDispatchChain(w.myRun.ReferencedWorkflows, run.ReferencedWorkflows) {
		return &rejection{"chain", "referenced_workflows differ from mine"}
	}
	if !boundToItem(run, w.cfg.RunName, w.cfg.Item.Number) {
		return &rejection{"item", "not bound to my work item"}
	}
	return nil
}

// jobChecks applies checks 4 and 5, which need the candidate's job list:
// its Route job authorized the event, and my stage was the one selected.
func (w *Watcher) jobChecks(ctx context.Context, run forge.WorkflowRun) (*rejection, error) {
	owner, repo, err := splitRepo(w.cfg.Repo)
	if err != nil {
		return nil, err
	}
	jobs, err := w.actions.ListWorkflowRunJobs(ctx, owner, repo, run.ID)
	if err != nil {
		return nil, err
	}
	if !routeSucceeded(jobs) {
		return &rejection{"route", "Route job did not conclude success"}, nil
	}
	selected, found := jobSelected(jobs, w.stageJob)
	if !found {
		return &rejection{"stage", fmt.Sprintf("no job named %q in the candidate run", w.stageJob)}, nil
	}
	if !selected {
		return &rejection{"stage", "my stage was skipped by the route job"}, nil
	}
	return nil, nil
}
