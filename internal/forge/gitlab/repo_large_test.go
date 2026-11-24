package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// serveFirstParentHistory registers a fake commits-list endpoint serving a
// first-parent history of total commits (tip first, root last) with
// GitLab-style offset pagination. When boundaryNextPage is set, a full page
// always advertises X-Next-Page, even when history ends exactly on it. It
// returns a pointer to the number of GET requests served.
func serveFirstParentHistory(t *testing.T, mux *http.ServeMux, total int, boundaryNextPage bool) *int {
	t.Helper()
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})
	gets := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		gets++
		q := r.URL.Query()
		assert.Equal(t, "true", q.Get("first_parent"))
		assert.Equal(t, "main", q.Get("ref_name"))
		perPage, err := strconv.Atoi(q.Get("per_page"))
		require.NoError(t, err)
		page, err := strconv.Atoi(q.Get("page"))
		require.NoError(t, err)
		require.GreaterOrEqual(t, page, 1)

		start := (page - 1) * perPage
		end := min(start+perPage, total)
		commits := []map[string]any{}
		for i := start; i < end; i++ {
			commits = append(commits, map[string]any{"id": fmt.Sprintf("c-%d", i)})
		}
		if end < total || (boundaryNextPage && len(commits) == perPage) {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		}
		json.NewEncoder(w).Encode(commits)
	})
	return &gets
}

func TestResolveRootCommitSHA_BoundedRequests(t *testing.T) {
	cases := []struct {
		name             string
		total            int
		boundaryNextPage bool
	}{
		{name: "single commit", total: 1},
		{name: "partial first page", total: 99},
		{name: "exact single page", total: 100},
		{name: "exact single page with next header", total: 100, boundaryNextPage: true},
		{name: "partial second page", total: 101},
		{name: "exact page boundary", total: 800},
		{name: "exact page boundary with next header", total: 800, boundaryNextPage: true},
		{name: "large history from issue 9401", total: 46_213},
		{name: "very large history", total: 1_000_000},
		{name: "very large exact boundary with next header", total: 1_000_000, boundaryNextPage: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, mux := setupTest(t)
			gets := serveFirstParentHistory(t, mux, tc.total, tc.boundaryNextPage)

			root, err := client.resolveRootCommitSHA(context.Background(), "owner", "repo")
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("c-%d", tc.total-1), root)

			pages := (tc.total + 99) / 100
			bound := 2*int(math.Ceil(math.Log2(float64(pages+1)))) + 2
			assert.LessOrEqual(t, *gets, bound, "root resolution must use O(log n) page requests")
		})
	}
}

func TestResolveRootCommitSHA_EmptyHistory(t *testing.T) {
	client, mux := setupTest(t)
	gets := serveFirstParentHistory(t, mux, 0, false)

	_, err := client.resolveRootCommitSHA(context.Background(), "owner", "repo")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.Equal(t, 1, *gets)
}

func TestResolveRootCommitSHA_ProbeErrorMidSearch(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})
	errorServed := false
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			commits := make([]map[string]any, 100)
			for i := range commits {
				commits[i] = map[string]any{"id": fmt.Sprintf("c-%d", i)}
			}
			w.Header().Set("X-Next-Page", "2")
			json.NewEncoder(w).Encode(commits)
			return
		}
		errorServed = true
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"message": "boom"})
	})

	_, err := client.resolveRootCommitSHA(context.Background(), "owner", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list root commits")
	assert.True(t, errorServed, "500 error handler should be invoked")
}

func TestResolveRootCommitSHA_NeverEndingHistoryIsBounded(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})
	gets := 0
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		gets++
		commits := make([]map[string]any, 100)
		for i := range commits {
			commits[i] = map[string]any{"id": "x"}
		}
		w.Header().Set("X-Next-Page", "next")
		json.NewEncoder(w).Encode(commits)
	})

	_, err := client.resolveRootCommitSHA(context.Background(), "owner", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.LessOrEqual(t, gets, 30)
}

// fakeTree is a fake GitLab repository tree. It serves only non-recursive
// directory listings and fails the test on recursive listings, which are
// what made scaffold commits walk the entire tree of large repositories.
type fakeTree struct {
	t        *testing.T
	blobs    map[string]treeEntry
	requests []string
}

func (f *fakeTree) handler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("recursive") == "true" {
		f.t.Errorf("recursive tree listing must not be used: %s", r.URL.RawQuery)
	}
	assert.Equal(f.t, "main", q.Get("ref"))
	dir := q.Get("path")
	f.requests = append(f.requests, dir)

	children := map[string]map[string]any{}
	for p, e := range f.blobs {
		rel := p
		if dir != "" {
			if !strings.HasPrefix(p, dir+"/") {
				continue
			}
			rel = strings.TrimPrefix(p, dir+"/")
		}
		if sub, _, ok := strings.Cut(rel, "/"); ok {
			if dir != "" {
				sub = dir + "/" + sub
			}
			children[sub] = map[string]any{"id": "tree-" + sub, "path": sub, "type": "tree", "mode": "040000"}
			continue
		}
		children[p] = map[string]any{"id": e.sha, "path": p, "type": "blob", "mode": e.mode}
	}
	if len(children) == 0 && dir != "" {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "404 Tree Not Found"})
		return
	}

	keys := make([]string, 0, len(children))
	for k := range children {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	perPage, _ := strconv.Atoi(q.Get("per_page"))
	page, _ := strconv.Atoi(q.Get("page"))
	start := min((page-1)*perPage, len(keys))
	end := min(start+perPage, len(keys))
	out := make([]map[string]any, 0, end-start)
	for _, k := range keys[start:end] {
		out = append(out, children[k])
	}
	if end < len(keys) {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	}
	json.NewEncoder(w).Encode(out)
}

func newLargeFakeTree(t *testing.T) *fakeTree {
	t.Helper()
	blobs := map[string]treeEntry{}
	// A large repository: 60k files under drivers/ that a full recursive
	// walk would have to page through 600 times.
	for i := range 60_000 {
		blobs[fmt.Sprintf("drivers/d%d/f%d.c", i%300, i)] = treeEntry{sha: "x", mode: "100644"}
	}
	// 150 root files so the root listing itself paginates.
	for i := range 150 {
		blobs[fmt.Sprintf("root-%03d.txt", i)] = treeEntry{sha: "x", mode: "100644"}
	}
	blobs[".gitlab-ci.yml"] = treeEntry{sha: blobSHA([]byte("old ci")), mode: "100644"}
	blobs[".fullsend/config.yaml"] = treeEntry{sha: blobSHA([]byte("same")), mode: "100644"}
	blobs[".gitlab/ci/scripts/pin.sh"] = treeEntry{sha: blobSHA([]byte("#!/bin/sh")), mode: "100644"}
	blobs[".gitlab/ci/scripts/was-exec.sh"] = treeEntry{sha: blobSHA([]byte("plain")), mode: "100755"}
	return &fakeTree{t: t, blobs: blobs}
}

func TestCommitFilesToBranch_LargeTreeListsOnlyTouchedDirectories(t *testing.T) {
	client, mux := setupTest(t)
	tree := newLargeFakeTree(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", tree.handler)

	var payload map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &payload))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "new"})
	})

	committed, err := client.CommitFilesToBranch(context.Background(), "owner", "repo", "main", "scaffold", []forge.TreeFile{
		{Path: ".gitlab-ci.yml", Content: []byte("new ci"), Mode: "100644"},                // update (content)
		{Path: ".fullsend/config.yaml", Content: []byte("same"), Mode: "100644"},           // no-op
		{Path: ".gitlab/ci/scripts/pin.sh", Content: []byte("#!/bin/sh"), Mode: "100755"},  // update (mode +x)
		{Path: ".gitlab/ci/scripts/was-exec.sh", Content: []byte("plain"), Mode: "100644"}, // update (mode -x)
		{Path: "new/dir/file.txt", Content: []byte("hi"), Mode: "100644"},                  // create (missing dir)
		{Path: "root-new.txt", Content: []byte("r"), Mode: "100644"},                       // create (root)
	})
	require.NoError(t, err)
	assert.True(t, committed)

	// One listing per touched directory (root needs 2 pages), regardless of
	// the 60k files elsewhere in the repository.
	assert.ElementsMatch(t, []string{"", "", ".fullsend", ".gitlab/ci/scripts", "new/dir"}, tree.requests)

	actions := map[string]map[string]any{}
	for _, a := range payload["actions"].([]any) {
		m := a.(map[string]any)
		actions[m["file_path"].(string)] = m
	}
	require.Len(t, actions, 5)
	assert.Equal(t, "update", actions[".gitlab-ci.yml"]["action"])
	assert.NotContains(t, actions[".gitlab-ci.yml"], "execute_filemode")
	assert.Equal(t, "update", actions[".gitlab/ci/scripts/pin.sh"]["action"])
	assert.Equal(t, true, actions[".gitlab/ci/scripts/pin.sh"]["execute_filemode"])
	assert.Equal(t, "update", actions[".gitlab/ci/scripts/was-exec.sh"]["action"])
	assert.Equal(t, false, actions[".gitlab/ci/scripts/was-exec.sh"]["execute_filemode"])
	assert.Equal(t, "create", actions["new/dir/file.txt"]["action"])
	assert.Equal(t, "create", actions["root-new.txt"]["action"])
	decoded, err := base64.StdEncoding.DecodeString(actions["new/dir/file.txt"]["content"].(string))
	require.NoError(t, err)
	assert.Equal(t, "hi", string(decoded))
}

func TestCommitFilesToBranch_LargeTreeNoOp(t *testing.T) {
	client, mux := setupTest(t)
	tree := newLargeFakeTree(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", tree.handler)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no commit expected when all files match")
	})

	committed, err := client.CommitFilesToBranch(context.Background(), "owner", "repo", "main", "scaffold", []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("same"), Mode: "100644"},
		{Path: ".gitlab/ci/scripts/was-exec.sh", Content: []byte("plain"), Mode: "100755"},
	})
	require.NoError(t, err)
	assert.False(t, committed)
	assert.ElementsMatch(t, []string{".fullsend", ".gitlab/ci/scripts"}, tree.requests)
}

func TestCommitFilesToBranch_TreeListingError(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"message": "boom"})
	})

	_, err := client.CommitFilesToBranch(context.Background(), "owner", "repo", "main", "m", []forge.TreeFile{
		{Path: "a/b.txt", Content: []byte("x"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get tree")
}

func TestCommitFilesToBranch_TreeDecodeError(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-json"))
	})

	_, err := client.CommitFilesToBranch(context.Background(), "owner", "repo", "main", "m", []forge.TreeFile{
		{Path: "a/b.txt", Content: []byte("x"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get tree")
}

func TestDeleteFiles_LargeTreeListsOnlyTouchedDirectories(t *testing.T) {
	client, mux := setupTest(t)
	tree := newLargeFakeTree(t)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", tree.handler)

	var payload map[string]any
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &payload))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "new"})
	})

	deleted, err := client.DeleteFiles(context.Background(), "owner", "repo", "cleanup", []string{
		".fullsend/config.yaml", ".fullsend/missing.yaml", "gone/dir/x.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	assert.ElementsMatch(t, []string{".fullsend", "gone/dir"}, tree.requests)
	actions := payload["actions"].([]any)
	require.Len(t, actions, 1)
	assert.Equal(t, ".fullsend/config.yaml", actions[0].(map[string]any)["file_path"])
}
