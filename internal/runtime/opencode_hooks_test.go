package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeHookAdapter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	adapter := filepath.Join(t.TempDir(), "adapter.mjs")
	require.NoError(t, os.WriteFile(adapter, openCodeHookAdapter, 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", "opencode_plugin/fullsend-hooks.test.mjs")
	cmd.Env = append(os.Environ(), "FULLSEND_TEST_ADAPTER="+adapter, "FULLSEND_TEST_PYTHON="+python)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestOpenCodeApplyBashPatternsPreservesTrustedPatterns(t *testing.T) {
	permission := map[string]any{"bash": map[string]any{"gh *": "allow", "*": "deny"}}
	rules := []openCodePermissionRule{{Key: "bash", Value: json.RawMessage(`{"gh *":"allow","*":"deny"}`)}}

	openCodeApplyBashPatterns(permission, []string{"curl"}, rules)

	assert.Equal(t, map[string]any{"gh *": "allow", "*": "deny"}, permission["bash"])
}

func TestOpenCodeBootstrapHookPlan(t *testing.T) {
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellOpenCode(t, filepath.Join(work, "log"), store)
	in := hooksBootstrapInput{bootstrapInput: bootstrapInput{sandboxName: "sb", agentName: "triage", agentPath: writeAgentFile(t, openCodeTestAgentDef)}, hooks: security.SandboxHookConfig{}}
	require.NoError(t, OpenCodeRuntime{}.Bootstrap(in))
	env, ok := lookupOpenCodeTrustedEnv("sb")
	require.True(t, ok)
	require.True(t, openCodeHasHookPlan(env.Hooks))
	assert.Equal(t, security.HookPlan(in.hooks), env.Hooks.Groups)
	assert.Equal(t, OpenCodeRuntime{}.ConfigDir()+"/hooks", env.Hooks.Dir)
	assert.Equal(t, "/sandbox", env.Hooks.ShellHome)
	log, err := os.ReadFile(filepath.Join(work, "log"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "refusing to run unhooked")
	assert.Contains(t, string(log), openCodeHookGuardCommand(env.Hooks))
	assert.Equal(t, openCodeHookAdapter, storedUpload(t, store, OpenCodeRuntime{}.openCodeHooksExtensionPath()))
	for name, content := range security.HookFiles(in.hooks) {
		assert.Equal(t, codexAssetSHA256(content), env.Hooks.Digests[name])
		assert.Equal(t, content, storedUpload(t, store, env.Hooks.Dir+"/"+name))
	}
	assert.Contains(t, openCodeHookEnvExport(env.Hooks), "FULLSEND_CANARY_TOKEN")
}

func TestOpenCodeBootstrapHookFailure(t *testing.T) {
	for _, failure := range []string{"rm -rf -- '/sandbox/opencode-config/hooks'", "sys.version_info", "fullsend-env-sep", `printf '%s' "$HOME"`, "rm -rf -- '/sandbox/opencode-config/plugins'"} {
		t.Run(failure, func(t *testing.T) {
			work := t.TempDir()
			fakeOpenshellOpenCode(t, filepath.Join(work, "log"), filepath.Join(work, "store"))
			t.Setenv("FULLSEND_OPENCODE_TEST_FAIL", failure)
			in := hooksBootstrapInput{bootstrapInput: bootstrapInput{sandboxName: "sb", agentName: "triage", agentPath: writeAgentFile(t, openCodeTestAgentDef)}}
			require.Error(t, OpenCodeRuntime{}.Bootstrap(in))
			_, ok := lookupOpenCodeTrustedEnv("sb")
			assert.False(t, ok, "failed bootstrap must not leave a runnable policy")
		})
	}
}

func TestOpenCodeSDKCacheRestore(t *testing.T) {
	sdk := filepath.Join(t.TempDir(), "sdk files")
	require.NoError(t, os.MkdirAll(filepath.Join(sdk, "node_modules"), 0o700))
	for _, name := range []string{"package.json", "package-lock.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(sdk, name), []byte(`{"test":"metadata"}`), 0o600))
	}
	home := t.TempDir()
	command := strings.ReplaceAll(OpenCodeRuntime{}.openCodeSDKCacheRestore(), OpenCodeRuntime{}.ConfigDir(), sdk)
	run := func() ([]byte, error) {
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "HOME="+home)
		return cmd.CombinedOutput()
	}
	out, err := run()
	require.NoError(t, err, "%s", out)
	for _, name := range []string{"package.json", "package-lock.json"} {
		data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", name))
		require.NoError(t, err)
		assert.JSONEq(t, `{"test":"metadata"}`, string(data))
	}
	target, err := os.Readlink(filepath.Join(home, ".config", "opencode", "node_modules"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(sdk, "node_modules"), target)
	require.NoError(t, os.Remove(filepath.Join(sdk, "package.json")))
	out, err = run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "%s", out)
	assert.Equal(t, 127, exit.ExitCode())
}

func TestOpenCodeRejectsAdditionalNativePlugins(t *testing.T) {
	for _, plugins := range []string{`[]`, `null`, `["file:///sandbox/unverified.ts"]`, `{}`, `"unverified-plugin"`} {
		err := validateOpenCodeTrustedEnv(openCodeTrustedEnv{ConfigContent: `{"permission":{"*":"allow"},"plugin":` + plugins + `}`, CredentialsPath: "/tmp/adc.json"})
		if plugins == `[]` || plugins == `null` {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "additional native plugins")
		}
	}
}

func TestOpenCodeMissingHookPolicy(t *testing.T) {
	for _, hooks := range []*openCodeHookConfig{nil, {}} {
		name := t.Name()
		recordOpenCodeTrustedEnv(name, openCodeTrustedEnv{Hooks: hooks})
		t.Cleanup(func() { forgetOpenCodeTrustedEnv(name) })
		_, err := OpenCodeRuntime{}.Run(t.Context(), RunParams{SandboxName: name, AgentBaseName: "code", HooksSettingsPath: "enabled"}, nil, time.Now(), &RunMetrics{})
		require.ErrorContains(t, err, "hook policy was not installed")
	}
	out, err := exec.Command("sh", "-c", openCodeHookGuard(nil)).CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "%s", out)
	assert.Equal(t, 97, exit.ExitCode())
}

func TestOpenCodeHookGuardCommandPinsUtilities(t *testing.T) {
	command := openCodeHookGuardCommand(nil)
	assert.True(t, strings.HasPrefix(command, openCodeUtilityPin()+" && "))
	assert.Contains(t, command, "hook policy not recorded")
}

func TestOpenCodeAdapterVersionMatchesImage(t *testing.T) {
	data, err := os.ReadFile("../../images/sandbox/Containerfile")
	require.NoError(t, err)
	assert.Contains(t, string(data), "ARG OPENCODE_VERSION="+openCodeHookRuntimeVersion+"\n")
	assert.NotContains(t, OpenCodeRuntime{}.openCodeOverridePaths(false), shellQuote(OpenCodeRuntime{}.ConfigDir()+"/plugins"), "reset must retain the vetted adapter")
}

func TestOpenCodePreflightRejectsV2(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte("#!/bin/sh\nprintf 'opencode v2.0.32\\n'\n"), 0o755))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	require.ErrorContains(t, openCodePreflightVersion("sb"), "requires version 1.19.1")
}

func TestOpenCodeHookIntegrity(t *testing.T) {
	requirePinnedSha256sum(t)
	for _, change := range []string{"clean", "adapter", "script", "added", "symlink"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			plugins := filepath.Join(dir, "plugins")
			hooks := filepath.Join(dir, "hooks")
			require.NoError(t, os.MkdirAll(plugins, 0o700))
			require.NoError(t, os.MkdirAll(hooks, 0o700))
			adapter := filepath.Join(plugins, openCodeHooksExtensionFile)
			require.NoError(t, os.WriteFile(adapter, openCodeHookAdapter, 0o600))
			script := filepath.Join(hooks, "test.py")
			require.NoError(t, os.WriteFile(script, []byte("pass"), 0o600))
			cfg := openCodeHookPolicy(hooks, dir, &piAgentDef{}, nil)
			cfg.Digests["test.py"] = codexAssetSHA256([]byte("pass"))
			switch change {
			case "adapter":
				require.NoError(t, os.WriteFile(adapter, []byte("// modified"), 0o600))
			case "script":
				require.NoError(t, os.WriteFile(script, []byte("# modified"), 0o600))
			case "added":
				require.NoError(t, os.WriteFile(filepath.Join(plugins, "evil.ts"), nil, 0o600))
			case "symlink":
				require.NoError(t, os.Remove(script))
				require.NoError(t, os.Symlink(adapter, script))
			}
			guard := strings.ReplaceAll(openCodeHookGuard(cfg), OpenCodeRuntime{}.ConfigDir(), dir)
			out, err := exec.Command("sh", "-c", openCodeUtilityPin()+" && "+guard).CombinedOutput()
			if change == "clean" {
				require.NoError(t, err, "%s", out)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, "%s", out)
				assert.Equal(t, 97, exit.ExitCode())
			}
		})
	}
}
