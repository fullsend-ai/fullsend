package cli

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildClaudeEntrypointArtifacts(t *testing.T) {
	entrypoint := []byte("#!/bin/sh\nexit 0\n")
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	artifacts, err := buildClaudeEntrypointArtifacts(entrypoint, "opus", "high", []string{"/sandbox/plugins/p"}, nil, map[string]string{"TIRITH_FAIL_ON": "high"}, hooks, true)
	require.NoError(t, err)
	assert.Equal(t, security.SandboxHooksSettings, artifacts.HooksSettings)
	assert.Contains(t, string(artifacts.Helper), "'--model' 'opus'")
	assert.Contains(t, string(artifacts.Helper), "'/sandbox/plugins/p'")
	guard := artifacts.Integrity.GuardCommand(true)
	assert.Contains(t, guard, remoteEntrypointPath)
	assert.Contains(t, guard, remoteClaudeHelperPath)
	assert.Contains(t, guard, security.SandboxHooksSettings)
	for name := range security.HookFiles(hooks) {
		assert.Contains(t, guard, security.SandboxHooksDir+"/"+name)
	}
	assert.Contains(t, guard, "find '/sandbox/claude-config/hooks'")

	withoutHooks, err := buildClaudeEntrypointArtifacts(entrypoint, "", "", nil, nil, nil, hooks, false)
	require.NoError(t, err)
	assert.Empty(t, withoutHooks.HooksSettings)
	assert.NotContains(t, withoutHooks.Integrity.GuardCommand(true), security.SandboxHooksDir)
	assert.ErrorContains(t, mustBuildClaudeEntrypointArtifacts(t, nil), "entrypoint executable is empty")
}

func mustBuildClaudeEntrypointArtifacts(t *testing.T, entrypoint []byte) error {
	t.Helper()
	_, err := buildClaudeEntrypointArtifacts(entrypoint, "", "", nil, nil, nil, security.SandboxHookConfig{}, false)
	return err
}

func TestInstallEntrypointFile(t *testing.T) {
	var uploaded []byte
	var commands []string
	upload := func(_ string, localPath, remotePath string) error {
		assert.Equal(t, remoteEntrypointPath, remotePath)
		content, err := os.ReadFile(localPath)
		require.NoError(t, err)
		uploaded = content
		return nil
	}
	exec := func(_ string, command string, timeout time.Duration) (string, string, int, error) {
		commands = append(commands, command)
		assert.Equal(t, 10*time.Second, timeout)
		return "", "", 0, nil
	}
	content := []byte("trusted entrypoint")
	require.NoError(t, installEntrypointFile("sb", remoteEntrypointPath, content, upload, exec))
	assert.Equal(t, content, uploaded)
	require.Equal(t, []string{"chmod 755 " + remoteEntrypointPath}, commands)

	uploadErr := errors.New("upload unavailable")
	err := installEntrypointFile("sb", remoteEntrypointPath, content, func(string, string, string) error { return uploadErr }, exec)
	assert.ErrorIs(t, err, uploadErr)
	err = installEntrypointFile("sb", remoteEntrypointPath, content, upload, func(string, string, time.Duration) (string, string, int, error) { return "", "denied", 1, nil })
	assert.ErrorContains(t, err, "exit 1: denied")
}

func TestConfigureEntrypointEvent(t *testing.T) {
	localEvent := "/tmp/event file.json"
	var uploadedRemote string
	var command string
	upload := func(_ string, localPath, remotePath string) error {
		assert.Equal(t, localEvent, localPath)
		uploadedRemote = remotePath
		return nil
	}
	exec := func(_ string, got string, timeout time.Duration) (string, string, int, error) {
		command = got
		assert.Equal(t, 10*time.Second, timeout)
		return "", "", 0, nil
	}
	require.NoError(t, configureEntrypointEvent("sb", localEvent, upload, exec))
	assert.Equal(t, "/sandbox/workspace/event.json", uploadedRemote)
	assert.True(t, strings.Contains(command, "FULLSEND_EVENT_FILE"))

	uploadErr := errors.New("event upload failed")
	assert.ErrorIs(t, configureEntrypointEvent("sb", localEvent, func(string, string, string) error { return uploadErr }, exec), uploadErr)
	assert.ErrorContains(t, configureEntrypointEvent("sb", localEvent, upload, func(string, string, time.Duration) (string, string, int, error) { return "", "bad env", 1, nil }), "exit 1: bad env")
}
