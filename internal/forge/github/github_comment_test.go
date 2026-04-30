package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateIssueComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues/42/comments", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "Great work!", body["body"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id":         123,
			"body":       "Great work!",
			"user":       map[string]any{"login": "bot"},
			"created_at": "2026-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	comment, err := client.CreateIssueComment(context.Background(), "owner", "repo", 42, "Great work!")
	require.NoError(t, err)
	assert.Equal(t, 123, comment.ID)
	assert.Equal(t, "Great work!", comment.Body)
	assert.Equal(t, "bot", comment.Author)
}

func TestUpdateIssueComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues/comments/456", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "Updated body", body["body"])

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"id":   456,
			"body": "Updated body",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.UpdateIssueComment(context.Background(), "owner", "repo", 456, "Updated body")
	require.NoError(t, err)
}

func TestListIssueComments_SinglePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues/10/comments", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		assert.Equal(t, "1", r.URL.Query().Get("page"))

		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "body": "first", "user": map[string]any{"login": "alice"}, "created_at": "2026-01-01T00:00:00Z"},
			{"id": 2, "body": "second", "user": map[string]any{"login": "bob"}, "created_at": "2026-01-02T00:00:00Z"},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	comments, err := client.ListIssueComments(context.Background(), "owner", "repo", 10)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, 1, comments[0].ID)
	assert.Equal(t, "first", comments[0].Body)
	assert.Equal(t, "alice", comments[0].Author)
	assert.Equal(t, 2, comments[1].ID)
	assert.Equal(t, "second", comments[1].Body)
	assert.Equal(t, "bob", comments[1].Author)
}

func TestListIssueComments_Pagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		assert.Equal(t, "GET", r.Method)

		switch page {
		case 1:
			assert.Equal(t, "1", r.URL.Query().Get("page"))
			// Return exactly 100 comments to trigger pagination.
			comments := make([]map[string]any, 100)
			for i := range comments {
				comments[i] = map[string]any{
					"id":         i + 1,
					"body":       "comment",
					"user":       map[string]any{"login": "bot"},
					"created_at": "2026-01-01T00:00:00Z",
				}
			}
			json.NewEncoder(w).Encode(comments)
		case 2:
			assert.Equal(t, "2", r.URL.Query().Get("page"))
			// Return fewer than 100 — pagination stops.
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 101, "body": "last", "user": map[string]any{"login": "bot"}, "created_at": "2026-01-02T00:00:00Z"},
			})
		default:
			t.Fatalf("unexpected page %d requested", page)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	comments, err := client.ListIssueComments(context.Background(), "owner", "repo", 5)
	require.NoError(t, err)
	assert.Len(t, comments, 101)
	assert.Equal(t, 2, page, "should have fetched exactly 2 pages")
	assert.Equal(t, 101, comments[100].ID)
	assert.Equal(t, "last", comments[100].Body)
}

func TestListIssueComments_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	comments, err := client.ListIssueComments(context.Background(), "owner", "repo", 999)
	assert.Error(t, err)
	assert.Nil(t, comments)
	assert.Contains(t, err.Error(), "list issue comments page 1")
}

func TestMinimizeComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/graphql", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Contains(t, body["query"], "minimizeComment")

		vars := body["variables"].(map[string]any)
		assert.Equal(t, "IC_kwDOTest", vars["id"])
		assert.Equal(t, "OUTDATED", vars["reason"])

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"minimizeComment": map[string]any{
					"minimizedComment": map[string]any{
						"isMinimized": true,
					},
				},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MinimizeComment(context.Background(), "IC_kwDOTest", "OUTDATED")
	require.NoError(t, err)
}

func TestMinimizeComment_GraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{
				{"message": "Could not resolve to a node with the global id of 'IC_kwDOTest'"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MinimizeComment(context.Background(), "IC_kwDOTest", "OUTDATED")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minimize comment IC_kwDOTest")
	assert.Contains(t, err.Error(), "Could not resolve to a node")
}

func TestMinimizeComment_InvalidReason(t *testing.T) {
	client := New("test-token")
	err := client.MinimizeComment(context.Background(), "IC_kwDOTest", "INVALID")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reason")
}

func TestCreatePullRequestReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/owner/repo/pulls/7/reviews", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "APPROVE", body["event"])
		assert.Equal(t, "Looks good!", body["body"])
		assert.Equal(t, "abc123", body["commit_id"])

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"id": 999})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreatePullRequestReview(context.Background(), "owner", "repo", 7, "APPROVE", "Looks good!", "abc123")
	require.NoError(t, err)
}

func TestCreatePullRequestReview_NoCommitSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "APPROVE", body["event"])
		_, hasCommitID := body["commit_id"]
		assert.False(t, hasCommitID, "commit_id should not be present when empty")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"id": 999})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreatePullRequestReview(context.Background(), "owner", "repo", 7, "APPROVE", "Looks good!", "")
	require.NoError(t, err)
}

func TestListPullRequestReviews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/pulls/3/reviews", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":           10,
				"node_id":      "PRR_abc",
				"user":         map[string]any{"login": "reviewer"},
				"state":        "APPROVED",
				"body":         "LGTM",
				"submitted_at": "2026-01-01T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	reviews, err := client.ListPullRequestReviews(context.Background(), "owner", "repo", 3)
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, 10, reviews[0].ID)
	assert.Equal(t, "PRR_abc", reviews[0].NodeID)
	assert.Equal(t, "reviewer", reviews[0].User)
	assert.Equal(t, "APPROVED", reviews[0].State)
	assert.Equal(t, "LGTM", reviews[0].Body)
}
