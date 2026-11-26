package forgejo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Forgejo Actions read operations. They have the signatures of the
// matching forge.Client methods, so a caller that needs only workflow-run
// observation can depend on that subset. Every operation is a read: none
// dispatches, cancels, or reruns a run.

const (
	// actionsPageSize is the page size of every Actions listing, the
	// Forgejo API's default maximum.
	actionsPageSize = 50
	// maxRunPages bounds how far ListWorkflowRunsSince and
	// ListRecentWorkflowRuns paginate, so a server that ignores the page
	// parameter cannot keep the client looping.
	maxRunPages = 40
	// maxArtifactPages bounds the artifact listing of one run.
	maxArtifactPages = 20
	// maxArtifactBytes bounds a downloaded artifact archive.
	maxArtifactBytes = 100 << 20
	// maxJobLogBytes bounds the log of one job.
	maxJobLogBytes = 10 << 20
)

// actionRun is the subset of Forgejo's ActionRun the client reads.
type actionRun struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	WorkflowID   string    `json:"workflow_id"`
	Event        string    `json:"event"`
	TriggerEvent string    `json:"trigger_event"`
	Status       string    `json:"status"`
	HTMLURL      string    `json:"html_url"`
	Created      time.Time `json:"created"`
}

// actionRunJob is the subset of Forgejo's ActionRunJob the client reads.
type actionRunJob struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Attempt int64  `json:"attempt"`
}

// actionArtifact is the subset of Forgejo's ActionArtifact the client
// reads.
type actionArtifact struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Expired bool   `json:"expired"`
}

// ActionsStatus maps a Forgejo Actions run or job status to the
// forge.WorkflowRun status and conclusion vocabulary: a finished status
// (success, failure, cancelled, skipped) becomes "completed" with that
// conclusion, running becomes "in_progress", waiting becomes "queued",
// and blocked (held for approval or on its needs) becomes "waiting".
// Any other value, including Forgejo's "unknown", is returned unchanged
// with no conclusion, so it never reads as finished.
func ActionsStatus(status string) (forgeStatus, conclusion string) {
	switch status {
	case "success", "failure", "cancelled", "skipped":
		return "completed", status
	case "running":
		return "in_progress", ""
	case "waiting":
		return "queued", ""
	case "blocked":
		return "waiting", ""
	default:
		return status, ""
	}
}

// toWorkflowRun converts a Forgejo run. Forgejo reports only the workflow
// file's base name and no workflow display name, so Name is the file name.
// Event is the workflow's `on` trigger (trigger_event) when Forgejo reports
// it, and the webhook event otherwise: a pull_request_target run carries
// the pull_request webhook event. CreatedAt is RFC 3339 in UTC.
func (r actionRun) toWorkflowRun() forge.WorkflowRun {
	status, conclusion := ActionsStatus(r.Status)
	event := r.TriggerEvent
	if event == "" {
		event = r.Event
	}
	created := ""
	if !r.Created.IsZero() {
		created = r.Created.UTC().Format(time.RFC3339)
	}
	return forge.WorkflowRun{
		ID:         int(r.ID),
		Name:       r.WorkflowID,
		Event:      event,
		Status:     status,
		Conclusion: conclusion,
		HTMLURL:    r.HTMLURL,
		CreatedAt:  created,
	}
}

// listRunsPage returns one page of runs, newest first, filtered to
// workflowFile's base name when it is not empty.
func (c *LiveClient) listRunsPage(ctx context.Context, owner, repo, workflowFile string, page, limit int) ([]actionRun, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("limit", strconv.Itoa(limit))
	if workflowFile != "" {
		q.Set("workflow_id", path.Base(workflowFile))
	}
	var out struct {
		WorkflowRuns []actionRun `json:"workflow_runs"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/actions/runs")+"?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.WorkflowRuns, nil
}

// ListWorkflowRuns returns the newest page of runs of workflowFile (every
// workflow when empty). Only the file's base name is compared, because
// Forgejo identifies a workflow by its file name.
func (c *LiveClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	runs, err := c.listRunsPage(ctx, owner, repo, workflowFile, 1, actionsPageSize)
	if err != nil {
		return nil, err
	}
	out := make([]forge.WorkflowRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.toWorkflowRun())
	}
	return out, nil
}

// ListWorkflowRunsSince returns the runs of workflowFile (every workflow
// when empty) created at or after since. It paginates until a page holds
// a run created before since or a short page ends the listing.
func (c *LiveClient) ListWorkflowRunsSince(ctx context.Context, owner, repo, workflowFile string, since time.Time) ([]forge.WorkflowRun, error) {
	var out []forge.WorkflowRun
	for page := 1; page <= maxRunPages; page++ {
		runs, err := c.listRunsPage(ctx, owner, repo, workflowFile, page, actionsPageSize)
		if err != nil {
			return nil, err
		}
		reachedOlder := false
		for _, r := range runs {
			if r.Created.Before(since) {
				reachedOlder = true
				continue
			}
			out = append(out, r.toWorkflowRun())
		}
		if reachedOlder || len(runs) < actionsPageSize {
			return out, nil
		}
	}
	return out, nil
}

// ListRecentWorkflowRuns returns at most perPage of the newest runs
// across every workflow, newest first. Path is the run's workflow file
// name, the only form Forgejo reports.
func (c *LiveClient) ListRecentWorkflowRuns(ctx context.Context, owner, repo string, perPage int) ([]forge.WorkflowRun, error) {
	if perPage <= 0 {
		return nil, nil
	}
	var out []forge.WorkflowRun
	for page := 1; page <= maxRunPages && len(out) < perPage; page++ {
		runs, err := c.listRunsPage(ctx, owner, repo, "", page, actionsPageSize)
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			if len(out) == perPage {
				break
			}
			run := r.toWorkflowRun()
			run.Path = r.WorkflowID
			out = append(out, run)
		}
		if len(runs) < actionsPageSize {
			break
		}
	}
	return out, nil
}

// GetWorkflowRun returns one run, or an error wrapping forge.ErrNotFound
// when it does not exist in owner/repo.
func (c *LiveClient) GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error) {
	var out actionRun
	if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/actions/runs/"+strconv.Itoa(runID)), nil, &out); err != nil {
		return nil, err
	}
	run := out.toWorkflowRun()
	return &run, nil
}

// ListWorkflowRunJobs returns the jobs of a run. Forgejo replaces a job
// whose matrix depends on another job's output with its expanded jobs
// once that output is known.
func (c *LiveClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	var jobs []actionRunJob
	if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/actions/runs/"+strconv.Itoa(runID)+"/jobs"), nil, &jobs); err != nil {
		return nil, err
	}
	out := make([]forge.WorkflowJob, 0, len(jobs))
	for _, j := range jobs {
		status, conclusion := ActionsStatus(j.Status)
		out = append(out, forge.WorkflowJob{
			ID:         int(j.ID),
			RunAttempt: int(j.Attempt),
			Name:       j.Name,
			Status:     status,
			Conclusion: conclusion,
		})
	}
	return out, nil
}

// ListWorkflowRunArtifacts returns the unexpired artifacts uploaded by a
// run. Only the run's own artifacts are listed; there is no cross-run
// lookup.
func (c *LiveClient) ListWorkflowRunArtifacts(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowArtifact, error) {
	var out []forge.WorkflowArtifact
	base := c.repoURL(owner, repo, "/actions/runs/"+strconv.Itoa(runID)+"/artifacts")
	for page := 1; page <= maxArtifactPages; page++ {
		var arts []actionArtifact
		q := "?page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(actionsPageSize)
		if err := c.do(ctx, http.MethodGet, base+q, nil, &arts); err != nil {
			return nil, err
		}
		for _, a := range arts {
			if a.Expired {
				continue
			}
			out = append(out, forge.WorkflowArtifact{ID: int(a.ID), Name: a.Name})
		}
		if len(arts) < actionsPageSize {
			break
		}
	}
	return out, nil
}

// DownloadWorkflowRunArtifact returns an artifact's zip archive. An
// instance that serves artifacts directly from object storage answers
// with a redirect, which the client refuses like every other redirect.
func (c *LiveClient) DownloadWorkflowRunArtifact(ctx context.Context, owner, repo string, artifactID int) ([]byte, error) {
	return c.raw(ctx, c.repoURL(owner, repo, "/actions/artifacts/"+strconv.Itoa(artifactID)+"/zip"), "application/zip", maxArtifactBytes)
}

// GetWorkflowRunLogs returns the plain-text log of every job of a run
// that was not skipped, each under a header naming the job. A job whose
// log Forgejo cannot serve (it never ran, or its log expired) gets a note
// in place of its log.
func (c *LiveClient) GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	var jobs []actionRunJob
	if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/actions/runs/"+strconv.Itoa(runID)+"/jobs"), nil, &jobs); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, j := range jobs {
		if j.Status == "skipped" {
			continue
		}
		fmt.Fprintf(&b, "=== job %d: %s (%s) ===\n", j.ID, j.Name, j.Status)
		data, err := c.raw(ctx, c.repoURL(owner, repo, "/actions/jobs/"+strconv.FormatInt(j.ID, 10)+"/logs"), "text/plain", maxJobLogBytes)
		switch {
		case errors.Is(err, forge.ErrNotFound):
			b.WriteString("(no log available)\n")
		case err != nil:
			return "", err
		default:
			b.Write(data)
			if len(data) > 0 && data[len(data)-1] != '\n' {
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}
