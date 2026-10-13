package repos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

const roleLeaseKey = "group/project/" + GitLabProjectLeaseVar

func roleLeaseProvisionConfig(client forge.Client, tokens *fakeTokens, dryRun bool) RoleProvisionConfig {
	return RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   client,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Now:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		DryRun:   dryRun,
	}
}

// Provisioning takes the project lease and frees it when it finishes.
func TestProvisionGitLabRoleCredentials_ReleasesProjectLease(t *testing.T) {
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	_, err := ProvisionGitLabRoleCredentials(context.Background(), roleLeaseProvisionConfig(fc, tokens, false))

	require.NoError(t, err)
	assert.NotEmpty(t, tokens.created)
	assert.Empty(t, fc.ProjectLeases, "the lease is released after provisioning")
}

// A failed provisioning still frees the lease.
func TestProvisionGitLabRoleCredentials_ReleasesProjectLeaseOnFailure(t *testing.T) {
	fc := provisionClient(t)
	fc.Errors["RepoSecretExists"] = errors.New("503 unavailable")
	tokens := &fakeTokens{}

	_, err := ProvisionGitLabRoleCredentials(context.Background(), roleLeaseProvisionConfig(fc, tokens, false))

	require.Error(t, err)
	assert.Empty(t, fc.ProjectLeases, "a failed provisioning does not strand the lease")
}

// Another installer holding the lease stops provisioning before it mints or
// stores any credential, and its lease is left alone.
func TestProvisionGitLabRoleCredentials_HeldLeaseRefused(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	fc := provisionClient(t)
	fc.ProjectLeases[roleLeaseKey] = leaseTestForeignHolder
	tokens := &fakeTokens{}

	_, err := ProvisionGitLabRoleCredentials(context.Background(), roleLeaseProvisionConfig(fc, tokens, false))

	require.ErrorContains(t, err, GitLabProjectLeaseVar)
	assert.Empty(t, tokens.created)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.Equal(t, leaseTestForeignHolder, fc.ProjectLeases[roleLeaseKey])
}

// A client that cannot take the lease is refused instead of provisioning
// unserialized.
func TestProvisionGitLabRoleCredentials_ClientWithoutLeaseRefused(t *testing.T) {
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	_, err := ProvisionGitLabRoleCredentials(context.Background(), roleLeaseProvisionConfig(noLeaseClient{fc}, tokens, false))

	require.ErrorContains(t, err, "cannot take the project lease")
	assert.Empty(t, tokens.created)
}

// A dry run writes nothing, so it needs no lease and is not blocked by one.
func TestProvisionGitLabRoleCredentials_DryRunTakesNoLease(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	fc := provisionClient(t)
	fc.ProjectLeases[roleLeaseKey] = leaseTestForeignHolder
	tokens := &fakeTokens{}

	_, err := ProvisionGitLabRoleCredentials(context.Background(), roleLeaseProvisionConfig(fc, tokens, true))

	require.NoError(t, err)
	assert.Empty(t, tokens.created)
	assert.Equal(t, leaseTestForeignHolder, fc.ProjectLeases[roleLeaseKey])
}

// Rotation is refused while another installer holds the lease and revokes
// nothing.
func TestRotateGitLabRoleCredentials_HeldLeaseRefused(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	fc.ProjectLeases[roleLeaseKey] = leaseTestForeignHolder
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	_, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Force:    true,
		Now:      time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	})

	require.ErrorContains(t, err, GitLabProjectLeaseVar)
	assert.Empty(t, tokens.created)
	assert.Empty(t, tokens.revoked)
	assert.Equal(t, leaseTestForeignHolder, fc.ProjectLeases[roleLeaseKey])
}

// Role-identity cleanup is refused while another installer holds the lease and
// deletes nothing; once the lease is free it runs and releases its own.
func TestCleanupGitLabRoleIdentity_HeldLeaseRefused(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	fc.ProjectLeases[roleLeaseKey] = leaseTestForeignHolder
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true})
	cfg := GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: tokens}

	_, err := CleanupGitLabRoleIdentity(context.Background(), cfg)

	require.ErrorContains(t, err, GitLabProjectLeaseVar)
	assert.Empty(t, tokens.revoked)
	assert.Empty(t, fc.DeletedVariables)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])

	delete(fc.ProjectLeases, roleLeaseKey)
	_, err = CleanupGitLabRoleIdentity(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, []int{1}, tokens.revoked)
	assert.Empty(t, fc.ProjectLeases, "the lease is released after cleanup")
}
