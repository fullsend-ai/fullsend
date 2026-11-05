package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
)

func TestGatewayRefreshWork_UsesOpenAISettle(t *testing.T) {
	// ADR 0137: the gateway route reuses the OpenAI placeholder settle, and
	// the default budget still fits a 300 s token's 150 s lead.
	assert.Equal(t, 90*time.Second, openAIPlaceholderSettle)
	assert.Equal(t, 130*time.Second, gatewayRefreshWork())
	assert.LessOrEqual(t, gatewayRefreshWork()+gatewayRefreshSafety, 150*time.Second)
}

func TestGatewayRefreshDelay_TokenLifetimes(t *testing.T) {
	iat := time.Unix(1_000_000, 0)
	need := gatewayRefreshWork() + gatewayRefreshSafety
	for _, lifetime := range []time.Duration{300 * time.Second, 10 * time.Minute, time.Hour} {
		exp := iat.Add(lifetime)
		for _, frac := range []float64{0, 0.5, 0.999} {
			d, ok := gatewayRefreshDelay(iat, exp, iat, frac)
			require.True(t, ok, "lifetime %v frac %v: refresh work does not fit", lifetime, frac)
			assert.GreaterOrEqual(t, d, gatewayRefreshMinDelay, "lifetime %v: delay below minimum", lifetime)
			assert.GreaterOrEqual(t, lifetime-d, need, "lifetime %v: too little left after the refresh fires", lifetime)
		}
	}
}

func TestGatewayRefreshDelay_300sTokenWindow(t *testing.T) {
	iat := time.Unix(1_000_000, 0)
	exp := iat.Add(300 * time.Second)
	early, _ := gatewayRefreshDelay(iat, exp, iat, 0.999)
	late, _ := gatewayRefreshDelay(iat, exp, iat, 0)
	assert.Equal(t, 150*time.Second, late, "no-jitter delay is half the lifetime")
	assert.GreaterOrEqual(t, early, 120*time.Second, "jittered delay in [120s,150s)")
	assert.Less(t, early, late, "jittered delay in [120s,150s)")
}

func TestGatewayRefreshDelay_LateStartAndTooShort(t *testing.T) {
	iat := time.Unix(1_000_000, 0)
	exp := iat.Add(300 * time.Second)
	// Fetched late: only the work budget is left, refresh at once.
	now := exp.Add(-(gatewayRefreshWork() + gatewayRefreshSafety + gatewayRefreshMinDelay))
	d, ok := gatewayRefreshDelay(iat, exp, now, 0.5)
	assert.Equal(t, gatewayRefreshMinDelay, d, "late start")
	assert.True(t, ok, "late start")
	// A token shorter than the refresh work cannot be refreshed in time;
	// with the 90 s settle that includes a 120 s token.
	for _, lifetime := range []time.Duration{30 * time.Second, 120 * time.Second} {
		d, ok = gatewayRefreshDelay(iat, iat.Add(lifetime), iat, 0)
		assert.Equal(t, gatewayRefreshMinDelay, d, "too short (%v)", lifetime)
		assert.False(t, ok, "too short (%v)", lifetime)
	}
	// An out-of-range jitter fraction is ignored.
	d, _ = gatewayRefreshDelay(iat, exp, iat, 7)
	assert.Equal(t, 150*time.Second, d, "bad jitter fraction")
}

func TestIsGatewayModel(t *testing.T) {
	for m, want := range map[string]bool{
		"gateway/gpt-5":       true,
		"Gateway/claude":      true,
		" GATEWAY/x ":         true,
		"openai/gpt-5":        false,
		"anthropic-vertex/c":  false,
		"gateway":             false,
		"gatewayx/foo":        false,
		"claude-haiku-latest": false,
	} {
		assert.Equal(t, want, isGatewayModel(m), "isGatewayModel(%q)", m)
	}
	assert.True(t, anyGatewayModel([]string{"openai/a", "gateway/b"}))
	assert.False(t, anyGatewayModel([]string{"openai/a"}))
}

func TestValidateGatewayRuntime(t *testing.T) {
	require.NoError(t, validateGatewayRuntime("pi", []string{"gateway/a", "openai/b"}))
	require.NoError(t, validateGatewayRuntime("codex", []string{"gateway/vendor/org/model"}))
	require.NoError(t, validateGatewayRuntime("claude", []string{"opus"}))
	for _, rt := range []string{"claude", "codex"} {
		require.NoError(t, validateGatewayRuntime(rt, []string{"gateway/a"}), rt)
	}
	assert.ErrorContains(t, validateGatewayRuntime("opencode", []string{"gateway/a"}), "opencode")
}

func TestValidateClaudeGatewayFallbacks(t *testing.T) {
	aliases := map[string]string{"fast": "gateway/claude-haiku-5-5", "sonnet": "claude-sonnet-5-5"}
	require.NoError(t, validateClaudeGatewayFallbacks(false, nil, aliases))
	require.NoError(t, validateClaudeGatewayFallbacks(false, []string{"sonnet", "opus"}, aliases))
	require.NoError(t, validateClaudeGatewayFallbacks(true, []string{"gateway/m", "fast", "sonnet"}, aliases), "on the route every fallback goes to the gateway")
	assert.ErrorContains(t, validateClaudeGatewayFallbacks(false, []string{"opus", "gateway/m"}, aliases), `fallback model "gateway/m" selects the inference gateway`)
	assert.ErrorContains(t, validateClaudeGatewayFallbacks(false, []string{"fast"}, aliases), `fallback model "fast"`, "checked after models.aliases")
	assert.ErrorContains(t, validateClaudeGatewayFallbacks(false, []string{"GATEWAY/m"}, nil), "selects the inference gateway")
	assert.ErrorContains(t, validateClaudeGatewayFallbacks(true, []string{"sonnet", "gateway/"}, aliases), "names no gateway model")
	assert.ErrorContains(t, validateClaudeGatewayFallbacks(true, []string{"Gateway/ "}, nil), "names no gateway model")
	assert.ErrorContains(t, validateClaudeGatewayFallbacks(true, []string{"empty"}, map[string]string{"empty": "gateway/"}), `fallback model "empty" names no gateway model`)
}

func TestValidateClaudeGatewayModels(t *testing.T) {
	oidc := config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"}
	apiKey := config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey}

	require.NoError(t, validateClaudeGatewayModels([]string{"opus"}, config.InferenceGatewayConfig{}, false), "no gateway model, no block needed")
	require.NoError(t, validateClaudeGatewayModels([]string{"gateway/claude-haiku-5-5"}, oidc, true))
	require.NoError(t, validateClaudeGatewayModels([]string{"GATEWAY/vendor/org/model"}, apiKey, true), "the id may itself hold slashes")

	assert.ErrorContains(t, validateClaudeGatewayModels([]string{"gateway/"}, oidc, true), "names no gateway model")
	assert.ErrorContains(t, validateClaudeGatewayModels([]string{"gateway/ "}, oidc, true), "names no gateway model")
	assert.ErrorContains(t, validateClaudeGatewayModels([]string{"gateway/m"}, config.InferenceGatewayConfig{}, false), "need an inference.gateway block")
	assert.ErrorContains(t, validateClaudeGatewayModels([]string{"gateway/m"}, oidc, false), "auth: api-key for a local run")
	assert.ErrorContains(t, validateClaudeGatewayModels([]string{"gateway/m"}, apiKey, false), "block that applies")
}

func stubGatewayOIDC(t *testing.T, reqURL, reqToken string) {
	t.Helper()
	orig := gatewayOIDCEnvFn
	gatewayOIDCEnvFn = func() (string, string) { return reqURL, reqToken }
	t.Cleanup(func() { gatewayOIDCEnvFn = orig })
}

func stubGatewayAssertion(t *testing.T, fn func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error)) {
	t.Helper()
	orig := fetchGatewayAssertionFn
	fetchGatewayAssertionFn = fn
	t.Cleanup(func() { fetchGatewayAssertionFn = orig })
}

func TestGatewayBlockApplies(t *testing.T) {
	full := config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"}

	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "tok")
	ok, err := gatewayBlockApplies(config.InferenceGatewayConfig{})
	require.NoError(t, err, "no block")
	assert.False(t, ok, "no block")
	ok, err = gatewayBlockApplies(full)
	require.NoError(t, err, "full block")
	assert.True(t, ok, "full block")
	_, err = gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com"})
	assert.ErrorContains(t, err, "audience", "partial block")
	_, err = gatewayBlockApplies(config.InferenceGatewayConfig{URL: "http://gw.example.com", Audience: "a"})
	assert.Error(t, err, "http url accepted")

	// A local run (no OIDC endpoint) leaves the route to the harness.
	stubGatewayOIDC(t, "", "")
	ok, err = gatewayBlockApplies(full)
	require.NoError(t, err, "local run")
	assert.False(t, ok, "local run")
}

func TestFetchGatewayToken(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")

	var got actionsoidc.AssertionConfig
	stubGatewayAssertion(t, func(_ context.Context, cfg actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		got = cfg
		return &actionsoidc.Assertion{Value: "jwt"}, nil
	})
	a, err := fetchGatewayToken(context.Background(), config.InferenceGatewayConfig{URL: "https://gw", Audience: " aud "})
	require.NoError(t, err)
	assert.Equal(t, "jwt", a.Value)
	assert.Equal(t, "aud", got.Audience)
	assert.Equal(t, "req", got.OIDCRequestToken)

	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		return nil, errors.New("refused")
	})
	_, err = fetchGatewayToken(context.Background(), config.InferenceGatewayConfig{Audience: "a"})
	assert.ErrorContains(t, err, "inference gateway")
}
