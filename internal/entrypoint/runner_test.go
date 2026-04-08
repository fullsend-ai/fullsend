package entrypoint

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeRunner_RecordsCalls(t *testing.T) {
	runner := &FakeRunner{}
	code, err := runner.Run(context.Background(), "claude", []string{"--agent", "code.md"}, "/workspace", []string{"PATH=/usr/bin"})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	require.Len(t, runner.Calls, 1)
	assert.Equal(t, "claude", runner.Calls[0].Name)
	assert.Equal(t, []string{"--agent", "code.md"}, runner.Calls[0].Args)
	assert.Equal(t, "/workspace", runner.Calls[0].Dir)
}

func TestFakeRunner_ConfiguredExitCode(t *testing.T) {
	runner := &FakeRunner{
		ExitCodes: map[string]int{"claude": 1},
	}
	code, err := runner.Run(context.Background(), "claude", nil, "", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, code)
}

func TestFakeRunner_ConfiguredError(t *testing.T) {
	runner := &FakeRunner{
		Errors: map[string]error{"claude": fmt.Errorf("binary not found")},
	}
	_, err := runner.Run(context.Background(), "claude", nil, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binary not found")
}

func TestFakeRunner_MultipleCalls(t *testing.T) {
	runner := &FakeRunner{}
	runner.Run(context.Background(), "claude", []string{"a"}, "/dir1", nil)
	runner.Run(context.Background(), "gitleaks", []string{"b"}, "/dir2", nil)
	require.Len(t, runner.Calls, 2)
	assert.Equal(t, "claude", runner.Calls[0].Name)
	assert.Equal(t, "gitleaks", runner.Calls[1].Name)
}

func TestSanitizeEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"FULLSEND_CODE_BOT_TOKEN=secret",
		"FULLSEND_OTHER=value",
		"GITHUB_TOKEN=ghp_xxx",
		"GH_TOKEN=ghp_yyy",
		"GITHUB_ENV=/tmp/env",
		"GITHUB_OUTPUT=/tmp/output",
		"GITHUB_PATH=/tmp/path",
		"GITHUB_STEP_SUMMARY=/tmp/summary",
		"GITHUB_STATE=/tmp/state",
		"ACTIONS_RUNTIME_TOKEN=gha_runtime",
		"ACTIONS_CACHE_URL=https://cache.example.com",
		"LANG=en_US.UTF-8",
	}
	result := SanitizeEnv(env)
	assert.Contains(t, result, "PATH=/usr/bin")
	assert.Contains(t, result, "HOME=/home/user")
	assert.Contains(t, result, "LANG=en_US.UTF-8")
	assert.NotContains(t, result, "FULLSEND_CODE_BOT_TOKEN=secret")
	assert.NotContains(t, result, "FULLSEND_OTHER=value")
	assert.NotContains(t, result, "GITHUB_TOKEN=ghp_xxx")
	assert.NotContains(t, result, "GH_TOKEN=ghp_yyy")
	assert.NotContains(t, result, "GITHUB_ENV=/tmp/env")
	assert.NotContains(t, result, "GITHUB_OUTPUT=/tmp/output")
	assert.NotContains(t, result, "GITHUB_PATH=/tmp/path")
	assert.NotContains(t, result, "GITHUB_STEP_SUMMARY=/tmp/summary")
	assert.NotContains(t, result, "GITHUB_STATE=/tmp/state")
	assert.NotContains(t, result, "ACTIONS_RUNTIME_TOKEN=gha_runtime")
	assert.NotContains(t, result, "ACTIONS_CACHE_URL=https://cache.example.com")
}

func TestSanitizeEnv_Empty(t *testing.T) {
	result := SanitizeEnv(nil)
	assert.Nil(t, result)
}

func TestExecRunner_CommandNotFound(t *testing.T) {
	runner := &ExecRunner{}
	_, err := runner.Run(context.Background(), "nonexistent-binary-xyz-12345", nil, "", nil)
	require.Error(t, err)
}
