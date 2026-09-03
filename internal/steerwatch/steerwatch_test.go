package steerwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/github"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

const (
	myRunID   = int64(33740015232)
	shimPath  = ".github/workflows/fullsend.yml"
	stageName = "dispatch / Review"
	runStart  = "2026-09-03T10:00:00Z"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return ts
}

// refWorkflows is the shape the Actions API really returns for a run that
// called the reusable dispatch workflow.
func refWorkflows() []map[string]string {
	return []map[string]string{{
		"path": "fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@main",
		"ref":  "refs/heads/main",
		"sha":  "2a103497ee1e9a1b3c4d5e6f7a8b9c0d1e2f3a4b",
	}}
}

// runJSON builds a workflow run record with the fields the checks read.
type runOpts struct {
	id         int64
	event      string
	path       string
	created    string
	title      string
	prNumbers  []int
	refs       []map[string]string
	conclusion string
}

func runJSON(o runOpts) map[string]any {
	if o.path == "" {
		o.path = shimPath
	}
	if o.event == "" {
		o.event = "issue_comment"
	}
	if o.created == "" {
		o.created = "2026-09-03T10:05:00Z"
	}
	if o.refs == nil {
		o.refs = refWorkflows()
	}
	prs := make([]map[string]any, 0, len(o.prNumbers))
	for _, n := range o.prNumbers {
		prs = append(prs, map[string]any{"number": n})
	}
	return map[string]any{
		"id":                   o.id,
		"name":                 "fullsend",
		"path":                 o.path,
		"event":                o.event,
		"status":               "completed",
		"conclusion":           o.conclusion,
		"created_at":           o.created,
		"display_title":        o.title,
		"actor":                map[string]any{"login": "octocat"},
		"triggering_actor":     map[string]any{"login": "reviewer"},
		"pull_requests":        prs,
		"referenced_workflows": o.refs,
	}
}

// jobsJSON builds a run's job list. A stage the route job did not select is
// completed/skipped, which is the negative case the checks turn on.
func jobsJSON(jobs ...map[string]string) map[string]any {
	out := make([]map[string]string, 0, len(jobs))
	out = append(out, jobs...)
	return map[string]any{"total_count": len(out), "jobs": out}
}

func routeJob(conclusion string) map[string]string {
	return map[string]string{"name": "dispatch / Route", "status": "completed", "conclusion": conclusion}
}

func stageJob(name, status, conclusion string) map[string]string {
	return map[string]string{"name": name, "status": status, "conclusion": conclusion}
}

// fakeAPI serves the three Actions endpoints the watcher reads.
type fakeAPI struct {
	mu sync.Mutex

	myRun    map[string]any
	myJobs   map[string]any
	listed   [][]map[string]any // one entry per poll, consumed in order
	jobsByID map[int64]map[string]any
	status   map[string]int // path prefix -> status to return instead of 200

	listCalls int
	jobCalls  map[int64]int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		jobsByID: map[int64]map[string]any{},
		status:   map[string]int{},
		jobCalls: map[int64]int{},
	}
}

func (f *fakeAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		for prefix, code := range f.status {
			if strings.Contains(r.URL.Path, prefix) {
				w.WriteHeader(code)
				return
			}
		}

		write := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(v))
		}

		switch {
		case strings.HasSuffix(r.URL.Path, fmt.Sprintf("/actions/runs/%d", myRunID)):
			write(f.myRun)
		case strings.HasSuffix(r.URL.Path, fmt.Sprintf("/actions/runs/%d/jobs", myRunID)):
			write(f.myJobs)
		case strings.Contains(r.URL.Path, "/actions/workflows/"):
			var page []map[string]any
			if f.listCalls < len(f.listed) {
				page = f.listed[f.listCalls]
			} else if len(f.listed) > 0 {
				page = f.listed[len(f.listed)-1]
			}
			f.listCalls++
			write(map[string]any{"total_count": len(page), "workflow_runs": page})
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			var id int64
			_, err := fmt.Sscanf(r.URL.Path[strings.LastIndex(r.URL.Path, "/runs/"):], "/runs/%d/jobs", &id)
			require.NoError(t, err)
			f.jobCalls[id]++
			jobs, ok := f.jobsByID[id]
			if !ok {
				jobs = jobsJSON()
			}
			write(jobs)
		default:
			t.Errorf("unexpected request: %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubItems is an ItemReader whose responses the test sets directly.
type stubItems struct {
	issue    *forge.Issue
	comments []forge.IssueComment
	reviews  []forge.PullRequestReview
	headSHA  string
	// notAPR makes GetPullRequestHeadSHA report ErrNotFound, which is how
	// the forge answers for an issue number and how Start tells the two
	// kinds of work item apart.
	notAPR bool
	err    error
}

func (s *stubItems) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.issue, nil
}

func (s *stubItems) ListIssueComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.comments, nil
}

func (s *stubItems) GetPullRequestHeadSHA(context.Context, string, string, int) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.notAPR {
		return "", fmt.Errorf("get pull request: %w", forge.ErrNotFound)
	}
	return s.headSHA, nil
}

func (s *stubItems) ListPullRequestReviews(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.reviews, nil
}

// recorder captures what the watcher delivered.
type recorder struct {
	mu       sync.Mutex
	msgs     []agentruntime.SteerMessage
	settled  int
	deliverE error
}

func (r *recorder) deliver(_ context.Context, m agentruntime.SteerMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deliverE != nil {
		return r.deliverE
	}
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recorder) settle(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled++
	return nil
}

func (r *recorder) delivered() []agentruntime.SteerMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agentruntime.SteerMessage(nil), r.msgs...)
}

func (r *recorder) settleCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settled
}

// newWatcher wires a started watcher against the fake API.
func newWatcher(t *testing.T, api *fakeAPI, items ItemReader, rec *recorder, mutate func(*Config)) *Watcher {
	t.Helper()
	if api.myRun == nil {
		api.myRun = runJSON(runOpts{id: myRunID, created: runStart})
	}
	if api.myJobs == nil {
		api.myJobs = jobsJSON(routeJob("success"), stageJob(stageName, "in_progress", ""))
	}
	srv := api.server(t)

	cfg := Config{
		Repo:         "org/repo",
		RunID:        myRunID,
		RunName:      "org/repo#7",
		StartedAt:    mustTime(t, runStart),
		MaxSteers:    2,
		PollInterval: time.Millisecond,
		Item:         WorkItem{IsPullRequest: true, Number: 7, HeadSHA: "aaa111"},
	}
	if mutate != nil {
		mutate(&cfg)
	}

	// A real forge client pointed at the fake API, so the adapter's own
	// decoding is exercised rather than stubbed past.
	w := New(cfg, testGitHubClient(srv.URL), items, rec.deliver, rec.settle)
	require.NoError(t, w.Start(context.Background(), "review"))
	return w
}

// testGitHubClient is a forge client pointed at the fake API, with its retry
// backoff collapsed so a test that exercises a 5xx path does not sleep
// through the real schedule.
func testGitHubClient(baseURL string) *github.LiveClient {
	return github.New("job-token").WithBaseURL(baseURL).
		WithAfterFunc(func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Now()
			return ch
		})
}

// forgeRun builds the decoded run the check functions take, from the same
// options runJSON renders on the wire. The wire-to-forge decoding itself is
// exercised through the real adapter wherever a test goes via the fake API.
func forgeRun(o runOpts) forge.WorkflowRun {
	if o.path == "" {
		o.path = shimPath
	}
	if o.event == "" {
		o.event = "issue_comment"
	}
	if o.created == "" {
		o.created = "2026-09-03T10:05:00Z"
	}
	if o.refs == nil {
		o.refs = refWorkflows()
	}
	run := forge.WorkflowRun{
		ID:              int(o.id),
		Name:            "fullsend",
		Path:            o.path,
		Event:           o.event,
		Status:          "completed",
		Conclusion:      o.conclusion,
		CreatedAt:       o.created,
		DisplayTitle:    o.title,
		Actor:           "octocat",
		TriggeringActor: "reviewer",
	}
	run.PullRequestNumbers = append(run.PullRequestNumbers, o.prNumbers...)
	for _, w := range o.refs {
		run.ReferencedWorkflows = append(run.ReferencedWorkflows,
			forge.ReferencedWorkflow{Path: w["path"], Ref: w["ref"], SHA: w["sha"]})
	}
	return run
}
