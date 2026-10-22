package gitlab

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestListUserSSHKeys(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/users/77/keys", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "1", r.URL.Query().Get("page"))
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 1, "usage_type": "auth", "expires_at": "2999-01-01T00:00:00Z"},
			{"id": 2, "usage_type": "signing"},
		})
	})

	got, err := client.ListUserSSHKeys(context.Background(), 77)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	require.Len(t, got, 2)
	assert.Equal(t, 1, got[0].ID)
	assert.Equal(t, "auth", got[0].UsageType)
	assert.Equal(t, "2999-01-01T00:00:00Z", got[0].ExpiresAt)
	assert.Equal(t, "signing", got[1].UsageType)
}

func TestListUserSSHKeys_Paginates(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/users/77/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			full := make([]map[string]any, serviceAccountPerPage)
			for i := range full {
				full[i] = map[string]any{"id": i + 1}
			}
			writeJSON(t, w, http.StatusOK, full)
			return
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{{"id": 1000}})
	})

	got, err := client.ListUserSSHKeys(context.Background(), 77)
	require.NoError(t, err)
	require.Len(t, got, serviceAccountPerPage+1)
	assert.Equal(t, 1000, got[len(got)-1].ID)
}

func TestListUserSSHKeys_Error(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/users/77/keys", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	_, err := client.ListUserSSHKeys(context.Background(), 77)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.True(t, handlerCalled, "the registered 403 handler must have served the request")
}
