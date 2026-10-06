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

// noWaitAfter returns a channel that is immediately ready, eliminating
// real sleeps in retry loops during tests.
func noWaitAfter(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

// setupTest creates a test server and a LiveClient pointed at it
// with retry delays disabled for fast tests.
func setupTest(t *testing.T) (*LiveClient, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := New("test-token", WithBaseURL(srv.URL), WithAfterFunc(noWaitAfter))
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

func TestBaseURL(t *testing.T) {
	c, err := New("tok", WithBaseURL("https://gitlab.self-hosted.example.com"))
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.self-hosted.example.com", c.BaseURL())
}

func TestBaseURL_Default(t *testing.T) {
	c, err := New("tok")
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.com", c.BaseURL())
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

		client, err := New("tok", WithBaseURL(srv.URL), WithAfterFunc(noWaitAfter))
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

		client, err := New("tok", WithBaseURL(srv.URL), WithAfterFunc(noWaitAfter))
		require.NoError(t, err)
		resp, err := client.do(context.Background(), http.MethodGet, "/ratelimit", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.EqualValues(t, 2, attempts.Load())
	})

	t.Run("retries transient network error then succeeds", func(t *testing.T) {
		var attempts atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/flaky-net", func(w http.ResponseWriter, r *http.Request) {
			n := attempts.Add(1)
			if n == 1 {
				// Simulate a transient network error by hijacking the connection
				// and closing it before writing any response.
				hj, ok := w.(http.Hijacker)
				require.True(t, ok)
				conn, _, err := hj.Hijack()
				require.NoError(t, err)
				conn.Close()
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		client, err := New("tok", WithBaseURL(srv.URL), WithAfterFunc(noWaitAfter))
		require.NoError(t, err)
		resp, err := client.do(context.Background(), http.MethodGet, "/flaky-net", nil)
		require.NoError(t, err)
		resp.Body.Close()
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

		client, err := New("tok", WithBaseURL(srv.URL), WithAfterFunc(noWaitAfter))
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

// The project's selected CI configuration path (possibly in another project)
// is exposed so safety checks can tell when .gitlab-ci.yml is not the
// pipeline GitLab runs.
func TestGetRepo_CIConfigPath(t *testing.T) {
	for name, path := range map[string]any{
		"custom path": "ci/main.yml@other/group",
		"unset":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			client, mux := setupTest(t)
			called := false
			mux.HandleFunc("/api/v4/projects/mygroup%2Fmyrepo", func(w http.ResponseWriter, r *http.Request) {
				called = true
				json.NewEncoder(w).Encode(map[string]any{
					"id":                  42,
					"name":                "myrepo",
					"path_with_namespace": "mygroup/myrepo",
					"default_branch":      "main",
					"ci_config_path":      path,
				})
			})

			repo, err := client.GetRepo(context.Background(), "mygroup", "myrepo")

			require.NoError(t, err)
			require.True(t, called)
			if path == nil {
				assert.Empty(t, repo.CIConfigPath)
			} else {
				assert.Equal(t, path, repo.CIConfigPath)
			}
		})
	}
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

func TestGetPipelineVariablesMinimumOverrideRole(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyrepo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		json.NewEncoder(w).Encode(map[string]any{
			"id": 42,
			"ci_pipeline_variables_minimum_override_role": "developer",
		})
	})

	role, err := client.GetPipelineVariablesMinimumOverrideRole(context.Background(), "mygroup", "myrepo")
	require.NoError(t, err)
	assert.Equal(t, forge.PipelineVarOverrideDeveloper, role)
}

func TestGetPipelineVariablesMinimumOverrideRole_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Fgone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"404 Project Not Found"}`)
	})

	_, err := client.GetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "gone")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestGetPipelineVariablesMinimumOverrideRole_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"403 Forbidden"}`)
	})

	_, err := client.GetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "repo")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestGetPipelineVariablesMinimumOverrideRole_ServerError(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"500 Internal Server Error"}`)
	})

	_, err := client.GetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "repo")
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
}

func TestGetPipelineVariablesMinimumOverrideRole_DecodeError(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := client.GetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode pipeline variables minimum override role")
}

func TestSetPipelineVariablesMinimumOverrideRole(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/mygroup%2Fmyrepo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, forge.PipelineVarOverrideOwner, body["ci_pipeline_variables_minimum_override_role"])
		json.NewEncoder(w).Encode(map[string]any{
			"id": 42,
			"ci_pipeline_variables_minimum_override_role": forge.PipelineVarOverrideOwner,
		})
	})

	err := client.SetPipelineVariablesMinimumOverrideRole(ctx, "mygroup", "myrepo", forge.PipelineVarOverrideOwner)
	require.NoError(t, err)
}

func TestSetPipelineVariablesMinimumOverrideRole_InvalidRole(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected request for an invalid role; validation must reject before issuing it")
	})

	err := client.SetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "repo", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrInvalidPipelineVarOverrideRole)
}

func TestSetPipelineVariablesMinimumOverrideRole_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Fgone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"404 Project Not Found"}`)
	})

	err := client.SetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "gone", forge.PipelineVarOverrideOwner)
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestSetPipelineVariablesMinimumOverrideRole_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"403 Forbidden"}`)
	})

	err := client.SetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "repo", forge.PipelineVarOverrideOwner)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestSetPipelineVariablesMinimumOverrideRole_ServerError(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"500 Internal Server Error"}`)
	})

	err := client.SetPipelineVariablesMinimumOverrideRole(context.Background(), "owner", "repo", forge.PipelineVarOverrideOwner)
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
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

func TestCreateBranchFromSHA(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "feature-branch", body["branch"])
		assert.Equal(t, "abc123sha", body["ref"])
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"name": "feature-branch"})
	})

	err := client.CreateBranchFromSHA(context.Background(), "owner", "repo", "feature-branch", "abc123sha")
	require.NoError(t, err)
}

func TestCreateBranchFromSHA_AlreadyExists(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Branch already exists",
		})
	})

	err := client.CreateBranchFromSHA(context.Background(), "owner", "repo", "feature-branch", "abc123sha")
	require.Error(t, err)
	assert.True(t, forge.IsAlreadyExists(err), "expected ErrAlreadyExists, got: %v", err)
}

func TestCreateBranchFromSHA_GenericError(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Internal server error",
		})
	})

	err := client.CreateBranchFromSHA(context.Background(), "owner", "repo", "feature-branch", "abc123sha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create branch feature-branch from SHA")
	assert.False(t, forge.IsAlreadyExists(err), "non-400 error should not be ErrAlreadyExists")
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

func TestCompareCommits(t *testing.T) {
	t.Run("ahead when only forward commits exist", func(t *testing.T) {
		client, mux := setupTest(t)
		callCount := 0
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/compare", func(w http.ResponseWriter, r *http.Request) {
			callCount++
			from := r.URL.Query().Get("from")
			to := r.URL.Query().Get("to")
			if from == "abc123" && to == "def456" {
				// Forward: commits exist → head has new commits.
				json.NewEncoder(w).Encode(map[string]any{
					"commits": []map[string]any{{"id": "def456"}},
					"diffs":   []map[string]any{{"new_path": "file.go"}},
				})
			} else if from == "def456" && to == "abc123" {
				// Reverse: no commits → head is strictly ahead.
				json.NewEncoder(w).Encode(map[string]any{
					"commits": []map[string]any{},
					"diffs":   []map[string]any{},
				})
			}
		})

		status, err := client.CompareCommits(context.Background(), "owner", "repo", "abc123", "def456")
		require.NoError(t, err)
		assert.Equal(t, "ahead", status)
		assert.Equal(t, 2, callCount, "should make both forward and reverse API calls")
	})

	t.Run("identical when no commits and no diffs", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/compare", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"commits": []map[string]any{},
				"diffs":   []map[string]any{},
			})
		})

		status, err := client.CompareCommits(context.Background(), "owner", "repo", "abc", "abc")
		require.NoError(t, err)
		assert.Equal(t, "identical", status)
	})

	t.Run("behind when reverse commits exist", func(t *testing.T) {
		client, mux := setupTest(t)
		callCount := 0
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/compare", func(w http.ResponseWriter, r *http.Request) {
			callCount++
			from := r.URL.Query().Get("from")
			to := r.URL.Query().Get("to")
			if from == "abc123" && to == "def456" {
				// Forward: no commits but has diffs → triggers reverse.
				json.NewEncoder(w).Encode(map[string]any{
					"commits": []map[string]any{},
					"diffs":   []map[string]any{{"new_path": "file.go"}},
				})
			} else if from == "def456" && to == "abc123" {
				// Reverse: commits exist → head is behind base.
				json.NewEncoder(w).Encode(map[string]any{
					"commits": []map[string]any{{"id": "abc123"}},
					"diffs":   []map[string]any{{"new_path": "file.go"}},
				})
			}
		})

		status, err := client.CompareCommits(context.Background(), "owner", "repo", "abc123", "def456")
		require.NoError(t, err)
		assert.Equal(t, "behind", status)
		assert.Equal(t, 2, callCount, "should make both forward and reverse API calls")
	})

	t.Run("diverged when neither direction has commits", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/compare", func(w http.ResponseWriter, r *http.Request) {
			// Both directions: diffs but no commits.
			json.NewEncoder(w).Encode(map[string]any{
				"commits": []map[string]any{},
				"diffs":   []map[string]any{{"new_path": "file.go"}},
			})
		})

		status, err := client.CompareCommits(context.Background(), "owner", "repo", "abc", "def")
		require.NoError(t, err)
		assert.Equal(t, "diverged", status)
	})

	t.Run("diverged when both directions have commits", func(t *testing.T) {
		client, mux := setupTest(t)
		callCount := 0
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/compare", func(w http.ResponseWriter, r *http.Request) {
			callCount++
			// Both forward and reverse return commits — truly diverged.
			json.NewEncoder(w).Encode(map[string]any{
				"commits": []map[string]any{{"id": "some-commit"}},
				"diffs":   []map[string]any{{"new_path": "file.go"}},
			})
		})

		status, err := client.CompareCommits(context.Background(), "owner", "repo", "abc", "def")
		require.NoError(t, err)
		assert.Equal(t, "diverged", status)
		assert.Equal(t, 2, callCount, "should make both forward and reverse API calls")
	})

	t.Run("returns error on API failure", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/compare", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		_, err := client.CompareCommits(context.Background(), "owner", "repo", "abc", "def")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compare commits")
	})

	t.Run("degrades to diverged on reverse call failure", func(t *testing.T) {
		client, mux := setupTest(t)
		callCount := 0
		mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/compare", func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 1 {
				// Forward: diffs but no commits.
				json.NewEncoder(w).Encode(map[string]any{
					"commits": []map[string]any{},
					"diffs":   []map[string]any{{"new_path": "file.go"}},
				})
			} else {
				// Reverse: API error.
				w.WriteHeader(http.StatusInternalServerError)
			}
		})

		status, err := client.CompareCommits(context.Background(), "owner", "repo", "abc", "def")
		require.NoError(t, err)
		assert.Equal(t, "diverged", status, "should degrade gracefully on reverse call failure")
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

func TestGetFileContentAtRef_NotFound(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/state.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "404 File Not Found"})
	})

	_, err := client.GetFileContentAtRef(context.Background(), "owner", "repo", "state.json", "fullsend-poll-state-slash")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestGetFileContentAtRef_NonNotFoundError(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/files/state.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "forbidden"})
	})

	_, err := client.GetFileContentAtRef(context.Background(), "owner", "repo", "state.json", "main")
	require.Error(t, err)
	assert.False(t, forge.IsNotFound(err))
	assert.Contains(t, err.Error(), "get file content")
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

func TestCommitFiles_DuplicateCreatePaths(t *testing.T) {
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

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		actions := body["actions"].([]any)
		require.Len(t, actions, 1, "duplicate create actions for the same path must be collapsed")

		action := actions[0].(map[string]any)
		assert.Equal(t, "create", action["action"])
		assert.Equal(t, ".gitlab/ci/scripts/trust-ci-server-ca.sh", action["file_path"])
		got, err := base64.StdEncoding.DecodeString(action["content"].(string))
		require.NoError(t, err)
		assert.Equal(t, []byte("first"), got, "first actionable entry must win")

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "commit-sha-dup"})
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "repair scaffold", []forge.TreeFile{
		{Path: ".gitlab/ci/scripts/trust-ci-server-ca.sh", Content: []byte("first"), Mode: "100755"},
		{Path: ".gitlab/ci/scripts/trust-ci-server-ca.sh", Content: []byte("second"), Mode: "100755"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_DeleteThenCreateSamePath(t *testing.T) {
	client, mux := setupTest(t)

	existing := []byte("old")
	existingSHA := blobSHA(existing)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": existingSHA, "path": "conflict.txt", "type": "blob", "mode": "100644"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		actions := body["actions"].([]any)
		require.Len(t, actions, 1, "delete then create of the same path must collapse to one action")

		action := actions[0].(map[string]any)
		assert.Equal(t, "delete", action["action"], "first actionable entry (delete) must win")
		assert.Equal(t, "conflict.txt", action["file_path"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "commit-sha-del-create"})
	})

	committed, err := client.CommitFiles(context.Background(), "owner", "repo", "replace file", []forge.TreeFile{
		{Path: "conflict.txt", Delete: true},
		{Path: "conflict.txt", Content: []byte("new"), Mode: "100644"},
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

func TestWithSkipCI(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"save state", "save state [skip ci]"},
		{"save state [skip ci]", "save state [skip ci]"},
		{"", "[skip ci]"},
		{"  padded  ", "padded [skip ci]"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, withSkipCI(tt.in), "in=%q", tt.in)
	}
}

func TestForceCommitFileToBranch(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tree endpoint must not be called for force-re-root commits")
	})

	var commitPayload map[string]any
	commitCalls := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			assert.Equal(t, "main", r.URL.Query().Get("ref_name"))
			assert.Equal(t, "true", r.URL.Query().Get("first_parent"))
			assert.Equal(t, "100", r.URL.Query().Get("per_page"))
			assert.Equal(t, "1", r.URL.Query().Get("page"))
			assert.Empty(t, r.URL.Query().Get("order_by"), "order_by is not a valid param on this endpoint")
			assert.Empty(t, r.URL.Query().Get("sort"), "sort is not a valid param on this endpoint")
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "root-sha-abc"},
			})
		case http.MethodPost:
			commitCalls++
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &commitPayload))
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "new-commit"})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	err := client.ForceCommitFileToBranch(ctx, "owner", "repo", "fullsend-poll-state-slash", "state.json", "save poll state", []byte(`{"n":1}`))
	require.NoError(t, err)
	assert.Equal(t, 1, commitCalls)
	assert.Equal(t, "fullsend-poll-state-slash", commitPayload["branch"])
	assert.Equal(t, true, commitPayload["force"])
	assert.Equal(t, "root-sha-abc", commitPayload["start_sha"])
	assert.Equal(t, "save poll state [skip ci]", commitPayload["commit_message"])

	actions, ok := commitPayload["actions"].([]any)
	require.True(t, ok)
	require.Len(t, actions, 1)
	action := actions[0].(map[string]any)
	assert.Equal(t, "create", action["action"])
	assert.Equal(t, "state.json", action["file_path"])
	assert.Equal(t, "base64", action["encoding"])
	decoded, err := base64.StdEncoding.DecodeString(action["content"].(string))
	require.NoError(t, err)
	assert.Equal(t, `{"n":1}`, string(decoded))
}

func TestForceCommitFileToBranch_RepeatedWrites(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	var payloads []map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{{"id": "fixed-root"}})
		case http.MethodPost:
			var payload map[string]any
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &payload))
			payloads = append(payloads, payload)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("c%d", len(payloads))})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	require.NoError(t, client.ForceCommitFileToBranch(ctx, "owner", "repo", "state-branch", "state.json", "w1", []byte("v1")))
	require.NoError(t, client.ForceCommitFileToBranch(ctx, "owner", "repo", "state-branch", "state.json", "w2", []byte("v2")))
	require.Len(t, payloads, 2)
	for i, p := range payloads {
		assert.Equal(t, true, p["force"], "write %d", i)
		assert.Equal(t, "fixed-root", p["start_sha"], "write %d", i)
		assert.Equal(t, "state-branch", p["branch"], "write %d", i)
	}
}

// TestForceCommitFileToBranch_WalksToLastPage asserts the actual root
// resolution protocol: first-parent history is paginated (not sorted via
// order_by/sort, which this endpoint does not support) and the root is the
// *last* entry of the *last* page - not the first entry of the first page,
// which would just be the default branch's current HEAD.
func TestForceCommitFileToBranch_WalksToLastPage(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	var commitPayload map[string]any
	var pagesRequested []string
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			assert.Equal(t, "true", r.URL.Query().Get("first_parent"))
			assert.Equal(t, "100", r.URL.Query().Get("per_page"))
			page := r.URL.Query().Get("page")
			pagesRequested = append(pagesRequested, page)
			switch page {
			case "1":
				// A full page: the branch tip ("head-commit") is first,
				// far from the true root. If resolution stopped after the
				// first page (or took its first entry), it would wrongly
				// pick a non-root commit.
				commits := make([]map[string]any, 100)
				commits[0] = map[string]any{"id": "head-commit"}
				for i := 1; i < 100; i++ {
					commits[i] = map[string]any{"id": fmt.Sprintf("mid-commit-%d", i)}
				}
				w.Header().Set("X-Next-Page", "2")
				json.NewEncoder(w).Encode(commits)
			case "2":
				// Final, partial page: its last entry is the true root.
				json.NewEncoder(w).Encode([]map[string]any{
					{"id": "penultimate-commit"},
					{"id": "true-root-commit"},
				})
			default:
				t.Fatalf("unexpected page %q", page)
			}
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &commitPayload))
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "new-commit"})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	err := client.ForceCommitFileToBranch(ctx, "owner", "repo", "state-branch", "state.json", "save poll state", []byte(`{"n":1}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, pagesRequested)
	assert.Equal(t, "true-root-commit", commitPayload["start_sha"])
}

func TestForceCommitFileToBranch_SkipCIAlreadyPresent(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	var message string
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{{"id": "root"}})
		case http.MethodPost:
			var payload map[string]any
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &payload))
			message = payload["commit_message"].(string)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "c"})
		}
	})

	err := client.ForceCommitFileToBranch(context.Background(), "owner", "repo", "b", "f", "already [skip ci]", []byte("x"))
	require.NoError(t, err)
	assert.Equal(t, "already [skip ci]", message)
}

func TestForceCommitFileToBranch_MissingBase(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	err := client.ForceCommitFileToBranch(context.Background(), "owner", "repo", "b", "f", "m", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.Contains(t, err.Error(), "resolve force-commit base")
}

func TestForceCommitFileToBranch_UnreachableBase(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"message": "404 Commit Not Found"})
			return
		}
		t.Fatal("POST must not run when the base is unreachable")
	})

	err := client.ForceCommitFileToBranch(context.Background(), "owner", "repo", "b", "f", "m", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestForceCommitFileToBranch_GetRepoError(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "404 Project Not Found"})
	})

	err := client.ForceCommitFileToBranch(context.Background(), "owner", "repo", "b", "f", "m", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve force-commit base")
}

func TestForceCommitFileToBranch_CommitError(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{{"id": "root"}})
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"message": "start_sha is invalid"})
		}
	})

	err := client.ForceCommitFileToBranch(context.Background(), "owner", "repo", "b", "f", "m", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "force commit f to b")
}

func TestForceCommitFileToBranch_RequiredArgs(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	err := client.ForceCommitFileToBranch(ctx, "owner", "repo", "", "f", "m", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch and path are required")

	err = client.ForceCommitFileToBranch(ctx, "owner", "repo", "b", "", "m", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch and path are required")
}

func TestForceCommitFileToBranch_DecodeCommitsError(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.Write([]byte("not-json"))
	})

	err := client.ForceCommitFileToBranch(context.Background(), "owner", "repo", "b", "f", "m", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode commits")
}

func TestForceCommitFileToBranch_EmptyCommitID(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "", r.URL.Query().Get("ref_name"))
		json.NewEncoder(w).Encode([]map[string]any{{"id": ""}})
	})

	err := client.ForceCommitFileToBranch(context.Background(), "owner", "repo", "b", "f", "m", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestCommitFileToBranch_EmptyExpectedSHAUsesRootNoForce(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	var treeRef string
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		treeRef = r.URL.Query().Get("ref")
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	var commitPayload map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{{"id": "root-sha"}})
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &commitPayload))
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "new-commit"})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "")
	require.NoError(t, err)
	assert.Equal(t, "root-sha", treeRef, "actions must be diffed against the root tree, not the target branch")
	_, hasForce := commitPayload["force"]
	assert.False(t, hasForce, "an empty-expectedSHA create must not force-re-root, so a concurrent first writer surfaces a conflict instead of being overwritten")
	assert.Equal(t, "root-sha", commitPayload["start_sha"])
	assert.Equal(t, "persist poll state [skip ci]", commitPayload["commit_message"])
}

// TestCommitFileToBranch_EmptyExpectedSHAConcurrentFirstWriterIsNonFastForward
// covers the missing-branch race: two writers both observe the branch as
// absent (expectedSHA == "") and race to create it. GitLab reports the
// loser's create as already-exists; that must surface as
// forge.ErrNonFastForward so persistWithCAS reloads the winner's document,
// unions this writer's dispatched keys, and retries — instead of the loser
// force-overwriting the winner's HMAC-signed state.
func TestCommitFileToBranch_EmptyExpectedSHAConcurrentFirstWriterIsNonFastForward(t *testing.T) {
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
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{{"id": "root-sha"}})
		case http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch named 'state-branch' already exists",
			})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist", []byte(`{"n":1}`), "")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
}

// TestCommitFileToBranch_EmptyExpectedSHAConcurrentFirstWriterBadRequestIsNonFastForward
// is the production GitLab shape of the missing-branch race: the commits API
// returns 400 (not 409) with "A branch called '...' already exists" when
// start_sha is the repository root and another writer created the branch
// first. persistWithCAS retries only on ErrNonFastForward, so this 400 must
// be mapped the same way as the 409 already-exists case above.
func TestCommitFileToBranch_EmptyExpectedSHAConcurrentFirstWriterBadRequestIsNonFastForward(t *testing.T) {
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
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{{"id": "root-sha"}})
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
			})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist", []byte(`{"n":1}`), "")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
}

func TestCommitFileToBranch_PinsStartSHAWithoutForce(t *testing.T) {
	client, mux := setupTest(t)

	var treeRef string
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		treeRef = r.URL.Query().Get("ref")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "oldblob", "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	var commitPayload map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &commitPayload))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "new-commit"})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":2}`), "loaded-sha")
	require.NoError(t, err)
	assert.Equal(t, "loaded-sha", treeRef, "actions must apply to the CAS parent tree")
	assert.Equal(t, "state-branch", commitPayload["branch"])
	assert.Equal(t, "loaded-sha", commitPayload["start_sha"])
	_, hasForce := commitPayload["force"]
	assert.False(t, hasForce, "CAS persist must not set force")
	assert.Equal(t, "persist poll state [skip ci]", commitPayload["commit_message"])
}

// TestCommitFileToBranch_NoOpDiffStaleStartSHAIsNonFastForward covers the
// CAS write path's no-op-diff gap: when the merged payload happens to
// byte-for-byte match what's already stored at opts.startSHA's tree,
// commitFilesImpl computes zero actions and, before this fix, returned
// success without ever POSTing — so GitLab's server-side fast-forward
// check (which only runs on an actual commit POST) never fired, and a
// stale start_sha (the branch has since advanced) would be silently
// reported as CAS success. CommitFileToBranch must now re-check the live
// branch tip in that case and surface forge.ErrNonFastForward when it no
// longer matches start_sha.
func TestCommitFileToBranch_NoOpDiffStaleStartSHAIsNonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	content := []byte(`{"n":1}`)
	fileSHA := blobSHA(content)

	// Tree at the (stale) start_sha already matches the payload, so the
	// diff yields zero actions.
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "stale-sha", r.URL.Query().Get("ref"))
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": fileSHA, "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	// The branch has since advanced past stale-sha.
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "new-tip-sha"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("commits endpoint should not be called for a zero-action diff")
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", content, "stale-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward for a stale start_sha on a no-op diff, got: %v", err)
}

// TestCommitFileToBranch_NoOpDiffFreshStartSHASucceeds is the mirror image
// of TestCommitFileToBranch_NoOpDiffStaleStartSHAIsNonFastForward: when the
// live branch tip still matches start_sha, a zero-action diff is a
// legitimate no-op and must succeed without POSTing a commit.
func TestCommitFileToBranch_NoOpDiffFreshStartSHASucceeds(t *testing.T) {
	client, mux := setupTest(t)

	content := []byte(`{"n":1}`)
	fileSHA := blobSHA(content)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": fileSHA, "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "fresh-sha"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("commits endpoint should not be called for a zero-action diff")
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", content, "fresh-sha")
	require.NoError(t, err)
}

func TestCommitFileToBranch_NonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Could not update refs/heads/state-branch. Please refresh and try again.",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist", []byte(`{"n":1}`), "stale-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
}

func TestCommitFileToBranch_AlreadyExistsIsNonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "A branch named 'state-branch' already exists",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
}

// TestCommitFileToBranch_BadRequestAlreadyExistsIsNonFastForward covers a
// genuine conflict disguised as the self-hosted GitLab EE start_sha quirk
// (issue #7892): the commits API 400s with "already exists" for an
// already-known-to-exist branch, but when retryCommitWithoutStartSHA
// re-checks the live tip it finds the branch really has advanced past
// start_sha (a concurrent writer got there first), so this must still
// surface as forge.ErrNonFastForward rather than retrying blindly.
func TestCommitFileToBranch_BadRequestAlreadyExistsIsNonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "concurrent-writer-sha"},
		})
	})

	commitPOSTs := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		commitPOSTs++
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
	assert.Equal(t, 1, commitPOSTs, "a genuine conflict must not retry the commit POST")
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsBranchDeletedIsNonFastForward
// covers retryCommitWithoutStartSHA's branch-disappeared case: the branch
// was known to exist (expectedSHA non-empty) but is gone by the time the
// live tip is re-checked (e.g. deleted concurrently). That must still
// surface as forge.ErrNonFastForward so persistWithCAS reloads and
// retries, rather than treating the lookup failure as unrelated.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsBranchDeletedIsNonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	branchLookupCalled := false
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		branchLookupCalled = true
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "404 Branch Not Found"})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
	assert.True(t, branchLookupCalled, "expected the registered branch-lookup handler to run, not an unmatched-route 404")
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetriesWithoutStartSHA
// is the core fix for issue #7892: self-hosted GitLab EE (observed on
// v19.2.7-ee) unconditionally rejects start_sha on an already-existing
// branch with 400 "already exists", even when start_sha exactly matches
// the branch's current tip. Since expectedSHA is non-empty here, the
// branch was already known to exist, so this 400 can never be a genuine
// create race. CommitFileToBranch must re-check the live tip, find it
// unchanged, and retry the commit once without start_sha instead of
// permanently failing the CAS persist loop.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetriesWithoutStartSHA(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "loaded-sha"},
		})
	})

	var payloads []map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		payloads = append(payloads, payload)

		if len(payloads) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "new-commit"})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "loaded-sha")
	require.NoError(t, err)
	require.Len(t, payloads, 2, "expected an initial attempt and one retry")
	assert.Equal(t, "loaded-sha", payloads[0]["start_sha"], "first attempt must still try start_sha")
	_, hasStartSHA := payloads[1]["start_sha"]
	assert.False(t, hasStartSHA, "retry must omit start_sha, which GitLab rejects for an existing branch")
	assert.Equal(t, "state-branch", payloads[1]["branch"])
	assert.Equal(t, "persist poll state [skip ci]", payloads[1]["commit_message"])
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryGuardsAgainstInterveningWrite
// covers the race retryCommitWithoutStartSHA's own tip check cannot close on
// its own: another poller can commit a change to state.json after the
// branch-tip GET but before the retry POST, since the retry omits start_sha
// entirely. Without a server-enforced guard on the retry itself, that
// intervening write would be silently overwritten (last-writer-wins). The
// retry must carry GitLab's last_commit_id guard on the update action so
// GitLab itself atomically rejects a stale write, and that rejection must
// surface as forge.ErrNonFastForward so persistWithCAS reloads and retries
// instead of reporting a stale write as success.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryGuardsAgainstInterveningWrite(t *testing.T) {
	client, mux := setupTest(t)

	// state.json already exists on the branch, so the retry's action is
	// "update" (not "create") and is eligible for the last_commit_id guard.
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": blobSHA([]byte(`{"n":0}`)), "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	// The branch-tip GET in retryCommitWithoutStartSHA reports loaded-sha,
	// so the naive tip check alone would wrongly conclude nothing has
	// changed. In reality, this models a writer whose commit lands between
	// this GET and the retry POST below — a window the tip check cannot
	// observe. Only the server-enforced last_commit_id guard on the retry
	// POST itself can catch that.
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "loaded-sha"},
		})
	})

	var payloads []map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// guardAgainstInterveningWrite resolves state.json's own
			// last-touching commit as of loaded-sha; here it is
			// loaded-sha itself, so this test still exercises the
			// branch-tip-equals-file-commit case.
			assert.Equal(t, "loaded-sha", r.URL.Query().Get("ref_name"))
			assert.Equal(t, "state.json", r.URL.Query().Get("path"))
			json.NewEncoder(w).Encode([]map[string]any{{"id": "loaded-sha"}})
			return
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		payloads = append(payloads, payload)

		if len(payloads) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
			})
			return
		}
		// GitLab rejects the retry: last_commit_id no longer matches
		// state.json's current last-touching commit because an
		// intervening writer already changed it since loaded-sha.
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "The file has changed since you started editing it: state.json",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
	require.Len(t, payloads, 2, "expected an initial attempt and one rejected retry")

	actions, ok := payloads[1]["actions"].([]any)
	require.True(t, ok, "retry payload must include actions")
	require.Len(t, actions, 1)
	action, ok := actions[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "update", action["action"])
	assert.Equal(t, "loaded-sha", action["last_commit_id"],
		"retry's update action must carry last_commit_id so GitLab can atomically detect the intervening write")
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryResolvesFileCommitNotTip
// covers the api-contract gap in guardAgainstInterveningWrite: GitLab's
// last_commit_id guard is matched against the target file's own
// last-touching commit, not the branch tip. If other files advanced the
// branch past the commit that last touched state.json, sending the tip
// itself as last_commit_id would be a value GitLab never recorded for this
// path and would wrongly reject an uncontended update. The retry must
// resolve and send state.json's actual last-touching commit (older than the
// tip here) and the update must then succeed.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryResolvesFileCommitNotTip(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": blobSHA([]byte(`{"n":0}`)), "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	// The branch tip has advanced past the commit that last touched
	// state.json because another file was committed in between.
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "tip-sha"},
		})
	})

	var payloads []map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			assert.Equal(t, "tip-sha", r.URL.Query().Get("ref_name"))
			assert.Equal(t, "state.json", r.URL.Query().Get("path"))
			json.NewEncoder(w).Encode([]map[string]any{{"id": "file-last-touched-sha"}})
			return
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		payloads = append(payloads, payload)

		if len(payloads) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "new-commit"})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "tip-sha")
	require.NoError(t, err)
	require.Len(t, payloads, 2, "expected an initial attempt and one successful retry")

	actions, ok := payloads[1]["actions"].([]any)
	require.True(t, ok, "retry payload must include actions")
	require.Len(t, actions, 1)
	action, ok := actions[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "update", action["action"])
	assert.Equal(t, "file-last-touched-sha", action["last_commit_id"],
		"retry's update action must carry state.json's own last-touching commit, not the branch tip")
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryConcurrentDeletionIsNonFastForward
// covers the edge-case gap in retryCommitWithoutStartSHA: if state.json
// exists at startSHA (the retained action is "update") but another writer
// deletes it between the branch-tip GET and the retry POST, GitLab rejects
// with a "doesn't exist" message distinct from the "already exists" and
// "file has changed" messages already handled. That must still surface as
// forge.ErrNonFastForward (after confirming the branch actually advanced)
// so persistWithCAS reloads, observes the deletion, and rebuilds the action
// as a create instead of aborting on a generic error.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryConcurrentDeletionIsNonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": blobSHA([]byte(`{"n":0}`)), "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	branchCalls := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		branchCalls++
		// The tip check in retryCommitWithoutStartSHA still sees
		// loaded-sha (the deletion commit hasn't been observed yet on
		// this call); the confirmation check after the rejected retry
		// observes the deletion's commit instead.
		tip := "loaded-sha"
		if branchCalls > 1 {
			tip = "deleter-sha"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": tip},
		})
	})

	var payloads []map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]map[string]any{{"id": "loaded-sha"}})
			return
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		payloads = append(payloads, payload)

		if len(payloads) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
			})
			return
		}
		// Another writer deleted state.json between the tip GET and
		// this retry POST; GitLab rejects the retained "update" action.
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "A file with this name doesn't exist",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
	require.Len(t, payloads, 2, "expected an initial attempt and one rejected retry")
	assert.Equal(t, 2, branchCalls, "expected the tip check plus a confirmation check after the deletion rejection")
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryBranchDeletedDuringRetryIsNonFastForward
// covers the edge-case gap distinct from
// TestCommitFileToBranch_ExistingBranchAlreadyExistsBranchDeletedIsNonFastForward:
// here the branch still exists at the initial tip check inside
// retryCommitWithoutStartSHA (so the retry POST is attempted), but is
// deleted entirely before the retry POST's rejection is classified. The
// confirmation GET after a "doesn't exist" rejection must recognize a
// confirmed forge.ErrNotFound (the branch is gone), not just a tip that has
// merely moved — otherwise persistWithCAS aborts on a generic error instead
// of reloading and retrying branch creation.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryBranchDeletedDuringRetryIsNonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": blobSHA([]byte(`{"n":0}`)), "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	branchCalls := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		branchCalls++
		if branchCalls == 1 {
			// The tip check in retryCommitWithoutStartSHA still observes
			// the branch, matching start_sha, so the retry POST proceeds.
			json.NewEncoder(w).Encode(map[string]any{
				"commit": map[string]any{"id": "loaded-sha"},
			})
			return
		}
		// By the time the retry POST is rejected and this confirmation
		// check runs, the branch itself has been deleted entirely.
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "404 Branch Not Found"})
	})

	var payloads []map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]map[string]any{{"id": "loaded-sha"}})
			return
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		payloads = append(payloads, payload)

		if len(payloads) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
			})
			return
		}
		// The branch was deleted concurrently; GitLab rejects the retry
		// with the same ambiguous "doesn't exist" message used for a
		// deleted file.
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "A file with this name doesn't exist",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
	require.Len(t, payloads, 2, "expected an initial attempt and one rejected retry")
	assert.Equal(t, 2, branchCalls, "expected the tip check plus a confirmation check after the deletion rejection")
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryLastCommitIDMalformedIsError
// covers the fail-open gap in resolveLastCommitForPath: a commits-list
// response whose single entry has a missing, null, or empty "id" must not be
// accepted as a usable last_commit_id guard. Silently sending an empty guard
// would not necessarily make GitLab enforce its intervening-write check, so
// an intervening write could be overwritten undetected.
// guardAgainstInterveningWrite must surface an error instead, and the retry
// POST must never be sent.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryLastCommitIDMalformedIsError(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "missing id field", body: `[{"short_id":"abc1234"}]`},
		{name: "null id", body: `[{"id":null}]`},
		{name: "empty id", body: `[{"id":""}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, mux := setupTest(t)

			mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]map[string]any{
					{"id": blobSHA([]byte(`{"n":0}`)), "path": "state.json", "type": "blob", "mode": "100644"},
				})
			})

			mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{
					"commit": map[string]any{"id": "loaded-sha"},
				})
			})

			commitPOSTs := 0
			mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(tc.body))
					return
				}
				commitPOSTs++
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{
					"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
				})
			})

			err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "loaded-sha")
			require.Error(t, err)
			assert.False(t, forge.IsNonFastForward(err), "a malformed last_commit_id is not itself a CAS conflict")
			assert.Equal(t, 1, commitPOSTs, "the retry POST must not be sent when the last_commit_id is unusable")
		})
	}
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryLastCommitLookupEmptyIsError
// covers resolveLastCommitForPath's defensive empty-result path: if GitLab's
// commits-list endpoint returns no history for state.json as of the tip
// (unexpected, but not ruled out by the API contract), guardAgainstInterveningWrite
// must surface an error rather than silently sending no last_commit_id guard,
// which would let the retry overwrite an intervening write undetected.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryLastCommitLookupEmptyIsError(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": blobSHA([]byte(`{"n":0}`)), "path": "state.json", "type": "blob", "mode": "100644"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "loaded-sha"},
		})
	})

	commitPOSTs := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		commitPOSTs++
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.False(t, forge.IsNonFastForward(err), "an unresolvable last_commit_id lookup is not itself a CAS conflict")
	assert.Equal(t, 1, commitPOSTs, "the retry POST must not be sent when the last_commit_id lookup fails")
}

// TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryCreateConflictIsNonFastForward
// covers the one race guardAgainstInterveningWrite cannot close: when
// state.json does not yet exist on the branch, the retry's action is
// "create", which GitLab exempts from the last_commit_id guard (only
// update/move/delete actions carry it). If another writer creates the file
// between the branch-tip GET in retryCommitWithoutStartSHA and the retry
// POST, GitLab rejects the retry with a 400 "already exists" for the file
// — not the "file has changed" message the update/move/delete guard
// produces. That must still surface as forge.ErrNonFastForward so
// persistWithCAS reloads and retries instead of aborting on a generic
// error.
func TestCommitFileToBranch_ExistingBranchAlreadyExistsRetryCreateConflictIsNonFastForward(t *testing.T) {
	client, mux := setupTest(t)

	// state.json does not exist on the branch yet, so the retry's action
	// is "create" (not "update") and is not eligible for the
	// last_commit_id guard.
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches/state-branch", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"id": "loaded-sha"},
		})
	})

	var payloads []map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		payloads = append(payloads, payload)

		if len(payloads) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "A branch called 'state-branch' already exists. Switch to that branch in order to make changes",
			})
			return
		}
		// GitLab rejects the retry: another writer created state.json
		// between the branch-tip GET and this POST, and the create
		// action carries no last_commit_id to let GitLab's per-file
		// guard catch it instead.
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "A file with this name already exists",
		})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist poll state", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
	require.Len(t, payloads, 2, "expected an initial attempt and one rejected retry")
}

func TestCommitFileToBranch_CommitError(t *testing.T) {
	client, mux := setupTest(t)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"message": "start_sha is invalid"})
	})

	err := client.CommitFileToBranch(context.Background(), "owner", "repo", "state-branch", "state.json", "persist", []byte(`{"n":1}`), "loaded-sha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit state.json to state-branch")
	assert.False(t, forge.IsNonFastForward(err))
}

func TestCommitFileToBranch_RequiredArgs(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	err := client.CommitFileToBranch(ctx, "owner", "repo", "", "f", "m", []byte("x"), "sha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch and path are required")

	err = client.CommitFileToBranch(ctx, "owner", "repo", "b", "", "m", []byte("x"), "sha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch and path are required")
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

func TestGetIssueComment_FoundInClosedIssues(t *testing.T) {
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

	// Note 99 not found on open issue 1.
	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues/1/notes/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"404 Not Found"}`)
	})

	// Note 99 found on closed issue 2.
	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues/2/notes/99", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":         99,
			"body":       "playback-current: 3",
			"created_at": "2026-01-01T00:00:00Z",
			"author":     map[string]string{"username": "fullsend-bot"},
		})
	})

	comment, err := client.GetIssueComment(ctx, "own", "repo", 99)
	require.NoError(t, err)
	assert.Equal(t, 99, comment.ID)
	assert.Equal(t, "playback-current: 3", comment.Body)
	assert.Equal(t, "fullsend-bot", comment.Author)
	assert.Contains(t, comment.HTMLURL, "/-/issues/2#note_99")
}

func TestGetIssueComment_NotFoundAnywhere(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	_, err := client.GetIssueComment(ctx, "own", "repo", 999)
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err), "expected ErrNotFound, got: %v", err)
}

// TestGetIssueComment_ListError guards the scanState branch that surfaces a
// non-404 failure listing noteables (e.g. a transient API error) instead of
// masking it as "not found".
func TestGetIssueComment_ListError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"bad request"}`)
	})

	_, err := client.GetIssueComment(ctx, "own", "repo", 42)
	require.Error(t, err)
	assert.False(t, forge.IsNotFound(err))
	assert.Contains(t, err.Error(), "list opened issues to find note 42")
}

// TestGetIssueComment_ListDecodeError guards scanState's decode-error branch
// when the noteable list page is not valid JSON.
func TestGetIssueComment_ListDecodeError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{not valid json")
	})

	_, err := client.GetIssueComment(ctx, "own", "repo", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode opened issues")
}

// TestGetIssueComment_FetchNoteNonNotFoundError guards scanState's
// early-abort branch: when fetching a candidate note fails with something
// other than "not found" (e.g. a server error), the scan must stop and
// propagate that error rather than continuing to the next candidate/state.
func TestGetIssueComment_FetchNoteNonNotFoundError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"iid": 5}})
	})
	mux.HandleFunc("/api/v4/projects/own%2Frepo/issues/5/notes/42", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"bad request"}`)
	})

	_, err := client.GetIssueComment(ctx, "own", "repo", 42)
	require.Error(t, err)
	assert.False(t, forge.IsNotFound(err))
}

// TestGetIssueComment_MRNoteTarget_FoundInOpen guards the merge_requests
// noteTarget path through scanState's first ("opened") call, which the
// issues-noteTarget tests above don't exercise.
func TestGetIssueComment_MRNoteTarget_FoundInOpen(t *testing.T) {
	client, mux := setupTest(t)
	client.noteTarget = "merge_requests"
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"iid": 3}})
	})
	mux.HandleFunc("/api/v4/projects/own%2Frepo/merge_requests/3/notes/55", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":         55,
			"body":       "playback-current: 1",
			"created_at": "2026-01-01T00:00:00Z",
			"author":     map[string]string{"username": "fullsend-bot"},
		})
	})

	comment, err := client.GetIssueComment(ctx, "own", "repo", 55)
	require.NoError(t, err)
	assert.Equal(t, 55, comment.ID)
	assert.Contains(t, comment.HTMLURL, "/-/merge_requests/3#note_55")
}

// TestGetIssueComment_MRNoteTarget_NotFoundAnywhere guards the
// merge-request-specific "not found" message (as opposed to the
// issue-specific one TestGetIssueComment_NotFoundAnywhere covers).
func TestGetIssueComment_MRNoteTarget_NotFoundAnywhere(t *testing.T) {
	client, mux := setupTest(t)
	client.noteTarget = "merge_requests"
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	_, err := client.GetIssueComment(ctx, "own", "repo", 999)
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err), "expected ErrNotFound, got: %v", err)
	assert.Contains(t, err.Error(), "could not find merge request containing this note")
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

// ---------- WithNoteTarget tests ----------

func TestWithNoteTarget_Default(t *testing.T) {
	c, err := New("tok")
	require.NoError(t, err)
	assert.Equal(t, "issues", c.noteTarget)
}

func TestWithNoteTarget_MergeRequests(t *testing.T) {
	c, err := New("tok", WithNoteTarget("merge_requests"))
	require.NoError(t, err)
	assert.Equal(t, "merge_requests", c.noteTarget)
}

func TestWithNoteTarget_RejectsInvalid(t *testing.T) {
	_, err := New("tok", WithNoteTarget("invalid"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid note target")
}

func TestCreateIssueComment_MRNoteTarget(t *testing.T) {
	client, mux := setupTest(t)
	client.noteTarget = "merge_requests"
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/merge_requests/42/notes", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id":         101,
			"body":       "hello MR",
			"created_at": "2025-01-01T00:00:00Z",
			"author":     map[string]string{"username": "bot"},
		})
	})

	comment, err := client.CreateIssueComment(ctx, "own", "repo", 42, "hello MR")
	require.NoError(t, err)
	assert.Equal(t, 101, comment.ID)
	assert.Contains(t, comment.HTMLURL, "/-/merge_requests/42#note_101")
}

func TestListIssueComments_MRNoteTarget(t *testing.T) {
	client, mux := setupTest(t)
	client.noteTarget = "merge_requests"
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/merge_requests/7/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "body": "note1", "created_at": "2025-01-01T00:00:00Z", "author": map[string]string{"username": "u1"}},
		})
	})

	comments, err := client.ListIssueComments(ctx, "own", "repo", 7)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].HTMLURL, "/-/merge_requests/7#note_1")
}

func TestDeleteIssueComment_MRNoteTarget_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	client.noteTarget = "merge_requests"
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/own%2Frepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	err := client.DeleteIssueComment(ctx, "own", "repo", 999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not find merge request containing this note")
}
