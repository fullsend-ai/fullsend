package e2etest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryRunCLIWithT_LogsOnFailure(t *testing.T) {
	failScript := filepath.Join(t.TempDir(), "fail.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	_, err := TryRunCLIWithT(t, failScript, "token", "version")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestTryRunCLIAndRunCLI(t *testing.T) {
	binary := BuildCLIBinary(t)

	out, err := TryRunCLI(binary, "token", "help")
	require.NoError(t, err)
	assert.Contains(t, out, "fullsend")

	helpOut := RunCLI(t, binary, "token", "help")
	assert.Contains(t, helpOut, "fullsend")
}

func TestRunCLIFromDir(t *testing.T) {
	binary := BuildCLIBinary(t)
	out := RunCLIFromDir(t, binary, "token", ModuleRoot(t), "help")
	assert.Contains(t, out, "fullsend")
}

func TestModuleDir_Invalid(t *testing.T) {
	_, err := moduleDir("github.com/fullsend-ai/fullsend/not-a-real-module-path")
	require.Error(t, err)
}
