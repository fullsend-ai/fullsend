package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

var tokenScopeClient = &http.Client{Timeout: 10 * time.Second}

// fetchTokenScope introspects a GitHub installation token by calling
// GET /installation/repositories and returning the full_name of each
// accessible repo. Returns (nil, nil) when the token is empty or not an
// installation token.
func fetchTokenScope(ctx context.Context, token, baseURL string) ([]string, error) {
	if token == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	repos, totalCount, isInstallation, err := gh.ListInstallationRepositories(ctx, tokenScopeClient, baseURL, token, 100)
	if err != nil {
		return nil, fmt.Errorf("fetching token scope: %w", err)
	}
	if !isInstallation {
		return nil, nil
	}

	if totalCount > len(repos) {
		repos = append(repos, fmt.Sprintf("... and %d more (%d total)",
			totalCount-len(repos), totalCount))
	}
	return repos, nil
}
