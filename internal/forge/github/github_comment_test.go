package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCreateIssueWithLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "Follow-up", body["title"])
		assert.Equal(t, "Body", body["body"])
		assert.Equal(t, []any{"type/chore"}, body["labels"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   77,
			"title":    "Follow-up",
			"body":     "Body",
			"html_url": "https://github.com/owner/repo/issues/77",
			"labels":   []map[string]any{{"name": "type/chore"}},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issue, err := client.CreateIssue(context.Background(), "owner", "repo", "Follow-up", "Body", "type/chore")
	require.NoError(t, err)
	assert.Equal(t, 77, issue.Number)
	assert.Equal(t, "Follow-up", issue.Title)
	assert.Equal(t, "Body", issue.Body)
	assert.Equal(t, []string{"type/chore"}, issue.Labels)
}

func TestCreateIssueRetriesWithoutLabelsOnValidationError(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if call == 1 {
			assert.Equal(t, []any{"missing-label"}, body["labels"])
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "Validation Failed",
				"errors":  []map[string]any{{"field": "labels", "code": "invalid"}},
			})
			return
		}

		_, hasLabels := body["labels"]
		assert.False(t, hasLabels)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   78,
			"title":    "Follow-up",
			"body":     "Body",
			"html_url": "https://github.com/owner/repo/issues/78",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issue, err := client.CreateIssue(context.Background(), "owner", "repo", "Follow-up", "Body", "missing-label")
	require.NoError(t, err)
	assert.Equal(t, 78, issue.Number)
	assert.Equal(t, 2, call)
}

func TestCreateIssueDoesNotRetryNonLabelValidationErrors(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Validation Failed",
			"errors":  []map[string]any{{"field": "title", "code": "missing"}},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CreateIssue(context.Background(), "owner", "repo", "Follow-up", "Body", "type/chore")
	require.Error(t, err)
	assert.Equal(t, 1, call)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode)
	require.Len(t, apiErr.Errors, 1)
	assert.Equal(t, "title", apiErr.Errors[0].Field)
}

func TestCreateIssueReturnsNonLabelErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible by integration"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CreateIssue(context.Background(), "owner", "repo", "Follow-up", "Body", "type/chore")
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
}

func TestListOpenIssuesSkipsPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues", r.URL.Path)
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		assert.Equal(t, "1", r.URL.Query().Get("page"))

		json.NewEncoder(w).Encode([]map[string]any{
			{
				"number":   1,
				"title":    "Issue",
				"body":     "Issue body",
				"html_url": "https://github.com/owner/repo/issues/1",
				"labels":   []map[string]any{{"name": "type/chore"}},
			},
			{
				"number":       2,
				"title":        "PR",
				"body":         "PR body",
				"html_url":     "https://github.com/owner/repo/pull/2",
				"pull_request": map[string]any{"url": "https://api.github.com/repos/owner/repo/pulls/2"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issues, err := client.ListOpenIssues(context.Background(), "owner", "repo")
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, 1, issues[0].Number)
	assert.Equal(t, "Issue body", issues[0].Body)
	assert.Equal(t, []string{"type/chore"}, issues[0].Labels)
}

func TestListOpenIssuesFiltersByLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues", r.URL.Path)
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		assert.Equal(t, "type/chore", r.URL.Query().Get("labels"))

		json.NewEncoder(w).Encode([]map[string]any{
			{
				"number":   1,
				"title":    "Issue",
				"body":     "Issue body",
				"html_url": "https://github.com/owner/repo/issues/1",
				"labels":   []map[string]any{{"name": "type/chore"}},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issues, err := client.ListOpenIssues(context.Background(), "owner", "repo", "type/chore")
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, []string{"type/chore"}, issues[0].Labels)
}

func TestListOpenIssuesPaginatesUntilShortPage(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/issues", r.URL.Path)
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		assert.Equal(t, strconv.Itoa(call), r.URL.Query().Get("page"))

		items := make([]map[string]any, 0, 100)
		limit := 100
		if call == 2 {
			limit = 1
		}
		for i := 0; i < limit; i++ {
			number := ((call - 1) * 100) + i + 1
			items = append(items, map[string]any{
				"number":   number,
				"title":    "Issue",
				"body":     "Issue body",
				"html_url": "https://github.com/owner/repo/issues/1",
			})
		}
		json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issues, err := client.ListOpenIssues(context.Background(), "owner", "repo")
	require.NoError(t, err)
	require.Len(t, issues, 101)
	assert.Equal(t, 2, call)
	assert.Equal(t, 101, issues[100].Number)
}

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
	err := client.CreatePullRequestReview(context.Background(), "owner", "repo", 7, "APPROVE", "Looks good!", "abc123", nil)
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
	err := client.CreatePullRequestReview(context.Background(), "owner", "repo", 7, "APPROVE", "Looks good!", "", nil)
	require.NoError(t, err)
}

func TestCreatePullRequestReview_WithInlineComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/owner/repo/pulls/7/reviews", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "REQUEST_CHANGES", body["event"])
		assert.Equal(t, "abc123", body["commit_id"])

		comments, ok := body["comments"].([]any)
		require.True(t, ok, "comments should be an array")
		require.Len(t, comments, 1)

		c := comments[0].(map[string]any)
		assert.Equal(t, "internal/service.go", c["path"])
		assert.Equal(t, float64(42), c["line"])
		assert.Contains(t, c["body"], "missing-test")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"id": 999})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	comments := []forge.ReviewComment{
		{Path: "internal/service.go", Line: 42, Body: "**[high]** missing-test\n\nAdd test coverage."},
	}
	err := client.CreatePullRequestReview(context.Background(), "owner", "repo", 7, "REQUEST_CHANGES", "Review", "abc123", comments)
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
