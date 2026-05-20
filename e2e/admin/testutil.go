//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

const (
	// testRepo is a pre-existing repo in the test org for enrollment testing.
	testRepo = "test-repo"

	// lockRepo is the name of the distributed lock repo.
	lockRepo = "e2e-lock"

	// defaultLockTimeout is how long to wait for the lock before giving up.
	// This is only used as the fallback if ALL orgs are locked.
	defaultLockTimeout = 2 * time.Minute

	// lockPollInterval is how often to poll while waiting for the lock.
	lockPollInterval = 30 * time.Second

	// freshLockThreshold is the age below which a lock is considered
	// "just acquired" and we reset the wait timer.
	freshLockThreshold = 1 * time.Minute
)

// orgPool is the set of GitHub orgs available for parallel e2e test runs.
// Each run acquires a lock on one org before proceeding.
var orgPool = []string{
	"halfsend-01",
	// "halfsend-02", // not yet enrolled in mint
	// "halfsend-03", // not yet enrolled in mint
	// "halfsend-04", // not yet enrolled in mint
	// "halfsend-05", // not yet enrolled in mint
	// "halfsend-06", // not yet enrolled in mint
}

// acquireOrg scans the org pool for an unlocked org and acquires its lock.
// If all orgs are locked, it falls back to waiting on each org in sequence,
// bounded by a single shared deadline (not per-org). Returns the org name.
func acquireOrg(ctx context.Context, client forge.Client, token, runID string, timeout time.Duration, logf func(string, ...any)) (string, error) {
	// First pass: try each org without waiting. If a lock exists but is
	// stale (older than timeout), force-acquire it so we don't waste pool
	// capacity on crashed runs.
	for _, org := range orgPool {
		logf("[org-pool] Trying to acquire %s...", org)
		acquired, err := tryCreateLock(ctx, client, org, runID, logf)
		if err != nil {
			logf("[org-pool] Error trying %s: %v", org, err)
			continue
		}
		if acquired {
			logf("[org-pool] Acquired %s", org)
			return org, nil
		}
		// Lock exists — check if it's stale and force-acquire if so.
		if token != "" {
			if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, timeout, logf); reclaimed {
				return org, nil
			}
		}
		logf("[org-pool] %s is locked, trying next", org)
	}

	// All orgs are locked. Fall back to waiting with a shared deadline
	// across all orgs so total wait is bounded by timeout, not N*timeout.
	logf("[org-pool] All %d orgs are locked, waiting with total timeout %s", len(orgPool), timeout)
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for _, org := range orgPool {
		if deadlineCtx.Err() != nil {
			break
		}
		// Pass remaining context time, not the full timeout, so that
		// each org only uses its fair share of the shared deadline.
		err := acquireLock(deadlineCtx, client, token, org, runID, timeout, logf)
		if err == nil {
			return org, nil
		}
		lastErr = err
		logf("[org-pool] Could not acquire %s: %v", org, err)
	}

	if lastErr != nil {
		return "", fmt.Errorf("could not acquire any org from pool after %s (tried %d orgs): %w", timeout, len(orgPool), lastErr)
	}
	return "", fmt.Errorf("could not acquire any org from pool after %s (tried %d orgs)", timeout, len(orgPool))
}

// defaultRoles is the standard set of agent roles.
var defaultRoles = []string{"fullsend", "triage", "coder", "review"}

// e2eAppSet is the app set prefix used by the shared public GitHub Apps.
const e2eAppSet = "fullsend-ai"

// envConfig holds required environment configuration.
type envConfig struct {
	sessionFile  string
	password     string
	totpSecret   string
	mintURL      string
	gcpProjectID string
	lockTimeout  time.Duration
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

	mintURL := os.Getenv("E2E_MINT_URL")
	if mintURL == "" {
		t.Skip("E2E_MINT_URL not set, skipping e2e test")
	}

	gcpProjectID := os.Getenv("E2E_GCP_PROJECT_ID")

	lockTimeout := defaultLockTimeout
	if v := os.Getenv("E2E_LOCK_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("invalid E2E_LOCK_TIMEOUT %q: %v", v, err)
		}
		lockTimeout = d
	}

	return envConfig{
		sessionFile:  sessionFile,
		password:     password,
		totpSecret:   totpSecret,
		mintURL:      mintURL,
		gcpProjectID: gcpProjectID,
		lockTimeout:  lockTimeout,
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

// buildCLIBinary compiles the fullsend CLI binary once per test run.
func buildCLIBinary(t *testing.T) string {
	t.Helper()
	modRoot, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("finding module root: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "fullsend")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/fullsend/")
	cmd.Dir = strings.TrimSpace(string(modRoot))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building fullsend binary: %s\n%s", err, out)
	}
	return binary
}

// runCLI executes the fullsend CLI with the given args, passing GITHUB_TOKEN.
func runCLI(t *testing.T, binary, token string, args ...string) string {
	t.Helper()
	t.Logf("[cli] fullsend %s", strings.Join(args, " "))
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+token)
	out, err := cmd.CombinedOutput()
	output := string(out)
	t.Logf("[cli] output:\n%s", output)
	if err != nil {
		t.Fatalf("[cli] fullsend %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

// retryOnNotFound retries an operation up to maxAttempts times with linear
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
