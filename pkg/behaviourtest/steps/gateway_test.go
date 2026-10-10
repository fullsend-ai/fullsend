package steps

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestGatewayProviderCleanupLogged(t *testing.T) {
	require.NoError(t, gatewayProviderCleanupLogged("x\n  ✓ Run-scoped provider deleted: inference-gateway-0123456789ab\n"))
	require.NoError(t, gatewayProviderCleanupLogged("Run-scoped provider already gone: inference-gateway-abc"))
	err := gatewayProviderCleanupLogged("Run-scoped provider deleted: openai-0123")
	require.Error(t, err, "another provider's cleanup does not count")
	assert.Contains(t, err.Error(), "inference gateway provider")
}

func TestThenGatewayProviderCleanedUp_NoRun(t *testing.T) {
	err := thenGatewayProviderCleanedUp(&world.World{})
	require.ErrorContains(t, err, "no workflow run recorded")
}

func TestTestGatewayFromEnv(t *testing.T) {
	t.Setenv(envInferenceGatewayURL, "")
	_, _, ok := testGatewayFromEnv()
	assert.False(t, ok)

	t.Setenv(envInferenceGatewayURL, " https://gw.example/ ")
	t.Setenv(envInferenceGatewayAudience, "")
	u, aud, ok := testGatewayFromEnv()
	require.True(t, ok)
	assert.Equal(t, "https://gw.example", u)
	assert.Equal(t, defaultGatewayAudience, aud)

	t.Setenv(envInferenceGatewayAudience, "custom-aud")
	_, aud, _ = testGatewayFromEnv()
	assert.Equal(t, "custom-aud", aud)
}

func TestExpandGatewayPlaceholder(t *testing.T) {
	t.Setenv(envInferenceGatewayURL, "")
	got, err := expandGatewayPlaceholder("GET https://other.example/ TOK")
	require.NoError(t, err)
	assert.Equal(t, "GET https://other.example/ TOK", got)
	_, err = expandGatewayPlaceholder("GET <gateway>/v1/models TOK")
	assert.Error(t, err)

	t.Setenv(envInferenceGatewayURL, "https://gw.example/")
	got, err = expandGatewayPlaceholder("GET <gateway>/v1/models TOK")
	require.NoError(t, err)
	assert.Equal(t, "GET https://gw.example/v1/models TOK", got)
}

func TestWithInferenceGatewayKeepsOtherKeys(t *testing.T) {
	t.Parallel()

	out, err := withInferenceGateway([]byte("runtime: dummy\ninference:\n  openai:\n    project: p\n"), map[string]any{"url": "https://gw.example", "audience": "aud"})
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.Equal(t, "dummy", doc["runtime"])
	inference := doc["inference"].(map[string]any)
	assert.Equal(t, map[string]any{"project": "p"}, inference["openai"])
	assert.Equal(t, map[string]any{"url": "https://gw.example", "audience": "aud"}, inference["gateway"])

	out, err = withInferenceGateway(nil, map[string]any{"url": "https://gw.example", "audience": "aud"})
	require.NoError(t, err)
	assert.Contains(t, string(out), "audience: aud")

	_, err = withInferenceGateway([]byte("{"), map[string]any{"url": "u"})
	assert.Error(t, err)
}

// TestGivenTestInferenceGateway_RecordsOriginalOnWorld: the step records
// the pre-scenario config on the World, keeps the first value when it runs
// twice, and the cleanup restores exactly that value.
func TestGivenTestInferenceGateway_RecordsOriginalOnWorld(t *testing.T) {
	t.Setenv(envInferenceGatewayURL, "https://gw.example")
	t.Setenv(envInferenceGatewayAudience, "aud")
	original := []byte("runtime: dummy\n")
	scmDriver := &fakeCleanupSCM{fileContent: original}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmDriver}

	require.NoError(t, givenTestInferenceGateway(w))
	assert.True(t, w.GatewayConfigOverridden)
	assert.Equal(t, original, w.GatewayConfigOriginal)
	require.Len(t, scmDriver.commits, 1)
	assert.Contains(t, string(scmDriver.commits[0].content), "audience: aud")
	assert.Contains(t, string(scmDriver.commits[0].content), "supportsStrictTools: false", "the model list pi needs is committed")

	// A second run reads the already-modified file; the pre-scenario
	// original must win.
	scmDriver.fileContent = scmDriver.commits[0].content
	require.NoError(t, givenTestInferenceGateway(w))
	assert.Equal(t, original, w.GatewayConfigOriginal)

	require.NoError(t, restoreGatewayConfig(w))
	assert.Equal(t, original, scmDriver.commits[len(scmDriver.commits)-1].content)
	assert.False(t, w.GatewayConfigOverridden)
	assert.Nil(t, w.GatewayConfigOriginal)
}

func TestGivenTestInferenceGateway_SkipsAndErrors(t *testing.T) {
	t.Setenv(envInferenceGatewayURL, "")
	w := &world.World{Org: "org", RepoName: "repo", SCM: &fakeCleanupSCM{}}
	assert.ErrorIs(t, givenTestInferenceGateway(w), godog.ErrSkip)
	assert.False(t, w.GatewayConfigOverridden)

	t.Setenv(envInferenceGatewayURL, "https://gw.example")
	assert.ErrorContains(t, givenTestInferenceGateway(&world.World{SCM: &fakeCleanupSCM{}}), "no repo configured")

	failing := &world.World{Org: "org", RepoName: "repo", SCM: &fakeCleanupSCM{getFileErr: errors.New("boom")}}
	assert.ErrorContains(t, givenTestInferenceGateway(failing), "reading config")
	assert.False(t, failing.GatewayConfigOverridden, "nothing recorded when the read fails")
}

func TestProbeResultAssertions(t *testing.T) {
	t.Parallel()

	data := []byte(`{"operations":[{"description":"chat","success":true,"http_status":200,"response_body":"{\"x-api-key\":\"stub\"}"},{"description":"denied","success":false,"http_status":403}]}`)
	res, err := probeResultFrom(data, " chat ")
	require.NoError(t, err)
	require.NoError(t, checkProbeStatus(res, "200"))
	assert.Error(t, checkProbeStatus(res, "403"))
	require.NoError(t, checkProbeBody(res, true, "x-api-key"))
	require.NoError(t, checkProbeBody(res, false, "eyJ"))
	assert.Error(t, checkProbeBody(res, false, "stub"))
	assert.Error(t, checkProbeBody(res, true, "eyJ"))
	assert.Error(t, checkProbeBody(res, true, ""))

	res, err = probeResultFrom(data, "denied")
	require.NoError(t, err)
	require.NoError(t, checkProbeStatus(res, "403"))

	_, err = probeResultFrom(data, "missing")
	assert.Error(t, err)
	_, err = probeResultFrom([]byte("{"), "chat")
	assert.Error(t, err)
}

// TestProbeBodyRedactedJWT checks the custody assertion against a body
// whose JWT the runtime redacted: the flag, not the redacted text, decides.
func TestProbeBodyRedactedJWT(t *testing.T) {
	t.Parallel()

	data := []byte(`{"operations":[{"description":"leak","success":true,"http_status":200,"response_body":"{\"authorization\":\"Bearer <redacted-jwt>\"}","body_had_jwt":true}]}`)
	res, err := probeResultFrom(data, "leak")
	require.NoError(t, err)
	require.True(t, res.BodyHadJWT)
	err = checkProbeBody(res, false, "eyJ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpectedly contains")
	require.NoError(t, checkProbeBody(res, true, "eyJ"))
	// Other needles still match the recorded text only.
	require.NoError(t, checkProbeBody(res, false, "x-api-key"))
	require.NoError(t, checkProbeBody(res, true, "<redacted-jwt>"))
}

// The api-key step commits url and auth only, and skips without the URL
// or the test key.
func TestGivenTestInferenceGatewayAPIKey(t *testing.T) {
	const cfgKey = "org/repo/.fullsend/config.yaml"
	newWorld := func() (*world.World, *forge.FakeClient) {
		fc := forge.NewFakeClient()
		fc.FileContents[cfgKey] = []byte("runtime: dummy\n")
		return &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}, fc
	}

	t.Setenv(envInferenceGatewayURL, "https://gw.example")
	t.Setenv(envInferenceGatewayTestKey, "")
	w, fc := newWorld()
	assert.ErrorIs(t, givenTestInferenceGatewayAPIKey(w), godog.ErrSkip, "no key: skip")
	assert.Empty(t, fc.CreatedSecrets, "no key: no secret")

	t.Setenv(envInferenceGatewayTestKey, "test-gateway-key-value")
	w, fc = newWorld()
	require.NoError(t, givenTestInferenceGatewayAPIKey(w))
	assert.Equal(t, []forge.SecretRecord{{Owner: "org", Repo: "repo", Name: forge.SecretInferenceGatewayAPIKey, Value: "test-gateway-key-value"}},
		fc.CreatedSecrets, "the step sets the enrolled repo's key secret")
	assert.True(t, w.GatewayAPIKeySecretSet)
	committed := fc.FileContents[cfgKey]
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(committed, &doc))
	gw := doc["inference"].(map[string]any)["gateway"].(map[string]any)
	assert.Equal(t, "https://gw.example", gw["url"])
	assert.Equal(t, "api-key", gw["auth"])
	assert.NotContains(t, gw, "audience")
	assert.Equal(t, map[string]any{"api": "anthropic-messages", "compat": map[string]any{"supportsStrictTools": false}},
		gw["models"].(map[string]any)["claude-haiku-5-5"], "pi runs offline: the model list is committed")
	assert.NotContains(t, string(committed), "test-gateway-key-value", "the key is never committed")

	require.NoError(t, deleteGatewayAPIKeySecret(w))
	assert.Equal(t, []forge.SecretRecord{{Owner: "org", Repo: "repo", Name: forge.SecretInferenceGatewayAPIKey}},
		stripSecretValues(fc.DeletedSecrets), "cleanup deletes the secret")
	assert.False(t, w.GatewayAPIKeySecretSet)
	require.NoError(t, deleteGatewayAPIKeySecret(w), "a second cleanup is a no-op")
	assert.Len(t, fc.DeletedSecrets, 1)

	t.Setenv(envInferenceGatewayURL, "")
	w, fc = newWorld()
	assert.ErrorIs(t, givenTestInferenceGatewayAPIKey(w), godog.ErrSkip, "no URL: skip")
	assert.Empty(t, fc.CreatedSecrets, "no URL: no secret")
}

func TestGivenTestInferenceGatewayAPIKey_RequiresGitHub(t *testing.T) {
	t.Setenv(envInferenceGatewayURL, "https://gw.example")
	t.Setenv(envInferenceGatewayTestKey, "test-gateway-key-value")
	w := &world.World{Org: "org", RepoName: "repo", SCM: &fakeCleanupSCM{fileContent: []byte("runtime: dummy\n")}}
	assert.ErrorContains(t, givenTestInferenceGatewayAPIKey(w), "requires GitHub SCM driver")
	assert.False(t, w.GatewayAPIKeySecretSet)
}

func TestDeleteGatewayAPIKeySecret_NotFoundIsFine(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"DeleteRepoSecret": fmt.Errorf("%w: secret", forge.ErrNotFound)}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc), GatewayAPIKeySecretSet: true}
	require.NoError(t, deleteGatewayAPIKeySecret(w), "an already-deleted secret is not an error")
	assert.False(t, w.GatewayAPIKeySecretSet)
}

func TestDeleteGatewayAPIKeySecret_ErrorKeepsFlagForRetry(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"DeleteRepoSecret": errors.New("boom")}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc), GatewayAPIKeySecretSet: true}
	require.ErrorContains(t, deleteGatewayAPIKeySecret(w), "boom")
	assert.True(t, w.GatewayAPIKeySecretSet, "a failed delete is retried by cleanupRetry")
}

func stripSecretValues(in []forge.SecretRecord) []forge.SecretRecord {
	out := make([]forge.SecretRecord, len(in))
	for i, r := range in {
		r.Value = ""
		out[i] = r
	}
	return out
}
