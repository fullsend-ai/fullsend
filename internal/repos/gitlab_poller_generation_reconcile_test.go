package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

func seedGenerationState(t *testing.T, c forge.Client, st PollerGenerationState) {
	t.Helper()
	require.NoError(t, writePollerGenerations(context.Background(), c, webhookTestOwner, webhookTestRepo, st))
}

// An interrupted replacement exists only in the generation document, so
// reconcilePollerElevation (which runs before every safety and readiness
// early return) must demote it and persist the quarantine or resume state.
func TestReconcilePollerElevation_InterruptedReplacementIsDemoted(t *testing.T) {
	for _, phase := range []PollerGenerationPhase{PollerPhaseElevated, PollerPhaseTriggerRequested} {
		t.Run(string(phase), func(t *testing.T) {
			c := newWebhookFake()
			h := newHandoffTriggerOwner(c)
			c.ProjectMemberAccess[cutoverNewUID] = forge.GitLabAccessLevelMaintainer
			seedGenerationState(t, c, PollerGenerationState{
				CurrentUserID: webhookPollerUserID,
				Pending:       &PendingPollerGeneration{UserID: cutoverNewUID, Phase: phase, TriggerAttempted: phase == PollerPhaseTriggerRequested},
			})

			res, err := reconcilePollerElevation(context.Background(), c, h, webhookTestOwner, webhookTestRepo)
			require.NoError(t, err)
			assert.Equal(t, "update", res.Action)
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[cutoverNewUID], "the replacement is back at Developer")
			assert.Contains(t, h.levelsByUser[cutoverNewUID], forge.GitLabAccessLevelDeveloper)

			st := generationState(t, c)
			require.NotNil(t, st.Pending)
			if phase == PollerPhaseElevated {
				assert.Equal(t, PollerPhaseAccountRecorded, st.Pending.Phase, "a generation that never sent its trigger request resumes")
			} else {
				assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase, "an in-flight trigger request keeps its quarantine obligation")
			}
		})
	}
}

func TestReconcilePollerElevation_UnverifiableReplacementStaysQuarantined(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	h.restoreErrs = 10
	c.ProjectMemberAccess[cutoverNewUID] = forge.GitLabAccessLevelMaintainer
	seedGenerationState(t, c, PollerGenerationState{
		CurrentUserID: webhookPollerUserID,
		Pending:       &PendingPollerGeneration{UserID: cutoverNewUID, Phase: PollerPhaseElevated},
	})

	_, err := reconcilePollerElevation(context.Background(), c, h, webhookTestOwner, webhookTestRepo)
	require.Error(t, err)
	st := generationState(t, c)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)
	assert.Equal(t, cutoverNewUID, st.Pending.UserID)
}

func TestReconcilePollerElevation_QuarantinedReplacementIsDemotedAndStaysQuarantined(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	c.ProjectMemberAccess[cutoverNewUID] = forge.GitLabAccessLevelMaintainer
	quarantined := &PendingPollerGeneration{UserID: cutoverNewUID, Phase: PollerPhaseQuarantined, Reason: "unknown trigger result"}
	seedGenerationState(t, c, PollerGenerationState{CurrentUserID: webhookPollerUserID, Pending: quarantined})

	_, err := reconcilePollerElevation(context.Background(), c, h, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[cutoverNewUID])
	assert.Equal(t, PollerGenerationState{CurrentUserID: webhookPollerUserID, Pending: quarantined}, generationState(t, c), "only an operator clears a quarantine")
}

// A pending replacement that was never demoted by its own handoff (it is not a
// managed account until cutover) is restored by an ordinary install when its
// membership drifted above Developer, and its recovery state is kept.
func TestReconcilePollerElevation_PendingReplacementAboveDeveloperIsRestored(t *testing.T) {
	for _, phase := range []PollerGenerationPhase{PollerPhaseAccountRecorded, PollerPhaseVerified} {
		t.Run(string(phase), func(t *testing.T) {
			c := newWebhookFake()
			h := newHandoffTriggerOwner(c)
			c.ProjectMemberAccess[cutoverNewUID] = forge.GitLabAccessLevelMaintainer
			pending := &PendingPollerGeneration{UserID: cutoverNewUID, Phase: phase}
			if phase == PollerPhaseVerified {
				pending.TriggerAttempted, pending.TriggerID = true, 3
			}
			seedGenerationState(t, c, PollerGenerationState{CurrentUserID: webhookPollerUserID, Pending: pending})

			_, err := reconcilePollerElevation(context.Background(), c, h, webhookTestOwner, webhookTestRepo)
			require.NoError(t, err)
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[cutoverNewUID])
			assert.Empty(t, h.contained, "a restorable replacement keeps its credentials")
			assert.Equal(t, PollerGenerationState{CurrentUserID: webhookPollerUserID, Pending: pending}, generationState(t, c), "the recovery state is kept")
		})
	}
}

func TestReconcilePollerElevation_PendingReplacementUnrestorableIsContained(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	h.restoreErrs = 10
	c.ProjectMemberAccess[cutoverNewUID] = forge.GitLabAccessLevelMaintainer
	pending := &PendingPollerGeneration{UserID: cutoverNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3}
	seedGenerationState(t, c, PollerGenerationState{CurrentUserID: webhookPollerUserID, Pending: pending})

	_, err := reconcilePollerElevation(context.Background(), c, h, webhookTestOwner, webhookTestRepo)
	require.Error(t, err)
	assert.Equal(t, []int64{cutoverNewUID}, h.contained)
	assert.Equal(t, PollerGenerationState{CurrentUserID: webhookPollerUserID, Pending: pending}, generationState(t, c), "the recovery state is kept")
}

func TestReconcilePollerElevation_NoGenerationStateChangesNothing(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	res, err := reconcilePollerElevation(context.Background(), c, h, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Empty(t, res.Action)
	assert.Empty(t, h.levelsByUser)
}

// After cutover the superseded Poller and any pending replacement leave the
// rotation state's managed account, so the destructive inventories must read
// them from the generation document.
func TestManagedGitLabRoleAccountInventoriesIncludeGenerationAccounts(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{
		Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 5005}},
	}))
	require.NoError(t, writePollerGenerations(ctx, fc, "g", "p", PollerGenerationState{
		CurrentUserID:  5005,
		RetiringUserID: 4004,
		Pending:        &PendingPollerGeneration{UserID: 6006, Phase: PollerPhaseQuarantined},
	}))

	ids, err := ManagedGitLabRoleAccountIDs(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{5005, 4004, 6006}, ids)

	names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, gitlabroles.PollerTokenName, names[4004])
	assert.Equal(t, gitlabroles.PollerTokenName, names[6006])
	assert.Contains(t, names, 5005)

	generation, err := ManagedGitLabPollerGenerationAccountIDs(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{5005, 4004, 6006}, generation)

	fc.VariableValues["g/p/"+forge.VarGitLabPollerGenerations] = "  "
	_, err = ManagedGitLabRoleAccountIDs(ctx, fc, "g", "p")
	require.Error(t, err, "an unreadable generation document fails closed")
}

// After the cutover's pre-publication rotation-state write, managed_user_id
// names the replacement while the generation document still names the previous
// Poller as current with the replacement pending. The previous Poller still
// holds active credentials, so it must stay in every destructive inventory.
func TestManagedGitLabRoleAccountInventoriesIncludeCurrentPollerDuringCutover(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{
		Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 5005}},
	}))
	require.NoError(t, writePollerGenerations(ctx, fc, "g", "p", PollerGenerationState{
		CurrentUserID: 4004,
		Pending:       &PendingPollerGeneration{UserID: 5005, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3},
	}))

	ids, err := ManagedGitLabRoleAccountIDs(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{5005, 4004}, ids)

	names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, gitlabroles.PollerTokenName, names[4004])
	assert.Contains(t, names, 5005)

	generation, err := ManagedGitLabPollerGenerationAccountIDs(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{4004, 5005}, generation)
}
