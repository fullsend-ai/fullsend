package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	handoffOwner      = webhookTestOwner
	handoffRepo       = webhookTestRepo
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
	inventoryErrs []error // consumed per VerifyReplacementPollerCredentialsRevoked call
	bootstrapErr  error
	bootRevokeErr error
	elevateErr    error
	restoreFails  bool
	mintErr       error
	mintNoToken   bool
	mintBadID     int64 // when nonzero, the created trigger reports this ID
	mintOwner     int64
	probeErr      error
	afterMint     func() // runs after the trigger is created, to change project policy
	onProbe       func() // runs when the protected-branch probe is called

	events         []string
	levelAtCreate  int
	triggerCreates int
	memberSets     map[int64][]int
}

var _ GitLabPollerHandoff = (*handoffFake)(nil)

func newHandoffFake() *handoffFake {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{{ID: 42, Name: handoffRepo, FullName: handoffOwner + "/" + handoffRepo, DefaultBranch: "main"}}
	// The project satisfies every trigger-safety invariant by default.
	seedWebhookFake(fc)
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
	if f.onProbe != nil {
		f.onProbe()
	}
	return f.probeErr
}

func (f *handoffFake) CreateReplacementPollerBootstrap(context.Context, string, string, int64) (*ReplacementPollerBootstrap, error) {
	f.events = append(f.events, "create-bootstrap")
	if f.bootstrapErr != nil {
		return nil, f.bootstrapErr
	}
	return &ReplacementPollerBootstrap{ID: 77, Token: "bootstrap-secret-value"}, nil
}

func (f *handoffFake) CreatePipelineTriggerTokenAsReplacementPoller(ctx context.Context, owner, repo, description string, bootstrap *ReplacementPollerBootstrap) (*forge.PipelineTriggerToken, error) {
	f.events = append(f.events, "create-trigger")
	f.triggerCreates++
	f.levelAtCreate = f.c.ProjectMemberAccess[handoffNewUID]
	if bootstrap == nil || bootstrap.Token == "" {
		return nil, errors.New("no bootstrap credential")
	}
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
	if f.mintBadID != 0 && tok != nil {
		tok.ID = f.mintBadID
	}
	if f.afterMint != nil {
		f.afterMint()
	}
	return tok, err
}

func (f *handoffFake) RevokeReplacementPollerBootstrap(context.Context, string, string, int64) error {
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

func (f *handoffFake) VerifyReplacementPollerCredentialsRevoked(context.Context, string, string, int64) error {
	f.events = append(f.events, "inventory")
	if len(f.inventoryErrs) == 0 {
		return nil
	}
	err := f.inventoryErrs[0]
	f.inventoryErrs = f.inventoryErrs[1:]
	return err
}

// run invokes the handoff under the project lease, as production callers
// must.
func (f *handoffFake) run(t *testing.T) (res PollerHandoffResult, err error) {
	t.Helper()
	ctx := context.Background()
	lease, release, lockErr := LockGitLabProjectLease(ctx, f.c, handoffOwner, handoffRepo)
	require.NoError(t, lockErr)
	defer release(nil)
	return handoffPollerGeneration(ctx, f.c, f, lease, handoffOwner, handoffRepo, &credentialRedactor{})
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
	assert.Equal(t, 1, f.triggerCreates, "exactly one trigger-create request")
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, f.levelAtCreate)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
	// The current Poller is never elevated or otherwise modified.
	assert.Empty(t, f.memberSets[handoffCurrentUID])
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffCurrentUID])
	assert.Empty(t, f.c.RevokedTriggerTokenIDs)

	st := f.state(t)
	assert.Equal(t, handoffCurrentUID, st.CurrentUserID)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: res.Trigger.ID}, *st.Pending)
}

func TestHandoffPollerGeneration_RecordsPhasesBeforeActing(t *testing.T) {
	f := newHandoffFake()
	res, err := f.run(t)
	require.NoError(t, err)
	require.NotNil(t, res.Trigger)

	var phases []PollerGenerationPhase
	for _, rec := range f.c.UpdatedVariables {
		if rec.Name != forge.VarGitLabPollerGenerations {
			continue
		}
		assert.True(t, rec.Protected)
		// The document holds account IDs and phases only, never a credential.
		assert.NotContains(t, rec.Value, "bootstrap-secret-value")
		assert.NotContains(t, rec.Value, res.Trigger.Token)
		st, err := decodePollerGenerations(rec.Value)
		require.NoError(t, err)
		require.NotNil(t, st.Pending)
		phases = append(phases, st.Pending.Phase)
	}
	assert.Equal(t, []PollerGenerationPhase{
		PollerPhaseAccountRequested, PollerPhaseAccountRecorded, PollerPhaseElevated,
		PollerPhaseTriggerRequested, PollerPhaseVerified,
	}, phases)
}

func TestHandoffPollerGeneration_RequiresProjectLease(t *testing.T) {
	f := newHandoffFake()

	ctx := context.Background()
	assertRefused := func(t *testing.T, lease *GitLabProjectLease) {
		t.Helper()
		res, err := handoffPollerGeneration(ctx, f.c, f, lease, handoffOwner, handoffRepo, &credentialRedactor{})
		require.ErrorIs(t, err, errPollerHandoffUnleased)
		assert.Equal(t, PollerHandoffResult{}, res)
		assert.Empty(t, f.events, "no side effects without the lease")
		assert.Zero(t, f.c.writes)
		assert.Empty(t, f.c.UpdatedVariables)
	}

	t.Run("no lease", func(t *testing.T) { assertRefused(t, nil) })

	// A dry-run lock takes only the process-local lock, so it cannot yield a
	// lease capability; an unrelated caller has none either, even while
	// another operation holds the lock.
	t.Run("dry-run lock and unrelated caller", func(t *testing.T) {
		release, err := LockGitLabProject(ctx, f.c, handoffOwner, handoffRepo, true)
		require.NoError(t, err)
		defer release(nil)
		assertRefused(t, nil)
		assertRefused(t, &GitLabProjectLease{owner: handoffOwner, repo: handoffRepo})
	})

	t.Run("released lease", func(t *testing.T) {
		lease, release, err := LockGitLabProjectLease(ctx, f.c, handoffOwner, handoffRepo)
		require.NoError(t, err)
		release(nil)
		assertRefused(t, lease)
	})

	t.Run("lease for another project", func(t *testing.T) {
		lease, release, err := LockGitLabProjectLease(ctx, f.c, handoffOwner, "other-project")
		require.NoError(t, err)
		defer release(nil)
		assertRefused(t, lease)
	})
}

func TestHandoffPollerGeneration_NilRedactor(t *testing.T) {
	f := newHandoffFake()
	ctx := context.Background()
	lease, release, err := LockGitLabProjectLease(ctx, f.c, handoffOwner, handoffRepo)
	require.NoError(t, err)
	defer release(nil)

	res, err := handoffPollerGeneration(ctx, f.c, f, lease, handoffOwner, handoffRepo, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Trigger)
}

func TestHandoffPollerGeneration_AmbiguousCreateQuarantinesWithoutRetry(t *testing.T) {
	f := newHandoffFake()
	f.mintErr = context.DeadlineExceeded

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "quarantined")
	assert.Contains(t, res.DeferReason, "never retried")
	assert.Contains(t, res.DeferReason, "request canceled or timed out", "the failure class is kept")
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

func TestHandoffPollerGeneration_CreateFailureClassIsRecordedWithoutServerText(t *testing.T) {
	f := newHandoffFake()
	f.mintErr = fmt.Errorf("secret-server-text: %w", forge.ErrForbidden)

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "forbidden")
	assert.NotContains(t, res.DeferReason, "secret-server-text")
	st := f.state(t)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)
	assert.Contains(t, st.Pending.Reason, "forbidden")
	assert.NotContains(t, st.Pending.Reason, "secret-server-text")
}

func TestHandoffPollerGeneration_MissingTokenValueNamesTheResponseGap(t *testing.T) {
	f := newHandoffFake()
	f.mintNoToken = true

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "lacked a usable trigger token or ID")
}

func TestHandoffPollerGeneration_QuarantineRecoveryKeepsCurrentPoller(t *testing.T) {
	f := newHandoffFake()
	f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID})
	f.mintErr = context.DeadlineExceeded

	res, err := f.run(t)
	require.NoError(t, err)
	// The recovery step must not tell the operator to wipe the whole document,
	// which would drop the running Poller's recorded ID.
	assert.Contains(t, res.DeferReason, "remove only the \"pending\" generation")
	assert.Contains(t, res.DeferReason, "current_user_id")
	assert.NotContains(t, res.DeferReason, "and clear ")
	assert.NotContains(t, res.DeferReason, "then clear ")
	st := f.state(t)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)

	// Manual recovery removes only the pending generation.
	st.Pending = nil
	f.seed(t, st)
	f.mintErr = nil

	res, err = f.run(t)
	require.NoError(t, err)
	require.Empty(t, res.DeferReason)
	require.NotNil(t, res.Trigger)

	cut, err := beginPollerCutover(f.state(t), res.NewUserID)
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{CurrentUserID: res.NewUserID, RetiringUserID: handoffCurrentUID}, cut,
		"the original current Poller becomes retiring")
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

func TestHandoffPollerGeneration_NegativeTriggerIDIsQuarantined(t *testing.T) {
	f := newHandoffFake()
	f.mintBadID = -5

	res, err := f.run(t)
	require.NoError(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "no confirmed result")
	assert.NotContains(t, f.events, "probe")
	assert.Empty(t, f.c.RevokedTriggerTokenIDs, "a nonpositive ID is never revoked")
	st := f.state(t)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)
	assert.Zero(t, st.Pending.TriggerID)
}

func TestHandoffPollerGeneration_IncompleteCreateWithFailedRevocationNamesExactTrigger(t *testing.T) {
	f := newHandoffFake()
	f.mintNoToken = true
	f.c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("500")}

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	st := f.state(t)
	require.NotNil(t, st.Pending)
	require.Positive(t, st.Pending.TriggerID, "the created trigger's ID survives in the quarantined document")
	want := fmt.Sprintf("delete pipeline trigger token ID %d, whatever its reported owner, and confirm it is gone", st.Pending.TriggerID)
	assert.Contains(t, res.DeferReason, want)

	// A later run reports only the persisted reason.
	res, err = f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "quarantined")
	assert.Contains(t, res.DeferReason, want)
}

func TestHandoffPollerGeneration_FailedDemotionPublishesNothing(t *testing.T) {
	f := newHandoffFake()
	f.restoreFails = true

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "5005")
	assert.Contains(t, res.DeferReason, "administrator")
	assert.NotContains(t, f.events, "probe")
	assert.Len(t, f.c.RevokedTriggerTokenIDs, 1, "the trigger of an unverified generation is revoked")
	st := f.state(t)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)
	assert.Equal(t, handoffNewUID, st.Pending.UserID)
}

func TestHandoffPollerGeneration_FailedDemotionAndRevocationNamesExactTrigger(t *testing.T) {
	// Demotion and revocation both fail, and the trigger reports a different
	// owner: removing the account would not delete it, so the exact ID must be
	// stored.
	f := newHandoffFake()
	f.restoreFails = true
	f.mintOwner = 999
	f.c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("500")}

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	st := f.state(t)
	require.NotNil(t, st.Pending)
	require.Positive(t, st.Pending.TriggerID)
	want := fmt.Sprintf("delete pipeline trigger token ID %d, whatever its reported owner, and confirm it is gone", st.Pending.TriggerID)
	assert.Contains(t, res.DeferReason, want)

	res, err = f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "quarantined")
	assert.Contains(t, res.DeferReason, want)
}

func TestHandoffPollerGeneration_DemotionFailureAfterAmbiguousCreate(t *testing.T) {
	f := newHandoffFake()
	f.restoreFails = true
	f.mintErr = context.DeadlineExceeded

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	assert.Empty(t, f.c.RevokedTriggerTokenIDs, "no trigger ID is known to revoke")
	assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_ActivePATAfterTriggerQuarantines(t *testing.T) {
	f := newHandoffFake()
	// Clean before elevation; a late self-rotation left a PAT afterwards.
	f.inventoryErrs = []error{nil, &PollerGenerationUnsafeError{Reason: "an unmanaged personal access token is active"}}

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

func TestHandoffPollerGeneration_OwnerMismatchWithFailedRevocationNamesTriggerCleanup(t *testing.T) {
	f := newHandoffFake()
	f.mintOwner = 999
	f.c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("500")}

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	assert.NotContains(t, res.DeferReason, "was revoked")
	assert.Contains(t, res.DeferReason, "Delete pipeline trigger token ID")
	assert.Contains(t, res.DeferReason, "confirm it is gone")

	// A later run reports only the persisted reason, which must still name
	// the trigger cleanup.
	res, err = f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "quarantined")
	assert.Contains(t, res.DeferReason, "Delete pipeline trigger token ID")
	assert.Contains(t, res.DeferReason, "confirm it is gone")
}

// unsafeTriggerProjects lists project configurations that violate the
// trigger-safety invariants, each applied by mutating the fake project.
var unsafeTriggerProjects = map[string]struct {
	apply  func(f *handoffFake)
	reason string
}{
	"variable override policy": {
		apply:  func(f *handoffFake) { f.c.PipelineVarOverrideRoles[handoffOwner+"/"+handoffRepo] = "developer" },
		reason: "ci_pipeline_variables_minimum_override_role",
	},
	"other protected branch": {
		apply:  func(f *handoffFake) { f.c.ProtectedBranches[handoffOwner+"/"+handoffRepo+"/release"] = true },
		reason: "protected-branch rules other than the default branch",
	},
	"protected tag": {
		apply:  func(f *handoffFake) { f.c.ProtectedTags[handoffOwner+"/"+handoffRepo+"/v*"] = true },
		reason: "protected-tag rules",
	},
	"custom CI config path": {
		apply:  func(f *handoffFake) { f.c.Repos[0].CIConfigPath = "ci/other.yml" },
		reason: "custom CI configuration path",
	},
}

func TestHandoffPollerGeneration_UnsafeProjectDefersBeforeElevation(t *testing.T) {
	for name, tc := range unsafeTriggerProjects {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFake()
			tc.apply(f)

			res, err := f.run(t)
			require.NoError(t, err)
			assert.Nil(t, res.Trigger)
			assert.Contains(t, res.DeferReason, tc.reason)
			assert.NotContains(t, f.events, "elevate")
			assert.Zero(t, f.triggerCreates)
			// The account stays resumable at Developer.
			assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase)
		})
	}
}

func TestHandoffPollerGeneration_UnverifiableSafetyBlocksHandoff(t *testing.T) {
	f := newHandoffFake()
	f.c.Errors = map[string]error{"ListProtectedTags": errors.New("500")}

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	assert.NotContains(t, f.events, "elevate")
	assert.Zero(t, f.triggerCreates)
}

func TestHandoffPollerGeneration_UnsafeProjectAfterMintRevokesAndQuarantines(t *testing.T) {
	for name, tc := range unsafeTriggerProjects {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFake()
			f.afterMint = func() { tc.apply(f) }

			res, err := f.run(t)
			require.NoError(t, err)
			assert.Nil(t, res.Trigger)
			assert.Contains(t, res.DeferReason, tc.reason)
			assert.Contains(t, res.DeferReason, "Delete pipeline trigger token ID")
			assert.NotContains(t, f.events, "probe", "the probe must not run before the safety checks pass")
			assert.Len(t, f.c.RevokedTriggerTokenIDs, 1)
			assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
		})
	}
}

func TestHandoffPollerGeneration_UnverifiableSafetyAfterMintQuarantines(t *testing.T) {
	f := newHandoffFake()
	f.afterMint = func() { f.c.Errors = map[string]error{"ListProtectedTags": errors.New("500")} }

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "could not be verified")
	assert.Contains(t, res.DeferReason, "Delete pipeline trigger token ID")
	assert.Len(t, f.c.RevokedTriggerTokenIDs, 1)
	assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_VerifiedOnlyAfterFinalChecks(t *testing.T) {
	f := newHandoffFake()
	var phaseAtProbe PollerGenerationPhase
	f.onProbe = func() { phaseAtProbe = f.state(t).Pending.Phase }

	res, err := f.run(t)
	require.NoError(t, err)
	require.NotNil(t, res.Trigger)
	assert.Equal(t, PollerPhaseTriggerRequested, phaseAtProbe, "not verified while the final checks are pending")
	assert.Equal(t, PollerPhaseVerified, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_CutoverRejectsIncompletelyVerified(t *testing.T) {
	t.Run("safety check interrupted", func(t *testing.T) {
		f := newHandoffFake()
		f.afterMint = func() { f.c.Errors = map[string]error{"ListProtectedTags": errors.New("500")} }
		f.c.failWrite = 5 // the quarantine write also fails
		_, err := f.run(t)
		require.Error(t, err)
		st := f.state(t)
		require.NotNil(t, st.Pending)
		assert.Equal(t, PollerPhaseTriggerRequested, st.Pending.Phase)
		_, err = beginPollerCutover(st, handoffNewUID)
		require.Error(t, err)
	})
	t.Run("probe failed", func(t *testing.T) {
		f := newHandoffFake()
		f.probeErr = forge.ErrForbidden
		_, err := f.run(t)
		require.NoError(t, err)
		_, err = beginPollerCutover(f.state(t), handoffNewUID)
		require.Error(t, err)
	})
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

func TestHandoffPollerGeneration_TriggerRevocationFailureIsReported(t *testing.T) {
	f := newHandoffFake()
	f.probeErr = forge.ErrForbidden
	f.c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("500 glpat-secret")}

	res, err := f.run(t)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-secret")
	assert.Contains(t, err.Error(), "revoking pipeline trigger token ID")
	assert.Nil(t, res.Trigger)
	assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
}

func TestHandoffPollerGeneration_ProbeAndRevocationFailureNamesTriggerCleanup(t *testing.T) {
	f := newHandoffFake()
	f.probeErr = forge.ErrForbidden
	f.c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("500")}

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res.Trigger)
	assert.Contains(t, res.DeferReason, "delete pipeline trigger token ID")

	// A later run reports only the persisted reason, which must still name
	// the trigger cleanup.
	res, err = f.run(t)
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "quarantined")
	assert.Contains(t, res.DeferReason, "delete pipeline trigger token ID")
	assert.Contains(t, res.DeferReason, "confirm it is gone")
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

// A document that records a trigger attempt but a resumable phase must never
// reach a second trigger-create request.
func TestAttemptTrigger_QuarantinesWhenAlreadyAttempted(t *testing.T) {
	f := newHandoffFake()
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelDeveloper
	f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}})

	ctx := context.Background()
	st, err := loadPollerGenerations(ctx, f.c, handoffOwner, handoffRepo)
	require.NoError(t, err)
	st.Pending.TriggerAttempted = true
	g := &pollerGenerationRun{ctx: ctx, client: f.c, h: f, owner: handoffOwner, repo: handoffRepo, red: &credentialRedactor{}, st: st}

	res, err := g.attemptTrigger()
	require.NoError(t, err)
	assert.Contains(t, res.DeferReason, "never retried")
	assert.Zero(t, f.triggerCreates)
	assert.Empty(t, f.events)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
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

// Removing pending forgets the account and any trigger its single create
// request produced, so the guidance must require removing both first, whether
// or not the trigger request was attempted.
func TestHandoffPollerGeneration_InterruptedRecoveryFailureRequiresAccountAndTriggerCleanup(t *testing.T) {
	for name, pending := range map[string]*PendingPollerGeneration{
		"elevated":          {UserID: handoffNewUID, Phase: PollerPhaseElevated},
		"trigger requested": {UserID: handoffNewUID, Phase: PollerPhaseTriggerRequested, TriggerAttempted: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFake()
			f.restoreFails = true
			f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelMaintainer
			f.seed(t, PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: pending})

			res, err := f.run(t)
			require.Error(t, err)
			assert.Contains(t, res.DeferReason, "delete any")
			assert.Contains(t, res.DeferReason, GitLabWebhookTriggerDescription)
			assert.Contains(t, res.DeferReason, "remove the account")
			assert.NotContains(t, res.DeferReason, "or remove the account")
			assert.Zero(t, f.triggerCreates)
			assert.Equal(t, PollerPhaseQuarantined, f.state(t).Pending.Phase)
		})
	}
}

func TestHandoffPollerGeneration_InterruptedRecoveryStateWriteFailure(t *testing.T) {
	f := newHandoffFake()
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelMaintainer
	f.seed(t, PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseElevated}})
	f.c.failWrite = 1 // the write returning the generation to account_recorded

	_, err := f.run(t)
	require.Error(t, err)
	assert.Zero(t, f.triggerCreates)
	assert.Equal(t, PollerPhaseElevated, f.state(t).Pending.Phase, "the next run recovers again")
}

func TestHandoffPollerGeneration_Blocked(t *testing.T) {
	for name, tc := range map[string]struct {
		st   PollerGenerationState
		want string
	}{
		"lost account response": {PollerGenerationState{Pending: &PendingPollerGeneration{Phase: PollerPhaseAccountRequested}}, "never recorded"},
		"retiring":              {PollerGenerationState{CurrentUserID: handoffNewUID, RetiringUserID: handoffCurrentUID}, "has not finished retiring"},
		"quarantined":           {PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseQuarantined, Reason: "operator action"}}, "operator action"},
		"verified":              {PollerGenerationState{Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3}}, "cutover did not complete"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFake()
			f.seed(t, tc.st)
			res, err := f.run(t)
			require.NoError(t, err)
			assert.Contains(t, res.DeferReason, tc.want)
			assert.Contains(t, res.DeferReason, "polling schedules remain in effect")
			assert.Empty(t, f.events)
			assert.Zero(t, f.c.writes)
		})
	}
}

func TestHandoffPollerGeneration_LoadFailure(t *testing.T) {
	f := newHandoffFake()
	f.c.Errors = map[string]error{"GetRepoVariable": errors.New("500")}

	_, err := f.run(t)
	require.Error(t, err)
	assert.Empty(t, f.events)
	assert.Zero(t, f.c.writes)
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
	for name, accountErr := range map[string]error{
		"timeout":  context.DeadlineExceeded,
		"no error": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFake()
			f.accountUID = 0
			f.accountErr = accountErr

			_, err := f.run(t)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "reconciled manually")
			assert.Equal(t, PollerPhaseAccountRequested, f.state(t).Pending.Phase)

			f.events = nil
			res, err := f.run(t)
			require.NoError(t, err)
			assert.Contains(t, res.DeferReason, "never recorded")
			assert.Equal(t, 1, f.accountCalls)
		})
	}
}

func TestHandoffPollerGeneration_AccountCreatedWithErrorIsRecorded(t *testing.T) {
	f := newHandoffFake()
	f.accountErr = errors.New("500 adding member")

	_, err := f.run(t)
	require.Error(t, err)
	st := f.state(t)
	assert.Equal(t, PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}, *st.Pending)
	assert.Zero(t, f.triggerCreates)
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
		f.inventoryErrs = []error{&PollerGenerationUnsafeError{Reason: "it holds an active personal access token"}}
		res, err := f.run(t)
		require.NoError(t, err)
		assert.Contains(t, res.DeferReason, "it holds an active personal access token")
		assert.NotContains(t, f.events, "create-bootstrap")
		assert.NotContains(t, f.events, "elevate")
		assert.Equal(t, PollerPhaseAccountRecorded, f.state(t).Pending.Phase, "the unexposed account may be resumed")
	})
	t.Run("inventory request fails", func(t *testing.T) {
		f := newHandoffFake()
		f.inventoryErrs = []error{errors.New("500 glpat-secret")}
		res, err := f.run(t)
		require.NoError(t, err)
		assert.Contains(t, res.DeferReason, "server error text withheld")
		assert.NotContains(t, res.DeferReason, "glpat-secret")
		assert.NotContains(t, f.events, "elevate")
	})
}

func TestHandoffPollerGeneration_BootstrapFailures(t *testing.T) {
	t.Run("create fails", func(t *testing.T) {
		f := newHandoffFake()
		f.bootstrapErr = errors.New("500")
		_, err := f.run(t)
		require.Error(t, err)
		assert.NotContains(t, f.events, "elevate")
		assert.Contains(t, f.events, "revoke-bootstrap")
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
		for _, rec := range f.c.UpdatedVariables {
			assert.NotContains(t, rec.Value, "bootstrap-secret-value")
		}
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
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
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
	t.Run("reverted but not recorded", func(t *testing.T) {
		f := newHandoffFake()
		f.elevateErr = errors.New("500")
		f.c.failWrite = 4 // the write returning the generation to account_recorded
		_, err := f.run(t)
		require.Error(t, err)
		assert.Equal(t, PollerPhaseElevated, f.state(t).Pending.Phase, "the next run recovers the recorded elevation")
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
		assert.Equal(t, PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseAccountRecorded}, *f.state(t).Pending)
	})
	t.Run("trigger attempt not recorded and demotion fails", func(t *testing.T) {
		f := newHandoffFake()
		f.c.failWrite = 4
		f.restoreFails = true
		_, err := f.run(t)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "returning the replacement GitLab Poller")
		assert.Zero(t, f.triggerCreates)
		assert.Equal(t, PollerPhaseElevated, f.state(t).Pending.Phase, "the next run recovers the recorded elevation")
	})
	t.Run("verified not recorded", func(t *testing.T) {
		f := newHandoffFake()
		f.c.failWrite = 5
		res, err := f.run(t)
		require.Error(t, err)
		assert.Nil(t, res.Trigger)
		assert.Len(t, f.c.RevokedTriggerTokenIDs, 1)
		assert.Equal(t, PollerPhaseTriggerRequested, f.state(t).Pending.Phase)

		// The next run never repeats the create request.
		res, err = f.run(t)
		require.NoError(t, err)
		assert.Contains(t, res.DeferReason, "unknown result")
		assert.Equal(t, 1, f.triggerCreates)
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
		"token field":      `{"version":1,"pending":{"user_id":5,"phase":"verified","token":"x"}}`,
		"bad version":      `{"version":2}`,
		"negative current": `{"version":1,"current_user_id":-1}`,
		"unknown phase":    `{"version":1,"pending":{"user_id":5,"phase":"nope"}}`,
		"negative pending": `{"version":1,"pending":{"user_id":-5,"phase":"elevated"}}`,
		"missing user":     `{"version":1,"pending":{"phase":"elevated"}}`,

		"trailing garbage":     `{"version":1}garbage`,
		"second document":      `{"version":1}{"version":1}`,
		"duplicate top key":    `{"version":1,"pending":{"user_id":5,"phase":"quarantined"},"pending":null}`,
		"duplicate nested key": `{"version":1,"pending":{"user_id":5,"phase":"verified","phase":"elevated"}}`,
		"cased top key":        `{"version":1,"pending":{"user_id":5,"phase":"quarantined"},"Pending":null}`,
		"upper top key":        `{"version":1,"pending":{"user_id":5,"phase":"quarantined"},"PENDING":null}`,
		"cased nested key":     `{"version":1,"pending":{"user_id":5,"phase":"verified","Phase":"elevated"}}`,
		"upper nested phase":   `{"version":1,"pending":{"user_id":5,"phase":"quarantined","PHASE":"account_recorded"}}`,
		"upper trigger flag":   `{"version":1,"pending":{"user_id":5,"phase":"quarantined","trigger_attempted":true,"TRIGGER_ATTEMPTED":false}}`,
		"unicode folded key":   `{"version":1,"pending":{"user_id":5,"phase":"quarantined","phaſe":"account_recorded"}}`,
		"current is retiring":  `{"version":1,"current_user_id":5,"retiring_user_id":5}`,
		"pending is current":   `{"version":1,"current_user_id":5,"pending":{"user_id":5,"phase":"account_recorded"}}`,
		"pending is retiring":  `{"version":1,"retiring_user_id":5,"pending":{"user_id":5,"phase":"account_recorded"}}`,
		"recorded attempted":   `{"version":1,"pending":{"user_id":5,"phase":"account_recorded","trigger_attempted":true}}`,
		"requested trigger id": `{"version":1,"pending":{"phase":"account_requested","trigger_id":9}}`,
		"elevated trigger id":  `{"version":1,"pending":{"user_id":5,"phase":"elevated","trigger_id":9}}`,
		"requested unattempt":  `{"version":1,"pending":{"user_id":5,"phase":"trigger_requested"}}`,
		"verified no trigger":  `{"version":1,"pending":{"user_id":5,"phase":"verified","trigger_attempted":true}}`,
		"verified unattempted": `{"version":1,"pending":{"user_id":5,"phase":"verified","trigger_id":9}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodePollerGenerations(raw)
			require.Error(t, err)
		})
	}
	ok, err := decodePollerGenerations(`{"version":1,"pending":{"phase":"account_requested"}}`)
	require.NoError(t, err)
	assert.Equal(t, PollerPhaseAccountRequested, ok.Pending.Phase)
	pair, err := decodePollerGenerations(`{"version":1,"current_user_id":6,"retiring_user_id":5}`)
	require.NoError(t, err)
	assert.Equal(t, int64(6), pair.CurrentUserID)
	assert.Equal(t, int64(5), pair.RetiringUserID)

	fc = forge.NewFakeClient()
	fc.VariableValues = map[string]string{handoffOwner + "/" + handoffRepo + "/" + forge.VarGitLabPollerGenerations: "  "}
	fc.VariablesExist = map[string]bool{handoffOwner + "/" + handoffRepo + "/" + forge.VarGitLabPollerGenerations: true}
	st, err = loadPollerGenerations(ctx, fc, handoffOwner, handoffRepo)
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{}, st, "an empty variable is an empty state")
}

func TestWritePollerGenerations_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"UpdateCIVariable": errors.New("500")}
	require.Error(t, writePollerGenerations(context.Background(), fc, handoffOwner, handoffRepo, PollerGenerationState{}))
}

func TestPollerGenerationSafetyReason(t *testing.T) {
	assert.Equal(t, "an active token", (&PollerGenerationUnsafeError{Reason: "an active token"}).Error())
	assert.Equal(t, "an active token", pollerGenerationSafetyReason(&PollerGenerationUnsafeError{Reason: "an active token"}))
	wrapped := errors.Join(errors.New("context"), &PollerGenerationUnsafeError{Reason: "an active token"})
	assert.Equal(t, "an active token", pollerGenerationSafetyReason(wrapped))
	generic := pollerGenerationSafetyReason(errors.New("500 glpat-secret"))
	assert.NotContains(t, generic, "glpat-secret")
	assert.Contains(t, generic, "server error text withheld")
}

func TestPollerCutoverTransitions(t *testing.T) {
	verified := PollerGenerationState{CurrentUserID: handoffCurrentUID, Pending: &PendingPollerGeneration{UserID: handoffNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3}}

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
