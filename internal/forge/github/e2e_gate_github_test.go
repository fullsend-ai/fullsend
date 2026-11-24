package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListIssueLabelEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/o/r/issues/5/events", r.URL.Path)
		json.NewEncoder(w).Encode([]map[string]any{
			{"event": "labeled", "created_at": "2026-01-02T00:00:00Z", "label": map[string]string{"name": "ok-to-test"}, "actor": map[string]string{"login": "alice"}},
			{"event": "commented", "created_at": "2026-01-01T00:00:00Z"},
		})
	}))
	defer srv.Close()

	events, err := newTestClient(t, srv).ListIssueLabelEvents(context.Background(), "o", "r", 5)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "ok-to-test", events[0].LabelName)
	assert.Equal(t, "alice", events[0].ActorLogin)
}

func TestGetCollaboratorPermissionDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"role_name": "write", "permission": "read"})
	}))
	defer srv.Close()

	detail, err := newTestClient(t, srv).GetCollaboratorPermissionDetail(context.Background(), "o", "r", "alice")
	require.NoError(t, err)
	assert.Equal(t, "write", detail.RoleName)
	assert.Equal(t, "read", detail.Permission)
}

func TestGetCollaboratorPermission_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GetCollaboratorPermission(context.Background(), "o", "r", "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get collaborator permission for alice")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode)
}

func TestGetCollaboratorPermissionDetail_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GetCollaboratorPermissionDetail(context.Background(), "o", "r", "alice")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
	assert.Contains(t, err.Error(), "collaborator alice")
}

func TestGetCollaboratorPermissionDetail_MissingPermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"role_name": "write"})
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GetCollaboratorPermissionDetail(context.Background(), "o", "r", "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing permission field")
}

func TestListIssueLabelEventsPaginates(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		assert.Equal(t, fmt.Sprintf("%d", page), r.URL.Query().Get("page"))
		if page == 1 {
			events := make([]map[string]any, 100)
			for i := range events {
				events[i] = map[string]any{
					"event":      "commented",
					"created_at": "2026-01-01T00:00:00Z",
				}
			}
			json.NewEncoder(w).Encode(events)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"event": "labeled", "created_at": "2026-01-02T00:00:00Z", "label": map[string]string{"name": "ok-to-test"}, "actor": map[string]string{"login": "alice"}},
		})
	}))
	defer srv.Close()

	events, err := newTestClient(t, srv).ListIssueLabelEvents(context.Background(), "o", "r", 5)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "alice", events[0].ActorLogin)
	assert.Equal(t, 2, page)
}

func TestRemoveIssueLabel(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := newTestClient(t, srv).RemoveIssueLabel(context.Background(), "o", "r", 5, "ok-to-test")
	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/repos/o/r/issues/5/labels/ok-to-test", path)
}

func TestRemoveIssueLabel_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := newTestClient(t, srv).RemoveIssueLabel(context.Background(), "o", "r", 5, "ok-to-test")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestGetPullRequestAuthorizationContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/o/r/pulls/5", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"updated_at": "2026-02-01T12:00:00Z",
			"user":       map[string]string{"login": "author"},
			"labels":     []map[string]string{{"name": "ok-to-test"}, {"name": "bug"}},
		})
	}))
	defer srv.Close()

	ctx, err := newTestClient(t, srv).GetPullRequestAuthorizationContext(context.Background(), "o", "r", 5)
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, "author", ctx.AuthorLogin)
	assert.Equal(t, "2026-02-01T12:00:00Z", ctx.UpdatedAt)
	assert.Equal(t, []string{"ok-to-test", "bug"}, ctx.Labels)
}

func TestGetPullRequestAuthorizationContext_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GetPullRequestAuthorizationContext(context.Background(), "o", "r", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get pull request #5 for authorization")
}
