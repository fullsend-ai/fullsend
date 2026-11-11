package gitlab

import (
	"context"
	"fmt"
	"net/http"
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
