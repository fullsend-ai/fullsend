package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

func TestOpenAIRouteSeed(t *testing.T) {
	s := OpenAIRouteSeed(PiRuntime{})
	assert.Equal(t, "OPENAI_API_KEY", s.PlaceholderEnv)
	assert.Equal(t, PiRuntime{}.OpenAIAuthSeed(), s.Seed)
	assert.Equal(t, PiRuntime{}.OpenAIAuthFile(), s.File)
	assert.False(t, s.IsZero())

	assert.True(t, OpenAIRouteSeed(ClaudeRuntime{}).IsZero(), "no OpenAI seeder")
}

func TestPiGatewayCredentialSeed_IsItsOwnRoute(t *testing.T) {
	gw := PiRuntime{}.GatewayCredentialSeed()
	oa := OpenAIRouteSeed(PiRuntime{})
	assert.Equal(t, "INFERENCE_GATEWAY_API_KEY", gw.PlaceholderEnv)
	assert.Equal(t, PiRuntime{}.ConfigDir()+"/inference-gateway.token", gw.File)
	assert.Equal(t, PiGatewayTokenSeed(PiRuntime{}.ConfigDir()), gw.Seed)
	assert.NotEqual(t, oa.PlaceholderEnv, gw.PlaceholderEnv, "independent handoffs per route")
	assert.NotEqual(t, oa.File, gw.File, "independent handoffs per route")

	var _ GatewayRouteRuntime = PiRuntime{}
	_, ok := Runtime(ClaudeRuntime{}).(GatewayRouteRuntime)
	assert.False(t, ok, "Claude Code has no gateway route yet")
}

// TestPiGatewayTokenSeed_Shell runs the seed under sh: a gateway
// placeholder is written to the token file as-is; anything else fails
// closed and leaves no file.
func TestPiGatewayTokenSeed_Shell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	placeholder := piPlaceholderPrefix + "v123_INFERENCE_GATEWAY_API_KEY"
	dir := filepath.Join(t.TempDir(), "cfg")
	cmd := exec.Command("sh", "-c", PiGatewayTokenSeed(dir))
	cmd.Env = append(os.Environ(), "INFERENCE_GATEWAY_API_KEY="+placeholder)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(filepath.Join(dir, piInferenceGatewayTokenFile))
	require.NoError(t, err)
	assert.Equal(t, placeholder, string(data))

	for name, value := range map[string]string{
		"a real token":        "eyJhbGciOi.eyJzdWIiOi.sig",
		"unset":               "",
		"another route":       piPlaceholderPrefix + "v1_OPENAI_API_KEY",
		"unexpected chars":    piPlaceholderPrefix + "v1;x_INFERENCE_GATEWAY_API_KEY",
		"embedded whitespace": piPlaceholderPrefix + "v1 x_INFERENCE_GATEWAY_API_KEY",
	} {
		dir := filepath.Join(t.TempDir(), "cfg")
		cmd := exec.Command("sh", "-c", PiGatewayTokenSeed(dir))
		cmd.Env = append(os.Environ(), "INFERENCE_GATEWAY_API_KEY="+value)
		out, err := cmd.CombinedOutput()
		assert.Error(t, err, "%s: %s", name, out)
		_, statErr := os.Stat(filepath.Join(dir, piInferenceGatewayTokenFile))
		assert.True(t, os.IsNotExist(statErr), "%s: no token file", name)
	}
}

func TestPiPrepareGatewayRun(t *testing.T) {
	const sb = "sb-gw-prepare"
	t.Cleanup(func() { SetPiGatewayRun(sb, nil) })
	block := config.InferenceGatewayConfig{
		URL:      "https://gw.example.com",
		Audience: "aud",
		Models:   map[string]config.InferenceGatewayModel{"m1": {API: config.GatewayAPIOpenAIResponses}},
	}
	r := PiRuntime{}
	require.NoError(t, r.PrepareGatewayRun(sb, GatewayRun{Block: block, BaseURL: "https://gw.example.com"}))
	gw := piGatewayRunFor(sb)
	require.NotNil(t, gw)
	assert.Equal(t, []string{"m1"}, gw.ModelIDs)
	assert.Equal(t, "https://gw.example.com", gw.BaseURL)
	assert.Equal(t, r.ConfigDir()+"/"+piInferenceGatewayTokenFile, gw.TokenFile)
	assert.Contains(t, string(gw.Config), `"authHeader": "authorization"`)
	require.NoError(t, validatePiGatewayRun(gw, nil), "a prepared run passes the launch check")

	r.ClearGatewayRun(sb)
	assert.Nil(t, piGatewayRunFor(sb))

	// No model list: pi runs offline, so the prepare fails and nothing is
	// registered.
	block.Models = nil
	err := r.PrepareGatewayRun(sb, GatewayRun{Block: block, BaseURL: "https://gw.example.com"})
	assert.ErrorContains(t, err, "model list")
	assert.Nil(t, piGatewayRunFor(sb))
}

func TestBuildPiRunCommand_GatewayTokenSeedBeforeEnv(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv("FULLSEND_PI_PROVIDER", "")
	params := piTestParams()
	params.SandboxName = "sb-gw-seed"
	params.Model = "gateway/m1"
	registerPiGatewayRun(t, params.SandboxName, piGatewayTestRun())

	cmd := buildPiRunCommand(params, &piManifest{}, nil, "")
	seed := PiGatewayTokenSeed(PiRuntime{}.ConfigDir())
	seedIdx := strings.Index(cmd, seed)
	envIdx := strings.Index(cmd, ". '/sandbox/workspace/.env'")
	unset := strings.Index(cmd, piGatewayEnvUnset())
	require.GreaterOrEqual(t, seedIdx, 0, cmd)
	require.GreaterOrEqual(t, envIdx, 0, cmd)
	assert.Less(t, strings.Index(cmd, piGatewayConfigGuard(PiRuntime{}.ConfigDir(), piGatewayTestRun().configSum())), seedIdx, "after the first guard pass")
	assert.Less(t, seedIdx, envIdx, "seeded before the agent-writable .env")
	assert.Less(t, seedIdx, unset, "seeded before the family is cleared")

	SetPiGatewayRun(params.SandboxName, nil)
	assert.NotContains(t, buildPiRunCommand(params, &piManifest{}, nil, ""), seed, "no block: no seed")
}

func TestNeedsGatewayRoute(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv("FULLSEND_PI_PROVIDER", "")
	assert.True(t, NeedsGatewayRoute("pi", "gateway/m1", "", nil))
	assert.True(t, NeedsGatewayRoute("pi", "Gateway/m1", "", nil), "case-folded")
	assert.True(t, NeedsGatewayRoute("pi", "", "gateway/m1", nil), "agent definition model")
	assert.True(t, NeedsGatewayRoute("pi", "fast", "", map[string]string{"fast": "gateway/m1"}), "alias")
	assert.False(t, NeedsGatewayRoute("pi", "openai/gpt-5", "", nil))
	assert.False(t, NeedsGatewayRoute("claude", "gateway/m1", "", nil), "only pi carries the route")
}

func TestGatewayChildren_NotPi(t *testing.T) {
	assert.Empty(t, GatewayChildren("claude", "/nonexistent", nil, nil, "a", nil))
	assert.Empty(t, GatewayChildren("pi", "/nonexistent", nil, nil, "a", nil))
}
