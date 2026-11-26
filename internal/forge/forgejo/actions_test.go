package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// fakeActions serves the Forgejo Actions read API for one repository
// under a path prefix and records every request. Runs are listed newest
// first, as Forgejo lists them.
type fakeActions struct {
	mu        sync.Mutex
	runs      []map[string]any // newest first
	handlers  map[string]http.HandlerFunc
	requests  []string
	nonGetHit bool
}

const fakePrefix = "/forgejo"

func (f *fakeActions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	if r.Method != http.MethodGet {
		f.nonGetHit = true
	}
	f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, fakePrefix+"/api/v1/repos/o/r")
	if h, ok := f.handlers[path]; ok {
		h(w, r)
		return
	}
	if path == "/actions/runs" {
		f.listRuns(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"not found"}`))
}

func (f *fakeActions) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	var matched []map[string]any
	for _, run := range f.runs {
		if wf := q.Get("workflow_id"); wf != "" && run["workflow_id"] != wf {
			continue
		}
		matched = append(matched, run)
	}
	start := min((page-1)*limit, len(matched))
	end := min(start+limit, len(matched))
	writeActionsJSON(w, map[string]any{"total_count": len(matched), "workflow_runs": matched[start:end]})
}

func writeActionsJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newFakeActions(t *testing.T) (*fakeActions, *LiveClient) {
	t.Helper()
	f := &fakeActions{handlers: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+fakePrefix+"/", "tok", srv.Client())
	require.NoError(t, err)
	return f, c
}

func fakeRun(id int, workflow, event, triggerEvent, status string, created time.Time) map[string]any {
	return map[string]any{
		"id": id, "workflow_id": workflow, "event": event, "trigger_event": triggerEvent,
		"status": status, "html_url": fmt.Sprintf("https://forgejo.example/o/r/actions/runs/%d", id),
		"title": "run title", "created": created.Format(time.RFC3339),
	}
}

func TestActionsStatus(t *testing.T) {
	t.Parallel()
	for in, want := range map[string][2]string{
		"success":   {"completed", "success"},
		"failure":   {"completed", "failure"},
		"cancelled": {"completed", "cancelled"},
		"skipped":   {"completed", "skipped"},
		"running":   {"in_progress", ""},
		"waiting":   {"queued", ""},
		"blocked":   {"waiting", ""},
		"unknown":   {"unknown", ""},
	} {
		status, conclusion := ActionsStatus(in)
		assert.Equal(t, want, [2]string{status, conclusion}, in)
	}
}

func TestListWorkflowRunsFiltersByWorkflowFileUnderPathPrefix(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	created := time.Date(2026, 11, 24, 7, 0, 0, 0, time.FixedZone("CET", 3600))
	f.runs = []map[string]any{
		fakeRun(3, "other.yaml", "push", "push", "running", created),
		fakeRun(2, "fullsend.yaml", "pull_request", "pull_request_target", "success", created),
		fakeRun(1, "fullsend.yaml", "issues", "", "waiting", created),
	}

	runs, err := c.ListWorkflowRuns(context.Background(), "o", "r", ".forgejo/workflows/fullsend.yaml")
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, forge.WorkflowRun{
		ID: 2, Name: "fullsend.yaml", Event: "pull_request_target", Status: "completed", Conclusion: "success",
		HTMLURL: "https://forgejo.example/o/r/actions/runs/2", CreatedAt: "2026-11-24T06:00:00Z",
	}, runs[0])
	assert.Equal(t, "issues", runs[1].Event, "the webhook event is used when no trigger event is reported")
	assert.Equal(t, "queued", runs[1].Status)
	assert.Empty(t, runs[1].Path)
	require.NotEmpty(t, f.requests)
	assert.Equal(t, "GET "+fakePrefix+"/api/v1/repos/o/r/actions/runs?limit=50&page=1&workflow_id=fullsend.yaml", f.requests[0])
}

func TestListWorkflowRunsSincePaginatesToTheBoundary(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	since := time.Date(2026, 11, 24, 7, 0, 0, 0, time.UTC)
	for i := 120; i >= 1; i-- {
		// Runs 61..120 are at or after since; older ones are before.
		created := since.Add(time.Duration(i-61) * time.Second)
		f.runs = append(f.runs, fakeRun(i, "fullsend.yaml", "issues", "issues", "success", created))
	}

	runs, err := c.ListWorkflowRunsSince(context.Background(), "o", "r", "fullsend.yaml", since)
	require.NoError(t, err)
	assert.Len(t, runs, 60)
	assert.Equal(t, 120, runs[0].ID)
	assert.Equal(t, 61, runs[59].ID)
	assert.Len(t, f.requests, 2, "pagination stops at the page holding an older run")
}

func TestListRecentWorkflowRunsSetsPathAndCapsTheCount(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	now := time.Now()
	for i := 70; i >= 1; i-- {
		f.runs = append(f.runs, fakeRun(i, "wf"+strconv.Itoa(i%2)+".yaml", "push", "", "success", now))
	}

	runs, err := c.ListRecentWorkflowRuns(context.Background(), "o", "r", 60)
	require.NoError(t, err)
	require.Len(t, runs, 60)
	assert.Equal(t, "wf0.yaml", runs[0].Path)
	assert.Equal(t, 11, runs[59].ID)

	none, err := c.ListRecentWorkflowRuns(context.Background(), "o", "r", 0)
	require.NoError(t, err)
	assert.Empty(t, none)

	all, err := c.ListRecentWorkflowRuns(context.Background(), "o", "r", 500)
	require.NoError(t, err)
	assert.Len(t, all, 70, "a short page ends the listing")
}

func TestGetWorkflowRunAndJobs(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	f.handlers["/actions/runs/7"] = func(w http.ResponseWriter, _ *http.Request) {
		writeActionsJSON(w, fakeRun(7, "fullsend.yaml", "issue_comment", "issue_comment", "failure", time.Now()))
	}
	f.handlers["/actions/runs/7/jobs"] = func(w http.ResponseWriter, _ *http.Request) {
		writeActionsJSON(w, []map[string]any{
			{"id": 70, "name": "dispatch / Harness run (triage)", "status": "failure", "attempt": 2},
			{"id": 71, "name": "Route", "status": "running", "attempt": 1},
		})
	}

	run, err := c.GetWorkflowRun(context.Background(), "o", "r", 7)
	require.NoError(t, err)
	assert.Equal(t, "completed", run.Status)
	assert.Equal(t, "failure", run.Conclusion)

	jobs, err := c.ListWorkflowRunJobs(context.Background(), "o", "r", 7)
	require.NoError(t, err)
	assert.Equal(t, []forge.WorkflowJob{
		{ID: 70, RunAttempt: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"},
		{ID: 71, RunAttempt: 1, Name: "Route", Status: "in_progress"},
	}, jobs)

	_, err = c.GetWorkflowRun(context.Background(), "o", "r", 8)
	require.ErrorIs(t, err, forge.ErrNotFound)
}

func TestListWorkflowRunArtifactsPaginatesAndSkipsExpired(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	f.handlers["/actions/runs/5/artifacts"] = func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var arts []map[string]any
		if page == 1 {
			for i := range 50 {
				arts = append(arts, map[string]any{"id": i + 1, "name": "a" + strconv.Itoa(i), "expired": i == 0})
			}
		} else {
			arts = []map[string]any{{"id": 99, "name": "fullsend-triage", "run_id": 5}}
		}
		writeActionsJSON(w, arts)
	}

	arts, err := c.ListWorkflowRunArtifacts(context.Background(), "o", "r", 5)
	require.NoError(t, err)
	require.Len(t, arts, 50)
	assert.Equal(t, forge.WorkflowArtifact{ID: 99, Name: "fullsend-triage"}, arts[49])
}

func TestDownloadWorkflowRunArtifact(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	f.handlers["/actions/artifacts/3/zip"] = func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/zip", r.Header.Get("Accept"))
		_, _ = w.Write([]byte("PK-zip"))
	}
	f.handlers["/actions/artifacts/4/zip"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://objects.example/signed", http.StatusFound)
	}

	data, err := c.DownloadWorkflowRunArtifact(context.Background(), "o", "r", 3)
	require.NoError(t, err)
	assert.Equal(t, []byte("PK-zip"), data)

	_, err = c.DownloadWorkflowRunArtifact(context.Background(), "o", "r", 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to follow redirect")
}

func TestRawRejectsAnOversizedBody(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	f.handlers["/big"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 11)))
	}

	_, err := c.raw(context.Background(), c.repoURL("o", "r", "/big"), "text/plain", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds 10 bytes")

	data, err := c.raw(context.Background(), c.repoURL("o", "r", "/big"), "text/plain", 11)
	require.NoError(t, err)
	assert.Len(t, data, 11)
}

func TestGetWorkflowRunLogs(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	f.handlers["/actions/runs/9/jobs"] = func(w http.ResponseWriter, _ *http.Request) {
		writeActionsJSON(w, []map[string]any{
			{"id": 1, "name": "Route", "status": "success"},
			{"id": 2, "name": "Code", "status": "skipped"},
			{"id": 3, "name": "Harness run (triage)", "status": "failure"},
			{"id": 4, "name": "Review", "status": "blocked"},
		})
	}
	f.handlers["/actions/jobs/1/logs"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("routed"))
	}
	f.handlers["/actions/jobs/3/logs"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("boom\n"))
	}
	f.handlers["/actions/jobs/2/logs"] = func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a skipped job's log must not be fetched")
	}

	logs, err := c.GetWorkflowRunLogs(context.Background(), "o", "r", 9)
	require.NoError(t, err)
	assert.Equal(t, "=== job 1: Route (success) ===\nrouted\n"+
		"=== job 3: Harness run (triage) (failure) ===\nboom\n"+
		"=== job 4: Review (blocked) ===\n(no log available)\n", logs)

	f.handlers["/actions/jobs/1/logs"] = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}
	_, err = c.GetWorkflowRunLogs(context.Background(), "o", "r", 9)
	require.Error(t, err)

	_, err = c.GetWorkflowRunLogs(context.Background(), "o", "r", 10)
	require.ErrorIs(t, err, forge.ErrNotFound)
}

func TestActionsReadsNeverMutate(t *testing.T) {
	t.Parallel()
	f, c := newFakeActions(t)
	f.runs = []map[string]any{fakeRun(1, "fullsend.yaml", "issues", "issues", "failure", time.Now())}
	ctx := context.Background()
	_, _ = c.ListWorkflowRuns(ctx, "o", "r", "")
	_, _ = c.ListWorkflowRunsSince(ctx, "o", "r", "", time.Time{})
	_, _ = c.ListRecentWorkflowRuns(ctx, "o", "r", 10)
	_, _ = c.GetWorkflowRun(ctx, "o", "r", 1)
	_, _ = c.ListWorkflowRunJobs(ctx, "o", "r", 1)
	_, _ = c.ListWorkflowRunArtifacts(ctx, "o", "r", 1)
	_, _ = c.DownloadWorkflowRunArtifact(ctx, "o", "r", 1)
	_, _ = c.GetWorkflowRunLogs(ctx, "o", "r", 1)
	assert.False(t, f.nonGetHit, "an Actions read must never dispatch, cancel, or rerun")
	for _, req := range f.requests {
		assert.NotContains(t, req, "rerun")
	}
}
