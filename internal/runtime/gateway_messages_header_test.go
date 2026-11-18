package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// Tests for messages_auth_header: x-api-key (ADR 0142, #8370) on pi and
// Claude Code.

func xAPIKeyGatewayBlock() config.InferenceGatewayConfig {
	return config.InferenceGatewayConfig{
		URL:                "https://gw.example.com",
		Auth:               config.GatewayAuthAPIKey,
		MessagesAuthHeader: config.GatewayMessagesAuthHeaderXAPIKey,
	}
}

// registerClaudeGatewayBlock registers a gateway run for block and clears
// it when the test ends.
func registerClaudeGatewayBlock(t *testing.T, sandboxName string, block config.InferenceGatewayConfig) {
	t.Helper()
	require.NoError(t, ClaudeRuntime{}.PrepareGatewayRun(sandboxName, GatewayRun{Block: block, BaseURL: "https://gw.example.com"}))
	t.Cleanup(func() { ClaudeRuntime{}.ClearGatewayRun(sandboxName) })
}

func TestRenderPiGatewayConfig_MessagesXAPIKey(t *testing.T) {
	models := map[string]config.InferenceGatewayModel{
		"gpt-6-luna":      {API: config.GatewayAPIOpenAIResponses},
		"claude-opus-5-5": {API: config.GatewayAPIAnthropicMessages},
	}
	g := xAPIKeyGatewayBlock()
	g.Models = models
	out, _, err := RenderPiGatewayConfig(g, nil)
	require.NoError(t, err)
	gw := decodeRendered(t, out)
	assert.Equal(t, map[string]any{
		"anthropic-messages": "x-api-key",
		"openai-responses":   "authorization",
		"openai-completions": "authorization",
		"discovery":          "authorization",
	}, gw["authHeader"], "only anthropic-messages moves; every other target is pinned to Bearer")

	// The models_file form gets the same runner-owned authHeader.
	f := xAPIKeyGatewayBlock()
	f.ModelsFile = ".fullsend/inference-gateway.json"
	out, _, err = RenderPiGatewayConfig(f, []byte(validPiGatewayModelsFile))
	require.NoError(t, err)
	assert.Equal(t, "x-api-key", decodeRendered(t, out)["authHeader"].(map[string]any)["anthropic-messages"])

	// The default, an explicit authorization, and an oidc block (which
	// ValidateEffective refuses before rendering) keep the single scheme.
	for name, block := range map[string]config.InferenceGatewayConfig{
		"default":       {URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey},
		"authorization": {URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey, MessagesAuthHeader: config.GatewayMessagesAuthHeaderAuthorization},
		"oidc":          {URL: "https://gw.example.com", Audience: "aud", MessagesAuthHeader: config.GatewayMessagesAuthHeaderXAPIKey},
	} {
		block.Models = models
		out, _, err := RenderPiGatewayConfig(block, nil)
		require.NoError(t, err, name)
		assert.Equal(t, "authorization", decodeRendered(t, out)["authHeader"], name)
	}
}

// A models_file still may not set authHeader, whatever the block's header.
func TestValidatePiGatewayModelsFile_AuthHeaderStillRefused(t *testing.T) {
	f := xAPIKeyGatewayBlock()
	f.ModelsFile = ".fullsend/inference-gateway.json"
	_, _, err := RenderPiGatewayConfig(f, []byte(`{"providers":{"gateway":{"authHeader":{"anthropic-messages":"x-api-key"},"models":{"m":{"api":"anthropic-messages"}}}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authHeader is runner-owned")
}

func TestClaudeGatewayRun_MessagesXAPIKey(t *testing.T) {
	registerClaudeGatewayBlock(t, "fs-claude-gw-xkey-reg", xAPIKeyGatewayBlock())
	gw := claudeGatewayRunFor("fs-claude-gw-xkey-reg")
	require.NotNil(t, gw)
	assert.True(t, gw.apiKey)
	assert.True(t, gw.xAPIKey)
	assert.Equal(t, "ANTHROPIC_API_KEY", gw.credentialEnv())

	// An oidc block never selects x-api-key, even if it names it.
	oidc := config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud", MessagesAuthHeader: config.GatewayMessagesAuthHeaderXAPIKey}
	registerClaudeGatewayBlock(t, "fs-claude-gw-xkey-oidc", oidc)
	assert.False(t, claudeGatewayRunFor("fs-claude-gw-xkey-oidc").xAPIKey)

	registerClaudeGateway(t, "fs-claude-gw-xkey-default", true)
	assert.False(t, claudeGatewayRunFor("fs-claude-gw-xkey-default").xAPIKey)
	assert.Equal(t, "ANTHROPIC_AUTH_TOKEN", claudeGatewayRunFor("fs-claude-gw-xkey-default").credentialEnv())
}

func TestClaudeGatewaySettings_MessagesXAPIKey(t *testing.T) {
	const sb = "fs-claude-gw-xkey-settings"
	registerClaudeGatewayBlock(t, sb, xAPIKeyGatewayBlock())
	settings := ClaudeRuntime{}.claudeGatewaySettings(sb)
	assert.Equal(t, "", settings["apiKeyHelper"], "no helper value is added in either header")
	env := settings["env"].(map[string]any)
	assert.NotContains(t, env, "ANTHROPIC_API_KEY", "before Bootstrap reads the placeholder there is nothing to pin")
	assert.Equal(t, "", env["ANTHROPIC_AUTH_TOKEN"], "Authorization carries no credential")

	cp := *claudeGatewayRunFor(sb)
	cp.placeholder = gatewayTestPlaceholder
	claudeGatewayRuns.Store(sb, &cp)
	env = ClaudeRuntime{}.claudeGatewaySettings(sb)["env"].(map[string]any)
	assert.Equal(t, gatewayTestPlaceholder, env["ANTHROPIC_API_KEY"])
	assert.Equal(t, "", env["ANTHROPIC_AUTH_TOKEN"])

	inline := inlineGatewaySettings(t, buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/m"}))
	assert.Equal(t, gatewayTestPlaceholder, inline["env"].(map[string]any)["ANTHROPIC_API_KEY"], "the inline settings carry the pin")
	assert.Equal(t, "", inline["env"].(map[string]any)["ANTHROPIC_AUTH_TOKEN"])
}

func TestBuildRunCommand_GatewayMessagesXAPIKey(t *testing.T) {
	const sb = "fs-claude-gw-xkey-cmd"
	registerClaudeGatewayBlock(t, sb, xAPIKeyGatewayBlock())
	cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/claude-haiku-5-5"})
	assert.Contains(t, cmd, `export ANTHROPIC_API_KEY="$FULLSEND_GATEWAY_PLACEHOLDER"`)
	assert.NotContains(t, cmd, "export ANTHROPIC_AUTH_TOKEN", "one credential header per request")
	assert.Contains(t, cmd, gatewayPlaceholderCheck())
}

func TestBuildRunCommand_GatewayEnvOwnership_MessagesXAPIKey(t *testing.T) {
	const sb = "fs-claude-gw-xkey-shell"
	registerClaudeGatewayBlock(t, sb, xAPIKeyGatewayBlock())
	out, _, err := gatewayLaunch(t, sb, hostileGatewayEnv)
	require.NoError(t, err, out)
	assert.Contains(t, out, "REAL-CLAUDE")
	assert.Contains(t, out, "ANTHROPIC_API_KEY="+gatewayTestPlaceholder+"\n", "the placeholder read before .env")
	assert.NotContains(t, out, "ANTHROPIC_AUTH_TOKEN=", ".env's Bearer credential is cleared, so only x-api-key is sent")
	assert.NotContains(t, out, "sk-planted")
	assert.Contains(t, out, "ANTHROPIC_BASE_URL=https://gw.example.com\n")
	assert.NotContains(t, out, "FULLSEND_GATEWAY_PLACEHOLDER", "the captured placeholder is never exported")
}
