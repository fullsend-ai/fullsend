package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubAPIBaseURL_DefaultsToGitHubCom(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "")
	assert.Equal(t, "https://api.github.com", githubAPIBaseURL())
	assert.Equal(t, "https://api.github.com", NewLiveClient("token").BaseURL())
}

func TestGitHubAPIBaseURL_HonoursGitHubAPIURL(t *testing.T) {
	t.Setenv("GITHUB_API_URL", " https://github.example/api/v3/ ")
	assert.Equal(t, "https://github.example/api/v3", githubAPIBaseURL())
	assert.Equal(t, "https://github.example/api/v3", NewLiveClient("token").BaseURL())
}

// The harness's raw REST calls follow GITHUB_API_URL too, not only the
// forge client.
func TestRawRequestsUseGitHubAPIURL(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created_at":"2026-11-07T12:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL+"/api/v3")

	created, err := getRepoCreatedAt(context.Background(), "token", "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, "/api/v3/repos/org/repo", gotPath)
	assert.Equal(t, "Bearer token", gotAuth)
	assert.True(t, created.Equal(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)))
}
