package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	isInstallation, err := gh.ProbeInstallationToken(ctx, tokenScopeClient, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("probing installation token: %w", err)
	}
	if !isInstallation {
		return nil, nil
	}

	url := baseURL + "/installation/repositories?per_page=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating scope request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := tokenScopeClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching token scope: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token scope check returned status %d", resp.StatusCode)
	}

	var result struct {
		TotalCount   int `json:"total_count"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding token scope: %w", err)
	}

	repos := make([]string, len(result.Repositories))
	for i, r := range result.Repositories {
		repos[i] = r.FullName
	}
	if result.TotalCount > len(result.Repositories) {
		repos = append(repos, fmt.Sprintf("... and %d more (%d total)",
			result.TotalCount-len(result.Repositories), result.TotalCount))
	}
	return repos, nil
}
