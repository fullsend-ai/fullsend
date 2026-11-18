package repos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Enrollment resolves the supplied credential's owner and token ID while it
// still authenticates, so a later expiry cannot make the owner unattributable.
func TestProvisionGitLabRoleCredentials_EnrollmentRecordsSuppliedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "administrator-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	reg := gitlabroles.BuiltinRegistry()

	newClient := func(identity func(context.Context, string) (SuppliedIdentity, error)) ServiceAccountTokenClient {
		sa := newFakeSAAPI()
		return ServiceAccountTokenClient{
			SA:                         sa,
			Legacy:                     &fakeTokens{},
			ManagedAccountIDs:          sa.ownedIDs,
			SuppliedCredentialIdentity: identity,
			VerifySuppliedAccess:       allowSuppliedAccess,
			VerifyToken:                acceptReplacementToken,
		}
	}

	t.Run("owner, token and exclusion are persisted", func(t *testing.T) {
		fc := provisionClient(t)
		var seen []string
		tokens := newClient(func(_ context.Context, token string) (SuppliedIdentity, error) {
			seen = append(seen, token)
			return describedCoderIdentity(777, 13), nil
		})

		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Registry: reg, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		})
		require.NoError(t, err)
		assert.Contains(t, result.Enrolled, gitlabroles.RoleCoder)
		require.NotEmpty(t, seen)
		for _, token := range seen {
			assert.Equal(t, provided, token, "the identity is resolved from the enrolled value")
		}

		rs := readRoleState(t, fc, gitlabroles.RoleCoder)
		assert.True(t, rs.Supplied)
		assert.Equal(t, 777, rs.SuppliedUserID)
		assert.Equal(t, 13, rs.SuppliedTokenID)
		assert.Equal(t, []int{777}, rs.ExcludedUserIDs)
		assert.NotContains(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation], provided)
	})

	for name, authErr := range map[string]error{
		"an authoritative rejection": errors.New("unauthorized"),
		"a transient failure":        context.DeadlineExceeded,
	} {
		t.Run(name+" fails the role without publishing or recording enrollment", func(t *testing.T) {
			fc := provisionClient(t)
			tokens := newClient(func(context.Context, string) (SuppliedIdentity, error) {
				return SuppliedIdentity{}, authErr
			})
			writesBefore := coderSecretWrites(fc)

			result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Registry: reg, Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			})
			require.NoError(t, err)
			assert.NotContains(t, result.Enrolled, gitlabroles.RoleCoder)
			var reason string
			for _, f := range result.Failed {
				if f.Role == gitlabroles.RoleCoder {
					reason = f.Reason
				}
			}
			assert.Contains(t, reason, "administrator-provided credential rejected")
			assert.Contains(t, reason, "could not be verified")
			assert.NotContains(t, reason, provided)
			assert.Equal(t, writesBefore, coderSecretWrites(fc), "the unverified credential is not published")
			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.False(t, rs.Supplied, "no supplied provenance is recorded")
			assert.Zero(t, rs.SuppliedUserID)
		})
	}

	t.Run("an identity without token metadata is rejected", func(t *testing.T) {
		fc := provisionClient(t)
		tokens := newClient(func(context.Context, string) (SuppliedIdentity, error) {
			return SuppliedIdentity{UserID: 777, TokenID: 13}, nil
		})
		writesBefore := coderSecretWrites(fc)

		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Registry: reg, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		})
		require.NoError(t, err)
		assert.NotContains(t, result.Enrolled, gitlabroles.RoleCoder)
		assert.Equal(t, writesBefore, coderSecretWrites(fc))
		assert.False(t, readRoleState(t, fc, gitlabroles.RoleCoder).Supplied)
	})
}

// Replacing a supplied credential with another supplied credential attributes
// the replacement to its own owner and keeps the replaced owner excluded.
func TestRotateGitLabRoleCredentials_ProvidedReplacementRecordsOwnerAndKeepsPreviousExcluded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600]}}}`, true))
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
			return describedCoderIdentity(777, 13), nil
		},
		VerifySuppliedAccess: allowSuppliedAccess,
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true,
		Now:            time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "replacement-coder-token"},
	})
	require.NoError(t, err)
	require.Equal(t, []gitlabroles.Role{gitlabroles.RoleCoder}, result.Rotated)

	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.True(t, rs.Supplied)
	assert.Equal(t, 777, rs.SuppliedUserID)
	assert.Equal(t, 13, rs.SuppliedTokenID)
	assert.Equal(t, []int{600, 777}, rs.ExcludedUserIDs)
}

// A requested supplied credential that is still listed as an outgoing token
// past its grace period is resolved before cleanup, so grace cleanup does not
// revoke it ahead of its own enrollment.
func TestRotateGitLabRoleCredentials_EnrollingOutgoingTokenIsNotRevokedByCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"overlapping","created_token_ids":[13,14],"incoming_id":14,"outgoing_ids":[13],"distributed_at":"2026-09-01T00:00:00Z"}}}`, true))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-01-01", UserID: 40})
	legacy.seed(ProjectAccessToken{ID: 14, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-01-01", UserID: 40})
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            legacy,
		ManagedAccountIDs: sa.ownedIDs,
		ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
		},
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
			return describedCoderIdentity(40, 13), nil
		},
		VerifySuppliedAccess: allowSuppliedAccess,
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true,
		Now:            time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "enrolled-coder-token"},
	})
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	assert.NotContains(t, legacy.revoked, 13, "the credential being enrolled must not be revoked as an expired outgoing token")
	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.True(t, rs.Supplied)
	assert.Equal(t, 13, rs.SuppliedTokenID)
	assert.Equal(t, 40, rs.SuppliedUserID)
}

// An owner that was enrolled without being recorded is attributed and
// persisted by rotation before any replacement, so the exclusion survives the
// replacement and a managed rotation never drops it.
func TestRotateGitLabRoleCredentials_PersistsUnrecordedSuppliedOwnerBeforeReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z"}}}`, true))
	sa := newFakeSAAPI()
	attributions := 0
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
		VerifyToken:       acceptReplacementToken,
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) {
			return []int{777}, nil
		},
		AttributeSuppliedOwners: func(context.Context, string, string) (map[gitlabroles.Role]int, error) {
			attributions++
			return map[gitlabroles.Role]int{gitlabroles.RoleCoder: 777}, nil
		},
	}

	_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true,
		Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, attributions, "the unrecorded owner is attributed")

	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.Contains(t, rs.ExcludedUserIDs, 777, "the owner's exclusion is durable after rotation")
	// The managed replacement is not the supplied credential: its provenance and
	// the supplied inventories no longer name the historical owner or token.
	assert.False(t, provenanceOf(rs).Supplied, "the managed replacement is not supplied")
	assert.False(t, rs.Supplied)
	assert.False(t, rs.SuppliedDistributed)
	assert.Zero(t, rs.SuppliedUserID)
	assert.Zero(t, rs.SuppliedTokenID)
	assert.Positive(t, rs.IncomingID)
}

// Enrolling a supplied credential over a leftover rotation-state entry
// (the credential variable was removed but its entry remained) replaces that
// entry with the new credential's provenance and keeps every previous owner
// permanently excluded.
func TestProvisionGitLabRoleCredentials_EnrollmentReplacesLeftoverEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "administrator-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	reg := gitlabroles.BuiltinRegistry()
	enroll := func(t *testing.T, seed string) (*forge.FakeClient, rotationRoleState) {
		fc := provisionClient(t)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
		sa := newFakeSAAPI()
		tokens := ServiceAccountTokenClient{
			SA:                sa,
			Legacy:            &fakeTokens{},
			ManagedAccountIDs: sa.ownedIDs,
			VerifyToken:       acceptReplacementToken,
			SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
				return describedCoderIdentity(777, 13), nil
			},
			VerifySuppliedAccess: allowSuppliedAccess,
		}
		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Registry: reg, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		})
		require.NoError(t, err)
		require.Contains(t, result.Enrolled, gitlabroles.RoleCoder)
		return fc, readRoleState(t, fc, gitlabroles.RoleCoder)
	}

	t.Run("supplied A to supplied B", func(t *testing.T) {
		_, rs := enroll(t, `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600]}}}`)
		assert.True(t, rs.Supplied)
		assert.Equal(t, 777, rs.SuppliedUserID)
		assert.Equal(t, 13, rs.SuppliedTokenID)
		assert.Equal(t, []int{600, 777}, rs.ExcludedUserIDs)
		assert.Equal(t, now.Format(time.RFC3339), rs.DistributedAt)
	})

	t.Run("managed to supplied", func(t *testing.T) {
		_, rs := enroll(t, `{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9]}}}`)
		assert.True(t, provenanceOf(rs).Supplied)
		assert.Equal(t, 777, rs.SuppliedUserID)
		assert.Equal(t, []int{777}, rs.ExcludedUserIDs)
		assert.Zero(t, rs.IncomingID)
		assert.Equal(t, []int{9}, rs.CreatedTokenIDs, "creation records are kept")
	})
}

// A healthy supplied credential the project inventory cannot list (a personal
// access token) is retained by an unforced rotation, not replaced as unverified.
func TestRotateGitLabRoleCredentials_RetainsSuppliedCredentialOutsideInventory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777]}}}`, true))
	sa := newFakeSAAPI()
	tokens := healthyInstalledTokens(&fakeTokens{})
	tokens.SA = sa
	tokens.ManagedAccountIDs = sa.ownedIDs

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
		Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, result.Failed)
	assert.Contains(t, result.Skipped, gitlabroles.RoleCoder)
	assert.Empty(t, sa.createdSAs, "no managed account is created to replace a healthy supplied credential")

	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.True(t, rs.Supplied)
	assert.Equal(t, 777, rs.SuppliedUserID)
}

// After managed-to-supplied enrollment the earlier managed tokens stay in the
// inventory under the role name. An expiring or revoked one must not bypass the
// retention of the healthy supplied credential on an unforced rotation.
func TestRotateGitLabRoleCredentials_RetainsSuppliedCredentialWithHistoricalManagedTokensListed(t *testing.T) {
	t.Parallel()
	for name, historical := range map[string]ProjectAccessToken{
		"expiring": {ID: 9, Active: true, ExpiresAt: "2026-10-03", UserID: 600},
		"revoked":  {ID: 9, Active: false, Revoked: true, ExpiresAt: "2026-10-03", UserID: 600},
	} {
		historical := historical
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
				`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777],"created_token_ids":[9],"managed_user_id":600}}}`, true))
			historical.Name = gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName
			legacy := &fakeTokens{}
			legacy.seed(historical)

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: healthyInstalledTokens(legacy),
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
				Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
			})
			require.NoError(t, err)
			assert.Empty(t, result.Rotated)
			assert.Empty(t, result.Failed)
			assert.Contains(t, result.Skipped, gitlabroles.RoleCoder)
			assert.Empty(t, legacy.created, "no credential is minted over a healthy supplied credential")

			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.True(t, rs.Supplied)
			assert.Equal(t, 777, rs.SuppliedUserID)
		})
	}
}

// A healthy historical managed token listed beside a revoked supplied credential
// under the role's token name must not make the role read as healthy: lifecycle
// health follows the recorded supplied token, so the unforced rotation replaces it.
func TestRotateGitLabRoleCredentials_RevokedSuppliedCredentialNotMaskedByHealthyHistoricalToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777],"created_token_ids":[9],"managed_user_id":600}}}`, true))
	tokenName := gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 9, Name: tokenName, Active: true, ExpiresAt: "2027-09-30", UserID: 600})
	legacy.seed(ProjectAccessToken{ID: 13, Name: tokenName, Active: false, Revoked: true, ExpiresAt: "2026-10-03", UserID: 777})

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: rejectedInstalledTokens(legacy),
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
		Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Empty(t, result.Failed)
	assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder, "a revoked supplied credential is not reported healthy")
	assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
}

// A historical managed token whose expiry equals the fresh expiry must not make
// an unforced rotation read as already fresh when the enrolled supplied
// credential beside it is revoked.
func TestRotateGitLabRoleCredentials_RevokedSuppliedNotMaskedByFreshExpiryHistoricalToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777],"created_token_ids":[9],"managed_user_id":600}}}`, true))
	tokenName := gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 9, Name: tokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now), UserID: 600})
	legacy.seed(ProjectAccessToken{ID: 13, Name: tokenName, Active: false, Revoked: true, ExpiresAt: "2026-10-03", UserID: 777})

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: rejectedInstalledTokens(legacy),
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
		Now: now,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Failed)
	assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder, "a revoked supplied credential is not reported already fresh")
	assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
}

func TestRotateGitLabRoleCredentials_UnrecordableSuppliedOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z"}}}`, true))
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
		VerifyToken:       acceptReplacementToken,
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) {
			return []int{777}, nil
		},
		AttributeSuppliedOwners: func(context.Context, string, string) (map[gitlabroles.Role]int, error) {
			return nil, errors.New("unauthorized")
		},
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true,
		Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "supplied credential owners could not be recorded; no credential rotated"+ownerlessSuppliedRecoveryHint, result.Failed[0].Reason)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, sa.createdSAs, "no account is created when the owner cannot be recorded")
}

// Missing-secret provisioning over a stale supplied entry publishes a managed
// credential, so the supplied provenance is cleared while the previous supplied
// owner stays excluded and creation records are kept.
func TestProvisionGitLabRoleCredentials_ManagedProvisioningReplacesSuppliedEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fc := provisionClient(t)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,`+
			`"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600],"created_token_ids":[4]}}}`, true))
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Now: now,
	})
	require.NoError(t, err)
	require.Contains(t, result.Created, gitlabroles.RoleCoder)
	for _, d := range result.Diagnostics {
		assert.NotContains(t, d, "recording rotation-state")
	}

	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.False(t, provenanceOf(rs).Supplied, "the published credential is managed")
	assert.False(t, rs.Supplied)
	assert.Zero(t, rs.SuppliedUserID)
	assert.Zero(t, rs.SuppliedTokenID)
	assert.NotZero(t, rs.IncomingID)
	assert.Equal(t, now.Format(time.RFC3339), rs.DistributedAt)
	assert.Equal(t, []int{600}, rs.ExcludedUserIDs, "the previous supplied owner stays excluded")
	assert.Equal(t, []int{4}, rs.CreatedTokenIDs, "creation records are kept")
}

// A supplied credential is never published when its enrollment state cannot be
// persisted first, so state never describes a published supplied credential as
// managed.
func TestProvisionGitLabRoleCredentials_EnrollmentStateWriteFailureRefusesToPublish(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9]}}}`, true))
	// The supplied credential verifies, so enrollment reaches its state write,
	// which is the only write that fails.
	client := &rotationStateWriteFailer{FakeClient: fc, marker: `"supplied_user_id":777`}
	before := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]

	// Drive the role loop directly: the registry write that precedes it is
	// not under test.
	var result RoleProvisionResult
	provisionOwnRoles(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: client,
		Tokens:         verifyingTokens(&fakeTokens{}, describedCoderIdentity(777, 13)),
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "administrator-coder-token"},
	}, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)

	var failed bool
	for _, f := range result.Failed {
		if f.Role == gitlabroles.RoleCoder {
			failed = true
			assert.Contains(t, f.Reason, "recording administrator-provided credential enrollment state failed")
			assert.Contains(t, f.Reason, "credential not stored")
		}
	}
	assert.True(t, failed)
	assert.Positive(t, client.failedWrites, "the enrollment state write was attempted and failed")
	assert.NotContains(t, result.Enrolled, gitlabroles.RoleCoder)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabCoderToken], "the supplied credential is not published")
	for _, rec := range fc.CreatedSecrets {
		assert.NotEqual(t, "administrator-coder-token", rec.Value)
	}
	assert.Equal(t, before, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation])
}

// rotationStateWriteFailer fails rotation-state writes whose value contains
// marker, leaving every other write untouched.
type rotationStateWriteFailer struct {
	*forge.FakeClient
	marker string
	// failedWrites counts the writes this client failed.
	failedWrites int
}

func (c *rotationStateWriteFailer) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if name == forge.VarGitLabRoleRotation && strings.Contains(value, c.marker) {
		c.failedWrites++
		return errors.New("offline")
	}
	return c.FakeClient.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

func (c *rotationStateWriteFailer) AcquireProjectLease(ctx context.Context, owner, repo, name, holder string) (bool, error) {
	return c.FakeClient.AcquireProjectLease(ctx, owner, repo, name, holder)
}

func (c *rotationStateWriteFailer) ReleaseProjectLease(ctx context.Context, owner, repo, name, holder string) error {
	return c.FakeClient.ReleaseProjectLease(ctx, owner, repo, name, holder)
}

func suppliedReplacementTokens(identity func(context.Context, string) (SuppliedIdentity, error)) ServiceAccountTokenClient {
	sa := newFakeSAAPI()
	return ServiceAccountTokenClient{
		SA:                         sa,
		Legacy:                     &fakeTokens{},
		ManagedAccountIDs:          sa.ownedIDs,
		SuppliedCredentialIdentity: identity,
		VerifySuppliedAccess:       allowSuppliedAccess,
		VerifyToken:                acceptReplacementToken,
	}
}

// A provided replacement is never published before its supplied transition and
// owner exclusions are durably recorded, for managed-to-supplied and
// supplied-A-to-supplied-B rotation alike. A failed publication keeps the
// exclusions and restores the previous provenance, so the rotation is retryable.
func TestRotateGitLabRoleCredentials_ProvidedReplacementRecordsStateBeforePublishing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "replacement-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return describedCoderIdentity(777, 13), nil
	}
	rotate := func(client forge.Client, tokens ServiceAccountTokenClient) (RoleRotateResult, error) {
		return RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "group", Repo: "project", Client: client, Tokens: tokens,
			Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		})
	}
	published := func(fc *forge.FakeClient) bool {
		for _, rec := range fc.CreatedSecrets {
			if rec.Value == provided {
				return true
			}
		}
		return false
	}

	for name, seed := range map[string]string{
		"managed to supplied": `{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9],"managed_user_id":55}}}`,
		"supplied A to supplied B": `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,` +
			`"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600]}}}`,
	} {
		t.Run(name+": a state write failure refuses to publish", func(t *testing.T) {
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
			client := &rotationStateWriteFailer{FakeClient: fc, marker: `"supplied_user_id":777`}

			result, err := rotate(client, suppliedReplacementTokens(identity))
			require.NoError(t, err)
			require.Len(t, result.Failed, 1)
			assert.Contains(t, result.Failed[0].Reason, "credential not stored")
			assert.Empty(t, result.Rotated)
			assert.False(t, published(fc), "the supplied credential is not published")
			assert.NotContains(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation], "777")
		})

		t.Run(name+": a publication failure keeps the exclusions and previous provenance", func(t *testing.T) {
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
			client := &selectiveSecretClient{Client: fc, fail: map[string]error{forge.SecretGitLabCoderToken: errors.New("offline")}}
			before := readRoleState(t, fc, gitlabroles.RoleCoder)

			result, err := rotate(client, suppliedReplacementTokens(identity))
			require.NoError(t, err)
			require.Len(t, result.Failed, 1)
			assert.Empty(t, result.Rotated)

			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.Contains(t, rs.ExcludedUserIDs, 777, "the replacement owner stays excluded")
			assert.Equal(t, before.SuppliedUserID, rs.SuppliedUserID, "the installed credential's provenance is unchanged")
			assert.Equal(t, before.Supplied, rs.Supplied)
			assert.Equal(t, before.IncomingID, rs.IncomingID)
		})
	}
}

// A supplied enrollment whose credential identity cannot be resolved is refused
// without recording an ownerless supplied owner, so an ordinary retry with a
// working credential is not refused by the fail-closed attribution of an
// unresolved supplied owner.
func TestProvisionGitLabRoleCredentials_UnresolvedIdentityPublishFailureIsRetryable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "administrator-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fc := provisionClient(t)
	// Live ownership callbacks: the supplied owner is attributed from state or
	// by authenticating, and an unresolved supplied owner fails closed.
	sa := newFakeSAAPI()
	identityOK := false
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
		VerifyToken:       acceptReplacementToken,
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
			if !identityOK {
				return SuppliedIdentity{}, errors.New("unauthorized")
			}
			return describedCoderIdentity(777, 13), nil
		},
		VerifySuppliedAccess: allowSuppliedAccess,
		SuppliedAccountIDs:   func(context.Context, string, string) ([]int, error) { return nil, nil },
	}
	cfg := func(client forge.Client) RoleProvisionConfig {
		return RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: client, Tokens: tokens,
			Registry: gitlabroles.BuiltinRegistry(), Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		}
	}

	// An identity that cannot be resolved is rejected before anything is
	// published or recorded, so no ownerless supplied state is left behind.
	writesBefore := coderSecretWrites(fc)
	var first RoleProvisionResult
	provisionOwnRoles(ctx, cfg(fc), gitlabroles.BuiltinRegistry(), map[string]bool{}, &first)
	require.NotEmpty(t, first.Failed)
	assert.NotContains(t, first.Enrolled, gitlabroles.RoleCoder)
	assert.Equal(t, writesBefore, coderSecretWrites(fc), "the unresolved credential is not published")
	assert.False(t, readRoleState(t, fc, gitlabroles.RoleCoder).Supplied, "no ownerless supplied enrollment is recorded")

	identityOK = true
	var second RoleProvisionResult
	provisionOwnRoles(ctx, cfg(fc), gitlabroles.BuiltinRegistry(), map[string]bool{}, &second)
	assert.Contains(t, second.Enrolled, gitlabroles.RoleCoder)
	for _, f := range second.Failed {
		assert.NotEqual(t, gitlabroles.RoleCoder, f.Role, "the retry is not refused")
	}
	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.True(t, rs.Supplied)
	assert.Equal(t, 777, rs.SuppliedUserID)
}

// commitThenErrorSecretClient models a secret write that GitLab committed but
// whose response was lost: the value is stored and the caller still sees an
// error. readBackErr makes the existence read-back fail as well.
type commitThenErrorSecretClient struct {
	forge.Client
	readBackErr error
}

func (c *commitThenErrorSecretClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if err := c.Client.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
		return err
	}
	return errors.New("connection reset after commit")
}

func (c *commitThenErrorSecretClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	if c.readBackErr != nil {
		return false, c.readBackErr
	}
	return c.Client.RepoSecretExists(ctx, owner, repo, name)
}

func (c *commitThenErrorSecretClient) AcquireProjectLease(ctx context.Context, owner, repo, name, holder string) (bool, error) {
	return c.Client.(forge.ProjectLeaser).AcquireProjectLease(ctx, owner, repo, name, holder)
}

func (c *commitThenErrorSecretClient) ReleaseProjectLease(ctx context.Context, owner, repo, name, holder string) error {
	return c.Client.(forge.ProjectLeaser).ReleaseProjectLease(ctx, owner, repo, name, holder)
}

// A token client that cannot authenticate supplied credentials or verify their
// owner's project access refuses enrollment outright: nothing is published and
// no supplied provenance, ownerless or otherwise, is recorded.
func TestProvisionGitLabRoleCredentials_UnverifiableTokenClientRefusesEnrollment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "administrator-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return describedCoderIdentity(777, 13), nil
	}
	sa := newFakeSAAPI()
	for name, tokens := range map[string]ProjectAccessTokenClient{
		"a generic client":               &fakeTokens{},
		"no credential authentication":   ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs, VerifySuppliedAccess: allowSuppliedAccess, VerifyToken: acceptReplacementToken},
		"no project access verification": ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs, SuppliedCredentialIdentity: identity, VerifyToken: acceptReplacementToken},
		"a pointer without verification": &ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs, SuppliedCredentialIdentity: identity, VerifyToken: acceptReplacementToken},
	} {
		t.Run(name, func(t *testing.T) {
			fc := provisionClient(t)
			writesBefore := coderSecretWrites(fc)

			result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			})
			require.NoError(t, err)
			assert.NotContains(t, result.Enrolled, gitlabroles.RoleCoder)
			var reason string
			for _, f := range result.Failed {
				if f.Role == gitlabroles.RoleCoder {
					reason = f.Reason
				}
			}
			assert.Contains(t, reason, "administrator-provided credential rejected")
			assert.Contains(t, reason, ErrSuppliedVerificationUnavailable.Error())
			assert.NotContains(t, reason, provided)
			assert.Equal(t, writesBefore, coderSecretWrites(fc), "the unverified credential is not published")
			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.False(t, rs.Supplied, "no supplied provenance is recorded")
			assert.Zero(t, rs.SuppliedUserID)
		})
	}
}

// A supplied enrollment whose secret write commits and then reports an error
// keeps its supplied intent, recorded with the verified owner: the installed
// credential is never left described as managed.
func TestProvisionGitLabRoleCredentials_AmbiguousPublicationKeepsSuppliedIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "administrator-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return describedCoderIdentity(777, 13), nil
	}
	for name, readBackErr := range map[string]error{
		"read-back shows the secret": nil,
		"read-back fails":            errors.New("lookup failed"),
	} {
		t.Run(name, func(t *testing.T) {
			fc := provisionClient(t)
			client := &commitThenErrorSecretClient{Client: fc, readBackErr: readBackErr}
			var result RoleProvisionResult
			provisionOwnRoles(ctx, RoleProvisionConfig{
				Owner: "group", Repo: "project", Client: client, Tokens: suppliedReplacementTokens(identity),
				Registry: gitlabroles.BuiltinRegistry(), Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			}, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)

			require.NotEmpty(t, result.Failed)
			assert.NotContains(t, result.Enrolled, gitlabroles.RoleCoder)
			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.True(t, rs.Supplied, "the supplied intent is retained, not rolled back")
			assert.Equal(t, 777, rs.SuppliedUserID, "the verified owner is recorded")
			assert.Contains(t, rs.ExcludedUserIDs, 777)
			assert.True(t, provenanceOf(rs).Supplied, "the committed credential is never classified as managed")
		})
	}
}

// A provided replacement is refused outright by a token client that cannot
// authenticate supplied credentials or verify their owner's project access:
// nothing is published, the installed credential is left in place and no
// supplied provenance, ownerless or otherwise, is recorded.
func TestRotateGitLabRoleCredentials_UnverifiableTokenClientRefusesProvidedReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "replacement-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return describedCoderIdentity(777, 13), nil
	}
	sa := newFakeSAAPI()
	for name, tokens := range map[string]ProjectAccessTokenClient{
		"a generic client":               &fakeTokens{},
		"no credential authentication":   ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs, VerifySuppliedAccess: allowSuppliedAccess, VerifyToken: acceptReplacementToken},
		"no project access verification": ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs, SuppliedCredentialIdentity: identity, VerifyToken: acceptReplacementToken},
		"a pointer without verification": &ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs, SuppliedCredentialIdentity: identity, VerifyToken: acceptReplacementToken},
	} {
		t.Run(name, func(t *testing.T) {
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			seed := `{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9],"managed_user_id":55}}}`
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
			before := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
			secretWrites := len(fc.CreatedSecrets)

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			})
			require.NoError(t, err)
			assert.Empty(t, result.Rotated)
			require.Len(t, result.Failed, 1)
			assert.Contains(t, result.Failed[0].Reason, ErrSuppliedVerificationUnavailable.Error())
			assert.Contains(t, result.Failed[0].Reason, "previous credential left in place")
			assert.NotContains(t, result.Failed[0].Reason, provided)
			assert.Len(t, fc.CreatedSecrets, secretWrites, "the unverified credential is not published")
			assert.Equal(t, before, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation], "state is untouched")
		})
	}
}

// An enrollment recorded from owner attribution carries the owner but no token
// ID. An explicit, different supplied replacement is processed rather than
// dropped as an idempotent match, and a requested credential whose token
// identity cannot be established is reported as a per-role failure.
func TestRotateGitLabRoleCredentials_SuppliedReplacementOverEnrollmentWithoutTokenID(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	const provided = "replacement-coder-token"
	seed := `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-30T00:00:00Z","supplied":true,` +
		`"supplied_user_id":777,"excluded_user_ids":[777]}}}`
	rotate := func(t *testing.T, identity SuppliedIdentity) (*forge.FakeClient, RoleRotateResult) {
		t.Helper()
		ctx := context.Background()
		fc := seededRoleClient(t, gitlabroles.RoleCoder)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc,
			Tokens:   suppliedReplacementTokens(func(context.Context, string) (SuppliedIdentity, error) { return identity, nil }),
			Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		})
		require.NoError(t, err)
		return fc, result
	}

	t.Run("a different credential is enrolled", func(t *testing.T) {
		t.Parallel()
		fc, result := rotate(t, describedCoderIdentity(888, 14))
		assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
		assert.Empty(t, result.Failed)
		rs := readRoleState(t, fc, gitlabroles.RoleCoder)
		assert.Equal(t, 888, rs.SuppliedUserID)
		assert.Equal(t, 14, rs.SuppliedTokenID)
	})

	t.Run("a credential without a resolvable token identity is a role failure", func(t *testing.T) {
		t.Parallel()
		fc, result := rotate(t, SuppliedIdentity{UserID: 888})
		assert.Empty(t, result.Rotated)
		assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder, "the request is not read as an idempotent match")
		require.Len(t, result.Failed, 1)
		assert.Contains(t, result.Failed[0].Reason, "previous credential left in place")
		assert.Equal(t, 777, readRoleState(t, fc, gitlabroles.RoleCoder).SuppliedUserID)
	})
}

// An installed supplied credential recorded without its owner after an
// ambiguous publication (state written by an earlier release, since enrollment
// now verifies the owner first) keeps rotation failing closed instead of
// treating the installed credential as managed, and nothing is revoked.
func TestRotateGitLabRoleCredentials_AmbiguousProvidedPublicationKeepsSuppliedIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "replacement-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	var identity func(context.Context, string) (SuppliedIdentity, error)
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	seed := `{"version":2,"roles":{"coder":{"phase":"failed","error":"` + providedPublicationFailedError + ` (replacement owner unresolved)",` +
		`"incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9],"managed_user_id":55,"supplied":true}}}`
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
	tokens := suppliedReplacementTokens(identity)
	cfg := RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	}

	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.True(t, rs.Supplied, "the supplied intent is recorded")
	assert.Zero(t, rs.SuppliedUserID)
	assert.True(t, provenanceOf(rs).Supplied)

	// A later managed rotation cannot attribute the installed credential, so it
	// fails closed without creating or revoking anything.
	cfg.ProvidedTokens = nil
	cfg.Client = fc
	later, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Empty(t, later.Rotated)
	assert.Empty(t, later.Cleaned)
	assert.NotEmpty(t, later.Failed)

	// Supplying a replacement through --gitlab-role-token again does not
	// recover the state: the project-wide exclusions are resolved first, so it
	// fails closed with the administrator recovery diagnostic and publishes
	// nothing.
	cfg.ProvidedTokens = map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided}
	retry, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Empty(t, retry.Rotated)
	require.Len(t, retry.Failed, 1)
	assert.Contains(t, retry.Failed[0].Reason, "set a working credential in the role's CI/CD variable by hand")
	assert.Equal(t, rs, readRoleState(t, fc, gitlabroles.RoleCoder), "state is unchanged by the refused retry")

	// The documented recovery: once the installed credential authenticates
	// again, rotation attributes its owner and records it durably.
	cfg.ProvidedTokens = nil
	tokens.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return []int{777}, nil }
	tokens.AttributeSuppliedOwners = func(context.Context, string, string) (map[gitlabroles.Role]int, error) {
		return map[gitlabroles.Role]int{gitlabroles.RoleCoder: 777}, nil
	}
	cfg.Tokens = tokens
	cfg.Force = false
	secretWrites := len(fc.CreatedSecrets)
	recoveredRun, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	recovered := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.Contains(t, recovered.ExcludedUserIDs, 777, "the attributed owner is durably excluded once the credential authenticates")
	assert.False(t, unresolvedSuppliedOwner(rotationStateFile{Roles: map[string]rotationRoleState{"coder": recovered}}),
		"the role no longer records a supplied credential without an owner")

	// Attribution proves ownership, not that publication completed, so the
	// ambiguous-publication hold stays: no managed token replaces the possibly
	// installed supplied credential until the administrator re-enrolls one or
	// forces a rotation.
	assert.True(t, providedPublicationUnresolved(recovered), "the ambiguous publication hold survives owner attribution")
	assert.Empty(t, recoveredRun.Rotated)
	assert.Empty(t, recoveredRun.Cleaned)
	require.Len(t, recoveredRun.Failed, 1)
	assert.Contains(t, recoveredRun.Failed[0].Reason, "outcome is unknown")
	assert.Len(t, fc.CreatedSecrets, secretWrites, "no secret was overwritten")
	assert.Empty(t, tokens.Legacy.(*fakeTokens).created, "no managed token was created")
}

// A provided replacement whose secret write commits and then reports an error,
// with the replacement owner resolved, may be the installed credential. An
// ordinary unforced rotation must not overwrite it with a managed token, for
// managed-to-supplied and supplied-to-supplied enrollment alike, and nothing is
// revoked. Re-enrolling a credential recovers the role.
func TestRotateGitLabRoleCredentials_AmbiguousResolvedProvidedPublicationHoldsManagedRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "replacement-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return describedCoderIdentity(777, 13), nil
	}
	for name, seed := range map[string]string{
		"managed to supplied": `{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9],"managed_user_id":55}}}`,
		"supplied to supplied": `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,` +
			`"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
			cfg := RoleRotateConfig{
				Owner: "group", Repo: "project", Client: &commitThenErrorSecretClient{Client: fc},
				Tokens:   suppliedReplacementTokens(identity),
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			}
			first, err := RotateGitLabRoleCredentials(ctx, cfg)
			require.NoError(t, err)
			require.Len(t, first.Failed, 1)
			secretWrites := len(fc.CreatedSecrets)

			// An ordinary run does not replace the possibly installed credential.
			cfg.Client, cfg.ProvidedTokens, cfg.Force = fc, nil, false
			later, err := RotateGitLabRoleCredentials(ctx, cfg)
			require.NoError(t, err)
			assert.Empty(t, later.Rotated)
			assert.Empty(t, later.Cleaned)
			require.Len(t, later.Failed, 1)
			assert.Contains(t, later.Failed[0].Reason, "outcome is unknown")
			assert.Len(t, fc.CreatedSecrets, secretWrites, "no managed token overwrote the installed credential")

			// Re-enrolling a credential resolves the held state.
			cfg.ProvidedTokens = map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided}
			retry, err := RotateGitLabRoleCredentials(ctx, cfg)
			require.NoError(t, err)
			assert.Empty(t, retry.Failed)
			assert.Contains(t, retry.Rotated, gitlabroles.RoleCoder)
			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.Equal(t, rotationPhaseIdle, rs.Phase)
			assert.Equal(t, 777, rs.SuppliedUserID)
			assert.False(t, providedPublicationUnresolved(rs))
		})
	}
}

// A requested supplied credential whose identity cannot be resolved before
// processing is not protected by its token ID, so grace cleanup is deferred for
// the run instead of revoking what may be that credential.
func TestRotateGitLabRoleCredentials_UnidentifiedSuppliedTokenDefersOutgoingCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"version":2,"roles":{"coder":{"phase":"overlapping","created_token_ids":[13,14],"incoming_id":14,"outgoing_ids":[13],"distributed_at":"2026-09-01T00:00:00Z"}}}`, true))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-01-01", UserID: 40})
	legacy.seed(ProjectAccessToken{ID: 14, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-01-01", UserID: 40})
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            legacy,
		ManagedAccountIDs: sa.ownedIDs,
		ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
		},
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
			return SuppliedIdentity{}, errors.New("transient lookup failure")
		},
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true,
		Now:            time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "enrolled-coder-token"},
	})
	require.NoError(t, err)
	assert.NotContains(t, legacy.revoked, 13, "an overdue outgoing token is kept while a requested credential is unidentified")
	assert.Empty(t, result.Cleaned)
	assert.Contains(t, result.Diagnostics, "coder: outgoing tokens were kept and not revoked because a requested supplied credential could not be identified")
}

// A requested supplied credential that differs from the enrolled, healthy and
// recently distributed supplied credential replaces it without a forced
// rotation; re-supplying the enrolled credential, or a credential whose
// identity cannot be told apart from it, retains the installed one.
func TestRotateGitLabRoleCredentials_UnforcedSuppliedReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "replacement-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	const seed = `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,` +
		`"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600]}}}`

	for _, tc := range []struct {
		name      string
		identity  func(context.Context, string) (SuppliedIdentity, error)
		published bool
		failed    bool
		tokenID   int
	}{
		{
			name: "a different supplied credential replaces the enrolled one",
			identity: func(context.Context, string) (SuppliedIdentity, error) {
				return describedCoderIdentity(777, 13), nil
			},
			published: true,
			tokenID:   13,
		},
		{
			name: "the enrolled supplied credential is retained",
			identity: func(context.Context, string) (SuppliedIdentity, error) {
				return describedCoderIdentity(600, 12), nil
			},
			tokenID: 12,
		},
		{
			name: "an unidentifiable supplied credential is retained and reported as refused",
			identity: func(context.Context, string) (SuppliedIdentity, error) {
				return SuppliedIdentity{}, errors.New("unauthorized")
			},
			failed:  true,
			tokenID: 12,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
			// The enrolled supplied credential is listed, active and not due.
			legacy := &fakeTokens{}
			legacy.seed(ProjectAccessToken{
				ID: 12, UserID: 600, Active: true, ExpiresAt: GitLabPATExpiresAt(now),
				Name: gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName,
			})
			sa := newFakeSAAPI()
			tokens := ServiceAccountTokenClient{
				SA: sa, Legacy: legacy, ManagedAccountIDs: sa.ownedIDs, SuppliedCredentialIdentity: tc.identity,
				VerifySuppliedAccess: allowSuppliedAccess,
				SuppliedAccountIDs:   func(context.Context, string, string) ([]int, error) { return []int{600}, nil },
			}

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			})
			require.NoError(t, err)

			if tc.failed {
				require.Len(t, result.Failed, 1)
				assert.Equal(t, gitlabroles.RoleCoder, result.Failed[0].Role)
				assert.Contains(t, result.Failed[0].Reason, "could not be identified")
			} else {
				assert.Empty(t, result.Failed)
			}

			published := false
			for _, rec := range fc.CreatedSecrets {
				if rec.Value == provided {
					published = true
				}
			}
			assert.Equal(t, tc.published, published)
			assert.Equal(t, tc.tokenID, readRoleState(t, fc, gitlabroles.RoleCoder).SuppliedTokenID)
		})
	}
}

// A supplied Poller is verified for protected-default-branch pipeline access
// before publication; other roles are not.
func TestVerifiedSuppliedIdentity_PollerPipelineAccess(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	poller := SuppliedIdentity{
		UserID: 888, TokenID: 14, Described: true, Name: gitlabroles.PollerTokenName,
		Scopes: []string{"api"}, ExpiresAt: "2027-06-01", Active: true,
	}
	build := func(id SuppliedIdentity, pollerAccess func(context.Context, string, string, int) error) ServiceAccountTokenClient {
		return ServiceAccountTokenClient{
			SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) { return id, nil },
			VerifySuppliedAccess:       allowSuppliedAccess,
			VerifySuppliedPollerAccess: pollerAccess,
		}
	}

	t.Run("poller without access is rejected", func(t *testing.T) {
		t.Parallel()
		var gotUser int
		tokens := build(poller, func(_ context.Context, _, _ string, userID int) error {
			gotUser = userID
			return errors.New("raw server detail")
		})
		_, err := verifiedSuppliedIdentity(context.Background(), tokens, "group", "project", "administrator-poller-token", gitlabroles.PollerTokenName, now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "protected-branch pipeline access")
		assert.NotContains(t, err.Error(), "raw server detail")
		assert.Equal(t, 888, gotUser, "access is checked for the authenticated owner")
	})

	t.Run("poller with access is accepted", func(t *testing.T) {
		t.Parallel()
		tokens := build(poller, func(context.Context, string, string, int) error { return nil })
		id, err := verifiedSuppliedIdentity(context.Background(), tokens, "group", "project", "administrator-poller-token", gitlabroles.PollerTokenName, now)
		require.NoError(t, err)
		assert.Equal(t, 888, id.UserID)
	})

	t.Run("other roles skip the poller check", func(t *testing.T) {
		t.Parallel()
		called := false
		tokens := build(describedCoderIdentity(777, 13), func(context.Context, string, string, int) error {
			called = true
			return errors.New("must not be called")
		})
		_, err := verifiedSuppliedIdentity(context.Background(), tokens, "group", "project", "administrator-coder-token", gitlabroles.CoderTokenName, now)
		require.NoError(t, err)
		assert.False(t, called)
	})
}
