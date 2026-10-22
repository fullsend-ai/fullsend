package repos

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const uninstallLeaseKey = "acme/api/" + GitLabProjectLeaseVar

// leaseProbeClient records whether the project lease was held when uninstall
// reached webhook teardown, role-credential cleanup, and the remaining
// deletions.
type leaseProbeClient struct {
	*forge.FakeClient
	triggerListHeld  []bool
	varDeleteHeld    []bool
	secretDeleteHeld []bool
	refDeleteHeld    []bool
}

func (c *leaseProbeClient) leaseHeld(owner, repo string) bool {
	ok, err := c.AcquireProjectLease(context.Background(), owner, repo, GitLabProjectLeaseVar, "probe")
	if err == nil && ok {
		_ = c.ReleaseProjectLease(context.Background(), owner, repo, GitLabProjectLeaseVar, "probe")
		return false
	}
	return true
}

func (c *leaseProbeClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	c.triggerListHeld = append(c.triggerListHeld, c.leaseHeld(owner, repo))
	return c.FakeClient.ListPipelineTriggerTokens(ctx, owner, repo)
}

func (c *leaseProbeClient) DeleteRepoVariable(ctx context.Context, owner, repo, name string) error {
	c.varDeleteHeld = append(c.varDeleteHeld, c.leaseHeld(owner, repo))
	return c.FakeClient.DeleteRepoVariable(ctx, owner, repo, name)
}

func (c *leaseProbeClient) DeleteRepoSecret(ctx context.Context, owner, repo, name string) error {
	c.secretDeleteHeld = append(c.secretDeleteHeld, c.leaseHeld(owner, repo))
	return c.FakeClient.DeleteRepoSecret(ctx, owner, repo, name)
}

func (c *leaseProbeClient) DeleteRef(ctx context.Context, owner, repo, refPath string) error {
	c.refDeleteHeld = append(c.refDeleteHeld, c.leaseHeld(owner, repo))
	return c.FakeClient.DeleteRef(ctx, owner, repo, refPath)
}

func uninstallGitLabAcmeAPI(client forge.Client, fake *forge.FakeClient) ([]UninstallResult, error) {
	return Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
	}, newTestClientFactory(client), uninstallCommitFn(fake), nil)
}

// Webhook teardown and role-credential cleanup run in one lease-protected
// transaction, so an installer cannot publish a replacement hook and trigger
// between uninstall's discovery and deletion; the lease is freed afterward.
func TestUninstall_GitLabHoldsProjectLeaseAcrossTeardownAndRoleCleanup(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}

	results, err := uninstallGitLabAcmeAPI(client, fake)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
	require.NotEmpty(t, client.triggerListHeld)
	for _, held := range client.triggerListHeld {
		assert.True(t, held, "webhook teardown discovers resources under the lease")
	}
	require.NotEmpty(t, client.varDeleteHeld)
	assert.True(t, client.varDeleteHeld[0], "role-credential cleanup runs under the same lease")
	assert.False(t, client.leaseHeld("acme", "api"), "the lease is released when uninstall finishes")
}

// The lease stays held through the variable, webhook credential secret, and
// poll-state branch deletions that follow role cleanup, so a concurrent
// installer cannot republish resources that are about to be deleted.
func TestUninstall_GitLabHoldsProjectLeaseThroughRemainingDeletions(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}

	results, err := uninstallGitLabAcmeAPI(client, fake)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
	for _, held := range client.varDeleteHeld {
		assert.True(t, held, "repository variables are deleted under the lease")
	}
	require.NotEmpty(t, client.secretDeleteHeld)
	for _, held := range client.secretDeleteHeld {
		assert.True(t, held, "webhook credential secrets are deleted under the lease")
	}
	require.NotEmpty(t, client.refDeleteHeld)
	for _, held := range client.refDeleteHeld {
		assert.True(t, held, "poll-state branches are deleted under the lease")
	}
	assert.False(t, client.leaseHeld("acme", "api"), "the lease is released when uninstall finishes")
}

// An installer in another process holding the lease stops uninstall before it
// tears anything down, and the other installer's lease is left alone.
func TestUninstall_GitLabRefusedWhileAnotherInstallerHoldsLease(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	fake := newInstalledFakeGitLabClient("acme/api")
	fake.ProjectLeases = map[string]string{uninstallLeaseKey: leaseTestForeignHolder}

	results, err := uninstallGitLabAcmeAPI(fake, fake)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), GitLabProjectLeaseVar)
	assert.False(t, results[0].WorkflowDeleted, "no scaffold is removed without exclusivity")
	assert.Empty(t, fake.DeletedVariables, "no variable is removed without exclusivity")
	assert.Equal(t, leaseTestForeignHolder, fake.ProjectLeases[uninstallLeaseKey])
}

// A client that cannot take the lease is refused before anything is removed.
func TestUninstall_GitLabClientWithoutLeaseRefused(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")

	results, err := uninstallGitLabAcmeAPI(noLeaseClient{fake}, fake)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.ErrorContains(t, results[0].Error, "cannot take the project lease")
	assert.False(t, results[0].WorkflowDeleted)
	assert.Empty(t, fake.DeletedVariables)
}
