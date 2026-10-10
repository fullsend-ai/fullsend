package runtime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// registerClaudeGateway registers a gateway run for sandboxName and clears
// it when the test ends.
func registerClaudeGateway(t *testing.T, sandboxName string, apiKey bool) {
	t.Helper()
	block := config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"}
	if apiKey {
		block = config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey}
	}
	require.NoError(t, ClaudeRuntime{}.PrepareGatewayRun(sandboxName, GatewayRun{Block: block, BaseURL: "https://gw.example.com"}))
	t.Cleanup(func() { ClaudeRuntime{}.ClearGatewayRun(sandboxName) })
}

func TestNeedsGatewayRoute_Claude(t *testing.T) {
	aliases := map[string]string{"opus": "gateway/claude-opus-5-5"}
	for _, tc := range []struct {
		name, run, agent string
		want             bool
	}{
		{"run model", "gateway/claude-haiku-5-5", "", true},
		{"case-folded prefix", "Gateway/claude-haiku-5-5", "", true},
		{"open-weight id with slashes", "gateway/vendor/org/model", "", true},
		{"alias remapped to a gateway model", "opus", "", true},
		{"agent frontmatter when the run names none", "", "gateway/claude-haiku-5-5", true},
		{"the run model wins over the frontmatter", "sonnet", "gateway/claude-haiku-5-5", false},
		{"a Vertex alias", "sonnet", "", false},
		{"pi's provider form for another provider", "openai/gpt-5", "", false},
		{"no model", "", "", false},
	} {
		assert.Equal(t, tc.want, NeedsGatewayRoute("claude", tc.run, tc.agent, aliases), tc.name)
	}
	assert.False(t, NeedsGatewayRoute("codex", "gateway/m", "", nil), "codex has no route")
	assert.Nil(t, GatewayChildren("claude", "", nil, nil, "", nil), "Claude Code children are not classified")
}

func TestClaudeGatewayModel(t *testing.T) {
	id, ok := claudeGatewayModel("gateway/vendor/org/model", "", nil)
	assert.True(t, ok)
	assert.Equal(t, "vendor/org/model", id, "only the first segment is the selector")
	id, ok = claudeGatewayModel("", "GATEWAY/claude-haiku-5-5", nil)
	assert.True(t, ok)
	assert.Equal(t, "claude-haiku-5-5", id)
	_, ok = claudeGatewayModel("opus", "", nil)
	assert.False(t, ok)
}

func TestClaudeRuntime_GatewayRouteRuntime(t *testing.T) {
	var _ GatewayRouteRuntime = ClaudeRuntime{}

	assert.ErrorContains(t, ClaudeRuntime{}.PrepareGatewayRun("fs-claude-gw-empty", GatewayRun{}), "no base URL")
	assert.Nil(t, claudeGatewayRunFor("fs-claude-gw-empty"))

	registerClaudeGateway(t, "fs-claude-gw-oidc", false)
	gw := claudeGatewayRunFor("fs-claude-gw-oidc")
	require.NotNil(t, gw)
	assert.Equal(t, "https://gw.example.com", gw.baseURL)
	assert.False(t, gw.apiKey)

	registerClaudeGateway(t, "fs-claude-gw-key", true)
	require.NotNil(t, claudeGatewayRunFor("fs-claude-gw-key"))
	assert.True(t, claudeGatewayRunFor("fs-claude-gw-key").apiKey)

	seed := ClaudeRuntime{}.GatewayCredentialSeed()
	assert.Equal(t, "INFERENCE_GATEWAY_API_KEY", seed.PlaceholderEnv)
	assert.Equal(t, sandbox.SandboxClaudeConfig+"/inference-gateway.token", seed.File)
	assert.Equal(t, gatewayTokenSeed(sandbox.SandboxClaudeConfig), seed.Seed)
	assert.Equal(t, PiGatewayTokenSeed(sandbox.SandboxClaudeConfig), seed.Seed, "the same seed as pi's, under Claude Code's config dir")

	ClaudeRuntime{}.ClearGatewayRun("fs-claude-gw-oidc")
	assert.Nil(t, claudeGatewayRunFor("fs-claude-gw-oidc"))
}

func TestSetClaudeGatewayAgentModel(t *testing.T) {
	setClaudeGatewayAgentModel("fs-claude-gw-none", "gateway/m")
	assert.Nil(t, claudeGatewayRunFor("fs-claude-gw-none"), "no registration is created")

	registerClaudeGateway(t, "fs-claude-gw-agent", false)
	setClaudeGatewayAgentModel("fs-claude-gw-agent", "gateway/claude-haiku-5-5")
	assert.Equal(t, "gateway/claude-haiku-5-5", claudeGatewayRunFor("fs-claude-gw-agent").agentModel)
	assert.Equal(t, "https://gw.example.com", claudeGatewayRunFor("fs-claude-gw-agent").baseURL)
}

func TestClaudeGatewaySettings(t *testing.T) {
	assert.Nil(t, ClaudeRuntime{}.claudeGatewaySettings("fs-claude-gw-unset"))
	registerClaudeGateway(t, "fs-claude-gw-key-settings", true)
	assert.Nil(t, ClaudeRuntime{}.claudeGatewaySettings("fs-claude-gw-key-settings"), "the api-key mode needs no helper")
	registerClaudeGateway(t, "fs-claude-gw-oidc-settings", false)
	assert.Equal(t, map[string]any{"apiKeyHelper": "command -p cat '/sandbox/claude-config/inference-gateway.token'"},
		ClaudeRuntime{}.claudeGatewaySettings("fs-claude-gw-oidc-settings"))
}

func TestMergeClaudeSettings(t *testing.T) {
	base := []byte(`{"hooks":{"PreToolUse":[]}}`)
	out, err := mergeClaudeSettings(base, nil)
	require.NoError(t, err)
	assert.Equal(t, base, out, "nothing to add leaves the file as generated")

	out, err = mergeClaudeSettings(base, map[string]any{"apiKeyHelper": "x"})
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Equal(t, "x", doc["apiKeyHelper"])
	assert.Contains(t, doc, "hooks", "the hooks are kept")

	_, err = mergeClaudeSettings([]byte("not json"), map[string]any{"apiKeyHelper": "x"})
	assert.Error(t, err)
}

// securityAllOff is a hook config with every hook disabled, so
// installClaudeHooks uploads only hooks.json.
func securityAllOff() security.SandboxHookConfig {
	off := false
	return security.SandboxHookConfigFromHarness(&harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		Tirith:                  &harness.TirithConfig{Enabled: &off},
		SSRFPreTool:             &off,
		CanaryPreTool:           &off,
		CanaryPostTool:          &off,
		SecretRedactPostTool:    &off,
		UnicodePostTool:         &off,
		ContextSuppressPostTool: &off,
	}}})
}

// TestInstallClaudeHooks_GatewaySettings: the oidc apiKeyHelper lands in the
// --settings file next to the hooks.
func TestInstallClaudeHooks_GatewaySettings(t *testing.T) {
	stubDir := t.TempDir()
	captured := filepath.Join(t.TempDir(), "hooks.json")
	// Copy the uploaded hooks.json aside: `sandbox upload <name> <src> <dest>`.
	script := "#!/bin/sh\ncase \"$*\" in *hooks.json*) for a; do last2=$prev; prev=$a; done; cp \"$last2\" '" + captured + "' ;; esac\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", stubDir+":/usr/bin:/bin")

	require.NoError(t, installClaudeHooks("test-sandbox", securityAllOff(), map[string]any{"apiKeyHelper": "helper"}))
	data, err := os.ReadFile(captured)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, "helper", doc["apiKeyHelper"])
	assert.Contains(t, doc, "hooks")
}

func TestBuildRunCommand_GatewayOIDC(t *testing.T) {
	const sb = "fs-claude-gw-cmd-oidc"
	registerClaudeGateway(t, sb, false)
	cmd := buildRunCommand(RunParams{
		SandboxName:    sb,
		AgentBaseName:  "agent",
		RepoDir:        "/sandbox/workspace/repo",
		Model:          "gateway/claude-haiku-5-5",
		FallbackModels: []string{"gateway/claude-sonnet-5-5", "sonnet"},
	})

	seedAt := strings.Index(cmd, gatewayTokenSeed(sandbox.SandboxClaudeConfig))
	envAt := strings.Index(cmd, ". /sandbox/workspace/.env")
	loaderAt := strings.Index(cmd, claudeLoaderEnvUnset)
	unsetAt := strings.Index(cmd, "unset "+strings.Join(claudeGatewayEnvUnset, " "))
	baseAt := strings.Index(cmd, "export ANTHROPIC_BASE_URL='https://gw.example.com'")
	launchAt := strings.Index(cmd, `"$FULLSEND_CLAUDE_BIN" --print`)
	for name, at := range map[string]int{"seed": seedAt, ".env": envAt, "loader unset": loaderAt, "gateway unset": unsetAt, "base url": baseAt, "launch": launchAt} {
		require.NotEqual(t, -1, at, "%s missing from %s", name, cmd)
	}
	assert.Less(t, seedAt, envAt, "the token file is seeded before .env can replace the placeholder")
	assert.Less(t, envAt, unsetAt, "the route's variables are cleared after .env")
	assert.Less(t, loaderAt, unsetAt)
	assert.Less(t, unsetAt, baseAt)
	assert.Less(t, baseAt, launchAt)
	assert.Contains(t, cmd, "export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	assert.Contains(t, cmd, "export CLAUDE_CODE_API_KEY_HELPER_TTL_MS=10000")
	assert.NotContains(t, cmd, "export ANTHROPIC_API_KEY", "oidc presents the credential through the helper")
	for _, name := range []string{"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		assert.Contains(t, claudeGatewayEnvUnset, name)
	}

	assert.Contains(t, cmd, "--model 'claude-haiku-5-5'", "the gateway/ prefix only selects the route")
	assert.Contains(t, cmd, "--fallback-model 'claude-sonnet-5-5,sonnet'")
	assert.Contains(t, cmd, `--settings '{"apiKeyHelper":"command -p cat '\''/sandbox/claude-config/inference-gateway.token'\''"}'`, "no hooks file, so the helper is inline")

	withHooks := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/m", HooksSettingsPath: "/sandbox/claude-config/hooks.json"})
	assert.Contains(t, withHooks, "--settings '/sandbox/claude-config/hooks.json'")
	assert.Equal(t, 1, strings.Count(withHooks, "--settings"), "Claude Code reads one --settings; the helper is in the hooks file")
}

func TestBuildRunCommand_GatewayAPIKey(t *testing.T) {
	const sb = "fs-claude-gw-cmd-key"
	registerClaudeGateway(t, sb, true)
	cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/claude-haiku-5-5"})

	captureAt := strings.Index(cmd, `FULLSEND_GATEWAY_PLACEHOLDER="$INFERENCE_GATEWAY_API_KEY" && readonly FULLSEND_GATEWAY_PLACEHOLDER`)
	envAt := strings.Index(cmd, ". /sandbox/workspace/.env")
	exportAt := strings.Index(cmd, `export ANTHROPIC_API_KEY="$FULLSEND_GATEWAY_PLACEHOLDER"`)
	require.NotEqual(t, -1, captureAt, cmd)
	require.NotEqual(t, -1, exportAt, cmd)
	assert.Less(t, captureAt, envAt, "the placeholder is read before .env")
	assert.Less(t, envAt, exportAt)
	assert.Contains(t, cmd, gatewayPlaceholderCheck())
	assert.NotContains(t, cmd, "inference-gateway.token", "the api-key mode writes no token file")
	assert.NotContains(t, cmd, "--settings", "no helper in the api-key mode")
	assert.NotContains(t, cmd, "CLAUDE_CODE_API_KEY_HELPER_TTL_MS=")
	assert.Contains(t, cmd, "--model 'claude-haiku-5-5'")
}

func TestBuildRunCommand_GatewayAgentModel(t *testing.T) {
	const sb = "fs-claude-gw-cmd-agent"
	registerClaudeGateway(t, sb, true)
	setClaudeGatewayAgentModel(sb, "gateway/claude-haiku-5-5")
	cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r"})
	assert.Contains(t, cmd, "--model 'claude-haiku-5-5'", "a frontmatter gateway/ model becomes an explicit --model")

	aliased := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "opus", ModelAliases: map[string]string{"opus": "gateway/claude-opus-5-5"}})
	assert.Contains(t, aliased, "--model 'claude-opus-5-5'")
}

func TestBuildRunCommand_NoGatewayUnchanged(t *testing.T) {
	cmd := buildRunCommand(RunParams{SandboxName: "fs-claude-gw-not-registered", AgentBaseName: "agent", RepoDir: "/r", Model: "opus", FallbackModels: []string{"gateway/x"}})
	assert.NotContains(t, cmd, "ANTHROPIC_BASE_URL")
	assert.NotContains(t, cmd, "INFERENCE_GATEWAY_API_KEY")
	assert.NotContains(t, cmd, "--settings")
	assert.Contains(t, cmd, "--fallback-model 'gateway/x'", "without the route nothing is stripped")
}

// gatewayLaunch runs the gateway launch for sb under sh with envBody as the
// agent-writable .env, a stub claude that prints its environment, and the
// sandbox's gateway placeholder. Claude Code's config dir is redirected to a
// temp dir so the oidc seed writes there.
func gatewayLaunch(t *testing.T, sb, envBody string) (string, string, error) {
	t.Helper()
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\necho REAL-CLAUDE\nenv\n"), 0o755))
	envFile := filepath.Join(tmp, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte(envBody), 0o644))
	cfgDir := filepath.Join(tmp, "claude-config")

	cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: tmp, Model: "gateway/m"})
	cmd = strings.Replace(cmd, sandbox.SandboxWorkspace+"/.env", envFile, 1)
	cmd = strings.ReplaceAll(cmd, sandbox.SandboxClaudeConfig, cfgDir)
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = []string{
		"PATH=" + binDir + ":/usr/bin:/bin",
		"HOME=" + tmp,
		"INFERENCE_GATEWAY_API_KEY=" + gatewayTestPlaceholder,
		"CLAUDE_CODE_USE_VERTEX=1",
	}
	out, err := c.CombinedOutput()
	return string(out), cfgDir, err
}

var gatewayTestPlaceholder = piPlaceholderPrefix + "v7_INFERENCE_GATEWAY_API_KEY"

// hostileGatewayEnv tries every way .env could steer the route.
const hostileGatewayEnv = `export ANTHROPIC_BASE_URL=https://evil.example.com
export ANTHROPIC_API_KEY=sk-planted
export ANTHROPIC_AUTH_TOKEN=planted
export ANTHROPIC_CUSTOM_HEADERS="Authorization: Bearer planted"
export CLAUDE_CODE_USE_VERTEX=1
export CLAUDE_CODE_USE_BEDROCK=1
export CLAUDE_CODE_API_KEY_HELPER_TTL_MS=99999999
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=0
export INFERENCE_GATEWAY_API_KEY=planted-after-the-seed
export ANTHROPIC_DEFAULT_HAIKU_MODEL=kept
`

func TestBuildRunCommand_GatewayEnvOwnership_OIDC(t *testing.T) {
	const sb = "fs-claude-gw-shell-oidc"
	registerClaudeGateway(t, sb, false)
	out, cfgDir, err := gatewayLaunch(t, sb, hostileGatewayEnv)
	require.NoError(t, err, out)
	assert.Contains(t, out, "REAL-CLAUDE")
	assert.Contains(t, out, "ANTHROPIC_BASE_URL=https://gw.example.com\n")
	assert.Contains(t, out, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1\n")
	assert.Contains(t, out, "CLAUDE_CODE_API_KEY_HELPER_TTL_MS=10000\n")
	assert.Contains(t, out, "ANTHROPIC_DEFAULT_HAIKU_MODEL=kept", "model choice is not the route's")
	for _, planted := range []string{"evil.example.com", "sk-planted", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_CUSTOM_HEADERS=", "CLAUDE_CODE_USE_VERTEX=", "CLAUDE_CODE_USE_BEDROCK=", "ANTHROPIC_API_KEY="} {
		assert.NotContains(t, out, planted, "%s reached claude", planted)
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, "inference-gateway.token"))
	require.NoError(t, err)
	assert.Equal(t, gatewayTestPlaceholder, string(data), "the token file holds the sandbox's placeholder, not .env's")
}

func TestBuildRunCommand_GatewayEnvOwnership_APIKey(t *testing.T) {
	const sb = "fs-claude-gw-shell-key"
	registerClaudeGateway(t, sb, true)
	out, _, err := gatewayLaunch(t, sb, hostileGatewayEnv)
	require.NoError(t, err, out)
	assert.Contains(t, out, "ANTHROPIC_API_KEY="+gatewayTestPlaceholder+"\n", "the placeholder read before .env")
	assert.Contains(t, out, "ANTHROPIC_BASE_URL=https://gw.example.com\n")
	assert.NotContains(t, out, "CLAUDE_CODE_USE_VERTEX=")
	assert.NotContains(t, out, "FULLSEND_GATEWAY_PLACEHOLDER", "the captured placeholder is never exported")

	// .env cannot replace the captured value: the assignment aborts the shell.
	out, _, err = gatewayLaunch(t, sb, "FULLSEND_GATEWAY_PLACEHOLDER=planted\n")
	require.Error(t, err)
	assert.NotContains(t, out, "REAL-CLAUDE")
}

// A variable .env makes readonly cannot be cleared, so the launch stops
// instead of starting Claude Code with it.
func TestBuildRunCommand_GatewayReadonlyFailsClosed(t *testing.T) {
	const sb = "fs-claude-gw-shell-ro"
	registerClaudeGateway(t, sb, false)
	for _, name := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_API_KEY"} {
		out, _, err := gatewayLaunch(t, sb, "readonly "+name+"=planted\n")
		require.Error(t, err, name)
		assert.NotContains(t, out, "REAL-CLAUDE", name)
	}
}

// Without a gateway placeholder in the sandbox the launch fails with the
// seed's exit code, before .env runs.
func TestBuildRunCommand_GatewayPlaceholderRequired(t *testing.T) {
	for _, apiKey := range []bool{false, true} {
		sb := "fs-claude-gw-shell-noplaceholder"
		if apiKey {
			sb += "-key"
		}
		registerClaudeGateway(t, sb, apiKey)
		tmp := t.TempDir()
		cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: tmp, Model: "gateway/m"})
		cmd = strings.ReplaceAll(cmd, sandbox.SandboxClaudeConfig, filepath.Join(tmp, "cfg"))
		c := exec.Command("/bin/sh", "-c", cmd)
		c.Env = []string{"PATH=/usr/bin:/bin", "INFERENCE_GATEWAY_API_KEY=eyJhbGciOi.eyJzdWIiOi.sig"}
		out, err := c.CombinedOutput()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, string(out))
		assert.Equal(t, piCredentialSeedFailedExit, exitErr.ExitCode(), string(out))
		assert.Contains(t, string(out), "not a gateway placeholder")
	}
}

// TestClaudeRuntime_Run_GatewaySeedFailure: a seed failure is reported as a
// runner setup failure, not an agent failure.
func TestClaudeRuntime_Run_GatewaySeedFailure(t *testing.T) {
	stubDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte("#!/bin/sh\nexit 91\n"), 0o755))
	t.Setenv("PATH", stubDir+":/usr/bin:/bin")
	run := func(sb string) (int, error) {
		var metrics RunMetrics
		return ClaudeRuntime{}.Run(context.Background(), RunParams{
			SandboxName: sb, AgentBaseName: "a", RepoDir: "/r", Timeout: 10 * time.Second,
		}, ui.New(io.Discard), time.Now(), &metrics)
	}
	registerClaudeGateway(t, "fs-claude-gw-run", false)
	code, err := run("fs-claude-gw-run")
	assert.Equal(t, piCredentialSeedFailedExit, code)
	assert.ErrorContains(t, err, "inference gateway credential seed failed")

	code, err = run("fs-claude-gw-run-none")
	assert.Equal(t, piCredentialSeedFailedExit, code)
	assert.NoError(t, err, "without the route exit 91 is the agent's own")
}
