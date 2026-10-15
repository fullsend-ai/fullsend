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

const freshPollerUID = int64(7007)

// publishFailClient fails the trigger-secret store, the webhook creation, or a
// generation-document write that follow the Poller handoff, so a publication
// failure can be followed by an ordinary reinstall.
type publishFailClient struct {
	webhookFake
	failTriggerSecret bool
	// ambiguousTriggerSecret commits the trigger secret and reports an error,
	// as a lost response would.
	ambiguousTriggerSecret bool
	failHookCreate         bool
	// failGenerationWrite fails a generation-document write whose value contains
	// the fragment.
	failGenerationWrite string
	// ignoreRevoke makes a trigger DELETE report success without removing it.
	ignoreRevoke bool
}

func (c *publishFailClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if name == forge.SecretTriggerToken {
		if c.failTriggerSecret {
			return errors.New("500 internal error")
		}
		if c.ambiguousTriggerSecret {
			_ = c.webhookFake.CreateRepoSecret(ctx, owner, repo, name, value)
			return errors.New("502 bad gateway")
		}
	}
	return c.webhookFake.CreateRepoSecret(ctx, owner, repo, name, value)
}

func (c *publishFailClient) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	if c.failHookCreate {
		return nil, errors.New("500 internal error")
	}
	return c.webhookFake.CreateProjectHook(ctx, owner, repo, hook)
}

func (c *publishFailClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if c.failGenerationWrite != "" && name == forge.VarGitLabPollerGenerations && strings.Contains(value, c.failGenerationWrite) {
		return errors.New("500 internal error")
	}
	return c.webhookFake.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

func (c *publishFailClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, id int64) error {
	if c.ignoreRevoke {
		return nil
	}
	return c.webhookFake.RevokePipelineTriggerToken(ctx, owner, repo, id)
}

func ensureWebhookAsPollerOn(c forge.Client, to GitLabTriggerOwner) (GitLabWebhookResult, error) {
	return EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, to, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
}

func newPublishFailClient() (*publishFailClient, *handoffTriggerOwner) {
	c := &publishFailClient{webhookFake: newWebhookFake()}
	ownedBy(c.webhookFake, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	h := newHandoffTriggerOwner(c.webhookFake)
	h.resolveFromRotation = true
	return c, h
}

func maintainerGrants(h *handoffTriggerOwner, uid int64) int {
	n := 0
	for _, level := range h.levelsByUser[uid] {
		if level == forge.GitLabAccessLevelMaintainer {
			n++
		}
	}
	return n
}

// A trigger whose secret store or webhook publication failed may have been
// distributed, so its account is never raised again. An ordinary reinstall
// revokes the trigger, confirms it is gone, abandons the account at Developer,
// and finishes with a genuinely fresh account while polling stays in service.
func TestEnsureGitLabWebhookFastPath_HandoffPublicationFailureThenReinstall(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(c *publishFailClient, on bool)
	}{
		{"trigger secret store", func(c *publishFailClient, on bool) { c.failTriggerSecret = on }},
		{"ambiguous trigger secret write", func(c *publishFailClient, on bool) { c.ambiguousTriggerSecret = on }},
		{"webhook creation", func(c *publishFailClient, on bool) { c.failHookCreate = on }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, h := newPublishFailClient()

			tc.fail(c, true)
			_, err := ensureWebhookAsPollerOn(c, h)
			require.Error(t, err)

			// Nothing was cut over: the current Poller keeps its credential and
			// the generation is durably marked as possibly distributed.
			assert.Empty(t, h.publishedFor, "no runtime credential is issued before publication succeeds")
			assert.Equal(t, "glpat-old-runtime-token", c.variable(forge.SecretGitLabPollerToken))
			st := generationState(t, c)
			assert.Equal(t, int64(webhookPollerUserID), st.CurrentUserID)
			assert.Zero(t, st.RetiringUserID)
			require.NotNil(t, st.Pending)
			assert.Equal(t, PollerPhaseVerified, st.Pending.Phase)
			assert.True(t, st.Pending.TriggerDistributed)
			assert.Empty(t, c.hooks(), "no webhook references the unwired trigger")

			// An ordinary reinstall never raises that account again.
			tc.fail(c, false)
			h.accountUID = freshPollerUID
			res, err := ensureWebhookAsPollerOn(c, h)
			require.NoError(t, err)
			assert.Equal(t, "update", res.Action)
			assert.Equal(t, 2, h.accounts, "a fresh account replaces the abandoned one")
			assert.Equal(t, 1, maintainerGrants(h, cutoverNewUID), "the abandoned account is raised exactly once")
			assert.Equal(t, []int64{freshPollerUID}, h.publishedFor)
			assert.Equal(t, PollerGenerationState{CurrentUserID: freshPollerUID, RetiringUserID: webhookPollerUserID, AbandonedUserIDs: []int64{cutoverNewUID}}, generationState(t, c))
			require.Len(t, c.triggers(), 1)
			assert.Equal(t, freshPollerUID, c.triggers()[0].OwnerID)
			require.Len(t, c.hooks(), 1)
			assert.Equal(t, "glpat-new-runtime-token", c.variable(forge.SecretGitLabPollerToken))
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[cutoverNewUID])
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[freshPollerUID])
			ids, err := ManagedGitLabPollerGenerationAccountIDs(context.Background(), c, webhookTestOwner, webhookTestRepo)
			require.NoError(t, err)
			assert.ElementsMatch(t, []int{int(freshPollerUID), webhookPollerUserID, int(cutoverNewUID)}, ids, "the abandoned account stays owned for uninstall")
		})
	}
}

// A DELETE that reports success while the trigger stays listed must not let
// recovery abandon, reset, or create anything.
func TestEnsureGitLabWebhookFastPath_HandoffRecoveryRequiresTriggerAbsent(t *testing.T) {
	c, h := newPublishFailClient()
	c.failHookCreate = true
	_, err := ensureWebhookAsPollerOn(c, h)
	require.Error(t, err)

	c.failHookCreate = false
	c.ignoreRevoke = true
	h.accountUID = freshPollerUID
	_, err = ensureWebhookAsPollerOn(c, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still listed after revocation")
	assert.Equal(t, 1, h.accounts, "no further account is created")
	assert.Equal(t, 1, maintainerGrants(h, cutoverNewUID), "the account is not raised again")
	st := generationState(t, c)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseVerified, st.Pending.Phase)
	assert.Empty(t, st.AbandonedUserIDs)
	assert.Empty(t, h.publishedFor)

	// Once the trigger is really gone the reinstall converges.
	c.ignoreRevoke = false
	_, err = ensureWebhookAsPollerOn(c, h)
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{CurrentUserID: freshPollerUID, RetiringUserID: webhookPollerUserID, AbandonedUserIDs: []int64{cutoverNewUID}}, generationState(t, c))
}

// When the possible-distribution record cannot be written the secret is never
// stored, so the trigger was not distributed; it is revoked and an ordinary
// reinstall may safely reuse the same account.
func TestEnsureGitLabWebhookFastPath_HandoffMarkerWriteFailureStoresNothing(t *testing.T) {
	c, h := newPublishFailClient()
	c.failGenerationWrite = `"trigger_distributed":true`

	_, err := ensureWebhookAsPollerOn(c, h)
	require.Error(t, err)
	assert.Empty(t, c.variable(forge.SecretTriggerToken), "the trigger secret is not written without the record")
	assert.Empty(t, c.triggers(), "the unpublished trigger is revoked")
	st := generationState(t, c)
	require.NotNil(t, st.Pending)
	assert.False(t, st.Pending.TriggerDistributed)

	c.failGenerationWrite = ""
	_, err = ensureWebhookAsPollerOn(c, h)
	require.NoError(t, err)
	assert.Equal(t, 1, h.accounts, "an undistributed generation resumes on the same account")
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, generationState(t, c))
}

// A failed final generation-state write leaves the webhook and the replacement
// credential live; the probe reports work and an ordinary reinstall finalizes
// the cutover without minting anything.
func TestEnsureGitLabWebhookFastPath_InterruptedFinalCutoverRecoversOnReinstall(t *testing.T) {
	c, h := newPublishFailClient()
	c.failGenerationWrite = `"current_user_id":5005`

	_, err := ensureWebhookAsPollerOn(c, h)
	require.Error(t, err)
	st := generationState(t, c)
	assert.Equal(t, int64(webhookPollerUserID), st.CurrentUserID)
	require.NotNil(t, st.Pending)
	assert.Equal(t, PollerPhaseVerified, st.Pending.Phase)
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, "glpat-new-runtime-token", c.variable(forge.SecretGitLabPollerToken))

	c.failGenerationWrite = ""
	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs, "unfinished generation work needs provisioning")

	res, err := ensureWebhookAsPollerOn(c, h)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Equal(t, 1, h.accounts, "no further account is created")
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, generationState(t, c))

	needs, err = GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.False(t, needs)
}

// A cutover contained before the runtime credential was stored leaves the
// webhook live on the verified generation's trigger; the reinstall retries the
// cutover instead of reporting the fast path provisioned.
func TestEnsureGitLabWebhookFastPath_ContainedCutoverIsRetriedOnReinstall(t *testing.T) {
	c, h := newPublishFailClient()
	h.runtimeCreateErr = errors.New("500 internal error")

	_, err := ensureWebhookAsPollerOn(c, h)
	require.Error(t, err)
	st := generationState(t, c)
	require.NotNil(t, st.Pending)
	require.Len(t, c.hooks(), 1)

	h.runtimeCreateErr = nil
	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs)

	_, err = ensureWebhookAsPollerOn(c, h)
	require.NoError(t, err)
	assert.Equal(t, 1, h.accounts)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, generationState(t, c))
	assert.Equal(t, "glpat-new-runtime-token", c.variable(forge.SecretGitLabPollerToken))
}

func TestCutOverPollerGeneration_KeepsCreatedTokenProvenance(t *testing.T) {
	c := newWebhookFake()
	h := newHandoffTriggerOwner(c)
	const legacyTokenID = 77
	require.NoError(t, writeRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo, rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RolePoller): {
			ManagedUserID:   webhookPollerUserID,
			CreatedTokenIDs: []int{legacyTokenID},
			OutgoingIDs:     []int{legacyTokenID},
		},
	}}))
	require.NoError(t, writePollerGenerations(context.Background(), c, webhookTestOwner, webhookTestRepo, PollerGenerationState{
		CurrentUserID: webhookPollerUserID,
		Pending:       &PendingPollerGeneration{UserID: cutoverNewUID, Phase: PollerPhaseVerified, TriggerAttempted: true, TriggerID: 3},
	}))

	_, err := cutOverPollerGeneration(context.Background(), c, h, webhookTestOwner, webhookTestRepo, webhookPollerUserID, cutoverNewUID, &credentialRedactor{})
	require.NoError(t, err)
	rs := pollerRotationState(t, c)
	assert.Equal(t, []int{legacyTokenID}, rs.OutgoingIDs)
	assert.ElementsMatch(t, []int{legacyTokenID, rs.IncomingID}, rs.CreatedTokenIDs, "the outgoing legacy token keeps its creation provenance so grace cleanup can revoke it")
	managed, err := ManagedGitLabLegacyTokenIDs(context.Background(), c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Contains(t, managed, legacyTokenID)
}
