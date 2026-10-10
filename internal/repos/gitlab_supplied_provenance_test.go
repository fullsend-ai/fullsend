package repos

import (
	"context"
	"testing"

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
	fc := suppliedProvenanceClient(`{"roles":{` +
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
		"negative supplied owner":   `{"roles":{"analyst":{"supplied":true,"supplied_user_id":-1}}}`,
		"nonpositive excluded user": `{"roles":{"poller":{"excluded_user_ids":[0]}}}`,
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
	fc := suppliedProvenanceClient(`{"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":77,"excluded_user_ids":[90]}}}`)

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
	const state = `{"roles":{` +
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
