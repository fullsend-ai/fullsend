package e2etest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testLockOrg = "halfsend-test"

func TestAcquireLock_NoExistingLock(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	runID := "test-uuid-1234"
	err := acquireLock(ctx, fake, "", testLockOrg, runID, 5*time.Minute, t.Logf)
	require.NoError(t, err)

	// Verify the lock repo was created with our UUID.
	content, err := fake.GetFileContent(ctx, testLockOrg, lockRepo, "README.md")
	require.NoError(t, err)
	assert.Equal(t, runID, string(content))
}

func TestReleaseLock_OwnedByUs(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	runID := "test-uuid-1234"
	// Pre-create the lock repo with our UUID.
	_, err := fake.CreateRepo(ctx, testLockOrg, lockRepo, "E2E test lock", false)
	require.NoError(t, err)
	err = fake.CreateFile(ctx, testLockOrg, lockRepo, "README.md", "acquire lock", []byte(runID))
	require.NoError(t, err)

	releaseLock(ctx, fake, testLockOrg, runID, t)

	// Verify repo was deleted.
	_, err = fake.GetRepo(ctx, testLockOrg, lockRepo)
	assert.True(t, forge.IsNotFound(err))
}

func TestReleaseLock_OwnedBySomeoneElse(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	// Pre-create the lock repo with a different UUID.
	_, err := fake.CreateRepo(ctx, testLockOrg, lockRepo, "E2E test lock", false)
	require.NoError(t, err)
	err = fake.CreateFile(ctx, testLockOrg, lockRepo, "README.md", "acquire lock", []byte("other-uuid"))
	require.NoError(t, err)

	releaseLock(ctx, fake, testLockOrg, "our-uuid", t)

	// Repo should NOT have been deleted (not our lock).
	_, err = fake.GetRepo(ctx, testLockOrg, lockRepo)
	assert.NoError(t, err)
}

func TestAcquireOrg_FirstOrgAvailable(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	pool := []string{"test-org-1", "test-org-2", "test-org-3"}

	org, err := acquireOrgWithClient(ctx, fake, "", "run-1", pool, 5*time.Second, t.Logf)
	require.NoError(t, err)
	assert.Contains(t, pool, org, "should acquire one of the pool orgs")

	// Verify the lock is held on the acquired org.
	content, err := fake.GetFileContent(ctx, org, lockRepo, "README.md")
	require.NoError(t, err)
	assert.Equal(t, "run-1", string(content))
}

func TestAcquireOrg_SkipsLockedOrg(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	pool := []string{"test-org-1", "test-org-2", "test-org-3"}

	// Lock the first org.
	fake.CreatedRepos = append(fake.CreatedRepos, forge.Repository{
		Name:     lockRepo,
		FullName: "test-org-1/" + lockRepo,
	})
	fake.FileContents["test-org-1/"+lockRepo+"/README.md"] = []byte("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

	org, err := acquireOrgWithClient(ctx, fake, "", "run-2", pool, 5*time.Second, t.Logf)
	require.NoError(t, err)
	assert.NotEqual(t, "test-org-1", org, "should skip locked test-org-1")
	assert.Contains(t, []string{"test-org-2", "test-org-3"}, org, "should acquire an unlocked org")
}

func TestAcquireOrg_AllLockedTimesOut(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	pool := []string{"test-org-1", "test-org-2"}

	// Lock all orgs by pre-populating directly (same-name repos across
	// orgs collide in the fake client's duplicate check).
	for _, org := range pool {
		fake.CreatedRepos = append(fake.CreatedRepos, forge.Repository{
			Name:     lockRepo,
			FullName: org + "/" + lockRepo,
		})
		fake.FileContents[org+"/"+lockRepo+"/README.md"] = []byte("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	}

	// Use a very short timeout so the test doesn't block.
	_, err := acquireOrgWithClient(ctx, fake, "", "run-3", pool, 1*time.Second, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not acquire any org")
}

func TestAcquireOrg_PropagatesErrors(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	pool := []string{"test-org-1"}

	// Inject a non-"already exists" error for CreateRepo.
	fake.Errors = map[string]error{"CreateRepo": fmt.Errorf("rate limited")}

	// The error from tryCreateLock should be logged and the function
	// should fall through to the timeout path.
	_, err := acquireOrgWithClient(ctx, fake, "", "run-4", pool, 1*time.Second, t.Logf)
	require.Error(t, err)
}

func TestAcquireOrg_RateLimitSkipsRemainingOrgs(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	pool := []string{"test-org-1", "test-org-2", "test-org-3"}

	// Inject a rate-limit APIError. Every CreateRepo call will hit this.
	fake.Errors = map[string]error{
		"CreateRepo": &gh.APIError{
			StatusCode: 403,
			Message:    "API rate limit exceeded for user ID 12345",
		},
	}

	// Track how many CreateRepo calls are made per polling round.
	// With rate-limit detection, the first pass should try org-1,
	// see the rate limit, and skip orgs 2 and 3 (they share the
	// same user-level quota). Use a short timeout so we don't block.
	var logs []string
	logf := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		logs = append(logs, msg)
		t.Log(msg)
	}

	_, err := acquireOrgWithClient(ctx, fake, "", "run-5", pool, 2*time.Second, logf)
	require.Error(t, err)

	// Count how many orgs were attempted before the first "skipping
	// remaining" message. Without the fix, all 3 would be attempted.
	// With the fix, only 1 should be attempted before breaking out.
	firstPassAttempts := 0
	for _, msg := range logs {
		if strings.Contains(msg, "skipping remaining") {
			break
		}
		if strings.Contains(msg, "[org-pool] Trying to acquire") {
			firstPassAttempts++
		}
	}
	assert.Equal(t, 1, firstPassAttempts,
		"should only attempt 1 org before detecting rate limit and skipping the rest")
}

func TestAcquireOrg_RateLimitReturnsSentinelError(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	pool := []string{"test-org-1", "test-org-2"}

	// Inject a persistent rate-limit error.
	fake.Errors = map[string]error{
		"CreateRepo": &gh.APIError{
			StatusCode: 403,
			Message:    "API rate limit exceeded for user ID 12345",
		},
	}

	// Use a short timeout so the test doesn't block.
	_, err := acquireOrgWithClient(ctx, fake, "", "run-rl", pool, 1*time.Second, t.Logf)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAllOrgsRateLimited,
		"should return ErrAllOrgsRateLimited when rate limits persist")
}

func TestAcquireOrg_RateLimitBacksOff(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	pool := []string{"test-org-1"}

	// Inject a persistent rate-limit error.
	fake.Errors = map[string]error{
		"CreateRepo": &gh.APIError{
			StatusCode: 403,
			Message:    "API rate limit exceeded for user ID 12345",
		},
	}

	var logs []string
	logf := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		logs = append(logs, msg)
		t.Log(msg)
	}

	// Use 2s timeout — enough for the first pass + one backoff round
	// (rateLimitBackoffInitial is 30s, so only the first pass runs
	// before the timeout expires).
	_, err := acquireOrgWithClient(ctx, fake, "", "run-bo", pool, 2*time.Second, logf)
	require.Error(t, err)

	// Verify backoff logging appeared.
	backoffSeen := false
	for _, msg := range logs {
		if strings.Contains(msg, "backing off") {
			backoffSeen = true
			break
		}
	}
	assert.True(t, backoffSeen,
		"should log rate-limit backoff during polling")
}

const otherHolder = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// stubLockAge makes lockRepoCreatedAtFn report a lock created age ago, so
// stale-lock decisions can be tested without calling the GitHub API.
func stubLockAge(t *testing.T, age time.Duration) {
	t.Helper()
	orig := lockRepoCreatedAtFn
	t.Cleanup(func() { lockRepoCreatedAtFn = orig })
	lockRepoCreatedAtFn = func(context.Context, string, string, string) (time.Time, error) {
		return time.Now().Add(-age), nil
	}
}

// seedLock creates the lock repo on org held by holder.
func seedLock(t *testing.T, fake *forge.FakeClient, org, holder string) {
	t.Helper()
	ctx := context.Background()
	_, err := fake.CreateRepo(ctx, org, lockRepo, "E2E test lock", false)
	require.NoError(t, err)
	require.NoError(t, fake.CreateFile(ctx, org, lockRepo, "README.md", "acquire lock", []byte(holder)))
}

func TestStaleLockTimeout(t *testing.T) {
	assert.Equal(t, 30*time.Minute, staleLockTimeout)
}

func TestTryReclaimStaleLock_AgeBoundary(t *testing.T) {
	tests := []struct {
		name          string
		age           time.Duration
		wantReclaimed bool
	}{
		{name: "16m not reclaimed (was stale at 15m)", age: 16 * time.Minute},
		{name: "25m not reclaimed", age: 25 * time.Minute},
		{name: "29m not reclaimed", age: 29 * time.Minute},
		{name: "31m reclaimed", age: 31 * time.Minute, wantReclaimed: true},
		{name: "45m reclaimed", age: 45 * time.Minute, wantReclaimed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := forge.NewFakeClient()
			ctx := context.Background()
			seedLock(t, fake, testLockOrg, otherHolder)
			stubLockAge(t, tt.age)

			got := tryReclaimStaleLock(ctx, fake, "token", testLockOrg, "our-run", t.Logf)
			assert.Equal(t, tt.wantReclaimed, got)

			content, err := fake.GetFileContent(ctx, testLockOrg, lockRepo, "README.md")
			require.NoError(t, err)
			if tt.wantReclaimed {
				assert.Contains(t, fake.DeletedRepos, testLockOrg+"/"+lockRepo)
				assert.Equal(t, "our-run", string(content))
			} else {
				assert.Empty(t, fake.DeletedRepos, "fresh lock must not be deleted")
				assert.Equal(t, otherHolder, string(content))
			}
		})
	}
}

func TestTryReclaimStaleLock_AgeLookupError(t *testing.T) {
	fake := forge.NewFakeClient()
	seedLock(t, fake, testLockOrg, otherHolder)
	orig := lockRepoCreatedAtFn
	t.Cleanup(func() { lockRepoCreatedAtFn = orig })
	lockRepoCreatedAtFn = func(context.Context, string, string, string) (time.Time, error) {
		return time.Time{}, fmt.Errorf("boom")
	}

	assert.False(t, tryReclaimStaleLock(context.Background(), fake, "token", testLockOrg, "our-run", t.Logf))
	assert.Empty(t, fake.DeletedRepos)
}

func TestAcquireLock_ReclaimsLockOlderThanStaleTimeout(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()
	seedLock(t, fake, testLockOrg, otherHolder)
	stubLockAge(t, staleLockTimeout+time.Minute)

	require.NoError(t, acquireLock(ctx, fake, "token", testLockOrg, "our-run", 5*time.Minute, t.Logf))

	assert.Contains(t, fake.DeletedRepos, testLockOrg+"/"+lockRepo)
	content, err := fake.GetFileContent(ctx, testLockOrg, lockRepo, "README.md")
	require.NoError(t, err)
	assert.Equal(t, "our-run", string(content))
}

func TestAcquireLock_DoesNotReclaimLockYoungerThanStaleTimeout(t *testing.T) {
	fake := forge.NewFakeClient()
	seedLock(t, fake, testLockOrg, otherHolder)
	stubLockAge(t, staleLockTimeout-time.Minute)

	// The lock is not stale, so acquireLock waits; cancel the context to
	// stop it from polling.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := acquireLock(ctx, fake, "token", testLockOrg, "our-run", 5*time.Minute, t.Logf)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	assert.Empty(t, fake.DeletedRepos, "fresh lock must not be deleted")
	content, err := fake.GetFileContent(context.Background(), testLockOrg, lockRepo, "README.md")
	require.NoError(t, err)
	assert.Equal(t, otherHolder, string(content))
}

// rateLimitPoolClient reports every org as locked for the first lockedCalls
// CreateRepo calls, then fails with a wrapped 429. When cancel is set it
// cancels the context first and also wraps context.Canceled, like a
// request cut short by cancellation.
type rateLimitPoolClient struct {
	*forge.FakeClient
	lockedCalls int
	cancel      context.CancelFunc
	calls       int
}

func (c *rateLimitPoolClient) CreateRepo(_ context.Context, org, name, _ string, _ bool) (*forge.Repository, error) {
	c.calls++
	if c.calls <= c.lockedCalls {
		return nil, fmt.Errorf("repository %s/%s already exists", org, name)
	}
	tooMany := &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "Too Many Requests"}
	if c.cancel != nil {
		c.cancel()
		return nil, fmt.Errorf("%w: %w", context.Canceled, tooMany)
	}
	return nil, tooMany
}

// stubPoolClient makes acquireOrg use client for every pool org instead of
// minting a token and building a live GitHub client.
func stubPoolClient(t *testing.T, client forge.Client) {
	t.Helper()
	origToken, origClient := poolTokenFn, poolClientFn
	t.Cleanup(func() { poolTokenFn, poolClientFn = origToken, origClient })
	poolTokenFn = func(context.Context, envConfig, string) (string, error) { return "test-token", nil }
	poolClientFn = func(string) forge.Client { return client }
}

func TestAcquireOrg_RateLimitVersusCancellation(t *testing.T) {
	pool := []string{"test-org-1", "test-org-2", "test-org-3"}

	type acquireFn func(ctx context.Context, t *testing.T, client forge.Client, timeout time.Duration, logf func(string, ...any)) error
	viaAcquireOrg := func(useMint bool) acquireFn {
		return func(ctx context.Context, t *testing.T, client forge.Client, timeout time.Duration, logf func(string, ...any)) error {
			stubPoolClient(t, client)
			_, _, err := acquireOrg(ctx, envConfig{useMint: useMint}, "run-ctx", pool, timeout, logf)
			return err
		}
	}
	modes := []struct {
		name string
		// perOrgLimit is true when a rate limit only covers one org
		// (mint installation tokens), so acquisition moves on to the
		// next org instead of ending the round.
		perOrgLimit bool
		acquire     acquireFn
	}{
		{name: "mint", perOrgLimit: true, acquire: viaAcquireOrg(true)},
		{name: "pat", acquire: viaAcquireOrg(false)},
		{name: "fixed client", acquire: func(ctx context.Context, _ *testing.T, client forge.Client, timeout time.Duration, logf func(string, ...any)) error {
			_, _, err := acquireOrgFromClient(ctx, client, "", "run-ctx", pool, timeout, logf)
			return err
		}},
	}
	passes := []struct {
		name        string
		lockedCalls int
		timeout     time.Duration
	}{
		// Zero timeout: the polling loop never runs.
		{name: "first pass", lockedCalls: 0, timeout: 0},
		// Every org is locked in the first pass; the 429 comes in the
		// single polling round that runs once the short timeout elapses.
		{name: "polling", lockedCalls: len(pool), timeout: 50 * time.Millisecond},
	}

	for _, mode := range modes {
		for _, pass := range passes {
			t.Run(mode.name+"/"+pass.name, func(t *testing.T) {
				stubLockAge(t, time.Minute) // locked orgs are fresh, not reclaimed

				t.Run("cancelled context returns ctx error", func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					client := &rateLimitPoolClient{FakeClient: forge.NewFakeClient(), lockedCalls: pass.lockedCalls, cancel: cancel}
					var logs []string
					logf := func(format string, args ...any) {
						logs = append(logs, fmt.Sprintf(format, args...))
					}

					err := mode.acquire(ctx, t, client, pass.timeout, logf)

					require.ErrorIs(t, err, context.Canceled)
					assert.NotErrorIs(t, err, ErrAllOrgsRateLimited)
					assert.Equal(t, pass.lockedCalls+1, client.calls, "must not try another org after cancellation")
					for _, msg := range logs {
						lower := strings.ToLower(msg)
						assert.NotContains(t, lower, "rate limit", "cancellation logged as rate limit")
						assert.NotContains(t, lower, "rate-limit", "cancellation logged as rate limit")
						assert.NotContains(t, lower, "next org", "cancellation logged as moving on")
					}
				})

				t.Run("live context still handles rate limit", func(t *testing.T) {
					client := &rateLimitPoolClient{FakeClient: forge.NewFakeClient(), lockedCalls: pass.lockedCalls}
					var logs []string
					logf := func(format string, args ...any) {
						logs = append(logs, fmt.Sprintf(format, args...))
					}

					err := mode.acquire(context.Background(), t, client, pass.timeout, logf)

					require.ErrorIs(t, err, ErrAllOrgsRateLimited)
					wantCalls, wantLog := pass.lockedCalls+1, "skipping remaining orgs"
					if mode.perOrgLimit {
						wantCalls, wantLog = pass.lockedCalls+len(pool), "trying the next org"
					}
					assert.Equal(t, wantCalls, client.calls)
					assert.Contains(t, strings.Join(logs, "\n"), wantLog)
				})
			})
		}
	}
}

func TestRateLimitedUnlessCancelled(t *testing.T) {
	tooMany := fmt.Errorf("creating lock repo: %w", &gh.APIError{StatusCode: http.StatusTooManyRequests})

	rateLimited, err := rateLimitedUnlessCancelled(context.Background(), tooMany)
	require.NoError(t, err)
	assert.True(t, rateLimited, "429 with a live context is a rate limit")

	rateLimited, err = rateLimitedUnlessCancelled(context.Background(), fmt.Errorf("network down"))
	require.NoError(t, err)
	assert.False(t, rateLimited)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rateLimited, err = rateLimitedUnlessCancelled(ctx, tooMany)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, rateLimited, "429 with a cancelled context is not a rate limit")
}

// seedSharedLock lays out a shared lock the way the behaviour install
// driver's bridge (pkg/behaviourtest/drivers/install/orglock.go, #8620)
// writes it: README.md holds a generation UUID, and the shared-mode flag
// and holder state live in separate files.
func seedSharedLock(t *testing.T, fake *forge.FakeClient, org, generation string) {
	t.Helper()
	ctx := context.Background()
	_, err := fake.CreateRepo(ctx, org, LockRepo, "E2E test lock — do not delete manually", false)
	require.NoError(t, err)
	_, err = fake.CommitFiles(ctx, org, LockRepo, "create shared e2e lock", []forge.TreeFile{
		{Path: "README.md", Content: []byte(generation)},
		{Path: "SHARED_LOCK", Content: []byte("shared e2e-lock: legacy suites treat this lock as held (#8620)\n")},
		{Path: "holders.json", Content: []byte(`{"generation":"` + generation + `","closed":false,"holders":{"h1":"2026-10-20T00:00:00Z"}}`)},
	})
	require.NoError(t, err)
}

// Legacy runners must treat a shared lock as held by someone else: they
// neither acquire, delete as invalid, nor release it.
func TestLegacyRunner_TreatsSharedLockAsHeld(t *testing.T) {
	const generation = "11111111-2222-3333-4444-555555555555"

	t.Run("acquireLock keeps the UUID holder and waits", func(t *testing.T) {
		fake := forge.NewFakeClient()
		seedSharedLock(t, fake, testLockOrg, generation)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		err := acquireLock(ctx, fake, "", testLockOrg, "our-run", 5*time.Minute, t.Logf)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Empty(t, fake.DeletedRepos, "a UUID README must not be deleted as an invalid holder")
		content, err := fake.GetFileContent(context.Background(), testLockOrg, LockRepo, "README.md")
		require.NoError(t, err)
		assert.Equal(t, generation, string(content))
	})

	t.Run("acquireLock deletes a non-UUID README as invalid", func(t *testing.T) {
		fake := forge.NewFakeClient()
		seedLock(t, fake, testLockOrg, "# e2e-lock")

		require.NoError(t, acquireLock(context.Background(), fake, "", testLockOrg, "our-run", 5*time.Minute, t.Logf))
		assert.Contains(t, fake.DeletedRepos, testLockOrg+"/"+LockRepo)
	})

	t.Run("acquireOrg does not acquire the org", func(t *testing.T) {
		fake := forge.NewFakeClient()
		seedSharedLock(t, fake, testLockOrg, generation)

		_, err := acquireOrgWithClient(context.Background(), fake, "", "our-run", []string{testLockOrg}, time.Millisecond, t.Logf)
		require.Error(t, err)
		assert.Empty(t, fake.DeletedRepos)
	})

	t.Run("releaseLock leaves the shared lock", func(t *testing.T) {
		fake := forge.NewFakeClient()
		seedSharedLock(t, fake, testLockOrg, generation)

		releaseLock(context.Background(), fake, testLockOrg, "our-run", t)
		assert.Empty(t, fake.DeletedRepos)
	})
}
