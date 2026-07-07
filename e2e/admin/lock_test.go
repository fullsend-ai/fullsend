//go:build e2e

package admin

import (
	"context"
	"fmt"
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
	assert.ErrorIs(t, err, errAllOrgsRateLimited,
		"should return errAllOrgsRateLimited when rate limits persist")
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
