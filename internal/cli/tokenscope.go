package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var tokenScopeClient = &http.Client{Timeout: 10 * time.Second}

// fetchTokenScope introspects a GitHub installation token by calling
// GET /installation/repositories and returning the full_name of each
// accessible repo. Returns (nil, nil) if the token is empty.
func fetchTokenScope(ctx context.Context, token, baseURL string) ([]string, error) {
	if token == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

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

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		// PATs and GITHUB_TOKENs can't call /installation/repositories.
		// Not an error — just means this isn't an installation token.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, nil
	}
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
