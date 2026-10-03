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

// Tests for obsolete inference-secret reconciliation and OpenAI key
// normalization (#8011).

// A deletion that failed once must be retried by a later run: the selected
// method's secrets already exist then, but the obsolete ones still do too.
func TestConverge_AuthSwitchDeletionFailureIsRetried(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	fc.Errors["DeleteRepoSecret"] = errors.New("forge unavailable")

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey], "replacement was written")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID], "obsolete secret survives the failed deletion")

	delete(fc.Errors, "DeleteRepoSecret")

	// Retry with the inputs supplied again.
	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.ElementsMatch(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, deletedSecretNames(fc, "api"))
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
}

// Obsolete secrets are reconciled whenever the selected method is already
// established, including a run that supplies no inputs; a dry run only plans.
func TestConverge_ReuseReconcilesObsoleteSecrets(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	cfg = withoutInferenceInputs(cfg)

	dry := cfg
	dry.DryRun = true
	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), dry, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.DeletedSecrets, "dry run must not delete secrets")
	planned := map[string]string{}
	for _, a := range result.Results[0].Actions {
		planned[a.Component] = a.Action
	}
	assert.Equal(t, "delete", planned["secret:"+forge.SecretGCPProjectID])
	assert.Equal(t, "delete", planned["secret:"+forge.SecretGCPWIFProvider])

	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.ElementsMatch(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, deletedSecretNames(fc, "api"))
	assert.Empty(t, fc.CreatedSecrets, "reuse must not rewrite secrets")
}

// A failed variable write for the selected method means the new
// configuration was not established, so the old credentials must stay.
func TestConverge_VariableWriteFailureKeepsObsoleteSecrets(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	// Switching openai-api-key -> vertex-wif with a stale region variable.
	delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
	delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	fc.VariableValues["acme/api/"+forge.VarGCPRegion] = "us-east1"
	fc.Errors["CreateOrUpdateRepoVariable"] = errors.New("forge unavailable")

	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Empty(t, fc.DeletedSecrets, "previous credentials must be kept when a selected-method write failed")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
}

func TestConverge_OpenAIKeyIsTrimmedBeforeWrite(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := openAIConvergeCfg("acme/api")
	cfg.OpenAIAPIKey = "  " + testOpenAIAPIKey + "\n"

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	value, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
	require.True(t, ok)
	assert.Equal(t, testOpenAIAPIKey, value)
}

func TestConverge_GitLabRejectsUnmaskableOpenAIKey(t *testing.T) {
	keys := map[string]string{
		"too short":           "short",
		"embedded whitespace": "test-openai key-value",
		"unsupported char":    "test-openai-key-value!",
		"multiline":           "test-openai-key\nvalue-more",
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			cfg := gitlabConvergeCfg("acme/api")
			cfg.OpenAIAPIKey = key

			sc := &fakeScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			failed := result.Failed()
			require.Len(t, failed, 1)
			assert.NotContains(t, failed[0].Error.Error(), strings.TrimSpace(key), "the error must never contain the key")
			assert.Empty(t, fc.CreatedSecrets, "an unmaskable key must not be stored")
		})
	}
}
