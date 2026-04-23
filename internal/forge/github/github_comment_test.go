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

func TestMinimizeComment(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET comment to retrieve node_id
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/issues/comments/789", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"id":      789,
				"node_id": "IC_kwDOTest",
			})
		case 2:
			// POST GraphQL mutation
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
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.MinimizeComment(context.Background(), "owner", "repo", 789, "OUTDATED")
	require.NoError(t, err)
	assert.Equal(t, 2, callNum)
}
