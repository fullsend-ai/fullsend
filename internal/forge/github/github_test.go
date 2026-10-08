package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

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

// newTestClient creates a LiveClient pointed at the given httptest server
// with retry delays disabled for fast tests.
func newTestClient(t *testing.T, srv *httptest.Server) *LiveClient {
	t.Helper()
	return New("test-token").WithBaseURL(srv.URL).WithAfterFunc(noWaitAfter)
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
			// First page: 4 repos (one archived, one fork, one private)
			json.NewEncoder(w).Encode([]map[string]any{
				{"name": "repo1", "full_name": "org/repo1", "default_branch": "main", "private": false, "archived": false, "fork": false},
				{"name": "archived-repo", "full_name": "org/archived-repo", "default_branch": "main", "private": false, "archived": true, "fork": false},
				{"name": "forked-repo", "full_name": "org/forked-repo", "default_branch": "main", "private": false, "archived": false, "fork": true},
				{"name": "private-repo", "full_name": "org/private-repo", "default_branch": "main", "private": true, "archived": false, "fork": false},
			})
		} else {
			// Second page: empty → stops pagination
			json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	repos, err := client.ListOrgRepos(context.Background(), "org", false)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "repo1", repos[0].Name)
	assert.Equal(t, "org/repo1", repos[0].FullName)
	assert.Equal(t, "main", repos[0].DefaultBranch)
}

func TestListOrgRepos_IncludePrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"name": "public-repo", "full_name": "org/public-repo", "default_branch": "main", "private": false, "archived": false, "fork": false},
			{"name": "private-repo", "full_name": "org/private-repo", "default_branch": "main", "private": true, "archived": false, "fork": false},
			{"name": "archived-repo", "full_name": "org/archived-repo", "default_branch": "main", "private": false, "archived": true, "fork": false},
			{"name": "forked-repo", "full_name": "org/forked-repo", "default_branch": "main", "private": false, "archived": false, "fork": true},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)

	// includePrivate=false excludes private repos.
	repos, err := client.ListOrgRepos(context.Background(), "org", false)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "public-repo", repos[0].Name)

	// includePrivate=true includes private repos but still excludes archived/fork.
	repos, err = client.ListOrgRepos(context.Background(), "org", true)
	require.NoError(t, err)
	require.Len(t, repos, 2)
	assert.Equal(t, "public-repo", repos[0].Name)
	assert.Equal(t, "private-repo", repos[1].Name)
	assert.True(t, repos[1].Private)
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

func TestDeleteRef(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			assert.Equal(t, "/repos/owner/repo/git/refs/heads/my-branch", r.URL.Path)
			called = true
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteRef(context.Background(), "owner", "repo", "heads/my-branch")
		require.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteRef(context.Background(), "owner", "repo", "heads/gone")
		require.Error(t, err)
		assert.True(t, forge.IsNotFound(err))
	})
}

func TestDeleteBranch(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			assert.Equal(t, "/repos/owner/repo/git/refs/heads/fullsend/scaffold-install", r.URL.Path)
			called = true
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteBranch(context.Background(), "owner", "repo", "fullsend/scaffold-install")
		require.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteBranch(context.Background(), "owner", "repo", "gone")
		require.Error(t, err)
		assert.True(t, forge.IsNotFound(err))
	})
}

func TestFindExistingFork(t *testing.T) {
	t.Run("returns fork owner when fork exists", func(t *testing.T) {
		callNum := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum++
			switch callNum {
			case 1:
				assert.Equal(t, "/user", r.URL.Path)
				json.NewEncoder(w).Encode(map[string]any{"login": "contributor"})
			case 2:
				assert.Equal(t, "/repos/contributor/repo", r.URL.Path)
				json.NewEncoder(w).Encode(map[string]any{
					"fork": true,
					"name": "repo",
					"parent": map[string]any{
						"full_name": "upstream/repo",
					},
				})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkOwner, forkRepo, err := client.FindExistingFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "contributor", forkOwner)
		assert.Equal(t, "repo", forkRepo)
	})

	t.Run("returns fork with renamed repo", func(t *testing.T) {
		callNum := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum++
			switch callNum {
			case 1:
				json.NewEncoder(w).Encode(map[string]any{"login": "contributor"})
			case 2:
				json.NewEncoder(w).Encode(map[string]any{
					"fork": true,
					"name": "repo-1",
					"parent": map[string]any{
						"full_name": "upstream/repo",
					},
				})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkOwner, forkRepo, err := client.FindExistingFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "contributor", forkOwner)
		assert.Equal(t, "repo-1", forkRepo)
	})

	t.Run("returns empty when no fork exists", func(t *testing.T) {
		callNum := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum++
			switch callNum {
			case 1:
				json.NewEncoder(w).Encode(map[string]any{"login": "contributor"})
			case 2:
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkOwner, forkRepo, err := client.FindExistingFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Empty(t, forkOwner)
		assert.Empty(t, forkRepo)
	})

	t.Run("returns empty when repo is not a fork of target", func(t *testing.T) {
		callNum := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum++
			switch callNum {
			case 1:
				json.NewEncoder(w).Encode(map[string]any{"login": "contributor"})
			case 2:
				json.NewEncoder(w).Encode(map[string]any{
					"fork":   false,
					"name":   "repo",
					"parent": nil,
				})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkOwner, forkRepo, err := client.FindExistingFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Empty(t, forkOwner)
		assert.Empty(t, forkRepo)
	})
}

func TestCreateFork(t *testing.T) {
	t.Run("creates fork successfully", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "/repos/upstream/repo/forks", r.URL.Path)
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{
				"name": "repo",
				"owner": map[string]any{
					"login": "contributor",
				},
			})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkOwner, forkRepo, err := client.CreateFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "contributor", forkOwner)
		assert.Equal(t, "repo", forkRepo)
	})

	t.Run("returns renamed fork repo", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{
				"name": "repo-1",
				"owner": map[string]any{
					"login": "contributor",
				},
			})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkOwner, forkRepo, err := client.CreateFork(context.Background(), "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "contributor", forkOwner)
		assert.Equal(t, "repo-1", forkRepo)
	})

	t.Run("returns error on API failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "Repository access blocked",
			})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, _, err := client.CreateFork(context.Background(), "upstream", "repo")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create fork")
	})
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

func TestGetRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/git/ref/tags/v0", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]any{
				"sha":  "abc123def456",
				"type": "commit",
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	sha, err := client.GetRef(context.Background(), "owner", "repo", "tags/v0")
	require.NoError(t, err)
	assert.Equal(t, "abc123def456", sha)
}

func TestGetRef_AnnotatedTag(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/git/ref/tags/v0", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{
					"sha":  "tag-object-sha",
					"type": "tag",
				},
			})
		case 2:
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/git/tags/tag-object-sha", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{
					"sha": "actual-commit-sha",
				},
			})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	sha, err := client.GetRef(context.Background(), "owner", "repo", "tags/v0")
	require.NoError(t, err)
	assert.Equal(t, "actual-commit-sha", sha)
}

func TestGetRef_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Not Found",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetRef(context.Background(), "owner", "repo", "tags/v99")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestGetRef_UnauthenticatedClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		// Unauthenticated client must not send an Authorization header.
		assert.Empty(t, r.Header.Get("Authorization"), "unauthenticated client should not send Authorization header")
		json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]any{
				"sha":  "abc123def456",
				"type": "commit",
			},
		})
	}))
	defer srv.Close()

	client := New("").WithBaseURL(srv.URL)
	sha, err := client.GetRef(context.Background(), "owner", "repo", "tags/v0")
	require.NoError(t, err)
	assert.Equal(t, "abc123def456", sha)
}

func TestGetBranchRef_DelegatesToGetRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/git/ref/heads/main", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]any{
				"sha":  "branch-sha-456",
				"type": "commit",
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	sha, err := client.GetBranchRef(context.Background(), "owner", "repo", "main")
	require.NoError(t, err)
	assert.Equal(t, "branch-sha-456", sha)
}

func TestCompareCommits(t *testing.T) {
	t.Run("returns status from response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/compare/abc123...def456", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"status": "ahead",
			})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		status, err := client.CompareCommits(context.Background(), "owner", "repo", "abc123", "def456")
		require.NoError(t, err)
		assert.Equal(t, "ahead", status)
	})

	t.Run("returns error on API failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintln(w, `{"message":"Not Found"}`)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CompareCommits(context.Background(), "owner", "repo", "abc", "def")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compare commits")
	})
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

func TestCreateBranch_Forbidden(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			json.NewEncoder(w).Encode(map[string]any{
				"default_branch": "main",
			})
		case 2:
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{
					"sha": "deadbeef1234567890",
				},
			})
		case 3:
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "Resource not accessible by integration",
			})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateBranch(context.Background(), "owner", "repo", "feature-branch")
	require.Error(t, err)
	assert.True(t, forge.IsForbidden(err), "CreateBranch 403 should wrap ErrForbidden")
}

func TestCreateBranchFromSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/owner/repo/git/refs", r.URL.Path)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "refs/heads/feature-branch", body["ref"])
		assert.Equal(t, "abc123sha", body["sha"])
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateBranchFromSHA(context.Background(), "owner", "repo", "feature-branch", "abc123sha")
	require.NoError(t, err)
}

func TestCreateBranchFromSHA_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Resource not accessible by integration",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateBranchFromSHA(context.Background(), "owner", "repo", "feature-branch", "abc123sha")
	require.Error(t, err)
	assert.True(t, forge.IsForbidden(err), "CreateBranchFromSHA 403 should wrap ErrForbidden")
}

func TestCreateBranchFromSHA_GenericError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Reference already exists",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateBranchFromSHA(context.Background(), "owner", "repo", "feature-branch", "abc123sha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create branch feature-branch from SHA")
	assert.False(t, forge.IsForbidden(err), "non-403 error should not be ErrForbidden")
}

func TestGetPullRequestInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/pulls/42", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/owner/repo/pull/42",
			"user":     map[string]any{"login": "alice"},
			"head": map[string]any{
				"ref":  "feature",
				"sha":  "deadbeef",
				"repo": map[string]any{"full_name": "owner/repo"},
			},
			"base": map[string]any{
				"ref":  "main",
				"repo": map[string]any{"full_name": "owner/repo"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	info, err := client.GetPullRequestInfo(context.Background(), "owner", "repo", 42)
	require.NoError(t, err)
	assert.Equal(t, 42, info.Number)
	assert.Equal(t, "feature", info.HeadRef)
	assert.False(t, info.IsFork)
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

func TestCreateCrossRepoChangeProposal(t *testing.T) {
	t.Run("same-org fork uses GraphQL", func(t *testing.T) {
		var getRepoCalls []string
		var graphqlCalled bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/"):
				getRepoCalls = append(getRepoCalls, r.URL.Path)
				// Return node_id for the repo.
				repoName := strings.TrimPrefix(r.URL.Path, "/repos/")
				json.NewEncoder(w).Encode(map[string]any{
					"node_id":   "NODE_" + strings.ReplaceAll(repoName, "/", "_"),
					"full_name": repoName,
				})
			case r.Method == "POST" && r.URL.Path == "/graphql":
				graphqlCalled = true
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)

				vars, _ := body["variables"].(map[string]any)
				input, _ := vars["input"].(map[string]any)
				assert.Equal(t, "NODE_org_repo", input["repositoryId"])
				assert.Equal(t, "NODE_org_repo-fork", input["headRepositoryId"])
				assert.Equal(t, "feature-branch", input["headRefName"])
				assert.Equal(t, "main", input["baseRefName"])
				assert.Equal(t, "PR title", input["title"])

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{
						"createPullRequest": map[string]any{
							"pullRequest": map[string]any{
								"number": 99,
								"title":  "PR title",
								"url":    "https://github.com/org/repo/pull/99",
							},
						},
					},
				})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		cp, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "repo", "org", "repo-fork",
			"PR title", "PR body", "feature-branch", "main",
		)
		require.NoError(t, err)
		assert.True(t, graphqlCalled, "should use GraphQL createPullRequest")
		require.Len(t, getRepoCalls, 2, "should fetch node IDs for both repos")
		assert.Equal(t, 99, cp.Number)
		assert.Equal(t, "PR title", cp.Title)
		assert.Equal(t, "https://github.com/org/repo/pull/99", cp.URL)
	})

	t.Run("graphql error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/"):
				json.NewEncoder(w).Encode(map[string]any{
					"node_id": "NODE_test",
				})
			case r.Method == "POST" && r.URL.Path == "/graphql":
				json.NewEncoder(w).Encode(map[string]any{
					"errors": []map[string]any{
						{"message": "head ref must be a branch in the head repository"},
					},
				})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "repo", "org", "repo-fork",
			"title", "body", "branch", "main",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "head ref must be a branch")
	})

	t.Run("base repo not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "missing", "org", "repo-fork",
			"title", "body", "branch", "main",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get repo node ID")
	})

	t.Run("head repo not found", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/") {
				callCount++
				if callCount == 1 {
					// Base repo succeeds.
					json.NewEncoder(w).Encode(map[string]any{
						"node_id": "NODE_base",
					})
					return
				}
				// Head repo fails.
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "repo", "org", "missing-fork",
			"title", "body", "branch", "main",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get repo node ID")
	})

	t.Run("empty node ID", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/") {
				// Return valid JSON but with empty node_id.
				json.NewEncoder(w).Encode(map[string]any{
					"node_id": "",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "repo", "org", "repo-fork",
			"title", "body", "branch", "main",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty node ID")
	})

	t.Run("graphql post error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/"):
				json.NewEncoder(w).Encode(map[string]any{
					"node_id": "NODE_test",
				})
			case r.Method == "POST" && r.URL.Path == "/graphql":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "repo", "org", "repo-fork",
			"title", "body", "branch", "main",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cross-repo pull request via graphql")
	})

	t.Run("graphql decode error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/"):
				json.NewEncoder(w).Encode(map[string]any{
					"node_id": "NODE_test",
				})
			case r.Method == "POST" && r.URL.Path == "/graphql":
				// Return invalid JSON body.
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("not json"))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "repo", "org", "repo-fork",
			"title", "body", "branch", "main",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode cross-repo pull request response")
	})

	t.Run("repo node ID decode error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/") {
				// Return invalid JSON for repo lookup.
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("not json"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateCrossRepoChangeProposal(
			context.Background(),
			"org", "repo", "org", "repo-fork",
			"title", "body", "branch", "main",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode repo node ID")
	})
}

func TestAPIError_FieldCode(t *testing.T) {
	// When a GitHub 422 has empty detail messages but provides field/code,
	// the error string should include them for debuggability.
	err := &APIError{
		StatusCode: 422,
		Message:    "Validation Failed",
		Errors: []APIErrorDetail{
			{Field: "head", Code: "invalid"},
		},
	}
	assert.Contains(t, err.Error(), "field=head")
	assert.Contains(t, err.Error(), "code=invalid")

	// When the detail message is present, it should still use that.
	err2 := &APIError{
		StatusCode: 422,
		Message:    "Validation Failed",
		Errors: []APIErrorDetail{
			{Message: "Branch not found", Field: "head", Code: "invalid"},
		},
	}
	assert.Contains(t, err2.Error(), "Branch not found")
	assert.NotContains(t, err2.Error(), "field=head")
}

func TestCheckStatus_EmptyMessageWithErrors(t *testing.T) {
	// When GitHub returns 422 with an empty top-level message but
	// populated errors, checkStatus should preserve the error details.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "",
			"errors": []map[string]any{
				{"resource": "PullRequestReviewComment", "field": "line", "code": "invalid"},
			},
		})
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)

	csErr := checkStatus(resp, http.StatusOK)
	require.Error(t, csErr)

	var apiErr *APIError
	require.ErrorAs(t, csErr, &apiErr)
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode)
	assert.Equal(t, "Unprocessable Entity", apiErr.Message)
	require.Len(t, apiErr.Errors, 1)
	assert.Equal(t, "PullRequestReviewComment", apiErr.Errors[0].Resource)
	assert.Equal(t, "line", apiErr.Errors[0].Field)
	assert.Equal(t, "invalid", apiErr.Errors[0].Code)
}

func TestCheckStatus_NoMessageNoErrors_UsesRawBody(t *testing.T) {
	// When GitHub returns a non-standard JSON body without a message
	// or errors array, checkStatus should include the raw body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"error":"something unexpected"}`)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)

	csErr := checkStatus(resp, http.StatusOK)
	require.Error(t, csErr)

	var apiErr *APIError
	require.ErrorAs(t, csErr, &apiErr)
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode)
	assert.Contains(t, apiErr.Message, "something unexpected")
}

func TestCheckStatus_NonJSONBody(t *testing.T) {
	// When GitHub returns a non-JSON body, checkStatus should use
	// the raw body as the error message.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "Bad Gateway: upstream timeout")
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)

	csErr := checkStatus(resp, http.StatusOK)
	require.Error(t, csErr)

	var apiErr *APIError
	require.ErrorAs(t, csErr, &apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	assert.Contains(t, apiErr.Message, "Bad Gateway: upstream timeout")
}

func TestCheckStatus_EmptyBody(t *testing.T) {
	// When the response body is empty, fall back to http.StatusText.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)

	csErr := checkStatus(resp, http.StatusOK)
	require.Error(t, csErr)

	var apiErr *APIError
	require.ErrorAs(t, csErr, &apiErr)
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode)
	assert.Equal(t, "Unprocessable Entity", apiErr.Message)
}

func TestCheckStatus_MultiByteTruncation(t *testing.T) {
	// When the raw body contains multi-byte UTF-8 characters and
	// exceeds the truncation limit, the result should not split a
	// character — truncation operates on runes, not bytes.
	// Build a body that is >200 runes, using multi-byte chars.
	body := strings.Repeat("日", 201) // 201 three-byte runes = 603 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)

	csErr := checkStatus(resp, http.StatusOK)
	require.Error(t, csErr)

	var apiErr *APIError
	require.ErrorAs(t, csErr, &apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)

	// Should be exactly 200 runes + "..." — no invalid byte sequences.
	assert.True(t, strings.HasSuffix(apiErr.Message, "..."), "should end with ellipsis")
	// The message without "..." should be exactly 200 runes of "日".
	withoutEllipsis := strings.TrimSuffix(apiErr.Message, "...")
	assert.Equal(t, 200, len([]rune(withoutEllipsis)), "should truncate at 200 runes")
	assert.True(t, utf8.ValidString(apiErr.Message), "truncated message must be valid UTF-8")
}

func TestListRepoPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/repos/owner/repo/pulls")
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		json.NewEncoder(w).Encode([]map[string]any{
			{
				"html_url": "https://github.com/owner/repo/pull/1",
				"title":    "PR 1",
				"number":   1,
				"head":     map[string]any{"ref": "feature-branch", "repo": map[string]any{"full_name": "owner/repo"}},
				"base":     map[string]any{"ref": "main"},
				"user":     map[string]any{"login": "alice"},
			},
			{
				"html_url": "https://github.com/owner/repo/pull/2",
				"title":    "PR 2",
				"number":   2,
				"head":     map[string]any{"ref": "fix-branch", "repo": map[string]any{"full_name": "contributor/repo"}},
				"base":     map[string]any{"ref": "main"},
				"user":     map[string]any{"login": "bob"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	prs, err := client.ListRepoPullRequests(context.Background(), "owner", "repo")
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, "PR 1", prs[0].Title)
	assert.Equal(t, "feature-branch", prs[0].Head)
	assert.Equal(t, "owner/repo", prs[0].HeadRepo)
	assert.Equal(t, "main", prs[0].Base)
	assert.Equal(t, "alice", prs[0].Author)
	assert.Equal(t, 2, prs[1].Number)
	assert.Equal(t, "fix-branch", prs[1].Head)
	assert.Equal(t, "contributor/repo", prs[1].HeadRepo)
	assert.Equal(t, "bob", prs[1].Author)
}

func TestCloseChangeProposal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		assert.Equal(t, "/repos/owner/repo/pulls/42", r.URL.Path)

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "closed", body["state"])

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"number": 42, "state": "closed"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CloseChangeProposal(context.Background(), "owner", "repo", 42)
	require.NoError(t, err)
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

func TestGetAuthenticatedUser_FallbackToApp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			// Simulate GitHub App installation token: /user returns 403.
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "Resource not accessible by integration",
			})
		case "/app":
			json.NewEncoder(w).Encode(map[string]any{
				"slug": "fullsend-ai-review",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	user, err := client.GetAuthenticatedUser(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai-review[bot]", user)
}

func TestGetAuthenticatedUser_FallbackToGraphQL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "Resource not accessible by integration",
			})
		case "/app":
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "A JSON web token could not be decoded",
			})
		case "/graphql":
			assert.Equal(t, http.MethodPost, r.Method)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"viewer": map[string]any{
						"login": "fullsend-e2e[bot]",
					},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	user, err := client.GetAuthenticatedUser(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fullsend-e2e[bot]", user)
}

func TestGraphQLViewerLogin_GraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/graphql", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]string{{"message": "insufficient permissions"}},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.graphqlViewerLogin(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient permissions")
}

func TestGraphQLViewerLogin_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "nope"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.graphqlViewerLogin(context.Background())
	require.Error(t, err)
}

func TestGetAuthenticatedUser_BothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "forbidden",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetAuthenticatedUser(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get authenticated user")
	assert.Contains(t, err.Error(), "graphql fallback")
}

func TestGetAuthenticatedUserIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/user", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"login": "octocat",
			"name":  "The Octocat",
			"email": "octocat@github.com",
			"id":    1,
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	id, err := client.GetAuthenticatedUserIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "The Octocat", id.Name)
	assert.Equal(t, "octocat@github.com", id.Email)
}

func TestGetAuthenticatedUserIdentity_FallbackNameAndEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"login": "octocat",
			"name":  nil,
			"email": nil,
			"id":    42,
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	id, err := client.GetAuthenticatedUserIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "octocat", id.Name, "should fall back to login when name is empty")
	assert.Equal(t, "42+octocat@users.noreply.github.com", id.Email, "should construct noreply email")
}

func TestGetAuthenticatedUserIdentity_AppTokenFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Resource not accessible by integration",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetAuthenticatedUserIdentity(context.Background())
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err), "should wrap ErrNotFound for App tokens")
}

func TestGetAuthenticatedUserIdentity_NonPermissionError_NotErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Bad Request",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetAuthenticatedUserIdentity(context.Background())
	require.Error(t, err)
	assert.False(t, forge.IsNotFound(err), "should NOT wrap ErrNotFound for non-permission errors")
}

func TestGetAuthenticatedUser_AppEmptySlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "forbidden",
			})
		case "/app":
			json.NewEncoder(w).Encode(map[string]any{
				"slug": "",
			})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetAuthenticatedUser(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty slug")
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

func TestGetWorkflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/actions/workflows/repo-maintenance.yml", r.URL.Path)

		json.NewEncoder(w).Encode(map[string]any{
			"id":    42,
			"name":  "Repo Maintenance",
			"path":  ".github/workflows/repo-maintenance.yml",
			"state": "active",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	wf, err := client.GetWorkflow(context.Background(), "owner", "repo", "repo-maintenance.yml")
	require.NoError(t, err)
	assert.Equal(t, 42, wf.ID)
	assert.Equal(t, "Repo Maintenance", wf.Name)
	assert.Equal(t, ".github/workflows/repo-maintenance.yml", wf.Path)
	assert.Equal(t, "active", wf.State)
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
			"event":      "workflow_dispatch",
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
	assert.Equal(t, "workflow_dispatch", run.Event)
	assert.Equal(t, "in_progress", run.Status)
}

func TestListOrgInstallations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/orgs/myorg/installations")
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		json.NewEncoder(w).Encode(map[string]any{
			"installations": []map[string]any{
				{
					"id": 1, "app_id": 100, "app_slug": "myorg-fullsend",
					"app": map[string]any{"owner": map[string]any{"login": "myorg"}},
				},
				{
					"id": 2, "app_id": 200, "app_slug": "myorg-triage",
					"app": map[string]any{"owner": map[string]any{"login": "other-org"}},
				},
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
	assert.Equal(t, "myorg", installs[0].AppOwnerLogin)
	assert.Equal(t, 200, installs[1].AppID)
	assert.Equal(t, "other-org", installs[1].AppOwnerLogin)
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

func TestAPIError_ErrorStringWithDetails(t *testing.T) {
	err := &APIError{
		StatusCode: 422,
		Message:    "Validation Failed",
		Errors: []APIErrorDetail{
			{Resource: "Repository", Field: "name", Code: "custom", Message: "name already exists on this account"},
		},
	}
	assert.Contains(t, err.Error(), "422")
	assert.Contains(t, err.Error(), "Validation Failed")
	assert.Contains(t, err.Error(), "name already exists on this account")
}

func TestIsPATForbiddenError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "classic PAT forbidden by org",
			err: &APIError{
				StatusCode: 403,
				Message:    `"test-org" forbids access via a personal access token (classic). Please use a GitHub App, OAuth App, or a personal access token with fine-grained permissions.`,
			},
			want: true,
		},
		{
			name: "wrapped error",
			err: fmt.Errorf("get repo: %w", &APIError{
				StatusCode: 403,
				Message:    `"test-org" forbids access via a personal access token (classic)`,
			}),
			want: true,
		},
		{
			name: "generic 403",
			err:  &APIError{StatusCode: 403, Message: "Resource not accessible by integration"},
			want: false,
		},
		{
			name: "non-API error",
			err:  fmt.Errorf("network error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPATForbiddenError(tt.err))
		})
	}
}

func TestIsBranchProtectionError(t *testing.T) {
	tests := []struct {
		name   string
		apiErr *APIError
		want   bool
	}{
		{
			name: "protected branch push rejected",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Update is not a fast forward",
				Errors: []APIErrorDetail{
					{Message: "Protected branch update failed for refs/heads/main."},
				},
			},
			want: true,
		},
		{
			name: "required status check failing",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Update is not a fast forward",
				Errors: []APIErrorDetail{
					{Message: "Required status check 'ci-build' is failing"},
				},
			},
			want: true,
		},
		{
			name: "required review",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Message: "Required review from a code owner is not satisfied"},
				},
			},
			want: true,
		},
		{
			name:   "protection in top-level message",
			apiErr: &APIError{StatusCode: 422, Message: "Protected branch 'main' does not allow direct pushes"},
			want:   true,
		},
		{
			name:   "non-fast-forward without protection",
			apiErr: &APIError{StatusCode: 422, Message: "Update is not a fast forward"},
			want:   false,
		},
		{
			name:   "reference already exists",
			apiErr: &APIError{StatusCode: 422, Message: "Reference already exists"},
			want:   false,
		},
		{
			name: "repository ruleset violation",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Update is not a fast forward",
				Errors: []APIErrorDetail{
					{Message: "Repository rule violations found for refs/heads/main."},
				},
			},
			want: true,
		},
		{
			name: "validation failed for unrelated reason",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Resource: "PullRequest", Code: "custom", Message: "No commits between main and main"},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isBranchProtectionError(tt.apiErr))
		})
	}
}

func TestIsNonFastForwardError(t *testing.T) {
	tests := []struct {
		name   string
		apiErr *APIError
		want   bool
	}{
		{
			name:   "not a fast forward (no hyphen)",
			apiErr: &APIError{StatusCode: 422, Message: "Update is not a fast forward"},
			want:   true,
		},
		{
			name: "not a fast-forward in detail (hyphenated)",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Update is not a fast forward",
				Errors:     []APIErrorDetail{{Message: "Cannot update ref: not a fast-forward"}},
			},
			want: true,
		},
		{
			name:   "unrelated 422",
			apiErr: &APIError{StatusCode: 422, Message: "Reference already exists"},
			want:   false,
		},
		{
			name: "overlaps with branch protection (caller checks protection first)",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Update is not a fast forward",
				Errors:     []APIErrorDetail{{Message: "Protected branch update failed for refs/heads/main."}},
			},
			want: true,
		},
		{
			name:   "validation failed",
			apiErr: &APIError{StatusCode: 422, Message: "Validation Failed"},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isNonFastForwardError(tt.apiErr))
		})
	}
}

func TestIsStaleTreeSHAError(t *testing.T) {
	tests := []struct {
		name   string
		apiErr *APIError
		want   bool
	}{
		{
			name:   "tree SHA does not exist in top-level message",
			apiErr: &APIError{StatusCode: 422, Message: "Tree SHA does not exist"},
			want:   true,
		},
		{
			name: "tree SHA does not exist in error detail",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors:     []APIErrorDetail{{Message: "Tree SHA does not exist"}},
			},
			want: true,
		},
		{
			name:   "unrelated 422",
			apiErr: &APIError{StatusCode: 422, Message: "Reference already exists"},
			want:   false,
		},
		{
			name:   "non-fast-forward is not a stale tree SHA",
			apiErr: &APIError{StatusCode: 422, Message: "Update is not a fast forward"},
			want:   false,
		},
		{
			name:   "case insensitive match",
			apiErr: &APIError{StatusCode: 422, Message: "tree sha does not exist"},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isStaleTreeSHAError(tt.apiErr))
		})
	}
}

func TestIsAlreadyExistsError(t *testing.T) {
	tests := []struct {
		name   string
		apiErr *APIError
		want   bool
	}{
		{
			name:   "reference already exists",
			apiErr: &APIError{StatusCode: 422, Message: "Reference already exists"},
			want:   true,
		},
		{
			name: "PR already exists via custom code",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Resource: "PullRequest", Code: "custom", Message: "A pull request already exists for user:branch."},
				},
			},
			want: true,
		},
		{
			name: "repo name already exists on account",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Resource: "Repository", Field: "name", Code: "custom", Message: "name already exists on this account"},
				},
			},
			want: true,
		},
		{
			name:   "non-fast-forward",
			apiErr: &APIError{StatusCode: 422, Message: "Update is not a fast forward"},
			want:   false,
		},
		{
			name: "branch protection",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Update is not a fast forward",
				Errors: []APIErrorDetail{
					{Message: "Protected branch update failed for refs/heads/main."},
				},
			},
			want: false,
		},
		{
			name:   "not found",
			apiErr: &APIError{StatusCode: 404, Message: "Not Found"},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isAlreadyExistsError(tt.apiErr))
		})
	}
}

func TestIsNoChangesError(t *testing.T) {
	tests := []struct {
		name   string
		apiErr *APIError
		want   bool
	}{
		{
			name: "no commits between branches",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Resource: "PullRequest", Code: "custom", Message: "No commits between main and main"},
				},
			},
			want: true,
		},
		{
			name: "no commits between different branches",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Resource: "PullRequest", Code: "custom", Message: "No commits between main and fullsend/scaffold-install"},
				},
			},
			want: true,
		},
		{
			name:   "top-level message only",
			apiErr: &APIError{StatusCode: 422, Message: "No commits between main and fullsend/scaffold-install"},
			want:   true,
		},
		{
			name:   "already exists is not no-changes",
			apiErr: &APIError{StatusCode: 422, Message: "Reference already exists"},
			want:   false,
		},
		{
			name:   "unrelated 422",
			apiErr: &APIError{StatusCode: 422, Message: "Update is not a fast forward"},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isNoChangesError(tt.apiErr))
		})
	}
}

func TestAPIError_Unwrap(t *testing.T) {
	tests := []struct {
		name    string
		apiErr  *APIError
		wantErr error
		wantNil bool
	}{
		{
			name:    "404 unwraps to ErrNotFound",
			apiErr:  &APIError{StatusCode: 404, Message: "Not Found"},
			wantErr: forge.ErrNotFound,
		},
		{
			name:    "422 reference already exists unwraps to ErrAlreadyExists",
			apiErr:  &APIError{StatusCode: 422, Message: "Reference already exists"},
			wantErr: forge.ErrAlreadyExists,
		},
		{
			name: "422 PR already exists unwraps to ErrAlreadyExists",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Resource: "PullRequest", Code: "custom", Message: "A pull request already exists for user:branch."},
				},
			},
			wantErr: forge.ErrAlreadyExists,
		},
		{
			name: "422 no commits between unwraps to ErrNoChanges",
			apiErr: &APIError{
				StatusCode: 422,
				Message:    "Validation Failed",
				Errors: []APIErrorDetail{
					{Resource: "PullRequest", Code: "custom", Message: "No commits between main and fullsend/scaffold-install"},
				},
			},
			wantErr: forge.ErrNoChanges,
		},
		{
			name:    "422 non-fast-forward does not unwrap",
			apiErr:  &APIError{StatusCode: 422, Message: "Update is not a fast forward"},
			wantNil: true,
		},
		{
			name:    "403 does not unwrap (context-dependent)",
			apiErr:  &APIError{StatusCode: 403, Message: "Resource not accessible by integration"},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.apiErr.Unwrap()
			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				assert.ErrorIs(t, got, tt.wantErr)
			}
		})
	}
}

func TestSecondaryRateLimit_RetriedWithoutRetryAfterHeader(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "You have exceeded a secondary rate limit",
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"name":           "test-repo",
			"full_name":      "org/test-repo",
			"default_branch": "main",
			"private":        false,
		})
	}))
	defer srv.Close()

	client := &LiveClient{
		token:     "test-token",
		baseURL:   srv.URL,
		http:      srv.Client(),
		afterFunc: noWaitAfter,
	}

	// Override the backoff for testing — we don't want to wait 60s.
	origBackoff := secondaryRateLimitBackoff
	defer func() { secondaryRateLimitBackoff = origBackoff }()
	secondaryRateLimitBackoff = 10 * time.Millisecond

	repo, err := client.CreateRepo(context.Background(), "org", "test-repo", "desc", false)
	require.NoError(t, err)
	assert.Equal(t, "test-repo", repo.Name)
	assert.Equal(t, 3, attempts, "should have retried twice before succeeding")
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

func TestCreateOrUpdateOrgVariableAll_Create(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch callNum {
		case 1:
			assert.Equal(t, "PATCH", r.Method)
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case 2:
			assert.Equal(t, "POST", r.Method)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "FULLSEND_FOREIGN_E2E_REPOS", body["name"])
			assert.Equal(t, "fullsend-ai/fullsend", body["value"])
			assert.Equal(t, "all", body["visibility"])
			_, hasRepoIDs := body["selected_repository_ids"]
			assert.False(t, hasRepoIDs)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateOrgVariableAll(context.Background(), "myorg", "FULLSEND_FOREIGN_E2E_REPOS", "fullsend-ai/fullsend")
	require.NoError(t, err)
}

func TestCreateOrUpdateOrgVariableAll_Update(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "fullsend-ai/fullsend", body["value"])
		assert.Equal(t, "all", body["visibility"])
		_, hasRepoIDs := body["selected_repository_ids"]
		assert.False(t, hasRepoIDs)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateOrgVariableAll(context.Background(), "myorg", "FULLSEND_FOREIGN_E2E_REPOS", "fullsend-ai/fullsend")
	require.NoError(t, err)
}

func TestGetOrgVariable(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/orgs/myorg/actions/variables/DISPATCH_URL", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{"name": "DISPATCH_URL", "value": "https://func.example.com"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		value, exists, err := client.GetOrgVariable(context.Background(), "myorg", "DISPATCH_URL")
		require.NoError(t, err)
		assert.True(t, exists)
		assert.Equal(t, "https://func.example.com", value)
	})

	t.Run("not exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		value, exists, err := client.GetOrgVariable(context.Background(), "myorg", "MISSING")
		require.NoError(t, err)
		assert.False(t, exists)
		assert.Empty(t, value)
	})
}

func TestDeleteOrgVariable(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			assert.Equal(t, "/orgs/myorg/actions/variables/DISPATCH_URL", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteOrgVariable(context.Background(), "myorg", "DISPATCH_URL")
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
		err := client.DeleteOrgVariable(context.Background(), "myorg", "ALREADY_GONE")
		require.NoError(t, err)
	})
}

func TestDeleteRepoVariable(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			assert.Equal(t, "/repos/myorg/myrepo/actions/variables/FULLSEND_PER_REPO_INSTALL", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteRepoVariable(context.Background(), "myorg", "myrepo", "FULLSEND_PER_REPO_INSTALL")
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
		err := client.DeleteRepoVariable(context.Background(), "myorg", "myrepo", "ALREADY_GONE")
		require.NoError(t, err)
	})

	t.Run("unexpected status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteRepoVariable(context.Background(), "myorg", "myrepo", "VAR")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status")
	})
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
	repos, err := client.ListOrgRepos(context.Background(), "org", false)
	require.NoError(t, err)
	assert.Len(t, repos, 101)
	assert.Equal(t, 2, page) // Should have made exactly 2 requests
}

func TestCreateOrUpdateFile_RetriesOn504(t *testing.T) {
	// 5xx is now retried at the do() level, so the PUT is retried
	// internally without re-running the GET.
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch {
		case callNum == 1:
			// GET for existing file — return 404 (file doesn't exist)
			assert.Equal(t, "GET", r.Method)
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case callNum == 2:
			// PUT — return 504 Gateway Timeout (do() will retry)
			assert.Equal(t, "PUT", r.Method)
			w.WriteHeader(http.StatusGatewayTimeout)
			json.NewEncoder(w).Encode(map[string]any{"message": "Gateway Timeout"})
		case callNum == 3:
			// do() retry: PUT — succeeds
			assert.Equal(t, "PUT", r.Method)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Errorf("unexpected call %d", callNum)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "test.txt", "add file", []byte("content"))
	require.NoError(t, err)
	assert.Equal(t, 3, callNum, "expected exactly 3 calls (GET, PUT fail, PUT retry succeed)")
}

func TestCreateOrUpdateFile_RetriesOnAll5xxCodes(t *testing.T) {
	// 5xx is retried at the do() level. The PUT fails once, do() retries,
	// and succeeds — without re-running the GET.
	for _, statusCode := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(fmt.Sprintf("status_%d", statusCode), func(t *testing.T) {
			callNum := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callNum++
				switch {
				case callNum == 1:
					// GET existing file — 404
					w.WriteHeader(http.StatusNotFound)
					json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
				case callNum == 2:
					// PUT — return 5xx (do() will retry)
					w.WriteHeader(statusCode)
					json.NewEncoder(w).Encode(map[string]any{"message": http.StatusText(statusCode)})
				case callNum == 3:
					// do() retry: PUT — succeeds
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]any{})
				}
			}))
			defer srv.Close()

			client := newTestClient(t, srv)
			err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "test.txt", "add", []byte("data"))
			require.NoError(t, err)
			assert.Equal(t, 3, callNum, "expected 3 calls (GET, PUT fail, PUT retry succeed) for %d", statusCode)
		})
	}
}

func TestCreateOrUpdateFile_NoRetryOnNon5xx(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		switch {
		case callNum == 1:
			// GET existing file — 404
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case callNum == 2:
			// PUT — return 422 Unprocessable Entity (not retryable)
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{"message": "Validation Failed"})
		default:
			t.Errorf("unexpected call %d — should not have retried", callNum)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "test.txt", "add", []byte("data"))
	require.Error(t, err)
	assert.Equal(t, 2, callNum, "should not retry on 422")
}

func TestCreateOrUpdateFile_MaxRetriesExceeded(t *testing.T) {
	// 5xx errors are retried at the do() level, not retryOnRepoRace.
	// With a persistent 504 on PUT, do() exhausts its 5 attempts and
	// returns immediately — retryOnRepoRace does not retry 5xx.
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		if r.Method == "GET" {
			// Always return 404 for the GET (file doesn't exist)
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
			return
		}
		// PUT always returns 504
		w.WriteHeader(http.StatusGatewayTimeout)
		json.NewEncoder(w).Encode(map[string]any{"message": "Gateway Timeout"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.CreateOrUpdateFile(context.Background(), "owner", "repo", "test.txt", "add", []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retryable error after 5 attempts")
}

func TestIsTransientStatus(t *testing.T) {
	// After moving 5xx retry to isRetryable in do(), isTransientStatus
	// only covers race-condition statuses (404 async repo init, 409 ref conflict).
	transient := []int{404, 409}
	for _, code := range transient {
		assert.True(t, isTransientStatus(code), "expected %d to be transient", code)
	}

	nonTransient := []int{200, 201, 400, 401, 403, 422, 500, 502, 503, 504}
	for _, code := range nonTransient {
		assert.False(t, isTransientStatus(code), "expected %d to not be transient", code)
	}
}

func TestAPIError_IsTransient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code int
		want bool
	}{
		{name: "429 rate limit", code: 429, want: true},
		{name: "500 internal server error", code: 500, want: true},
		{name: "502 bad gateway", code: 502, want: true},
		{name: "503 service unavailable", code: 503, want: true},
		{name: "504 gateway timeout", code: 504, want: true},
		{name: "200 OK", code: 200, want: false},
		{name: "401 unauthorized", code: 401, want: false},
		{name: "403 forbidden", code: 403, want: false},
		{name: "404 not found", code: 404, want: false},
		{name: "422 unprocessable entity", code: 422, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &APIError{StatusCode: tt.code, Message: http.StatusText(tt.code)}
			assert.Equal(t, tt.want, err.IsTransient())
		})
	}
}

func TestIsRetryable_PrimaryRateLimitAs403(t *testing.T) {
	// GitHub sometimes returns primary rate limits as 403 with body
	// containing "API rate limit exceeded" instead of 429. This must
	// be detected as retryable.
	body := `{"message":"API rate limit exceeded for user ID 12345."}`
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	retryable, _ := isRetryable(resp)
	assert.True(t, retryable, "403 with 'API rate limit exceeded' should be retryable")
}

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "primary rate limit as 403",
			err:      &APIError{StatusCode: 403, Message: "API rate limit exceeded for user ID 12345"},
			expected: true,
		},
		{
			name:     "secondary rate limit as 403",
			err:      &APIError{StatusCode: 403, Message: "You have exceeded a secondary rate limit"},
			expected: true,
		},
		{
			name:     "429 too many requests",
			err:      &APIError{StatusCode: 429, Message: "rate limit exceeded"},
			expected: true,
		},
		{
			name:     "403 not rate limit",
			err:      &APIError{StatusCode: 403, Message: "Resource not accessible by integration"},
			expected: false,
		},
		{
			name:     "wrapped rate limit",
			err:      fmt.Errorf("create repo: %w", &APIError{StatusCode: 403, Message: "API rate limit exceeded"}),
			expected: true,
		},
		{
			name:     "non-API error",
			err:      fmt.Errorf("network error"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsRateLimitError(tt.err))
		})
	}
}

func TestIsRetryable_403NotRateLimit(t *testing.T) {
	// A 403 that is NOT a rate limit (e.g. insufficient permissions)
	// should not be retryable.
	body := `{"message":"Resource not accessible by integration"}`
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	retryable, returnedBody := isRetryable(resp)
	assert.False(t, retryable, "403 without rate limit text should not be retryable")
	assert.NotNil(t, returnedBody, "body should be returned for non-rate-limit 403")
}

func TestIsRetryable_ServerErrors(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		resp := &http.Response{
			StatusCode: code,
			Body:       http.NoBody,
		}
		retryable, _ := isRetryable(resp)
		assert.True(t, retryable, "expected %d to be retryable", code)
	}
}

func TestClientTimeoutIs60s(t *testing.T) {
	c := New("test-token")
	assert.Equal(t, 60*time.Second, c.http.Timeout)
}

func TestDoRetriesOnTimeout(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			// Sleep longer than the client timeout to trigger a timeout error.
			time.Sleep(200 * time.Millisecond)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	client.http.Timeout = 50 * time.Millisecond

	resp, err := client.do(context.Background(), http.MethodGet, "/test", nil)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, int32(3), attempts.Load(), "expected 3 attempts (2 timeouts + 1 success)")
}

func TestDoRetriesOnTimeout_Exhausted(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// Always sleep longer than the client timeout.
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	client.http.Timeout = 50 * time.Millisecond

	_, err := client.do(context.Background(), http.MethodGet, "/test", nil)
	require.Error(t, err)
	assert.Equal(t, int32(maxRetries), attempts.Load(), "expected all retry attempts to be used")
	assert.Contains(t, err.Error(), "after 5 attempts")
}

func TestDoDoesNotRetryOnCallerContextCancel(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// Sleep longer than the client timeout.
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	client.http.Timeout = 50 * time.Millisecond

	// Use a context that will be cancelled before the client timeout fires.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := client.do(ctx, http.MethodGet, "/test", nil)
	require.Error(t, err)
	// Should not retry — the caller's context was cancelled.
	assert.Equal(t, int32(1), attempts.Load(), "should not retry when caller context is cancelled")
}

func TestIsTimeoutError(t *testing.T) {
	// Create an already-cancelled context for testing the context guard.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "nil error",
			ctx:  context.Background(),
			err:  nil,
			want: false,
		},
		{
			name: "generic error",
			ctx:  context.Background(),
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
		{
			name: "context.DeadlineExceeded with active context",
			ctx:  context.Background(),
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "context.DeadlineExceeded with cancelled context",
			ctx:  cancelledCtx,
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "wrapped context.DeadlineExceeded with cancelled context",
			ctx:  cancelledCtx,
			err:  fmt.Errorf("request failed: %w", context.DeadlineExceeded),
			want: false,
		},
		{
			name: "context.Canceled with active context",
			ctx:  context.Background(),
			err:  context.Canceled,
			want: false,
		},
		{
			name: "context.Canceled with cancelled context",
			ctx:  cancelledCtx,
			err:  context.Canceled,
			want: false,
		},
		{
			name: "timeout error with cancelled context returns false",
			ctx:  cancelledCtx,
			err:  context.DeadlineExceeded,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTimeoutError(tt.ctx, tt.err))
		})
	}
}

func TestDo_RetriesOnServerError(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintln(w, `{"message":"Bad Gateway"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.get(context.Background(), "/test")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 2, attempt, "expected exactly 2 attempts (1 retry)")
}

func TestDo_MaxRetries5(t *testing.T) {
	// do() should attempt up to 5 times before giving up on retryable errors.
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintln(w, `{"message":"Bad Gateway"}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.get(context.Background(), "/test")
	require.Error(t, err)
	assert.Equal(t, 5, attempt, "expected 5 attempts total")
	assert.Contains(t, err.Error(), "retryable error after 5 attempts")
}

func TestRetryDelay_HasJitter(t *testing.T) {
	// retryDelay should add jitter so that repeated calls with the same
	// inputs produce varying delays, preventing thundering-herd effects.
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{},
	}

	seen := make(map[time.Duration]bool)
	for range 50 {
		d := retryDelay(resp, 2) // attempt 2 → base 4s
		seen[d] = true
	}
	assert.Greater(t, len(seen), 1, "retryDelay should produce varying results due to jitter")
}

func TestRetryDelay_SecondaryRateLimit_HasJitter(t *testing.T) {
	origBackoff := secondaryRateLimitBackoff
	defer func() { secondaryRateLimitBackoff = origBackoff }()
	secondaryRateLimitBackoff = 100 * time.Millisecond

	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
	}

	seen := make(map[time.Duration]bool)
	for range 50 {
		d := retryDelay(resp, 1)
		seen[d] = true
	}
	assert.Greater(t, len(seen), 1, "secondary rate limit retryDelay should have jitter")
}

func TestRetryDelay_RespectsRetryAfterHeader(t *testing.T) {
	// When Retry-After header is present, jitter should NOT apply —
	// the server told us exactly how long to wait.
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"30"}},
	}

	for range 10 {
		d := retryDelay(resp, 0)
		assert.Equal(t, 30*time.Second, d, "Retry-After should be used exactly, no jitter")
	}
}

func TestBlobSHA(t *testing.T) {
	// printf "blob 5\0hello" | sha1sum
	got := blobSHA([]byte("hello"))
	assert.Equal(t, "b6fc4c620b67d95f953a5c1c1230aaab5db5a1b0", got)

	// echo -n "" | git hash-object --stdin
	got = blobSHA([]byte{})
	assert.Equal(t, "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391", got)
}

func TestBlobSHAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))
	got, err := blobSHAFile(path)
	require.NoError(t, err)
	assert.Equal(t, blobSHA([]byte("hello")), got)

	_, err = blobSHAFile(filepath.Join(dir, "missing"))
	require.Error(t, err)
}

func TestCommitFiles_AllNew(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)

		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": "abc123"},
			})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{
				"tree": map[string]string{"sha": "tree000"},
			})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{
				"tree":      []any{},
				"truncated": false,
			})

		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "tree000", body["base_tree"])
			entries := body["tree"].([]any)
			assert.Len(t, entries, 2)
			for _, raw := range entries {
				entry := raw.(map[string]any)
				assert.NotContains(t, entry, "encoding")
				assert.IsType(t, "", entry["content"])
			}

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})

		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "newtree", body["tree"])
			assert.Equal(t, []any{"abc123"}, body["parents"])

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})

		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "newcommit", body["sha"])
			json.NewEncoder(w).Encode(map[string]any{})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	files := []forge.TreeFile{
		{Path: "file1.txt", Content: []byte("content1"), Mode: "100644"},
		{Path: "scripts/run.sh", Content: []byte("#!/bin/bash"), Mode: "100755"},
	}
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "test commit", files)
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_BinaryUsesBlobAPI(t *testing.T) {
	binaryContent := []byte{0x7f, 0x45, 0x4c, 0x46, 0xff, 0xfe, 0x00}
	blobSHAValue := blobSHA(binaryContent)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/blobs":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "base64", body["encoding"])
			decoded, err := base64.StdEncoding.DecodeString(body["content"])
			require.NoError(t, err)
			assert.Equal(t, binaryContent, decoded)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": blobSHAValue})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			entries := body["tree"].([]any)
			require.Len(t, entries, 1)
			entry := entries[0].(map[string]any)
			assert.Equal(t, blobSHAValue, entry["sha"])
			assert.NotContains(t, entry, "content")
			assert.NotContains(t, entry, "encoding")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "vendor binary", []forge.TreeFile{
		{Path: "bin/fullsend", Content: binaryContent, Mode: "100755"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_LocalPathUsesBlobAPI(t *testing.T) {
	binaryContent := []byte{0x7f, 0x45, 0x4c, 0x46, 0xff, 0xfe, 0x00}
	blobSHAValue := blobSHA(binaryContent)
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fullsend")
	require.NoError(t, os.WriteFile(binPath, binaryContent, 0o755))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/blobs":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode blob body: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			assert.Equal(t, "base64", body["encoding"])
			decoded, err := base64.StdEncoding.DecodeString(body["content"])
			if err != nil {
				t.Errorf("decode blob content: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			assert.Equal(t, binaryContent, decoded)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": blobSHAValue})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode tree body: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			entries, _ := body["tree"].([]any)
			if len(entries) != 1 {
				t.Errorf("expected 1 tree entry, got %d", len(entries))
				http.Error(w, "unexpected tree entries", http.StatusBadRequest)
				return
			}
			entry, _ := entries[0].(map[string]any)
			assert.Equal(t, blobSHAValue, entry["sha"])
			assert.NotContains(t, entry, "content")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "vendor binary", []forge.TreeFile{
		{Path: "bin/fullsend", LocalPath: binPath, Mode: "100755"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_LocalPathUnchanged(t *testing.T) {
	content := []byte{0x7f, 0x45, 0x4c, 0x46, 0x00}
	existingSHA := blobSHA(content)
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fullsend")
	require.NoError(t, os.WriteFile(binPath, content, 0o755))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{
				"tree": []map[string]string{
					{"path": "bin/fullsend", "mode": "100755", "sha": existingSHA},
				},
				"truncated": false,
			})
		default:
			t.Errorf("unexpected request: %s %s (should not create blob/tree/commit)", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "no-op", []forge.TreeFile{
		{Path: "bin/fullsend", LocalPath: binPath, Mode: "100755"},
	})
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommitFiles_LocalPathMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "bin/fullsend", LocalPath: filepath.Join(t.TempDir(), "missing"), Mode: "100755"},
	})
	require.Error(t, err)
}

func TestCommitFiles_LocalPathBlobError(t *testing.T) {
	content := []byte{0x7f, 0x45, 0x4c, 0x46, 0xff}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fullsend")
	require.NoError(t, os.WriteFile(binPath, content, 0o755))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/blobs":
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"message": "blob failed"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CommitFiles(context.Background(), "org", "repo", "vendor binary", []forge.TreeFile{
		{Path: "bin/fullsend", LocalPath: binPath, Mode: "100755"},
	})
	require.Error(t, err)
}

func TestCommitFiles_LocalPathBlobDecodeError(t *testing.T) {
	content := []byte{0x7f, 0x45, 0x4c, 0x46, 0xff}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fullsend")
	require.NoError(t, os.WriteFile(binPath, content, 0o755))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/blobs":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte("not-json"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CommitFiles(context.Background(), "org", "repo", "vendor binary", []forge.TreeFile{
		{Path: "bin/fullsend", LocalPath: binPath, Mode: "100755"},
	})
	require.Error(t, err)
}

func TestCommitFiles_AllUnchanged(t *testing.T) {
	content := []byte("existing content")
	existingSHA := blobSHA(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": "abc123"},
			})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{
				"tree": map[string]string{"sha": "tree000"},
			})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{
				"tree": []map[string]string{
					{"path": "file.txt", "mode": "100644", "sha": existingSHA},
				},
				"truncated": false,
			})

		default:
			t.Errorf("unexpected request: %s %s (should not create tree/commit)", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	files := []forge.TreeFile{
		{Path: "file.txt", Content: content, Mode: "100644"},
	}
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "no-op", files)
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommitFiles_ModeChange(t *testing.T) {
	content := []byte("#!/bin/bash\necho hello")
	existingSHA := blobSHA(content)

	var treeCreated bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": "abc123"},
			})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{
				"tree": map[string]string{"sha": "tree000"},
			})

		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{
				"tree": []map[string]string{
					{"path": "scripts/run.sh", "mode": "100644", "sha": existingSHA},
				},
				"truncated": false,
			})

		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			treeCreated = true
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			entries := body["tree"].([]any)
			require.Len(t, entries, 1)
			entry := entries[0].(map[string]any)
			assert.Equal(t, "100755", entry["mode"])

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})

		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})

		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	files := []forge.TreeFile{
		{Path: "scripts/run.sh", Content: content, Mode: "100755"},
	}
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "fix modes", files)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.True(t, treeCreated, "should create tree for mode change")
}

func TestCommitFiles_Empty(t *testing.T) {
	client := New("token")
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "msg", nil)
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommitFiles_RetriesTransientGetCommit404(t *testing.T) {
	const failCount = 2
	commitGets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": "abc123"},
			})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			commitGets++
			if commitGets <= failCount {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"tree": map[string]string{"sha": "tree000"},
			})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{
				"tree":      []any{},
				"truncated": false,
			})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, failCount+1, commitGets, "get commit should retry the transient 404s then succeed")
}

func TestCommitFiles_NoRetryOnNonTransientGetCommitError(t *testing.T) {
	commitGets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": "abc123"},
			})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			commitGets++
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"message": "Validation Failed"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get commit")
	assert.Equal(t, 1, commitGets, "non-transient get commit errors must not retry")
}

func TestListRepositoryFiles_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit-sha"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/commit-sha":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree-sha"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/tree-sha"):
			assert.Contains(t, r.URL.RawQuery, "recursive=1")
			json.NewEncoder(w).Encode(map[string]any{
				"tree": []map[string]string{
					{"path": "cmd/main.go", "type": "blob"},
					{"path": "internal", "type": "tree"},
					{"path": "internal/handler.go", "type": "blob"},
					{"path": "README.md", "type": "blob"},
				},
				"truncated": false,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	paths, err := client.ListRepositoryFiles(context.Background(), "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, []string{"cmd/main.go", "internal/handler.go", "README.md"}, paths)
}

func TestListRepositoryFiles_Truncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit-sha"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/commit-sha":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree-sha"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/tree-sha"):
			json.NewEncoder(w).Encode(map[string]any{
				"tree":      []map[string]string{{"path": "a.go", "type": "blob"}},
				"truncated": true,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListRepositoryFiles(context.Background(), "org", "repo")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrTreeTruncated)
}

func TestListRepositoryFiles_RepoNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListRepositoryFiles(context.Background(), "org", "missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestListRepositoryFiles_EmptyRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/empty":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/empty/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit-sha"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/empty/git/commits/commit-sha":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree-sha"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/empty/git/trees/tree-sha"):
			json.NewEncoder(w).Encode(map[string]any{
				"tree":      []map[string]string{},
				"truncated": false,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	paths, err := client.ListRepositoryFiles(context.Background(), "org", "empty")
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestListRepositoryFiles_RefError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListRepositoryFiles(context.Background(), "org", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get branch ref")
}

func TestListRepositoryFiles_CommitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit-sha"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/commit-sha":
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "Internal Server Error"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListRepositoryFiles(context.Background(), "org", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get commit")
}

func TestListRepositoryFiles_TreeFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit-sha"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/commit-sha":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree-sha"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/tree-sha"):
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "Internal Server Error"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListRepositoryFiles(context.Background(), "org", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get tree")
}

func TestDeleteFiles_Empty(t *testing.T) {
	client := New("token")
	deleted, err := client.DeleteFiles(context.Background(), "org", "repo", "msg", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}

func TestDeleteFiles_Atomic(t *testing.T) {
	var treeCreated bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/commit":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/tree"):
			json.NewEncoder(w).Encode(map[string]any{
				"tree": []map[string]string{
					{"path": "bin/fullsend", "sha": "abc", "mode": "100755"},
					{"path": ".defaults/action.yml", "sha": "def", "mode": "100644"},
				},
				"truncated": false,
			})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			treeCreated = true
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			entries := body["tree"].([]any)
			require.Len(t, entries, 2)
			for _, raw := range entries {
				entry := raw.(map[string]any)
				assert.Equal(t, "blob", entry["type"])
				assert.NotEmpty(t, entry["mode"])
				assert.Nil(t, entry["sha"])
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	deleted, err := client.DeleteFiles(context.Background(), "org", "repo", "remove stale", []string{
		"bin/fullsend",
		".defaults/action.yml",
		"missing.yml",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	assert.True(t, treeCreated)
}

func TestGetIssueComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/org/repo/issues/comments/42", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"id":         42,
			"node_id":    "IC_42",
			"html_url":   "https://github.com/org/repo/issues/1#issuecomment-42",
			"body":       "playback-current: 3",
			"user":       map[string]string{"login": "fullsend-bot"},
			"created_at": "2026-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	comment, err := client.GetIssueComment(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
	assert.Equal(t, 42, comment.ID)
	assert.Equal(t, "IC_42", comment.NodeID)
	assert.Equal(t, "playback-current: 3", comment.Body)
	assert.Equal(t, "fullsend-bot", comment.Author)
}

func TestGetIssueComment_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetIssueComment(context.Background(), "org", "repo", 42)
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestGetIssueComment_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{not valid json"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetIssueComment(context.Background(), "org", "repo", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode issue comment")
}

// TestGetIssueComment_EscapesOwnerAndRepo guards against a crafted owner
// or repo value redirecting the request to a different path or smuggling
// query data (e.g. an unescaped "?" terminating the path early). Both
// fields are exercised independently.
func TestGetIssueComment_EscapesOwnerAndRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/org%3Fevil/repo/issues/comments/42", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery, "a stray delimiter in owner must not start a query string")
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "body": "ok"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetIssueComment(context.Background(), "org?evil", "repo", 42)
	require.NoError(t, err)
}

func TestGetIssueComment_EscapesRepoField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/org/repo%3Fx=/issues/comments/42", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery, "a stray delimiter in repo must not start a query string")
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "body": "ok"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.GetIssueComment(context.Background(), "org", "repo?x=", 42)
	require.NoError(t, err)
}

// TestUpdateIssueComment_EscapesOwnerAndRepo is UpdateIssueComment's
// counterpart to TestGetIssueComment_EscapesOwnerAndRepo.
func TestUpdateIssueComment_EscapesOwnerAndRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		assert.Equal(t, "/repos/org%3Fevil/repo%3Fx=/issues/comments/42", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery, "a stray delimiter in owner/repo must not start a query string")
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "body": "updated"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.UpdateIssueComment(context.Background(), "org?evil", "repo?x=", 42, "updated")
	require.NoError(t, err)
}

func TestDeleteIssueComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/repos/org/repo/issues/comments/42", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.DeleteIssueComment(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
}

func TestListOrgVariables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/orgs/myorg/actions/variables", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"total_count": 2,
			"variables": []map[string]string{
				{"name": "FULLSEND_FOREIGN_E2E_REPOS", "value": "fullsend-ai"},
				{"name": "OTHER", "value": "x"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	vars, err := client.ListOrgVariables(context.Background(), "myorg")
	require.NoError(t, err)
	require.Len(t, vars, 2)
}
func TestGetIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/issues/7", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   7,
			"title":    "Bug",
			"body":     "details",
			"html_url": "https://github.com/org/repo/issues/7",
			"labels":   []map[string]string{{"name": "ready-to-code"}},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issue, err := client.GetIssue(context.Background(), "org", "repo", 7)
	require.NoError(t, err)
	assert.Equal(t, 7, issue.Number)
	assert.Equal(t, []string{"ready-to-code"}, issue.Labels)
}

func TestListRecentWorkflowRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/actions/runs", r.URL.Path)
		assert.Equal(t, "20", r.URL.Query().Get("per_page"))
		json.NewEncoder(w).Encode(map[string]any{
			"workflow_runs": []map[string]any{
				{
					"id": 99, "name": "Triage Agent", "event": "issue_comment",
					"status": "completed", "conclusion": "success", "html_url": "https://example/run/99",
					"created_at": "2024-01-01T00:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	runs, err := client.ListRecentWorkflowRuns(context.Background(), "org", "repo", 20)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "Triage Agent", runs[0].Name)
	assert.Equal(t, "issue_comment", runs[0].Event)
}

func TestListWorkflowRuns_IncludesEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/actions/workflows/fullsend.yaml/runs", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"workflow_runs": []map[string]any{
				{
					"id": 7, "name": "fullsend", "event": "issues",
					"status": "completed", "conclusion": "success",
					"html_url": "https://example/run/7", "created_at": "2024-01-01T00:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	runs, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "issues", runs[0].Event)
}

// TestListWorkflowRunsSince_PaginatesBeyondFirstPage is a regression test
// (#7996 review): ListWorkflowRuns's live implementation requests only
// per_page=10 with no pagination, so an eligible run older than the ten
// newest runs would never be seen — e.g. by harnessRoundPollOnce's
// earliest-round selection. ListWorkflowRunsSince must instead keep
// paginating (ordered newest-first) until it reaches a run older than the
// since boundary, so a run far older than a single page is still returned.
func TestListWorkflowRunsSince_PaginatesBeyondFirstPage(t *testing.T) {
	since := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	var pageRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageRequests = append(pageRequests, r.URL.RawQuery)
		switch r.URL.Query().Get("page") {
		case "1":
			// A full page of 100 runs, all newer than the boundary — this
			// is more than ListWorkflowRuns's old per_page=10 cap ever saw.
			runs := make([]map[string]any, 100)
			for i := range runs {
				runs[i] = map[string]any{
					"id": 300 - i, "name": "fullsend", "event": "issues",
					"status": "completed", "conclusion": "success",
					"html_url": "https://example/run", "created_at": "2024-01-03T00:00:00Z",
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
		case "2":
			// The earliest eligible run (id 100, at the boundary) plus one
			// older run (id 99) that signals pagination can stop.
			json.NewEncoder(w).Encode(map[string]any{
				"workflow_runs": []map[string]any{
					{
						"id": 100, "name": "fullsend", "event": "issues",
						"status": "completed", "conclusion": "success",
						"html_url": "https://example/run/100", "created_at": "2024-01-02T00:00:00Z",
					},
					{
						"id": 99, "name": "fullsend", "event": "issues",
						"status": "completed", "conclusion": "success",
						"html_url": "https://example/run/99", "created_at": "2024-01-01T00:00:00Z",
					},
				},
			})
		default:
			t.Errorf("unexpected page request %q", r.URL.RawQuery)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	runs, err := client.ListWorkflowRunsSince(context.Background(), "org", "repo", "fullsend.yaml", since)
	require.NoError(t, err)
	require.Len(t, runs, 101, "should include the 100 newer runs plus the earliest eligible run at the boundary")
	assert.Equal(t, 100, runs[len(runs)-1].ID, "the earliest eligible run beyond the first page must be included")
	for _, r := range runs {
		assert.NotEqual(t, 99, r.ID, "a run older than the since boundary must not be included")
	}
	assert.Len(t, pageRequests, 2, "pagination must stop once a run older than since is seen")
}

// TestListWorkflowRunsSince_EscapesPathComponents is a regression test
// (#7996 review): owner, repo, and workflowFile were interpolated
// directly into the request path. A "#" or "?" delimiter character in one
// of them would be parsed by url.Parse as the start of the fragment or
// query component instead of literal path content, silently truncating
// the request (observed: everything from "#" onward, including the
// "runs" path suffix and the per_page/page query, was dropped). Escaping
// each component keeps the delimiter inert so the intended path and
// query survive intact.
func TestListWorkflowRunsSince_EscapesPathComponents(t *testing.T) {
	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRunsSince(context.Background(), "org/evil", "repo#frag", "file?.yml", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "/repos/org/evil/repo#frag/actions/workflows/file?.yml/runs", gotPath,
		"the full path must survive intact instead of being truncated at an unescaped '#' or '?'")
	assert.Equal(t, "per_page=100&page=1", gotRawQuery,
		"the per_page/page query must not be dropped by an unescaped delimiter earlier in the path")
}

func TestListWorkflowRunsSince_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRunsSince(context.Background(), "org", "repo", "fullsend.yaml", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list workflow runs since")
}

func TestListWorkflowRunsSince_EmptyFirstPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	runs, err := client.ListWorkflowRunsSince(context.Background(), "org", "repo", "fullsend.yaml", time.Now())
	require.NoError(t, err)
	assert.Empty(t, runs)
}

// TestListWorkflowRunsSince_PaginationExceeded guards the maxPages safety
// valve: if every page is full and since is never reached, pagination must
// stop with an error instead of looping indefinitely.
func TestListWorkflowRunsSince_PaginationExceeded(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		runs := make([]map[string]any, 100)
		for i := range runs {
			runs[i] = map[string]any{
				"id": page*1000 + i, "name": "fullsend", "event": "issues",
				"status": "completed", "conclusion": "success",
				"html_url": "https://example/run", "created_at": "2024-01-03T00:00:00Z",
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := client.ListWorkflowRunsSince(context.Background(), "org", "repo", "fullsend.yaml", since)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination exceeded")
	assert.Equal(t, 100, page)
}

// TestGetCached_ConditionalRequestReuses304 exercises the #6702 fix
// through ListWorkflowRuns: the first request has no If-None-Match, the
// server returns 200 with an ETag; the second request must send that
// exact ETag back, and on 304 the client must decode the cached body
// rather than an empty one.
func TestGetCached_ConditionalRequestReuses304(t *testing.T) {
	const etag = `W/"abc123"`
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			assert.Empty(t, r.Header.Get("If-None-Match"), "first request must not send If-None-Match")
			w.Header().Set("ETag", etag)
			json.NewEncoder(w).Encode(map[string]any{
				"workflow_runs": []map[string]any{
					{"id": 1, "status": "in_progress", "created_at": "2024-01-01T00:00:00Z"},
				},
			})
		case 2:
			assert.Equal(t, etag, r.Header.Get("If-None-Match"), "second request must echo the weak ETag verbatim")
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Errorf("unexpected call %d", calls)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	first, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	require.Len(t, second, 1, "304 must decode to the cached body, not an empty one")
	assert.Equal(t, first[0].ID, second[0].ID)
	assert.Equal(t, "in_progress", second[0].Status)
	assert.Equal(t, 2, calls)
}

// TestGetCached_ChangedETagRefetchesBody guards against the flake class
// this fix could reintroduce if done wrong: a status change must always
// come with a new ETag from the (real) server, and the client must not
// keep serving a stale cached body once the ETag changes.
func TestGetCached_ChangedETagRefetchesBody(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		status := "in_progress"
		etag := `"v1"`
		if calls > 1 {
			status = "completed"
			etag = `"v2"`
		}
		w.Header().Set("ETag", etag)
		json.NewEncoder(w).Encode(map[string]any{
			"workflow_runs": []map[string]any{
				{"id": 1, "status": status, "created_at": "2024-01-01T00:00:00Z"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	first, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	require.Equal(t, "in_progress", first[0].Status)

	second, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	require.Equal(t, "completed", second[0].Status)
}

// TestGetCached_NoETagNotCached ensures a response without an ETag
// header is decoded normally and never triggers a conditional request
// on the next call — there is nothing to send.
func TestGetCached_NoETagNotCached(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Empty(t, r.Header.Get("If-None-Match"))
		json.NewEncoder(w).Encode(map[string]any{
			"workflow_runs": []map[string]any{
				{"id": 1, "status": "in_progress", "created_at": "2024-01-01T00:00:00Z"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	_, err = client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

// TestEtagCache_Bounded ensures a long-lived client polling many
// distinct URLs (one per workflow run, in the behaviour suite) does not
// grow etagCache without bound.
func TestEtagCache_Bounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"`+r.URL.Path+`"`)
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	for i := range etagCacheLimit * 2 {
		_, err := client.ListWorkflowRunJobs(context.Background(), "org", "repo", i)
		require.NoError(t, err)
	}
	client.etagMu.Lock()
	size := len(client.etagCache)
	client.etagMu.Unlock()
	assert.LessOrEqual(t, size, etagCacheLimit)
}

func TestListWorkflowRunJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/actions/runs/42/jobs", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		json.NewEncoder(w).Encode(map[string]any{
			"jobs": []map[string]any{
				{
					"id":         1,
					"name":       "dispatch / Route",
					"status":     "completed",
					"conclusion": "success",
				},
				{
					"id":         2,
					"name":       "dispatch / Harness run (triage)",
					"status":     "completed",
					"conclusion": "success",
				},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	jobs, err := client.ListWorkflowRunJobs(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	assert.Equal(t, 1, jobs[0].ID)
	assert.Equal(t, "dispatch / Route", jobs[0].Name)
	assert.Equal(t, "completed", jobs[0].Status)
	assert.Equal(t, "success", jobs[0].Conclusion)
	assert.Equal(t, 2, jobs[1].ID)
	assert.Equal(t, "dispatch / Harness run (triage)", jobs[1].Name)
}

// TestGetWorkflowRunLogs_TruncationIsMarked verifies that an oversized job
// log and a jobs listing that omits jobs are each reported in the returned
// text, so callers can tell the snapshot is incomplete.
func TestGetWorkflowRunLogs_TruncationIsMarked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/actions/runs/42/jobs":
			assert.Equal(t, "100", r.URL.Query().Get("per_page"))
			json.NewEncoder(w).Encode(map[string]any{
				"total_count": 3,
				"jobs": []map[string]any{
					{"id": 1, "name": "big", "status": "completed", "conclusion": "success"},
					{"id": 2, "name": "small", "status": "completed", "conclusion": "success"},
				},
			})
		case "/repos/org/repo/actions/jobs/1/logs":
			_, _ = w.Write([]byte(strings.Repeat("x", maxJobLogBytes+5)))
		case "/repos/org/repo/actions/jobs/2/logs":
			_, _ = w.Write([]byte("small log"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	logs, err := client.GetWorkflowRunLogs(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(logs, "[log truncated:"), "only the oversized log is marked")
	assert.Contains(t, logs, "[log truncated: job 1 exceeds 1048576 bytes]")
	assert.Contains(t, logs, "small log")
	assert.Contains(t, logs, "[job list truncated: 2 of 3 jobs included]")
}

// TestGetWorkflowRunLogs_CompleteHasNoTruncationMarkers verifies that a
// complete snapshot carries no truncation note.
func TestGetWorkflowRunLogs_CompleteHasNoTruncationMarkers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/actions/runs/42/jobs":
			json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"jobs":        []map[string]any{{"id": 1, "name": "build", "status": "completed", "conclusion": "success"}},
			})
		case "/repos/org/repo/actions/jobs/1/logs":
			_, _ = w.Write([]byte(strings.Repeat("x", maxJobLogBytes)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	logs, err := client.GetWorkflowRunLogs(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
	assert.NotContains(t, logs, "truncated")
}

// TestListWorkflowRunJobs_EscapesPathComponents is a regression test
// (#7996 review): owner and repo were interpolated into the request URL
// without escaping, so a delimiter-containing value (e.g. "#") could alter
// the requested path or turn the jobs suffix and pagination query into a
// URL fragment instead of part of the request.
func TestListWorkflowRunJobs_EscapesPathComponents(t *testing.T) {
	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRunJobs(context.Background(), "org/evil", "repo#frag", 42)
	require.NoError(t, err)
	assert.Equal(t, "/repos/org/evil/repo#frag/actions/runs/42/jobs", gotPath,
		"the full path must survive intact instead of being truncated at an unescaped '#'")
	assert.Equal(t, "per_page=100&page=1", gotRawQuery,
		"the per_page/page query must not be dropped by an unescaped delimiter earlier in the path")
}

func TestListWorkflowRunJobs_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRunJobs(context.Background(), "org", "repo", 42)
	require.Error(t, err)
}

// TestListWorkflowRunJobs_PaginatesBeyondFirstPage is a regression test
// (#7996 review): ListWorkflowRunJobs previously issued a single
// per_page=100 request with no pagination, so a run with more than 100
// jobs (e.g. a large matrix build) would silently drop jobs beyond that
// page — including, for earliest-round selection
// (harnessRoundPollOnce), the earliest eligible run's matching agent job,
// which could make the scan fall through to a later run whose matching
// job had already succeeded. ListWorkflowRunJobs must instead keep
// paginating until a short page signals the end of the listing, so a job
// far beyond a single page is still returned.
func TestListWorkflowRunJobs_PaginatesBeyondFirstPage(t *testing.T) {
	var pageRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageRequests = append(pageRequests, r.URL.RawQuery)
		switch r.URL.Query().Get("page") {
		case "1":
			// A full page of 100 unrelated jobs — more than the matching
			// agent job ever needed to share a run with on the old,
			// unpaginated per_page=100 request.
			jobs := make([]map[string]any, 100)
			for i := range jobs {
				jobs[i] = map[string]any{
					"id": i + 10, "name": "dispatch / Other", "status": "completed", "conclusion": "success",
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
		case "2":
			// The earliest eligible run's matching agent job, beyond the
			// first page.
			json.NewEncoder(w).Encode(map[string]any{
				"jobs": []map[string]any{
					{"id": 1, "name": "dispatch / Harness run (review)", "status": "completed", "conclusion": "success"},
				},
			})
		default:
			t.Errorf("unexpected page request %q", r.URL.RawQuery)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	jobs, err := client.ListWorkflowRunJobs(context.Background(), "org", "repo", 100)
	require.NoError(t, err)
	require.Len(t, jobs, 101, "should include the 100 jobs on the first page plus the matching job beyond it")
	assert.Equal(t, "dispatch / Harness run (review)", jobs[len(jobs)-1].Name, "the matching job beyond the first page must be included")
	assert.Len(t, pageRequests, 2, "pagination must stop once a short page is seen")
}

// TestListWorkflowRunJobs_PaginationExceeded guards the maxPages safety
// valve: if every page is full, pagination must stop with an error instead
// of looping indefinitely.
func TestListWorkflowRunJobs_PaginationExceeded(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		jobs := make([]map[string]any, 100)
		for i := range jobs {
			jobs[i] = map[string]any{
				"id": page*1000 + i, "name": "dispatch / Other", "status": "completed", "conclusion": "success",
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRunJobs(context.Background(), "org", "repo", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination exceeded")
	assert.Equal(t, 100, page)
}

func TestListWorkflowRunArtifacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/actions/runs/42/artifacts", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"artifacts": []map[string]any{
				{"id": 5, "name": "fullsend-triage"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	arts, err := client.ListWorkflowRunArtifacts(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, 5, arts[0].ID)
}

func TestListRepositoryArtifacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/actions/artifacts", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"artifacts": []map[string]any{
				{
					"id": 9, "name": "fullsend-triage", "created_at": "2024-01-01T00:00:00Z",
					"expired": false, "workflow_run": map[string]any{"id": 42},
				},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	arts, err := client.ListRepositoryArtifacts(context.Background(), "org", "repo", 100)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, 42, arts[0].WorkflowRunID)
}

func TestAddIssueLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/org/repo/issues/7/labels", r.URL.Path)
		var body struct {
			Labels []string `json:"labels"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, []string{"ready-for-triage"}, body.Labels)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	require.NoError(t, client.AddIssueLabels(context.Background(), "org", "repo", 7, "ready-for-triage"))
}

func TestAddIssueLabels_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP request for empty labels")
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	require.NoError(t, client.AddIssueLabels(context.Background(), "org", "repo", 7))
}

func TestDownloadWorkflowRunArtifact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/actions/artifacts/9/zip", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PK\x03\x04fake-zip"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	data, err := client.DownloadWorkflowRunArtifact(context.Background(), "org", "repo", 9)
	require.NoError(t, err)
	assert.Equal(t, []byte("PK\x03\x04fake-zip"), data)
}

func TestListRepositoryArtifacts_SkipsExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"artifacts": []map[string]any{
				{"id": 1, "name": "expired", "expired": true, "workflow_run": map[string]any{"id": 1}},
				{"id": 2, "name": "fresh", "expired": false, "created_at": "2024-01-01T00:00:00Z", "workflow_run": map[string]any{"id": 42}},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	arts, err := client.ListRepositoryArtifacts(context.Background(), "org", "repo", 100)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, "fresh", arts[0].Name)
}

func TestDownloadWorkflowRunArtifact_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.DownloadWorkflowRunArtifact(context.Background(), "org", "repo", 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "download workflow artifact")
}

func TestCommitFiles_NonFastForwardRetry(t *testing.T) {
	patchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			sha := "abc123"
			if patchCount > 0 {
				sha = "def456"
			}
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/commits/"):
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/"):
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			patchCount++
			if patchCount == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{"message": "Update is not a fast forward"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "f.txt", Content: []byte("x"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, patchCount)
}

func TestCommitFiles_StaleTreeSHARetry(t *testing.T) {
	treePostCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			sha := "abc123"
			if treePostCount > 0 {
				sha = "def456"
			}
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/commits/"):
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/"):
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			treePostCount++
			if treePostCount == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]any{
					"message": "Validation Failed",
					"errors":  []map[string]string{{"message": "Tree SHA does not exist"}},
				})
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "f.txt", Content: []byte("x"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, treePostCount)
}

func TestCommitFiles_StaleTreeSHAOnCommitRetry(t *testing.T) {
	commitPostCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			sha := "abc123"
			if commitPostCount > 0 {
				sha = "def456"
			}
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/commits/"):
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/"):
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			commitPostCount++
			if commitPostCount == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]any{
					"message": "Validation Failed",
					"errors":  []map[string]string{{"message": "Tree SHA does not exist"}},
				})
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "f.txt", Content: []byte("x"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, commitPostCount)
}

func TestCommitFiles_NonFastForward(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/commit":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/tree"):
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "Update is not a fast forward",
				"errors":  []map[string]string{{"message": "Cannot update ref: not a fast-forward"}},
			})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err))
}

func TestCommitFiles_BranchProtected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "commit"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/commit":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/trees/tree"):
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{
				"message": "Validation Failed",
				"errors":  []map[string]string{{"message": "Protected branch update failed for refs/heads/main."}},
			})
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.True(t, forge.IsBranchProtected(err))
	assert.False(t, forge.IsNonFastForward(err), "should not match non-fast-forward")
}

func TestListRepoVariables_SinglePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/actions/variables", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		json.NewEncoder(w).Encode(map[string]any{
			"total_count": 2,
			"variables": []map[string]string{
				{"name": "FOO", "value": "bar"},
				{"name": "BAZ", "value": "qux"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	vars, err := client.ListRepoVariables(context.Background(), "owner", "repo")
	require.NoError(t, err)
	require.Len(t, vars, 2)
	assert.Equal(t, "bar", vars["FOO"])
	assert.Equal(t, "qux", vars["BAZ"])
}

func TestListRepoVariables_Paginated(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		switch page {
		case 1:
			// Return a full page with total_count indicating more pages
			vars := make([]map[string]string, 100)
			for i := range vars {
				vars[i] = map[string]string{
					"name":  fmt.Sprintf("VAR_%d", i),
					"value": fmt.Sprintf("val_%d", i),
				}
			}
			json.NewEncoder(w).Encode(map[string]any{
				"total_count": 101,
				"variables":   vars,
			})
		case 2:
			// Second page: 1 variable
			json.NewEncoder(w).Encode(map[string]any{
				"total_count": 101,
				"variables": []map[string]string{
					{"name": "VAR_100", "value": "val_100"},
				},
			})
		default:
			t.Errorf("unexpected page %d", page)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	vars, err := client.ListRepoVariables(context.Background(), "owner", "repo")
	require.NoError(t, err)
	assert.Len(t, vars, 101)
	assert.Equal(t, "val_0", vars["VAR_0"])
	assert.Equal(t, "val_100", vars["VAR_100"])
	assert.Equal(t, 2, page, "should have made exactly 2 requests")
}

func TestListRepoVariables_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"total_count": 0,
			"variables":   []map[string]string{},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	vars, err := client.ListRepoVariables(context.Background(), "owner", "repo")
	require.NoError(t, err)
	assert.Empty(t, vars)
}

func TestListRepoVariables_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"message": "Internal Server Error"})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListRepoVariables(context.Background(), "owner", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list repo variables")
}

func TestListRepoVariables_PaginationTruncation(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		// Always return 1 variable but claim there are 20000, so pagination never completes.
		json.NewEncoder(w).Encode(map[string]any{
			"total_count": 20000,
			"variables": []map[string]string{
				{"name": fmt.Sprintf("VAR_%d", page), "value": "v"},
			},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListRepoVariables(context.Background(), "owner", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination exceeded")
	assert.Equal(t, 100, page)
}

func TestDeleteRepoSecret(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			assert.Equal(t, "/repos/owner/repo/actions/secrets/MY_SECRET", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteRepoSecret(context.Background(), "owner", "repo", "MY_SECRET")
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
		err := client.DeleteRepoSecret(context.Background(), "owner", "repo", "ALREADY_GONE")
		require.NoError(t, err)
	})

	t.Run("unexpected status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.DeleteRepoSecret(context.Background(), "owner", "repo", "SECRET")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status")
	})
}

func TestGetCollaboratorPermission(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/repos/o/r/collaborators/alice/permission", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]string{"role_name": "write"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		role, err := client.GetCollaboratorPermission(context.Background(), "o", "r", "alice")
		require.NoError(t, err)
		assert.Equal(t, "write", role)
	})

	t.Run("custom roles", func(t *testing.T) {
		cases := []struct {
			name, body, want string
		}{
			{"maintain flags", `{"permission":"write","user":{"login":"custom-role-maintainer","type":"User","permissions":{"admin":false,"maintain":true,"push":true,"triage":true,"pull":true},"role_name":"Repo Maintainer"},"role_name":"Repo Maintainer"}`, "maintain"},
			{"admin flags", `{"permission":"admin","role_name":"Org Admin","user":{"permissions":{"admin":true,"maintain":true,"push":true,"triage":true,"pull":true}}}`, "admin"},
			{"push flags", `{"permission":"write","role_name":"Dev","user":{"permissions":{"push":true,"pull":true}}}`, "write"},
			{"triage flags", `{"permission":"read","role_name":"Helper","user":{"permissions":{"triage":true,"pull":true}}}`, "triage"},
			{"pull flags", `{"permission":"read","role_name":"Viewer","user":{"permissions":{"pull":true}}}`, "read"},
			{"legacy write only", `{"permission":"write","role_name":"Dev"}`, "write"},
			{"legacy read only stays read", `{"permission":"read","role_name":"Helper"}`, "read"},
			{"no signals", `{"role_name":"Mystery"}`, "none"},
			{"all flags false ignores legacy", `{"permission":"write","role_name":"Dev","user":{"permissions":{"admin":false,"maintain":false,"push":false,"triage":false,"pull":false}}}`, "none"},
			{"null flags fall back to legacy", `{"permission":"write","role_name":"Dev","user":{"permissions":null}}`, "write"},
			{"built-in role wins over flags", `{"permission":"read","role_name":"triage","user":{"permissions":{"pull":true}}}`, "triage"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(tc.body))
				}))
				defer srv.Close()

				role, err := newTestClient(t, srv).GetCollaboratorPermission(context.Background(), "o", "r", "alice")
				require.NoError(t, err)
				assert.Equal(t, tc.want, role)
			})
		}
	})

	t.Run("malformed flags fail", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"role_name":"Dev","user":{"permissions":{"push":"true"}}}`))
		}))
		defer srv.Close()

		_, err := newTestClient(t, srv).GetCollaboratorPermission(context.Background(), "o", "r", "alice")
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.GetCollaboratorPermission(context.Background(), "o", "r", "nobody")
		require.Error(t, err)
		assert.True(t, forge.IsNotFound(err))
	})
}

func TestAddCollaborator(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPut, r.Method)
			assert.Equal(t, "/repos/o/r/collaborators/alice", r.URL.Path)
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "push", body["permission"])
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		require.NoError(t, client.AddCollaborator(context.Background(), "o", "r", "alice", "push"))
	})

	t.Run("invitation pending", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.AddCollaborator(context.Background(), "o", "r", "alice", "push")
		require.ErrorContains(t, err, "invitation pending")
	})

	t.Run("forbidden", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		err := client.AddCollaborator(context.Background(), "o", "r", "alice", "push")
		require.ErrorContains(t, err, "add collaborator alice")
	})
}

func TestGetOrgMembership(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/orgs/halfsend-01/memberships/fstest-write", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]string{"state": "active", "role": "member"})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		got, err := client.GetOrgMembership(context.Background(), "halfsend-01", "fstest-write")
		require.NoError(t, err)
		assert.Equal(t, forge.OrgMembership{State: "active", Role: "member"}, got)
	})

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.GetOrgMembership(context.Background(), "org", "nobody")
		require.Error(t, err)
		assert.True(t, forge.IsNotFound(err))
	})

	t.Run("decode error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "not-json")
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.GetOrgMembership(context.Background(), "org", "alice")
		require.ErrorContains(t, err, "decode org membership")
	})
}

func TestIsProtectedBranch(t *testing.T) {
	t.Run("protected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/owner/repo/branches/main/protection", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"url":"https://api.github.com/repos/owner/repo/branches/main/protection"}`)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		protected, err := client.IsProtectedBranch(context.Background(), "owner", "repo", "main")
		require.NoError(t, err)
		assert.True(t, protected)
	})

	t.Run("not protected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Branch not protected"}`)
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		protected, err := client.IsProtectedBranch(context.Background(), "owner", "repo", "dev")
		require.NoError(t, err)
		assert.False(t, protected)
	})
}

func TestIsProtectedBranch_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.IsProtectedBranch(context.Background(), "owner", "repo", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "check branch protection")
}

func TestIsProtectedBranch_SlashInBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/owner/repo/branches/release%2F1.2/protection", r.URL.RawPath)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"url":"https://api.github.com/repos/owner/repo/branches/release%2F1.2/protection"}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	protected, err := client.IsProtectedBranch(context.Background(), "owner", "repo", "release/1.2")
	require.NoError(t, err)
	assert.True(t, protected)
}

func TestCreateForkInOrg(t *testing.T) {
	t.Run("creates fork in org successfully", func(t *testing.T) {
		callNum := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum++
			switch callNum {
			case 1:
				// Pre-check GET returns 404 (no existing repo)
				assert.Equal(t, "GET", r.Method)
				assert.Equal(t, "/repos/target-org/my-fork", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
			case 2:
				// Fork creation POST
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "/repos/upstream/repo/forks", r.URL.Path)
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				assert.Equal(t, "target-org", body["organization"])
				assert.Equal(t, "my-fork", body["name"])
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]any{
					"name": "my-fork",
				})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkRepo, err := client.CreateForkInOrg(context.Background(), "upstream", "repo", "target-org", "my-fork")
		require.NoError(t, err)
		assert.Equal(t, "my-fork", forkRepo)
	})

	t.Run("existing non-fork repo returns ErrNotFork", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/repos/target-org/my-fork", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"fork": false,
				"name": "my-fork",
			})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateForkInOrg(context.Background(), "upstream", "repo", "target-org", "my-fork")
		require.Error(t, err)
		assert.ErrorIs(t, err, forge.ErrNotFork)
		assert.Contains(t, err.Error(), "not a fork")
	})

	t.Run("existing fork of different source returns ErrNotFork", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			json.NewEncoder(w).Encode(map[string]any{
				"fork": true,
				"name": "my-fork",
				"parent": map[string]any{
					"full_name": "other-owner/other-repo",
				},
			})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateForkInOrg(context.Background(), "upstream", "repo", "target-org", "my-fork")
		require.Error(t, err)
		assert.ErrorIs(t, err, forge.ErrNotFork)
		assert.Contains(t, err.Error(), "other-owner/other-repo")
	})

	t.Run("existing fork of same source is idempotent", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			json.NewEncoder(w).Encode(map[string]any{
				"fork": true,
				"name": "my-fork",
				"parent": map[string]any{
					"full_name": "upstream/repo",
				},
			})
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkRepo, err := client.CreateForkInOrg(context.Background(), "upstream", "repo", "target-org", "my-fork")
		require.NoError(t, err)
		assert.Equal(t, "my-fork", forkRepo)
	})

	t.Run("pre-check non-200 falls through to fork creation", func(t *testing.T) {
		callNum := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum++
			switch callNum {
			case 1:
				// Pre-check returns 403 (not 200, not an error from do())
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible"})
			case 2:
				// Fork creation
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]any{"name": "my-fork"})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		forkRepo, err := client.CreateForkInOrg(context.Background(), "upstream", "repo", "target-org", "my-fork")
		require.NoError(t, err)
		assert.Equal(t, "my-fork", forkRepo)
	})

	t.Run("fork creation API error", func(t *testing.T) {
		callNum := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum++
			switch callNum {
			case 1:
				// Pre-check returns 404
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
			case 2:
				// Fork creation fails
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]any{
					"message": "Validation Failed",
				})
			}
		}))
		defer srv.Close()

		client := newTestClient(t, srv)
		_, err := client.CreateForkInOrg(context.Background(), "upstream", "repo", "target-org", "my-fork")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create fork")
	})
}

func TestUnsupportedMethods(t *testing.T) {
	client := New("test-token")
	ctx := context.Background()

	t.Run("GetProtectedBranch", func(t *testing.T) {
		_, err := client.GetProtectedBranch(ctx, "o", "r", "main")
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("GrantProtectedBranchMergeUser", func(t *testing.T) {
		err := client.GrantProtectedBranchMergeUser(ctx, "o", "r", "main", 1)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("CreatePipeline", func(t *testing.T) {
		_, err := client.CreatePipeline(ctx, "o", "r", "main", nil)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("CreatePipelineWithInputs", func(t *testing.T) {
		_, err := client.CreatePipelineWithInputs(ctx, "o", "r", "main", map[string]forge.PipelineInputValue{
			"STAGE": forge.StringInput("triage"),
		})
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("CreatePipelineSchedule", func(t *testing.T) {
		_, err := client.CreatePipelineSchedule(ctx, "o", "r", "main", "desc", "0 * * * *", nil)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("DeletePipelineSchedule", func(t *testing.T) {
		err := client.DeletePipelineSchedule(ctx, "o", "r", 1)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("ListPipelineSchedules", func(t *testing.T) {
		_, err := client.ListPipelineSchedules(ctx, "o", "r")
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("UpdatePipelineSchedule", func(t *testing.T) {
		err := client.UpdatePipelineSchedule(ctx, "o", "r", 1, true)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("UpdateCIVariable", func(t *testing.T) {
		err := client.UpdateCIVariable(ctx, "o", "r", "KEY", "val", false)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("CreateProtectedCIVariable", func(t *testing.T) {
		err := client.CreateProtectedCIVariable(ctx, "o", "r", "KEY", "val")
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("CreatePipelineTriggerToken", func(t *testing.T) {
		_, err := client.CreatePipelineTriggerToken(ctx, "o", "r", "desc")
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("ListPipelineTriggerTokens", func(t *testing.T) {
		_, err := client.ListPipelineTriggerTokens(ctx, "o", "r")
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("RevokePipelineTriggerToken", func(t *testing.T) {
		err := client.RevokePipelineTriggerToken(ctx, "o", "r", 1)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("CreateProjectHook", func(t *testing.T) {
		_, err := client.CreateProjectHook(ctx, "o", "r", forge.ProjectHook{})
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("ListProjectHooks", func(t *testing.T) {
		_, err := client.ListProjectHooks(ctx, "o", "r")
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("UpdateProjectHook", func(t *testing.T) {
		_, err := client.UpdateProjectHook(ctx, "o", "r", 1, forge.ProjectHook{})
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("DeleteProjectHook", func(t *testing.T) {
		err := client.DeleteProjectHook(ctx, "o", "r", 1)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("GetPipelineVariablesMinimumOverrideRole", func(t *testing.T) {
		_, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, "o", "r")
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("SetPipelineVariablesMinimumOverrideRole", func(t *testing.T) {
		err := client.SetPipelineVariablesMinimumOverrideRole(ctx, "o", "r", forge.PipelineVarOverrideOwner)
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
	t.Run("ForceCommitFileToBranch", func(t *testing.T) {
		err := client.ForceCommitFileToBranch(ctx, "o", "r", "b", "p", "m", []byte("c"))
		assert.ErrorIs(t, err, forge.ErrNotSupported)
	})
}

func TestDo_ObservesRateLimitHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.Header().Set("X-RateLimit-Reset", "1767225600") // 2026-01-01T00:00:00Z
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"name": "test-repo", "full_name": "org/test-repo", "default_branch": "main"})
	}))
	defer srv.Close()
	client := &LiveClient{token: "test-token", baseURL: srv.URL, http: srv.Client(), afterFunc: noWaitAfter}

	_, seen := client.RateLimit()
	assert.False(t, seen, "nothing observed before the first response")

	_, err := client.GetRepo(context.Background(), "org", "test-repo")
	require.NoError(t, err)

	rl, seen := client.RateLimit()
	require.True(t, seen)
	assert.Equal(t, 5000, rl.Limit)
	assert.Equal(t, 4321, rl.Remaining)
	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), rl.Reset.UTC())
	assert.Equal(t, "core", rl.Resource)
	assert.False(t, rl.Observed.IsZero())
	assert.Equal(t, "remaining=4321/5000 reset=2026-01-01T00:00:00Z resource=core", rl.String())
}

func TestDo_ResponseWithoutRateLimitHeadersKeepsPreviousObservation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", "10")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"name": "test-repo", "full_name": "org/test-repo", "default_branch": "main"})
	}))
	defer srv.Close()
	client := &LiveClient{token: "test-token", baseURL: srv.URL, http: srv.Client(), afterFunc: noWaitAfter}
	for i := 0; i < 2; i++ {
		_, err := client.GetRepo(context.Background(), "org", "test-repo")
		require.NoError(t, err)
	}
	rl, seen := client.RateLimit()
	require.True(t, seen)
	assert.Equal(t, 10, rl.Remaining)
}

func TestDo_RateLimitExhaustedErrorSelfIdentifies(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1767225600")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "API rate limit exceeded for installation ID 1."})
	}))
	defer srv.Close()
	client := &LiveClient{token: "test-token", baseURL: srv.URL, http: srv.Client(), afterFunc: noWaitAfter}
	origBackoff := secondaryRateLimitBackoff
	defer func() { secondaryRateLimitBackoff = origBackoff }()
	secondaryRateLimitBackoff = time.Millisecond

	_, err := client.GetRepo(context.Background(), "org", "test-repo")
	require.Error(t, err)
	assert.Equal(t, maxRetries, attempts)
	assert.True(t, IsRateLimitError(err), "an exhausted-retry 403 must be recognised as a rate limit: %v", err)
	assert.Contains(t, err.Error(), "rate limit: retryable error after 5 attempts on GET /repos/org/test-repo")
	assert.Contains(t, err.Error(), "[remaining=0/5000 reset=2026-01-01T00:00:00Z resource=core]")
}

func TestDo_RateLimitExhaustedWithoutHeadersNamesLastObservation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// A healthy response establishes the last observation.
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", "4000")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"name": "test-repo", "full_name": "org/test-repo", "default_branch": "main"})
			return
		}
		// Secondary-limit shape: Retry-After, no X-RateLimit-* headers.
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "You have exceeded a secondary rate limit"})
	}))
	defer srv.Close()
	client := &LiveClient{token: "test-token", baseURL: srv.URL, http: srv.Client(), afterFunc: noWaitAfter}
	_, err := client.GetRepo(context.Background(), "org", "test-repo")
	require.NoError(t, err)

	_, err = client.GetRepo(context.Background(), "org", "test-repo")
	require.Error(t, err)
	assert.True(t, IsRateLimitError(err))
	assert.Contains(t, err.Error(), "rate limit: retryable error after 5 attempts")
	assert.Contains(t, err.Error(), "[rate-limit headers absent; last seen remaining=4000/5000 reset=unknown resource=unknown, ")
	assert.NotContains(t, err.Error(), "[remaining=4000", "a stale observation must not be presented as the failing response's budget")
}

func TestDo_RateLimitExhaustedWithoutAnyObservationSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "You have exceeded a secondary rate limit"})
	}))
	defer srv.Close()
	client := &LiveClient{token: "test-token", baseURL: srv.URL, http: srv.Client(), afterFunc: noWaitAfter}

	_, err := client.GetRepo(context.Background(), "org", "test-repo")
	require.Error(t, err)
	assert.True(t, IsRateLimitError(err))
	assert.Contains(t, err.Error(), "[rate-limit headers absent; no prior observation]")
}

func TestRateLimitString_UnknownFields(t *testing.T) {
	assert.Equal(t, "remaining=7 reset=unknown resource=unknown", forge.RateLimit{Remaining: 7}.String())
}

func TestDo_ServerErrorExhaustedIsNotARateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	client := &LiveClient{token: "test-token", baseURL: srv.URL, http: srv.Client(), afterFunc: noWaitAfter}

	_, err := client.GetRepo(context.Background(), "org", "test-repo")
	require.Error(t, err)
	assert.False(t, IsRateLimitError(err))
	assert.NotContains(t, err.Error(), "rate limit:")
	assert.Contains(t, err.Error(), "retryable error after 5 attempts")
}

// gitDataRaceServer serves the read side of the Git Data API for org/repo
// and returns 404 for the first commitFails commit reads and the first
// treeFails tree reads, the replica lag seen on a freshly auto_init'd
// repo (#7861). Write endpoints used by CommitFiles succeed.
func gitDataRaceServer(t *testing.T, commitFails, treeFails int, commitGets, treeGets *int) *httptest.Server {
	t.Helper()
	var repoGets int
	return gitDataRaceServerWithRepo(t, 0, &repoGets, commitFails, treeFails, commitGets, treeGets)
}

// gitDataRaceServerWithRepo is gitDataRaceServer that also 404s the first
// repoFails repo reads.
func gitDataRaceServerWithRepo(t *testing.T, repoFails int, repoGets *int, commitFails, treeFails int, commitGets, treeGets *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			*repoGets++
			if *repoGets <= repoFails {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			*commitGets++
			if *commitGets <= commitFails {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			*treeGets++
			if *treeGets <= treeFails {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"tree":      []map[string]string{{"path": "README.md", "mode": "100644", "type": "blob", "sha": "r1"}},
				"truncated": false,
			})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestCommitFiles_GetCommit404ExhaustsRetryBudget(t *testing.T) {
	var commitGets, treeGets int
	srv := gitDataRaceServer(t, 100, 0, &commitGets, &treeGets)
	defer srv.Close()

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 5 attempts")
	assert.Equal(t, 5, commitGets, "a persistent 404 stops after the retry budget")
	assert.Zero(t, treeGets)
}

func TestCommitFiles_RetriesTransientGetTree404(t *testing.T) {
	var commitGets, treeGets int
	srv := gitDataRaceServer(t, 0, 2, &commitGets, &treeGets)
	defer srv.Close()

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 3, treeGets, "get tree should retry the transient 404s then succeed")
}

func TestDeleteFiles_RetriesTransientGitData404(t *testing.T) {
	var commitGets, treeGets int
	srv := gitDataRaceServer(t, 1, 1, &commitGets, &treeGets)
	defer srv.Close()

	// No listed path exists in the tree, so DeleteFiles returns after the reads.
	deleted, err := newTestClient(t, srv).DeleteFiles(context.Background(), "org", "repo", "msg", []string{"missing.txt"})
	require.NoError(t, err)
	assert.Zero(t, deleted)
	assert.Equal(t, 2, commitGets)
	assert.Equal(t, 2, treeGets)
}

func TestListRepositoryFiles_RetriesTransientGitData404(t *testing.T) {
	var commitGets, treeGets int
	srv := gitDataRaceServer(t, 1, 1, &commitGets, &treeGets)
	defer srv.Close()

	files, err := newTestClient(t, srv).ListRepositoryFiles(context.Background(), "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, []string{"README.md"}, files)
	assert.Equal(t, 2, commitGets)
	assert.Equal(t, 2, treeGets)
}

func TestDeleteFiles_GetTree404ExhaustsRetryBudget(t *testing.T) {
	var commitGets, treeGets int
	srv := gitDataRaceServer(t, 0, 100, &commitGets, &treeGets)
	defer srv.Close()

	_, err := newTestClient(t, srv).DeleteFiles(context.Background(), "org", "repo", "msg", []string{"missing.txt"})
	require.Error(t, err)
	assert.Equal(t, 5, treeGets, "a persistent tree 404 stops after the retry budget")
	assert.Contains(t, err.Error(), "after 5 attempts")
	assert.NotContains(t, err.Error(), "decode tree", "a GET failure must not be reported as a decode error")
}

func TestGetCommitTreeSHA_DecodeErrorIsNotRetried(t *testing.T) {
	commitGets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/org/repo/git/commits/abc123" {
			commitGets++
			w.Write([]byte("not-json"))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).getCommitTreeSHA(context.Background(), "org", "repo", "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode commit")
	assert.NotContains(t, err.Error(), "after 5 attempts")
	assert.Equal(t, 1, commitGets, "a decode error is not a replica-lag race")
}

// TestGitDataReads_RetryTransientGetRepo404 covers the repo read that
// starts CommitFiles, DeleteFiles and ListRepositoryFiles: the "get repo:
// 404" seen when committing to a just-created harness-hosting repo.
func TestGitDataReads_RetryTransientGetRepo404(t *testing.T) {
	calls := map[string]func(*LiveClient) error{
		"CommitFiles": func(c *LiveClient) error {
			_, err := c.CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
				{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
			})
			return err
		},
		"DeleteFiles": func(c *LiveClient) error {
			_, err := c.DeleteFiles(context.Background(), "org", "repo", "msg", []string{"missing.txt"})
			return err
		},
		"ListRepositoryFiles": func(c *LiveClient) error {
			_, err := c.ListRepositoryFiles(context.Background(), "org", "repo")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			var repoGets, commitGets, treeGets int
			srv := gitDataRaceServerWithRepo(t, 2, &repoGets, 0, 0, &commitGets, &treeGets)
			defer srv.Close()

			require.NoError(t, call(newTestClient(t, srv)))
			assert.Equal(t, 3, repoGets, "get repo should retry the transient 404s then succeed")
		})
	}
}

// treeStatusServer serves the Git Data reads and writes for org/repo;
// the first tree read answers with status and message, later reads
// succeed.
func treeStatusServer(t *testing.T, status int, message string, treeGets *int) *httptest.Server {
	t.Helper()
	return treeStatusServerBody(t, status, map[string]any{"message": message}, treeGets)
}

// treeStatusServerBody is treeStatusServer with a caller-supplied error
// body for the first tree read.
func treeStatusServerBody(t *testing.T, status int, body map[string]any, treeGets *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			*treeGets++
			if *treeGets == 1 {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(body)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"tree":      []map[string]string{{"path": "README.md", "mode": "100644", "type": "blob", "sha": "r1"}},
				"truncated": false,
			})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
		case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/main":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestListRepositoryFiles_RetriesInvalidObject422(t *testing.T) {
	treeGets := 0
	srv := treeStatusServer(t, http.StatusUnprocessableEntity,
		"Invalid object requested. SHA must identify a commit or a tree.", &treeGets)
	defer srv.Close()

	files, err := newTestClient(t, srv).ListRepositoryFiles(context.Background(), "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, []string{"README.md"}, files)
	assert.Equal(t, 2, treeGets, "a replica-lag 422 on a just-returned SHA is retried")
}

func TestListRepositoryFiles_OtherValidation422NotRetried(t *testing.T) {
	treeGets := 0
	srv := treeStatusServer(t, http.StatusUnprocessableEntity, "Validation Failed", &treeGets)
	defer srv.Close()

	_, err := newTestClient(t, srv).ListRepositoryFiles(context.Background(), "org", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Validation Failed")
	assert.Equal(t, 1, treeGets, "any other 422 is a real error")
}

// TestCommitFiles_RetriesInvalidObject422 pins the production path: the
// scaffold commit in github setup failed with this 422 on the tree read.
func TestCommitFiles_RetriesInvalidObject422(t *testing.T) {
	treeGets := 0
	srv := treeStatusServer(t, http.StatusUnprocessableEntity,
		"Invalid object requested. SHA must identify a commit or a tree.", &treeGets)
	defer srv.Close()

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "file.txt", Content: []byte("content"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, treeGets)
}

func TestIsGitDataReadLag(t *testing.T) {
	tests := []struct {
		name string
		err  *APIError
		want bool
	}{
		{"404", &APIError{StatusCode: http.StatusNotFound}, true},
		{"409", &APIError{StatusCode: http.StatusConflict}, true},
		{"422 invalid object", &APIError{StatusCode: 422, Message: "Invalid object requested. SHA must identify a commit or a tree."}, true},
		{"422 other case", &APIError{StatusCode: 422, Message: "invalid OBJECT requested"}, true},
		{"422 in errors envelope", &APIError{StatusCode: 422, Message: "Validation Failed",
			Errors: []APIErrorDetail{{Message: "Invalid object requested. SHA must identify a commit or a tree."}}}, true},
		{"422 validation", &APIError{StatusCode: 422, Message: "Validation Failed"}, false},
		{"invalid object on 400", &APIError{StatusCode: http.StatusBadRequest, Message: "Invalid object requested"}, false},
		{"403", &APIError{StatusCode: http.StatusForbidden}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isGitDataReadLag(tc.err))
		})
	}
}

// runsBody is a minimal valid ListWorkflowRuns payload.
func runsBody(status string) map[string]any {
	return map[string]any{"workflow_runs": []map[string]any{
		{"id": 1, "status": status, "created_at": "2024-01-01T00:00:00Z"},
	}}
}

// TestGetCached_ConcurrentCallersShareOneRequest: concurrent pollers of
// one URL share a single conditional GET, so they cannot race each
// other's cache writes (and spend one request, not N).
func TestGetCached_ConcurrentCallersShareOneRequest(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		w.Header().Set("ETag", `"v1"`)
		json.NewEncoder(w).Encode(runsBody("in_progress"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
		}(i)
	}
	// Let every caller join the in-flight request before it completes.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "caller %d", i)
	}
	// Callers that joined before the response share it; a straggler that
	// arrives after close(release) may start a second request.
	assert.Less(t, calls.Load(), int32(callers), "concurrent callers of one URL share requests")
}

// TestGetCached_OlderResponseNeverOverwritesNewer: a later poll always
// sees the newest state once a newer response has been cached.
func TestGetCached_OlderResponseNeverOverwritesNewer(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("ETag", `"v1"`)
			json.NewEncoder(w).Encode(runsBody("in_progress"))
			return
		}
		if r.Header.Get("If-None-Match") == `"v2"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v2"`)
		json.NewEncoder(w).Encode(runsBody("completed"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx := context.Background()
	for i, want := range []string{"in_progress", "completed", "completed", "completed"} {
		runs, err := client.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml")
		require.NoError(t, err)
		assert.Equal(t, want, runs[0].Status, "poll %d", i)
	}
}

// TestGetCached_InvalidJSONNotCached: a malformed ETagged body is not
// cached, so the next poll is a plain GET rather than a replay.
func TestGetCached_InvalidJSONNotCached(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"bad"`)
			w.Write([]byte("{not json"))
			return
		}
		assert.Empty(t, r.Header.Get("If-None-Match"), "a malformed body must not be cached")
		w.Header().Set("ETag", `"good"`)
		json.NewEncoder(w).Encode(runsBody("in_progress"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list workflow runs: decode /repos/org/repo/actions/workflows/fullsend.yaml/runs")
	runs, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	assert.Equal(t, "in_progress", runs[0].Status)
}

// TestGetCached_UndecodableCachedBodyEvicted: valid JSON that the caller
// cannot decode is dropped from the cache instead of replayed on 304.
func TestGetCached_UndecodableCachedBodyEvicted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"odd"`)
			w.Write([]byte(`{"workflow_runs":"not-a-list"}`))
			return
		}
		assert.Empty(t, r.Header.Get("If-None-Match"), "an undecodable body must be evicted")
		w.Header().Set("ETag", `"good"`)
		json.NewEncoder(w).Encode(runsBody("in_progress"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.Error(t, err)
	_, err = client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
}

// TestGetCached_OversizeBodyReturnedNotRetained: a body larger than
// etagMaxBodyBytes is returned intact but not cached.
func TestGetCached_OversizeBodyReturnedNotRetained(t *testing.T) {
	name := strings.Repeat("x", etagMaxBodyBytes)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Empty(t, r.Header.Get("If-None-Match"), "an oversize body must not be cached")
		w.Header().Set("ETag", `"big"`)
		json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{
			{"id": 7, "name": name, "status": "queued", "created_at": "2024-01-01T00:00:00Z"},
		}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	for range 2 {
		runs, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, name, runs[0].Name, "the oversize body is returned intact")
	}
	assert.Equal(t, int32(2), calls.Load())
	client.etagMu.Lock()
	defer client.etagMu.Unlock()
	assert.Empty(t, client.etagCache)
	assert.Zero(t, client.etagBytes)
}

// cachedKeys returns the cached keys, most recently used first.
func cachedKeys(c *LiveClient) []string {
	c.etagMu.Lock()
	defer c.etagMu.Unlock()
	var keys []string
	if c.etagLRU == nil {
		return keys
	}
	for el := c.etagLRU.Front(); el != nil; el = el.Next() {
		keys = append(keys, el.Value.(*etagEntry).key)
	}
	return keys
}

// TestEtagCache_EvictsLeastRecentlyUsed: a key that keeps being looked up
// survives, and the least recently used one is evicted first.
func TestEtagCache_EvictsLeastRecentlyUsed(t *testing.T) {
	client := New("tok")
	for i := range etagCacheLimit {
		client.storeCachedETag(fmt.Sprintf("k%d", i), "e", []byte("1"))
	}
	// k0 is the oldest store, but it is hot: a lookup marks it recent.
	_, ok := client.lookupCachedETag("k0")
	require.True(t, ok)

	client.storeCachedETag("new", "e", []byte("1"))
	keys := cachedKeys(client)
	assert.Len(t, keys, etagCacheLimit)
	assert.Contains(t, keys, "k0", "a recently used entry survives eviction")
	assert.NotContains(t, keys, "k1", "the least recently used entry is evicted first")
	assert.Equal(t, "new", keys[0])
}

// TestEtagCache_EntryBoundEvicts: exceeding etagCacheLimit evicts only as
// many entries as needed, not the whole cache.
func TestEtagCache_EntryBoundEvicts(t *testing.T) {
	client := New("tok")
	for i := range etagCacheLimit + 3 {
		client.storeCachedETag(fmt.Sprintf("k%d", i), "e", []byte("1"))
	}
	keys := cachedKeys(client)
	assert.Len(t, keys, etagCacheLimit)
	for i := range 3 {
		assert.NotContains(t, keys, fmt.Sprintf("k%d", i))
	}
	assert.Contains(t, keys, "k3")
	client.etagMu.Lock()
	assert.Equal(t, etagCacheLimit, client.etagBytes)
	client.etagMu.Unlock()
}

// TestEtagCache_ByteBoundEvicts: exceeding etagMaxTotalBytes evicts the
// least recently used entries until the total fits.
func TestEtagCache_ByteBoundEvicts(t *testing.T) {
	client := New("tok")
	body := make([]byte, etagMaxBodyBytes)
	n := etagMaxTotalBytes / etagMaxBodyBytes
	for i := range n {
		client.storeCachedETag(fmt.Sprintf("k%d", i), "e", body)
	}
	require.Len(t, cachedKeys(client), n)

	client.storeCachedETag("one-more", "e", []byte("x"))
	keys := cachedKeys(client)
	assert.NotContains(t, keys, "k0", "the least recently used entry makes room")
	assert.Contains(t, keys, "k1")
	assert.Contains(t, keys, "one-more")
	client.etagMu.Lock()
	defer client.etagMu.Unlock()
	assert.LessOrEqual(t, client.etagBytes, etagMaxTotalBytes)
	assert.Equal(t, (n-1)*etagMaxBodyBytes+1, client.etagBytes)
}

// TestEtagCache_ReplaceAdjustsBytes: storing over an existing key replaces
// its byte count instead of adding to it.
func TestEtagCache_ReplaceAdjustsBytes(t *testing.T) {
	client := New("tok")
	client.storeCachedETag("k", "e1", []byte("1"))
	client.storeCachedETag("k", "e2", []byte("123"))
	client.etagMu.Lock()
	defer client.etagMu.Unlock()
	assert.Equal(t, 3, client.etagBytes)
	assert.Len(t, client.etagCache, 1)
}

// TestGetCached_CachedBodyNotAliased: the cache keeps its own copy of the
// body, so mutating a fetched body cannot corrupt it.
func TestGetCached_CachedBodyNotAliased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		json.NewEncoder(w).Encode(runsBody("in_progress"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	path := "/repos/org/repo/actions/runs?per_page=1"
	res, err := client.fetchConditional(context.Background(), client.baseURL+path, path)
	require.NoError(t, err)
	for i := range res.body {
		res.body[i] = 'X'
	}
	client.etagMu.Lock()
	cached := client.etagCache[client.baseURL+path].Value.(*etagEntry).body
	client.etagMu.Unlock()
	assert.True(t, json.Valid(cached), "the cached body must not alias the returned one")

	// A 304 returns the cached body; mutating that result must not reach
	// the cache either.
	notModified, err := client.fetchConditional(context.Background(), client.baseURL+path, path)
	require.NoError(t, err)
	for i := range notModified.body {
		notModified.body[i] = 'X'
	}
	client.etagMu.Lock()
	cached = client.etagCache[client.baseURL+path].Value.(*etagEntry).body
	client.etagMu.Unlock()
	assert.True(t, json.Valid(cached), "the 304 result must not alias the cache")
}

// TestGetCached_ReadErrorNamesPath: a body read failure says which
// request failed.
func TestGetCached_ReadErrorNamesPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"workflow_runs":`))
		// Close with the body short of Content-Length.
		hj, ok := w.(http.Hijacker)
		if !assert.True(t, ok) {
			return
		}
		conn, _, err := hj.Hijack()
		if !assert.NoError(t, err) {
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read response /repos/org/repo/actions/workflows/fullsend.yaml/runs")
}

// TestGetCached_304AfterRetry: a retried conditional GET keeps its
// If-None-Match, and a 304 after a 5xx retry serves the cached body.
func TestGetCached_304AfterRetry(t *testing.T) {
	var calls atomic.Int32
	var inm []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		mu.Lock()
		inm = append(inm, r.Header.Get("If-None-Match"))
		mu.Unlock()
		switch n {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			json.NewEncoder(w).Encode(runsBody("in_progress"))
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotModified)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx := context.Background()
	_, err := client.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	runs, err := client.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	assert.Equal(t, "in_progress", runs[0].Status, "the 304 after a retried 502 serves the cached body")
	assert.Equal(t, int32(3), calls.Load())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"", `"v1"`, `"v1"`}, inm, "the retry keeps If-None-Match")
}

// TestFetchConditional_InvalidJSONNotStored: the cache gate itself
// rejects a malformed ETagged body, before any caller decodes it.
func TestFetchConditional_InvalidJSONNotStored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"bad"`)
		w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	path := "/repos/org/repo/actions/runs?per_page=1"
	res, err := client.fetchConditional(context.Background(), client.baseURL+path, path)
	require.NoError(t, err)
	assert.Equal(t, "{not json", string(res.body), "the body is still returned to the caller")
	client.etagMu.Lock()
	defer client.etagMu.Unlock()
	assert.Empty(t, client.etagCache)
}

// TestGetCached_LeaderCancelDoesNotFailWaiter: when the caller whose
// request is shared gives up, a waiter with its own live context still
// gets the result rather than the other caller's cancellation (#6797
// review).
func TestGetCached_LeaderCancelDoesNotFailWaiter(t *testing.T) {
	received := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		<-release
		w.Header().Set("ETag", `"v1"`)
		json.NewEncoder(w).Encode(runsBody("in_progress"))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := client.ListWorkflowRuns(leaderCtx, "org", "repo", "fullsend.yaml")
		leaderErr <- err
	}()
	<-received // the leader's request is in flight

	waiterErr := make(chan error, 1)
	var waiterRuns []forge.WorkflowRun
	go func() {
		runs, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
		waiterRuns = runs
		waiterErr <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter join the flight
	cancelLeader()
	select {
	case err := <-leaderErr:
		require.ErrorIs(t, err, context.Canceled, "the leader stops on its own context")
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the leader did not stop on its own cancelled context")
	}

	close(release)
	select {
	case err := <-waiterErr:
		require.NoError(t, err, "the waiter must not inherit the leader's cancellation")
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter did not get the shared result")
	}
	require.Len(t, waiterRuns, 1)
}

// TestGetCached_WaiterHonoursOwnDeadline: a caller waiting on another
// caller's in-flight request stops at its own deadline.
func TestGetCached_WaiterHonoursOwnDeadline(t *testing.T) {
	received := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		<-release
		w.Header().Set("ETag", `"v1"`)
		json.NewEncoder(w).Encode(runsBody("in_progress"))
	}))
	defer srv.Close()
	defer close(release)

	client := newTestClient(t, srv)
	go client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml") //nolint:errcheck
	<-received

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml")
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter did not return at its own deadline")
	}
}

// TestFetchConditional_304WithoutEntryIsAnError: a 304 for a request sent
// without If-None-Match is reported, not decoded as an empty body.
func TestFetchConditional_304WithoutEntryIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "304 Not Modified without a cached entry")
}

// TestGetCached_CancelledCallerStartsNoFetch: a caller whose context
// is already done returns at once without starting a request.
func TestGetCached_CancelledCallerStartsNoFetch(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml")
	require.ErrorIs(t, err, context.Canceled)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, calls.Load(), "no request is started for a caller that already gave up")
}

// TestGetCached_UncacheableResponseDropsStaleEntry: when a new 200 cannot
// be cached (here: no ETag), the previous entry is dropped instead of
// being sent as If-None-Match forever.
func TestGetCached_UncacheableResponseDropsStaleEntry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			json.NewEncoder(w).Encode(runsBody("in_progress"))
		case 2:
			assert.Equal(t, `"v1"`, r.Header.Get("If-None-Match"))
			json.NewEncoder(w).Encode(runsBody("completed")) // no ETag
		default:
			assert.Empty(t, r.Header.Get("If-None-Match"), "the stale entry must be dropped")
			json.NewEncoder(w).Encode(runsBody("completed"))
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx := context.Background()
	for i, want := range []string{"in_progress", "completed", "completed"} {
		runs, err := client.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml")
		require.NoError(t, err)
		assert.Equal(t, want, runs[0].Status, "poll %d", i)
	}
	assert.Empty(t, cachedKeys(client))
}

// TestGetCached_AbandonedFetchStillFillsCache: when every caller leaves,
// the detached fetch completes and caches its result for the next caller.
func TestGetCached_AbandonedFetchStillFillsCache(t *testing.T) {
	received := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			received <- struct{}{}
			<-release
			w.Header().Set("ETag", `"v1"`)
			json.NewEncoder(w).Encode(runsBody("in_progress"))
			return
		}
		assert.Equal(t, `"v1"`, r.Header.Get("If-None-Match"), "the abandoned fetch filled the cache")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := client.ListWorkflowRuns(ctx, "org", "repo", "fullsend.yaml")
		errc <- err
	}()
	<-received
	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
	close(release)

	require.Eventually(t, func() bool { return len(cachedKeys(client)) == 1 }, 5*time.Second, 10*time.Millisecond)
	runs, err := client.ListWorkflowRuns(context.Background(), "org", "repo", "fullsend.yaml")
	require.NoError(t, err)
	assert.Equal(t, "in_progress", runs[0].Status)
}

func writeBlobTestFile(t *testing.T, size int) (string, []byte) {
	t.Helper()
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i * 7)
	}
	path := filepath.Join(t.TempDir(), "blob.bin")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path, content
}

// assertBlobUpload validates a blob upload request. It is called from httptest
// handler goroutines, so it uses nonfatal assertions and reports whether the
// upload was valid so the handler can respond with an explicit error.
func assertBlobUpload(t *testing.T, r *http.Request, want []byte) bool {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if !assert.NoError(t, err) {
		return false
	}
	ok := assert.Equal(t, int64(len(body)), r.ContentLength, "uploaded bytes must match ContentLength")
	ok = assert.Equal(t, blobJSONLength(int64(len(want))), int64(len(body))) && ok
	var parsed map[string]string
	if !assert.NoError(t, json.Unmarshal(body, &parsed)) {
		return false
	}
	ok = assert.Equal(t, "base64", parsed["encoding"]) && ok
	decoded, err := base64.StdEncoding.DecodeString(parsed["content"])
	if !assert.NoError(t, err) {
		return false
	}
	return assert.Equal(t, want, decoded) && ok
}

func TestCreateBlobFromFile_RetryReplaysFullFile(t *testing.T) {
	path, content := writeBlobTestFile(t, 200*1024+1)
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/git/blobs", r.URL.Path)
		if attempts.Add(1) == 1 {
			// Consume the first body fully, then ask for a retry.
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !assertBlobUpload(t, r, content) {
			http.Error(w, "invalid blob upload", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"sha": "blobsha"})
	}))
	defer srv.Close()

	sha, err := newTestClient(t, srv).createBlobFromFile(context.Background(), "org", "repo", path)
	require.NoError(t, err)
	assert.Equal(t, "blobsha", sha)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestCreateBlobFromFile_RedirectReplaysBody(t *testing.T) {
	path, content := writeBlobTestFile(t, 50*1024+2)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/git/blobs":
			_, _ = io.Copy(io.Discard, r.Body)
			http.Redirect(w, r, "/redirected/blobs", http.StatusTemporaryRedirect)
		case "/redirected/blobs":
			hits.Add(1)
			assert.Equal(t, http.MethodPost, r.Method)
			if !assertBlobUpload(t, r, content) {
				http.Error(w, "invalid blob upload", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"sha": "blobsha"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	sha, err := newTestClient(t, srv).createBlobFromFile(context.Background(), "org", "repo", path)
	require.NoError(t, err)
	assert.Equal(t, "blobsha", sha)
	assert.Equal(t, int32(1), hits.Load())
}
