package repos

import (
	"context"
	"errors"
	"strings"
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
		// A generic client cannot authenticate the enrolled credential or verify
		// its owner, so the role is reported as unverifiable and left untouched.
		require.Len(t, result.Failed, 1)
		assert.Contains(t, result.Failed[0].Reason, "credential left untouched")
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

// completedWriteFailer fails the rotation-state write that follows a successful
// publication of the role secret, so the completed-state write never lands.
type completedWriteFailer struct {
	*forge.FakeClient
	published bool
}

func (c *completedWriteFailer) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if err := c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
		return err
	}
	c.published = true
	return nil
}

func (c *completedWriteFailer) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if c.published && name == forge.VarGitLabRoleRotation {
		return forge.ErrForbidden
	}
	return c.FakeClient.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

// A capability-resolved owner is recorded before the replacement is published,
// so a failed completed-state write or an interrupted publication still leaves
// the replaced supplied credential's owner excluded.
func TestRotationReplacementRecordsResolvedOwnerBeforePublication(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	seed := func(t *testing.T, fc *forge.FakeClient) *fakeTokens {
		require.NoError(t, writeRotationState(context.Background(), fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
			"coder": {
				Supplied: true, IncomingID: 12, OutgoingIDs: []int{40}, CreatedTokenIDs: []int{12, 40}, Phase: rotationPhaseOverlapping,
				DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
			},
		}}))
		tokens := &fakeTokens{}
		tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 5))})
		return tokens
	}
	// A provided replacement needs a client that verifies the credential. The
	// replaced owner (501) is attributed through the project's supplied account
	// inventory and the replacement authenticates as its own owner (777).
	verifying := func(tokens *fakeTokens) ServiceAccountTokenClient {
		c := verifyingTokens(tokens, describedCoderIdentity(777, 13))
		c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return []int{501}, nil }
		return c
	}

	t.Run("completed state write fails after publication", func(t *testing.T) {
		ctx := context.Background()
		inner := seededRoleClient(t, gitlabroles.RoleCoder)
		fc := &completedWriteFailer{FakeClient: inner}
		tokens := seed(t, inner)
		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "g", Repo: "p", Client: fc, Tokens: ownerAwareTokens{fakeTokens: tokens, ids: []int{501}},
			Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
		})
		require.NoError(t, err)
		require.True(t, fc.published)
		require.NotEmpty(t, result.Failed, "the completed-state write failed")
		assert.Empty(t, tokens.revoked)

		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		rs := state.Roles["coder"]
		assert.Contains(t, rs.ExcludedUserIDs, 501, "the owner was recorded before publication")
		assert.Contains(t, rs.OutgoingIDs, 40)

		// A rerun past the grace period must not revoke the carried token.
		later := now.Add(72 * time.Hour)
		_, err = RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "g", Repo: "p", Client: inner, Tokens: tokens,
			Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: later,
		})
		require.NoError(t, err)
		assert.NotContains(t, tokens.revoked, 40)
	})

	t.Run("provided replacement whose publication fails", func(t *testing.T) {
		ctx := context.Background()
		inner := seededRoleClient(t, gitlabroles.RoleCoder)
		tokens := seed(t, inner)
		fc := &selectiveSecretClient{Client: inner, fail: map[string]error{rec.Credential.SecretName: forge.ErrForbidden}}
		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "g", Repo: "p", Client: fc, Tokens: verifying(tokens),
			Roles:          []gitlabroles.Role{gitlabroles.RoleCoder},
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "providedXXXX"}, Force: true, Now: now,
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Failed)

		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		assert.Contains(t, state.Roles["coder"].ExcludedUserIDs, 501, "the resolved owner is excluded on the provided-token path")
		assert.Contains(t, state.Roles["coder"].ExcludedUserIDs, 777, "the replacement's verified owner is excluded before publication")
	})

	t.Run("supplied to supplied replacement succeeds", func(t *testing.T) {
		ctx := context.Background()
		inner := seededRoleClient(t, gitlabroles.RoleCoder)
		tokens := seed(t, inner)
		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "g", Repo: "p", Client: inner, Tokens: verifying(tokens),
			Roles:          []gitlabroles.Role{gitlabroles.RoleCoder},
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "providedXXXX"}, Force: true, Now: now,
		})
		require.NoError(t, err)
		require.Empty(t, result.Failed)
		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		assert.Contains(t, state.Roles["coder"].ExcludedUserIDs, 501)
		assert.Contains(t, state.Roles["coder"].ExcludedUserIDs, 777)
	})
}

// Owners resolved through SuppliedAccountIDs are persisted before a managed
// replacement even when the optional AttributeSuppliedOwners callback is nil.
func TestRotationServiceAccountClientPersistsResolvedOwnersWithoutCallback(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {
			Supplied: true, IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle,
			DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
		},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 5))})
	c := ServiceAccountTokenClient{
		SA: newFakeSAAPI(), Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
		SuppliedAccountIDs:    func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}
	require.Nil(t, c.AttributeSuppliedOwners)

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	})
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Contains(t, result.Rotated, gitlabroles.RoleCoder)

	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	rs := state.Roles["coder"]
	assert.False(t, rs.Supplied)
	assert.Contains(t, rs.ExcludedUserIDs, 501, "the frozen resolved owner is persisted without the attribution callback")
}

// A managed replacement of an outside-inventory supplied credential whose
// completed-state write failed leaves phase=distributing with the replacement
// current. The supplied-credential retention shortcut must not mask that
// interrupted recovery: an unforced rerun recovers the lifecycle state, clears
// the supplied provenance, and keeps the owner excluded.
//
// The in-window variant keeps DistributedAt inside IdempotentRotationWindow, so
// the idempotency shortcut must not report "already rotated" either.
func TestRotateGitLabRoleCredentials_RecoversInterruptedManagedReplacementOfSuppliedCredential(t *testing.T) {
	for _, tc := range []struct {
		name          string
		distributedAt time.Duration
	}{
		{name: "outside the idempotency window", distributedAt: -48 * time.Hour},
		{name: "inside the idempotency window", distributedAt: -time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
				"coder": {
					Supplied: true, SuppliedUserID: 777, SuppliedTokenID: 13, ExcludedUserIDs: []int{777},
					IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseDistributing,
					DistributedAt: now.Add(tc.distributedAt).Format(time.RFC3339),
				},
			}}))
			tokens := &fakeTokens{}
			tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 5))})

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "g", Repo: "p", Client: fc, Tokens: tokens,
				Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
			})
			require.NoError(t, err)
			require.Empty(t, result.Failed)
			assert.Contains(t, result.Rotated, gitlabroles.RoleCoder, "the interrupted replacement is recovered, not retained")
			assert.Empty(t, tokens.revoked)

			state, _, err := loadRotationState(ctx, fc, "g", "p")
			require.NoError(t, err)
			rs := state.Roles["coder"]
			assert.False(t, rs.Supplied, "the incomplete supplied provenance is cleared")
			assert.NotEqual(t, rotationPhaseDistributing, rs.Phase)
			assert.Contains(t, rs.ExcludedUserIDs, 777, "the supplied owner stays excluded")
		})
	}
}

// afterSecretStateFailer fails rotation-state writes once the named secret has
// been published, so the completion write that follows publication never lands.
type afterSecretStateFailer struct {
	*forge.FakeClient
	secret    string
	published bool
}

func (c *afterSecretStateFailer) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if err := c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
		return err
	}
	if name == c.secret {
		c.published = true
	}
	return nil
}

func (c *afterSecretStateFailer) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if c.published && name == forge.VarGitLabRoleRotation {
		return forge.ErrForbidden
	}
	return c.FakeClient.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

// A managed credential provisioned over leftover supplied state records a
// recoverable intent before publication, so a failed completion write is
// resolved by a later unforced run instead of being retained forever as the
// supplied credential.
func TestProvisionOverSuppliedStateRecoversFromFailedCompletionWrite(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	seedSupplied := func(t *testing.T, fc *forge.FakeClient) {
		require.NoError(t, writeRotationState(context.Background(), fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
			"coder": {
				Supplied: true, SuppliedUserID: 600, ExcludedUserIDs: []int{600}, Phase: rotationPhaseIdle,
				DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
			},
		}}))
	}

	t.Run("failed completion write is recovered by an unforced rotation", func(t *testing.T) {
		ctx := context.Background()
		inner := provisionClient(t)
		seedSupplied(t, inner)
		fc := &afterSecretStateFailer{FakeClient: inner, secret: rec.Credential.SecretName}
		tokens := &fakeTokens{}

		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Now: now})
		require.NoError(t, err)
		require.True(t, fc.published)
		require.Contains(t, result.Created, gitlabroles.RoleCoder)

		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		rs := state.Roles["coder"]
		require.NotZero(t, rs.IncomingID, "the replacement token was recorded as an intent")
		assert.Equal(t, rotationPhaseDistributing, rs.Phase)
		assert.True(t, rs.Supplied, "provenance is only cleared once completion lands")
		assert.Contains(t, rs.ExcludedUserIDs, 600)
		intentID := rs.IncomingID

		// An ordinary unforced run recovers instead of retaining the role as the
		// supplied credential.
		later := now.Add(time.Hour)
		rotated, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "g", Repo: "p", Client: inner, Tokens: tokens,
			Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: later,
		})
		require.NoError(t, err)
		require.Empty(t, rotated.Failed)
		require.Contains(t, rotated.Rotated, gitlabroles.RoleCoder)
		assert.NotContains(t, tokens.revoked, intentID, "the possibly published token is never revoked")

		state, _, err = loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		rs = state.Roles["coder"]
		assert.False(t, rs.Supplied)
		assert.NotEqual(t, intentID, rs.IncomingID)
		assert.Contains(t, rs.OutgoingIDs, intentID)
		assert.Contains(t, rs.ExcludedUserIDs, 600)
	})

	t.Run("failed intent write refuses to publish", func(t *testing.T) {
		ctx := context.Background()
		inner := provisionClient(t)
		seedSupplied(t, inner)
		fc := &rotationStateWriteFailer{FakeClient: inner, marker: `"phase":"distributing"`}
		tokens := &fakeTokens{}

		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Now: now})
		require.NoError(t, err)
		require.NotEmpty(t, result.Failed)
		assert.False(t, inner.Secrets["g/p/"+rec.Credential.SecretName], "the credential is not published")
		assert.NotEmpty(t, tokens.revoked, "the unpublished token is revoked")
		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		assert.True(t, state.Roles["coder"].Supplied)
		assert.Equal(t, rotationPhaseIdle, state.Roles["coder"].Phase)
	})

	t.Run("failed publication restores the previous entry", func(t *testing.T) {
		ctx := context.Background()
		inner := provisionClient(t)
		seedSupplied(t, inner)
		fc := &selectiveSecretClient{Client: inner, fail: map[string]error{rec.Credential.SecretName: forge.ErrForbidden}}
		tokens := &fakeTokens{}

		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Now: now})
		require.NoError(t, err)
		require.NotEmpty(t, result.Failed)

		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		rs := state.Roles["coder"]
		assert.True(t, rs.Supplied)
		assert.Equal(t, rotationPhaseIdle, rs.Phase)
		assert.Zero(t, rs.IncomingID)
		assert.Contains(t, rs.ExcludedUserIDs, 600)
	})
}

// A managed publication whose secret write commits and then reports an error
// keeps its provisioning intent and never revokes the replacement, for a fresh
// role and for one provisioned over stale supplied state, whether the existence
// read-back shows the secret or itself fails. An unforced run then recovers.
func TestProvisionManagedAmbiguousPublicationRetainsIntent(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	for _, tc := range []struct {
		name         string
		seed         map[string]rotationRoleState
		wantSupplied bool
	}{
		{name: "fresh managed"},
		{name: "ownership-only entry", seed: map[string]rotationRoleState{"coder": {ManagedUserID: 70, ExcludedUserIDs: []int{600}}}},
		{
			name: "stale supplied state", wantSupplied: true,
			seed: map[string]rotationRoleState{"coder": {
				Supplied: true, SuppliedUserID: 600, ExcludedUserIDs: []int{600}, Phase: rotationPhaseIdle,
				DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
			}},
		},
	} {
		for readName, readBackErr := range map[string]error{
			"read-back shows the secret": nil,
			"read-back fails":            errors.New("lookup failed"),
		} {
			t.Run(tc.name+": "+readName, func(t *testing.T) {
				ctx := context.Background()
				inner := provisionClient(t)
				if tc.seed != nil {
					require.NoError(t, writeRotationState(ctx, inner, "g", "p", rotationStateFile{Roles: tc.seed}))
				}
				client := &commitThenErrorSecretClient{Client: inner, readBackErr: readBackErr}
				tokens := &fakeTokens{}

				// provisionOwnRoles is called directly: a failing read-back
				// would otherwise abort the up-front presence scan.
				var result RoleProvisionResult
				provisionOwnRoles(ctx, RoleProvisionConfig{
					Owner: "g", Repo: "p", Client: client, Tokens: tokens, Registry: gitlabroles.BuiltinRegistry(), Now: now,
				}, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)
				var coderFailed bool
				for _, f := range result.Failed {
					coderFailed = coderFailed || f.Role == gitlabroles.RoleCoder
				}
				require.True(t, coderFailed)
				assert.NotContains(t, result.Created, gitlabroles.RoleCoder)
				assert.True(t, inner.Secrets["g/p/"+rec.Credential.SecretName], "the write committed")
				assert.Empty(t, tokens.revoked, "a possibly installed replacement is never revoked")

				state, _, err := loadRotationState(ctx, inner, "g", "p")
				require.NoError(t, err)
				rs := state.Roles["coder"]
				require.NotZero(t, rs.IncomingID, "the provisioning intent is retained")
				assert.Equal(t, rotationPhaseDistributing, rs.Phase)
				assert.Equal(t, tc.wantSupplied, rs.Supplied)
				assert.Equal(t, tc.seed["coder"].ManagedUserID, rs.ManagedUserID)
				assert.Equal(t, tc.seed["coder"].ExcludedUserIDs, rs.ExcludedUserIDs)
				intentID := rs.IncomingID

				provs, err := RoleCredentialProvenances(ctx, inner, "g", "p")
				require.NoError(t, err)
				assert.True(t, provs[gitlabroles.RoleCoder].Known, "the installed credential is not unknown provenance")

				// An ordinary unforced run replaces the possibly installed
				// credential instead of refusing or retaining it.
				rotated, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
					Owner: "g", Repo: "p", Client: inner, Tokens: tokens,
					Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now.Add(time.Hour),
				})
				require.NoError(t, err)
				require.Empty(t, rotated.Failed)
				require.Contains(t, rotated.Rotated, gitlabroles.RoleCoder)
				assert.NotContains(t, tokens.revoked, intentID)

				state, _, err = loadRotationState(ctx, inner, "g", "p")
				require.NoError(t, err)
				rs = state.Roles["coder"]
				assert.False(t, rs.Supplied)
				assert.NotEqual(t, intentID, rs.IncomingID)
				assert.Contains(t, rs.OutgoingIDs, intentID)
				for _, id := range tc.seed["coder"].ExcludedUserIDs {
					assert.Contains(t, rs.ExcludedUserIDs, id, "earlier exclusions are preserved")
				}
			})
		}
	}
}

// A fresh managed credential records a recoverable intent before publication
// too, so a failed completion write leaves state that proves the credential's
// managed provenance and that an unforced run recovers, rather than an
// ownership-only entry that reads as unknown provenance.
func TestProvisionFreshManagedCredentialRecoversFromFailedCompletionWrite(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	for _, tc := range []struct {
		name string
		seed map[string]rotationRoleState
	}{
		{name: "no entry"},
		{name: "ownership-only entry", seed: map[string]rotationRoleState{"coder": {ManagedUserID: 70, ExcludedUserIDs: []int{600}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			inner := provisionClient(t)
			if tc.seed != nil {
				require.NoError(t, writeRotationState(ctx, inner, "g", "p", rotationStateFile{Roles: tc.seed}))
			}
			fc := &afterSecretStateFailer{FakeClient: inner, secret: rec.Credential.SecretName}
			tokens := &fakeTokens{}

			result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Now: now})
			require.NoError(t, err)
			require.True(t, fc.published)
			require.Contains(t, result.Created, gitlabroles.RoleCoder)

			state, _, err := loadRotationState(ctx, inner, "g", "p")
			require.NoError(t, err)
			rs := state.Roles["coder"]
			require.NotZero(t, rs.IncomingID, "the published token was recorded as an intent")
			assert.Equal(t, rotationPhaseDistributing, rs.Phase)
			assert.False(t, rs.Supplied)
			assert.Equal(t, tc.seed["coder"].ManagedUserID, rs.ManagedUserID, "ownership is preserved")
			assert.Equal(t, tc.seed["coder"].ExcludedUserIDs, rs.ExcludedUserIDs, "exclusions are preserved")
			intentID := rs.IncomingID

			provs, err := RoleCredentialProvenances(ctx, inner, "g", "p")
			require.NoError(t, err)
			assert.True(t, provs[gitlabroles.RoleCoder].Known, "the unproven credential is not unknown provenance")

			later := now.Add(time.Hour)
			rotated, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "g", Repo: "p", Client: inner, Tokens: tokens,
				Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: later,
			})
			require.NoError(t, err)
			require.Empty(t, rotated.Failed)
			require.Contains(t, rotated.Rotated, gitlabroles.RoleCoder)
			assert.NotContains(t, tokens.revoked, intentID, "the possibly published token is never revoked")

			state, _, err = loadRotationState(ctx, inner, "g", "p")
			require.NoError(t, err)
			rs = state.Roles["coder"]
			assert.NotEqual(t, intentID, rs.IncomingID)
			assert.Contains(t, rs.OutgoingIDs, intentID)
			assert.NotEqual(t, rotationPhaseDistributing, rs.Phase)
		})
	}

	t.Run("successful completion records proof", func(t *testing.T) {
		ctx := context.Background()
		inner := provisionClient(t)
		require.NoError(t, writeRotationState(ctx, inner, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
			"coder": {ManagedUserID: 70},
		}}))
		_, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: inner, Tokens: &fakeTokens{}, Now: now})
		require.NoError(t, err)
		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		rs := state.Roles["coder"]
		assert.Equal(t, rotationPhaseIdle, rs.Phase)
		assert.NotZero(t, rs.IncomingID)
		assert.NotEmpty(t, rs.DistributedAt)
		assert.Equal(t, 70, rs.ManagedUserID)
	})

	t.Run("failed publication removes the intent", func(t *testing.T) {
		ctx := context.Background()
		inner := provisionClient(t)
		fc := &selectiveSecretClient{Client: inner, fail: map[string]error{rec.Credential.SecretName: forge.ErrForbidden}}
		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: &fakeTokens{}, Now: now})
		require.NoError(t, err)
		require.NotEmpty(t, result.Failed)
		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		_, ok := state.Roles["coder"]
		assert.False(t, ok, "no intent-only entry is left for an unpublished credential")
	})

	t.Run("failed publication restores an ownership-only entry", func(t *testing.T) {
		ctx := context.Background()
		inner := provisionClient(t)
		require.NoError(t, writeRotationState(ctx, inner, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
			"coder": {ManagedUserID: 70},
		}}))
		fc := &selectiveSecretClient{Client: inner, fail: map[string]error{rec.Credential.SecretName: forge.ErrForbidden}}
		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: &fakeTokens{}, Now: now})
		require.NoError(t, err)
		require.NotEmpty(t, result.Failed)
		state, _, err := loadRotationState(ctx, inner, "g", "p")
		require.NoError(t, err)
		rs := state.Roles["coder"]
		assert.Equal(t, 70, rs.ManagedUserID)
		assert.Zero(t, rs.IncomingID)
		assert.Empty(t, rs.Phase)
	})
}

// Status judges an enrolled supplied credential on its own lifecycle: a revoked
// supplied token is reported as drift even while a historical managed token
// listed under the same name stays healthy.
func TestEnrichGitLabRoleStatusJudgesEnrolledSuppliedCredential(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	fc := provisionClient(t)
	for _, name := range []string{forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken} {
		fc.Secrets["group/project/"+name] = true
	}
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {
			Supplied: true, SuppliedUserID: 777, SuppliedTokenID: 13, ExcludedUserIDs: []int{777},
			CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: "2026-09-20T00:00:00Z",
		},
	}}))
	healthy := GitLabPATExpiresAt(now.AddDate(0, 6, 0))
	tokens := []ProjectAccessToken{
		{ID: 12, Name: gitlabroles.CoderTokenName, Active: true, UserID: 70, ExpiresAt: healthy},
		{ID: 13, Name: gitlabroles.CoderTokenName, Active: false, Revoked: true, UserID: 777, ExpiresAt: healthy},
	}

	status := &RepoStatus{}
	EnrichGitLabRoleStatus(ctx, fc, "group", "project", tokens, now, status)
	var actuals []string
	for _, d := range status.Drifts {
		actuals = append(actuals, d.Field+"="+d.Actual)
	}
	assert.Contains(t, actuals, "gitlab-role:coder=revoked")

	// Without the enrolled credential in the inventory (an outside-inventory
	// personal access token), the recorded historical managed token must not
	// drive the diagnosis; the installed-credential check decides health.
	assert.Empty(t, suppliedAwareInventory(gitlabroles.BuiltinRegistry(), rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true, SuppliedTokenID: 99, CreatedTokenIDs: []int{12}},
	}}, tokens[:1]))
	assert.Nil(t, suppliedAwareInventory(gitlabroles.BuiltinRegistry(), rotationStateFile{}, nil))
}

// Enrolling an administrator-provided credential over an overlapping managed
// entry keeps the previously distributed managed tokens recorded as outgoing
// obligations and reports that nothing retires them automatically, instead of
// silently forgetting them. Grace cleanup never revokes the enrolled supplied
// credential.
func TestProvisionEnrollmentRetainsManagedObligations(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	inner := provisionClient(t)
	require.NoError(t, writeRotationState(ctx, inner, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {
			ManagedUserID: 70, CreatedTokenIDs: []int{11, 12}, Phase: rotationPhaseOverlapping,
			IncomingID: 12, OutgoingIDs: []int{11}, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339),
		},
	}}))
	// The other roles are installed already; only coder enrolls.
	for _, name := range []string{forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken} {
		inner.Secrets["g/p/"+name] = true
	}
	tokens := &fakeTokens{}
	// Enrollment needs a client that verifies the credential, and that client
	// reconciles coder's recorded managed account, so the account is present.
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 70, Name: gitlabroles.CoderTokenName}}
	verified := verifyingTokens(tokens, describedCoderIdentity(777, 13))
	verified.SA, verified.ManagedAccountIDs = sa, sa.ownedIDs

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner: "g", Repo: "p", Client: inner, Tokens: verified, Now: now,
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "providedXXXX"},
	})
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Contains(t, result.Enrolled, gitlabroles.RoleCoder)
	assert.Contains(t, strings.Join(result.Diagnostics, "\n"), "2 previously distributed managed credential(s)")

	state, _, err := loadRotationState(ctx, inner, "g", "p")
	require.NoError(t, err)
	rs := state.Roles["coder"]
	assert.True(t, rs.Supplied)
	assert.Equal(t, rotationPhaseIdle, rs.Phase, "the supplied distribution proof is unchanged")
	assert.Zero(t, rs.IncomingID)
	assert.ElementsMatch(t, []int{11, 12}, rs.OutgoingIDs)

	// An unforced run keeps retaining the enrolled credential and revokes nothing.
	_, err = RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: inner, Tokens: tokens,
		Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now.Add(72 * time.Hour),
	})
	require.NoError(t, err)
	assert.Empty(t, tokens.revoked)
}

// A resolved identity without a positive token ID leaves the requested
// credential as unprotected as a failed lookup, so grace cleanup is deferred.
func TestResolveRequestedSuppliedTokenIDs_NonpositiveTokenIDIsUnresolved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	provided := map[gitlabroles.Role]string{gitlabroles.RoleCoder: "administrator-coder-token"}
	identityOf := func(id SuppliedIdentity) ServiceAccountTokenClient {
		return ServiceAccountTokenClient{
			SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) { return id, nil },
		}
	}

	ids, unresolved := resolveRequestedSuppliedTokenIDs(ctx, identityOf(SuppliedIdentity{TokenID: 13}), provided, nil)
	assert.Equal(t, []int{13}, ids)
	assert.False(t, unresolved)

	ids, unresolved = resolveRequestedSuppliedTokenIDs(ctx, identityOf(SuppliedIdentity{}), provided, nil)
	assert.Empty(t, ids)
	assert.True(t, unresolved, "a zero token ID is unresolved")

	ids, unresolved = resolveRequestedSuppliedTokenIDs(ctx, &fakeTokens{}, provided, nil)
	assert.Empty(t, ids)
	assert.True(t, unresolved, "a generic client cannot identify the credential")

	ids, unresolved = resolveRequestedSuppliedTokenIDs(ctx, ServiceAccountTokenClient{}, provided, nil)
	assert.Empty(t, ids)
	assert.True(t, unresolved, "a client without the identity callback cannot identify the credential")
}

// A forced administrator-supplied replacement is refused when project-wide
// supplied ownership could not be resolved, before the previous owner would be
// replaced without an exclusion recorded for it.
func TestRotateGitLabRoleCredentials_ForcedSuppliedReplacementRefusedWhenOwnerUnresolved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, ownerlessCoderState, true))
	writesBefore := coderSecretWrites(fc)

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
		Force: true, Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "administrator-coder-token"},
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "ownership is unresolved")
	assert.Empty(t, result.Rotated)
	assert.Equal(t, writesBefore, coderSecretWrites(fc), "the installed credential is untouched")
}
