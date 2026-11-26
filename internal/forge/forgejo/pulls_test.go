package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// pullsRequest is one request the fake pulls API received.
type pullsRequest struct {
	Method string
	Path   string // escaped path below /forgejo/api/v1/repos/o/r
	Query  string
	Auth   string
	Body   map[string]any
}

// fakePulls serves handlers keyed by "METHOD path", where path is the
// escaped path below the repository's API URL under a /forgejo prefix. It
// records every request.
type fakePulls struct {
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	requests []pullsRequest
}

func (f *fakePulls) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutPrefix(r.URL.EscapedPath(), fakePrefix+"/api/v1/repos/o/r")
	if !ok {
		// Another repository is keyed "METHOD @owner/repo".
		path, ok = strings.CutPrefix(r.URL.EscapedPath(), fakePrefix+"/api/v1/repos/")
		path = "@" + path
	}
	req := pullsRequest{Method: r.Method, Path: path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
	data, _ := io.ReadAll(r.Body)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &req.Body)
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	f.mu.Lock()
	f.requests = append(f.requests, req)
	h, found := f.handlers[r.Method+" "+path]
	f.mu.Unlock()
	if !ok || !found {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
		return
	}
	h(w, r)
}

func (f *fakePulls) handle(key string, h http.HandlerFunc) { f.handlers[key] = h }

func (f *fakePulls) reqs() []pullsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pullsRequest(nil), f.requests...)
}

func newFakePulls(t *testing.T) (*fakePulls, *LiveClient) {
	t.Helper()
	f := &fakePulls{handlers: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+fakePrefix+"/", "secret-token", srv.Client())
	require.NoError(t, err)
	return f, c
}

func respond(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
}

func apiMsg(status int, msg string) http.HandlerFunc {
	return respond(status, map[string]string{"message": msg})
}

func fakePR(number int, head, headRepo, author string) map[string]any {
	return map[string]any{
		"id": 1000 + number, "number": number, "title": "PR " + strconv.Itoa(number),
		"html_url": fmt.Sprintf("https://forgejo.example/forgejo/o/r/pulls/%d", number),
		"user":     map[string]any{"login": author},
		"head":     map[string]any{"ref": head, "sha": "headsha", "repo": map[string]any{"full_name": headRepo}},
		"base":     map[string]any{"ref": "main", "sha": "basesha", "repo": map[string]any{"full_name": "o/r"}},
	}
}

func TestBranchOperations(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("GET /branches/feature/x%20y", respond(200, map[string]any{"name": "feature/x y", "commit": map[string]any{"id": "abc123"}}))
	f.handle("GET /branches/empty", respond(200, map[string]any{"name": "empty"}))
	f.handle("POST /branches", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["new_branch_name"] {
		case "exists":
			apiMsg(409, "branch already exists")(w, r)
		case "denied":
			apiMsg(403, "no push access")(w, r)
		case "unknown-sha":
			apiMsg(404, "ref not found")(w, r)
		default:
			respond(201, map[string]any{"name": body["new_branch_name"]})(w, r)
		}
	})
	f.handle("DELETE /branches/agent/1-fix", respond(204, nil))
	f.handle("DELETE /branches/protected", apiMsg(403, "branch protected"))

	sha, err := c.GetBranchRef(ctx, "o", "r", "feature/x y")
	require.NoError(t, err)
	assert.Equal(t, "abc123", sha)
	_, err = c.GetBranchRef(ctx, "o", "r", "empty")
	assert.ErrorContains(t, err, "no commit SHA")
	_, err = c.GetBranchRef(ctx, "o", "r", "missing")
	assert.True(t, forge.IsNotFound(err), "%v", err)

	require.NoError(t, c.CreateBranch(ctx, "o", "r", "agent/1-fix"))
	require.NoError(t, c.CreateBranchFromSHA(ctx, "o", "r", "agent/2-fix", "deadbeef"))
	assert.True(t, forge.IsAlreadyExists(c.CreateBranch(ctx, "o", "r", "exists")))
	assert.True(t, forge.IsForbidden(c.CreateBranchFromSHA(ctx, "o", "r", "denied", "deadbeef")))
	assert.True(t, forge.IsNotFound(c.CreateBranchFromSHA(ctx, "o", "r", "unknown-sha", "deadbeef")))

	require.NoError(t, c.DeleteBranch(ctx, "o", "r", "agent/1-fix"))
	assert.True(t, forge.IsNotFound(c.DeleteBranch(ctx, "o", "r", "gone")))
	err = c.DeleteBranch(ctx, "o", "r", "protected")
	assert.True(t, forge.IsForbidden(err))
	assert.NotContains(t, err.Error(), "secret-token")

	reqs := f.reqs()
	for _, r := range reqs {
		assert.Equal(t, "token secret-token", r.Auth)
	}
	assert.Equal(t, map[string]any{"new_branch_name": "agent/1-fix"}, reqs[3].Body, "CreateBranch starts from the default branch")
	assert.Equal(t, map[string]any{"new_branch_name": "agent/2-fix", "old_ref_name": "deadbeef"}, reqs[4].Body)

	n := len(f.reqs())
	assert.Error(t, c.CreateBranchFromSHA(ctx, "o", "r", "x", ""))
	assert.Len(t, f.reqs(), n, "an empty SHA makes no request")
}

func TestCreateChangeProposal(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	forkOf := func(parent string) http.HandlerFunc {
		return respond(200, map[string]any{"fork": true, "parent": map[string]any{"full_name": parent}})
	}
	f.handle("GET @fork/r", forkOf("o/r"))
	f.handle("GET @stranger/r", forkOf("o/r"))
	f.handle("GET @plain/r", respond(200, map[string]any{"fork": false}))
	f.handle("GET @elsewhere/r", forkOf("someone/else"))
	f.handle("POST /pulls", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["head"] {
		case "dup":
			apiMsg(409, "pull request already exists for these targets")(w, r)
		case "nonum":
			respond(201, map[string]any{})(w, r)
		case "stranger:feat":
			respond(201, fakePR(8, "feat", "stranger/other", "bot"))(w, r)
		case "fork:feat":
			respond(201, fakePR(9, "feat", "fork/r", "bot"))(w, r)
		default:
			respond(201, fakePR(7, body["head"], "o/r", "bot"))(w, r)
		}
	})

	cp, err := c.CreateChangeProposal(ctx, "o", "r", "Fix it", "Body", "agent/1-fix", "main")
	require.NoError(t, err)
	assert.Equal(t, forge.ChangeProposal{
		URL: "https://forgejo.example/forgejo/o/r/pulls/7", Title: "PR 7", Number: 7, ID: 1007,
		Head: "agent/1-fix", HeadRepo: "o/r", Base: "main", Author: "bot",
	}, *cp)
	assert.Equal(t, map[string]any{"title": "Fix it", "body": "Body", "head": "agent/1-fix", "base": "main"}, f.reqs()[0].Body)

	_, err = c.CreateChangeProposal(ctx, "o", "r", "t", "b", "dup", "main")
	assert.True(t, forge.IsAlreadyExists(err), "%v", err)
	_, err = c.CreateChangeProposal(ctx, "o", "r", "t", "b", "nonum", "main")
	assert.ErrorContains(t, err, "no number")

	cp, err = c.CreateCrossRepoChangeProposal(ctx, "o", "r", "fork", "r", "t", "b", "feat", "main")
	require.NoError(t, err)
	assert.Equal(t, "fork/r", cp.HeadRepo)
	assert.Equal(t, "GET", f.reqs()[3].Method, "the head repository is looked up first")
	assert.Equal(t, "fork:feat", f.reqs()[4].Body["head"])

	_, err = c.CreateCrossRepoChangeProposal(ctx, "o", "r", "stranger", "r", "t", "b", "feat", "main")
	assert.ErrorContains(t, err, `opened from "stranger/other"`)

	cp, err = c.CreateCrossRepoChangeProposal(ctx, "o", "r", "O", "R", "t", "b", "same", "main")
	require.NoError(t, err)
	assert.Equal(t, "same", f.reqs()[7].Body["head"], "the base repository itself is a same-repo pull request")
	assert.Equal(t, 7, cp.Number)

	posts := func() (n int) {
		for _, r := range f.reqs() {
			if r.Method == "POST" && r.Path == "/pulls" {
				n++
			}
		}
		return n
	}
	before := posts()
	for _, head := range [][2]string{{"plain", "r"}, {"elsewhere", "r"}, {"missing", "r"}} {
		_, err = c.CreateCrossRepoChangeProposal(ctx, "o", "r", head[0], head[1], "t", "b", "feat", "main")
		assert.Error(t, err, head[0])
	}
	assert.ErrorContains(t, err, "looking up head repository")
	assert.Equal(t, before, posts(), "a head that is not a fork of the base opens no pull request")

	n := len(f.reqs())
	_, err = c.CreateCrossRepoChangeProposal(ctx, "o", "r", "o", "r-fork", "t", "b", "feat", "main")
	assert.True(t, forge.IsNotSupported(err), "%v", err)
	assert.Len(t, f.reqs(), n, "an unsupported head makes no request")
}

// pagedList serves total items across pages of the requested limit.
func pagedList(total int, item func(i int) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out := []any{}
		for i := (page - 1) * limit; i < min(page*limit, total); i++ {
			out = append(out, item(i))
		}
		respond(200, out)(w, r)
	}
}

func TestListRepoPullRequests(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("GET /pulls", pagedList(51, func(i int) any { return fakePR(i+1, "b", "o/r", "u") }))
	prs, err := c.ListRepoPullRequests(ctx, "o", "r")
	require.NoError(t, err)
	require.Len(t, prs, 51)
	assert.Equal(t, 51, prs[50].Number)
	reqs := f.reqs()
	require.Len(t, reqs, 2)
	assert.Equal(t, "state=open&page=2&limit=50", reqs[1].Query)

	f.handle("GET /pulls", pagedList(1<<30, func(i int) any { return fakePR(i+1, "b", "o/r", "u") }))
	_, err = c.ListRepoPullRequests(ctx, "o", "r")
	assert.ErrorIs(t, err, forge.ErrPaginationLimit)
}

func TestPullRequestReads(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	pr := fakePR(5, "feat", "fork/r", "alice")
	pr["changed_files"] = 3
	pr["updated_at"] = "2026-01-02T03:04:05Z"
	pr["labels"] = []map[string]any{{"name": "ready"}, {"name": "bug"}}
	f.handle("GET /pulls/5", respond(200, pr))
	deleted := fakePR(6, "feat", "", "bob")
	deleted["head"] = map[string]any{"ref": "feat", "sha": "", "repo": nil}
	f.handle("GET /pulls/6", respond(200, deleted))

	info, err := c.GetPullRequestInfo(ctx, "o", "r", 5)
	require.NoError(t, err)
	assert.Equal(t, forge.PullRequestInfo{
		Number: 5, HTMLURL: "https://forgejo.example/forgejo/o/r/pulls/5", HeadRepo: "fork/r", BaseRepo: "o/r",
		HeadRef: "feat", BaseRef: "main", HeadSHA: "headsha", BaseSHA: "basesha", ChangedFiles: 3,
		AuthorID: "alice", IsFork: true,
	}, *info)

	info, err = c.GetPullRequestInfo(ctx, "o", "r", 6)
	require.NoError(t, err)
	assert.True(t, info.IsFork, "a deleted head repository is treated as a fork")

	auth, err := c.GetPullRequestAuthorizationContext(ctx, "o", "r", 5)
	require.NoError(t, err)
	assert.Equal(t, forge.PullRequestAuthorizationContext{AuthorLogin: "alice", UpdatedAt: "2026-01-02T03:04:05Z", Labels: []string{"ready", "bug"}}, *auth)

	sha, err := c.GetPullRequestHeadSHA(ctx, "o", "r", 5)
	require.NoError(t, err)
	assert.Equal(t, "headsha", sha)
	_, err = c.GetPullRequestHeadSHA(ctx, "o", "r", 6)
	assert.ErrorContains(t, err, "no head SHA")

	_, err = c.GetPullRequestInfo(ctx, "o", "r", 99)
	assert.True(t, forge.IsNotFound(err))
	_, err = c.GetPullRequestAuthorizationContext(ctx, "o", "r", 99)
	assert.True(t, forge.IsNotFound(err))
	_, err = c.GetPullRequestHeadSHA(ctx, "o", "r", 99)
	assert.True(t, forge.IsNotFound(err))
}

func TestListPullRequestFilesAndCommits(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("GET /pulls/3/files", pagedList(52, func(i int) any { return map[string]any{"filename": fmt.Sprintf("f%d.go", i)} }))
	f.handle("GET /pulls/3/commits", pagedList(51, func(i int) any { return map[string]any{"sha": fmt.Sprintf("c%d", 50-i)} }))

	files, err := c.ListPullRequestFiles(ctx, "o", "r", 3)
	require.NoError(t, err)
	require.Len(t, files, 52)
	assert.Equal(t, "f51.go", files[51])

	commits, err := c.ListPullRequestCommits(ctx, "o", "r", 3)
	require.NoError(t, err)
	require.Len(t, commits, 51)
	assert.Equal(t, "c0", commits[0], "oldest first")
	assert.Equal(t, "c50", commits[50])
	reqs := f.reqs()
	assert.Equal(t, "page=1&limit=50&verification=false&files=false", reqs[len(reqs)-2].Query)

	f.handle("GET /pulls/4/files", pagedList(1<<30, func(i int) any { return map[string]any{"filename": "x"} }))
	f.handle("GET /pulls/4/commits", pagedList(1<<30, func(i int) any { return map[string]any{"sha": "x"} }))
	_, err = c.ListPullRequestFiles(ctx, "o", "r", 4)
	assert.ErrorIs(t, err, forge.ErrPaginationLimit)
	_, err = c.ListPullRequestCommits(ctx, "o", "r", 4)
	assert.ErrorIs(t, err, forge.ErrPaginationLimit)
	_, err = c.ListPullRequestFiles(ctx, "o", "r", 99)
	assert.True(t, forge.IsNotFound(err))
	_, err = c.ListPullRequestCommits(ctx, "o", "r", 99)
	assert.True(t, forge.IsNotFound(err))
}

const sampleDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 package main
--- not a header
-var a = 1
+var a = 2
@@ -10,2 +10,3 @@ func f() {
 x
+y
diff --git a/old name.txt b/new name.txt
similarity index 90%
rename from old name.txt
rename to new name.txt
index 3333333..4444444 100644
--- a/old name.txt
+++ b/new name.txt
@@ -1 +1 @@
-old
+new
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 5555555..0000000
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..6666666
Binary files /dev/null and b/logo.png differ
diff --git "a/t\303\244st.md" "b/t\303\244st.md"
new file mode 100644
index 0000000..7777777
--- /dev/null
+++ "b/t\303\244st.md"
@@ -0,0 +1 @@
+hi
`

func TestSplitUnifiedDiff(t *testing.T) {
	assert.Equal(t, map[string]string{
		"main.go":      "@@ -1,3 +1,3 @@\n package main\n--- not a header\n-var a = 1\n+var a = 2\n@@ -10,2 +10,3 @@ func f() {\n x\n+y",
		"new name.txt": "@@ -1 +1 @@\n-old\n+new",
		"gone.txt":     "@@ -1 +0,0 @@\n-bye",
		"täst.md":      "@@ -0,0 +1 @@\n+hi",
	}, splitUnifiedDiff(sampleDiff))
	assert.Empty(t, splitUnifiedDiff(""))
}

func TestListPullRequestFileDiffs(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("GET /pulls/2/files", respond(200, []map[string]any{
		{"filename": "main.go", "status": "changed"},
		{"filename": "new name.txt", "previous_filename": "old name.txt", "status": "renamed"},
		{"filename": "gone.txt", "status": "deleted"},
		{"filename": "logo.png", "status": "added"},
		{"filename": "täst.md", "status": "added"},
		{"filename": "copy.go", "previous_filename": "main.go", "status": "copied"},
	}))
	f.handle("GET /pulls/2.diff", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "text/plain", r.Header.Get("Accept"))
		_, _ = w.Write([]byte(sampleDiff))
	})

	diffs, err := c.ListPullRequestFileDiffs(ctx, "o", "r", 2)
	require.NoError(t, err)
	assert.Equal(t, []forge.PullRequestFileDiff{
		{Path: "main.go", Status: "modified", Patch: "@@ -1,3 +1,3 @@\n package main\n--- not a header\n-var a = 1\n+var a = 2\n@@ -10,2 +10,3 @@ func f() {\n x\n+y"},
		{Path: "new name.txt", PreviousPath: "old name.txt", Status: "renamed", Patch: "@@ -1 +1 @@\n-old\n+new"},
		{Path: "gone.txt", Status: "removed", Patch: "@@ -1 +0,0 @@\n-bye"},
		{Path: "logo.png", Status: "added"},
		{Path: "täst.md", Status: "added", Patch: "@@ -0,0 +1 @@\n+hi"},
		{Path: "copy.go", Status: "added"},
	}, diffs)

	_, err = c.ListPullRequestFileDiffs(ctx, "o", "r", 99)
	assert.True(t, forge.IsNotFound(err))
	f.handle("GET /pulls/3/files", respond(200, []map[string]any{{"filename": "a"}}))
	_, err = c.ListPullRequestFileDiffs(ctx, "o", "r", 3)
	assert.True(t, forge.IsNotFound(err), "a missing diff is an error: %v", err)
}

func TestListPullRequestFileDiffsTruncation(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("GET /pulls/1/files", respond(200, []map[string]any{{"filename": "big.txt", "status": "changed"}}))
	f.handle("GET /pulls/1.diff", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -1 +1 @@\n"))
		_, _ = w.Write([]byte(strings.Repeat("+", maxPullDiffBytes)))
	})
	diffs, err := c.ListPullRequestFileDiffs(ctx, "o", "r", 1)
	assert.ErrorIs(t, err, forge.ErrDiffListingTruncated)
	assert.Equal(t, []forge.PullRequestFileDiff{{Path: "big.txt", Status: "modified"}}, diffs)

	f.handle("GET /pulls/2/files", pagedList(1<<30, func(i int) any { return map[string]any{"filename": "a.go", "status": "changed"} }))
	f.handle("GET /pulls/2.diff", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b\n"))
	})
	diffs, err = c.ListPullRequestFileDiffs(ctx, "o", "r", 2)
	assert.ErrorIs(t, err, forge.ErrDiffListingTruncated)
	require.Len(t, diffs, maxPullPages*pullsPageSize)
	assert.Equal(t, "@@ -1 +1 @@\n-a\n+b", diffs[0].Patch)
}

func TestCreatePullRequestReview(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("POST /pulls/4/reviews", respond(200, map[string]any{"id": 1}))

	require.NoError(t, c.CreatePullRequestReview(ctx, "o", "r", 4, "APPROVE", "LGTM", "headsha", nil))
	require.NoError(t, c.CreatePullRequestReview(ctx, "o", "r", 4, "REQUEST_CHANGES", "Fix these", "", []forge.ReviewComment{
		{Path: "a.go", Line: 12, Body: "off by one"},
		{Path: "b.go", Line: 0, Body: "whole file concern"},
	}))
	require.NoError(t, c.CreatePullRequestReview(ctx, "o", "r", 4, "COMMENT", "", "", []forge.ReviewComment{{Path: "c.go", Body: "note"}}))

	reqs := f.reqs()
	require.Len(t, reqs, 3)
	assert.Equal(t, map[string]any{"event": "APPROVED", "body": "LGTM", "commit_id": "headsha"}, reqs[0].Body)
	assert.Equal(t, map[string]any{
		"event": "REQUEST_CHANGES",
		"body":  "Fix these\n\n**b.go**\n\nwhole file concern",
		"comments": []any{
			map[string]any{"path": "a.go", "body": "off by one", "new_position": float64(12)},
		},
	}, reqs[1].Body)
	assert.Equal(t, map[string]any{"event": "COMMENT", "body": "**c.go**\n\nnote"}, reqs[2].Body)

	assert.ErrorContains(t, c.CreatePullRequestReview(ctx, "o", "r", 4, "DISMISS", "", "", nil), "invalid event")
	assert.Len(t, f.reqs(), 3, "an invalid event makes no request")

	f.handle("POST /pulls/5/reviews", apiMsg(403, "cannot approve your own pull request"))
	err := c.CreatePullRequestReview(ctx, "o", "r", 5, "APPROVE", "", "", nil)
	assert.True(t, forge.IsForbidden(err), "%v", err)
}

func TestListAndDismissPullRequestReviews(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("GET /pulls/4/reviews", respond(200, []map[string]any{
		{"id": 1, "user": map[string]any{"login": "a"}, "state": "APPROVED", "body": "ok", "commit_id": "c1", "submitted_at": "2026-01-01T00:00:00Z"},
		{"id": 2, "user": map[string]any{"login": "b"}, "state": "REQUEST_CHANGES", "dismissed": true},
		{"id": 3, "user": map[string]any{"login": "c"}, "state": "COMMENT"},
		{"id": 4, "user": map[string]any{"login": "d"}, "state": "REQUEST_REVIEW"},
		{"id": 5, "user": map[string]any{"login": "e"}, "state": "PENDING"},
		{"id": 6, "user": map[string]any{"login": "f"}, "state": "REQUEST_CHANGES"},
	}))
	reviews, err := c.ListPullRequestReviews(ctx, "o", "r", 4)
	require.NoError(t, err)
	assert.Equal(t, []forge.PullRequestReview{
		{ID: 1, User: "a", State: "APPROVED", Body: "ok", SubmittedAt: "2026-01-01T00:00:00Z", CommitID: "c1"},
		{ID: 2, User: "b", State: "DISMISSED"},
		{ID: 3, User: "c", State: "COMMENTED"},
		{ID: 5, User: "e", State: "PENDING"},
		{ID: 6, User: "f", State: "CHANGES_REQUESTED"},
	}, reviews)

	f.handle("GET /pulls/5/reviews", pagedList(1<<30, func(i int) any { return map[string]any{"id": i, "state": "COMMENT"} }))
	_, err = c.ListPullRequestReviews(ctx, "o", "r", 5)
	assert.ErrorIs(t, err, forge.ErrPaginationLimit)
	_, err = c.ListPullRequestReviews(ctx, "o", "r", 99)
	assert.True(t, forge.IsNotFound(err))

	f.handle("POST /pulls/4/reviews/2/dismissals", respond(200, map[string]any{"id": 2}))
	require.NoError(t, c.DismissPullRequestReview(ctx, "o", "r", 4, 2, "superseded"))
	reqs := f.reqs()
	assert.Equal(t, map[string]any{"message": "superseded"}, reqs[len(reqs)-1].Body)
	assert.True(t, forge.IsNotFound(c.DismissPullRequestReview(ctx, "o", "r", 4, 9, "x")))
}

func TestListPullRequestReviewThreadsIsNotSupported(t *testing.T) {
	f, c := newFakePulls(t)
	page, err := c.ListPullRequestReviewThreads(context.Background(), "o", "r", 1)
	assert.True(t, forge.IsNotSupported(err), "%v", err)
	assert.Empty(t, page.Threads)
	assert.Empty(t, f.reqs(), "an unsupported operation makes no request")
}

func TestCloseMergeAndUpdatePullRequest(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("PATCH /pulls/4", respond(201, fakePR(4, "b", "o/r", "u")))
	f.handle("POST /pulls/4/merge", respond(200, nil))
	f.handle("POST /pulls/5/merge", apiMsg(409, "head out of date"))
	f.handle("POST /pulls/6/merge", apiMsg(405, "not mergeable"))
	f.handle("POST /pulls/4/update", respond(200, nil))
	f.handle("POST /pulls/5/update", apiMsg(409, "merge conflict"))

	require.NoError(t, c.CloseChangeProposal(ctx, "o", "r", 4))
	require.NoError(t, c.MergeChangeProposal(ctx, "o", "r", 4))
	require.NoError(t, c.UpdatePullRequestBranch(ctx, "o", "r", 4))
	reqs := f.reqs()
	assert.Equal(t, map[string]any{"state": "closed"}, reqs[0].Body)
	assert.Equal(t, map[string]any{"Do": "squash"}, reqs[1].Body)
	assert.Equal(t, "style=merge", reqs[2].Query)

	assert.True(t, forge.IsConflict(c.MergeChangeProposal(ctx, "o", "r", 5)))
	err := c.MergeChangeProposal(ctx, "o", "r", 6)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 405, apiErr.StatusCode)
	assert.True(t, forge.IsConflict(c.UpdatePullRequestBranch(ctx, "o", "r", 5)))
	assert.True(t, forge.IsNotFound(c.CloseChangeProposal(ctx, "o", "r", 99)))
}

func TestGetCombinedStatus(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)
	f.handle("GET /commits/nostatus/status", respond(200, map[string]any{"sha": "nostatus", "state": "", "total_count": 0, "statuses": nil}))
	f.handle("GET /commits/mixed/status", respond(200, map[string]any{
		"sha": "mixed", "state": "success", "total_count": 2,
		"statuses": []map[string]any{
			{"context": "ci / lint", "status": "success", "target_url": "https://forgejo.example/run/1"},
			{"context": "ci / test", "status": "failure", "description": "tests failed", "updated_at": "2026-01-01T00:00:00Z"},
		},
	}))
	f.handle("GET /commits/feature%2Fx/status", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		statuses := []map[string]any{}
		n := pullsPageSize
		if page == 2 {
			n = 1
		}
		for i := range n {
			st := "success"
			if page == 2 {
				st = "pending"
			}
			statuses = append(statuses, map[string]any{"context": fmt.Sprintf("c%d-%d", page, i), "status": st})
		}
		respond(200, map[string]any{"sha": "abc", "state": "success", "total_count": pullsPageSize + 1, "statuses": statuses})(w, r)
	})
	f.handle("GET /commits/broken/status", apiMsg(502, "bad gateway"))

	st, err := c.GetCombinedStatus(ctx, "o", "r", "nostatus")
	require.NoError(t, err, "a commit with no statuses is not an error")
	assert.Equal(t, &CombinedStatus{SHA: "nostatus", State: "pending"}, st)

	st, err = c.GetCombinedStatus(ctx, "o", "r", "mixed")
	require.NoError(t, err)
	assert.Equal(t, "failure", st.State)
	assert.Equal(t, []CommitStatus{
		{Context: "ci / lint", State: "success", TargetURL: "https://forgejo.example/run/1"},
		{Context: "ci / test", State: "failure", Description: "tests failed", UpdatedAt: "2026-01-01T00:00:00Z"},
	}, st.Statuses)

	st, err = c.GetCombinedStatus(ctx, "o", "r", "feature/x")
	require.NoError(t, err)
	assert.Equal(t, "pending", st.State, "the combined state covers every page, not only the first")
	assert.Len(t, st.Statuses, pullsPageSize+1)

	_, err = c.GetCombinedStatus(ctx, "o", "r", "broken")
	assert.True(t, forge.IsTransient(err), "%v", err)
	assert.NotContains(t, err.Error(), "secret-token")
	_, err = c.GetCombinedStatus(ctx, "o", "r", "missing")
	assert.True(t, forge.IsNotFound(err))

	f.handle("GET /commits/endless/status", func(w http.ResponseWriter, r *http.Request) {
		statuses := make([]map[string]any, pullsPageSize)
		for i := range statuses {
			statuses[i] = map[string]any{"context": "c", "status": "success"}
		}
		respond(200, map[string]any{"sha": "e", "statuses": statuses})(w, r)
	})
	_, err = c.GetCombinedStatus(ctx, "o", "r", "endless")
	assert.ErrorIs(t, err, forge.ErrPaginationLimit)
}

func TestCombineStatuses(t *testing.T) {
	for _, tc := range []struct {
		states []string
		want   string
	}{
		{nil, "pending"},
		{[]string{"success"}, "success"},
		{[]string{"success", "pending"}, "pending"},
		{[]string{"success", "warning", "pending"}, "warning"},
		{[]string{"failure", "error"}, "error"},
		{[]string{"success", "failure", "warning"}, "failure"},
		{[]string{"success", "unknown"}, "pending"},
	} {
		var statuses []CommitStatus
		for _, s := range tc.states {
			statuses = append(statuses, CommitStatus{State: s})
		}
		assert.Equal(t, tc.want, combineStatuses(statuses), "%v", tc.states)
	}
}

func TestClassify(t *testing.T) {
	plain := fmt.Errorf("plain")
	assert.Equal(t, plain, classify(plain, forge.ErrConflict))
	conflict := &APIError{StatusCode: 409}
	assert.Equal(t, error(conflict), classify(conflict, nil), "409 is unchanged without a sentinel")
	assert.True(t, forge.IsAlreadyExists(classify(conflict, forge.ErrAlreadyExists)))
	assert.True(t, forge.IsForbidden(classify(&APIError{StatusCode: 403}, nil)))
	assert.Equal(t, error(&APIError{StatusCode: 422}), classify(&APIError{StatusCode: 422}, forge.ErrConflict))
}

func TestDotSegmentsAreRejected(t *testing.T) {
	ctx := context.Background()
	f, c := newFakePulls(t)

	_, err := c.GetBranchRef(ctx, "o", "r", "../../../x/y")
	assert.ErrorContains(t, err, "refusing request")
	assert.Error(t, c.DeleteBranch(ctx, "o", "r", "a/./b"))
	_, err = c.GetPullRequestHeadSHA(ctx, "..", "r", 1)
	assert.ErrorContains(t, err, "refusing request")
	_, err = c.GetPullRequestHeadSHA(ctx, "o", ".", 1)
	assert.ErrorContains(t, err, "refusing request")
	// An encoded slash must not hide a dot segment: it decodes to one when a
	// proxy decodes %2F before normalizing.
	_, err = c.GetCombinedStatus(ctx, "o", "r", "../other")
	assert.ErrorContains(t, err, "refusing request")
	_, err = c.GetCombinedStatus(ctx, "o", "r", "a/../b")
	assert.ErrorContains(t, err, "refusing request")
	_, err = c.GetPullRequestHeadSHA(ctx, "o/..", "r", 1)
	assert.ErrorContains(t, err, "refusing request")
	_, err = c.GetPullRequestHeadSHA(ctx, "o", "../x", 1)
	assert.ErrorContains(t, err, "refusing request")
	assert.Empty(t, f.reqs(), "a dot segment makes no request")

	_, err = c.GetBranchRef(ctx, "o", "r", "feature/..x/v1.0")
	assert.True(t, forge.IsNotFound(err), "dots inside a segment are ordinary: %v", err)
	assert.Len(t, f.reqs(), 1)
}
