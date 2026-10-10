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

// TestClaudeGatewaySettings: the --settings document pins the route
// against the repository's own .claude/settings.json, whose env block
// Claude Code applies after launch: command-line settings rank above it, and
// "" unsets a variable there.
func TestClaudeGatewaySettings(t *testing.T) {
	assert.Nil(t, ClaudeRuntime{}.claudeGatewaySettings("fs-claude-gw-unset"))

	registerClaudeGateway(t, "fs-claude-gw-oidc-settings", false)
	oidc := ClaudeRuntime{}.claudeGatewaySettings("fs-claude-gw-oidc-settings")
	assert.Equal(t, "command -p cat '/sandbox/claude-config/inference-gateway.token'", oidc["apiKeyHelper"])
	oidcEnv, ok := oidc["env"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://gw.example.com", oidcEnv["ANTHROPIC_BASE_URL"])
	assert.Equal(t, "1", oidcEnv["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"])
	assert.Equal(t, "10000", oidcEnv["CLAUDE_CODE_API_KEY_HELPER_TTL_MS"], "a project TTL would otherwise apply")
	for _, name := range claudeGatewayEnvUnset {
		require.Contains(t, oidcEnv, name, "every cleared variable is pinned")
	}
	for _, name := range []string{"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS"} {
		assert.Equal(t, "", oidcEnv[name], name)
	}

	registerClaudeGateway(t, "fs-claude-gw-key-settings", true)
	key := ClaudeRuntime{}.claudeGatewaySettings("fs-claude-gw-key-settings")
	assert.Equal(t, "", key["apiKeyHelper"], `"" overrides a project helper; null would not`)
	keyEnv, ok := key["env"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://gw.example.com", keyEnv["ANTHROPIC_BASE_URL"])
	assert.NotContains(t, keyEnv, "ANTHROPIC_AUTH_TOKEN", "before Bootstrap reads the placeholder there is nothing to pin, and an empty pin would erase it")
	assert.Equal(t, "", keyEnv["ANTHROPIC_API_KEY"])
	assert.Equal(t, "", keyEnv["CLAUDE_CODE_USE_VERTEX"])
	assert.Equal(t, "", keyEnv["CLAUDE_CODE_API_KEY_HELPER_TTL_MS"])
}

// inlineGatewaySettings returns the inline --settings document of a launch
// command.
func inlineGatewaySettings(t *testing.T, cmd string) map[string]any {
	t.Helper()
	_, rest, ok := strings.Cut(cmd, "--settings '")
	require.True(t, ok, cmd)
	raw, _, ok := strings.Cut(rest, "' --")
	require.True(t, ok, cmd)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(raw, `'\''`, "'")), &doc))
	return doc
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

	// Each mode's real settings survive the merge next to the hooks.
	for _, apiKey := range []bool{false, true} {
		sb := "fs-claude-gw-hooks-oidc"
		if apiKey {
			sb = "fs-claude-gw-hooks-key"
		}
		registerClaudeGateway(t, sb, apiKey)
		extra := ClaudeRuntime{}.claudeGatewaySettings(sb)
		require.NoError(t, installClaudeHooks(sb, securityAllOff(), extra))
		data, err := os.ReadFile(captured)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Contains(t, got, "hooks", sb)
		assert.Equal(t, extra["apiKeyHelper"], got["apiKeyHelper"], sb)
		env, ok := got["env"].(map[string]any)
		require.True(t, ok, sb)
		assert.Equal(t, "https://gw.example.com", env["ANTHROPIC_BASE_URL"], sb)
		assert.Equal(t, "", env["CLAUDE_CODE_USE_VERTEX"], sb)
		assert.Equal(t, string(data), ClaudeRuntime{}.claudeGatewaySettingsArg(sb), "%s: the launch passes the same document inline", sb)
	}
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
	assert.NotContains(t, cmd, "export ANTHROPIC_AUTH_TOKEN", "oidc presents the credential through the helper")
	assert.NotContains(t, cmd, "export ANTHROPIC_API_KEY")
	for _, name := range []string{"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		assert.Contains(t, claudeGatewayEnvUnset, name)
	}

	assert.Contains(t, cmd, "--model 'claude-haiku-5-5'", "the gateway/ prefix only selects the route")
	assert.Contains(t, cmd, "--fallback-model 'claude-sonnet-5-5,sonnet'")
	inline := inlineGatewaySettings(t, cmd)
	assert.Equal(t, "command -p cat '/sandbox/claude-config/inference-gateway.token'", inline["apiKeyHelper"], "no hooks file, so the settings are inline")
	assert.Contains(t, inline, "env")

	// With hooks, Bootstrap records the merged document; the launch passes
	// it inline and never names hooks.json.
	merged, err := mergeClaudeSettings([]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"python3 '/sandbox/hooks/x.py'"}]}]}}`), ClaudeRuntime{}.claudeGatewaySettings(sb))
	require.NoError(t, err)
	setClaudeGatewaySettingsJSON(sb, merged)
	withHooks := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/m", HooksSettingsPath: "/sandbox/claude-config/hooks.json"})
	assert.NotContains(t, withHooks, "hooks.json", "a gateway run never reads the settings file")
	assert.Equal(t, 1, strings.Count(withHooks, "--settings"), "Claude Code reads one --settings")
	doc := inlineGatewaySettings(t, withHooks)
	assert.Contains(t, doc, "hooks", "the inline document carries the hooks")
	assert.Equal(t, "command -p cat '/sandbox/claude-config/inference-gateway.token'", doc["apiKeyHelper"])
	assert.Equal(t, "https://gw.example.com", doc["env"].(map[string]any)["ANTHROPIC_BASE_URL"], "and the route pins")
}

func TestBuildRunCommand_GatewayAPIKey(t *testing.T) {
	const sb = "fs-claude-gw-cmd-key"
	registerClaudeGateway(t, sb, true)
	cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/claude-haiku-5-5"})

	captureAt := strings.Index(cmd, `FULLSEND_GATEWAY_PLACEHOLDER="$INFERENCE_GATEWAY_API_KEY" && readonly FULLSEND_GATEWAY_PLACEHOLDER`)
	envAt := strings.Index(cmd, ". /sandbox/workspace/.env")
	exportAt := strings.Index(cmd, `export ANTHROPIC_AUTH_TOKEN="$FULLSEND_GATEWAY_PLACEHOLDER"`)
	require.NotEqual(t, -1, captureAt, cmd)
	require.NotEqual(t, -1, exportAt, cmd)
	assert.Less(t, captureAt, envAt, "the placeholder is read before .env")
	assert.Less(t, envAt, exportAt)
	assert.Contains(t, cmd, gatewayPlaceholderCheck())
	assert.NotContains(t, cmd, "export ANTHROPIC_API_KEY", "x-api-key is never used; both modes send Bearer")
	assert.NotContains(t, cmd, "inference-gateway.token", "the api-key mode writes no token file")
	inline := inlineGatewaySettings(t, cmd)
	assert.Equal(t, "", inline["apiKeyHelper"], "a project helper is overridden")
	assert.Contains(t, inline, "env")
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
	assert.Contains(t, out, "ANTHROPIC_AUTH_TOKEN="+gatewayTestPlaceholder+"\n", "the placeholder read before .env")
	assert.NotContains(t, out, "ANTHROPIC_API_KEY=", ".env's x-api-key credential is cleared, so it cannot change the header")
	assert.NotContains(t, out, "ANTHROPIC_AUTH_TOKEN=planted")
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
	for _, name := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
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
		// The launch pins the claude binary before the seed runs, so the
		// test needs one on PATH (CI has none installed).
		binDir := filepath.Join(tmp, "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\necho REAL-CLAUDE\n"), 0o755))
		cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: tmp, Model: "gateway/m"})
		cmd = strings.ReplaceAll(cmd, sandbox.SandboxClaudeConfig, filepath.Join(tmp, "cfg"))
		c := exec.Command("/bin/sh", "-c", cmd)
		c.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin", "INFERENCE_GATEWAY_API_KEY=eyJhbGciOi.eyJzdWIiOi.sig"}
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

// The api-key mode reads its placeholder at Bootstrap, pins it as
// ANTHROPIC_AUTH_TOKEN in the --settings env, and the launch refuses a
// sandbox that now hands out another one.
func TestClaudeGatewayAPIKeyPlaceholder(t *testing.T) {
	const sb = "fs-claude-gw-key-pin"
	stubDir := t.TempDir()
	out := filepath.Join(stubDir, "out")
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte("#!/bin/sh\ncat '"+out+"'\n"), 0o755))
	t.Setenv("PATH", stubDir+":/usr/bin:/bin")

	require.NoError(t, claudeGatewayAPIKeyPlaceholder("fs-claude-gw-key-pin-unregistered"), "no registration, nothing to read")
	registerClaudeGateway(t, sb+"-oidc", false)
	require.NoError(t, claudeGatewayAPIKeyPlaceholder(sb+"-oidc"), "the oidc mode pins no placeholder")

	registerClaudeGateway(t, sb, true)
	for _, bad := range []string{"", "sk-real-key", gatewayTestPlaceholder + "x", gatewayTestPlaceholder + " evil"} {
		require.NoError(t, os.WriteFile(out, []byte(bad+"\n"), 0o644))
		assert.Error(t, claudeGatewayAPIKeyPlaceholder(sb), "%q is not a placeholder", bad)
	}
	require.NoError(t, os.WriteFile(out, []byte(gatewayTestPlaceholder+"\n"), 0o644))
	require.NoError(t, claudeGatewayAPIKeyPlaceholder(sb))
	env := ClaudeRuntime{}.claudeGatewaySettings(sb)["env"].(map[string]any)
	assert.Equal(t, gatewayTestPlaceholder, env["ANTHROPIC_AUTH_TOKEN"], "a project env value cannot swap the runner's credential")

	inline := inlineGatewaySettings(t, buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/m"}))
	assert.Equal(t, gatewayTestPlaceholder, inline["env"].(map[string]any)["ANTHROPIC_AUTH_TOKEN"], "the inline settings carry the pin")

	// Every launch re-checks (a validation loop re-runs it); an unchanged
	// placeholder passes each time.
	for i := 0; i < 2; i++ {
		got, _, err := gatewayLaunch(t, sb, "")
		require.NoError(t, err, got)
		assert.Contains(t, got, "ANTHROPIC_AUTH_TOKEN="+gatewayTestPlaceholder+"\n")
	}

	cp := *claudeGatewayRunFor(sb)
	cp.placeholder = piPlaceholderPrefix + "v6_" + piGatewayCredentialEnv
	claudeGatewayRuns.Store(sb, &cp)
	got, _, err := gatewayLaunch(t, sb, "")
	require.Error(t, err)
	assert.Contains(t, got, "placeholder changed since Bootstrap")
	assert.NotContains(t, got, "REAL-CLAUDE")
}

// The inline --settings document reaches Claude Code as one argument, byte
// for byte, even when it spans lines and holds single quotes (hook
// commands quote their paths).
func TestBuildRunCommand_GatewayInlineSettingsArgv(t *testing.T) {
	const sb = "fs-claude-gw-argv"
	registerClaudeGateway(t, sb, false)
	merged, err := mergeClaudeSettings([]byte("{\"hooks\":{\"PreToolUse\":[{\"matcher\":\"Bash\",\"hooks\":[{\"type\":\"command\",\"command\":\"python3 '/sandbox/hooks/it'\\\\''s.py'\"}]}]}}"), ClaudeRuntime{}.claudeGatewaySettings(sb))
	require.NoError(t, err)
	require.Contains(t, string(merged), "\n", "the recorded document is multi-line")
	setClaudeGatewaySettingsJSON(sb, merged)

	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	argFile := filepath.Join(tmp, "settings-arg")
	stub := "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = --settings ]; then printf '%s' \"$2\" > '" + argFile + "'; fi; shift; done\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(stub), 0o755))
	envFile := filepath.Join(tmp, ".env")
	require.NoError(t, os.WriteFile(envFile, nil, 0o644))

	cmd := buildRunCommand(RunParams{SandboxName: sb, AgentBaseName: "agent", RepoDir: tmp, Model: "gateway/m", HooksSettingsPath: "/sandbox/claude-config/hooks.json"})
	cmd = strings.Replace(cmd, sandbox.SandboxWorkspace+"/.env", envFile, 1)
	cmd = strings.ReplaceAll(cmd, sandbox.SandboxClaudeConfig, filepath.Join(tmp, "claude-config"))
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + tmp, "INFERENCE_GATEWAY_API_KEY=" + gatewayTestPlaceholder}
	out, err := c.CombinedOutput()
	require.NoError(t, err, string(out))
	got, err := os.ReadFile(argFile)
	require.NoError(t, err)
	// The test redirects the config dir in the command, so expect the same.
	want := strings.ReplaceAll(string(merged), sandbox.SandboxClaudeConfig, filepath.Join(tmp, "claude-config"))
	assert.Equal(t, want, string(got), "the argument is the recorded document, unchanged")
}
