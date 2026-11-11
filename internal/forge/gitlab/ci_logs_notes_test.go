package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetWorkflowRunLogs_TraceTransportErrorIsMarked verifies that a job
// whose trace request fails at the transport level is reported with the
// shared error-fetching-trace note, so callers can tell the snapshot is
// incomplete.
func TestGetWorkflowRunLogs_TraceTransportErrorIsMarked(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/30/jobs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"name":"build","status":"success"}]`)
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/jobs/1/trace", func(w http.ResponseWriter, _ *http.Request) {
		// Kill the connection mid-request so c.do itself errors.
		hj, ok := w.(http.Hijacker)
		if !assert.True(t, ok) {
			return
		}
		conn, _, err := hj.Hijack()
		if !assert.NoError(t, err) {
			return
		}
		conn.Close()
	})
	logs, err := client.GetWorkflowRunLogs(context.Background(), "o", "r", 30)
	require.NoError(t, err)
	assert.Contains(t, logs, "error fetching trace:")
}

// TestGetWorkflowRunLogs_TraceReadErrorIsMarked verifies that a trace body
// that cannot be read in full is reported with the shared
// error-reading-trace note, so callers can tell the snapshot is incomplete.
func TestGetWorkflowRunLogs_TraceReadErrorIsMarked(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/30/jobs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"name":"build","status":"success"}]`)
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/jobs/1/trace", func(w http.ResponseWriter, _ *http.Request) {
		// Promise more body than is written, then close, so the
		// client's read fails (the github client's
		// TestGetCached_ReadErrorNamesPath pattern).
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial trace"))
		hj, ok := w.(http.Hijacker)
		if !assert.True(t, ok) {
			return
		}
		conn, _, err := hj.Hijack()
		if !assert.NoError(t, err) {
			return
		}
		conn.Close()
	})
	logs, err := client.GetWorkflowRunLogs(context.Background(), "o", "r", 30)
	require.NoError(t, err)
	assert.Contains(t, logs, "error reading trace:")
}

// TestGetWorkflowRunLogs_SkippedJobsAfterAggregateLimit verifies that a
// skipped job's header is written even after earlier traces reach the
// aggregate byte cap: the cap limits fetched bytes, not the job listing, so
// a snapshot whose remaining jobs are all skipped stays complete (no
// aggregate-limit note marks it truncated) and the skipped jobs' traces are
// still never requested.
func TestGetWorkflowRunLogs_SkippedJobsAfterAggregateLimit(t *testing.T) {
	client, mux := setupTest(t)
	// Ten traces of exactly the per-job cap (10 MiB each) bring totalRead
	// to the 100 MiB aggregate limit; every job after them is skipped.
	jobs := make([]string, 0, 11)
	for i := 1; i <= 10; i++ {
		jobs = append(jobs, fmt.Sprintf(`{"id":%d,"name":"job%d","status":"success"}`, i, i))
	}
	jobs = append(jobs, `{"id":11,"name":"deploy","status":"skipped"}`)
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/30/jobs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%s]", strings.Join(jobs, ","))
	})
	trace := strings.Repeat("a", 10<<20)
	for i := 1; i <= 10; i++ {
		mux.HandleFunc(fmt.Sprintf("/api/v4/projects/o%%2Fr/jobs/%d/trace", i), func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(trace))
		})
	}
	skippedTraceRequests := 0
	mux.HandleFunc("/api/v4/projects/o%2Fr/jobs/11/trace", func(w http.ResponseWriter, r *http.Request) {
		skippedTraceRequests++
		http.NotFound(w, r)
	})
	logs, err := client.GetWorkflowRunLogs(context.Background(), "o", "r", 30)
	require.NoError(t, err)
	assert.Contains(t, logs, "Job 11 (deploy) [skipped]")
	// The literal is the shared forge log-note constant's text
	// (forge.LogNoteAggregateTraceLimit); the note spelling itself is
	// pinned by TestLogNoteMarkersAreSharedWithForge in
	// pkg/behaviourtest/steps.
	assert.NotContains(t, logs, "aggregate trace limit")
	assert.Zero(t, skippedTraceRequests, "a skipped job's trace is never requested")
}
