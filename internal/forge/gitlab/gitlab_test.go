package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// setupTest creates a test server and a LiveClient pointed at it.
func setupTest(t *testing.T) (*LiveClient, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := New("test-token", WithBaseURL(srv.URL))
	require.NoError(t, err)
	return client, mux
}

// ---------- gitlab.go tests ----------

func TestNew(t *testing.T) {
	c, err := New("my-token")
	require.NoError(t, err)
	assert.Equal(t, "my-token", c.token)
	assert.Equal(t, "https://gitlab.com", c.baseURL)
	assert.NotNil(t, c.http)
}

func TestWithBaseURL(t *testing.T) {
	c, err := New("tok", WithBaseURL("https://gitlab.example.com/"))
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.example.com", c.baseURL, "trailing slash should be trimmed")
}

func TestNew_RejectsEmptyToken(t *testing.T) {
	_, err := New("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token must not be empty")
}

func TestNew_RejectsInsecureURL(t *testing.T) {
	_, err := New("tok", WithBaseURL("http://gitlab.example.com"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insecure scheme")
}

func TestNew_AllowsLocalhostHTTP(t *testing.T) {
	c, err := New("tok", WithBaseURL("http://localhost:8080"))
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080", c.baseURL)
}

func TestAPIError_Error(t *testing.T) {
	e := &APIError{StatusCode: 422, Message: "validation failed"}
	assert.Equal(t, "gitlab api: 422 validation failed", e.Error())
}

func TestAPIError_Unwrap(t *testing.T) {
	tests := []struct {
		code   int
		target error
	}{
		{http.StatusNotFound, forge.ErrNotFound},
		{http.StatusConflict, forge.ErrAlreadyExists},
		{http.StatusForbidden, forge.ErrForbidden},
		{http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.code), func(t *testing.T) {
			e := &APIError{StatusCode: tt.code, Message: "msg"}
			assert.Equal(t, tt.target, e.Unwrap())
		})
	}
}

func TestCheckStatus(t *testing.T) {
	t.Run("acceptable status returns nil", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/ok", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{}`)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		client, err := New("tok", WithBaseURL(srv.URL))
		require.NoError(t, err)
		resp, err := client.do(context.Background(), http.MethodGet, "/ok", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.NoError(t, checkStatus(resp, http.StatusOK, http.StatusCreated))
	})

	t.Run("unacceptable status returns APIError", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/bad", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"name already taken"}`)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		client, err := New("tok", WithBaseURL(srv.URL))
		require.NoError(t, err)
		resp, err := client.do(context.Background(), http.MethodGet, "/bad", nil)
		require.NoError(t, err)
		err = checkStatus(resp, http.StatusOK)
		require.Error(t, err)
		var apiErr *APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode)
		assert.Equal(t, "name already taken", apiErr.Message)
	})

	t.Run("no JSON body falls back to status text", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/nojson", func(w http.ResponseWriter, r *http.Request) {
			// Use 418 (not retryable, not in 500-504 range)
			w.WriteHeader(http.StatusTeapot)
			fmt.Fprint(w, "not json at all")
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		client, err := New("tok", WithBaseURL(srv.URL))
		require.NoError(t, err)
		resp, err := client.do(context.Background(), http.MethodGet, "/nojson", nil)
		require.NoError(t, err)
		err = checkStatus(resp, http.StatusOK)
		require.Error(t, err)
		var apiErr *APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, "I'm a teapot", apiErr.Message)
	})
}

func TestExtractMessage(t *testing.T) {
	t.Run("string message", func(t *testing.T) {
		assert.Equal(t, "something broke", extractMessage("something broke", "fallback"))
	})
	t.Run("empty string uses fallback", func(t *testing.T) {
		assert.Equal(t, "fallback", extractMessage("", "fallback"))
	})
	t.Run("map message", func(t *testing.T) {
		m := map[string]any{"name": []any{"is too short"}}
		msg := extractMessage(m, "")
		assert.Contains(t, msg, "name:")
	})
	t.Run("array message", func(t *testing.T) {
		a := []any{"error one", "error two"}
		msg := extractMessage(a, "")
		assert.Contains(t, msg, "error one")
		assert.Contains(t, msg, "error two")
	})
	t.Run("nil message uses fallback", func(t *testing.T) {
		assert.Equal(t, "fallback", extractMessage(nil, "fallback"))
	})
	t.Run("empty map uses fallback", func(t *testing.T) {
		assert.Equal(t, "fallback", extractMessage(map[string]any{}, "fallback"))
	})
	t.Run("empty array uses fallback", func(t *testing.T) {
		assert.Equal(t, "fallback", extractMessage([]any{}, "fallback"))
	})
}

func TestRetryOnServerError(t *testing.T) {
	t.Run("retries on 500 then succeeds", func(t *testing.T) {
		var attempts atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/flaky", func(w http.ResponseWriter, r *http.Request) {
			n := attempts.Add(1)
			if n <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"status":"ok"}`)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		client, err := New("tok", WithBaseURL(srv.URL))
		require.NoError(t, err)
		resp, err := client.do(context.Background(), http.MethodGet, "/flaky", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.EqualValues(t, 3, attempts.Load())
	})

	t.Run("retries on 429 respects Retry-After", func(t *testing.T) {
		var attempts atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/ratelimit", func(w http.ResponseWriter, r *http.Request) {
			n := attempts.Add(1)
			if n == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{}`)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		client, err := New("tok", WithBaseURL(srv.URL))
		require.NoError(t, err)
		resp, err := client.do(context.Background(), http.MethodGet, "/ratelimit", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.EqualValues(t, 2, attempts.Load())
	})

	t.Run("gives up after max retries", func(t *testing.T) {
		var attempts atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/down", func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		client, err := New("tok", WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, err = client.do(context.Background(), http.MethodGet, "/down", nil)
		require.Error(t, err)
		var apiErr *APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
		assert.EqualValues(t, maxRetries, attempts.Load())
	})
}

func TestAuthHeader(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-token", r.Header.Get("PRIVATE-TOKEN"))
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	_, err := client.GetRepo(context.Background(), "owner", "repo")
	require.NoError(t, err)
}

// ---------- repo.go tests ----------

func TestGetRepo(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyrepo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		json.NewEncoder(w).Encode(map[string]any{
			"id":                  42,
			"name":                "myrepo",
			"path_with_namespace": "mygroup/myrepo",
			"default_branch":      "main",
			"visibility":          "public",
			"archived":            false,
			"forked_from_project": nil,
		})
	})

	repo, err := client.GetRepo(context.Background(), "mygroup", "myrepo")
	require.NoError(t, err)
	assert.Equal(t, int64(42), repo.ID)
	assert.Equal(t, "myrepo", repo.Name)
	assert.Equal(t, "mygroup/myrepo", repo.FullName)
	assert.Equal(t, "main", repo.DefaultBranch)
	assert.False(t, repo.Private)
	assert.False(t, repo.Archived)
	assert.False(t, repo.Fork)
}

func TestGetRepo_Fork(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/user%2Ffork", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":                  99,
			"name":                "fork",
			"path_with_namespace": "user/fork",
			"default_branch":      "main",
			"visibility":          "private",
			"archived":            false,
			"forked_from_project": map[string]any{"id": 1},
		})
	})

	repo, err := client.GetRepo(context.Background(), "user", "fork")
	require.NoError(t, err)
	assert.True(t, repo.Private)
	assert.True(t, repo.Fork)
}

func TestGetRepo_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Fgone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"404 Project Not Found"}`)
	})

	_, err := client.GetRepo(context.Background(), "owner", "gone")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestListOrgRepos(t *testing.T) {
	client, mux := setupTest(t)

	callCount := 0
	mux.HandleFunc("/api/v4/groups/myorg/projects", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		callCount++

		page := r.URL.Query().Get("page")
		switch page {
		case "1", "":
			w.Header().Set("X-Next-Page", "2")
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"id": 1, "name": "public-repo", "path_with_namespace": "myorg/public-repo",
					"default_branch": "main", "visibility": "public", "archived": false,
					"forked_from_project": nil,
				},
				{
					"id": 2, "name": "archived-repo", "path_with_namespace": "myorg/archived-repo",
					"default_branch": "main", "visibility": "public", "archived": true,
					"forked_from_project": nil,
				},
				{
					"id": 3, "name": "forked-repo", "path_with_namespace": "myorg/forked-repo",
					"default_branch": "main", "visibility": "public", "archived": false,
					"forked_from_project": map[string]any{"id": 99},
				},
				{
					"id": 4, "name": "private-repo", "path_with_namespace": "myorg/private-repo",
					"default_branch": "main", "visibility": "private", "archived": false,
					"forked_from_project": nil,
				},
			})
		case "2":
			// second page -- empty, stops pagination
			json.NewEncoder(w).Encode([]map[string]any{})
		}
	})

	repos, err := client.ListOrgRepos(context.Background(), "myorg", false)
	require.NoError(t, err)
	// Only public-repo passes the filter (not archived, not forked, not private)
	require.Len(t, repos, 1)
	assert.Equal(t, "public-repo", repos[0].Name)
	assert.Equal(t, "myorg/public-repo", repos[0].FullName)
	assert.Equal(t, int64(1), repos[0].ID)
	assert.Equal(t, 1, callCount, "only one page fetched because first page had fewer than 100 items")
}

func TestCreateRepo(t *testing.T) {
	client, mux := setupTest(t)

	// Handler for GET /groups/:group (lookup group ID)
	mux.HandleFunc("/api/v4/groups/myorg", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		json.NewEncoder(w).Encode(map[string]any{"id": 10})
	})

	// Handler for POST /projects
	mux.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "new-repo", body["name"])
		assert.Equal(t, float64(10), body["namespace_id"])
		assert.Equal(t, "A new repo", body["description"])
		assert.Equal(t, "private", body["visibility"])
		assert.Equal(t, true, body["initialize_with_readme"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id":                  55,
			"name":                "new-repo",
			"path_with_namespace": "myorg/new-repo",
			"default_branch":      "main",
			"visibility":          "private",
		})
	})

	repo, err := client.CreateRepo(context.Background(), "myorg", "new-repo", "A new repo", true)
	require.NoError(t, err)
	assert.Equal(t, int64(55), repo.ID)
	assert.Equal(t, "new-repo", repo.Name)
	assert.Equal(t, "myorg/new-repo", repo.FullName)
	assert.True(t, repo.Private)
}

func TestCreateRepo_Public(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/groups/org", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 5})
	})
	mux.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "public", body["visibility"])
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "org/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	repo, err := client.CreateRepo(context.Background(), "org", "repo", "desc", false)
	require.NoError(t, err)
	assert.False(t, repo.Private)
}

func TestDeleteRepo(t *testing.T) {
	client, mux := setupTest(t)
	called := false
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		called = true
		w.WriteHeader(http.StatusAccepted)
	})

	err := client.DeleteRepo(context.Background(), "owner", "repo")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestFindExistingFork(t *testing.T) {
	t.Run("fork found", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/upstream%2Frepo/forks", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "true", r.URL.Query().Get("owned"))
			assert.Equal(t, "1", r.URL.Query().Get("per_page"))
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"path": "repo",
					"namespace": map[string]any{
						"full_path": "myuser",
					},
				},
			})
		})

		forkOwner, forkRepo, err := client.FindExistingFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "myuser", forkOwner)
		assert.Equal(t, "repo", forkRepo)
	})

	t.Run("no fork found", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/upstream%2Frepo/forks", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]map[string]any{})
		})

		forkOwner, forkRepo, err := client.FindExistingFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Empty(t, forkOwner)
		assert.Empty(t, forkRepo)
	})
}

func TestCreateFork(t *testing.T) {
	t.Run("creates fork successfully", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/upstream%2Frepo/fork", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"path": "repo",
				"namespace": map[string]any{
					"full_path": "contributor",
				},
			})
		})

		forkOwner, forkRepo, err := client.CreateFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "contributor", forkOwner)
		assert.Equal(t, "repo", forkRepo)
	})

	t.Run("conflict falls back to FindExistingFork", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/upstream%2Frepo/fork", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"already forked"}`)
		})
		mux.HandleFunc("/api/v4/projects/upstream%2Frepo/forks", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"path": "repo",
					"namespace": map[string]any{
						"full_path": "existinguser",
					},
				},
			})
		})

		forkOwner, forkRepo, err := client.CreateFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "existinguser", forkOwner)
		assert.Equal(t, "repo", forkRepo)
	})
}

func TestGetBranchRef(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/main", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{
				"id": "abc123def456",
			},
		})
	})

	sha, err := client.GetBranchRef(context.Background(), "owner", "repo", "main")
	require.NoError(t, err)
	assert.Equal(t, "abc123def456", sha)
}

func TestCreateBranch(t *testing.T) {
	client, mux := setupTest(t)

	// GetRepo call to get default branch
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
				"default_branch": "main", "visibility": "public",
			})
		}
	})

	// POST to create branch
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "feature-branch", body["branch"])
		assert.Equal(t, "main", body["ref"])
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"name": "feature-branch"})
	})

	err := client.CreateBranch(context.Background(), "owner", "repo", "feature-branch")
	require.NoError(t, err)
}

func TestGetRef(t *testing.T) {
	t.Run("heads prefix", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits/main", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"id": "sha-branch-123"})
		})

		sha, err := client.GetRef(context.Background(), "owner", "repo", "heads/main")
		require.NoError(t, err)
		assert.Equal(t, "sha-branch-123", sha)
	})

	t.Run("tags prefix", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits/v1.0", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"id": "sha-tag-456"})
		})

		sha, err := client.GetRef(context.Background(), "owner", "repo", "tags/v1.0")
		require.NoError(t, err)
		assert.Equal(t, "sha-tag-456", sha)
	})

	t.Run("plain ref", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits/abc123", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"id": "abc123"})
		})

		sha, err := client.GetRef(context.Background(), "owner", "repo", "abc123")
		require.NoError(t, err)
		assert.Equal(t, "abc123", sha)
	})
}

func TestCreateFile(t *testing.T) {
	client, mux := setupTest(t)

	// GetRepo for default branch
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	// POST file create
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/README.md", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "main", body["branch"])
		assert.Equal(t, "add readme", body["commit_message"])
		assert.Equal(t, "base64", body["encoding"])

		decoded, err := base64.StdEncoding.DecodeString(body["content"].(string))
		require.NoError(t, err)
		assert.Equal(t, "hello world", string(decoded))

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"file_path": "README.md"})
	})

	err := client.CreateFile(context.Background(), "owner", "repo", "README.md", "add readme", []byte("hello world"))
	require.NoError(t, err)
}

func TestGetFileContent(t *testing.T) {
	client, mux := setupTest(t)
	content := "file content here"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	// url.PathEscape encodes "docs/guide.md" to "docs%2Fguide.md"
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/docs%2Fguide.md", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "HEAD", r.URL.Query().Get("ref"))
		json.NewEncoder(w).Encode(map[string]any{
			"content":  encoded,
			"encoding": "base64",
		})
	})

	data, err := client.GetFileContent(context.Background(), "owner", "repo", "docs/guide.md")
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestGetFileContentAtRef(t *testing.T) {
	client, mux := setupTest(t)
	content := "versioned content"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/config.yml", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "v2.0", r.URL.Query().Get("ref"))
		json.NewEncoder(w).Encode(map[string]any{
			"content":  encoded,
			"encoding": "base64",
		})
	})

	data, err := client.GetFileContentAtRef(context.Background(), "owner", "repo", "config.yml", "v2.0")
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestGetFileContent_PlainEncoding(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/plain.txt", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"content":  "plain text content",
			"encoding": "text",
		})
	})

	data, err := client.GetFileContent(context.Background(), "owner", "repo", "plain.txt")
	require.NoError(t, err)
	assert.Equal(t, "plain text content", string(data))
}

func TestCreateOrUpdateFile(t *testing.T) {
	t.Run("create succeeds on first try", func(t *testing.T) {
		client, mux := setupTest(t)

		// GetRepo for default branch
		mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
				"default_branch": "main", "visibility": "public",
			})
		})

		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/new.txt", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"file_path": "new.txt"})
		})

		err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "new.txt", "add file", []byte("data"))
		require.NoError(t, err)
	})

	t.Run("falls back to PUT on already exists", func(t *testing.T) {
		client, mux := setupTest(t)

		mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
				"default_branch": "main", "visibility": "public",
			})
		})

		callCount := 0
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/existing.txt", func(w http.ResponseWriter, r *http.Request) {
			callCount++
			switch r.Method {
			case http.MethodPost:
				// First call: file already exists
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{
					"message": "A file with this name already exists",
				})
			case http.MethodPut:
				// Second call: update succeeds
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{"file_path": "existing.txt"})
			}
		})

		err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "existing.txt", "update file", []byte("new data"))
		require.NoError(t, err)
		assert.Equal(t, 2, callCount) // POST then PUT
	})
}

func TestDeleteFile(t *testing.T) {
	client, mux := setupTest(t)

	// GetRepo for default branch
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/old.txt", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)

		// Read and check the body payload
		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal(bodyBytes, &body))
		assert.Equal(t, "main", body["branch"])
		assert.Equal(t, "remove old file", body["commit_message"])

		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteFile(context.Background(), "owner", "repo", "old.txt", "remove old file")
	require.NoError(t, err)
}

func TestDeleteFiles(t *testing.T) {
	t.Run("deletes existing and skips missing atomically", func(t *testing.T) {
		client, mux := setupTest(t)

		mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
				"default_branch": "main", "visibility": "public",
			})
		})

		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "abc123", "path": "exists.txt", "type": "blob", "mode": "100644"},
			})
		})

		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			bodyBytes, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var body map[string]any
			require.NoError(t, json.Unmarshal(bodyBytes, &body))
			assert.Equal(t, "main", body["branch"])
			assert.Equal(t, "cleanup", body["commit_message"])
			actions := body["actions"].([]any)
			assert.Len(t, actions, 1)
			action := actions[0].(map[string]any)
			assert.Equal(t, "delete", action["action"])
			assert.Equal(t, "exists.txt", action["file_path"])

			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"abc"}`)
		})

		deleted, err := client.DeleteFiles(context.Background(), "owner", "repo", "cleanup", []string{"exists.txt", "gone.txt"})
		require.NoError(t, err)
		assert.Equal(t, 1, deleted)
	})

	t.Run("empty paths returns zero", func(t *testing.T) {
		client, _ := setupTest(t)
		deleted, err := client.DeleteFiles(context.Background(), "owner", "repo", "msg", nil)
		require.NoError(t, err)
		assert.Equal(t, 0, deleted)
	})

	t.Run("all paths missing returns zero", func(t *testing.T) {
		client, mux := setupTest(t)

		mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
				"default_branch": "main", "visibility": "public",
			})
		})

		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]map[string]any{})
		})

		deleted, err := client.DeleteFiles(context.Background(), "owner", "repo", "cleanup", []string{"gone.txt"})
		require.NoError(t, err)
		assert.Equal(t, 0, deleted)
	})
}

func TestListDirectoryContents(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "docs", r.URL.Query().Get("path"))
		assert.Equal(t, "main", r.URL.Query().Get("ref"))

		json.NewEncoder(w).Encode([]map[string]any{
			{"name": "README.md", "path": "docs/README.md", "type": "blob"},
			{"name": "api", "path": "docs/api", "type": "tree"},
			{"name": "guide.md", "path": "docs/guide.md", "type": "blob"},
		})
	})

	entries, err := client.ListDirectoryContents(context.Background(), "owner", "repo", "docs", "main", false)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	assert.Equal(t, "README.md", entries[0].Path)
	assert.Equal(t, "file", entries[0].Type)
	assert.Equal(t, "api", entries[1].Path)
	assert.Equal(t, "dir", entries[1].Type)
	assert.Equal(t, "guide.md", entries[2].Path)
	assert.Equal(t, "file", entries[2].Type)
}

func TestListDirectoryContents_Recursive(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "true", r.URL.Query().Get("recursive"))
		json.NewEncoder(w).Encode([]map[string]any{
			{"name": "file.txt", "path": "file.txt", "type": "blob"},
		})
	})

	entries, err := client.ListDirectoryContents(context.Background(), "owner", "repo", "", "main", true)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "file.txt", entries[0].Path)
}

func TestListRepositoryFiles(t *testing.T) {
	client, mux := setupTest(t)

	callCount := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		page := r.URL.Query().Get("page")

		switch page {
		case "1":
			w.Header().Set("X-Next-Page", "2")
			// Return exactly 100 items to trigger pagination
			entries := make([]map[string]any, 100)
			for i := range entries {
				entries[i] = map[string]any{
					"path": fmt.Sprintf("file%d.go", i),
					"type": "blob",
				}
			}
			json.NewEncoder(w).Encode(entries)
		case "2":
			json.NewEncoder(w).Encode([]map[string]any{
				{"path": "dir", "type": "tree"},
				{"path": "extra.go", "type": "blob"},
			})
		}
	})

	files, err := client.ListRepositoryFiles(context.Background(), "owner", "repo")
	require.NoError(t, err)
	// 100 from page 1 + 1 blob from page 2 (tree entries excluded)
	assert.Len(t, files, 101)
	assert.Equal(t, "file0.go", files[0])
	assert.Equal(t, "extra.go", files[100])
	assert.Equal(t, 2, callCount)
}

func TestCommitFiles(t *testing.T) {
	client, mux := setupTest(t)

	// GetRepo for default branch
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	// Tree listing for idempotency check -- return empty tree (new repo)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	// POST commit
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		assert.Equal(t, "main", body["branch"])
		assert.Equal(t, "initial commit", body["commit_message"])

		actions := body["actions"].([]any)
		require.Len(t, actions, 2)

		action0 := actions[0].(map[string]any)
		assert.Equal(t, "create", action0["action"])
		assert.Equal(t, "README.md", action0["file_path"])

		action1 := actions[1].(map[string]any)
		assert.Equal(t, "create", action1["action"])
		assert.Equal(t, "script.sh", action1["file_path"])
		assert.Equal(t, true, action1["execute_filemode"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "commit-sha-123"})
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "initial commit", []forge.TreeFile{
		{Path: "README.md", Content: []byte("# Hello"), Mode: "100644"},
		{Path: "script.sh", Content: []byte("#!/bin/bash"), Mode: "100755"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_Idempotent(t *testing.T) {
	client, mux := setupTest(t)

	fileContent := []byte("# Hello")
	fileSHA := blobSHA(fileContent)

	// GetRepo for default branch
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	// Tree listing -- file already matches
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":   fileSHA,
				"path": "README.md",
				"type": "blob",
				"mode": "100644",
			},
		})
	})

	// The commits endpoint should NOT be called
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("commits endpoint should not be called when files are unchanged")
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "no-op commit", []forge.TreeFile{
		{Path: "README.md", Content: fileContent, Mode: "100644"},
	})
	require.NoError(t, err)
	assert.False(t, committed, "should not commit when files already match")
}

func TestCommitFiles_UpdateExisting(t *testing.T) {
	client, mux := setupTest(t)

	oldSHA := blobSHA([]byte("old content"))

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": oldSHA, "path": "README.md", "type": "blob", "mode": "100644"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		actions := body["actions"].([]any)
		require.Len(t, actions, 1)
		action := actions[0].(map[string]any)
		assert.Equal(t, "update", action["action"])
		assert.Equal(t, "README.md", action["file_path"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "new-sha"})
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "update readme", []forge.TreeFile{
		{Path: "README.md", Content: []byte("new content"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_Delete(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "someid", "path": "obsolete.txt", "type": "blob", "mode": "100644"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		actions := body["actions"].([]any)
		require.Len(t, actions, 1)
		action := actions[0].(map[string]any)
		assert.Equal(t, "delete", action["action"])
		assert.Equal(t, "obsolete.txt", action["file_path"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "del-sha"})
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "remove old file", []forge.TreeFile{
		{Path: "obsolete.txt", Delete: true},
	})
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_DeleteNonExistent(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "no-op delete", []forge.TreeFile{
		{Path: "nonexistent.txt", Delete: true},
	})
	require.NoError(t, err)
	assert.False(t, committed, "deleting non-existent file should be a no-op")
}

func TestCommitFiles_EmptyFiles(t *testing.T) {
	client, _ := setupTest(t)
	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "msg", nil)
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommitFiles_ModeChange(t *testing.T) {
	client, mux := setupTest(t)

	existingSHA := blobSHA([]byte("#!/bin/bash"))

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	// Same content but mode changed from 100755 to 100644
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": existingSHA, "path": "script.sh", "type": "blob", "mode": "100755"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		actions := body["actions"].([]any)
		require.Len(t, actions, 1)
		action := actions[0].(map[string]any)
		assert.Equal(t, "update", action["action"])
		// When going from 100755 to 100644, execute_filemode should be false
		assert.Equal(t, false, action["execute_filemode"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "mode-sha"})
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "remove exec bit", []forge.TreeFile{
		{Path: "script.sh", Content: []byte("#!/bin/bash"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed, "mode change should trigger commit")
}

func TestProjectPath(t *testing.T) {
	assert.Equal(t, "owner%2Frepo", projectPath("owner", "repo"))
	assert.Equal(t, "group%2Fsubgroup%2Frepo", projectPath("group/subgroup", "repo"))
}

func TestBlobSHA(t *testing.T) {
	// Verify blobSHA produces a 40-char hex SHA-1 and is deterministic
	content := []byte("hello")
	sha := blobSHA(content)
	assert.Len(t, sha, 40, "SHA-1 hex should be 40 chars")
	// Same input always produces the same hash
	assert.Equal(t, sha, blobSHA(content))
	// Different input produces a different hash
	assert.NotEqual(t, sha, blobSHA([]byte("world")))
}

func TestCommitFilesToBranch(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	treeCalled := false
	mux.HandleFunc("/api/v4/projects/own%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		treeCalled = true
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	var commitPayload map[string]any
	mux.HandleFunc("/api/v4/projects/own%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &commitPayload)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"abc123"}`)
	})

	committed, err := client.CommitFilesToBranch(ctx, "own", "repo", "feature", "add file", []forge.TreeFile{
		{Path: "new.txt", Content: []byte("content"), Mode: "100644"},
	})

	require.NoError(t, err)
	assert.True(t, committed)
	assert.True(t, treeCalled)
	assert.Equal(t, "feature", commitPayload["branch"])
}

func TestCommitFilesToBranch_Empty(t *testing.T) {
	client, err := New("token")
	require.NoError(t, err)
	committed, err := client.CommitFilesToBranch(context.Background(), "o", "r", "b", "msg", nil)
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestUpdateIssueComment_FoundInClosedIssues(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		if state == "opened" {
			json.NewEncoder(w).Encode([]map[string]any{
				{"iid": 1},
			})
			return
		}
		// closed issues
		json.NewEncoder(w).Encode([]map[string]any{
			{"iid": 2},
		})
	})

	// Note 99 not found on open issue 1
	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues/1/notes/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"404 Not Found"}`)
	})

	// Note 99 found on closed issue 2
	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues/2/notes/99", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":99}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"404 Not Found"}`)
	})

	err := client.UpdateIssueComment(ctx, "own", "repo", 99, "updated body")
	require.NoError(t, err)
}

func TestDeleteIssueComment_NotFoundAnywhere(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	err := client.DeleteIssueComment(ctx, "own", "repo", 999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not find issue containing this note")
}

func TestGetAuthenticatedUserIdentity_NoEmail(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"name":     "Test User",
			"username": "testuser",
			"email":    "",
		})
	})

	identity, err := client.GetAuthenticatedUserIdentity(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Test User", identity.Name)
}

func TestCreateOrUpdateFileOnBranch(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/repository/files/path%2Fto%2Ffile.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"file_path":"path/to/file.txt"}`)
			return
		}
	})

	err := client.CreateOrUpdateFileOnBranch(ctx, "own", "repo", "feature", "path/to/file.txt", "add file", []byte("data"))
	require.NoError(t, err)
}

func TestCreateOrUpdateFileOnBranch_Update(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/repository/files/file.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"A file with this name already exists"}`)
			return
		}
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"file_path":"file.txt"}`)
			return
		}
	})

	err := client.CreateOrUpdateFileOnBranch(ctx, "own", "repo", "main", "file.txt", "update", []byte("new"))
	require.NoError(t, err)
}

func TestCreateFileOnBranch(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	var gotBranch string
	mux.HandleFunc("/api/v4/projects/own%2Frepo/repository/files/readme.md", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		gotBranch = body["branch"]
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"file_path":"readme.md"}`)
	})

	err := client.CreateFileOnBranch(ctx, "own", "repo", "dev", "readme.md", "init", []byte("# readme"))
	require.NoError(t, err)
	assert.Equal(t, "dev", gotBranch)
}

func TestCheckRedirect_StripsTokenOnCrossOrigin(t *testing.T) {
	var gotToken string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(target.Close)

	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/landed", http.StatusTemporaryRedirect)
	})

	resp, err := client.do(ctx, http.MethodGet, "/projects/own%2Frepo", nil)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Empty(t, gotToken, "PRIVATE-TOKEN should be stripped on cross-origin redirect")
}

func TestCheckRedirect_StripsTokenOnTLSDowngrade(t *testing.T) {
	client, err := New("test-token")
	require.NoError(t, err)

	originalReq := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "gitlab.com", Path: "/original"},
		Header: http.Header{"PRIVATE-TOKEN": []string{"test-token"}},
	}
	redirectReq := &http.Request{
		URL:    &url.URL{Scheme: "http", Host: "gitlab.com", Path: "/redirect"},
		Header: http.Header{"PRIVATE-TOKEN": []string{"test-token"}},
	}

	err = client.http.CheckRedirect(redirectReq, []*http.Request{originalReq})
	require.NoError(t, err)
	assert.Empty(t, redirectReq.Header.Get("PRIVATE-TOKEN"), "PRIVATE-TOKEN should be stripped on TLS downgrade")
}

func TestRetryDelay_CapsLargeRetryAfter(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Retry-After": []string{"9999999999"}},
	}
	delay := retryDelay(resp, 0)
	assert.Equal(t, 300*time.Second, delay, "large Retry-After should be capped at 300s")
}

func TestTransportRetry_SkipsNonIdempotent(t *testing.T) {
	var attempts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/create", func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// Close connection without response to trigger net.Error
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server doesn't support hijack")
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := New("tok", WithBaseURL(srv.URL))
	require.NoError(t, err)
	_, err = client.do(context.Background(), http.MethodPost, "/create", map[string]string{"key": "val"})
	require.Error(t, err)
	assert.EqualValues(t, 1, attempts.Load(), "POST should not retry on transport error")
}
