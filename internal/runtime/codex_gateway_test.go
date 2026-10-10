package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// testCodexGatewayBaseURL is the fullsend-gateway provider's base_url for a
// block whose url is https://gateway.example.com.
const testCodexGatewayBaseURL = "https://gateway.example.com/v1"

func TestCodexGatewayBaseURL(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"https://gateway.example.com":   testCodexGatewayBaseURL,
		"https://gateway.example.com/":  testCodexGatewayBaseURL,
		" https://gateway.example.com ": testCodexGatewayBaseURL,
		"http://127.0.0.1:8080":         "http://127.0.0.1:8080/v1",
	} {
		got, err := codexGatewayBaseURL(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{
		"",
		"http://gateway.example.com",
		"https://gateway.example.com/v1",
		"https://gateway.example.com:8443",
		"https://user@gateway.example.com",
		"https://gate'way.example.com",
	} {
		_, err := codexGatewayBaseURL(in)
		assert.Error(t, err, "%q must be refused", in)
	}
}

func TestCodexPrepareGatewayRun(t *testing.T) {
	t.Parallel()

	r := CodexRuntime{}
	const sb = "sb-codex-prepare-gateway"
	require.NoError(t, r.PrepareGatewayRun(sb, GatewayRun{
		Block:   config.InferenceGatewayConfig{URL: "https://gateway.example.com", Audience: "aud"},
		BaseURL: "https://gateway.example.com",
	}))
	assert.Equal(t, testCodexGatewayBaseURL, codexGatewayRunFor(sb))
	r.ClearGatewayRun(sb)
	assert.Empty(t, codexGatewayRunFor(sb))

	err := r.PrepareGatewayRun(sb, GatewayRun{BaseURL: ""})
	require.Error(t, err)
	assert.Empty(t, codexGatewayRunFor(sb), "a refused run registers nothing")
}

func TestCodexGatewayCredentialSeed_IsItsOwnRoute(t *testing.T) {
	t.Parallel()

	r := CodexRuntime{}
	gw := r.GatewayCredentialSeed()
	oa := OpenAIRouteSeed(r)
	assert.Equal(t, "INFERENCE_GATEWAY_API_KEY", gw.PlaceholderEnv)
	assert.Equal(t, r.ConfigDir()+"/gateway-token", gw.File)
	assert.Equal(t, r.gatewayAuthSeed(), gw.Seed)
	assert.False(t, gw.IsZero())
	assert.NotEqual(t, oa.PlaceholderEnv, gw.PlaceholderEnv, "independent handoffs per route")
	assert.NotEqual(t, oa.File, gw.File, "independent handoffs per route")
}

// codexGatewaySeedIn renders the gateway seed against a temp config dir.
func codexGatewaySeedIn(dir string) string {
	return strings.ReplaceAll(CodexRuntime{}.gatewayAuthSeed(), sandbox.SandboxCodexConfig, dir)
}

// The seed writes a gateway placeholder to the token file as-is; anything
// else fails closed and leaves no file.
func TestCodexGatewayAuthSeed_Shell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	placeholder := piPlaceholderPrefix + "v123_INFERENCE_GATEWAY_API_KEY"
	dir := filepath.Join(t.TempDir(), "cfg")
	cmd := exec.Command("sh", "-c", codexGatewaySeedIn(dir))
	cmd.Env = append(os.Environ(), "INFERENCE_GATEWAY_API_KEY="+placeholder)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(filepath.Join(dir, codexGatewayTokenFile))
	require.NoError(t, err)
	assert.Equal(t, placeholder, string(data))

	for name, value := range map[string]string{
		"a real token":     "eyJhbGciOi.eyJzdWIiOi.sig",
		"unset":            "",
		"another route":    piPlaceholderPrefix + "v1_OPENAI_API_KEY",
		"unexpected chars": piPlaceholderPrefix + "v1;x_INFERENCE_GATEWAY_API_KEY",
	} {
		dir := filepath.Join(t.TempDir(), "cfg")
		cmd := exec.Command("sh", "-c", codexGatewaySeedIn(dir))
		cmd.Env = append(os.Environ(), "INFERENCE_GATEWAY_API_KEY="+value)
		out, err := cmd.CombinedOutput()
		assert.Error(t, err, "%s: %s", name, out)
		assert.Contains(t, string(out), "refusing to run codex", name)
		_, statErr := os.Stat(filepath.Join(dir, codexGatewayTokenFile))
		assert.True(t, os.IsNotExist(statErr), "%s: no token file", name)
	}
}

// The iteration-start seed and a refresher's re-seed can overlap: each
// writes its own temp file, so neither fails and none is left behind.
func TestCodexGatewayAuthSeed_Concurrent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			cmd := exec.Command("sh", "-c", codexGatewaySeedIn(dir))
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
	assert.Equal(t, codexGatewayTokenFile, entries[0].Name())
}

// The gateway auth.command prints the seeded placeholder and nothing else;
// every other content fails the model call with no fallback.
func TestCodexGatewayAuthScript_Executes(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, codexGatewayAuthScriptFile)
	tokenPath := filepath.Join(dir, codexGatewayTokenFile)
	body := strings.ReplaceAll(string(codexGatewayAuthScriptSH), codexSandboxGatewayTokenFile, tokenPath)
	require.NoError(t, os.WriteFile(scriptPath, []byte(body), 0o755))
	placeholder := piPlaceholderPrefix + "vabc123_INFERENCE_GATEWAY_API_KEY"

	require.NoError(t, os.WriteFile(tokenPath, []byte(placeholder), 0o600))
	out, err := exec.Command("/bin/sh", scriptPath).Output()
	require.NoError(t, err)
	assert.Equal(t, placeholder, string(out))

	require.NoError(t, os.WriteFile(tokenPath, []byte(piPlaceholderPrefix+"vabc_OPENAI_API_KEY"), 0o600))
	out, err = exec.Command("/bin/sh", scriptPath).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "does not hold a gateway placeholder", "the OpenAI route's placeholder is not this route's")

	require.NoError(t, os.WriteFile(tokenPath, []byte("eyJhbGciOi.eyJzdWIiOi.sig"), 0o600))
	out, err = exec.Command("/bin/sh", scriptPath).CombinedOutput()
	require.Error(t, err)
	assert.NotContains(t, string(out), "eyJhbGciOi", "a real token must not be echoed into the run log")

	require.NoError(t, os.Remove(tokenPath))
	out, err = exec.Command("/bin/sh", scriptPath).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "missing or unreadable")
}

// The `-c` flags select and pin fullsend-gateway for a gateway/ model, the
// gateway token is seeded before the agent-writable .env, and its
// placeholder is unset after it. Nothing of the OpenAI route is used.
func TestBuildCodexRunCommand_GatewayRoute(t *testing.T) {
	t.Parallel()

	r := CodexRuntime{}
	digests := testRunnerHeldDigests
	digests.GatewayBaseURL = testCodexGatewayBaseURL
	cmd := buildCodexRunCommand(hooksOn, codexModel{ID: "vendor/org/model", Gateway: true}, "high", true, digests)

	assert.Contains(t, cmd, "--model 'vendor/org/model'", "the gateway/ prefix is stripped, the rest is passed as-is")
	assert.Contains(t, cmd, "-c 'model_provider="+codexGatewayProviderID+"'")
	assert.Contains(t, cmd, "-c "+shellQuote(`model_providers.fullsend-gateway.base_url="`+testCodexGatewayBaseURL+`"`))
	assert.Contains(t, cmd, "-c "+shellQuote(`model_providers.fullsend-gateway.auth.command="`+r.codexGatewayAuthScriptPath()+`"`))
	assert.NotContains(t, cmd, "model_provider="+codexProviderID)
	assert.NotContains(t, cmd, codexBaseURL)
	assert.NotContains(t, cmd, r.OpenAIAuthSeed(), "a gateway run needs no OpenAI placeholder")

	envAt := strings.Index(cmd, ". '"+sandbox.SandboxWorkspace+"/.env'")
	require.Positive(t, envAt)
	seedAt := strings.Index(cmd, r.gatewayAuthSeed())
	require.Positive(t, seedAt, "the gateway token is seeded")
	assert.Less(t, seedAt, envAt, "seeded before .env can replace the placeholder")
	unsetAt := strings.Index(cmd, "unset OPENAI_BASE_URL OPENAI_API_KEY CODEX_API_KEY INFERENCE_GATEWAY_API_KEY ")
	require.Positive(t, unsetAt, "user-supplied OpenAI variables and the gateway placeholder are unset")
	assert.Greater(t, unsetAt, envAt, "unset after .env")

	scriptCheck := codexSHACheck(r.codexGatewayAuthScriptPath(), codexAssetSHA256(codexGatewayAuthScriptSH))
	assert.Equal(t, 2, strings.Count(cmd, scriptCheck), "the gateway auth script is pinned before and after .env")
}

// With a block registered, an openai/ model keeps the direct route: the
// fullsend-openai pin, the OpenAI seed and its endpoint, unchanged.
func TestBuildCodexRunCommand_OpenAIRouteWithGatewayConfigured(t *testing.T) {
	t.Parallel()

	r := CodexRuntime{}
	digests := testRunnerHeldDigests
	digests.GatewayBaseURL = testCodexGatewayBaseURL
	cmd := buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, digests)

	assert.Contains(t, cmd, "-c 'model_provider="+codexProviderID+"'")
	assert.Contains(t, cmd, "-c "+shellQuote(`model_providers.fullsend-openai.base_url="`+codexBaseURL+`"`))
	assert.Contains(t, cmd, r.OpenAIAuthSeed())
	assert.NotContains(t, cmd, "model_provider="+codexGatewayProviderID)
	assert.NotContains(t, cmd, r.gatewayAuthSeed())
	assert.Contains(t, cmd, "unset OPENAI_BASE_URL OPENAI_API_KEY CODEX_API_KEY NODE_OPTIONS")

	plain := buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, testRunnerHeldDigests)
	assert.NotContains(t, plain, codexGatewayAuthScriptFile, "no block, no gateway script guard")
}

// An edited or missing gateway auth script fails the asset guard, exactly as
// the OpenAI one does.
func TestCodexAssetGuard_GatewayAuthScript(t *testing.T) {
	dir := t.TempDir()
	r := CodexRuntime{}
	digests := testRunnerHeldDigests
	digests.GatewayBaseURL = testCodexGatewayBaseURL
	guard := strings.ReplaceAll(codexAssetGuard(r, false, digests), sandbox.SandboxCodexConfig, dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, codexConfigFile), []byte("cfg"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, codexAuthScriptFile), codexAuthScriptSH, 0o755))

	_, err := exec.Command("/bin/sh", "-c", guard+" && echo RAN").CombinedOutput()
	require.Error(t, err, "a missing gateway script must fail the guard")
	assert.Equal(t, codexHooksMissingExit, exitCodeOf(t, err))

	require.NoError(t, os.WriteFile(filepath.Join(dir, codexGatewayAuthScriptFile), codexGatewayAuthScriptSH, 0o755))
	out, err := exec.Command("/bin/sh", "-c", guard+" && echo RAN").CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "RAN")

	require.NoError(t, os.WriteFile(filepath.Join(dir, codexGatewayAuthScriptFile),
		[]byte("#!/bin/sh\necho not-a-placeholder\n"), 0o755))
	_, err = exec.Command("/bin/sh", "-c", guard+" && echo RAN").CombinedOutput()
	require.Error(t, err)
	assert.Equal(t, codexHooksMissingExit, exitCodeOf(t, err))
}

// With a gateway registered, Bootstrap renders both providers, uploads the
// gateway auth script byte-identical and executable, and records the
// gateway base_url in the runner-held digests Run reads.
func TestCodexRuntimeBootstrap_GatewayProvider(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	r := CodexRuntime{}
	const sb = "sb-codex-gateway-bootstrap"
	require.NoError(t, r.PrepareGatewayRun(sb, GatewayRun{BaseURL: "https://gateway.example.com"}))
	t.Cleanup(func() { r.ClearGatewayRun(sb); forgetRunnerHeldDigests(sb) })

	require.NoError(t, r.Bootstrap(bootstrapInput{
		sandboxName: sb,
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
	}))

	cfg := storedUpload(t, storeDir, r.codexConfigPath())
	assert.Contains(t, string(cfg), "[model_providers."+codexProviderID+"]")
	assert.Contains(t, string(cfg), "[model_providers."+codexGatewayProviderID+"]")
	assert.Contains(t, string(cfg), `base_url = "`+testCodexGatewayBaseURL+`"`)
	assert.Equal(t, codexGatewayAuthScriptSH, storedUpload(t, storeDir, r.codexGatewayAuthScriptPath()))
	assert.Contains(t, readFileString(t, logPath), "chmod 755 '"+r.codexGatewayAuthScriptPath()+"'")

	held, ok := lookupRunnerHeldDigests(sb)
	require.True(t, ok)
	assert.Equal(t, testCodexGatewayBaseURL, held.GatewayBaseURL)
	assert.Equal(t, codexAssetSHA256(cfg), held.ConfigTOML, "the gateway config is what the digest guard pins")
}

func TestCodexRuntimeBootstrap_NoGatewayWithoutBlock(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	r := CodexRuntime{}
	const sb = "sb-codex-no-gateway"
	t.Cleanup(func() { forgetRunnerHeldDigests(sb) })
	require.NoError(t, r.Bootstrap(bootstrapInput{
		sandboxName: sb,
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
	}))
	assert.NotContains(t, string(storedUpload(t, storeDir, r.codexConfigPath())), codexGatewayProviderID)
	_, err := os.Stat(filepath.Join(storeDir, sanitizeStorePath(r.codexGatewayAuthScriptPath())))
	assert.True(t, os.IsNotExist(err), "no gateway script without a block")
	held, ok := lookupRunnerHeldDigests(sb)
	require.True(t, ok)
	assert.Empty(t, held.GatewayBaseURL)
}

// Run launches a gateway/ model on fullsend-gateway when Bootstrap rendered
// it, and refuses one before spending when it did not.
func TestCodexRun_GatewayModel(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	held, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok)
	held.GatewayBaseURL = testCodexGatewayBaseURL
	recordRunnerHeldDigests("sb", held)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", filepath.Join("testdata", "codex", "basic_run.ndjson"))

	metrics := &RunMetrics{}
	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "gateway/vendor/org/model",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, "vendor/org/model", metrics.Model)
	log := readFileString(t, logPath)
	assert.Contains(t, log, "model_provider="+codexGatewayProviderID)
	assert.NotContains(t, log, "model_provider="+codexProviderID)
}

func TestCodexRun_GatewayModelWithoutBlock(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "gateway/vendor/org/model",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})
	require.Error(t, err)
	assert.Equal(t, -1, exit)
	assert.Contains(t, err.Error(), "no inference.gateway block applies")
	assert.NotContains(t, readFileString(t, logPath), "exec --json", "the run must not start")
}
