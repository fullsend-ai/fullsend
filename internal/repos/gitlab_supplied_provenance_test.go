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

func suppliedProvenanceClient(state string) *forge.FakeClient {
	fc := forge.NewFakeClient()
	if state != "" {
		fc.VariableValues = map[string]string{"g/p/" + forge.VarGitLabRoleRotation: state}
	}
	return fc
}

func readRoleStates(t *testing.T, fc *forge.FakeClient) map[string]rotationRoleState {
	t.Helper()
	state, _, err := loadRotationState(context.Background(), fc, "g", "p")
	require.NoError(t, err)
	return state.Roles
}

func TestRoleCredentialProvenances(t *testing.T) {
	ctx := context.Background()
	fc := suppliedProvenanceClient(`{"version":2,"roles":{` +
		`"poller":{"phase":"idle","incoming_id":5,"excluded_user_ids":[90]},` +
		`"analyst":{"supplied":true,"supplied_user_id":55,"supplied_token_id":12},` +
		`"coder":{}}}`)
	provs, err := RoleCredentialProvenances(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, RoleProvenance{Known: true, ExcludedUserIDs: []int{90}}, provs[gitlabroles.RolePoller])
	assert.Equal(t, RoleProvenance{Known: true, Supplied: true, UserID: 55, TokenID: 12, ExcludedUserIDs: []int{55}}, provs[gitlabroles.RoleAnalyst])
	assert.False(t, provs[gitlabroles.RoleCoder].Known, "an empty entry is unknown, never managed")

	poller, err := PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, provs[gitlabroles.RolePoller], poller)

	// A missing document is unknown provenance, not an error.
	poller, err = PollerCredentialProvenance(ctx, suppliedProvenanceClient(""), "g", "p")
	require.NoError(t, err)
	assert.False(t, poller.Known)

	for name, state := range map[string]string{
		"invalid JSON":              `{`,
		"negative supplied owner":   `{"version":2,"roles":{"analyst":{"supplied":true,"supplied_user_id":-1}}}`,
		"nonpositive excluded user": `{"version":2,"roles":{"poller":{"excluded_user_ids":[0]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RoleCredentialProvenances(ctx, suppliedProvenanceClient(state), "g", "p")
			require.Error(t, err)
			_, err = PollerCredentialProvenance(ctx, suppliedProvenanceClient(state), "g", "p")
			require.Error(t, err)
		})
	}
}

func TestRecordSuppliedExclusions(t *testing.T) {
	ctx := context.Background()
	fc := suppliedProvenanceClient(`{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":77,"excluded_user_ids":[90]}}}`)

	require.NoError(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RolePoller, []int{91, 0, 90}))
	roles := readRoleStates(t, fc)
	assert.Equal(t, []int{90, 91}, roles["poller"].ExcludedUserIDs)
	assert.Equal(t, 5, roles["poller"].IncomingID, "lifecycle fields are preserved")
	assert.Equal(t, 77, roles["poller"].ManagedUserID)
	require.Len(t, fc.UpdatedVariables, 1)

	// Already recorded exclusions do not rewrite the document.
	require.NoError(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RolePoller, []int{90, 91}))
	assert.Len(t, fc.UpdatedVariables, 1)

	// A role without an entry gets an exclusions-only entry.
	require.NoError(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RoleCoder, []int{55}))
	assert.Equal(t, []int{55}, readRoleStates(t, fc)["coder"].ExcludedUserIDs)

	fc = suppliedProvenanceClient(`{`)
	require.ErrorContains(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RolePoller, []int{1}), "recording supplied exclusions")
	assert.Empty(t, fc.UpdatedVariables)
}

func TestRecordSuppliedOwners(t *testing.T) {
	ctx := context.Background()
	const state = `{"version":2,"roles":{` +
		`"poller":{"supplied":true,"supplied_distributed":true,"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"},` +
		`"analyst":{"supplied":true,"supplied_user_id":55},` +
		`"coder":{"phase":"idle","incoming_id":8}}}`
	fc := suppliedProvenanceClient(state)

	// Only the supplied role without a recorded owner is updated; a recorded
	// owner, a managed role, an absent role, and a nonpositive ID are ignored.
	require.NoError(t, RecordSuppliedOwners(ctx, fc, "g", "p", map[gitlabroles.Role]int{
		gitlabroles.RolePoller:  90,
		gitlabroles.RoleAnalyst: 56,
		gitlabroles.RoleCoder:   57,
		"scanner":               58,
		"triage":                0,
	}))
	roles := readRoleStates(t, fc)
	assert.Equal(t, 90, roles["poller"].SuppliedUserID)
	assert.True(t, roles["poller"].Supplied)
	assert.Equal(t, []int{90}, roles["poller"].ExcludedUserIDs)
	assert.True(t, roles["poller"].SuppliedDistributed, "recording an owner keeps the enrollment's distribution proof")
	assert.Equal(t, "2026-01-01T00:00:00Z", roles["poller"].DistributedAt)
	assert.Equal(t, 55, roles["analyst"].SuppliedUserID)
	assert.Zero(t, roles["coder"].SuppliedUserID)
	assert.NotContains(t, roles, "scanner")
	require.Len(t, fc.UpdatedVariables, 1)

	// Nothing left to record does not rewrite the document.
	require.NoError(t, RecordSuppliedOwners(ctx, fc, "g", "p", map[gitlabroles.Role]int{gitlabroles.RolePoller: 91}))
	assert.Len(t, fc.UpdatedVariables, 1)
	require.NoError(t, RecordSuppliedOwners(ctx, fc, "g", "p", nil))
	assert.Len(t, fc.UpdatedVariables, 1)

	fc = suppliedProvenanceClient(`{`)
	require.ErrorContains(t, RecordSuppliedOwners(ctx, fc, "g", "p", map[gitlabroles.Role]int{gitlabroles.RolePoller: 90}), "recording supplied owners")
	assert.Empty(t, fc.UpdatedVariables)
}

// Rotation leaves an installed supplied credential untouched, and reports a role
// failure, when its owner's access is elevated, cannot be verified, or has no
// known owner, instead of retaining it as healthy.
func TestCheckInstalledSuppliedCredential_UnverifiableOwnerIsNeverHealthy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	healthy := SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}
	installed := func(id SuppliedIdentity) func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
		return func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
			return id, true, nil
		}
	}

	t.Run("owner with Developer access is healthy", func(t *testing.T) {
		t.Parallel()
		tokens := ServiceAccountTokenClient{InstalledRoleCredentialSelf: installed(healthy), VerifySuppliedAccess: allowSuppliedAccess}
		check, _, _ := checkInstalledSuppliedCredential(context.Background(), tokens, "group", "project", "secret", now)
		assert.Equal(t, installedCredentialHealthy, check)
	})

	t.Run("elevated owner is unverifiable", func(t *testing.T) {
		t.Parallel()
		tokens := ServiceAccountTokenClient{
			InstalledRoleCredentialSelf: installed(healthy),
			VerifySuppliedAccess: func(context.Context, string, string, SuppliedIdentity) error {
				return errors.New("owner does not have exactly effective Developer access")
			},
		}
		check, _, reason := checkInstalledSuppliedCredential(context.Background(), tokens, "group", "project", "secret", now)
		assert.Equal(t, installedCredentialUnverifiable, check)
		assert.Contains(t, reason, "effective project access")
	})

	t.Run("missing verifier is unverifiable", func(t *testing.T) {
		t.Parallel()
		tokens := ServiceAccountTokenClient{InstalledRoleCredentialSelf: installed(healthy)}
		check, _, reason := checkInstalledSuppliedCredential(context.Background(), tokens, "group", "project", "secret", now)
		assert.Equal(t, installedCredentialUnverifiable, check)
		assert.Contains(t, reason, "no access verifier")
	})

	for name, userID := range map[string]int{"zero owner ID": 0, "negative owner ID": -1} {
		t.Run(name+" is unverifiable", func(t *testing.T) {
			t.Parallel()
			id := healthy
			id.UserID = userID
			called := false
			tokens := ServiceAccountTokenClient{
				InstalledRoleCredentialSelf: installed(id),
				VerifySuppliedAccess: func(context.Context, string, string, SuppliedIdentity) error {
					called = true
					return nil
				},
			}
			check, _, reason := checkInstalledSuppliedCredential(context.Background(), tokens, "group", "project", "secret", now)
			assert.Equal(t, installedCredentialUnverifiable, check)
			assert.Contains(t, reason, "no owner user ID")
			assert.False(t, called)
		})
	}
}

// A supplied credential that the token inventory lists is not covered by the
// installed-secret check, but its owner's project access is still reverified:
// status reports drift and rotation refuses without touching the account or token.
func TestListedSuppliedCredential_ReverifiesOwnerAccess(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	listed := []ProjectAccessToken{{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-06-01", UserID: 777}}
	elevated := func(context.Context, string, string, SuppliedIdentity) error {
		return errors.New("the supplied credential's owner does not have exactly effective Developer access on the project")
	}

	t.Run("status", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fc := seededRoleClient(t, gitlabroles.RoleCoder)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
		tokens := ServiceAccountTokenClient{
			InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
				return SuppliedIdentity{}, true, errors.New("must not authenticate a listed credential")
			},
			VerifySuppliedAccess: elevated,
		}
		status := &RepoStatus{GitLabRolesReady: true}

		changed := tokens.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", listed, now, status)

		assert.True(t, changed)
		assert.False(t, status.GitLabRolesReady)
		require.Len(t, status.Drifts, 1)
		assert.Equal(t, "gitlab-role:coder", status.Drifts[0].Field)
		assert.Contains(t, strings.Join(status.GitLabRoleDiagnostics, "\n"), "effective project access")
	})

	t.Run("rotation", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fc := seededRoleClient(t, gitlabroles.RoleCoder)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
		legacy := &fakeTokens{}
		legacy.seed(listed[0])
		sa := newFakeSAAPI()
		tokens := ServiceAccountTokenClient{
			SA: sa, Legacy: legacy, ManagedAccountIDs: sa.ownedIDs,
			ManagedLegacyTokenIDs: func(context.Context, string, string) ([]int, error) { return []int{13}, nil },
			VerifySuppliedAccess:  elevated,
		}
		before := len(fc.CreatedSecrets)

		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
			Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
		})

		require.NoError(t, err)
		require.Len(t, result.Failed, 1)
		assert.Contains(t, result.Failed[0].Reason, "owner access")
		assert.Empty(t, result.Rotated)
		assert.Empty(t, legacy.created, "no token is created")
		assert.Empty(t, legacy.revoked, "the supplied token is not revoked")
		assert.Len(t, fc.CreatedSecrets, before, "no secret is replaced")
	})

	// Passing the already-enrolled credential again is not a new enrollment, so
	// the idempotent retention must still verify the owner's effective access.
	for name, verifier := range map[string]func(context.Context, string, string, SuppliedIdentity) error{
		"elevated owner": elevated,
		"lookup failure": func(context.Context, string, string, SuppliedIdentity) error {
			return errors.New("the supplied credential's owner membership could not be determined")
		},
	} {
		verifier := verifier
		t.Run("rotation re-supplying the enrolled credential with "+name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
			legacy := &fakeTokens{}
			legacy.seed(listed[0])
			sa := newFakeSAAPI()
			tokens := ServiceAccountTokenClient{
				SA: sa, Legacy: legacy, ManagedAccountIDs: sa.ownedIDs,
				ManagedLegacyTokenIDs: func(context.Context, string, string) ([]int, error) { return []int{13}, nil },
				SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
					return SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}, nil
				},
				VerifySuppliedAccess: verifier,
			}
			before := len(fc.CreatedSecrets)

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "enrolled-secret"},
			})

			require.NoError(t, err)
			require.Len(t, result.Failed, 1)
			assert.Contains(t, result.Failed[0].Reason, "owner access")
			assert.Empty(t, result.Skipped)
			assert.Empty(t, result.Rotated)
			assert.Empty(t, legacy.created, "no token is created")
			assert.Empty(t, legacy.revoked, "the supplied token is not revoked")
			assert.Len(t, fc.CreatedSecrets, before, "no secret is replaced")
		})
	}
}

// verifyListedSuppliedAccess fails closed: a missing verifier or an owner that
// cannot be determined is an error, never silently verified.
func TestVerifyListedSuppliedAccess_FailsClosedWithoutVerifiableOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	matches := []ProjectAccessToken{{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, UserID: 777}}
	rs := rotationRoleState{Supplied: true, SuppliedTokenID: 13, SuppliedUserID: 777}

	err := ServiceAccountTokenClient{}.verifyListedSuppliedAccess(ctx, "group", "project", rs, matches, time.Now())
	require.ErrorContains(t, err, "no access verifier")

	verified := ServiceAccountTokenClient{VerifySuppliedAccess: allowSuppliedAccess}
	require.NoError(t, verified.verifyListedSuppliedAccess(ctx, "group", "project", rs, matches, time.Now()))

	for name, id := range map[string]int{"zero": 0, "negative": -1} {
		ownerless := []ProjectAccessToken{{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, UserID: id}}
		noOwner := rotationRoleState{Supplied: true, SuppliedTokenID: 13, SuppliedUserID: id}
		err := verified.verifyListedSuppliedAccess(ctx, "group", "project", noOwner, ownerless, time.Now())
		require.ErrorContains(t, err, "no owner user ID", name)
	}
}

// A listed supplied credential that is revoked, inactive or expired is not
// verified for owner access: it is unusable whatever the owner's access, and
// rotation replaces it, so an elevated owner must not block that replacement.
func TestVerifyListedSuppliedAccess_SkipsUnusableCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	rs := rotationRoleState{Supplied: true, SuppliedTokenID: 13, SuppliedUserID: 777}
	elevated := ServiceAccountTokenClient{VerifySuppliedAccess: func(context.Context, string, string, SuppliedIdentity) error {
		return errors.New("owner access is elevated")
	}}
	for name, tok := range map[string]ProjectAccessToken{
		"revoked":  {ID: 13, Name: gitlabroles.CoderTokenName, Active: false, Revoked: true, UserID: 777},
		"inactive": {ID: 13, Name: gitlabroles.CoderTokenName, Active: false, UserID: 777},
		"expired":  {ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2026-10-01", UserID: 777},
	} {
		require.NoError(t, elevated.verifyListedSuppliedAccess(ctx, "group", "project", rs, []ProjectAccessToken{tok}, now), name)
	}
	usable := ProjectAccessToken{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2026-12-01", UserID: 777}
	require.ErrorContains(t, elevated.verifyListedSuppliedAccess(ctx, "group", "project", rs, []ProjectAccessToken{usable}, now), "elevated")
}

// Only a positively revoked, inactive or expired listed credential skips owner
// verification. A malformed or whitespace-padded expiry is not proof of
// unusability, so the owner's access is still verified; an empty expiry is a
// never-expiring token and is verified too.
func TestVerifyListedSuppliedAccess_VerifiesWhenExpiryIsNotPositivelyUnusable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	rs := rotationRoleState{Supplied: true, SuppliedTokenID: 13, SuppliedUserID: 777}
	elevated := ServiceAccountTokenClient{VerifySuppliedAccess: func(context.Context, string, string, SuppliedIdentity) error {
		return errors.New("owner access is elevated")
	}}
	for name, expiry := range map[string]string{
		"malformed":  "not-a-date",
		"whitespace": " 2026-12-01 ",
		"empty":      "",
		"timestamp":  "2026-12-01T00:00:00Z",
	} {
		tok := ProjectAccessToken{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: expiry, UserID: 777}
		require.ErrorContains(t, elevated.verifyListedSuppliedAccess(ctx, "group", "project", rs, []ProjectAccessToken{tok}, now), "elevated", name)
	}
	padded := ProjectAccessToken{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: " 2026-10-01 ", UserID: 777}
	require.NoError(t, elevated.verifyListedSuppliedAccess(ctx, "group", "project", rs, []ProjectAccessToken{padded}, now), "a padded date that is positively expired is skipped")
}
