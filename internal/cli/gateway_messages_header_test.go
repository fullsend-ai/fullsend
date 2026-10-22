package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/runtime"
)

// Tests for messages_auth_header: x-api-key (ADR 0142, #8370) in the
// runner: block checks, the egress profile, route planning and status.

const gatewayXAPIKeyBlock = `version: "1"
inference:
  gateway:
    url: https://gw.example.com/team-a
    auth: api-key
    messages_auth_header: x-api-key
    models:
      m1:
        api: anthropic-messages
`

func TestGatewayBlockApplies_MessagesAuthHeader(t *testing.T) {
	stubGatewayOIDC(t, "", "")
	ok, err := gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey, MessagesAuthHeader: config.GatewayMessagesAuthHeaderXAPIKey})
	require.NoError(t, err)
	assert.True(t, ok)

	// An oidc block (auth unset, as layers may leave it) that sets
	// x-api-key is a config error, not a silently ignored setting.
	_, err = gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud", MessagesAuthHeader: config.GatewayMessagesAuthHeaderXAPIKey})
	assert.ErrorContains(t, err, "allowed only with auth: api-key")

	_, err = gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey, MessagesAuthHeader: "bearer"})
	assert.ErrorContains(t, err, "inference.gateway.messages_auth_header")
}

func TestValidateEffectiveGateway_MessagesAuthHeader(t *testing.T) {
	t.Parallel()
	cfg := githubSetupConfig{gatewayURL: "https://gw.example.com", gatewayAudience: "aud", changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience")}
	overlay := buildPresetOverlay(cfg, nil)
	require.NotNil(t, overlay)
	// The inherited layer opts into x-api-key while the composed block
	// stays oidc: setup refuses it.
	base := []byte("version: \"1\"\ninference:\n  gateway:\n    messages_auth_header: x-api-key\n")
	effective, err := composeSetupLayers(nil, overlay, base)
	require.NoError(t, err)
	err = validateEffectiveGateway(cfg, effective)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed only with auth: api-key")
}

func TestRenderGatewayProfile_ClaudeXAPIKey(t *testing.T) {
	bearerData, bearerID, err := renderGatewayProfileFor(gatewayProfile{host: "gateway.example.com", prefix: "/team-a", claude: true})
	require.NoError(t, err)
	data, id, err := renderGatewayProfileFor(gatewayProfile{host: "gateway.example.com", prefix: "/team-a", claude: true, messagesXAPIKey: true})
	require.NoError(t, err)

	assert.Regexp(t, `^fullsend-inference-gateway-claude-[0-9a-f]{12}$`, id)
	assert.NotEqual(t, bearerID, id, "the header binding is part of the profile id")
	assert.Equal(t, gatewayProfileIDForHeader(gatewayClaudeProfileTemplateID, "gateway.example.com", "/team-a", "x-api-key"), id)
	assert.Equal(t, gatewayProfileIDFor(gatewayClaudeProfileTemplateID, "gateway.example.com", "/team-a"), bearerID, "the default binding keeps its id")
	_, otherPrefix, err := renderGatewayProfileFor(gatewayProfile{host: "gateway.example.com", prefix: "/team-b", claude: true, messagesXAPIKey: true})
	require.NoError(t, err)
	assert.NotEqual(t, id, otherPrefix)

	var p renderedClaudeGatewayProfile
	require.NoError(t, yaml.Unmarshal(data, &p))
	assert.Equal(t, id, p.ID)
	require.Len(t, p.Credentials, 1, "one binding, so never both headers")
	assert.Equal(t, []string{gatewayCredentialKey}, p.Credentials[0].EnvVars)
	assert.Equal(t, "header", p.Credentials[0].AuthStyle, "a plain header, no Bearer prefix")
	assert.Equal(t, "x-api-key", p.Credentials[0].HeaderName)
	assert.NotContains(t, string(data), "auth_style: bearer")
	assert.NotContains(t, string(data), "header_name: authorization")

	// Endpoints, rules and binaries are unchanged.
	var bearer renderedClaudeGatewayProfile
	require.NoError(t, yaml.Unmarshal(bearerData, &bearer))
	assert.Equal(t, bearer.Endpoints, p.Endpoints)
	assert.Equal(t, bearer.Binaries, p.Binaries)
	assert.Equal(t, "/team-a/v1/**", p.Endpoints[0].Path)

	// The pi and codex rendering has no x-api-key binding.
	_, _, err = renderGatewayProfileFor(gatewayProfile{host: "gateway.example.com", messagesXAPIKey: true})
	assert.ErrorContains(t, err, "Claude Code profile only")
}

func TestPlanGatewayRoute_MessagesXAPIKey(t *testing.T) {
	rc := gatewayTestRunConfig(t, gatewayXAPIKeyBlock, nil)

	const csb = "fs-plan-claude-xkey"
	plan, err := planGatewayRoute(rc, runtime.Backend{Runtime: runtime.ClaudeRuntime{}}, csb, []string{"gateway/claude-haiku-5-5"}, true)
	require.NoError(t, err)
	require.NotNil(t, plan)
	t.Cleanup(func() { plan.prepared.ClearGatewayRun(csb) })
	assert.Equal(t, gatewayProfile{host: "gw.example.com", prefix: "/team-a", claude: true, messagesXAPIKey: true}, plan.profileSpec())

	// pi keeps the shared rendering: OpenShell resolves the placeholder in
	// x-api-key on /v1/messages and in Authorization on the other paths.
	const psb = "fs-plan-pi-xkey"
	piPlan, err := planGatewayRoute(rc, runtime.Backend{Runtime: runtime.PiRuntime{}}, psb, []string{"gateway/m1"}, true)
	require.NoError(t, err)
	require.NotNil(t, piPlan)
	t.Cleanup(func() { piPlan.prepared.ClearGatewayRun(psb) })
	assert.Equal(t, gatewayProfile{host: "gw.example.com", prefix: "/team-a"}, piPlan.profileSpec())
}

func TestInferenceGatewayStatus_MessagesAuthHeader(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n    messages_auth_header: x-api-key\n", "")
	out, err := runGatewayStatusForTest(t, dir, newGatewayStatusFixture(map[string]string{}, nil))
	require.NoError(t, err)
	assert.Contains(t, out, "messages_auth_header: x-api-key (from config.yaml)")
	assert.Contains(t, out, "ANTHROPIC_API_KEY placeholder, sent as x-api-key")
	assert.NotContains(t, out, "ANTHROPIC_AUTH_TOKEN")

	// The default is reported as such.
	dir = writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n", "")
	out, err = runGatewayStatusForTest(t, dir, newGatewayStatusFixture(map[string]string{}, nil))
	require.NoError(t, err)
	assert.Contains(t, out, "messages_auth_header: authorization (default)")

	// Inherited from the base layer.
	dir = writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n", "inference:\n  gateway:\n    auth: api-key\n    messages_auth_header: x-api-key\n")
	out, err = runGatewayStatusForTest(t, dir, newGatewayStatusFixture(map[string]string{}, nil))
	require.NoError(t, err)
	assert.Contains(t, out, "messages_auth_header: x-api-key (from config.base.yaml)")

	// An oidc block that inherits x-api-key is reported invalid.
	dir = writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n", "inference:\n  gateway:\n    messages_auth_header: x-api-key\n")
	out, err = runGatewayStatusForTest(t, dir, newGatewayStatusFixture(map[string]string{}, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed only with auth: api-key")
	assert.True(t, strings.Contains(out, "Invalid inference.gateway block"), out)
}
