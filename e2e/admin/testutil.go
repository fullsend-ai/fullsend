//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

const (
	// testOrg is the dedicated GitHub org for e2e tests.
	testOrg = "halfsend"

	// testRepo is a pre-existing repo in the test org for enrollment testing.
	testRepo = "test-repo"

	// lockRepo is the name of the distributed lock repo.
	lockRepo = "e2e-lock"

	// defaultLockTimeout is how long to wait for the lock before giving up.
	defaultLockTimeout = 30 * time.Minute

	// lockPollInterval is how often to poll while waiting for the lock.
	lockPollInterval = 1 * time.Minute

	// freshLockThreshold is the age below which a lock is considered
	// "just acquired" and we reset the wait timer.
	freshLockThreshold = 2 * time.Minute
)

// defaultRoles is the standard set of agent roles.
var defaultRoles = []string{"fullsend", "triage", "coder", "review"}

// envConfig holds required environment configuration.
type envConfig struct {
	username    string
	password    string
	lockTimeout time.Duration
}

// loadEnvConfig reads and validates required env vars. Calls t.Skip if
// credentials are not set (allows running `go test -tags e2e` without
// credentials to check compilation).
func loadEnvConfig(t *testing.T) envConfig {
	t.Helper()

	username := os.Getenv("E2E_GITHUB_USERNAME")
	if username == "" {
		t.Skip("E2E_GITHUB_USERNAME not set, skipping e2e test")
	}

	password := os.Getenv("E2E_GITHUB_PASSWORD")
	if password == "" {
		t.Skip("E2E_GITHUB_PASSWORD not set, skipping e2e test")
	}

	lockTimeout := defaultLockTimeout
	if v := os.Getenv("E2E_LOCK_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("invalid E2E_LOCK_TIMEOUT %q: %v", v, err)
		}
		lockTimeout = d
	}

	return envConfig{
		username:    username,
		password:    password,
		lockTimeout: lockTimeout,
	}
}

// newLiveClient creates a GitHub API client from the token.
func newLiveClient(token string) *gh.LiveClient {
	return gh.New(token)
}

// getRepoCreatedAt fetches a repo's created_at timestamp directly from the
// GitHub REST API. This is intentionally NOT added to forge.Client since it's
// only needed for e2e lock management.
func getRepoCreatedAt(ctx context.Context, token, org, repo string) (time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", org, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("fetching repo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return time.Time{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, fmt.Errorf("decoding response: %w", err)
	}

	return result.CreatedAt, nil
}
