package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// hooksBootstrapInput is a BootstrapInput that also carries sandbox hook config.
type hooksBootstrapInput struct {
	bootstrapInput
	hooks security.SandboxHookConfig
}

func (h hooksBootstrapInput) SandboxHookConfig() security.SandboxHookConfig { return h.hooks }

var _ SandboxHooksBootstrap = hooksBootstrapInput{}

func TestClaudeRuntime_Bootstrap_InstallsSandboxHooks(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	sentinelPath := filepath.Join(t.TempDir(), "unused.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	agentPath := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("---\nname: x\n---\n"), 0o644))

	failOn := "high"
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		Tirith: &harness.TirithConfig{FailOn: failOn},
	}}}
	in := hooksBootstrapInput{
		bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: agentPath, agentName: "x"},
		hooks:          security.SandboxHookConfigFromHarness(h),
	}
	require.NoError(t, ClaudeRuntime{}.Bootstrap(in))

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	log := string(logBytes)

	// Hook scripts are uploaded to the Claude hooks dir and chmod'ed.
	assert.Contains(t, log, "/sandbox/claude-config/hooks/tirith_check.py")
	assert.Contains(t, log, "chmod +x '/sandbox/claude-config/hooks/tirith_check.py'")
	// Claude-specific wiring (hooks.json, loaded via --settings) is installed.
	assert.Contains(t, log, "/sandbox/claude-config/hooks.json")
	// Hook env is appended to the workspace .env.
	assert.Contains(t, log, "export TIRITH_FAIL_ON=high")
	assert.Contains(t, log, "export TIRITH_REQUIRED=1")
}

func TestClaudeRuntime_Bootstrap_NoHooksWithoutExtension(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	sentinelPath := filepath.Join(t.TempDir(), "unused.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	agentPath := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("---\nname: x\n---\n"), 0o644))

	in := bootstrapInput{sandboxName: "sb", agentPath: agentPath, agentName: "x"}
	require.NoError(t, ClaudeRuntime{}.Bootstrap(in))

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(logBytes), "claude-config/hooks"),
		"no hook scripts must be installed when the input lacks SandboxHooksBootstrap")
}

func TestInstallHookScripts_CustomDir(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	sentinelPath := filepath.Join(t.TempDir(), "unused.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	require.NoError(t, installHookScripts("sb", "/sandbox/other-runtime/hooks", hooks))

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	log := string(logBytes)
	for name := range security.HookFiles(hooks) {
		assert.Contains(t, log, "/sandbox/other-runtime/hooks/"+name)
	}
	assert.NotContains(t, log, ".claude/")
}

func TestAppendHookEnv_TirithDisabled(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	sentinelPath := filepath.Join(t.TempDir(), "unused.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	disabled := false
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		Tirith: &harness.TirithConfig{Enabled: &disabled},
	}}}
	require.NoError(t, appendHookEnv("sb", security.SandboxHookConfigFromHarness(h)))

	// Nothing to export → no sandbox exec at all (the fake openshell log is never created).
	_, err := os.Stat(logPath)
	assert.True(t, os.IsNotExist(err), "expected no openshell invocation, log exists")
}

// fakeOpenshellFailing installs a fake "openshell" that fails every
// invocation whose subcommand matches failOn (e.g. "upload" or "exec").
// exec failures exit 124 because sandbox.Exec only surfaces start failures
// and timeouts (exit 124) as errors, not ordinary non-zero exits.
func fakeOpenshellFailing(t *testing.T, failOn string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"" + failOn + "\" ]; then echo 'boom' >&2; exit 124; fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestInstallHookScripts_UploadFailure(t *testing.T) {
	fakeOpenshellFailing(t, "upload")
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	err := installHookScripts("sb", "/sandbox/x/hooks", hooks)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying hook")
}

func TestInstallHookScripts_ExecFailure(t *testing.T) {
	fakeOpenshellFailing(t, "exec")
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	err := installHookScripts("sb", "/sandbox/x/hooks", hooks)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating hooks dir")
}

func TestAppendHookEnv_ExecFailure(t *testing.T) {
	fakeOpenshellFailing(t, "exec")
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	err := appendHookEnv("sb", hooks)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TIRITH_REQUIRED")
}

func TestClaudeRuntime_Bootstrap_HooksChmodFailure(t *testing.T) {
	// Agent upload happens first, so make only later steps fail: time out
	// (exit 124, the only exec failure sandbox.Exec reports) on the chmod
	// of a hook script.
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$*\" in *'chmod +x'*) echo 'boom' >&2; exit 124 ;; esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agentPath := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("---\nname: x\n---\n"), 0o644))
	in := hooksBootstrapInput{
		bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: agentPath, agentName: "x"},
		hooks:          security.SandboxHookConfigFromHarness(&harness.Harness{}),
	}
	err := ClaudeRuntime{}.Bootstrap(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chmod hook")
}
