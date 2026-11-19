package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Handlers run on the httptest server goroutine: assert only, never require.

var (
	truncWorkflow = []byte("name: fullsend\n")
	truncConfig   = []byte("version: 1\n")
	truncBinary   = []byte{0x7f, 0x45, 0x4c, 0x46, 0xff, 0xfe, 0x00}
)

// truncatedTrees is a repository whose root tree "tree000" has a
// recursive listing GitHub reports as truncated. Non-recursive listings
// are served per tree SHA; "tree-big" stands in for the unrelated
// content that makes the recursive listing too large and must never be
// listed.
func truncatedTrees() map[string]map[string]any {
	listing := func(entries ...map[string]string) map[string]any {
		return map[string]any{"tree": entries, "truncated": false}
	}
	return map[string]map[string]any{
		"tree000": listing(
			map[string]string{"path": "README.md", "mode": "100644", "type": "blob", "sha": "readme-sha"},
			map[string]string{"path": ".github", "mode": "040000", "type": "tree", "sha": "tree-gh"},
			map[string]string{"path": ".fullsend", "mode": "040000", "type": "tree", "sha": "tree-fs"},
			map[string]string{"path": "big", "mode": "040000", "type": "tree", "sha": "tree-big"},
		),
		"tree-gh": listing(
			map[string]string{"path": "workflows", "mode": "040000", "type": "tree", "sha": "tree-wf"},
		),
		"tree-wf": listing(
			map[string]string{"path": "fullsend.yaml", "mode": "100644", "type": "blob", "sha": blobSHA(truncWorkflow)},
			map[string]string{"path": "old.yaml", "mode": "100644", "type": "blob", "sha": "old-sha"},
		),
		"tree-fs": listing(
			map[string]string{"path": "config.yaml", "mode": "100644", "type": "blob", "sha": blobSHA(truncConfig)},
			map[string]string{"path": "bin", "mode": "040000", "type": "tree", "sha": "tree-bin"},
		),
		"tree-bin": listing(
			map[string]string{"path": "fullsend", "mode": "100755", "type": "blob", "sha": blobSHA(truncBinary)},
		),
	}
}

// truncatedTreeServer serves the reads commitFilesTo performs against
// truncatedTrees (branch head "abc123", root tree "tree000") and hands
// every other request to write. It records each request as
// "METHOD /path?query".
func truncatedTreeServer(t *testing.T, branch string, trees map[string]map[string]any, write func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	const treesPrefix = "/repos/org/repo/git/trees/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": branch})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/"+branch:
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, treesPrefix):
			sha := strings.TrimPrefix(r.URL.Path, treesPrefix)
			if r.URL.Query().Get("recursive") != "" {
				assert.Equal(t, "tree000", sha, "only the root tree is listed recursively")
				// A truncated listing holds an arbitrary subset; this one
				// omits every scaffold path.
				json.NewEncoder(w).Encode(map[string]any{
					"tree":      []map[string]string{{"path": "README.md", "mode": "100644", "type": "blob", "sha": "readme-sha"}},
					"truncated": true,
				})
				return
			}
			assert.NotEqual(t, "tree-big", sha, "unrelated directories must not be listed")
			listing, ok := trees[sha]
			if !ok {
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"message": "forbidden"})
				return
			}
			json.NewEncoder(w).Encode(listing)
		default:
			write(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// commitEntryPoints runs a commit through the direct (CommitFiles) and
// PR-based (CommitFilesToBranch) scaffold delivery paths.
var commitEntryPoints = []struct {
	name   string
	branch string
	commit func(c *LiveClient, files []forge.TreeFile) (bool, error)
}{
	{"direct", "main", func(c *LiveClient, files []forge.TreeFile) (bool, error) {
		return c.CommitFiles(context.Background(), "org", "repo", "install", files)
	}},
	{"pr-based", "fullsend/install", func(c *LiveClient, files []forge.TreeFile) (bool, error) {
		return c.CommitFilesToBranch(context.Background(), "org", "repo", "fullsend/install", "install", files)
	}},
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

func writeTruncBinary(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fullsend")
	require.NoError(t, os.WriteFile(p, content, 0o755))
	return p
}

func TestCommitFiles_TruncatedTree_UnchangedIsNoOp(t *testing.T) {
	for _, ep := range commitEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			srv, calls := truncatedTreeServer(t, ep.branch, truncatedTrees(), func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			})
			client := newTestClient(t, srv)

			committed, err := ep.commit(client, []forge.TreeFile{
				{Path: ".github/workflows/fullsend.yaml", Content: truncWorkflow, Mode: "100644"},
				{Path: ".fullsend/config.yaml", Content: truncConfig, Mode: "100644"},
				{Path: ".fullsend/bin/fullsend", LocalPath: writeTruncBinary(t, truncBinary), Mode: "100755"},
				{Path: ".github/workflows/missing.yaml", Delete: true},
			})
			require.NoError(t, err)
			assert.False(t, committed)
			// Each directory along the requested paths is listed once.
			assert.Equal(t, 1, countCalls(*calls, "GET /repos/org/repo/git/trees/tree000"))
			assert.Equal(t, 1, countCalls(*calls, "GET /repos/org/repo/git/trees/tree-wf"))
			assert.Equal(t, 1, countCalls(*calls, "GET /repos/org/repo/git/trees/tree-fs"))
			assert.Equal(t, 1, countCalls(*calls, "GET /repos/org/repo/git/trees/tree-bin"))
		})
	}
}

func TestCommitFiles_TruncatedTree_GraphQLCommitsOnlyChanges(t *testing.T) {
	for _, ep := range commitEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			var mutations int
			srv, calls := truncatedTreeServer(t, ep.branch, truncatedTrees(), func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/graphql" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				mutations++
				var body struct {
					Variables struct {
						Input commitMutationInput `json:"input"`
					} `json:"variables"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				input := body.Variables.Input
				assert.Equal(t, ep.branch, input.Branch.BranchName)
				assert.Equal(t, "abc123", input.ExpectedHeadOid)
				var added []string
				for _, a := range input.FileChanges.Additions {
					added = append(added, a["path"])
				}
				assert.ElementsMatch(t, []string{".github/workflows/fullsend.yaml", ".fullsend/new/dir/file.txt"}, added)
				var deleted []string
				for _, d := range input.FileChanges.Deletions {
					deleted = append(deleted, d["path"])
				}
				assert.Equal(t, []string{".github/workflows/old.yaml"}, deleted)
				graphqlCommitOK(w, "newcommit")
			})
			client := newTestClient(t, srv)

			committed, err := ep.commit(client, []forge.TreeFile{
				{Path: ".github/workflows/fullsend.yaml", Content: []byte("name: fullsend v2\n"), Mode: "100644"},
				{Path: ".fullsend/config.yaml", Content: truncConfig, Mode: "100644"},
				{Path: ".fullsend/new/dir/file.txt", Content: []byte("new\n"), Mode: "100644"},
				{Path: ".github/workflows/old.yaml", Delete: true},
				{Path: ".github/workflows/missing.yaml", Delete: true},
			})
			require.NoError(t, err)
			assert.True(t, committed)
			assert.Equal(t, 1, mutations)
			assertNoGitDataWrites(t, *calls)
		})
	}
}

func TestCommitFiles_TruncatedTree_GitDataCommitsVendoredAssets(t *testing.T) {
	newBinary := []byte{0x7f, 0x45, 0x4c, 0x46, 0x01, 0xfe, 0x00}
	extraBinary := []byte{0x00, 0xff, 0x10}
	for _, ep := range commitEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			var blobs []string
			var treeEntries []any
			srv, _ := truncatedTreeServer(t, ep.branch, truncatedTrees(), func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/blobs":
					var body map[string]string
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					blobs = append(blobs, body["content"])
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]string{"sha": "blob" + string(rune('0'+len(blobs)))})
				case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
					var body map[string]any
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					// base_tree carries every unrelated path over unchanged.
					assert.Equal(t, "tree000", body["base_tree"])
					treeEntries, _ = body["tree"].([]any)
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]string{"sha": "newtree"})
				case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/commits":
					var body map[string]any
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, []any{"abc123"}, body["parents"])
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]string{"sha": "newcommit"})
				case r.Method == "PATCH" && r.URL.Path == "/repos/org/repo/git/refs/heads/"+ep.branch:
					json.NewEncoder(w).Encode(map[string]any{})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			client := newTestClient(t, srv)

			committed, err := ep.commit(client, []forge.TreeFile{
				{Path: ".github/workflows/fullsend.yaml", Content: truncWorkflow, Mode: "100644"},
				{Path: ".fullsend/bin/fullsend", LocalPath: writeTruncBinary(t, newBinary), Mode: "100755"},
				{Path: ".fullsend/bin/extra", Content: extraBinary, Mode: "100755"},
				{Path: ".fullsend/config.yaml", Content: truncConfig, Mode: "100755"}, // mode-only
			})
			require.NoError(t, err)
			assert.True(t, committed)
			assert.Len(t, blobs, 2, "only the changed and new binaries are uploaded")

			byPath := make(map[string]map[string]any, len(treeEntries))
			for _, raw := range treeEntries {
				entry, _ := raw.(map[string]any)
				path, _ := entry["path"].(string)
				byPath[path] = entry
			}
			assert.Len(t, byPath, 3, "unchanged files are not rewritten")
			assert.NotContains(t, byPath, ".github/workflows/fullsend.yaml")
			assert.Equal(t, "100755", byPath[".fullsend/bin/fullsend"]["mode"])
			assert.Equal(t, "100755", byPath[".fullsend/bin/extra"]["mode"])
			assert.Equal(t, "100755", byPath[".fullsend/config.yaml"]["mode"])
		})
	}
}

func TestCommitFiles_TruncatedTree_UnresolvablePathErrors(t *testing.T) {
	cases := []struct {
		name    string
		trees   func() map[string]map[string]any
		wantErr string
	}{
		{
			name: "subtree listing truncated",
			trees: func() map[string]map[string]any {
				trees := truncatedTrees()
				trees["tree-wf"]["truncated"] = true
				return trees
			},
			wantErr: `tree listing for "/.github/workflows" too large (truncated); cannot diff`,
		},
		{
			name: "subtree listing fails",
			trees: func() map[string]map[string]any {
				trees := truncatedTrees()
				delete(trees, "tree-wf")
				return trees
			},
			wantErr: "get tree",
		},
	}
	for _, tc := range cases {
		for _, ep := range commitEntryPoints {
			t.Run(tc.name+"/"+ep.name, func(t *testing.T) {
				srv, _ := truncatedTreeServer(t, ep.branch, tc.trees(), func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				})
				client := newTestClient(t, srv)

				committed, err := ep.commit(client, []forge.TreeFile{
					{Path: ".fullsend/config.yaml", Content: []byte("changed\n"), Mode: "100644"},
					{Path: ".github/workflows/fullsend.yaml", Content: truncWorkflow, Mode: "100644"},
				})
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.False(t, committed)
			})
		}
	}
}

func TestResolveTreePaths(t *testing.T) {
	srv, _ := truncatedTreeServer(t, "main", truncatedTrees(), func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	client := newTestClient(t, srv)

	got, err := client.resolveTreePaths(context.Background(), "org", "repo", "tree000", []string{
		"README.md",
		"README.md/nested",            // parent is a blob, not a directory
		".github",                     // a directory itself
		".github/workflows/old.yaml",  // nested blob
		".github/absent/file.txt",     // missing parent directory
		".fullsend/bin/fullsend",      // executable
		".fullsend/missing-file.yaml", // missing leaf
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]treeEntryInfo{
		"README.md":                  {sha: "readme-sha", mode: "100644"},
		".github":                    {sha: "tree-gh", mode: "040000"},
		".github/workflows/old.yaml": {sha: "old-sha", mode: "100644"},
		".fullsend/bin/fullsend":     {sha: blobSHA(truncBinary), mode: "100755"},
	}, got)
}
