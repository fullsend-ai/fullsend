package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// elevatingHookFake raises every project member to Maintainer once the managed
// webhook is created, as an out-of-band change after publication would.
type elevatingHookFake struct {
	webhookCLIFake
	// cancel, when set, cancels the install context at the same moment.
	cancel func()
}

func (e elevatingHookFake) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	h, err := e.webhookCLIFake.CreateProjectHook(ctx, owner, repo, hook)
	for id := range e.ProjectMemberAccess {
		e.ProjectMemberAccess[id] = forge.GitLabAccessLevelMaintainer
	}
	if e.cancel != nil {
		e.cancel()
	}
	return h, err
}

// seedTriggerAccountLedger records a decodable ledger that selects no active
// account route (the account is bootstrapping), as the webhook step reads it
// before the lifecycle settles. A ledger that does not decode fails closed.
func seedTriggerAccountLedger(c webhookCLIFake) {
	c.VariableValues["group/app/"+forge.VarGitLabTriggerAccount] = `{"version":1,"phase":"bootstrapping","branch":"main","nonce":"0123456789abcdef"}`
}

// Early containment withdraws the dispatch activation on every install, and
// only the trigger-account lifecycle records it again. On a reinstall (a ledger
// exists) the lifecycle therefore runs before the webhook, which is then judged
// against the settled route instead of the temporarily withdrawn Poller route.
// A fresh install keeps the webhook first.
func TestConvergeGitLabDispatchRoute_StageOrder(t *testing.T) {
	cases := []struct {
		name             string
		ledger           bool
		tokensAtConverge int
	}{
		{"reinstall settles the account before the webhook", true, 0},
		{"fresh install provisions the webhook first", false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newWebhookCLIFake()
			if tc.ledger {
				seedTriggerAccountLedger(c)
			}
			converged := 0
			seen := -1
			opts := &reposInstallConfig{testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error {
				converged++
				seen = len(c.CreatedTriggerTokens)
				return nil
			}}
			var buf bytes.Buffer

			require.NoError(t, convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", true, nil))

			assert.Equal(t, 1, converged)
			assert.Equal(t, tc.tokensAtConverge, seen, "webhook provisioning had run before the account stage: %d tokens", seen)
			assert.Len(t, c.CreatedProjectHooks, 1, "the webhook is provisioned in both orders")
		})
	}
}

// Convergence probes the webhook before the lifecycle settles the route, so a
// reinstall rechecks whether the webhook needs work afterwards and creates a
// missing one rather than leaving it to a refresh that never provisions.
func TestConvergeGitLabDispatchRoute_ReinstallRechecksMissingWebhook(t *testing.T) {
	c := newWebhookCLIFake()
	seedTriggerAccountLedger(c)
	opts := &reposInstallConfig{testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error { return nil }}
	var buf bytes.Buffer

	require.NoError(t, convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", false, nil))

	assert.Len(t, c.CreatedProjectHooks, 1, "a missing webhook is created after the account stage")

	// Once provisioned, the recheck finds nothing to do.
	hooks := len(c.CreatedProjectHooks)
	require.NoError(t, convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", false, nil))
	assert.Len(t, c.CreatedProjectHooks, hooks)
}

// A lifecycle failure does not stop the webhook stage on a reinstall, and both
// failures are reported.
func TestConvergeGitLabDispatchRoute_AccountFailureStillRunsWebhook(t *testing.T) {
	c := newWebhookCLIFake()
	seedTriggerAccountLedger(c)
	opts := &reposInstallConfig{testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error {
		return errors.New("account stage failed")
	}}
	var buf bytes.Buffer

	err := convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", true, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "account stage failed")
	assert.Len(t, c.CreatedProjectHooks, 1)
}

// When the webhook's owner is found elevated after publication the webhook
// step removes only the webhook, so install must contain the trigger account
// itself, and must do so on a context that outlives a canceled install.
func TestConvergeGitLabDispatchRoute_ContainsAccountWhenWebhookOwnerElevated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := elevatingHookFake{webhookCLIFake: newWebhookCLIFake(), cancel: cancel}
	seedTriggerAccountLedger(c.webhookCLIFake)
	contained := 0
	var containCtxErr error
	opts := &reposInstallConfig{
		testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error { return nil },
		testContainTriggerAccount: func(ctx context.Context, _ forge.Client, _, _ string) error {
			contained++
			containCtxErr = ctx.Err()
			return nil
		},
	}
	var buf bytes.Buffer

	err := convergeGitLabDispatchRoute(ctx, opts, c, ui.New(&buf), "group", "app", true, nil)

	require.Error(t, err)
	assert.Equal(t, 1, contained, "the trigger account is contained after the owner-safety failure")
	require.Error(t, ctx.Err(), "the install context was canceled")
	assert.NoError(t, containCtxErr, "containment runs on a context detached from the canceled install")
	assert.Empty(t, c.ProjectHooks["group/app"], "the managed webhook is removed")

	t.Run("a containment failure is reported with the webhook failure", func(t *testing.T) {
		c := elevatingHookFake{webhookCLIFake: newWebhookCLIFake()}
		opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
			return errors.New("demotion failed")
		}
		err := convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", true, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "demotion failed")
	})
}

// movingHookElevatingFake raises the trigger account to Maintainer when the
// webhook is moved onto its trigger token, as an out-of-band change after
// publication would.
type movingHookElevatingFake struct{ webhookCLIFake }

func (m movingHookElevatingFake) UpdateProjectHook(ctx context.Context, owner, repo string, hookID int64, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	h, err := m.webhookCLIFake.UpdateProjectHook(ctx, owner, repo, hookID, hook)
	m.ProjectMemberAccess[501] = forge.GitLabAccessLevelMaintainer
	return h, err
}

// seedActiveTriggerAccountRoute records a ready trigger account whose dispatch
// activation is recorded, as the lifecycle leaves it after activating the route.
func seedActiveTriggerAccountRoute(c webhookCLIFake) {
	const nonce = "0123456789abcdef"
	digest := sha256.Sum256([]byte("published-account-token"))
	c.VariableValues["group/app/"+forge.VarGitLabTriggerAccount] = `{"version":1,"phase":"ready","branch":"main","nonce":"` + nonce + `","account_id":501,"deploy_key_fingerprint":"SHA256:abc","deploy_key_id":77,"grant_entry_id":5,"trigger_token_id":9001,"trigger_token_digest":"` + hex.EncodeToString(digest[:]) + `","trigger_published":true,"publish_started":true}`
	c.VariableValues["group/app/"+forge.VarGitLabDispatchActivation] = `{"version":1,"nonce":"` + nonce + `","account_id":501,"deploy_key_id":77,"grant_entry_id":5,"trigger_token_id":9001}`
	c.PipelineTriggerTokens["group/app"] = append(c.PipelineTriggerTokens["group/app"], forge.PipelineTriggerToken{ID: 9001, Description: "fullsend-trigger-" + nonce, OwnerID: 501})
	c.ProjectMemberAccess[501] = forge.GitLabAccessLevelDeveloper
	c.ProtectedBranchRules["group/app/main"] = &forge.ProtectedBranchRule{
		PushAccessLevels:  []forge.ProtectedBranchAccess{{DeployKeyID: 77}},
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: forge.GitLabAccessLevelMaintainer}, {UserID: 1001}},
	}
	if err := c.CreateRepoSecret(context.Background(), "group", "app", forge.SecretGitLabTriggerAccountToken, "published-account-token"); err != nil {
		panic(err)
	}
}

func TestRefreshGitLabWebhookAfterPollerFailure_KeepsAccountHookWhenActivationWithdrawn(t *testing.T) {
	c := newWebhookCLIFake()
	seedActiveTriggerAccountRoute(c)
	var buf bytes.Buffer
	require.NoError(t, setupGitLabWebhookFastPath(context.Background(), c, ui.New(&buf), "group", "app", false, false))
	require.Len(t, c.CreatedProjectHooks, 1)
	require.Empty(t, c.CreatedTriggerTokens, "the account route reuses its published trigger")
	hook := c.CreatedProjectHooks[0]
	delete(c.VariableValues, "group/app/"+forge.VarGitLabDispatchActivation)

	require.NoError(t, refreshGitLabWebhookAfterPollerFailure(context.Background(), c, ui.New(&buf), "group", "app", false))
	assert.Empty(t, c.CreatedTriggerTokens, "a failed Poller grant cannot mint a Poller trigger")
	assert.Len(t, c.CreatedProjectHooks, 1)
	require.Len(t, c.ProjectHooks["group/app"], 1)
	assert.Equal(t, hook.URL, c.ProjectHooks["group/app"][0].URL)
}

func TestRefreshGitLabWebhookAfterPollerFailure_ProbesActiveAccountRoute(t *testing.T) {
	c := newWebhookCLIFake()
	seedActiveTriggerAccountRoute(c)
	c.Errors["ListProjectHooks"] = errors.New("route probe unavailable")
	err := refreshGitLabWebhookAfterPollerFailure(context.Background(), c, ui.New(&bytes.Buffer{}), "group", "app", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing project webhooks")
}

// The webhook refresh that follows the lifecycle can reach the account route
// just activated. An owner-safety failure there needs the same containment as
// the initial webhook step, on a context that outlives a canceled install.
func TestConvergeGitLabDispatchRoute_ContainsAccountWhenRefreshOwnerUnsafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := newWebhookCLIFake()
	c := movingHookElevatingFake{webhookCLIFake: base}
	contained := 0
	var containCtxErr error
	opts := &reposInstallConfig{
		testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error {
			seedActiveTriggerAccountRoute(base)
			return nil
		},
		testContainTriggerAccount: func(ctx context.Context, _ forge.Client, _, _ string) error {
			contained++
			containCtxErr = ctx.Err()
			return nil
		},
	}
	var buf bytes.Buffer

	// The cancellation lands with the refresh's webhook move.
	cancelling := cancelOnUpdateFake{movingHookElevatingFake: c, cancel: cancel}
	err := convergeGitLabDispatchRoute(ctx, opts, cancelling, ui.New(&buf), "group", "app", true, nil)

	require.Error(t, err)
	assert.Equal(t, 1, contained, "the trigger account is contained after the refresh's owner-safety failure")
	require.Error(t, ctx.Err(), "the install context was canceled")
	assert.NoError(t, containCtxErr, "containment runs on a context detached from the canceled install")
	assert.Empty(t, base.ProjectHooks["group/app"], "the managed webhook is removed")

	t.Run("a containment failure is reported with the refresh failure", func(t *testing.T) {
		base := newWebhookCLIFake()
		c := movingHookElevatingFake{webhookCLIFake: base}
		opts := &reposInstallConfig{
			testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error {
				seedActiveTriggerAccountRoute(base)
				return nil
			},
			testContainTriggerAccount: func(context.Context, forge.Client, string, string) error {
				return errors.New("demotion failed")
			},
		}
		err := convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", true, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "demotion failed")
	})
}

// cancelOnUpdateFake cancels the install context when the webhook is moved.
type cancelOnUpdateFake struct {
	movingHookElevatingFake
	cancel func()
}

func (f cancelOnUpdateFake) UpdateProjectHook(ctx context.Context, owner, repo string, hookID int64, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	h, err := f.movingHookElevatingFake.UpdateProjectHook(ctx, owner, repo, hookID, hook)
	f.cancel()
	return h, err
}

// A webhook failure that does not concern the trigger owner's access does not
// run the account containment, which would also withdraw the dispatch
// activation.
func TestConvergeGitLabDispatchRoute_OtherWebhookFailureDoesNotContain(t *testing.T) {
	c := newWebhookCLIFake()
	c.Errors = map[string]error{"CreateProjectHook": errors.New("boom")}
	contained := 0
	opts := &reposInstallConfig{
		testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error { return nil },
		testContainTriggerAccount: func(context.Context, forge.Client, string, string) error {
			contained++
			return nil
		},
	}
	var buf bytes.Buffer

	err := convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", true, nil)

	require.Error(t, err)
	assert.Zero(t, contained)
}

// A ledger that cannot be read falls back to the fresh-install order and the
// webhook still runs.
func TestConvergeGitLabDispatchRoute_UnreadableLedgerFallsBack(t *testing.T) {
	c := newWebhookCLIFake()
	c.Errors = map[string]error{"GetRepoVariable": errors.New("503")}
	opts := &reposInstallConfig{testConvergeTriggerAccount: func(context.Context, forge.Client, string, string, bool) error { return nil }}
	var buf bytes.Buffer

	_ = convergeGitLabDispatchRoute(context.Background(), opts, c, ui.New(&buf), "group", "app", true, nil)

	assert.Contains(t, buf.String(), "Could not read the GitLab trigger account ledger")
}

// Failed-repository safety reconciliation only removes the managed webhook, so
// an elevated trigger account must also be contained there: its trigger and
// published token stay valid through the trigger API otherwise.
func TestReconcileGitLabWebhookSafety_ContainsElevatedTriggerAccount(t *testing.T) {
	newFake := func() webhookCLIFake {
		c := newWebhookCLIFake()
		seedActiveTriggerAccountRoute(c)
		c.ProjectMemberAccess[501] = forge.GitLabAccessLevelMaintainer
		return c
	}
	contained := 0
	opts := &reposInstallConfig{
		testContainTriggerAccount: func(context.Context, forge.Client, string, string) error {
			contained++
			return nil
		},
	}
	var buf bytes.Buffer

	err := reconcileGitLabWebhookSafety(context.Background(), opts, newFake(), ui.New(&buf), "group", "app")

	require.Error(t, err, "the unsafe account is reported")
	assert.Equal(t, 1, contained, "the trigger account is contained")

	t.Run("a containment failure is reported", func(t *testing.T) {
		opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
			return errors.New("demotion failed")
		}
		err := reconcileGitLabWebhookSafety(context.Background(), opts, newFake(), ui.New(&buf), "group", "app")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "demotion failed")
	})

	t.Run("dry run does not contain", func(t *testing.T) {
		contained = 0
		opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
			contained++
			return nil
		}
		opts.dryRun = true
		_ = reconcileGitLabWebhookSafety(context.Background(), opts, newFake(), ui.New(&buf), "group", "app")
		assert.Zero(t, contained)
	})
}
