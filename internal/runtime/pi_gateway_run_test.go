package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piGatewayTestRun() *PiGatewayRun {
	return &PiGatewayRun{
		Config:    []byte(`{"providers":{"gateway":{"models":[{"id":"m1"}]}}}`),
		ModelIDs:  []string{"m1", "m2"},
		BaseURL:   "https://gw.example.com",
		TokenFile: "/sandbox/gateway/token",
	}
}

func registerPiGatewayRun(t *testing.T, sandboxName string, run *PiGatewayRun) {
	t.Helper()
	SetPiGatewayRun(sandboxName, run)
	t.Cleanup(func() { SetPiGatewayRun(sandboxName, nil) })
}

func TestBuildPiRunCommand_GatewayExtension(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv("FULLSEND_PI_PROVIDER", "")
	gwExt := "-e " + shellQuote(piInferenceGatewayExtensionPath)

	params := piTestParams()
	params.SandboxName = "sb-gw-ext"
	registerPiGatewayRun(t, params.SandboxName, piGatewayTestRun())

	for _, model := range []string{"gateway/m1", "Gateway/m1", "GATEWAY/m2"} {
		params.Model = model
		cmd := buildPiRunCommand(params, &piManifest{}, nil, "")
		assert.Contains(t, cmd, gwExt, "model %s loads the extension (prefix is case-folded)", model)
		assert.NotContains(t, cmd, "-e "+shellQuote(piVertexExtensionPath))
	}

	// Parent on Vertex, children admitted on the gateway: the parent loads
	// it too, since children inherit its environment and config dir.
	params.Model = "anthropic-vertex/claude-opus-4-6"
	withChildren := &piManifest{Agent: &piAgentManifest{Enabled: true, ProviderModels: map[string][]string{piGatewayProvider: {"m1"}}}}
	assert.Contains(t, buildPiRunCommand(params, withChildren, nil, ""), gwExt)
	assert.NotContains(t, buildPiRunCommand(params, &piManifest{}, nil, ""), gwExt,
		"no gateway model anywhere: no -e")
}

func TestBuildPiRunCommand_GatewayUnchangedWithoutBlock(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv("FULLSEND_PI_PROVIDER", "")
	params := piTestParams()
	params.SandboxName = "sb-gw-none"
	exts := []piManifestExtension{{Name: "inference-gateway-local", Path: "/sandbox/pi-config/extensions/x", SHA256: strings.Repeat("a", 64), Env: map[string]string{"INFERENCE_GATEWAY_BASE_URL": "http://local"}}}
	for _, model := range []string{"gateway/m1", "anthropic-vertex/claude-opus-4-6"} {
		params.Model = model
		cmd := buildPiRunCommand(params, &piManifest{}, exts, "")
		assert.NotContains(t, cmd, "-e "+shellQuote(piInferenceGatewayExtensionPath), "no block: the runner does not own the route")
		assert.NotContains(t, cmd, PiInferenceGatewayConfigFile)
		assert.NotContains(t, cmd, "unset $(command -p env")
		assert.Contains(t, cmd, "export INFERENCE_GATEWAY_BASE_URL='http://local'", "plugin env is exported as today")
	}
	require.NoError(t, validatePiGatewayRun(nil, []PluginInput{{Name: "inference-gateway", Path: "/x/inference-gateway", Env: map[string]string{"INFERENCE_GATEWAY_BASE_URL": "x"}}}))
}

func TestBuildPiRunCommand_GatewayGuardAndEnvOrdering(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv("FULLSEND_PI_PROVIDER", "")
	params := piTestParams()
	params.SandboxName = "sb-gw-order"
	params.Model = "gateway/m1"
	run := piGatewayTestRun()
	registerPiGatewayRun(t, params.SandboxName, run)
	exts := []piManifestExtension{{Name: "other", Path: "/sandbox/pi-config/extensions/other", SHA256: strings.Repeat("a", 64), Env: map[string]string{"OTHER_KEY": "v"}}}

	cmd := buildPiRunCommand(params, &piManifest{}, exts, "abc123")
	guard := piGatewayConfigGuard(PiRuntime{}.ConfigDir(), run.configSum())
	assert.Equal(t, 2, strings.Count(cmd, guard), "the config guard runs twice")
	envIdx := strings.Index(cmd, ". '/sandbox/workspace/.env'")
	first := strings.Index(cmd, guard)
	second := strings.LastIndex(cmd, guard)
	plugin := strings.Index(cmd, "export OTHER_KEY='v'")
	unset := strings.Index(cmd, piGatewayEnvUnset())
	baseURL := strings.Index(cmd, "export INFERENCE_GATEWAY_BASE_URL='https://gw.example.com'")
	token := strings.Index(cmd, "export INFERENCE_GATEWAY_TOKEN_FILE='/sandbox/gateway/token'")
	launch := strings.Index(cmd, "--print")
	for _, i := range []int{envIdx, first, plugin, unset, baseURL, token, launch} {
		require.GreaterOrEqual(t, i, 0, cmd)
	}
	assert.Less(t, first, envIdx, "first pass before .env")
	assert.Less(t, envIdx, second, "second pass after .env")
	assert.Less(t, strings.LastIndex(cmd, piManifestGuard(PiRuntime{}.piManifestPath(), "abc123")), second, "after the manifest's second pass")
	assert.Less(t, plugin, unset, "the family is cleared after plugin env")
	assert.Less(t, unset, baseURL)
	assert.Less(t, baseURL, token)
	assert.Less(t, token, launch)

	// No token file: only the base URL is re-exported.
	run.TokenFile = ""
	registerPiGatewayRun(t, params.SandboxName, run)
	assert.NotContains(t, buildPiRunCommand(params, &piManifest{}, nil, ""), "INFERENCE_GATEWAY_TOKEN_FILE=")
}

// TestPiGatewayConfigGuard_Shell runs the guard under sh against a real
// directory: the runner's file passes; a modified one, a missing one and a
// planted local overlay are refused with the guard's exit code.
func TestPiGatewayConfigGuard_Shell(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	run := piGatewayTestRun()
	dir := t.TempDir()
	cfg := filepath.Join(dir, PiInferenceGatewayConfigFile)
	guard := piGatewayConfigGuard(dir, run.configSum())
	exitOf := func() int {
		err := exec.Command("sh", "-c", guard).Run()
		if err == nil {
			return 0
		}
		var ee *exec.ExitError
		require.ErrorAs(t, err, &ee)
		return ee.ExitCode()
	}

	assert.Equal(t, piGatewayConfigTamperedExit, exitOf(), "missing file")
	require.NoError(t, os.WriteFile(cfg, run.Config, 0o644))
	assert.Equal(t, 0, exitOf(), "the runner's file")
	require.NoError(t, os.WriteFile(cfg, append(append([]byte(nil), run.Config...), ' '), 0o644))
	assert.Equal(t, piGatewayConfigTamperedExit, exitOf(), "a modified file")
	require.NoError(t, os.WriteFile(cfg, run.Config, 0o644))
	local := filepath.Join(dir, PiInferenceGatewayLocalConfigFile)
	require.NoError(t, os.WriteFile(local, []byte(`{}`), 0o644))
	assert.Equal(t, piGatewayConfigTamperedExit, exitOf(), "a planted local overlay")
	require.NoError(t, os.Remove(local))
	require.NoError(t, os.Symlink("/nonexistent", local))
	assert.Equal(t, piGatewayConfigTamperedExit, exitOf(), "a dangling local symlink")
}

func TestPiGatewayEnvUnset_Shell(t *testing.T) {
	run := piGatewayTestRun()
	script := "export INFERENCE_GATEWAY_BASE_URL=evil INFERENCE_GATEWAY_PROVIDER_ID=x INFERENCE_GATEWAY_AUTH_HEADER=y KEEP_ME=1" +
		" " + strings.Join(piGatewayEnvParts(run), " ") +
		" && command -p env | command -p grep -E '^(INFERENCE_GATEWAY_|KEEP_ME)' | command -p sort"
	out, err := exec.Command("sh", "-c", script).Output()
	require.NoError(t, err)
	assert.Equal(t, "INFERENCE_GATEWAY_BASE_URL=https://gw.example.com\nINFERENCE_GATEWAY_TOKEN_FILE=/sandbox/gateway/token\nKEEP_ME=1\n", string(out))
}

func TestValidatePiGatewayPluginEnv(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidatePiGatewayPluginEnv(nil))
	require.NoError(t, ValidatePiGatewayPluginEnv([]PluginInput{{Path: "/p/go-diag", Env: map[string]string{"GO_FLAGS": "x"}}}))

	err := ValidatePiGatewayPluginEnv([]PluginInput{{Path: "/p/x", Env: map[string]string{"INFERENCE_GATEWAY_BASE_URL": "x"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFERENCE_GATEWAY_BASE_URL")
	require.Error(t, ValidatePiGatewayPluginEnv([]PluginInput{{Path: "/p/x", Env: map[string]string{"inference_gateway_provider_id": "x"}}}))

	err = ValidatePiGatewayPluginEnv([]PluginInput{{Path: "/p/inference-gateway"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference-gateway extension")
	require.Error(t, ValidatePiGatewayPluginEnv([]PluginInput{{Name: "inference-gateway", Path: "/p/vendored"}}))
	require.Error(t, ValidatePiGatewayPluginEnv([]PluginInput{{Name: "gw", Path: "/p/inference-gateway"}}))
}

func TestValidatePiGatewayRun(t *testing.T) {
	t.Parallel()
	run := piGatewayTestRun()
	require.NoError(t, validatePiGatewayRun(run, nil))
	noURL := *run
	noURL.BaseURL = " "
	err := validatePiGatewayRun(&noURL, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFERENCE_GATEWAY_BASE_URL")
	noCfg := *run
	noCfg.Config = nil
	require.Error(t, validatePiGatewayRun(&noCfg, nil))
	require.Error(t, validatePiGatewayRun(run, []PluginInput{{Path: "/p/x", Env: map[string]string{"INFERENCE_GATEWAY_TOKEN_FILE": "/t"}}}))
}

func TestSetPiGatewayRun(t *testing.T) {
	t.Parallel()
	assert.Nil(t, piGatewayRunFor("sb-gw-set"))
	run := piGatewayTestRun()
	SetPiGatewayRun("sb-gw-set", run)
	run.ModelIDs[0] = "mutated"
	got := piGatewayRunFor("sb-gw-set")
	require.NotNil(t, got)
	assert.Equal(t, []string{"m1", "m2"}, got.ModelIDs, "registration is a copy")
	assert.Nil(t, piGatewayRunFor("sb-gw-other"), "per sandbox")
	SetPiGatewayRun("sb-gw-set", nil)
	assert.Nil(t, piGatewayRunFor("sb-gw-set"))
}

func TestPiAgentProviderModels_Gateway(t *testing.T) {
	t.Parallel()
	_, ok := piAgentProviderModels(nil)[piGatewayProvider]
	assert.False(t, ok, "no block: no gateway children")
	ids := []string{"m1", "m2"}
	got := piAgentProviderModels(ids)
	assert.Equal(t, ids, got[piGatewayProvider])
	ids[0] = "mutated"
	assert.Equal(t, "m1", got[piGatewayProvider][0], "the manifest holds a copy")
	assert.NotEmpty(t, got[piGoogleVertexProvider])
}

func TestPiAgentProbe_Gateway(t *testing.T) {
	t.Parallel()
	assert.Contains(t, piAgentProbeCommand(), shellQuote(piInferenceGatewayExtensionPath))
	_, exts := parsePiAgentProbe("/usr/bin/pi\n" + piVertexExtensionPath + "\n" + piInferenceGatewayExtensionPath + "\n")
	assert.Equal(t, []string{piVertexExtensionPath, piInferenceGatewayExtensionPath}, exts)
	assert.Equal(t, []string{piVertexExtensionPath}, piAgentProviderExtensions(exts, false), "no block: children do not load it")
	assert.Equal(t, exts, piAgentProviderExtensions(exts, true))
}
