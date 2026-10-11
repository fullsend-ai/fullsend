package repos

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const suppliedIdentityEnrollment = `{"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z"}}}`

func readRoleState(t *testing.T, fc *forge.FakeClient, role gitlabroles.Role) rotationRoleState {
	t.Helper()
	var state rotationStateFile
	require.NoError(t, json.Unmarshal([]byte(fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]), &state))
	return state.Roles[string(role)]
}

func TestRecordSuppliedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("records owner, token and permanent exclusion on an enrolled entry", func(t *testing.T) {
		fc := provisionClient(t)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedIdentityEnrollment, true))

		require.NoError(t, RecordSuppliedIdentity(ctx, fc, "group", "project", gitlabroles.RoleCoder, SuppliedIdentity{UserID: 777, TokenID: 13}))

		rs := readRoleState(t, fc, gitlabroles.RoleCoder)
		assert.True(t, rs.Supplied)
		assert.Equal(t, 777, rs.SuppliedUserID)
		assert.Equal(t, 13, rs.SuppliedTokenID)
		assert.Equal(t, []int{777}, rs.ExcludedUserIDs)
		assert.Equal(t, "2026-10-01T00:00:00Z", rs.DistributedAt, "distribution proof is preserved")
	})

	t.Run("an entry with a recorded owner is left alone", func(t *testing.T) {
		fc := provisionClient(t)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
			`{"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":600,"excluded_user_ids":[600]}}}`, true))

		require.NoError(t, RecordSuppliedIdentity(ctx, fc, "group", "project", gitlabroles.RoleCoder, SuppliedIdentity{UserID: 777, TokenID: 13}))

		rs := readRoleState(t, fc, gitlabroles.RoleCoder)
		assert.Equal(t, 600, rs.SuppliedUserID)
		assert.Equal(t, []int{600}, rs.ExcludedUserIDs)
	})

	t.Run("a managed entry is never reclassified as supplied", func(t *testing.T) {
		fc := provisionClient(t)
		managed := `{"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-10-01T00:00:00Z","created_token_ids":[9],"managed_user_id":41}}}`
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, managed, true))

		require.NoError(t, RecordSuppliedIdentity(ctx, fc, "group", "project", gitlabroles.RoleCoder, SuppliedIdentity{UserID: 777, TokenID: 13}))

		rs := readRoleState(t, fc, gitlabroles.RoleCoder)
		assert.False(t, rs.Supplied)
		assert.Zero(t, rs.SuppliedUserID)
		assert.Empty(t, rs.ExcludedUserIDs)
	})

	t.Run("an unresolved owner records nothing", func(t *testing.T) {
		fc := provisionClient(t)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedIdentityEnrollment, true))

		require.NoError(t, RecordSuppliedIdentity(ctx, fc, "group", "project", gitlabroles.RoleCoder, SuppliedIdentity{}))
		require.NoError(t, RecordSuppliedIdentity(ctx, fc, "group", "project", gitlabroles.RoleAnalyst, SuppliedIdentity{UserID: 5}))

		assert.Equal(t, suppliedIdentityEnrollment, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation])
	})
}

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
		}
	}

	t.Run("owner, token and exclusion are persisted", func(t *testing.T) {
		fc := provisionClient(t)
		var seen []string
		tokens := newClient(func(_ context.Context, token string) (SuppliedIdentity, error) {
			seen = append(seen, token)
			return SuppliedIdentity{UserID: 777, TokenID: 13}, nil
		})

		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Registry: reg, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		})
		require.NoError(t, err)
		assert.Contains(t, result.Enrolled, gitlabroles.RoleCoder)
		assert.Equal(t, []string{provided}, seen, "the identity is resolved from the enrolled value")

		rs := readRoleState(t, fc, gitlabroles.RoleCoder)
		assert.True(t, rs.Supplied)
		assert.Equal(t, 777, rs.SuppliedUserID)
		assert.Equal(t, 13, rs.SuppliedTokenID)
		assert.Equal(t, []int{777}, rs.ExcludedUserIDs)
		assert.NotContains(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation], provided)
	})

	t.Run("an unresolvable credential keeps the distribution proof and reports a diagnostic", func(t *testing.T) {
		fc := provisionClient(t)
		tokens := newClient(func(context.Context, string) (SuppliedIdentity, error) {
			return SuppliedIdentity{}, errors.New("unauthorized")
		})

		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Registry: reg, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		})
		require.NoError(t, err)
		assert.Contains(t, result.Enrolled, gitlabroles.RoleCoder)
		assert.Contains(t, result.Diagnostics, "coder: recording the enrolled credential's owner failed"+ownerlessSuppliedRecoveryHint)

		rs := readRoleState(t, fc, gitlabroles.RoleCoder)
		assert.Equal(t, rotationPhaseIdle, rs.Phase)
		assert.Zero(t, rs.SuppliedUserID)
	})
}

// Replacing a supplied credential with another supplied credential attributes
// the replacement to its own owner and keeps the replaced owner excluded.
func TestRotateGitLabRoleCredentials_ProvidedReplacementRecordsOwnerAndKeepsPreviousExcluded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600]}}}`, true))
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
			return SuppliedIdentity{UserID: 777, TokenID: 13}, nil
		},
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

// An owner that was enrolled without being recorded is attributed and
// persisted by rotation before any replacement, so the exclusion survives the
// replacement and a managed rotation never drops it.
func TestRotateGitLabRoleCredentials_PersistsUnrecordedSuppliedOwnerBeforeReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z"}}}`, true))
	sa := newFakeSAAPI()
	attributions := 0
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
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
			SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
				return SuppliedIdentity{UserID: 777, TokenID: 13}, nil
			},
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
		_, rs := enroll(t, `{"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":600,"supplied_token_id":12,"excluded_user_ids":[600]}}}`)
		assert.True(t, rs.Supplied)
		assert.Equal(t, 777, rs.SuppliedUserID)
		assert.Equal(t, 13, rs.SuppliedTokenID)
		assert.Equal(t, []int{600, 777}, rs.ExcludedUserIDs)
		assert.Equal(t, now.Format(time.RFC3339), rs.DistributedAt)
	})

	t.Run("managed to supplied", func(t *testing.T) {
		_, rs := enroll(t, `{"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9]}}}`)
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
		`{"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777]}}}`, true))
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
	}

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
				`{"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777],"created_token_ids":[9],"managed_user_id":600}}}`, true))
			historical.Name = gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName
			legacy := &fakeTokens{}
			legacy.seed(historical)

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: legacy,
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
		`{"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777],"created_token_ids":[9],"managed_user_id":600}}}`, true))
	tokenName := gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 9, Name: tokenName, Active: true, ExpiresAt: "2027-09-30", UserID: 600})
	legacy.seed(ProjectAccessToken{ID: 13, Name: tokenName, Active: false, Revoked: true, ExpiresAt: "2026-10-03", UserID: 777})

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: legacy,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
		Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Empty(t, result.Failed)
	assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder, "a revoked supplied credential is not reported healthy")
	assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
}

func TestRotateGitLabRoleCredentials_UnrecordableSuppliedOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z"}}}`, true))
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA:                sa,
		Legacy:            &fakeTokens{},
		ManagedAccountIDs: sa.ownedIDs,
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
	fc.Errors["UpdateCIVariable"] = errors.New("offline")
	before := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]

	// Drive the role loop directly: the registry write that precedes it would
	// otherwise hit the injected failure first.
	var result RoleProvisionResult
	provisionOwnRoles(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "administrator-coder-token"},
	}, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)

	var failed bool
	for _, f := range result.Failed {
		if f.Role == gitlabroles.RoleCoder {
			failed = true
			assert.Contains(t, f.Reason, "credential not stored")
		}
	}
	assert.True(t, failed)
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
}

func (c *rotationStateWriteFailer) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if name == forge.VarGitLabRoleRotation && strings.Contains(value, c.marker) {
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
		return SuppliedIdentity{UserID: 777, TokenID: 13}, nil
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

// An ownerless supplied enrollment whose publication fails is rolled back, so
// an ordinary retry with a working credential is not refused by the fail-closed
// attribution of an unresolved supplied owner.
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
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
			if !identityOK {
				return SuppliedIdentity{}, errors.New("unauthorized")
			}
			return SuppliedIdentity{UserID: 777, TokenID: 13}, nil
		},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return nil, nil },
	}
	cfg := func(client forge.Client) RoleProvisionConfig {
		return RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: client, Tokens: tokens,
			Registry: gitlabroles.BuiltinRegistry(), Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
		}
	}

	failing := &selectiveSecretClient{Client: fc, fail: map[string]error{forge.SecretGitLabCoderToken: errors.New("offline")}}
	var first RoleProvisionResult
	provisionOwnRoles(ctx, cfg(failing), gitlabroles.BuiltinRegistry(), map[string]bool{}, &first)
	require.NotEmpty(t, first.Failed)
	assert.NotContains(t, first.Enrolled, gitlabroles.RoleCoder)
	assert.False(t, readRoleState(t, fc, gitlabroles.RoleCoder).Supplied, "the unpublished ownerless enrollment is rolled back")

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

// A supplied enrollment whose secret write commits and then reports an error
// keeps its ownerless supplied intent when the replacement owner could not be
// resolved: the installed credential is never left described as managed.
func TestProvisionGitLabRoleCredentials_AmbiguousPublicationKeepsSuppliedIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "administrator-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return SuppliedIdentity{}, errors.New("unauthorized")
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
			assert.Zero(t, rs.SuppliedUserID)
			assert.True(t, provenanceOf(rs).Supplied, "the committed credential is never classified as managed")
		})
	}
}

// A provided replacement whose secret write commits and then reports an error,
// with the replacement owner unresolved, keeps the ownerless supplied
// transition, so a later rotation fails closed instead of treating the
// installed credential as managed, and nothing is revoked.
func TestRotateGitLabRoleCredentials_AmbiguousProvidedPublicationKeepsSuppliedIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const provided = "replacement-coder-token"
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return SuppliedIdentity{}, errors.New("unauthorized")
	}
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	seed := `{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9],"managed_user_id":55}}}`
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
	tokens := suppliedReplacementTokens(identity)
	cfg := RoleRotateConfig{
		Owner: "group", Repo: "project", Client: &commitThenErrorSecretClient{Client: fc}, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
	}

	result, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Empty(t, result.Rotated)
	rs := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.True(t, rs.Supplied, "the supplied intent is retained after the ambiguous write")
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
	recoveredRun, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Empty(t, recoveredRun.Failed)
	recovered := readRoleState(t, fc, gitlabroles.RoleCoder)
	assert.Contains(t, recovered.ExcludedUserIDs, 777, "the attributed owner is durably excluded once the credential authenticates")
	assert.False(t, unresolvedSuppliedOwner(rotationStateFile{Roles: map[string]rotationRoleState{"coder": recovered}}),
		"the role no longer records a supplied credential without an owner")
}
