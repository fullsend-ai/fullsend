package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowReaders_RunAttempt(t *testing.T) {
	const run = `{"id":42,"run_attempt":2,"status":"completed","created_at":"2026-01-01T00:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/actions/runs/42":
			fmt.Fprint(w, run)
		case "/repos/org/repo/actions/runs/42/jobs":
			fmt.Fprintf(w, `{"jobs":[%s]}`, run)
		default:
			fmt.Fprintf(w, `{"workflow_runs":[%s]}`, run)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()
	for name, read := range map[string]func() ([]forge.WorkflowRun, error){
		"latest": func() ([]forge.WorkflowRun, error) {
			r, err := c.GetLatestWorkflowRun(ctx, "org", "repo", "fullsend.yaml")
			require.NoError(t, err)
			return []forge.WorkflowRun{*r}, nil
		},
		"by ID": func() ([]forge.WorkflowRun, error) {
			r, err := c.GetWorkflowRun(ctx, "org", "repo", 42)
			require.NoError(t, err)
			return []forge.WorkflowRun{*r}, nil
		},
		"workflow": func() ([]forge.WorkflowRun, error) { return c.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml") },
		"since": func() ([]forge.WorkflowRun, error) {
			return c.ListWorkflowRunsSince(ctx, "org", "repo", "fullsend.yaml", time.Time{})
		},
		"recent": func() ([]forge.WorkflowRun, error) { return c.ListRecentWorkflowRuns(ctx, "org", "repo", 10) },
	} {
		t.Run(name, func(t *testing.T) {
			runs, err := read()
			require.NoError(t, err)
			require.Len(t, runs, 1)
			assert.Equal(t, 2, runs[0].RunAttempt)
		})
	}
	jobs, err := c.ListWorkflowRunJobs(ctx, "org", "repo", 42)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, 2, jobs[0].RunAttempt)
}
