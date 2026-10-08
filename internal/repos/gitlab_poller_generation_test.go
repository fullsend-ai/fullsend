package repos

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	handoffOwner      = "group"
	handoffRepo       = "project"
	handoffCurrentUID = int64(4004)
	handoffNewUID     = int64(5005)
)

// handoffClient fails the nth UpdateCIVariable call (1-based) when failWrite
// is set, so individual generation-state writes can be failed.
type handoffClient struct {
	*forge.FakeClient
	writes    int
	failWrite int
}

func (c *handoffClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	c.writes++
	if c.failWrite != 0 && c.writes == c.failWrite {
		return errors.New("500 internal error")
	}
	return c.FakeClient.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

// handoffFake models the replacement Poller account. Membership changes
// update the client's effective access map; triggers it creates are owned by
// the account that holds the bootstrap credential.
type handoffFake struct {
	c *handoffClient

	accountUID    int64
	accountErr    error
	accountCalls  int
	inventoryErrs []error // consumed per VerifyPollerCredentialsRevoked call
	bootstrapErr  error
	bootRevokeErr error
	elevateErr    error
	restoreFails  bool
	mintErr       error
	mintNoToken   bool
	mintOwner     int64
	probeErr      error

	events         []string
	levelAtCreate  int
	triggerCreates int
	memberSets     map[int64][]int
}

func newHandoffFake() *handoffFake {
	fc := forge.NewFakeClient()
	fc.ProjectMemberAccess = map[int64]int{handoffCurrentUID: forge.GitLabAccessLevelDeveloper}
	return &handoffFake{c: &handoffClient{FakeClient: fc}, accountUID: handoffNewUID, memberSets: map[int64][]int{}}
}

func (f *handoffFake) CreateReplacementPoller(context.Context, string, string) (int64, error) {
	f.accountCalls++
	f.events = append(f.events, "create-account")
	if f.accountUID > 0 {
		f.c.ProjectMemberAccess[f.accountUID] = forge.GitLabAccessLevelDeveloper
	}
	return f.accountUID, f.accountErr
}

func (f *handoffFake) VerifyTriggerStartsProtectedPipeline(context.Context, string, string, *forge.PipelineTriggerToken) error {
	f.events = append(f.events, "probe")
	return f.probeErr
}

func (f *handoffFake) PollerUserID(context.Context, string, string) (int64, error) {
	return handoffCurrentUID, nil
}

func (f *handoffFake) CreatePollerBootstrap(context.Context, string, string, int64) (*PollerBootstrap, error) {
	f.events = append(f.events, "create-bootstrap")
	if f.bootstrapErr != nil {
		return nil, f.bootstrapErr
	}
	return &PollerBootstrap{ID: 77, Token: "bootstrap-secret-value"}, nil
}

func (f *handoffFake) CreatePipelineTriggerTokenAsPoller(ctx context.Context, owner, repo, description string, _ *PollerBootstrap) (*forge.PipelineTriggerToken, error) {
	f.events = append(f.events, "create-trigger")
	f.triggerCreates++
	f.levelAtCreate = f.c.ProjectMemberAccess[handoffNewUID]
	f.c.TriggerTokenOwnerID = handoffNewUID
	if f.mintOwner != 0 {
		f.c.TriggerTokenOwnerID = f.mintOwner
	}
	if f.mintErr != nil {
		return nil, f.mintErr
	}
	tok, err := f.c.CreatePipelineTriggerToken(ctx, owner, repo, description)
	if f.mintNoToken && tok != nil {
		tok.Token = ""
	}
	return tok, err
}

func (f *handoffFake) RevokePollerBootstrap(context.Context, string, string, int64) error {
	f.events = append(f.events, "revoke-bootstrap")
	return f.bootRevokeErr
}

func (f *handoffFake) SetProjectMemberAccessLevel(_ context.Context, _, _ string, uid int64, level int) error {
	f.memberSets[uid] = append(f.memberSets[uid], level)
	if level == forge.GitLabAccessLevelMaintainer {
		f.events = append(f.events, "elevate")
		if f.elevateErr != nil {
			// A failed update may still have changed the role.
			f.c.ProjectMemberAccess[uid] = level
			return f.elevateErr
		}
	} else {
		f.events = append(f.events, "demote")
		if f.restoreFails {
			return errors.New("500 internal error")
		}
	}
	f.c.ProjectMemberAccess[uid] = level
	return nil
}

func (f *handoffFake) VerifyPollerElevationSafe(context.Context, string, string, int64) error {
	return nil
}

func (f *handoffFake) VerifyPollerCredentialsRevoked(context.Context, string, string, int64) error {
	f.events = append(f.events, "inventory")
	if len(f.inventoryErrs) == 0 {
		return nil
	}
	err := f.inventoryErrs[0]
	f.inventoryErrs = f.inventoryErrs[1:]
	return err
}

func (f *handoffFake) RevokePollerRuntimeCredentials(context.Context, string, string, int64) error {
	f.events = append(f.events, "revoke-runtime")
	return nil
}

func (f *handoffFake) CreatePollerRuntimeToken(context.Context, string, string, int64, string) (*PollerRuntimeToken, error) {
	f.events = append(f.events, "publish-runtime")
	return nil, errors.New("must not be called")
}

func (f *handoffFake) ContainPoller(context.Context, string, string, int64) error {
	f.events = append(f.events, "contain")
	return nil
}

func (f *handoffFake) run(t *testing.T) (PollerHandoffResult, error) {
	t.Helper()
	return handoffPollerGeneration(context.Background(), f.c, f, f, handoffOwner, handoffRepo, &credentialRedactor{})
}

func (f *handoffFake) state(t *testing.T) PollerGenerationState {
	t.Helper()
	st, err := loadPollerGenerations(context.Background(), f.c, handoffOwner, handoffRepo)
	require.NoError(t, err)
	return st
}

func (f *handoffFake) seed(t *testing.T, st PollerGenerationState) {
	t.Helper()
	require.NoError(t, writePollerGenerations(context.Background(), f.c.FakeClient, handoffOwner, handoffRepo, st))
}

func TestHandoffPollerGeneration_FreshIdentityOwnsTrigger(t *testing.T) {
	f := newHandoffFake()
	f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID})

	res, err := f.run(t)
	require.NoError(t, err)
	require.Empty(t, res.DeferReason)
	require.NotNil(t, res.Trigger)
	assert.Equal(t, handoffNewUID, res.NewUserID)
	assert.Equal(t, handoffNewUID, res.Trigger.OwnerID)
	assert.NotEmpty(t, res.Trigger.Token)

	// Bootstrap revocation and the PAT inventory precede demotion; nothing is
	// published and the protected-branch probe runs after demotion.
	assert.Equal(t, []string{
		"create-account", "inventory", "create-bootstrap", "elevate", "create-trigger",
		"revoke-bootstrap", "inventory", "demote", "probe",
	}, f.events)
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, f.levelAtCreate)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
	// The current Poller is never elevated or otherwise modified.
	assert.Empty(t, f.memberSets[handoffCurrentUID])
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffCurrentUID])

	st := f.state(t)
	assert.Equal(t, handoffCurrentUID, st.CurrentUserID)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: res.Trigger.ID}, *st.Pending)
	assert.NotContains(t, f.c.VariableValues[handoffOwner+"/"+handoffRepo+"/"+forge.VarGitLabPollerGenerations], res.Trigger.Token)
}

func TestHandoffPollerGeneration_RecordsPhasesBeforeActing(t *testing.T) {
	f := newHandoffFake()
	_, err := f.run(t)
	require.NoError(t, err)

	var phases []PollerGenerationPhase
	for _, rec := range f.c.UpdatedVariables {
		if rec.Name != forge.VarGitLabPollerGenerations {
			continue
		}
		st, err := decodePollerGenerations(rec.Value)
		require.NoError(t, err)
		require.NotNil(t, st.Pending)
		phases = append(phases, st.Pending.Phase)
		assert.True(t, rec.Protected)
	}
	assert.Equal(t, []PollerGenerationPhase{
		PollerPhaseAccountRequested, PollerPhaseAccountRecorded, PollerPhaseElevated,
		PollerPhaseTriggerRequested, PollerPhaseVerified,
	}, phases)
}

func TestHandoffPollerGeneration_AmbiguousCreateQuarantinesWithoutRetry(t *testing.T) {
	f := newHandoffFake()
	f.mintErr = context.DeadlineExceeded

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "quarantined")
	assert.Contains(t, res.DeferReason, "never retried")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
	st := f.state(t)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)
	assert.True(t, st.Pending.TriggerAttempted)

	// A later run neither retries the create nor starts another generation.
	f.events = nil
	res, err = f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "quarantined")
	assert.Empty(t, f.events)
	assert.Equal(t, 1, f.triggerCreates)
	assert.Equal(t, 1, f.accountCalls)
}

func TestHandoffPollerGeneration_MissingTokenValueIsAmbiguous(t *testing.T) {
	f := newHandoffFake()
	f.mintNoToken = true

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "no confirmed result")
	assert.Len(t, f.c.RevokedTriggerTokenIDs, 1)
}

func TestHandoffPollerGeneration_FailedDemotionPublishesNothing(t *testing.T) {
	f := newHandoffFake()
	f.restoreFails = true

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "5005")
	assert.Contains(t, res.DeferReason, "administrator")
	assert.NotContains(t, f.events, "publish-runtime")
	assert.NotContains(t, f.events, "probe")
	assert.Len(t, f.c.RevokedTriggerTokenIDs, 1, "the trigger of an unverified generation is revoked")
	st := f.state(t)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)
	assert.Equal(t, handoffNewUID, st.Pending.UserID)
}

func TestHandoffPollerGeneration_ActivePATAfterTriggerQuarantines(t *testing.T) {
	f := newHandoffFake()
	// Clean before elevation; a late self-rotation left a PAT afterwards.
	f.inventoryErrs = []error{nil, &PollerElevationUnsafeError{Reason: "an unmanaged personal access token is active"}}

	res, err := f.run(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "an unmanaged personal access token is active")
	assert.Nil(t, res.Trigger)
	assert.Contains(t, f.events, "demote", "demotion still runs")
	assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_OwnerMismatchQuarantines(t *testing.T) {
	f := newHandoffFake()
	f.mintOwner = 999

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "reports owner user ID 999")
	assert.Len(t, f.c.RevokedTriggerTokenIDs, 1)
}

func TestHandoffPollerGeneration_ProtectedBranchProbeFailure(t *testing.T) {
	f := newHandoffFake()
	f.probeErr = forge.ErrForbidden

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "protected default branch")
	assert.Contains(t, res.DeferReason, "does not broaden branch protection")
	assert.Len(t, f.c.RevokedTriggerTokenIDs, 1)
	assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_InterruptedTriggerRequestQuarantines(t *testing.T) {
	f := newHandoffFake()
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelMaintainer
	f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseTriggerRequested, TriggerAttempted: true}})

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "unknown result")
	assert.Equal(t, []string{"revoke-bootstrap", "inventory", "demote"}, f.events)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
	assert.Zero(t, f.triggerCreates)
	assert.Zero(t, f.accountCalls)
	assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_InterruptedElevationResumes(t *testing.T) {
	f := newHandoffFake()
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelMaintainer
	f.seed(t, PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseElevated}})

	res, err := f.run(t)
	require.NoError(t, err)
	require.NotNil(t, res.Trigger)
	assert.Zero(t, f.accountCalls, "the recorded account is reused")
	assert.Equal(t, 1, f.triggerCreates)
	assert.Equal(t, []string{"revoke-bootstrap", "inventory", "demote"}, f.events[:3])
	assert.Contains(t, res.Details[0], "interrupted")
}

// An interruption after the bootstrap credential is created but before the
// account is raised leaves the account at Developer with an active bootstrap
// credential. The elevated phase is recorded before that credential exists,
// so recovery revokes it before the zero-credential inventory check.
func TestHandoffPollerGeneration_InterruptedBootstrapResumes(t *testing.T) {
	f := newHandoffFake()
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelDeveloper
	f.seed(t, PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseElevated}})

	res, err := f.run(t)
	require.NoError(t, err)
	require.NotNil(t, res.Trigger)
	assert.Equal(t, []string{"revoke-bootstrap", "inventory", "demote"}, f.events[:3])
	assert.Zero(t, f.accountCalls, "the recorded account is reused")
	assert.Equal(t, 1, f.triggerCreates)
}

func TestHandoffPollerGeneration_InterruptedRecoveryFailureQuarantines(t *testing.T) {
	f := newHandoffFake()
	f.restoreFails = true
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelMaintainer
	f.seed(t, PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseElevated}})

	res, err := f.run(t)
	require.Error(t, err)
	assert.Contains(t, res.DeferReason, "administrator")
	assert.Zero(t, f.triggerCreates)
	assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_Blocked(t *testing.T) {
	for name, tc := range map[string]struct {
		st   PollerGenerationState
		want string
	}{
		"lost account response": {PollerGenerationState{Pending: &PendingPollerGeneration{Phase: PollerPhaseAccountRequested}}, "never recorded"},
		"retiring":              {PollerGenerationState{CurrentUserID: handoffNewUID, RetiringUserID: handoffCurrentUID}, "has not finished retiring"},
		"quarantined":           {PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseQuarantined, Reason: "operator action"}}, "operator action"},
		"verified":              {PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseVerified, TriggerID: 3}}, "cutover did not complete"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFake()
			f.seed(t, tc.st)
			res, err := f.run(t)
			require.NoError(t, err)
			assert.Contains(t, res.DeferReason, tc.want)
			assert.Contains(t, res.DeferReason, "polling schedules remain in effect")
			assert.Empty(t, f.events)
		})
	}
}

func TestHandoffPollerGeneration_ServiceAccountsUnsupported(t *testing.T) {
	f := newHandoffFake()
	f.accountUID = 0
	f.accountErr = forge.ErrNotFound

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "does not support project service accounts")
	assert.Nil(t, f.state(t).Pending, "no account was created, so nothing stays pending")
}

func TestHandoffPollerGeneration_AmbiguousAccountCreateNeedsManualReconciliation(t *testing.T) {
	f := newHandoffFake()
	f.accountUID = 0
	f.accountErr = context.DeadlineExceeded

	_, err := f.run(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reconciled manually")
	assert.Equal(t, PollerPhaseAccountRequested, f.state(t).Pending.Phase)

	f.events = nil
	res, err := f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "never recorded")
	assert.Equal(t, 1, f.accountCalls)
}

func TestHandoffPollerGeneration_AccountCreatedWithErrorIsRecorded(t *testing.T) {
	f := newHandoffFake()
	f.accountErr = errors.New("500 adding member")

	_, err := f.run(t)
	require.Error(t, err)
	st := f.state(t)
	assert.Equal(t, PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}, *st.Pending)
}

func TestHandoffPollerGeneration_UnrecordedAccountReportsID(t *testing.T) {
	f := newHandoffFake()
	f.c.failWrite = 2 // the write recording the new account ID

	_, err := f.run(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user ID 5005")
	assert.Contains(t, err.Error(), "not recorded")
}

func TestHandoffPollerGeneration_NotRaisedWithoutPreconditions(t *testing.T) {
	t.Run("not developer", func(t *testing.T) {
		f := newHandoffFake()
		f.seed(t, PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}})
		f.c.ProjectMemberAccess[handoffNewUID] = 20
		res, err := f.run(t)
		require.NoError(t, err)
		assert.Contains(t, res.DeferReason, "not Developer")
		assert.Empty(t, f.events)
	})
	t.Run("not a member", func(t *testing.T) {
		f := newHandoffFake()
		f.seed(t, PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}})
		_, err := f.run(t)
		require.Error(t, err)
		assert.Empty(t, f.events)
	})
	t.Run("active credential", func(t *testing.T) {
		f := newHandoffFake()
		f.inventoryErrs = []error{&PollerElevationUnsafeError{Reason: "it holds an active personal access token"}}
		res, err := f.run(t)
		require.NoError(t, err)
		assert.Contains(t, res.DeferReason, "it holds an active personal access token")
		assert.NotContains(t, f.events, "create-bootstrap")
		assert.NotContains(t, f.events, "elevate")
		assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase, "the unexposed account may be resumed")
	})
}

func TestHandoffPollerGeneration_BootstrapFailures(t *testing.T) {
	t.Run("create fails", func(t *testing.T) {
		f := newHandoffFake()
		f.bootstrapErr = errors.New("500")
		_, err := f.run(t)
		require.Error(t, err)
		assert.NotContains(t, f.events, "elevate")
		assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase)
	})
	t.Run("create and revoke fail", func(t *testing.T) {
		f := newHandoffFake()
		f.bootstrapErr = errors.New("500")
		f.bootRevokeErr = errors.New("500")
		res, err := f.run(t)
		require.Error(t, err)
		assert.Contains(t, res.DeferReason, "fullsend-poller-bootstrap")
		assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
	})
	t.Run("token value never stored or reported", func(t *testing.T) {
		f := newHandoffFake()
		f.restoreFails = true
		_, err := f.run(t)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "bootstrap-secret-value")
	})
}

func TestHandoffPollerGeneration_ElevationFailure(t *testing.T) {
	t.Run("forbidden defers and resumes later", func(t *testing.T) {
		f := newHandoffFake()
		f.elevateErr = forge.ErrForbidden
		res, err := f.run(t)
		require.NoError(t, err)
		assert.Contains(t, res.DeferReason, "temporary Maintainer access")
		assert.Zero(t, f.triggerCreates)
		assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase)
	})
	t.Run("other error", func(t *testing.T) {
		f := newHandoffFake()
		f.elevateErr = errors.New("500")
		_, err := f.run(t)
		require.Error(t, err)
		assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase)
	})
	t.Run("revert unverified", func(t *testing.T) {
		f := newHandoffFake()
		f.elevateErr = errors.New("500")
		f.restoreFails = true
		_, err := f.run(t)
		require.Error(t, err)
		assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
	})
}

func TestHandoffPollerGeneration_StateWriteFailures(t *testing.T) {
	t.Run("elevation not recorded", func(t *testing.T) {
		f := newHandoffFake()
		f.c.failWrite = 3
		_, err := f.run(t)
		require.Error(t, err)
		assert.NotContains(t, f.events, "create-bootstrap", "no bootstrap credential is created before the phase is recorded")
		assert.NotContains(t, f.events, "elevate")
	})
	t.Run("trigger attempt not recorded", func(t *testing.T) {
		f := newHandoffFake()
		f.c.failWrite = 4
		_, err := f.run(t)
		require.Error(t, err)
		assert.Zero(t, f.triggerCreates, "an unrecorded attempt is never sent")
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
		assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase)
	})
	t.Run("verified not recorded", func(t *testing.T) {
		f := newHandoffFake()
		f.c.failWrite = 5
		res, err := f.run(t)
		require.Error(t, err)
		assert.Nil(t, res.Trigger)
		assert.Len(t, f.c.RevokedTriggerTokenIDs, 1)
		assert.Equal(t, PollerPhaseTriggerRequested, f.state(t).Pending.Phase)
	})
	t.Run("account request not recorded", func(t *testing.T) {
		f := newHandoffFake()
		f.c.failWrite = 1
		_, err := f.run(t)
		require.Error(t, err)
		assert.Zero(t, f.accountCalls)
	})
}

func TestLoadPollerGenerations_Validation(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	st, err := loadPollerGenerations(ctx, fc, handoffOwner, handoffRepo)
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{}, st)

	fc.Errors = map[string]error{"GetRepoVariable": errors.New("boom glpat-secret")}
	_, err = loadPollerGenerations(ctx, fc, handoffOwner, handoffRepo)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-secret")

	for name, raw := range map[string]string{
		"bad json":         `{`,
		"unknown field":    `{"version":1,"surprise":1}`,
		"bad version":      `{"version":2}`,
		"negative current": `{"version":1,"current_user_id":-1}`,
		"unknown phase":    `{"version":1,"pending":{"user_id":5,"phase":"nope"}}`,
		"negative pending": `{"version":1,"pending":{"user_id":-5,"phase":"elevated"}}`,
		"missing user":     `{"version":1,"pending":{"phase":"elevated"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodePollerGenerations(raw)
			require.Error(t, err)
		})
	}
	ok, err := decodePollerGenerations(`{"version":1,"pending":{"phase":"account_requested"}}`)
	require.NoError(t, err)
	assert.Equal(t, PollerPhaseAccountRequested, ok.Pending.Phase)
}

func TestWritePollerGenerations_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"UpdateCIVariable": errors.New("500")}
	require.Error(t, writePollerGenerations(context.Background(), fc, handoffOwner, handoffRepo, PollerGenerationState{}))
}

func TestPollerCutoverTransitions(t *testing.T) {
	verified := PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseVerified, TriggerID: 3}}

	next, err := beginPollerCutover(verified, handoffNewUID)
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{CurrentUserID: handoffNewUID, RetiringUserID: handoffCurrentUID}, next)
	raw, err := json.Marshal(pollerGenerationEnvelope{Version: 1, PollerGenerationState: next})
	require.NoError(t, err)
	assert.JSONEq(t, `{"version":1,"current_user_id":5005,"retiring_user_id":4004}`, string(raw))

	_, err = beginPollerCutover(verified, 1)
	require.Error(t, err, "only the verified pending account")
	_, err = beginPollerCutover(PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseQuarantined}}, handoffNewUID)
	require.Error(t, err, "only a verified generation")
	busy := verified
	busy.RetiringUserID = 1
	_, err = beginPollerCutover(busy, handoffNewUID)
	require.Error(t, err, "at most one old/new pair")

	done, err := finishPollerRetirement(next, handoffCurrentUID)
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{CurrentUserID: handoffNewUID}, done)
	_, err = finishPollerRetirement(next, handoffNewUID)
	require.Error(t, err)
	_, err = finishPollerRetirement(done, 0)
	require.Error(t, err)
}
