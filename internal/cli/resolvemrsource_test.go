package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// mrAPIServer starts a fake GitLab API server that serves a single
// merge-request response at /api/v4/projects/<escaped path>/merge_requests/<iid>.
func mrAPIServer(t *testing.T, path string, iid int, body map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSplitProjectPath(t *testing.T) {
	owner, name, ok := splitProjectPath("group/project")
	require.True(t, ok)
	assert.Equal(t, "group", owner)
	assert.Equal(t, "project", name)

	owner, name, ok = splitProjectPath("group/subgroup/project")
	require.True(t, ok)
	assert.Equal(t, "group", owner)
	assert.Equal(t, "subgroup/project", name)
	// Round-trips through internal/forge/gitlab's owner+"/"+repo join.
	assert.Equal(t, "group/subgroup/project", owner+"/"+name)

	_, _, ok = splitProjectPath("no-slash")
	assert.False(t, ok)

	_, _, ok = splitProjectPath("/leading-slash")
	assert.False(t, ok)
}

func TestResolveMRSource_SameProjectSuccess(t *testing.T) {
	srv := mrAPIServer(t, "group/project", 5, map[string]any{
		"iid":               5,
		"sha":               "abc123",
		"source_branch":     "feature",
		"target_branch":     "main",
		"source_project_id": 10,
		"target_project_id": 10,
		"author":            map[string]any{"id": 1, "username": "dev"},
	})
	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	result, err := resolveMRSource(context.Background(), client, "group/project", 5)
	require.NoError(t, err)
	assert.Equal(t, "feature", result.SourceBranch)
	assert.Equal(t, "abc123", result.SourceSHA)
	assert.Equal(t, "group/project", result.SourceProjectPath)
}

func TestResolveMRSource_ForkRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iid":               5,
			"sha":               "abc123",
			"source_branch":     "feature",
			"target_branch":     "main",
			"source_project_id": 200,
			"target_project_id": 10,
			"author":            map[string]any{"id": 1, "username": "dev"},
		})
	})
	mux.HandleFunc("/api/v4/projects/200", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path_with_namespace": "fork/project",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	_, err = resolveMRSource(context.Background(), client, "group/project", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-project/fork MR source checkout is not supported")
}

func TestResolveMRSource_MissingSourceBranch(t *testing.T) {
	srv := mrAPIServer(t, "group/project", 5, map[string]any{
		"iid":               5,
		"sha":               "abc123",
		"source_branch":     "",
		"target_branch":     "main",
		"source_project_id": 10,
		"target_project_id": 10,
		"author":            map[string]any{"id": 1, "username": "dev"},
	})
	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	_, err = resolveMRSource(context.Background(), client, "group/project", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no source branch")
}

func TestResolveMRSource_MissingSourceSHA(t *testing.T) {
	srv := mrAPIServer(t, "group/project", 5, map[string]any{
		"iid":               5,
		"sha":               "",
		"source_branch":     "feature",
		"target_branch":     "main",
		"source_project_id": 10,
		"target_project_id": 10,
		"author":            map[string]any{"id": 1, "username": "dev"},
	})
	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	_, err = resolveMRSource(context.Background(), client, "group/project", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no source SHA")
}

func TestResolveMRSource_InvalidProjectPath(t *testing.T) {
	client, err := gl.New("test-token", gl.WithBaseURL("https://gitlab.example.com"))
	require.NoError(t, err)

	_, err = resolveMRSource(context.Background(), client, "no-slash", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace/project format")
}

func TestResolveMRSource_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)

	_, err = resolveMRSource(context.Background(), client, "group/project", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve merge request !5")
}

func TestResolveMRSourceCmd_MissingProject(t *testing.T) {
	t.Setenv("CI_PROJECT_PATH", "")
	cmd := newResolveMRSourceCmd()
	cmd.SetArgs([]string{"--mr-iid", "5"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project or CI_PROJECT_PATH is required")
}

func TestResolveMRSourceCmd_MissingMRIID(t *testing.T) {
	cmd := newResolveMRSourceCmd()
	cmd.SetArgs([]string{"--project", "group/project"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mr-iid must be a positive integer")
}

func TestResolveMRSourceCmd_MissingToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	cmd := newResolveMRSourceCmd()
	cmd.SetArgs([]string{"--project", "group/project", "--mr-iid", "5"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitLab token")
}

func TestResolveMRSourceCmd_PrintsJSON(t *testing.T) {
	srv := mrAPIServer(t, "group/project", 5, map[string]any{
		"iid":               5,
		"sha":               "abc123",
		"source_branch":     "feature",
		"target_branch":     "main",
		"source_project_id": 10,
		"target_project_id": 10,
		"author":            map[string]any{"id": 1, "username": "dev"},
	})
	t.Setenv("GITLAB_TOKEN", "test-token")
	cmd := newResolveMRSourceCmd()
	cmd.SetArgs([]string{"--project", "group/project", "--mr-iid", "5", "--gitlab-url", srv.URL})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	var decoded mrSource
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))
	assert.Equal(t, "feature", decoded.SourceBranch)
	assert.Equal(t, "abc123", decoded.SourceSHA)
	assert.Equal(t, "group/project", decoded.SourceProjectPath)
}
