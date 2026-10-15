package repos

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// Generation accounts are managed, so the status consumer reports them as
// managed and checks their effective access, even though rotation state
// records only the current Poller.
func TestServiceAccountStatus_GenerationAccountsAreManagedAndDriftChecked(t *testing.T) {
	const current, retiring, pending, abandoned = 20, 10, 30, 40
	for _, tc := range []struct {
		name  string
		id    int
		level int
		drift bool
	}{
		{"retiring at Developer", retiring, gitlabroles.DeveloperAccessLevel, false},
		{"retiring above Developer", retiring, 40, true},
		{"pending above Developer", pending, 40, true},
		{"abandoned above Developer", abandoned, 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fc, sa := generationCleanupFixture(t)
			for i := range sa.accounts {
				sa.members[int64(sa.accounts[i].ID)] = gitlabroles.DeveloperAccessLevel
			}
			sa.members[int64(tc.id)] = tc.level
			status := &RepoStatus{GitLabRolesReady: true}

			changed := (ServiceAccountTokenClient{SA: sa}).AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status)

			assert.Equal(t, tc.drift, changed)
			assert.Equal(t, !tc.drift, status.GitLabRolesReady)
			managed := map[int]bool{}
			for _, acct := range status.GitLabServiceAccounts {
				managed[acct.ID] = acct.Managed
			}
			for _, id := range []int{current, retiring, pending, abandoned} {
				assert.True(t, managed[id], "account %d is reported managed", id)
			}
			if tc.drift {
				require.Len(t, status.Drifts, 1)
				assert.Equal(t, fmt.Sprintf("gitlab-service-account:%d", tc.id), status.Drifts[0].Field)
			} else {
				assert.Empty(t, status.Drifts)
			}
		})
	}
}

// A supplied-account exclusion still wins over generation ownership.
func TestServiceAccountStatus_GenerationAccountExclusionWins(t *testing.T) {
	ctx := context.Background()
	fc, sa := generationCleanupFixture(t)
	for i := range sa.accounts {
		sa.members[int64(sa.accounts[i].ID)] = gitlabroles.DeveloperAccessLevel
	}
	sa.members[10] = 40
	status := &RepoStatus{GitLabRolesReady: true}
	c := ServiceAccountTokenClient{SA: sa, SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil }}

	assert.False(t, c.AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
	assert.True(t, status.GitLabRolesReady)
	assert.Empty(t, status.Drifts)
}

// An unreadable generation document is reported, never read as "no generation
// accounts".
func TestServiceAccountStatus_UnreadableGenerationDocumentIsUnverified(t *testing.T) {
	ctx := context.Background()
	fc, sa := generationCleanupFixture(t)
	fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations] = "{not json"
	status := &RepoStatus{GitLabRolesReady: true}

	assert.True(t, (ServiceAccountTokenClient{SA: sa}).AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
	assert.False(t, status.GitLabRolesReady)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, "gitlab-service-accounts", status.Drifts[0].Field)
}

// Uninstall retires the generation document once every account it records was
// cleaned up, so a reinstall starts from an empty generation state.
func TestCleanupGitLabRoleIdentity_RetiresGenerationDocumentAfterAccountCleanup(t *testing.T) {
	ctx := context.Background()
	fc, sa := generationCleanupFixture(t)
	c := ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{10, 20, 30, 40}, fc.DeletedProjectServiceAccounts)
	assert.False(t, fc.VariablesExist["g/p/"+forge.VarGitLabPollerGenerations], "the generation document is retired")
	st, err := loadPollerGenerations(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{}, st, "a reinstall sees an empty generation state")
}

// Incomplete or ambiguous cleanup keeps the generation document.
func TestCleanupGitLabRoleIdentity_KeepsGenerationDocumentWhenCleanupIncomplete(t *testing.T) {
	t.Run("account verification fails", func(t *testing.T) {
		ctx := context.Background()
		fc, sa := generationCleanupFixture(t)
		c := ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(_ context.Context, _, _ string, id int) error {
			if id == 10 {
				return assert.AnError
			}
			return nil
		}}
		_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
		require.Error(t, err)
		assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabPollerGenerations])
	})
	t.Run("recorded account is administrator-owned", func(t *testing.T) {
		ctx := context.Background()
		fc, sa := generationCleanupFixture(t)
		c := ServiceAccountTokenClient{
			SA:                    sa,
			VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil },
			SuppliedAccountIDs:    func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
		}
		res, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
		require.NoError(t, err)
		assert.NotContains(t, fc.DeletedProjectServiceAccounts, 10)
		assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabPollerGenerations])
		assert.Contains(t, strings.Join(res.Diagnostics, "\n"), "Preserved "+forge.VarGitLabPollerGenerations)
	})
	t.Run("pending creation has no recorded account", func(t *testing.T) {
		ctx := context.Background()
		fc, sa := generationCleanupFixture(t)
		require.NoError(t, writePollerGenerations(ctx, fc, "g", "p", PollerGenerationState{CurrentUserID: 20, Pending: &PendingPollerGeneration{Phase: PollerPhaseAccountRequested}}))
		c := ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}
		_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
		require.NoError(t, err)
		assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabPollerGenerations])
	})
	t.Run("unreadable document", func(t *testing.T) {
		ctx := context.Background()
		fc, sa := generationCleanupFixture(t)
		fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations] = "{not json"
		c := ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}
		_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
		require.Error(t, err)
		assert.Equal(t, "{not json", fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations])
	})
	t.Run("no account cleaner", func(t *testing.T) {
		ctx := context.Background()
		fc, _ := generationCleanupFixture(t)
		_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc})
		require.Error(t, err, "recorded accounts cannot be cleaned up without an account cleaner")
		assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabPollerGenerations])
	})
}

// A resumed account_recorded replacement that already owns a pipeline trigger,
// even one with the managed description, is quarantined and never raised.
func TestHandoffPollerGeneration_PreExistingTriggerQuarantinesBeforeElevation(t *testing.T) {
	for _, description := range []string{GitLabWebhookTriggerDescription, "other trigger"} {
		t.Run(description, func(t *testing.T) {
			f := newHandoffFake()
			f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}})
			f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelDeveloper
			f.c.PipelineTriggerTokens[handoffOwner+"/"+handoffRepo] = []forge.PipelineTriggerToken{{ID: 91, Description: description, OwnerID: handoffNewUID}}

			res, err := f.run(t)
			require.NoError(t, err)
			assert.Contains(t, res.DeferReason, "already owns pipeline trigger token IDs [91]")
			assert.Empty(t, f.memberSets[handoffNewUID], "the account is never raised to Maintainer")
			assert.NotContains(t, f.events, "create-bootstrap")
			assert.NotContains(t, f.events, "elevate")
			assert.Zero(t, f.triggerCreates)
			assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
		})
	}
}

// Triggers owned by other accounts (the current Poller's published trigger)
// do not block the replacement.
func TestHandoffPollerGeneration_OtherOwnersTriggerDoesNotBlockElevation(t *testing.T) {
	f := newHandoffFake()
	f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}})
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelDeveloper
	f.c.PipelineTriggerTokens[handoffOwner+"/"+handoffRepo] = []forge.PipelineTriggerToken{{ID: 91, Description: GitLabWebhookTriggerDescription, OwnerID: handoffCurrentUID}}

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Empty(t, res.DeferReason)
	assert.Contains(t, f.events, "elevate")
}

// An unreadable trigger inventory fails closed before elevation.
func TestHandoffPollerGeneration_TriggerInventoryFailureStopsBeforeElevation(t *testing.T) {
	f := newHandoffFake()
	f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}})
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelDeveloper
	f.c.Errors["ListPipelineTriggerTokens"] = assert.AnError

	_, err := f.run(t)
	require.Error(t, err)
	assert.NotContains(t, f.events, "elevate")
	assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase)
}

// A provisioned-looking webhook is not reported complete while a verified
// pending generation's cutover is unrecorded and the Poller identity cannot be
// resolved (a runtime-secret write that failed without committing).
func TestEnsureGitLabWebhookFastPath_UnrecordedCutoverIsNotReportedProvisioned(t *testing.T) {
	c, h := newPublishFailClient()
	_, err := ensureWebhookAsPollerOn(c, h)
	require.NoError(t, err)
	require.Len(t, c.hooks(), 1)

	// The verified replacement owns the live trigger but the generation
	// document does not record the cutover, and the live identity cannot be
	// resolved.
	seedGenerationState(t, c, PollerGenerationState{
		CurrentUserID: cutoverNewUID,
		Pending:       &PendingPollerGeneration{UserID: freshPollerUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3},
	})
	h.resolveFromRotation = false
	h.uidErr = forge.ErrNotFound

	res, err := ensureWebhookAsPollerOn(c, h)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	require.NotEmpty(t, res.Details)
	assert.Contains(t, res.Details[len(res.Details)-1], "cutover")
	assert.Contains(t, res.Details[len(res.Details)-1], "user ID 7007")
}
