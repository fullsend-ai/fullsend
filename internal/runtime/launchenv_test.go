package runtime

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/envmanifest"
)

// launchCommandVariants builds each runtime's production launch command
// under every configuration that changes what it exports: hooks on and off,
// an inference gateway registered and not, and each provider path. The
// union of their exports is what the runtime can set, which is the set
// LaunchEnvExports must declare.
func launchCommandVariants(t *testing.T, rt Runtime) []string {
	t.Helper()
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	t.Setenv(openCodeProviderEnv, "")
	switch rt.(type) {
	case ClaudeRuntime:
		cmds := []string{buildRunCommand(RunParams{SandboxName: "fs-launchenv-claude", AgentBaseName: "agent", RepoDir: "/r"})}
		for _, gw := range []struct {
			sandbox string
			block   config.InferenceGatewayConfig
		}{
			{"fs-launchenv-claude-oidc", config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"}},
			{"fs-launchenv-claude-bearer", config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey}},
			{"fs-launchenv-claude-xapikey", config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey, MessagesAuthHeader: config.GatewayMessagesAuthHeaderXAPIKey}},
		} {
			require.NoError(t, ClaudeRuntime{}.PrepareGatewayRun(gw.sandbox, GatewayRun{Block: gw.block, BaseURL: "https://gw.example.com"}))
			t.Cleanup(func() { ClaudeRuntime{}.ClearGatewayRun(gw.sandbox) })
			cmds = append(cmds, buildRunCommand(RunParams{SandboxName: gw.sandbox, AgentBaseName: "agent", RepoDir: "/r", Model: "gateway/m", HooksSettingsPath: "/sandbox/claude-config/hooks.json"}))
		}
		return cmds
	case CodexRuntime:
		noHooks := testRunnerHeldDigests
		noHooks.HooksJSON = ""
		noHooks.HookScripts = nil
		noHooks.SecurityEnv = nil
		gateway := testRunnerHeldDigests
		gateway.GatewayBaseURL = testCodexGatewayBaseURL
		failOn := testRunnerHeldDigests
		failOn.SecurityEnv = append(slices.Clone(failOn.SecurityEnv), hookEnvPair{"TIRITH_FAIL_ON", "high"})
		return []string{
			buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, testRunnerHeldDigests),
			buildCodexRunCommand(RunParams{RepoDir: "/r"}, codexModel{ID: "gpt-5.6-luna"}, "", false, noHooks),
			buildCodexRunCommand(hooksOn, codexModel{ID: "vendor/org/model", Gateway: true}, "high", true, gateway),
			buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, failOn),
		}
	case OpenCodeRuntime:
		params := RunParams{SandboxName: "fs-launchenv-opencode", AgentBaseName: "triage", Model: "opus", Effort: "high", RepoDir: "/r"}
		trusted := openCodeTrustedEnv{ConfigContent: `{"permission":{"*":"deny"}}`, CredentialsPath: "/runner/adc.json"}
		withHooks := trusted
		withHooks.Hooks = &openCodeHookConfig{}
		return []string{
			buildOpenCodeRunCommand(params, "triage", trusted),
			buildOpenCodeRunCommand(params, "triage", withHooks),
		}
	case PiRuntime:
		var cmds []string
		for _, model := range []string{"opus", piXaiVertexProvider + "/xai/grok-4.6", piOpenAIProvider + "/gpt-5.6-luna"} {
			params := piTestParams()
			params.Model = model
			cmds = append(cmds, buildPiRunCommand(params, &piManifest{}, nil, "manifest-sum"))
		}
		params := piTestParams()
		params.SandboxName = "fs-launchenv-pi-gw"
		params.Model = "gateway/m1"
		run := piGatewayTestRun()
		run.ConfigFile = "/sandbox/gateway/config.json"
		registerPiGatewayRun(t, params.SandboxName, run)
		return append(cmds, buildPiRunCommand(params, &piManifest{}, nil, "manifest-sum"))
	}
	return nil
}

// TestLaunchEnvExportsMatchLaunchCommands pins every LaunchEnvExports list to
// the launch commands themselves: the names a runtime declares must be
// exactly the names its launch-command variants export beyond EnvExports. A
// launch builder that gains, drops, or renames an export without updating
// the declaration fails here, so the sandbox env contract cannot drift from
// the commands.
func TestLaunchEnvExportsMatchLaunchCommands(t *testing.T) {
	for _, rt := range []Runtime{ClaudeRuntime{}, CodexRuntime{}, OpenCodeRuntime{}, PiRuntime{}} {
		t.Run(rt.Name(), func(t *testing.T) {
			fromEnvExports := envmanifest.ExportedNames(rt.EnvExports()...)
			derived := slices.DeleteFunc(envmanifest.ExportedNames(launchCommandVariants(t, rt)...), func(name string) bool {
				return slices.Contains(fromEnvExports, name)
			})
			declared := slices.Clone(LaunchEnvExportsFor(rt))
			slices.Sort(declared)
			assert.Equal(t, derived, declared, "LaunchEnvExports must list exactly what the launch commands export beyond EnvExports")
		})
	}
}

// TestLaunchEnvExportsFor_Stubs pins that the local test stubs declare no
// launch exports: they are outside the sandbox env contract.
func TestLaunchEnvExportsFor_Stubs(t *testing.T) {
	assert.Nil(t, LaunchEnvExportsFor(DummyRuntime{}))
	assert.Nil(t, LaunchEnvExportsFor(DummyPlaybackRuntime{}))
}
