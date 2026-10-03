package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Tests for inference credential provisioning keyed by the effective
// inference.auth of each repository (#8011).

func secretValue(fc *forge.FakeClient, repo, name string) (string, bool) {
	for _, rec := range fc.CreatedSecrets {
		if rec.Repo == repo && rec.Name == name {
			return rec.Value, true
		}
	}
	return "", false
}

func deletedSecretNames(fc *forge.FakeClient, repo string) []string {
	var names []string
	for _, rec := range fc.DeletedSecrets {
		if rec.Repo == repo {
			names = append(names, rec.Name)
		}
	}
	return names
}

// openAIConvergeCfg returns a GitHub configuration whose repos select
// openai-api-key and whose only credential input is the OpenAI key.
func openAIConvergeCfg(repoNames ...string) ConvergeConfig {
	m := newConvergeManifest(repoNames...)
	m.Defaults.Inference.Auth = InferenceAuthOpenAIAPIKey
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.OpenAIAPIKey = testOpenAIAPIKey
	return cfg
}

func TestConverge_OpenAIFreshInstallWritesOnlyOpenAIKey(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := openAIConvergeCfg("acme/api")
	lookups := 0
	cfg.ResolveProjectNumber = func(context.Context, string) (string, error) {
		lookups++
		return "123456789", nil
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, result.Installed(), 1)

	value, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
	require.True(t, ok, "FULLSEND_OPENAI_API_KEY must be written")
	assert.Equal(t, testOpenAIAPIKey, value)
	for _, rec := range fc.CreatedSecrets {
		assert.False(t, strings.HasPrefix(rec.Name, "FULLSEND_GCP_"), "unexpected GCP secret %s", rec.Name)
	}
	_, hasRegion := fc.VariableValues["acme/api/"+forge.VarGCPRegion]
	assert.False(t, hasRegion, "OpenAI-only repos need no GCP region")
	assert.Zero(t, lookups, "OpenAI-only repos must not trigger GCP lookups")
}

func TestConverge_GitLabOpenAIFreshInstallWritesPrefixedKey(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	value, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
	require.True(t, ok, "GitLab must receive FULLSEND_OPENAI_API_KEY")
	assert.Equal(t, testOpenAIAPIKey, value)
	_, legacy := secretValue(fc, "api", "OPENAI_API_KEY")
	assert.False(t, legacy, "the unprefixed OPENAI_API_KEY must never be written")
}

func mixedFleetManifest() *Manifest {
	m := newConvergeManifest("acme/api", "acme/web")
	m.GitHub.Repos[1].Inference.Auth = InferenceAuthOpenAIAPIKey
	return m
}

func TestConverge_MixedFleetProvisionsPerRepo(t *testing.T) {
	fc := newFakeClientForBatch("acme/api", "acme/web")
	cfg := convergeCfgWithDefaults(mixedFleetManifest())
	cfg.OpenAIAPIKey = testOpenAIAPIKey

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	_, ok := secretValue(fc, "api", forge.SecretGCPProjectID)
	assert.True(t, ok, "vertex-wif repo gets FULLSEND_GCP_PROJECT_ID")
	_, ok = secretValue(fc, "api", forge.SecretGCPWIFProvider)
	assert.True(t, ok, "vertex-wif repo gets FULLSEND_GCP_WIF_PROVIDER")
	_, ok = secretValue(fc, "api", forge.SecretOpenAIAPIKey)
	assert.False(t, ok, "vertex-wif repo must not get the OpenAI key")

	_, ok = secretValue(fc, "web", forge.SecretOpenAIAPIKey)
	assert.True(t, ok, "openai-api-key repo gets FULLSEND_OPENAI_API_KEY")
	_, ok = secretValue(fc, "web", forge.SecretGCPProjectID)
	assert.False(t, ok, "openai-api-key repo must not get GCP secrets")
}

func TestConverge_MixedFleetMissingInputNamesRepoAndParameters(t *testing.T) {
	fc := newFakeClientForBatch("acme/api", "acme/web")
	// Only Vertex inputs: acme/api is satisfied, acme/web is not. The
	// Vertex inputs are needed by acme/api and must not be rejected.
	cfg := convergeCfgWithDefaults(mixedFleetManifest())

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)

	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "web", failed[0].Repo)
	msg := failed[0].Error.Error()
	assert.Contains(t, msg, "acme/web")
	assert.Contains(t, msg, forge.SecretOpenAIAPIKey)
	assert.Contains(t, msg, "--openai-api-key")
	for _, rec := range fc.CreatedSecrets {
		assert.NotEqual(t, "web", rec.Repo, "failed validation must not write secrets to acme/web")
	}
	_, ok := secretValue(fc, "api", forge.SecretGCPProjectID)
	assert.True(t, ok, "acme/api must still be provisioned")
}

// installedOpenAISwitchFixture is an installed vertex-wif repo whose
// manifest now selects openai-api-key.
func installedOpenAISwitchFixture(t *testing.T) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	return fc, openAIConvergeCfg("acme/api")
}

func TestConverge_AuthSwitchRemovesObsoleteSecretsAfterWrite(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	value, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
	require.True(t, ok)
	assert.Equal(t, testOpenAIAPIKey, value)
	assert.ElementsMatch(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, deletedSecretNames(fc, "api"))
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])

	// A re-run without inputs reuses the new key and changes nothing.
	fc.CreatedSecrets = nil
	fc.DeletedSecrets = nil
	result, err = Converge(context.Background(), withoutInferenceInputs(cfg), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.CreatedSecrets, "reuse must not rewrite secrets")
	assert.Empty(t, fc.DeletedSecrets)
}

func TestConverge_AuthSwitchWriteFailureKeepsOldSecrets(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	fc.Errors["CreateRepoSecret"] = errors.New("permission denied")

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)

	assert.Empty(t, fc.DeletedSecrets, "old credentials must be kept when the new setup fails")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
}

func TestConverge_AuthSwitchDryRunReportsNamesWithoutValues(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	cfg.DryRun = true

	var mu sync.Mutex
	var messages []string
	progress := func(repo, phase, message string) {
		mu.Lock()
		defer mu.Unlock()
		messages = append(messages, fmt.Sprintf("%s %s %s", repo, phase, message))
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), progress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.CreatedSecrets, "dry run must not write secrets")
	assert.Empty(t, fc.DeletedSecrets, "dry run must not delete secrets")

	components := map[string]string{}
	for _, a := range result.Results[0].Actions {
		assert.NotContains(t, a.Detail, testOpenAIAPIKey, "dry run must never report secret values")
		if strings.HasPrefix(a.Component, "secret:") {
			components[a.Component] = a.Action
		}
	}
	assert.Contains(t, components, "secret:"+forge.SecretOpenAIAPIKey)
	assert.Contains(t, components, "secret:"+forge.SecretGCPProjectID)
	assert.Contains(t, components, "secret:"+forge.SecretGCPWIFProvider)
	for _, m := range messages {
		assert.NotContains(t, m, testOpenAIAPIKey, "progress output must never contain secret values")
	}
}

func TestConverge_MissingOpenAIKeyWithoutInputFails(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	cfg = withoutInferenceInputs(cfg)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "acme/api")
	assert.Contains(t, failed[0].Error.Error(), "--openai-api-key")
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.DeletedSecrets, "existing GCP secrets must be kept")
}

func TestConverge_GitLabLegacyOpenAIKeyDoesNotSatisfyCheck(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	delete(fc.Secrets, "acme/api/"+forge.SecretOpenAIAPIKey)
	fc.Secrets["acme/api/OPENAI_API_KEY"] = true

	cfg := gitlabConvergeCfg("acme/api")
	cfg.OpenAIAPIKey = ""
	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), forge.SecretOpenAIAPIKey)
	assert.True(t, fc.Secrets["acme/api/OPENAI_API_KEY"], "the unprefixed OPENAI_API_KEY must never be deleted")

	// Supplying the key provisions the prefixed variable and still leaves
	// the unprefixed one alone.
	cfg.OpenAIAPIKey = testOpenAIAPIKey
	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
	assert.True(t, fc.Secrets["acme/api/OPENAI_API_KEY"], "the unprefixed OPENAI_API_KEY must never be deleted")
	for _, name := range deletedSecretNames(fc, "api") {
		assert.NotEqual(t, "OPENAI_API_KEY", name)
	}
}

func TestProbeComponentsForAuth_RequiresSelectedMethodSecrets(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")

	components, err := ProbeComponentsForAuth(context.Background(), fc, "acme", "api", ForgeGitHub, InferenceAuthOpenAIAPIKey, defaultForgeConfig, nil)
	require.NoError(t, err)
	present := map[string]bool{}
	for _, c := range components {
		if strings.HasPrefix(c.Name, "secret:") {
			present[c.Name] = c.Present
		}
	}
	require.Contains(t, present, "secret:"+forge.SecretOpenAIAPIKey)
	assert.False(t, present["secret:"+forge.SecretOpenAIAPIKey], "the OpenAI key is missing")
	assert.NotContains(t, present, "secret:"+forge.SecretGCPProjectID, "GCP secrets are not required for openai-api-key")

	components, err = ProbeComponentsForAuth(context.Background(), fc, "acme", "api", ForgeGitHub, InferenceAuthVertexWIF, defaultForgeConfig, nil)
	require.NoError(t, err)
	present = map[string]bool{}
	for _, c := range components {
		if strings.HasPrefix(c.Name, "secret:") {
			present[c.Name] = c.Present
		}
	}
	assert.True(t, present["secret:"+forge.SecretGCPProjectID])
	assert.True(t, present["secret:"+forge.SecretGCPWIFProvider])
	assert.NotContains(t, present, "secret:"+forge.SecretOpenAIAPIKey)
}

func TestConverge_ProjectNumberLookupOnlyForVertexAndOnce(t *testing.T) {
	fc := newFakeClientForBatch("acme/api", "acme/web", "acme/ops")
	m := newConvergeManifest("acme/api", "acme/web", "acme/ops")
	m.GitHub.Repos[2].Inference.Auth = InferenceAuthOpenAIAPIKey
	cfg := convergeCfgWithDefaults(m)
	cfg.InferenceProjectNumber = ""
	cfg.OpenAIAPIKey = testOpenAIAPIKey
	lookups := 0
	cfg.ResolveProjectNumber = func(_ context.Context, projectID string) (string, error) {
		lookups++
		assert.Equal(t, "test-inference", projectID)
		return "987654321", nil
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, 1, lookups, "project number is derived at most once")
	wif, ok := secretValue(fc, "api", forge.SecretGCPWIFProvider)
	require.True(t, ok)
	assert.Contains(t, wif, "projects/987654321/")
}

func TestConverge_ProjectNumberLookupErrorFailsBeforeWrites(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	cfg.InferenceProjectNumber = ""
	cfg.ResolveProjectNumber = func(context.Context, string) (string, error) {
		return "", errors.New("API unavailable")
	}

	sc := &fakeScaffoldCommit{}
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API unavailable")
	assert.Contains(t, err.Error(), "--inference-wif-provider")
	assert.Empty(t, fc.CreatedSecrets)
	assert.False(t, sc.called)
}
