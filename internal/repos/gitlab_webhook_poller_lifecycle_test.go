package repos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// recheckOwner reports the Poller safe on the first verification and unsafe on
// every later one, as when a holder of the runtime credential registers a
// credential that the pre-elevation revocations do not invalidate.
type recheckOwner struct {
	*fakeTriggerOwner
	calls int
}

// latePATOwner models a managed-name PAT appearing after the first revocation
// inventory, for example from an already in-flight self-rotation request.
type latePATOwner struct{ *fakeTriggerOwner }

func (o *latePATOwner) VerifyPollerCredentialsRevoked(context.Context, string, string, int64) error {
	return &PollerElevationUnsafeError{Reason: "fullsend-poller PAT appeared after revocation"}
}

func TestEnsureGitLabWebhookFastPath_LateManagedPATRefusesElevation(t *testing.T) {
	c := newWebhookFake()
	po := &latePATOwner{fakeTriggerOwner: newPollerOwner(c)}
	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, po.events, "revoke-runtime")
	assert.NotContains(t, po.events, "create-bootstrap")
	assert.NotContains(t, po.events, "elevate")
	assert.Empty(t, po.sets)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
}

func (o *recheckOwner) VerifyPollerElevationSafe(context.Context, string, string, int64) error {
	o.calls++
	if o.calls >= 2 {
		return &PollerElevationUnsafeError{Reason: "it has an SSH authentication key"}
	}
	return nil
}

func (o *recheckOwner) VerifyPollerCredentialsRevoked(ctx context.Context, owner, repo string, uid int64) error {
	return o.VerifyPollerElevationSafe(ctx, owner, repo, uid)
}

// The safety inventory is repeated once the runtime credential is revoked: a
// credential registered with it between the first check and the revocation
// still refuses the elevation.
func TestEnsureGitLabWebhookFastPath_SafetyRecheckedAfterRuntimeRevocation(t *testing.T) {
	c := newWebhookFake()
	po := &recheckOwner{fakeTriggerOwner: newPollerOwner(c)}

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)

	assert.Equal(t, 2, po.calls)
	assert.Equal(t, "deferred", res.Action)
	assert.NotContains(t, po.events, "elevate", "the Poller is never raised")
	assert.NotContains(t, po.events, "create-bootstrap")
	assert.Empty(t, po.sets)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.Contains(t, po.events, "revoke-runtime")
	assert.Equal(t, 1, po.published, "the runtime credential is republished at Developer access")
	assert.Contains(t, res.Details[len(res.Details)-1], "SSH authentication key")
}

// revokeCtxOwner records whether the context given to runtime-credential
// revocation was live.
type revokeCtxOwner struct {
	*fakeTriggerOwner
	revokeCtxErrs []error
}

func (o *revokeCtxOwner) RevokePollerRuntimeCredentials(ctx context.Context, owner, repo string, uid int64) error {
	o.revokeCtxErrs = append(o.revokeCtxErrs, ctx.Err())
	return o.fakeTriggerOwner.RevokePollerRuntimeCredentials(ctx, owner, repo, uid)
}

// pollerSecretTimeoutClient blocks secret storage until its context ends.
type pollerSecretTimeoutClient struct {
	webhookFake
}

func (c pollerSecretTimeoutClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if name == forge.SecretGitLabPollerToken {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.webhookFake.CreateRepoSecret(ctx, owner, repo, name, value)
}

// A secret store that exhausts the publication deadline still gets its
// compensating revocation on a fresh live context.
func TestEnsureGitLabWebhookFastPath_PublicationDeadlineStillRevokesReplacement(t *testing.T) {
	prev := gitlabCleanupTimeout
	gitlabCleanupTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gitlabCleanupTimeout = prev })

	base := newWebhookFake()
	c := pollerSecretTimeoutClient{webhookFake: base}
	po := &revokeCtxOwner{fakeTriggerOwner: newPollerOwner(base)}

	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)

	require.Len(t, po.revokeCtxErrs, 2, "the original credential, then the unstored replacement")
	for i, ctxErr := range po.revokeCtxErrs {
		assert.NoError(t, ctxErr, "revocation %d received a live context", i)
	}
	assert.False(t, po.runtimeActive, "the unstored replacement is revoked")
}

// Role provisioning records the Poller's rotation state first. Webhook
// provisioning then replaces the credential, so the entry is updated to name
// the replacement and keeps outgoing credentials whose revocation was not
// confirmed (here a legacy token queued by role rotation) for grace cleanup.
func TestEnsureGitLabWebhookFastPath_ReplacementUpdatesRotationState(t *testing.T) {
	ctx := context.Background()
	c := newWebhookFake()
	po := newPollerOwner(c)
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	require.NoError(t, writeRotationState(ctx, c, webhookTestOwner, webhookTestRepo, rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RolePoller): {
			Phase:         rotationPhaseOverlapping,
			IncomingID:    5,
			OutgoingIDs:   []int{4},
			DistributedAt: old,
			ExpiresAt:     "2026-12-31",
		},
	}}))

	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	require.Equal(t, 1, po.published)

	state, _, err := loadRotationState(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	rs := state.Roles[string(gitlabroles.RolePoller)]
	assert.Equal(t, 9002, rs.IncomingID, "the replacement, not the revoked credential")
	assert.Equal(t, []int{4}, rs.OutgoingIDs, "the unconfirmed outgoing token stays scheduled for grace cleanup")
	assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
	assert.NotEqual(t, old, rs.DistributedAt)
	assert.NotEqual(t, "2026-12-31", rs.ExpiresAt)

	// Outside the idempotency window a reinstall sees a healthy credential.
	assert.True(t, recentlyDistributed(rs, time.Now()))
}

// Install reconciles every managed Poller account, not only the one that
// supplies the runtime credential: an interrupted transaction may have left a
// duplicate elevated with its bootstrap credential behind.
func TestReconcileGitLabPollerElevation_ReconcilesDuplicateManagedPollers(t *testing.T) {
	const duplicateID = int64(webhookPollerUserID) - 100
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.managedIDs = []int64{duplicateID, webhookPollerUserID}
	c.ProjectMemberAccess[duplicateID] = forge.GitLabAccessLevelMaintainer

	_, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)

	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[duplicateID], "the elevated duplicate is restored")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.ElementsMatch(t, []int64{webhookPollerUserID, duplicateID}, po.bootstrapRevokedFor, "orphaned bootstrap credentials are revoked on every managed account")
}

// Uninstall inventories and revokes an orphaned bootstrap credential, which
// is named differently from the service account.
func TestServiceAccountTokenClient_StrictInventoryIncludesBootstrap(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}, {ID: 20, Name: gitlabroles.CoderTokenName}}
	sa.tokens[10] = []ProjectAccessToken{
		{ID: 101, Name: gitlabroles.PollerTokenName, Active: true},
		{ID: 102, Name: gitlabroles.PollerBootstrapTokenName, Active: true},
	}
	// A token that merely carries the bootstrap name on another account is
	// not a Poller bootstrap credential.
	sa.tokens[20] = []ProjectAccessToken{{ID: 201, Name: gitlabroles.PollerBootstrapTokenName, Active: true}}
	c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}

	operational, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, operational, 1, "rotation selection never sees the bootstrap token")
	assert.Equal(t, 101, operational[0].ID)

	strict, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	ids := []int{}
	for _, tok := range strict {
		ids = append(ids, tok.ID)
	}
	assert.ElementsMatch(t, []int{101, 102}, ids)

	revoked, _, err := revokeGitLabIdentityTokens(ctx, c, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 2, revoked)
	assert.ElementsMatch(t, []int{101, 102}, sa.revoked)
}

// uninstallPollerOwner is a GitLabTriggerOwner stand-in for the uninstall
// reconciliation.
type uninstallPollerOwner struct {
	*fakeTriggerOwner
	members     map[int64]int
	bootstrap   map[int64]bool
	revokeErrs  map[int64]error
	restoreErrs map[int64]error
}

func (o *uninstallPollerOwner) SetProjectMemberAccessLevel(_ context.Context, _, _ string, uid int64, level int) error {
	if err := o.restoreErrs[uid]; err != nil {
		return err
	}
	o.members[uid] = level
	return nil
}

func (o *uninstallPollerOwner) RevokePollerBootstrap(_ context.Context, _, _ string, uid int64) error {
	if err := o.revokeErrs[uid]; err != nil {
		return err
	}
	o.bootstrap[uid] = false
	return nil
}

// Uninstall after an interrupted elevation restores and verifies Developer
// access and revokes the bootstrap credential of every managed Poller,
// duplicates included, and fails when cleanup cannot be confirmed.
func TestReconcileManagedPollersForUninstall(t *testing.T) {
	ctx := context.Background()
	setup := func() (*webhookFake, *uninstallPollerOwner) {
		c := newWebhookFake()
		c.ProjectMemberAccess[10] = forge.GitLabAccessLevelMaintainer
		c.ProjectMemberAccess[20] = forge.GitLabAccessLevelDeveloper
		o := &uninstallPollerOwner{
			fakeTriggerOwner: newPollerOwner(c),
			members:          map[int64]int{},
			bootstrap:        map[int64]bool{10: true, 20: true},
			revokeErrs:       map[int64]error{},
			restoreErrs:      map[int64]error{},
		}
		return &c, o
	}

	t.Run("restores and revokes on every managed account", func(t *testing.T) {
		c, o := setup()
		// The fake's Set updates the shared effective-access map.
		to := &effectiveAccessOwner{uninstallPollerOwner: o, access: c.ProjectMemberAccess}
		require.NoError(t, ReconcileManagedPollersForUninstall(ctx, c, to, webhookTestOwner, webhookTestRepo, []int64{10, 20}))
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[10])
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[20])
		assert.False(t, o.bootstrap[10])
		assert.False(t, o.bootstrap[20])
		_, touched := o.members[20]
		assert.False(t, touched, "a Poller already at Developer is not modified")
	})

	t.Run("an unverifiable restore fails the uninstall but still revokes", func(t *testing.T) {
		c, o := setup()
		o.restoreErrs[10] = errors.New("500 internal error")
		to := &effectiveAccessOwner{uninstallPollerOwner: o, access: c.ProjectMemberAccess}
		err := ReconcileManagedPollersForUninstall(ctx, c, to, webhookTestOwner, webhookTestRepo, []int64{10, 20})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "user ID 10")
		assert.False(t, o.bootstrap[10], "the bootstrap credential is still revoked")
		assert.False(t, o.bootstrap[20])
		assert.Equal(t, []int64{10}, o.contained, "only the Poller that could not be restored is contained")
	})

	t.Run("an unrestorable elevated poller with an active runtime credential is contained", func(t *testing.T) {
		c, o := setup()
		o.restoreErrs[10] = errors.New("500 internal error")
		o.runtimeActive = true
		to := &effectiveAccessOwner{uninstallPollerOwner: o, access: c.ProjectMemberAccess}
		err := ReconcileManagedPollersForUninstall(ctx, c, to, webhookTestOwner, webhookTestRepo, []int64{10})
		require.Error(t, err)
		assert.Equal(t, []int64{10}, o.contained)
		assert.False(t, o.runtimeActive, "the runtime credential no longer authenticates")
	})

	t.Run("a failed containment is reported with the restore failure", func(t *testing.T) {
		c, o := setup()
		o.restoreErrs[10] = errors.New("500 internal error")
		o.containErr = errors.New("500 internal error")
		to := &effectiveAccessOwner{uninstallPollerOwner: o, access: c.ProjectMemberAccess}
		err := ReconcileManagedPollersForUninstall(ctx, c, to, webhookTestOwner, webhookTestRepo, []int64{10})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "restoring the GitLab Poller identity (user ID 10)")
		assert.Contains(t, err.Error(), "containing the GitLab Poller credential (user ID 10)")
	})

	t.Run("a poller restored to Developer is not contained", func(t *testing.T) {
		c, o := setup()
		to := &effectiveAccessOwner{uninstallPollerOwner: o, access: c.ProjectMemberAccess}
		require.NoError(t, ReconcileManagedPollersForUninstall(ctx, c, to, webhookTestOwner, webhookTestRepo, []int64{10, 20}))
		assert.Empty(t, o.contained)
	})

	t.Run("an unrevocable bootstrap credential fails the uninstall", func(t *testing.T) {
		c, o := setup()
		o.revokeErrs[20] = errors.New("500 internal error")
		to := &effectiveAccessOwner{uninstallPollerOwner: o, access: c.ProjectMemberAccess}
		err := ReconcileManagedPollersForUninstall(ctx, c, to, webhookTestOwner, webhookTestRepo, []int64{10, 20})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "user ID 20")
		assert.True(t, o.bootstrap[20])
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[10])
	})
}

// effectiveAccessOwner applies membership changes to the fake's effective
// access map, which restorePollerDeveloper verifies.
type effectiveAccessOwner struct {
	*uninstallPollerOwner
	access map[int64]int
}

func (o *effectiveAccessOwner) SetProjectMemberAccessLevel(ctx context.Context, owner, repo string, uid int64, level int) error {
	if err := o.uninstallPollerOwner.SetProjectMemberAccessLevel(ctx, owner, repo, uid, level); err != nil {
		return err
	}
	o.access[uid] = level
	return nil
}

// Embedding only the required interface models an adapter that cannot establish
// server-side quiescence, even when every resource inventory is empty.
type noQuiescenceOwner struct{ GitLabTriggerOwner }

func TestPollerElevationWithoutQuiescencePreservesRuntimeCredential(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	res, err := ensureWebhookAsPoller(c, noQuiescenceOwner{po}, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.NotContains(t, po.events, "revoke-runtime")
	assert.NotContains(t, po.events, "create-bootstrap")
	assert.NotContains(t, po.events, "elevate")
	assert.Empty(t, po.sets)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
	assert.Contains(t, res.Details[len(res.Details)-1], "requests")
}

func TestPollerElevationRefusesRequestsCompletingAfterFinalInventory(t *testing.T) {
	for _, resource := range []string{"credential", "asynchronous job"} {
		t.Run(resource, func(t *testing.T) {
			c := newWebhookFake()
			po := newPollerOwner(c)
			// Both safety inventories pass; the server still has an accepted request
			// that may materialize a new resource after the final inventory.
			po.quiescenceErr = errors.New(resource + " creation request remains in flight")
			res, err := ensureWebhookAsPoller(c, po, false)
			require.NoError(t, err)
			assert.Equal(t, "deferred", res.Action)
			assert.Equal(t, 2, po.verified)
			assert.Contains(t, po.events, "revoke-runtime")
			assert.NotContains(t, po.events, "create-bootstrap")
			assert.NotContains(t, po.events, "elevate")
			assert.Empty(t, po.sets)
			assert.Equal(t, 1, po.published, "polling resumes at Developer access")
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, po.levelAtPublish)
			assert.Empty(t, c.triggers())
			assert.Empty(t, c.hooks())
		})
	}
}
