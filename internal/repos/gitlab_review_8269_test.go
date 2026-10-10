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

// A service-account client with no SA backend implements the cleaner interface
// but cannot delete accounts, so preflight must refuse before any teardown.
func TestCleanupPreflightRejectsServiceAccountClientWithoutBackend(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501},
	}}))
	fc.VariablesExist["g/p/"+forge.VarGitLabRoleRegistry] = true
	before := fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation]

	for name, tokens := range map[string]ProjectAccessTokenClient{
		"value":   ServiceAccountTokenClient{},
		"pointer": &ServiceAccountTokenClient{},
	} {
		t.Run(name, func(t *testing.T) {
			err := PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", tokens)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "managed-account cleanup capability unavailable")
			_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
			require.Error(t, err)
			assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRegistry], "variables are untouched")
			assert.Equal(t, before, fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation], "rotation state is untouched")
		})
	}

	// An administrator-owned (excluded) account needs no backend.
	excluded := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, excluded, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501},
		"coder":  {SuppliedUserID: 501},
	}}))
	require.NoError(t, PreflightGitLabRoleCleanupOwnership(ctx, excluded, "g", "p", ServiceAccountTokenClient{}))
}

// Status ownership subtracts supplied owners attributed only through the
// resolver callback, matching convergence and cleanup.
func TestServiceAccountStatusSubtractsResolvedSuppliedOwners(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
	sa.members[501] = 20
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501},
		"coder":  {Supplied: true},
	}}))
	c := ServiceAccountTokenClient{SA: sa, SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil }}
	status := &RepoStatus{GitLabRolesReady: true}
	assert.False(t, c.AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
	require.Len(t, status.GitLabServiceAccounts, 1)
	assert.False(t, status.GitLabServiceAccounts[0].Managed)
	assert.Empty(t, status.Drifts)
	assert.True(t, status.GitLabRolesReady)

	// Without the resolver the owner stays unattributed and status is unverified.
	status = &RepoStatus{GitLabRolesReady: true}
	assert.True(t, ServiceAccountTokenClient{SA: sa}.AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
	assert.False(t, status.GitLabRolesReady)
}

// A dry run plans the direct membership a live run would add instead of
// recording a reconciliation failure.
func TestInstallConvergenceDryRunPlansMissingMembership(t *testing.T) {
	ctx := context.Background()
	for _, dryRun := range []bool{true, false} {
		fc := forge.NewFakeClient()
		sa := newFakeSAAPI()
		sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 501, IncomingID: 1}}}))
		cfg := RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ServiceAccountTokenClient{SA: sa}, DryRun: dryRun}
		result := RoleProvisionResult{}
		rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
		convergeInstalledServiceAccount(ctx, cfg, rec, time.Now(), false, &result)
		if dryRun {
			assert.Empty(t, result.Failed)
			require.Len(t, result.Diagnostics, 1)
			assert.Contains(t, result.Diagnostics[0], "would be added")
			_, member := sa.members[501]
			assert.False(t, member, "dry run must not mutate membership")
		} else {
			assert.Empty(t, result.Failed)
			assert.Equal(t, gitlabroles.DeveloperAccessLevel, sa.members[501])
		}
	}

	// Other lookup failures still fail closed in a dry run.
	fc := forge.NewFakeClient()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
	sa.failLevel = forge.ErrForbidden
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 501, IncomingID: 1}}}))
	cfg := RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ServiceAccountTokenClient{SA: sa}, DryRun: true}
	result := RoleProvisionResult{}
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
	convergeInstalledServiceAccount(ctx, cfg, rec, time.Now(), false, &result)
	assert.Len(t, result.Failed, 1)
}

// A generic resolver's error text never reaches cleanup or preflight errors.
func TestCleanupRedactsGenericSuppliedOwnerResolverError(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true},
	}}))
	const secretText = "credential-echoed-by-resolver"
	tokens := ownerAwareTokens{fakeTokens: &fakeTokens{}, err: errors.New("lookup failed for " + secretText)}

	err := PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", tokens)
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	assert.NotContains(t, err.Error(), secretText)

	_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	assert.NotContains(t, err.Error(), secretText)
}
