package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// The Poller trigger is created through a bootstrap-credential lifecycle
// (#8083): the distributed runtime credential is revoked before the Poller is
// raised, an installer-only bootstrap credential creates the trigger, and the
// bootstrap credential is revoked before the replacement runtime credential
// or the webhook is published.
func TestEnsureGitLabWebhookFastPath_BootstrapLifecycleOrder(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"revoke-bootstrap", // the orphan sweep that precedes every transaction
		"revoke-runtime", "create-bootstrap", "elevate", "create-trigger", "restore", "revoke-bootstrap", "publish-runtime",
	}, po.events, "revocation precedes elevation; the bootstrap credential is revoked before the replacement is published")
	assert.False(t, po.runtimeActiveAtElevate, "no distributed runtime credential is valid while the Poller is elevated")
	assert.Equal(t, fakeBootstrapToken, po.bootstrapAtTrigger, "the trigger is created with the bootstrap credential")
	assert.False(t, po.bootstrapActive, "the bootstrap credential is revoked")
	assert.False(t, po.bootstrapActiveAtPublish, "the bootstrap credential is gone before the replacement is published")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, po.levelAtPublish, "the replacement is published only at verified Developer access")
	assert.Zero(t, po.hooksAtPublish, "the replacement is published before the webhook is enabled")
	assert.True(t, po.runtimeActive, "a replacement runtime credential is published")
	assert.Equal(t, "glpat-new-runtime-token", c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretGitLabPollerToken])
	require.Len(t, c.hooks(), 1)
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, int64(webhookPollerUserID), c.triggers()[0].OwnerID)
	assert.True(t, strings.Contains(strings.Join(res.Details, "\n"), "Revoked the distributed GitLab Poller runtime credential"))
	assert.True(t, strings.Contains(strings.Join(res.Details, "\n"), "installer-only bootstrap credential"))
	assert.NotContains(t, strings.Join(res.Details, "\n"), fakeBootstrapToken, "the bootstrap value is never reported")
}

// An unsafe account (credentials that cannot be accounted for) is refused
// before anything is revoked or elevated, and the runtime credential stays.
func TestEnsureGitLabWebhookFastPath_UnaccountedCredentialRefusesBeforeRevocation(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.unsafeErr = &PollerElevationUnsafeError{Reason: "the account holds an unmanaged personal access token"}

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)

	assert.Equal(t, "deferred", res.Action)
	assert.Equal(t, []string{"revoke-bootstrap"}, po.events, "only the orphan sweep runs: nothing is revoked, created, or elevated")
	assert.True(t, po.runtimeActive, "the runtime credential is preserved")
	assert.Equal(t, "glpat-old-runtime-token", c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretGitLabPollerToken])
}

// A runtime credential whose revocation fails keeps the Poller at Developer:
// it is never elevated.
func TestEnsureGitLabWebhookFastPath_RuntimeRevocationFailureNeverElevates(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.runtimeRevokeErr = errors.New("500 internal error")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoking the distributed GitLab Poller runtime credential")

	assert.Empty(t, po.sets, "the Poller is never raised while the runtime credential is valid")
	assert.NotContains(t, po.events, "create-bootstrap")
	assert.Empty(t, c.hooks())
}

// A failed bootstrap creation leaves the Poller at Developer, sweeps any
// ambiguous bootstrap credential, and still provisions a runtime credential.
func TestEnsureGitLabWebhookFastPath_BootstrapCreationFailure(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.bootstrapCreateErr = errors.New("500 internal error")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrap credential")

	assert.Empty(t, po.sets, "the Poller is not elevated without a bootstrap credential")
	assert.Equal(t, []string{"revoke-bootstrap", "revoke-runtime", "create-bootstrap", "revoke-bootstrap", "publish-runtime"}, po.events)
	assert.False(t, po.bootstrapActive)
	assert.True(t, po.runtimeActive, "the runtime credential is re-provisioned at Developer access")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.Empty(t, c.hooks())
}

// When the bootstrap sweep after a failed creation fails too, no runtime
// credential is published next to an unaccounted-for credential.
func TestEnsureGitLabWebhookFastPath_BootstrapCreationFailureWithFailedSweep(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.bootstrapCreateErr = errors.New("500 internal error")
	po.bootstrapRevokeSkip = 1
	po.bootstrapRevokeErrs = 1

	res, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Zero(t, po.published, "no runtime credential is published")
	assert.Empty(t, po.sets)

	// The unaccounted-for bootstrap credential is a typed failure, so the
	// caller contains the Poller and tears the fast path down at once under
	// the lease it already holds instead of leaving that to a later run.
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)
	assert.True(t, restoreErr.bootstrap)
	assert.Equal(t, "deferred", res.Action)
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained, "the Poller credential is contained")
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
}

// A failed elevation that is restored revokes the bootstrap credential and
// publishes the replacement at verified Developer access.
func TestEnsureGitLabWebhookFastPath_ElevationFailureRevokesBootstrapAndRepublishes(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.elevateErr = errors.New("500 internal error")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)

	assert.False(t, po.bootstrapActive)
	assert.True(t, po.runtimeActive)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, po.levelAtPublish)
	assert.False(t, po.bootstrapActiveAtPublish)
	assert.Empty(t, c.hooks())
}

// A Poller that cannot be restored publishes no runtime credential, is
// contained, and the bootstrap credential does not survive containment.
func TestEnsureGitLabWebhookFastPath_RestoreFailureNeverPublishesRuntime(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.restoreErrs = 2

	_, err := ensureWebhookAsPoller(c, po, false)
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)

	assert.Zero(t, po.published, "no runtime credential while permissions cannot be verified at Developer")
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
	assert.False(t, po.bootstrapActive, "containment revokes the bootstrap credential")
	assert.False(t, po.runtimeActive)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
}

// A bootstrap credential whose revocation cannot be verified fails closed
// like a failed restore: the new trigger is revoked, nothing is published, and
// the Poller is contained.
func TestEnsureGitLabWebhookFastPath_BootstrapRevocationFailureFailsClosed(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	po.bootstrapRevokeSkip = 1
	po.bootstrapRevokeErrs = 1

	res, err := ensureWebhookAsPoller(c, po, false)
	var failure *gitlabPollerRestoreError
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.bootstrap)
	assert.Contains(t, err.Error(), "bootstrap credential")
	assert.Equal(t, "deferred", res.Action)

	assert.Zero(t, po.published, "no runtime credential is published while the bootstrap credential is unaccounted for")
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
	assert.False(t, po.bootstrapActive)
	assert.Empty(t, c.triggers(), "the trigger created with the bootstrap credential is revoked")
	assert.Empty(t, c.hooks())
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
}

// Cancellation during the elevation window does not stop the bounded cleanup:
// Developer access is restored, the bootstrap credential is revoked, and the
// replacement runtime credential is published.
func TestEnsureGitLabWebhookFastPath_CancellationStillRevokesBootstrapAndRepublishes(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	ctx, cancel := context.WithCancel(context.Background())
	po.cancelAfterCreate = cancel

	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(ctx, c, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	_ = err

	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.False(t, po.bootstrapActive, "the bootstrap credential is revoked even after cancellation")
	assert.Contains(t, po.events, "revoke-bootstrap")
	assert.True(t, po.runtimeActive, "the replacement is published after cancellation")
	assert.False(t, po.bootstrapActiveAtPublish)
	assert.False(t, holdsLease(c), "the lease covers the transaction and is released at its end")
}

// A replacement that cannot be created is reported with recovery guidance
// and the Poller stays at Developer.
func TestEnsureGitLabWebhookFastPath_RuntimePublicationFailureReported(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.runtimeCreateErr = errors.New("500 internal error")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-run 'fullsend repos install'")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.False(t, po.bootstrapActive)
}

// A replacement that cannot be stored is revoked again.
func TestEnsureGitLabWebhookFastPath_RuntimeStoreFailureRevokesReplacement(t *testing.T) {
	base := newWebhookFake()
	c := pollerSecretFailClient{webhookFake: base}
	po := newPollerOwner(base)

	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing the replacement")
	assert.False(t, po.runtimeActive, "the unstored replacement is revoked")
	assert.Equal(t, 2, po.revokedRuntime, "the original and the unstored replacement are revoked")
}

// A replacement that GitLab cannot mask is never stored (it would reach
// protected job logs in the clear); it is revoked instead.
func TestEnsureGitLabWebhookFastPath_UnmaskableReplacementNotPublished(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.runtimeToken = "glpat short\nmultiline"

	_, err := ensureWebhookAsPoller(c, po, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be masked")
	assert.NotContains(t, err.Error(), "multiline", "the value is never reported")
	_, stored := c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretGitLabPollerToken]
	assert.False(t, stored, "an unmaskable credential is never published")
	assert.False(t, po.runtimeActive, "the unmaskable replacement is revoked")
	assert.Equal(t, 2, po.revokedRuntime, "the original and the unmaskable replacement are revoked")
}

type pollerSecretFailClient struct {
	webhookFake
}

func (c pollerSecretFailClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if name == forge.SecretGitLabPollerToken {
		return errors.New("500 internal error")
	}
	return c.webhookFake.CreateRepoSecret(ctx, owner, repo, name, value)
}

// A previous run interrupted after the runtime credential was revoked and the
// Poller was raised leaves an orphaned bootstrap credential. The next run
// recovers through installer authority: Developer access is restored and the
// bootstrap credential is revoked without any use of the revoked runtime
// credential.
func TestReconcileGitLabPollerElevation_InterruptedRunRecovery(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.runtimeActive = false // revoked by the interrupted run
	require.NoError(t, c.DeleteRepoSecret(context.Background(), webhookTestOwner, webhookTestRepo, forge.SecretGitLabPollerToken))
	po.bootstrapActive = true
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer

	res, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, err)

	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.False(t, po.bootstrapActive, "the orphaned bootstrap credential is revoked")
	assert.Equal(t, []string{"restore", "revoke-bootstrap"}, po.events, "Developer access is restored first")
	assert.Contains(t, strings.Join(res.Details, "\n"), "Restored the GitLab Poller identity")
}

// An orphaned bootstrap credential is revoked even when the Poller is
// already at Developer access.
func TestReconcileGitLabPollerElevation_OrphanedBootstrapAtDeveloper(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.bootstrapActive = true

	_, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, err)
	assert.False(t, po.bootstrapActive)
	assert.Empty(t, po.sets)
}

// An orphan that cannot be revoked fails closed and contains the Poller.
func TestReconcileGitLabPollerElevation_OrphanRevocationFailureContains(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.bootstrapActive = true
	po.bootstrapRevokeErrs = 1

	res, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)
	var failure *gitlabPollerRestoreError
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.bootstrap)
	assert.Equal(t, "deferred", res.Action)
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
	assert.False(t, po.bootstrapActive)
}

// The reconciliation sweep only revokes: a dry run touches nothing.
func TestReconcileGitLabPollerElevation_DryRunLeavesBootstrapAlone(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.bootstrapActive = true

	_, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, true)
	require.NoError(t, err)
	assert.True(t, po.bootstrapActive)
	assert.Empty(t, po.events)
}

// The bootstrap value never reaches a returned error even when GitLab echoes
// it back in a failure.
func TestEnsureGitLabWebhookFastPath_BootstrapValueNeverLeaks(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.createErr = errors.New("400 bad request: token " + fakeBootstrapToken + " rejected")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), fakeBootstrapToken)
}
