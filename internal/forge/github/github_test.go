package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *LiveClient) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := NewLiveClient("test-token")
	client.baseURL = srv.URL

	return srv, client
}

func TestLiveClient_ListOrgRepos(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Contains(t, r.URL.Path, "/orgs/my-org/repos")

		w.Header().Set("Content-Type", "application/json")

		// Return repos on page 1, empty on page 2+
		if r.URL.Query().Get("page") != "1" {
			_ = json.NewEncoder(w).Encode([]forge.Repository{})
			return
		}

		repos := []forge.Repository{
			{Name: "api", FullName: "my-org/api", DefaultBranch: "main"},
			{Name: "archived", FullName: "my-org/archived", Archived: true},
			{Name: "fork", FullName: "my-org/fork", Fork: true},
			{Name: "web", FullName: "my-org/web", DefaultBranch: "main"},
		}

		_ = json.NewEncoder(w).Encode(repos)
	})

	repos, err := client.ListOrgRepos(context.Background(), "my-org")
	require.NoError(t, err)

	// Should filter out archived and forked repos
	assert.Len(t, repos, 2)
	assert.Equal(t, "api", repos[0].Name)
	assert.Equal(t, "web", repos[1].Name)
}

func TestLiveClient_ListOrgRepos_Pagination(t *testing.T) {
	callCount := 0
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			repos := []forge.Repository{
				{Name: "repo1", FullName: "org/repo1"},
			}
			_ = json.NewEncoder(w).Encode(repos)
		} else {
			// Empty response stops pagination
			_ = json.NewEncoder(w).Encode([]forge.Repository{})
		}
	})

	repos, err := client.ListOrgRepos(context.Background(), "org")
	require.NoError(t, err)
	assert.Len(t, repos, 1)
	assert.Equal(t, 2, callCount, "should paginate until empty response")
}

func TestLiveClient_ListOrgRepos_APIError(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"message": "Must have admin access to organization",
		})
	})

	_, err := client.ListOrgRepos(context.Background(), "org")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "admin access")
}

func TestLiveClient_CreateRepo(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/orgs/my-org/repos", r.URL.Path)

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, ".fullsend", body["name"])
		assert.Equal(t, "config repo", body["description"])
		assert.Equal(t, true, body["auto_init"])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(forge.Repository{
			Name:          ".fullsend",
			FullName:      "my-org/.fullsend",
			DefaultBranch: "main",
		})
	})

	repo, err := client.CreateRepo(context.Background(),
		"my-org", ".fullsend", "config repo", false)
	require.NoError(t, err)
	assert.Equal(t, ".fullsend", repo.Name)
	assert.Equal(t, "my-org/.fullsend", repo.FullName)
}

func TestLiveClient_CreateFile(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/repos/org/repo/contents/config.yaml", r.URL.Path)

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "init config", body["message"])
		assert.NotEmpty(t, body["content"], "content should be base64-encoded")
		// branch should not be set when empty
		_, hasBranch := body["branch"]
		assert.False(t, hasBranch)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"content":{"sha":"abc123"}}`))
	})

	err := client.CreateFile(context.Background(),
		"org", "repo", "config.yaml", "init config", []byte("data"))
	require.NoError(t, err)
}

func TestLiveClient_CreateFileOnBranch(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "feature-branch", body["branch"])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"content":{"sha":"abc123"}}`))
	})

	err := client.CreateFileOnBranch(context.Background(),
		"org", "repo", "feature-branch", "file.txt", "add file", []byte("content"))
	require.NoError(t, err)
}

func TestLiveClient_CreateBranch(t *testing.T) {
	callCount := 0
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		switch callCount {
		case 1:
			// GET repo info for default branch
			assert.Equal(t, "/repos/org/repo", r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"default_branch": "main",
			})
		case 2:
			// GET ref for default branch SHA
			assert.Equal(t, "/repos/org/repo/git/ref/heads/main", r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": "abc123"},
			})
		case 3:
			// POST create ref
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/repos/org/repo/git/refs", r.URL.Path)

			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "refs/heads/fullsend/enroll", body["ref"])
			assert.Equal(t, "abc123", body["sha"])

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ref":"refs/heads/fullsend/enroll"}`))
		}
	})

	err := client.CreateBranch(context.Background(), "org", "repo", "fullsend/enroll")
	require.NoError(t, err)
	assert.Equal(t, 3, callCount)
}

func TestLiveClient_CreateChangeProposal(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repos/org/repo/pulls", r.URL.Path)

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "My PR", body["title"])
		assert.Equal(t, "Description", body["body"])
		assert.Equal(t, "feature", body["head"])
		assert.Equal(t, "main", body["base"])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/org/repo/pull/42",
			"title":    "My PR",
		})
	})

	proposal, err := client.CreateChangeProposal(context.Background(),
		"org", "repo", "My PR", "Description", "feature", "main")
	require.NoError(t, err)
	assert.Equal(t, 42, proposal.Number)
	assert.Equal(t, "https://github.com/org/repo/pull/42", proposal.URL)
}

func TestLiveClient_APIError_WithDetails(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Validation Failed",
			"errors": []map[string]string{
				{"code": "already_exists", "message": "name already exists on this account"},
			},
		})
	})

	_, err := client.CreateRepo(context.Background(), "org", "repo", "desc", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "422")
	assert.Contains(t, err.Error(), "Validation Failed")
	assert.Contains(t, err.Error(), "already_exists")
}

func TestLiveClient_SetsAPIHeaders(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		assert.Equal(t, "2022-11-28", r.Header.Get("X-GitHub-Api-Version"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]forge.Repository{})
	})

	_, err := client.ListOrgRepos(context.Background(), "org")
	require.NoError(t, err)
}

func TestLiveClient_GetFileContent(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/org/repo/contents/config.yaml", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		content := base64.StdEncoding.EncodeToString([]byte("version: 1\n"))
		_, _ = fmt.Fprintf(w, `{"content":"%s","encoding":"base64"}`, content)
	})

	data, err := client.GetFileContent(context.Background(), "org", "repo", "config.yaml")
	require.NoError(t, err)
	assert.Equal(t, "version: 1\n", string(data))
}

func TestLiveClient_DeleteRepo(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/repos/org/repo", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteRepo(context.Background(), "org", "repo")
	require.NoError(t, err)
}

func TestLiveClient_DeleteRepo_NotFound(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
	})

	err := client.DeleteRepo(context.Background(), "org", "repo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestNewLiveClient(t *testing.T) {
	client := NewLiveClient("ghp_abc123")
	assert.Equal(t, "https://api.github.com", client.baseURL)
	assert.Equal(t, "ghp_abc123", client.token)
}
