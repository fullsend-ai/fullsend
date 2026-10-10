package repos

import (
	"context"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The outgoing-inactivity check is an optimization: when it cannot establish
// inactivity (no service-account client, or an unavailable service-account
// listing), grace cleanup still revokes through the authorization-enforcing
// RevokeProjectAccessToken, and keeps the obligation only if that fails.
func TestGraceCleanupFallsBackToRevokeWhenInactivityUnknown(t *testing.T) {
	for _, tc := range []struct {
		name       string
		saListErr  error
		withSA     bool
		failRevoke error
		wantClean  bool
	}{
		{name: "nil service-account client", wantClean: true},
		{name: "service accounts not supported", withSA: true, saListErr: forge.ErrNotSupported, wantClean: true},
		{name: "service accounts not found", withSA: true, saListErr: forge.ErrNotFound, wantClean: true},
		{name: "service accounts forbidden", withSA: true, saListErr: forge.ErrForbidden, wantClean: true},
		{name: "revoke failure keeps obligation", failRevoke: forge.ErrForbidden, wantClean: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := &fakeTokens{failRevoke: tc.failRevoke}
			legacy.seed(ProjectAccessToken{ID: 88, Name: gitlabroles.CoderTokenName, Active: true})
			c := ServiceAccountTokenClient{Legacy: legacy, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy)}
			if tc.withSA {
				sa := newFakeSAAPI()
				sa.failList = tc.saListErr
				c.SA = sa
			}
			now := time.Now()
			rs := rotationRoleState{Phase: rotationPhaseOverlapping, IncomingID: 99, OutgoingIDs: []int{88}, DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}
			listed := []ProjectAccessToken{}
			cleaned := cleanupOutgoing(context.Background(), RoleRotateConfig{Owner: "g", Repo: "p", Tokens: c}, &rs, now, 24*time.Hour, &listed, nil)
			if tc.wantClean {
				assert.True(t, cleaned)
				assert.Equal(t, []int{88}, legacy.revoked, "the outgoing legacy token is revoked")
				assert.Empty(t, rs.OutgoingIDs)
				assert.Equal(t, rotationPhaseIdle, rs.Phase)
			} else {
				assert.False(t, cleaned)
				assert.Equal(t, []int{88}, rs.OutgoingIDs)
				assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
			}
		})
	}
}

// Cleanup applies recorded managed-account ownership to the client preflight
// returns, so a client with durable ManagedUserID entries and no ownership
// callbacks revokes the role PAT and then deletes the account, rather than
// deleting variables and secrets and failing at account deletion.
func TestCleanupAppliesRecordedOwnershipWithoutCallbacks(t *testing.T) {
	ctx := context.Background()
	fc := &accountDeletingClient{FakeClient: forge.NewFakeClient()}
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {ManagedUserID: 501},
	}}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	sa.tokens[501] = []ProjectAccessToken{{ID: 7, Name: gitlabroles.CoderTokenName, Active: true, UserID: 501}}
	c := ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}

	result, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
	require.NoError(t, err)
	assert.Equal(t, []int{7}, sa.revoked, "the managed account's role PAT is revoked")
	assert.Equal(t, 1, result.TokensRevoked)
	assert.Equal(t, []int{501}, fc.deleted, "the managed account is deleted once its PAT is revoked")
	assert.Equal(t, 1, result.AccountsDeleted)
}

// Direct rotation freezes project-wide exclusions recorded in rotation state
// before selecting an account or changing membership, even with no
// SuppliedAccountIDs callback and an ownership resolver that still reports the
// excluded account.
func TestDirectRotationAppliesRecordedExclusionsWithoutResolver(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
		"coder":  {IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	sa.nextUser = 600 // a replacement account must not collide with the seeded ID
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 20
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
		ManagedAccountIDs:     func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}

	_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	})
	require.NoError(t, err)

	assert.Equal(t, 20, sa.members[501], "excluded account membership is untouched")
	assert.Empty(t, sa.tokens[501], "no credential is minted for the excluded account")
	assert.Empty(t, sa.revoked)
	assert.Equal(t, []string{rec.Credential.TokenName}, sa.createdSAs, "a new account is created instead of reusing the excluded one")
}

// Direct rotation fails closed, creating nothing, when another role's supplied
// ownership cannot be attributed.
func TestDirectRotationFailsClosedOnUnresolvedSuppliedOwnership(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {Supplied: true},
		"coder":  {IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "exclusions could not be resolved")
	assert.Empty(t, sa.createdSAs)
	assert.Empty(t, sa.tokens)
	assert.Empty(t, sa.revoked)
}

// A *ServiceAccountTokenClient is frozen with the recorded project-wide
// exclusions in direct rotation, exactly like the value form.
func TestDirectRotationAppliesRecordedExclusionsForPointerClient(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
		"coder":  {IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	sa.nextUser = 600
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 20
	c := &ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
		ManagedAccountIDs:     func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}

	_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	})
	require.NoError(t, err)

	assert.Equal(t, 20, sa.members[501], "excluded account membership is untouched")
	assert.Empty(t, sa.tokens[501], "no credential is minted for the excluded account")
	assert.Empty(t, sa.revoked)
	assert.Equal(t, []string{rec.Credential.TokenName}, sa.createdSAs)
}

// Missing-secret provisioning applies the recorded exclusions to a pointer
// client too.
func TestMissingSecretProvisioningAppliesRecordedExclusionsForPointerClient(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
	}}))
	sa := newFakeSAAPI()
	sa.nextUser = 600
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 20
	c := &ServiceAccountTokenClient{
		SA:                sa,
		ManagedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}
	result := &RoleProvisionResult{}
	present := map[string]bool{}
	for _, other := range gitlabroles.BuiltinRegistry().Registrations() {
		present[other.Credential.SecretName] = other.Name != rec.Name
	}
	provisionOwnRoles(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c}, gitlabroles.BuiltinRegistry(), present, result)

	assert.Equal(t, 20, sa.members[501], "excluded account membership is untouched")
	assert.Empty(t, sa.tokens[501], "no credential is minted for the excluded account")
	assert.Equal(t, []string{rec.Credential.TokenName}, sa.createdSAs)
	assert.Empty(t, result.Failed)
}
