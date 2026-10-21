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

func TestGetCollaboratorPermissionDetail_MissingPermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"role_name": "write"})
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GetCollaboratorPermissionDetail(context.Background(), "o", "r", "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing permission field")
}
