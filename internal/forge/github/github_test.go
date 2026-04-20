package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient creates a LiveClient pointed at the given httptest server.
func newTestClient(t *testing.T, srv *httptest.Server) *LiveClient {
	t.Helper()
	return New("test-token").WithBaseURL(srv.URL)
}

func TestListOrgRepos(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		assert.Equal(t, "2022-11-28", r.Header.Get("X-GitHub-Api-Version"))

		page++
		if page == 1 {
			// First page: 3 repos (one archived, one fork)
			json.NewEncoder(w).Encode([]map[string]any{
				{"name": "repo1", "full_name": "org/repo1", "default_branch": "main", "private": false, "archived": false, "fork": false},
				{"name": "archived-repo", "full_name": "org/archived-repo", "default_branch": "main", "private": false, "archived": true, "fork": false},
				{"name": "forked-repo", "full_name": "org/forked-repo", "default_branch": "main", "private": false, "archived": false, "fork": true},
			})
		} else {
			// Second page: empty → stops pagination
			json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	repos, err := client.ListOrgRepos(context.Background(), "org")
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "repo1", repos[0].Name)
	assert.Equal(t, "org/repo1", repos[0].FullName)
	assert.Equal(t, "main", repos[0].DefaultBranch)
}

func TestCreateRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/orgs/myorg/repos", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "new-repo", body["name"])
		assert.Equal(t, "A repo", body["description"])
		assert.Equal(t, true, body["private"])
		assert.Equal(t, true, body["auto_init"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"name":           "new-repo",
			"full_name":      "myorg/new-repo",
			"default_branch": "main",
			"private":        true,
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	repo, err := client.CreateRepo(context.Background(), "myorg", "new-repo", "A repo", true)
	require.NoError(t, err)
	assert.Equal(t, "new-repo", repo.Name)
	assert.Equal(t, "myorg/new-repo", repo.FullName)
	assert.True(t, repo.Private)
}

func TestDeleteRepo(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/repos/owner/repo", r.URL.Path)
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.DeleteRepo(context.Background(), "owner", "repo")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestCreateFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/repos/owner/repo/contents/README.md", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "add readme", body["message"])

		// Verify content is base64-encoded
		decoded, err := base64.StdEncoding.DecodeString(body["content"].(string))
		require.NoError(t, err)
		assert.Equal(t, "hello world", string(decoded))

		// Should not have a branch field (empty branch = default)
		_, hasBranch := body["branch"]
		assert.False(t, hasBranch)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateFile(context.Background(), "owner", "repo", "README.md", "add readme", []byte("hello world"))
	require.NoError(t, err)
}

func TestCreateOrUpdateFile_Update(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET existing file
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/contents/existing.txt", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"sha": "abc123",
			})
		case 2:
			// PUT with SHA
			assert.Equal(t, "PUT", r.Method)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "abc123", body["sha"])
			assert.Equal(t, "update file", body["message"])
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "existing.txt", "update file", []byte("updated"))
	require.NoError(t, err)
}

func TestCreateOrUpdateFile_Create(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET returns 404 → file doesn't exist
			assert.Equal(t, "GET", r.Method)
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case 2:
			// PUT without SHA (create)
			assert.Equal(t, "PUT", r.Method)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			_, hasSHA := body["sha"]
			assert.False(t, hasSHA)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "new.txt", "add file", []byte("new content"))
	require.NoError(t, err)
}

func TestGetFileContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/contents/config.yaml", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"content":  base64.StdEncoding.EncodeToString([]byte("key: value")),
			"encoding": "base64",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	data, err := client.GetFileContent(context.Background(), "owner", "repo", "config.yaml")
	require.NoError(t, err)
	assert.Equal(t, "key: value", string(data))
}

func TestCreateBranch(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET repo → default_branch
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"default_branch": "main",
			})
		case 2:
			// GET ref → SHA
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/git/ref/heads/main", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{
					"sha": "deadbeef1234567890",
				},
			})
		case 3:
			// POST create ref
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "/repos/owner/repo/git/refs", r.URL.Path)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "refs/heads/feature-branch", body["ref"])
			assert.Equal(t, "deadbeef1234567890", body["sha"])
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateBranch(context.Background(), "owner", "repo", "feature-branch")
	require.NoError(t, err)
}

func TestCreateChangeProposal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/owner/repo/pulls", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "Fix bug", body["title"])
		assert.Equal(t, "This fixes the bug", body["body"])
		assert.Equal(t, "fix-branch", body["head"])
		assert.Equal(t, "main", body["base"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"html_url": "https://github.com/owner/repo/pull/42",
			"title":    "Fix bug",
			"number":   42,
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	cp, err := client.CreateChangeProposal(context.Background(), "owner", "repo", "Fix bug", "This fixes the bug", "fix-branch", "main")
	require.NoError(t, err)
	assert.Equal(t, 42, cp.Number)
	assert.Equal(t, "Fix bug", cp.Title)
	assert.Equal(t, "https://github.com/owner/repo/pull/42", cp.URL)
}

func TestListRepoPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/repos/owner/repo/pulls")
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		json.NewEncoder(w).Encode([]map[string]any{
			{"html_url": "https://github.com/owner/repo/pull/1", "title": "PR 1", "number": 1},
			{"html_url": "https://github.com/owner/repo/pull/2", "title": "PR 2", "number": 2},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	prs, err := client.ListRepoPullRequests(context.Background(), "owner", "repo")
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, "PR 1", prs[0].Title)
	assert.Equal(t, 2, prs[1].Number)
}

func TestGetAuthenticatedUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/user", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"login": "test-bot",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	user, err := client.GetAuthenticatedUser(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-bot", user)
}

func TestCreateRepoSecret(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET public key
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/actions/secrets/public-key", r.URL.Path)

			// Generate a real NaCl public key for testing
			// Use a fixed key (32 bytes) encoded as base64
			pubKey := make([]byte, 32)
			for i := range pubKey {
				pubKey[i] = byte(i + 1)
			}

			json.NewEncoder(w).Encode(map[string]any{
				"key_id": "key-123",
				"key":    base64.StdEncoding.EncodeToString(pubKey),
			})
		case 2:
			// PUT secret
			assert.Equal(t, "PUT", r.Method)
			assert.Equal(t, "/repos/owner/repo/actions/secrets/MY_SECRET", r.URL.Path)

			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "key-123", body["key_id"])
			assert.NotEmpty(t, body["encrypted_value"])

			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateRepoSecret(context.Background(), "owner", "repo", "MY_SECRET", "super-secret-value")
	require.NoError(t, err)
}

func TestRepoSecretExists(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/actions/secrets/TOKEN", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{"name": "TOKEN"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		exists, err := client.RepoSecretExists(context.Background(), "owner", "repo", "TOKEN")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("not exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		exists, err := client.RepoSecretExists(context.Background(), "owner", "repo", "MISSING")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestCreateOrUpdateRepoVariable_Patch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// PATCH succeeds → variable updated
		assert.Equal(t, "PATCH", r.Method)
		assert.Equal(t, "/repos/owner/repo/actions/variables/MY_VAR", r.URL.Path)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "new-value", body["value"])
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateRepoVariable(context.Background(), "owner", "repo", "MY_VAR", "new-value")
	require.NoError(t, err)
}

func TestCreateOrUpdateRepoVariable_FallbackToPost(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// PATCH returns 404 → variable doesn't exist
			assert.Equal(t, "PATCH", r.Method)
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case 2:
			// POST creates variable
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "/repos/owner/repo/actions/variables", r.URL.Path)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "MY_VAR", body["name"])
			assert.Equal(t, "new-value", body["value"])
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateRepoVariable(context.Background(), "owner", "repo", "MY_VAR", "new-value")
	require.NoError(t, err)
}

func TestGetLatestWorkflowRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/actions/workflows/ci.yml/runs", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("per_page"))

		json.NewEncoder(w).Encode(map[string]any{
			"workflow_runs": []map[string]any{
				{
					"id":         100,
					"name":       "CI",
					"status":     "completed",
					"conclusion": "success",
					"html_url":   "https://github.com/owner/repo/actions/runs/100",
					"created_at": "2024-01-01T00:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	run, err := client.GetLatestWorkflowRun(context.Background(), "owner", "repo", "ci.yml")
	require.NoError(t, err)
	assert.Equal(t, 100, run.ID)
	assert.Equal(t, "CI", run.Name)
	assert.Equal(t, "completed", run.Status)
	assert.Equal(t, "success", run.Conclusion)
	assert.Equal(t, "https://github.com/owner/repo/actions/runs/100", run.HTMLURL)
}

func TestGetLatestWorkflowRun_NoRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"workflow_runs": []map[string]any{},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetLatestWorkflowRun(context.Background(), "owner", "repo", "ci.yml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workflow runs")
}

func TestGetWorkflowRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/actions/runs/42", r.URL.Path)

		json.NewEncoder(w).Encode(map[string]any{
			"id":         42,
			"name":       "Deploy",
			"status":     "in_progress",
			"conclusion": "",
			"html_url":   "https://github.com/owner/repo/actions/runs/42",
			"created_at": "2024-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	run, err := client.GetWorkflowRun(context.Background(), "owner", "repo", 42)
	require.NoError(t, err)
	assert.Equal(t, 42, run.ID)
	assert.Equal(t, "Deploy", run.Name)
	assert.Equal(t, "in_progress", run.Status)
}

func TestListOrgInstallations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/orgs/myorg/installations")
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		json.NewEncoder(w).Encode(map[string]any{
			"installations": []map[string]any{
				{"id": 1, "app_id": 100, "app_slug": "myorg-fullsend"},
				{"id": 2, "app_id": 200, "app_slug": "myorg-triage"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	installs, err := client.ListOrgInstallations(context.Background(), "myorg")
	require.NoError(t, err)
	require.Len(t, installs, 2)
	assert.Equal(t, 1, installs[0].ID)
	assert.Equal(t, "myorg-fullsend", installs[0].AppSlug)
	assert.Equal(t, 200, installs[1].AppID)
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Resource not accessible by integration",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetAuthenticatedUser(context.Background())
	require.Error(t, err)

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Contains(t, apiErr.Message, "Resource not accessible")
}

func TestAPIError_ErrorString(t *testing.T) {
	err := &APIError{
		StatusCode: 404,
		Message:    "Not Found",
	}
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), "Not Found")
}

func TestCreateFileOnBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/repos/owner/repo/contents/path/to/file.txt", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "feature-branch", body["branch"])
		assert.Equal(t, "add file", body["message"])

		decoded, err := base64.StdEncoding.DecodeString(body["content"].(string))
		require.NoError(t, err)
		assert.Equal(t, "file contents", string(decoded))

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateFileOnBranch(context.Background(), "owner", "repo", "feature-branch", "path/to/file.txt", "add file", []byte("file contents"))
	require.NoError(t, err)
}

func TestNew(t *testing.T) {
	client := New("my-token")
	assert.Equal(t, "https://api.github.com", client.baseURL)
	assert.Equal(t, "my-token", client.token)
	assert.NotNil(t, client.http)
}

func TestWithBaseURL(t *testing.T) {
	client := New("token").WithBaseURL("https://custom.api.com/")
	// Trailing slash should be trimmed
	assert.Equal(t, "https://custom.api.com", client.baseURL)
}

func TestCreateOrgSecret(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET org public key
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/orgs/myorg/actions/secrets/public-key", r.URL.Path)

			pubKey := make([]byte, 32)
			for i := range pubKey {
				pubKey[i] = byte(i + 1)
			}

			json.NewEncoder(w).Encode(map[string]any{
				"key_id": "org-key-123",
				"key":    base64.StdEncoding.EncodeToString(pubKey),
			})
		case 2:
			// PUT org secret
			assert.Equal(t, "PUT", r.Method)
			assert.Equal(t, "/orgs/myorg/actions/secrets/DISPATCH_TOKEN", r.URL.Path)

			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "org-key-123", body["key_id"])
			assert.NotEmpty(t, body["encrypted_value"])
			assert.Equal(t, "selected", body["visibility"])

			repoIDs, ok := body["selected_repository_ids"].([]any)
			require.True(t, ok)
			assert.Len(t, repoIDs, 2)
			assert.Equal(t, float64(100), repoIDs[0])
			assert.Equal(t, float64(200), repoIDs[1])

			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrgSecret(context.Background(), "myorg", "DISPATCH_TOKEN", "token-value", []int64{100, 200})
	require.NoError(t, err)
}

func TestCreateOrgSecret_NilRepoIDs_VisibilitySelected(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET org public key
			pubKey := make([]byte, 32)
			for i := range pubKey {
				pubKey[i] = byte(i + 1)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"key_id": "org-key-123",
				"key":    base64.StdEncoding.EncodeToString(pubKey),
			})
		case 2:
			// PUT org secret — should use visibility "selected" with empty repo IDs
			// so that SetOrgSecretRepos can later update access without a 409 Conflict.
			assert.Equal(t, "PUT", r.Method)

			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "selected", body["visibility"],
				"visibility should be 'selected' even when no repo IDs are specified")
			repoIDs, ok := body["selected_repository_ids"].([]any)
			require.True(t, ok, "selected_repository_ids should be an empty array, not nil")
			assert.Empty(t, repoIDs, "selected_repository_ids should be empty")

			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrgSecret(context.Background(), "myorg", "TOKEN", "value", nil)
	require.NoError(t, err)
}

func TestCreateOrgSecret_EmptySliceRepoIDs_VisibilitySelected(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			// GET org public key
			pubKey := make([]byte, 32)
			for i := range pubKey {
				pubKey[i] = byte(i + 1)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"key_id": "org-key-123",
				"key":    base64.StdEncoding.EncodeToString(pubKey),
			})
		case 2:
			// PUT org secret — empty slice should behave the same as nil:
			// visibility "selected" with an empty repo ID array.
			assert.Equal(t, "PUT", r.Method)

			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "selected", body["visibility"],
				"visibility should be 'selected' even with an empty slice")
			repoIDs, ok := body["selected_repository_ids"].([]any)
			require.True(t, ok, "selected_repository_ids should be an empty array, not nil")
			assert.Empty(t, repoIDs, "selected_repository_ids should be empty")

			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrgSecret(context.Background(), "myorg", "TOKEN", "value", []int64{})
	require.NoError(t, err)
}

func TestOrgSecretExists(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/orgs/myorg/actions/secrets/TOKEN", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{"name": "TOKEN"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		exists, err := client.OrgSecretExists(context.Background(), "myorg", "TOKEN")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("not exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		exists, err := client.OrgSecretExists(context.Background(), "myorg", "MISSING")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestDeleteOrgSecret(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			assert.Equal(t, "/orgs/myorg/actions/secrets/TOKEN", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteOrgSecret(context.Background(), "myorg", "TOKEN")
		require.NoError(t, err)
	})

	t.Run("idempotent 404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteOrgSecret(context.Background(), "myorg", "ALREADY_GONE")
		require.NoError(t, err)
	})
}

func TestSetOrgSecretRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/orgs/myorg/actions/secrets/TOKEN/repositories", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		repoIDs, ok := body["selected_repository_ids"].([]any)
		require.True(t, ok)
		assert.Len(t, repoIDs, 3)
		assert.Equal(t, float64(10), repoIDs[0])
		assert.Equal(t, float64(20), repoIDs[1])
		assert.Equal(t, float64(30), repoIDs[2])

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.SetOrgSecretRepos(context.Background(), "myorg", "TOKEN", []int64{10, 20, 30})
	require.NoError(t, err)
}

func TestListOrgRepos_Pagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		switch page {
		case 1:
			// Return 100 repos (full page)
			repos := make([]map[string]any, 100)
			for i := range repos {
				repos[i] = map[string]any{
					"name":           fmt.Sprintf("repo-%d", i),
					"full_name":      fmt.Sprintf("org/repo-%d", i),
					"default_branch": "main",
					"private":        false,
					"archived":       false,
					"fork":           false,
				}
			}
			json.NewEncoder(w).Encode(repos)
		case 2:
			// Return 1 repo (partial page → stops pagination)
			json.NewEncoder(w).Encode([]map[string]any{
				{"name": "repo-100", "full_name": "org/repo-100", "default_branch": "main", "private": false, "archived": false, "fork": false},
			})
		default:
			t.Error("unexpected page request")
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	repos, err := client.ListOrgRepos(context.Background(), "org")
	require.NoError(t, err)
	assert.Len(t, repos, 101)
	assert.Equal(t, 2, page) // Should have made exactly 2 requests
}

func TestDeleteFile(t *testing.T) {
	step := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		switch step {
		case 1:
			// GET to fetch SHA
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/contents/bin/fullsend", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{"sha": "abc123"})
		case 2:
			// DELETE with SHA
			assert.Equal(t, "DELETE", r.Method)
			assert.Equal(t, "/repos/owner/repo/contents/bin/fullsend", r.URL.Path)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "abc123", body["sha"])
			assert.Equal(t, "remove file", body["message"])
			json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"sha": "def456"}})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.DeleteFile(context.Background(), "owner", "repo", "bin/fullsend", "remove file")
	require.NoError(t, err)
	assert.Equal(t, 2, step)
}

func TestDeleteFile_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintln(w, `{"message": "Not Found"}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.DeleteFile(context.Background(), "owner", "repo", "bin/fullsend", "remove file")
	require.NoError(t, err) // idempotent: returns nil when file doesn't exist
}
