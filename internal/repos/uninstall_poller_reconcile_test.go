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
	// the uninstall context).
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

// Uninstall reconciles the managed Pollers under the project lease before role
// credential cleanup, and fails (retaining the manifest entry) when the
// reconciliation cannot be confirmed.
func TestUninstall_GitLabReconcilesPollersUnderLease(t *testing.T) {
	for name, reconcileErr := range map[string]error{"confirmed": nil, "unconfirmed": errors.New("restoring the GitLab Poller identity (user ID 10)")} {
		t.Run(name, func(t *testing.T) {
			fake := newInstalledFakeGitLabClient("acme/api")
			client := &leaseProbeClient{FakeClient: fake}
			tokens := &reconcilingTokens{err: reconcileErr, leaseHeld: func() bool { return client.leaseHeld("acme", "api") }}

			results, err := Uninstall(context.Background(), UninstallConfig{
				Manifest:       testGitLabManifest("acme/api"),
				Repos:          []string{"acme/api"},
				Direct:         true,
				MaxConcurrency: 1,
				GitLabTokens:   tokens,
			}, newTestClientFactory(client), uninstallCommitFn(fake), nil)

			require.NoError(t, err)
			require.Len(t, results, 1)
			assert.Equal(t, 1, tokens.calls)
			assert.True(t, tokens.heldOnRun, "reconciliation runs under the project lease")
			if reconcileErr == nil {
				assert.NoError(t, results[0].Error)
				return
			}
			require.Error(t, results[0].Error)
			assert.Contains(t, results[0].Error.Error(), "user ID 10")
		})
	}
}

// A supplied Poller credential whose owner cannot be attributed makes uninstall
// fail closed before any state is deleted: the role identity variables, the
// installed credential, and the rotation provenance a retry needs all remain,
// and no tokens are revoked.
func TestUninstall_GitLabUnresolvedSuppliedPollerLeavesIdentityStateInPlace(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &reconcilingTokens{
		err:       fmt.Errorf("%w: attribution failed", ErrPollerSuppliedUnresolved),
		leaseHeld: func() bool { return client.leaseHeld("acme", "api") },
	}
	tokens.seed(ProjectAccessToken{Name: "fullsend-poller", Active: true})
	before := len(fake.DeletedVariables)

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
		GitLabTokens:   tokens,
	}, newTestClientFactory(client), uninstallCommitFn(fake), nil)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.ErrorIs(t, results[0].Error, ErrPollerSuppliedUnresolved)
	for _, d := range fake.DeletedVariables[before:] {
		assert.False(t, isGitLabIdentityUninstallVar(d.Name), "identity variable %s must be retained", d.Name)
	}
	assert.True(t, fake.VariablesExist["acme/api/"+forge.VarGitLabRoleRotation], "rotation provenance is retained")
	assert.Empty(t, tokens.revoked, "no token is revoked")
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
	cfg := UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
		GitLabTokens:   tokens,
	}
	before := len(fake.DeletedVariables)

	results, err := Uninstall(context.Background(), cfg, newTestClientFactory(client), uninstallCommitFn(fake), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.NotErrorIs(t, results[0].Error, ErrPollerSuppliedUnresolved)
	for _, d := range fake.DeletedVariables[before:] {
		assert.False(t, isGitLabIdentityUninstallVar(d.Name), "identity variable %s must be retained", d.Name)
	}
	assert.True(t, fake.VariablesExist["acme/api/"+forge.VarGitLabRoleRotation], "rotation state is retained")
	assert.Empty(t, tokens.revoked, "no token is revoked")

	tokens.err = nil
	results, err = Uninstall(context.Background(), cfg, newTestClientFactory(client), uninstallCommitFn(fake), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
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

			results, err := Uninstall(context.Background(), UninstallConfig{
				Manifest:       testGitLabManifest("acme/api"),
				Repos:          []string{"acme/api"},
				Direct:         true,
				MaxConcurrency: 1,
				GitLabTokens:   tokens,
			}, newTestClientFactory(client), tc.commit(fake), nil)

			require.NoError(t, err)
			require.Len(t, results, 1)
			assert.Equal(t, 1, tokens.calls, "the Poller is reconciled before the early return")
			assert.True(t, tokens.heldOnRun, "reconciliation runs under the project lease")
			require.Error(t, results[0].Error)
			assert.Contains(t, results[0].Error.Error(), tc.want)
			assert.Contains(t, results[0].Error.Error(), "user ID 10", "the reconciliation failure is aggregated")
			assert.False(t, results[0].Success)
		})
	}
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

	results, err := Uninstall(ctx, UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
		GitLabTokens:   tokens,
	}, newTestClientFactory(client), uninstallCommitFn(fake), nil)

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "user ID 10")
	assert.Equal(t, []int64{5}, fake.RevokedTriggerTokenIDs, "the Poller-owned trigger is revoked despite the cancellation")
	assert.Empty(t, fake.PipelineTriggerTokens["acme/api"])
}
