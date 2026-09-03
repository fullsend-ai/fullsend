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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// defaultAPIBase is the GitHub REST root. Tests point this at an httptest
// server instead.
const defaultAPIBase = "https://api.github.com"

// listPerPage bounds one follow-up run listing. A run that produces more
// than this many follow-ups in its lifetime is far past the steer cap.
const listPerPage = 50

// apiTimeout bounds a single Actions API call. The watcher runs beside a
// live agent; a hung poll must not hold the loop.
const apiTimeout = 30 * time.Second

// referencedWorkflow is one entry of a run's referenced_workflows: the
// reusable workflows that run called, pinned by ref (and reporting the sha).
type referencedWorkflow struct {
	Path string `json:"path"`
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
}

// workflowRun is the subset of a run record the provenance checks read.
// Every field here is written by the forge, not by the sender.
type workflowRun struct {
	ID              int64                  `json:"id"`
	Name            string                 `json:"name"`
	Path            string                 `json:"path"`
	Event           string                 `json:"event"`
	Status          string                 `json:"status"`
	Conclusion      string                 `json:"conclusion"`
	CreatedAt       time.Time              `json:"created_at"`
	DisplayTitle    string                 `json:"display_title"`
	Actor           struct{ Login string } `json:"actor"`
	TriggeringActor struct{ Login string } `json:"triggering_actor"`
	PullRequests    []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
	ReferencedWorkflows []referencedWorkflow `json:"referenced_workflows"`
}

// actorLogin returns the login that caused the run, preferring the
// triggering actor (the human who commented) over the run's actor.
func (r workflowRun) actorLogin() string {
	if r.TriggeringActor.Login != "" {
		return r.TriggeringActor.Login
	}
	return r.Actor.Login
}

// job is the subset of a run's job record the provenance checks read.
type job struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// actionsClient is a read-only GitHub Actions client scoped to one
// repository. It holds the JOB token — the GH_TOKEN the action passed in
// before the runner swapped in the minted role token — because that is the
// token every stage job already grants `actions: write` to, and reading
// runs needs nothing more.
type actionsClient struct {
	http    *http.Client
	baseURL string
	token   string
	repo    string // owner/repo
}

func newActionsClient(hc *http.Client, baseURL, token, repo string) *actionsClient {
	if hc == nil {
		hc = &http.Client{Timeout: apiTimeout}
	}
	if baseURL == "" {
		baseURL = defaultAPIBase
	}
	return &actionsClient{http: hc, baseURL: strings.TrimSuffix(baseURL, "/"), token: token, repo: repo}
}

func (c *actionsClient) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The body can carry the token in an error echo; report the status
		// and the path only.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s: unexpected status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	return nil
}

// Run fetches one workflow run record.
func (c *actionsClient) Run(ctx context.Context, runID int64) (workflowRun, error) {
	var r workflowRun
	err := c.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d", c.repo, runID), &r)
	return r, err
}

// Jobs fetches the jobs of one workflow run.
func (c *actionsClient) Jobs(ctx context.Context, runID int64) ([]job, error) {
	var out struct {
		Jobs []job `json:"jobs"`
	}
	err := c.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=100", c.repo, runID), &out)
	return out.Jobs, err
}

// RunsSince lists runs of one workflow file created at or after since.
//
// The per-workflow endpoint is used rather than the repository-wide one so a
// run of any other workflow is filtered out server-side. `event` is NOT
// passed: the endpoint accepts a single event value and the allowlist has
// five, so events are filtered client-side by acceptEvent.
func (c *actionsClient) RunsSince(ctx context.Context, workflowFile string, since time.Time) ([]workflowRun, error) {
	q := url.Values{}
	q.Set("created", ">="+since.UTC().Format(time.RFC3339))
	q.Set("per_page", fmt.Sprintf("%d", listPerPage))
	path := fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?%s",
		c.repo, url.PathEscape(workflowFile), q.Encode())

	var out struct {
		Runs []workflowRun `json:"workflow_runs"`
	}
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	sort.Slice(out.Runs, func(i, j int) bool { return out.Runs[i].CreatedAt.Before(out.Runs[j].CreatedAt) })
	return out.Runs, nil
}
