package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Webhook trigger credential on the trigger-account route (#8509).

const (
	webhookAccountID      = 501
	webhookAccountKeyID   = 77
	webhookAccountTrigger = 9001
	webhookAccountToken   = "glptt-account-9001"
	webhookAccountNonce   = "0123456789abcdef"
)

// seedWebhookAccountRoute records a ready trigger-account generation that owns
// triggerID and publishes token, with the dispatch activation bound to it when
// active is set. The default branch's exact rule admits the account only
// through its deploy-key push grant, and the legacy Poller-owned trigger owner
// (1001) through a per-user merge grant.
func seedWebhookAccountRoute(t *testing.T, c webhookFake, triggerID int64, token string, active bool) gitLabTriggerAccountState {
	t.Helper()
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	st := gitLabTriggerAccountState{
		Version: gitLabTriggerAccountStateVersion, Phase: gitLabTriggerPhaseReady, Branch: "main", Nonce: webhookAccountNonce,
		AccountID: webhookAccountID, DeployKeyFingerprint: "SHA256:abc", DeployKeyID: webhookAccountKeyID, GrantEntryID: 5,
		TriggerTokenID: triggerID, TriggerTokenDigest: triggerTokenDigest(token), TriggerPublished: true, PublishStarted: true,
	}
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	c.VariableValues[prefix+forge.VarGitLabTriggerAccount] = string(raw)
	if active {
		op := &gitLabTriggerAccountOp{st: st}
		rawAct, err := json.Marshal(op.activationFor())
		require.NoError(t, err)
		c.VariableValues[prefix+forge.VarGitLabDispatchActivation] = string(rawAct)
	} else {
		delete(c.VariableValues, prefix+forge.VarGitLabDispatchActivation)
	}
	key := webhookTestOwner + "/" + webhookTestRepo
	var kept []forge.PipelineTriggerToken
	for _, tr := range c.PipelineTriggerTokens[key] {
		if tr.OwnerID != webhookAccountID {
			kept = append(kept, tr)
		}
	}
	c.PipelineTriggerTokens[key] = append(kept, forge.PipelineTriggerToken{ID: triggerID, Description: st.accountName(), OwnerID: webhookAccountID})
	c.ProjectMemberAccess[webhookAccountID] = forge.GitLabAccessLevelDeveloper
	c.ProtectedBranchRules[prefix+"main"] = &forge.ProtectedBranchRule{
		PushAccessLevels:  []forge.ProtectedBranchAccess{{DeployKeyID: webhookAccountKeyID}},
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: forge.GitLabAccessLevelMaintainer}, {UserID: 1001}},
	}
	require.NoError(t, c.CreateRepoSecret(context.Background(), webhookTestOwner, webhookTestRepo, forge.SecretGitLabTriggerAccountToken, token))
	return st
}

func accountHookURL(token string) string {
	return webhookTestBase + "/api/v4/projects/42/ref/main/trigger/pipeline?token=" + token
}

func webhookNeedsProvisioning(t *testing.T, c webhookFake) bool {
	t.Helper()
	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	return needs
}

func webhookRouteStale(t *testing.T, c webhookFake) bool {
	t.Helper()
	stale, err := GitLabWebhookTriggerRouteStale(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	return stale
}

type activationWithdrawOnLease struct{ webhookFake }

func (c activationWithdrawOnLease) AcquireProjectLease(ctx context.Context, owner, repo, name, holder string) (bool, error) {
	delete(c.VariableValues, owner+"/"+repo+"/"+forge.VarGitLabDispatchActivation)
	return c.webhookFake.AcquireProjectLease(ctx, owner, repo, name, holder)
}

func TestEnsureGitLabWebhookAccountRouteAfterPollerFailure_LeaseGuardsRoute(t *testing.T) {
	ctx := context.Background()
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.hooks(), 1)
	oldURL := c.hooks()[0].URL

	// Another installer withdraws activation just as this callback takes
	// the project lease. A pre-lease route check would allow the later
	// ensure to fall through to a newly minted Poller trigger.
	withdrawing := activationWithdrawOnLease{webhookFake: c}
	res, err := EnsureGitLabWebhookAccountRouteAfterPollerFailure(ctx, withdrawing, nil, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Equal(t, "none", res.Action)
	assert.Empty(t, c.CreatedTriggerTokens)
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, oldURL, c.hooks()[0].URL)
}

func TestEnsureGitLabWebhookAccountRouteAfterPollerFailure_RepairsActiveRoute(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	res, err := EnsureGitLabWebhookAccountRouteAfterPollerFailure(context.Background(), c, nil, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Empty(t, c.CreatedTriggerTokens)
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, accountHookURL(webhookAccountToken), c.hooks()[0].URL)
}

func TestGitLabWebhookAccountRoute_InstallUsesAccountToken(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	assert.Empty(t, c.CreatedTriggerTokens, "nothing is minted for the Poller on the trigger-account route")
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, accountHookURL(webhookAccountToken), c.hooks()[0].URL)
	assert.Contains(t, c.hooks()[0].Description, "(trigger token ID 9001)")
	assert.Empty(t, c.variable(forge.SecretTriggerToken))
	assert.Len(t, c.variable(forge.SecretWebhookSecret), 64)
	assertNoCredentialLeak(t, c, res)
	for _, d := range res.Details {
		assert.NotContains(t, d, webhookAccountToken)
	}
	assert.False(t, webhookNeedsProvisioning(t, c))
	assert.False(t, webhookRouteStale(t, c))

	// Reinstall is a no-op.
	again := ensureWebhook(t, c, false, false)
	assert.Equal(t, "none", again.Action)
	assert.Empty(t, again.Details)
	assert.Len(t, c.CreatedProjectHooks, 1)
	assert.Empty(t, c.UpdatedProjectHooks)
}

func TestGitLabWebhookAccountRoute_MovesPollerOwnedWebhook(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	require.Len(t, c.CreatedTriggerTokens, 1)
	legacyID := c.CreatedTriggerTokens[0].ID
	require.NotEmpty(t, c.variable(forge.SecretTriggerToken))

	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	assert.True(t, webhookNeedsProvisioning(t, c))
	assert.True(t, webhookRouteStale(t, c))

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	assert.Len(t, c.CreatedTriggerTokens, 1, "no replacement Poller-owned trigger is minted")
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, accountHookURL(webhookAccountToken), c.hooks()[0].URL)
	assert.Contains(t, c.hooks()[0].Description, "(trigger token ID 9001)")
	assert.Equal(t, []int64{legacyID}, c.RevokedTriggerTokenIDs)
	assert.Empty(t, c.variable(forge.SecretTriggerToken))
	assert.Contains(t, strings.Join(res.Details, "\n"), "Moved the webhook to the trigger account's pipeline trigger token (ID 9001")
	assertNoCredentialLeak(t, c, res)
	assert.False(t, webhookNeedsProvisioning(t, c))
	assert.False(t, webhookRouteStale(t, c))
}

func TestGitLabWebhookAccountRoute_DryRunChangesNothing(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	hookBefore := c.hooks()[0]

	res := ensureWebhook(t, c, false, true)

	assert.Equal(t, "would-update", res.Action)
	joined := strings.Join(res.Details, "\n")
	assert.Contains(t, joined, "Would move the webhook")
	assert.Contains(t, joined, "Would revoke Poller-owned pipeline trigger token")
	assert.Contains(t, joined, "Would delete the "+forge.SecretTriggerToken)
	assert.Equal(t, hookBefore, c.hooks()[0])
	assert.Empty(t, c.RevokedTriggerTokenIDs)
	assert.NotEmpty(t, c.variable(forge.SecretTriggerToken))
}

func TestGitLabWebhookAccountRoute_InterruptedCleanupIsFinished(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	legacyID := c.CreatedTriggerTokens[0].ID
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)

	c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("503 unavailable"), "DeleteRepoSecret": errors.New("503 unavailable")}
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.Equal(t, accountHookURL(webhookAccountToken), c.hooks()[0].URL, "the webhook moves before the Poller-owned credential is retired")
	assert.True(t, webhookNeedsProvisioning(t, c), "a leftover Poller-owned credential is work")

	c.Errors = nil
	res := ensureWebhook(t, c, false, false)
	assert.Equal(t, "update", res.Action)
	assert.Equal(t, []int64{legacyID}, c.RevokedTriggerTokenIDs)
	assert.Empty(t, c.variable(forge.SecretTriggerToken))
	assert.Empty(t, c.UpdatedProjectHooks[1:], "the already-moved webhook is not updated again")
	assert.False(t, webhookNeedsProvisioning(t, c))
}

func TestGitLabWebhookAccountRoute_AccountRotationRepointsWebhook(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)

	// The trigger-account lifecycle rotated: a new trigger and value replace
	// the recorded ones and the activation is bound to the new generation.
	seedWebhookAccountRoute(t, c, 9002, "glptt-account-9002", true)
	assert.True(t, webhookRouteStale(t, c))
	assert.True(t, webhookNeedsProvisioning(t, c))

	res := ensureWebhook(t, c, false, false)
	assert.Equal(t, "update", res.Action)
	assert.Equal(t, accountHookURL("glptt-account-9002"), c.hooks()[0].URL)
	assert.Contains(t, c.hooks()[0].Description, "(trigger token ID 9002)")
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Empty(t, c.RevokedTriggerTokenIDs, "the webhook never revokes the account's trigger")
	assert.False(t, webhookRouteStale(t, c))
}

func TestGitLabWebhookAccountRoute_RotateFlagPointsToAccountRotation(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)

	res := ensureWebhook(t, c, true, false)

	assert.Equal(t, "update", res.Action)
	require.Len(t, res.Details, 1)
	assert.Contains(t, res.Details[0], "--rotate-gitlab-trigger-account")
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Empty(t, c.RevokedTriggerTokenIDs)
}

func TestGitLabWebhookAccountRoute_WithdrawnActivationReturnsToPollerTrigger(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)

	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, false)
	assert.True(t, webhookRouteStale(t, c))
	assert.True(t, webhookNeedsProvisioning(t, c))

	res := ensureWebhook(t, c, false, false)
	assert.Equal(t, "update", res.Action)
	require.Len(t, c.CreatedTriggerTokens, 1)
	legacy := c.CreatedTriggerTokens[0]
	assert.Equal(t, accountHookURL(legacy.Token), c.hooks()[0].URL)
	assert.Equal(t, legacy.Token, c.variable(forge.SecretTriggerToken))
	assert.Empty(t, c.RevokedTriggerTokenIDs, "the account's trigger is left to its lifecycle")
	assert.False(t, webhookRouteStale(t, c))
}

func TestGitLabWebhookAccountRoute_DefersWithoutUsablePublishedToken(t *testing.T) {
	cases := map[string]struct {
		mutate func(c webhookFake)
		want   string
		// tearsDown marks a state the existing safety reconciliation already
		// removes the Poller-owned fast path for.
		tearsDown bool
	}{
		"sink missing": {
			mutate: func(c webhookFake) {
				require.NoError(t, c.DeleteRepoSecret(context.Background(), webhookTestOwner, webhookTestRepo, forge.SecretGitLabTriggerAccountToken))
			},
			want: "is not published",
		},
		"recorded trigger gone": {
			mutate: func(c webhookFake) {
				key := webhookTestOwner + "/" + webhookTestRepo
				var kept []forge.PipelineTriggerToken
				for _, tr := range c.PipelineTriggerTokens[key] {
					if tr.ID != webhookAccountTrigger {
						kept = append(kept, tr)
					}
				}
				c.PipelineTriggerTokens[key] = kept
			},
			want: "no longer exists",
		},
		"recorded trigger owned by someone else": {
			mutate: func(c webhookFake) {
				key := webhookTestOwner + "/" + webhookTestRepo
				for i, tr := range c.PipelineTriggerTokens[key] {
					if tr.ID == webhookAccountTrigger {
						c.PipelineTriggerTokens[key][i].OwnerID = 1001
					}
				}
			},
			want: "is not owned by the trigger account",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			legacyHook := c.hooks()[0]
			seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
			tc.mutate(c)

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, strings.Join(res.Details, "\n"), tc.want)
			for _, h := range c.hooks() {
				assert.NotContains(t, h.URL, webhookAccountToken)
			}
			assert.True(t, webhookNeedsProvisioning(t, c))
			if tc.tearsDown {
				return
			}
			require.Len(t, c.hooks(), 1)
			assert.Equal(t, legacyHook.URL, c.hooks()[0].URL, "an existing webhook keeps working")
			assert.Empty(t, c.RevokedTriggerTokenIDs)
		})
	}
}

func TestGitLabWebhookAccountRoute_ElevatedAccountTearsWebhookDown(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.hooks(), 1)

	c.ProjectMemberAccess[webhookAccountID] = forge.GitLabAccessLevelMaintainer
	assert.True(t, webhookNeedsProvisioning(t, c))
	res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err), "the caller must contain the trigger account's trigger and token")

	assert.Equal(t, "update", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "pipeline trigger token ID 9001 is owned by a user with Maintainer or Owner access")
	assert.Empty(t, c.hooks(), "no managed webhook may carry a trigger owned above Developer")
	assert.Empty(t, c.RevokedTriggerTokenIDs, "the account's trigger is contained by its own lifecycle")
}

// The account trigger's owner ceiling does not depend on the dispatch
// activation: a ready, published ledger keeps its bearer live through the
// trigger API even when the activation is absent, so an elevated owner is
// reported for containment and the webhook is removed.
func TestGitLabWebhookSafety_AccountTriggerCheckedWithoutActivation(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.hooks(), 1)

	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, false)
	c.ProjectMemberAccess[webhookAccountID] = forge.GitLabAccessLevelMaintainer

	res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err), "the caller must contain the trigger account")
	assert.Equal(t, "update", res.Action)
	assert.Empty(t, c.hooks(), "a webhook naming the elevated account's trigger is removed")
}

// A dispatch activation that is absent does not make a Developer-owned account
// trigger unsafe: only the privilege ceiling is enforced for it.
func TestGitLabWebhookSafety_AccountTriggerWithoutActivationWithinCeilingIsSafe(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, false)

	problem, accountUnsafe, err := gitlabTriggerSafetyCheck(context.Background(), c, webhookTestOwner, webhookTestRepo, "main")

	require.NoError(t, err)
	assert.Empty(t, problem)
	assert.False(t, accountUnsafe)
}

func TestGitLabWebhookSafety_ProjectDriftRequestsAccountContainment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(webhookFake)
	}{
		{"extra protected ref", func(c webhookFake) {
			c.ProtectedBranches[webhookTestOwner+"/"+webhookTestRepo+"/release"] = true
		}},
		{"unreadable protected refs", func(c webhookFake) {
			c.Errors = map[string]error{"ListProtectedBranches": errors.New("503 unavailable")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newWebhookFake()
			seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
			ensureWebhook(t, c, false, false)
			tc.mutate(c)

			_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

			require.Error(t, err)
			assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err), "the installer must contain the published account credential")
			assert.Empty(t, c.hooks())
		})
	}
}

// A failed safety read must not drop the containment signal: removing the
// webhook does not invalidate a published account bearer whose safety cannot be
// verified.
func TestGitLabWebhookSafety_UnreadableInvariantsRequestAccountContainment(t *testing.T) {
	for _, method := range []string{"ListPipelineTriggerTokens", "ListProjectHooks", "GetRepo"} {
		t.Run(method, func(t *testing.T) {
			c := newWebhookFake()
			seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
			ensureWebhook(t, c, false, false)
			c.Errors = map[string]error{method: errors.New("503 unavailable")}

			_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

			require.Error(t, err)
			assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err), "the installer must contain the published account credential")
		})
	}
	t.Run("unreadable ledger", func(t *testing.T) {
		c := newWebhookFake()
		seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
		c.Errors = map[string]error{"GetRepoVariable": errors.New("503 unavailable")}

		_, accountUnsafe, err := gitlabTriggerSafetyCheck(context.Background(), c, webhookTestOwner, webhookTestRepo, "main")

		require.Error(t, err)
		assert.True(t, accountUnsafe)
	})
	t.Run("no published credential", func(t *testing.T) {
		c := newWebhookFake()
		c.Errors = map[string]error{"ListPipelineTriggerTokens": errors.New("503 unavailable")}

		_, accountUnsafe, err := gitlabTriggerSafetyCheck(context.Background(), c, webhookTestOwner, webhookTestRepo, "main")

		require.Error(t, err)
		assert.False(t, accountUnsafe, "a project without a published account bearer needs no containment")
	})
}

func TestGitLabTriggerAccountCredentialPublished_RetiredGeneration(t *testing.T) {
	c := newWebhookFake()
	st := seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, false)
	st.Phase = gitLabTriggerPhaseRotating
	st.Nonce = "fedcba9876543210"
	st.TriggerPublished = false
	st.PublishStarted = false
	st.Retired = []gitLabTriggerAccountState{{
		Version: gitLabTriggerAccountStateVersion, Phase: gitLabTriggerPhaseReady,
		Branch: "main", Nonce: webhookAccountNonce, AccountID: webhookAccountID,
		TriggerTokenID: webhookAccountTrigger, PublishStarted: true, TriggerPublished: true,
	}}
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.VarGitLabTriggerAccount] = string(raw)

	published, err := gitlabTriggerAccountCredentialPublished(context.Background(), c, webhookTestOwner, webhookTestRepo)

	require.NoError(t, err)
	assert.True(t, published, "a retired generation can still own the published bearer")

	t.Run("absent ledger", func(t *testing.T) {
		absent := newWebhookFake()
		published, err := gitlabTriggerAccountCredentialPublished(context.Background(), absent, webhookTestOwner, webhookTestRepo)
		require.NoError(t, err)
		assert.False(t, published)
	})
	t.Run("invalid ledger", func(t *testing.T) {
		c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.VarGitLabTriggerAccount] = "not json"
		published, err := gitlabTriggerAccountCredentialPublished(context.Background(), c, webhookTestOwner, webhookTestRepo)
		require.Error(t, err)
		assert.False(t, published)
	})
	t.Run("unreadable ledger", func(t *testing.T) {
		c.Errors = map[string]error{"GetRepoVariable": errors.New("503 unavailable")}
		published, err := gitlabTriggerAccountCredentialPublished(context.Background(), c, webhookTestOwner, webhookTestRepo)
		require.Error(t, err)
		assert.False(t, published)
	})
}

func TestGitLabWebhookAccountRoute_RejectsCopiedPollerToken(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	oldToken := c.variable(forge.SecretTriggerToken)
	require.NotEmpty(t, oldToken)
	oldHook := c.hooks()[0]
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, oldToken, true)

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "matches the Poller-owned trigger credential")
	assert.Equal(t, oldHook.URL, c.hooks()[0].URL)
	assert.Empty(t, c.RevokedTriggerTokenIDs)
	assert.Equal(t, oldToken, c.variable(forge.SecretTriggerToken))

	t.Run("webhook URL still proves the token belongs to another trigger after the Poller variable is removed", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		oldToken := c.variable(forge.SecretTriggerToken)
		oldHook := c.hooks()[0]
		require.NoError(t, c.DeleteRepoSecret(context.Background(), webhookTestOwner, webhookTestRepo, forge.SecretTriggerToken))
		seedWebhookAccountRoute(t, c, webhookAccountTrigger, oldToken, true)

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Contains(t, strings.Join(res.Details, "\n"), "matches a webhook credential attributed to another trigger")
		require.Len(t, c.hooks(), 1)
		assert.Equal(t, oldHook.URL, c.hooks()[0].URL)
		assert.Empty(t, c.RevokedTriggerTokenIDs)
	})
}

// An already-active account webhook whose published token loses its protection
// after convergence is removed and reported as unsafe, so repos install
// contains the account instead of leaving the webhook dispatching with a
// possibly exposed bearer.
func TestGitLabWebhookAccountRoute_UnsafeTokenProtectionTearsDownAndRequestsContainment(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	for name, prot := range map[string]forge.SecretProtection{
		"unprotected": {Exists: true, Masked: true},
		"unmasked":    {Exists: true, Protected: true},
		"file typed":  {Exists: true, Masked: true, Protected: true, FileType: true},
		"env scoped":  {Exists: true, Masked: true, Protected: true, EnvironmentScoped: true},
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
			ensureWebhook(t, c, false, false)
			require.Len(t, c.hooks(), 1)
			c.SecretProtections[prefix+forge.SecretGitLabTriggerAccountToken] = prot

			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

			require.Error(t, err)
			assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err), "the caller must contain the trigger account")
			assert.NotContains(t, err.Error(), webhookAccountToken)
			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, strings.Join(res.Details, "\n"), "not a masked, protected, wildcard-scoped")
			assert.Empty(t, c.hooks(), "no webhook may keep dispatching with a possibly exposed bearer")
		})
	}

	t.Run("dry run changes nothing", func(t *testing.T) {
		c := newWebhookFake()
		seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
		ensureWebhook(t, c, false, false)
		c.SecretProtections[prefix+forge.SecretGitLabTriggerAccountToken] = forge.SecretProtection{Exists: true, Masked: true}

		res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, true)

		require.NoError(t, err)
		assert.Equal(t, "deferred", res.Action)
		assert.Len(t, c.hooks(), 1)
	})
}

// The ledger fingerprint is the positive binding between the variable value
// and the account-owned trigger. A copied Developer or Maintainer bearer must
// not survive in an existing hook or be installed in a new one.
func TestGitLabWebhookAccountRoute_CopiedTokenTearsDownAndRequestsContainment(t *testing.T) {
	for _, level := range []int{forge.GitLabAccessLevelDeveloper, forge.GitLabAccessLevelMaintainer} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			c := newWebhookFake()
			seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
			ensureWebhook(t, c, false, false)
			key := webhookTestOwner + "/" + webhookTestRepo
			c.PipelineTriggerTokens[key] = append(c.PipelineTriggerTokens[key], forge.PipelineTriggerToken{ID: 7000, Description: "someone else's", OwnerID: 7000})
			c.ProjectMemberAccess[7000] = level
			c.VariableValues[key+"/"+forge.SecretGitLabTriggerAccountToken] = "copied-other-bearer"
			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
			require.Error(t, err)
			assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err))
			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, strings.Join(res.Details, "\n"), "does not match the recorded trigger token fingerprint")
			assert.Empty(t, c.hooks())
			assert.NotContains(t, err.Error(), "copied-other-bearer")
		})
	}
}

// A malformed or blank ledger must not hide an account-owned webhook from the
// safety checks: revocation-only reconciliation removes the managed webhook.
func TestReconcileGitLabWebhookSafety_UndecodableAccountLedgerRevokesWebhook(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	for _, bad := range []string{"not json", ""} {
		c := newWebhookFake()
		seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
		ensureWebhook(t, c, false, false)
		require.Len(t, c.hooks(), 1)
		c.ProjectMemberAccess[webhookAccountID] = forge.GitLabAccessLevelMaintainer
		c.VariableValues[prefix+forge.VarGitLabTriggerAccount] = bad

		_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.Error(t, err, "the unreadable ledger is reported: %q", bad)
		assert.Empty(t, c.hooks(), "the account-owned webhook is removed instead of left unchecked: %q", bad)
	}
}

// An environment-scoped FULLSEND_TRIGGER_TOKEN cannot be deleted by the
// wildcard-scoped delete, so it must not keep the account route from settling.
func TestGitLabWebhookAccountRoute_EnvironmentScopedTriggerVariableDoesNotBlockConvergence(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)
	// Listed by the variable inventory, but absent from the wildcard scope.
	c.VariableValues[prefix+forge.SecretTriggerToken] = "env-scoped-value"

	assert.False(t, webhookNeedsProvisioning(t, c), "the account route is settled")
	res := ensureWebhook(t, c, false, false)
	assert.Equal(t, "none", res.Action)
	assert.Empty(t, c.DeletedSecrets, "the environment-scoped variable is left untouched")
}

func TestGitLabWebhookAccountRoute_UninstallTeardownRemovesWebhookOnly(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)

	td, err := TeardownGitLabWebhookFastPath(context.Background(), c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)

	assert.Equal(t, 1, td.HooksDeleted)
	assert.Zero(t, td.TriggersRevoked, "the trigger account removal revokes its own trigger")
	assert.Empty(t, c.hooks())
	assert.Empty(t, c.RevokedTriggerTokenIDs)
}

func TestGitLabWebhookAccountRoute_ReadFailuresFailClosed(t *testing.T) {
	for _, method := range []string{"GetRepoVariable"} {
		t.Run(method, func(t *testing.T) {
			c := newWebhookFake()
			seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
			c.Errors = map[string]error{method: errors.New("503 unavailable")}

			_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
			require.Error(t, err)
			assert.Empty(t, c.CreatedTriggerTokens, "nothing is minted when the route cannot be read")

			_, err = GitLabWebhookTriggerRouteStale(context.Background(), webhookFake{FakeClient: c.FakeClient, base: webhookTestBase}, webhookTestBase, webhookTestOwner, webhookTestRepo)
			assert.Error(t, err, "the route is read even without a managed webhook, and an unreadable route is not reported as settled")
		})
	}
}

func TestGitLabWebhookAccountRoute_ProtectionLookupErrorIsRedacted(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.hooks(), 1, "the test starts with a live account-owned webhook")
	client := accountProtectionFailClient{webhookFake: c, err: errors.New("500: value " + webhookAccountToken)}

	_, err := EnsureGitLabWebhookFastPath(context.Background(), client, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), webhookAccountToken)
	assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err), "the caller must contain the published account credential")
	assert.Empty(t, c.hooks(), "the managed webhook cannot keep dispatching with unverified protection")

	_, err = GitLabWebhookNeedsProvisioning(context.Background(), client, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), webhookAccountToken)
}

type accountProtectionFailClient struct {
	webhookFake
	err error
}

func (c accountProtectionFailClient) GetRepoSecretProtection(ctx context.Context, owner, repo, name string) (forge.SecretProtection, error) {
	if name == forge.SecretGitLabTriggerAccountToken {
		return forge.SecretProtection{}, c.err
	}
	return c.webhookFake.GetRepoSecretProtection(ctx, owner, repo, name)
}

func TestLoadGitLabWebhookTriggerAccount_RequiresBoundActivation(t *testing.T) {
	ctx := context.Background()
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	c := newWebhookFake()

	acct, err := loadGitLabWebhookTriggerAccount(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Nil(t, acct, "no ledger")

	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	acct, err = loadGitLabWebhookTriggerAccount(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	require.NotNil(t, acct)
	assert.Equal(t, map[int64]int{webhookAccountID: webhookAccountKeyID}, acct.deployKeys())

	c.VariableValues[prefix+forge.VarGitLabDispatchActivation] = `{"version":1}`
	acct, err = loadGitLabWebhookTriggerAccount(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Nil(t, acct, "an activation bound to another generation")

	for _, bad := range []string{"not json", "", "  "} {
		c.VariableValues[prefix+forge.VarGitLabTriggerAccount] = bad
		acct, err = loadGitLabWebhookTriggerAccount(ctx, c, webhookTestOwner, webhookTestRepo)
		require.Error(t, err, "a present ledger that does not decode fails closed: %q", bad)
		assert.Nil(t, acct)
		assert.NotContains(t, err.Error(), "invalid character", "the decode error text is withheld")
	}

	st := gitLabTriggerAccountState{Version: 1, Phase: gitLabTriggerPhaseRotating, Branch: "main", Nonce: webhookAccountNonce, AccountID: webhookAccountID}
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	c.VariableValues[prefix+forge.VarGitLabTriggerAccount] = string(raw)
	acct, err = loadGitLabWebhookTriggerAccount(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Nil(t, acct, "a ledger that is not ready")

	var nilAcct *gitlabWebhookAccount
	assert.Nil(t, nilAcct.deployKeys())
}

func TestGitLabTriggerOwnerProblem_AcceptsAccountDeployKeyPush(t *testing.T) {
	ctx := context.Background()
	c := newWebhookFake()
	c.ProtectedBranchRules[webhookTestOwner+"/"+webhookTestRepo+"/main"] = &forge.ProtectedBranchRule{
		PushAccessLevels:  []forge.ProtectedBranchAccess{{DeployKeyID: webhookAccountKeyID}},
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: forge.GitLabAccessLevelMaintainer}},
	}
	c.ProjectMemberAccess[webhookAccountID] = forge.GitLabAccessLevelDeveloper
	trigger := []forge.PipelineTriggerToken{{ID: webhookAccountTrigger, OwnerID: webhookAccountID}}
	keys := map[int64]int{webhookAccountID: webhookAccountKeyID}

	problem, err := gitlabTriggerOwnerProblem(ctx, c, webhookTestOwner, webhookTestRepo, "main", trigger, keys)
	require.NoError(t, err)
	assert.Empty(t, problem, "the account's recorded deploy-key push grant admits its trigger")

	problem, err = gitlabTriggerOwnerProblem(ctx, c, webhookTestOwner, webhookTestRepo, "main", trigger, nil)
	require.NoError(t, err)
	assert.Contains(t, problem, "without verifiable push or merge access", "without the recorded key the grant is not tied to the owner")

	problem, err = gitlabTriggerOwnerProblem(ctx, c, webhookTestOwner, webhookTestRepo, "main", trigger, map[int64]int{webhookAccountID: 78})
	require.NoError(t, err)
	assert.NotEmpty(t, problem, "another deploy key does not admit the account")

	c.ProjectMemberAccess[webhookAccountID] = forge.GitLabAccessLevelMaintainer
	problem, err = gitlabTriggerOwnerProblem(ctx, c, webhookTestOwner, webhookTestRepo, "main", trigger, keys)
	require.NoError(t, err)
	assert.Contains(t, problem, "Maintainer or Owner access", "the deploy key never lifts the Developer ceiling")
}

func TestProtectedRuleAdmitsDeployKey(t *testing.T) {
	rule := forge.ProtectedBranchRule{
		PushAccessLevels:  []forge.ProtectedBranchAccess{{DeployKeyID: 7}},
		MergeAccessLevels: []forge.ProtectedBranchAccess{{DeployKeyID: 8}},
	}
	assert.True(t, protectedRuleAdmitsDeployKey(rule, 7))
	assert.False(t, protectedRuleAdmitsDeployKey(rule, 8), "only push grants carry deploy keys")
	assert.False(t, protectedRuleAdmitsDeployKey(rule, 0))
}

func TestGitLabWebhookTriggerRouteStale(t *testing.T) {
	ctx := context.Background()
	c := newWebhookFake()
	assert.False(t, webhookRouteStale(t, c), "no managed webhook")

	stale, err := GitLabWebhookTriggerRouteStale(ctx, c, "", webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.False(t, stale, "no base URL")

	ensureWebhook(t, c, false, false)
	assert.False(t, webhookRouteStale(t, c), "a Poller-owned webhook without an active account route")

	for _, method := range []string{"GetRepo", "ListProjectHooks", "ListPipelineTriggerTokens"} {
		c.Errors = map[string]error{method: errors.New("503 unavailable")}
		_, err := GitLabWebhookTriggerRouteStale(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo)
		assert.Error(t, err, method)
	}
	c.Errors = nil

	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	c.Errors = map[string]error{"GetRepoVariable": errors.New("503 unavailable")}
	_, err = GitLabWebhookTriggerRouteStale(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	assert.Error(t, err)
}

// A fresh install can activate the account route after the webhook step
// deferred without creating a hook. The active route with no managed webhook is
// stale, so the post-lifecycle refresh creates the account-owned webhook.
func TestGitLabWebhookTriggerRouteStale_ActiveRouteWithoutWebhook(t *testing.T) {
	c := newWebhookFake()
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, false)
	assert.False(t, webhookRouteStale(t, c), "an inactive route leaves a missing webhook to the webhook step")

	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	assert.True(t, webhookRouteStale(t, c), "an active route needs its account-owned webhook")
	assert.Empty(t, c.hooks())

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, accountHookURL(webhookAccountToken), c.hooks()[0].URL)
	assert.False(t, webhookRouteStale(t, c))
}

func TestGitLabWebhookTriggerRouteStale_ActiveHookDrift(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(c webhookFake)
	}{
		{"wrong bearer", func(c webhookFake) {
			c.ProjectHooks[webhookTestOwner+"/"+webhookTestRepo][0].URL = accountHookURL("stale-bearer")
		}},
		{"disabled", func(c webhookFake) { c.ProjectHooks[webhookTestOwner+"/"+webhookTestRepo][0].AlertStatus = "disabled" }},
		{"duplicate", func(c webhookFake) {
			key := webhookTestOwner + "/" + webhookTestRepo
			copy := c.ProjectHooks[key][0]
			copy.ID++
			c.ProjectHooks[key] = append(c.ProjectHooks[key], copy)
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			c := newWebhookFake()
			seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
			ensureWebhook(t, c, false, false)
			mutate.fn(c)
			assert.True(t, webhookRouteStale(t, c))
		})
	}
}

// repos install contains the trigger account only for failures that say the
// published trigger's owner is outside the Developer ceiling or unverifiable.
func TestGitLabWebhookTriggerOwnerUnsafe(t *testing.T) {
	for _, err := range []error{errGitLabAccountTriggerUnsafe, errGitLabTriggerOwnerElevated, errGitLabTriggerOwnerBelowDeveloper, errGitLabTriggerOwnerUnverified, errGitLabAccountTokenUnsafe} {
		assert.True(t, GitLabWebhookTriggerOwnerUnsafe(err))
		assert.True(t, GitLabWebhookTriggerOwnerUnsafe(errors.Join(errors.New("tearing down"), err)))
	}
	assert.False(t, GitLabWebhookTriggerOwnerUnsafe(nil))
	assert.False(t, GitLabWebhookTriggerOwnerUnsafe(errors.New("boom")))
}
