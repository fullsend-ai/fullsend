package cli

import (
	"os"
	"strings"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

// newGitHubLiveClient builds a GitHub API client, honoring GITHUB_API_URL for tests.
func newGitHubLiveClient(token string) *gh.LiveClient {
	client := gh.New(token)
	if base := strings.TrimSpace(os.Getenv("GITHUB_API_URL")); base != "" {
		client = client.WithBaseURL(base)
	}
	return client
}
