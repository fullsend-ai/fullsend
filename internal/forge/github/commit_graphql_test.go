package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Handlers run on the httptest server goroutine: assert only, never require.

type commitMutationInput struct {
	Branch struct {
		RepositoryNameWithOwner string `json:"repositoryNameWithOwner"`
		BranchName              string `json:"branchName"`
	} `json:"branch"`
	ExpectedHeadOid string `json:"expectedHeadOid"`
	Message         struct {
		Headline string  `json:"headline"`
		Body     *string `json:"body"`
	} `json:"message"`
	FileChanges struct {
		Additions []map[string]string `json:"additions"`
		Deletions []map[string]string `json:"deletions"`
	} `json:"fileChanges"`
}

// graphqlCommitServer serves the Git Data reads commitFilesTo performs
// (branch head "abc123", tree "tree000" holding existing) and hands every
// createCommitOnBranch mutation to handle. It records request paths.
func graphqlCommitServer(t *testing.T, branch string, existing []map[string]string, handle func(w http.ResponseWriter, input commitMutationInput)) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": branch})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/"+branch:
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": existing, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/graphql":
			var body struct {
				Query     string `json:"query"`
				Variables struct {
					Input commitMutationInput `json:"input"`
				} `json:"variables"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Contains(t, body.Query, "createCommitOnBranch")
			handle(w, body.Variables.Input)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func graphqlCommitOK(w http.ResponseWriter, oid string) {
	json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"createCommitOnBranch": map[string]any{"commit": map[string]string{"oid": oid}},
	}})
}

func graphqlCommitErrors(w http.ResponseWriter, errs ...graphQLError) {
	entries := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		entries = append(entries, map[string]any{"type": e.Type, "message": e.Message, "path": []string{"createCommitOnBranch"}})
	}
	json.NewEncoder(w).Encode(map[string]any{
		"data":   map[string]any{"createCommitOnBranch": nil},
		"errors": entries,
	})
}

func graphqlCommitError(w http.ResponseWriter, typ, message string) {
	graphqlCommitErrors(w, graphQLError{Type: typ, Message: message})
}

// assertNoGitDataWrites fails when a Git Data write endpoint was hit; reads
// (ref, commit, tree) happen on every path.
func assertNoGitDataWrites(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		assert.NotContains(t, call, "/git/blobs")
		assert.NotEqual(t, "POST /repos/org/repo/git/trees", call)
		assert.NotEqual(t, "POST /repos/org/repo/git/commits", call)
		assert.False(t, strings.HasPrefix(call, "PATCH "), "unexpected ref update: %s", call)
	}
}

const (
	pullRequestRuleViolation = "Repository rule violations found\n\nChanges must be made through a pull request.\n\n"
	signatureRuleViolation   = "Repository rule violations found\n\nCommits must have verified signatures.\n\n"
	// Classic branch protection (require a pull request + a status check),
	// returned with type BRANCH_PROTECTION_RULE_VIOLATION.
	classicProtectionViolation = "protected branch 'probe-classic-protection' check failed:\n  Changes must be made through a pull request. Required status check \"probe-check\" is expected."
	samlForbidden              = "Resource protected by organization SAML enforcement. You must grant your Personal Access token access to this organization."
	staleHead                  = `Expected branch to point to "abc123" but it did not.  Pull and try again.`
	// The same stale-head condition, returned with type FORBIDDEN (#8580).
	forbiddenStaleHead = "is at 98ad40bffbd1e5fb27d3636e7f7b6cbce85113c2 but expected 23ab78126def5042fe61d7e66dcbf5d9f35b589a"
)

var regularFile = []forge.TreeFile{{Path: "f.txt", Content: []byte("x"), Mode: "100644"}}

func TestCommitFiles_GraphQL_RegularFilesTakeSignedPath(t *testing.T) {
	const message = "chore: initialize fullsend per-repo installation [skip ci]\n\nSigned-off-by: Install User <install@example.com>"
	var mutations int
	srv, calls := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, input commitMutationInput) {
		mutations++
		assert.Equal(t, "org/repo", input.Branch.RepositoryNameWithOwner)
		assert.Equal(t, "main", input.Branch.BranchName)
		assert.Equal(t, "abc123", input.ExpectedHeadOid)
		assert.Equal(t, "chore: initialize fullsend per-repo installation [skip ci]", input.Message.Headline)
		if assert.NotNil(t, input.Message.Body) {
			assert.Equal(t, "Signed-off-by: Install User <install@example.com>", *input.Message.Body)
		}
		if assert.Len(t, input.FileChanges.Additions, 2) {
			assert.Equal(t, ".fullsend/config.yaml", input.FileChanges.Additions[0]["path"])
			decoded, err := base64.StdEncoding.DecodeString(input.FileChanges.Additions[0]["contents"])
			assert.NoError(t, err)
			assert.Equal(t, "version: \"1\"\n", string(decoded))
		}
		assert.Empty(t, input.FileChanges.Deletions)
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", message, []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("version: \"1\"\n"), Mode: "100644"},
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("name: fullsend\n"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 1, mutations)
	assertNoGitDataWrites(t, *calls)
}

func TestCommitFilesToBranch_GraphQL_UsesGivenBranch(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "fullsend/scaffold-install", nil, func(w http.ResponseWriter, input commitMutationInput) {
		assert.Equal(t, "fullsend/scaffold-install", input.Branch.BranchName)
		assert.Equal(t, "subject only", input.Message.Headline)
		assert.Nil(t, input.Message.Body)
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFilesToBranch(context.Background(), "org", "repo", "fullsend/scaffold-install", "subject only", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFilesToBranch_Empty(t *testing.T) {
	committed, err := New("token").CommitFilesToBranch(context.Background(), "org", "repo", "main", "msg", nil)
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommitFiles_GraphQL_EmptyModeIsRegular(t *testing.T) {
	srv, calls := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "f.txt", Content: []byte("x")},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assertNoGitDataWrites(t, *calls)
}

func TestCommitFiles_GraphQL_BinaryContentInline(t *testing.T) {
	binary := []byte{0x7f, 0x45, 0x4c, 0x46, 0xff, 0xfe, 0x00}
	srv, calls := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, input commitMutationInput) {
		if assert.Len(t, input.FileChanges.Additions, 1) {
			decoded, err := base64.StdEncoding.DecodeString(input.FileChanges.Additions[0]["contents"])
			assert.NoError(t, err)
			assert.Equal(t, binary, decoded)
		}
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "data.bin", Content: binary, Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assertNoGitDataWrites(t, *calls)
}

func TestCommitFiles_GraphQL_DeletionsFilteredAgainstTree(t *testing.T) {
	existing := []map[string]string{{"path": "old.txt", "mode": "100644", "sha": "aaa"}}
	srv, _ := graphqlCommitServer(t, "main", existing, func(w http.ResponseWriter, input commitMutationInput) {
		assert.Equal(t, []map[string]string{{"path": "old.txt"}}, input.FileChanges.Deletions)
		if assert.Len(t, input.FileChanges.Additions, 1) {
			assert.Equal(t, "new.txt", input.FileChanges.Additions[0]["path"])
		}
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "old.txt", Delete: true},
		{Path: "missing.txt", Delete: true},
		{Path: "new.txt", Content: []byte("new"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommitFiles_GraphQL_OnlyMissingDeletionsIsNoop(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		t.Error("no mutation expected when nothing changes")
		w.WriteHeader(http.StatusInternalServerError)
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "missing.txt", Delete: true},
	})
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommitFiles_GraphQL_ModeOnlyDriftIsUnchanged(t *testing.T) {
	content := []byte("#!/bin/sh\necho hi\n")
	existing := []map[string]string{{"path": "run.sh", "mode": "100755", "sha": blobSHA(content)}}
	srv, _ := graphqlCommitServer(t, "main", existing, func(w http.ResponseWriter, _ commitMutationInput) {
		t.Error("a mode-only difference must not create an empty commit")
		w.WriteHeader(http.StatusInternalServerError)
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "run.sh", Content: content, Mode: "100644"},
	})
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommitFiles_GraphQL_UnchangedVendoredEntryDoesNotForceREST(t *testing.T) {
	binary := []byte{0x7f, 0x45, 0x4c, 0x46, 0x02}
	binPath := filepath.Join(t.TempDir(), "fullsend")
	require.NoError(t, os.WriteFile(binPath, binary, 0o755))
	existing := []map[string]string{{"path": ".fullsend/bin/fullsend", "mode": "100755", "sha": blobSHA(binary)}}

	srv, calls := graphqlCommitServer(t, "main", existing, func(w http.ResponseWriter, input commitMutationInput) {
		if assert.Len(t, input.FileChanges.Additions, 1) {
			assert.Equal(t, ".fullsend/config.yaml", input.FileChanges.Additions[0]["path"])
		}
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: ".fullsend/bin/fullsend", LocalPath: binPath, Mode: "100755"},
		{Path: ".fullsend/config.yaml", Content: []byte("version: \"1\"\n"), Mode: "100644"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	assertNoGitDataWrites(t, *calls)
}

func TestCommitFiles_GraphQL_StaleDataRetries(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		if mutations == 1 {
			graphqlCommitError(w, "STALE_DATA", staleHead)
			return
		}
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, mutations)
}

func TestCommitFiles_GraphQL_ForbiddenStaleHeadRetries(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		if mutations == 1 {
			graphqlCommitError(w, "FORBIDDEN", forbiddenStaleHead)
			return
		}
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, mutations)
}

func TestCommitFiles_GraphQL_StaleDataExhaustsRetries(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		graphqlCommitError(w, "STALE_DATA", staleHead)
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err))
	assert.Equal(t, 3, mutations)
}

// appliedCommitServer serves branch "main" at "abc123" (tree000 holding
// before). The first mutation moves the head to "newcommit" (tree001
// holding after) and answers 502: what the client sees when GitHub applied
// the commit but the response was lost, or when another commit landed
// meanwhile. doRequest's resend gets STALE_DATA; a later mutation succeeds.
func appliedCommitServer(t *testing.T, before, after []map[string]string) (*httptest.Server, *int) {
	t.Helper()
	var mutations int
	head := "abc123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": head}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/newcommit":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree001"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": before, "truncated": false})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree001":
			json.NewEncoder(w).Encode(map[string]any{"tree": after, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/graphql":
			mutations++
			switch mutations {
			case 1:
				head = "newcommit"
				w.WriteHeader(http.StatusBadGateway)
			case 2:
				graphqlCommitError(w, "STALE_DATA", staleHead)
			default:
				graphqlCommitOK(w, "newcommit2")
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &mutations
}

func TestCommitFiles_GraphQL_AppliedMutationWithLostResponseIsCommitted(t *testing.T) {
	srv, mutations := appliedCommitServer(t, nil, []map[string]string{{"path": "f.txt", "mode": "100644", "sha": blobSHA([]byte("x"))}})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, *mutations)
}

func TestCommitFiles_GraphQL_StaleDataAfterUnrelatedCommitIsRetried(t *testing.T) {
	srv, mutations := appliedCommitServer(t, nil, []map[string]string{{"path": "other.txt", "mode": "100644", "sha": "bbb"}})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 3, *mutations)
}

func TestCommitFiles_GraphQL_StaleDataContentCheckReadError(t *testing.T) {
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			if mutations > 0 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && r.URL.Path == "/graphql":
			mutations++
			graphqlCommitError(w, "STALE_DATA", staleHead)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get branch ref")
	assert.False(t, forge.IsNonFastForward(err))
	assert.Equal(t, 1, mutations)
}

func TestCommitFiles_GraphQL_NotFoundIsRetriedAsReplicaLag(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		if mutations == 1 {
			graphqlCommitError(w, "NOT_FOUND", "Could not resolve to a Ref with the name 'refs/heads/main'.")
			return
		}
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, mutations)
}

func TestCommitFiles_GraphQL_NotFoundExhaustsRetries(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		graphqlCommitError(w, "NOT_FOUND", "Could not resolve to a Repository with the name 'org/repo'.")
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
	assert.Equal(t, 3, mutations)
}

func TestCommitFiles_GraphQL_RuleViolationIsBranchProtected(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitError(w, "FORBIDDEN", pullRequestRuleViolation)
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsBranchProtected(err), "the --direct fallback to a PR depends on this")
	assert.False(t, forge.IsNonFastForward(err))
	assert.False(t, forge.IsSignedCommitsRequired(err))
	assert.False(t, forge.IsForbidden(err))
	assert.Contains(t, err.Error(), "Changes must be made through a pull request")
}

func TestCommitFiles_GraphQL_SignatureRuleIsSignedCommitsRequired(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitError(w, "FORBIDDEN", signatureRuleViolation)
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsSignedCommitsRequired(err))
	assert.True(t, forge.IsBranchProtected(err))
	assert.Contains(t, err.Error(), "Commits must have verified signatures")
}

func TestCommitFiles_GraphQL_SAMLForbiddenIsNotProtection(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitError(w, "FORBIDDEN", samlForbidden)
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsForbidden(err))
	assert.False(t, forge.IsBranchProtected(err))
	assert.Contains(t, err.Error(), "SAML")
}

func TestCommitFiles_GraphQL_ForbiddenWithoutRuleIsForbidden(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitError(w, "FORBIDDEN", "Resource not accessible by personal access token")
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsForbidden(err))
	assert.False(t, forge.IsBranchProtected(err))
}

// httpForbiddenServer answers the mutation with an HTTP-level 403 (not a
// GraphQL FORBIDDEN error delivered with HTTP 200).
func httpForbiddenServer(t *testing.T, header http.Header, message string) *httptest.Server {
	t.Helper()
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": message})
	})
	return srv
}

func TestCommitFiles_GraphQL_HTTP403IsForbidden(t *testing.T) {
	srv := httpForbiddenServer(t, nil, "Resource not accessible by integration")

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsForbidden(err), "HTTP-level 403s must name App permissions like GraphQL FORBIDDEN does")
	assert.False(t, forge.IsBranchProtected(err))
}

func TestCommitFiles_GraphQL_HTTP403RateLimitIsNotForbidden(t *testing.T) {
	srv := httpForbiddenServer(t, http.Header{"Retry-After": {"600"}}, "You have exceeded a secondary rate limit")

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, IsRateLimitError(err))
	assert.False(t, forge.IsForbidden(err))
}

func TestCommitFiles_GraphQL_RateLimitedRetries(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		if mutations == 1 {
			w.Header().Set("Retry-After", "1")
			graphqlCommitError(w, "RATE_LIMITED", "API rate limit already exceeded for user ID 1.")
			return
		}
		graphqlCommitOK(w, "newcommit")
	})

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, mutations)
}

func TestCommitFiles_GraphQL_RateLimitedExhaustedIsTransientAPIError(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		graphqlCommitError(w, "RATE_LIMITED", "API rate limit already exceeded for user ID 1.")
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.Equal(t, maxRetries, mutations)
	assert.True(t, IsRateLimitError(err))
	assert.True(t, forge.IsTransient(err))
	assert.False(t, forge.IsBranchProtected(err))
	assert.False(t, forge.IsForbidden(err))
}

func TestCommitFiles_GraphQL_RateLimitedHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		graphqlCommitError(w, "RATE_LIMITED", "API rate limit already exceeded for user ID 1.")
	})
	// Cancel from the backoff hook so the response has already arrived.
	cancelInsteadOfWaiting := func(time.Duration) <-chan time.Time {
		cancel()
		return make(chan time.Time)
	}

	_, err := New("test-token").WithBaseURL(srv.URL).WithAfterFunc(cancelInsteadOfWaiting).CommitFiles(ctx, "org", "repo", "msg", regularFile)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, mutations)
}

// rateLimitedOnceServer answers the first mutation with an HTTP-200
// RATE_LIMITED and every later one with success. mutations counts the
// mutation requests received.
func rateLimitedOnceServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var mutations atomic.Int32
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		if mutations.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			graphqlCommitError(w, "RATE_LIMITED", "API rate limit already exceeded for user ID 1.")
			return
		}
		graphqlCommitOK(w, "newcommit")
	})
	return srv, &mutations
}

func TestCreateCommitOnBranch_RateLimitedResendsWithoutGuard(t *testing.T) {
	srv, mutations := rateLimitedOnceServer(t)

	oid, err := newTestClient(t, srv).createCommitOnBranch(context.Background(), "org", "repo", "main", "abc123", "msg", regularFile, nil)
	require.NoError(t, err)
	assert.Equal(t, "newcommit", oid)
	assert.Equal(t, int32(2), mutations.Load())
}

func TestCreateCommitOnBranch_RateLimitedGuardRunsAfterWaitAndAllowsResend(t *testing.T) {
	srv, mutations := rateLimitedOnceServer(t)

	var guardCalls, mutationsAtGuard atomic.Int32
	ctx := forge.WithRetryGuard(context.Background(), func(context.Context) error {
		guardCalls.Add(1)
		mutationsAtGuard.Store(mutations.Load())
		return nil
	})
	oid, err := newTestClient(t, srv).createCommitOnBranch(ctx, "org", "repo", "main", "abc123", "msg", regularFile, nil)
	require.NoError(t, err)
	assert.Equal(t, "newcommit", oid)
	assert.Equal(t, int32(1), guardCalls.Load(), "the guard runs once, after the rate-limit wait")
	assert.Equal(t, int32(1), mutationsAtGuard.Load(), "the guard runs after the rate-limited mutation, before the resend")
	assert.Equal(t, int32(2), mutations.Load())
}

func TestCreateCommitOnBranch_RateLimitedGuardRefusalStopsResend(t *testing.T) {
	srv, mutations := rateLimitedOnceServer(t)

	stop := errors.New("ownership lost")
	var guardCalls atomic.Int32
	ctx := forge.WithRetryGuard(context.Background(), func(context.Context) error {
		guardCalls.Add(1)
		return stop
	})
	oid, err := newTestClient(t, srv).createCommitOnBranch(ctx, "org", "repo", "main", "abc123", "msg", regularFile, nil)
	require.ErrorIs(t, err, stop)
	assert.Contains(t, err.Error(), "not retried")
	assert.Empty(t, oid)
	assert.Equal(t, int32(1), guardCalls.Load())
	assert.Equal(t, int32(1), mutations.Load(), "the mutation is not resent after the guard refuses")
}

func TestCommitFiles_GraphQL_RateLimitedGuardRefusalAfterWaitStopsResend(t *testing.T) {
	// CommitFiles also runs the guard before its first mutation; let that
	// call pass and refuse the one after the rate-limit wait.
	srv, mutations := rateLimitedOnceServer(t)

	stop := errors.New("ownership lost")
	ctx := forge.WithRetryGuard(context.Background(), func(context.Context) error {
		if mutations.Load() > 0 {
			return stop
		}
		return nil
	})
	committed, err := newTestClient(t, srv).CommitFiles(ctx, "org", "repo", "msg", regularFile)
	require.ErrorIs(t, err, stop)
	assert.False(t, committed)
	assert.Equal(t, int32(1), mutations.Load(), "the mutation is not resent after the guard refuses")
}

// rateLimitHeaders sets the X-RateLimit-* headers GitHub sends with a
// GraphQL response.
func rateLimitHeaders(w http.ResponseWriter, remaining int, reset time.Time) {
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	w.Header().Set("X-RateLimit-Resource", "graphql")
}

func TestCommitFiles_GraphQL_RateLimitedWaitsForReset(t *testing.T) {
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		if mutations == 1 {
			rateLimitHeaders(w, 0, time.Now().Add(90*time.Second))
			graphqlCommitError(w, "RATE_LIMITED", "API rate limit already exceeded for user ID 1.")
			return
		}
		graphqlCommitOK(w, "newcommit")
	})
	var waits []time.Duration
	recordWait := func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		return noWaitAfter(d)
	}

	committed, err := New("test-token").WithBaseURL(srv.URL).WithAfterFunc(recordWait).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, mutations)
	if assert.Len(t, waits, 1) {
		assert.InDelta(t, 91*time.Second, waits[0], float64(2*time.Second))
	}
}

func TestCommitFiles_GraphQL_RateLimitedDistantResetIsReportedAtOnce(t *testing.T) {
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		mutations++
		rateLimitHeaders(w, 0, reset)
		graphqlCommitError(w, "RATE_LIMITED", "API rate limit already exceeded for user ID 1.")
	})
	failOnWait := func(d time.Duration) <-chan time.Time {
		t.Errorf("unexpected wait of %s", d)
		return noWaitAfter(d)
	}

	_, err := New("test-token").WithBaseURL(srv.URL).WithAfterFunc(failOnWait).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.Equal(t, 1, mutations)
	assert.True(t, IsRateLimitError(err))
	assert.True(t, forge.IsTransient(err))
	assert.Contains(t, err.Error(), "[remaining=0/5000 reset="+reset.UTC().Format(time.RFC3339)+" resource=graphql]")
}

func TestRateLimitedDelay(t *testing.T) {
	headers := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	resetIn := func(d time.Duration) string { return strconv.FormatInt(time.Now().Add(d).Unix(), 10) }
	tests := []struct {
		name    string
		header  http.Header
		attempt int
		want    time.Duration
	}{
		{name: "retry-after", header: headers("Retry-After", "7"), want: 7 * time.Second},
		{name: "retry-after wins over a spent quota", header: headers("Retry-After", "7", "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetIn(time.Hour)), want: 7 * time.Second},
		{name: "spent quota waits for the reset", header: headers("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetIn(90*time.Second)), want: 91 * time.Second},
		{name: "spent quota already reset", header: headers("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetIn(-10*time.Second)), want: time.Second},
		{name: "spent quota without a reset time", header: headers("X-RateLimit-Remaining", "0"), want: 61 * time.Second},
		{name: "quota left", header: headers("X-RateLimit-Remaining", "10", "X-RateLimit-Reset", resetIn(90*time.Second)), want: 61 * time.Second},
		{name: "no headers", header: headers(), attempt: 2, want: 64 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rateLimitedDelay(&http.Response{StatusCode: http.StatusOK, Header: tt.header}, tt.attempt)
			assert.InDelta(t, tt.want, got, float64(2*time.Second))
		})
	}
}

func TestRetryCommitOp_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	never := func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	attempts := 0

	_, err := retryCommitOp(ctx, New("token").WithAfterFunc(never), "op", func() (bool, error) {
		attempts++
		return false, forge.ErrNonFastForward
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, attempts)
}

func TestCommitFiles_REST_DeletionAlongsideExecutable(t *testing.T) {
	var treeEntries []map[string]any
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
				"tree":      []map[string]string{{"path": "old.txt", "mode": "100644", "sha": "aaa"}},
				"truncated": false,
			})
		case r.Method == "POST" && r.URL.Path == "/repos/org/repo/git/trees":
			var body struct {
				Tree []map[string]any `json:"tree"`
			}
			if assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				treeEntries = body.Tree
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

	committed, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		{Path: "old.txt", Delete: true},
		{Path: "bin/run.sh", Content: []byte("#!/bin/sh\n"), Mode: "100755"},
	})
	require.NoError(t, err)
	assert.True(t, committed)
	require.Len(t, treeEntries, 2)
	assert.Equal(t, "old.txt", treeEntries[0]["path"])
	assert.Nil(t, treeEntries[0]["sha"])
	assert.Equal(t, "100755", treeEntries[1]["mode"])
	assert.Equal(t, "#!/bin/sh\n", treeEntries[1]["content"])
}

func TestCommitFiles_GraphQL_MultipleErrorsMostActionableWins(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitErrors(w,
			graphQLError{Type: "UNPROCESSABLE", Message: "Variable $input of type CreateCommitOnBranchInput! was provided invalid value"},
			graphQLError{Type: "FORBIDDEN", Message: pullRequestRuleViolation},
		)
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.True(t, forge.IsBranchProtected(err))
}

func TestCommitFiles_GraphQL_UnknownErrorTypeIsPlainError(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitError(w, "UNPROCESSABLE", "Something went wrong")
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github graphql: UNPROCESSABLE: Something went wrong")
	assert.False(t, forge.IsBranchProtected(err))
	assert.False(t, forge.IsNonFastForward(err))
	assert.False(t, forge.IsNotFound(err))
	assert.False(t, forge.IsForbidden(err))
}

func TestCommitFiles_GraphQL_MissingOID(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitOK(w, "")
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no commit oid")
}

func TestCommitFiles_GraphQL_HTTPErrorIsAPIError(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.Contains(t, err.Error(), "create commit on branch")
}

func TestCommitFiles_GraphQL_DecodeError(t *testing.T) {
	srv, _ := graphqlCommitServer(t, "main", nil, func(w http.ResponseWriter, _ commitMutationInput) {
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode create commit on branch response")
}

func TestCreateCommitOnBranch_LocalPathReadError(t *testing.T) {
	_, err := New("token").createCommitOnBranch(context.Background(), "org", "repo", "main", "abc123", "msg",
		[]forge.TreeFile{{Path: "bin", LocalPath: "/nonexistent/fullsend-probe"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read bin")
}

func TestCommitFiles_GHES_UsesAPIGraphQLEndpoint(t *testing.T) {
	var mutationPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		switch {
		case r.Method == "GET" && path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == "GET" && path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == "GET" && path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "graphql"):
			mutationPath = r.URL.Path
			graphqlCommitOK(w, "newcommit")
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New("test-token").WithBaseURL(srv.URL + "/api/v3").WithAfterFunc(noWaitAfter)
	committed, err := client.CommitFiles(context.Background(), "org", "repo", "msg", regularFile)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, "/api/graphql", mutationPath)
}

func TestCommitFiles_REST_SignatureRuleOnRefUpdate(t *testing.T) {
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
			json.NewEncoder(w).Encode(map[string]any{"message": signatureRuleViolation})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).CommitFiles(context.Background(), "org", "repo", "msg", []forge.TreeFile{
		// Mode 100755 pins the Git Data REST path.
		{Path: "bin/fullsend", Content: []byte("elf"), Mode: "100755"},
	})
	require.Error(t, err)
	assert.True(t, forge.IsSignedCommitsRequired(err))
	assert.True(t, forge.IsBranchProtected(err))
	assert.False(t, forge.IsNonFastForward(err))
}

func TestDeleteFiles_GraphQL_StaleDataRetries(t *testing.T) {
	existing := []map[string]string{{"path": "stale.yml", "mode": "100644", "sha": "aaa"}}
	var mutations int
	srv, _ := graphqlCommitServer(t, "main", existing, func(w http.ResponseWriter, input commitMutationInput) {
		mutations++
		assert.Equal(t, []map[string]string{{"path": "stale.yml"}}, input.FileChanges.Deletions)
		if mutations == 1 {
			graphqlCommitError(w, "STALE_DATA", staleHead)
			return
		}
		graphqlCommitOK(w, "newcommit")
	})

	deleted, err := newTestClient(t, srv).DeleteFiles(context.Background(), "org", "repo", "remove", []string{"stale.yml"})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	assert.Equal(t, 2, mutations)
}

func TestDeleteFiles_GraphQL_AppliedMutationWithLostResponseCountsDeletions(t *testing.T) {
	srv, mutations := appliedCommitServer(t, []map[string]string{{"path": "stale.yml", "mode": "100644", "sha": "aaa"}}, nil)

	deleted, err := newTestClient(t, srv).DeleteFiles(context.Background(), "org", "repo", "remove", []string{"stale.yml"})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	assert.Equal(t, 2, *mutations)
}

func TestDeleteFiles_GraphQL_ErrorIsClassified(t *testing.T) {
	existing := []map[string]string{{"path": "stale.yml", "mode": "100644", "sha": "aaa"}}
	srv, _ := graphqlCommitServer(t, "main", existing, func(w http.ResponseWriter, _ commitMutationInput) {
		graphqlCommitError(w, "FORBIDDEN", signatureRuleViolation)
	})

	deleted, err := newTestClient(t, srv).DeleteFiles(context.Background(), "org", "repo", "remove", []string{"stale.yml"})
	require.Error(t, err)
	assert.Zero(t, deleted)
	assert.True(t, forge.IsSignedCommitsRequired(err))
}

func TestUsesGitDataPath(t *testing.T) {
	tests := []struct {
		name  string
		files []forge.TreeFile
		want  bool
	}{
		{name: "no files", files: nil, want: false},
		{name: "regular inline file", files: []forge.TreeFile{{Path: "a", Content: []byte("x"), Mode: "100644"}}, want: false},
		{name: "empty mode is regular", files: []forge.TreeFile{{Path: "a", Content: []byte("x")}}, want: false},
		{name: "deletion never forces REST", files: []forge.TreeFile{{Path: "a", Delete: true, Mode: "100755"}}, want: false},
		{name: "executable mode", files: []forge.TreeFile{{Path: "a", Content: []byte("x"), Mode: "100755"}}, want: true},
		{name: "symlink mode", files: []forge.TreeFile{{Path: "a", Content: []byte("x"), Mode: "120000"}}, want: true},
		{name: "streamed local path", files: []forge.TreeFile{{Path: "a", LocalPath: "/tmp/bin", Mode: "100644"}}, want: true},
		{name: "mixed commit is all-or-nothing", files: []forge.TreeFile{
			{Path: "a", Content: []byte("x"), Mode: "100644"},
			{Path: "b", LocalPath: "/tmp/bin", Mode: "100755"},
		}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, usesGitDataPath(tt.files))
		})
	}
}

func TestSplitCommitMessage(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		headline string
		body     string
	}{
		{name: "subject only", message: "chore: init", headline: "chore: init", body: ""},
		{name: "subject with trailing newline", message: "chore: init\n", headline: "chore: init", body: ""},
		{name: "conventional subject and body", message: "chore: init\n\nSigned-off-by: A <a@x>", headline: "chore: init", body: "Signed-off-by: A <a@x>"},
		{name: "single newline separator", message: "chore: init\nbody", headline: "chore: init", body: "body"},
		{name: "extra blank lines collapse", message: "chore: init\n\n\n\nbody\n", headline: "chore: init", body: "body"},
		{name: "multi-paragraph body kept", message: "s\n\np1\n\np2", headline: "s", body: "p1\n\np2"},
		{name: "empty", message: "", headline: "", body: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headline, body := splitCommitMessage(tt.message)
			assert.Equal(t, tt.headline, headline)
			assert.Equal(t, tt.body, body)
		})
	}
}

func TestGraphqlEndpoint(t *testing.T) {
	assert.Equal(t, "https://api.github.com/graphql", New("t").graphqlEndpoint())
	assert.Equal(t, "https://ghes.example/api/graphql", New("t").WithBaseURL("https://ghes.example/api/v3/").graphqlEndpoint())
	assert.Equal(t, "http://127.0.0.1:1/graphql", New("t").WithBaseURL("http://127.0.0.1:1/").graphqlEndpoint())
}

func TestClassifyCreateCommitErrors(t *testing.T) {
	tests := []struct {
		name      string
		errs      []graphQLError
		protected bool
		signature bool
		nonFF     bool
		notFound  bool
		forbidden bool
		rateLimit bool
		contains  string
	}{
		{name: "pull_request rule", errs: []graphQLError{{Type: "FORBIDDEN", Message: pullRequestRuleViolation}}, protected: true, contains: "FORBIDDEN"},
		{name: "required_signatures rule", errs: []graphQLError{{Type: "FORBIDDEN", Message: signatureRuleViolation}}, protected: true, signature: true, contains: "verified signatures"},
		{name: "classic branch protection", errs: []graphQLError{{Type: "BRANCH_PROTECTION_RULE_VIOLATION", Message: classicProtectionViolation}}, protected: true, contains: "BRANCH_PROTECTION_RULE_VIOLATION"},
		{name: "classic type with unknown check text", errs: []graphQLError{{Type: "BRANCH_PROTECTION_RULE_VIOLATION", Message: "some new check"}}, protected: true, contains: "some new check"},
		{name: "rule text with unexpected type still protected", errs: []graphQLError{{Type: "UNPROCESSABLE", Message: "Repository rule violations found"}}, protected: true, contains: "UNPROCESSABLE"},
		{name: "SAML enforcement is forbidden, not protection", errs: []graphQLError{{Type: "FORBIDDEN", Message: samlForbidden}}, forbidden: true, contains: "SAML"},
		{name: "stale head", errs: []graphQLError{{Type: "STALE_DATA", Message: staleHead}}, nonFF: true, contains: "STALE_DATA"},
		{name: "missing branch", errs: []graphQLError{{Type: "NOT_FOUND", Message: "Could not resolve to a Ref"}}, notFound: true, contains: "NOT_FOUND"},
		{name: "no contents:write", errs: []graphQLError{{Type: "FORBIDDEN", Message: "Resource not accessible by personal access token"}}, forbidden: true, contains: "FORBIDDEN"},
		{name: "rate limited", errs: []graphQLError{{Type: "RATE_LIMITED", Message: "API rate limit already exceeded"}}, rateLimit: true, contains: "429"},
		{name: "other", errs: []graphQLError{{Type: "INTERNAL", Message: "boom"}}, contains: "INTERNAL"},
		{name: "rule violation wins over earlier unrelated error", errs: []graphQLError{{Type: "INTERNAL", Message: "boom"}, {Type: "FORBIDDEN", Message: pullRequestRuleViolation}}, protected: true, contains: "rule violations"},
		{name: "stale wins over later forbidden", errs: []graphQLError{{Type: "FORBIDDEN", Message: "Resource not accessible by personal access token"}, {Type: "STALE_DATA", Message: staleHead}}, nonFF: true, contains: "STALE_DATA"},
		{name: "stale head typed forbidden", errs: []graphQLError{{Type: "FORBIDDEN", Message: forbiddenStaleHead}}, nonFF: true, contains: "but expected"},
		{name: "stale head text needs both shas", errs: []graphQLError{{Type: "FORBIDDEN", Message: "is at abc123 but expected def456"}}, forbidden: true, contains: "FORBIDDEN"},
		{name: "rule violation wins over stale head text", errs: []graphQLError{{Type: "FORBIDDEN", Message: pullRequestRuleViolation}, {Type: "FORBIDDEN", Message: forbiddenStaleHead}}, protected: true, contains: "rule violations"},
		{name: "STALE_DATA wins over earlier stale head text", errs: []graphQLError{{Type: "FORBIDDEN", Message: forbiddenStaleHead}, {Type: "STALE_DATA", Message: staleHead}}, nonFF: true, contains: "STALE_DATA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyCreateCommitErrors(tt.errs)
			require.Error(t, err)
			assert.Equal(t, tt.protected, forge.IsBranchProtected(err), "ErrBranchProtected")
			assert.Equal(t, tt.signature, forge.IsSignedCommitsRequired(err), "ErrSignedCommitsRequired")
			assert.Equal(t, tt.nonFF, forge.IsNonFastForward(err), "ErrNonFastForward")
			assert.Equal(t, tt.notFound, forge.IsNotFound(err), "ErrNotFound")
			assert.Equal(t, tt.forbidden, forge.IsForbidden(err), "ErrForbidden")
			assert.Equal(t, tt.rateLimit, IsRateLimitError(err), "rate limit")
			assert.Contains(t, err.Error(), tt.contains)
		})
	}

	err := classifyCreateCommitErrors(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty errors array")
}

func TestIsRetriableCommitError(t *testing.T) {
	assert.True(t, isRetriableCommitError(forge.ErrNonFastForward))
	assert.True(t, isRetriableCommitError(classifyCreateCommitErrors([]graphQLError{{Type: "STALE_DATA", Message: staleHead}})))
	assert.True(t, isRetriableCommitError(classifyCreateCommitErrors([]graphQLError{{Type: "FORBIDDEN", Message: forbiddenStaleHead}})))
	assert.False(t, isRetriableCommitError(classifyCreateCommitErrors([]graphQLError{{Type: "FORBIDDEN", Message: "Resource not accessible by personal access token"}})))
	assert.True(t, isRetriableCommitError(classifyCreateCommitErrors([]graphQLError{{Type: "NOT_FOUND", Message: "Could not resolve to a Ref"}})))
	assert.False(t, isRetriableCommitError(classifyCreateCommitErrors([]graphQLError{{Type: "FORBIDDEN", Message: pullRequestRuleViolation}})))
	assert.False(t, isRetriableCommitError(&APIError{StatusCode: http.StatusNotFound, Message: "Not Found"}))
	assert.False(t, isRetriableCommitError(nil))
}
