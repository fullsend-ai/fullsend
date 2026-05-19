//go:build e2e

package admin

import (
	"context"
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
