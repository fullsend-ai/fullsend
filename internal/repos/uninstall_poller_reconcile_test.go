package repos

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// reconcilingTokens is a token client that also reconciles Poller accounts, as
// the CLI's uninstall token client does.
type reconcilingTokens struct {
	fakeTokens
	leaseHeld func() bool
	calls     int
	err       error
	heldOnRun bool
	// onReconcile, when set, runs during reconciliation (for example to cancel
	// the uninstall context or to observe what was already removed).
	onReconcile func()
}

func (r *reconcilingTokens) ReconcileGitLabPollersForUninstall(context.Context, forge.Client, string, string) error {
	r.calls++
	r.heldOnRun = r.leaseHeld()
	if r.onReconcile != nil {
		r.onReconcile()
	}
	return r.err
}

func uninstallGitLabAcmeAPIWithTokens(ctx context.Context, client forge.Client, commit ScaffoldCommitFunc, tokens ProjectAccessTokenClient) ([]UninstallResult, error) {
	return Uninstall(ctx, UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
		GitLabTokens:   tokens,
	}, newTestClientFactory(client), commit, nil)
}

// Uninstall reconciles the managed Pollers under the project lease before role
// credential cleanup, and fails (retaining the manifest entry) when the
// reconciliation cannot be confirmed.
func TestUninstall_GitLabReconcilesPollersUnderLease(t *testing.T) {
	for name, reconcileErr := range map[string]error{"confirmed": nil, "unconfirmed": errors.New("restoring the GitLab Poller identity (user ID 10)")} {
		t.Run(name, func(t *testing.T) {
			fake := newInstalledFakeGitLabClient("acme/api")
			client := &leaseProbeClient{FakeClient: fake}
			tokens := &reconcilingTokens{err: reconcileErr, leaseHeld: func() bool { return client.leaseHeld("acme", "api") }}

			results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)

			require.NoError(t, err)
			require.Len(t, results, 1)
			assert.Equal(t, 1, tokens.calls)
			assert.True(t, tokens.heldOnRun, "reconciliation runs under the project lease")
			assert.False(t, client.leaseHeld("acme", "api"), "the lease is released afterward")
			if reconcileErr == nil {
				assert.NoError(t, results[0].Error)
				assert.True(t, results[0].Success)
				return
			}
			require.Error(t, results[0].Error)
			assert.False(t, results[0].Success)
			assert.Contains(t, results[0].Error.Error(), "user ID 10")
		})
	}
}

// Reconciliation runs before anything is removed: no webhook trigger has been
// discovered, no variable or secret deleted, and no token revoked yet.
func TestUninstall_GitLabReconcilesPollersBeforeAnyTeardown(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &reconcilingTokens{leaseHeld: func() bool { return client.leaseHeld("acme", "api") }}
	tokens.seed(ProjectAccessToken{Name: "fullsend-poller", Active: true})
	var triggerLists, varDeletes, secretDeletes, revoked int
	tokens.onReconcile = func() {
		triggerLists = len(client.triggerListHeld)
		varDeletes = len(client.varDeleteHeld)
		secretDeletes = len(client.secretDeleteHeld)
		revoked = len(tokens.revoked)
	}

	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
	assert.Equal(t, 1, tokens.calls)
	assert.Zero(t, triggerLists, "reconciliation precedes webhook teardown")
	assert.Zero(t, varDeletes, "reconciliation precedes variable deletion")
	assert.Zero(t, secretDeletes, "reconciliation precedes secret deletion")
	assert.Zero(t, revoked, "reconciliation precedes role credential revocation")
	assert.NotEmpty(t, tokens.revoked, "role cleanup runs after a confirmed reconciliation")
}

// A token client without the reconciliation capability keeps today's behavior,
// and a non-GitLab uninstall never reconciles a Poller.
func TestUninstall_PollerReconcileSkippedWhenNotApplicable(t *testing.T) {
	t.Run("non-implementing GitLab token client", func(t *testing.T) {
		fake := newInstalledFakeGitLabClient("acme/api")
		tokens := &fakeTokens{}
		tokens.seed(ProjectAccessToken{Name: "fullsend-poller", Active: true})
		_, implements := any(tokens).(GitLabPollerUninstallReconciler)
		require.False(t, implements)

		results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), fake, uninstallCommitFn(fake), tokens)

		require.NoError(t, err)
		require.Len(t, results, 1)
		require.NoError(t, results[0].Error)
		assert.NotEmpty(t, tokens.revoked, "role cleanup still runs")
		assert.False(t, fake.VariablesExist["acme/api/"+forge.VarGitLabRoleRotation])
	})
	t.Run("GitHub repository", func(t *testing.T) {
		client := newInstalledFakeClient("acme/api")
		tokens := &reconcilingTokens{leaseHeld: func() bool { return false }}

		results, err := Uninstall(context.Background(), UninstallConfig{
			Manifest:       testManifest("acme/api"),
			Repos:          []string{"acme/api"},
			Direct:         true,
			MaxConcurrency: 1,
			GitLabTokens:   tokens,
		}, newTestClientFactory(client), uninstallCommitFn(client), nil)

		require.NoError(t, err)
		require.Len(t, results, 1)
		require.NoError(t, results[0].Error)
		assert.Zero(t, tokens.calls, "a GitHub uninstall never reconciles a GitLab Poller")
	})
}

// A supplied Poller credential whose owner cannot be attributed leaves the role
// identity variables, the installed credential, and the rotation provenance a
// retry needs in place, and no token is revoked.
func TestUninstall_GitLabUnresolvedSuppliedPollerLeavesIdentityStateInPlace(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &reconcilingTokens{
		err:       fmt.Errorf("%w: attribution failed", ErrPollerSuppliedUnresolved),
		leaseHeld: func() bool { return client.leaseHeld("acme", "api") },
	}
	tokens.seed(ProjectAccessToken{Name: "fullsend-poller", Active: true})
	var messages []string
	progress := func(_, _, msg string) { messages = append(messages, msg) }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
		GitLabTokens:   tokens,
	}, newTestClientFactory(client), uninstallCommitFn(fake), progress)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.ErrorIs(t, results[0].Error, ErrPollerSuppliedUnresolved)
	assert.False(t, results[0].Success)
	for _, d := range fake.DeletedVariables {
		assert.False(t, isGitLabIdentityUninstallVar(d.Name), "identity variable %s must be retained", d.Name)
	}
	assert.True(t, fake.VariablesExist["acme/api/"+forge.VarGitLabRoleRotation], "rotation provenance is retained")
	assert.True(t, fake.VariablesExist["acme/api/"+forge.SecretGitLabPollerToken], "the installed Poller credential is retained")
	assert.Empty(t, tokens.revoked, "no token is revoked")
	assert.Contains(t, messages, "Skipping GitLab role identity cleanup: the owner of a supplied GitLab role credential is unresolved")
	assert.False(t, client.leaseHeld("acme", "api"), "the lease is released")
}

// Any failed Poller reconciliation, not only an unresolved supplied owner, keeps
// role identity cleanup from running: the managed Poller's membership was never
// restored, so its tokens and rotation state stay for a retry, which then
// completes the cleanup once reconciliation succeeds.
func TestUninstall_GitLabReconcileFailureLeavesIdentityStateForRetry(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &reconcilingTokens{
		err:       errors.New("listing GitLab project service accounts to reconcile the Poller"),
		leaseHeld: func() bool { return client.leaseHeld("acme", "api") },
	}
	tokens.seed(ProjectAccessToken{Name: "fullsend-poller", Active: true})
	var messages []string
	progress := func(_, _, msg string) { messages = append(messages, msg) }
	cfg := UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
		GitLabTokens:   tokens,
	}

	results, err := Uninstall(context.Background(), cfg, newTestClientFactory(client), uninstallCommitFn(fake), progress)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.NotErrorIs(t, results[0].Error, ErrPollerSuppliedUnresolved)
	for _, d := range fake.DeletedVariables {
		assert.False(t, isGitLabIdentityUninstallVar(d.Name), "identity variable %s must be retained", d.Name)
	}
	assert.True(t, fake.VariablesExist["acme/api/"+forge.VarGitLabRoleRotation], "rotation state is retained")
	assert.Empty(t, tokens.revoked, "no token is revoked")
	assert.Contains(t, messages, "Skipping GitLab role identity cleanup: the Poller identity could not be reconciled; retry the uninstall")

	tokens.err = nil
	results, err = Uninstall(context.Background(), cfg, newTestClientFactory(client), uninstallCommitFn(fake), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
	assert.Equal(t, 2, tokens.calls)
	assert.NotEmpty(t, tokens.revoked, "the retry completes the cleanup")
	assert.False(t, fake.VariablesExist["acme/api/"+forge.VarGitLabRoleRotation], "rotation state is retired on the retry")
}

// Poller reconciliation runs right after the lease is acquired, so a webhook
// teardown or scaffold failure never skips demotion or bootstrap revocation, and
// its outcome is reported with the failure that stopped the uninstall.
func TestUninstall_GitLabReconcilesPollersBeforeEarlyFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(*forge.FakeClient)
		commit func(*forge.FakeClient) ScaffoldCommitFunc
		want   string
	}{
		"webhook teardown failure": {
			setup: func(c *forge.FakeClient) {
				c.ProjectHooks = map[string][]forge.ProjectHook{"acme/api": {{ID: 7, Name: GitLabWebhookName}}}
				c.Errors["DeleteProjectHook"] = errors.New("forbidden")
			},
			commit: uninstallCommitFn,
			want:   "removing webhook fast-path",
		},
		"scaffold commit failure": {
			setup:  func(*forge.FakeClient) {},
			commit: func(*forge.FakeClient) ScaffoldCommitFunc { return uninstallCommitErr(errors.New("commit canceled")) },
			want:   "removing scaffold files",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newInstalledFakeGitLabClient("acme/api")
			tc.setup(fake)
			client := &leaseProbeClient{FakeClient: fake}
			tokens := &reconcilingTokens{
				err:       errors.New("restoring the GitLab Poller identity (user ID 10)"),
				leaseHeld: func() bool { return client.leaseHeld("acme", "api") },
			}

			results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, tc.commit(fake), tokens)

			require.NoError(t, err)
			require.Len(t, results, 1)
			assert.Equal(t, 1, tokens.calls, "the Poller is reconciled before the early return")
			assert.True(t, tokens.heldOnRun, "reconciliation runs under the project lease")
			require.Error(t, results[0].Error)
			assert.Contains(t, results[0].Error.Error(), tc.want)
			assert.Contains(t, results[0].Error.Error(), "user ID 10", "the reconciliation failure is aggregated")
			assert.False(t, results[0].Success)
			assert.False(t, client.leaseHeld("acme", "api"), "the lease is released")
		})
	}
}

// An unreadable ownership preflight still stops the uninstall before any
// teardown, but only after the Poller was reconciled.
func TestUninstall_GitLabReconcilesPollersBeforeOwnershipPreflight(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	fake.VariableValues["acme/api/"+forge.VarGitLabRoleRotation] = "{not json"
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &reconcilingTokens{leaseHeld: func() bool { return client.leaseHeld("acme", "api") }}
	tokens.seed(ProjectAccessToken{Name: "fullsend-poller", Active: true})
	committed := false
	commit := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct, remove bool) error {
		committed = true
		return uninstallCommitFn(fake)(ctx, owner, repo, files, direct, remove)
	}

	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, commit, tokens)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "verifying managed-account ownership before uninstall")
	assert.Equal(t, 1, tokens.calls, "the Poller is reconciled before the preflight")
	assert.False(t, committed, "no scaffold file is removed")
	assert.Empty(t, client.triggerListHeld, "the webhook fast-path is not torn down")
	assert.Empty(t, client.varDeleteHeld, "no variable is deleted")
	assert.Empty(t, tokens.revoked, "no token is revoked")
}

// ctxRespectingClient fails trigger discovery and revocation on a canceled
// context, as the live client's requests do.
type ctxRespectingClient struct {
	*forge.FakeClient
}

func (c *ctxRespectingClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.FakeClient.ListPipelineTriggerTokens(ctx, owner, repo)
}

func (c *ctxRespectingClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, tokenID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.FakeClient.RevokePipelineTriggerToken(ctx, owner, repo, tokenID)
}

// When cancellation arrives during Poller reconciliation and the Developer
// restore fails, the managed webhook trigger is still revoked: teardown runs on
// its own context detached from the canceled uninstall context.
func TestUninstall_GitLabReconcileFailureStillRevokesTriggerAfterCancellation(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	fake.PipelineTriggerTokens["acme/api"] = []forge.PipelineTriggerToken{{ID: 5, Description: GitLabWebhookTriggerDescription, OwnerID: 10}}
	client := &ctxRespectingClient{FakeClient: fake}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tokens := &reconcilingTokens{
		err:       errors.New("restoring the GitLab Poller identity (user ID 10)"),
		leaseHeld: func() bool { return false },
	}
	tokens.onReconcile = cancel

	results, err := uninstallGitLabAcmeAPIWithTokens(ctx, client, uninstallCommitFn(fake), tokens)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "user ID 10")
	assert.Equal(t, []int64{5}, fake.RevokedTriggerTokenIDs, "the Poller-owned trigger is revoked despite the cancellation")
	assert.Empty(t, fake.PipelineTriggerTokens["acme/api"])
}
