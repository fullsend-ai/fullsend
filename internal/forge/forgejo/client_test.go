package forgejo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// fakeForgejo is a minimal contents API served under a path prefix.
type fakeForgejo struct {
	mu       sync.Mutex
	prefix   string
	files    map[string]string // "owner/repo/path" -> content
	sha      int
	requests []string
	auth     []string
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.EscapedPath())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	base := f.prefix + "/api/v1/repos/"
	if !strings.HasPrefix(r.URL.Path, base) {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, base), "/", 4)
	if len(parts) != 4 || parts[2] != "contents" {
		http.NotFound(w, r)
		return
	}
	key := parts[0] + "/" + parts[1] + "/" + parts[3]
	var body map[string]string
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	content, exists := f.files[key]
	switch r.Method {
	case http.MethodGet:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"type": "file", "encoding": "base64", "sha": "sha-" + key,
			"content": base64.StdEncoding.EncodeToString([]byte(content)),
		})
	case http.MethodPost, http.MethodPut:
		if (r.Method == http.MethodPost) == exists {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"wrong method"}`))
			return
		}
		if r.Method == http.MethodPut && body["sha"] != "sha-"+key {
			w.WriteHeader(http.StatusConflict)
			return
		}
		data, _ := base64.StdEncoding.DecodeString(body["content"])
		f.files[key] = string(data)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case http.MethodDelete:
		if !exists || body["sha"] != "sha-"+key {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.files, key)
		w.WriteHeader(http.StatusOK)
	}
}

func newFake(t *testing.T, prefix string) (*fakeForgejo, *LiveClient) {
	t.Helper()
	f := &fakeForgejo{prefix: prefix, files: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+prefix+"/", "secret-token", srv.Client())
	require.NoError(t, err)
	return f, c
}

func TestClient_FileLifecyclePreservesPathPrefix(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t, "/forgejo")

	_, err := c.GetFile(ctx, "acme", "app", ".fullsend/config.yaml")
	require.True(t, forge.IsNotFound(err), "missing file maps to forge.ErrNotFound: %v", err)

	require.NoError(t, c.CreateFile(ctx, "acme", "app", ".fullsend/config.yaml", "create", []byte("a: 1\n")))
	got, err := c.GetFile(ctx, "acme", "app", ".fullsend/config.yaml")
	require.NoError(t, err)
	assert.Equal(t, "a: 1\n", string(got.Content))

	require.NoError(t, c.UpdateFile(ctx, "acme", "app", ".fullsend/config.yaml", "update", []byte("a: 2\n"), got.SHA))
	assert.Equal(t, "a: 2\n", f.files["acme/app/.fullsend/config.yaml"])

	// A stale SHA is rejected rather than overwriting or deleting.
	assert.Error(t, c.UpdateFile(ctx, "acme", "app", ".fullsend/config.yaml", "update", []byte("a: 3\n"), "stale"))
	assert.Error(t, c.DeleteFile(ctx, "acme", "app", ".fullsend/config.yaml", "delete", "stale"))
	assert.Equal(t, "a: 2\n", f.files["acme/app/.fullsend/config.yaml"])

	require.NoError(t, c.DeleteFile(ctx, "acme", "app", ".fullsend/config.yaml", "delete", got.SHA))
	assert.Empty(t, f.files)
	err = c.DeleteFile(ctx, "acme", "app", ".fullsend/config.yaml", "delete", got.SHA)
	assert.True(t, forge.IsNotFound(err))

	for _, r := range f.requests {
		assert.True(t, strings.Contains(r, " /forgejo/api/v1/repos/acme/app/contents/.fullsend/config.yaml"), r)
	}
	for _, a := range f.auth {
		assert.Equal(t, "token secret-token", a)
	}
}

func TestClient_EscapesPathSegments(t *testing.T) {
	f, c := newFake(t, "")
	_, err := c.GetFile(context.Background(), "ac?me", "a#pp", "dir/fi le")
	require.True(t, forge.IsNotFound(err))
	require.Len(t, f.requests, 1)
	assert.Equal(t, "GET /api/v1/repos/ac%3Fme/a%23pp/contents/dir/fi%20le", f.requests[0])
}

func TestClient_APIErrorOmitsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"token lacks write:repository"}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "secret-token", srv.Client())
	require.NoError(t, err)
	_, err = c.GetFile(context.Background(), "acme", "app", "x")
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Contains(t, err.Error(), "token lacks write:repository")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestClient_APIErrorSanitizesServerMessage(t *testing.T) {
	tests := []struct {
		name, body string
		want       string
	}{
		{"newline workflow command", `{"message":"boom\n::add-mask::secret"}`, "boom?: :add-mask: :secret"},
		{"colon runs", `{"message":"a:::b::::c"}`, "a: : :b: : : :c"},
		{"ansi escape", `{"message":"red\u001b[31mtext\u001b]0;title\u0007"}`, "red?[31mtext?]0;title?"},
		{"carriage return", `{"message":"a\r::error::b"}`, "a?: :error: :b"},
		{"truncated multibyte", `{"message":"` + strings.Repeat("é", 200) + `"}`, strings.Repeat("é", 150) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c, err := New(srv.URL, "t", srv.Client())
			require.NoError(t, err)
			_, err = c.GetFile(context.Background(), "acme", "app", "x")
			var apiErr *APIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, tt.want, apiErr.Message)
			assert.NotContains(t, err.Error(), "\n")
			assert.NotContains(t, err.Error(), "\x1b")
			assert.NotContains(t, err.Error(), "::")
		})
	}
}

func TestClient_RejectsNonFileAndBadEncoding(t *testing.T) {
	resp := `{"type":"dir"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "", srv.Client())
	require.NoError(t, err)
	_, err = c.GetFile(context.Background(), "acme", "app", "x")
	assert.ErrorContains(t, err, `is a "dir", not a file`)

	resp = `{"type":"file","encoding":"none","content":"x"}`
	_, err = c.GetFile(context.Background(), "acme", "app", "x")
	assert.ErrorContains(t, err, "unsupported content encoding")

	resp = `{"type":"file","encoding":"base64","content":"!!"}`
	_, err = c.GetFile(context.Background(), "acme", "app", "x")
	assert.ErrorContains(t, err, "decoding content")

	resp = `not json`
	_, err = c.GetFile(context.Background(), "acme", "app", "x")
	assert.ErrorContains(t, err, "decoding response")
}

func TestNew_ValidatesBaseURL(t *testing.T) {
	_, err := New("ftp://example.com", "", nil)
	assert.ErrorContains(t, err, "must use https or http")
	_, err = New("https://", "", nil)
	assert.ErrorContains(t, err, "no host")
	_, err = New("://bad", "", nil)
	assert.ErrorContains(t, err, "parsing forgejo base URL")
	c, err := New("https://example.com/sub/?q=1#f", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/sub/api/v1/repos/o/r/contents/p", c.contentsURL("o", "r", "p"))
}

func TestClient_TransportError(t *testing.T) {
	c, err := New("http://127.0.0.1:1", "secret-token", nil)
	require.NoError(t, err)
	err = c.CreateFile(context.Background(), "o", "r", "p", "m", []byte("x"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestClient_RefusesRedirects(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		assert.Empty(t, r.Header.Get("Authorization"), "the token must never reach a redirect target")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/v1/repos/o/r/contents/p", http.StatusFound)
	}))
	defer origin.Close()

	// Supplied clients (including one that would allow redirects) and the
	// default client both refuse: here an https-to-http redirect.
	supplied := origin.Client()
	supplied.CheckRedirect = nil
	c, err := New(origin.URL, "secret-token", supplied)
	require.NoError(t, err)
	_, err = c.GetFile(context.Background(), "o", "r", "p")
	require.Error(t, err)
	assert.ErrorContains(t, err, "refusing to follow redirect")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.Zero(t, targetHits)

	// Cross-origin redirect on the same scheme.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusTemporaryRedirect)
	}))
	defer other.Close()
	c, err = New(other.URL, "secret-token", nil)
	require.NoError(t, err)
	err = c.CreateFile(context.Background(), "o", "r", "p", "m", []byte("x"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "refusing to follow redirect")
	assert.Zero(t, targetHits)
}

func TestAPIError_IsTransient(t *testing.T) {
	for code, want := range map[int]bool{
		http.StatusTooManyRequests:     true,
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          true,
		http.StatusGatewayTimeout:      true,
		http.StatusBadRequest:          false,
		http.StatusForbidden:           false,
		http.StatusConflict:            false,
		505:                            false,
	} {
		assert.Equal(t, want, forge.IsTransient(&APIError{StatusCode: code}), "status %d", code)
	}
}
