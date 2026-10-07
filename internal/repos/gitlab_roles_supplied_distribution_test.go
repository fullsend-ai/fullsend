package repos

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// A healthy recorded supplied credential is retained by an unforced rotation
// even when the entry still carries a managed incoming ID and an overlapping or
// failed phase left by the managed rotation it was enrolled over: successful
// supplied distribution is tracked independently of that cleanup state.
func TestRotateGitLabRoleCredentials_UnforcedKeepsHealthySuppliedOverSurvivingManagedState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, phase := range []string{rotationPhaseOverlapping, rotationPhaseFailed} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RolePoller)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
				fmt.Sprintf(`{"roles":{"poller":{"phase":%q,"incoming_id":8,"distributed_at":"2026-09-20T00:00:00Z","supplied":true,"supplied_distributed":true,"supplied_user_id":70,"supplied_token_id":50,"excluded_user_ids":[70]}}}`, phase), true))
			legacy := &fakeTokens{}
			legacy.seed(ProjectAccessToken{ID: 50, Name: gitlabroles.PollerTokenName, Active: true, UserID: 70, ExpiresAt: "2027-09-21"})
			tokens := ServiceAccountTokenClient{
				SA: newFakeSAAPI(), Legacy: legacy,
				SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{70}, nil },
			}

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
			})
			require.NoError(t, err)
			assert.Empty(t, result.Rotated, "a healthy supplied credential is not replaced")
			assert.Empty(t, result.Failed)
			assert.Empty(t, legacy.created, "no managed credential is minted")
			assert.NotContains(t, legacy.revoked, 50, "the supplied credential is never revoked")
		})
	}
}

// Publication succeeded, but its completion write failed. The next unforced
// rotation must recover rather than trust the previous supplied-token proof.
func TestRotateGitLabRoleCredentials_SuppliedToManagedRetriesAfterPublication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {Phase: rotationPhaseIdle, Supplied: true, SuppliedDistributed: true,
			SuppliedUserID: 70, SuppliedTokenID: 50, ExcludedUserIDs: []int{70}, DistributedAt: now.Format(time.RFC3339)},
	}}))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 50, Name: gitlabroles.PollerTokenName, Active: true, UserID: 70, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{ID: 51, Name: gitlabroles.PollerTokenName, Active: true, UserID: 71, ExpiresAt: "2027-09-21"})
	broken := &failStateAfterStoreClient{FakeClient: fc}
	cfg := RoleRotateConfig{Owner: "group", Repo: "project", Client: broken, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Force: true, Now: now}
	first, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	require.Len(t, first.Failed, 1)
	assert.Contains(t, first.Failed[0].Reason, "completed distribution state")
	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	pending := state.Roles["poller"]
	assert.Equal(t, rotationPhaseDistributing, pending.Phase)
	assert.False(t, pending.SuppliedDistributed)
	assert.Contains(t, pending.ExcludedUserIDs, 70)
	require.NotZero(t, pending.IncomingID)
	assert.Equal(t, tokens.created[0].Token, fc.CreatedSecrets[len(fc.CreatedSecrets)-1].Value)
	// Live inventory reports the minted service-account token's owner. The
	// generic token fake has no owner by default; fill that evidence in.
	for i := range tokens.listed {
		if tokens.listed[i].ID == pending.IncomingID {
			tokens.listed[i].UserID = 71
		}
	}

	cfg.Client, cfg.Force = fc, false
	second, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Empty(t, second.Failed)
	assert.Contains(t, second.Rotated, gitlabroles.RolePoller)
	state, _, err = loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	complete := state.Roles["poller"]
	assert.False(t, complete.Supplied)
	assert.Contains(t, complete.OutgoingIDs, pending.IncomingID)
	assert.Contains(t, complete.OutgoingIDs, 51)
	assert.Contains(t, complete.ExcludedUserIDs, 70)
	assert.NotContains(t, tokens.revoked, 50, "the supplied token remains excluded")
}

// Enrolling a supplied credential over a surviving managed entry records the
// successful distribution independently of that entry's managed-token state.
func TestRecordSuppliedEnrollment_RecordsDistributionOverManagedEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"overlapping","incoming_id":8,"outgoing_ids":[6],"distributed_at":"2026-09-01T00:00:00Z"}}}`, true))

	require.NoError(t, recordSuppliedEnrollment(ctx, fc, "group", "project", gitlabroles.RolePoller, 70, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)))
	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	rs := state.Roles["poller"]
	assert.True(t, rs.Supplied)
	assert.True(t, rs.SuppliedDistributed)
	assert.Equal(t, 8, rs.IncomingID, "managed cleanup state is preserved")
	assert.Equal(t, []int{6}, rs.OutgoingIDs)
	assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
}

// Provisioning a genuinely missing role secret replaces the provenance of an
// older supplied credential with the newly published managed credential, while
// keeping the historical exclusion and the previous managed token's cleanup.
func TestProvisionOwnRoles_MissingSecretReplacesSuppliedProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rotationKey := "group/project/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"overlapping","incoming_id":8,"outgoing_ids":[6],"distributed_at":"2026-01-01T00:00:00Z","supplied":true,"supplied_distributed":true,"supplied_user_id":70,"supplied_token_id":50,"excluded_user_ids":[70]}}}`
	fc.VariablesExist[rotationKey] = true
	tokens := &fakeTokens{}

	cfg := RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	}
	var result RoleProvisionResult
	provisionOwnRoles(ctx, cfg, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)
	require.Contains(t, result.Created, gitlabroles.RolePoller)

	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	rs := state.Roles["poller"]
	require.Len(t, tokens.created, 3)
	var newID int
	for _, tok := range tokens.created {
		if tok.Name == gitlabroles.PollerTokenName {
			newID = tok.ID
		}
	}
	require.NotZero(t, newID)
	assert.False(t, rs.Supplied)
	assert.False(t, rs.SuppliedDistributed)
	assert.Zero(t, rs.SuppliedUserID)
	assert.Zero(t, rs.SuppliedTokenID)
	assert.Equal(t, newID, rs.IncomingID, "lifecycle follows the newly published token")
	assert.Equal(t, []int{70}, rs.ExcludedUserIDs, "the previous supplied owner stays excluded")
	assert.ElementsMatch(t, []int{6, 8}, rs.OutgoingIDs, "outstanding and superseded managed tokens stay queued for cleanup")
}

// A failed supplied-replacement store leaves the installed credential's
// identity, token ID, and distribution proof untouched while still protecting
// the replacement owner's account, so status and inventory keep describing the
// previous credential and the attempt is retried.
func TestRotateGitLabRoleCredentials_FailedSuppliedReplacementKeepsInstalledIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	inner := seededRoleClient(t, gitlabroles.RolePoller)
	require.NoError(t, inner.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_distributed":true,"supplied_user_id":70,"supplied_token_id":50,"excluded_user_ids":[70]}}}`, true))
	fc := &selectiveSecretClient{Client: inner, fail: map[string]error{forge.SecretGitLabPollerToken: fmt.Errorf("nope")}}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
		Registry: gitlabroles.BuiltinRegistry(),
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller}, Force: true, Now: now,
		ProvidedCredentials: map[gitlabroles.Role]ProvidedRoleCredential{gitlabroles.RolePoller: {Token: "enrolledXXXX", OwnerID: 80, TokenID: 51}},
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)

	state, _, err := loadRotationState(ctx, inner, "group", "project")
	require.NoError(t, err)
	rs := state.Roles["poller"]
	assert.Equal(t, 70, rs.SuppliedUserID, "the installed credential's owner is unchanged")
	assert.Equal(t, 50, rs.SuppliedTokenID, "the installed credential's token is unchanged")
	assert.Equal(t, []int{70, 80}, rs.ExcludedUserIDs, "the replacement owner is protected before publication")
	assert.Equal(t, rotationPhaseFailed, rs.Phase)
	assert.False(t, rs.SuppliedDistributed, "the failed attempt is not proven and is retried")
}
