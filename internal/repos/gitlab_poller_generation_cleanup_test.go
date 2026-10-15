package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// generationCleanupFixture records a managed Poller in rotation state plus a
// retiring, a pending, and an abandoned generation account in the generation
// document, all live project service accounts.
func generationCleanupFixture(t *testing.T) (*forge.FakeClient, *fakeSAAPI) {
	t.Helper()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	const current, retiring, pending, abandoned = 20, 10, 30, 40
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RolePoller): {ManagedUserID: current, Phase: rotationPhaseIdle},
	}}))
	require.NoError(t, writePollerGenerations(ctx, fc, "g", "p", PollerGenerationState{
		CurrentUserID:    current,
		RetiringUserID:   retiring,
		AbandonedUserIDs: []int64{abandoned},
		Pending:          &PendingPollerGeneration{UserID: pending, Phase: PollerPhaseAccountRecorded},
	}))
	sa := newFakeSAAPI()
	for _, id := range []int{current, retiring, pending, abandoned} {
		sa.accounts = append(sa.accounts, GitLabServiceAccount{ID: id, Name: gitlabroles.PollerTokenName})
	}
	return fc, sa
}

// Uninstall verifies and deletes every owned Poller generation account, not
// only the one rotation state records.
func TestDeleteManagedServiceAccounts_DeletesRetiringPendingAndAbandonedGenerations(t *testing.T) {
	ctx := context.Background()
	fc, sa := generationCleanupFixture(t)
	var verified []int
	c := ServiceAccountTokenClient{
		SA: sa,
		VerifyAccountDeletion: func(_ context.Context, _, _ string, id int) error {
			verified = append(verified, id)
			return nil
		},
	}

	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.ElementsMatch(t, []int{10, 20, 30, 40}, fc.DeletedProjectServiceAccounts)
	assert.ElementsMatch(t, []int{10, 20, 30, 40}, verified, "resources of each owned account are verified before deletion")
}

// Supplied-account exclusions apply to generation accounts like any other.
func TestDeleteManagedServiceAccounts_GenerationAccountsHonorExclusions(t *testing.T) {
	ctx := context.Background()
	fc, sa := generationCleanupFixture(t)
	c := ServiceAccountTokenClient{
		SA:                    sa,
		VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil },
		SuppliedAccountIDs:    func(context.Context, string, string) ([]int, error) { return []int{10, 30}, nil },
	}

	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.ElementsMatch(t, []int{20, 40}, fc.DeletedProjectServiceAccounts)
}

// A generation account whose resources cannot be verified is not deleted and
// the failure is returned, so uninstall retains ownership state.
func TestDeleteManagedServiceAccounts_GenerationAccountVerificationFailureRetainsOwnership(t *testing.T) {
	ctx := context.Background()
	fc, sa := generationCleanupFixture(t)
	c := ServiceAccountTokenClient{
		SA: sa,
		VerifyAccountDeletion: func(_ context.Context, _, _ string, id int) error {
			if id == 10 || id == 40 {
				return errors.New("500 verifying resources")
			}
			return nil
		},
	}

	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.Error(t, err)
	assert.Equal(t, 2, n)
	assert.ElementsMatch(t, []int{20, 30}, fc.DeletedProjectServiceAccounts)
	assert.NotContains(t, fc.DeletedProjectServiceAccounts, 10)
	assert.NotContains(t, fc.DeletedProjectServiceAccounts, 40)
}

// An unreadable generation document fails closed rather than reading as no
// generation accounts.
func TestDeleteManagedServiceAccounts_UnreadableGenerationDocumentFailsClosed(t *testing.T) {
	ctx := context.Background()
	fc, sa := generationCleanupFixture(t)
	fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations] = "{not json"
	c := ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}

	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.Error(t, err)
	assert.Zero(t, n)
	assert.Empty(t, fc.DeletedProjectServiceAccounts)
}

// A generation document that records a Poller account makes cleanup require an
// account-cleanup capability even when rotation state names no managed account,
// so cleanup cannot report success while that account stays behind.
func TestPreflightGitLabRoleCleanup_GenerationOnlyOwnershipRequiresCleaner(t *testing.T) {
	for name, tokens := range map[string]ProjectAccessTokenClient{
		"nil client":     nil,
		"generic client": &fakeTokens{},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fc := forge.NewFakeClient()
			require.NoError(t, writePollerGenerations(ctx, fc, "g", "p", PollerGenerationState{CurrentUserID: 20, RetiringUserID: 10}))
			fc.VariablesExist["g/p/"+forge.VarGitLabRoleRegistry] = true
			before := fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations]

			err := PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", tokens)
			require.Error(t, err)
			assert.ErrorIs(t, err, forge.ErrNotSupported)
			_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
			require.Error(t, err)
			assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRegistry], "installation variables are untouched")
			assert.Equal(t, before, fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations], "the generation document is untouched")
		})
	}
}

// Generation accounts named only by rotation-state exclusions need no cleaner.
func TestPreflightGitLabRoleCleanup_ExcludedGenerationAccountNeedsNoCleaner(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {SuppliedUserID: 20},
	}}))
	require.NoError(t, writePollerGenerations(ctx, fc, "g", "p", PollerGenerationState{CurrentUserID: 20}))
	require.NoError(t, PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", &fakeTokens{}))
}

// An unreadable generation document refuses cleanup for every token client.
func TestPreflightGitLabRoleCleanup_UnreadableGenerationDocumentRefuses(t *testing.T) {
	for name, tokens := range map[string]ProjectAccessTokenClient{
		"nil client":     nil,
		"generic client": &fakeTokens{},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fc := forge.NewFakeClient()
			fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations] = "{not json"
			fc.VariablesExist["g/p/"+forge.VarGitLabRoleRegistry] = true

			err := PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", tokens)
			require.Error(t, err)
			_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
			require.Error(t, err)
			assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRegistry], "installation variables are untouched")
		})
	}
}
