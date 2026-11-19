package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests below cover the reads the fork PR review poll (ADR 0171) adds
// or extends.

func TestListRepoPullRequests_ForkPollFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"number":     4,
			"updated_at": "2026-11-17T19:00:00Z",
			"user":       map[string]any{"login": "alice", "type": "User"},
			"head": map[string]any{
				"ref": "f", "sha": "headsha",
				"repo": map[string]any{"full_name": "alice/repo", "owner": map[string]any{"login": "alice"}},
			},
			"base": map[string]any{"ref": "main", "sha": "basesha", "repo": map[string]any{"full_name": "owner/repo"}},
		}})
	}))
	defer srv.Close()

	prs, err := newTestClient(t, srv).ListRepoPullRequests(context.Background(), "owner", "repo")
	require.NoError(t, err)
	require.Len(t, prs, 1)
	pr := prs[0]
	assert.Equal(t, "headsha", pr.HeadSHA)
	assert.Equal(t, "alice", pr.HeadOwner)
	assert.Equal(t, "owner/repo", pr.BaseRepo)
	assert.Equal(t, "basesha", pr.BaseSHA)
	assert.Equal(t, "User", pr.AuthorType)
	assert.Equal(t, "2026-11-17T19:00:00Z", pr.UpdatedAt)
}

func TestListIssueComments_AppClientID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "body": "app", "user": map[string]any{"login": "x-review[bot]"}, "performed_via_github_app": map[string]any{"client_id": "Iv1.abc"}},
			{"id": 2, "body": "human", "user": map[string]any{"login": "alice"}, "performed_via_github_app": nil},
		})
	}))
	defer srv.Close()

	comments, err := newTestClient(t, srv).ListIssueComments(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, "Iv1.abc", comments[0].AppClientID)
	assert.Empty(t, comments[1].AppClientID)
}

func TestListPullRequestReviews_CommitID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "user": map[string]any{"login": "x-review[bot]"}, "state": "APPROVED", "submitted_at": "2026-11-17T19:00:00Z", "commit_id": "abc123"},
		})
	}))
	defer srv.Close()

	reviews, err := newTestClient(t, srv).ListPullRequestReviews(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, "abc123", reviews[0].CommitID)
}

func TestListWorkflowRunsByStatus(t *testing.T) {
	since := time.Date(2026, 11, 17, 18, 0, 0, 0, time.FixedZone("x", 3600))
	var pages []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/owner/repo/actions/workflows/fullsend-poll-prs.yml/runs", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "in_progress", q.Get("status"))
		assert.Equal(t, "100", q.Get("per_page"))
		assert.Equal(t, ">=2026-11-17T17:00:00Z", q.Get("created"), "created is rendered in UTC")
		page, _ := strconv.Atoi(q.Get("page"))
		pages = append(pages, page)
		n := 100
		if page == 2 {
			n = 1
		}
		runs := make([]map[string]any, n)
		for i := range runs {
			runs[i] = map[string]any{"id": page*1000 + i, "status": "in_progress", "created_at": "2026-11-17T19:00:00Z"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
	}))
	defer srv.Close()

	runs, err := newTestClient(t, srv).ListWorkflowRunsByStatus(context.Background(), "owner", "repo", "fullsend-poll-prs.yml", "in_progress", since)
	require.NoError(t, err)
	assert.Len(t, runs, 101)
	assert.Equal(t, []int{1, 2}, pages)
	assert.Equal(t, "in_progress", runs[0].Status)
	assert.Equal(t, 2000, runs[100].ID)
}

func TestListWorkflowRunsByStatus_NoCreatedFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasCreated := r.URL.Query()["created"]
		assert.False(t, hasCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []any{}})
	}))
	defer srv.Close()

	runs, err := newTestClient(t, srv).ListWorkflowRunsByStatus(context.Background(), "owner", "repo", "w.yml", "failure", time.Time{})
	require.NoError(t, err)
	assert.Empty(t, runs)
}

func TestListWorkflowRunsByStatus_Errors(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"bad"}`, http.StatusUnprocessableEntity)
		}))
		defer srv.Close()
		_, err := newTestClient(t, srv).ListWorkflowRunsByStatus(context.Background(), "owner", "repo", "w.yml", "failure", time.Time{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list failure workflow runs page 1")
	})
	t.Run("decode", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "{not json")
		}))
		defer srv.Close()
		_, err := newTestClient(t, srv).ListWorkflowRunsByStatus(context.Background(), "owner", "repo", "w.yml", "failure", time.Time{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode failure workflow runs page 1")
	})
}
