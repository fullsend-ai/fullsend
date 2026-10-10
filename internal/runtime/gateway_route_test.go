package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// Overlapping seeds (iteration start and a refresher's re-seed) each
// write their own temp file, so neither fails and none is left behind.
func TestPiGatewayTokenSeed_Concurrent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			cmd := exec.Command("sh", "-c", PiGatewayTokenSeed(dir))
			cmd.Env = append(os.Environ(), "INFERENCE_GATEWAY_API_KEY="+piPlaceholderPrefix+"v"+strconv.Itoa(i)+"_INFERENCE_GATEWAY_API_KEY")
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, out)
			}
			errs <- err
		}(i)
	}
	for i := 0; i < 16; i++ {
		require.NoError(t, <-errs)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the token file: no temp file is left behind")
	assert.Equal(t, piInferenceGatewayTokenFile, entries[0].Name())

}

// Overlapping OpenAI seeds write their own temp files, as the gateway seed
// does: none is left behind, auth.json holds one writer's whole value, and
// a writer that fails does so only because another writer replaced its
// value after its move.
func TestPiOpenAIAuthSeed_Concurrent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	errs := make(chan error, 16)
	for i := range 16 {
		go func(i int) {
			cmd := exec.Command("sh", "-c", PiOpenAIAuthSeed(dir))
			cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+piPlaceholderPrefix+"v"+strconv.Itoa(i)+"_OPENAI_API_KEY")
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, out)
			}
			errs <- err
		}(i)
	}
	// A writer whose value a later writer replaced still succeeds: that
	// is a legitimate outcome (a refresher's newer generation winning),
	// not a failed seed.
	for range 16 {
		require.NoError(t, <-errs)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only auth.json: no temp file is left behind")
	assert.Equal(t, piOpenAIAuthFile, entries[0].Name())
	data, err := os.ReadFile(filepath.Join(dir, piOpenAIAuthFile))
	require.NoError(t, err)
	assert.Regexp(t, `^\{"openai":\{"type":"api_key","key":"`+piPlaceholderPrefix+`v[0-9]+_OPENAI_API_KEY"\}\}\n$`, string(data))
}

// Both credential seeds fail with their own exit code, so the runner can
// tell a failed seed from an agent failure, and Run attributes that code
// to the seed only when the command carries one.
func TestPiCredentialSeeds_DedicatedExitCode(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	for name, seed := range map[string]string{
		"openai":  PiOpenAIAuthSeed(dir),
		"gateway": PiGatewayTokenSeed(dir),
	} {
		cmd := exec.Command("sh", "-c", seed)
		cmd.Env = append(os.Environ(), "OPENAI_API_KEY=sk-real", "INFERENCE_GATEWAY_API_KEY=real-token")
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "%s: %s", name, out)
		assert.Equal(t, piCredentialSeedFailedExit, exitErr.ExitCode(), name)
	}
	assert.True(t, piRunSeedsCredential("x && "+PiOpenAIAuthSeed(dir)+" && y", dir))
	assert.True(t, piRunSeedsCredential("x && "+PiGatewayTokenSeed(dir)+" && y", dir))
	assert.False(t, piRunSeedsCredential("pi --model anthropic-vertex/claude", dir))
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
	assert.Equal(t, r.ConfigDir()+"/"+PiInferenceGatewayConfigFile, gw.ConfigFile)
	assert.Contains(t, strings.Join(piGatewayEnvParts(gw), " "), "export INFERENCE_GATEWAY_CONFIG_FILE='"+r.ConfigDir()+"/"+PiInferenceGatewayConfigFile+"'",
		"the parent reads exactly the guarded file, never an overlay")
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

// A Vertex parent with gateway children: the children alone put the run
// on the gateway route, so each source must be found, aliases resolved and
// tombstones ignored, as for OpenAIChildren.
func TestGatewayChildren(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	agent := filepath.Join(t.TempDir(), "code.md")
	require.NoError(t, os.WriteFile(agent, []byte("---\nname: code\nmodel: opus\n---\nYou code.\n"), 0o644))
	skills := t.TempDir()
	writePersonaFile(t, skills, "checker", "---\nname: checker\nmodel: gateway/m1\n---\nCheck.\n")
	writePersonaFile(t, skills, "writer", "---\nname: writer\nmodel: sonnet\n---\nWrite.\n")

	got := GatewayChildren("pi", agent, map[string]*string{"writer": strp("fast"), "default": nil}, []string{skills}, "code", map[string]string{"fast": "gateway/m2"})
	assert.Equal(t, []PiChild{
		{Source: `persona "checker" frontmatter model`, Spec: "gateway/m1"},
		{Source: "subagents.writer", Spec: "gateway/m2", Configured: true},
	}, got)
	assert.False(t, NeedsGatewayRoute("pi", "", AgentDefinitionModel(agent), nil), "the parent itself stays off the route")

	assert.Empty(t, GatewayChildren("pi", agent, map[string]*string{"checker": strp("opus")}, []string{skills}, "code", nil),
		"a config override away from the gateway beats its frontmatter")
}

func TestGatewayChildren_NotPi(t *testing.T) {
	assert.Empty(t, GatewayChildren("claude", "/nonexistent", nil, nil, "a", nil))
	assert.Empty(t, GatewayChildren("pi", "/nonexistent", nil, nil, "a", nil))
}
