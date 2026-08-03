package e2etest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCLIScript(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "cli.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return script
}

func TestTryRunCLIWithT_LogsOnFailure(t *testing.T) {
	script := writeCLIScript(t, "exit 1")

	_, err := TryRunCLIWithT(t, script, "token", "version")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestTryRunCLIAndRunCLI(t *testing.T) {
	script := writeCLIScript(t, "echo fullsend help output")

	out, err := TryRunCLI(script, "token", "help")
	require.NoError(t, err)
	assert.Contains(t, out, "fullsend")

	helpOut := RunCLI(t, script, "token", "help")
	assert.Contains(t, helpOut, "fullsend")
}

func TestRunCLIFromDir(t *testing.T) {
	script := writeCLIScript(t, "echo fullsend help output")
	out := RunCLIFromDir(t, script, "token", t.TempDir(), "help")
	assert.Contains(t, out, "fullsend")
}

func TestRunCLIWithEnv(t *testing.T) {
	script := writeCLIScript(t, "echo fullsend repos output")
	out := RunCLIWithEnv(t, script, map[string]string{"GITHUB_TOKEN": "tok", "GITLAB_TOKEN": "gl-tok"}, "repos", "status")
	assert.Contains(t, out, "fullsend")
}

func TestTryRunCLIWithEnv_Success(t *testing.T) {
	script := writeCLIScript(t, "echo fullsend repos output")
	out, err := TryRunCLIWithEnv(t, script, map[string]string{"GITHUB_TOKEN": "tok"}, "repos", "status")
	require.NoError(t, err)
	assert.Contains(t, out, "fullsend")
}

func TestTryRunCLIWithEnv_Failure(t *testing.T) {
	script := writeCLIScript(t, "echo error >&2; exit 1")
	_, err := TryRunCLIWithEnv(t, script, map[string]string{"GITHUB_TOKEN": "tok"}, "repos", "upgrade-mint")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upgrade-mint")
}

func TestModuleRoot(t *testing.T) {
	dir := ModuleRoot(t)
	assert.DirExists(t, dir)
}
