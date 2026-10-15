package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

const cutoverNewUID = int64(5005)

// handoffTriggerOwner is a trigger owner that also implements
// GitLabPollerHandoff, as the live GitLab adapter does. The replacement
// account it creates is cutoverNewUID; the current Poller is the embedded
// fakeTriggerOwner's account.
type handoffTriggerOwner struct {
	*fakeTriggerOwner

	accountUID int64
	accountErr error
	probeErr   error
	// resolveFromRotation makes PollerUserID follow the managed Poller recorded
	// in rotation state, as the live adapter does.
	resolveFromRotation bool

	accounts      int
	levelsByUser  map[int64][]int
	publishedFor  []int64
	levelAtIssue  int
	triggerOwners []int64
}

var _ GitLabPollerHandoff = (*handoffTriggerOwner)(nil)

func newHandoffTriggerOwner(c webhookFake) *handoffTriggerOwner {
	return &handoffTriggerOwner{fakeTriggerOwner: newPollerOwner(c), accountUID: cutoverNewUID, levelsByUser: map[int64][]int{}}
}

// PollerUserID resolves the managed Poller from rotation state, as the live
// adapter does, so a recorded cutover changes which account is the Poller.
func (h *handoffTriggerOwner) PollerUserID(ctx context.Context, owner, repo string) (int64, error) {
	if h.resolveFromRotation {
		if id, err := ManagedGitLabRoleAccountID(ctx, h.c, owner, repo, gitlabroles.RolePoller); err == nil && id > 0 {
			return int64(id), nil
		}
	}
	return h.fakeTriggerOwner.PollerUserID(ctx, owner, repo)
}

func (h *handoffTriggerOwner) CreateReplacementPoller(context.Context, string, string) (int64, error) {
	h.accounts++
	if h.accountUID > 0 {
		h.c.ProjectMemberAccess[h.accountUID] = forge.GitLabAccessLevelDeveloper
	}
	return h.accountUID, h.accountErr
}

func (h *handoffTriggerOwner) EnsureReplacementPollerAccess(context.Context, string, string, int64) error {
	return nil
}

func (h *handoffTriggerOwner) VerifyReplacementPollerCredentialsRevoked(context.Context, string, string, int64) error {
	return nil
}

func (h *handoffTriggerOwner) CreateReplacementPollerBootstrap(context.Context, string, string, int64) (*ReplacementPollerBootstrap, error) {
	return &ReplacementPollerBootstrap{ID: 88, Token: fakeBootstrapToken}, nil
}

func (h *handoffTriggerOwner) RevokeReplacementPollerBootstrap(context.Context, string, string, int64) error {
	return nil
}

func (h *handoffTriggerOwner) SetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, level int) error {
	h.levelsByUser[userID] = append(h.levelsByUser[userID], level)
	return h.fakeTriggerOwner.SetProjectMemberAccessLevel(ctx, owner, repo, userID, level)
}

func (h *handoffTriggerOwner) CreatePipelineTriggerTokenAsReplacementPoller(ctx context.Context, owner, repo, description string, bootstrap *ReplacementPollerBootstrap) (*forge.PipelineTriggerToken, error) {
	if bootstrap == nil || bootstrap.Token == "" {
		return nil, errors.New("no bootstrap credential")
	}
	prev := h.c.TriggerTokenOwnerID
	h.c.TriggerTokenOwnerID = h.accountUID
	defer func() { h.c.TriggerTokenOwnerID = prev }()
	return h.c.CreatePipelineTriggerToken(ctx, owner, repo, description)
}

func (h *handoffTriggerOwner) VerifyTriggerStartsProtectedPipeline(_ context.Context, _, _ string, trigger *forge.PipelineTriggerToken) error {
	h.triggerOwners = append(h.triggerOwners, trigger.OwnerID)
	return h.probeErr
}

func (h *handoffTriggerOwner) CreatePollerRuntimeToken(ctx context.Context, owner, repo string, userID int64, expiresAt string) (*PollerRuntimeToken, error) {
	h.publishedFor = append(h.publishedFor, userID)
	h.levelAtIssue = h.c.ProjectMemberAccess[userID]
	return h.fakeTriggerOwner.CreatePollerRuntimeToken(ctx, owner, repo, userID, expiresAt)
}

func generationState(t *testing.T, c forge.Client) PollerGenerationState {
	t.Helper()
	st, err := loadPollerGenerations(context.Background(), c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	return st
}

func pollerRotationState(t *testing.T, c forge.Client) rotationRoleState {
	t.Helper()
	state, _, err := loadRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	return state.Roles[string(gitlabroles.RolePoller)]
}

func TestEnsureGitLabWebhookFastPath_HandoffFreshPollerOwnsTrigger(t *testing.T) {
	c := newWebhookFake()
	// A trigger minted by the Maintainer admin identity would be rejected, so
	// the fast path depends on the fresh Poller generation owning it.
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)

	res, err := ensureWebhookAsPoller(c, h, false)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)

	// The current Poller is never raised: only the fresh account is.
	assert.NotContains(t, h.levelsByUser[webhookPollerUserID], forge.GitLabAccessLevelMaintainer)
	assert.Contains(t, h.levelsByUser[cutoverNewUID], forge.GitLabAccessLevelMaintainer)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[cutoverNewUID])
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.Equal(t, []int64{cutoverNewUID}, h.triggerOwners, "the protected-pipeline probe runs on the fresh identity's trigger")

	// The runtime credential is issued only to the fresh identity, at
	// Developer, after verification.
	assert.Equal(t, []int64{cutoverNewUID}, h.publishedFor)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, h.levelAtIssue)
	assert.Equal(t, "glpat-new-runtime-token", c.variable(forge.SecretGitLabPollerToken))

	require.Len(t, c.triggers(), 1)
	assert.Equal(t, cutoverNewUID, c.triggers()[0].OwnerID)
	require.Len(t, c.hooks(), 1, "the webhook fast path is enabled")
	assert.Equal(t, c.CreatedTriggerTokens[0].Token, c.variable(forge.SecretTriggerToken))

	rs := pollerRotationState(t, c)
	assert.Equal(t, int(cutoverNewUID), rs.ManagedUserID)
	assert.Equal(t, 9002, rs.IncomingID)
	assert.Equal(t, []int{9002}, rs.CreatedTokenIDs)
	assert.Equal(t, rotationPhaseIdle, rs.Phase)

	st := generationState(t, c)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, st)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Cut the GitLab Poller over to user ID 5005")
	assertNoCredentialLeak(t, c, res)

	// A reinstall reuses the provisioned fast path without a new generation.
	h.uid = cutoverNewUID
	again, err := ensureWebhookAsPoller(c, h, false)
	require.NoError(t, err)
	assert.Equal(t, "none", again.Action)
	assert.Equal(t, 1, h.accounts)
}

func TestEnsureGitLabWebhookFastPath_HandoffRotationWaitsForRetirement(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)
	_, err := ensureWebhookAsPoller(c, h, false)
	require.NoError(t, err)

	// The superseded Poller has not been retired, so a forced rotation starts
	// no second generation and preserves the compliant fast path.
	h.uid = cutoverNewUID
	res, err := ensureWebhookAsPoller(c, h, true)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "has not finished retiring")
	assert.Equal(t, 1, h.accounts)
	require.Len(t, c.hooks(), 1)
	require.Len(t, c.triggers(), 1)
}

func TestEnsureGitLabWebhookFastPath_HandoffProbeFailureKeepsPolling(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)
	h.probeErr = errors.New("403 forbidden")

	res, err := ensureWebhookAsPoller(c, h, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "quarantined")
	assert.Empty(t, h.publishedFor, "no runtime credential is published for an unverified generation")
	assert.Equal(t, "glpat-old-runtime-token", c.variable(forge.SecretGitLabPollerToken), "the current Poller keeps polling")
	assert.Empty(t, c.hooks())
	assert.Empty(t, c.triggers())
	st := generationState(t, c)
	assert.Equal(t, int64(webhookPollerUserID), st.CurrentUserID)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseQuarantined, st.Pending.Phase)
	assert.Zero(t, pollerRotationState(t, c).ManagedUserID)
}

func TestEnsureGitLabWebhookFastPath_HandoffUnsupportedServiceAccountsDefers(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)
	h.accountUID = 0
	h.accountErr = forge.ErrNotFound

	res, err := ensureWebhookAsPoller(c, h, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "does not support project service accounts")
	assert.Empty(t, h.publishedFor)
	assert.Empty(t, c.hooks())
	assert.Nil(t, generationState(t, c).Pending)
}

func TestEnsureGitLabWebhookFastPath_HandoffRecordedCurrentMismatchDefers(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)
	require.NoError(t, writePollerGenerations(context.Background(), c, webhookTestOwner, webhookTestRepo, PollerGenerationState{CurrentUserID: 777}))

	res, err := ensureWebhookAsPoller(c, h, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "records GitLab Poller generation user ID 777 as current")
	assert.Zero(t, h.accounts)
	assert.Empty(t, c.hooks())
}

func TestEnsureGitLabWebhookFastPath_HandoffBlankGenerationStateFailsClosed(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)
	c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.VarGitLabPollerGenerations] = " "

	_, err := ensureWebhookAsPoller(c, h, false)
	require.ErrorIs(t, err, errPollerGenerationsBlank)
	assert.Zero(t, h.accounts)
	assert.Empty(t, c.hooks())
	assert.Equal(t, "glpat-old-runtime-token", c.variable(forge.SecretGitLabPollerToken))
}

func TestEnsureGitLabWebhookFastPath_HandoffUnmanagedPollerMintsAsAdmin(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)
	h.uidErr = forge.ErrNotFound

	res, err := ensureWebhookAsPoller(c, h, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action, "an admin-minted trigger is rejected by the owner check")
	assert.Zero(t, h.accounts)
	_, exists := c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.VarGitLabPollerGenerations]
	assert.False(t, exists, "no generation state is written for an unmanaged Poller")
}

func TestEnsureGitLabWebhookFastPath_HandoffDryRunChangesNothing(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c)

	res, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, h, webhookTestBase, webhookTestOwner, webhookTestRepo, false, true)
	require.NoError(t, err)
	assert.Equal(t, "would-update", res.Action)
	assert.Zero(t, h.accounts)
	assert.Empty(t, c.hooks())
}

func TestHandoffPollerOwnedTrigger_RequiresLease(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	_, err := handoffPollerOwnedTrigger(context.Background(), c, h, h, nil, webhookTestOwner, webhookTestRepo, &credentialRedactor{})
	require.ErrorIs(t, err, errPollerHandoffUnleased)
	assert.Zero(t, h.accounts)
}

func TestHandoffPollerOwnedTrigger_LookupError(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	h.uidErr = errors.New("500 glpat-secret")
	pm, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-secret")
	assert.Nil(t, pm.minted)
}

// runHandoffPollerOwnedTrigger runs the handoff under the project lease and
// revokes nothing: callers inspect what was minted.
func runHandoffPollerOwnedTrigger(t *testing.T, c forge.Client, h *handoffTriggerOwner) (pollerMint, error) {
	t.Helper()
	ctx := context.Background()
	lease, release, err := LockGitLabProjectLease(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	defer release(nil)
	return handoffPollerOwnedTrigger(ctx, c, h, h, lease, webhookTestOwner, webhookTestRepo, &credentialRedactor{})
}

// cutoverFailClient fails selected writes so each cutover step can fail.
type cutoverFailClient struct {
	webhookFake
	failSecret     bool
	commitThenFail bool // the secret write takes effect, then reports an error
	failGenWriteAt int  // 1-based generation-state write to fail
	genWrites      int
	failRotation   bool
	// failRotationAt fails the 1-based rotation-state write counted from the
	// first write of the cutover (zero disables it).
	failRotationAt int
	rotationWrites int
}

func (c *cutoverFailClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if c.failSecret && name == forge.SecretGitLabPollerToken {
		return errors.New("500 internal error " + value)
	}
	if c.commitThenFail && name == forge.SecretGitLabPollerToken {
		if err := c.webhookFake.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
			return err
		}
		return errors.New("502 bad gateway, response lost " + value)
	}
	return c.webhookFake.CreateRepoSecret(ctx, owner, repo, name, value)
}

func (c *cutoverFailClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	switch name {
	case forge.VarGitLabPollerGenerations:
		c.genWrites++
		if c.failGenWriteAt != 0 && c.genWrites == c.failGenWriteAt {
			return errors.New("500 internal error")
		}
	case forge.VarGitLabRoleRotation:
		c.rotationWrites++
		if c.failRotation || (c.failRotationAt != 0 && c.rotationWrites == c.failRotationAt) {
			return errors.New("500 internal error")
		}
	}
	return c.webhookFake.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

func TestHandoffPollerOwnedTrigger_RuntimeCredentialFailureContainsReplacement(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	h.runtimeCreateErr = errors.New("500 internal error")

	pm, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	require.NotNil(t, pm.minted, "the trigger is returned so the caller revokes it")
	assert.Equal(t, []int64{cutoverNewUID}, h.contained)
	assert.Equal(t, "glpat-old-runtime-token", c.variable(forge.SecretGitLabPollerToken))
	assert.Zero(t, pollerRotationState(t, c).ManagedUserID)
	st := generationState(t, c)
	assert.Equal(t, int64(webhookPollerUserID), st.CurrentUserID)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseVerified, st.Pending.Phase, "the verified generation blocks further generations until reconciled")
}

func TestHandoffPollerOwnedTrigger_UnmaskableRuntimeCredentialIsContained(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	h.runtimeToken = "short"

	_, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be masked")
	assert.Equal(t, []int64{cutoverNewUID}, h.contained)
	assert.Equal(t, "glpat-old-runtime-token", c.variable(forge.SecretGitLabPollerToken))
}

func TestCutOverPollerGeneration_EmptyRuntimeCredentialIsContained(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	h.containErr = errors.New("500 glpat-secret")

	_, err := cutOverPollerGeneration(context.Background(), c, emptyRuntimeOwner{h}, webhookTestOwner, webhookTestRepo, webhookPollerUserID, cutoverNewUID, &credentialRedactor{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitLab returned no token value or ID")
	assert.Contains(t, err.Error(), "revoking the credentials of the replacement GitLab Poller (user ID 5005)")
	assert.NotContains(t, err.Error(), "glpat-secret")
	assert.Equal(t, []int64{cutoverNewUID}, h.contained)
	assert.Equal(t, "glpat-old-runtime-token", c.variable(forge.SecretGitLabPollerToken))
}

func TestCutOverPollerGeneration_RotationStateUnreadable(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.VarGitLabRoleRotation] = "{"

	_, err := cutOverPollerGeneration(context.Background(), c, h, webhookTestOwner, webhookTestRepo, webhookPollerUserID, cutoverNewUID, &credentialRedactor{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading rotation state before the GitLab Poller cutover")
	assert.Equal(t, []int64{cutoverNewUID}, h.contained)
}

func TestCutOverPollerGeneration_KeepsOutstandingObligations(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	require.NoError(t, writeRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo, rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RolePoller):  {ManagedUserID: webhookPollerUserID, OutgoingIDs: []int{21, 21, 9002}, ExcludedUserIDs: []int{31}},
		string(gitlabroles.RoleAnalyst): {ManagedUserID: 6006},
	}}))
	require.NoError(t, writePollerGenerations(context.Background(), c, webhookTestOwner, webhookTestRepo, PollerGenerationState{
		CurrentUserID: webhookPollerUserID,
		Pending:       &PendingPollerGeneration{UserID: cutoverNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3},
	}))

	_, err := cutOverPollerGeneration(context.Background(), c, h, webhookTestOwner, webhookTestRepo, webhookPollerUserID, cutoverNewUID, &credentialRedactor{})
	require.NoError(t, err)
	rs := pollerRotationState(t, c)
	assert.Equal(t, []int{21}, rs.OutgoingIDs)
	assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
	assert.Equal(t, []int{31}, rs.ExcludedUserIDs)
	state, _, err := loadRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Equal(t, 6006, state.Roles[string(gitlabroles.RoleAnalyst)].ManagedUserID, "other roles are untouched")
}

// emptyRuntimeOwner returns a runtime credential without a value.
type emptyRuntimeOwner struct{ *handoffTriggerOwner }

func (emptyRuntimeOwner) CreatePollerRuntimeToken(context.Context, string, string, int64, string) (*PollerRuntimeToken, error) {
	return &PollerRuntimeToken{ID: 9002}, nil
}

func TestHandoffPollerOwnedTrigger_SecretStoreFailureKeepsReplacementForRecovery(t *testing.T) {
	// A store error is an unknown outcome even when the write never took
	// effect: the replacement is kept and reconciled, never revoked.
	c := &cutoverFailClient{webhookFake: newWebhookFake(), failSecret: true}
	h := newHandoffTriggerOwner(c.webhookFake)
	require.NoError(t, writeRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo, rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RolePoller): {ManagedUserID: webhookPollerUserID, IncomingID: 11, CreatedTokenIDs: []int{11}, Phase: rotationPhaseIdle},
	}}))

	pm, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-new-runtime-token")
	require.NotNil(t, pm.minted, "the trigger is returned so the caller revokes it")
	assert.Empty(t, h.contained, "the replacement credential is not revoked on an ambiguous write")
	rs := pollerRotationState(t, c)
	assert.Equal(t, int(cutoverNewUID), rs.ManagedUserID)
	assert.Equal(t, rotationPhaseDistributing, rs.Phase)
	assert.NotZero(t, rs.IncomingID)
	assert.Contains(t, rs.CreatedTokenIDs, rs.IncomingID)
}

func TestHandoffPollerOwnedTrigger_SecretCommitThenErrorKeepsPublishedCredential(t *testing.T) {
	c := &cutoverFailClient{webhookFake: newWebhookFake(), commitThenFail: true}
	h := newHandoffTriggerOwner(c.webhookFake)

	pm, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-new-runtime-token")
	require.NotNil(t, pm.minted)
	assert.Empty(t, h.contained, "the installed credential belongs to the replacement and is not deleted or revoked")
	assert.Equal(t, "glpat-new-runtime-token", c.variable(forge.SecretGitLabPollerToken))
	rs := pollerRotationState(t, c)
	assert.Equal(t, int(cutoverNewUID), rs.ManagedUserID)
	assert.Equal(t, rotationPhaseDistributing, rs.Phase, "the next run's rotation recovery reconciles the publication")
	st := generationState(t, c)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseVerified, st.Pending.Phase)

	// The next run sees the replacement as the managed Poller and records the
	// cutover that the failed run did not reach.
	h.uid = cutoverNewUID
	pm, err = runHandoffPollerOwnedTrigger(t, c, h)
	require.NoError(t, err)
	assert.Nil(t, pm.minted)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, generationState(t, c))
}

func TestHandoffPollerOwnedTrigger_FailedPublicationThenFailedMarkerStaysDistributing(t *testing.T) {
	// The distributing record is written before the secret write, so a failed
	// publication followed by a failed error-marker write cannot leave a record
	// that claims the replacement was distributed.
	c := &cutoverFailClient{webhookFake: newWebhookFake(), failSecret: true, failRotationAt: 2}
	h := newHandoffTriggerOwner(c.webhookFake)

	_, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Empty(t, h.contained, "the replacement credential is kept on an ambiguous write")
	assert.Equal(t, 2, c.rotationWrites, "the distributing record and the best-effort error marker")
	rs := pollerRotationState(t, c)
	assert.Equal(t, int(cutoverNewUID), rs.ManagedUserID)
	assert.Equal(t, rotationPhaseDistributing, rs.Phase)
	assert.NotZero(t, rs.IncomingID)
	assert.Empty(t, rs.DistributedAt, "distribution is not recorded before the secret write succeeds")
}

func TestHandoffPollerOwnedTrigger_FailedDistributionRecordStaysDistributing(t *testing.T) {
	// The secret write succeeded but recording the distribution failed: the
	// cutover still completes, and the role stays distributing so the next
	// run's rotation recovery reconciles it.
	c := &cutoverFailClient{webhookFake: newWebhookFake(), failRotationAt: 2}
	h := newHandoffTriggerOwner(c.webhookFake)

	pm, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.NoError(t, err)
	require.NotNil(t, pm.minted)
	assert.Contains(t, strings.Join(pm.details, "\n"), "stays marked distributing")
	assert.Equal(t, "glpat-new-runtime-token", c.variable(forge.SecretGitLabPollerToken))
	rs := pollerRotationState(t, c)
	assert.Equal(t, rotationPhaseDistributing, rs.Phase)
	assert.Empty(t, rs.DistributedAt)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, generationState(t, c))
}

func TestHandoffPollerOwnedTrigger_RotationStateFailureContainsReplacement(t *testing.T) {
	c := &cutoverFailClient{webhookFake: newWebhookFake(), failRotation: true}
	h := newHandoffTriggerOwner(c.webhookFake)

	_, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recording the replacement GitLab Poller (user ID 5005) in rotation state")
	assert.Equal(t, []int64{cutoverNewUID}, h.contained)
	assert.Equal(t, "glpat-old-runtime-token", c.variable(forge.SecretGitLabPollerToken))
}

func TestHandoffPollerOwnedTrigger_RotationStateNamesAnotherPollerRefuses(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	require.NoError(t, writeRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo, rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RolePoller): {ManagedUserID: 999},
	}}))

	_, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "records managed Poller user ID 999")
	assert.Equal(t, []int64{cutoverNewUID}, h.contained)
	assert.Equal(t, 999, pollerRotationState(t, c).ManagedUserID)
}

func TestHandoffPollerOwnedTrigger_InterruptedCutoverIsRecordedOnNextRun(t *testing.T) {
	// Writes: record current, account requested, account recorded, elevated,
	// trigger requested, verified, then the cutover record.
	c := &cutoverFailClient{webhookFake: newWebhookFake(), failGenWriteAt: 7}
	h := newHandoffTriggerOwner(c.webhookFake)

	pm, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recording the GitLab Poller cutover to user ID 5005")
	require.NotNil(t, pm.minted)
	assert.Equal(t, "glpat-new-runtime-token", c.variable(forge.SecretGitLabPollerToken), "the credential was already published")
	st := generationState(t, c)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseVerified, st.Pending.Phase)

	// The next run sees the published generation as the managed Poller and
	// records the cutover instead of asking for the account to be removed.
	h.uid = cutoverNewUID
	pm, err = runHandoffPollerOwnedTrigger(t, c, h)
	require.NoError(t, err)
	assert.Nil(t, pm.minted)
	assert.Contains(t, pm.deferReason, "has not finished retiring")
	assert.Contains(t, strings.Join(pm.details, "\n"), "Recorded the interrupted cutover")
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, generationState(t, c))
}

func TestHandoffPollerOwnedTrigger_InterruptedCutoverWriteFailure(t *testing.T) {
	c := &cutoverFailClient{webhookFake: newWebhookFake()}
	h := newHandoffTriggerOwner(c.webhookFake)
	h.uid = cutoverNewUID
	require.NoError(t, writePollerGenerations(context.Background(), c.webhookFake, webhookTestOwner, webhookTestRepo, PollerGenerationState{
		CurrentUserID: webhookPollerUserID,
		Pending:       &PendingPollerGeneration{UserID: cutoverNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3},
	}))
	c.failGenWriteAt = 1

	_, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recording the interrupted GitLab Poller cutover")
}

func TestHandoffPollerOwnedTrigger_RecordCurrentWriteFailure(t *testing.T) {
	c := &cutoverFailClient{webhookFake: newWebhookFake(), failGenWriteAt: 1}
	h := newHandoffTriggerOwner(c.webhookFake)

	_, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Zero(t, h.accounts, "no account is created before the current Poller is recorded")
}

func TestHandoffPollerOwnedTrigger_HandoffErrorKeepsDeferral(t *testing.T) {
	// The quarantine write after a failed probe fails too: the error is
	// returned and the deferral is kept in the details.
	c := &cutoverFailClient{webhookFake: newWebhookFake(), failGenWriteAt: 6}
	h := newHandoffTriggerOwner(c.webhookFake)
	h.probeErr = errors.New("403 forbidden")

	pm, err := runHandoffPollerOwnedTrigger(t, c, h)
	require.Error(t, err)
	assert.Empty(t, pm.deferReason)
	assert.Contains(t, strings.Join(pm.details, "\n"), "quarantined")
	assert.Nil(t, pm.minted)
}

func TestPollerCurrentMismatch(t *testing.T) {
	assert.Empty(t, pollerCurrentMismatch(PollerGenerationState{CurrentUserID: 5}, 5))
	assert.Empty(t, pollerCurrentMismatch(PollerGenerationState{}, 5))
	assert.Contains(t, pollerCurrentMismatch(PollerGenerationState{RetiringUserID: 5}, 5), "has not finished retiring")
	assert.Contains(t, pollerCurrentMismatch(PollerGenerationState{Pending: &PendingPollerGeneration{UserID: 5, Phase: PollerPhaseAccountRecorded}}, 5), "records GitLab Poller generation user ID 0 as current")
	assert.Contains(t, pollerCurrentMismatch(PollerGenerationState{CurrentUserID: 6}, 5), "user ID 6 as current, but the managed Poller is user ID 5")
}
