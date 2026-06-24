package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestGetIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/o/r/issues/3", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   3,
			"title":    "Test",
			"body":     "body",
			"html_url": "https://github.com/o/r/issues/3",
			"labels":   []map[string]any{{"name": "bug"}},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issue, err := client.GetIssue(context.Background(), "o", "r", 3)
	require.NoError(t, err)
	assert.Equal(t, 3, issue.Number)
	assert.Equal(t, []string{"bug"}, issue.Labels)
}

func TestAddIssueLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/o/r/issues/2/labels", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, []any{"workflow-change-needed"}, body["labels"])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	require.NoError(t, client.AddIssueLabels(context.Background(), "o", "r", 2, "workflow-change-needed"))
}

func TestAddIssueLabels_EmptyNoOp(t *testing.T) {
	client := New("token")
	require.NoError(t, client.AddIssueLabels(context.Background(), "o", "r", 1))
}

func TestRemoveIssueLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/repos/o/r/issues/5/labels/workflow-change-needed", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	require.NoError(t, client.RemoveIssueLabel(context.Background(), "o", "r", 5, "workflow-change-needed"))
}

func TestGetLabelAppliedAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/repos/o/r/issues/7/timeline")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"event":      "labeled",
				"created_at": "2026-06-24T10:00:00Z",
				"label":      map[string]string{"name": "workflow-change-allowed"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	ts, err := client.GetLabelAppliedAt(context.Background(), "o", "r", 7, "workflow-change-allowed")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC), ts.UTC())
}

func TestGetLabelAppliedAt_LatestAcrossPages(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		switch page {
		case 1:
			events := make([]map[string]any, 100)
			for i := range events {
				events[i] = map[string]any{"event": "commented", "created_at": "2026-01-01T00:00:00Z"}
			}
			events[0] = map[string]any{
				"event":      "labeled",
				"created_at": "2026-06-24T10:00:00Z",
				"label":      map[string]string{"name": "workflow-change-allowed"},
			}
			json.NewEncoder(w).Encode(events)
		case 2:
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"event":      "labeled",
					"created_at": "2026-06-25T12:00:00Z",
					"label":      map[string]string{"name": "workflow-change-allowed"},
				},
			})
		default:
			t.Fatalf("unexpected page %d", page)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	ts, err := client.GetLabelAppliedAt(context.Background(), "o", "r", 7, "workflow-change-allowed")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC), ts.UTC())
}

func TestGetLabelAppliedAt_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetLabelAppliedAt(context.Background(), "o", "r", 1, "missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestGetCommentAuthorAssociation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/o/r/issues/comments/99", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{"author_association": "NONE"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	assoc, err := client.GetCommentAuthorAssociation(context.Background(), "o", "r", 1, 99)
	require.NoError(t, err)
	assert.Equal(t, "NONE", assoc)
}

func TestGetCommentAuthorAssociation_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"author_association": ""})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetCommentAuthorAssociation(context.Background(), "o", "r", 1, 99)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty author_association")
}
