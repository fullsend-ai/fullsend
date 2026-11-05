package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// hookEnvReadStubArm is the case arm a stub openshell uses to answer
// Bootstrap's read of the harness hook environment (readHarnessHookEnvCmd):
// FULLSEND_TEST_ENV_READ when the test set it, both values unset otherwise.
const hookEnvReadStubArm = `*fullsend-env-sep*) printf '%s' "${FULLSEND_TEST_ENV_READ-|fullsend-env-sep|}" ;;`

// resetPiHookEnvs drops every recorded pi hook environment, so a Bootstrap in
// one test does not change the launch another test builds for the same
// sandbox name.
func resetPiHookEnvs() {
	piHookEnvs.Range(func(k, _ any) bool {
		piHookEnvs.Delete(k)
		return true
	})
}

func TestHookEnvExports(t *testing.T) {
	t.Parallel()

	t.Run("exports recorded values and unsets the rest", func(t *testing.T) {
		got := hookEnvExports([]hookEnvPair{
			{"FULLSEND_TOOL_ALLOWLIST", "Bash,Read"},
			{"TIRITH_REQUIRED", "1"},
			{"FULLSEND_CANARY_TOKEN", ""},
		})
		assert.Equal(t, "unset TIRITH_FAIL_ON FULLSEND_EGRESS_ALLOWLIST FULLSEND_CANARY_TOKEN"+
			" && export TIRITH_REQUIRED='1' && export FULLSEND_TOOL_ALLOWLIST='Bash,Read'", got)
	})

	t.Run("nothing recorded unsets every managed name", func(t *testing.T) {
		assert.Equal(t, "unset "+strings.Join(hookEnvManagedKeys, " "), hookEnvExports(nil))
	})

	t.Run("every managed name recorded needs no unset", func(t *testing.T) {
		var env []hookEnvPair
		for _, key := range hookEnvManagedKeys {
			env = append(env, hookEnvPair{key, "v"})
		}
		got := hookEnvExports(env)
		assert.NotContains(t, got, "unset")
		assert.Equal(t, len(hookEnvManagedKeys)-1, strings.Count(got, " && "))
	})

	t.Run("values are shell-quoted", func(t *testing.T) {
		got := hookEnvExports([]hookEnvPair{{"FULLSEND_EGRESS_ALLOWLIST", "a'b $(touch x)"}})
		assert.Contains(t, got, "export FULLSEND_EGRESS_ALLOWLIST="+shellQuote("a'b $(touch x)"))
	})
}

// TestBootstrapHookEnv: the SandboxHookConfig values first, then the harness
// pair read back from .env.
func TestBootstrapHookEnv(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
	t.Setenv("FULLSEND_TEST_ENV_READ", "canary-from-harness"+hookEnvReadSeparator+"Bash,Read")

	on := true
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{
		Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
			Tirith: &harness.TirithConfig{Enabled: &on, FailOn: "medium"},
		}},
	})
	got, err := bootstrapHookEnv("sb", hooks)
	require.NoError(t, err)
	assert.Equal(t, append(configHookEnv(hooks),
		hookEnvPair{"FULLSEND_CANARY_TOKEN", "canary-from-harness"},
		hookEnvPair{"FULLSEND_TOOL_ALLOWLIST", "Bash,Read"},
	), got)

	t.Setenv("FULLSEND_TEST_ENV_READ", "only-one-value")
	_, err = bootstrapHookEnv("sb", hooks)
	require.Error(t, err)
}

// launchHookEnvStatements returns the statements of the launch command cmd
// that come after its .env load and before the CLI starts and that export or
// unset a managed hook name — the launch's own .env handling for the hook
// settings, taken from the real command rather than rebuilt by the test.
func launchHookEnvStatements(t *testing.T, cmd, load, launch string) string {
	t.Helper()
	loadAt := strings.Index(cmd, load)
	require.GreaterOrEqual(t, loadAt, 0, "the launch has no .env load: %s", cmd)
	rest := cmd[loadAt+len(load):]
	launchAt := strings.Index(rest, launch)
	require.GreaterOrEqual(t, launchAt, 0, "the launch does not start the CLI after .env: %s", cmd)

	var keep []string
	for _, stmt := range strings.Split(rest[:launchAt], " && ") {
		stmt = strings.TrimSpace(stmt)
		if strings.HasPrefix(stmt, "unset -") {
			continue
		}
		for _, key := range hookEnvManagedKeys {
			if strings.HasPrefix(stmt, "export "+key+"=") ||
				(strings.HasPrefix(stmt, "unset ") && hasField(stmt, key)) {
				keep = append(keep, stmt)
				break
			}
		}
	}
	return strings.Join(keep, " && ")
}

func hasField(s, field string) bool {
	for _, f := range strings.Fields(s) {
		if f == field {
			return true
		}
	}
	return false
}

// hookEnvSeen runs, under /bin/sh, the .env load followed by statements, and
// returns the managed hook names a child process (a hook) sees in its
// environment. The .env written first changes or adds every managed name.
func hookEnvSeen(t *testing.T, statements string) map[string]string {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), ".env")
	var body strings.Builder
	for _, key := range hookEnvManagedKeys {
		body.WriteString("export " + key + "=" + shellQuote("agent-"+key) + "\n")
	}
	require.NoError(t, os.WriteFile(envFile, []byte(body.String()), 0o600))

	script := EnvDataLoad(envFile)
	if statements != "" {
		script += " && " + statements
	}
	script += " && command -p env"
	c := exec.Command("/bin/sh", "-c", script)
	c.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := c.CombinedOutput()
	require.NoError(t, err, string(out))

	seen := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		for _, managed := range hookEnvManagedKeys {
			if key == managed {
				seen[key] = value
			}
		}
	}
	return seen
}

// TestHookEnv_LaunchesTakeTheRunnerValues runs each runtime's .env handling
// under /bin/sh with a .env that changes or adds every hook setting: the
// hooks must see the values Bootstrap recorded, and no value at all for a
// name the runner did not set.
func TestHookEnv_LaunchesTakeTheRunnerValues(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	envFile := sandbox.SandboxWorkspace + "/.env"

	runtimes := map[string]func(t *testing.T, env []hookEnvPair) string{
		"claude": func(t *testing.T, env []hookEnvPair) string {
			cmd := buildRunCommandWithDigests(RunParams{
				AgentBaseName:     "agent",
				RepoDir:           "/repo",
				HooksSettingsPath: security.SandboxHooksSettings,
			}, claudeHookDigests{HooksJSON: strings.Repeat("a", 64), HookScripts: testCodexHookScripts(), SecurityEnv: env})
			return launchHookEnvStatements(t, cmd,
				envDataLoad(envFile, claudeBinaryVar, claudeNodeVar, claudeGatewayPlaceholderVar),
				cliLaunch(claudeBinaryVar, claudeNodeVar))
		},
		"codex": func(t *testing.T, env []hookEnvPair) string {
			digests := testRunnerHeldDigests
			digests.SecurityEnv = env
			cmd := buildCodexRunCommand(RunParams{
				RepoDir:           sandbox.SandboxWorkspace + "/repo",
				HooksSettingsPath: CodexRuntime{}.codexHooksPath(),
			}, codexModel{ID: "gpt-5.6-luna"}, "", true, digests)
			return launchHookEnvStatements(t, cmd, codexEnvLoad, cliLaunch(codexBinaryVar, codexNodeVar))
		},
		"pi": func(t *testing.T, env []hookEnvPair) string {
			params := piTestParams()
			params.SandboxName = "hook-env-pi"
			params.HooksSettingsPath = "/sandbox/claude-config/hooks.json"
			recordPiHookEnv(params.SandboxName, env)
			t.Cleanup(resetPiHookEnvs)
			cmd := buildPiRunCommand(params, &piManifest{AgentName: "triage", Model: "opus", Tools: []string{"bash"}, Hooks: &piHooksManifest{}}, nil, "")
			return launchHookEnvStatements(t, cmd, piEnvLoad, cliLaunch(piBinaryVar, piNodeVar))
		},
	}

	cases := map[string]struct {
		recorded []hookEnvPair
		want     map[string]string
	}{
		"runner values win, a name the runner left out is unset": {
			recorded: []hookEnvPair{
				{"TIRITH_FAIL_ON", "high"},
				{"TIRITH_REQUIRED", "1"},
				{"FULLSEND_EGRESS_ALLOWLIST", "api.github.com:443"},
				{"FULLSEND_CANARY_TOKEN", "canary-from-harness"},
				{"FULLSEND_TOOL_ALLOWLIST", ""},
			},
			want: map[string]string{
				"TIRITH_FAIL_ON":            "high",
				"TIRITH_REQUIRED":           "1",
				"FULLSEND_EGRESS_ALLOWLIST": "api.github.com:443",
				"FULLSEND_CANARY_TOKEN":     "canary-from-harness",
			},
		},
		"nothing recorded leaves no value": {
			recorded: nil,
			want:     map[string]string{},
		},
	}

	for rtName, build := range runtimes {
		t.Run(rtName, func(t *testing.T) {
			for name, tc := range cases {
				t.Run(name, func(t *testing.T) {
					statements := build(t, tc.recorded)
					require.NotEmpty(t, statements, "the launch does not re-assert the hook settings")
					assert.Equal(t, tc.want, hookEnvSeen(t, statements))
				})
			}
		})
	}

	// Without the re-assertion the hooks would see the .env values, so the
	// cases above are not passing on an empty .env.
	t.Run("the .env alone reaches the hooks", func(t *testing.T) {
		seen := hookEnvSeen(t, "")
		for _, key := range hookEnvManagedKeys {
			assert.Equal(t, "agent-"+key, seen[key])
		}
	})
}

// TestHookEnv_NotEmittedWithoutHooks: with hooks off nothing reads the
// settings, and the launches do not touch them.
func TestHookEnv_NotEmittedWithoutHooks(t *testing.T) {
	claude := buildRunCommandWithDigests(RunParams{AgentBaseName: "agent", RepoDir: "/repo"}, claudeHookDigests{})
	codex := buildCodexRunCommand(RunParams{RepoDir: "/repo"}, codexModel{ID: "gpt-5.6-luna"}, "", false, testRunnerHeldDigests)
	params := piTestParams()
	params.SandboxName = "hook-env-pi-off"
	recordPiHookEnv(params.SandboxName, []hookEnvPair{{"TIRITH_REQUIRED", "1"}})
	t.Cleanup(resetPiHookEnvs)
	pi := buildPiRunCommand(params, &piManifest{AgentName: "triage", Model: "opus"}, nil, "")
	for name, cmd := range map[string]string{"claude": claude, "codex": codex, "pi": pi} {
		for _, key := range hookEnvManagedKeys {
			assert.NotContains(t, cmd, key, "%s: hooks are off", name)
		}
	}
}

// TestBuildPiRunCommand_HookEnvNeedsBootstrapEntry: a launch for a sandbox
// this process did not bootstrap emits no hook settings, as for the manifest
// guard.
func TestBuildPiRunCommand_HookEnvNeedsBootstrapEntry(t *testing.T) {
	params := piTestParams()
	params.SandboxName = "hook-env-pi-unbootstrapped"
	params.HooksSettingsPath = "/sandbox/claude-config/hooks.json"
	cmd := buildPiRunCommand(params, &piManifest{AgentName: "triage", Model: "opus", Hooks: &piHooksManifest{}}, nil, "")
	assert.Empty(t, launchHookEnvStatements(t, cmd, piEnvLoad, cliLaunch(piBinaryVar, piNodeVar)))
}

// TestInstallClaudeHooks_RecordsHookEnv: Bootstrap records the hook settings
// beside the digests, and a failed read fails Bootstrap.
func TestInstallClaudeHooks_RecordsHookEnv(t *testing.T) {
	const name = "claude-hook-env"
	t.Cleanup(func() { forgetClaudeHookDigests(name) })
	stubDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte(claudeHooksStubScript("", "")), 0o755))
	t.Setenv("PATH", stubDir)
	t.Setenv("FULLSEND_TEST_ENV_READ", "canary-from-harness"+hookEnvReadSeparator+"Bash,Read")

	hooks := security.SandboxHookConfig{}
	require.NoError(t, installClaudeHooks(name, hooks, nil))
	d, ok := lookupClaudeHookDigests(name)
	require.True(t, ok)
	assert.Equal(t, append(configHookEnv(hooks),
		hookEnvPair{"FULLSEND_CANARY_TOKEN", "canary-from-harness"},
		hookEnvPair{"FULLSEND_TOOL_ALLOWLIST", "Bash,Read"},
	), d.SecurityEnv)

	forgetClaudeHookDigests(name)
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte(claudeHooksStubScript("", "*fullsend-env-sep*")), 0o755))
	err := installClaudeHooks(name, hooks, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the harness hook environment")
	_, ok = lookupClaudeHookDigests(name)
	assert.False(t, ok)
}

// TestPiRuntimeBootstrap_RecordsHookEnv: pi's Bootstrap records the hook
// settings for Run, and a failed read fails Bootstrap.
func TestPiRuntimeBootstrap_RecordsHookEnv(t *testing.T) {
	work := t.TempDir()
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), filepath.Join(work, "store"), "/dev/null")
	t.Setenv("FULLSEND_TEST_ENV_READ", "canary-from-harness"+hookEnvReadSeparator)

	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{}}})
	in := piHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "pi-hook-env",
			agentPath:   writeAgentFile(t, testAgentDef),
			agentName:   "triage",
		},
		hooks: hooks,
	}
	require.NoError(t, PiRuntime{}.Bootstrap(in))
	env, ok := piHookEnv("pi-hook-env")
	require.True(t, ok)
	assert.Equal(t, append(configHookEnv(hooks),
		hookEnvPair{"FULLSEND_CANARY_TOKEN", "canary-from-harness"},
		hookEnvPair{"FULLSEND_TOOL_ALLOWLIST", ""},
	), env)

	resetPiHookEnvs()
	t.Setenv("FULLSEND_TEST_ENV_READ", "only-one-value")
	require.Error(t, PiRuntime{}.Bootstrap(in))
	_, ok = piHookEnv("pi-hook-env")
	assert.False(t, ok)
}

// TestPiRuntimeRun_RefusesWithoutRecordedHookEnv: with sandbox hooks on,
// Run refuses to start pi when Bootstrap recorded no hook settings in this
// process, as Claude Code and codex refuse without their runner-held
// digests, rather than leaving the settings to .env.
func TestPiRuntimeRun_RefusesWithoutRecordedHookEnv(t *testing.T) {
	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "hook-env-pi-not-bootstrapped", RepoDir: "/r", Timeout: 30 * time.Second,
		HooksSettingsPath: "/sandbox/claude-config/hooks.json",
		OnEvent:           func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, -1, exit)
	require.ErrorContains(t, err, "no runner-held hook settings for sandbox hook-env-pi-not-bootstrapped")
}
