package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const retiringTriggerID = int64(70)

func triggerKey() string { return webhookTestOwner + "/" + webhookTestRepo }

func addRetiringTrigger(c webhookFake, tr forge.PipelineTriggerToken) {
	c.PipelineTriggerTokens[triggerKey()] = append(c.PipelineTriggerTokens[triggerKey()], tr)
}

// legacyWebhookFake installs the Poller-owned webhook and trigger, owned by the
// Poller account a later cutover records as current, so the install keeps it
// instead of rotating.
func legacyWebhookFake(t *testing.T) webhookFake {
	t.Helper()
	c := newWebhookFake()
	ownedBy(c, cutoverNewUID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	return c
}

func hasTrigger(c webhookFake, id int64) bool {
	for _, tr := range c.triggers() {
		if tr.ID == id {
			return true
		}
	}
	return false
}

// On the completed trigger-account route FULLSEND_TRIGGER_TOKEN is absent, so
// the legacy active-trigger probe resolves nothing. Retirement still runs
// because the ledger and the webhook's account token identify the trigger in
// use, and it keeps the account's trigger.
func TestEnsureGitLabWebhookFastPath_AccountRouteRetiresSupersededGeneration(t *testing.T) {
	c, ro := retirementFixture(t)
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	addRetiringTrigger(c, forge.PipelineTriggerToken{ID: retiringTriggerID, OwnerID: webhookPollerUserID})
	seedDefaultBranchGrants(t, c)

	res, err := ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)

	assert.Equal(t, "update", res.Action)
	assert.Empty(t, c.variable(forge.SecretTriggerToken), "the legacy trigger token is not used on this route")
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, accountHookURL(webhookAccountToken), c.hooks()[0].URL)
	assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID}, generationState(t, c))
	assert.False(t, hasTrigger(c, retiringTriggerID))
	assert.True(t, hasTrigger(c, webhookAccountTrigger), "the account's trigger is never revoked")
	assert.Contains(t, strings.Join(res.Details, "\n"), "Retired the superseded GitLab Poller generation (user ID 4004)")
	assert.Contains(t, mergeGrantUsers(defaultBranchRule(t, c)), webhookPollerUserID, "the merge grant stays")
	assert.Empty(t, c.RevokedProtectedBranchMergeEntries)

	// Nothing more to do: the route is quiet and stays retired.
	again, err := ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Equal(t, "none", again.Action)
	assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
}

// A retirement that cannot finish on the account route never undoes the
// account-owned webhook.
func TestEnsureGitLabWebhookFastPath_AccountRouteRetirementFailureKeepsRoute(t *testing.T) {
	c, ro := retirementFixture(t)
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
	ro.retireErr = assert.AnError

	res, err := ensureWebhookAsPollerOn(c, ro)
	require.ErrorContains(t, err, "stays recorded as retiring")
	assert.Contains(t, strings.Join(res.Details, "\n"), "did not finish and is retried by the next install")
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, accountHookURL(webhookAccountToken), c.hooks()[0].URL)
	assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
}

// While the dispatch activation is deferred the installer-owned Poller trigger
// stays the webhook's trigger, and retirement still converges: the retiring
// account's own trigger goes, the one the webhook uses stays.
func TestEnsureGitLabWebhookFastPath_PollerFallbackRetiresWhenActivationDeferred(t *testing.T) {
	c, ro := seedRetirement(t, legacyWebhookFake(t))
	require.Len(t, c.triggers(), 1)
	activeID := c.triggers()[0].ID
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, false)
	rule := c.ProtectedBranchRules[triggerKey()+"/main"]
	rule.MergeAccessLevels = append(rule.MergeAccessLevels, forge.ProtectedBranchAccess{UserID: int(cutoverNewUID)})
	addRetiringTrigger(c, forge.PipelineTriggerToken{ID: retiringTriggerID, OwnerID: webhookPollerUserID})

	_, err := ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)

	assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
	assert.Zero(t, generationState(t, c).RetiringUserID)
	assert.True(t, hasTrigger(c, activeID), "the trigger the webhook uses is kept")
	assert.False(t, hasTrigger(c, retiringTriggerID))
	require.Len(t, c.hooks(), 1)
}

// The webhook's trigger is read again immediately before revocation: when the
// webhook now uses a trigger the retiring account owns, nothing is revoked.
func TestRetireSupersededPollerGeneration_RefreshedEvidenceProtectsWebhookTrigger(t *testing.T) {
	c, ro := retirementFixture(t)
	addRetiringTrigger(c, forge.PipelineTriggerToken{ID: retiringTriggerID, OwnerID: webhookPollerUserID})
	addRetiringTrigger(c, forge.PipelineTriggerToken{ID: 72, OwnerID: webhookPollerUserID})

	calls := 0
	res, err := runRetirementWith(t, c, ro, func(context.Context) ([]int64, string, error) {
		calls++
		return []int64{72}, "", nil // the webhook was moved onto trigger 72 since the earlier probe
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, res.waiting, "the webhook uses pipeline trigger token ID(s) 72")
	assert.Empty(t, c.RevokedTriggerTokenIDs, "nothing is revoked while the webhook uses a retiring trigger")
	assert.Empty(t, ro.retired)
	assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
}

func TestCurrentWebhookTriggerEvidence(t *testing.T) {
	ctx := context.Background()
	t.Run("legacy webhook protects its trigger", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		activeID := c.triggers()[0].ID
		protected, unresolved, err := currentWebhookTriggerEvidence(c, webhookTestBase, webhookTestOwner, webhookTestRepo)(ctx)
		require.NoError(t, err)
		assert.Empty(t, unresolved)
		assert.Contains(t, protected, activeID)
	})
	t.Run("account webhook protects the account trigger without a legacy token", func(t *testing.T) {
		c := newWebhookFake()
		seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)
		ensureWebhook(t, c, false, false)
		protected, unresolved, err := currentWebhookTriggerEvidence(c, webhookTestBase, webhookTestOwner, webhookTestRepo)(ctx)
		require.NoError(t, err)
		assert.Empty(t, unresolved)
		assert.Contains(t, protected, int64(webhookAccountTrigger))
	})
	t.Run("no webhook leaves nothing to protect", func(t *testing.T) {
		c := newWebhookFake()
		protected, unresolved, err := currentWebhookTriggerEvidence(c, webhookTestBase, webhookTestOwner, webhookTestRepo)(ctx)
		require.NoError(t, err)
		assert.Empty(t, unresolved)
		assert.Empty(t, protected)
	})
	t.Run("webhook with an unknown credential is unresolved", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.NoError(t, c.DeleteRepoSecret(ctx, webhookTestOwner, webhookTestRepo, forge.SecretTriggerToken))
		_, unresolved, err := currentWebhookTriggerEvidence(c, webhookTestBase, webhookTestOwner, webhookTestRepo)(ctx)
		require.NoError(t, err)
		assert.Contains(t, unresolved, "cannot be identified")
	})
	t.Run("read failures are reported", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.Errors["ListRepoVariables"] = assert.AnError
		_, _, err := currentWebhookTriggerEvidence(c, webhookTestBase, webhookTestOwner, webhookTestRepo)(ctx)
		require.Error(t, err)
	})
}

// A trigger whose owner GitLab omits is revoked only on evidence that it is a
// fullsend-managed webhook trigger no webhook uses; any other ownerless
// trigger keeps retirement waiting with an explicit operator step.
func TestRetireSupersededPollerGeneration_UnknownOwnerTriggers(t *testing.T) {
	t.Run("managed webhook trigger is revoked", func(t *testing.T) {
		c, ro := retirementFixture(t)
		addRetiringTrigger(c, forge.PipelineTriggerToken{ID: 80, Description: GitLabWebhookTriggerDescription})
		addRetiringTrigger(c, forge.PipelineTriggerToken{ID: retiringTriggerID, OwnerID: webhookPollerUserID})
		res, err := runRetirement(t, c, ro, 0)
		require.NoError(t, err)
		assert.Empty(t, res.waiting)
		assert.ElementsMatch(t, []int64{80, retiringTriggerID}, c.RevokedTriggerTokenIDs)
		assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
		assert.Zero(t, generationState(t, c).RetiringUserID)
	})
	t.Run("unattributable trigger waits with an operator path", func(t *testing.T) {
		c, ro := retirementFixture(t)
		addRetiringTrigger(c, forge.PipelineTriggerToken{ID: 81, Description: "someone else's trigger"})
		addRetiringTrigger(c, forge.PipelineTriggerToken{ID: retiringTriggerID, OwnerID: webhookPollerUserID})
		res, err := runRetirement(t, c, ro, 0)
		require.NoError(t, err)
		assert.Contains(t, res.waiting, "does not report the owner of pipeline trigger token ID(s) 81")
		assert.Contains(t, res.waiting, "Settings → CI/CD → Pipeline trigger tokens")
		assert.Empty(t, c.RevokedTriggerTokenIDs, "nothing is changed while a trigger cannot be attributed")
		assert.Empty(t, ro.retired)
		assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
	})
	t.Run("unattributable trigger the webhook uses waits", func(t *testing.T) {
		c, ro := retirementFixture(t)
		addRetiringTrigger(c, forge.PipelineTriggerToken{ID: 82, Description: GitLabWebhookTriggerDescription})
		res, err := runRetirementWith(t, c, ro, staticTriggerEvidence("", 82))
		require.NoError(t, err)
		assert.Contains(t, res.waiting, "the webhook uses pipeline trigger token ID(s) 82")
		assert.Empty(t, c.RevokedTriggerTokenIDs)
		assert.Empty(t, ro.retired)
	})
	t.Run("revoked managed trigger that stays listed fails", func(t *testing.T) {
		c, ro := retirementFixture(t)
		addRetiringTrigger(c, forge.PipelineTriggerToken{ID: 80, Description: GitLabWebhookTriggerDescription})
		_, err := runRetirement(t, &triggerRevokeClient{webhookFake: c, ignoreRevoke: true}, ro, 0)
		require.ErrorContains(t, err, "pipeline trigger token ID 80")
		assert.Empty(t, ro.retired)
	})
}

// A persistent retirement failure never stops the webhook repair: the repaired
// webhook is published and the retirement error is still returned, so the next
// install retries.
func TestEnsureGitLabWebhookFastPath_RetirementFailureDoesNotBlockWebhookRepair(t *testing.T) {
	c, ro := seedRetirement(t, legacyWebhookFake(t))
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectHooks[key][0].PushEvents = true // drifted webhook configuration
	require.True(t, webhookNeedsProvisioning(t, c))
	ro.retireErr = assert.AnError

	res, err := ensureWebhookAsPollerOn(c, ro)
	require.ErrorContains(t, err, "stays recorded as retiring")
	assert.Contains(t, strings.Join(res.Details, "\n"), "did not finish and is retried by the next install")
	require.Len(t, c.hooks(), 1)
	assert.False(t, c.hooks()[0].PushEvents, "the drifted webhook was repaired despite the retirement failure")
	assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)

	// A later install retires once the failure clears.
	ro.retireErr = nil
	_, err = ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Zero(t, generationState(t, c).RetiringUserID)
}

// A deleted account is confirmed gone before the record is cleared: an account
// that stays listed after the deletion request keeps it recorded as retiring.
func TestEnsureGitLabWebhookFastPath_InterruptedRetirementRetriesSafely(t *testing.T) {
	c, ro := seedRetirement(t, legacyWebhookFake(t))
	addRetiringTrigger(c, forge.PipelineTriggerToken{ID: retiringTriggerID, OwnerID: webhookPollerUserID})
	ro.retireErr = assert.AnError

	_, err := ensureWebhookAsPollerOn(c, ro)
	require.Error(t, err)
	assert.False(t, hasTrigger(c, retiringTriggerID), "triggers were revoked before the interruption")
	assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)

	ro.retireErr = nil
	_, err = ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Zero(t, generationState(t, c).RetiringUserID)
	assert.Len(t, ro.retired, 2, "the retry is idempotent")
	assert.Len(t, c.hooks(), 1)
}
