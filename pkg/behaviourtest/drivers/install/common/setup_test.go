package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultGitHubSetupOpts_HasVendor(t *testing.T) {
	opts := DefaultGitHubSetupOpts()
	assert.True(t, opts.Vendor, "default should enable vendoring")
	assert.Empty(t, opts.FullsendRef, "default should not set a fullsend ref")
}

func TestRunGitHubSetupWithOpts_VendoredMode(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.Contains(t, joined, "--vendor")
	assert.NotContains(t, joined, "--fullsend-ref")
	assert.Contains(t, joined, "--mint-url")
	assert.Contains(t, joined, "--direct")
	assert.Contains(t, joined, "--runtime")
}

func TestRunGitHubSetupWithOpts_NonVendoredMode(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: false, FullsendRef: "main"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.NotContains(t, joined, "--vendor")
	assert.Contains(t, joined, "--fullsend-ref")
	assert.Contains(t, joined, "main")
}

func TestRunGitHubSetupWithOpts_WithGCPProject(t *testing.T) {
	var capturedCalls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedCalls = append(capturedCalls, args)
		if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
			return `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`, nil
		}
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: false, FullsendRef: "main"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "test-project", opts, runner, t.Logf)
	require.NoError(t, err)

	// Expect 3 calls: inference provision, inference status, github setup.
	require.Len(t, capturedCalls, 3)
	assert.Equal(t, "inference", capturedCalls[0][0])
	assert.Equal(t, "provision", capturedCalls[0][1])
	assert.Equal(t, "inference", capturedCalls[1][0])
	assert.Equal(t, "status", capturedCalls[1][1])
	assert.Equal(t, "github", capturedCalls[2][0])
	assert.Equal(t, "setup", capturedCalls[2][1])

	setupArgs := strings.Join(capturedCalls[2], " ")
	assert.Contains(t, setupArgs, "--inference-project")
	assert.Contains(t, setupArgs, "--fullsend-ref")
	assert.NotContains(t, setupArgs, "--vendor")
}

func TestRunGitHubSetup_DelegatesToWithOpts(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	err := RunGitHubSetup("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", runner, t.Logf)
	require.NoError(t, err)

	// RunGitHubSetup should use default opts (vendored).
	joined := strings.Join(capturedArgs, " ")
	assert.Contains(t, joined, "--vendor")
	assert.NotContains(t, joined, "--fullsend-ref")
}
