package forgejo

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// gitAPI serves canned git data API responses under a path prefix and
// records each request's path and query.
type gitAPI struct {
	mu       sync.Mutex
	routes   map[string]string // escaped path + "?" + query -> JSON body
	requests []string
}

func (g *gitAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := r.URL.EscapedPath() + "?" + r.URL.RawQuery
	g.requests = append(g.requests, key)
	body, ok := g.routes[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"object does not exist"}`))
		return
	}
	_, _ = w.Write([]byte(body))
}

func newGitAPIClient(t *testing.T, routes map[string]string) (*LiveClient, *gitAPI) {
	t.Helper()
	api := &gitAPI{routes: routes}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/forgejo", "tok", srv.Client())
	require.NoError(t, err)
	return c, api
}

const repoAPI = "/forgejo/api/v1/repos/acme/agents"

func treeQuery(recursive bool, page int) string {
	return fmt.Sprintf("?page=%d&per_page=1000&recursive=%t", page, recursive)
}

func TestClient_GetCommitSHA(t *testing.T) {
	sha := strings.Repeat("a", 40)
	c, api := newGitAPIClient(t, map[string]string{
		repoAPI + "/git/commits/" + sha + "?stat=false&verification=false&files=false": `{"sha":"` + sha + `"}`,
		repoAPI + "/git/commits/empty?stat=false&verification=false&files=false":       `{}`,
	})

	got, err := c.GetCommitSHA(context.Background(), "acme", "agents", sha)
	require.NoError(t, err)
	assert.Equal(t, sha, got)
	assert.Equal(t, []string{repoAPI + "/git/commits/" + sha + "?stat=false&verification=false&files=false"}, api.requests)

	_, err = c.GetCommitSHA(context.Background(), "acme", "agents", strings.Repeat("b", 40))
	assert.ErrorIs(t, err, forge.ErrNotFound)

	_, err = c.GetCommitSHA(context.Background(), "acme", "agents", "empty")
	assert.ErrorContains(t, err, "response has no SHA")
}

func TestClient_GetTree(t *testing.T) {
	c, api := newGitAPIClient(t, map[string]string{
		repoAPI + "/git/trees/root" + treeQuery(false, 1): `{"tree":[{"path":"harness","mode":"040000","type":"tree","sha":"t1"}],"truncated":false}`,
		repoAPI + "/git/trees/big" + treeQuery(true, 1):   `{"tree":[{"path":"a","mode":"100644","type":"blob","size":1,"sha":"b1"}],"truncated":true}`,
		repoAPI + "/git/trees/big" + treeQuery(true, 2):   `{"tree":[{"path":"b","mode":"100755","type":"blob","size":2,"sha":"b2"}],"truncated":false}`,
		repoAPI + "/git/trees/stuck" + treeQuery(true, 1): `{"tree":[],"truncated":true}`,
	})
	ctx := context.Background()

	got, err := c.GetTree(ctx, "acme", "agents", "root", false, 10)
	require.NoError(t, err)
	assert.Equal(t, []TreeEntry{{Path: "harness", Mode: "040000", Type: "tree", SHA: "t1"}}, got)

	got, err = c.GetTree(ctx, "acme", "agents", "big", true, 10)
	require.NoError(t, err, "a truncated listing is read page by page")
	assert.Equal(t, []TreeEntry{
		{Path: "a", Mode: "100644", Type: "blob", Size: 1, SHA: "b1"},
		{Path: "b", Mode: "100755", Type: "blob", Size: 2, SHA: "b2"},
	}, got)
	assert.Contains(t, api.requests, repoAPI+"/git/trees/big"+treeQuery(true, 2))

	_, err = c.GetTree(ctx, "acme", "agents", "big", true, 1)
	assert.ErrorContains(t, err, "more than 1 entries")

	_, err = c.GetTree(ctx, "acme", "agents", "stuck", true, 10)
	assert.ErrorContains(t, err, "truncated but empty")

	_, err = c.GetTree(ctx, "acme", "agents", "missing", false, 10)
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestClient_GetBlob(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte("hello"))
	c, _ := newGitAPIClient(t, map[string]string{
		repoAPI + "/git/blobs/ok?":       `{"content":"` + enc + `","encoding":"base64","size":5}`,
		repoAPI + "/git/blobs/wrapped?":  `{"content":"aGVs\nbG8=","encoding":"base64","size":5}`,
		repoAPI + "/git/blobs/liar?":     `{"content":"` + enc + `","encoding":"base64","size":1}`,
		repoAPI + "/git/blobs/plain?":    `{"content":"hello","encoding":"utf-8","size":5}`,
		repoAPI + "/git/blobs/garbled?":  `{"content":"!!!","encoding":"base64","size":3}`,
		repoAPI + "/git/blobs/declared?": `{"content":"","encoding":"base64","size":100}`,
	})
	ctx := context.Background()

	for _, sha := range []string{"ok", "wrapped"} {
		got, err := c.GetBlob(ctx, "acme", "agents", sha, 5)
		require.NoError(t, err, sha)
		assert.Equal(t, "hello", string(got), sha)
	}

	_, err := c.GetBlob(ctx, "acme", "agents", "declared", 10)
	assert.ErrorContains(t, err, "over the 10-byte limit", "the declared size is checked before decoding")
	_, err = c.GetBlob(ctx, "acme", "agents", "liar", 4)
	assert.ErrorContains(t, err, "exceeds the 4-byte limit", "the decoded size is checked too")
	_, err = c.GetBlob(ctx, "acme", "agents", "plain", 10)
	assert.ErrorContains(t, err, "unsupported content encoding")
	_, err = c.GetBlob(ctx, "acme", "agents", "garbled", 10)
	assert.ErrorContains(t, err, "decoding content")
	_, err = c.GetBlob(ctx, "acme", "agents", "missing", 10)
	assert.ErrorIs(t, err, forge.ErrNotFound)
}
