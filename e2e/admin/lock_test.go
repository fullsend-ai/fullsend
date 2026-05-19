//go:build e2e

package admin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
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

	// Save and restore the real pool.
	origPool := orgPool
	defer func() { orgPool = origPool }()
	orgPool = []string{"test-org-1", "test-org-2", "test-org-3"}

	org, err := acquireOrg(ctx, fake, "", "run-1", 5*time.Second, t.Logf)
	require.NoError(t, err)
	assert.Equal(t, "test-org-1", org)

	// Verify the lock is held.
	content, err := fake.GetFileContent(ctx, "test-org-1", lockRepo, "README.md")
	require.NoError(t, err)
	assert.Equal(t, "run-1", string(content))
}

func TestAcquireOrg_AllLockedTimesOut(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	origPool := orgPool
	defer func() { orgPool = origPool }()
	orgPool = []string{"test-org-1", "test-org-2"}

	// Lock all orgs by pre-populating directly (same-name repos across
	// orgs collide in the fake client's duplicate check).
	for _, org := range orgPool {
		fake.CreatedRepos = append(fake.CreatedRepos, forge.Repository{
			Name:     lockRepo,
			FullName: org + "/" + lockRepo,
		})
		fake.FileContents[org+"/"+lockRepo+"/README.md"] = []byte("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	}

	// Use a very short timeout so the test doesn't block.
	_, err := acquireOrg(ctx, fake, "", "run-3", 1*time.Second, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not acquire any org")
}

func TestAcquireOrg_PropagatesErrors(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	origPool := orgPool
	defer func() { orgPool = origPool }()
	orgPool = []string{"test-org-1"}

	// Inject a non-"already exists" error for CreateRepo.
	fake.Errors = map[string]error{"CreateRepo": fmt.Errorf("rate limited")}

	// The error from tryCreateLock should be logged and the function
	// should fall through to the timeout path.
	_, err := acquireOrg(ctx, fake, "", "run-4", 1*time.Second, t.Logf)
	require.Error(t, err)
}
