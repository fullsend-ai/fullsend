package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPostCommentCmd_RequiredFlags(t *testing.T) {
	cmd := newPostCommentCmd()

	// Verify required flags are registered.
	for _, name := range []string{"repo", "number", "marker"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}
}

func TestNewPostCommentCmd_DefaultFlags(t *testing.T) {
	cmd := newPostCommentCmd()

	result := cmd.Flags().Lookup("result")
	require.NotNil(t, result)
	assert.Equal(t, "-", result.DefValue)

	dryRun := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRun)
	assert.Equal(t, "false", dryRun.DefValue)

	token := cmd.Flags().Lookup("token")
	require.NotNil(t, token)
	assert.Contains(t, token.Usage, "GH_TOKEN")
	assert.Contains(t, token.Usage, "GITHUB_TOKEN")
	assert.Contains(t, token.Usage, "gh auth token")
}

func TestPostCommentCmd_MissingTokenUsesSharedError(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")

	cmd := newPostCommentCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"--repo", "owner/repo",
		"--number", "1",
		"--marker", "<!-- fullsend:test -->",
		"--result", "-",
		"--dry-run",
	})
	cmd.SetIn(strings.NewReader("comment body"))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitHub token found")
	assert.Contains(t, err.Error(), "GH_TOKEN")
	assert.Contains(t, err.Error(), "--token")
	assert.Contains(t, err.Error(), "gh auth login")
}

func TestPostCommentCmd_AcceptsGHToken(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "octocat"})
		case strings.Contains(r.URL.Path, "/issues/1/comments"):
			_, _ = io.WriteString(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GH_TOKEN", "gho_from_gh_token")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")

	bodyFile := t.TempDir() + "/body.md"
	require.NoError(t, os.WriteFile(bodyFile, []byte("comment body"), 0o644))

	cmd := newPostCommentCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"--repo", "owner/repo",
		"--number", "1",
		"--marker", "<!-- fullsend:test -->",
		"--result", bodyFile,
		"--dry-run",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "Bearer gho_from_gh_token", sawAuth,
		"post-comment must authenticate with GH_TOKEN, not only GITHUB_TOKEN")
}
