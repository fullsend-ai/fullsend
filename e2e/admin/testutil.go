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

	"github.com/fullsend-ai/fullsend/internal/forge"
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
	defaultLockTimeout = 2 * time.Minute

	// lockPollInterval is how often to poll while waiting for the lock.
	lockPollInterval = 30 * time.Second

	// freshLockThreshold is the age below which a lock is considered
	// "just acquired" and we reset the wait timer.
	freshLockThreshold = 1 * time.Minute
)

// defaultRoles is the standard set of agent roles.
var defaultRoles = []string{"fullsend", "triage", "coder", "review"}

// envConfig holds required environment configuration.
type envConfig struct {
	sessionFile string
	password    string
	totpSecret  string
	lockTimeout time.Duration
}

// loadEnvConfig reads and validates required env vars. Calls t.Skip if
// credentials are not set (allows running `go test -tags e2e` without
// credentials to check compilation).
func loadEnvConfig(t *testing.T) envConfig {
	t.Helper()

	sessionFile := os.Getenv("E2E_GITHUB_SESSION_FILE")
	if sessionFile == "" {
		t.Skip("E2E_GITHUB_SESSION_FILE not set, skipping e2e test")
	}
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("E2E_GITHUB_SESSION_FILE %q does not exist: %v", sessionFile, err)
	}

	password := os.Getenv("E2E_GITHUB_PASSWORD")
	totpSecret := os.Getenv("E2E_GITHUB_TOTP_SECRET")

	lockTimeout := defaultLockTimeout
	if v := os.Getenv("E2E_LOCK_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("invalid E2E_LOCK_TIMEOUT %q: %v", v, err)
		}
		lockTimeout = d
	}

	return envConfig{
		sessionFile: sessionFile,
		password:    password,
		totpSecret:  totpSecret,
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

// e2eDispatcher is a no-op dispatch.Dispatcher for e2e tests. It returns a
// dummy mint URL so the OIDC dispatch layer can create org variables without
// provisioning real cloud infrastructure.
type e2eDispatcher struct{}

func (d *e2eDispatcher) Name() string { return "e2e-test" }

func (d *e2eDispatcher) Provision(_ context.Context) (map[string]string, error) {
	return map[string]string{"FULLSEND_MINT_URL": "https://e2e-test.example.com/mint"}, nil
}

func (d *e2eDispatcher) StoreAgentPEM(_ context.Context, _, _ string, _ []byte) error { return nil }

func (d *e2eDispatcher) OrgSecretNames() []string { return nil }

func (d *e2eDispatcher) OrgVariableNames() []string { return []string{"FULLSEND_MINT_URL"} }

// retryOnNotFound retries an operation up to maxAttempts times with exponential
// backoff when it returns a not-found error (GitHub eventual consistency).
func retryOnNotFound(ctx context.Context, maxAttempts int, fn func() error) error {
	var err error
	for i := range maxAttempts {
		if i > 0 {
			select {
			case <-time.After(time.Duration(i+1) * 2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err = fn()
		if err == nil || !forge.IsNotFound(err) {
			return err
		}
	}
	return err
}
