package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// A Poller identity lookup failure must not skip independent trigger-safety
// reconciliation: an unsafe existing fast path is still revoked by the
// administrative client, the Poller error is preserved, and nothing is
// provisioned.
func TestPollerIdentityLookupFailure_StillReconcilesTriggerSafety(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo

	setup := func(t *testing.T) (webhookFake, *fakeTriggerOwner) {
		t.Helper()
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		require.Len(t, c.hooks(), 1)
		c.PipelineVarOverrideRoles[key] = forge.PipelineVarOverrideDeveloper
		po := newPollerOwner(c)
		po.uidErr = errors.New("401 unauthorized")
		return c, po
	}

	t.Run("provisioning", func(t *testing.T) {
		c, po := setup(t)
		minted := len(c.CreatedTriggerTokens)

		res, err := ensureWebhookAsPoller(c, po, false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving the GitLab Poller identity")
		assert.Empty(t, c.triggers(), "the unsafe trigger is revoked despite the Poller failure")
		assert.Empty(t, c.hooks())
		assert.Len(t, c.CreatedTriggerTokens, minted, "nothing is provisioned")
		assert.Equal(t, "update", res.Action)
	})

	t.Run("failed-install reconciliation", func(t *testing.T) {
		c, po := setup(t)

		_, err := ReconcileGitLabWebhookSafetyWithTriggerOwner(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving the GitLab Poller identity")
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("safe state is left alone", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		po := newPollerOwner(c)
		po.uidErr = errors.New("401 unauthorized")

		_, err := ReconcileGitLabWebhookSafetyWithTriggerOwner(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)

		require.Error(t, err)
		assert.Len(t, c.triggers(), 1)
		assert.Len(t, c.hooks(), 1)
	})
}

// A revoked superseded Poller-owned trigger must not tear down a compliant
// active trigger owned by someone else when elevation is then refused.
func TestEnsureGitLabWebhookFastPath_RevokedSupersededPollerTriggerPreservesActiveFastPath(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.triggers(), 1)
	activeID := c.triggers()[0].ID

	// A superseded managed trigger owned by the Poller.
	po := newPollerOwner(c)
	c.TriggerTokenOwnerID = webhookPollerUserID
	_, err := c.CreatePipelineTriggerToken(context.Background(), webhookTestOwner, webhookTestRepo, GitLabWebhookTriggerDescription)
	require.NoError(t, err)
	require.Len(t, c.triggers(), 2)
	po.elevateErr = fmt.Errorf("update member: %w", forge.ErrForbidden)

	res, err := ensureWebhookAsPoller(c, po, true)

	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, activeID, c.triggers()[0].ID, "the compliant active trigger is kept")
	assert.Len(t, c.hooks(), 1, "the compliant webhook is kept")
	assert.Contains(t, strings.Join(res.Details, "\n"), "Preserved")
}

// revokeSecretClient fails trigger revocation with an error that echoes a
// credential the redactor does not know.
type revokeSecretClient struct {
	webhookFake
	secret string
}

func (c revokeSecretClient) RevokePipelineTriggerToken(context.Context, string, string, int64) error {
	return errors.New("500 server echoed credential " + c.secret)
}

// Cleanup revocation failures must withhold server text, which can echo the
// administrative authentication credential.
func TestMintPollerOwnedTrigger_CleanupRevocationErrorsWithholdServerText(t *testing.T) {
	const adminSecret = "glpat-admin-auth-secret"

	t.Run("revocation after failed restore", func(t *testing.T) {
		base := newWebhookFake()
		c := revokeSecretClient{webhookFake: base, secret: adminSecret}
		po := newPollerOwner(base)
		po.restoreErrs = 2

		_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

		var restoreErr *gitlabPollerRestoreError
		require.ErrorAs(t, err, &restoreErr)
		// The compensating revocation of the minted trigger withholds the
		// server text. (The separate managed-teardown error that
		// failClosedPoller also joins is outside this check.)
		for _, line := range strings.Split(err.Error(), "\n") {
			if strings.Contains(line, "minted before the failed restore") {
				assert.NotContains(t, line, adminSecret)
				assert.Contains(t, line, "server error text withheld")
			}
		}
		assert.Contains(t, err.Error(), "minted before the failed restore")
	})

	t.Run("revocation after owner mismatch", func(t *testing.T) {
		base := newWebhookFake()
		c := revokeSecretClient{webhookFake: base, secret: adminSecret}
		po := newPollerOwner(base)
		po.ownerID = webhookDeveloperUserID

		_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), adminSecret)
		assert.Contains(t, err.Error(), "reports owner user ID")
		assert.Contains(t, err.Error(), "server error text withheld")
	})
}

// Installers for one project are serialized: a second operation does not
// start its transaction while another holds the project lock.
func TestEnsureGitLabWebhookFastPath_SerializedPerProject(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)

	unlock, lockErr := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, lockErr)
	done := make(chan error, 1)
	go func() {
		_, err := ensureWebhookAsPoller(c, po, false)
		done <- err
	}()

	select {
	case <-done:
		t.Fatal("the transaction ran while another operation held the project lock")
	case <-time.After(100 * time.Millisecond):
	}
	assert.Empty(t, po.sets, "no membership change happens before the lock is acquired")
	assert.Empty(t, c.CreatedTriggerTokens)

	var releaseErr error
	unlock(&releaseErr)
	require.NoError(t, releaseErr)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the transaction did not run after the lock was released")
	}
	assert.Len(t, c.triggers(), 1)
}

// elevateAfterHookClient raises a user's effective access right after the
// webhook is published, modelling another process elevating the owner.
type elevateAfterHookClient struct {
	webhookFake
	userID int64
}

func (c elevateAfterHookClient) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	created, err := c.webhookFake.CreateProjectHook(ctx, owner, repo, hook)
	c.ProjectMemberAccess[c.userID] = forge.GitLabAccessLevelMaintainer
	return created, err
}

// The post-publication check verifies the trigger owner's current role and
// fails closed when it was raised above Developer in the meantime.
func TestEnsureGitLabWebhookFastPath_OwnerElevatedAfterPublicationTearsDown(t *testing.T) {
	base := newWebhookFake()
	ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	c := elevateAfterHookClient{webhookFake: base, userID: webhookDeveloperUserID}

	res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

	require.ErrorIs(t, err, errGitLabTriggerOwnerElevated)
	assert.Equal(t, "deferred", res.Action)
	assert.Empty(t, base.triggers(), "the privileged trigger is revoked")
	assert.Empty(t, base.hooks(), "the webhook is removed")
}

// demoteAfterHookClient lowers a user's effective access right after the
// webhook is published, modelling the owner losing Developer membership.
type demoteAfterHookClient struct {
	webhookFake
	userID int64
	level  int
}

func (c demoteAfterHookClient) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	created, err := c.webhookFake.CreateProjectHook(ctx, owner, repo, hook)
	c.ProjectMemberAccess[c.userID] = c.level
	return created, err
}

// The post-publication check also fails closed when the owner dropped below
// Developer and can no longer start the trigger's pipelines.
func TestEnsureGitLabWebhookFastPath_OwnerBelowDeveloperAfterPublicationTearsDown(t *testing.T) {
	for name, level := range map[string]int{"reporter": 20, "guest": 10} {
		t.Run(name, func(t *testing.T) {
			base := newWebhookFake()
			ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
			c := demoteAfterHookClient{webhookFake: base, userID: webhookDeveloperUserID, level: level}

			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

			require.ErrorIs(t, err, errGitLabTriggerOwnerBelowDeveloper)
			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, base.triggers(), "the unusable trigger is revoked")
			assert.Empty(t, base.hooks(), "the webhook is removed")
		})
	}
}

// A replacement runtime credential that could not be published leaves no
// live trigger behind: the freshly minted Poller-owned trigger is revoked
// because nothing wires it in.
func TestEnsureGitLabWebhookFastPath_PublicationFailureRevokesMintedTrigger(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.runtimeCreateErr = errors.New("500 internal error")

	_, err := ensureWebhookAsPoller(c, po, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-run 'fullsend repos install'")
	assert.Empty(t, c.triggers(), "the unwired trigger is revoked")
}

// A failed revocation of the previous runtime credential followed by a
// successful publication keeps the previous credential tracked for cleanup.
func TestEnsureGitLabWebhookFastPath_UnconfirmedRevocationKeepsPreviousTracked(t *testing.T) {
	ctx := context.Background()
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.runtimeRevokeErr = errors.New("500 internal error")
	require.NoError(t, writeRotationState(ctx, c, webhookTestOwner, webhookTestRepo, rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RolePoller): {
			Phase:         rotationPhaseIdle,
			IncomingID:    5,
			DistributedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
	}}))

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)

	state, _, err := loadRotationState(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	rs := state.Roles[string(gitlabroles.RolePoller)]
	assert.NotEqual(t, 5, rs.IncomingID, "the replacement is recorded as incoming")
	assert.Equal(t, []int{5}, rs.OutgoingIDs, "the possibly surviving previous credential stays scheduled for cleanup")
	assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
}

// A managed-account inventory that cannot be read (for example a forbidden
// service-account listing) is not a confirmed absence: reconciliation fails
// instead of skipping demotion, and provisioning does not proceed.
func TestReconcileGitLabPollerElevation_ForbiddenInventoryFailsClosed(t *testing.T) {
	forbidden := fmt.Errorf("listing project service accounts: %w", forge.ErrForbidden)

	t.Run("reconciliation", func(t *testing.T) {
		c := newWebhookFake()
		po := newPollerOwner(c)
		po.uidErr = forge.ErrNotFound
		po.managedErr = forbidden

		_, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)

		require.Error(t, err)
		assert.ErrorContains(t, err, "listing the managed GitLab Poller identities")
	})

	t.Run("identity lookup", func(t *testing.T) {
		c := newWebhookFake()
		po := newPollerOwner(c)
		po.uidErr = forbidden
		minted := len(c.CreatedTriggerTokens)

		_, err := ensureWebhookAsPoller(c, po, false)

		require.Error(t, err)
		assert.ErrorContains(t, err, "resolving the GitLab Poller identity")
		assert.Len(t, c.CreatedTriggerTokens, minted, "nothing is provisioned")
	})
}

// An unidentified selected credential does not hide an elevated managed
// Poller account from reconciliation.
func TestReconcileGitLabPollerElevation_NotFoundSelectionStillReconcilesManagedAccounts(t *testing.T) {
	const managedID = int64(webhookPollerUserID) - 100
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.uidErr = forge.ErrNotFound
	po.managedIDs = []int64{managedID}
	c.ProjectMemberAccess[managedID] = forge.GitLabAccessLevelMaintainer

	_, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)

	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[managedID], "the elevated managed account is restored")
	assert.Equal(t, []int64{managedID}, po.bootstrapRevokedFor)
}
