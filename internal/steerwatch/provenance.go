package steerwatch

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// amendmentEvents are the events whose run record's actor is guaranteed to
// be the same principal the route job authorized. Only those confer
// amendment authority; every other accepted event contributes context.
//
// The run record exposes `event` but not the action, so an event qualifies
// only if EVERY arm handling it checks the login the run reports. Audited
// against reusable-dispatch.yml:
//
//	issue_comment              every slash-command arm checks
//	                           github.event.comment.user.login, which is the
//	                           author of the comment and the run's sender.
//	                           ELIGIBLE.
//	issues                     opened checks issue.user.login and edited
//	                           checks sender.login (both the reported actor),
//	                           but `labeled` with ready-for-triage or
//	                           ready-for-review checks NOTHING and still
//	                           selects a stage. The action is invisible in the
//	                           run record, so the authorized arms cannot be
//	                           told from the unauthorized ones. EXCLUDED.
//	pull_request_target        opened/synchronize/ready_for_review check
//	                           pull_request.user.login — the PR AUTHOR — while
//	                           the run's actor is whoever pushed. On a fork PR
//	                           those are different people, and the pusher needs
//	                           no upstream permission at all. labeled and
//	                           closed check nothing. EXCLUDED.
//	pull_request_review        checks pull_request.user.login while the run's
//	                           actor is the review submitter, which the arm
//	                           requires to be the review App. EXCLUDED (and
//	                           bot actors are filtered out regardless).
//	pull_request_review_comment
//	                           has no arm at all, so every stage job is
//	                           skipped and check 5 already rejects it.
//	                           EXCLUDED.
//
// A push, a label, and a closure are state changes rather than instructions,
// which is the same reason issue title, body and label edits are context —
// so excluding them costs nothing the agent needs. A head move still reaches
// the agent as context, carrying the new SHA.
var amendmentEvents = map[string]bool{
	"issue_comment": true,
}

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
	// pending marks a verdict of "not yet" rather than "no" — the Route job
	// is still running, or the stage job is not in the listing yet. The
	// caller leaves such a run unseen so a later poll can judge it.
	pending bool
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
// That holds exactly as far as the ref is protected: whoever can push to
// the ref the shim pins can change the Route job between two events, so a
// shim pinned to an unprotected ref extends this trust to them. The refs
// fullsend pins are branch-protected, and that protection is an invariant
// this check relies on rather than verifies (ADR 0118).
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

// routeVerdict reports whether the candidate's Route job concluded success,
// whether the answer is still pending, and whether the run has no Route
// job at all. The run's own conclusion is deliberately not read as the
// verdict: under `queue: single` a later event cancels the earlier pending
// stage job and the run concludes `cancelled` while its authorization
// stands.
//
// An empty conclusion means the job is queued or running — "not yet", since
// treating it as a refusal would reject a legitimate update for having been
// polled a moment early. A listing with no Route job is "not yet" only
// while the run itself is still running; once the run has completed it is
// final: the shim's own `if` skipped the event and never called the
// reusable workflow, which is what every run fired by the runner's own
// status comment looks like (jobs `stop-fix` and the bare `dispatch`
// wrapper, both skipped). Keeping such a run pending would re-examine it
// on every poll until the deadline.
func routeVerdict(run forge.WorkflowRun, jobs []forge.WorkflowJob) (ok, pending, noRoute bool) {
	for _, j := range jobs {
		if routeJobName(j.Name) {
			if j.Conclusion == "" {
				return false, true, false
			}
			return j.Conclusion == "success", false, false
		}
	}
	if run.Status == "completed" {
		return false, false, true
	}
	return false, true, false
}

// boundToItem reports whether the candidate run concerns the work item this
// run is serving.
//
// PR events carry pull_requests[], and when that list is present it is the
// verdict: a run the forge already associated with other pull requests is
// another item's run, whatever its title says. issue_comment and issues
// carry no association, so they bind only when the run's display_title
// equals runName — the "<owner/repo>#<number>" a shim declares as its
// run-name (Config.RunName). A candidate that matches neither is skipped
// rather than guessed at — a wrong binding steers one work item's agent
// with another's content.
func boundToItem(run forge.WorkflowRun, runName string, number int) bool {
	if len(run.PullRequestNumbers) > 0 {
		for _, n := range run.PullRequestNumbers {
			if n == number {
				return true
			}
		}
		return false
	}
	return runName != "" && runNameItem(run.DisplayTitle) == runName
}

// runNameItem returns the work-item half of a display title: the shim's
// run-name is "<owner/repo>#<number>", optionally followed by
// " comment:<id>" on an issue_comment run so the watcher can bind that run
// to its comment exactly (see runCommentID).
func runNameItem(title string) string {
	if i := strings.Index(title, " comment:"); i >= 0 {
		return title[:i]
	}
	return title
}

// runCommentID returns the comment id a display title names, or 0 when the
// shim did not declare one.
func runCommentID(title string) int {
	i := strings.Index(title, " comment:")
	if i < 0 {
		return 0
	}
	id, err := strconv.Atoi(strings.TrimSpace(title[i+len(" comment:"):]))
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// isFollowUp reports whether a candidate was created after my own run.
//
// The comparison is against my run's server-side created_at rather than the
// runner's clock, so a follow-up that landed while my job was still queued is
// not stranded with the pending run. Both timestamps come from the same
// server, so a same-second tie is real rather than clock skew, and it is
// broken by run id: GitHub allocates them monotonically, so a larger id at
// the same second was created later.
func (w *Watcher) isFollowUp(run forge.WorkflowRun) bool {
	created := runCreatedAt(run)
	switch {
	case created.After(w.freshAfter):
		return true
	case created.Equal(w.freshAfter):
		return int64(run.ID) > w.cfg.RunID
	default:
		return false
	}
}

// candidateChecks applies checks 2, 3, 6 of the provenance table in
// docs/contributing/steering.md — the
// ones answerable from the run record alone, without a second API call.
// Check 1 (same repository) is implicit in the API path.
func (w *Watcher) candidateChecks(run forge.WorkflowRun) *rejection {
	if int64(run.ID) == w.cfg.RunID {
		return &rejection{check: "self", detail: "my own run"}
	}
	if run.Path != w.myRun.Path {
		return &rejection{check: "shim", detail: fmt.Sprintf("path %q is not the shim %q", run.Path, w.myRun.Path)}
	}
	if !allowedEvents[run.Event] {
		return &rejection{check: "event", detail: fmt.Sprintf("event %q is not a work-item update", run.Event)}
	}
	if !w.isFollowUp(run) {
		return &rejection{check: "fresh", detail: "not created after my own run"}
	}
	if !sameDispatchChain(w.myRun.ReferencedWorkflows, run.ReferencedWorkflows) {
		return &rejection{check: "chain", detail: "referenced_workflows differ from mine"}
	}
	if !boundToItem(run, w.cfg.RunName, w.cfg.Item.Number) {
		return &rejection{check: "item", detail: "not bound to my work item"}
	}
	if run.Event == "issue_comment" {
		// Recorded once the run is known to be this shim's, on this work
		// item, and a follow-up — before the job checks, so a run whose
		// Route job refuses its actor is still in view for bindTriggers and
		// keeps its own comment, and before the once check, so a run this
		// watcher (or an earlier iteration's, via AlreadySeen) already
		// judged still occupies its slot in the pairing on every poll.
		// Another item's run must not be recorded: it would take a slot and
		// leave this item's run unbound.
		w.mu.Lock()
		w.observed[int64(run.ID)] = run
		w.mu.Unlock()
	}
	if w.seen[int64(run.ID)] {
		return &rejection{check: "once", detail: "already judged"}
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
	routeOK, routePending, noRoute := routeVerdict(run, jobs)
	if noRoute {
		return &rejection{check: "route", detail: "no Route job: the shim did not dispatch this event"}, nil
	}
	if routePending {
		return &rejection{check: "route", detail: "Route job has not concluded yet", pending: true}, nil
	}
	if !routeOK {
		return &rejection{check: "route", detail: "Route job did not conclude success"}, nil
	}
	selected, found := jobSelected(jobs, w.stageJob)
	if !found {
		// A completed run will never list a job it does not have; only a
		// run still going may not have reported mine yet.
		return &rejection{
			check:   "stage",
			detail:  fmt.Sprintf("no job named %q in the candidate run", w.stageJob),
			pending: run.Status != "completed",
		}, nil
	}
	if !selected {
		return &rejection{check: "stage", detail: "my stage was skipped by the route job"}, nil
	}
	return nil, nil
}
