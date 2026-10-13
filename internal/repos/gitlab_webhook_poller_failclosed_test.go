package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// postPublishClient alters what a verification after webhook publication
// sees: it can omit the trigger owner or fail the effective-access lookup.
type postPublishClient struct {
	webhookFake
	published *bool
	hideOwner bool
	failLevel bool
}

func (c postPublishClient) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	created, err := c.webhookFake.CreateProjectHook(ctx, owner, repo, hook)
	*c.published = true
	if c.failLevel {
		c.Errors["GetProjectMemberAccessLevel"] = errors.New("503 lookup failed")
	}
	return created, err
}

func (c postPublishClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	triggers, err := c.webhookFake.ListPipelineTriggerTokens(ctx, owner, repo)
	if *c.published && c.hideOwner {
		for i := range triggers {
			triggers[i].OwnerID = 0
		}
	}
	return triggers, err
}

// A published trigger whose owner GitLab omits, or whose current access cannot
// be looked up, is unverified: the managed fast path is torn down rather than
// left live.
func TestEnsureGitLabWebhookFastPath_UnverifiedOwnerAfterPublicationTearsDown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hideOwner bool
		failLevel bool
	}{
		{name: "owner omitted", hideOwner: true},
		{name: "access lookup failed", failLevel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newWebhookFake()
			ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
			published := false
			c := postPublishClient{webhookFake: base, published: &published, hideOwner: tc.hideOwner, failLevel: tc.failLevel}

			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

			require.ErrorIs(t, err, errGitLabTriggerOwnerUnverified)
			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, base.triggers(), "the unverified trigger is revoked")
			assert.Empty(t, base.hooks(), "the webhook is removed")
		})
	}
}

// failClosedPoller joins the managed teardown failure; it must withhold server
// text that can echo the administrative authentication credential.
func TestFailClosedPoller_TeardownErrorWithholdsServerText(t *testing.T) {
	const adminSecret = "glpat-admin-auth-secret"
	base := newWebhookFake()
	c := revokeSecretClient{webhookFake: base, secret: adminSecret}
	po := newPollerOwner(base)
	po.restoreErrs = 2

	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)
	assert.NotContains(t, err.Error(), adminSecret, "no part of the returned error echoes the credential")
	assert.Contains(t, err.Error(), "tearing down the managed webhook fast path")
}

// ReconcileGitLabPollerElevation restores a leftover elevated Poller without
// any webhook work, and does nothing on a dry run.
func TestReconcileGitLabPollerElevation(t *testing.T) {
	setup := func() (webhookFake, *fakeTriggerOwner) {
		c := newWebhookFake()
		po := newPollerOwner(c)
		c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
		return c, po
	}

	t.Run("restores an elevated Poller", func(t *testing.T) {
		c, po := setup()

		res, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
		assert.NotEmpty(t, res.Details)
	})

	t.Run("dry run changes nothing", func(t *testing.T) {
		c, po := setup()

		_, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, true)

		require.NoError(t, err)
		assert.Equal(t, forge.GitLabAccessLevelMaintainer, c.ProjectMemberAccess[webhookPollerUserID])
		assert.Empty(t, po.sets)
	})

	t.Run("nil trigger owner changes nothing", func(t *testing.T) {
		c, _ := setup()

		_, err := ReconcileGitLabPollerElevation(context.Background(), c, nil, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Equal(t, forge.GitLabAccessLevelMaintainer, c.ProjectMemberAccess[webhookPollerUserID])
	})
}

// listFailAfterPublishClient fails the discovery calls the post-publication
// verification makes. A transient failure affects only the first such call,
// so the independent teardown can still reach GitLab; a persistent one also
// fails the teardown's own discovery.
type listFailAfterPublishClient struct {
	webhookFake
	published  *bool
	failed     *bool
	failHooks  bool
	persistent bool
}

func (c listFailAfterPublishClient) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	created, err := c.webhookFake.CreateProjectHook(ctx, owner, repo, hook)
	*c.published = true
	return created, err
}

func (c listFailAfterPublishClient) shouldFail() bool {
	if !*c.published {
		return false
	}
	if c.persistent {
		return true
	}
	if *c.failed {
		return false
	}
	*c.failed = true
	return true
}

func (c listFailAfterPublishClient) ListProjectHooks(ctx context.Context, owner, repo string) ([]forge.ProjectHook, error) {
	if c.failHooks && c.shouldFail() {
		return nil, errors.New("503 hook listing failed")
	}
	return c.webhookFake.ListProjectHooks(ctx, owner, repo)
}

func (c listFailAfterPublishClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	if !c.failHooks && c.shouldFail() {
		return nil, errors.New("503 trigger listing failed")
	}
	return c.webhookFake.ListPipelineTriggerTokens(ctx, owner, repo)
}

// A discovery failure after publication leaves the published bearer's owner
// unverified: the managed fast path is torn down on an independent context,
// and a teardown that also fails is reported.
func TestEnsureGitLabWebhookFastPath_DiscoveryFailureAfterPublicationTearsDown(t *testing.T) {
	for _, failHooks := range []bool{true, false} {
		name := "trigger listing failed"
		if failHooks {
			name = "hook listing failed"
		}
		t.Run(name, func(t *testing.T) {
			base := newWebhookFake()
			ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
			published, failed := false, false
			c := listFailAfterPublishClient{webhookFake: base, published: &published, failed: &failed, failHooks: failHooks}

			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

			require.ErrorIs(t, err, errGitLabTriggerOwnerUnverified)
			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, base.triggers(), "the unverified trigger is revoked")
			assert.Empty(t, base.hooks(), "the webhook is removed")
		})

		t.Run(name+" and teardown failed", func(t *testing.T) {
			base := newWebhookFake()
			ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
			published, failed := false, false
			c := listFailAfterPublishClient{webhookFake: base, published: &published, failed: &failed, failHooks: failHooks, persistent: true}

			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

			require.ErrorIs(t, err, errGitLabTriggerOwnerUnverified)
			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, err.Error(), "tearing down the managed fast path", "the cleanup failure is reported")
			assert.Empty(t, base.triggers(), "the published trigger is revoked by its known ID even though discovery keeps failing")
		})
	}
}
