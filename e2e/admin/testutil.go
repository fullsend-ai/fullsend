//go:build e2e || behaviour

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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
	defaultLockTimeout = 10 * time.Minute

	// lockPollInterval is how often to poll while waiting for the lock.
	lockPollInterval = 30 * time.Second

	// freshLockThreshold is the age below which a lock is considered
	// "just acquired" and we reset the wait timer.
	freshLockThreshold = 1 * time.Minute

	// staleLockTimeout is the age above which a lock from a crashed run
	// is considered stale and eligible for force-reclaim. Must be longer
	// than the longest expected e2e run (~7 min) but shorter than the
	// job timeout (30 min).
	staleLockTimeout = 15 * time.Minute
)

// orgPool is the set of GitHub orgs available for parallel e2e test runs.
// Each run acquires a lock on one org before proceeding.
var orgPool = []string{
	"halfsend-01",
	"halfsend-02",
	"halfsend-03",
	"halfsend-04",
	"halfsend-05",
	"halfsend-06",
	"halfsend-07",
	"halfsend-08",
	"halfsend-09",
	"halfsend-10",
	"halfsend-11",
	"halfsend-12",
}

// acquireOrg scans the pool for an unlocked org and acquires its lock.
// Returns the org name and token used to hold the lock.
func acquireOrg(ctx context.Context, cfg envConfig, runID string, pool []string, timeout time.Duration, logf func(string, ...any)) (string, string, error) {
	// Shuffle the pool so concurrent runners don't all compete for the
	// same first org (thundering herd).
	shuffled := make([]string, len(pool))
	copy(shuffled, pool)
	rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	// First pass: try each org without waiting. If a lock exists but is
	// stale (older than staleLockTimeout), force-acquire it so we don't
	// waste pool capacity on crashed runs.
	for _, org := range shuffled {
		logf("[org-pool] Trying to acquire %s...", org)
		token, tokErr := tokenForOrg(ctx, cfg, org)
		if tokErr != nil {
			logf("[org-pool] Could not get token for %s: %v", org, tokErr)
			continue
		}
		client := newLiveClient(token)

		acquired, err := tryCreateLock(ctx, client, org, runID, logf)
		if err != nil {
			logf("[org-pool] Error trying %s: %v", org, err)
			// Rate limits are per-user, not per-org — trying more orgs
			// just burns quota and delays recovery. Break immediately.
			if gh.IsRateLimitError(err) {
				logf("[org-pool] Hit rate limit, skipping remaining orgs this round")
				break
			}
			// Only attempt stale lock recovery for 422 errors (repo
			// likely exists). Auth failures and network errors would
			// just waste more API quota.
			var apiErr *gh.APIError
			if token != "" && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
				logf("[org-pool] 422 on %s — will check for stale lock", org)
				if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
					return org, token, nil
				}
			}
			continue
		}
		if acquired {
			logf("[org-pool] Acquired %s", org)
			return org, token, nil
		}
		// Lock exists — check if it's stale and force-acquire if so.
		if token != "" {
			if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
				return org, token, nil
			}
		}
		logf("[org-pool] %s is locked, trying next", org)
	}

	// All orgs are locked. Round-robin poll until one frees up or
	// the shared deadline expires.
	logf("[org-pool] All %d orgs are locked, polling with timeout %s", len(pool), timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := min(lockPollInterval, remaining)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		for _, org := range shuffled {
			token, tokErr := tokenForOrg(ctx, cfg, org)
			if tokErr != nil {
				logf("[org-pool] Could not get token for %s: %v", org, tokErr)
				continue
			}
			client := newLiveClient(token)

			acquired, err := tryCreateLock(ctx, client, org, runID, logf)
			if err != nil {
				logf("[org-pool] Error trying %s: %v", org, err)
				if gh.IsRateLimitError(err) {
					logf("[org-pool] Hit rate limit, skipping remaining orgs this round")
					break
				}
				var apiErr *gh.APIError
				if token != "" && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
					logf("[org-pool] 422 on %s — will check for stale lock", org)
					if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
						return org, token, nil
					}
				}
				continue
			}
			if acquired {
				logf("[org-pool] Acquired %s", org)
				return org, token, nil
			}
			// Also try stale reclaim during polling — a lock may
			// have aged past staleLockTimeout since the first pass.
			if token != "" {
				if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
					return org, token, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("could not acquire any org from pool after %s (tried %d orgs)", timeout, len(pool))
}

// acquireOrgWithClient runs pool acquisition using a fixed client (unit tests).
func acquireOrgWithClient(ctx context.Context, client forge.Client, token, runID string, pool []string, timeout time.Duration, logf func(string, ...any)) (string, error) {
	org, _, err := acquireOrgFromClient(ctx, client, token, runID, pool, timeout, logf)
	return org, err
}

func acquireOrgFromClient(ctx context.Context, client forge.Client, token, runID string, pool []string, timeout time.Duration, logf func(string, ...any)) (string, string, error) {
	shuffled := make([]string, len(pool))
	copy(shuffled, pool)
	rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	for _, org := range shuffled {
		logf("[org-pool] Trying to acquire %s...", org)
		acquired, err := tryCreateLock(ctx, client, org, runID, logf)
		if err != nil {
			logf("[org-pool] Error trying %s: %v", org, err)
			if gh.IsRateLimitError(err) {
				logf("[org-pool] Hit rate limit, skipping remaining orgs this round")
				break
			}
			var apiErr *gh.APIError
			if token != "" && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
				if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
					return org, token, nil
				}
			}
			continue
		}
		if acquired {
			return org, token, nil
		}
		if token != "" {
			if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
				return org, token, nil
			}
		}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := min(lockPollInterval, remaining)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		for _, org := range shuffled {
			acquired, err := tryCreateLock(ctx, client, org, runID, logf)
			if err != nil {
				if gh.IsRateLimitError(err) {
					logf("[org-pool] Hit rate limit, skipping remaining orgs this round")
					break
				}
				var apiErr *gh.APIError
				if token != "" && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
					if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
						return org, token, nil
					}
				}
				continue
			}
			if acquired {
				return org, token, nil
			}
			if token != "" {
				if reclaimed := tryReclaimStaleLock(ctx, client, token, org, runID, logf); reclaimed {
					return org, token, nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("could not acquire any org from pool after %s (tried %d orgs)", timeout, len(pool))
}

// defaultRoles is the standard set of agent roles.
var defaultRoles = []string{"fullsend", "triage", "coder", "review", "retro", "prioritize"}

// e2eAppSet is the app set prefix used by the shared public GitHub Apps.
const e2eAppSet = "fullsend-ai"

// envConfig holds required environment configuration.
type envConfig struct {
	mintURL      string
	useMint      bool
	gcpProjectID string
	wifProvider  string
	lockTimeout  time.Duration
}

// EnvConfig is the exported view of envConfig for behaviour tests.
type EnvConfig struct {
	MintURL      string
	UseMint      bool
	GCPProjectID string
	WIFProvider  string
	LockTimeout  time.Duration
}

func (c envConfig) exported() EnvConfig {
	return EnvConfig{
		MintURL:      c.mintURL,
		UseMint:      c.useMint,
		GCPProjectID: c.gcpProjectID,
		WIFProvider:  c.wifProvider,
		LockTimeout:  c.lockTimeout,
	}
}

func (c EnvConfig) internal() envConfig {
	return envConfig{
		mintURL:      c.MintURL,
		useMint:      c.UseMint,
		gcpProjectID: c.GCPProjectID,
		wifProvider:  c.WIFProvider,
		lockTimeout:  c.LockTimeout,
	}
}

// LoadEnvConfig reads and validates required env vars for e2e and behaviour tests.
func LoadEnvConfig(t *testing.T) EnvConfig {
	return loadEnvConfig(t).exported()
}

// loadEnvConfig reads and validates required env vars. Calls t.Skip if
// credentials are not set (allows running `go test -tags e2e` without
// credentials to check compilation).
func loadEnvConfig(t *testing.T) envConfig {
	t.Helper()

	mintURL := resolveMintURL()
	useMint := runningInGitHubActions()
	if !useMint {
		if _, err := resolveLocalToken(); err != nil {
			t.Skip("no local GitHub token (gh auth login), skipping e2e test")
		}
	}

	gcpProjectID := os.Getenv("E2E_GCP_PROJECT_ID")
	wifProvider := os.Getenv("E2E_GCP_WIF_PROVIDER")

	lockTimeout := defaultLockTimeout
	if v := os.Getenv("E2E_LOCK_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("invalid E2E_LOCK_TIMEOUT %q: %v", v, err)
		}
		lockTimeout = d
	}

	return envConfig{
		mintURL:      mintURL,
		useMint:      useMint,
		gcpProjectID: gcpProjectID,
		wifProvider:  wifProvider,
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

func ensureRepoLabel(ctx context.Context, token, owner, repo, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/labels", owner, repo)
	payload, err := json.Marshal(map[string]string{
		"name":  label,
		"color": "5319e7",
	})
	if err != nil {
		return fmt.Errorf("encoding label payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating label request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("creating repo label: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusUnprocessableEntity {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d creating label %q: %s", resp.StatusCode, label, body)
}

func addIssueLabel(ctx context.Context, token, owner, repo string, issueNum int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/labels", owner, repo, issueNum)
	payload, err := json.Marshal(map[string][]string{"labels": {label}})
	if err != nil {
		return fmt.Errorf("encoding issue label payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating issue label request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("adding issue label: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d adding label %q: %s", resp.StatusCode, label, body)
}

// BuildCLIBinary compiles the fullsend CLI binary once per test run.
func BuildCLIBinary(t *testing.T) string {
	return buildCLIBinary(t)
}

// RunCLI executes the fullsend CLI with the given args, passing GITHUB_TOKEN.
func RunCLI(t *testing.T, binary, token string, args ...string) string {
	return runCLI(t, binary, token, args...)
}

// TryRunCLI is like RunCLI but returns an error instead of calling t.Fatalf.
func TryRunCLI(binary, token string, args ...string) (string, error) {
	modRoot, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("finding module root: %w", err)
	}
	dir := strings.TrimSpace(string(modRoot))
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+token, "CI=true")
	out, runErr := cmd.CombinedOutput()
	output := string(out)
	if runErr != nil {
		return output, fmt.Errorf("[cli] fullsend %s failed: %w\n%s", strings.Join(args, " "), runErr, output)
	}
	return output, nil
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
// By default the working directory is the module root. Use runCLIFromDir to
// run from a subdirectory (GOMOD discovery makes this work for vendoring).
func runCLI(t *testing.T, binary, token string, args ...string) string {
	return runCLIFromDir(t, binary, token, moduleRoot(t), args...)
}

// runCLIFromDir runs the CLI with cwd set to dir.
func runCLIFromDir(t *testing.T, binary, token, dir string, args ...string) string {
	t.Helper()
	t.Logf("[cli] fullsend %s (cwd=%s)", strings.Join(args, " "), dir)

	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+token, "CI=true")
	out, runErr := cmd.CombinedOutput()
	output := string(out)
	t.Logf("[cli] output:\n%s", output)
	if runErr != nil {
		t.Fatalf("[cli] fullsend %s failed: %v\n%s", strings.Join(args, " "), runErr, output)
	}
	return output
}

// tryRunCLI is like runCLI but returns an error instead of calling t.Fatalf.
// Use this when the caller needs to retry on transient failures (e.g., GitHub
// propagation delays after repo creation).
func tryRunCLI(t *testing.T, binary, token string, args ...string) (string, error) {
	t.Helper()
	dir := moduleRoot(t)
	t.Logf("[cli] fullsend %s (cwd=%s)", strings.Join(args, " "), dir)

	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+token, "CI=true")
	out, runErr := cmd.CombinedOutput()
	output := string(out)
	t.Logf("[cli] output:\n%s", output)
	if runErr != nil {
		return output, fmt.Errorf("[cli] fullsend %s failed: %w\n%s", strings.Join(args, " "), runErr, output)
	}
	return output, nil
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	modRoot, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("finding module root: %v", err)
	}
	return strings.TrimSpace(string(modRoot))
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

// AcquireOrg exports org pool acquisition for behaviour tests.
func AcquireOrg(ctx context.Context, cfg EnvConfig, runID string, pool []string, timeout time.Duration, logf func(string, ...any)) (string, string, error) {
	return acquireOrg(ctx, cfg.internal(), runID, pool, timeout, logf)
}

// OrgPool returns the halfsend org names used for parallel e2e runs.
func OrgPool() []string {
	return orgPool
}

// NewLiveClient creates a GitHub API client from a token.
func NewLiveClient(token string) *gh.LiveClient {
	return newLiveClient(token)
}
