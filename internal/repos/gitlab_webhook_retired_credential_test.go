package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const retiredCredBaseURL = "https://gitlab.example.com"

// retiredCredEnv rotates the trigger account with retirement deferred, the way
// the installer does, and returns the retired and current generations'
// trigger IDs and token values.
func retiredCredEnv(t *testing.T) (e *trigEnv, oldID int64, oldToken string, newID int64, newToken string) {
	t.Helper()
	ctx := context.Background()
	e = newTrigEnv()
	e.state.Repos[0].ID = 42
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	oldToken = e.f.published[old.TriggerTokenID]
	require.NotEmpty(t, oldToken)
	cur, err := RotateGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	require.NotEqual(t, old.TriggerTokenID, cur.TriggerTokenID)
	st, ok := e.ledger(t)
	require.True(t, ok)
	require.Len(t, st.Retired, 1)
	return e, old.TriggerTokenID, oldToken, cur.TriggerTokenID, e.f.published[cur.TriggerTokenID]
}

func (e *trigEnv) addManagedHook(t *testing.T, token string, triggerID int64) {
	t.Helper()
	_, err := e.state.CreateProjectHook(context.Background(), trigOwner, trigRepo, forge.ProjectHook{
		Name:        GitLabWebhookName,
		URL:         GitLabWebhookTriggerURL(retiredCredBaseURL, 42, trigBranch, token),
		Description: gitlabWebhookDescriptionFor(triggerID),
	})
	require.NoError(t, err)
}

func (e *trigEnv) usesRetired(t *testing.T) bool {
	t.Helper()
	inUse, err := GitLabWebhookUsesRetiredTriggerCredential(context.Background(), e.state, retiredCredBaseURL, trigOwner, trigRepo)
	require.NoError(t, err)
	return inUse
}

// A rotation whose webhook fast path is deferred has no managed webhook, so
// nothing references the retired generation and retirement may proceed.
func TestGitLabWebhookUsesRetiredTriggerCredential_NoManagedWebhook(t *testing.T) {
	e, oldID, _, _, _ := retiredCredEnv(t)
	assert.False(t, e.usesRetired(t))

	require.NoError(t, RetireGitLabTriggerAccount(context.Background(), e.opts()))
	assert.NotContains(t, e.f.triggers, oldID)
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.Empty(t, st.Retired)
}

func TestGitLabWebhookUsesRetiredTriggerCredential_WebhookNamesRetiredTrigger(t *testing.T) {
	e, oldID, _, _, _ := retiredCredEnv(t)
	e.addManagedHook(t, "unrelated-token", oldID)
	assert.True(t, e.usesRetired(t))
}

func TestGitLabWebhookUsesRetiredTriggerCredential_WebhookEmbedsRetiredToken(t *testing.T) {
	e, _, oldToken, newID, _ := retiredCredEnv(t)
	// The description already names the new trigger, but the URL still carries
	// the retired bearer.
	e.addManagedHook(t, oldToken, newID)
	assert.True(t, e.usesRetired(t))
}

func TestGitLabWebhookUsesRetiredTriggerCredential_WebhookOnOtherCredentials(t *testing.T) {
	e, _, _, newID, newToken := retiredCredEnv(t)
	// A Poller-owned webhook (some other trigger and token) does not reference
	// the retired generation.
	e.addManagedHook(t, "poller-token", 9999)
	assert.False(t, e.usesRetired(t))

	// Neither does one that already carries the current generation.
	e.state.ProjectHooks[trigOwner+"/"+trigRepo] = nil
	e.addManagedHook(t, newToken, newID)
	assert.False(t, e.usesRetired(t))
}

func TestGitLabWebhookUsesRetiredTriggerCredential_NoRetiredGenerationOrLedger(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	inUse, err := GitLabWebhookUsesRetiredTriggerCredential(ctx, e.state, retiredCredBaseURL, trigOwner, trigRepo)
	require.NoError(t, err)
	assert.False(t, inUse, "no ledger")

	_, err = ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	inUse, err = GitLabWebhookUsesRetiredTriggerCredential(ctx, e.state, retiredCredBaseURL, trigOwner, trigRepo)
	require.NoError(t, err)
	assert.False(t, inUse, "no retired generation")
}

func TestGitLabWebhookUsesRetiredTriggerCredential_FailsClosed(t *testing.T) {
	ctx := context.Background()
	e, _, _, _, _ := retiredCredEnv(t)

	inUse, err := GitLabWebhookUsesRetiredTriggerCredential(ctx, e.state, "", trigOwner, trigRepo)
	require.Error(t, err)
	assert.True(t, inUse)

	e.state.Errors = map[string]error{"ListProjectHooks": assert.AnError}
	inUse, err = GitLabWebhookUsesRetiredTriggerCredential(ctx, e.state, retiredCredBaseURL, trigOwner, trigRepo)
	require.Error(t, err)
	assert.True(t, inUse)
}
