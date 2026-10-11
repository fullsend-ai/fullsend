package repos

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// ownerAwareTokens is a generic token client that exposes the optional
// SuppliedOwnerIDs ownership-resolution capability.
type ownerAwareTokens struct {
	*fakeTokens
	ids []int
	err error
}

func (o ownerAwareTokens) SuppliedOwnerIDs(context.Context, string, string) ([]int, error) {
	return o.ids, o.err
}

// A resolver that returns no positive owner without error attributes nothing,
// so it cannot authorize mutation while a supplied credential has no recorded
// owner.
func TestWithProjectExclusionsRejectsEmptyAttributionForUnresolvedSupplied(t *testing.T) {
	ctx := context.Background()
	unresolved := rotationStateFile{Roles: map[string]rotationRoleState{"coder": {Supplied: true, ManagedUserID: 501}}}
	resolvers := map[string]func(context.Context, string, string) ([]int, error){
		"nil":          nil,
		"empty":        func(context.Context, string, string) ([]int, error) { return nil, nil },
		"invalid only": func(context.Context, string, string) ([]int, error) { return []int{0, -3}, nil },
	}
	for name, resolver := range resolvers {
		t.Run(name, func(t *testing.T) {
			_, frozen, err := ServiceAccountTokenClient{SuppliedAccountIDs: resolver}.withProjectExclusions(ctx, "g", "p", unresolved)
			require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
			assert.Empty(t, frozen)
		})
	}

	_, frozen, err := ServiceAccountTokenClient{SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{0, 501}, nil }}.
		withProjectExclusions(ctx, "g", "p", unresolved)
	require.NoError(t, err)
	assert.Equal(t, []int{501}, frozen, "nonpositive IDs are dropped")

	// With no unresolved supplied credential an empty resolver result is fine.
	_, _, err = ServiceAccountTokenClient{SuppliedAccountIDs: resolvers["empty"]}.withProjectExclusions(ctx, "g", "p",
		rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 501}}})
	require.NoError(t, err)
}

// Cleanup with a service-account client whose resolver attributes nothing
// refuses before any revocation, deletion or state retirement.
func TestCleanupRefusesEmptyAttributionForServiceAccountClient(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true, ManagedUserID: 501},
	}}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: gitlabroles.CoderTokenName, Active: true}}
	c := ServiceAccountTokenClient{SA: sa, SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return nil, nil }}

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	assert.Empty(t, sa.revoked)
	assert.Empty(t, sa.deletedSAs)
	assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation], "rotation state is retained")
}

// Direct managed-account deletion refuses when the resolver attributes nothing.
func TestDeleteManagedServiceAccountsRefusesEmptyAttribution(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501},
		"coder":  {Supplied: true},
	}}))
	sa := newFakeSAAPI()
	c := ServiceAccountTokenClient{
		SA:                    sa,
		VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil },
		SuppliedAccountIDs:    func(context.Context, string, string) ([]int, error) { return []int{-1}, nil },
	}
	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
	assert.Zero(t, n)
	assert.Empty(t, sa.deletedSAs)
	state, _, readErr := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, readErr)
	assert.Equal(t, 501, state.Roles["poller"].ManagedUserID)
}

// A configured ownership resolver that omits a recorded, still-live account
// must not let cleanup report success and retire the ownership state.
func TestCleanupRetainsStateWhenResolverOmitsLiveRecordedAccount(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {ManagedUserID: 501},
	}}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	c := ServiceAccountTokenClient{
		SA:                    sa,
		VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil },
		ManagedAccountIDs:     func(context.Context, string, string) ([]int, error) { return nil, nil },
	}

	_, err := CleanupGitLabRoleIdentityLocked(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	assert.Empty(t, sa.deletedSAs)
	assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation], "rotation state is retained")

	// Once the account is really gone, an empty allowlist is not an error.
	sa.accounts = nil
	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Zero(t, n)
}

// Owners returned by the supplied-owner resolver are subtracted from the
// managed set even when state records no supplied owner ID, so a client with no
// service-account capability is not refused for an administrator-owned account.
func TestDeleteManagedServiceAccountsSubtractsResolverSuppliedOwners(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true, ManagedUserID: 501},
	}}))
	c := ServiceAccountTokenClient{SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil }}
	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Zero(t, n)
}

// Direct callers get a frozen supplied-owner set: a resolver that attributes the
// account on the first call and nothing on the second cannot expose it to deletion.
func TestDeleteManagedServiceAccountsFreezesSuppliedOwners(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true, ManagedUserID: 501},
	}}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	calls := 0
	c := ServiceAccountTokenClient{
		SA:                    sa,
		VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil },
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) {
			calls++
			if calls == 1 {
				return []int{501}, nil
			}
			return nil, nil
		},
	}
	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, sa.deletedSAs)
}

// Cleanup that is handed the ownership resolution frozen by the uninstall gate
// reuses it: a resolver that answers differently the second time is never
// consulted again, so the administrator-owned account keeps its state and is
// not deleted.
func TestCleanupReusesFrozenPreflightWithFlappingResolver(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true, ManagedUserID: 501},
	}}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	calls := 0
	c := ServiceAccountTokenClient{
		SA:                    sa,
		VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil },
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) {
			calls++
			if calls == 1 {
				return []int{501}, nil
			}
			return nil, nil
		},
	}
	plan, err := PlanGitLabRoleCleanup(ctx, fc, "g", "p", c)
	require.NoError(t, err)
	require.Equal(t, 1, calls)

	res, err := CleanupGitLabRoleIdentityLocked(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c, Preflight: plan})
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "cleanup must not resolve supplied owners again")
	assert.Zero(t, res.AccountsDeleted)
	assert.Empty(t, sa.deletedSAs)
}

// A generic ownership-aware client whose capability attributes nothing is
// refused at cleanup, like an absent capability.
func TestCleanupRefusesEmptyAttributionForOwnerAwareGenericClient(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true},
	}}))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.CoderTokenName, Active: true, UserID: 600})

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ownerAwareTokens{fakeTokens: tokens}})
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	assert.Empty(t, tokens.revoked)
	assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation], "rotation state is retained")
}

// An administrator-owned managed account resolved through SuppliedOwnerIDs does
// not need account-cleanup capability from a generic client.
func TestCleanupPreflightSubtractsResolvedOwnersFromManagedAccounts(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true, ManagedUserID: 501},
	}}))
	tokens := ownerAwareTokens{fakeTokens: &fakeTokens{}, ids: []int{501}}
	require.NoError(t, PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", tokens))

	// An unresolved owner that is not the managed account still needs the capability.
	other := ownerAwareTokens{fakeTokens: &fakeTokens{}, ids: []int{777}}
	err := PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "managed-account cleanup capability unavailable")
}

// Rotation with a generic client consults SuppliedOwnerIDs: a resolved owner's
// outgoing token is dropped without revocation, while a failing, empty or
// absent capability keeps the refusal.
func TestRotationGenericClientConsultsSuppliedOwnerCapability(t *testing.T) {
	now := time.Now()
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	run := func(t *testing.T, build func(*fakeTokens) ProjectAccessTokenClient) (RoleRotateResult, *fakeTokens, rotationRoleState) {
		ctx := context.Background()
		fc := seededRoleClient(t, gitlabroles.RoleCoder)
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
			"coder": {
				Supplied: true, IncomingID: 12, OutgoingIDs: []int{13}, CreatedTokenIDs: []int{12, 13}, Phase: rotationPhaseOverlapping,
				DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
			},
		}}))
		tokens := &fakeTokens{}
		tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 5))})
		tokens.seed(ProjectAccessToken{ID: 13, Name: rec.Credential.TokenName, Active: true, UserID: 501, ExpiresAt: GitLabPATExpiresAt(now)})
		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "g", Repo: "p", Client: fc, Tokens: build(tokens), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
		})
		require.NoError(t, err)
		state, _, err := loadRotationState(ctx, fc, "g", "p")
		require.NoError(t, err)
		return result, tokens, state.Roles["coder"]
	}

	t.Run("resolved", func(t *testing.T) {
		result, tokens, rs := run(t, func(f *fakeTokens) ProjectAccessTokenClient { return ownerAwareTokens{fakeTokens: f, ids: []int{501}} })
		assert.Empty(t, result.Failed)
		assert.Empty(t, tokens.revoked, "the resolved owner's token is never revoked")
		assert.Empty(t, rs.OutgoingIDs, "the excluded outgoing obligation is dropped")
	})
	unresolved := map[string]func(*fakeTokens) ProjectAccessTokenClient{
		"error": func(f *fakeTokens) ProjectAccessTokenClient {
			return ownerAwareTokens{fakeTokens: f, err: forge.ErrForbidden}
		},
		"empty":  func(f *fakeTokens) ProjectAccessTokenClient { return ownerAwareTokens{fakeTokens: f} },
		"absent": func(f *fakeTokens) ProjectAccessTokenClient { return f },
	}
	for name, build := range unresolved {
		t.Run(name, func(t *testing.T) {
			_, tokens, rs := run(t, build)
			assert.Empty(t, tokens.revoked)
			assert.Equal(t, []int{13}, rs.OutgoingIDs, "the outgoing obligation is kept")
		})
	}
}

// Owners resolved only through the SuppliedOwnerIDs capability are persisted
// before a managed replacement clears the supplied provenance, so a carried-
// forward outgoing token that is absent from the listing or ownerless is still
// never revoked by a later grace cleanup through a generic client.
func TestRotationReplacementPersistsCapabilityResolvedExclusions(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	cases := map[string][]ProjectAccessToken{
		"absent":    nil,
		"ownerless": {{ID: 40, Name: "renamed", Active: true, UserID: 0}},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
				"coder": {
					Supplied: true, IncomingID: 12, OutgoingIDs: []int{40}, CreatedTokenIDs: []int{12, 40}, Phase: rotationPhaseOverlapping,
					DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
				},
			}}))
			tokens := &fakeTokens{}
			tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 5))})
			for _, tok := range extra {
				tokens.seed(tok)
			}
			client := ownerAwareTokens{fakeTokens: tokens, ids: []int{501}}
			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "g", Repo: "p", Client: fc, Tokens: client, Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
				Force: true, Now: now,
			})
			require.NoError(t, err)
			require.Empty(t, result.Failed)
			require.Contains(t, result.Rotated, gitlabroles.RoleCoder)
			assert.Empty(t, tokens.revoked)

			state, _, err := loadRotationState(ctx, fc, "g", "p")
			require.NoError(t, err)
			rs := state.Roles["coder"]
			assert.False(t, rs.Supplied, "supplied provenance is cleared by the managed replacement")
			assert.Contains(t, rs.ExcludedUserIDs, 501, "the capability-resolved owner stays excluded")
			assert.Contains(t, rs.OutgoingIDs, 40, "the undischarged obligation is carried forward")

			// A later run past the grace period, with no ownership capability and no
			// supplied provenance left, must still refuse to revoke the carried token.
			later := now.Add(72 * time.Hour)
			_, err = RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
				Now: later,
			})
			require.NoError(t, err)
			assert.NotContains(t, tokens.revoked, 40, "an unverified carried-forward token is never revoked")
		})
	}
}

// The service-account branch freezes project-wide exclusions into grace
// cleanup, so an excluded owner's outgoing obligation is dropped rather than
// left overlapping behind a refused revocation.
func TestRotationServiceAccountClientDropsExcludedOutgoing(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {
			IncomingID: 12, OutgoingIDs: []int{13}, CreatedTokenIDs: []int{12}, Phase: rotationPhaseOverlapping,
			DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
		},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 5))})
	legacy.seed(ProjectAccessToken{ID: 13, Name: rec.Credential.TokenName, Active: true, UserID: 501, ExpiresAt: GitLabPATExpiresAt(now)})
	c := ServiceAccountTokenClient{
		SA: newFakeSAAPI(), Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
		SuppliedAccountIDs:    func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}

	_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
	})
	require.NoError(t, err)
	assert.Empty(t, legacy.revoked, "the excluded owner's token is never revoked")

	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, state.Roles["coder"].OutgoingIDs)
	assert.Equal(t, rotationPhaseIdle, state.Roles["coder"].Phase)
}
