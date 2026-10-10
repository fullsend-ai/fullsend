package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

func TestValidateGatewaySetupFlags_Auth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  githubSetupConfig
		want string // empty means valid
	}{
		{"api-key with url only", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAuth: "api-key",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-auth"),
		}, ""},
		{"explicit oidc", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAudience: "aud", gatewayAuth: "oidc",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-auth"),
		}, ""},
		{"unknown mode", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAuth: "bearer",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-auth"),
		}, "inference.gateway.auth"},
		{"auth beside empty url and audience is not a clear", githubSetupConfig{
			gatewayAuth:  "api-key",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-auth"),
		}, "--inference-gateway-url is empty"},
		{"clear with an empty auth flag", githubSetupConfig{
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-auth"),
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateGatewaySetupFlags(tc.cfg)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestApplySetupFlagsToConfig_GatewayAuth(t *testing.T) {
	t.Parallel()
	cfg := githubSetupConfig{
		gatewayURL: "https://gw.example.com", gatewayAuth: " api-key ",
		gatewayModels: []string{"m=openai-responses"},
		changedFlags:  gatewayFlags("inference-gateway-url", "inference-gateway-auth", "inference-gateway-model"),
	}
	require.True(t, setupConfigFlagsChanged(cfg), "--inference-gateway-auth is a config flag")
	o := buildPresetOverlay(cfg, nil)
	require.NotNil(t, o)
	g := o.ConfigInferenceGateway()
	assert.Equal(t, config.GatewayAuthAPIKey, g.Auth)
	assert.Empty(t, g.Missing(), "api-key needs no audience")

	data, err := o.Marshal()
	require.NoError(t, err)
	parsed, err := config.ParsePerRepoConfigWriter(data)
	require.NoError(t, err)
	require.NoError(t, parsed.Validate())
	assert.Equal(t, g, parsed.ConfigInferenceGateway())

	// Switching back to oidc changes only auth (and adds the audience).
	applySetupFlagsToConfig(githubSetupConfig{
		gatewayAudience: "aud", gatewayAuth: "oidc",
		changedFlags: gatewayFlags("inference-gateway-audience", "inference-gateway-auth"),
	}, parsed, nil)
	g = parsed.ConfigInferenceGateway()
	assert.Equal(t, config.GatewayAuthOIDC, g.Auth)
	assert.Equal(t, "https://gw.example.com", g.URL)
	assert.Equal(t, []string{"m"}, g.ModelIDs())

	// Clearing removes the whole block, auth included.
	changed := applySetupFlagsToConfig(githubSetupConfig{
		changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience"),
	}, parsed, nil)
	assert.Equal(t, []string{"inference.gateway"}, changed)
	assert.True(t, parsed.ConfigInferenceGateway().IsZero())
	data, err = parsed.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "auth:")
}

func TestValidateEffectiveGateway_AuthAlone(t *testing.T) {
	t.Parallel()
	authOnly := githubSetupConfig{
		gatewayAuth:  "api-key",
		changedFlags: gatewayFlags("inference-gateway-auth"),
	}
	overlay := buildPresetOverlay(authOnly, nil)
	require.NotNil(t, overlay)

	// Nothing to inherit: auth alone does not create a block.
	effective, err := composeSetupLayers(nil, overlay, nil)
	require.NoError(t, err)
	err = validateEffectiveGateway(authOnly, effective)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would have no url")
	assert.NotContains(t, err.Error(), "audience", "api-key needs no audience")

	// With an inherited url, auth alone switches the mode.
	base := []byte("version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n")
	effective, err = composeSetupLayers(nil, overlay, base)
	require.NoError(t, err)
	require.NoError(t, validateEffectiveGateway(authOnly, effective))
	assert.Equal(t, config.GatewayAuthAPIKey, effective.ConfigInferenceGateway().Auth)
}

func TestInferenceGatewayStatus_APIKey(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n", "")
	env := actionsEnv("acme/widget")
	f := newGatewayStatusFixture(env, nil)
	out, err := runGatewayStatusForTest(t, dir, f)
	require.NoError(t, err)
	assert.Contains(t, out, "auth: api-key (from config.yaml)")
	assert.Contains(t, out, "long-lived")
	assert.Contains(t, out, "prefer auth: oidc")
	assert.Contains(t, out, gatewayAPIKeyEnv+" is not set")
	assert.Empty(t, f.fetched, "the api-key mode fetches no assertion")

	env[gatewayAPIKeyEnv] = gatewayTestAPIKey
	out, err = runGatewayStatusForTest(t, dir, f)
	require.NoError(t, err)
	assert.Contains(t, out, gatewayAPIKeyEnv+" is set")
	assert.NotContains(t, out, gatewayTestAPIKey, "the key is never printed")
}

func TestInferenceGatewayStatus_APIKeyPartial(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    auth: api-key\n    models:\n      m1:\n        api: openai-responses\n", "")
	f := newGatewayStatusFixture(actionsEnv("acme/widget"), nil)
	out, err := runGatewayStatusForTest(t, dir, f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing url")
	assert.NotContains(t, err.Error(), "audience")
	assert.Contains(t, out, "url must be set")
}

func TestInferenceGatewayStatus_DefaultAuth(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n", "")
	f := newGatewayStatusFixture(map[string]string{}, nil)
	out, err := runGatewayStatusForTest(t, dir, f)
	require.NoError(t, err)
	assert.Contains(t, out, "auth: oidc (default)")
}

// The status command reports the Claude Code route and the header its
// credential travels in, which differs per mode.
func TestInferenceGatewayStatus_ClaudeRoute(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com/\n    audience: aud\n", "")
	out, err := runGatewayStatusForTest(t, dir, newGatewayStatusFixture(map[string]string{}, nil))
	require.NoError(t, err)
	assert.Contains(t, out, "claude runtime: gateway/<model> runs Claude Code against ANTHROPIC_BASE_URL=https://gw.example.com as <model>")
	assert.Contains(t, out, "apiKeyHelper")
	assert.Contains(t, out, "Authorization: Bearer")
	assert.NotContains(t, out, "x-api-key")

	dir = writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n", "")
	out, err = runGatewayStatusForTest(t, dir, newGatewayStatusFixture(map[string]string{}, nil))
	require.NoError(t, err)
	assert.Contains(t, out, "ANTHROPIC_BASE_URL=https://gw.example.com")
	assert.Contains(t, out, "ANTHROPIC_API_KEY placeholder, sent as x-api-key")
	assert.NotContains(t, out, "apiKeyHelper")
}
