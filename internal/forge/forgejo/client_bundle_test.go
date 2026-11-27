package forgejo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// recorded is one request the bundle test server received.
type recorded struct {
	Method, Path, Query string
	Body                map[string]any
}

func bundleServer(t *testing.T, handler func(w http.ResponseWriter, r recorded)) (*LiveClient, *[]recorded) {
	t.Helper()
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery}
		_ = json.NewDecoder(r.Body).Decode(&rec.Body)
		reqs = append(reqs, rec)
		handler(w, rec)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/sub", "tok", nil)
	require.NoError(t, err)
	return c, &reqs
}

func TestClient_GetFileAt(t *testing.T) {
	c, reqs := bundleServer(t, func(w http.ResponseWriter, r recorded) {
		if r.Query != "ref=abc%2Fdef" {
			http.NotFound(w, nil)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"type": "file", "encoding": "base64", "sha": "s1",
			"content": base64.StdEncoding.EncodeToString([]byte("hi")),
		})
	})
	f, err := c.GetFileAt(context.Background(), "o", "r", ".forgejo/workflows/x.yml", "abc/def")
	require.NoError(t, err)
	assert.Equal(t, "hi", string(f.Content))
	assert.Equal(t, "/sub/api/v1/repos/o/r/contents/.forgejo/workflows/x.yml", (*reqs)[0].Path)

	_, err = c.GetFileAt(context.Background(), "o", "r", "x", "other")
	assert.True(t, forge.IsNotFound(err))
}

func TestClient_GetFileAtRejectsNonFiles(t *testing.T) {
	for name, body := range map[string]map[string]string{
		"dir":      {"type": "dir"},
		"encoding": {"type": "file", "encoding": "utf8"},
		"content":  {"type": "file", "encoding": "base64", "content": "!!"},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := bundleServer(t, func(w http.ResponseWriter, _ recorded) { _ = json.NewEncoder(w).Encode(body) })
			_, err := c.GetFileAt(context.Background(), "o", "r", "x", "main")
			assert.Error(t, err)
		})
	}
}

func TestClient_ChangeFiles(t *testing.T) {
	c, reqs := bundleServer(t, func(w http.ResponseWriter, _ recorded) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": "c1"}})
	})
	sha, err := c.ChangeFiles(context.Background(), "o", "r", "msg", []FileChange{
		{Operation: FileCreate, Path: "a", Content: []byte("A")},
		{Operation: FileUpdate, Path: "b", Content: []byte("B"), SHA: "sb"},
		{Operation: FileDelete, Path: "c", SHA: "sc"},
	})
	require.NoError(t, err)
	assert.Equal(t, "c1", sha)
	req := (*reqs)[0]
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/sub/api/v1/repos/o/r/contents", req.Path)
	assert.Equal(t, "msg", req.Body["message"])
	files := req.Body["files"].([]any)
	require.Len(t, files, 3)
	assert.Equal(t, map[string]any{"operation": "create", "path": "a", "content": base64.StdEncoding.EncodeToString([]byte("A"))}, files[0])
	assert.Equal(t, "sb", files[1].(map[string]any)["sha"])
	assert.Equal(t, map[string]any{"operation": "delete", "path": "c", "sha": "sc"}, files[2])

	_, err = c.ChangeFiles(context.Background(), "o", "r", "msg", nil)
	assert.ErrorContains(t, err, "no file changes")
	_, err = c.ChangeFiles(context.Background(), "o", "r", "msg", []FileChange{{Operation: "move", Path: "x"}})
	assert.ErrorContains(t, err, "unsupported file operation")
}

func TestClient_ChangeFilesErrors(t *testing.T) {
	c, _ := bundleServer(t, func(w http.ResponseWriter, _ recorded) { _ = json.NewEncoder(w).Encode(map[string]any{}) })
	_, err := c.ChangeFiles(context.Background(), "o", "r", "m", []FileChange{{Operation: FileCreate, Path: "a"}})
	assert.ErrorContains(t, err, "no commit SHA")

	c, _ = bundleServer(t, func(w http.ResponseWriter, _ recorded) { w.WriteHeader(http.StatusConflict) })
	_, err = c.ChangeFiles(context.Background(), "o", "r", "m", []FileChange{{Operation: FileCreate, Path: "a"}})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.StatusCode)
}

func TestClient_Tags(t *testing.T) {
	c, reqs := bundleServer(t, func(w http.ResponseWriter, r recorded) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/sub/api/v1/repos/o/r/tags/fullsend-bundle-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": "c1"}})
		case r.Method == http.MethodPost && r.Path == "/sub/api/v1/repos/o/r/tags":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": "c2"}})
		case r.Path == "/sub/api/v1/repos/o/r/tags/empty":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, nil)
		}
	})
	ctx := context.Background()
	sha, err := c.GetTag(ctx, "o", "r", "fullsend-bundle-1")
	require.NoError(t, err)
	assert.Equal(t, "c1", sha)

	_, err = c.GetTag(ctx, "o", "r", "missing")
	assert.True(t, forge.IsNotFound(err))
	_, err = c.GetTag(ctx, "o", "r", "empty")
	assert.ErrorContains(t, err, "no commit SHA")

	sha, err = c.CreateTag(ctx, "o", "r", "fullsend-bundle-2", "c2", "bundle 2")
	require.NoError(t, err)
	assert.Equal(t, "c2", sha)
	last := (*reqs)[len(*reqs)-1]
	assert.Equal(t, map[string]any{"tag_name": "fullsend-bundle-2", "target": "c2", "message": "bundle 2"}, last.Body)
}

func TestClient_CreateTagErrors(t *testing.T) {
	c, _ := bundleServer(t, func(w http.ResponseWriter, _ recorded) { _ = json.NewEncoder(w).Encode(map[string]any{}) })
	_, err := c.CreateTag(context.Background(), "o", "r", "t", "", "m")
	assert.ErrorContains(t, err, "no commit SHA")

	c, _ = bundleServer(t, func(w http.ResponseWriter, _ recorded) { w.WriteHeader(http.StatusConflict) })
	_, err = c.CreateTag(context.Background(), "o", "r", "t", "", "m")
	assert.Error(t, err)
}
