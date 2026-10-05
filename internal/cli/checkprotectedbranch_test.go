package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// protectedBranchCalls tracks how many times each endpoint registered by
// protectedBranchAPIServer was actually invoked. GetProtectedBranch always
// calls both the exact-name lookup and the wildcard listing unconditionally
// (internal/forge/gitlab/ci.go), so tests assert both were hit — an
// unmatched exact-name route also returns the mux's default 404, so a
// passing "not protected" assertion alone does not prove the intended
// exact-name handler (rather than some other 404) was exercised.
type protectedBranchCalls struct {
	exact int32
	list  int32
}

func (c *protectedBranchCalls) exactCalled() bool { return atomic.LoadInt32(&c.exact) > 0 }
func (c *protectedBranchCalls) listCalled() bool  { return atomic.LoadInt32(&c.list) > 0 }

// protectedBranchAPIServer starts a fake GitLab API server exposing the
// two endpoints GetProtectedBranch calls: the exact-name lookup
// (/protected_branches/<branch>) and the paginated listing
// (/protected_branches) used for wildcard matching. exactStatus/exactBody
// control the exact-name response; wildcardNames lists additional
// protected-branch patterns (e.g. "release-*") returned by the listing.
// The returned *protectedBranchCalls lets callers assert both endpoints
// were actually invoked, not just that the overall result matched.
func protectedBranchAPIServer(t *testing.T, exactStatus int, wildcardNames ...string) (*httptest.Server, *protectedBranchCalls) {
	t.Helper()
	calls := &protectedBranchCalls{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls.exact, 1)
		if exactStatus == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(exactStatus)
		if exactStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{"name":"main","push_access_levels":[{"access_level":40}],"merge_access_levels":[{"access_level":40}]}`))
		}
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls.list, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("["))
		for i, name := range wildcardNames {
			if i > 0 {
				w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(`{"name":"` + name + `","push_access_levels":[],"merge_access_levels":[]}`))
		}
		w.Write([]byte("]"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, calls
}

func TestCheckProtectedBranch_NotProtected(t *testing.T) {
	srv, calls := protectedBranchAPIServer(t, http.StatusNotFound)
	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	err = checkProtectedBranch(context.Background(), client, "group/project", "feature")
	assert.NoError(t, err)
	assert.True(t, calls.exactCalled(), "exact-name endpoint must be exercised")
	assert.True(t, calls.listCalled(), "wildcard listing endpoint must be exercised")
}

func TestCheckProtectedBranch_ExactRuleFailsClosed(t *testing.T) {
	srv, calls := protectedBranchAPIServer(t, http.StatusOK)
	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	err = checkProtectedBranch(context.Background(), client, "group/project", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected")
	assert.True(t, calls.exactCalled(), "exact-name endpoint must be exercised")
}

func TestCheckProtectedBranch_WildcardRuleFailsClosed(t *testing.T) {
	// No exact-name rule for "release-1.0" (404), but a wildcard rule
	// "release-*" matches it — GetProtectedBranch unions both lookups,
	// so this must still fail closed.
	srv, calls := protectedBranchAPIServer(t, http.StatusNotFound, "release-*")
	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	err = checkProtectedBranch(context.Background(), client, "group/project", "release-1.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected")
	assert.True(t, calls.exactCalled(), "exact-name endpoint must be exercised")
	assert.True(t, calls.listCalled(), "wildcard listing endpoint must be exercised")
}

func TestCheckProtectedBranch_InvalidProjectPathFailsClosed(t *testing.T) {
	err := checkProtectedBranch(context.Background(), nil, "no-slash", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace/project")
}

type erroringProtectedBranchClient struct {
	forge.Client
}

func (erroringProtectedBranchClient) IsProtectedBranch(ctx context.Context, owner, repo, branch string) (bool, error) {
	return false, errors.New("boom")
}

func TestCheckProtectedBranch_APIErrorFailsClosed(t *testing.T) {
	err := checkProtectedBranch(context.Background(), erroringProtectedBranchClient{}, "group/project", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestCheckProtectedBranchCmd_MissingProject(t *testing.T) {
	cmd := newCheckProtectedBranchCmd()
	cmd.SetArgs([]string{"--branch", "main"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestCheckProtectedBranchCmd_MissingBranch(t *testing.T) {
	cmd := newCheckProtectedBranchCmd()
	cmd.SetArgs([]string{"--project", "group/project"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--branch is required")
}

func TestCheckProtectedBranchCmd_MissingToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	cmd := newCheckProtectedBranchCmd()
	cmd.SetArgs([]string{"--project", "group/project", "--branch", "main"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitLab token")
}

func TestCheckProtectedBranchCmd_SucceedsWhenNotProtected(t *testing.T) {
	srv, calls := protectedBranchAPIServer(t, http.StatusNotFound)
	t.Setenv("GITLAB_TOKEN", "test-token")
	cmd := newCheckProtectedBranchCmd()
	cmd.SetArgs([]string{"--project", "group/project", "--branch", "feature", "--gitlab-url", srv.URL})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())
	assert.True(t, calls.exactCalled(), "exact-name endpoint must be exercised")
	assert.True(t, calls.listCalled(), "wildcard listing endpoint must be exercised")
}

func TestCheckProtectedBranchCmd_FailsWhenProtected(t *testing.T) {
	srv, calls := protectedBranchAPIServer(t, http.StatusOK)
	t.Setenv("GITLAB_TOKEN", "test-token")
	cmd := newCheckProtectedBranchCmd()
	cmd.SetArgs([]string{"--project", "group/project", "--branch", "main", "--gitlab-url", srv.URL})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected")
	assert.True(t, calls.exactCalled(), "exact-name endpoint must be exercised")
}
