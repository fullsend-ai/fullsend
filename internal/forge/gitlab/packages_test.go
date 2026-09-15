package gitlab

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadPackageFile(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages/generic/fullsend-poll-state/1.0/state.json", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "test-token", r.Header.Get("PRIVATE-TOKEN"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"last_poll_at_full":"2026-01-01T00:00:00Z"}`))
	})

	data, err := client.DownloadPackageFile(ctx, "myorg", "myrepo", "fullsend-poll-state", "1.0", "state.json")
	require.NoError(t, err)
	assert.Equal(t, `{"last_poll_at_full":"2026-01-01T00:00:00Z"}`, string(data))
	assert.True(t, handlerCalled, "handler was not called — URL path mismatch")
}

func TestDownloadPackageFile_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages/generic/fullsend-poll-state/1.0/state.json", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	})

	_, err := client.DownloadPackageFile(ctx, "myorg", "myrepo", "fullsend-poll-state", "1.0", "state.json")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.True(t, handlerCalled, "handler was not called — URL path mismatch")
}

func TestUploadPackageFile(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	payload := []byte(`{"last_poll_at_fast":"2026-01-02T00:00:00Z"}`)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages/generic/fullsend-poll-state/1.0/state.json", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, body)
		w.WriteHeader(http.StatusCreated)
	})

	err := client.UploadPackageFile(ctx, "myorg", "myrepo", "fullsend-poll-state", "1.0", "state.json", payload)
	require.NoError(t, err)
	assert.True(t, handlerCalled, "handler was not called — URL path mismatch")
}

func TestUploadPackageFile_RepeatedPUTSamePath(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	var received [][]byte
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages/generic/fullsend-poll-state/1.0/state.json", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		cp := make([]byte, len(body))
		copy(cp, body)
		received = append(received, cp)
		w.WriteHeader(http.StatusCreated)
	})

	first := []byte(`{"last_poll_at_full":"2026-01-01T00:00:00Z"}`)
	second := []byte(`{"last_poll_at_full":"2026-01-01T00:05:00Z"}`)

	require.NoError(t, client.UploadPackageFile(ctx, "myorg", "myrepo", "fullsend-poll-state", "1.0", "state.json", first))
	require.NoError(t, client.UploadPackageFile(ctx, "myorg", "myrepo", "fullsend-poll-state", "1.0", "state.json", second))

	require.Len(t, received, 2, "expected two PUTs to the same package file path")
	assert.Equal(t, first, received[0])
	assert.Equal(t, second, received[1])
}

func TestUploadPackageFile_Error(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages/generic/fullsend-poll-state/1.0/state.json", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	err := client.UploadPackageFile(ctx, "myorg", "myrepo", "fullsend-poll-state", "1.0", "state.json", []byte("{}"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload package file")
	assert.True(t, handlerCalled, "handler was not called — URL path mismatch")
}

func TestDeletePackage(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	listCalled, deleteCalled := false, false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages", func(w http.ResponseWriter, r *http.Request) {
		listCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "fullsend-poll-state", r.URL.Query().Get("package_name"))
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 42, "name": "fullsend-poll-state"},
		})
	})
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages/42", func(w http.ResponseWriter, r *http.Request) {
		deleteCalled = true
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeletePackage(ctx, "myorg", "myrepo", "fullsend-poll-state")
	require.NoError(t, err)
	assert.True(t, listCalled, "list packages was not called")
	assert.True(t, deleteCalled, "delete package was not called")
}

func TestDeletePackage_NoMatchingPackageIsNoop(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	deleteCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages/42", func(w http.ResponseWriter, r *http.Request) {
		deleteCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeletePackage(ctx, "myorg", "myrepo", "fullsend-poll-state")
	require.NoError(t, err)
	assert.False(t, deleteCalled, "delete should not be called when no package matches")
}

func TestDeletePackage_ListError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/packages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	err := client.DeletePackage(ctx, "myorg", "myrepo", "fullsend-poll-state")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list packages")
}
