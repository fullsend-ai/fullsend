package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastMergeRetryPoll overrides the merge retry poll vars to near-zero for tests.
// It returns a cleanup function that restores the original values.
func fastMergeRetryPoll(t *testing.T) {
	t.Helper()
	origInterval := mergeRetryPollInterval
	origTimeout := mergeRetryPollTimeout
	mergeRetryPollInterval = time.Millisecond
	mergeRetryPollTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		mergeRetryPollInterval = origInterval
		mergeRetryPollTimeout = origTimeout
	})
}

func TestMergeChangeProposal_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/repos/org/repo/pulls/42/merge", r.URL.Path)

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "squash", body["merge_method"])

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"sha": "abc123"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MergeChangeProposal(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
}

func TestMergeChangeProposal_409UpdatesBranchAndRetries(t *testing.T) {
	fastMergeRetryPoll(t)

	var mergeAttempts atomic.Int32
	var updateCalls atomic.Int32
	var headSHA atomic.Value
	headSHA.Store("old-sha")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/merge":
			attempt := mergeAttempts.Add(1)
			if attempt == 1 {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]string{
					"message": "Head branch is out of date",
				})
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"sha": "def456"})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/pulls/7":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"head": map[string]string{"sha": headSHA.Load().(string)},
			})

		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/update-branch":
			updateCalls.Add(1)
			headSHA.Store("new-sha")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MergeChangeProposal(context.Background(), "org", "repo", 7)
	require.NoError(t, err)
	assert.Equal(t, int32(2), mergeAttempts.Load(), "should have attempted merge twice")
	assert.Equal(t, int32(1), updateCalls.Load(), "should have called update-branch once")
}

func TestMergeChangeProposal_NonConflictErrorNotRetried(t *testing.T) {
	var mergeAttempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mergeAttempts.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Pull Request is not mergeable",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MergeChangeProposal(context.Background(), "org", "repo", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not mergeable")
	assert.Equal(t, int32(1), mergeAttempts.Load(), "should not retry non-409 errors")
}

func TestMergeChangeProposal_409PersistsAfterRetries(t *testing.T) {
	fastMergeRetryPoll(t)

	var mergeAttempts atomic.Int32
	var headSHACounter atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/merge":
			mergeAttempts.Add(1)
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "Head branch is out of date",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/pulls/7":
			// Return a new SHA each time so the poll completes.
			n := headSHACounter.Add(1)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"head": map[string]string{"sha": fmt.Sprintf("sha-%d", n)},
			})

		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/update-branch":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MergeChangeProposal(context.Background(), "org", "repo", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "merge pull request #7")
	assert.Equal(t, int32(3), mergeAttempts.Load(), "should have retried merge exactly 3 times")
}

func TestMergeChangeProposal_UpdateBranchFailsMidRetry(t *testing.T) {
	fastMergeRetryPoll(t)

	var mergeAttempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/merge":
			mergeAttempts.Add(1)
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "Head branch is out of date",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/pulls/7":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"head": map[string]string{"sha": "old-sha"},
			})

		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/update-branch":
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"message": "Resource not accessible by integration"})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MergeChangeProposal(context.Background(), "org", "repo", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update branch failed")
	assert.Equal(t, int32(1), mergeAttempts.Load(), "should not retry merge after update-branch failure")
}

func TestMergeChangeProposal_ContextCancelledDuringPoll(t *testing.T) {
	fastMergeRetryPoll(t)
	// Set a long poll timeout so cancellation fires first.
	origTimeout := mergeRetryPollTimeout
	mergeRetryPollTimeout = 10 * time.Second
	t.Cleanup(func() { mergeRetryPollTimeout = origTimeout })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/merge":
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "Head branch is out of date",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/pulls/7":
			// Always return the same SHA so the poll never completes.
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"head": map[string]string{"sha": "stuck-sha"},
			})

		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo/pulls/7/update-branch":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	client := newTestClient(t, srv)
	err := client.MergeChangeProposal(ctx, "org", "repo", 7)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
